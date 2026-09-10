package config

import (
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

// 串行执行 OnLoad 和变更回调，锁不用于注册或配置读取，回调中可读取配置和停止监听。
var configUpdateMu sync.Mutex

type watchedValue struct {
	index uint64
	name  string
	group *Group
}

type configWatcher struct {
	prefix string
	groups map[string]*Group
	cache  map[string]watchedValue
	plan   *watch.Plan
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
	w := &configWatcher{prefix: prefix, groups: make(map[string]*Group), cache: make(map[string]watchedValue)}
	for name, group := range registered {
		if consulPrefix(group.Path) == prefix {
			w.groups[name] = group
		}
	}
	return w
}

func startConsulWatches(client *api.Client) {
	registryMu.Lock()
	consulClient = client
	oldPlans := watchPlans
	watchPlans = nil
	prefixes := make(map[string]struct{})
	for _, group := range groups {
		prefixes[consulPrefix(group.Path)] = struct{}{}
	}
	for prefix := range prefixes {
		plan, err := watch.Parse(map[string]interface{}{"type": "keyprefix", "prefix": prefix})
		if err != nil {
			log.Sugar.Errorf("watch param parse error: %v", err)
			continue
		}
		w := newConfigWatcher(prefix, groups)
		w.plan = plan
		plan.Handler = func(_ uint64, data interface{}) {
			if pairs, ok := data.(api.KVPairs); ok {
				w.apply(pairs)
			}
		}
		watchPlans = append(watchPlans, plan)
	}
	plans := append([]*watch.Plan(nil), watchPlans...)
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
	registryMu.Unlock()
	for _, plan := range plans {
		plan.Stop()
	}
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
	if w.plan != nil && w.plan.IsStopped() {
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
		if old, ok := w.cache[pair.Key]; ok && old.index == pair.ModifyIndex {
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
		if w.plan != nil && w.plan.IsStopped() {
			return
		}
		if !LoadConfigFromBytes(c.pair.Value, c.cfg) {
			continue
		}
		c.group.ConfigMap.Store(c.name, c.cfg)
		// 只有成功发布才记下版本，失败配置不会阻止之后重试。
		w.cache[c.pair.Key] = watchedValue{index: c.pair.ModifyIndex, name: c.name, group: c.group}
		log.Sugar.Infof("config updated: %s", c.name)
		for _, f := range callbacks {
			f(c.cfg)
		}
	}
	for key, old := range w.cache {
		if _, exists := currentKeys[key]; !exists {
			old.group.ConfigMap.Delete(old.name)
			delete(w.cache, key)
			log.Sugar.Infof("config deleted: %s", key)
		}
	}
}
