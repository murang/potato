package net

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/murang/potato/log"
)

type SessionEventType int32

const (
	SessionOpen SessionEventType = iota
	SessionClose
	SessionMsg
)

type Session struct {
	manager     *Manager
	id          uint64
	conn        net.Conn
	connGuard   sync.RWMutex
	exitSync    sync.WaitGroup
	sendChan    chan any
	sendRawChan chan []byte
	state       int64 //正常情况是0 主动关闭是1 出错关闭是2
}

type SessionEvent struct {
	Session *Session
	Type    SessionEventType
	Msg     interface{}
}

func (s *Session) setConn(conn net.Conn) {
	s.connGuard.Lock()
	s.conn = conn
	s.connGuard.Unlock()
}

func (s *Session) Conn() net.Conn {
	s.connGuard.RLock()
	defer s.connGuard.RUnlock()
	return s.conn
}

func (s *Session) ID() uint64 {
	return s.id
}

func (s *Session) Raw() interface{} {
	return s.Conn()
}

func (s *Session) Close() {
	if !atomic.CompareAndSwapInt64(&s.state, 0, 2) {
		return
	}
	s.connGuard.Lock()
	conn := s.conn
	s.connGuard.Unlock()
	if conn != nil {
		conn.SetDeadline(time.Now())
		conn.Close()
	}
}

func (s *Session) Send(msg interface{}) {
	if msg == nil {
		return
	}
	// 已经关闭，不再发送
	if s.IsClosed() {
		return
	}
	s.sendChan <- msg
}

func (s *Session) SendRaw(data []byte) {
	if data == nil {
		return
	}
	// 已经关闭，不再发送
	if s.IsClosed() {
		return
	}
	s.sendRawChan <- data
}

func (s *Session) IsClosed() bool {
	return atomic.LoadInt64(&s.state) != 0
}

func (s *Session) Start() {

	atomic.StoreInt64(&s.state, 0)

	// 需要接收和发送线程同时完成时才算真正的完成
	s.exitSync.Add(2)
	go func() {
		// 等待2个任务结束
		s.exitSync.Wait()
		s.Close()
		if s.manager.msgHandler != nil && s.manager.msgHandler.IsMsgInRoutine() {
			s.manager.sessionMap.Delete(s.ID())
			s.manager.connMu.Lock()
			s.manager.sessionCount--
			s.manager.connMu.Unlock()
			log.Sugar.Infof("session close: %d", s.ID())
			s.manager.msgHandler.OnSessionClose(s)
		} else {
			s.manager.sessionEventChan <- &SessionEvent{
				Session: s,
				Type:    SessionClose,
			}
		}
	}()

	if s.manager.msgHandler != nil && s.manager.msgHandler.IsMsgInRoutine() {
		s.manager.sessionMap.Store(s.ID(), s)
		s.manager.connMu.Lock()
		s.manager.sessionCount++
		s.manager.connMu.Unlock()
		log.Sugar.Infof("session open: %d", s.ID())
		s.manager.msgHandler.OnSessionOpen(s)
	} else {
		s.manager.sessionEventChan <- &SessionEvent{
			Session: s,
			Type:    SessionOpen,
		}
	}

	// 启动并发接收goroutine
	go s.readLoop()

	// 启动并发发送goroutine
	go s.writeLoop()
}

// 接收循环
func (s *Session) readLoop() {

	for !s.IsClosed() {

		var msgBytes []byte
		var err error

		msgBytes, err = s.readMessageBytes()

		if err != nil {
			var ip string
			if s.conn != nil {
				addr := s.conn.RemoteAddr()
				if addr != nil {
					ip = addr.String()
				}
			}
			if atomic.LoadInt64(&s.state) != 1 || !isClosedError(err) {
				log.Sugar.Warnf("session read err, sesid: %d, err: %s ip: %s", s.ID(), err, ip)
			}
			s.sendChan <- nil //给写队列传空 用于关闭写队列
			break
		}

		msg, err := s.manager.codec.Decode(msgBytes)
		if err != nil {
			log.Sugar.Errorf("decode msg error, sesid: %d, err: %s", s.ID(), err)
			s.sendChan <- nil //给写队列传空 用于关闭写队列
			break
		}
		if s.manager.msgHandler != nil && s.manager.msgHandler.IsMsgInRoutine() {
			s.manager.msgHandler.OnMsg(s, msg)
		} else {
			s.manager.sessionEventChan <- &SessionEvent{
				Session: s,
				Type:    SessionMsg,
				Msg:     msg,
			}
		}
	}

	// 通知完成
	s.exitSync.Done()
}

func (s *Session) readMessageBytes() (msg []byte, err error) {
	if s.manager.timeout != 0 {
		if err = s.conn.SetReadDeadline(time.Now().Add(time.Duration(s.manager.timeout) * time.Second)); err != nil {
			return
		}
	}

	reader, ok := s.Raw().(io.Reader)

	// 转换错误，或者连接已经关闭时退出
	if !ok || reader == nil {
		return nil, errors.New("reader cast error")
	}

	msg, err = ReadPacket(reader)

	if err != nil {
		return
	}

	return
}

// 发送循环
func (s *Session) writeLoop() {
loop:
	for !s.IsClosed() {
		var msgBytes []byte
		select {
		case raw := <-s.sendRawChan:
			msgBytes = raw
		case msg := <-s.sendChan:
			if msg == nil { //在读loop的时候出错 这边需要break关闭
				break loop
			}
			data, err := s.manager.codec.Encode(msg)
			if err != nil {
				log.Sugar.Errorf("encode msg error, sesid: %d, err: %s", s.ID(), err)
				break loop
			}
			msgBytes = data
		}

		if err := s.sendMessageBytes(msgBytes); err != nil {
			if atomic.LoadInt64(&s.state) != 1 || !isClosedError(err) {
				log.Sugar.Warnf("session sendLoop sendMessage err: sesid: %d, err: %s", s.ID(), err.Error())
			}
			break
		}
	}

	// 通知完成
	s.exitSync.Done()
}

func (s *Session) sendMessageBytes(msg []byte) (err error) {
	if s.manager.timeout != 0 {
		if err = s.conn.SetWriteDeadline(time.Now().Add(time.Duration(s.manager.timeout) * time.Second)); err != nil {
			return
		}
	}

	writer, ok := s.Raw().(io.Writer)

	// 转换错误，或者连接已经关闭时退出
	if !ok || writer == nil {
		return errors.New("writer cast error")
	}

	err = WritePacket(writer, msg)
	if err != nil {
		return
	}
	return
}

func (s *Session) updateDeadline() (err error) {
	if s.manager.timeout == 0 {
		err = s.Conn().SetDeadline(time.Now().Add(time.Second * 30))
	} else {
		err = s.Conn().SetDeadline(time.Now().Add(time.Second * time.Duration(s.manager.timeout)))
	}
	if err != nil {
		log.Logger.Error("session flush deadline err")
	}
	return
}

// isClosedError 判断是否是连接已关闭的错误
func isClosedError(err error) bool {
	if errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	return false
}
