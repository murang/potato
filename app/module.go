package app

type IModule interface {
	Name() string                          // 模块名称
	FPS() uint                             // 模块帧率 0的话就是不tick
	OnStart()                              // 按注册顺序初始化；可请求已 ready 依赖，不使用 RPC
	OnUpdate()                             // 模块tick
	OnDestroy()                            // 逆注册顺序销毁；只使用仍存活依赖，不等待回投自身的任务
	OnMsg(msg interface{})                 // 模块收到消息 不用返回结果
	OnRequest(msg interface{}) interface{} // 模块收到请求 需要返回结果
}
