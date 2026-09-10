package app

import (
	"sync/atomic"
	"testing"
	"time"
)

type lifecycleModule struct {
	started, stopping, release, stopped chan struct{}
	messages                            atomic.Int32
	saved                               int32
}

func (*lifecycleModule) Name() string { return "lifecycle" }
func (*lifecycleModule) FPS() uint    { return 0 }
func (m *lifecycleModule) OnStart()   { close(m.started) }
func (*lifecycleModule) OnUpdate()    {}
func (m *lifecycleModule) OnDestroy() {
	close(m.stopping)
	<-m.release
	m.saved = m.messages.Load()
	close(m.stopped)
}
func (m *lifecycleModule) OnMsg(any)           { m.messages.Add(1) }
func (*lifecycleModule) OnRequest(msg any) any { return msg }

func await(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("lifecycle did not complete")
	}
}

func TestEndWaitsForModulesAndDrainsMessages(t *testing.T) {
	a := NewApplication()
	m := &lifecycleModule{started: make(chan struct{}), stopping: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	a.RegisterModule(m.Name(), m)
	a.Start(nil)
	await(t, m.started)
	for i := 0; i < 20; i++ {
		a.SendToModule(m.Name(), i)
	}
	callback, ended := make(chan struct{}), make(chan struct{})
	go func() { a.End(func() { close(callback) }); close(ended) }()
	await(t, m.stopping)
	select {
	case <-callback:
		t.Error("completion callback preceded module cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	close(m.release)
	await(t, ended)
	await(t, m.stopped)
	if m.saved != 20 {
		t.Errorf("only %d queued messages processed before save", m.saved)
	}
	a.End(func() { t.Error("End executed twice") })
}

func TestRPCIsOptional(t *testing.T) {
	a := NewApplication()
	defer a.End(nil)
	if a.GetCluster() != nil {
		t.Fatal("unexpected cluster")
	}
	a.BroadcastEvent("event", false)
}

func TestExitRequestIsNotShutdownCompletion(t *testing.T) {
	a := NewApplication()
	a.Exit()
	if a.waitForShutdown(time.Millisecond) {
		t.Error("exit request was reported as completed cleanup")
	}
	a.End(nil)
	if !a.waitForShutdown(time.Second) {
		t.Error("completed shutdown was not signaled")
	}
}
