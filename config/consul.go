package config

import (
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/api/watch"
	"github.com/murang/potato/log"
	"path/filepath"
	"reflect"
	"strings"
)

var consulClient *api.Client
var onConfigChange = make([]func(IConfig), 0)

// 缓存上一次的 KV 状态，用于检测真正变更的配置
// key: KV的Key, value: ModifyIndex
var kvCache = make(map[string]uint64)

// 监听配置更新
func watchConfigUpdate() {
	// 获取全部配置前缀
	prefixMap := map[string]struct{}{}
	for _, v := range groups {
		if _, ok := prefixMap[v.Path]; !ok {
			prefixMap[v.Path] = struct{}{}
		}
	}

	for k, _ := range prefixMap {
		params := map[string]interface{}{
			"type":   "keyprefix",
			"prefix": k,
		}
		plan, err := watch.Parse(params)
		if err != nil {
			log.Sugar.Errorf("watch param parse error:%v", err)
		}
		plan.Handler = func(idx uint64, data interface{}) {
			switch d := data.(type) {
			case api.KVPairs:
				handleConfigChanges(d)
			default:
				log.Sugar.Warnf("watch data type error:%v", d)
			}
		}

		// 使用带logger的运行方式，可以输出更多调试信息
		if err = plan.RunWithClientAndHclog(consulClient, nil); err != nil {
			log.Sugar.Errorf("watchConfigUpdate error:%v", err)
		}
	}
}

// 处理配置变更
func handleConfigChanges(pairs api.KVPairs) {
	// 构建当前 KV 的 key 集合，用于检测删除
	currentKeys := make(map[string]struct{}, len(pairs))

	// 处理新增或修改的配置
	for _, pair := range pairs {
		currentKeys[pair.Key] = struct{}{}

		// 检查是否真正变更（通过 ModifyIndex 判断）
		if lastIndex, exists := kvCache[pair.Key]; exists && lastIndex == pair.ModifyIndex {
			continue // ModifyIndex 未变，跳过
		}

		// 更新缓存
		kvCache[pair.Key] = pair.ModifyIndex

		if len(pair.Value) == 0 {
			continue // 跳过空配置
		}

		fileName := filepath.Base(pair.Key)
		fileNameWithoutExt := strings.TrimSuffix(fileName, filepath.Ext(fileName))
		fileNameBase := strings.Split(fileNameWithoutExt, "_")[0]

		// 分组配置
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

	// 清理已删除的 key 缓存（可选）
	for key := range kvCache {
		if _, exists := currentKeys[key]; !exists {
			delete(kvCache, key)
			log.Sugar.Infof("config deleted: %s", key)
		}
	}
}
