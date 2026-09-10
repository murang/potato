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
}
