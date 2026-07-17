package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"portflow/internal/domain"
)

type Config struct {
	Version       uint64               `json:"version"`
	ControlURL    string               `json:"controlUrl,omitempty"`
	NodeID        string               `json:"nodeId,omitempty"`
	Credential    string               `json:"credential,omitempty"`
	TunnelAddress string               `json:"tunnelAddress,omitempty"`
	Rules         []domain.ForwardRule `json:"rules"`
}

func (config Config) Validate() error {
	if config.Version == 0 {
		return errors.New("config version must be greater than zero")
	}
	identityFields := 0
	for _, value := range []string{config.ControlURL, config.NodeID, config.Credential} {
		if value != "" {
			identityFields++
		}
	}
	if identityFields != 0 && identityFields != 3 {
		return errors.New("control URL, node ID, and credential must be configured together")
	}
	if config.TunnelAddress != "" && !domain.ValidTunnelAddress(config.TunnelAddress) {
		return errors.New("tunnel address must be a private IPv4 address")
	}
	ids := make(map[string]struct{}, len(config.Rules))
	for index, rule := range config.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("rule %d: %w", index, err)
		}
		belongsToNode := rule.IngressNodeID == config.NodeID || rule.Mode == domain.ForwardRelay && rule.EgressNodeID == config.NodeID
		if config.NodeID != "" && !belongsToNode {
			return fmt.Errorf("rule %s does not belong to this node", rule.ID)
		}
		if rule.Mode == domain.ForwardRelay && rule.Enabled {
			if !domain.ValidTunnelAddress(config.TunnelAddress) {
				return fmt.Errorf("rule %s requires this node to have a private tunnel address", rule.ID)
			}
			if config.NodeID == rule.EgressNodeID && config.TunnelAddress != rule.RelayHost {
				return fmt.Errorf("rule %s relay host does not match this egress node tunnel address", rule.ID)
			}
		}
		if _, exists := ids[rule.ID]; exists {
			return fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		ids[rule.ID] = struct{}{}
	}
	return nil
}

type ConfigStore struct {
	path string
}

func NewConfigStore(path string) *ConfigStore {
	return &ConfigStore{path: path}
}

func (store *ConfigStore) Load() (Config, error) {
	contents, err := os.ReadFile(store.path)
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal(contents, &config); err != nil {
		return Config{}, fmt.Errorf("decode local configuration: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate local configuration: %w", err)
	}
	return config, nil
}

func (store *ConfigStore) Save(config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode local configuration: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	temporaryPath := store.path + ".tmp"
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open temporary configuration: %w", err)
	}
	err = file.Chmod(0o600)
	if err == nil {
		_, err = file.Write(append(contents, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("commit configuration: %w", err)
	}
	if directory, err := os.Open(filepath.Dir(store.path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
