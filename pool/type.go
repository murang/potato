package pool

import (
	"reflect"
	"sync"
)

// TypedPool 泛型对象池，避免反射开销
type TypedPool[T any] struct {
	pool sync.Pool
}

// NewTypedPool 创建一个泛型对象池
func NewTypedPool[T any](newFunc func() T) *TypedPool[T] {
	return &TypedPool[T]{
		pool: sync.Pool{
			New: func() any {
				return newFunc()
			},
		},
	}
}

// Get 从池中获取对象
func (p *TypedPool[T]) Get() T {
	return p.pool.Get().(T)
}

// Put 归还对象到池中
func (p *TypedPool[T]) Put(obj T) {
	p.pool.Put(obj)
}

// 以下保留基于 reflect 的旧 API 以兼容已有代码

var poolMap = sync.Map{}

func getPool(t reflect.Type) *sync.Pool {
	pool, ok := poolMap.Load(t)
	if !ok {
		pool = &sync.Pool{
			New: func() any {
				if t.Kind() == reflect.Ptr {
					return reflect.New(t.Elem()).Interface()
				} else {
					return reflect.New(t).Interface()
				}
			},
		}
		poolMap.Store(t, pool)
	}
	return pool.(*sync.Pool)
}

func Get(reflectType reflect.Type) any {
	return getPool(reflectType).Get()
}

func Put(t reflect.Type, obj any) {
	getPool(t).Put(obj)
}
