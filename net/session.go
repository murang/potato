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

type Session struct {
	manager   *Manager
	id        uint64
	conn      net.Conn // 创建后不再替换，可安全并发读写和关闭
	handler   IMsgHandler
	inRoutine bool
	outgoing  chan outgoingMessage
	done      chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	exitSync  sync.WaitGroup
}

type SessionEvent struct {
	Session *Session
	Type    SessionEventType
	Msg     interface{}
}

func (s *Session) Conn() net.Conn   { return s.conn }
func (s *Session) ID() uint64       { return s.id }
func (s *Session) Raw() interface{} { return s.Conn() }

// Close 取消排队操作并立即中断连接 I/O，不等待业务回调返回。
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
}

func (s *Session) Send(msg interface{}) {
	if msg != nil {
		s.enqueue(outgoingMessage{message: msg})
	}
}

// SendRaw 在返回前复制数据，调用方可以随后复用原切片。
// Send 和 SendRaw 共用 FIFO；连接关闭时正在等待入队的调用会返回。
func (s *Session) SendRaw(data []byte) {
	if data != nil && !s.IsClosed() {
		s.enqueue(outgoingMessage{raw: bytes.Clone(data), isRaw: true})
	}
}

func (s *Session) enqueue(msg outgoingMessage) {
	if s.IsClosed() {
		return
	}
	select {
	case <-s.done:
	case s.outgoing <- msg:
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
		// 先启动 writer，允许 OnSessionOpen 发送超过一个队列容量的数据。
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
	for {
		var next outgoingMessage
		select {
		case <-s.done:
			return
		case next = <-s.outgoing:
		}
		if s.IsClosed() {
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
