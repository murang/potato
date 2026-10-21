package net

import (
	"github.com/murang/potato/internal/kcp"
	"github.com/murang/potato/log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// server
type kcpListener struct {
	addr            string
	listener        *kcp.Listener
	exit            atomic.Bool
	startOnce       sync.Once
	onNewConnection func(net.Conn)
}

func newKcpListener(addr string) (*kcpListener, error) {
	l, err := kcp.ListenWithOptions(addr, nil, 0, 0)
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
	// 包括尚未 Accept 的 KCP 会话；它们还没有进入 Manager，必须由监听器
	// 清理。完整关闭等待最后一个 UDP 包处理结束，再移除全局 updater 引用。
	err := s.listener.Shutdown()
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
