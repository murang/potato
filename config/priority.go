package config

// 使用Consul读取配置的话 可以实现当前接口 可以设置配置读取优先级
// 实现此接口的配置表会被按优先级升序优先读取 没实现此接口的配置表会在最后读取
type IPriority interface {
	Priority() int
}
