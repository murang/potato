package rpc

import (
	"fmt"
	"sync"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/cluster/clusterproviders/consul"
	"github.com/asynkron/protoactor-go/cluster/identitylookup/disthash"
	"github.com/asynkron/protoactor-go/eventstream"
	"github.com/asynkron/protoactor-go/remote"
	"github.com/hashicorp/consul/api"
	"github.com/murang/potato/log"
	"github.com/murang/potato/util"
)

type Config struct {
	ClusterName  string                 // 集群名称
	Consul       string                 // 服务发现注册地址
	ServiceKind  []*cluster.Kind        // 使用proto actor grain生成的服务类型
	EventHandler func(any)              // event处理
	Options      []cluster.ConfigOption // 集群设置
}

type Manager struct {
	lifecycleMu  sync.Mutex
	mu           sync.Mutex
	stopping     bool
	active       sync.WaitGroup
	startOnce    sync.Once
	stopOnce     sync.Once
	startErr     error
	started      bool
	ownedActors  []string // 锁定 Protoactor 版本创建的系统 actor；启动前确认这些名称未占用
	cluster      *cluster.Cluster
	clusterName  string                    // 集群名称
	consul       string                    // 服务发现注册地址
	serviceKinds []*cluster.Kind           // 使用proto actor grain生成的服务类型
	eventHandler func(any)                 // event stream处理
	eventSub     *eventstream.Subscription // event stream订阅
	options      []cluster.ConfigOption
}

func NewManagerWithConfig(config *Config) *Manager {
	if config == nil {
		config = &Config{}
	}
	return &Manager{
		clusterName:  config.ClusterName,
		consul:       config.Consul,
		serviceKinds: append([]*cluster.Kind(nil), config.ServiceKind...),
		eventHandler: config.EventHandler,
		options:      append([]cluster.ConfigOption(nil), config.Options...),
	}
}

func (m *Manager) GetCluster() *cluster.Cluster {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cluster
}

// Start 保留原签名；应用生命周期使用 StartWithError，避免 RPC 初始化失败后仍开放网络。
func (m *Manager) Start(actorSystem *actor.ActorSystem) *cluster.Cluster {
	cls, err := m.StartWithError(actorSystem)
	if err != nil {
		log.Sugar.Errorf("RPC start failed: %v", err)
	}
	return cls
}

// StartWithError 与同一 ActorSystem 的生命周期管理串行调用。每个 ActorSystem
// 只由一个 RPC Manager 管理；从开始启动直到 OnDestroy 返回，调用方不得自行
// 创建、停止后重建下面列出的 RPC 系统 actor，以免破坏依赖内部 PID 的所有权。
func (m *Manager) StartWithError(actorSystem *actor.ActorSystem) (*cluster.Cluster, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.startOnce.Do(func() {
		m.mu.Lock()
		stopping := m.stopping
		m.mu.Unlock()
		if stopping {
			m.startErr = fmt.Errorf("RPC manager is stopping")
			return
		}
		m.startErr = m.start(actorSystem)
	})
	if m.startErr != nil {
		return nil, m.startErr
	}
	return m.GetCluster(), nil
}

func (m *Manager) start(actorSystem *actor.ActorSystem) (err error) {
	if actorSystem == nil {
		return fmt.Errorf("RPC actor system is nil")
	}
	// Protoactor 的 StartMember 用 panic 报告绑定/启动错误。只在生命周期边界
	// 转成错误，不把业务 panic 吞掉后继续运行；失败应用仍应执行 End 清理。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("RPC startup panic: %v", r)
		}
	}()
	provider, err := consul.NewWithConfig(&api.Config{
		Address: m.consul,
	})
	if err != nil {
		return fmt.Errorf("create Consul provider: %w", err)
	}
	lookup := disthash.New()
	lanIp, err := util.GetLocalEthernetIP()
	if err != nil {
		return fmt.Errorf("RPC local address: %w", err)
	}
	availablePort, err := util.GetAvailablePort(40000, 50000)
	if err != nil {
		return fmt.Errorf("RPC port: %w", err)
	}
	config := remote.Configure(lanIp, availablePort, remote.WithAdvertisedHost(fmt.Sprintf("%s:%d", lanIp, availablePort)))
	clusterConfig := cluster.Configure(m.clusterName, provider, lookup, config, m.options...)
	if err := m.configureKinds(clusterConfig); err != nil {
		return err
	}
	// 主模块锁定的 3c0e61ca19c9 和 example 的 d5226bee08eb 均使用下列名称和
	// 生命周期顺序。Remote.Start 在绑定 socket 之后才创建固定名称的 actor，重名
	// panic 时 socket 仍是依赖的局部变量、没有公开回收句柄。提前拒绝名称
	// 冲突，也明确本 Manager 的清理所有权，不能误停调用方已有的 actor。
	names := []string{"activator", "EndpointSupervisor", cluster.DefaultGossipActorName, cluster.PubSubDeliveryName}
	if _, ok := clusterConfig.IdentityLookup.(*disthash.IdentityLookup); ok {
		names = append(names, disthash.PartitionActivatorActorName)
	}
	if _, ok := clusterConfig.ClusterProvider.(*consul.Provider); ok {
		names = append(names, "consul-provider")
	}
	for _, name := range names {
		if _, exists := actorSystem.ProcessRegistry.GetLocal(name); exists {
			return fmt.Errorf("RPC system actor name already in use: %q", name)
		}
	}
	m.ownedActors = names
	cls := cluster.New(actorSystem, clusterConfig)
	m.mu.Lock()
	m.cluster = cls
	m.mu.Unlock()

	cls.StartMember()
	m.started = true

	// 订阅通知
	if m.eventHandler != nil {
		m.eventSub = cls.ActorSystem.EventStream.Subscribe(func(event any) {
			if !m.begin() {
				return
			}
			defer m.active.Done()
			m.eventHandler(event)
		})
	}

	return nil
}

func (m *Manager) configureKinds(config *cluster.Config) error {
	// Options 也可以配置 Kind，统一在最终配置上门控。复制 Props，避免修改调用方
	// 复用的 Kind。Options 可以直接提供共享 map，因此先复制 map，再合并
	// ServiceKind，不能先 WithKinds 写入共享 map、最后才复制。
	kinds := make(map[string]*cluster.Kind, len(config.Kinds)+len(m.serviceKinds))
	for name, kind := range config.Kinds {
		kinds[name] = kind
	}
	for _, kind := range m.serviceKinds {
		if kind == nil {
			return fmt.Errorf("invalid nil RPC service kind")
		}
		kinds[kind.Kind] = kind
	}
	for name, kind := range kinds {
		if kind == nil || kind.Props == nil {
			return fmt.Errorf("invalid RPC kind %q", name)
		}
		copyKind := *kind
		copyKind.Props = kind.Props.Clone(actor.WithReceiverMiddleware(m.receiveGate))
		kinds[name] = &copyKind
	}
	config.Kinds = kinds
	return nil
}

func (m *Manager) begin() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return false
	}
	// Add 与关门共用锁；Quiesce 关门后才 Wait，杜绝 Wait 与新 Add 的竞态。
	m.active.Add(1)
	return true
}

func (m *Manager) receiveGate(next actor.ReceiverFunc) actor.ReceiverFunc {
	return func(ctx actor.ReceiverContext, envelope *actor.MessageEnvelope) {
		if _, ok := envelope.Message.(*cluster.GrainRequest); !ok {
			next(ctx, envelope)
			return
		}
		if !m.begin() {
			if envelope.Sender != nil {
				ctx.ActorSystem().Root.Send(envelope.Sender, cluster.NewGrainErrorResponse(cluster.ErrorReason_UNAVAILABLE, "RPC manager is stopping"))
			}
			return
		}
		defer m.active.Done()
		next(ctx, envelope)
	}
}

// Quiesce 从生命周期协程调用：停止新的入站 grain/event 业务并等待已开始的
// 回调；保留出站 transport。不能在这些回调内同步等待自身退出。
// 路由回本节点的 grain 也会被拒绝；自定义 Kind 的非 GrainRequest 消息及
// Init/Terminate 仍需遵守应用生命周期，不得向已停止模块派生业务。
func (m *Manager) Quiesce() {
	m.lifecycleMu.Lock()
	m.mu.Lock()
	m.stopping = true
	m.mu.Unlock()
	if m.eventSub != nil {
		m.cluster.ActorSystem.EventStream.Unsubscribe(m.eventSub)
		m.eventSub = nil
	}
	m.lifecycleMu.Unlock()
	m.active.Wait()
}

func (m *Manager) OnDestroy() {
	m.Quiesce()
	m.stopOnce.Do(func() {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		cls := m.GetCluster()
		if cls == nil {
			return
		}
		if m.started {
			cls.Shutdown(true)
			return
		}
		m.cleanupPartial(cls)
	})
}

// 部分启动不能直接调用 Cluster.Shutdown：它会先使用尚未初始化的 gossip。
// 每个已创建组件独立尝试清理，某个组件的 nil 状态或清理错误不能跳过后续项；
// 原始 startErr 保持不变。ActorSystem.Shutdown 只关闭标记，不能代替这些停止动作。
func (m *Manager) cleanupPartial(cls *cluster.Cluster) {
	if cls.Config.ClusterProvider != nil {
		cleanupRPC("provider", func() error { return cls.Config.ClusterProvider.Shutdown(false) })
	}
	if cls.Gossip != nil {
		cleanupRPC("gossip", func() error { cls.Gossip.Shutdown(); return nil })
	}
	if cls.IdentityLookup != nil {
		cleanupRPC("identity lookup", func() error { cls.IdentityLookup.Shutdown(); return nil })
	}
	if cls.Remote != nil {
		// graceful 路径会关闭 endpoint actors 和连接；false 只停止 gRPC server。
		// 若 Remote 自身只初始化了一半，逐项保护后再尝试 gRPC 的直接停止。
		if !cleanupRPC("remote", func() error { cls.Remote.Shutdown(true); return nil }) {
			cleanupRPC("remote transport", func() error { cls.Remote.Shutdown(false); return nil })
		}
	}
	// PubSub 没有公开 Shutdown；这里也回收上一步部分初始化后残留的系统 actor。
	// 这些固定名称在启动前已确认空闲，并受 StartWithError 的生命周期约束保护；
	// 不扫描或停止其他模块及用户 actor。
	for _, name := range m.ownedActors {
		if _, exists := cls.ActorSystem.ProcessRegistry.GetLocal(name); exists {
			pid := actor.NewPID(cls.ActorSystem.Address(), name)
			cleanupRPC("actor "+name, func() error { return cls.ActorSystem.Root.StopFuture(pid).Wait() })
		}
	}
}

func cleanupRPC(stage string, stop func() error) (ok bool) {
	defer func() {
		if reason := recover(); reason != nil {
			log.Sugar.Errorf("RPC partial startup cleanup (%s): %v", stage, reason)
			ok = false
		}
	}()
	if err := stop(); err != nil {
		log.Sugar.Errorf("RPC partial startup cleanup (%s): %v", stage, err)
		return false
	}
	return true
}
