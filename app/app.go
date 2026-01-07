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
	exit     bool
	name2mod map[string]IModule // ModuleID -> IModule
	name2pid sync.Map           // ModuleID -> actor PID
	cancels  []scheduler.CancelFunc

	ActorSystem *actor.ActorSystem
	Cluster     *cluster.Cluster
	NetManager  *net.Manager
	RpcManager  *rpc.Manager
}

func NewApplication() *Application {
	a := &Application{
		ActorSystem: actor.NewActorSystem(actor.WithLoggerFactory(log.ColoredConsoleLogging)),
		name2mod:    map[string]IModule{},
		name2pid:    sync.Map{},
	}
	return a
}

func (a *Application) GetActorSystem() *actor.ActorSystem {
	return a.ActorSystem
}
func (a *Application) GetCluster() *cluster.Cluster {
	return a.Cluster
}
func (a *Application) GetNetManager() *net.Manager {
	return a.NetManager
}
func (a *Application) GetRpcManager() *rpc.Manager {
	return a.RpcManager
}

func (a *Application) BroadcastEvent(event any, includeSelf bool) {
	if a.Cluster == nil {
		return
	}
	a.Cluster.MemberList.BroadcastEvent(event, includeSelf)
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
	// catch signal
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGILL, syscall.SIGTRAP, syscall.SIGABRT)
		log.Sugar.Infof("caught signal: %v", <-c)
		a.Exit()
		time.Sleep(1 * time.Minute)
		var buf [65536]byte
		n := runtime.Stack(buf[:], true)
		log.Sugar.Errorf("server not stopped in 1 minute, all stack is:\n%s", string(buf[:n]))
		time.Sleep(2 * time.Second)
		os.Exit(1)
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
		a.Cluster = a.RpcManager.Start(a.ActorSystem)
	}
	// 网络
	if a.NetManager != nil {
		a.NetManager.Start()
	}

	for mid, mod := range a.name2mod {
		props := actor.PropsFromProducer(func() actor.Actor {
			return &moduleActor{module: mod.(IModule)}
		})
		pid := a.ActorSystem.Root.Spawn(props)
		a.name2pid.Store(mid, pid)
		log.Logger.Info("module init : " + mid)
	}
}

func (a *Application) Run() {
	sch := scheduler.NewTimerScheduler(a.ActorSystem.Root)
	for mid, mod := range a.name2mod {
		if mod.FPS() > 0 {
			interval := time.Duration(1000/mod.FPS()) * time.Millisecond
			pid, _ := a.name2pid.Load(mid)
			a.cancels = append(a.cancels, sch.SendRepeatedly(interval, interval, pid.(*actor.PID), &ModuleUpdate{}))
		}
	}
	for {
		if a.exit {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (a *Application) End(f func()) {
	// 网络
	if a.NetManager != nil {
		a.NetManager.OnDestroy()
	}
	// rpc
	if a.RpcManager != nil {
		a.RpcManager.OnDestroy()
	}

	for _, cancel := range a.cancels {
		cancel()
	}
	a.name2pid.Range(func(key, value interface{}) bool {
		a.ActorSystem.Root.Stop(value.(*actor.PID))
		return true
	})
	if f != nil {
		f()
	}
	_ = log.Logger.Sync()
	time.Sleep(1 * time.Second)
}

func (a *Application) Exit() {
	a.exit = true
}
