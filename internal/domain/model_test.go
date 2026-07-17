package domain

import "testing"

func TestForwardRuleValidate(t *testing.T) {
	valid := ForwardRule{
		ID: "rule-1", Name: "SSH", Protocol: ProtocolTCP, Mode: ForwardDirect,
		IngressNodeID: "node-1", ListenHost: "0.0.0.0", ListenPort: 2222,
		TargetHost: "10.0.0.8", TargetPort: 22,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ForwardRule)
	}{
		{"missing target", func(rule *ForwardRule) { rule.TargetHost = "" }},
		{"missing relay egress", func(rule *ForwardRule) { rule.Mode = ForwardRelay }},
		{"direct with egress", func(rule *ForwardRule) { rule.EgressNodeID = "node-2" }},
		{"invalid protocol", func(rule *ForwardRule) { rule.Protocol = Protocol("icmp") }},
		{"invalid cidr", func(rule *ForwardRule) { rule.AllowCIDRs = []string{"not-a-cidr"} }},
		{"excessive bandwidth", func(rule *ForwardRule) { rule.BandwidthKbps = MaxBandwidthKbps + 1 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule := valid
			test.mutate(&rule)
			if err := rule.Validate(); err == nil {
				t.Fatal("invalid rule accepted")
			}
		})
	}
}

func TestProtocolsOverlap(t *testing.T) {
	tests := []struct {
		first, second Protocol
		want          bool
	}{
		{ProtocolTCP, ProtocolTCP, true},
		{ProtocolUDP, ProtocolUDP, true},
		{ProtocolTCP, ProtocolUDP, false},
		{ProtocolUDP, ProtocolTCP, false},
		{ProtocolTCPUDP, ProtocolTCP, true},
		{ProtocolUDP, ProtocolTCPUDP, true},
	}
	for _, test := range tests {
		if got := ProtocolsOverlap(test.first, test.second); got != test.want {
			t.Errorf("ProtocolsOverlap(%q, %q)=%v, want %v", test.first, test.second, got, test.want)
		}
	}
}
