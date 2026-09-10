package net

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/murang/potato/log"
)

const maxWsBufferSize = maxPackSize + lenSize // 包体上限加四字节长度头

// server
type wsListener struct {
	addr            string
	listener        net.Listener
	server          *http.Server
	upgrade         *websocket.Upgrader
	exit            atomic.Bool
	startOnce       sync.Once
	onNewConnection func(net.Conn)
}

func newWsListener(addr string) (*wsListener, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Sugar.Errorf("listen error on %s, because: %v", addr, err)
		return nil, err
	}
	log.Sugar.Infof("ws listen on %s", addr)
	s := &wsListener{
		addr:     addr,
		listener: l,
		upgrade: &websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
	s.server = &http.Server{
		Handler: s,
	}
	return s, nil
}

func (s *wsListener) Start() {
	s.startOnce.Do(func() {
		if s.exit.Load() {
			return
		}
		go func() {
			if err := s.server.Serve(s.listener); err != nil && err != http.ErrServerClosed && !s.exit.Load() {
				log.Sugar.Errorf("ws serve error:%v", err)
			}
		}()
	})
}

func (s *wsListener) Stop() {
	if s.exit.Swap(true) {
		return
	}
	// listener 在构造时已经绑定；尚未 Serve 时 http.Server 不会替我们关闭它。
	_ = s.listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := s.server.Shutdown(ctx)
	if err != nil {
		log.Sugar.Errorf("close ws listener error: %v", err)
		return
	}
}

func (s *wsListener) OnNewConnection(f func(net.Conn)) {
	s.onNewConnection = f
}

func (s *wsListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.exit.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) { // 如果不是websocket请求 就返回
		if r.Method == http.MethodHead {
			// 健康检查逻辑
			w.WriteHeader(http.StatusOK)
		} else {
			// 其他 HTTP 请求
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if s.onNewConnection == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	conn, err := s.upgrade.Upgrade(w, r, nil)
	if err != nil {
		log.Sugar.Warnf("Error while upgrading connection:%v", err)
		return
	}

	conn.SetReadLimit(maxWsBufferSize)
	wc := &wsConn{Conn: conn}
	go s.onNewConnection(wc)
}

type wsConn struct {
	reader io.Reader
	*websocket.Conn
	mu sync.Mutex
}

// 实现Conn接口
func (w *wsConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	for {
		if w.reader == nil {
			_, reader, err := w.Conn.NextReader()
			if err != nil {
				return 0, err
			}
			w.reader = reader
		}
		n, err := w.reader.Read(b)
		if err == io.EOF {
			w.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (w *wsConn) Write(b []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	err = w.Conn.WriteMessage(websocket.BinaryMessage, b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

// 与 WriteMessage 串行设置 gorilla 的写 deadline；Close 无需该锁，可中断阻塞写入。
func (w *wsConn) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Conn.SetWriteDeadline(t)
}

func (w *wsConn) SetDeadline(t time.Time) error {
	if err := w.Conn.SetReadDeadline(t); err != nil {
		return err
	}
	return w.SetWriteDeadline(t)
}
