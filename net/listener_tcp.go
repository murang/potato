package net

import (
	"github.com/murang/potato/log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// server
type tcpListener struct {
	addr            string
	listener        net.Listener
	exit            atomic.Bool
	startOnce       sync.Once
	onNewConnection func(net.Conn)
}

func newTcpListener(addr string) (*tcpListener, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Sugar.Errorf("listen error on %s, because: %v", addr, err)
		return nil, err
	}
	log.Sugar.Infof("tcp listen on %s", addr)
	s := &tcpListener{
		addr:     addr,
		listener: l,
	}
	return s, nil
}

func (s *tcpListener) Start() {
	s.startOnce.Do(func() {
		if !s.exit.Load() {
			go s.accept()
		}
	})
}

func (s *tcpListener) Stop() {
	if s.exit.Swap(true) {
		return
	}
	err := s.listener.Close()
	if err != nil {
		log.Sugar.Errorf("close tcp listener error: %v", err)
		return
	}
}

func (s *tcpListener) OnNewConnection(f func(net.Conn)) {
	s.onNewConnection = f
}

func (s *tcpListener) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if nerr, ok := err.(net.Error); ok && nerr.Temporary() {
				time.Sleep(time.Millisecond)
				continue
			}
			if s.exit.Load() {
				break
			}
			// 调试状态时, 才打出accept的具体错误
			log.Sugar.Errorf("tcp.accept failed: %v", err.Error())
			break
		} else {
			if s.exit.Load() {
				_ = conn.Close()
				break
			}
			if s.onNewConnection == nil {
				_ = conn.Close()
				continue
			}
			go s.onNewConnection(conn)
		}
	}
}
