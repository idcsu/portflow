package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"portflow/internal/domain"
)

type listenFunc func(context.Context, string, string) (net.Listener, error)
type packetListenFunc func(context.Context, string, string) (net.PacketConn, error)
type dialFunc func(context.Context, string, string) (net.Conn, error)

type Options struct {
	NodeID       string
	Listen       listenFunc
	ListenPacket packetListenFunc
	Dial         dialFunc
	Logf         func(string, ...interface{})
}

type Stats struct {
	ActiveConnections   uint64
	BytesIn             uint64
	BytesOut            uint64
	Rules               []RuleStats
	Connections         []ConnectionStats
	ConnectionsComplete bool
}

const MaxConnectionSnapshots = 2000

type ConnectionStats struct {
	ID            string
	RuleID        string
	Protocol      string
	SourceAddress string
	TargetAddress string
	StartedAt     time.Time
	LastActivity  time.Time
	BytesIn       uint64
	BytesOut      uint64
}

type RuleStats struct {
	RuleID            string
	ActiveConnections uint64
	BytesIn           uint64
	BytesOut          uint64
}

type Manager struct {
	mu            sync.Mutex
	forwarders    map[string]*tcpForwarder
	udpForwarders map[string]*udpForwarder
	limiters      map[string]*bandwidthLimiter
	listen        listenFunc
	listenPacket  packetListenFunc
	dial          dialFunc
	logf          func(string, ...interface{})
	nodeID        string
	closed        bool
}

func NewManager(options Options) *Manager {
	if options.Listen == nil {
		listener := net.ListenConfig{KeepAlive: 30 * time.Second}
		options.Listen = listener.Listen
	}
	if options.ListenPacket == nil {
		listener := net.ListenConfig{}
		options.ListenPacket = listener.ListenPacket
	}
	if options.Dial == nil {
		dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		options.Dial = dialer.DialContext
	}
	if options.Logf == nil {
		options.Logf = func(string, ...interface{}) {}
	}
	return &Manager{forwarders: make(map[string]*tcpForwarder), udpForwarders: make(map[string]*udpForwarder), limiters: make(map[string]*bandwidthLimiter),
		listen: options.Listen, listenPacket: options.ListenPacket, dial: options.Dial, logf: options.Logf, nodeID: options.NodeID}
}

// Apply replaces the desired rule set transactionally where socket semantics allow it.
// All new listeners are opened before any existing listener is closed. Rules retaining
// the same ID and listen address are updated in place without dropping connections.
func (manager *Manager) Apply(ctx context.Context, rules []domain.ForwardRule) error {
	desired, err := prepareRules(rules, manager.nodeID)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return errors.New("forward manager is closed")
	}
	nextLimiters := make(map[string]*bandwidthLimiter, len(desired))
	createdLimiters := make(map[string]*bandwidthLimiter)
	for id, prepared := range desired {
		limiter := manager.limiters[id]
		if limiter == nil {
			limiter = newBandwidthLimiter(prepared.rule.BandwidthKbps)
			createdLimiters[id] = limiter
		}
		prepared.limiter = limiter
		desired[id] = prepared
		nextLimiters[id] = limiter
	}
	rollbackLimiters := func() {
		for _, limiter := range createdLimiters {
			limiter.Close()
		}
	}

	started := make(map[string]*tcpForwarder)
	for id, prepared := range desired {
		if prepared.rule.Protocol != domain.ProtocolTCP && prepared.rule.Protocol != domain.ProtocolTCPUDP {
			continue
		}
		current, exists := manager.forwarders[id]
		if exists && current.listenAddress == prepared.listenAddress {
			continue
		}
		listener, err := manager.listen(ctx, "tcp", prepared.listenAddress)
		if err != nil {
			for _, forwarder := range started {
				forwarder.close()
			}
			rollbackLimiters()
			return fmt.Errorf("start rule %s on %s: %w", id, prepared.listenAddress, err)
		}
		forwarder := newTCPForwarder(listener, prepared, manager.dial, manager.logf)
		started[id] = forwarder
	}
	startedUDP := make(map[string]*udpForwarder)
	for id, prepared := range desired {
		if prepared.rule.Protocol != domain.ProtocolUDP && prepared.rule.Protocol != domain.ProtocolTCPUDP {
			continue
		}
		current, exists := manager.udpForwarders[id]
		if exists && current.listenAddress == prepared.listenAddress {
			continue
		}
		packet, err := manager.listenPacket(ctx, "udp", prepared.listenAddress)
		if err != nil {
			for _, forwarder := range started {
				forwarder.close()
			}
			for _, forwarder := range startedUDP {
				forwarder.close()
			}
			rollbackLimiters()
			return fmt.Errorf("start UDP rule %s on %s: %w", id, prepared.listenAddress, err)
		}
		startedUDP[id] = newUDPForwarder(packet, prepared, manager.dial, manager.logf)
	}
	// Reused limiters are changed only after every new listener has opened, so a
	// failed Apply leaves both the old listeners and their old rate intact.
	for id, prepared := range desired {
		nextLimiters[id].Update(prepared.rule.BandwidthKbps)
	}

	next := make(map[string]*tcpForwarder, len(desired))
	for id, prepared := range desired {
		if prepared.rule.Protocol != domain.ProtocolTCP && prepared.rule.Protocol != domain.ProtocolTCPUDP {
			continue
		}
		if forwarder, exists := started[id]; exists {
			next[id] = forwarder
			forwarder.start()
			continue
		}
		forwarder := manager.forwarders[id]
		if !reflect.DeepEqual(forwarder.config.Load().(preparedRule).rule, prepared.rule) {
			forwarder.config.Store(prepared)
		}
		next[id] = forwarder
	}
	nextUDP := make(map[string]*udpForwarder, len(startedUDP)+len(manager.udpForwarders))
	for id, prepared := range desired {
		if prepared.rule.Protocol != domain.ProtocolUDP && prepared.rule.Protocol != domain.ProtocolTCPUDP {
			continue
		}
		if forwarder, exists := startedUDP[id]; exists {
			nextUDP[id] = forwarder
			forwarder.start()
			continue
		}
		forwarder := manager.udpForwarders[id]
		forwarder.update(prepared)
		nextUDP[id] = forwarder
	}
	for id, forwarder := range manager.forwarders {
		if next[id] != forwarder {
			forwarder.close()
		}
	}
	manager.forwarders = next
	for id, forwarder := range manager.udpForwarders {
		if nextUDP[id] != forwarder {
			forwarder.close()
		}
	}
	manager.udpForwarders = nextUDP
	for id, limiter := range manager.limiters {
		if nextLimiters[id] != limiter {
			limiter.Close()
		}
	}
	manager.limiters = nextLimiters
	return nil
}

func (manager *Manager) Stats() Stats {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := Stats{ConnectionsComplete: true}
	byRule := make(map[string]RuleStats, len(manager.forwarders)+len(manager.udpForwarders))
	ids := make([]string, 0, len(manager.forwarders)+len(manager.udpForwarders))
	for id := range manager.forwarders {
		forwarder := manager.forwarders[id]
		byRule[id] = RuleStats{RuleID: id, ActiveConnections: atomic.LoadUint64(&forwarder.active), BytesIn: atomic.LoadUint64(&forwarder.bytesIn), BytesOut: atomic.LoadUint64(&forwarder.bytesOut)}
		connections, total := forwarder.connectionStats(MaxConnectionSnapshots - len(result.Connections))
		result.Connections = append(result.Connections, connections...)
		if total > len(connections) {
			result.ConnectionsComplete = false
		}
	}
	for id, forwarder := range manager.udpForwarders {
		rule := byRule[id]
		rule.RuleID = id
		rule.ActiveConnections += atomic.LoadUint64(&forwarder.active)
		rule.BytesIn += atomic.LoadUint64(&forwarder.bytesIn)
		rule.BytesOut += atomic.LoadUint64(&forwarder.bytesOut)
		byRule[id] = rule
		connections, total := forwarder.connectionStats(MaxConnectionSnapshots - len(result.Connections))
		result.Connections = append(result.Connections, connections...)
		if total > len(connections) {
			result.ConnectionsComplete = false
		}
	}
	for id := range byRule {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rule := byRule[id]
		result.ActiveConnections += rule.ActiveConnections
		result.BytesIn += rule.BytesIn
		result.BytesOut += rule.BytesOut
		result.Rules = append(result.Rules, rule)
	}
	sort.Slice(result.Connections, func(left, right int) bool {
		if result.Connections[left].LastActivity.Equal(result.Connections[right].LastActivity) {
			return result.Connections[left].ID < result.Connections[right].ID
		}
		return result.Connections[left].LastActivity.After(result.Connections[right].LastActivity)
	})
	return result
}

func (manager *Manager) Close() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil
	}
	manager.closed = true
	for _, forwarder := range manager.forwarders {
		forwarder.close()
	}
	for _, forwarder := range manager.udpForwarders {
		forwarder.close()
	}
	for _, limiter := range manager.limiters {
		limiter.Close()
	}
	manager.forwarders = make(map[string]*tcpForwarder)
	manager.udpForwarders = make(map[string]*udpForwarder)
	manager.limiters = make(map[string]*bandwidthLimiter)
	return nil
}

type preparedRule struct {
	rule          domain.ForwardRule
	listenAddress string
	targetAddress string
	allow         []*net.IPNet
	deny          []*net.IPNet
	limiter       *bandwidthLimiter
}

func prepareRules(rules []domain.ForwardRule, nodeID string) (map[string]preparedRule, error) {
	desired := make(map[string]preparedRule)
	listeners := make(map[string]string)
	for _, rule := range rules {
		if err := rule.Validate(); err != nil {
			return nil, fmt.Errorf("validate rule %s: %w", rule.ID, err)
		}
		if !rule.Enabled {
			continue
		}
		if _, exists := desired[rule.ID]; exists {
			return nil, fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		effective := rule
		if rule.Mode == domain.ForwardRelay {
			switch nodeID {
			case rule.IngressNodeID:
				effective.TargetHost = rule.RelayHost
				effective.TargetPort = rule.RelayPort
			case rule.EgressNodeID:
				effective.ListenHost = rule.RelayHost
				effective.ListenPort = rule.RelayPort
				effective.AllowCIDRs = nil
				effective.DenyCIDRs = nil
				// Relay traffic is shaped once at ingress. Applying the same limit
				// again at egress would add latency without lowering the cap.
				effective.BandwidthKbps = 0
			default:
				return nil, fmt.Errorf("relay rule %s does not belong to node %s", rule.ID, nodeID)
			}
		}
		listenHost := strings.TrimSpace(effective.ListenHost)
		if listenHost == "" {
			listenHost = "0.0.0.0"
		}
		prepared := preparedRule{rule: effective, listenAddress: net.JoinHostPort(listenHost, fmt.Sprint(effective.ListenPort)),
			targetAddress: net.JoinHostPort(strings.TrimSpace(effective.TargetHost), fmt.Sprint(effective.TargetPort))}
		for _, cidr := range effective.AllowCIDRs {
			_, network, _ := net.ParseCIDR(cidr)
			prepared.allow = append(prepared.allow, network)
		}
		for _, cidr := range effective.DenyCIDRs {
			_, network, _ := net.ParseCIDR(cidr)
			prepared.deny = append(prepared.deny, network)
		}
		networks := []string{}
		if effective.Protocol == domain.ProtocolTCP || effective.Protocol == domain.ProtocolTCPUDP {
			networks = append(networks, "tcp")
		}
		if effective.Protocol == domain.ProtocolUDP || effective.Protocol == domain.ProtocolTCPUDP {
			networks = append(networks, "udp")
		}
		for _, network := range networks {
			key := network + "|" + prepared.listenAddress
			if other, exists := listeners[key]; exists {
				return nil, fmt.Errorf("rules %s and %s both listen on %s/%s", other, rule.ID, network, prepared.listenAddress)
			}
			listeners[key] = rule.ID
		}
		desired[rule.ID] = prepared
	}
	return desired, nil
}

type tcpForwarder struct {
	listener      net.Listener
	listenAddress string
	dial          dialFunc
	logf          func(string, ...interface{})
	config        atomic.Value
	closed        chan struct{}
	closeOnce     sync.Once
	wait          sync.WaitGroup
	connectionsMu sync.Mutex
	connections   map[net.Conn]struct{}
	sessionsMu    sync.Mutex
	sessions      map[string]*connectionRuntime
	active        uint64
	bytesIn       uint64
	bytesOut      uint64
}

func newTCPForwarder(listener net.Listener, config preparedRule, dial dialFunc, logf func(string, ...interface{})) *tcpForwarder {
	forwarder := &tcpForwarder{listener: listener, listenAddress: config.listenAddress, dial: dial, logf: logf,
		closed: make(chan struct{}), connections: make(map[net.Conn]struct{}), sessions: make(map[string]*connectionRuntime)}
	forwarder.config.Store(config)
	return forwarder
}

func (forwarder *tcpForwarder) start() {
	forwarder.wait.Add(1)
	go forwarder.acceptLoop()
}

func (forwarder *tcpForwarder) close() {
	forwarder.closeOnce.Do(func() {
		close(forwarder.closed)
		_ = forwarder.listener.Close()
		forwarder.connectionsMu.Lock()
		for connection := range forwarder.connections {
			_ = connection.Close()
		}
		forwarder.connectionsMu.Unlock()
	})
	forwarder.wait.Wait()
}

func (forwarder *tcpForwarder) acceptLoop() {
	defer forwarder.wait.Done()
	delay := time.Duration(0)
	for {
		connection, err := forwarder.listener.Accept()
		if err != nil {
			select {
			case <-forwarder.closed:
				return
			default:
			}
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else {
				delay *= 2
				if delay > time.Second {
					delay = time.Second
				}
			}
			forwarder.logf("accept failed rule=%s retry=%s error=%v", forwarder.config.Load().(preparedRule).rule.ID, delay, err)
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-forwarder.closed:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			}
			continue
		}
		delay = 0
		config := forwarder.config.Load().(preparedRule)
		if !config.permits(connection.RemoteAddr()) {
			_ = connection.Close()
			continue
		}
		active := atomic.AddUint64(&forwarder.active, 1)
		if config.rule.MaxConnections > 0 && active > uint64(config.rule.MaxConnections) {
			atomic.AddUint64(&forwarder.active, ^uint64(0))
			_ = connection.Close()
			continue
		}
		if !forwarder.track(connection) {
			atomic.AddUint64(&forwarder.active, ^uint64(0))
			continue
		}
		forwarder.wait.Add(1)
		go forwarder.handle(connection, config)
	}
}

func (config preparedRule) permits(address net.Addr) bool {
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, network := range config.deny {
		if network.Contains(ip) {
			return false
		}
	}
	if len(config.allow) == 0 {
		return true
	}
	for _, network := range config.allow {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

var bufferPool = sync.Pool{New: func() interface{} { return make([]byte, 16*1024) }}

type copyResult struct {
	bytes uint64
	in    bool
}

type connectionRuntime struct {
	id            string
	ruleID        string
	protocol      string
	sourceAddress string
	targetAddress string
	startedAt     time.Time
	lastActivity  int64
	bytesIn       uint64
	bytesOut      uint64
}

var connectionSequence uint64

func newConnectionRuntime(ruleID, protocol, sourceAddress, targetAddress string) *connectionRuntime {
	now := time.Now()
	connection := &connectionRuntime{
		id:     fmt.Sprintf("%s-%x-%x", protocol, now.UnixNano(), atomic.AddUint64(&connectionSequence, 1)),
		ruleID: ruleID, protocol: protocol, sourceAddress: sourceAddress, targetAddress: targetAddress, startedAt: now,
	}
	atomic.StoreInt64(&connection.lastActivity, now.UnixNano())
	return connection
}

func (connection *connectionRuntime) record(in bool, count int) {
	if count <= 0 {
		return
	}
	if in {
		atomic.AddUint64(&connection.bytesIn, uint64(count))
	} else {
		atomic.AddUint64(&connection.bytesOut, uint64(count))
	}
	atomic.StoreInt64(&connection.lastActivity, time.Now().UnixNano())
}

func (connection *connectionRuntime) stats() ConnectionStats {
	return ConnectionStats{
		ID: connection.id, RuleID: connection.ruleID, Protocol: connection.protocol,
		SourceAddress: connection.sourceAddress, TargetAddress: connection.targetAddress,
		StartedAt: connection.startedAt, LastActivity: time.Unix(0, atomic.LoadInt64(&connection.lastActivity)),
		BytesIn: atomic.LoadUint64(&connection.bytesIn), BytesOut: atomic.LoadUint64(&connection.bytesOut),
	}
}

func (forwarder *tcpForwarder) connectionStats(limit int) ([]ConnectionStats, int) {
	forwarder.sessionsMu.Lock()
	total := len(forwarder.sessions)
	if limit < 0 {
		limit = 0
	}
	capacity := limit
	if total < capacity {
		capacity = total
	}
	sessions := make([]*connectionRuntime, 0, capacity)
	for _, session := range forwarder.sessions {
		if len(sessions) == limit {
			break
		}
		sessions = append(sessions, session)
	}
	forwarder.sessionsMu.Unlock()
	result := make([]ConnectionStats, 0, len(sessions))
	for _, session := range sessions {
		result = append(result, session.stats())
	}
	return result, total
}

func (forwarder *tcpForwarder) track(connection net.Conn) bool {
	forwarder.connectionsMu.Lock()
	defer forwarder.connectionsMu.Unlock()
	select {
	case <-forwarder.closed:
		_ = connection.Close()
		return false
	default:
		forwarder.connections[connection] = struct{}{}
		return true
	}
}

func (forwarder *tcpForwarder) untrack(connection net.Conn) {
	forwarder.connectionsMu.Lock()
	delete(forwarder.connections, connection)
	forwarder.connectionsMu.Unlock()
}

func (forwarder *tcpForwarder) handle(client net.Conn, config preparedRule) {
	defer forwarder.wait.Done()
	defer atomic.AddUint64(&forwarder.active, ^uint64(0))
	defer forwarder.untrack(client)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	target, err := forwarder.dial(ctx, "tcp", config.targetAddress)
	cancel()
	if err != nil {
		forwarder.logf("dial failed rule=%s target=%s error=%v", config.rule.ID, config.targetAddress, err)
		return
	}
	if !forwarder.track(target) {
		return
	}
	defer forwarder.untrack(target)
	defer target.Close()
	session := newConnectionRuntime(config.rule.ID, "tcp", client.RemoteAddr().String(), config.targetAddress)
	forwarder.sessionsMu.Lock()
	forwarder.sessions[session.id] = session
	forwarder.sessionsMu.Unlock()
	defer func() {
		forwarder.sessionsMu.Lock()
		delete(forwarder.sessions, session.id)
		forwarder.sessionsMu.Unlock()
	}()

	results := make(chan copyResult, 2)
	go copyConnection(target, client, config.limiter, forwarder.closed, session, true, results)
	go copyConnection(client, target, config.limiter, forwarder.closed, session, false, results)
	for count := 0; count < 2; count++ {
		result := <-results
		if result.in {
			atomic.AddUint64(&forwarder.bytesIn, result.bytes)
		} else {
			atomic.AddUint64(&forwarder.bytesOut, result.bytes)
		}
	}
}

func copyConnection(destination, source net.Conn, limiter *bandwidthLimiter, done <-chan struct{}, session *connectionRuntime, in bool, result chan<- copyResult) {
	buffer := bufferPool.Get().([]byte)
	written, _ := io.CopyBuffer(limitedWriter{Writer: destination, limiter: limiter, done: done, session: session, in: in}, source, buffer)
	bufferPool.Put(buffer)
	if tcp, ok := destination.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
	result <- copyResult{bytes: uint64(written), in: in}
}

type limitedWriter struct {
	io.Writer
	limiter *bandwidthLimiter
	done    <-chan struct{}
	session *connectionRuntime
	in      bool
}

func (writer limitedWriter) Write(buffer []byte) (int, error) {
	written := 0
	for len(buffer) > 0 {
		chunkSize := writer.limiter.maxChunkSize(len(buffer))
		if err := writer.limiter.Wait(writer.done, chunkSize); err != nil {
			return written, err
		}
		count, err := writer.Writer.Write(buffer[:chunkSize])
		written += count
		writer.session.record(writer.in, count)
		buffer = buffer[count:]
		if err != nil {
			return written, err
		}
		if count != chunkSize {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}
