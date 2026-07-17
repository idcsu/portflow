//go:build integration
// +build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"portflow/internal/domain"
	"portflow/internal/store"
)

func TestPostgresStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("PORTFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PORTFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Health(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	user := store.User{ID: "integration-user", Username: "integration-admin", PasswordHash: "not-used-by-this-test", Role: store.RoleAdmin, CreatedAt: now}
	if err := database.CreateInitialUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateUser(ctx, user.ID, store.UserUpdate{Role: store.RoleMember}); !errors.Is(err, store.ErrLastAdministrator) {
		t.Fatalf("last administrator update error=%v", err)
	}
	member := store.User{ID: "integration-member", Username: "integration-member", PasswordHash: "not-used", Role: store.RoleMember, CreatedAt: now.Add(time.Second)}
	if err := database.CreateUser(ctx, member); err != nil {
		t.Fatal(err)
	}
	memberSession := store.Session{ID: "integration-member-session", UserID: member.ID, TokenHash: "integration-member-token", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := database.CreateSession(ctx, memberSession); err != nil {
		t.Fatal(err)
	}
	updatedMember, err := database.UpdateUser(ctx, member.ID, store.UserUpdate{Role: store.RoleMember, Disabled: true, PasswordHash: "updated-hash"})
	if err != nil || !updatedMember.Disabled || updatedMember.PasswordHash != "updated-hash" {
		t.Fatalf("unexpected member update: %#v err=%v", updatedMember, err)
	}
	if _, _, err := database.SessionUserByTokenHash(ctx, memberSession.TokenHash, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("updated member session was not revoked: %v", err)
	}
	users, err := database.ListUsers(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("unexpected users: %#v err=%v", users, err)
	}
	mfaUser, err := database.SetUserMFA(ctx, user.ID, true, "encrypted-test-secret", []string{"recovery-one", "recovery-two"})
	if err != nil || !mfaUser.MFAEnabled || len(mfaUser.MFARecoveryHashes) != 2 {
		t.Fatalf("unexpected MFA user: %#v err=%v", mfaUser, err)
	}
	mfaSession := store.Session{ID: "integration-mfa-session", UserID: user.ID, TokenHash: "integration-mfa-token", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := database.CreateSession(ctx, mfaSession); err != nil {
		t.Fatal(err)
	}
	_, sessionUser, err := database.SessionUserByTokenHash(ctx, mfaSession.TokenHash, now)
	if err != nil || !sessionUser.MFAEnabled || sessionUser.MFASecret != "encrypted-test-secret" {
		t.Fatalf("unexpected MFA session user: %#v err=%v", sessionUser, err)
	}
	consumed, err := database.ConsumeRecoveryCode(ctx, user.ID, "recovery-one")
	if err != nil || !consumed {
		t.Fatalf("consume recovery code: consumed=%v err=%v", consumed, err)
	}
	consumed, err = database.ConsumeRecoveryCode(ctx, user.ID, "recovery-one")
	if err != nil || consumed {
		t.Fatalf("reused recovery code: consumed=%v err=%v", consumed, err)
	}
	resetUser, err := database.UpdateUser(ctx, user.ID, store.UserUpdate{Role: store.RoleAdmin, ResetMFA: true})
	if err != nil || resetUser.MFAEnabled || resetUser.MFASecret != "" || len(resetUser.MFARecoveryHashes) != 0 {
		t.Fatalf("atomic MFA reset failed: %#v err=%v", resetUser, err)
	}
	enrollment := store.EnrollmentToken{ID: "integration-token", Name: "integration", TokenHash: "integration-token-hash", CreatedBy: user.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := database.CreateEnrollmentToken(ctx, enrollment); err != nil {
		t.Fatal(err)
	}
	node, err := database.EnrollAgent(ctx, enrollment.TokenHash, now, store.AgentIdentity{
		ID: "integration-node", Name: "Integration Node", Region: "test", PublicIP: "192.0.2.10",
		Architecture: "linux/amd64", AgentVersion: "integration", CredentialHash: "integration-credential-hash", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule, err := database.CreateForwardRule(ctx, domain.ForwardRule{
		ID: "integration-rule", Name: "Integration UDP", Protocol: domain.ProtocolUDP, Mode: domain.ForwardDirect,
		IngressNodeID: node.ID, ListenHost: "0.0.0.0", ListenPort: 53000,
		TargetHost: "198.51.100.53", TargetPort: 53, Enabled: true, BandwidthKbps: 512, MaxConnections: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rule.ConfigVersion != 2 {
		t.Fatalf("rule config version=%d, want 2", rule.ConfigVersion)
	}
	config, err := database.ConfigForAgent(ctx, node.ID, "integration-credential-hash")
	if err != nil {
		t.Fatal(err)
	}
	if config.Version != 2 || len(config.Rules) != 1 || config.Rules[0].Protocol != domain.ProtocolUDP || config.Rules[0].BandwidthKbps != 512 {
		t.Fatalf("unexpected agent config: %#v", config)
	}

	metricStart := now.Truncate(time.Minute)
	_, err = database.UpdateAgentHeartbeat(ctx, node.ID, "integration-credential-hash", store.AgentHeartbeat{
		PublicIP: "192.0.2.11", TunnelAddress: "10.203.0.1", AgentVersion: "integration", ConfigVersion: 2, AttemptedConfigVersion: 2,
		CPUPercent: 12, MemoryPercent: 34, LoadOne: 0.5, DiskPercent: 44, NetworkRxBps: 1000, NetworkTxBps: 2000,
		ActiveConns: 3, BytesIn: 100, BytesOut: 200,
		RuleStats: []store.RuleRuntimeStats{{RuleID: rule.ID, ActiveConns: 3, BytesIn: 40, BytesOut: 50}}, ReceivedAt: metricStart,
		ConnectionsComplete: true,
		Connections:         []store.ActiveConnection{{ConnectionID: "integration-connection", RuleID: rule.ID, Protocol: "udp", SourceAddress: "192.0.2.25:53000", TargetAddress: "198.51.100.53:53", StartedAt: metricStart.Add(-time.Minute), LastActivity: metricStart, BytesIn: 5, BytesOut: 7}},
		Logs:                []store.AgentLog{{EventID: "integration-log", Level: "error", Component: "forward", Message: "dial failed", OccurredAt: metricStart}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := database.ListForwardRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].ActiveConns != 3 || rules[0].BytesIn != 40 || rules[0].BytesOut != 50 {
		t.Fatalf("unexpected runtime rule: %#v", rules)
	}
	nodes, err := database.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].AppliedConfigVersion != 2 || nodes[0].PublicIP != "192.0.2.11" ||
		nodes[0].DiskPercent != 44 || nodes[0].NetworkRxBps != 1000 || nodes[0].NetworkTxBps != 2000 {
		t.Fatalf("unexpected runtime node: %#v", nodes)
	}
	connections, err := database.ListActiveConnections(ctx)
	if err != nil || len(connections) != 1 || connections[0].ConnectionID != "integration-connection" || connections[0].NodeID != node.ID || connections[0].BytesOut != 7 {
		t.Fatalf("unexpected active connections: %#v err=%v", connections, err)
	}
	_, err = database.UpdateAgentHeartbeat(ctx, node.ID, "integration-credential-hash", store.AgentHeartbeat{
		PublicIP: "192.0.2.11", TunnelAddress: "10.203.0.1", AgentVersion: "integration", ConfigVersion: 2, AttemptedConfigVersion: 2,
		CPUPercent: 13, MemoryPercent: 35, LoadOne: 0.6, DiskPercent: 46, NetworkRxBps: 3000, NetworkTxBps: 4000,
		ActiveConns: 2, BytesIn: 175, BytesOut: 325,
		ReceivedAt: metricStart.Add(time.Minute), ConnectionsComplete: true,
		Logs: []store.AgentLog{{EventID: "integration-log", Level: "error", Component: "forward", Message: "dial failed", OccurredAt: metricStart}},
	})
	if err != nil {
		t.Fatal(err)
	}
	connections, err = database.ListActiveConnections(ctx)
	if err != nil || len(connections) != 0 {
		t.Fatalf("complete empty snapshot did not clear active connections: %#v err=%v", connections, err)
	}
	traffic, err := database.TrafficHistory(ctx, metricStart.Add(30*time.Second), metricStart.Add(2*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].UploadBytes != 75 || traffic[0].DownloadBytes != 125 {
		t.Fatalf("unexpected traffic history: %#v", traffic)
	}
	nodeMetrics, err := database.NodeMetricHistory(ctx, node.ID, metricStart.Add(30*time.Second), metricStart.Add(2*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodeMetrics) != 1 || nodeMetrics[0].CPUPercent != 13 || nodeMetrics[0].MemoryPercent != 35 || nodeMetrics[0].DiskPercent != 46 ||
		nodeMetrics[0].NetworkRxBps != 3000 || nodeMetrics[0].NetworkTxBps != 4000 ||
		nodeMetrics[0].ActiveConns != 2 || nodeMetrics[0].UploadBytes != 75 || nodeMetrics[0].DownloadBytes != 125 {
		t.Fatalf("unexpected node metric history: %#v", nodeMetrics)
	}
	logs, err := database.ListAgentLogs(ctx, store.AgentLogFilter{NodeID: node.ID, Level: "error", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].EventID != "integration-log" || logs[0].Message != "dial failed" {
		t.Fatalf("unexpected agent logs: %#v", logs)
	}
	audit := store.AuditEvent{ID: "integration-audit", ActorType: "user", ActorID: user.ID, Action: "integration.test",
		TargetType: "node", TargetID: node.ID, RemoteIP: "192.0.2.20", Details: map[string]interface{}{"result": "ok"}, CreatedAt: now}
	if err := database.RecordAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	events, err := database.ListAudit(ctx, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != audit.Action || events[0].Details["result"] != "ok" {
		t.Fatalf("unexpected audit events: %#v", events)
	}

	secondEnrollment := store.EnrollmentToken{ID: "integration-token-2", Name: "integration relay exit", TokenHash: "integration-token-hash-2",
		CreatedBy: user.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := database.CreateEnrollmentToken(ctx, secondEnrollment); err != nil {
		t.Fatal(err)
	}
	exitNode, err := database.EnrollAgent(ctx, secondEnrollment.TokenHash, now, store.AgentIdentity{
		ID: "integration-node-exit", Name: "Integration Exit", Region: "test-exit", PublicIP: "192.0.2.30",
		Architecture: "linux/amd64", AgentVersion: "integration", CredentialHash: "integration-exit-credential-hash", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpdateAgentHeartbeat(ctx, exitNode.ID, "integration-exit-credential-hash", store.AgentHeartbeat{
		PublicIP: "192.0.2.30", TunnelAddress: "10.203.0.2", AgentVersion: "integration", ConfigVersion: 1, AttemptedConfigVersion: 1, ReceivedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	relay, err := database.CreateForwardRule(ctx, domain.ForwardRule{
		ID: "integration-relay", Name: "Integration relay draft", Protocol: domain.ProtocolTCP, Mode: domain.ForwardRelay,
		IngressNodeID: node.ID, EgressNodeID: exitNode.ID, ListenHost: "0.0.0.0", ListenPort: 24443,
		TargetHost: "198.51.100.20", TargetPort: 443, RelayPort: 32443, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if relay.ConfigVersion != 3 || relay.EgressConfigVersion != 2 {
		t.Fatalf("unexpected relay endpoint versions: %#v", relay)
	}
	ingressConfig, err := database.ConfigForAgent(ctx, node.ID, "integration-credential-hash")
	if err != nil || ingressConfig.Version != 3 || len(ingressConfig.Rules) != 2 {
		t.Fatalf("unexpected relay ingress config: %#v err=%v", ingressConfig, err)
	}
	exitConfig, err := database.ConfigForAgent(ctx, exitNode.ID, "integration-exit-credential-hash")
	if err != nil || exitConfig.Version != 2 || len(exitConfig.Rules) != 1 || exitConfig.Rules[0].ID != relay.ID || exitConfig.Rules[0].RelayHost != "10.203.0.2" {
		t.Fatalf("unexpected relay exit config: %#v err=%v", exitConfig, err)
	}
	_, err = database.CreateForwardRule(ctx, domain.ForwardRule{
		ID: "integration-relay-conflict", Name: "Conflicting relay listener", Protocol: domain.ProtocolUDP, Mode: domain.ForwardRelay,
		IngressNodeID: node.ID, EgressNodeID: exitNode.ID, ListenHost: "0.0.0.0", ListenPort: 24444,
		TargetHost: "198.51.100.21", TargetPort: 53, RelayPort: relay.RelayPort, Enabled: false,
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("relay listener conflict error=%v, want ErrConflict", err)
	}
}
