package config

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/hashicorp/consul/api"
)

// IConfig 管理配置数据的对象需要实现此接口；Consul 和 tag 配置使用指针类型注册。
type IConfig interface {
	Name() string  // 配置名称
	Path() string  // 本地目录或 Consul KV 目录前缀
	ValuePtr() any // 返回用于 JSON 解析的指针
	OnLoad()       // 解析完成后初始化派生数据
}

func registerConfig(cfg IConfig, consul bool) *Group {
	if cfg == nil || (reflect.TypeOf(cfg).Kind() == reflect.Ptr && reflect.ValueOf(cfg).IsNil()) {
		panic("config is nil")
	}
	group := &Group{Name: cfg.Name(), Path: cfg.Path(), ConfigType: reflect.TypeOf(cfg), ConfigMap: &sync.Map{}}
	registryMu.Lock()
	defer registryMu.Unlock()
	if consul && consulClient != nil {
		panic("SetConsul must be called after FocusConsulConfig")
	}
	if _, exists := groups[group.Name]; exists {
		panic("config name already exists")
	}
	groups[group.Name] = group
	return group
}

// FocusConsulConfig 必须在 SetConsul 之前注册需要监听的配置。
func FocusConsulConfig(cfg IConfig) { registerConfig(cfg, true) }

// SetConsul 保留异步加载语义，返回前完成所有 watch 的登记，便于可靠停止。
func SetConsul(addr string) {
	cli, err := api.NewClient(&api.Config{Address: addr})
	if err != nil {
		panic(fmt.Sprintf("Config SetConsul NewClient err: %s", err))
	}
	startConsulWatches(cli)
}

// OnConsulConfigChange 注册成功加载后的回调。回调由配置更新流程串行执行。
func OnConsulConfigChange(f func(IConfig)) {
	if f == nil {
		return
	}
	registryMu.Lock()
	onConfigChange = append(onConfigChange, f)
	registryMu.Unlock()
}

func LoadConfig(cfg IConfig, tag ...string) {
	group := registerConfig(cfg, false)
	if LoadConfigFromFile(group.Name, group.Path, cfg) {
		group.ConfigMap.Store(group.Name, cfg)
	}
	for _, t := range tag {
		tagConfig := reflect.New(group.ConfigType.Elem()).Interface().(IConfig)
		name := fmt.Sprintf("%s_%s", group.Name, t)
		if LoadConfigFromFile(name, group.Path, tagConfig) {
			group.ConfigMap.Store(name, tagConfig)
		}
	}
}

func getGroup(name string) *Group {
	registryMu.RLock()
	group := groups[name]
	registryMu.RUnlock()
	return group
}

func GetConfig[T IConfig]() T {
	var cfg T
	group := getGroup(cfg.Name())
	if group == nil {
		return cfg
	}
	value, ok := group.ConfigMap.Load(group.Name)
	if !ok {
		return cfg
	}
	return value.(T)
}

// GetConfigWithTag 在 tag 不存在且 fallback 为 true 时返回主配置。
func GetConfigWithTag[T IConfig](tag string, fallback bool) T {
	var cfg T
	group := getGroup(cfg.Name())
	if group == nil {
		return cfg
	}
	value, ok := group.ConfigMap.Load(fmt.Sprintf("%s_%s", group.Name, tag))
	if !ok {
		if fallback {
			return GetConfig[T]()
		}
		return cfg
	}
	return value.(T)
}
