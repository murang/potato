package main

import (
	"github.com/murang/potato"
	"github.com/murang/potato/log"
	"github.com/murang/potato/net"
	"github.com/murang/potato/rpc"
)

func main() {
	// 添加模块 模块需要实现IModule 可以把组件理解为unity的组件 有自己的生命周期
	// potato可以注册多个模块 每个模块有自己的帧率 帧率设置为0的话就是不tick
	potato.RegisterModule(&NiceModule{})

	// 网络设置
	potato.SetNetConfig(&net.Config{
		SessionStartId: 0,
		ConnectLimit:   1000,
		Timeout:        30,
		Codec:          &net.PbCodec{},
		MsgHandler:     &MyMsgHandler{},
	})
	// 网络监听器 支持tcp/kcp/ws
	ln, err := net.NewListener("ws", ":10086")
	if err != nil {
		panic(err)
	}
	// 添加网络监听器 可支持同时接收多个监听器消息
	potato.GetNetManager().AddListener(ln)

	// rpc设置
	potato.SetRpcConfig(&rpc.Config{
		ClusterName:  "nice",
		Consul:       "0.0.0.0:8500",
		ServiceKind:  nil, // 当前节点没有service 就不用设置
		EventHandler: nil, // event stream事件处理器 如果没有订阅事件就不用设置
	})

	potato.Start(func() bool { // 初始化app 入参为启动函数 在初始化所有组件后执行
		log.Logger.Info("all module started, server start")
		return true
	})
	potato.Run() // 开始update 所有组件开始tick 主线程阻塞
	potato.End(func() { // 主线程开始退出 所有组件销毁后执行入参函数
		log.Logger.Info("all module stopped, server stop")
	})
}
