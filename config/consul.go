package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/api/watch"
	"github.com/murang/potato/log"
)

// 这些注册状态由 registryMu 保护。每个 watch 单独持有版本缓存。
var consulClient *api.Client
var onConfigChange []func(IConfig)
var watchPlans []*watch.Plan
var watchGeneration uint64

// 串行执行 OnLoad 和变更回调，锁不用于注册或配置读取，回调中可读取配置和停止监听。
var configUpdateMu sync.Mutex

type configWatcher struct {
	prefix     string
	groups     map[string]*Group
	cache      map[string]uint64
	plan       *watch.Plan
	generation uint64
}

func consulPrefix(path string) string {
	// Consul 查询会移除开头的斜杠，返回的 KV Key 也不带该斜杠；匹配前统一格式。
	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}
	return path + "/"
}

func newConfigWatcher(prefix string, registered map[string]*Group) *configWatcher {
	w := &configWatcher{prefix: prefix, groups: make(map[string]*Group), cache: make(map[string]uint64), generation: watchGeneration}
	for name, group := range registered {
		if group.consul && consulPrefix(group.Path) == prefix {
			w.groups[name] = group
		}
	}
	return w
}

func startConsulWatches(client *api.Client) {
	registryMu.Lock()
	// 所有计划校验完成后再切换客户端和代次；任何配置错误都保留旧监听。
	nextGeneration := watchGeneration + 1
	var plans []*watch.Plan
	prefixes := make(map[string]struct{})
	for _, group := range groups {
		if group.consul {
			prefixes[consulPrefix(group.Path)] = struct{}{}
		}
	}
	for prefix := range prefixes {
		plan, err := watch.Parse(map[string]interface{}{"type": "keyprefix", "prefix": prefix})
		if err != nil {
			registryMu.Unlock()
			panic(fmt.Sprintf("Config SetConsul watch prefix %q: %v", prefix, err))
		}
		w := newConfigWatcher(prefix, groups)
		w.plan = plan
		w.generation = nextGeneration
		plan.Handler = func(_ uint64, data interface{}) {
			if pairs, ok := data.(api.KVPairs); ok {
				w.apply(pairs)
			}
		}
		plans = append(plans, plan)
	}
	oldPlans := watchPlans
	consulClient, watchPlans, watchGeneration = client, plans, nextGeneration
	registryMu.Unlock()

	for _, plan := range oldPlans {
		plan.Stop()
	}
	for _, plan := range plans {
		go func(p *watch.Plan) {
			if err := p.RunWithClientAndHclog(client, nil); err != nil {
				log.Sugar.Errorf("watchConfigUpdate error: %v", err)
			}
		}(plan)
	}
}

// StopWatch 取消所有已登记的监听；已开始的回调允许完成。
// 不同步等待回调自身，因此也可以在配置变更回调内调用。
func StopWatch() {
	registryMu.Lock()
	plans := watchPlans
	watchPlans = nil
	watchGeneration++
	registryMu.Unlock()
	for _, plan := range plans {
		plan.Stop()
	}
}

func (w *configWatcher) isCurrent() bool {
	registryMu.RLock()
	current := w.generation == watchGeneration
	registryMu.RUnlock()
	return current && (w.plan == nil || !w.plan.IsStopped())
}

func (w *configWatcher) resolve(key string) (string, *Group) {
	if !strings.HasPrefix(key, w.prefix) {
		return "", nil
	}
	file := strings.TrimPrefix(key, w.prefix)
	if strings.Contains(file, "/") || !strings.HasSuffix(file, ".json") {
		return "", nil
	}
	name := strings.TrimSuffix(file, ".json")
	if group := w.groups[name]; group != nil {
		return name, group
	}
	var match *Group
	for base, group := range w.groups {
		if strings.HasPrefix(name, base+"_") && (match == nil || len(base) > len(match.Name)) {
			match = group
		}
	}
	return name, match
}

func (w *configWatcher) apply(pairs api.KVPairs) {
	configUpdateMu.Lock()
	defer configUpdateMu.Unlock()
	if !w.isCurrent() {
		return
	}

	type change struct {
		pair        *api.KVPair
		name        string
		group       *Group
		cfg         IConfig
		priority    int
		hasPriority bool
	}
	currentKeys := make(map[string]struct{}, len(pairs))
	var changes []change
	for _, pair := range pairs {
		if pair == nil {
			continue
		}
		name, group := w.resolve(pair.Key)
		if group == nil {
			continue
		}
		currentKeys[pair.Key] = struct{}{}
		if old, ok := w.cache[pair.Key]; ok && old == pair.ModifyIndex {
			continue
		}
		if len(pair.Value) == 0 {
			continue
		}
		cfg := reflect.New(group.ConfigType.Elem()).Interface().(IConfig)
		c := change{pair: pair, name: name, group: group, cfg: cfg}
		if p, ok := cfg.(IPriority); ok {
			c.priority, c.hasPriority = p.Priority(), true
		}
		changes = append(changes, c)
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].hasPriority && changes[j].hasPriority {
			return changes[i].priority < changes[j].priority
		}
		return changes[i].hasPriority && !changes[j].hasPriority
	})

	registryMu.RLock()
	callbacks := append([]func(IConfig){}, onConfigChange...)
	registryMu.RUnlock()
	for _, c := range changes {
		if !w.isCurrent() {
			return
		}
		if !LoadConfigFromBytes(c.pair.Value, c.cfg) {
			continue
		}
		// OnLoad 期间可能已停止或更换 watch。代次检查和发布共用注册锁，
		// 防止“检查后切代、旧配置再写入”的竞态；用户代码不持有此锁，
		// 因而回调内 StopWatch 不会等待自身。版本缓存不跨代复用：
		// 不同 Consul 后端可能以相同 ModifyIndex 返回不同内容。
		registryMu.RLock()
		if w.generation != watchGeneration {
			registryMu.RUnlock()
			return
		}
		c.group.ConfigMap.Store(c.name, c.cfg)
		c.group.consulKeys[c.pair.Key] = c.name
		w.cache[c.pair.Key] = c.pair.ModifyIndex
		registryMu.RUnlock()
		log.Sugar.Infof("config updated: %s", c.name)
		for _, f := range callbacks {
			if !w.isCurrent() {
				return
			}
			f(c.cfg)
		}
	}
	// 只删除完整快照中缺失的已发布键。存在但 JSON 无效或为空的键仍在
	// currentKeys 中，保留上次有效值；跨代共享的来源清单覆盖停听期间的删除。
	for _, group := range w.groups {
		for key, name := range group.consulKeys {
			if _, exists := currentKeys[key]; exists {
				continue
			}
			registryMu.RLock()
			if w.generation != watchGeneration {
				registryMu.RUnlock()
				return
			}
			group.ConfigMap.Delete(name)
			delete(group.consulKeys, key)
			delete(w.cache, key)
			registryMu.RUnlock()
			log.Sugar.Infof("config deleted: %s", key)
		}
	}
}
