package app

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/asynkron/protoactor-go/actor"
)

var (
	ErrModuleNotRegistered = errors.New("module has not been registered")
	ErrNotReady            = errors.New("module is not ready")
	ErrBusy                = errors.New("module is busy")
	ErrStopping            = errors.New("module is stopping")
	ErrModuleFailed        = errors.New("module has failed")
)

// ModuleOptions limits accepted work, including the message currently executing.
// These are item limits, not a bound on the heap size of arbitrary message objects.
type ModuleOptions struct {
	QueueLimit int // <= 0 uses 1024
	MaxAsync   int // <= 0 uses 32; includes results waiting for OnMsg
}

type ModuleStats struct {
	Pending, Async int
	TickPending    bool
	Rejected       uint64
	Ready          bool
	Failure        error
}

type moduleRuntime struct {
	app      *Application
	name     string
	module   IModule
	options  ModuleOptions
	pid      *actor.PID
	interval time.Duration
	done     chan struct{}
	failed   chan struct{}
	changed  chan struct{}
	doneOnce sync.Once

	mu              sync.Mutex
	ready, stopping bool
	pending, async  int
	tickPending     bool
	rejected        uint64
	failure         error
}

func (r *moduleRuntime) notify() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *moduleRuntime) admissionError() error {
	if r.failure != nil {
		return r.failure
	}
	if r.stopping {
		return ErrStopping
	}
	if !r.ready {
		return ErrNotReady
	}
	return nil
}

func (r *moduleRuntime) reserve(async bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.admissionError()
	if err == nil && ((!async && r.pending >= r.options.QueueLimit) || (async && r.async >= r.options.MaxAsync)) {
		err = ErrBusy
	}
	if err != nil {
		r.rejected++
		return err
	}
	if async {
		r.async++
	} else {
		r.pending++
	}
	return nil
}

func (r *moduleRuntime) release(async bool) {
	r.mu.Lock()
	// A failed actor discards its remaining work and clears all logical credits.
	// Workers already running may still return, but cannot revive this instance.
	if async && r.async > 0 {
		r.async--
	} else if !async && r.pending > 0 {
		r.pending--
	}
	r.notify()
	r.mu.Unlock()
}

func (r *moduleRuntime) fail(reason any) error {
	r.mu.Lock()
	first := r.failure == nil
	if first {
		r.failure = fmt.Errorf("%w: %s: %v", ErrModuleFailed, r.name, reason)
		r.ready = false
		r.pending, r.async, r.tickPending = 0, 0, false
		close(r.failed)
	}
	err := r.failure
	r.notify()
	r.mu.Unlock()
	if first {
		r.app.fail(err)
		if r.pid != nil {
			r.app.ActorSystem.Root.Stop(r.pid)
		}
	}
	return err
}

func (r *moduleRuntime) tick() {
	r.mu.Lock()
	if r.admissionError() != nil || r.tickPending {
		r.mu.Unlock()
		return
	}
	r.tickPending = true
	r.mu.Unlock()
	r.app.ActorSystem.Root.Send(r.pid, &moduleTick{})
}

// drain runs outside the actor. Producers must already be stopped and cross-module
// work must flow toward earlier registrations. Credits cover the window between
// reservation and Send, so no framework lock needs to be held across actor code.
func (r *moduleRuntime) drain() {
	for {
		r.mu.Lock()
		if r.failure != nil || (r.pending == 0 && r.async == 0 && !r.tickPending) {
			r.stopping = true
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		<-r.changed
	}
}
