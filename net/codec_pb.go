package net

import (
	"encoding/binary"
	"errors"
	"reflect"

	"github.com/murang/potato/pb"
	"github.com/murang/potato/pb/vt"
	"google.golang.org/protobuf/proto"
)

// pb消息按照 【消息id + 消息内容bytes】 的格式进行传输 消息id占4字节

const (
	lenMsgId = 4
)

var (
	ErrorMsgNotRegister  = errors.New("msg not register")
	ErrorMsgTypeNotMatch = errors.New("msg type not match protobuf")
)

type PbCodec struct {
}

func (c *PbCodec) Encode(v interface{}) (msgBytes []byte, err error) {
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

func (c *PbCodec) Decode(data []byte) (msg interface{}, err error) {
	// 取出消息id
	msgId := binary.BigEndian.Uint32(data)
	msgType := pb.GetTypeById(msgId)
	if msgType == nil {
		err = ErrorMsgNotRegister
		return
	}

	// 消息反序列化
	msg = reflect.New(msgType.Elem()).Interface()
	err = vt.Unmarshal(data[lenMsgId:], msg.(proto.Message))
	return
}
