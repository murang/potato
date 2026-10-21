package main

import (
	"example/nicepb/nice"
	"reflect"

	"github.com/murang/potato"
	"github.com/murang/potato/log"
	"google.golang.org/protobuf/proto"
)

// 用泛型定义函数 让入参可以是不同类型 方便路由不同消息
type HandlerFunc[T proto.Message] func(agent *Agent, msg T)

func WrapHandler[T proto.Message](handler HandlerFunc[T]) func(agent *Agent, msg proto.Message) {
	return func(agent *Agent, msg proto.Message) {
		req, ok := msg.(T)
		if !ok {
			return
		}
		handler(agent, req)
	}
}

// 消息分发
var msgDispatcher = map[reflect.Type]func(agent *Agent, msg proto.Message){
	reflect.TypeOf(&nice.C2S_Hello{}): WrapHandler(Hello),
	// ...
}

func Hello(agent *Agent, msg *nice.C2S_Hello) {
	// The global network dispatcher only routes. Waiting here would block every
	// session behind one slow RPC. The module owns the per-session request state.
	if err := potato.TrySendToModule[*NiceModule](helloRequest{session: agent.session, name: msg.Name}); err != nil {
		log.Sugar.Warnf("hello rejected: %v", err)
		agent.session.Close()
	}
}
