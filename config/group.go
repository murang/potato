package config

import (
	"reflect"
	"sync"
)

var groups = make(map[string]*Group)

var registryMu sync.RWMutex

type Group struct {
	Name       string
	Path       string
	ConfigType reflect.Type
	ConfigMap  *sync.Map
	consul     bool
	// 已发布的 Consul KV key -> 配置名，由 configUpdateMu 保护。
	// 清单属于配置来源而非某次 watch，重启后才能清理停听期间已删除的键。
	consulKeys map[string]string
}
