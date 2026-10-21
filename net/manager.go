package net

import (
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/murang/potato/log"
)

type Config struct {
	SessionStartId       uint64        // 会话起始 ID
	ConnectLimit         int32         // Manager 会话总数上限，同时作为每个 WS listener 的原始 TCP 连接上限
	Timeout              int32         // 超时，单位秒
	Codec                ICodec        // 未设置时使用 JSON
	MsgHandler           IMsgHandler   // 消息处理器
	SendQueueLimit       int           // 排队消息上限，不含正在写入的消息；<=0 使用 4096
	SendQueueIdleTimeout time.Duration // 扩容队列的空闲缩容时间；<=0 使用 30 秒
	SendQueueBytesLimit  int64         // 排队及 writer 的消息体计费预算；<=0 不启用，不代表对象堆上限
	ReceiveBytesLimit    int64         // 等待包体、解码及网络回调的包体预算；<=0 使用 64 MiB
	MaxPacketSize        int           // 包体上限；<=0 或超过长度头/平台 int 可表示范围使用 1 MiB
}

func defaultConfig() *Config {
	return &Config{
		ConnectLimit:         50000,
		Timeout:              30,
		Codec:                &JsonCodec{},
		SendQueueLimit:       defaultSendQueueLimit,
		SendQueueIdleTimeout: defaultSendQueueIdleTimeout,
	}
}

type Manager struct {
	idGen                uint64
	sessionMap           sync.Map
	sessionCount         int32
	connMu               sync.Mutex // 保护准入、计数、监听器和处理器配置
	listeners            []IListener
	codec                ICodec
	connectLimit         int32
	timeout              int32
	sendQueueLimit       int
	sendQueueIdleTimeout time.Duration
	sendQueueBytesLimit  int64
	maxPacketSize        int
	receiveBudget        receiveBudget
	sessionEventChan     chan *queuedSessionEvent
	msgHandler           IMsgHandler
	started              bool
	stopping             bool
	startOnce            sync.Once
	stopOnce             sync.Once
	eventOnce            sync.Once
	eventsDone           chan struct{}
	sessions             sync.WaitGroup
}

func NewManager() *Manager { return NewManagerWithConfig(defaultConfig()) }

func NewManagerWithConfig(config *Config) *Manager {
	if config == nil {
		config = defaultConfig()
	}
	m := &Manager{
		idGen:                config.SessionStartId,
		codec:                config.Codec,
		connectLimit:         config.ConnectLimit,
		timeout:              config.Timeout,
		msgHandler:           config.MsgHandler,
		sendQueueLimit:       config.SendQueueLimit,
		sendQueueIdleTimeout: config.SendQueueIdleTimeout,
		sendQueueBytesLimit:  config.SendQueueBytesLimit,
		maxPacketSize:        config.MaxPacketSize,
		receiveBudget:        receiveBudget{limit: config.ReceiveBytesLimit},
		sessionEventChan:     make(chan *queuedSessionEvent, 1024),
		eventsDone:           make(chan struct{}),
	}
	if m.codec == nil {
		m.codec = &JsonCodec{}
	}
	if m.connectLimit <= 0 {
		m.connectLimit = 50000
	}
	if m.timeout <= 0 {
		m.timeout = 30
	}
	if m.sendQueueLimit <= 0 {
		m.sendQueueLimit = defaultSendQueueLimit
	}
	if m.sendQueueIdleTimeout <= 0 {
		m.sendQueueIdleTimeout = defaultSendQueueIdleTimeout
	}
	if m.maxPacketSize <= 0 || uint64(m.maxPacketSize) > math.MaxUint32 || m.maxPacketSize > math.MaxInt-lenSize {
		m.maxPacketSize = maxPackSize
	}
	if m.receiveBudget.limit <= 0 {
		m.receiveBudget.limit = 64 * 1024 * 1024
	}
	return m
}

func (sm *Manager) OnNewConnection(conn net.Conn) { sm.NewSession(conn).Start() }

func (sm *Manager) AddListener(ln IListener) {
	if ws, ok := ln.(*wsListener); ok {
		ws.configure(sm.maxPacketSize, sm.connectLimit)
	}
	ln.OnNewConnection(sm.OnNewConnection)
	sm.connMu.Lock()
	if sm.stopping {
		sm.connMu.Unlock()
		ln.Stop()
		return
	}
	sm.listeners = append(sm.listeners, ln)
	started := sm.started
	sm.connMu.Unlock()
	if started {
		ln.Start()
	}
}

// SetMsgHandler 设置新会话使用的处理器；已有会话保留接入时的处理器及并发模式。
func (sm *Manager) SetMsgHandler(handler IMsgHandler) {
	sm.connMu.Lock()
	sm.msgHandler = handler
	sm.connMu.Unlock()
}

func (sm *Manager) NewSession(conn net.Conn) *Session {
	s := &Session{
		manager:      sm,
		id:           atomic.AddUint64(&sm.idGen, 1),
		conn:         conn,
		outgoing:     make([]outgoingMessage, min(initialOutgoingCapacity, sm.sendQueueLimit)),
		outgoingWake: make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
	return s
}

// admit 在启动任何会话回调前预留名额。关闭过程与 WaitGroup.Add 使用同一把锁。
func (sm *Manager) admit(s *Session) bool {
	sm.connMu.Lock()
	if sm.stopping {
		sm.connMu.Unlock()
		return false
	}
	if sm.sessionCount >= sm.connectLimit {
		sm.connMu.Unlock()
		log.Sugar.Errorf("connect limit: %d", sm.connectLimit)
		return false
	}
	sm.startEvents()
	sm.sessionCount++
	sm.sessions.Add(1)
	sm.sessionMap.Store(s.ID(), s)
	s.handler = sm.msgHandler
	sm.connMu.Unlock()
	return true
}

func (sm *Manager) remove(s *Session) {
	sm.connMu.Lock()
	sm.sessionMap.Delete(s.ID())
	sm.sessionCount--
	sm.connMu.Unlock()
}

func (sm *Manager) startEvents() {
	sm.eventOnce.Do(func() {
		go func() {
			defer close(sm.eventsDone)
			for event := range sm.sessionEventChan {
				sm.dispatch(event)
			}
		}()
	})
}

func (sm *Manager) dispatch(event *queuedSessionEvent) {
	// 一旦成功投递，只有消费者拥有额度；Session.Close 不可提前归还
	// 尚在排队或执行的事件，否则关闭连接就能绕过在途消息预算。
	defer sm.receiveBudget.release(event.receiveBytes)
	h := event.Session.handler
	switch event.Type {
	case SessionOpen:
		log.Sugar.Infof("session open: %d", event.Session.ID())
		if h != nil {
			h.OnSessionOpen(event.Session)
		}
	case SessionClose:
		log.Sugar.Infof("session close: %d", event.Session.ID())
		if h != nil {
			h.OnSessionClose(event.Session)
		}
	case SessionMsg:
		if h != nil {
			h.OnMsg(event.Session, event.Msg)
		}
	}
}

func (sm *Manager) Start() {
	sm.startOnce.Do(func() {
		sm.connMu.Lock()
		if sm.stopping {
			sm.connMu.Unlock()
			return
		}
		sm.started = true
		sm.startEvents()
		listeners := append([]IListener(nil), sm.listeners...)
		sm.connMu.Unlock()
		for _, ln := range listeners {
			ln.Start()
		}
	})
}

// OnDestroy 停止接入，关闭会话并等待所有已接受的生命周期事件处理完成。
// 应从生命周期管理协程调用，不能在本管理器的消息回调中同步等待自身退出。
func (sm *Manager) OnDestroy() {
	sm.stopOnce.Do(func() {
		sm.connMu.Lock()
		sm.stopping = true
		sm.startEvents()
		listeners := append([]IListener(nil), sm.listeners...)
		sm.connMu.Unlock()
		for _, ln := range listeners {
			ln.Stop()
		}
		sm.sessionMap.Range(func(_, value any) bool {
			value.(*Session).Close()
			return true
		})
		sm.sessions.Wait()
		close(sm.sessionEventChan)
		<-sm.eventsDone
	})
}
