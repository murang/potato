## 来创建一个土豆服务器吧 ～ ヽ(⌐■_■)ノ♪

---

potato是一个轻量级的go语言游戏网络框架。致力于用最简单的方式，让开发者快速搭建游戏的网络服务，同时提供更多的扩展能力，让开发者能够更好的满足自己的需求。

框架特性：  
1. 网络模块支持tcp.kcp.ws协议, 并且支持多个监听器同时接收消息
2. 消息编解码支持protobuf和json， pb消息生成插件支持自动注册到消息列表
3. 进程内各模块运行在各自的goroutine中运行，在保证多核利用效率的情况下，业务代码可以不用考虑并发问题
4. 通过consul的服务发现，用极简单的配置，实现集群中服务之间的远程调用
5. 框架很轻量，代码量不多，对于学习go语言的新手来说，通过阅读源码可以学习到网络消息处理，rpc服务等相关知识

---

框架主要组成：
* application: 整个服务的主体，作为单例存在，它的生命周期也是整个进程的生命周期
* netManager: application自带网络管理器。如果服务不需要网络监听，可以不设置。通过在管理器中添加监听器和实现了消息处理接口的实例来实现网络消息的处理
* rpcManager: application自带rpc管理器。如果服务不需要rpc服务，可以不设置。通过设置consul的服务发现以及当前节点的rpc服务配置，实现集群中的rpc功能
* module: 如果你玩过unity之类的游戏引擎，你能很容易理解模块的生命周期，模块可以设置自己的循环帧率，便于处理不同帧率的需求
* config: 配置读取。支持本地配置和consul的kv。支持AB测试中不同配置数据的读取。consul为数据源的时候支持配置动态更新和添加。
---

使用方法：
```bash
go get github.com/murang/potato
```

---
 
引用potato会初始化一个Application单例 把实现了IModule接口的模块注册到potato 执行Run之后模块的生命周期就开始了
```go
package main

import (
	"github.com/murang/potato"
)

func main() {
  // 添加模块 模块需要实现IModule 可以把组件理解为unity的组件 有自己的生命周期
  // potato可以注册多个模块 每个模块有自己的帧率 帧率设置为0的话就是不tick
  potato.RegisterModule(&NiceModule{})

  // 初始化app 入参为启动函数 在初始化所有组件后执行
  potato.Start(func() bool { // 初始化app 入参为启动函数 在初始化所有组件后执行
    log.Logger.Info("all module started, server start")
    return true
  })
  potato.Run()        // 开始update 所有组件开始tick 主线程阻塞
  potato.End(func() { // 主线程开始退出 所有组件销毁后执行入参函数
    log.Logger.Info("all module stopped, server stop")
  })
}
```
模块需要实现IModule接口
```go
type NiceModule struct {}

// 模块名称
func (n *NiceModule) Name() string {
    return "nice"
}

func (n *NiceModule) FPS() uint {
	return 60 // 帧率 设置60的话 模块每秒OnUpdate会执行60次 设置为0的话就是不tick
}
// 模块初始化
func (n *NiceModule) OnStart() {
	log.Sugar.Info("start")
}
// 根据模块设置的帧率执行
func (n *NiceModule) OnUpdate() {
}
// 模块销毁
func (n *NiceModule) OnDestroy() {
	log.Sugar.Info("destroy")
}
// 有消息到达
func (n *NiceModule) OnMsg(msg interface{}) {
	log.Sugar.Infof("msg: %v", msg)
}
// 需要返回结果的消息
func (n *NiceModule) OnRequest(msg interface{}) interface{} {
	log.Sugar.Infof("request: %v", msg)
	return fmt.Sprintf("Nice ~  %s ", msg)
}
```

---

设置网络监听：
```go
potato.SetNetConfig(&net.Config{
		SessionStartId: 0,
		ConnectLimit:   1000,
		Timeout:        30,
		Codec:          &net.PbCodec{}, // 框架内置JsonCodec和PbCodec 可以实现ICodec接口来实现自定义消息编解码
		MsgHandler:     &MyMsgHandler{}, // 需要用户自己实现IMsgHandler 用于处理消息
	})
// 网络监听器 支持tcp/kcp/ws
ln, _ := net.NewListener("tcp", ":10086")
// 添加网络监听器 可支持同时接收多个监听器消息 统一由MsgHandler处理
potato.GetNetManager().AddListener(ln)
```

⚠️⚠️⚠️ 网络消息按照 `[消息体长度(4字节)] + [消息体]` 为一个数据包来发送 这个4字节的长度默认`大端序` ⚠️⚠️⚠️

消息处理器实现IMsgHandler
```go
// 消息是否在协程中处理 如果设置为true 消息不会经过NetManager的消息channel依次处理 
// 而是运行在goroutine中的每个session有事件后在session所在goroutine立即处理 需要注意并发安全
func (m *MyMsgHandler) IsMsgInRoutine() bool {
    return false
}
// 新的session生成
func (m *MyMsgHandler) OnSessionOpen(session *net.Session) {
	log.Sugar.Info("handler got open:", session.ID())
}
// session关闭
func (m *MyMsgHandler) OnSessionClose(session *net.Session) {
	log.Sugar.Info("handler got close:", session.ID())
}
// 收到消息
func (m *MyMsgHandler) OnMsg(session *net.Session, msg any) {
	log.Sugar.Infof("handler got msg: %v", msg)
}
```

---

设置服务器集群需要有consul提供服务发现 具体安装方法等参考[consul](https://github.com/hashicorp/consul) 本地测试推荐docker安装

rpc使用到了proto actor的grain生成 编译pb的时候需要安装插件 详情参考[protoc-gen-go-grain](https://github.com/asynkron/protoactor-go/tree/dev/protobuf/protoc-gen-go-grain)和示例
```shell
# 安装插件
go install github.com/asynkron/protoactor-go/protobuf/protoc-gen-go-grain@latest
# 添加环境变量
export PATH="$PATH:$(go env GOPATH)/bin"
# 编译rpc所需pb文件
protoc --go_out=. --go_opt=paths=source_relative \
         --go-grain_out=. --go-grain_opt=paths=source_relative hello.proto
```

设置rpc服务：  
```go
potato.SetRpcConfig(&rpc.Config{
    ClusterName: "nice",    // 集群名称 同一集群中服务需要设置想用集群名称 才能正常组网
    Consul:      "0.0.0.0:8500", // consul地址 用于服务发现
    ServiceKind: []*cluster.Kind{nice.NewServiceKind(func() nice.Service { // 通过proto-actor的grain生成rpc相关代码生成的rpc服务 如果本服务没有供其他服务调用的rpc服务 可以不设置
    return &ServiceImpl{}
    }, 0)},
    EventHandler: OnEvent, // event stream 集群广播事件处理器 如果没有需要处理的事件就不设置
})
```
rpc服务需要实现对应的rpc接口
```go
type ServiceImpl struct{}
func (c ServiceImpl) Init(ctx cluster.GrainContext) {} // grain生成的时候会回调这个方法
func (c ServiceImpl) Terminate(ctx cluster.GrainContext) {} // grain销毁的时候会回调这个方法
func (c ServiceImpl) ReceiveDefault(ctx cluster.GrainContext) {}
func (c ServiceImpl) DoSth(req *pb.Req, ctx cluster.GrainContext) (*pb.Res, error) { // rpc service中的方法
    return &pb.Res{}, nil
}
```
调用rpc服务
```go
// 自定义id用于在service方生成虚拟actor 在service节点没有变动的情况下 同一个identity会始终路由到同一个service
grain := nice.GetServicGrainClient(potato.GetCluster(), "MyIdentity")
res, err := grain.DoSth(&pb.Req{A: 6, B: 6})
```
发送集群订阅事件
```go
potato.BroadcastEvent(&nice.EventHello{SayHello: "niceman"}, false) // 第二个参数为广播是否包含当前节点
```
---

详细的功能代码可以参考 [example](https://github.com/murang/potato/tree/main/example)
* server 用于客户端连接的服务器
* calculator 用于计算的服务 server节点通过rpc调用calculator节点进行计算
* client 客户端
* nicepb 使用protobuf所需的消息文件
* pairpb 作为消息对注册的protobuf
* config 配置文件读取示例
* unity_network Unity的网络实现与示例
---

### 框架结构

在介绍框架结构之前 首先介绍一个开源的actor框架：[protoactor-go](https://github.com/asynkron/protoactor-go)

这个框架在golang中实现了简单易用的actor模型以及集群中的rpc调用虚拟actor(grain)等功能。potato的模块运行模式和rpc就是基于此库的actor和grain进行封装实现。
强烈推荐对这个框架感兴趣的同学可以深入学习一下以便于实现满足自己的需求。

* app
    - Application的实现, 管理整个进程的生命周期。 
    - 不同模块通过actor模型运行在不同的goroutine中, 模块中的业务逻辑可以不用考虑并发。 
    - 模块间的通讯需要通过Application的SendToModule和RequestToModule方法，让消息用队列的方式处理。

* net
    - 网络模块，管理网络监听器，会话，消息编解码等等。
    - 通过抽象监听器和编解码接口，适配不同的网络协议。
    - 每个网络连接的消息收发都是在各自的goroutine中，并发效率高。

* pb
    - protobuf消息注册模块，管理protobuf消息的注册
    - 代码生成插件帮助消息代码生成时自动注册到消息列表中，无需手动注册 详情见 [protoc-gen-autoregister](https://github.com/murang/potato/tree/main/pb/README.md)
    - 支持消息和ID一对一映射，以及消息ID与消息对的映射 Codec也做了相应支持
    - 添加了vtproto对默认proto进行增强 基准测试性能可以提升将近一倍 还有gc优化 详情见 [vtprotobuf](https://github.com/planetscale/vtprotobuf)

* rpc
    - rpc模块，rpc管理器的生命周期管理
    - 实现rpc功能自动注册到集群
    - 可通过EventStream广播消息到集群的其他节点

* log
    - 日志模块，管理日志的输出
    - 日志输出用到了大名鼎鼎的zap，持久化日志通过lumberjack实现。
    - 不进行任何设置的话 默认输出到标准输出。通过InitLogger进行设置，可实现自定义日志级别，保存文件夹，保存天数等等。
    - webhook用于服务器报错通知到飞书，钉钉等。

* config
  - 配置读取 详细可以参考[config](https://github.com/murang/potato/tree/main/config/README.md)
  - 配置文件格式当前只支持json。支持读取本地文件和从consul中读取配置。
  - 可加载同类型但是不同数据的配置，同类型配置通过tag区分，主要解决问题就是AB测试的时候使用不同配置
  - 加载本地配置的时候需要传入tag用于检索配置文件
  - consul配置则不用传入tag 通过关注主配置的key进行检索 模块会自动加载tag配置 通过consul的kv监听 模块支持动态更新和添加配置

### tips
- 数据库，缓存等等模块通常需要根据具体需求来进行选择，并且有很多优秀的库可供选择，这里就不做设计了。
- 根据压力测试的火焰图分析，通常游戏服务器的网络通信这部分会占用绝大部分cpu资源，所以不用刻意把业务逻辑分割到过多的goroutine中。如同传统的c++游戏服务器，只要把阻塞业务放到其他线程，一个主线程基本就能满足所有计算逻辑。单线程的主循环更方便进行性能监控和优化。