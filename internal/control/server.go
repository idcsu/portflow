package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"portflow/internal/auth"
	"portflow/internal/domain"
	"portflow/internal/store"
)

const sessionCookie = "portflow_session"

const (
	defaultSessionTTL          = 12 * time.Hour
	loginFailureLimit          = 5
	loginFailureWindow         = 15 * time.Minute
	heartbeatInterval          = 15 * time.Second
	nodeOfflineThreshold       = 45 * time.Second
	maxConnectionsPerHeartbeat = 2000
	maxAgentLogsPerHeartbeat   = 100
)

type BuildInfo struct {
	Version string
}

type Options struct {
	Build         BuildInfo
	Store         store.Store
	StorageMode   string
	SecureCookies bool
	SessionTTL    time.Duration
	Now           func() time.Time
}

type server struct {
	startedAt     time.Time
	build         BuildInfo
	store         store.Store
	storageMode   string
	secureCookies bool
	sessionTTL    time.Duration
	now           func() time.Time
	loginMu       sync.Mutex
	loginAttempts map[string]loginAttempt
}

type loginAttempt struct {
	count       int
	windowStart time.Time
}

func NewServer(options Options) http.Handler {
	if options.Store == nil {
		panic("control server requires a store")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.SessionTTL == 0 {
		options.SessionTTL = defaultSessionTTL
	}
	if options.StorageMode == "" {
		options.StorageMode = "memory"
	}
	application := &server{
		startedAt: options.Now().UTC(), build: options.Build, store: options.Store,
		storageMode: options.StorageMode, secureCookies: options.SecureCookies, sessionTTL: options.SessionTTL, now: options.Now,
		loginAttempts: make(map[string]loginAttempt),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", application.health)
	mux.HandleFunc("/api/v1/setup/admin", application.setupAdmin)
	mux.HandleFunc("/api/v1/auth/login", application.login)
	mux.HandleFunc("/api/v1/auth/logout", application.logout)
	mux.HandleFunc("/api/v1/auth/me", application.me)
	mux.HandleFunc("/api/v1/users", application.users)
	mux.HandleFunc("/api/v1/users/", application.user)
	mux.HandleFunc("/api/v1/enrollment-tokens", application.enrollmentTokens)
	mux.HandleFunc("/api/v1/agent/enroll", application.enrollAgent)
	mux.HandleFunc("/api/v1/agent/heartbeat", application.agentHeartbeat)
	mux.HandleFunc("/api/v1/agent/config", application.agentConfig)
	mux.HandleFunc("/api/v1/nodes", application.nodes)
	mux.HandleFunc("/api/v1/nodes/", application.nodeDetail)
	mux.HandleFunc("/api/v1/forward-rules", application.forwardRules)
	mux.HandleFunc("/api/v1/forward-rules/", application.forwardRule)
	mux.HandleFunc("/api/v1/dashboard/summary", application.dashboardSummary)
	mux.HandleFunc("/api/v1/metrics/traffic", application.trafficHistory)
	mux.HandleFunc("/api/v1/audit-events", application.auditEvents)
	mux.HandleFunc("/api/v1/agent-logs", application.agentLogs)
	mux.HandleFunc("/api/v1/connections", application.activeConnections)
	mux.HandleFunc("/api/v1/system/settings", application.systemSettings)
	return securityHeaders(requestLog(mux))
}

func (server *server) health(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := server.store.Health(ctx); err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]interface{}{
			"status": "degraded", "version": server.build.Version, "startedAt": server.startedAt, "time": server.now().UTC(),
		})
		return
	}
	writeJSON(response, http.StatusOK, map[string]interface{}{
		"status": "ok", "version": server.build.Version, "startedAt": server.startedAt, "time": server.now().UTC(),
	})
}

func (server *server) setupAdmin(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	username, err := auth.NormalizeUsername(input.Username)
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_username", err.Error())
		return
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_password", err.Error())
		return
	}
	userID, err := auth.NewID("usr")
	if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error", "unable to create administrator")
		return
	}
	user := store.User{ID: userID, Username: username, PasswordHash: passwordHash, Role: store.RoleAdmin, CreatedAt: server.now().UTC()}
	if err := server.store.CreateInitialUser(request.Context(), user); err != nil {
		if errors.Is(err, store.ErrBootstrapCompleted) {
			writeError(response, http.StatusConflict, "setup_completed", "initial administrator already exists")
			return
		}
		log.Printf("create initial administrator: %v", err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to create administrator")
		return
	}
	server.audit(request, "user", user.ID, "user.bootstrap", "user", user.ID, nil)
	writeJSON(response, http.StatusCreated, publicUser(user))
}

func (server *server) login(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	username, err := auth.NormalizeUsername(input.Username)
	if err != nil {
		server.invalidCredentials(response)
		return
	}
	attemptKey := remoteIP(request) + "\x00" + username
	if server.loginRateLimited(attemptKey) {
		response.Header().Set("Retry-After", strconv.Itoa(int(loginFailureWindow/time.Second)))
		writeError(response, http.StatusTooManyRequests, "login_rate_limited", "too many failed login attempts; try again later")
		return
	}
	user, err := server.store.UserByUsername(request.Context(), username)
	if err != nil || user.Disabled || !auth.CheckPassword(user.PasswordHash, input.Password) {
		server.recordLoginFailure(attemptKey)
		server.audit(request, "anonymous", "", "auth.login_failed", "user", "", map[string]interface{}{"username": username})
		server.invalidCredentials(response)
		return
	}
	rawToken, err := auth.NewToken(32)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error", "unable to create session")
		return
	}
	sessionID, err := auth.NewID("ses")
	if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error", "unable to create session")
		return
	}
	now := server.now().UTC()
	session := store.Session{ID: sessionID, UserID: user.ID, TokenHash: auth.TokenHash(rawToken), CreatedAt: now, ExpiresAt: now.Add(server.sessionTTL)}
	if err := server.store.CreateSession(request.Context(), session); err != nil {
		log.Printf("create session: %v", err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to create session")
		return
	}
	server.clearLoginFailures(attemptKey)
	server.setSessionCookie(response, rawToken, session.ExpiresAt)
	server.audit(request, "user", user.ID, "auth.login", "session", session.ID, nil)
	writeJSON(response, http.StatusOK, map[string]interface{}{"user": publicUser(user), "expiresAt": session.ExpiresAt})
}

func (server *server) loginRateLimited(key string) bool {
	server.loginMu.Lock()
	defer server.loginMu.Unlock()
	attempt, exists := server.loginAttempts[key]
	if !exists {
		return false
	}
	if server.now().Sub(attempt.windowStart) >= loginFailureWindow {
		delete(server.loginAttempts, key)
		return false
	}
	return attempt.count >= loginFailureLimit
}

func (server *server) recordLoginFailure(key string) {
	server.loginMu.Lock()
	defer server.loginMu.Unlock()
	attempt, exists := server.loginAttempts[key]
	if !exists || server.now().Sub(attempt.windowStart) >= loginFailureWindow {
		server.loginAttempts[key] = loginAttempt{count: 1, windowStart: server.now()}
		return
	}
	attempt.count++
	server.loginAttempts[key] = attempt
}

func (server *server) clearLoginFailures(key string) {
	server.loginMu.Lock()
	defer server.loginMu.Unlock()
	delete(server.loginAttempts, key)
}

func (server *server) invalidCredentials(response http.ResponseWriter) {
	writeError(response, http.StatusUnauthorized, "invalid_credentials", "username or password is incorrect")
}

func (server *server) logout(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	rawToken := sessionToken(request)
	if rawToken != "" {
		_, user, _ := server.store.SessionUserByTokenHash(request.Context(), auth.TokenHash(rawToken), server.now().UTC())
		_ = server.store.DeleteSessionByTokenHash(request.Context(), auth.TokenHash(rawToken))
		if user.ID != "" {
			server.audit(request, "user", user.ID, "auth.logout", "session", "", nil)
		}
	}
	http.SetCookie(response, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: server.secureCookies, SameSite: http.SameSiteStrictMode})
	response.WriteHeader(http.StatusNoContent)
}

func (server *server) me(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	_, user, ok := server.requireUser(response, request, "")
	if !ok {
		return
	}
	writeJSON(response, http.StatusOK, publicUser(user))
}

func (server *server) users(response http.ResponseWriter, request *http.Request) {
	_, actor, ok := server.requireUser(response, request, store.RoleAdmin)
	if !ok {
		return
	}
	switch request.Method {
	case http.MethodGet:
		users, err := server.store.ListUsers(request.Context())
		if err != nil {
			log.Printf("list users: %v", err)
			writeError(response, http.StatusInternalServerError, "storage_error", "unable to load users")
			return
		}
		items := make([]map[string]interface{}, 0, len(users))
		for _, user := range users {
			items = append(items, publicUser(user))
		}
		writeJSON(response, http.StatusOK, map[string]interface{}{"items": items})
	case http.MethodPost:
		var input struct {
			Username string     `json:"username"`
			Password string     `json:"password"`
			Role     store.Role `json:"role"`
		}
		if !decodeJSON(response, request, &input) {
			return
		}
		username, err := auth.NormalizeUsername(input.Username)
		if err != nil {
			writeError(response, http.StatusUnprocessableEntity, "invalid_username", err.Error())
			return
		}
		if input.Role == "" {
			input.Role = store.RoleMember
		}
		if input.Role != store.RoleAdmin && input.Role != store.RoleMember {
			writeError(response, http.StatusUnprocessableEntity, "invalid_role", "role must be admin or member")
			return
		}
		passwordHash, err := auth.HashPassword(input.Password)
		if err != nil {
			writeError(response, http.StatusUnprocessableEntity, "invalid_password", err.Error())
			return
		}
		userID, err := auth.NewID("usr")
		if err != nil {
			writeError(response, http.StatusInternalServerError, "internal_error", "unable to create user")
			return
		}
		created := store.User{ID: userID, Username: username, PasswordHash: passwordHash, Role: input.Role, CreatedAt: server.now().UTC()}
		if err := server.store.CreateUser(request.Context(), created); err != nil {
			if errors.Is(err, store.ErrConflict) {
				writeError(response, http.StatusConflict, "username_conflict", "username already exists")
				return
			}
			log.Printf("create user: %v", err)
			writeError(response, http.StatusInternalServerError, "storage_error", "unable to create user")
			return
		}
		server.audit(request, "user", actor.ID, "user.create", "user", created.ID, map[string]interface{}{"username": created.Username, "role": created.Role})
		writeJSON(response, http.StatusCreated, publicUser(created))
	default:
		response.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (server *server) user(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut {
		methodNotAllowed(response, http.MethodPut)
		return
	}
	_, actor, ok := server.requireUser(response, request, store.RoleAdmin)
	if !ok {
		return
	}
	userID := strings.TrimPrefix(request.URL.Path, "/api/v1/users/")
	if userID == "" || strings.Contains(userID, "/") || len(userID) > 128 {
		writeError(response, http.StatusNotFound, "not_found", "user not found")
		return
	}
	if userID == actor.ID {
		writeError(response, http.StatusConflict, "self_modification", "use another administrator account to change your own role or status")
		return
	}
	var input struct {
		Role     store.Role `json:"role"`
		Disabled bool       `json:"disabled"`
		Password string     `json:"password"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	if input.Role != store.RoleAdmin && input.Role != store.RoleMember {
		writeError(response, http.StatusUnprocessableEntity, "invalid_role", "role must be admin or member")
		return
	}
	passwordHash := ""
	if input.Password != "" {
		var err error
		passwordHash, err = auth.HashPassword(input.Password)
		if err != nil {
			writeError(response, http.StatusUnprocessableEntity, "invalid_password", err.Error())
			return
		}
	}
	previous, err := server.store.UserByID(request.Context(), userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(response, http.StatusNotFound, "not_found", "user not found")
			return
		}
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load user")
		return
	}
	updated, err := server.store.UpdateUser(request.Context(), userID, store.UserUpdate{Role: input.Role, Disabled: input.Disabled, PasswordHash: passwordHash})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(response, http.StatusNotFound, "not_found", "user not found")
			return
		}
		if errors.Is(err, store.ErrLastAdministrator) {
			writeError(response, http.StatusConflict, "last_administrator", "at least one active administrator is required")
			return
		}
		log.Printf("update user id=%s: %v", userID, err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to update user")
		return
	}
	server.audit(request, "user", actor.ID, "user.update", "user", updated.ID, map[string]interface{}{
		"username": updated.Username, "previousRole": previous.Role, "role": updated.Role,
		"previousDisabled": previous.Disabled, "disabled": updated.Disabled, "passwordReset": passwordHash != "",
	})
	writeJSON(response, http.StatusOK, publicUser(updated))
}

func (server *server) enrollmentTokens(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	_, user, ok := server.requireUser(response, request, store.RoleAdmin)
	if !ok {
		return
	}
	var input struct {
		Name      string `json:"name"`
		ExpiresIn int    `json:"expiresInMinutes"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		writeError(response, http.StatusUnprocessableEntity, "invalid_name", "token name is required and must not exceed 80 characters")
		return
	}
	if input.ExpiresIn == 0 {
		input.ExpiresIn = 30
	}
	if input.ExpiresIn < 5 || input.ExpiresIn > 1440 {
		writeError(response, http.StatusUnprocessableEntity, "invalid_expiration", "expiration must be between 5 and 1440 minutes")
		return
	}
	rawToken, err := auth.NewToken(32)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error", "unable to create enrollment token")
		return
	}
	tokenID, err := auth.NewID("ent")
	if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error", "unable to create enrollment token")
		return
	}
	now := server.now().UTC()
	token := store.EnrollmentToken{ID: tokenID, Name: input.Name, TokenHash: auth.TokenHash(rawToken), CreatedBy: user.ID, CreatedAt: now, ExpiresAt: now.Add(time.Duration(input.ExpiresIn) * time.Minute)}
	if err := server.store.CreateEnrollmentToken(request.Context(), token); err != nil {
		log.Printf("create enrollment token: %v", err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to create enrollment token")
		return
	}
	server.audit(request, "user", user.ID, "enrollment_token.create", "enrollment_token", token.ID, map[string]interface{}{"name": token.Name})
	writeJSON(response, http.StatusCreated, map[string]interface{}{"id": token.ID, "name": token.Name, "token": rawToken, "expiresAt": token.ExpiresAt})
}

func (server *server) enrollAgent(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct {
		Token        string `json:"token"`
		Name         string `json:"name"`
		Region       string `json:"region"`
		Architecture string `json:"architecture"`
		AgentVersion string `json:"agentVersion"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Region = strings.TrimSpace(input.Region)
	input.Architecture = strings.TrimSpace(input.Architecture)
	input.AgentVersion = strings.TrimSpace(input.AgentVersion)
	if input.Token == "" || input.Name == "" || len(input.Name) > 80 || input.Architecture == "" || len(input.Architecture) > 32 || input.AgentVersion == "" || len(input.AgentVersion) > 32 || len(input.Region) > 80 {
		writeError(response, http.StatusUnprocessableEntity, "invalid_agent", "token, valid name, architecture, and agent version are required")
		return
	}
	rawCredential, err := auth.NewToken(48)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error", "unable to create agent identity")
		return
	}
	agentID, err := auth.NewID("nod")
	if err != nil {
		writeError(response, http.StatusInternalServerError, "internal_error", "unable to create agent identity")
		return
	}
	now := server.now().UTC()
	agent := store.AgentIdentity{ID: agentID, Name: input.Name, Region: input.Region, PublicIP: remoteIP(request), Architecture: input.Architecture,
		AgentVersion: input.AgentVersion, CredentialHash: auth.TokenHash(rawCredential), CreatedAt: now}
	node, err := server.store.EnrollAgent(request.Context(), auth.TokenHash(input.Token), now, agent)
	if err != nil {
		if errors.Is(err, store.ErrTokenInvalid) {
			writeError(response, http.StatusUnauthorized, "invalid_enrollment_token", "enrollment token is invalid, expired, or already used")
			return
		}
		log.Printf("enroll agent: %v", err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to enroll agent")
		return
	}
	server.audit(request, "agent", node.ID, "agent.enroll", "node", node.ID, map[string]interface{}{"name": node.Name})
	writeJSON(response, http.StatusCreated, map[string]interface{}{"node": node, "credential": rawCredential})
}

func (server *server) agentHeartbeat(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	nodeID, credential, ok := requireAgentCredentials(response, request)
	if !ok {
		return
	}
	var input struct {
		AgentVersion           string  `json:"agentVersion"`
		TunnelAddress          string  `json:"tunnelAddress"`
		ConfigVersion          uint64  `json:"configVersion"`
		AttemptedConfigVersion uint64  `json:"attemptedConfigVersion"`
		LastConfigError        string  `json:"lastConfigError"`
		CPUPercent             float64 `json:"cpuPercent"`
		MemoryPercent          float64 `json:"memoryPercent"`
		LoadOne                float64 `json:"loadOne"`
		DiskPercent            float64 `json:"diskPercent"`
		NetworkRxBps           uint64  `json:"networkRxBps"`
		NetworkTxBps           uint64  `json:"networkTxBps"`
		ActiveConns            uint64  `json:"activeConnections"`
		BytesIn                uint64  `json:"bytesIn"`
		BytesOut               uint64  `json:"bytesOut"`
		RuleStats              []struct {
			RuleID      string `json:"ruleId"`
			ActiveConns uint64 `json:"activeConnections"`
			BytesIn     uint64 `json:"bytesIn"`
			BytesOut    uint64 `json:"bytesOut"`
		} `json:"ruleStats"`
		Connections []struct {
			ID            string    `json:"id"`
			RuleID        string    `json:"ruleId"`
			Protocol      string    `json:"protocol"`
			SourceAddress string    `json:"sourceAddress"`
			TargetAddress string    `json:"targetAddress"`
			StartedAt     time.Time `json:"startedAt"`
			LastActivity  time.Time `json:"lastActivity"`
			BytesIn       uint64    `json:"bytesIn"`
			BytesOut      uint64    `json:"bytesOut"`
		} `json:"connections"`
		ConnectionsComplete bool `json:"connectionsComplete"`
		Logs                []struct {
			ID         string    `json:"id"`
			Level      string    `json:"level"`
			Component  string    `json:"component"`
			Message    string    `json:"message"`
			OccurredAt time.Time `json:"occurredAt"`
		} `json:"logs"`
	}
	if !decodeJSON(response, request, &input) {
		return
	}
	input.AgentVersion = strings.TrimSpace(input.AgentVersion)
	input.TunnelAddress = strings.TrimSpace(input.TunnelAddress)
	const maxSignedCounter = uint64(1<<63 - 1)
	if input.AttemptedConfigVersion == 0 {
		input.AttemptedConfigVersion = input.ConfigVersion
	}
	if input.AgentVersion == "" || len(input.AgentVersion) > 32 || input.ConfigVersion == 0 ||
		input.AttemptedConfigVersion == 0 || len(input.LastConfigError) > 2000 || len(input.RuleStats) > 10000 || len(input.Connections) > maxConnectionsPerHeartbeat || len(input.Logs) > maxAgentLogsPerHeartbeat || invalidPercent(input.CPUPercent) ||
		invalidPercent(input.MemoryPercent) || invalidPercent(input.DiskPercent) || input.LoadOne < 0 || math.IsNaN(input.LoadOne) || math.IsInf(input.LoadOne, 0) {
		writeError(response, http.StatusUnprocessableEntity, "invalid_heartbeat", "heartbeat metrics are invalid")
		return
	}
	if input.ActiveConns > maxSignedCounter || input.BytesIn > maxSignedCounter || input.BytesOut > maxSignedCounter ||
		input.NetworkRxBps > maxSignedCounter || input.NetworkTxBps > maxSignedCounter {
		writeError(response, http.StatusUnprocessableEntity, "invalid_heartbeat", "heartbeat counters exceed the supported range")
		return
	}
	if input.TunnelAddress != "" && !domain.ValidTunnelAddress(input.TunnelAddress) {
		writeError(response, http.StatusUnprocessableEntity, "invalid_heartbeat", "tunnel address must be a private IPv4 address")
		return
	}
	ruleStats := make([]store.RuleRuntimeStats, 0, len(input.RuleStats))
	seenRuleStats := make(map[string]struct{}, len(input.RuleStats))
	for _, stats := range input.RuleStats {
		if stats.RuleID == "" || len(stats.RuleID) > 128 || stats.ActiveConns > maxSignedCounter || stats.BytesIn > maxSignedCounter || stats.BytesOut > maxSignedCounter {
			writeError(response, http.StatusUnprocessableEntity, "invalid_heartbeat", "rule statistics are invalid")
			return
		}
		if _, exists := seenRuleStats[stats.RuleID]; exists {
			writeError(response, http.StatusUnprocessableEntity, "invalid_heartbeat", "rule statistics contain duplicate rule IDs")
			return
		}
		seenRuleStats[stats.RuleID] = struct{}{}
		ruleStats = append(ruleStats, store.RuleRuntimeStats{RuleID: stats.RuleID, ActiveConns: stats.ActiveConns, BytesIn: stats.BytesIn, BytesOut: stats.BytesOut})
	}
	now := server.now().UTC()
	connections := make([]store.ActiveConnection, 0, len(input.Connections))
	seenConnections := make(map[string]struct{}, len(input.Connections))
	for _, connection := range input.Connections {
		connection.ID = strings.TrimSpace(connection.ID)
		connection.RuleID = strings.TrimSpace(connection.RuleID)
		connection.Protocol = strings.ToLower(strings.TrimSpace(connection.Protocol))
		connection.SourceAddress = strings.TrimSpace(connection.SourceAddress)
		connection.TargetAddress = strings.TrimSpace(connection.TargetAddress)
		if connection.ID == "" || len(connection.ID) > 128 || connection.RuleID == "" || len(connection.RuleID) > 128 ||
			(connection.Protocol != "tcp" && connection.Protocol != "udp") || connection.SourceAddress == "" || len(connection.SourceAddress) > 300 ||
			connection.TargetAddress == "" || len(connection.TargetAddress) > 300 || connection.StartedAt.IsZero() || connection.LastActivity.IsZero() ||
			connection.LastActivity.Before(connection.StartedAt) || connection.StartedAt.After(now.Add(10*time.Minute)) || connection.LastActivity.After(now.Add(10*time.Minute)) ||
			connection.BytesIn > maxSignedCounter || connection.BytesOut > maxSignedCounter {
			writeError(response, http.StatusUnprocessableEntity, "invalid_heartbeat", "connection snapshots are invalid")
			return
		}
		if _, exists := seenConnections[connection.ID]; exists {
			writeError(response, http.StatusUnprocessableEntity, "invalid_heartbeat", "connection snapshots contain duplicate IDs")
			return
		}
		seenConnections[connection.ID] = struct{}{}
		connections = append(connections, store.ActiveConnection{
			ConnectionID: connection.ID, RuleID: connection.RuleID, Protocol: connection.Protocol,
			SourceAddress: connection.SourceAddress, TargetAddress: connection.TargetAddress,
			StartedAt: connection.StartedAt.UTC(), LastActivity: connection.LastActivity.UTC(),
			BytesIn: connection.BytesIn, BytesOut: connection.BytesOut,
		})
	}
	logs := make([]store.AgentLog, 0, len(input.Logs))
	seenLogs := make(map[string]struct{}, len(input.Logs))
	for _, entry := range input.Logs {
		entry.ID = strings.TrimSpace(entry.ID)
		entry.Level = strings.ToLower(strings.TrimSpace(entry.Level))
		entry.Component = strings.TrimSpace(entry.Component)
		entry.Message = strings.TrimSpace(entry.Message)
		if entry.ID == "" || len(entry.ID) > 128 || (entry.Level != "info" && entry.Level != "warning" && entry.Level != "error") ||
			entry.Component == "" || len(entry.Component) > 64 || entry.Message == "" || len(entry.Message) > 2000 || entry.OccurredAt.IsZero() ||
			entry.OccurredAt.Before(now.Add(-30*24*time.Hour)) || entry.OccurredAt.After(now.Add(10*time.Minute)) {
			writeError(response, http.StatusUnprocessableEntity, "invalid_heartbeat", "heartbeat logs are invalid")
			return
		}
		if _, exists := seenLogs[entry.ID]; exists {
			writeError(response, http.StatusUnprocessableEntity, "invalid_heartbeat", "heartbeat logs contain duplicate IDs")
			return
		}
		seenLogs[entry.ID] = struct{}{}
		logs = append(logs, store.AgentLog{EventID: entry.ID, Level: entry.Level, Component: entry.Component,
			Message: entry.Message, OccurredAt: entry.OccurredAt.UTC()})
	}
	node, err := server.store.UpdateAgentHeartbeat(request.Context(), nodeID, auth.TokenHash(credential), store.AgentHeartbeat{
		PublicIP: remoteIP(request), TunnelAddress: input.TunnelAddress, AgentVersion: input.AgentVersion, ConfigVersion: input.ConfigVersion,
		AttemptedConfigVersion: input.AttemptedConfigVersion, LastConfigError: input.LastConfigError, CPUPercent: input.CPUPercent,
		MemoryPercent: input.MemoryPercent, LoadOne: input.LoadOne, DiskPercent: input.DiskPercent,
		NetworkRxBps: input.NetworkRxBps, NetworkTxBps: input.NetworkTxBps, ActiveConns: input.ActiveConns,
		BytesIn: input.BytesIn, BytesOut: input.BytesOut, RuleStats: ruleStats,
		Connections: connections, ConnectionsComplete: input.ConnectionsComplete, Logs: logs, ReceivedAt: now,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(response, http.StatusUnauthorized, "invalid_agent_credentials", "agent identity is invalid or disabled")
			return
		}
		log.Printf("update heartbeat node=%s: %v", nodeID, err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to store heartbeat")
		return
	}
	writeJSON(response, http.StatusOK, map[string]interface{}{
		"serverTime": now, "configVersion": node.ConfigVersion, "configChanged": node.ConfigVersion != input.ConfigVersion,
		"heartbeatIntervalSeconds": int(heartbeatInterval / time.Second),
	})
}

func (server *server) agentConfig(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	nodeID, credential, ok := requireAgentCredentials(response, request)
	if !ok {
		return
	}
	config, err := server.store.ConfigForAgent(request.Context(), nodeID, auth.TokenHash(credential))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(response, http.StatusUnauthorized, "invalid_agent_credentials", "agent identity is invalid or disabled")
			return
		}
		log.Printf("load agent config node=%s: %v", nodeID, err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load agent configuration")
		return
	}
	if config.Rules == nil {
		config.Rules = []domain.ForwardRule{}
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, map[string]interface{}{"version": config.Version, "rules": config.Rules})
}

func requireAgentCredentials(response http.ResponseWriter, request *http.Request) (string, string, bool) {
	nodeID := strings.TrimSpace(request.Header.Get("X-PortFlow-Node-ID"))
	const bearer = "Bearer "
	authorization := request.Header.Get("Authorization")
	if nodeID == "" || !strings.HasPrefix(authorization, bearer) {
		writeError(response, http.StatusUnauthorized, "agent_authentication_required", "agent node ID and bearer credential are required")
		return "", "", false
	}
	credential := strings.TrimSpace(strings.TrimPrefix(authorization, bearer))
	if credential == "" || len(nodeID) > 128 || len(credential) > 256 {
		writeError(response, http.StatusUnauthorized, "invalid_agent_credentials", "agent credentials are invalid")
		return "", "", false
	}
	return nodeID, credential, true
}

func invalidPercent(value float64) bool {
	return value < 0 || value > 100 || math.IsNaN(value) || math.IsInf(value, 0)
}

func (server *server) nodes(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	if _, _, ok := server.requireUser(response, request, ""); !ok {
		return
	}
	nodes, err := server.store.ListNodes(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to list nodes")
		return
	}
	nodes = effectiveNodeStatuses(nodes, server.now().UTC())
	writeJSON(response, http.StatusOK, map[string]interface{}{"items": nodes})
}

func (server *server) nodeDetail(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	if _, _, ok := server.requireUser(response, request, ""); !ok {
		return
	}
	nodeID := strings.TrimSpace(strings.TrimPrefix(request.URL.Path, "/api/v1/nodes/"))
	if nodeID == "" || len(nodeID) > 128 || strings.Contains(nodeID, "/") {
		writeError(response, http.StatusNotFound, "not_found", "node was not found")
		return
	}
	hours := 24
	if raw := request.URL.Query().Get("hours"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 720 {
			writeError(response, http.StatusUnprocessableEntity, "invalid_range", "hours must be between 1 and 720")
			return
		}
		hours = parsed
	}
	interval := 30 * time.Minute
	if hours <= 6 {
		interval = 5 * time.Minute
	} else if hours > 168 {
		interval = 6 * time.Hour
	} else if hours > 24 {
		interval = 2 * time.Hour
	}
	nodes, err := server.store.ListNodes(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load node")
		return
	}
	var node domain.Node
	found := false
	for _, candidate := range effectiveNodeStatuses(nodes, server.now().UTC()) {
		if candidate.ID == nodeID {
			node, found = candidate, true
			break
		}
	}
	if !found {
		writeError(response, http.StatusNotFound, "not_found", "node was not found")
		return
	}
	to := server.now().UTC()
	from := to.Add(-time.Duration(hours) * time.Hour)
	points, err := server.store.NodeMetricHistory(request.Context(), nodeID, from, to, interval)
	if err != nil {
		log.Printf("load node metric history: %v", err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load node history")
		return
	}
	if points == nil {
		points = []store.NodeMetricPoint{}
	}
	rules, err := server.store.ListForwardRules(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load node rules")
		return
	}
	nodeRules := make([]domain.ForwardRule, 0)
	for _, rule := range rules {
		if rule.IngressNodeID == nodeID || rule.EgressNodeID == nodeID {
			nodeRules = append(nodeRules, rule)
		}
	}
	var uploadBytes, downloadBytes uint64
	for _, point := range points {
		uploadBytes += point.UploadBytes
		downloadBytes += point.DownloadBytes
	}
	writeJSON(response, http.StatusOK, map[string]interface{}{
		"node": node, "rules": nodeRules, "from": from, "to": to, "intervalSeconds": int(interval / time.Second),
		"uploadBytes": uploadBytes, "downloadBytes": downloadBytes, "points": points,
	})
}

type forwardRuleInput struct {
	Name           string          `json:"name"`
	Protocol       domain.Protocol `json:"protocol"`
	Mode           string          `json:"mode"`
	IngressNodeID  string          `json:"ingressNodeId"`
	EgressNodeID   string          `json:"egressNodeId"`
	ListenHost     string          `json:"listenHost"`
	ListenPort     uint16          `json:"listenPort"`
	TargetHost     string          `json:"targetHost"`
	TargetPort     uint16          `json:"targetPort"`
	RelayPort      uint16          `json:"relayPort"`
	Enabled        bool            `json:"enabled"`
	BandwidthKbps  uint64          `json:"bandwidthKbps"`
	MaxConnections uint32          `json:"maxConnections"`
	AllowCIDRs     []string        `json:"allowCidrs"`
	DenyCIDRs      []string        `json:"denyCidrs"`
}

func (server *server) forwardRules(response http.ResponseWriter, request *http.Request) {
	_, user, ok := server.requireUser(response, request, "")
	if !ok {
		return
	}
	switch request.Method {
	case http.MethodGet:
		rules, err := server.store.ListForwardRules(request.Context())
		if err != nil {
			log.Printf("list forward rules: %v", err)
			writeError(response, http.StatusInternalServerError, "storage_error", "unable to list forwarding rules")
			return
		}
		if rules == nil {
			rules = []domain.ForwardRule{}
		}
		writeJSON(response, http.StatusOK, map[string]interface{}{"items": rules})
	case http.MethodPost:
		var input forwardRuleInput
		if !decodeJSON(response, request, &input) {
			return
		}
		rule, valid := server.buildForwardRule(request.Context(), response, input, "", user.Role)
		if !valid {
			return
		}
		created, err := server.store.CreateForwardRule(request.Context(), rule)
		if err != nil {
			writeForwardRuleStoreError(response, err)
			return
		}
		server.audit(request, "user", user.ID, "forward_rule.create", "forward_rule", created.ID,
			map[string]interface{}{"name": created.Name, "mode": created.Mode, "ingressNodeId": created.IngressNodeID, "egressNodeId": created.EgressNodeID})
		writeJSON(response, http.StatusCreated, created)
	default:
		response.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (server *server) forwardRule(response http.ResponseWriter, request *http.Request) {
	_, user, ok := server.requireUser(response, request, "")
	if !ok {
		return
	}
	id := strings.TrimPrefix(request.URL.Path, "/api/v1/forward-rules/")
	if id == "" || strings.Contains(id, "/") || len(id) > 128 {
		writeError(response, http.StatusNotFound, "not_found", "forwarding rule was not found")
		return
	}
	switch request.Method {
	case http.MethodPut:
		var input forwardRuleInput
		if !decodeJSON(response, request, &input) {
			return
		}
		rule, valid := server.buildForwardRule(request.Context(), response, input, id, user.Role)
		if !valid {
			return
		}
		updated, err := server.store.UpdateForwardRule(request.Context(), rule)
		if err != nil {
			writeForwardRuleStoreError(response, err)
			return
		}
		server.audit(request, "user", user.ID, "forward_rule.update", "forward_rule", updated.ID,
			map[string]interface{}{"name": updated.Name, "mode": updated.Mode, "ingressNodeId": updated.IngressNodeID, "egressNodeId": updated.EgressNodeID})
		writeJSON(response, http.StatusOK, updated)
	case http.MethodDelete:
		if err := server.store.DeleteForwardRule(request.Context(), id); err != nil {
			writeForwardRuleStoreError(response, err)
			return
		}
		server.audit(request, "user", user.ID, "forward_rule.delete", "forward_rule", id, nil)
		response.WriteHeader(http.StatusNoContent)
	default:
		response.Header().Set("Allow", http.MethodPut+", "+http.MethodDelete)
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (server *server) buildForwardRule(ctx context.Context, response http.ResponseWriter, input forwardRuleInput, id string, role store.Role) (domain.ForwardRule, bool) {
	input.Name = strings.TrimSpace(input.Name)
	input.Mode = strings.TrimSpace(input.Mode)
	input.IngressNodeID = strings.TrimSpace(input.IngressNodeID)
	input.EgressNodeID = strings.TrimSpace(input.EgressNodeID)
	input.ListenHost = strings.TrimSpace(input.ListenHost)
	input.TargetHost = strings.TrimSpace(input.TargetHost)
	if id == "" {
		var err error
		id, err = auth.NewID("rul")
		if err != nil {
			writeError(response, http.StatusInternalServerError, "internal_error", "unable to create forwarding rule")
			return domain.ForwardRule{}, false
		}
	}
	if input.ListenHost == "" {
		input.ListenHost = "0.0.0.0"
	}
	if input.Protocol == "" {
		input.Protocol = domain.ProtocolTCP
	}
	if input.Mode == "" {
		input.Mode = domain.ForwardDirect
	}
	rule := domain.ForwardRule{ID: id, Name: input.Name, Protocol: input.Protocol, Mode: input.Mode,
		IngressNodeID: input.IngressNodeID, EgressNodeID: input.EgressNodeID,
		ListenHost: input.ListenHost, ListenPort: input.ListenPort, TargetHost: input.TargetHost, TargetPort: input.TargetPort, RelayPort: input.RelayPort,
		Enabled: input.Enabled, BandwidthKbps: input.BandwidthKbps, MaxConnections: input.MaxConnections,
		AllowCIDRs: input.AllowCIDRs, DenyCIDRs: input.DenyCIDRs}
	if len(rule.Name) > 80 || len(rule.TargetHost) > 253 || len(rule.ListenHost) > 45 || len(rule.AllowCIDRs) > 100 || len(rule.DenyCIDRs) > 100 || rule.MaxConnections > 1_000_000 || rule.BandwidthKbps > domain.MaxBandwidthKbps {
		writeError(response, http.StatusUnprocessableEntity, "invalid_rule", "forwarding rule exceeds supported limits")
		return domain.ForwardRule{}, false
	}
	if rule.Protocol != domain.ProtocolTCP && rule.Protocol != domain.ProtocolUDP && rule.Protocol != domain.ProtocolTCPUDP {
		writeError(response, http.StatusUnprocessableEntity, "unsupported_rule", "protocol must be TCP, UDP, or TCP+UDP")
		return domain.ForwardRule{}, false
	}
	if rule.Mode != domain.ForwardDirect && rule.Mode != domain.ForwardRelay {
		writeError(response, http.StatusUnprocessableEntity, "unsupported_rule", "forwarding mode must be direct or relay")
		return domain.ForwardRule{}, false
	}
	if rule.Mode == domain.ForwardDirect && rule.EgressNodeID != "" {
		writeError(response, http.StatusUnprocessableEntity, "invalid_rule", "direct rules cannot specify an egress node")
		return domain.ForwardRule{}, false
	}
	if rule.Mode == domain.ForwardDirect && rule.RelayPort != 0 {
		writeError(response, http.StatusUnprocessableEntity, "invalid_rule", "direct rules cannot specify a relay port")
		return domain.ForwardRule{}, false
	}
	if rule.Mode == domain.ForwardRelay && role != store.RoleAdmin {
		writeError(response, http.StatusForbidden, "permission_denied", "only administrators may configure relay rules")
		return domain.ForwardRule{}, false
	}
	if rule.Mode == domain.ForwardRelay {
		if rule.RelayPort == 0 {
			rule.RelayPort = rule.ListenPort
		}
		nodes, err := server.store.ListNodes(ctx)
		if err != nil {
			writeError(response, http.StatusInternalServerError, "storage_error", "unable to validate relay nodes")
			return domain.ForwardRule{}, false
		}
		var ingressReady bool
		for _, node := range nodes {
			if node.ID == rule.IngressNodeID {
				ingressReady = domain.ValidTunnelAddress(node.TunnelAddress)
			}
			if node.ID == rule.EgressNodeID {
				rule.RelayHost = node.TunnelAddress
			}
		}
		if rule.Enabled && (!ingressReady || !domain.ValidTunnelAddress(rule.RelayHost)) {
			writeError(response, http.StatusUnprocessableEntity, "relay_not_provisioned", "both relay nodes must report private WireGuard tunnel addresses before the rule can be enabled")
			return domain.ForwardRule{}, false
		}
	}
	listenIP := net.ParseIP(rule.ListenHost)
	if listenIP == nil || listenIP.IsMulticast() {
		writeError(response, http.StatusUnprocessableEntity, "invalid_listen_host", "listen host must be a valid non-multicast IP address")
		return domain.ForwardRule{}, false
	}
	if !validTargetHost(rule.TargetHost) {
		writeError(response, http.StatusUnprocessableEntity, "invalid_target_host", "target host is invalid")
		return domain.ForwardRule{}, false
	}
	if role != store.RoleAdmin && sensitiveTarget(rule.TargetHost) {
		writeError(response, http.StatusForbidden, "sensitive_target", "only administrators may forward to loopback or link-local targets")
		return domain.ForwardRule{}, false
	}
	if err := rule.Validate(); err != nil {
		writeError(response, http.StatusUnprocessableEntity, "invalid_rule", err.Error())
		return domain.ForwardRule{}, false
	}
	return rule, true
}

func sensitiveTarget(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.EqualFold(host, "metadata.google.internal") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && (address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified())
}

func validTargetHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, character := range host {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '.' && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func writeForwardRuleStoreError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(response, http.StatusNotFound, "not_found", "forwarding rule, ingress node, or egress node was not found")
	case errors.Is(err, store.ErrConflict):
		writeError(response, http.StatusConflict, "listener_conflict", "another rule already uses this node, protocol, address, and port")
	default:
		log.Printf("forward rule storage: %v", err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to save forwarding rule")
	}
}

func (server *server) dashboardSummary(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	if _, _, ok := server.requireUser(response, request, ""); !ok {
		return
	}
	nodes, err := server.store.ListNodes(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load dashboard")
		return
	}
	nodes = effectiveNodeStatuses(nodes, server.now().UTC())
	online, offline := 0, 0
	var activeConnections uint64
	nodeByID := make(map[string]domain.Node, len(nodes))
	for _, node := range nodes {
		nodeByID[node.ID] = node
		if node.Status == "online" {
			online++
		} else {
			offline++
		}
		activeConnections += node.ActiveConns
	}
	rules, err := server.store.ListForwardRules(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load dashboard rules")
		return
	}
	healthy, degraded, stopped := 0, 0, 0
	for _, rule := range rules {
		if !rule.Enabled {
			stopped++
			continue
		}
		node, exists := nodeByID[rule.IngressNodeID]
		if exists && node.Status == domain.NodeOnline && node.AppliedConfigVersion >= rule.ConfigVersion {
			healthy++
		} else {
			degraded++
		}
	}
	now := server.now().UTC()
	traffic, err := server.store.TrafficHistory(request.Context(), now.Add(-24*time.Hour), now, 30*time.Minute)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load dashboard traffic")
		return
	}
	var uploadBytes, downloadBytes uint64
	for _, point := range traffic {
		uploadBytes += point.UploadBytes
		downloadBytes += point.DownloadBytes
	}
	writeJSON(response, http.StatusOK, map[string]interface{}{
		"nodes":        map[string]int{"online": online, "offline": offline},
		"rules":        map[string]int{"healthy": healthy, "degraded": degraded, "stopped": stopped},
		"connections":  activeConnections,
		"trafficToday": map[string]uint64{"uploadBytes": uploadBytes, "downloadBytes": downloadBytes},
		"source":       "store",
	})
}

func (server *server) trafficHistory(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	if _, _, ok := server.requireUser(response, request, ""); !ok {
		return
	}
	hours := 24
	if raw := request.URL.Query().Get("hours"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 168 {
			writeError(response, http.StatusUnprocessableEntity, "invalid_range", "hours must be between 1 and 168")
			return
		}
		hours = parsed
	}
	interval := 30 * time.Minute
	if hours <= 6 {
		interval = 5 * time.Minute
	} else if hours > 24 {
		interval = 2 * time.Hour
	}
	to := server.now().UTC()
	from := to.Add(-time.Duration(hours) * time.Hour)
	points, err := server.store.TrafficHistory(request.Context(), from, to, interval)
	if err != nil {
		log.Printf("load traffic history: %v", err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load traffic history")
		return
	}
	if points == nil {
		points = []store.TrafficPoint{}
	}
	var uploadBytes, downloadBytes uint64
	for _, point := range points {
		uploadBytes += point.UploadBytes
		downloadBytes += point.DownloadBytes
	}
	writeJSON(response, http.StatusOK, map[string]interface{}{
		"from": from, "to": to, "intervalSeconds": int(interval / time.Second), "uploadBytes": uploadBytes,
		"downloadBytes": downloadBytes, "points": points,
	})
}

func (server *server) auditEvents(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	if _, _, ok := server.requireUser(response, request, store.RoleAdmin); !ok {
		return
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(response, http.StatusUnprocessableEntity, "invalid_limit", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	var before time.Time
	if raw := request.URL.Query().Get("before"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(response, http.StatusUnprocessableEntity, "invalid_cursor", "before must be an RFC3339 timestamp")
			return
		}
		before = parsed.UTC()
	}
	events, err := server.store.ListAudit(request.Context(), limit, before)
	if err != nil {
		log.Printf("list audit events: %v", err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load audit events")
		return
	}
	if events == nil {
		events = []store.AuditEvent{}
	}
	var nextBefore interface{}
	if len(events) == limit {
		nextBefore = events[len(events)-1].CreatedAt
	}
	writeJSON(response, http.StatusOK, map[string]interface{}{"items": events, "nextBefore": nextBefore})
}

func (server *server) agentLogs(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	if _, _, ok := server.requireUser(response, request, store.RoleAdmin); !ok {
		return
	}
	filter := store.AgentLogFilter{NodeID: strings.TrimSpace(request.URL.Query().Get("nodeId")),
		Level: strings.ToLower(strings.TrimSpace(request.URL.Query().Get("level"))), Limit: 100}
	if len(filter.NodeID) > 128 || filter.Level != "" && filter.Level != "info" && filter.Level != "warning" && filter.Level != "error" {
		writeError(response, http.StatusUnprocessableEntity, "invalid_filter", "nodeId or level is invalid")
		return
	}
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(response, http.StatusUnprocessableEntity, "invalid_limit", "limit must be between 1 and 200")
			return
		}
		filter.Limit = parsed
	}
	if raw := request.URL.Query().Get("before"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(response, http.StatusUnprocessableEntity, "invalid_cursor", "before must be an RFC3339 timestamp")
			return
		}
		filter.Before = parsed.UTC()
	}
	entries, err := server.store.ListAgentLogs(request.Context(), filter)
	if err != nil {
		log.Printf("list agent logs: %v", err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load agent logs")
		return
	}
	if entries == nil {
		entries = []store.AgentLog{}
	}
	var nextBefore interface{}
	if len(entries) == filter.Limit {
		nextBefore = entries[len(entries)-1].OccurredAt
	}
	writeJSON(response, http.StatusOK, map[string]interface{}{"items": entries, "nextBefore": nextBefore})
}

func (server *server) activeConnections(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	if _, _, ok := server.requireUser(response, request, ""); !ok {
		return
	}
	connections, err := server.store.ListActiveConnections(request.Context())
	if err != nil {
		log.Printf("list active connections: %v", err)
		writeError(response, http.StatusInternalServerError, "storage_error", "unable to load active connections")
		return
	}
	if connections == nil {
		connections = []store.ActiveConnection{}
	}
	writeJSON(response, http.StatusOK, map[string]interface{}{"items": connections, "serverTime": server.now().UTC()})
}

func (server *server) systemSettings(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	if _, _, ok := server.requireUser(response, request, store.RoleAdmin); !ok {
		return
	}
	now := server.now().UTC()
	uptime := now.Sub(server.startedAt)
	if uptime < 0 {
		uptime = 0
	}
	type readinessCheck struct {
		ID     string `json:"id"`
		Label  string `json:"label"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	checks := make([]readinessCheck, 0, 6)
	appendCheck := func(id, label string, passed bool, success, failure string) {
		status, detail := "pass", success
		if !passed {
			status, detail = "fail", failure
		}
		checks = append(checks, readinessCheck{ID: id, Label: label, Status: status, Detail: detail})
	}
	storageContext, cancelStorageCheck := context.WithTimeout(request.Context(), 2*time.Second)
	storageHealthy := server.store.Health(storageContext) == nil
	cancelStorageCheck()
	appendCheck("storage_health", "存储健康", storageHealthy, "存储连接正常", "存储健康检查失败")
	appendCheck("persistent_storage", "持久化存储", server.storageMode == "postgres", "正在使用 PostgreSQL", "当前是易失性内存存储")
	appendCheck("release_version", "正式版本号", server.build.Version != "" && server.build.Version != "dev", "版本号已固定", "版本仍为 dev 或为空")
	appendCheck("secure_cookies", "Secure Cookie", server.secureCookies, "Secure Cookie 已启用", "公网部署必须启用 Secure Cookie")
	httpsObserved := request.TLS != nil || strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")), "https")
	appendCheck("https", "HTTPS 访问", httpsObserved, "本次请求通过 HTTPS", "本次请求未观察到 HTTPS")
	activeAdministrators := 0
	usersContext, cancelUsersCheck := context.WithTimeout(request.Context(), 2*time.Second)
	users, usersErr := server.store.ListUsers(usersContext)
	cancelUsersCheck()
	if usersErr == nil {
		for _, user := range users {
			if user.Role == store.RoleAdmin && !user.Disabled {
				activeAdministrators++
			}
		}
	}
	appendCheck("active_administrator", "启用管理员", usersErr == nil && activeAdministrators > 0,
		"至少有一名启用管理员", "无法确认启用管理员")
	ready := true
	for _, check := range checks {
		if check.Status != "pass" {
			ready = false
			break
		}
	}
	writeJSON(response, http.StatusOK, map[string]interface{}{
		"runtime": map[string]interface{}{
			"version": server.build.Version, "startedAt": server.startedAt, "serverTime": now,
			"uptimeSeconds": int64(uptime / time.Second),
		},
		"security": map[string]interface{}{
			"secureCookies": server.secureCookies, "httpOnlyCookies": true, "sameSite": "strict",
			"sessionTtlSeconds": int64(server.sessionTTL / time.Second),
			"passwordMinLength": auth.MinPasswordLength, "passwordMaxLength": auth.MaxPasswordLength,
			"loginFailureLimit": loginFailureLimit, "loginFailureWindowSeconds": int64(loginFailureWindow / time.Second),
		},
		"agents": map[string]interface{}{
			"heartbeatIntervalSeconds":    int64(heartbeatInterval / time.Second),
			"offlineAfterSeconds":         int64(nodeOfflineThreshold / time.Second),
			"maxConnectionsPerHeartbeat":  maxConnectionsPerHeartbeat,
			"maxLogsPerHeartbeat":         maxAgentLogsPerHeartbeat,
			"maxStoredConnectionsPerNode": store.MaxStoredConnectionsPerNode,
		},
		"retention": map[string]interface{}{
			"nodeMetricsDays":        int64(store.NodeMetricRetention / (24 * time.Hour)),
			"agentLogsDays":          int64(store.AgentLogRetention / (24 * time.Hour)),
			"auditEventsAutoCleanup": false, "activeConnectionsMode": "latest_snapshot",
		},
		"deployment": map[string]interface{}{
			"ready": ready, "storageMode": server.storageMode, "httpsObserved": httpsObserved,
			"activeAdministrators": activeAdministrators, "checks": checks,
		},
	})
}

func effectiveNodeStatuses(nodes []domain.Node, now time.Time) []domain.Node {
	result := append([]domain.Node(nil), nodes...)
	for index := range result {
		if result[index].Status == domain.NodeOnline && now.Sub(result[index].LastHeartbeat) > nodeOfflineThreshold {
			result[index].Status = domain.NodeOffline
		}
	}
	return result
}

func (server *server) requireUser(response http.ResponseWriter, request *http.Request, role store.Role) (store.Session, store.User, bool) {
	rawToken := sessionToken(request)
	if rawToken == "" {
		writeError(response, http.StatusUnauthorized, "authentication_required", "authentication is required")
		return store.Session{}, store.User{}, false
	}
	session, user, err := server.store.SessionUserByTokenHash(request.Context(), auth.TokenHash(rawToken), server.now().UTC())
	if err != nil {
		writeError(response, http.StatusUnauthorized, "invalid_session", "session is invalid or expired")
		return store.Session{}, store.User{}, false
	}
	if role != "" && user.Role != role {
		writeError(response, http.StatusForbidden, "permission_denied", "administrator permission is required")
		return store.Session{}, store.User{}, false
	}
	return session, user, true
}

func (server *server) setSessionCookie(response http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(response, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: expiresAt, HttpOnly: true, Secure: server.secureCookies, SameSite: http.SameSiteStrictMode})
}

func sessionToken(request *http.Request) string {
	if cookie, err := request.Cookie(sessionCookie); err == nil {
		return cookie.Value
	}
	const bearer = "Bearer "
	if header := request.Header.Get("Authorization"); strings.HasPrefix(header, bearer) {
		return strings.TrimSpace(strings.TrimPrefix(header, bearer))
	}
	return ""
}

func publicUser(user store.User) map[string]interface{} {
	return map[string]interface{}{"id": user.ID, "username": user.Username, "role": user.Role, "disabled": user.Disabled, "createdAt": user.CreatedAt}
}

func decodeJSON(response http.ResponseWriter, request *http.Request, destination interface{}) bool {
	if contentType := request.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		writeError(response, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json")
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(response, http.StatusBadRequest, "invalid_json", "request body must contain a single JSON object")
		return false
	}
	return true
}

func writeJSON(response http.ResponseWriter, status int, value interface{}) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]interface{}{"error": map[string]string{"code": code, "message": message}})
}

func methodNotAllowed(response http.ResponseWriter, allowed string) {
	response.Header().Set("Allow", allowed)
	writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func remoteIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

func (server *server) audit(request *http.Request, actorType, actorID, action, targetType, targetID string, details map[string]interface{}) {
	eventID, err := auth.NewID("aud")
	if err != nil {
		log.Printf("generate audit id: %v", err)
		return
	}
	event := store.AuditEvent{ID: eventID, ActorType: actorType, ActorID: actorID, Action: action, TargetType: targetType,
		TargetID: targetID, RemoteIP: remoteIP(request), Details: details, CreatedAt: server.now().UTC()}
	if err := server.store.RecordAudit(request.Context(), event); err != nil {
		log.Printf("record audit action=%s: %v", action, err)
	}
}
