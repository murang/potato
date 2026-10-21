package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/murang/potato/log"
	"github.com/murang/potato/net"
	"github.com/murang/potato/rpc"
)

var errModuleNotRegistered = ErrModuleNotRegistered

type Application struct {
	exitCh                chan struct{}
	exitOnce              sync.Once
	endOnce               sync.Once
	shutdownDone          chan struct{}
	startDone             chan struct{}
	ticks                 sync.WaitGroup
	mu                    sync.RWMutex
	modules               map[string]*moduleRuntime
	order                 []*moduleRuntime
	started, running, ran bool
	modulesReady          bool
	err                   error
	unknownRejected       atomic.Uint64

	ActorSystem *actor.ActorSystem
	NetManager  *net.Manager
	RpcManager  *rpc.Manager
}

func NewApplication() *Application {
	return &Application{
		ActorSystem: actor.NewActorSystem(actor.WithLoggerFactory(log.ColoredConsoleLogging)),
		modules:     make(map[string]*moduleRuntime), exitCh: make(chan struct{}),
		shutdownDone: make(chan struct{}), startDone: make(chan struct{}),
	}
}

func (a *Application) GetActorSystem() *actor.ActorSystem { return a.ActorSystem }
func (a *Application) GetNetManager() *net.Manager        { return a.NetManager }
func (a *Application) GetRpcManager() *rpc.Manager        { return a.RpcManager }
func (a *Application) GetCluster() *cluster.Cluster {
	if a.RpcManager == nil {
		return nil
	}
	return a.RpcManager.GetCluster()
}
func (a *Application) BroadcastEvent(event any, includeSelf bool) {
	if c := a.GetCluster(); c != nil {
		c.MemberList.BroadcastEvent(event, includeSelf)
	}
}
func (a *Application) SetNetConfig(config *net.Config) {
	a.NetManager = net.NewManagerWithConfig(config)
}
func (a *Application) SetRpcConfig(config *rpc.Config) {
	a.RpcManager = rpc.NewManagerWithConfig(config)
}
func (a *Application) RegisterModule(name string, mod IModule) {
	a.RegisterModuleWithOptions(name, mod, ModuleOptions{})
}

// Dependencies register first. Startup and shutdown use this order and its
// reverse; callbacks must not synchronously request themselves or create cycles.
func (a *Application) RegisterModuleWithOptions(name string, mod IModule, options ModuleOptions) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started || a.exiting() {
		panic("RegisterModule after application start/exit")
	}
	if _, exists := a.modules[name]; exists {
		panic("RegisterModule err, repeated module name: " + name)
	}
	if options.QueueLimit <= 0 {
		options.QueueLimit = 1024
	}
	if options.MaxAsync <= 0 {
		options.MaxAsync = 32
	}
	r := &moduleRuntime{app: a, name: name, module: mod, options: options, done: make(chan struct{}), failed: make(chan struct{}), changed: make(chan struct{}, 1)}
	a.modules[name] = r
	a.order = append(a.order, r)
	log.Logger.Info("module register : " + name)
}
func (a *Application) module(name string) (*moduleRuntime, error) {
	a.mu.RLock()
	r := a.modules[name]
	a.mu.RUnlock()
	if r == nil {
		return nil, ErrModuleNotRegistered
	}
	return r, nil
}
func (a *Application) ModuleStats(name string) (ModuleStats, error) {
	r, err := a.module(name)
	if err != nil {
		return ModuleStats{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return ModuleStats{Pending: r.pending, Async: r.async, TickPending: r.tickPending, Rejected: r.rejected, Ready: r.ready && !r.stopping, Failure: r.failure}, nil
}

// TrySendToModule never waits for capacity. Credits include the executing message.
func (a *Application) TrySendToModule(name string, msg any) error {
	r, err := a.module(name)
	if err != nil {
		return err
	}
	if err = r.reserve(false); err != nil {
		return err
	}
	a.ActorSystem.Root.Send(r.pid, &moduleMessage{message: msg})
	return nil
}
func (a *Application) SendToModule(name string, msg interface{}) {
	if err := a.TrySendToModule(name, msg); err != nil {
		// Log the first rejection and powers of two, not every rejected packet.
		stats, statErr := a.ModuleStats(name)
		rejected := stats.Rejected
		if statErr != nil {
			rejected = a.unknownRejected.Add(1)
		}
		if rejected&(rejected-1) == 0 {
			log.Sugar.Warnf("module %s rejected message (%d rejected): %v", name, rejected, err)
		}
	}
}
func (a *Application) RequestToModule(name string, msg interface{}) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, err := a.RequestToModuleContext(ctx, name, msg)
	if errors.Is(err, context.DeadlineExceeded) {
		err = actor.ErrTimeout
	}
	return value, err
}

// Context cancels waiting and skips requests expired before execution. It cannot
// undo an OnRequest already executing; state-changing retries need idempotency.
func (a *Application) RequestToModuleContext(ctx context.Context, name string, msg any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := a.module(name)
	if err != nil {
		return nil, err
	}
	if err = r.reserve(false); err != nil {
		return nil, err
	}
	reply := make(chan moduleReply, 1)
	a.ActorSystem.Root.Send(r.pid, &moduleRequest{ctx: ctx, message: msg, reply: reply})
	select {
	case result := <-reply:
		return result.value, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.failed:
		r.mu.Lock()
		err = r.failure
		r.mu.Unlock()
		return nil, err
	case <-r.done:
		// A normal shutdown can finish after the reply was buffered but before
		// this goroutine resumes. Prefer that completed reply over ErrStopping.
		select {
		case result := <-reply:
			return result.value, result.err
		default:
		}
		r.mu.Lock()
		err = r.failure
		r.mu.Unlock()
		if err == nil {
			err = ErrStopping
		}
		return nil, err
	}
}

// TryAsyncToModule runs immutable-input work off the actor and delivers its result
// to OnMsg. Work must not touch module fields. Its reserved completion slot remains
// held until OnMsg returns, even when the ordinary queue is full. Register work
// before its parent message returns; unmanaged goroutines cannot be drained.
func (a *Application) TryAsyncToModule(name string, work func() any) error {
	a.mu.RLock()
	running := a.modulesReady
	a.mu.RUnlock()
	if !running {
		return ErrNotReady
	}
	r, err := a.module(name)
	if err != nil {
		return err
	}
	if err = r.reserve(true); err != nil {
		return err
	}
	go func() {
		returned := false
		defer func() {
			if reason := recover(); reason != nil {
				r.fail(reason)
			} else if !returned {
				// runtime.Goexit runs defers without returning a result or panic.
				r.fail("async work exited without returning")
			}
		}()
		result := work()
		returned = true
		r.mu.Lock()
		failed := r.failure != nil
		r.mu.Unlock()
		if failed {
			r.release(true)
			return
		}
		// Transfer the task credit to the completion without a transient zero.
		a.ActorSystem.Root.Send(r.pid, &moduleCompletion{message: result})
	}()
	return nil
}

func (a *Application) Start(f func() bool) {
	if err := a.StartWithError(f); err != nil {
		log.Sugar.Errorf("application start failed: %v", err)
	}
}
func (a *Application) StartWithError(f func() bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return a.StartContext(ctx, f)
}

// OnStart and its dependency chain must not use RPC or create unmanaged business
// producers. All PIDs exist before explicit, ordered business initialization.
func (a *Application) StartContext(ctx context.Context, f func() bool) (err error) {
	a.mu.Lock()
	if a.started || a.exiting() {
		a.mu.Unlock()
		return ErrStopping
	}
	a.started = true
	a.mu.Unlock()
	defer func() {
		if reason := recover(); reason != nil {
			err = fmt.Errorf("application startup panic: %v", reason)
		}
		if err != nil {
			a.fail(err)
		}
		close(a.startDone)
	}()
	a.watchSignals()
	if err = ctx.Err(); err != nil {
		return err
	}
	if f != nil && !f() {
		return errors.New("application resource initialization failed")
	}
	for _, r := range a.order {
		// Business hooks recover at the module boundary. The guardian is a final
		// stop-only safeguard: even an unexpected framework panic must never
		// reconstruct a wrapper around the same, potentially corrupted IModule.
		strategy := actor.NewOneForOneStrategy(0, 0, func(reason any) actor.Directive {
			r.fail(reason)
			return actor.StopDirective
		})
		props := actor.PropsFromProducer(func() actor.Actor { return &moduleActor{runtime: r} }, actor.WithGuardian(strategy))
		r.pid = a.ActorSystem.Root.Spawn(props)
	}
	for _, r := range a.order {
		if err = ctx.Err(); err != nil {
			return err
		}
		if a.exiting() {
			return ErrStopping
		}
		done := make(chan error, 1)
		a.ActorSystem.Root.Send(r.pid, &moduleInit{done: done})
		select {
		case err = <-done:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-a.exitCh:
			if err = a.Err(); err != nil {
				return err
			}
			return ErrStopping
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if a.exiting() {
		return ErrStopping
	}
	// Cluster startup can expose inbound handlers before StartWithError returns.
	// Those handlers may submit work because every module is initialized now;
	// OnStart itself still cannot submit async work or depend on RPC.
	a.mu.Lock()
	a.modulesReady = true
	a.mu.Unlock()
	if a.RpcManager != nil {
		if _, err = a.RpcManager.StartWithError(a.ActorSystem); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if a.exiting() {
		return ErrStopping
	}
	a.mu.Lock()
	a.running = true
	a.mu.Unlock()
	if a.NetManager != nil {
		a.NetManager.Start()
	}
	return nil
}

func (a *Application) Run() {
	a.mu.Lock()
	if !a.running || a.exiting() {
		a.mu.Unlock()
		return
	}
	if !a.ran {
		a.ran = true
		for _, r := range a.order {
			if r.interval <= 0 {
				continue
			}
			a.ticks.Add(1)
			go func() {
				defer a.ticks.Done()
				ticker := time.NewTicker(r.interval)
				defer ticker.Stop()
				for {
					select {
					case <-a.exitCh:
						return
					case <-ticker.C:
						r.tick()
					}
				}
			}()
		}
	}
	a.mu.Unlock()
	<-a.exitCh
}

// End runs on the lifecycle goroutine, never in a module/network callback. Accepted
// work can still send toward earlier registered dependencies during reverse drain.
func (a *Application) End(f func()) {
	a.endOnce.Do(func() {
		a.Exit()
		a.mu.RLock()
		started := a.started
		a.mu.RUnlock()
		if started {
			<-a.startDone
		}
		// Run registers ticker goroutines under mu and checks exitCh before Add.
		a.mu.Lock()
		a.mu.Unlock()
		a.ticks.Wait()
		if a.NetManager != nil {
			a.NetManager.OnDestroy()
		}
		if a.RpcManager != nil {
			a.RpcManager.Quiesce()
		}
		for i := len(a.order) - 1; i >= 0; i-- {
			r := a.order[i]
			r.drain()
			if r.pid != nil {
				a.ActorSystem.Root.Poison(r.pid)
				<-r.done
			}
		}
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
func (a *Application) Err() error { a.mu.RLock(); defer a.mu.RUnlock(); return a.err }
func (a *Application) fail(err error) {
	a.mu.Lock()
	if a.err == nil {
		a.err = err
	}
	a.mu.Unlock()
	log.Sugar.Errorf("application failed: %v", err)
	a.Exit()
}
func (a *Application) Exit() { a.exitOnce.Do(func() { close(a.exitCh) }) }
func (a *Application) exiting() bool {
	select {
	case <-a.exitCh:
		return true
	default:
		return false
	}
}
func (a *Application) watchSignals() {
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
			for _, r := range a.order {
				stats, _ := a.ModuleStats(r.name)
				log.Sugar.Errorf("shutdown pending module %s: %+v", r.name, stats)
			}
			var buf [65536]byte
			n := runtime.Stack(buf[:], true)
			log.Sugar.Errorf("server not stopped in 1 minute, all stack is:\n%s", string(buf[:n]))
			_ = log.Logger.Sync()
			os.Exit(1)
		}
	}()
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
