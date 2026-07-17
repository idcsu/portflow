package forward

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"portflow/internal/domain"
)

func TestTCPRelayAcrossIngressAndEgressManagers(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetDone := make(chan error, 1)
	go func() {
		connection, err := target.Accept()
		if err != nil {
			targetDone <- err
			return
		}
		defer connection.Close()
		buffer := make([]byte, 4)
		if _, err := io.ReadFull(connection, buffer); err == nil && string(buffer) == "ping" {
			_, err = connection.Write([]byte("pong"))
		}
		targetDone <- err
	}()

	ingressPort := freeTCPPort(t)
	relayPort := freeTCPPort(t)
	targetPort := uint16(target.Addr().(*net.TCPAddr).Port)
	rule := domain.ForwardRule{ID: "relay-tcp", Name: "relay TCP", Protocol: domain.ProtocolTCP, Mode: domain.ForwardRelay,
		IngressNodeID: "node-in", EgressNodeID: "node-out", ListenHost: "127.0.0.1", ListenPort: ingressPort,
		RelayHost: "127.0.0.1", RelayPort: relayPort, TargetHost: "127.0.0.1", TargetPort: targetPort, Enabled: true}
	egress := NewManager(Options{NodeID: "node-out"})
	defer egress.Close()
	ingress := NewManager(Options{NodeID: "node-in"})
	defer ingress.Close()
	if err := egress.Apply(context.Background(), []domain.ForwardRule{rule}); err != nil {
		t.Fatalf("apply egress: %v", err)
	}
	if err := ingress.Apply(context.Background(), []domain.ForwardRule{rule}); err != nil {
		t.Fatalf("apply ingress: %v", err)
	}

	client, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", ingressPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(client, reply); err != nil || string(reply) != "pong" {
		t.Fatalf("relay reply=%q err=%v", reply, err)
	}
	if err := <-targetDone; err != nil {
		t.Fatal(err)
	}
}

func TestUDPRelayAcrossIngressAndEgressManagers(t *testing.T) {
	target, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 64)
		count, address, err := target.ReadFrom(buffer)
		if err == nil {
			_, err = target.WriteTo(buffer[:count], address)
		}
		targetDone <- err
	}()

	first, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	second, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	ingressPort := uint16(first.LocalAddr().(*net.UDPAddr).Port)
	relayPort := uint16(second.LocalAddr().(*net.UDPAddr).Port)
	_ = first.Close()
	_ = second.Close()
	rule := domain.ForwardRule{ID: "relay-udp", Name: "relay UDP", Protocol: domain.ProtocolUDP, Mode: domain.ForwardRelay,
		IngressNodeID: "node-in", EgressNodeID: "node-out", ListenHost: "127.0.0.1", ListenPort: ingressPort,
		RelayHost: "127.0.0.1", RelayPort: relayPort, TargetHost: "127.0.0.1",
		TargetPort: uint16(target.LocalAddr().(*net.UDPAddr).Port), Enabled: true}
	egress := NewManager(Options{NodeID: "node-out"})
	defer egress.Close()
	ingress := NewManager(Options{NodeID: "node-in"})
	defer ingress.Close()
	if err := egress.Apply(context.Background(), []domain.ForwardRule{rule}); err != nil {
		t.Fatalf("apply egress: %v", err)
	}
	if err := ingress.Apply(context.Background(), []domain.ForwardRule{rule}); err != nil {
		t.Fatalf("apply ingress: %v", err)
	}

	client, err := net.DialTimeout("udp", fmt.Sprintf("127.0.0.1:%d", ingressPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write([]byte("datagram")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 64)
	count, err := client.Read(reply)
	if err != nil || string(reply[:count]) != "datagram" {
		t.Fatalf("relay reply=%q err=%v", reply[:count], err)
	}
	if err := <-targetDone; err != nil {
		t.Fatal(err)
	}
}

func freeTCPPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	return port
}
