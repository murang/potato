package potato

import (
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
	if _app.GetCluster() == nil {
		return
	}
	_app.GetCluster().MemberList.BroadcastEvent(event, includeSelf)
}

func SetNetConfig(config *net.Config) {
	_app.NetManager = net.NewManagerWithConfig(config)
}

func SetRpcConfig(config *rpc.Config) {
	_app.RpcManager = rpc.NewManagerWithConfig(config)
}

func RegisterModule(mod app.IModule) {
	_app.RegisterModule(mod.Name(), mod)
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

func Run() {
	_app.Run()
}

func End(f func()) {
	_app.End(f)
}
