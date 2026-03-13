package net

import (
	"github.com/murang/potato/log"
	"net"
	"sync"
	"sync/atomic"
)

type Config struct {
	SessionStartId uint64      // 会话起始id
	ConnectLimit   int32       // 连接限制
	Timeout        int32       // 超时 单位秒
	Codec          ICodec      // 消息编解码
	MsgHandler     IMsgHandler // 消息处理器
}

func defaultConfig() *Config {
	return &Config{
		ConnectLimit: 50000,
		Timeout:      30,
		Codec:        &JsonCodec{},
	}
}

type Manager struct {
	idGen            uint64
	sessionMap       sync.Map
	sessionCount     int32
	connMu           sync.Mutex
	listeners        []IListener
	codec            ICodec
	connectLimit     int32
	timeout          int32
	sessionEventChan chan *SessionEvent
	msgHandler       IMsgHandler
}

func NewManager() *Manager {
	config := defaultConfig()
	return NewManagerWithConfig(config)
}

func NewManagerWithConfig(config *Config) *Manager {
	m := &Manager{
		sessionMap:       sync.Map{},
		listeners:        make([]IListener, 0),
		sessionEventChan: make(chan *SessionEvent, 1024),
	}
	m.idGen = config.SessionStartId
	m.codec = config.Codec
	if config.Codec == nil {
		config.Codec = &JsonCodec{}
	}
	m.connectLimit = config.ConnectLimit
	if m.connectLimit <= 0 {
		m.connectLimit = 50000
	}
	m.timeout = config.Timeout
	if m.timeout <= 0 {
		m.timeout = 30
	}
	m.msgHandler = config.MsgHandler
	return m
}

func (sm *Manager) OnNewConnection(conn net.Conn) {
	if sm.connectLimit > 0 {
		sm.connMu.Lock()
		if sm.sessionCount >= sm.connectLimit {
			sm.connMu.Unlock()
			log.Sugar.Warnf("connect limit: %d", sm.connectLimit)
			_ = conn.Close()
			return
		}
		sm.sessionCount++
		sm.connMu.Unlock()
	}
	sess := sm.NewSession(conn)
	sess.Start()
}

func (sm *Manager) AddListener(ln IListener) {
	ln.OnNewConnection(sm.OnNewConnection)
	sm.listeners = append(sm.listeners, ln)
}

func (sm *Manager) SetMsgHandler(handler IMsgHandler) {
	sm.msgHandler = handler
}

func (sm *Manager) NewSession(conn net.Conn) *Session {
	atomic.AddUint64(&sm.idGen, 1)
	s := &Session{
		manager:     sm,
		id:          atomic.LoadUint64(&sm.idGen),
		conn:        conn,
		connGuard:   sync.RWMutex{},
		exitSync:    sync.WaitGroup{},
		sendChan:    make(chan any, 32),
		sendRawChan: make(chan []byte, 32),
	}
	return s
}

func (sm *Manager) Start() {
	for _, ln := range sm.listeners {
		ln.Start()
	}
	go func() {
		for ses := range sm.sessionEventChan {
			switch ses.Type {
			case SessionOpen:
				sm.sessionMap.Store(ses.Session.ID(), ses.Session)
				sm.connMu.Lock()
				sm.sessionCount++
				sm.connMu.Unlock()
				log.Sugar.Infof("session open: %d", ses.Session.ID())
				if sm.msgHandler != nil {
					sm.msgHandler.OnSessionOpen(ses.Session)
				}
			case SessionClose:
				sm.sessionMap.Delete(ses.Session.ID())
				sm.connMu.Lock()
				sm.sessionCount--
				sm.connMu.Unlock()
				log.Sugar.Infof("session close: %d", ses.Session.ID())
				if sm.msgHandler != nil {
					sm.msgHandler.OnSessionClose(ses.Session)
				}
			case SessionMsg:
				if sm.msgHandler != nil {
					sm.msgHandler.OnMsg(ses.Session, ses.Msg)
				}
			}
		}
	}()
}

func (sm *Manager) OnDestroy() {
	for _, ln := range sm.listeners {
		ln.Stop()
	}
}
