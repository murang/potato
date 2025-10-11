package main

import (
	"example/nicepb/nice"
	"fmt"

	"github.com/murang/potato"
	"github.com/murang/potato/log"
)

type NiceModule struct {
}

func (n *NiceModule) Name() string {
	return "nice"
}

func (n *NiceModule) FPS() uint {
	return 1
}

func (n *NiceModule) OnStart() {
	log.Sugar.Info("start")
}

func (n *NiceModule) OnUpdate() {
	potato.BroadcastEvent(&nice.EventHello{SayHello: "niceman"}, false)
}

func (n *NiceModule) OnDestroy() {
	log.Sugar.Info("destroy")
}

func (n *NiceModule) OnMsg(msg interface{}) {
	log.Sugar.Infof("msg: %v", msg)
}

func (n *NiceModule) OnRequest(msg interface{}) interface{} {
	log.Sugar.Infof("request: %v", msg)
	// 自定义id用于在service方生成虚拟actor 在service节点没有变动的情况下 同一个identity会始终路由到同一个service
	grain := nice.GetCalculatorGrainClient(potato.GetCluster(), "NiceIdentity")
	sum, err := grain.Sum(&nice.Input{A: 6, B: 6})
	if err != nil {
		log.Sugar.Errorf("sum error: %v", err)
		return "sum error: " + err.Error()
	}
	return fmt.Sprintf("Nice ~  %s cal sum : %d", msg, sum.Result)
}
