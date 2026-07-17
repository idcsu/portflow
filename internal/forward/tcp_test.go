package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"portflow/internal/domain"
)

func testRule(id string, port uint16) domain.ForwardRule {
	return domain.ForwardRule{ID: id, Name: id, Protocol: domain.ProtocolTCP, Mode: domain.ForwardDirect,
		IngressNodeID: "node-1", ListenHost: "127.0.0.1", ListenPort: port,
		TargetHost: "127.0.0.1", TargetPort: port + 1000, Enabled: true}
}

func TestPrepareRulesSupportsUDPBandwidthAndRejectsConflicts(t *testing.T) {
	udp := testRule("udp", 12000)
	udp.Protocol = domain.ProtocolUDP
	if _, err := prepareRules([]domain.ForwardRule{udp}, ""); err != nil {
		t.Fatalf("UDP rule rejected: %v", err)
	}
	limited := testRule("limited", 12001)
	limited.BandwidthKbps = 1024
	prepared, err := prepareRules([]domain.ForwardRule{limited}, "")
	if err != nil || prepared[limited.ID].rule.BandwidthKbps != 1024 {
		t.Fatalf("bandwidth limit rejected or lost: prepared=%#v err=%v", prepared, err)
	}
	first := testRule("first", 12002)
	second := testRule("second", 12002)
	if _, err := prepareRules([]domain.ForwardRule{first, second}, ""); err == nil {
		t.Fatal("listener conflict accepted")
	}
}

func TestPreparedRuleAccessControl(t *testing.T) {
	rule := testRule("access", 13000)
	rule.AllowCIDRs = []string{"192.0.2.0/24"}
	rule.DenyCIDRs = []string{"192.0.2.10/32"}
	desired, err := prepareRules([]domain.ForwardRule{rule}, "")
	if err != nil {
		t.Fatal(err)
	}
	prepared := desired[rule.ID]
	if !prepared.permits(&net.TCPAddr{IP: net.ParseIP("192.0.2.11"), Port: 5000}) {
		t.Fatal("allowed address rejected")
	}
	if prepared.permits(&net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 5000}) {
		t.Fatal("denied address accepted")
	}
	if prepared.permits(&net.TCPAddr{IP: net.ParseIP("198.51.100.1"), Port: 5000}) {
		t.Fatal("address outside allow list accepted")
	}
}

func TestRelayBandwidthLimitAppliesOnlyAtIngress(t *testing.T) {
	rule := testRule("relay-limit", 13001)
	rule.Mode = domain.ForwardRelay
	rule.IngressNodeID = "ingress"
	rule.EgressNodeID = "egress"
	rule.RelayHost = "10.203.0.2"
	rule.RelayPort = 23001
	rule.BandwidthKbps = 2048

	ingress, err := prepareRules([]domain.ForwardRule{rule}, "ingress")
	if err != nil {
		t.Fatal(err)
	}
	egress, err := prepareRules([]domain.ForwardRule{rule}, "egress")
	if err != nil {
		t.Fatal(err)
	}
	if ingress[rule.ID].rule.BandwidthKbps != 2048 {
		t.Fatal("ingress bandwidth limit was lost")
	}
	if egress[rule.ID].rule.BandwidthKbps != 0 {
		t.Fatal("egress would apply the relay bandwidth limit a second time")
	}
}

type fakeListener struct {
	address net.Addr
	accept  chan net.Conn
	closed  chan struct{}
	once    sync.Once
}

func newFakeListener(address string) *fakeListener {
	return &fakeListener{address: fakeAddr(address), accept: make(chan net.Conn), closed: make(chan struct{})}
}

func (listener *fakeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.accept:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}
func (listener *fakeListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}
func (listener *fakeListener) Addr() net.Addr { return listener.address }

type fakeAddr string

func (address fakeAddr) Network() string { return "tcp" }
func (address fakeAddr) String() string  { return string(address) }

func TestManagerKeepsOldConfigurationWhenNewListenerFails(t *testing.T) {
	listeners := map[string]*fakeListener{}
	manager := NewManager(Options{Listen: func(_ context.Context, _, address string) (net.Listener, error) {
		if address == "127.0.0.1:14001" {
			return nil, errors.New("address unavailable")
		}
		listener := newFakeListener(address)
		listeners[address] = listener
		return listener, nil
	}})
	defer manager.Close()
	first := testRule("first", 14000)
	first.BandwidthKbps = 512
	if err := manager.Apply(context.Background(), []domain.ForwardRule{first}); err != nil {
		t.Fatal(err)
	}
	limiter := manager.limiters[first.ID]
	updatedFirst := first
	updatedFirst.BandwidthKbps = 2048
	if err := manager.Apply(context.Background(), []domain.ForwardRule{updatedFirst, testRule("second", 14001)}); err == nil {
		t.Fatal("listener failure was ignored")
	}
	if len(manager.forwarders) != 1 || manager.forwarders[first.ID] == nil {
		t.Fatal("working configuration was not retained")
	}
	select {
	case <-listeners["127.0.0.1:14000"].closed:
		t.Fatal("existing listener was closed")
	default:
	}
	if manager.limiters[first.ID] != limiter || limiter.rateKbps() != 512 {
		t.Fatalf("failed Apply changed active bandwidth limit: limiter=%p rate=%d", manager.limiters[first.ID], limiter.rateKbps())
	}
}

func TestManagerRollsBackNewTCPListenerWhenPairedUDPListenerFails(t *testing.T) {
	tcpListener := newFakeListener("127.0.0.1:14002")
	manager := NewManager(Options{
		Listen: func(context.Context, string, string) (net.Listener, error) {
			return tcpListener, nil
		},
		ListenPacket: func(context.Context, string, string) (net.PacketConn, error) {
			return nil, errors.New("UDP address unavailable")
		},
	})
	defer manager.Close()
	rule := testRule("paired", 14002)
	rule.Protocol = domain.ProtocolTCPUDP
	if err := manager.Apply(context.Background(), []domain.ForwardRule{rule}); err == nil {
		t.Fatal("UDP listener failure was ignored")
	}
	select {
	case <-tcpListener.closed:
	case <-time.After(time.Second):
		t.Fatal("new TCP listener was not rolled back")
	}
	if len(manager.forwarders) != 0 || len(manager.udpForwarders) != 0 {
		t.Fatal("partially applied TCP+UDP rule remained active")
	}
}

type addressedConn struct {
	net.Conn
	remote net.Addr
}

func (connection addressedConn) RemoteAddr() net.Addr { return connection.remote }

func TestTCPForwardingAndStatsWithoutNetworkSockets(t *testing.T) {
	listener := newFakeListener("127.0.0.1:15000")
	targetAgent, targetServer := net.Pipe()
	manager := NewManager(Options{
		Listen: func(context.Context, string, string) (net.Listener, error) { return listener, nil },
		Dial:   func(context.Context, string, string) (net.Conn, error) { return targetAgent, nil },
	})
	defer manager.Close()
	if err := manager.Apply(context.Background(), []domain.ForwardRule{testRule("copy", 15000)}); err != nil {
		t.Fatal(err)
	}
	client, accepted := net.Pipe()
	listener.accept <- addressedConn{Conn: accepted, remote: &net.TCPAddr{IP: net.ParseIP("192.0.2.25"), Port: 5000}}

	done := make(chan error, 1)
	go func() {
		if _, err := client.Write([]byte("hello")); err != nil {
			done <- err
			return
		}
		buffer := make([]byte, 5)
		if _, err := io.ReadFull(client, buffer); err != nil {
			done <- err
			return
		}
		if string(buffer) != "world" {
			done <- errors.New("unexpected reply")
			return
		}
		done <- nil
	}()
	buffer := make([]byte, 5)
	if _, err := io.ReadFull(targetServer, buffer); err != nil || string(buffer) != "hello" {
		t.Fatalf("forwarded request=%q err=%v", buffer, err)
	}
	if _, err := targetServer.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("forwarding timed out")
	}
	var stats Stats
	statsDeadline := time.Now().Add(time.Second)
	for {
		stats = manager.Stats()
		if len(stats.Connections) == 1 && stats.Connections[0].BytesIn == 5 && stats.Connections[0].BytesOut == 5 {
			break
		}
		if time.Now().After(statsDeadline) {
			t.Fatalf("TCP connection bytes were not updated: %#v", stats)
		}
		time.Sleep(time.Millisecond)
	}
	if !stats.ConnectionsComplete || len(stats.Connections) != 1 {
		t.Fatalf("unexpected TCP connection snapshots: %#v", stats)
	}
	connection := stats.Connections[0]
	if connection.RuleID != "copy" || connection.Protocol != "tcp" || connection.SourceAddress != "192.0.2.25:5000" ||
		connection.TargetAddress != "127.0.0.1:16000" || connection.BytesIn != 5 || connection.BytesOut != 5 || connection.StartedAt.IsZero() || connection.LastActivity.Before(connection.StartedAt) {
		t.Fatalf("unexpected TCP connection snapshot: %#v", connection)
	}
	_ = client.Close()
	_ = targetServer.Close()
	deadline := time.Now().Add(time.Second)
	for len(manager.Stats().Connections) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("closed TCP connection remained in snapshots")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConnectionSnapshotsAreBounded(t *testing.T) {
	manager := NewManager(Options{})
	forwarder := &tcpForwarder{sessions: make(map[string]*connectionRuntime)}
	for index := 0; index < MaxConnectionSnapshots+1; index++ {
		connection := newConnectionRuntime("rule-many", "tcp", fmt.Sprintf("192.0.2.1:%d", index), "198.51.100.1:443")
		forwarder.sessions[connection.id] = connection
	}
	manager.forwarders["rule-many"] = forwarder
	stats := manager.Stats()
	if stats.ConnectionsComplete || len(stats.Connections) != MaxConnectionSnapshots {
		t.Fatalf("connection snapshot was not bounded: complete=%v count=%d", stats.ConnectionsComplete, len(stats.Connections))
	}
}
