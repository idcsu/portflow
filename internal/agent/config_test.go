package agent

import (
	"os"
	"path/filepath"
	"testing"

	"portflow/internal/domain"
)

func TestConfigStoreRoundTrip(t *testing.T) {
	store := NewConfigStore(filepath.Join(t.TempDir(), "nested", "config.json"))
	want := Config{Version: 7, ControlURL: "https://control.example", NodeID: "node-1", Credential: "agent-secret", Rules: []domain.ForwardRule{{
		ID: "rule-1", Name: "DNS", Protocol: domain.ProtocolUDP, Mode: domain.ForwardDirect,
		IngressNodeID: "node-1", ListenHost: "0.0.0.0", ListenPort: 5353,
		TargetHost: "1.1.1.1", TargetPort: 53, Enabled: true,
	}}}
	if err := store.Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Version != want.Version || len(got.Rules) != 1 || got.Rules[0].ID != want.Rules[0].ID {
		t.Fatalf("unexpected config: %#v", got)
	}
	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatalf("stat configuration: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("configuration permissions=%v", info.Mode().Perm())
	}
}

func TestConfigRequiresCompleteIdentity(t *testing.T) {
	config := Config{Version: 1, ControlURL: "https://control.example"}
	if err := config.Validate(); err == nil {
		t.Fatal("partial identity accepted")
	}
}

func TestConfigRejectsRuleForAnotherNode(t *testing.T) {
	config := Config{Version: 1, ControlURL: "https://control.example", NodeID: "node-1", Credential: "secret", Rules: []domain.ForwardRule{{
		ID: "rule-1", Name: "wrong node", Protocol: domain.ProtocolTCP, Mode: domain.ForwardDirect,
		IngressNodeID: "node-2", ListenPort: 10001, TargetHost: "127.0.0.1", TargetPort: 10002,
	}}}
	if err := config.Validate(); err == nil {
		t.Fatal("rule for another node accepted")
	}
}

func TestConfigAcceptsDisabledRelayForBothEndpoints(t *testing.T) {
	rule := domain.ForwardRule{ID: "relay-1", Name: "relay draft", Protocol: domain.ProtocolTCP, Mode: domain.ForwardRelay,
		IngressNodeID: "node-in", EgressNodeID: "node-out", ListenPort: 20001, RelayPort: 30001, TargetHost: "192.0.2.50", TargetPort: 443}
	for _, nodeID := range []string{"node-in", "node-out"} {
		config := Config{Version: 2, ControlURL: "https://control.example", NodeID: nodeID, Credential: "secret", Rules: []domain.ForwardRule{rule}}
		if err := config.Validate(); err != nil {
			t.Fatalf("relay endpoint %s rejected: %v", nodeID, err)
		}
	}
}

func TestConfigAcceptsProvisionedRelayAndRejectsEgressMismatch(t *testing.T) {
	rule := domain.ForwardRule{ID: "relay-live", Name: "relay live", Protocol: domain.ProtocolTCPUDP, Mode: domain.ForwardRelay,
		IngressNodeID: "node-in", EgressNodeID: "node-out", ListenPort: 20001, RelayHost: "10.203.0.2", RelayPort: 30001,
		TargetHost: "192.0.2.50", TargetPort: 443, Enabled: true}
	for nodeID, tunnelAddress := range map[string]string{"node-in": "10.203.0.1", "node-out": "10.203.0.2"} {
		config := Config{Version: 3, ControlURL: "https://control.example", NodeID: nodeID, Credential: "secret", TunnelAddress: tunnelAddress, Rules: []domain.ForwardRule{rule}}
		if err := config.Validate(); err != nil {
			t.Fatalf("provisioned relay endpoint %s rejected: %v", nodeID, err)
		}
	}
	mismatch := Config{Version: 3, ControlURL: "https://control.example", NodeID: "node-out", Credential: "secret", TunnelAddress: "10.203.0.9", Rules: []domain.ForwardRule{rule}}
	if err := mismatch.Validate(); err == nil {
		t.Fatal("mismatched egress tunnel address accepted")
	}
}

func TestConfigRejectsDuplicateRules(t *testing.T) {
	rule := domain.ForwardRule{
		ID: "same", Name: "duplicate", Protocol: domain.ProtocolTCP, Mode: domain.ForwardDirect,
		IngressNodeID: "node-1", ListenPort: 10001, TargetHost: "127.0.0.1", TargetPort: 10002,
	}
	config := Config{Version: 1, Rules: []domain.ForwardRule{rule, rule}}
	if err := config.Validate(); err == nil {
		t.Fatal("duplicate rule ids were accepted")
	}
}
