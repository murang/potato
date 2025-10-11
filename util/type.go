package util

import "unsafe"

// unsafe 获取类型唯一 ID（避免反射开销）
func TypePtrOf(t any) uintptr {
	return (*[2]uintptr)(unsafe.Pointer(&t))[0]
}
