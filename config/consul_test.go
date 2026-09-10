package config

import (
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/api/watch"
)

type priceConfig struct {
	Value int `json:"value"`
}

func (*priceConfig) Name() string    { return "price" }
func (*priceConfig) Path() string    { return "a" }
func (c *priceConfig) ValuePtr() any { return c }
func (*priceConfig) OnLoad()         {}

type otherConfig struct {
	Value int `json:"value"`
}

func (*otherConfig) Name() string    { return "other" }
func (*otherConfig) Path() string    { return "b" }
func (c *otherConfig) ValuePtr() any { return c }
func (*otherConfig) OnLoad()         {}

func setupConfigs(t *testing.T) (*configWatcher, *configWatcher) {
	t.Helper()
	registryMu.Lock()
	oldGroups, oldClient, oldCallbacks, oldPlans := groups, consulClient, onConfigChange, watchPlans
	groups, consulClient, onConfigChange, watchPlans = make(map[string]*Group), nil, nil, nil
	registryMu.Unlock()
	t.Cleanup(func() {
		StopWatch()
		registryMu.Lock()
		groups, consulClient, onConfigChange, watchPlans = oldGroups, oldClient, oldCallbacks, oldPlans
		registryMu.Unlock()
	})
	FocusConsulConfig(&priceConfig{})
	FocusConsulConfig(&otherConfig{})
	return newConfigWatcher("a/", groups), newConfigWatcher("b/", groups)
}

func kv(key string, index uint64, value int) *api.KVPair {
	return &api.KVPair{Key: key, ModifyIndex: index, Value: []byte(fmt.Sprintf(`{"value":%d}`, value))}
}

func TestWatchPrefixesAreIndependent(t *testing.T) {
	a, b := setupConfigs(t)
	changes := 0
	OnConsulConfigChange(func(c IConfig) {
		if c.Name() == "price" {
			changes++
		}
	})
	first := kv("a/price.json", 1, 1)
	a.apply(api.KVPairs{first})
	b.apply(api.KVPairs{kv("b/other.json", 2, 2), kv("b/price.json", 3, 99)})
	a.apply(api.KVPairs{first})
	if changes != 1 {
		t.Errorf("unchanged price loaded %d times", changes)
	}
	if got := GetConfig[*priceConfig]().Value; got != 1 {
		t.Errorf("foreign path overwrote price: %d", got)
	}
	if got := GetConfig[*otherConfig]().Value; got != 2 {
		t.Errorf("other config missing: %d", got)
	}
}

func TestDeletedTagFallsBackAndDeletedBaseIsAbsent(t *testing.T) {
	a, _ := setupConfigs(t)
	base := kv("a/price.json", 1, 10)
	a.apply(api.KVPairs{base, kv("a/price_b.json", 2, 20)})
	a.apply(api.KVPairs{base})
	if got := GetConfigWithTag[*priceConfig]("b", false); got != nil {
		t.Errorf("deleted tag remains: %+v", got)
	}
	if got := GetConfigWithTag[*priceConfig]("b", true); got == nil || got.Value != 10 {
		t.Errorf("fallback failed: %+v", got)
	}
	a.apply(nil)
	if got := GetConfig[*priceConfig](); got != nil {
		t.Errorf("deleted base remains: %+v", got)
	}
}

func TestInvalidUpdateRetainsLastGoodConfig(t *testing.T) {
	a, _ := setupConfigs(t)
	a.apply(api.KVPairs{kv("a/price.json", 1, 10)})
	a.apply(api.KVPairs{{Key: "a/price.json", ModifyIndex: 2, Value: []byte(`invalid`)}})
	if got := GetConfig[*priceConfig](); got == nil || got.Value != 10 {
		t.Errorf("invalid update lost last good config: %+v", got)
	}
	a.apply(api.KVPairs{kv("a/price.json", 3, 30)})
	if got := GetConfig[*priceConfig]().Value; got != 30 {
		t.Errorf("recovery failed: %d", got)
	}
}

func TestConcurrentWatchesSerializeCallbacks(t *testing.T) {
	a, b := setupConfigs(t)
	var active, concurrent, calls atomic.Int32
	OnConsulConfigChange(func(IConfig) {
		if active.Add(1) != 1 {
			concurrent.Add(1)
		}
		runtime.Gosched()
		_ = GetConfig[*priceConfig]()
		calls.Add(1)
		active.Add(-1)
	})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, entry := range []struct {
		watcher *configWatcher
		key     string
	}{{a, "a/price.json"}, {b, "b/other.json"}} {
		wg.Add(1)
		go func(w *configWatcher, key string) {
			defer wg.Done()
			<-start
			for i := 1; i <= 30; i++ {
				w.apply(api.KVPairs{kv(key, uint64(i), i)})
			}
		}(entry.watcher, entry.key)
	}
	close(start)
	wg.Wait()
	if concurrent.Load() != 0 || calls.Load() != 60 {
		t.Fatalf("concurrent=%d, callbacks=%d", concurrent.Load(), calls.Load())
	}
}

func TestWatchRoutingUsesFullRegisteredName(t *testing.T) {
	a, _ := setupConfigs(t)
	group := &Group{Name: "price_table", Path: "a", ConfigType: reflect.TypeOf(&priceConfig{}), ConfigMap: &sync.Map{}}
	a.groups[group.Name] = group
	name, got := a.resolve("a/price_table_b.json")
	if got != group || name != "price_table_b" {
		t.Fatal("name with underscore was routed to shorter group")
	}
	for _, key := range []string{"ab/price.json", "a/sub/price.json", "a/price.txt"} {
		if _, group := a.resolve(key); group != nil {
			t.Errorf("accepted unrelated key %s", key)
		}
	}
}

func TestStopWatchCanBeCalledFromCallback(t *testing.T) {
	a, _ := setupConfigs(t)
	plan, err := watch.Parse(map[string]interface{}{"type": "keyprefix", "prefix": "a/"})
	if err != nil {
		t.Fatal(err)
	}
	a.plan = plan
	registryMu.Lock()
	watchPlans = []*watch.Plan{plan}
	registryMu.Unlock()
	OnConsulConfigChange(func(IConfig) { StopWatch() })
	a.apply(api.KVPairs{kv("a/price.json", 1, 10)})
	if !plan.IsStopped() {
		t.Fatal("watch was not canceled")
	}
	a.apply(api.KVPairs{kv("a/price.json", 2, 20)})
	if got := GetConfig[*priceConfig]().Value; got != 10 {
		t.Fatalf("stopped watch published value %d", got)
	}
	StopWatch()
}

type orderedBase struct {
	Value int `json:"value"`
}

func (*orderedBase) Name() string    { return "base" }
func (*orderedBase) Path() string    { return "ordered" }
func (c *orderedBase) ValuePtr() any { return c }
func (*orderedBase) OnLoad()         {}
func (*orderedBase) Priority() int   { return 1 }

type orderedDependent struct{ Seen int }

func (*orderedDependent) Name() string    { return "dependent" }
func (*orderedDependent) Path() string    { return "ordered" }
func (c *orderedDependent) ValuePtr() any { return c }
func (c *orderedDependent) OnLoad() {
	if base := GetConfig[*orderedBase](); base != nil {
		c.Seen = base.Value
	}
}
func (*orderedDependent) Priority() int { return 2 }

func TestConfigPriorityPublishesDependenciesBeforeOnLoad(t *testing.T) {
	setupConfigs(t)
	FocusConsulConfig(&orderedBase{})
	FocusConsulConfig(&orderedDependent{})
	w := newConfigWatcher("ordered/", groups)
	w.apply(api.KVPairs{kv("ordered/dependent.json", 1, 0), kv("ordered/base.json", 1, 42)})
	if got := GetConfig[*orderedDependent]().Seen; got != 42 {
		t.Errorf("dependent loaded before base: %d", got)
	}
}
