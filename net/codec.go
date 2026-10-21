package net

type ICodec interface {
	Decode([]byte) (interface{}, error)
	Encode(interface{}) ([]byte, error)
}

// ICodecSizer 是发送消息体预算的可选能力，不改变 ICodec 的实现要求。
// Size 必须包含 codec 自己的消息头，但不包含框架四字节长度头。
// 消息在编码结束前不得修改；没有此能力时按 MaxPacketSize 保守预留。
// 返回的是编码长度，不是任意 Go 对象的堆大小。
type ICodecSizer interface {
	Size(interface{}) (int, error)
}
