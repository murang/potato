package net

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type countingConn struct {
	stdnet.Conn
	bytesRead atomic.Int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.bytesRead.Add(int64(n))
	return n, err
}

type upgradeWriter struct {
	conn    stdnet.Conn
	rw      *bufio.ReadWriter
	headers http.Header
}

func (w *upgradeWriter) Header() http.Header         { return w.headers }
func (*upgradeWriter) WriteHeader(int)               {}
func (w *upgradeWriter) Write(b []byte) (int, error) { return w.rw.Write(b) }
func (w *upgradeWriter) Hijack() (stdnet.Conn, *bufio.ReadWriter, error) {
	return w.conn, w.rw, nil
}

// Exercise the production upgrade handler entirely through net.Pipe.
func newWSPair(t *testing.T) (*wsConn, *websocket.Conn, *countingConn) {
	t.Helper()
	a, b := stdnet.Pipe()
	counted := &countingConn{Conn: a}
	connections := make(chan stdnet.Conn, 1)
	serverErrors := make(chan error, 1)
	l := &wsListener{upgrade: &websocket.Upgrader{}, onNewConnection: func(c stdnet.Conn) { connections <- c }}
	go func() {
		rw := bufio.NewReadWriter(bufio.NewReader(counted), bufio.NewWriter(counted))
		r, err := http.ReadRequest(rw.Reader)
		if err != nil {
			serverErrors <- err
			return
		}
		l.ServeHTTP(&upgradeWriter{conn: counted, rw: rw, headers: make(http.Header)}, r)
	}()
	u, _ := url.Parse("ws://review.invalid/socket")
	client, _, err := websocket.NewClient(b, u, nil, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	select {
	case c := <-connections:
		return c.(*wsConn), client, counted
	case err := <-serverErrors:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("in-memory websocket upgrade timed out")
	}
	return nil, nil, nil
}

func TestWSLimitBeforeReadingEntireMessage(t *testing.T) {
	server, client, counted := newWSPair(t)
	before := counted.bytesRead.Load()
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- client.WriteMessage(websocket.BinaryMessage, bytes.Repeat([]byte{1}, 2*maxWsBufferSize))
	}()
	_, err := io.Copy(io.Discard, server)
	if !errors.Is(err, websocket.ErrReadLimit) {
		t.Fatalf("expected read limit, got %v", err)
	}
	if n := counted.bytesRead.Load() - before; n > maxWsBufferSize+16384 {
		t.Errorf("1 MiB limit checked only after consuming %d wire bytes", n)
	}
	_ = server.Close()
	<-done
}

func TestWSAcceptsMaximumPacketBody(t *testing.T) {
	server, client, _ := newWSPair(t)
	done := make(chan error, 1)
	var packet bytes.Buffer
	if err := WritePacket(&packet, make([]byte, maxPackSize)); err != nil {
		t.Fatal(err)
	}
	go func() { done <- client.WriteMessage(websocket.BinaryMessage, packet.Bytes()) }()
	if _, err := ReadPacket(server); err != nil {
		t.Errorf("valid maximum body rejected by websocket adapter: %v", err)
	}
	_ = server.Close()
	<-done
}

func TestWSDeadlineConcurrentWithWrite(t *testing.T) {
	server, client, _ := newWSPair(t)
	start := make(chan struct{})
	done := make(chan struct{})
	go func() {
		<-start
		for i := 0; i < 100; i++ {
			_ = server.SetDeadline(time.Now().Add(time.Second))
		}
		close(done)
	}()
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()
	close(start)
	for i := 0; i < 100; i++ {
		if _, err := server.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	_ = server.Close()
}

var _ http.Hijacker = (*upgradeWriter)(nil)
var _ io.ReadWriter = (*wsConn)(nil)

func TestWSCloseInterruptsBlockedWriter(t *testing.T) {
	server, _, _ := newWSPair(t)
	started, done := make(chan struct{}), make(chan struct{})
	go func() { close(started); _, _ = server.Write([]byte("pending")); close(done) }()
	waitFor(t, started)
	_ = server.Close()
	waitFor(t, done)
}

type closeTrackingListener struct{ closes atomic.Int32 }

func (*closeTrackingListener) Accept() (stdnet.Conn, error) { return nil, stdnet.ErrClosed }
func (*closeTrackingListener) Addr() stdnet.Addr            { return &stdnet.TCPAddr{} }
func (l *closeTrackingListener) Close() error               { l.closes.Add(1); return nil }

func TestManagerDestroyClosesUnstartedWSListener(t *testing.T) {
	bound := &closeTrackingListener{}
	ln := &wsListener{listener: bound, server: &http.Server{}}
	m := NewManager()
	m.AddListener(ln)
	m.OnDestroy()
	m.OnDestroy()
	ln.Start()
	if got := bound.closes.Load(); got != 1 {
		t.Fatalf("unstarted listener closed %d times, want 1", got)
	}
}
