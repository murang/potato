package net

import (
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
	connMu          sync.Mutex
	connections     map[*wsTrackedConn]struct{}
	connectLimit    int32
	maxPacketSize   int
}

func newWsListener(addr string) (*wsListener, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Sugar.Errorf("listen error on %s, because: %v", addr, err)
		return nil, err
	}
	log.Sugar.Infof("ws listen on %s", addr)
	s := &wsListener{
		addr:          addr,
		listener:      l,
		connections:   make(map[*wsTrackedConn]struct{}),
		connectLimit:  50000,
		maxPacketSize: maxPackSize,
		upgrade: &websocket.Upgrader{
			HandshakeTimeout: 5 * time.Second,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
	s.server = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
	return s, nil
}

func (s *wsListener) Start() {
	s.startOnce.Do(func() {
		if s.exit.Load() {
			return
		}
		go func() {
			if err := s.server.Serve(&wsAcceptListener{Listener: s.listener, owner: s}); err != nil && err != http.ErrServerClosed && !s.exit.Load() {
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
	if err := s.server.Close(); err != nil {
		log.Sugar.Errorf("close ws listener error: %v", err)
	}
	// HTTP server 不再跟踪 Hijack 的连接，升级响应也可能尚未写完。
	// 原始连接从 Accept 到最终 Close 都在此登记，不靠 ConnState 交接额度。
	s.connMu.Lock()
	connections := make([]*wsTrackedConn, 0, len(s.connections))
	for conn := range s.connections {
		connections = append(connections, conn)
	}
	s.connMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
}

func (s *wsListener) OnNewConnection(f func(net.Conn)) {
	s.connMu.Lock()
	s.onNewConnection = f
	s.connMu.Unlock()
}

func (s *wsListener) configure(maxPacketSize int, connectLimit int32) {
	s.connMu.Lock()
	s.maxPacketSize = maxPacketSize
	s.connectLimit = connectLimit
	s.connMu.Unlock()
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
	s.connMu.Lock()
	onNewConnection, maxPacketSize := s.onNewConnection, s.maxPacketSize
	s.connMu.Unlock()
	if onNewConnection == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	conn, err := s.upgrade.Upgrade(w, r, nil)
	if err != nil {
		log.Sugar.Warnf("Error while upgrading connection:%v", err)
		return
	}

	if s.exit.Load() {
		_ = conn.Close()
		return
	}
	if maxPacketSize <= 0 {
		maxPacketSize = maxPackSize
	}
	conn.SetReadLimit(int64(maxPacketSize) + lenSize)
	wc := &wsConn{Conn: conn}
	go onNewConnection(wc)
}

// 同一 TCP 连接在 HTTP、升级中和正式 WS 阶段都占一个额度；
// 只限制每个 listener 的原始连接，不替代 Manager 的正式 Session 上限。
type wsAcceptListener struct {
	net.Listener
	owner *wsListener
}

func (l *wsAcceptListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		s := l.owner
		s.connMu.Lock()
		if s.exit.Load() {
			s.connMu.Unlock()
			_ = conn.Close()
			return nil, net.ErrClosed
		}
		limit := s.connectLimit
		if limit <= 0 {
			limit = 50000
		}
		if len(s.connections) >= int(limit) {
			s.connMu.Unlock()
			_ = conn.Close()
			continue
		}
		if s.connections == nil {
			s.connections = make(map[*wsTrackedConn]struct{})
		}
		tracked := &wsTrackedConn{Conn: conn, owner: s}
		s.connections[tracked] = struct{}{}
		s.connMu.Unlock()
		return tracked, nil
	}
}

type wsTrackedConn struct {
	net.Conn
	owner     *wsListener
	closeOnce sync.Once
	closeErr  error
}

func (c *wsTrackedConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
		c.owner.connMu.Lock()
		delete(c.owner.connections, c)
		c.owner.connMu.Unlock()
	})
	return c.closeErr
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
