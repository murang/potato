package potato

import (
	"context"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/murang/potato/app"
	"github.com/murang/potato/net"
	"github.com/murang/potato/rpc"
)

var (
	_app *app.Application
)

func init() {
	_app = app.NewApplication()
}

func GetActorSystem() *actor.ActorSystem {
	return _app.ActorSystem
}
func GetCluster() *cluster.Cluster {
	return _app.GetCluster()
}
func GetNetManager() *net.Manager {
	return _app.NetManager
}
func GetRpcManager() *rpc.Manager {
	return _app.RpcManager
}

func BroadcastEvent(event any, includeSelf bool) {
	_app.BroadcastEvent(event, includeSelf)
}

func SetNetConfig(config *net.Config) {
	_app.SetNetConfig(config)
}

func SetRpcConfig(config *rpc.Config) {
	_app.SetRpcConfig(config)
}

func RegisterModule(mod app.IModule) {
	_app.RegisterModule(mod.Name(), mod)
}

func RegisterModuleWithOptions(mod app.IModule, options app.ModuleOptions) {
	_app.RegisterModuleWithOptions(mod.Name(), mod, options)
}

func TrySendToModule[T app.IModule](msg any) error {
	var mod T
	return _app.TrySendToModule(mod.Name(), msg)
}

func RequestToModuleContext[T app.IModule](ctx context.Context, msg any) (any, error) {
	var mod T
	return _app.RequestToModuleContext(ctx, mod.Name(), msg)
}

func TryAsyncToModule[T app.IModule](work func() any) error {
	var mod T
	return _app.TryAsyncToModule(mod.Name(), work)
}

func ModuleStats[T app.IModule]() (app.ModuleStats, error) {
	var mod T
	return _app.ModuleStats(mod.Name())
}

func SendToModule[T app.IModule](msg interface{}) {
	var mod T
	modName := mod.Name()
	_app.SendToModule(modName, msg)
}

func RequestToModule[T app.IModule](msg interface{}) (interface{}, error) {
	var mod T
	modName := mod.Name()
	return _app.RequestToModule(modName, msg)
}

func Start(f func() bool) {
	_app.Start(f)
}

func StartWithError(f func() bool) error                    { return _app.StartWithError(f) }
func StartContext(ctx context.Context, f func() bool) error { return _app.StartContext(ctx, f) }
func Exit()                                                 { _app.Exit() }
func Err() error                                            { return _app.Err() }

func Run() {
	_app.Run()
}

func End(f func()) {
	_app.End(f)
}
