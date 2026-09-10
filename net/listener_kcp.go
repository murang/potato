package net

import (
	"github.com/murang/potato/log"
	"github.com/xtaci/kcp-go"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// server
type kcpListener struct {
	addr            string
	listener        net.Listener
	exit            atomic.Bool
	startOnce       sync.Once
	onNewConnection func(net.Conn)
}

func newKcpListener(addr string) (*kcpListener, error) {
	l, err := kcp.Listen(addr)
	if err != nil {
		log.Sugar.Errorf("listen error on %s, because: %v", addr, err)
		return nil, err
	}
	log.Sugar.Infof("kcp listen on %s", addr)
	s := &kcpListener{
		addr:     addr,
		listener: l,
	}
	return s, nil
}

func (s *kcpListener) Start() {
	s.startOnce.Do(func() {
		if !s.exit.Load() {
			go s.accept()
		}
	})
}

func (s *kcpListener) Stop() {
	if s.exit.Swap(true) {
		return
	}
	err := s.listener.Close()
	if err != nil {
		log.Sugar.Errorf("close kcp listener error: %v", err)
		return
	}
}

func (s *kcpListener) OnNewConnection(f func(net.Conn)) {
	s.onNewConnection = f
}

func (s *kcpListener) accept() {
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
			log.Sugar.Errorf("kcp.accept failed: %v", err.Error())
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

			kcpConn := conn.(*kcp.UDPSession)
			//kcpConn.SetNoDelay(0, 40, 0, 0) // normal mode
			kcpConn.SetNoDelay(1, 10, 2, 1) // turbo mode
			kcpConn.SetStreamMode(true)

			go s.onNewConnection(kcpConn)
		}
	}
}
