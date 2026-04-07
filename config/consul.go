package config

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/api/watch"
	"github.com/murang/potato/log"
)

var consulClient *api.Client
var onConfigChange = make([]func(IConfig), 0)

// 缓存上一次的 KV 状态，用于检测真正变更的配置
// key: KV的Key, value: ModifyIndex
var kvCache = make(map[string]uint64)

// watchPlans 保存所有 watch plan 用于关闭时停止
var watchPlans []*watch.Plan

// 监听配置更新
func watchConfigUpdate() {
	// 获取全部配置前缀
	prefixMap := map[string]struct{}{}
	for _, v := range groups {
		if _, ok := prefixMap[v.Path]; !ok {
			prefixMap[v.Path] = struct{}{}
		}
	}

	for k := range prefixMap {
		params := map[string]interface{}{
			"type":   "keyprefix",
			"prefix": k,
		}
		plan, err := watch.Parse(params)
		if err != nil {
			log.Sugar.Errorf("watch param parse error:%v", err)
			continue
		}
		plan.Handler = func(idx uint64, data interface{}) {
			switch d := data.(type) {
			case api.KVPairs:
				handleConfigChanges(d)
			default:
				log.Sugar.Warnf("watch data type error:%v", d)
			}
		}

		watchPlans = append(watchPlans, plan)

		go func(p *watch.Plan) {
			// RunWithClientAndHclog 会阻塞直到 plan.Stop() 被调用
			if err := p.RunWithClientAndHclog(consulClient, nil); err != nil {
				log.Sugar.Errorf("watchConfigUpdate error:%v", err)
			}
		}(plan)
	}
}

// StopWatch 停止所有 consul watch goroutine
func StopWatch() {
	for _, plan := range watchPlans {
		plan.Stop()
	}
	watchPlans = nil
}

// 处理配置变更
func handleConfigChanges(pairs api.KVPairs) {
	// 构建当前 KV 的 key 集合，用于检测删除
	currentKeys := make(map[string]struct{}, len(pairs))

	// 第一轮：过滤出有效变更的 pairs
	var validPairs api.KVPairs
	for _, pair := range pairs {
		currentKeys[pair.Key] = struct{}{}

		if lastIndex, exists := kvCache[pair.Key]; exists && lastIndex == pair.ModifyIndex {
			continue
		}
		kvCache[pair.Key] = pair.ModifyIndex

		if len(pair.Value) == 0 {
			continue
		}
		validPairs = append(validPairs, pair)
	}

	// 按 IPriority 排序：实现了 IPriority 的配置优先加载，按 Priority() 升序
	sort.SliceStable(validPairs, func(i, j int) bool {
		pi, hasPi := configPriority(validPairs[i])
		pj, hasPj := configPriority(validPairs[j])
		if hasPi && hasPj {
			return pi < pj
		}
		return hasPi && !hasPj
	})

	// 第二轮：按排序后的顺序加载配置
	for _, pair := range validPairs {
		fileName := filepath.Base(pair.Key)
		fileNameWithoutExt := strings.TrimSuffix(fileName, filepath.Ext(fileName))
		fileNameBase := strings.Split(fileNameWithoutExt, "_")[0]

		if group := groups[fileNameBase]; group != nil {
			cfg := reflect.New(group.ConfigType.Elem()).Interface().(IConfig)
			if LoadConfigFromBytes(pair.Value, cfg) {
				group.ConfigMap.Store(fileNameWithoutExt, cfg)
				log.Sugar.Infof("config updated: %s", fileNameWithoutExt)
			} else {
				log.Sugar.Errorf("config update failed: %s", fileNameWithoutExt)
				continue
			}
			for _, f := range onConfigChange {
				f(cfg)
			}
		}
	}

	// 清理已删除的 key 缓存
	for key := range kvCache {
		if _, exists := currentKeys[key]; !exists {
			delete(kvCache, key)
			log.Sugar.Infof("config deleted: %s", key)
		}
	}
}

// configPriority 获取 KV pair 对应配置的优先级
func configPriority(pair *api.KVPair) (int, bool) {
	fileName := filepath.Base(pair.Key)
	fileNameWithoutExt := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	fileNameBase := strings.Split(fileNameWithoutExt, "_")[0]

	group := groups[fileNameBase]
	if group == nil {
		return 0, false
	}
	cfg := reflect.New(group.ConfigType.Elem()).Interface()
	if p, ok := cfg.(IPriority); ok {
		return p.Priority(), true
	}
	return 0, false
}
