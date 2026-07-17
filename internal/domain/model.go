package domain

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

type Protocol string

const (
	ProtocolTCP    Protocol = "tcp"
	ProtocolUDP    Protocol = "udp"
	ProtocolTCPUDP Protocol = "tcp_udp"
	ForwardDirect           = "direct"
	ForwardRelay            = "relay"
	// MaxBandwidthKbps bounds arithmetic and rejects accidental values above
	// 10 Gbit/s. Zero means unlimited.
	MaxBandwidthKbps uint64 = 10_000_000
)

func ProtocolsOverlap(first, second Protocol) bool {
	if first == ProtocolTCPUDP || second == ProtocolTCPUDP {
		return true
	}
	return first == second
}

func ValidTunnelAddress(value string) bool {
	address := net.ParseIP(strings.TrimSpace(value))
	return address != nil && address.To4() != nil && !address.IsUnspecified() && (address.IsPrivate() || address.IsLoopback())
}

type NodeStatus string

const (
	NodeOnline   NodeStatus = "online"
	NodeOffline  NodeStatus = "offline"
	NodeDisabled NodeStatus = "disabled"
)

type Node struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Region                 string     `json:"region"`
	PublicIP               string     `json:"publicIp"`
	Status                 NodeStatus `json:"status"`
	Architecture           string     `json:"architecture"`
	AgentVersion           string     `json:"agentVersion"`
	LastHeartbeat          time.Time  `json:"lastHeartbeat"`
	ConfigVersion          uint64     `json:"configVersion"`
	AppliedConfigVersion   uint64     `json:"appliedConfigVersion"`
	AttemptedConfigVersion uint64     `json:"attemptedConfigVersion"`
	LastConfigError        string     `json:"lastConfigError,omitempty"`
	LastConfigAttempt      time.Time  `json:"lastConfigAttempt,omitempty"`
	CPUPercent             float64    `json:"cpuPercent"`
	MemoryPercent          float64    `json:"memoryPercent"`
	LoadOne                float64    `json:"loadOne"`
	DiskPercent            float64    `json:"diskPercent"`
	NetworkRxBps           uint64     `json:"networkRxBps"`
	NetworkTxBps           uint64     `json:"networkTxBps"`
	ActiveConns            uint64     `json:"activeConnections"`
	BytesIn                uint64     `json:"bytesIn"`
	BytesOut               uint64     `json:"bytesOut"`
	TunnelAddress          string     `json:"tunnelAddress,omitempty"`
}

type ForwardRule struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Protocol            Protocol  `json:"protocol"`
	Mode                string    `json:"mode"`
	IngressNodeID       string    `json:"ingressNodeId"`
	EgressNodeID        string    `json:"egressNodeId,omitempty"`
	ListenHost          string    `json:"listenHost"`
	ListenPort          uint16    `json:"listenPort"`
	TargetHost          string    `json:"targetHost"`
	TargetPort          uint16    `json:"targetPort"`
	RelayHost           string    `json:"relayHost,omitempty"`
	RelayPort           uint16    `json:"relayPort,omitempty"`
	Enabled             bool      `json:"enabled"`
	BandwidthKbps       uint64    `json:"bandwidthKbps,omitempty"`
	MaxConnections      uint32    `json:"maxConnections,omitempty"`
	AllowCIDRs          []string  `json:"allowCidrs,omitempty"`
	DenyCIDRs           []string  `json:"denyCidrs,omitempty"`
	ConfigVersion       uint64    `json:"configVersion,omitempty"`
	EgressConfigVersion uint64    `json:"egressConfigVersion,omitempty"`
	ActiveConns         uint64    `json:"activeConnections"`
	BytesIn             uint64    `json:"bytesIn"`
	BytesOut            uint64    `json:"bytesOut"`
	RuntimeUpdated      time.Time `json:"runtimeUpdatedAt,omitempty"`
}

func (rule ForwardRule) Validate() error {
	if strings.TrimSpace(rule.ID) == "" {
		return errors.New("rule id is required")
	}
	if strings.TrimSpace(rule.Name) == "" {
		return errors.New("rule name is required")
	}
	if rule.Protocol != ProtocolTCP && rule.Protocol != ProtocolUDP && rule.Protocol != ProtocolTCPUDP {
		return fmt.Errorf("unsupported protocol %q", rule.Protocol)
	}
	if rule.Mode != ForwardDirect && rule.Mode != ForwardRelay {
		return fmt.Errorf("unsupported forwarding mode %q", rule.Mode)
	}
	if rule.IngressNodeID == "" {
		return errors.New("ingress node is required")
	}
	if rule.Mode == ForwardRelay && rule.EgressNodeID == "" {
		return errors.New("egress node is required for relay mode")
	}
	if rule.Mode == ForwardRelay && rule.IngressNodeID == rule.EgressNodeID {
		return errors.New("ingress and egress nodes must differ")
	}
	if rule.Mode == ForwardRelay && rule.RelayPort == 0 {
		return errors.New("relay port is required for relay mode")
	}
	if rule.Mode == ForwardRelay && rule.Enabled && !ValidTunnelAddress(rule.RelayHost) {
		return errors.New("an enabled relay requires a private egress tunnel address")
	}
	if rule.Mode == ForwardDirect && (rule.RelayHost != "" || rule.RelayPort != 0) {
		return errors.New("direct mode cannot specify a relay endpoint")
	}
	if rule.Mode == ForwardDirect && rule.EgressNodeID != "" {
		return errors.New("direct mode cannot specify an egress node")
	}
	if rule.ListenPort == 0 || rule.TargetPort == 0 {
		return errors.New("listen and target ports must be between 1 and 65535")
	}
	if rule.BandwidthKbps > MaxBandwidthKbps {
		return fmt.Errorf("bandwidth limit must not exceed %d Kbit/s", MaxBandwidthKbps)
	}
	if strings.TrimSpace(rule.TargetHost) == "" {
		return errors.New("target host is required")
	}
	for _, cidr := range append(append([]string{}, rule.AllowCIDRs...), rule.DenyCIDRs...) {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
	}
	return nil
}
