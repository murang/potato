package net

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

const (
	lenSize     = 4           // 包体大小字段 数值为后续包体总共长度
	maxPackSize = 1024 * 1024 //消息最大长度
)

var (
	ErrMaxPacket     = errors.New("packet over size")
	ErrMinPacket     = errors.New("packet short size")
	ErrReceiveBudget = errors.New("receive message body budget exhausted")
)

// receiveBudget 限制已接入、尚未完成网络回调的包体计费字节。
// 它不估算解码膨胀、业务转发后的对象或协议内部缓冲，也不是 heap 硬上限。
type receiveBudget struct {
	limit int64
	used  atomic.Int64
}

func (b *receiveBudget) reserve(n int64) bool {
	for {
		used := b.used.Load()
		if n > b.limit-used {
			return false
		}
		if b.used.CompareAndSwap(used, used+n) {
			return true
		}
	}
}

func (b *receiveBudget) release(n int64) {
	if n != 0 {
		b.used.Add(-n)
	}
}

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
	v, _, err = readPacket(reader, maxPackSize, nil)
	return
}

// readPacket 在分配前预留额度。成功时额度交给事件消费者；短读时在此归还。
// 公开 ReadPacket 保持原来的默认包长及调用签名，不使用 Manager 的预算。
func readPacket(reader io.Reader, maxSize int, budget *receiveBudget) (v []byte, charge int64, err error) {

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

	if uint64(bodyLen) > uint64(maxSize) {
		return nil, 0, ErrMaxPacket
	}
	if budget != nil {
		charge = int64(bodyLen)
		if !budget.reserve(charge) {
			return nil, 0, ErrReceiveBudget
		}
	}

	// 分配包体大小
	v = make([]byte, bodyLen)

	// 读取包体数据
	_, err = io.ReadFull(reader, v)
	if err != nil && budget != nil {
		budget.release(charge)
		charge = 0
	}

	return
}

// 发送Length-Value格式的封包
func WritePacket(writer io.Writer, msgData []byte) error {
	return writePacket(writer, msgData, maxPackSize)
}

func writePacket(writer io.Writer, msgData []byte, maxSize int) error {
	if len(msgData) > maxSize {
		return ErrMaxPacket
	}
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
		if n == 0 {
			err = io.ErrShortWrite
			break
		}
		pos += n
	}

	*bp = pkt
	pktBufferPool.Put(bp)

	return err
}
