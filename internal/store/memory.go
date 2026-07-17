package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"portflow/internal/domain"
)

type Memory struct {
	mu                sync.RWMutex
	users             map[string]User
	usernames         map[string]string
	sessions          map[string]Session
	enrollmentTokens  map[string]EnrollmentToken
	nodes             map[string]domain.Node
	agentCredentials  map[string]string
	agentConfigs      map[string]AgentConfig
	forwardRules      map[string]domain.ForwardRule
	audits            []AuditEvent
	metricSamples     []MetricSample
	agentLogs         []AgentLog
	agentLogKeys      map[string]struct{}
	activeConnections map[string]map[string]ActiveConnection
}

func NewMemory() *Memory {
	return &Memory{
		users:             make(map[string]User),
		usernames:         make(map[string]string),
		sessions:          make(map[string]Session),
		enrollmentTokens:  make(map[string]EnrollmentToken),
		nodes:             make(map[string]domain.Node),
		agentCredentials:  make(map[string]string),
		agentConfigs:      make(map[string]AgentConfig),
		forwardRules:      make(map[string]domain.ForwardRule),
		agentLogKeys:      make(map[string]struct{}),
		activeConnections: make(map[string]map[string]ActiveConnection),
	}
}

func (memory *Memory) Close() error { return nil }

func (memory *Memory) Health(context.Context) error { return nil }

func (memory *Memory) CreateInitialUser(_ context.Context, user User) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if len(memory.users) != 0 {
		return ErrBootstrapCompleted
	}
	memory.users[user.ID] = user
	memory.usernames[user.Username] = user.ID
	return nil
}

func (memory *Memory) CreateUser(_ context.Context, user User) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if _, exists := memory.users[user.ID]; exists {
		return ErrConflict
	}
	if _, exists := memory.usernames[user.Username]; exists {
		return ErrConflict
	}
	memory.users[user.ID] = user
	memory.usernames[user.Username] = user.ID
	return nil
}

func (memory *Memory) UpdateUser(_ context.Context, userID string, update UserUpdate) (User, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	user, exists := memory.users[userID]
	if !exists {
		return User{}, ErrNotFound
	}
	if user.Role == RoleAdmin && !user.Disabled && (update.Role != RoleAdmin || update.Disabled) {
		activeAdministrators := 0
		for _, candidate := range memory.users {
			if candidate.Role == RoleAdmin && !candidate.Disabled {
				activeAdministrators++
			}
		}
		if activeAdministrators <= 1 {
			return User{}, ErrLastAdministrator
		}
	}
	user.Role = update.Role
	user.Disabled = update.Disabled
	if update.PasswordHash != "" {
		user.PasswordHash = update.PasswordHash
	}
	if update.ResetMFA {
		user.MFAEnabled = false
		user.MFASecret = ""
		user.MFARecoveryHashes = nil
	}
	memory.users[userID] = user
	for tokenHash, session := range memory.sessions {
		if session.UserID == userID {
			delete(memory.sessions, tokenHash)
		}
	}
	return user, nil
}

func (memory *Memory) DeleteUser(_ context.Context, userID string) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	user, exists := memory.users[userID]
	if !exists {
		return ErrNotFound
	}
	if user.Role == RoleAdmin && !user.Disabled {
		activeAdministrators := 0
		for _, candidate := range memory.users {
			if candidate.Role == RoleAdmin && !candidate.Disabled {
				activeAdministrators++
			}
		}
		if activeAdministrators <= 1 {
			return ErrLastAdministrator
		}
	}
	delete(memory.users, userID)
	delete(memory.usernames, user.Username)
	for tokenHash, token := range memory.enrollmentTokens {
		if token.CreatedBy == userID {
			delete(memory.enrollmentTokens, tokenHash)
		}
	}
	for tokenHash, session := range memory.sessions {
		if session.UserID == userID {
			delete(memory.sessions, tokenHash)
		}
	}
	return nil
}

func (memory *Memory) SetUserMFA(_ context.Context, userID string, enabled bool, secret string, recoveryHashes []string) (User, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	user, exists := memory.users[userID]
	if !exists {
		return User{}, ErrNotFound
	}
	user.MFAEnabled = enabled
	user.MFASecret = secret
	user.MFARecoveryHashes = append([]string(nil), recoveryHashes...)
	memory.users[userID] = user
	return user, nil
}

func (memory *Memory) ConsumeRecoveryCode(_ context.Context, userID, hash string) (bool, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	user, exists := memory.users[userID]
	if !exists {
		return false, ErrNotFound
	}
	for index, candidate := range user.MFARecoveryHashes {
		if candidate == hash {
			user.MFARecoveryHashes = append(user.MFARecoveryHashes[:index], user.MFARecoveryHashes[index+1:]...)
			memory.users[userID] = user
			return true, nil
		}
	}
	return false, nil
}

func (memory *Memory) ListUsers(_ context.Context) ([]User, error) {
	memory.mu.RLock()
	defer memory.mu.RUnlock()
	result := make([]User, 0, len(memory.users))
	for _, user := range memory.users {
		result = append(result, user)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].Username < result[right].Username
		}
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

func (memory *Memory) UserByUsername(_ context.Context, username string) (User, error) {
	memory.mu.RLock()
	defer memory.mu.RUnlock()
	id, ok := memory.usernames[username]
	if !ok {
		return User{}, ErrNotFound
	}
	return memory.users[id], nil
}

func (memory *Memory) UserByID(_ context.Context, id string) (User, error) {
	memory.mu.RLock()
	defer memory.mu.RUnlock()
	user, ok := memory.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return user, nil
}

func (memory *Memory) CreateSession(_ context.Context, session Session) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.sessions[session.TokenHash] = session
	return nil
}

func (memory *Memory) SessionUserByTokenHash(_ context.Context, tokenHash string, now time.Time) (Session, User, error) {
	memory.mu.RLock()
	defer memory.mu.RUnlock()
	session, ok := memory.sessions[tokenHash]
	if !ok || !session.ExpiresAt.After(now) {
		return Session{}, User{}, ErrNotFound
	}
	user, ok := memory.users[session.UserID]
	if !ok || user.Disabled {
		return Session{}, User{}, ErrNotFound
	}
	return session, user, nil
}

func (memory *Memory) DeleteSessionByTokenHash(_ context.Context, tokenHash string) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	delete(memory.sessions, tokenHash)
	return nil
}

func (memory *Memory) CreateEnrollmentToken(_ context.Context, token EnrollmentToken) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if _, exists := memory.enrollmentTokens[token.TokenHash]; exists {
		return ErrConflict
	}
	memory.enrollmentTokens[token.TokenHash] = token
	return nil
}

func (memory *Memory) EnrollAgent(_ context.Context, tokenHash string, now time.Time, agent AgentIdentity) (domain.Node, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	token, ok := memory.enrollmentTokens[tokenHash]
	if !ok || token.UsedAt != nil || !token.ExpiresAt.After(now) {
		return domain.Node{}, ErrTokenInvalid
	}
	if _, exists := memory.nodes[agent.ID]; exists {
		return domain.Node{}, ErrConflict
	}
	usedAt := now
	token.UsedAt = &usedAt
	memory.enrollmentTokens[tokenHash] = token
	node := domain.Node{
		ID: agent.ID, Name: agent.Name, Region: agent.Region, PublicIP: agent.PublicIP,
		Status: domain.NodeOnline, Architecture: agent.Architecture, AgentVersion: agent.AgentVersion,
		LastHeartbeat: now, ConfigVersion: 1, AppliedConfigVersion: 1, AttemptedConfigVersion: 1,
	}
	memory.nodes[node.ID] = node
	memory.agentCredentials[node.ID] = agent.CredentialHash
	memory.agentConfigs[node.ID] = AgentConfig{Version: 1, Rules: []domain.ForwardRule{}}
	return node, nil
}

func (memory *Memory) UpdateAgentHeartbeat(_ context.Context, nodeID, credentialHash string, heartbeat AgentHeartbeat) (domain.Node, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	node, exists := memory.nodes[nodeID]
	if !exists || memory.agentCredentials[nodeID] != credentialHash || node.Status == domain.NodeDisabled {
		return domain.Node{}, ErrNotFound
	}
	node.Status = domain.NodeOnline
	node.LastHeartbeat = heartbeat.ReceivedAt
	node.PublicIP = heartbeat.PublicIP
	node.TunnelAddress = heartbeat.TunnelAddress
	node.AgentVersion = heartbeat.AgentVersion
	node.AppliedConfigVersion = heartbeat.ConfigVersion
	if node.AppliedConfigVersion > node.ConfigVersion {
		node.AppliedConfigVersion = node.ConfigVersion
	}
	node.AttemptedConfigVersion = heartbeat.AttemptedConfigVersion
	if node.AttemptedConfigVersion == 0 {
		node.AttemptedConfigVersion = node.AppliedConfigVersion
	}
	if node.AttemptedConfigVersion > node.ConfigVersion {
		node.AttemptedConfigVersion = node.ConfigVersion
	}
	node.LastConfigError = heartbeat.LastConfigError
	node.LastConfigAttempt = heartbeat.ReceivedAt
	node.CPUPercent = heartbeat.CPUPercent
	node.MemoryPercent = heartbeat.MemoryPercent
	node.LoadOne = heartbeat.LoadOne
	node.DiskPercent = heartbeat.DiskPercent
	node.NetworkRxBps = heartbeat.NetworkRxBps
	node.NetworkTxBps = heartbeat.NetworkTxBps
	node.ActiveConns = heartbeat.ActiveConns
	node.BytesIn = heartbeat.BytesIn
	node.BytesOut = heartbeat.BytesOut
	memory.nodes[nodeID] = node
	memory.recordMetricLocked(nodeID, heartbeat)
	memory.recordAgentLogsLocked(nodeID, heartbeat)
	connections := memory.activeConnections[nodeID]
	if heartbeat.ConnectionsComplete || connections == nil {
		connections = make(map[string]ActiveConnection, len(heartbeat.Connections))
	}
	for _, connection := range heartbeat.Connections {
		connection.NodeID = nodeID
		connection.ObservedAt = heartbeat.ReceivedAt
		connections[connection.ConnectionID] = connection
	}
	if len(connections) > MaxStoredConnectionsPerNode {
		ordered := make([]ActiveConnection, 0, len(connections))
		for _, connection := range connections {
			ordered = append(ordered, connection)
		}
		sort.Slice(ordered, func(left, right int) bool {
			if ordered[left].ObservedAt.Equal(ordered[right].ObservedAt) {
				return ordered[left].LastActivity.After(ordered[right].LastActivity)
			}
			return ordered[left].ObservedAt.After(ordered[right].ObservedAt)
		})
		for _, connection := range ordered[MaxStoredConnectionsPerNode:] {
			delete(connections, connection.ConnectionID)
		}
	}
	memory.activeConnections[nodeID] = connections
	reportedRules := make(map[string]struct{}, len(heartbeat.RuleStats))
	for _, stats := range heartbeat.RuleStats {
		rule, exists := memory.forwardRules[stats.RuleID]
		if !exists || rule.IngressNodeID != nodeID {
			continue
		}
		reportedRules[rule.ID] = struct{}{}
		rule.ActiveConns = stats.ActiveConns
		rule.BytesIn = stats.BytesIn
		rule.BytesOut = stats.BytesOut
		rule.RuntimeUpdated = heartbeat.ReceivedAt
		memory.forwardRules[rule.ID] = rule
	}
	for id, rule := range memory.forwardRules {
		if rule.IngressNodeID == nodeID {
			if _, reported := reportedRules[id]; !reported {
				rule.ActiveConns = 0
				rule.RuntimeUpdated = heartbeat.ReceivedAt
				memory.forwardRules[id] = rule
			}
		}
	}
	return node, nil
}

func (memory *Memory) ListActiveConnections(_ context.Context) ([]ActiveConnection, error) {
	memory.mu.RLock()
	defer memory.mu.RUnlock()
	var result []ActiveConnection
	for _, byID := range memory.activeConnections {
		for _, connection := range byID {
			result = append(result, connection)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].LastActivity.Equal(result[right].LastActivity) {
			if result[left].NodeID == result[right].NodeID {
				return result[left].ConnectionID < result[right].ConnectionID
			}
			return result[left].NodeID < result[right].NodeID
		}
		return result[left].LastActivity.After(result[right].LastActivity)
	})
	return result, nil
}

func (memory *Memory) ConfigForAgent(_ context.Context, nodeID, credentialHash string) (AgentConfig, error) {
	memory.mu.RLock()
	defer memory.mu.RUnlock()
	node, exists := memory.nodes[nodeID]
	if !exists || memory.agentCredentials[nodeID] != credentialHash || node.Status == domain.NodeDisabled {
		return AgentConfig{}, ErrNotFound
	}
	config, exists := memory.agentConfigs[nodeID]
	if !exists {
		return AgentConfig{}, ErrNotFound
	}
	config.Rules = append([]domain.ForwardRule(nil), config.Rules...)
	return config, nil
}

// SetAgentConfig is used by the control-plane rule service and deterministic tests.
func (memory *Memory) SetAgentConfig(nodeID string, config AgentConfig) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	node, exists := memory.nodes[nodeID]
	if !exists {
		return ErrNotFound
	}
	memory.agentConfigs[nodeID] = AgentConfig{Version: config.Version, Rules: append([]domain.ForwardRule(nil), config.Rules...)}
	node.ConfigVersion = config.Version
	memory.nodes[nodeID] = node
	return nil
}

func (memory *Memory) CreateForwardRule(_ context.Context, rule domain.ForwardRule) (domain.ForwardRule, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if _, exists := memory.forwardRules[rule.ID]; exists {
		return domain.ForwardRule{}, ErrConflict
	}
	if !memory.ruleNodesAvailableLocked(rule) {
		return domain.ForwardRule{}, ErrNotFound
	}
	if memory.listenerConflictLocked(rule, "") {
		return domain.ForwardRule{}, ErrConflict
	}
	for _, nodeID := range ruleNodeIDs(rule) {
		node := memory.nodes[nodeID]
		node.ConfigVersion++
		memory.nodes[node.ID] = node
	}
	rule.ConfigVersion = memory.nodes[rule.IngressNodeID].ConfigVersion
	if rule.Mode == domain.ForwardRelay {
		rule.EgressConfigVersion = memory.nodes[rule.EgressNodeID].ConfigVersion
	}
	memory.forwardRules[rule.ID] = cloneRule(rule)
	for _, nodeID := range ruleNodeIDs(rule) {
		memory.rebuildAgentConfigLocked(nodeID)
	}
	return cloneRule(rule), nil
}

func (memory *Memory) UpdateForwardRule(_ context.Context, rule domain.ForwardRule) (domain.ForwardRule, error) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	previous, exists := memory.forwardRules[rule.ID]
	if !exists {
		return domain.ForwardRule{}, ErrNotFound
	}
	if !memory.ruleNodesAvailableLocked(rule) {
		return domain.ForwardRule{}, ErrNotFound
	}
	if memory.listenerConflictLocked(rule, rule.ID) {
		return domain.ForwardRule{}, ErrConflict
	}
	rule.ActiveConns = previous.ActiveConns
	rule.BytesIn = previous.BytesIn
	rule.BytesOut = previous.BytesOut
	rule.RuntimeUpdated = previous.RuntimeUpdated
	affected := make(map[string]struct{})
	for _, nodeID := range append(ruleNodeIDs(previous), ruleNodeIDs(rule)...) {
		affected[nodeID] = struct{}{}
	}
	for nodeID := range affected {
		node := memory.nodes[nodeID]
		node.ConfigVersion++
		memory.nodes[nodeID] = node
	}
	rule.ConfigVersion = memory.nodes[rule.IngressNodeID].ConfigVersion
	if rule.Mode == domain.ForwardRelay {
		rule.EgressConfigVersion = memory.nodes[rule.EgressNodeID].ConfigVersion
	} else {
		rule.EgressConfigVersion = 0
	}
	memory.forwardRules[rule.ID] = cloneRule(rule)
	for nodeID := range affected {
		memory.rebuildAgentConfigLocked(nodeID)
	}
	return cloneRule(rule), nil
}

func (memory *Memory) DeleteForwardRule(_ context.Context, id string) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	rule, exists := memory.forwardRules[id]
	if !exists {
		return ErrNotFound
	}
	nodeIDs := ruleNodeIDs(rule)
	for _, nodeID := range nodeIDs {
		node := memory.nodes[nodeID]
		node.ConfigVersion++
		memory.nodes[node.ID] = node
	}
	delete(memory.forwardRules, id)
	for _, nodeID := range nodeIDs {
		memory.rebuildAgentConfigLocked(nodeID)
	}
	return nil
}

func (memory *Memory) ListForwardRules(_ context.Context) ([]domain.ForwardRule, error) {
	memory.mu.RLock()
	defer memory.mu.RUnlock()
	rules := make([]domain.ForwardRule, 0, len(memory.forwardRules))
	for _, rule := range memory.forwardRules {
		cloned := cloneRule(rule)
		if cloned.Mode == domain.ForwardRelay {
			cloned.RelayHost = memory.nodes[cloned.EgressNodeID].TunnelAddress
		}
		rules = append(rules, cloned)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	return rules, nil
}

func (memory *Memory) listenerConflictLocked(candidate domain.ForwardRule, excludedID string) bool {
	for id, rule := range memory.forwardRules {
		if id != excludedID && rule.IngressNodeID == candidate.IngressNodeID && domain.ProtocolsOverlap(rule.Protocol, candidate.Protocol) &&
			rule.ListenHost == candidate.ListenHost && rule.ListenPort == candidate.ListenPort {
			return true
		}
		if id != excludedID && candidate.Mode == domain.ForwardRelay && rule.Mode == domain.ForwardRelay &&
			rule.EgressNodeID == candidate.EgressNodeID && rule.RelayPort == candidate.RelayPort {
			return true
		}
	}
	return false
}

func (memory *Memory) rebuildAgentConfigLocked(nodeID string) {
	node, exists := memory.nodes[nodeID]
	if !exists {
		return
	}
	rules := make([]domain.ForwardRule, 0)
	for _, rule := range memory.forwardRules {
		if rule.IngressNodeID == nodeID || rule.Mode == domain.ForwardRelay && rule.EgressNodeID == nodeID {
			cloned := cloneRule(rule)
			if cloned.Mode == domain.ForwardRelay {
				cloned.RelayHost = memory.nodes[cloned.EgressNodeID].TunnelAddress
			}
			rules = append(rules, cloned)
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	memory.agentConfigs[nodeID] = AgentConfig{Version: node.ConfigVersion, Rules: rules}
}

func (memory *Memory) ruleNodesAvailableLocked(rule domain.ForwardRule) bool {
	for _, nodeID := range ruleNodeIDs(rule) {
		node, exists := memory.nodes[nodeID]
		if !exists || node.Status == domain.NodeDisabled {
			return false
		}
	}
	return true
}

func ruleNodeIDs(rule domain.ForwardRule) []string {
	result := []string{rule.IngressNodeID}
	if rule.Mode == domain.ForwardRelay && rule.EgressNodeID != "" && rule.EgressNodeID != rule.IngressNodeID {
		result = append(result, rule.EgressNodeID)
	}
	return result
}

func cloneRule(rule domain.ForwardRule) domain.ForwardRule {
	rule.AllowCIDRs = append([]string(nil), rule.AllowCIDRs...)
	rule.DenyCIDRs = append([]string(nil), rule.DenyCIDRs...)
	return rule
}

func (memory *Memory) ListNodes(_ context.Context) ([]domain.Node, error) {
	memory.mu.RLock()
	defer memory.mu.RUnlock()
	nodes := make([]domain.Node, 0, len(memory.nodes))
	for _, node := range memory.nodes {
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, nil
}

func (memory *Memory) RecordAudit(_ context.Context, event AuditEvent) error {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.audits = append(memory.audits, event)
	return nil
}

func (memory *Memory) ListAudit(_ context.Context, limit int, before time.Time) ([]AuditEvent, error) {
	memory.mu.RLock()
	defer memory.mu.RUnlock()
	result := make([]AuditEvent, 0, limit)
	for index := len(memory.audits) - 1; index >= 0 && len(result) < limit; index-- {
		event := memory.audits[index]
		if !before.IsZero() && !event.CreatedAt.Before(before) {
			continue
		}
		event.Details = cloneDetails(event.Details)
		result = append(result, event)
	}
	return result, nil
}

func (memory *Memory) recordAgentLogsLocked(nodeID string, heartbeat AgentHeartbeat) {
	cutoff := heartbeat.ReceivedAt.Add(-AgentLogRetention)
	retained := memory.agentLogs[:0]
	for _, entry := range memory.agentLogs {
		if entry.ReceivedAt.Before(cutoff) {
			delete(memory.agentLogKeys, entry.NodeID+"\x00"+entry.EventID)
			continue
		}
		retained = append(retained, entry)
	}
	memory.agentLogs = retained
	for _, entry := range heartbeat.Logs {
		key := nodeID + "\x00" + entry.EventID
		if _, exists := memory.agentLogKeys[key]; exists {
			continue
		}
		entry.NodeID = nodeID
		entry.ReceivedAt = heartbeat.ReceivedAt
		memory.agentLogs = append(memory.agentLogs, entry)
		memory.agentLogKeys[key] = struct{}{}
	}
}

func (memory *Memory) ListAgentLogs(_ context.Context, filter AgentLogFilter) ([]AgentLog, error) {
	memory.mu.RLock()
	defer memory.mu.RUnlock()
	entries := append([]AgentLog(nil), memory.agentLogs...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].OccurredAt.Equal(entries[j].OccurredAt) {
			return entries[i].EventID > entries[j].EventID
		}
		return entries[i].OccurredAt.After(entries[j].OccurredAt)
	})
	result := make([]AgentLog, 0, filter.Limit)
	for _, entry := range entries {
		if filter.NodeID != "" && entry.NodeID != filter.NodeID || filter.Level != "" && entry.Level != filter.Level ||
			!filter.Before.IsZero() && !entry.OccurredAt.Before(filter.Before) {
			continue
		}
		result = append(result, entry)
		if len(result) == filter.Limit {
			break
		}
	}
	return result, nil
}

func (memory *Memory) recordMetricLocked(nodeID string, heartbeat AgentHeartbeat) {
	bucket := heartbeat.ReceivedAt.UTC().Truncate(time.Minute)
	cutoff := bucket.Add(-NodeMetricRetention)
	filtered := memory.metricSamples[:0]
	replaced := false
	for _, sample := range memory.metricSamples {
		if sample.SampledAt.Before(cutoff) {
			continue
		}
		if sample.NodeID == nodeID && sample.SampledAt.Equal(bucket) {
			filtered = append(filtered, MetricSample{NodeID: nodeID, SampledAt: bucket, CPUPercent: heartbeat.CPUPercent,
				MemoryPercent: heartbeat.MemoryPercent, LoadOne: heartbeat.LoadOne, DiskPercent: heartbeat.DiskPercent,
				NetworkRxBps: heartbeat.NetworkRxBps, NetworkTxBps: heartbeat.NetworkTxBps, ActiveConns: heartbeat.ActiveConns,
				BytesIn: heartbeat.BytesIn, BytesOut: heartbeat.BytesOut})
			replaced = true
			continue
		}
		filtered = append(filtered, sample)
	}
	if !replaced {
		filtered = append(filtered, MetricSample{NodeID: nodeID, SampledAt: bucket, CPUPercent: heartbeat.CPUPercent,
			MemoryPercent: heartbeat.MemoryPercent, LoadOne: heartbeat.LoadOne, DiskPercent: heartbeat.DiskPercent,
			NetworkRxBps: heartbeat.NetworkRxBps, NetworkTxBps: heartbeat.NetworkTxBps, ActiveConns: heartbeat.ActiveConns,
			BytesIn: heartbeat.BytesIn, BytesOut: heartbeat.BytesOut})
	}
	memory.metricSamples = filtered
}

func (memory *Memory) TrafficHistory(_ context.Context, from, to time.Time, interval time.Duration) ([]TrafficPoint, error) {
	memory.mu.RLock()
	samples := append([]MetricSample(nil), memory.metricSamples...)
	memory.mu.RUnlock()
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].NodeID == samples[j].NodeID {
			return samples[i].SampledAt.Before(samples[j].SampledAt)
		}
		return samples[i].NodeID < samples[j].NodeID
	})
	type counters struct{ in, out uint64 }
	previous := make(map[string]counters)
	points := make(map[int64]TrafficPoint)
	seconds := int64(interval / time.Second)
	for _, sample := range samples {
		if sample.SampledAt.After(to) {
			continue
		}
		prior, exists := previous[sample.NodeID]
		previous[sample.NodeID] = counters{sample.BytesIn, sample.BytesOut}
		if sample.SampledAt.Before(from) || !exists {
			continue
		}
		upload, download := sample.BytesIn, sample.BytesOut
		if sample.BytesIn >= prior.in {
			upload = sample.BytesIn - prior.in
		}
		if sample.BytesOut >= prior.out {
			download = sample.BytesOut - prior.out
		}
		bucketUnix := sample.SampledAt.Unix() / seconds * seconds
		point := points[bucketUnix]
		point.Time = time.Unix(bucketUnix, 0).UTC()
		point.UploadBytes += upload
		point.DownloadBytes += download
		points[bucketUnix] = point
	}
	keys := make([]int64, 0, len(points))
	for key := range points {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := make([]TrafficPoint, 0, len(keys))
	for _, key := range keys {
		result = append(result, points[key])
	}
	return result, nil
}

func (memory *Memory) NodeMetricHistory(_ context.Context, nodeID string, from, to time.Time, interval time.Duration) ([]NodeMetricPoint, error) {
	memory.mu.RLock()
	samples := make([]MetricSample, 0, len(memory.metricSamples))
	for _, sample := range memory.metricSamples {
		if sample.NodeID == nodeID && !sample.SampledAt.After(to) {
			samples = append(samples, sample)
		}
	}
	memory.mu.RUnlock()
	sort.Slice(samples, func(i, j int) bool { return samples[i].SampledAt.Before(samples[j].SampledAt) })

	type aggregate struct {
		point NodeMetricPoint
		count float64
		rx    float64
		tx    float64
	}
	type counters struct{ in, out uint64 }
	var previous counters
	hasPrevious := false
	buckets := make(map[int64]aggregate)
	seconds := int64(interval / time.Second)
	for _, sample := range samples {
		prior, exists := previous, hasPrevious
		previous = counters{sample.BytesIn, sample.BytesOut}
		hasPrevious = true
		if sample.SampledAt.Before(from) {
			continue
		}
		bucketUnix := sample.SampledAt.Unix() / seconds * seconds
		bucket := buckets[bucketUnix]
		bucket.point.Time = time.Unix(bucketUnix, 0).UTC()
		bucket.point.CPUPercent += sample.CPUPercent
		bucket.point.MemoryPercent += sample.MemoryPercent
		bucket.point.LoadOne += sample.LoadOne
		bucket.point.DiskPercent += sample.DiskPercent
		bucket.rx += float64(sample.NetworkRxBps)
		bucket.tx += float64(sample.NetworkTxBps)
		bucket.point.ActiveConns = sample.ActiveConns
		bucket.count++
		if exists {
			if sample.BytesIn >= prior.in {
				bucket.point.UploadBytes += sample.BytesIn - prior.in
			} else {
				bucket.point.UploadBytes += sample.BytesIn
			}
			if sample.BytesOut >= prior.out {
				bucket.point.DownloadBytes += sample.BytesOut - prior.out
			} else {
				bucket.point.DownloadBytes += sample.BytesOut
			}
		}
		buckets[bucketUnix] = bucket
	}
	keys := make([]int64, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := make([]NodeMetricPoint, 0, len(keys))
	for _, key := range keys {
		bucket := buckets[key]
		bucket.point.CPUPercent /= bucket.count
		bucket.point.MemoryPercent /= bucket.count
		bucket.point.LoadOne /= bucket.count
		bucket.point.DiskPercent /= bucket.count
		bucket.point.NetworkRxBps = uint64(bucket.rx / bucket.count)
		bucket.point.NetworkTxBps = uint64(bucket.tx / bucket.count)
		result = append(result, bucket.point)
	}
	return result, nil
}

func cloneDetails(details map[string]interface{}) map[string]interface{} {
	if details == nil {
		return nil
	}
	result := make(map[string]interface{}, len(details))
	for key, value := range details {
		result[key] = value
	}
	return result
}
