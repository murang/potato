package main

import (
	"example/nicepb/nice"
	"fmt"
	"time"

	"github.com/asynkron/protoactor-go/cluster"
	"github.com/murang/potato"
	"github.com/murang/potato/log"
	"github.com/murang/potato/net"
)

type NiceModule struct {
	pending map[uint64]bool // accessed only by the module actor
}

type helloRequest struct {
	session *net.Session
	name    string
}

type helloResult struct {
	session *net.Session
	text    string
	err     error
}

func (n *NiceModule) Name() string {
	return "nice"
}

func (n *NiceModule) FPS() uint {
	return 1
}

func (n *NiceModule) OnStart() {
	n.pending = make(map[uint64]bool)
	log.Sugar.Info("start")
}

func (n *NiceModule) OnUpdate() {
	potato.BroadcastEvent(&nice.EventHello{SayHello: "niceman"}, false)
}

func (n *NiceModule) OnDestroy() {
	log.Sugar.Info("destroy")
}

func (n *NiceModule) OnMsg(msg interface{}) {
	switch msg := msg.(type) {
	case helloRequest:
		if msg.session.IsClosed() {
			return
		}
		if n.pending[msg.session.ID()] {
			// This protocol has no request ID. Closing on overlapping requests is
			// explicit; an uncorrelated Busy reply could be mistaken for success.
			msg.session.Close()
			return
		}
		c := potato.GetCluster()
		n.pending[msg.session.ID()] = true
		err := potato.TryAsyncToModule[*NiceModule](func() any {
			// Only immutable snapshots are captured; never read or write n here.
			grain := nice.GetCalculatorGrainClient(c, "NiceIdentity")
			sum, err := grain.Sum(&nice.Input{A: 6, B: 6}, cluster.WithTimeout(3*time.Second))
			result := helloResult{session: msg.session, err: err}
			if err == nil {
				if sum == nil {
					result.err = fmt.Errorf("calculator returned no result")
				} else {
					result.text = fmt.Sprintf("Nice ~  %s cal sum : %d", msg.name, sum.Result)
				}
			}
			return result
		})
		if err != nil {
			delete(n.pending, msg.session.ID())
			msg.session.Close()
		}
	case helloResult:
		delete(n.pending, msg.session.ID())
		if msg.err != nil {
			log.Sugar.Warnf("sum failed: %v", msg.err)
			msg.session.Close()
			return
		}
		msg.session.Send(&nice.S2C_Hello{SayHi: msg.text})
	}
}

func (n *NiceModule) OnRequest(msg interface{}) interface{} {
	// Synchronous module requests are reserved for short, local computations.
	return fmt.Sprintf("Nice ~ %s", msg)
}
