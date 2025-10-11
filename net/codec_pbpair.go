package net

import (
	"encoding/binary"
	"reflect"

	"github.com/murang/potato/pb"
	"github.com/murang/potato/pb/vt"
	"google.golang.org/protobuf/proto"
)

type PbPairCodec struct {
}

func (c *PbPairCodec) Encode(v interface{}) (msgBytes []byte, err error) {
	msgType := reflect.TypeOf(v)

	msgId := pb.GetIdByType(msgType)
	if msgId == 0 {
		err = ErrorMsgNotRegister
		return
	}

	msg, ok := v.(proto.Message)
	if !ok {
		err = ErrorMsgTypeNotMatch
		return
	}

	// 消息序列化
	data, err := vt.Marshal(msg)
	if err != nil {
		return
	}

	// 加上消息id
	msgBytes = make([]byte, lenMsgId+len(data))
	binary.BigEndian.PutUint32(msgBytes, uint32(msgId))
	copy(msgBytes[lenMsgId:], data)
	return
}

func (c *PbPairCodec) Decode(data []byte) (msg interface{}, err error) {
	// 取出消息id
	msgId := binary.BigEndian.Uint32(data)
	msgType := pb.GetC2STypeById(msgId) // 和PbCodec不一样 这里需要区别是c2s还是s2c
	if msgType == nil {
		err = ErrorMsgNotRegister
		return
	}

	// 消息反序列化
	msg = reflect.New(msgType.Elem()).Interface()
	err = vt.Unmarshal(data[lenMsgId:], msg.(proto.Message))
	return
}
