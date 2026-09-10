package config

import (
	"testing"

	"github.com/hashicorp/consul/api"
)

type consulPathConfig struct {
	Value int `json:"value"`
	path  string
}

func (*consulPathConfig) Name() string    { return "prefix" }
func (c *consulPathConfig) Path() string  { return c.path }
func (c *consulPathConfig) ValuePtr() any { return c }
func (*consulPathConfig) OnLoad()         {}

func TestWatchPathMatchesConsulKeys(t *testing.T) {
	for _, tt := range []struct {
		name   string
		path   string
		prefix string
	}{
		{name: "relative", path: "a", prefix: "a/"},
		{name: "trailing slash", path: "a/", prefix: "a/"},
		{name: "leading slash", path: "/a", prefix: "a/"},
		{name: "surrounding slashes", path: "/a/", prefix: "a/"},
		{name: "empty root", path: "", prefix: ""},
		{name: "slash root", path: "/", prefix: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupConfigs(t)
			FocusConsulConfig(&consulPathConfig{path: tt.path})
			watcher := newConfigWatcher(consulPrefix(tt.path), groups)
			// Consul 返回的 KV Key 不带路径开头的斜杠，需与注册路径使用相同格式。
			watcher.apply(api.KVPairs{kv(tt.prefix+"prefix.json", 1, 10), kv(tt.prefix+"prefix_b.json", 2, 20)})
			if got := GetConfig[*consulPathConfig](); got == nil || got.Value != 10 {
				t.Fatalf("Path=%q did not load key %q: %+v", tt.path, tt.prefix+"prefix.json", got)
			}
			if got := GetConfigWithTag[*consulPathConfig]("b", false); got == nil || got.Value != 20 {
				t.Fatalf("Path=%q did not load tag key %q: %+v", tt.path, tt.prefix+"prefix_b.json", got)
			}
		})
	}
}
