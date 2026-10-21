package app

import (
	"context"
	"fmt"
	"github.com/asynkron/protoactor-go/actor"
	"github.com/murang/potato/log"
	"time"
)

// Public shapes remain compatible. Direct ActorSystem sends bypass admission;
// application code should use Application's message methods.
type ModuleUpdate struct{}
type ModuleOnMsg struct{ Msg interface{} }
type ModuleOnRequest struct{ Request interface{} }

type moduleInit struct{ done chan error }
type moduleMessage struct{ message any }
type moduleCompletion struct{ message any }
type moduleTick struct{}
type moduleRequest struct {
	ctx     context.Context
	message any
	reply   chan moduleReply
}
type moduleReply struct {
	value any
	err   error
}
type moduleActor struct {
	runtime *moduleRuntime
	started bool
}

// Recover only at the module boundary, fail the instance and stop it. Returning
// from a panicking OnDestroy lets protoactor reach Stopped instead of stranding
// lifecycle waiters; it does not resume business on potentially corrupt state.
func (m *moduleActor) invoke(fn func()) (err error) {
	defer func() {
		if reason := recover(); reason != nil {
			log.Sugar.Errorf("module %s callback panic: %v", m.runtime.name, reason)
			err = m.runtime.fail(reason)
		}
	}()
	fn()
	return nil
}
func (m *moduleActor) Receive(ctx actor.Context) {
	r := m.runtime
	// A worker can fail the module concurrently. Do not begin another business
	// callback while the actor's stop system message is on its way to the mailbox.
	switch ctx.Message().(type) {
	case *actor.Stopping, *actor.Stopped, *actor.Started:
	default:
		r.mu.Lock()
		failed := r.failure != nil
		r.mu.Unlock()
		if failed {
			return
		}
	}
	switch msg := ctx.Message().(type) {
	case *actor.Started:
		// All PID registrations must finish before business initialization.
	case *moduleInit:
		m.started = true
		err := m.invoke(func() {
			r.module.OnStart()
			fps := r.module.FPS()
			if fps > uint(time.Second) {
				panic(fmt.Errorf("module FPS %d produces a non-positive interval", fps))
			}
			if fps > 0 {
				r.interval = time.Second / time.Duration(fps)
			}
		})
		r.mu.Lock()
		if err == nil && r.failure == nil {
			r.ready = true
		}
		r.mu.Unlock()
		msg.done <- err
	case *actor.Stopping:
		if m.started {
			_ = m.invoke(r.module.OnDestroy)
		}
	case *actor.Stopped:
		r.doneOnce.Do(func() { close(r.done) })
	case *moduleMessage:
		defer r.release(false)
		_ = m.invoke(func() { r.module.OnMsg(msg.message) })
	case *moduleCompletion:
		defer r.release(true)
		_ = m.invoke(func() { r.module.OnMsg(msg.message) })
	case *moduleTick:
		defer func() { r.mu.Lock(); r.tickPending = false; r.notify(); r.mu.Unlock() }()
		_ = m.invoke(r.module.OnUpdate)
	case *moduleRequest:
		defer r.release(false)
		response := moduleReply{err: msg.ctx.Err()}
		if response.err == nil {
			response.err = m.invoke(func() { response.value = r.module.OnRequest(msg.message) })
		}
		msg.reply <- response // buffered: caller cancellation cannot block the actor
	case *ModuleUpdate:
		_ = m.invoke(r.module.OnUpdate)
	case *ModuleOnMsg:
		_ = m.invoke(func() { r.module.OnMsg(msg.Msg) })
	case *ModuleOnRequest:
		var response any
		if err := m.invoke(func() { response = r.module.OnRequest(msg.Request) }); err != nil {
			ctx.Respond(err)
		} else {
			ctx.Respond(response)
		}
	}
}
