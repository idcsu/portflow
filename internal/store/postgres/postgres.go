package postgres

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgconn"
	_ "github.com/jackc/pgx/v4/stdlib"

	"portflow/internal/domain"
	"portflow/internal/store"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	database *sql.DB
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	database.SetMaxOpenConns(10)
	database.SetMaxIdleConns(5)
	database.SetConnMaxLifetime(30 * time.Minute)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	result := &Store{database: database}
	if err := result.migrate(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return result, nil
}

func (postgres *Store) Close() error { return postgres.database.Close() }

func (postgres *Store) Health(ctx context.Context) error { return postgres.database.PingContext(ctx) }

func (postgres *Store) migrate(ctx context.Context) error {
	if _, err := postgres.database.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL
		)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		var exists bool
		if err := postgres.database.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)", entry.Name()).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if exists {
			continue
		}
		contents, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		transaction, err := postgres.database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err = transaction.ExecContext(ctx, string(contents)); err == nil {
			_, err = transaction.ExecContext(ctx, "INSERT INTO schema_migrations(name, applied_at) VALUES ($1, $2)", entry.Name(), time.Now().UTC())
		}
		if err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (postgres *Store) CreateInitialUser(ctx context.Context, user store.User) error {
	transaction, err := postgres.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "SELECT pg_advisory_xact_lock(706733041)"); err != nil {
		return err
	}
	var count int
	if err := transaction.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return store.ErrBootstrapCompleted
	}
	_, err = transaction.ExecContext(ctx, `INSERT INTO users(id, username, password_hash, role, disabled, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		user.ID, user.Username, user.PasswordHash, user.Role, user.Disabled, user.CreatedAt)
	if err != nil {
		return err
	}
	return transaction.Commit()
}

func (postgres *Store) CreateUser(ctx context.Context, user store.User) error {
	transaction, err := postgres.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "SELECT pg_advisory_xact_lock(706733042)"); err != nil {
		return err
	}
	_, err = transaction.ExecContext(ctx, `INSERT INTO users(id,username,password_hash,role,disabled,created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		user.ID, user.Username, user.PasswordHash, user.Role, user.Disabled, user.CreatedAt)
	if uniqueViolation(err) {
		return store.ErrConflict
	}
	if err != nil {
		return err
	}
	return transaction.Commit()
}

func (postgres *Store) UpdateUser(ctx context.Context, userID string, update store.UserUpdate) (store.User, error) {
	transaction, err := postgres.database.BeginTx(ctx, nil)
	if err != nil {
		return store.User{}, err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "SELECT pg_advisory_xact_lock(706733042)"); err != nil {
		return store.User{}, err
	}
	previous, err := scanUser(transaction.QueryRowContext(ctx, `SELECT id,username,password_hash,role,disabled,mfa_enabled,mfa_secret,mfa_recovery_hashes,created_at FROM users WHERE id=$1 FOR UPDATE`, userID))
	if err != nil {
		return store.User{}, err
	}
	if previous.Role == store.RoleAdmin && !previous.Disabled && (update.Role != store.RoleAdmin || update.Disabled) {
		var activeAdministrators int
		if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=FALSE`).Scan(&activeAdministrators); err != nil {
			return store.User{}, err
		}
		if activeAdministrators <= 1 {
			return store.User{}, store.ErrLastAdministrator
		}
	}
	passwordHash := previous.PasswordHash
	if update.PasswordHash != "" {
		passwordHash = update.PasswordHash
	}
	mfaEnabled := previous.MFAEnabled
	mfaSecret := previous.MFASecret
	mfaRecoveryHashes := previous.MFARecoveryHashes
	if update.ResetMFA {
		mfaEnabled = false
		mfaSecret = ""
		mfaRecoveryHashes = nil
	}
	encodedRecoveryHashes, err := json.Marshal(mfaRecoveryHashes)
	if err != nil {
		return store.User{}, err
	}
	updated, err := scanUser(transaction.QueryRowContext(ctx, `
		UPDATE users SET role=$2,disabled=$3,password_hash=$4,mfa_enabled=$5,mfa_secret=$6,mfa_recovery_hashes=$7::jsonb WHERE id=$1
		RETURNING id,username,password_hash,role,disabled,mfa_enabled,mfa_secret,mfa_recovery_hashes,created_at`,
		userID, update.Role, update.Disabled, passwordHash, mfaEnabled, mfaSecret, string(encodedRecoveryHashes)))
	if err != nil {
		return store.User{}, err
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
		return store.User{}, err
	}
	if err := transaction.Commit(); err != nil {
		return store.User{}, err
	}
	return updated, nil
}

func (postgres *Store) DeleteUser(ctx context.Context, userID string) error {
	transaction, err := postgres.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "SELECT pg_advisory_xact_lock(706733042)"); err != nil {
		return err
	}
	user, err := scanUser(transaction.QueryRowContext(ctx, `SELECT id,username,password_hash,role,disabled,mfa_enabled,mfa_secret,mfa_recovery_hashes,created_at FROM users WHERE id=$1 FOR UPDATE`, userID))
	if err != nil {
		return err
	}
	if user.Role == store.RoleAdmin && !user.Disabled {
		var activeAdministrators int
		if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=FALSE`).Scan(&activeAdministrators); err != nil {
			return err
		}
		if activeAdministrators <= 1 {
			return store.ErrLastAdministrator
		}
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM enrollment_tokens WHERE created_by=$1`, userID); err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return store.ErrNotFound
	}
	return transaction.Commit()
}

func (postgres *Store) SetUserMFA(ctx context.Context, userID string, enabled bool, secret string, recoveryHashes []string) (store.User, error) {
	encoded, err := json.Marshal(recoveryHashes)
	if err != nil {
		return store.User{}, err
	}
	return scanUser(postgres.database.QueryRowContext(ctx, `
		UPDATE users SET mfa_enabled=$2,mfa_secret=$3,mfa_recovery_hashes=$4::jsonb WHERE id=$1
		RETURNING id,username,password_hash,role,disabled,mfa_enabled,mfa_secret,mfa_recovery_hashes,created_at`,
		userID, enabled, secret, string(encoded)))
}

func (postgres *Store) ConsumeRecoveryCode(ctx context.Context, userID, hash string) (bool, error) {
	transaction, err := postgres.database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer transaction.Rollback()
	user, err := scanUser(transaction.QueryRowContext(ctx, `SELECT id,username,password_hash,role,disabled,mfa_enabled,mfa_secret,mfa_recovery_hashes,created_at FROM users WHERE id=$1 FOR UPDATE`, userID))
	if err != nil {
		return false, err
	}
	found := false
	remaining := make([]string, 0, len(user.MFARecoveryHashes))
	for _, candidate := range user.MFARecoveryHashes {
		if !found && candidate == hash {
			found = true
			continue
		}
		remaining = append(remaining, candidate)
	}
	if !found {
		return false, nil
	}
	encoded, err := json.Marshal(remaining)
	if err != nil {
		return false, err
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE users SET mfa_recovery_hashes=$2::jsonb WHERE id=$1`, userID, string(encoded)); err != nil {
		return false, err
	}
	if err := transaction.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (postgres *Store) ListUsers(ctx context.Context) ([]store.User, error) {
	rows, err := postgres.database.QueryContext(ctx, `SELECT id,username,password_hash,role,disabled,mfa_enabled,mfa_secret,mfa_recovery_hashes,created_at FROM users ORDER BY created_at,username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []store.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, user)
	}
	return result, rows.Err()
}

func scanUser(row interface{ Scan(...interface{}) error }) (store.User, error) {
	var user store.User
	var recoveryJSON []byte
	err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.Disabled, &user.MFAEnabled, &user.MFASecret, &recoveryJSON, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.User{}, store.ErrNotFound
	}
	if err != nil {
		return store.User{}, err
	}
	if len(recoveryJSON) > 0 {
		if err := json.Unmarshal(recoveryJSON, &user.MFARecoveryHashes); err != nil {
			return store.User{}, err
		}
	}
	return user, nil
}

func (postgres *Store) UserByUsername(ctx context.Context, username string) (store.User, error) {
	return scanUser(postgres.database.QueryRowContext(ctx, `SELECT id,username,password_hash,role,disabled,mfa_enabled,mfa_secret,mfa_recovery_hashes,created_at FROM users WHERE username=$1`, username))
}

func (postgres *Store) UserByID(ctx context.Context, id string) (store.User, error) {
	return scanUser(postgres.database.QueryRowContext(ctx, `SELECT id,username,password_hash,role,disabled,mfa_enabled,mfa_secret,mfa_recovery_hashes,created_at FROM users WHERE id=$1`, id))
}

func (postgres *Store) CreateSession(ctx context.Context, session store.Session) error {
	_, err := postgres.database.ExecContext(ctx, `INSERT INTO sessions(id,user_id,token_hash,expires_at,created_at) VALUES ($1,$2,$3,$4,$5)`,
		session.ID, session.UserID, session.TokenHash, session.ExpiresAt, session.CreatedAt)
	return err
}

func (postgres *Store) SessionUserByTokenHash(ctx context.Context, tokenHash string, now time.Time) (store.Session, store.User, error) {
	var session store.Session
	var user store.User
	var recoveryJSON []byte
	err := postgres.database.QueryRowContext(ctx, `
		SELECT s.id,s.user_id,s.token_hash,s.expires_at,s.created_at,
		       u.id,u.username,u.password_hash,u.role,u.disabled,u.mfa_enabled,u.mfa_secret,u.mfa_recovery_hashes,u.created_at
		FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=$1 AND s.expires_at>$2 AND u.disabled=FALSE`, tokenHash, now).Scan(
		&session.ID, &session.UserID, &session.TokenHash, &session.ExpiresAt, &session.CreatedAt,
		&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.Disabled, &user.MFAEnabled, &user.MFASecret, &recoveryJSON, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Session{}, store.User{}, store.ErrNotFound
	}
	if err == nil && len(recoveryJSON) > 0 {
		err = json.Unmarshal(recoveryJSON, &user.MFARecoveryHashes)
	}
	return session, user, err
}

func (postgres *Store) DeleteSessionByTokenHash(ctx context.Context, tokenHash string) error {
	_, err := postgres.database.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=$1", tokenHash)
	return err
}

func (postgres *Store) CreateEnrollmentToken(ctx context.Context, token store.EnrollmentToken) error {
	_, err := postgres.database.ExecContext(ctx, `INSERT INTO enrollment_tokens(id,name,token_hash,created_by,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		token.ID, token.Name, token.TokenHash, token.CreatedBy, token.ExpiresAt, token.CreatedAt)
	return err
}

func (postgres *Store) EnrollAgent(ctx context.Context, tokenHash string, now time.Time, agent store.AgentIdentity) (domain.Node, error) {
	transaction, err := postgres.database.BeginTx(ctx, nil)
	if err != nil {
		return domain.Node{}, err
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `UPDATE enrollment_tokens SET used_at=$2 WHERE token_hash=$1 AND used_at IS NULL AND expires_at>$2`, tokenHash, now)
	if err != nil {
		return domain.Node{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return domain.Node{}, store.ErrTokenInvalid
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO nodes(id,name,region,public_ip,architecture,agent_version,credential_hash,status,last_heartbeat,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, agent.ID, agent.Name, agent.Region, agent.PublicIP,
		agent.Architecture, agent.AgentVersion, agent.CredentialHash, domain.NodeOnline, now, agent.CreatedAt)
	if err != nil {
		return domain.Node{}, err
	}
	if err := transaction.Commit(); err != nil {
		return domain.Node{}, err
	}
	return domain.Node{ID: agent.ID, Name: agent.Name, Region: agent.Region, PublicIP: agent.PublicIP,
		Status: domain.NodeOnline, Architecture: agent.Architecture, AgentVersion: agent.AgentVersion,
		LastHeartbeat: now, ConfigVersion: 1, AppliedConfigVersion: 1}, nil
}

func (postgres *Store) UpdateAgentHeartbeat(ctx context.Context, nodeID, credentialHash string, heartbeat store.AgentHeartbeat) (domain.Node, error) {
	transaction, err := postgres.database.BeginTx(ctx, nil)
	if err != nil {
		return domain.Node{}, err
	}
	defer transaction.Rollback()
	var node domain.Node
	var lastAttempt sql.NullTime
	err = transaction.QueryRowContext(ctx, `
		UPDATE nodes SET public_ip=$3, agent_version=$4, status='online', last_heartbeat=$5,
			cpu_percent=$6, memory_percent=$7, load_one=$8, disk_percent=$9,network_rx_bps=$10,network_tx_bps=$11,
			active_connections=$12, bytes_in=$13, bytes_out=$14,
			applied_config_version=LEAST($15,config_version),attempted_config_version=LEAST($16,config_version),
			last_config_error=$17,last_config_attempt=$5,tunnel_address=NULLIF($18,'')::inet
		WHERE id=$1 AND credential_hash=$2 AND status<>'disabled'
		RETURNING id,name,region,host(public_ip),COALESCE(host(tunnel_address),''),status,architecture,agent_version,last_heartbeat,config_version,applied_config_version,
			attempted_config_version,last_config_error,last_config_attempt,
			cpu_percent,memory_percent,load_one,disk_percent,network_rx_bps,network_tx_bps,active_connections,bytes_in,bytes_out`,
		nodeID, credentialHash, heartbeat.PublicIP, heartbeat.AgentVersion, heartbeat.ReceivedAt,
		heartbeat.CPUPercent, heartbeat.MemoryPercent, heartbeat.LoadOne, heartbeat.DiskPercent, heartbeat.NetworkRxBps, heartbeat.NetworkTxBps,
		heartbeat.ActiveConns, heartbeat.BytesIn, heartbeat.BytesOut,
		heartbeat.ConfigVersion, heartbeat.AttemptedConfigVersion, heartbeat.LastConfigError, heartbeat.TunnelAddress).Scan(
		&node.ID, &node.Name, &node.Region, &node.PublicIP, &node.TunnelAddress, &node.Status, &node.Architecture, &node.AgentVersion,
		&node.LastHeartbeat, &node.ConfigVersion, &node.AppliedConfigVersion, &node.AttemptedConfigVersion,
		&node.LastConfigError, &lastAttempt, &node.CPUPercent, &node.MemoryPercent, &node.LoadOne, &node.DiskPercent, &node.NetworkRxBps, &node.NetworkTxBps,
		&node.ActiveConns, &node.BytesIn, &node.BytesOut)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Node{}, store.ErrNotFound
	}
	if err != nil {
		return domain.Node{}, err
	}
	if lastAttempt.Valid {
		node.LastConfigAttempt = lastAttempt.Time
	}
	metricBucket := heartbeat.ReceivedAt.UTC().Truncate(time.Minute)
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO node_metric_samples(node_id,sampled_at,cpu_percent,memory_percent,load_one,disk_percent,network_rx_bps,network_tx_bps,active_connections,bytes_in,bytes_out)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT(node_id,sampled_at) DO UPDATE SET cpu_percent=EXCLUDED.cpu_percent,
			memory_percent=EXCLUDED.memory_percent,load_one=EXCLUDED.load_one,disk_percent=EXCLUDED.disk_percent,
			network_rx_bps=EXCLUDED.network_rx_bps,network_tx_bps=EXCLUDED.network_tx_bps,
			active_connections=EXCLUDED.active_connections,bytes_in=EXCLUDED.bytes_in,bytes_out=EXCLUDED.bytes_out`,
		nodeID, metricBucket, heartbeat.CPUPercent, heartbeat.MemoryPercent, heartbeat.LoadOne, heartbeat.DiskPercent, heartbeat.NetworkRxBps, heartbeat.NetworkTxBps,
		heartbeat.ActiveConns, heartbeat.BytesIn, heartbeat.BytesOut); err != nil {
		return domain.Node{}, err
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM node_metric_samples WHERE sampled_at<$1`, metricBucket.Add(-store.NodeMetricRetention)); err != nil {
		return domain.Node{}, err
	}
	for _, entry := range heartbeat.Logs {
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO agent_logs(node_id,event_id,level,component,message,occurred_at,received_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(node_id,event_id) DO NOTHING`,
			nodeID, entry.EventID, entry.Level, entry.Component, entry.Message, entry.OccurredAt, heartbeat.ReceivedAt); err != nil {
			return domain.Node{}, err
		}
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM agent_logs WHERE received_at<$1`, heartbeat.ReceivedAt.Add(-store.AgentLogRetention)); err != nil {
		return domain.Node{}, err
	}
	if heartbeat.ConnectionsComplete {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM active_connection_snapshots WHERE node_id=$1`, nodeID); err != nil {
			return domain.Node{}, err
		}
	}
	for _, connection := range heartbeat.Connections {
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO active_connection_snapshots(node_id,connection_id,rule_id,protocol,source_address,target_address,started_at,last_activity,bytes_in,bytes_out,observed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT(node_id,connection_id) DO UPDATE SET rule_id=EXCLUDED.rule_id,protocol=EXCLUDED.protocol,
				source_address=EXCLUDED.source_address,target_address=EXCLUDED.target_address,started_at=EXCLUDED.started_at,
				last_activity=EXCLUDED.last_activity,bytes_in=EXCLUDED.bytes_in,bytes_out=EXCLUDED.bytes_out,observed_at=EXCLUDED.observed_at`,
			nodeID, connection.ConnectionID, connection.RuleID, connection.Protocol, connection.SourceAddress, connection.TargetAddress,
			connection.StartedAt, connection.LastActivity, connection.BytesIn, connection.BytesOut, heartbeat.ReceivedAt); err != nil {
			return domain.Node{}, err
		}
	}
	if _, err := transaction.ExecContext(ctx, `
		DELETE FROM active_connection_snapshots WHERE node_id=$1 AND connection_id IN (
			SELECT connection_id FROM active_connection_snapshots WHERE node_id=$1
			ORDER BY observed_at DESC,last_activity DESC,connection_id OFFSET $2
		)`, nodeID, store.MaxStoredConnectionsPerNode); err != nil {
		return domain.Node{}, err
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE rule_runtime_stats SET active_connections=0,updated_at=$2 WHERE node_id=$1`, nodeID, heartbeat.ReceivedAt); err != nil {
		return domain.Node{}, err
	}
	for _, stats := range heartbeat.RuleStats {
		_, err := transaction.ExecContext(ctx, `
			INSERT INTO rule_runtime_stats(rule_id,node_id,active_connections,bytes_in,bytes_out,updated_at)
			SELECT id,$2,$3,$4,$5,$6 FROM forward_rules WHERE id=$1 AND ingress_node_id=$2
			ON CONFLICT(rule_id) DO UPDATE SET active_connections=EXCLUDED.active_connections,
				bytes_in=EXCLUDED.bytes_in,bytes_out=EXCLUDED.bytes_out,updated_at=EXCLUDED.updated_at`,
			stats.RuleID, nodeID, stats.ActiveConns, stats.BytesIn, stats.BytesOut, heartbeat.ReceivedAt)
		if err != nil {
			return domain.Node{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return domain.Node{}, err
	}
	return node, nil
}

func (postgres *Store) ConfigForAgent(ctx context.Context, nodeID, credentialHash string) (store.AgentConfig, error) {
	var config store.AgentConfig
	err := postgres.database.QueryRowContext(ctx, `SELECT config_version FROM nodes WHERE id=$1 AND credential_hash=$2 AND status<>'disabled'`, nodeID, credentialHash).Scan(&config.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return store.AgentConfig{}, store.ErrNotFound
	}
	if err != nil {
		return store.AgentConfig{}, err
	}
	rows, err := postgres.database.QueryContext(ctx, `
		SELECT r.id,r.name,r.protocol,r.mode,r.ingress_node_id,COALESCE(r.egress_node_id,''),r.listen_host,r.listen_port,
			r.target_host,r.target_port,COALESCE(host(e.tunnel_address),''),COALESCE(r.relay_port,0),
			r.enabled,r.bandwidth_kbps,r.max_connections,r.allow_cidrs,r.deny_cidrs,r.config_version,COALESCE(r.egress_config_version,0)
		FROM forward_rules r LEFT JOIN nodes e ON e.id=r.egress_node_id
		WHERE r.ingress_node_id=$1 OR (r.mode='relay' AND r.egress_node_id=$1) ORDER BY r.id`, nodeID)
	if err != nil {
		return store.AgentConfig{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var rule domain.ForwardRule
		var listenPort, targetPort, relayPort int
		var bandwidth int64
		var maxConnections int64
		var allowJSON, denyJSON []byte
		if err := rows.Scan(&rule.ID, &rule.Name, &rule.Protocol, &rule.Mode, &rule.IngressNodeID, &rule.EgressNodeID,
			&rule.ListenHost, &listenPort, &rule.TargetHost, &targetPort, &rule.RelayHost, &relayPort, &rule.Enabled, &bandwidth, &maxConnections,
			&allowJSON, &denyJSON, &rule.ConfigVersion, &rule.EgressConfigVersion); err != nil {
			return store.AgentConfig{}, err
		}
		rule.ListenPort = uint16(listenPort)
		rule.TargetPort = uint16(targetPort)
		rule.RelayPort = uint16(relayPort)
		rule.BandwidthKbps = uint64(bandwidth)
		rule.MaxConnections = uint32(maxConnections)
		if err := json.Unmarshal(allowJSON, &rule.AllowCIDRs); err != nil {
			return store.AgentConfig{}, fmt.Errorf("decode allow CIDRs for rule %s: %w", rule.ID, err)
		}
		if err := json.Unmarshal(denyJSON, &rule.DenyCIDRs); err != nil {
			return store.AgentConfig{}, fmt.Errorf("decode deny CIDRs for rule %s: %w", rule.ID, err)
		}
		config.Rules = append(config.Rules, rule)
	}
	return config, rows.Err()
}

func (postgres *Store) ListNodes(ctx context.Context) ([]domain.Node, error) {
	rows, err := postgres.database.QueryContext(ctx, `
		SELECT id,name,region,host(public_ip),COALESCE(host(tunnel_address),''),status,architecture,agent_version,last_heartbeat,config_version,applied_config_version,
			attempted_config_version,last_config_error,last_config_attempt,
			cpu_percent,memory_percent,load_one,disk_percent,network_rx_bps,network_tx_bps,active_connections,bytes_in,bytes_out
		FROM nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []domain.Node
	for rows.Next() {
		var node domain.Node
		var lastAttempt sql.NullTime
		if err := rows.Scan(&node.ID, &node.Name, &node.Region, &node.PublicIP, &node.TunnelAddress, &node.Status, &node.Architecture,
			&node.AgentVersion, &node.LastHeartbeat, &node.ConfigVersion, &node.AppliedConfigVersion,
			&node.AttemptedConfigVersion, &node.LastConfigError, &lastAttempt, &node.CPUPercent, &node.MemoryPercent,
			&node.LoadOne, &node.DiskPercent, &node.NetworkRxBps, &node.NetworkTxBps, &node.ActiveConns, &node.BytesIn, &node.BytesOut); err != nil {
			return nil, err
		}
		if lastAttempt.Valid {
			node.LastConfigAttempt = lastAttempt.Time
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (postgres *Store) CreateForwardRule(ctx context.Context, rule domain.ForwardRule) (domain.ForwardRule, error) {
	transaction, err := postgres.database.BeginTx(ctx, nil)
	if err != nil {
		return domain.ForwardRule{}, err
	}
	defer transaction.Rollback()
	if err := ensureListenerAvailable(ctx, transaction, rule, ""); err != nil {
		return domain.ForwardRule{}, err
	}
	for _, nodeID := range sortedUniqueNodeIDs(ruleNodeIDs(rule)) {
		version, err := bumpNodeConfig(ctx, transaction, nodeID)
		if err != nil {
			return domain.ForwardRule{}, err
		}
		if nodeID == rule.IngressNodeID {
			rule.ConfigVersion = version
		}
		if nodeID == rule.EgressNodeID {
			rule.EgressConfigVersion = version
		}
	}
	allowJSON, denyJSON, err := encodeCIDRs(rule)
	if err != nil {
		return domain.ForwardRule{}, err
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO forward_rules(id,name,protocol,mode,ingress_node_id,egress_node_id,listen_host,listen_port,
			target_host,target_port,relay_port,enabled,bandwidth_kbps,max_connections,allow_cidrs,deny_cidrs,config_version,egress_config_version,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$19)`,
		rule.ID, rule.Name, rule.Protocol, rule.Mode, rule.IngressNodeID, nullable(rule.EgressNodeID), rule.ListenHost,
		rule.ListenPort, rule.TargetHost, rule.TargetPort, nullablePort(rule.RelayPort), rule.Enabled, rule.BandwidthKbps, rule.MaxConnections,
		allowJSON, denyJSON, rule.ConfigVersion, nullableVersion(rule.EgressConfigVersion), time.Now().UTC())
	if err != nil {
		if uniqueViolation(err) {
			return domain.ForwardRule{}, store.ErrConflict
		}
		return domain.ForwardRule{}, err
	}
	if err := transaction.Commit(); err != nil {
		return domain.ForwardRule{}, err
	}
	return rule, nil
}

func (postgres *Store) UpdateForwardRule(ctx context.Context, rule domain.ForwardRule) (domain.ForwardRule, error) {
	transaction, err := postgres.database.BeginTx(ctx, nil)
	if err != nil {
		return domain.ForwardRule{}, err
	}
	defer transaction.Rollback()
	var previous domain.ForwardRule
	if err := transaction.QueryRowContext(ctx, `SELECT ingress_node_id,COALESCE(egress_node_id,''),mode FROM forward_rules WHERE id=$1 FOR UPDATE`, rule.ID).
		Scan(&previous.IngressNodeID, &previous.EgressNodeID, &previous.Mode); errors.Is(err, sql.ErrNoRows) {
		return domain.ForwardRule{}, store.ErrNotFound
	} else if err != nil {
		return domain.ForwardRule{}, err
	}
	if err := ensureListenerAvailable(ctx, transaction, rule, rule.ID); err != nil {
		return domain.ForwardRule{}, err
	}
	affected := make(map[string]struct{})
	for _, nodeID := range append(ruleNodeIDs(previous), ruleNodeIDs(rule)...) {
		affected[nodeID] = struct{}{}
	}
	versions := make(map[string]uint64, len(affected))
	for _, nodeID := range sortedNodeIDSet(affected) {
		version, err := bumpNodeConfig(ctx, transaction, nodeID)
		if err != nil {
			return domain.ForwardRule{}, err
		}
		versions[nodeID] = version
	}
	rule.ConfigVersion = versions[rule.IngressNodeID]
	if rule.Mode == domain.ForwardRelay {
		rule.EgressConfigVersion = versions[rule.EgressNodeID]
	}
	allowJSON, denyJSON, err := encodeCIDRs(rule)
	if err != nil {
		return domain.ForwardRule{}, err
	}
	_, err = transaction.ExecContext(ctx, `
		UPDATE forward_rules SET name=$2,protocol=$3,mode=$4,ingress_node_id=$5,egress_node_id=$6,listen_host=$7,
			listen_port=$8,target_host=$9,target_port=$10,relay_port=$11,enabled=$12,bandwidth_kbps=$13,max_connections=$14,
			allow_cidrs=$15,deny_cidrs=$16,config_version=$17,egress_config_version=$18,updated_at=$19 WHERE id=$1`,
		rule.ID, rule.Name, rule.Protocol, rule.Mode, rule.IngressNodeID, nullable(rule.EgressNodeID), rule.ListenHost,
		rule.ListenPort, rule.TargetHost, rule.TargetPort, nullablePort(rule.RelayPort), rule.Enabled, rule.BandwidthKbps, rule.MaxConnections,
		allowJSON, denyJSON, rule.ConfigVersion, nullableVersion(rule.EgressConfigVersion), time.Now().UTC())
	if err != nil {
		if uniqueViolation(err) {
			return domain.ForwardRule{}, store.ErrConflict
		}
		return domain.ForwardRule{}, err
	}
	if err := transaction.Commit(); err != nil {
		return domain.ForwardRule{}, err
	}
	return rule, nil
}

func (postgres *Store) DeleteForwardRule(ctx context.Context, id string) error {
	transaction, err := postgres.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	var rule domain.ForwardRule
	if err := transaction.QueryRowContext(ctx, `SELECT ingress_node_id,COALESCE(egress_node_id,''),mode FROM forward_rules WHERE id=$1 FOR UPDATE`, id).
		Scan(&rule.IngressNodeID, &rule.EgressNodeID, &rule.Mode); errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	} else if err != nil {
		return err
	}
	for _, nodeID := range sortedUniqueNodeIDs(ruleNodeIDs(rule)) {
		if _, err := bumpNodeConfig(ctx, transaction, nodeID); err != nil {
			return err
		}
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM forward_rules WHERE id=$1`, id); err != nil {
		return err
	}
	return transaction.Commit()
}

func ruleNodeIDs(rule domain.ForwardRule) []string {
	result := []string{rule.IngressNodeID}
	if rule.Mode == domain.ForwardRelay && rule.EgressNodeID != "" && rule.EgressNodeID != rule.IngressNodeID {
		result = append(result, rule.EgressNodeID)
	}
	return result
}

func sortedUniqueNodeIDs(nodeIDs []string) []string {
	set := make(map[string]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		set[nodeID] = struct{}{}
	}
	return sortedNodeIDSet(set)
}

func sortedNodeIDSet(set map[string]struct{}) []string {
	result := make([]string, 0, len(set))
	for nodeID := range set {
		result = append(result, nodeID)
	}
	sort.Strings(result)
	return result
}

func (postgres *Store) ListForwardRules(ctx context.Context) ([]domain.ForwardRule, error) {
	rows, err := postgres.database.QueryContext(ctx, `
		SELECT r.id,r.name,r.protocol,r.mode,r.ingress_node_id,COALESCE(r.egress_node_id,''),r.listen_host,r.listen_port,
			r.target_host,r.target_port,COALESCE(host(e.tunnel_address),''),COALESCE(r.relay_port,0),r.enabled,r.bandwidth_kbps,r.max_connections,r.allow_cidrs,r.deny_cidrs,r.config_version,COALESCE(r.egress_config_version,0),
			COALESCE(s.active_connections,0),COALESCE(s.bytes_in,0),COALESCE(s.bytes_out,0),s.updated_at
		FROM forward_rules r LEFT JOIN rule_runtime_stats s ON s.rule_id=r.id LEFT JOIN nodes e ON e.id=r.egress_node_id ORDER BY r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []domain.ForwardRule
	for rows.Next() {
		rule, err := scanForwardRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func bumpNodeConfig(ctx context.Context, transaction *sql.Tx, nodeID string) (uint64, error) {
	var version uint64
	err := transaction.QueryRowContext(ctx, `UPDATE nodes SET config_version=config_version+1 WHERE id=$1 AND status<>'disabled' RETURNING config_version`, nodeID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrNotFound
	}
	return version, err
}

func ensureListenerAvailable(ctx context.Context, transaction *sql.Tx, rule domain.ForwardRule, excludedID string) error {
	lockKey := fmt.Sprintf("%d:%s:%d:%s:%d", len(rule.IngressNodeID), rule.IngressNodeID, len(rule.ListenHost), rule.ListenHost, rule.ListenPort)
	if _, err := transaction.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return err
	}
	var exists bool
	err := transaction.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM forward_rules
			WHERE ingress_node_id=$1 AND listen_host=$2 AND listen_port=$3 AND id<>$4
			AND (protocol=$5 OR protocol='tcp_udp' OR $5='tcp_udp')
		)`, rule.IngressNodeID, rule.ListenHost, rule.ListenPort, excludedID, rule.Protocol).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return store.ErrConflict
	}
	return nil
}

func encodeCIDRs(rule domain.ForwardRule) ([]byte, []byte, error) {
	allow := rule.AllowCIDRs
	deny := rule.DenyCIDRs
	if allow == nil {
		allow = []string{}
	}
	if deny == nil {
		deny = []string{}
	}
	allowJSON, err := json.Marshal(allow)
	if err != nil {
		return nil, nil, err
	}
	denyJSON, err := json.Marshal(deny)
	return allowJSON, denyJSON, err
}

type rowScanner interface {
	Scan(...interface{}) error
}

func scanForwardRule(row rowScanner) (domain.ForwardRule, error) {
	var rule domain.ForwardRule
	var listenPort, targetPort, relayPort int
	var bandwidth, maxConnections int64
	var allowJSON, denyJSON []byte
	var runtimeUpdated sql.NullTime
	err := row.Scan(&rule.ID, &rule.Name, &rule.Protocol, &rule.Mode, &rule.IngressNodeID, &rule.EgressNodeID,
		&rule.ListenHost, &listenPort, &rule.TargetHost, &targetPort, &rule.RelayHost, &relayPort, &rule.Enabled, &bandwidth, &maxConnections,
		&allowJSON, &denyJSON, &rule.ConfigVersion, &rule.EgressConfigVersion, &rule.ActiveConns, &rule.BytesIn, &rule.BytesOut, &runtimeUpdated)
	if err != nil {
		return domain.ForwardRule{}, err
	}
	rule.ListenPort, rule.TargetPort = uint16(listenPort), uint16(targetPort)
	rule.RelayPort = uint16(relayPort)
	rule.BandwidthKbps, rule.MaxConnections = uint64(bandwidth), uint32(maxConnections)
	if runtimeUpdated.Valid {
		rule.RuntimeUpdated = runtimeUpdated.Time
	}
	if err := json.Unmarshal(allowJSON, &rule.AllowCIDRs); err != nil {
		return domain.ForwardRule{}, err
	}
	if err := json.Unmarshal(denyJSON, &rule.DenyCIDRs); err != nil {
		return domain.ForwardRule{}, err
	}
	return rule, nil
}

func uniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

func (postgres *Store) RecordAudit(ctx context.Context, event store.AuditEvent) error {
	details, err := json.Marshal(event.Details)
	if err != nil {
		return err
	}
	_, err = postgres.database.ExecContext(ctx, `INSERT INTO audit_events(id,actor_type,actor_id,action,target_type,target_id,remote_ip,details,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		event.ID, event.ActorType, nullable(event.ActorID), event.Action, event.TargetType, nullable(event.TargetID), nullable(event.RemoteIP), details, event.CreatedAt)
	return err
}

func (postgres *Store) ListAudit(ctx context.Context, limit int, before time.Time) ([]store.AuditEvent, error) {
	query := `SELECT id,actor_type,COALESCE(actor_id,''),action,target_type,COALESCE(target_id,''),COALESCE(remote_ip,''),details,created_at
		FROM audit_events`
	arguments := []interface{}{}
	if !before.IsZero() {
		query += ` WHERE created_at<$1 ORDER BY created_at DESC,id DESC LIMIT $2`
		arguments = append(arguments, before, limit)
	} else {
		query += ` ORDER BY created_at DESC,id DESC LIMIT $1`
		arguments = append(arguments, limit)
	}
	rows, err := postgres.database.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]store.AuditEvent, 0, limit)
	for rows.Next() {
		var event store.AuditEvent
		var details []byte
		if err := rows.Scan(&event.ID, &event.ActorType, &event.ActorID, &event.Action, &event.TargetType, &event.TargetID,
			&event.RemoteIP, &details, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(details, &event.Details); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func (postgres *Store) ListAgentLogs(ctx context.Context, filter store.AgentLogFilter) ([]store.AgentLog, error) {
	query := `SELECT node_id,event_id,level,component,message,occurred_at,received_at FROM agent_logs WHERE TRUE`
	arguments := make([]interface{}, 0, 4)
	addArgument := func(value interface{}) string {
		arguments = append(arguments, value)
		return fmt.Sprintf("$%d", len(arguments))
	}
	if filter.NodeID != "" {
		query += ` AND node_id=` + addArgument(filter.NodeID)
	}
	if filter.Level != "" {
		query += ` AND level=` + addArgument(filter.Level)
	}
	if !filter.Before.IsZero() {
		query += ` AND occurred_at<` + addArgument(filter.Before)
	}
	query += ` ORDER BY occurred_at DESC,event_id DESC LIMIT ` + addArgument(filter.Limit)
	rows, err := postgres.database.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]store.AgentLog, 0, filter.Limit)
	for rows.Next() {
		var entry store.AgentLog
		if err := rows.Scan(&entry.NodeID, &entry.EventID, &entry.Level, &entry.Component, &entry.Message, &entry.OccurredAt, &entry.ReceivedAt); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (postgres *Store) ListActiveConnections(ctx context.Context) ([]store.ActiveConnection, error) {
	rows, err := postgres.database.QueryContext(ctx, `
		SELECT node_id,connection_id,rule_id,protocol,source_address,target_address,started_at,last_activity,bytes_in,bytes_out,observed_at
		FROM active_connection_snapshots ORDER BY last_activity DESC,node_id,connection_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []store.ActiveConnection
	for rows.Next() {
		var connection store.ActiveConnection
		if err := rows.Scan(&connection.NodeID, &connection.ConnectionID, &connection.RuleID, &connection.Protocol,
			&connection.SourceAddress, &connection.TargetAddress, &connection.StartedAt, &connection.LastActivity,
			&connection.BytesIn, &connection.BytesOut, &connection.ObservedAt); err != nil {
			return nil, err
		}
		result = append(result, connection)
	}
	return result, rows.Err()
}

func (postgres *Store) TrafficHistory(ctx context.Context, from, to time.Time, interval time.Duration) ([]store.TrafficPoint, error) {
	intervalSeconds := int64(interval / time.Second)
	rows, err := postgres.database.QueryContext(ctx, `
		WITH selected AS (
			SELECT node_id,sampled_at,bytes_in,bytes_out FROM node_metric_samples WHERE sampled_at>=$1 AND sampled_at<=$2
		), prior AS (
			SELECT DISTINCT ON (node_id) node_id,sampled_at,bytes_in,bytes_out
			FROM node_metric_samples WHERE sampled_at<$1 ORDER BY node_id,sampled_at DESC
		), ordered AS (
			SELECT * FROM prior UNION ALL SELECT * FROM selected
		), deltas AS (
			SELECT node_id,sampled_at,bytes_in,bytes_out,
				LAG(bytes_in) OVER (PARTITION BY node_id ORDER BY sampled_at) AS previous_in,
				LAG(bytes_out) OVER (PARTITION BY node_id ORDER BY sampled_at) AS previous_out
			FROM ordered
		)
		SELECT date_bin(($3::bigint * interval '1 second'),sampled_at,TIMESTAMPTZ '1970-01-01'),
			SUM(CASE WHEN previous_in IS NULL THEN 0 WHEN bytes_in>=previous_in THEN bytes_in-previous_in ELSE bytes_in END),
			SUM(CASE WHEN previous_out IS NULL THEN 0 WHEN bytes_out>=previous_out THEN bytes_out-previous_out ELSE bytes_out END)
		FROM deltas WHERE sampled_at>=$1 GROUP BY 1 ORDER BY 1`, from, to, intervalSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []store.TrafficPoint
	for rows.Next() {
		var point store.TrafficPoint
		if err := rows.Scan(&point.Time, &point.UploadBytes, &point.DownloadBytes); err != nil {
			return nil, err
		}
		result = append(result, point)
	}
	return result, rows.Err()
}

func (postgres *Store) NodeMetricHistory(ctx context.Context, nodeID string, from, to time.Time, interval time.Duration) ([]store.NodeMetricPoint, error) {
	intervalSeconds := int64(interval / time.Second)
	rows, err := postgres.database.QueryContext(ctx, `
		WITH selected AS (
			SELECT sampled_at,cpu_percent,memory_percent,load_one,disk_percent,network_rx_bps,network_tx_bps,active_connections,bytes_in,bytes_out
			FROM node_metric_samples WHERE node_id=$1 AND sampled_at>=$2 AND sampled_at<=$3
		), prior AS (
			SELECT sampled_at,cpu_percent,memory_percent,load_one,disk_percent,network_rx_bps,network_tx_bps,active_connections,bytes_in,bytes_out
			FROM node_metric_samples WHERE node_id=$1 AND sampled_at<$2 ORDER BY sampled_at DESC LIMIT 1
		), ordered AS (
			SELECT * FROM prior UNION ALL SELECT * FROM selected
		), deltas AS (
			SELECT *,LAG(bytes_in) OVER (ORDER BY sampled_at) AS previous_in,
				LAG(bytes_out) OVER (ORDER BY sampled_at) AS previous_out
			FROM ordered
		)
		SELECT date_bin(($4::bigint * interval '1 second'),sampled_at,TIMESTAMPTZ '1970-01-01'),
			AVG(cpu_percent),AVG(memory_percent),AVG(load_one),AVG(disk_percent),
			ROUND(AVG(network_rx_bps))::bigint,ROUND(AVG(network_tx_bps))::bigint,
			(ARRAY_AGG(active_connections ORDER BY sampled_at DESC))[1],
			SUM(CASE WHEN previous_in IS NULL THEN 0 WHEN bytes_in>=previous_in THEN bytes_in-previous_in ELSE bytes_in END),
			SUM(CASE WHEN previous_out IS NULL THEN 0 WHEN bytes_out>=previous_out THEN bytes_out-previous_out ELSE bytes_out END)
		FROM deltas WHERE sampled_at>=$2 GROUP BY 1 ORDER BY 1`, nodeID, from, to, intervalSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []store.NodeMetricPoint
	for rows.Next() {
		var point store.NodeMetricPoint
		if err := rows.Scan(&point.Time, &point.CPUPercent, &point.MemoryPercent, &point.LoadOne, &point.DiskPercent,
			&point.NetworkRxBps, &point.NetworkTxBps, &point.ActiveConns,
			&point.UploadBytes, &point.DownloadBytes); err != nil {
			return nil, err
		}
		result = append(result, point)
	}
	return result, rows.Err()
}

func nullable(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}

func nullablePort(value uint16) interface{} {
	if value == 0 {
		return nil
	}
	return value
}

func nullableVersion(value uint64) interface{} {
	if value == 0 {
		return nil
	}
	return value
}
