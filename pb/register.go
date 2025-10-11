package pb

import (
	"os"
	"reflect"

	"github.com/murang/potato/log"
)

// ⚠️ 注册的消息type全是指针
var (
	id2Type = make(map[uint32]reflect.Type) // msgId -> type

	pairId2TypeC2S = make(map[uint32]reflect.Type)
	pairId2TypeS2C = make(map[uint32]reflect.Type)

	type2Id = make(map[reflect.Type]uint32) // type -> msgId
)

func RegisterMsg(msgId uint32, msgType reflect.Type) {
	if _, ok := type2Id[msgType]; ok {
		log.Sugar.Errorf("RegisterMsgMateType2Id err, msg repeat : %s", msgType.String())
		os.Exit(2)
	}
	type2Id[msgType] = msgId
	if _, ok := id2Type[msgId]; ok {
		log.Sugar.Errorf("RegisterC2SMsgMate err, msg repeat : %d", msgId)
		os.Exit(2)
	}
	id2Type[msgId] = msgType
}

func RegisterMsgPair(msgId uint32, c2s, s2c reflect.Type) {
	if c2s == nil && s2c == nil {
		log.Sugar.Fatalf("RegisterMsgMateType2Id err")
		return
	}
	if c2s != nil {
		if _, ok := type2Id[c2s]; ok {
			log.Sugar.Fatalf("RegisterMsgMateType2Id err, msg repeat : %s", c2s.String())
		}
		type2Id[c2s] = msgId
		if _, ok := pairId2TypeC2S[msgId]; ok {
			log.Sugar.Fatalf("RegisterC2SMsgMate err, msg repeat : %d", msgId)
		}
		pairId2TypeC2S[msgId] = c2s
	}
	if s2c != nil {
		if _, ok := type2Id[s2c]; ok {
			log.Sugar.Fatalf("RegisterMsgMateType2Id err, msg repeat : %s", s2c.String())
		}
		type2Id[s2c] = msgId
		if _, ok := pairId2TypeS2C[msgId]; ok {
			log.Sugar.Fatalf("RegisterS2CMsgMate err, msg repeat : %d", msgId)
		}
		pairId2TypeS2C[msgId] = s2c
	}
}

func GetIdByType(t reflect.Type) uint32 {
	id, ok := type2Id[t]
	if !ok {
		return 0
	}
	return id
}

func GetTypeById(id uint32) reflect.Type {
	t, ok := id2Type[id]
	if !ok {
		return nil
	}
	return t
}

func GetC2STypeById(id uint32) reflect.Type {
	t, ok := pairId2TypeC2S[id]
	if !ok {
		return nil
	}
	return t
}

func GetS2CTypeById(id uint32) reflect.Type {
	t, ok := pairId2TypeS2C[id]
	if !ok {
		return nil
	}
	return t
}
