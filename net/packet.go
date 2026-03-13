package net

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

const (
	lenSize     = 4           // 包体大小字段 数值为后续包体总共长度
	maxPackSize = 1024 * 1024 //消息最大长度
)

var (
	ErrMaxPacket = errors.New("packet over size")
	ErrMinPacket = errors.New("packet short size")
)

var sizeBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, lenSize)
		return &b
	},
}

var pktBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 512)
		return &b
	},
}

// 接收Length-Value格式的封包流程 返回包中的Value
func ReadPacket(reader io.Reader) (v []byte, err error) {

	// Size为uint32，占4字节
	bp := sizeBufferPool.Get().(*[]byte)
	sizeBuffer := *bp

	// 持续读取Size直到读到为止
	_, err = io.ReadFull(reader, sizeBuffer)

	// 发生错误时返回
	if err != nil {
		sizeBufferPool.Put(bp)
		return
	}

	// 用大端格式读取Size
	bodyLen := binary.BigEndian.Uint32(sizeBuffer)
	sizeBufferPool.Put(bp)

	if int(bodyLen) > maxPackSize {
		return nil, ErrMaxPacket
	}

	// 分配包体大小
	v = make([]byte, bodyLen)

	// 读取包体数据
	_, err = io.ReadFull(reader, v)

	return
}

// 发送Length-Value格式的封包
func WritePacket(writer io.Writer, msgData []byte) error {
	totalLen := lenSize + len(msgData)

	bp := pktBufferPool.Get().(*[]byte)
	pkt := *bp

	if cap(pkt) < totalLen {
		pkt = make([]byte, totalLen)
	} else {
		pkt = pkt[:totalLen]
	}

	// Length
	binary.BigEndian.PutUint32(pkt, uint32(len(msgData)))

	// Value
	copy(pkt[lenSize:], msgData)

	// 将数据写入Socket
	var err error
	for pos := 0; pos < totalLen; {
		n, werr := writer.Write(pkt[pos:])
		if werr != nil {
			err = werr
			break
		}
		pos += n
	}

	*bp = pkt
	pktBufferPool.Put(bp)

	return err
}
