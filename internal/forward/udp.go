package forward

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	udpIdleTimeout = 60 * time.Second
	udpReadPoll    = 10 * time.Second
)

type udpForwarder struct {
	packet        net.PacketConn
	listenAddress string
	dial          dialFunc
	logf          func(string, ...interface{})
	config        atomic.Value
	closed        chan struct{}
	closeOnce     sync.Once
	wait          sync.WaitGroup
	sessionsMu    sync.Mutex
	sessions      map[string]*udpSession
	active        uint64
	bytesIn       uint64
	bytesOut      uint64
}

type udpSession struct {
	id         string
	ruleID     string
	key        string
	client     net.Addr
	targetAddr string
	target     net.Conn
	startedAt  time.Time
	lastActive int64
	bytesIn    uint64
	bytesOut   uint64
	owner      *udpForwarder
	closeOnce  sync.Once
}

func newUDPForwarder(packet net.PacketConn, config preparedRule, dial dialFunc, logf func(string, ...interface{})) *udpForwarder {
	forwarder := &udpForwarder{packet: packet, listenAddress: config.listenAddress, dial: dial, logf: logf,
		closed: make(chan struct{}), sessions: make(map[string]*udpSession)}
	forwarder.config.Store(config)
	return forwarder
}

func (forwarder *udpForwarder) start() {
	forwarder.wait.Add(1)
	go forwarder.readLoop()
}

func (forwarder *udpForwarder) update(config preparedRule) {
	previous := forwarder.config.Load().(preparedRule)
	forwarder.config.Store(config)
	if previous.targetAddress != config.targetAddress {
		forwarder.closeSessions()
	}
}

func (forwarder *udpForwarder) close() {
	forwarder.closeOnce.Do(func() {
		close(forwarder.closed)
		_ = forwarder.packet.Close()
		forwarder.closeSessions()
	})
	forwarder.wait.Wait()
}

func (forwarder *udpForwarder) closeSessions() {
	forwarder.sessionsMu.Lock()
	sessions := make([]*udpSession, 0, len(forwarder.sessions))
	for _, session := range forwarder.sessions {
		sessions = append(sessions, session)
	}
	forwarder.sessionsMu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

func (forwarder *udpForwarder) readLoop() {
	defer forwarder.wait.Done()
	buffer := make([]byte, 64*1024)
	for {
		count, clientAddress, err := forwarder.packet.ReadFrom(buffer)
		if err != nil {
			select {
			case <-forwarder.closed:
				return
			default:
				forwarder.logf("UDP read failed rule=%s error=%v", forwarder.config.Load().(preparedRule).rule.ID, err)
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}
		config := forwarder.config.Load().(preparedRule)
		if !config.permits(clientAddress) {
			continue
		}
		session, err := forwarder.sessionFor(clientAddress, config)
		if err != nil {
			forwarder.logf("UDP target dial failed rule=%s target=%s error=%v", config.rule.ID, config.targetAddress, err)
			continue
		}
		atomic.StoreInt64(&session.lastActive, time.Now().UnixNano())
		if err := config.limiter.Wait(forwarder.closed, count); err != nil {
			return
		}
		written, err := session.target.Write(buffer[:count])
		if written > 0 {
			atomic.AddUint64(&forwarder.bytesIn, uint64(written))
			atomic.AddUint64(&session.bytesIn, uint64(written))
			atomic.StoreInt64(&session.lastActive, time.Now().UnixNano())
		}
		if err != nil {
			session.close()
		}
	}
}

func (forwarder *udpForwarder) sessionFor(clientAddress net.Addr, config preparedRule) (*udpSession, error) {
	key := clientAddress.String()
	forwarder.sessionsMu.Lock()
	if session := forwarder.sessions[key]; session != nil {
		forwarder.sessionsMu.Unlock()
		return session, nil
	}
	if config.rule.MaxConnections > 0 && len(forwarder.sessions) >= int(config.rule.MaxConnections) {
		forwarder.sessionsMu.Unlock()
		return nil, errors.New("maximum UDP sessions reached")
	}
	forwarder.sessionsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	target, err := forwarder.dial(ctx, "udp", config.targetAddress)
	cancel()
	if err != nil {
		return nil, err
	}
	runtime := newConnectionRuntime(config.rule.ID, "udp", clientAddress.String(), config.targetAddress)
	session := &udpSession{id: runtime.id, ruleID: config.rule.ID, key: key, client: cloneAddr(clientAddress), targetAddr: config.targetAddress,
		target: target, owner: forwarder, startedAt: runtime.startedAt}
	atomic.StoreInt64(&session.lastActive, runtime.startedAt.UnixNano())

	forwarder.sessionsMu.Lock()
	select {
	case <-forwarder.closed:
		forwarder.sessionsMu.Unlock()
		_ = target.Close()
		return nil, net.ErrClosed
	default:
	}
	if existing := forwarder.sessions[key]; existing != nil {
		forwarder.sessionsMu.Unlock()
		_ = target.Close()
		return existing, nil
	}
	forwarder.sessions[key] = session
	atomic.AddUint64(&forwarder.active, 1)
	forwarder.wait.Add(1)
	forwarder.sessionsMu.Unlock()
	go session.responseLoop()
	return session, nil
}

func (session *udpSession) responseLoop() {
	defer session.owner.wait.Done()
	buffer := make([]byte, 64*1024)
	for {
		_ = session.target.SetReadDeadline(time.Now().Add(udpReadPoll))
		count, err := session.target.Read(buffer)
		if count > 0 {
			config := session.owner.config.Load().(preparedRule)
			if limitErr := config.limiter.Wait(session.owner.closed, count); limitErr != nil {
				session.close()
				return
			}
			written, writeErr := session.owner.packet.WriteTo(buffer[:count], session.client)
			if written > 0 {
				atomic.AddUint64(&session.owner.bytesOut, uint64(written))
				atomic.AddUint64(&session.bytesOut, uint64(written))
				atomic.StoreInt64(&session.lastActive, time.Now().UnixNano())
			}
			if writeErr != nil {
				session.close()
				return
			}
		}
		if err == nil {
			continue
		}
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			lastActive := time.Unix(0, atomic.LoadInt64(&session.lastActive))
			if time.Since(lastActive) < udpIdleTimeout {
				continue
			}
		}
		session.close()
		return
	}
}

func (forwarder *udpForwarder) connectionStats(limit int) ([]ConnectionStats, int) {
	forwarder.sessionsMu.Lock()
	total := len(forwarder.sessions)
	if limit < 0 {
		limit = 0
	}
	capacity := limit
	if total < capacity {
		capacity = total
	}
	sessions := make([]*udpSession, 0, capacity)
	for _, session := range forwarder.sessions {
		if len(sessions) == limit {
			break
		}
		sessions = append(sessions, session)
	}
	forwarder.sessionsMu.Unlock()
	result := make([]ConnectionStats, 0, len(sessions))
	for _, session := range sessions {
		result = append(result, ConnectionStats{
			ID: session.id, RuleID: session.ruleID, Protocol: "udp", SourceAddress: session.client.String(), TargetAddress: session.targetAddr,
			StartedAt: session.startedAt, LastActivity: time.Unix(0, atomic.LoadInt64(&session.lastActive)),
			BytesIn: atomic.LoadUint64(&session.bytesIn), BytesOut: atomic.LoadUint64(&session.bytesOut),
		})
	}
	return result, total
}

func (session *udpSession) close() {
	session.closeOnce.Do(func() {
		_ = session.target.Close()
		session.owner.sessionsMu.Lock()
		if session.owner.sessions[session.key] == session {
			delete(session.owner.sessions, session.key)
			atomic.AddUint64(&session.owner.active, ^uint64(0))
		}
		session.owner.sessionsMu.Unlock()
	})
}

func cloneAddr(address net.Addr) net.Addr {
	switch value := address.(type) {
	case *net.UDPAddr:
		clone := *value
		clone.IP = append(net.IP(nil), value.IP...)
		return &clone
	default:
		return immutableAddr{network: address.Network(), value: address.String()}
	}
}

type immutableAddr struct {
	network string
	value   string
}

func (address immutableAddr) Network() string { return address.network }
func (address immutableAddr) String() string  { return address.value }
