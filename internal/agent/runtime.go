package agent

import (
	"context"
	"fmt"
	"time"

	"portflow/internal/forward"
)

type Runtime struct {
	store                  *ConfigStore
	control                *ControlClient
	forward                *forward.Manager
	collector              *MetricsCollector
	version                string
	logf                   func(string, ...interface{})
	logs                   *LogBuffer
	attemptedConfigVersion uint64
	lastConfigError        string
}

func NewRuntime(store *ConfigStore, control *ControlClient, manager *forward.Manager, version string, logf func(string, ...interface{}), logs *LogBuffer) *Runtime {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &Runtime{store: store, control: control, forward: manager, collector: &MetricsCollector{}, version: version, logf: logf, logs: logs}
}

func (runtime *Runtime) Run(ctx context.Context, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if err := runtime.forward.Apply(ctx, config.Rules); err != nil {
		return fmt.Errorf("apply local configuration: %w", err)
	}
	defer runtime.forward.Close()

	delay := time.Duration(0)
	failures := 0
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
		interval, err := runtime.synchronize(ctx, &config)
		if err != nil {
			failures++
			delay = retryDelay(failures)
			runtime.logf("control synchronization failed; current forwarding remains active; retry=%s error=%v", delay, err)
			continue
		}
		failures = 0
		delay = interval
	}
}

func (runtime *Runtime) synchronize(ctx context.Context, current *Config) (time.Duration, error) {
	metrics := runtime.collector.Sample()
	stats := runtime.forward.Stats()
	ruleStats := make([]RuleStatsHeartbeat, 0, len(stats.Rules))
	for _, rule := range stats.Rules {
		ruleStats = append(ruleStats, RuleStatsHeartbeat{RuleID: rule.RuleID, ActiveConns: rule.ActiveConnections, BytesIn: rule.BytesIn, BytesOut: rule.BytesOut})
	}
	connections := make([]ConnectionHeartbeat, 0, len(stats.Connections))
	for _, connection := range stats.Connections {
		connections = append(connections, ConnectionHeartbeat{
			ID: connection.ID, RuleID: connection.RuleID, Protocol: connection.Protocol,
			SourceAddress: connection.SourceAddress, TargetAddress: connection.TargetAddress,
			StartedAt: connection.StartedAt, LastActivity: connection.LastActivity,
			BytesIn: connection.BytesIn, BytesOut: connection.BytesOut,
		})
	}
	attemptedVersion := runtime.attemptedConfigVersion
	if attemptedVersion == 0 {
		attemptedVersion = current.Version
	}
	logBatch := runtime.logs.Pending(100)
	heartbeat, err := runtime.control.SendHeartbeat(ctx, Heartbeat{
		AgentVersion: runtime.version, TunnelAddress: current.TunnelAddress, ConfigVersion: current.Version, AttemptedConfigVersion: attemptedVersion,
		LastConfigError: runtime.lastConfigError, CPUPercent: metrics.CPUPercent,
		MemoryPercent: metrics.MemoryPercent, LoadOne: metrics.LoadOne, DiskPercent: metrics.DiskPercent,
		NetworkRxBps: metrics.NetworkRxBps, NetworkTxBps: metrics.NetworkTxBps, ActiveConns: stats.ActiveConnections,
		BytesIn: stats.BytesIn, BytesOut: stats.BytesOut, RuleStats: ruleStats,
		Connections: connections, ConnectionsComplete: stats.ConnectionsComplete, Logs: logBatch,
	})
	if err != nil {
		return 0, err
	}
	runtime.logs.Acknowledge(logBatch)
	interval := time.Duration(heartbeat.HeartbeatIntervalSeconds) * time.Second
	if interval < 5*time.Second || interval > 5*time.Minute {
		interval = 15 * time.Second
	}
	if !heartbeat.ConfigChanged && heartbeat.ConfigVersion == current.Version {
		runtime.clearConfigFailure(current.Version)
		return interval, nil
	}

	remote, err := runtime.control.FetchConfig(ctx)
	if err != nil {
		runtime.recordConfigFailure(heartbeat.ConfigVersion, err)
		return 0, err
	}
	if remote.Version < current.Version {
		err := fmt.Errorf("control config version regressed from %d to %d", current.Version, remote.Version)
		runtime.recordConfigFailure(remote.Version, err)
		return 0, err
	}
	if remote.Version == current.Version {
		runtime.clearConfigFailure(current.Version)
		return interval, nil
	}
	candidate := Config{Version: remote.Version, ControlURL: current.ControlURL, NodeID: current.NodeID, Credential: current.Credential, TunnelAddress: current.TunnelAddress, Rules: remote.Rules}
	if err := candidate.Validate(); err != nil {
		runtime.recordConfigFailure(remote.Version, err)
		return 0, fmt.Errorf("validate new configuration: %w", err)
	}
	if err := runtime.forward.Apply(ctx, candidate.Rules); err != nil {
		runtime.recordConfigFailure(remote.Version, err)
		return 0, fmt.Errorf("apply new configuration: %w", err)
	}
	if err := runtime.store.Save(candidate); err != nil {
		if rollbackErr := runtime.forward.Apply(ctx, current.Rules); rollbackErr != nil {
			runtime.recordConfigFailure(remote.Version, fmt.Errorf("save failed: %v; rollback failed: %w", err, rollbackErr))
			return 0, fmt.Errorf("save new configuration: %v; rollback also failed: %w", err, rollbackErr)
		}
		runtime.recordConfigFailure(remote.Version, err)
		return 0, fmt.Errorf("save new configuration: %w", err)
	}
	*current = candidate
	runtime.clearConfigFailure(candidate.Version)
	runtime.logf("applied configuration version=%d rules=%d", candidate.Version, len(candidate.Rules))
	return interval, nil
}

func (runtime *Runtime) recordConfigFailure(version uint64, err error) {
	runtime.attemptedConfigVersion = version
	message := err.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	runtime.lastConfigError = message
}

func (runtime *Runtime) clearConfigFailure(version uint64) {
	runtime.attemptedConfigVersion = version
	runtime.lastConfigError = ""
}

func retryDelay(failures int) time.Duration {
	if failures < 1 {
		return 15 * time.Second
	}
	delay := 15 * time.Second
	for attempt := 1; attempt < failures && delay < 2*time.Minute; attempt++ {
		delay *= 2
	}
	if delay > 2*time.Minute {
		return 2 * time.Minute
	}
	return delay
}
