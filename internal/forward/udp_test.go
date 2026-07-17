package forward

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"portflow/internal/domain"
)

type packetMessage struct {
	data []byte
	addr net.Addr
}

type fakePacketConn struct {
	incoming chan packetMessage
	outgoing chan packetMessage
	closed   chan struct{}
	once     sync.Once
}

func newFakePacketConn() *fakePacketConn {
	return &fakePacketConn{incoming: make(chan packetMessage, 2), outgoing: make(chan packetMessage, 2), closed: make(chan struct{})}
}

func (connection *fakePacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	select {
	case message := <-connection.incoming:
		return copy(buffer, message.data), message.addr, nil
	case <-connection.closed:
		return 0, nil, net.ErrClosed
	}
}
func (connection *fakePacketConn) WriteTo(buffer []byte, address net.Addr) (int, error) {
	copyOfBuffer := append([]byte(nil), buffer...)
	select {
	case connection.outgoing <- packetMessage{data: copyOfBuffer, addr: address}:
		return len(buffer), nil
	case <-connection.closed:
		return 0, net.ErrClosed
	}
}
func (connection *fakePacketConn) Close() error {
	connection.once.Do(func() { close(connection.closed) })
	return nil
}
func (connection *fakePacketConn) LocalAddr() net.Addr              { return fakeAddr("127.0.0.1:16000") }
func (connection *fakePacketConn) SetDeadline(time.Time) error      { return nil }
func (connection *fakePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (connection *fakePacketConn) SetWriteDeadline(time.Time) error { return nil }

type fakeDatagramConn struct {
	reads        chan []byte
	writes       chan []byte
	closed       chan struct{}
	once         sync.Once
	mu           sync.Mutex
	readDeadline time.Time
}

func newFakeDatagramConn() *fakeDatagramConn {
	return &fakeDatagramConn{reads: make(chan []byte, 2), writes: make(chan []byte, 2), closed: make(chan struct{})}
}

func (connection *fakeDatagramConn) Read(buffer []byte) (int, error) {
	connection.mu.Lock()
	deadline := connection.readDeadline
	connection.mu.Unlock()
	delay := time.Hour
	if !deadline.IsZero() {
		delay = time.Until(deadline)
		if delay < 0 {
			delay = 0
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case data := <-connection.reads:
		return copy(buffer, data), nil
	case <-connection.closed:
		return 0, net.ErrClosed
	case <-timer.C:
		return 0, timeoutError{}
	}
}
func (connection *fakeDatagramConn) Write(buffer []byte) (int, error) {
	data := append([]byte(nil), buffer...)
	select {
	case connection.writes <- data:
		return len(buffer), nil
	case <-connection.closed:
		return 0, net.ErrClosed
	}
}
func (connection *fakeDatagramConn) Close() error {
	connection.once.Do(func() { close(connection.closed) })
	return nil
}
func (connection *fakeDatagramConn) LocalAddr() net.Addr  { return fakeAddr("127.0.0.1:30000") }
func (connection *fakeDatagramConn) RemoteAddr() net.Addr { return fakeAddr("198.51.100.1:53") }
func (connection *fakeDatagramConn) SetDeadline(deadline time.Time) error {
	return connection.SetReadDeadline(deadline)
}
func (connection *fakeDatagramConn) SetReadDeadline(deadline time.Time) error {
	connection.mu.Lock()
	connection.readDeadline = deadline
	connection.mu.Unlock()
	return nil
}
func (connection *fakeDatagramConn) SetWriteDeadline(time.Time) error { return nil }

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestUDPForwardingAndStatsWithoutNetworkSockets(t *testing.T) {
	packet := newFakePacketConn()
	target := newFakeDatagramConn()
	manager := NewManager(Options{
		ListenPacket: func(context.Context, string, string) (net.PacketConn, error) { return packet, nil },
		Dial: func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "udp" || address != "198.51.100.1:53" {
				t.Fatalf("unexpected target %s %s", network, address)
			}
			return target, nil
		},
	})
	defer manager.Close()
	rule := testRule("udp-dns", 16000)
	rule.Protocol = domain.ProtocolUDP
	rule.TargetHost = "198.51.100.1"
	rule.TargetPort = 53
	if err := manager.Apply(context.Background(), []domain.ForwardRule{rule}); err != nil {
		t.Fatal(err)
	}
	client := &net.UDPAddr{IP: net.ParseIP("192.0.2.20"), Port: 53000}
	packet.incoming <- packetMessage{data: []byte("query"), addr: client}
	select {
	case request := <-target.writes:
		if string(request) != "query" {
			t.Fatalf("unexpected request %q", request)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP request forwarding timed out")
	}
	target.reads <- []byte("reply")
	select {
	case response := <-packet.outgoing:
		if string(response.data) != "reply" || response.addr.String() != client.String() {
			t.Fatalf("unexpected response %#v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP response forwarding timed out")
	}
	deadline := time.Now().Add(time.Second)
	for {
		stats := manager.Stats()
		if len(stats.Rules) == 1 && stats.Rules[0].BytesIn == 5 && stats.Rules[0].BytesOut == 5 && len(stats.Connections) == 1 {
			connection := stats.Connections[0]
			if !stats.ConnectionsComplete || connection.RuleID != rule.ID || connection.Protocol != "udp" ||
				connection.SourceAddress != client.String() || connection.TargetAddress != "198.51.100.1:53" ||
				connection.BytesIn != 5 || connection.BytesOut != 5 || connection.LastActivity.Before(connection.StartedAt) {
				t.Fatalf("unexpected UDP connection snapshot: %#v", connection)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("UDP stats not updated: %#v", stats)
		}
		time.Sleep(time.Millisecond)
	}
}
