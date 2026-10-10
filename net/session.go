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
	if msg != nil {
		s.enqueue(outgoingMessage{message: msg})
	}
}

// SendRaw 在返回前复制数据，调用方可以随后复用原切片。
// Send 和 SendRaw 共用可扩容的 FIFO，都不等待队列空位或网络写入。
func (s *Session) SendRaw(data []byte) {
	if data != nil && !s.IsClosed() {
		s.enqueue(outgoingMessage{raw: bytes.Clone(data), isRaw: true})
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
	if s.outgoingLen >= s.manager.sendQueueLimit {
		// Close 也会取得队列锁，必须先解锁，避免超限处理死锁。
		s.outgoingMu.Unlock()
		s.Close()
		return
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
	event := &SessionEvent{Session: s, Type: kind}
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
		data, err := s.readMessageBytes()
		if err != nil {
			if !s.IsClosed() && !isClosedError(err) {
				log.Sugar.Warnf("session read error, sesid: %d, err: %v", s.ID(), err)
			}
			return
		}
		msg, err := s.manager.codec.Decode(data)
		if err != nil {
			log.Sugar.Warnf("decode msg error, sesid: %d, err: %v", s.ID(), err)
			return
		}
		if s.IsClosed() {
			return
		}
		event := &SessionEvent{Session: s, Type: SessionMsg, Msg: msg}
		if s.inRoutine {
			s.manager.dispatch(event)
		} else {
			select {
			case <-s.done:
				return
			case s.manager.sessionEventChan <- event:
			}
		}
	}
}

func (s *Session) readMessageBytes() ([]byte, error) {
	if s.manager.timeout != 0 {
		if err := s.conn.SetReadDeadline(time.Now().Add(time.Duration(s.manager.timeout) * time.Second)); err != nil {
			return nil, err
		}
	}
	return ReadPacket(s.conn)
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
			return
		}
		data := next.raw
		if !next.isRaw {
			var err error
			data, err = s.manager.codec.Encode(next.message)
			if err != nil {
				log.Sugar.Errorf("encode msg error, sesid: %d, err: %v", s.ID(), err)
				return
			}
		}
		if err := s.sendMessageBytes(data); err != nil {
			if !s.IsClosed() && !isClosedError(err) {
				log.Sugar.Warnf("session write error, sesid: %d, err: %v", s.ID(), err)
			}
			return
		}
	}
}

func (s *Session) sendMessageBytes(msg []byte) error {
	if s.manager.timeout != 0 {
		if err := s.conn.SetWriteDeadline(time.Now().Add(time.Duration(s.manager.timeout) * time.Second)); err != nil {
			return err
		}
	}
	return WritePacket(s.conn, msg)
}

func isClosedError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed)
}
