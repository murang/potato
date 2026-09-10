package net

import (
	stdnet "net"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type testHandler struct {
	routine bool
	open    func(*Session)
	close   func(*Session)
	message func(*Session, any)
}

func TestCloseUnblocksFullSendQueue(t *testing.T) {
	for _, raw := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			s := NewManager().NewSession(nil)
			send := func() {
				if raw {
					s.SendRaw([]byte(`{}`))
				} else {
					s.Send(1)
				}
			}
			for i := 0; i < cap(s.outgoing); i++ {
				send()
			}
			done := make(chan struct{})
			go func() { send(); close(done) }()
			synctest.Wait()
			select {
			case <-done:
				t.Fatal("queue did not apply backpressure")
			default:
			}
			s.Close()
			synctest.Wait()
			select {
			case <-done:
			default:
				t.Fatal("close did not unblock sender")
			}
		})
	}
}

func TestSendAndSendRawPreserveOrderAndOwnRawBytes(t *testing.T) {
	m := NewManager()
	defer m.OnDestroy()
	a, b := stdnet.Pipe()
	defer b.Close()
	s := m.NewSession(a)
	s.Send(1)
	raw := []byte(`2`)
	s.SendRaw(raw)
	raw[0] = '9'
	s.Send(3)
	s.Start()
	for _, want := range []string{"1", "2", "3"} {
		_ = b.SetReadDeadline(time.Now().Add(time.Second))
		got, err := ReadPacket(b)
		if err != nil || string(got) != want {
			t.Fatalf("got %q/%v, want %q", got, err, want)
		}
	}
}

func TestConcurrentAdmissionAndShutdown(t *testing.T) {
	for _, routine := range []bool{false, true} {
		var opened, closed atomic.Int32
		h := &testHandler{routine: routine, open: func(*Session) { opened.Add(1) }, close: func(*Session) { closed.Add(1) }}
		m := NewManagerWithConfig(&Config{ConnectLimit: 128, MsgHandler: h})
		m.Start()
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; a, b := stdnet.Pipe(); m.OnNewConnection(a); _ = b.Close() }()
		}
		wg.Add(1)
		go func() { defer wg.Done(); <-start; m.OnDestroy() }()
		close(start)
		wg.Wait()
		m.OnDestroy()
		if opened.Load() != closed.Load() || m.sessionCount != 0 {
			t.Fatalf("open=%d close=%d active=%d", opened.Load(), closed.Load(), m.sessionCount)
		}
		select {
		case <-m.eventsDone:
		default:
			t.Error("event consumer still running")
		}
	}
}

func TestEncodeFailureClosesReaderAndSession(t *testing.T) {
	closed := make(chan struct{})
	m := NewManagerWithConfig(&Config{MsgHandler: &testHandler{close: func(*Session) { close(closed) }}})
	defer m.OnDestroy()
	a, b := stdnet.Pipe()
	defer b.Close()
	s := m.NewSession(a)
	s.Start()
	s.Send(make(chan int)) // JSON cannot encode channels.
	waitFor(t, closed)
	if !s.IsClosed() {
		t.Fatal("encode failure left the session alive")
	}
}

func TestMalformedProtobufClosesOnlyItsConnection(t *testing.T) {
	closed := make(chan struct{}, 2)
	h := &testHandler{close: func(*Session) { closed <- struct{}{} }}
	m := NewManagerWithConfig(&Config{Codec: &PbCodec{}, MsgHandler: h})
	defer m.OnDestroy()
	a, b := stdnet.Pipe()
	defer b.Close()
	s := m.NewSession(a)
	s.Start()
	other, peer := stdnet.Pipe()
	defer peer.Close()
	healthy := m.NewSession(other)
	healthy.Start()
	if err := WritePacket(b, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, closed)
	if !s.IsClosed() || healthy.IsClosed() {
		t.Fatal("malformed input did not isolate the affected session")
	}
}

func (h *testHandler) IsMsgInRoutine() bool { return h.routine }
func (h *testHandler) OnSessionOpen(s *Session) {
	if h.open != nil {
		h.open(s)
	}
}
func (h *testHandler) OnSessionClose(s *Session) {
	if h.close != nil {
		h.close(s)
	}
}
func (h *testHandler) OnMsg(s *Session, msg any) {
	if h.message != nil {
		h.message(s, msg)
	}
}

func waitFor(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for completion")
	}
}

func TestDefaultCodecDoesNotMutateConfig(t *testing.T) {
	cfg := &Config{}
	m := NewManagerWithConfig(cfg)
	if m.codec == nil {
		t.Error("default codec is nil")
	}
	if cfg.Codec != nil {
		t.Error("constructor modified caller's config")
	}
}

func TestConcurrentSessionIDsAreUnique(t *testing.T) {
	m := NewManager()
	var seen sync.Map
	var duplicates atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				if _, exists := seen.LoadOrStore(m.NewSession(nil).ID(), true); exists {
					duplicates.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if n := duplicates.Load(); n != 0 {
		t.Fatalf("%d duplicate session IDs", n)
	}
}

func TestConnectionLimitIncludesPendingOpenEvents(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h := &testHandler{open: func(*Session) { once.Do(func() { close(entered); <-release }) }}
	m := NewManagerWithConfig(&Config{ConnectLimit: 2, Codec: &JsonCodec{}, MsgHandler: h})
	m.Start()
	defer m.OnDestroy()
	for i := 0; i < 5; i++ {
		a, b := stdnet.Pipe()
		defer b.Close()
		m.OnNewConnection(a)
		if i == 0 {
			waitFor(t, entered)
		}
	}
	queued := len(m.sessionEventChan)
	close(release)
	if queued > 1 {
		t.Fatalf("limit=2 admitted 1 active + %d pending sessions", queued)
	}
}

func TestCloseFromMessageCompletesSession(t *testing.T) {
	for _, routine := range []bool{false, true} {
		closed := make(chan struct{})
		h := &testHandler{routine: routine, close: func(*Session) { close(closed) }, message: func(s *Session, _ any) { s.Close() }}
		m := NewManagerWithConfig(&Config{Codec: &JsonCodec{}, MsgHandler: h})
		m.Start()
		a, b := stdnet.Pipe()
		s := m.NewSession(a)
		s.Start()
		time.Sleep(10 * time.Millisecond)
		if err := WritePacket(b, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, closed)
		_ = b.Close()
		m.OnDestroy()
	}
}

func TestDestroyClosesSessionsAndRejectsNewConnections(t *testing.T) {
	closed := make(chan struct{})
	h := &testHandler{routine: true, close: func(*Session) { close(closed) }}
	m := NewManagerWithConfig(&Config{Codec: &JsonCodec{}, MsgHandler: h})
	a, b := stdnet.Pipe()
	defer b.Close()
	s := m.NewSession(a)
	s.Start()
	m.OnDestroy()
	if !s.IsClosed() {
		t.Error("destroy left an active session")
	}
	select {
	case <-closed:
	default:
		t.Error("destroy returned before close callback")
	}
	_ = b.Close()
	a, b = stdnet.Pipe()
	defer b.Close()
	m.OnNewConnection(a)
	_ = b.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := b.Write([]byte{1}); err == nil {
		t.Error("accepted connection after destroy")
	} else if ne, ok := err.(stdnet.Error); ok && ne.Timeout() {
		t.Error("connection after destroy was not closed")
	}
	m.OnDestroy()
}
