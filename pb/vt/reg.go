package vt

import (
	"sync"

	"github.com/murang/potato/util"
	"google.golang.org/protobuf/proto"
)

// vtproto 方法接口
type VTProtoMessage interface {
	MarshalVT() ([]byte, error)
	UnmarshalVT([]byte) error
	SizeVT() int
}

// 内部函数类型
type marshalFunc func(msg VTProtoMessage) ([]byte, error)
type unmarshalFunc func(msg VTProtoMessage, b []byte) error
type sizeFunc func(msg VTProtoMessage) int

var (
	mu               sync.RWMutex
	vtMarshalFuncs   = make(map[uintptr]marshalFunc)
	vtUnmarshalFuncs = make(map[uintptr]unmarshalFunc)
	vtSizeFuncs      = make(map[uintptr]sizeFunc)
)

// 注册一个消息类型
func Register[T VTProtoMessage](t T) {
	typePtr := util.TypePtrOf(t)

	mu.Lock()
	defer mu.Unlock()

	vtMarshalFuncs[typePtr] = func(msg VTProtoMessage) ([]byte, error) {
		return msg.(T).MarshalVT()
	}
	vtUnmarshalFuncs[typePtr] = func(msg VTProtoMessage, b []byte) error {
		return msg.(T).UnmarshalVT(b)
	}
	vtSizeFuncs[typePtr] = func(msg VTProtoMessage) int {
		return msg.(T).SizeVT()
	}
}

// 统一 Marshal
func Marshal(msg proto.Message) ([]byte, error) {
	if v, ok := msg.(VTProtoMessage); ok {
		typePtr := util.TypePtrOf(v)

		mu.RLock()
		fn, ok := vtMarshalFuncs[typePtr]
		mu.RUnlock()
		if ok {
			return fn(v)
		}
		// 没注册也能直接用 vt 方法
		return v.MarshalVT()
	}
	// fallback
	return proto.Marshal(msg)
}

// 统一 Unmarshal
func Unmarshal(data []byte, msg proto.Message) error {
	// vt和proto不同 不会使用默认值重置对象 如果遇到有脏值的对象就会出问题 所以这里手动重置一下
	if r, ok := msg.(interface{ Reset() }); ok {
		r.Reset()
	}
	if v, ok := msg.(VTProtoMessage); ok {
		typePtr := util.TypePtrOf(v)

		mu.RLock()
		fn, ok := vtUnmarshalFuncs[typePtr]
		mu.RUnlock()
		if ok {
			return fn(v, data)
		}
		return v.UnmarshalVT(data)
	}
	return proto.Unmarshal(data, msg)
}

// 统一 Size
func Size(msg proto.Message) int {
	if v, ok := msg.(VTProtoMessage); ok {
		typePtr := util.TypePtrOf(v)

		mu.RLock()
		fn, ok := vtSizeFuncs[typePtr]
		mu.RUnlock()
		if ok {
			return fn(v)
		}
		return v.SizeVT()
	}
	return proto.Size(msg)
}
