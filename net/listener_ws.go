package net

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/murang/potato/log"
)

// server
type wsListener struct {
	addr            string
	listener        net.Listener
	server          *http.Server
	upgrade         *websocket.Upgrader
	exit            bool
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
	go func() {
		err := s.server.Serve(s.listener)
		if err != nil && !s.exit {
			log.Sugar.Errorf("ws serve error:%v", err)
		}
	}()
}

func (s *wsListener) Stop() {
	s.exit = true
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
	conn, err := s.upgrade.Upgrade(w, r, nil)
	if err != nil {
		log.Sugar.Warnf("Error while upgrading connection:%v", err)
		return
	}

	wc := &wsConn{Conn: conn}
	go s.onNewConnection(wc)
}

type wsConn struct {
	buffer []byte
	*websocket.Conn
	mu sync.Mutex
}

// 实现Conn接口
func (w *wsConn) Read(b []byte) (n int, err error) {
	// 先从buffer中读取
	if len(w.buffer) > 0 {
		n = copy(b, w.buffer)
		w.buffer = w.buffer[n:]
		// 写满b就返回
		if n == len(b) {
			return
		}
	}
	_, p, err := w.Conn.ReadMessage()
	if err != nil {
		log.Sugar.Warnf("ws read message error: %v", err)
		return
	}
	// 把p放入buffer
	w.buffer = append(w.buffer, p...)
	// 把buffer中的数据拷贝到b
	nn := copy(b[n:], w.buffer)
	w.buffer = w.buffer[nn:]
	n += nn
	return
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

func (w *wsConn) SetDeadline(t time.Time) (err error) {
	err = w.Conn.SetReadDeadline(t)
	if err != nil {
		return
	}
	err = w.Conn.SetWriteDeadline(t)
	return err
}
