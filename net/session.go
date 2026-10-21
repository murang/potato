package net

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/murang/potato/log"
)

type SessionEventType int32

const (
	SessionOpen SessionEventType = iota
	SessionClose
	SessionMsg
)

type outgoingMessage struct {
	message any
	raw     []byte
	isRaw   bool
	charge  int64
}

const (
	initialOutgoingCapacity     = 64
	defaultSendQueueLimit       = 4096
	defaultSendQueueIdleTimeout = 30 * time.Second
)

type Session struct {
	manager           *Manager
	id                uint64
	conn              net.Conn // 创建后不再替换，可安全并发读写和关闭
	handler           IMsgHandler
	inRoutine         bool
	outgoingMu        sync.Mutex
	outgoing          []outgoingMessage
	outgoingHead      int
	outgoingLen       int
	outgoingBytes     int64 // 包含已出队但尚未写完的消息；由 outgoingMu 保护
	outgoingWake      chan struct{}
	outgoingIdleAt    time.Time
	outgoingIdleTimer *time.Timer // 仅由 writer 创建、复用和停止
	done              chan struct{}
	startOnce         sync.Once
	closeOnce         sync.Once
	exitSync          sync.WaitGroup
}

type SessionEvent struct {
	Session *Session
	Type    SessionEventType
	Msg     interface{}
}

// 保留公开 SessionEvent 的形状；内部事件携带原始包体额度，不另建消息队列。
type queuedSessionEvent struct {
	SessionEvent
	receiveBytes int64
}

func (s *Session) Conn() net.Conn   { return s.conn }
func (s *Session) ID() uint64       { return s.id }
func (s *Session) Raw() interface{} { return s.Conn() }

// Close 立即中断连接 I/O 并丢弃待发消息，不等待业务回调返回。
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.conn != nil {
			_ = s.conn.Close()
		}
		s.outgoingMu.Lock()
		if s.outgoingBytes != 0 {
			// 默认未启用计费时保持 O(1) 清理；启用时也只扫描仍在排队的槽位。
			for i := 0; i < s.outgoingLen; i++ {
				index := (s.outgoingHead + i) % len(s.outgoing)
				s.outgoingBytes -= s.outgoing[index].charge
			}
		}
		s.outgoing = nil
		s.outgoingHead = 0
		s.outgoingLen = 0
		s.outgoingIdleAt = time.Time{}
		s.outgoingMu.Unlock()
	})
}

// Send 将消息加入按需扩容的发送队列，不等待队列空位或网络写入。
// 达到排队消息上限后的新发送会关闭会话；返回不表示消息已送达。
func (s *Session) Send(msg interface{}) {
	if msg == nil || s.IsClosed() {
		return
	}
	next := outgoingMessage{message: msg}
	if s.manager.sendQueueBytesLimit > 0 {
		// Size 在队列锁外运行；禁用预算时不增加旧 Send 的同步遍历成本。
		// 未知 codec 按最大合法包体计费，只约束编码量，不能估算任意对象的堆大小。
		size := s.manager.maxPacketSize
		if codec, ok := s.manager.codec.(ICodecSizer); ok {
			var err error
			size, err = codec.Size(msg)
			if err != nil || size < 0 || size > s.manager.maxPacketSize {
				s.Close()
				return
			}
		}
		next.charge = int64(size)
	}
	s.enqueue(next)
}

// SendRaw 在返回前复制数据，调用方可以随后复用原切片。
// Send 和 SendRaw 共用可扩容的 FIFO，都不等待队列空位或网络写入。
func (s *Session) SendRaw(data []byte) {
	if data != nil && !s.IsClosed() {
		if len(data) > s.manager.maxPacketSize {
			s.Close()
			return
		}
		next := outgoingMessage{raw: data, isRaw: true}
		if s.manager.sendQueueBytesLimit > 0 {
			next.charge = int64(len(data))
		}
		s.enqueue(next)
	}
}

func (s *Session) enqueue(msg outgoingMessage) {
	if s.IsClosed() {
		return
	}
	s.outgoingMu.Lock()
	// Close 可在加锁前发生，必须在锁内复查，避免清理后重新保留消息。
	if s.IsClosed() {
		s.outgoingMu.Unlock()
		return
	}
	if s.outgoingLen >= s.manager.sendQueueLimit ||
		(s.manager.sendQueueBytesLimit > 0 && msg.charge > s.manager.sendQueueBytesLimit-s.outgoingBytes) {
		// Close 也会取得队列锁，必须先解锁，避免超限处理死锁。
		s.outgoingMu.Unlock()
		s.Close()
		return
	}
	if msg.isRaw {
		// 先准入再复制，避免被拒绝的大包仍瞬时占用额外内存。
		// ponytail: 复制和入队共用短锁以保持 FIFO；仅在实测锁争用后再拆预留阶段。
		msg.raw = bytes.Clone(msg.raw)
	}
	// 环形队列复用出队槽位，上限附近也不需要逐条搬动积压消息。
	// 只有槽位用完时才扩容；拆开环的两段复制，保持已有消息的顺序。
	if s.outgoingLen == len(s.outgoing) {
		capacity := min(len(s.outgoing)*2, s.manager.sendQueueLimit)
		grown := make([]outgoingMessage, capacity)
		copied := copy(grown, s.outgoing[s.outgoingHead:])
		copy(grown[copied:], s.outgoing[:s.outgoingHead])
		s.outgoing = grown
		s.outgoingHead = 0
	}
	tail := s.outgoingHead + s.outgoingLen
	if tail >= len(s.outgoing) {
		tail -= len(s.outgoing)
	}
	s.outgoing[tail] = msg
	s.outgoingLen++
	s.outgoingBytes += msg.charge
	if s.outgoingLen == 1 {
		s.outgoingIdleAt = time.Time{}
		// 只在空队列变为非空时唤醒 writer；通知合并，不等待发送方。
		select {
		case s.outgoingWake <- struct{}{}:
		default:
		}
	}
	s.outgoingMu.Unlock()
}

func (s *Session) nextOutgoing() (outgoingMessage, bool) {
	for {
		s.outgoingMu.Lock()
		if s.IsClosed() {
			s.outgoingMu.Unlock()
			return outgoingMessage{}, false
		}
		if s.outgoingLen != 0 {
			next := s.outgoing[s.outgoingHead]
			// 释放消息引用，保留槽位供后续突发复用。编码和网络写入在锁外。
			s.outgoing[s.outgoingHead] = outgoingMessage{}
			s.outgoingHead++
			if s.outgoingHead == len(s.outgoing) {
				s.outgoingHead = 0
			}
			s.outgoingLen--
			if s.outgoingLen == 0 {
				s.outgoingHead = 0
				if len(s.outgoing) > initialOutgoingCapacity {
					s.outgoingIdleAt = time.Now()
				}
			}
			s.outgoingMu.Unlock()
			return next, true
		}
		var idle <-chan time.Time
		if len(s.outgoing) > initialOutgoingCapacity {
			remaining := s.manager.sendQueueIdleTimeout - time.Since(s.outgoingIdleAt)
			if remaining <= 0 {
				s.outgoing = make([]outgoingMessage, initialOutgoingCapacity)
				s.outgoingIdleAt = time.Time{}
			} else {
				// 仅扩大的队列需要空闲计时，writer 复用一个 timer；不为每条
				// 消息分配 timer，也不启动额外的缩容 goroutine。
				if s.outgoingIdleTimer == nil {
					s.outgoingIdleTimer = time.NewTimer(remaining)
				} else {
					s.outgoingIdleTimer.Reset(remaining)
				}
				idle = s.outgoingIdleTimer.C
			}
		}
		s.outgoingMu.Unlock()
		select {
		case <-s.done:
			return outgoingMessage{}, false
		case <-s.outgoingWake:
		case <-idle:
		}
		if s.outgoingIdleTimer != nil {
			s.outgoingIdleTimer.Stop()
		}
	}
}

func (s *Session) IsClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Session) Start() {
	s.startOnce.Do(func() {
		if s.IsClosed() || s.conn == nil || !s.manager.admit(s) {
			s.Close()
			return
		}
		s.inRoutine = s.handler != nil && s.handler.IsMsgInRoutine()
		s.exitSync.Add(2)
		go func() {
			s.exitSync.Wait()
			s.Close()
			s.manager.remove(s)
			s.lifecycleEvent(SessionClose)
			s.manager.sessions.Done()
		}()
		// 先启动 writer，让 OnSessionOpen 回调产生的消息可以立即被消费。
		go s.writeLoop()
		s.lifecycleEvent(SessionOpen)
		go s.readLoop()
	})
}

func (s *Session) lifecycleEvent(kind SessionEventType) {
	event := &queuedSessionEvent{SessionEvent: SessionEvent{Session: s, Type: kind}}
	if s.inRoutine {
		s.manager.dispatch(event)
	} else {
		// Open/Close 必须成对送达；Manager 在所有生产者结束后才关闭队列。
		s.manager.sessionEventChan <- event
	}
}

func (s *Session) readLoop() {
	defer s.exitSync.Done()
	defer s.Close()
	for !s.IsClosed() {
		data, charge, err := s.readMessageBytes()
		if err != nil {
			if !s.IsClosed() && !isClosedError(err) {
				log.Sugar.Warnf("session read error, sesid: %d, err: %v", s.ID(), err)
			}
			return
		}
		msg, err := s.manager.codec.Decode(data)
		if err != nil {
			s.manager.receiveBudget.release(charge)
			log.Sugar.Warnf("decode msg error, sesid: %d, err: %v", s.ID(), err)
			return
		}
		if s.IsClosed() {
			s.manager.receiveBudget.release(charge)
			return
		}
		event := &queuedSessionEvent{
			SessionEvent: SessionEvent{Session: s, Type: SessionMsg, Msg: msg},
			receiveBytes: charge,
		}
		if s.inRoutine {
			s.manager.dispatch(event)
		} else {
			select {
			case <-s.done:
				s.manager.receiveBudget.release(charge)
				return
			case s.manager.sessionEventChan <- event:
			}
		}
	}
}

func (s *Session) readMessageBytes() ([]byte, int64, error) {
	if s.manager.timeout != 0 {
		if err := s.conn.SetReadDeadline(time.Now().Add(time.Duration(s.manager.timeout) * time.Second)); err != nil {
			return nil, 0, err
		}
	}
	return readPacket(s.conn, s.manager.maxPacketSize, &s.manager.receiveBudget)
}

func (s *Session) writeLoop() {
	defer s.exitSync.Done()
	defer s.Close()
	defer func() {
		if s.outgoingIdleTimer != nil {
			s.outgoingIdleTimer.Stop()
		}
	}()
	for {
		next, ok := s.nextOutgoing()
		if !ok || s.IsClosed() {
			if ok {
				s.releaseOutgoing(next.charge)
			}
			return
		}
		if err := s.writeOutgoing(next); err != nil {
			if !s.IsClosed() && !isClosedError(err) {
				log.Sugar.Warnf("session write error, sesid: %d, err: %v", s.ID(), err)
			}
			return
		}
	}
}

func (s *Session) releaseOutgoing(charge int64) {
	if charge != 0 {
		s.outgoingMu.Lock()
		s.outgoingBytes -= charge
		s.outgoingMu.Unlock()
	}
}

func (s *Session) writeOutgoing(next outgoingMessage) error {
	// 出队不归还额度：慢连接仍持有包体。Close 只归还排队条目，
	// writer 无论编码失败、部分写失败或连接关闭，都在自己的退出路径归还一次。
	defer func() { s.releaseOutgoing(next.charge) }()
	data := next.raw
	if !next.isRaw {
		var err error
		data, err = s.manager.codec.Encode(next.message)
		if err != nil {
			return err
		}
		next.message = nil
	}
	if len(data) > s.manager.maxPacketSize {
		return ErrMaxPacket
	}
	if s.manager.sendQueueBytesLimit > 0 && int64(len(data)) > next.charge {
		// 自定义 Size 低估、或调用方违反对象不可变契约时，先补足额度再写。
		// 编码已经产生的临时分配只能事后检测，因此不能宣称 heap 硬上限。
		extra := int64(len(data)) - next.charge
		s.outgoingMu.Lock()
		if s.IsClosed() || extra > s.manager.sendQueueBytesLimit-s.outgoingBytes {
			s.outgoingMu.Unlock()
			return errors.New("send message body budget exhausted")
		}
		s.outgoingBytes += extra
		next.charge += extra
		s.outgoingMu.Unlock()
	}
	return s.sendMessageBytes(data)
}

func (s *Session) sendMessageBytes(msg []byte) error {
	if s.manager.timeout != 0 {
		if err := s.conn.SetWriteDeadline(time.Now().Add(time.Duration(s.manager.timeout) * time.Second)); err != nil {
			return err
		}
	}
	return writePacket(s.conn, msg, s.manager.maxPacketSize)
}

func isClosedError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed)
}
