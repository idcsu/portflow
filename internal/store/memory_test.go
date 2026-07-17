package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"portflow/internal/domain"
)

func TestMemoryListenerConflictsAcrossCombinedProtocol(t *testing.T) {
	memory := NewMemory()
	memory.nodes["node-1"] = domain.Node{ID: "node-1", Status: domain.NodeOnline, ConfigVersion: 1}
	memory.agentConfigs["node-1"] = AgentConfig{Version: 1}

	base := domain.ForwardRule{ID: "tcp", Name: "TCP", Protocol: domain.ProtocolTCP, Mode: domain.ForwardDirect,
		IngressNodeID: "node-1", ListenHost: "0.0.0.0", ListenPort: 20000,
		TargetHost: "198.51.100.1", TargetPort: 80, Enabled: true}
	if _, err := memory.CreateForwardRule(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	udp := base
	udp.ID, udp.Name, udp.Protocol = "udp", "UDP", domain.ProtocolUDP
	if _, err := memory.CreateForwardRule(context.Background(), udp); err != nil {
		t.Fatalf("separate UDP listener on the same port should be allowed: %v", err)
	}
	combined := base
	combined.ID, combined.Name, combined.Protocol = "combined", "Combined", domain.ProtocolTCPUDP
	if _, err := memory.CreateForwardRule(context.Background(), combined); !errors.Is(err, ErrConflict) {
		t.Fatalf("TCP+UDP overlap error=%v, want ErrConflict", err)
	}
}

func TestMemoryHeartbeatClearsOmittedActiveRuleStats(t *testing.T) {
	memory := NewMemory()
	memory.nodes["node-1"] = domain.Node{ID: "node-1", Status: domain.NodeOnline, ConfigVersion: 2}
	memory.agentCredentials["node-1"] = "credential"
	memory.forwardRules["rule-1"] = domain.ForwardRule{ID: "rule-1", IngressNodeID: "node-1", ActiveConns: 4, BytesIn: 10}
	now := time.Now().UTC()
	_, err := memory.UpdateAgentHeartbeat(context.Background(), "node-1", "credential", AgentHeartbeat{
		ConfigVersion: 2, AttemptedConfigVersion: 99, ReceivedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	rule := memory.forwardRules["rule-1"]
	if rule.ActiveConns != 0 || rule.BytesIn != 10 || !rule.RuntimeUpdated.Equal(now) {
		t.Fatalf("unexpected cleared rule stats: %#v", rule)
	}
	if memory.nodes["node-1"].AttemptedConfigVersion != 2 {
		t.Fatalf("attempted config version was not clamped: %#v", memory.nodes["node-1"])
	}
}

func TestMemoryUserManagementPreservesAdministratorAndRevokesSessions(t *testing.T) {
	memory := NewMemory()
	now := time.Now().UTC()
	administrator := User{ID: "admin-1", Username: "admin", PasswordHash: "hash", Role: RoleAdmin, CreatedAt: now}
	if err := memory.CreateInitialUser(context.Background(), administrator); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.UpdateUser(context.Background(), administrator.ID, UserUpdate{Role: RoleMember}); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("last administrator update error=%v, want ErrLastAdministrator", err)
	}
	member := User{ID: "member-1", Username: "member", PasswordHash: "old-hash", Role: RoleMember, CreatedAt: now.Add(time.Second)}
	if err := memory.CreateUser(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if err := memory.CreateUser(context.Background(), User{ID: "member-2", Username: member.Username}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate username error=%v, want ErrConflict", err)
	}
	memory.sessions["member-session"] = Session{ID: "session-1", UserID: member.ID, TokenHash: "member-session", ExpiresAt: now.Add(time.Hour)}
	updated, err := memory.UpdateUser(context.Background(), member.ID, UserUpdate{Role: RoleAdmin, Disabled: false, PasswordHash: "new-hash"})
	if err != nil || updated.Role != RoleAdmin || updated.PasswordHash != "new-hash" {
		t.Fatalf("unexpected user update: %#v err=%v", updated, err)
	}
	if _, _, err := memory.SessionUserByTokenHash(context.Background(), "member-session", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("updated user session was not revoked: %v", err)
	}
	users, err := memory.ListUsers(context.Background())
	if err != nil || len(users) != 2 || users[0].ID != administrator.ID || users[1].ID != member.ID {
		t.Fatalf("unexpected users: %#v err=%v", users, err)
	}
}

func TestMemoryActiveConnectionSnapshotsReplaceOnlyWhenComplete(t *testing.T) {
	memory := NewMemory()
	memory.nodes["node-1"] = domain.Node{ID: "node-1", Status: domain.NodeOnline, ConfigVersion: 1}
	memory.agentCredentials["node-1"] = "credential"
	now := time.Now().UTC()
	first := ActiveConnection{ConnectionID: "tcp-1", RuleID: "rule-1", Protocol: "tcp", SourceAddress: "192.0.2.1:5000",
		TargetAddress: "198.51.100.1:443", StartedAt: now.Add(-time.Minute), LastActivity: now, BytesIn: 10}
	if _, err := memory.UpdateAgentHeartbeat(context.Background(), "node-1", "credential", AgentHeartbeat{
		ConfigVersion: 1, Connections: []ActiveConnection{first}, ConnectionsComplete: true, ReceivedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	second := ActiveConnection{ConnectionID: "udp-2", RuleID: "rule-2", Protocol: "udp", SourceAddress: "192.0.2.2:5001",
		TargetAddress: "198.51.100.2:53", StartedAt: now, LastActivity: now.Add(time.Second), BytesOut: 20}
	if _, err := memory.UpdateAgentHeartbeat(context.Background(), "node-1", "credential", AgentHeartbeat{
		ConfigVersion: 1, Connections: []ActiveConnection{second}, ConnectionsComplete: false, ReceivedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	connections, err := memory.ListActiveConnections(context.Background())
	if err != nil || len(connections) != 2 || connections[0].ConnectionID != second.ConnectionID || !connections[0].ObservedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("incomplete snapshot replaced existing connections: %#v err=%v", connections, err)
	}
	if _, err := memory.UpdateAgentHeartbeat(context.Background(), "node-1", "credential", AgentHeartbeat{
		ConfigVersion: 1, ConnectionsComplete: true, ReceivedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	connections, err = memory.ListActiveConnections(context.Background())
	if err != nil || len(connections) != 0 {
		t.Fatalf("complete empty snapshot did not clear connections: %#v err=%v", connections, err)
	}
}

func TestMemoryRelayRuleVersionsAndConfigsBothEndpoints(t *testing.T) {
	memory := NewMemory()
	for index, nodeID := range []string{"node-in", "node-out"} {
		memory.nodes[nodeID] = domain.Node{ID: nodeID, Status: domain.NodeOnline, ConfigVersion: 1, TunnelAddress: []string{"10.203.0.1", "10.203.0.2"}[index]}
		memory.agentCredentials[nodeID] = nodeID + "-credential"
		memory.agentConfigs[nodeID] = AgentConfig{Version: 1}
	}
	rule := domain.ForwardRule{ID: "relay-1", Name: "Relay draft", Protocol: domain.ProtocolTCP, Mode: domain.ForwardRelay,
		IngressNodeID: "node-in", EgressNodeID: "node-out", ListenHost: "0.0.0.0", ListenPort: 2443,
		TargetHost: "198.51.100.20", TargetPort: 443, RelayPort: 32443, Enabled: false}
	created, err := memory.CreateForwardRule(context.Background(), rule)
	if err != nil {
		t.Fatal(err)
	}
	if created.ConfigVersion != 2 || created.EgressConfigVersion != 2 || memory.nodes["node-in"].ConfigVersion != 2 || memory.nodes["node-out"].ConfigVersion != 2 {
		t.Fatalf("relay versions were not advanced together: rule=%#v ingress=%#v egress=%#v", created, memory.nodes["node-in"], memory.nodes["node-out"])
	}
	for _, nodeID := range []string{"node-in", "node-out"} {
		config, err := memory.ConfigForAgent(context.Background(), nodeID, nodeID+"-credential")
		if err != nil || config.Version != 2 || len(config.Rules) != 1 || config.Rules[0].ID != rule.ID || config.Rules[0].RelayHost != "10.203.0.2" {
			t.Fatalf("endpoint %s config=%#v err=%v", nodeID, config, err)
		}
	}
	if err := memory.DeleteForwardRule(context.Background(), rule.ID); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{"node-in", "node-out"} {
		config, err := memory.ConfigForAgent(context.Background(), nodeID, nodeID+"-credential")
		if err != nil || config.Version != 3 || len(config.Rules) != 0 {
			t.Fatalf("endpoint %s delete config=%#v err=%v", nodeID, config, err)
		}
	}
}

func TestMemoryTrafficHistoryHandlesCounterReset(t *testing.T) {
	memory := NewMemory()
	memory.nodes["node-1"] = domain.Node{ID: "node-1", Status: domain.NodeOnline, ConfigVersion: 1}
	memory.agentCredentials["node-1"] = "credential"
	base := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	for _, heartbeat := range []AgentHeartbeat{
		{ConfigVersion: 1, BytesIn: 100, BytesOut: 200, ReceivedAt: base},
		{ConfigVersion: 1, BytesIn: 140, BytesOut: 260, ReceivedAt: base.Add(time.Minute)},
		{ConfigVersion: 1, BytesIn: 5, BytesOut: 7, ReceivedAt: base.Add(2 * time.Minute)},
	} {
		if _, err := memory.UpdateAgentHeartbeat(context.Background(), "node-1", "credential", heartbeat); err != nil {
			t.Fatal(err)
		}
	}
	points, err := memory.TrafficHistory(context.Background(), base.Add(30*time.Second), base.Add(3*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].UploadBytes != 45 || points[0].DownloadBytes != 67 {
		t.Fatalf("unexpected traffic points: %#v", points)
	}
}

func TestMemoryNodeMetricHistoryAggregatesResourcesAndCounterReset(t *testing.T) {
	memory := NewMemory()
	memory.nodes["node-1"] = domain.Node{ID: "node-1", Status: domain.NodeOnline, ConfigVersion: 1}
	memory.agentCredentials["node-1"] = "credential"
	base := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	for _, heartbeat := range []AgentHeartbeat{
		{ConfigVersion: 1, CPUPercent: 10, MemoryPercent: 20, LoadOne: 0.2, DiskPercent: 40, NetworkRxBps: 100, NetworkTxBps: 200, ActiveConns: 1, BytesIn: 100, BytesOut: 200, ReceivedAt: base},
		{ConfigVersion: 1, CPUPercent: 30, MemoryPercent: 40, LoadOne: 0.4, DiskPercent: 50, NetworkRxBps: 300, NetworkTxBps: 400, ActiveConns: 2, BytesIn: 140, BytesOut: 260, ReceivedAt: base.Add(time.Minute)},
		{ConfigVersion: 1, CPUPercent: 50, MemoryPercent: 60, LoadOne: 0.6, DiskPercent: 60, NetworkRxBps: 500, NetworkTxBps: 600, ActiveConns: 3, BytesIn: 5, BytesOut: 7, ReceivedAt: base.Add(2 * time.Minute)},
	} {
		if _, err := memory.UpdateAgentHeartbeat(context.Background(), "node-1", "credential", heartbeat); err != nil {
			t.Fatal(err)
		}
	}
	points, err := memory.NodeMetricHistory(context.Background(), "node-1", base.Add(30*time.Second), base.Add(3*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].CPUPercent != 40 || points[0].MemoryPercent != 50 || points[0].LoadOne != 0.5 || points[0].DiskPercent != 55 ||
		points[0].NetworkRxBps != 400 || points[0].NetworkTxBps != 500 ||
		points[0].ActiveConns != 3 || points[0].UploadBytes != 45 || points[0].DownloadBytes != 67 {
		t.Fatalf("unexpected node metric points: %#v", points)
	}
}

func TestMemoryAuditPagination(t *testing.T) {
	memory := NewMemory()
	base := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	for index, action := range []string{"first", "second", "third"} {
		if err := memory.RecordAudit(context.Background(), AuditEvent{ID: action, Action: action, CreatedAt: base.Add(time.Duration(index) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	firstPage, err := memory.ListAudit(context.Background(), 2, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 || firstPage[0].Action != "third" || firstPage[1].Action != "second" {
		t.Fatalf("unexpected first audit page: %#v", firstPage)
	}
	secondPage, err := memory.ListAudit(context.Background(), 2, firstPage[1].CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 1 || secondPage[0].Action != "first" {
		t.Fatalf("unexpected second audit page: %#v", secondPage)
	}
}

func TestMemoryAgentLogsAreDeduplicatedAndFiltered(t *testing.T) {
	memory := NewMemory()
	memory.nodes["node-1"] = domain.Node{ID: "node-1", Status: domain.NodeOnline, ConfigVersion: 1}
	memory.agentCredentials["node-1"] = "credential"
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	logs := []AgentLog{
		{EventID: "event-info", Level: "info", Component: "agent", Message: "ready", OccurredAt: now.Add(-time.Minute)},
		{EventID: "event-error", Level: "error", Component: "forward", Message: "dial failed", OccurredAt: now},
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := memory.UpdateAgentHeartbeat(context.Background(), "node-1", "credential", AgentHeartbeat{ConfigVersion: 1, Logs: logs, ReceivedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := memory.ListAgentLogs(context.Background(), AgentLogFilter{NodeID: "node-1", Level: "error", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].EventID != "event-error" || entries[0].NodeID != "node-1" || !entries[0].ReceivedAt.Equal(now) {
		t.Fatalf("unexpected filtered logs: %#v", entries)
	}
}
