package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"portflow/internal/domain"
)

type Heartbeat struct {
	AgentVersion           string                `json:"agentVersion"`
	TunnelAddress          string                `json:"tunnelAddress,omitempty"`
	ConfigVersion          uint64                `json:"configVersion"`
	AttemptedConfigVersion uint64                `json:"attemptedConfigVersion"`
	LastConfigError        string                `json:"lastConfigError,omitempty"`
	CPUPercent             float64               `json:"cpuPercent"`
	MemoryPercent          float64               `json:"memoryPercent"`
	LoadOne                float64               `json:"loadOne"`
	DiskPercent            float64               `json:"diskPercent"`
	NetworkRxBps           uint64                `json:"networkRxBps"`
	NetworkTxBps           uint64                `json:"networkTxBps"`
	ActiveConns            uint64                `json:"activeConnections"`
	BytesIn                uint64                `json:"bytesIn"`
	BytesOut               uint64                `json:"bytesOut"`
	RuleStats              []RuleStatsHeartbeat  `json:"ruleStats"`
	Connections            []ConnectionHeartbeat `json:"connections"`
	ConnectionsComplete    bool                  `json:"connectionsComplete"`
	Logs                   []AgentLogHeartbeat   `json:"logs,omitempty"`
}

type ConnectionHeartbeat struct {
	ID            string    `json:"id"`
	RuleID        string    `json:"ruleId"`
	Protocol      string    `json:"protocol"`
	SourceAddress string    `json:"sourceAddress"`
	TargetAddress string    `json:"targetAddress"`
	StartedAt     time.Time `json:"startedAt"`
	LastActivity  time.Time `json:"lastActivity"`
	BytesIn       uint64    `json:"bytesIn"`
	BytesOut      uint64    `json:"bytesOut"`
}

type RuleStatsHeartbeat struct {
	RuleID      string `json:"ruleId"`
	ActiveConns uint64 `json:"activeConnections"`
	BytesIn     uint64 `json:"bytesIn"`
	BytesOut    uint64 `json:"bytesOut"`
}

type HeartbeatResponse struct {
	ServerTime               time.Time `json:"serverTime"`
	ConfigVersion            uint64    `json:"configVersion"`
	ConfigChanged            bool      `json:"configChanged"`
	HeartbeatIntervalSeconds int       `json:"heartbeatIntervalSeconds"`
}

type RemoteConfig struct {
	Version uint64               `json:"version"`
	Rules   []domain.ForwardRule `json:"rules"`
}

type ControlClient struct {
	baseURL    string
	nodeID     string
	credential string
	http       *http.Client
}

func NewControlClient(baseURL, nodeID, credential string, client *http.Client) (*ControlClient, error) {
	parsed, err := validateControlURL(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(nodeID) == "" || strings.TrimSpace(credential) == "" {
		return nil, errors.New("node identity is incomplete")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &ControlClient{baseURL: strings.TrimRight(parsed.String(), "/"), nodeID: nodeID, credential: credential, http: client}, nil
}

func (client *ControlClient) SendHeartbeat(ctx context.Context, heartbeat Heartbeat) (HeartbeatResponse, error) {
	var response HeartbeatResponse
	if err := client.request(ctx, http.MethodPost, "/api/v1/agent/heartbeat", heartbeat, &response); err != nil {
		return HeartbeatResponse{}, err
	}
	return response, nil
}

func (client *ControlClient) FetchConfig(ctx context.Context) (RemoteConfig, error) {
	var config RemoteConfig
	if err := client.request(ctx, http.MethodGet, "/api/v1/agent/config", nil, &config); err != nil {
		return RemoteConfig{}, err
	}
	if config.Version == 0 {
		return RemoteConfig{}, errors.New("control server returned config version zero")
	}
	for index, rule := range config.Rules {
		if err := rule.Validate(); err != nil {
			return RemoteConfig{}, fmt.Errorf("control server returned invalid rule %d: %w", index, err)
		}
	}
	return config, nil
}

func (client *ControlClient) request(ctx context.Context, method, path string, input, output interface{}) error {
	var body io.Reader
	if input != nil {
		contents, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(contents)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.credential)
	request.Header.Set("X-PortFlow-Node-ID", client.nodeID)
	request.Header.Set("User-Agent", "PortFlow-Agent")
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("contact control server: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 2<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(limited).Decode(&failure)
		if failure.Error.Message == "" {
			failure.Error.Message = response.Status
		}
		return fmt.Errorf("control server rejected request: %s", failure.Error.Message)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(limited).Decode(output); err != nil {
		return fmt.Errorf("decode control response: %w", err)
	}
	return nil
}
