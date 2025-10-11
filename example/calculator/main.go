package main

import (
	"example/nicepb/nice"

	"github.com/asynkron/protoactor-go/cluster"
	"github.com/murang/potato"
	"github.com/murang/potato/rpc"
)

func main() {
	potato.SetRpcConfig(&rpc.Config{
		ClusterName: "nice",
		Consul:      "0.0.0.0:8500",
		ServiceKind: []*cluster.Kind{nice.NewCalculatorKind(func() nice.Calculator {
			return &CalculatorImpl{}
		}, 0)},
		EventHandler: OnEvent,
	})

	potato.Start(nil) // 初始化app 入参为启动函数 在初始化所有组件后执行
	potato.Run()      // 启动app 所有组件开始tick 主线程阻塞
	potato.End(nil)   // 主线程开始退出 所有组件销毁后执行入参函数
}
