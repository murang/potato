package main

import (
	"math/rand"

	"github.com/murang/potato/config"
	"github.com/murang/potato/log"
)

type WorkModule struct {
}

func (w WorkModule) Name() string {
	return "work"
}

func (w WorkModule) FPS() uint {
	return 1
}

func (w WorkModule) OnStart() {
	log.Sugar.Info("work module start, load configs first ~")

	// 本地配置
	//config.LoadConfig(&UserConfig{})
	//config.LoadConfig(&PriceConfig{}, "b") // 加载主配置以及tag配置

	// consul配置 可把配置放入consul的kv中尝试读取
	config.FocusConsulConfig(&UserConfig{})
	config.FocusConsulConfig(&PriceConfig{})
	config.SetConsul("localhost:8500")
}

func (w WorkModule) OnUpdate() {
	// 使用泛型获取config
	userCfg := config.GetConfig[*UserConfig]()
	priceCfg := config.GetConfig[*PriceConfig]()
	if rand.Intn(100) > 50 {
		tom := userCfg.GetUserByName("Tomi")
		log.Sugar.Info("Tom likes ", tom.Likes)
		log.Sugar.Info("Milk price: ", priceCfg.Milk)
	} else {
		jerry := userCfg.GetUserByName("Jerry")
		log.Sugar.Info("Jerry likes ", jerry.Likes)
		log.Sugar.Info("Cheese price: ", priceCfg.Cheese)
	}
	// fallback用于找不到tag时是否返回默认不带tag的配置
	priceTagCfg := config.GetConfigWithTag[*PriceConfig]("b", true)
	log.Sugar.Info("Milk—b price: ", priceTagCfg.Milk)
}

func (w WorkModule) OnDestroy() {
	log.Sugar.Info("work module end")
}

func (w WorkModule) OnMsg(msg interface{}) {
}

func (w WorkModule) OnRequest(msg interface{}) interface{} {
	return nil
}
