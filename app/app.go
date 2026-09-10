package app

import (
	"errors"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/scheduler"
	"github.com/murang/potato/log"
	"github.com/murang/potato/net"
	"github.com/murang/potato/rpc"
)

var (
	errModuleNotRegistered = errors.New("module has not been registered")
)

type Application struct {
	exitCh       chan struct{}
	exitOnce     sync.Once
	endOnce      sync.Once
	shutdownDone chan struct{}
	moduleDone   sync.Map
	cancelsMu    sync.Mutex
	name2mod     map[string]IModule // ModuleID -> IModule
	name2pid     sync.Map           // ModuleID -> actor PID
	cancels      []scheduler.CancelFunc

	ActorSystem *actor.ActorSystem
	NetManager  *net.Manager
	RpcManager  *rpc.Manager
}

func NewApplication() *Application {
	a := &Application{
		ActorSystem:  actor.NewActorSystem(actor.WithLoggerFactory(log.ColoredConsoleLogging)),
		name2mod:     map[string]IModule{},
		name2pid:     sync.Map{},
		exitCh:       make(chan struct{}),
		shutdownDone: make(chan struct{}),
	}
	return a
}

func (a *Application) GetActorSystem() *actor.ActorSystem {
	return a.ActorSystem
}
func (a *Application) GetCluster() *cluster.Cluster {
	if a.RpcManager == nil {
		return nil
	}
	return a.RpcManager.GetCluster()
}
func (a *Application) GetNetManager() *net.Manager {
	return a.NetManager
}
func (a *Application) GetRpcManager() *rpc.Manager {
	return a.RpcManager
}

func (a *Application) BroadcastEvent(event any, includeSelf bool) {
	if cluster := a.GetCluster(); cluster != nil {
		cluster.MemberList.BroadcastEvent(event, includeSelf)
	}
}

func (a *Application) SetNetConfig(config *net.Config) {
	a.NetManager = net.NewManagerWithConfig(config)
}

func (a *Application) SetRpcConfig(config *rpc.Config) {
	a.RpcManager = rpc.NewManagerWithConfig(config)
}

func (a *Application) RegisterModule(modName string, mod IModule) {
	if _, ok := a.name2mod[modName]; ok {
		panic("RegisterModule err, repeated module name: " + modName)
	}
	a.name2mod[modName] = mod
	log.Logger.Info("module register : " + modName)
}

func (a *Application) SendToModule(modName string, msg interface{}) {
	if pid, ok := a.name2pid.Load(modName); ok {
		a.ActorSystem.Root.Send(pid.(*actor.PID), &ModuleOnMsg{Msg: msg})
	} else {
		log.Sugar.Warnf("module %s has not been registered", modName)
	}
}

func (a *Application) RequestToModule(modName string, msg interface{}) (interface{}, error) {
	if pid, ok := a.name2pid.Load(modName); ok {
		return a.ActorSystem.Root.RequestFuture(pid.(*actor.PID), &ModuleOnRequest{Request: msg}, time.Second).Result()
	} else {
		log.Sugar.Warnf("module %s has not been registered", modName)
		return nil, errModuleNotRegistered
	}
}

func (a *Application) Start(f func() bool) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGILL, syscall.SIGTRAP, syscall.SIGABRT)
	go func() {
		defer signal.Stop(c)
		select {
		case sig := <-c:
			log.Sugar.Infof("caught signal: %v", sig)
			a.Exit()
		case <-a.exitCh:
		case <-a.shutdownDone:
			return
		}
		if !a.waitForShutdown(time.Minute) {
			var buf [65536]byte
			n := runtime.Stack(buf[:], true)
			log.Sugar.Errorf("server not stopped in 1 minute, all stack is:\n%s", string(buf[:n]))
			_ = log.Logger.Sync()
			os.Exit(1)
		}
	}()

	// 先执行初始化逻辑 再执行集群和网络 否则可能出现网络或者rpc消息过来 但是数据库等等没准备好的情况
	if f != nil {
		ret := f()
		if !ret {
			_ = log.Logger.Sync()
			os.Exit(1)
		}
	}
	// rpc StartMember 需要先执行 否则net中获取grain会出错
	if a.RpcManager != nil {
		a.RpcManager.Start(a.ActorSystem)
	}
	// 网络
	if a.NetManager != nil {
		a.NetManager.Start()
	}

	for mid, mod := range a.name2mod {
		done := make(chan struct{})
		a.moduleDone.Store(mid, done)
		props := actor.PropsFromProducer(func() actor.Actor {
			return &moduleActor{module: mod, stopped: done}
		})
		pid := a.ActorSystem.Root.Spawn(props)
		a.name2pid.Store(mid, pid)
		log.Logger.Info("module init : " + mid)
	}
}

func (a *Application) Run() {
	a.cancelsMu.Lock()
	select {
	case <-a.exitCh:
		a.cancelsMu.Unlock()
		return
	default:
	}
	sch := scheduler.NewTimerScheduler(a.ActorSystem.Root)
	for mid, mod := range a.name2mod {
		if mod.FPS() > 0 {
			interval := time.Second / time.Duration(mod.FPS())
			pid, _ := a.name2pid.Load(mid)
			a.cancels = append(a.cancels, sch.SendRepeatedly(interval, interval, pid.(*actor.PID), &ModuleUpdate{}))
		}
	}
	a.cancelsMu.Unlock()
	<-a.exitCh
}

// End 从应用生命周期协程调用，等待网络回调和模块清理后再执行完成回调。
func (a *Application) End(f func()) {
	a.endOnce.Do(func() {
		a.Exit()
		a.cancelsMu.Lock()
		for _, cancel := range a.cancels {
			cancel()
		}
		a.cancels = nil
		a.cancelsMu.Unlock()

		if a.NetManager != nil {
			a.NetManager.OnDestroy()
		}
		// 停止新的网络事件后，按 mailbox 顺序完成已接受的模块消息，再清理模块。
		a.name2pid.Range(func(_, value interface{}) bool {
			a.ActorSystem.Root.Poison(value.(*actor.PID))
			return true
		})
		a.moduleDone.Range(func(_, value interface{}) bool {
			<-value.(chan struct{})
			return true
		})
		// 模块 OnDestroy 期间仍可使用 RPC。
		if a.RpcManager != nil {
			a.RpcManager.OnDestroy()
		}
		if !a.ActorSystem.IsStopped() {
			a.ActorSystem.Shutdown()
		}
		if f != nil {
			f()
		}
		_ = log.Logger.Sync()
		close(a.shutdownDone)
	})
}

func (a *Application) Exit() {
	a.exitOnce.Do(func() {
		close(a.exitCh)
	})
}

func (a *Application) waitForShutdown(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-a.shutdownDone:
		return true
	case <-timer.C:
		return false
	}
}
