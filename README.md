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

  // 先初始化数据库等基础资源，再按注册顺序初始化模块，最后启动 RPC 和网络
  potato.Start(func() bool {
    log.Logger.Info("resources initialized, starting server")
    return true
  })
  potato.Run()        // 开始update 所有组件开始tick 主线程阻塞
  potato.End(func() { // 主线程开始退出 所有组件销毁后执行入参函数
    log.Logger.Info("all module stopped, server stop")
  })
}
```

`Start()` 先登记全部模块 PID，再按注册顺序等待各模块 `OnStart()`，全部就绪后才启动 RPC 和网络。依赖模块先注册；初始化中只调用已经就绪的依赖，不同步请求自身、不调用尚未启动的 RPC。异步配置加载仍需业务等待就绪或处理 `nil`。`StartWithError()`、`StartContext()` 可显式处理启动错误；旧 `Start()` 保留签名，失败时记录错误并请求退出。`Err()` 返回应用失败原因。

`Application.Exit()`（或 `potato.Exit()`）表示请求退出；`End()` 停止 tick、关闭网络并等待回调，随后停止 RPC 入站业务，再按注册逆序等待每个模块的已接受消息和受管异步任务完成，执行 `OnDestroy()`，最后关闭 RPC。普通跨模块依赖需从后注册模块指向先注册模块；业务自行启动的生产者要先停止或纳入任务登记。RPC 出站 transport 在模块清理期间保留，但路由回本节点的 grain 会被入站门控拒绝，RPC 的 `Terminate` 不得依赖已停止模块。

`End()` 可重复调用，清理及完成回调只执行一次。它应从生命周期协程调用，在模块、网络或 RPC 回调中同步调用会等待自身。业务排空不等于发送队列排空或客户端收到消息。模块 panic 后停止失败实例并请求应用退出，不再自动重跑同一实例的 `OnStart()`；检查 `Err()` 区分失败退出与正常排空，示例 server 在失败时返回非零退出码。

模块默认最多接受 1024 条普通消息（包含正在执行的一条），最多 32 个异步任务或待消费结果。可用 `RegisterModuleWithOptions(..., app.ModuleOptions{QueueLimit: 1024, MaxAsync: 32})` 调整。原 `SendToModule()` 保留无返回值签名，拒绝时限频记录；关键业务使用 `TrySendToModule()` 处理 `ErrBusy`、`ErrNotReady`、`ErrStopping` 等错误，`ModuleStats()` 可读取计数。直接通过 `ActorSystem` 投递会绕过这些保证。

`RequestToModuleContext()` 在执行前跳过已过期请求；旧 `RequestToModule()` 保留 1 秒默认超时和 `actor.ErrTimeout`。超时不撤销已经执行的业务，写操作重试仍需幂等。`OnUpdate()` 最多保留一个未完成 tick，过载时跳过后续 tick，不补跑全部过期帧。

网络消息处理器应只路由消息，避免在全局分发回调中同步等待 RPC。`TryAsyncToModule()` 非阻塞申请任务额度，让 `work func() any` 在后台执行，并把返回的消息交回模块 `OnMsg()`；worker 只能操作不可变输入，不能修改模块字段。额度持续到结果消费完成，普通队列已满也不会丢完成消息。当前 Hello 示例没有请求 ID，同一会话只允许一个未完成请求，重叠或过载时关闭连接。

异步额度限制本地任务和完成消息，不限制第三方 RPC 传输或远端 mailbox。RPC 超时返回后，远端操作仍可能继续；下游慢服务需要自己的准入与幂等策略，不能把本地 MaxAsync 当作全链路在途操作上限。

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
		SessionStartId:       0,
		ConnectLimit:         1000,
		Timeout:              30,
		Codec:                &net.PbCodec{},    // 框架内置JsonCodec和PbCodec，可以实现ICodec接口来定制编解码
		MsgHandler:           &MyMsgHandler{}, // 需要用户自己实现IMsgHandler，用于处理消息
		SendQueueLimit:       4096,             // 未出队消息的上限，超过时关闭连接
		SendQueueIdleTimeout: 30 * time.Second, // 扩大的发送队列空闲多久后缩回初始容量
		SendQueueBytesLimit:  8 << 20,          // 显式启用包体计费预算，包含 writer；默认关闭
		ReceiveBytesLimit:    64 << 20,         // Manager 在途接收包体预算，默认 64 MiB
		MaxPacketSize:        1 << 20,          // 包体上限，默认 1 MiB
	})
// 网络监听器 支持tcp/kcp/ws
ln, _ := net.NewListener("tcp", ":10086")
// 添加网络监听器 可支持同时接收多个监听器消息 统一由MsgHandler处理
potato.GetNetManager().AddListener(ln)
```

⚠️⚠️⚠️ 网络消息按照 `[消息体长度(4字节)] + [消息体]` 为一个数据包来发送 这个4字节的长度默认`大端序` ⚠️⚠️⚠️

Manager 的消息体上限默认 1 MiB，可用 `MaxPacketSize` 设置，长度头不计入包体上限。TCP、KCP 和 WebSocket 使用同一包格式；WebSocket 单条消息上限包含额外的 4 字节长度头。超长包和不完整的 Protobuf 消息 ID 会返回错误并关闭对应会话。独立的 `ReadPacket`、`WritePacket` 保持原签名和 1 MiB 默认上限。

接收端在按长度头分配之前预留 `ReceiveBytesLimit` 额度；短读、解码失败和回调完成后归还。已入队事件的额度由消费者持有，关闭连接不会提前归还。预算耗尽立即关闭该会话，不等待空位。该预算按包体计费，不包含解码膨胀、转发到业务模块后的对象、内核及协议缓冲；多个 Manager 的预算需合计规划。

`Send()` 与 `SendRaw()` 共用按需扩容的环形 FIFO 发送队列，初始容量为 64 条（上限小于 64 时使用该上限），同一调用方依次发送的消息保持入队顺序。发送调用只在内存队列操作期间持有互斥锁，不等待队列空位、编码或网络写入。`SendQueueLimit` 默认 4096，限制尚未出队的消息数量，不包含 writer 已取出的那一条；队列已经达到上限时，下一次发送会立即关闭该会话并丢弃积压消息。环形队列复用出队槽位，底层容量也不超过配置上限。

出队时立即清除消息引用；扩大的队列清空后保留容量，连续空闲达到 `SendQueueIdleTimeout`（默认 30 秒）才缩回初始容量，新一轮发送会重新开始空闲计时。writer 只在需要缩容时启用并复用一个计时器，不为每条消息创建计时器或额外的缩容协程。`Close()` 中断连接读写、释放整个队列并唤醒等待消息的 writer。两个队列配置为零或负数时使用默认值，读写超时仍由 `Timeout` 控制。队列上限按条数计算，消息大小和空闲窗口内保留的槽位容量仍需计入内存预算。

`SendRaw()` 在检查包长、条数和字节额度后于队列锁内复制切片，返回后可复用原数据；`Send()` 的消息对象在编码完成前仍需由调用方保证不被修改。发送方法保持原有的无返回值签名，关闭后的发送会被忽略，返回不表示对端已收到消息。

`SendQueueBytesLimit` 默认关闭；启用后同时限制排队和 writer 正在处理的消息体计费，超过预算关闭会话。PB codec 实现可选 `ICodecSizer`，在发送调用方计算大小；其他 codec 默认按 `MaxPacketSize` 保守预留。比如 8 MiB 预算配合 1 MiB 最大包长，未知 codec 只能容纳约 8 条待发消息，小 JSON 突发也可能达到限制。自定义 `Size` 必须返回可信包体大小，低估在编码后会被核对；任意对象的堆大小仍不由此预算严格限制。未启用预算时不增加 Size 遍历，编码仍由 writer 完成。

会话接入时确定消息处理器及 `IsMsgInRoutine()` 模式；后续 `SetMsgHandler()` 影响新会话。`Manager.OnDestroy()` 会等待现有会话及回调完成，应从管理生命周期的协程调用。业务回调可以直接调用 `Session.Close()`。

WebSocket 的 HTTP 头、请求、响应及升级握手超时各为 5 秒，HTTP 空闲超时 30 秒，头部上限 32 KiB。每个 WS listener 从 TCP Accept 到最终 Close 独立统计原始连接，并直接使用 `ConnectLimit` 作为上限，尚未完成握手的连接也受限制。Manager 仍用同一个配置值限制所有协议的正式 Session 总数；两层计数不合并，多个 WS listener 的握手中连接也不共享一个计数器。`ConnectLimit` 为零或负数时，两层均使用默认值 50000。停止时一并关闭 HTTP 和已经 Hijack 的连接，归还原始连接额度。

KCP 暂时保留基于上游 v4.3.4 的内部生命周期补丁，确保未被 Accept 的会话也在停止时清理；仅关闭 UDP socket 或框架已经接入的连接不足以完整回收。包格式和 `net.Conn` 接口不变，但 `Session.Conn()/Raw()` 返回的 KCP 具体类型属于内部包，不能再断言为外部 `kcp-go.UDPSession`；需要额外设置时应断言所需方法接口。已验证原版 v4.3.4 Go 客户端在本机 UDP、无加密和 FEC 的配置下互通。保留原因、补丁范围、兼容性和性能边界，以及切回上游前必须通过的验证，见 [KCP 生命周期补丁说明](internal/kcp/README.md)。

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
    - webhook 通知采用尽力投递，队列满时丢弃通知，普通日志仍照常记录，业务协程不等待通知服务。

* config
  - 配置读取 详细可以参考[config](https://github.com/murang/potato/tree/main/config/README.md)
  - 配置文件格式当前只支持json。支持读取本地文件和从consul中读取配置。
  - 可加载同类型但是不同数据的配置，同类型配置通过tag区分，主要解决问题就是AB测试的时候使用不同配置
  - 加载本地配置的时候需要传入tag用于检索配置文件
  - consul配置则不用传入tag 通过关注主配置的key进行检索 模块会自动加载tag配置 通过consul的kv监听 模块支持动态更新和添加配置

### tips
- 数据库，缓存等等模块通常需要根据具体需求来进行选择，并且有很多优秀的库可供选择，这里就不做设计了。
- 根据压力测试的火焰图分析，通常游戏服务器的网络通信这部分会占用绝大部分cpu资源，所以不用刻意把业务逻辑分割到过多的goroutine中。如同传统的c++游戏服务器，只要把阻塞业务放到其他线程，一个主线程基本就能满足所有计算逻辑。单线程的主循环更方便进行性能监控和优化。
