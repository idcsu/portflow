package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"portflow/internal/auth"
	"portflow/internal/store"
)

type unhealthyStore struct{ *store.Memory }

func (unhealthyStore) Health(context.Context) error { return errors.New("database unavailable") }

func newTestServer() http.Handler {
	fixed := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	return NewServer(Options{Build: BuildInfo{Version: "test"}, Store: store.NewMemory(), Now: func() time.Time { return fixed }})
}

func requestJSON(t *testing.T, handler http.Handler, method, path string, body interface{}, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var contents bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&contents).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &contents)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "192.0.2.10:32100"
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func responseObject(t *testing.T, response *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func TestHealth(t *testing.T) {
	handler := newTestServer()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	body := responseObject(t, response)
	if body["status"] != "ok" || body["version"] != "test" {
		t.Fatalf("unexpected body: %#v", body)
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers missing")
	}
}

func TestHealthReportsStorageFailure(t *testing.T) {
	handler := NewServer(Options{Build: BuildInfo{Version: "test"}, Store: unhealthyStore{store.NewMemory()}})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || responseObject(t, response)["status"] != "degraded" {
		t.Fatalf("unexpected degraded health response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSetupStatusAndTOTPLoginFlow(t *testing.T) {
	fixed := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	handler := NewServer(Options{Build: BuildInfo{Version: "test"}, Store: store.NewMemory(), MFAEncryptionKey: bytes.Repeat([]byte{4}, 32), Now: func() time.Time { return fixed }})
	status := requestJSON(t, handler, http.MethodGet, "/api/v1/setup/status", nil, nil)
	if status.Code != http.StatusOK || responseObject(t, status)["required"] != true {
		t.Fatalf("unexpected initial setup status: %d %s", status.Code, status.Body.String())
	}
	setup := requestJSON(t, handler, http.MethodPost, "/api/v1/setup/admin", map[string]string{"username": "operator", "password": "a-long-test-password"}, nil)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup status=%d body=%s", setup.Code, setup.Body.String())
	}
	status = requestJSON(t, handler, http.MethodGet, "/api/v1/setup/status", nil, nil)
	if responseObject(t, status)["required"] != false {
		t.Fatal("setup should no longer be offered")
	}
	login := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "operator", "password": "a-long-test-password", "code": "", "recoveryCode": ""}, nil)
	cookie := login.Result().Cookies()[0]
	start := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/mfa/setup", map[string]string{"password": "a-long-test-password"}, cookie)
	if start.Code != http.StatusOK {
		t.Fatalf("MFA setup status=%d body=%s", start.Code, start.Body.String())
	}
	secret := responseObject(t, start)["secret"].(string)
	code, err := auth.TOTPCode(secret, fixed)
	if err != nil {
		t.Fatal(err)
	}
	enable := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/mfa/enable", map[string]string{"password": "a-long-test-password", "code": code}, cookie)
	if enable.Code != http.StatusOK {
		t.Fatalf("MFA enable status=%d body=%s", enable.Code, enable.Body.String())
	}
	recoveryCodes := responseObject(t, enable)["recoveryCodes"].([]interface{})
	challenge := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "operator", "password": "a-long-test-password", "code": "", "recoveryCode": ""}, nil)
	if challenge.Code != http.StatusAccepted || responseObject(t, challenge)["mfaRequired"] != true || len(challenge.Result().Cookies()) != 0 {
		t.Fatalf("unexpected MFA challenge: %d %s", challenge.Code, challenge.Body.String())
	}
	verified := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "operator", "password": "a-long-test-password", "code": code, "recoveryCode": ""}, nil)
	if verified.Code != http.StatusOK || responseObject(t, verified)["user"].(map[string]interface{})["mfaEnabled"] != true {
		t.Fatalf("TOTP login status=%d body=%s", verified.Code, verified.Body.String())
	}
	recovered := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "operator", "password": "a-long-test-password", "code": "", "recoveryCode": recoveryCodes[0].(string)}, nil)
	if recovered.Code != http.StatusOK {
		t.Fatalf("recovery login status=%d body=%s", recovered.Code, recovered.Body.String())
	}
	reused := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "operator", "password": "a-long-test-password", "code": "", "recoveryCode": recoveryCodes[0].(string)}, nil)
	if reused.Code != http.StatusUnauthorized {
		t.Fatalf("reused recovery code status=%d", reused.Code)
	}
	disabled := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/mfa/disable", map[string]string{"password": "a-long-test-password", "code": "", "recoveryCode": recoveryCodes[1].(string)}, recovered.Result().Cookies()[0])
	if disabled.Code != http.StatusOK {
		t.Fatalf("MFA disable status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	passwordOnly := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "operator", "password": "a-long-test-password", "code": "", "recoveryCode": ""}, nil)
	if passwordOnly.Code != http.StatusOK || responseObject(t, passwordOnly)["user"].(map[string]interface{})["mfaEnabled"] != false {
		t.Fatalf("password-only login after disable status=%d body=%s", passwordOnly.Code, passwordOnly.Body.String())
	}
}

func TestAdministratorManagesMembersAndRevokesTheirSessions(t *testing.T) {
	handler := newTestServer()
	setup := requestJSON(t, handler, http.MethodPost, "/api/v1/setup/admin", map[string]string{
		"username": "operator", "password": "a-long-test-password",
	}, nil)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup status=%d body=%s", setup.Code, setup.Body.String())
	}
	administratorID := responseObject(t, setup)["id"].(string)
	login := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "operator", "password": "a-long-test-password",
	}, nil)
	administratorCookie := login.Result().Cookies()[0]
	settings := requestJSON(t, handler, http.MethodGet, "/api/v1/system/settings", nil, administratorCookie)
	if settings.Code != http.StatusOK {
		t.Fatalf("system settings status=%d body=%s", settings.Code, settings.Body.String())
	}
	settingsBody := responseObject(t, settings)
	security := settingsBody["security"].(map[string]interface{})
	agents := settingsBody["agents"].(map[string]interface{})
	retention := settingsBody["retention"].(map[string]interface{})
	deployment := settingsBody["deployment"].(map[string]interface{})
	if security["sessionTtlSeconds"] != float64(12*60*60) || security["passwordMinLength"] != float64(12) ||
		agents["heartbeatIntervalSeconds"] != float64(15) || agents["offlineAfterSeconds"] != float64(45) ||
		retention["nodeMetricsDays"] != float64(30) || retention["agentLogsDays"] != float64(14) ||
		deployment["ready"] != false || deployment["storageMode"] != "memory" || len(deployment["checks"].([]interface{})) != 6 {
		t.Fatalf("unexpected system settings: %#v", settingsBody)
	}
	created := requestJSON(t, handler, http.MethodPost, "/api/v1/users", map[string]interface{}{
		"username": "teammate", "password": "member-test-password", "role": "member",
	}, administratorCookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("create member status=%d body=%s", created.Code, created.Body.String())
	}
	member := responseObject(t, created)
	memberID := member["id"].(string)
	if member["role"] != "member" || member["disabled"] != false {
		t.Fatalf("unexpected member: %#v", member)
	}
	duplicate := requestJSON(t, handler, http.MethodPost, "/api/v1/users", map[string]interface{}{
		"username": "teammate", "password": "another-test-password", "role": "member",
	}, administratorCookie)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate member status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	list := requestJSON(t, handler, http.MethodGet, "/api/v1/users", nil, administratorCookie)
	if list.Code != http.StatusOK || len(responseObject(t, list)["items"].([]interface{})) != 2 {
		t.Fatalf("list users status=%d body=%s", list.Code, list.Body.String())
	}
	memberLogin := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "teammate", "password": "member-test-password",
	}, nil)
	if memberLogin.Code != http.StatusOK {
		t.Fatalf("member login status=%d body=%s", memberLogin.Code, memberLogin.Body.String())
	}
	memberCookie := memberLogin.Result().Cookies()[0]
	forbidden := requestJSON(t, handler, http.MethodGet, "/api/v1/users", nil, memberCookie)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("member users access status=%d", forbidden.Code)
	}
	forbiddenSettings := requestJSON(t, handler, http.MethodGet, "/api/v1/system/settings", nil, memberCookie)
	if forbiddenSettings.Code != http.StatusForbidden {
		t.Fatalf("member system settings access status=%d", forbiddenSettings.Code)
	}
	updated := requestJSON(t, handler, http.MethodPut, "/api/v1/users/"+memberID, map[string]interface{}{
		"role": "member", "disabled": true, "password": "",
	}, administratorCookie)
	if updated.Code != http.StatusOK || responseObject(t, updated)["disabled"] != true {
		t.Fatalf("disable member status=%d body=%s", updated.Code, updated.Body.String())
	}
	memberMe := requestJSON(t, handler, http.MethodGet, "/api/v1/auth/me", nil, memberCookie)
	if memberMe.Code != http.StatusUnauthorized {
		t.Fatalf("disabled member session status=%d", memberMe.Code)
	}
	selfUpdate := requestJSON(t, handler, http.MethodPut, "/api/v1/users/"+administratorID, map[string]interface{}{
		"role": "member", "disabled": false, "password": "",
	}, administratorCookie)
	if selfUpdate.Code != http.StatusConflict {
		t.Fatalf("self update status=%d body=%s", selfUpdate.Code, selfUpdate.Body.String())
	}
	deleted := requestJSON(t, handler, http.MethodDelete, "/api/v1/users/"+memberID, nil, administratorCookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete member status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	list = requestJSON(t, handler, http.MethodGet, "/api/v1/users", nil, administratorCookie)
	if list.Code != http.StatusOK || len(responseObject(t, list)["items"].([]interface{})) != 1 {
		t.Fatalf("list after delete status=%d body=%s", list.Code, list.Body.String())
	}
}

func TestSystemSettingsReportsReleaseReadiness(t *testing.T) {
	fixed := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	handler := NewServer(Options{Build: BuildInfo{Version: "1.0.0"}, Store: store.NewMemory(), StorageMode: "postgres",
		SecureCookies: true, Now: func() time.Time { return fixed }})
	setup := requestJSON(t, handler, http.MethodPost, "/api/v1/setup/admin", map[string]string{
		"username": "release-admin", "password": "release-test-password",
	}, nil)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup status=%d body=%s", setup.Code, setup.Body.String())
	}
	login := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "release-admin", "password": "release-test-password",
	}, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system/settings", nil)
	request.AddCookie(login.Result().Cookies()[0])
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", response.Code, response.Body.String())
	}
	deployment := responseObject(t, response)["deployment"].(map[string]interface{})
	if deployment["ready"] != true || deployment["activeAdministrators"] != float64(1) || deployment["httpsObserved"] != true {
		t.Fatalf("unexpected release readiness: %#v", deployment)
	}
}

func TestAdministratorLoginAndAgentEnrollmentFlow(t *testing.T) {
	handler := newTestServer()

	setup := requestJSON(t, handler, http.MethodPost, "/api/v1/setup/admin", map[string]string{
		"username": "operator", "password": "a-long-test-password",
	}, nil)
	if setup.Code != http.StatusCreated {
		t.Fatalf("setup status=%d body=%s", setup.Code, setup.Body.String())
	}
	secondSetup := requestJSON(t, handler, http.MethodPost, "/api/v1/setup/admin", map[string]string{
		"username": "second", "password": "another-long-password",
	}, nil)
	if secondSetup.Code != http.StatusConflict {
		t.Fatalf("second setup status=%d", secondSetup.Code)
	}

	failedLogin := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "operator", "password": "wrong-password-value",
	}, nil)
	if failedLogin.Code != http.StatusUnauthorized {
		t.Fatalf("failed login status=%d", failedLogin.Code)
	}

	login := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "operator", "password": "a-long-test-password",
	}, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || !cookies[0].HttpOnly {
		t.Fatalf("unexpected login cookie: %#v", cookies)
	}

	createToken := requestJSON(t, handler, http.MethodPost, "/api/v1/enrollment-tokens", map[string]interface{}{
		"name": "Shanghai bootstrap", "expiresInMinutes": 30,
	}, cookies[0])
	if createToken.Code != http.StatusCreated {
		t.Fatalf("token status=%d body=%s", createToken.Code, createToken.Body.String())
	}
	rawToken, ok := responseObject(t, createToken)["token"].(string)
	if !ok || rawToken == "" {
		t.Fatal("raw enrollment token missing")
	}

	enrollmentPayload := map[string]string{
		"token": rawToken, "name": "Shanghai Edge 01", "region": "Shanghai",
		"architecture": "linux/amd64", "agentVersion": "0.1.0",
	}
	enroll := requestJSON(t, handler, http.MethodPost, "/api/v1/agent/enroll", enrollmentPayload, nil)
	if enroll.Code != http.StatusCreated {
		t.Fatalf("enroll status=%d body=%s", enroll.Code, enroll.Body.String())
	}
	enrollment := responseObject(t, enroll)
	if enrollment["credential"] == "" {
		t.Fatal("agent credential missing")
	}
	node := enrollment["node"].(map[string]interface{})
	if node["publicIp"] != "192.0.2.10" {
		t.Fatalf("agent public IP not observed from connection: %#v", node)
	}
	nodeID := node["id"].(string)
	credential := enrollment["credential"].(string)
	heartbeatBody, _ := json.Marshal(map[string]interface{}{
		"agentVersion": "0.1.1", "tunnelAddress": "10.203.0.1", "configVersion": 1, "cpuPercent": 12.5,
		"memoryPercent": 34.5, "loadOne": 0.8, "diskPercent": 61.5, "networkRxBps": 125000, "networkTxBps": 64000, "activeConnections": 7,
		"bytesIn": 1024, "bytesOut": 2048,
		"logs": []map[string]interface{}{{"id": "runtime-1", "level": "warning", "component": "forward", "message": "retry upstream", "occurredAt": "2026-07-11T11:59:00Z"}},
	})
	heartbeatRequest := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", bytes.NewReader(heartbeatBody))
	heartbeatRequest.Header.Set("Content-Type", "application/json")
	heartbeatRequest.Header.Set("X-PortFlow-Node-ID", nodeID)
	heartbeatRequest.Header.Set("Authorization", "Bearer "+credential)
	heartbeatRequest.RemoteAddr = "192.0.2.11:40000"
	heartbeat := httptest.NewRecorder()
	handler.ServeHTTP(heartbeat, heartbeatRequest)
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("heartbeat status=%d body=%s", heartbeat.Code, heartbeat.Body.String())
	}
	heartbeatResult := responseObject(t, heartbeat)
	if heartbeatResult["configChanged"] != false || heartbeatResult["configVersion"] != float64(1) {
		t.Fatalf("unexpected heartbeat response: %#v", heartbeatResult)
	}

	configRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent/config", nil)
	configRequest.Header.Set("X-PortFlow-Node-ID", nodeID)
	configRequest.Header.Set("Authorization", "Bearer "+credential)
	config := httptest.NewRecorder()
	handler.ServeHTTP(config, configRequest)
	if config.Code != http.StatusOK || config.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("config status=%d body=%s", config.Code, config.Body.String())
	}
	if responseObject(t, config)["version"] != float64(1) {
		t.Fatal("unexpected config version")
	}

	rulePayload := map[string]interface{}{
		"name": "Production SSH", "protocol": "tcp_udp", "mode": "direct", "ingressNodeId": nodeID,
		"listenHost": "0.0.0.0", "listenPort": 22022, "targetHost": "198.51.100.10", "targetPort": 22,
		"enabled": true, "bandwidthKbps": 512, "maxConnections": 100, "allowCidrs": []string{"192.0.2.0/24"}, "denyCidrs": []string{},
	}
	createRule := requestJSON(t, handler, http.MethodPost, "/api/v1/forward-rules", rulePayload, cookies[0])
	if createRule.Code != http.StatusCreated {
		t.Fatalf("create rule status=%d body=%s", createRule.Code, createRule.Body.String())
	}
	createdRule := responseObject(t, createRule)
	ruleID := createdRule["id"].(string)
	if createdRule["configVersion"] != float64(2) || createdRule["bandwidthKbps"] != float64(512) {
		t.Fatalf("unexpected rule version: %#v", createdRule)
	}
	conflictPayload := make(map[string]interface{}, len(rulePayload))
	for key, value := range rulePayload {
		conflictPayload[key] = value
	}
	conflictPayload["name"] = "Conflicting listener"
	conflict := requestJSON(t, handler, http.MethodPost, "/api/v1/forward-rules", conflictPayload, cookies[0])
	if conflict.Code != http.StatusConflict {
		t.Fatalf("listener conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}

	changedHeartbeatRequest := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", bytes.NewReader(heartbeatBody))
	changedHeartbeatRequest.Header.Set("Content-Type", "application/json")
	changedHeartbeatRequest.Header.Set("X-PortFlow-Node-ID", nodeID)
	changedHeartbeatRequest.Header.Set("Authorization", "Bearer "+credential)
	changedHeartbeatRequest.RemoteAddr = "192.0.2.11:40000"
	changedHeartbeat := httptest.NewRecorder()
	handler.ServeHTTP(changedHeartbeat, changedHeartbeatRequest)
	changedResult := responseObject(t, changedHeartbeat)
	if changedHeartbeat.Code != http.StatusOK || changedResult["configChanged"] != true || changedResult["configVersion"] != float64(2) {
		t.Fatalf("changed heartbeat status=%d body=%#v", changedHeartbeat.Code, changedResult)
	}

	updatedConfigRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent/config", nil)
	updatedConfigRequest.Header.Set("X-PortFlow-Node-ID", nodeID)
	updatedConfigRequest.Header.Set("Authorization", "Bearer "+credential)
	updatedConfig := httptest.NewRecorder()
	handler.ServeHTTP(updatedConfig, updatedConfigRequest)
	updatedConfigBody := responseObject(t, updatedConfig)
	if updatedConfigBody["version"] != float64(2) || len(updatedConfigBody["rules"].([]interface{})) != 1 {
		t.Fatalf("rule not published to agent config: %#v", updatedConfigBody)
	}
	appliedHeartbeatBody, _ := json.Marshal(map[string]interface{}{
		"agentVersion": "0.1.1", "tunnelAddress": "10.203.0.1", "configVersion": 2, "cpuPercent": 12.5,
		"memoryPercent": 34.5, "loadOne": 0.8, "diskPercent": 61.5, "networkRxBps": 125000, "networkTxBps": 64000,
		"activeConnections": 7, "bytesIn": 1024, "bytesOut": 2048,
		"ruleStats":           []map[string]interface{}{{"ruleId": ruleID, "activeConnections": 3, "bytesIn": 512, "bytesOut": 768}},
		"connectionsComplete": true,
		"connections":         []map[string]interface{}{{"id": "tcp-live-1", "ruleId": ruleID, "protocol": "tcp", "sourceAddress": "192.0.2.25:5000", "targetAddress": "198.51.100.10:22", "startedAt": "2026-07-11T11:58:00Z", "lastActivity": "2026-07-11T11:59:30Z", "bytesIn": 12, "bytesOut": 34}},
	})
	appliedHeartbeatRequest := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", bytes.NewReader(appliedHeartbeatBody))
	appliedHeartbeatRequest.Header.Set("Content-Type", "application/json")
	appliedHeartbeatRequest.Header.Set("X-PortFlow-Node-ID", nodeID)
	appliedHeartbeatRequest.Header.Set("Authorization", "Bearer "+credential)
	appliedHeartbeatRequest.RemoteAddr = "192.0.2.11:40000"
	appliedHeartbeat := httptest.NewRecorder()
	handler.ServeHTTP(appliedHeartbeat, appliedHeartbeatRequest)
	if appliedHeartbeat.Code != http.StatusOK {
		t.Fatalf("applied heartbeat status=%d", appliedHeartbeat.Code)
	}
	connectionsResponse := requestJSON(t, handler, http.MethodGet, "/api/v1/connections", nil, cookies[0])
	connectionsBody := responseObject(t, connectionsResponse)
	connections := connectionsBody["items"].([]interface{})
	if connectionsResponse.Code != http.StatusOK || len(connections) != 1 {
		t.Fatalf("live connections status=%d body=%#v", connectionsResponse.Code, connectionsBody)
	}
	connection := connections[0].(map[string]interface{})
	if connection["nodeId"] != nodeID || connection["ruleId"] != ruleID || connection["protocol"] != "tcp" || connection["bytesOut"] != float64(34) {
		t.Fatalf("unexpected live connection: %#v", connection)
	}
	dashboard := requestJSON(t, handler, http.MethodGet, "/api/v1/dashboard/summary", nil, cookies[0])
	dashboardRules := responseObject(t, dashboard)["rules"].(map[string]interface{})
	if dashboardRules["healthy"] != float64(1) || dashboardRules["degraded"] != float64(0) {
		t.Fatalf("applied rule health not reflected: %#v", dashboardRules)
	}

	rulePayload["targetPort"] = 2222
	updateRule := requestJSON(t, handler, http.MethodPut, "/api/v1/forward-rules/"+ruleID, rulePayload, cookies[0])
	if updateRule.Code != http.StatusOK || responseObject(t, updateRule)["configVersion"] != float64(3) {
		t.Fatalf("update rule status=%d body=%s", updateRule.Code, updateRule.Body.String())
	}
	ruleList := requestJSON(t, handler, http.MethodGet, "/api/v1/forward-rules", nil, cookies[0])
	ruleListBody := responseObject(t, ruleList)
	if ruleList.Code != http.StatusOK || len(ruleListBody["items"].([]interface{})) != 1 {
		t.Fatalf("list rules status=%d body=%s", ruleList.Code, ruleList.Body.String())
	}
	listedRule := ruleListBody["items"].([]interface{})[0].(map[string]interface{})
	if listedRule["activeConnections"] != float64(3) || listedRule["bytesIn"] != float64(512) {
		t.Fatalf("per-rule stats not reflected: %#v", listedRule)
	}
	failedHeartbeatBody, _ := json.Marshal(map[string]interface{}{
		"agentVersion": "0.1.1", "tunnelAddress": "10.203.0.1", "configVersion": 2, "attemptedConfigVersion": 3,
		"lastConfigError": "listen UDP 0.0.0.0:22022: address already in use",
		"cpuPercent":      12.5, "memoryPercent": 34.5, "loadOne": 0.8, "diskPercent": 61.5, "networkRxBps": 125000, "networkTxBps": 64000,
		"activeConnections": 7, "bytesIn": 1024, "bytesOut": 2048,
		"ruleStats": []map[string]interface{}{{"ruleId": ruleID, "activeConnections": 3, "bytesIn": 512, "bytesOut": 768}},
	})
	failedHeartbeatRequest := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", bytes.NewReader(failedHeartbeatBody))
	failedHeartbeatRequest.Header.Set("Content-Type", "application/json")
	failedHeartbeatRequest.Header.Set("X-PortFlow-Node-ID", nodeID)
	failedHeartbeatRequest.Header.Set("Authorization", "Bearer "+credential)
	failedHeartbeatRequest.RemoteAddr = "192.0.2.11:40000"
	failedHeartbeat := httptest.NewRecorder()
	handler.ServeHTTP(failedHeartbeat, failedHeartbeatRequest)
	if failedHeartbeat.Code != http.StatusOK {
		t.Fatalf("failed config heartbeat status=%d body=%s", failedHeartbeat.Code, failedHeartbeat.Body.String())
	}
	nodeList := requestJSON(t, handler, http.MethodGet, "/api/v1/nodes", nil, cookies[0])
	failedNode := responseObject(t, nodeList)["items"].([]interface{})[0].(map[string]interface{})
	if failedNode["appliedConfigVersion"] != float64(2) || failedNode["attemptedConfigVersion"] != float64(3) || failedNode["lastConfigError"] == "" {
		t.Fatalf("config application failure not reflected: %#v", failedNode)
	}
	deleteRule := requestJSON(t, handler, http.MethodDelete, "/api/v1/forward-rules/"+ruleID, nil, cookies[0])
	if deleteRule.Code != http.StatusNoContent {
		t.Fatalf("delete rule status=%d body=%s", deleteRule.Code, deleteRule.Body.String())
	}
	afterDeleteConfigRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent/config", nil)
	afterDeleteConfigRequest.Header.Set("X-PortFlow-Node-ID", nodeID)
	afterDeleteConfigRequest.Header.Set("Authorization", "Bearer "+credential)
	afterDeleteConfig := httptest.NewRecorder()
	handler.ServeHTTP(afterDeleteConfig, afterDeleteConfigRequest)
	afterDeleteBody := responseObject(t, afterDeleteConfig)
	if afterDeleteBody["version"] != float64(4) || len(afterDeleteBody["rules"].([]interface{})) != 0 {
		t.Fatalf("deleted rule still published: %#v", afterDeleteBody)
	}

	badHeartbeatRequest := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", bytes.NewReader(heartbeatBody))
	badHeartbeatRequest.Header.Set("Content-Type", "application/json")
	badHeartbeatRequest.Header.Set("X-PortFlow-Node-ID", nodeID)
	badHeartbeatRequest.Header.Set("Authorization", "Bearer wrong-credential")
	badHeartbeat := httptest.NewRecorder()
	handler.ServeHTTP(badHeartbeat, badHeartbeatRequest)
	if badHeartbeat.Code != http.StatusUnauthorized {
		t.Fatalf("bad heartbeat status=%d", badHeartbeat.Code)
	}

	reuse := requestJSON(t, handler, http.MethodPost, "/api/v1/agent/enroll", enrollmentPayload, nil)
	if reuse.Code != http.StatusUnauthorized {
		t.Fatalf("one-time token reuse status=%d", reuse.Code)
	}

	list := requestJSON(t, handler, http.MethodGet, "/api/v1/nodes", nil, cookies[0])
	if list.Code != http.StatusOK {
		t.Fatalf("list nodes status=%d body=%s", list.Code, list.Body.String())
	}
	items := responseObject(t, list)["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("unexpected nodes: %#v", items)
	}
	listedNode := items[0].(map[string]interface{})
	if listedNode["publicIp"] != "192.0.2.11" || listedNode["activeConnections"] != float64(7) ||
		listedNode["diskPercent"] != float64(61.5) || listedNode["networkRxBps"] != float64(125000) || listedNode["networkTxBps"] != float64(64000) {
		t.Fatalf("heartbeat metrics not reflected: %#v", listedNode)
	}
	traffic := requestJSON(t, handler, http.MethodGet, "/api/v1/metrics/traffic?hours=24", nil, cookies[0])
	if traffic.Code != http.StatusOK {
		t.Fatalf("traffic history status=%d body=%s", traffic.Code, traffic.Body.String())
	}
	trafficBody := responseObject(t, traffic)
	if trafficBody["intervalSeconds"] != float64(1800) {
		t.Fatalf("unexpected traffic history: %#v", trafficBody)
	}
	badTraffic := requestJSON(t, handler, http.MethodGet, "/api/v1/metrics/traffic?hours=0", nil, cookies[0])
	if badTraffic.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid traffic range status=%d", badTraffic.Code)
	}
	detail := requestJSON(t, handler, http.MethodGet, "/api/v1/nodes/"+nodeID+"?hours=24", nil, cookies[0])
	if detail.Code != http.StatusOK {
		t.Fatalf("node detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	detailBody := responseObject(t, detail)
	if detailBody["intervalSeconds"] != float64(1800) || detailBody["node"].(map[string]interface{})["id"] != nodeID || len(detailBody["points"].([]interface{})) != 1 {
		t.Fatalf("unexpected node detail: %#v", detailBody)
	}
	detailPoint := detailBody["points"].([]interface{})[0].(map[string]interface{})
	if detailPoint["diskPercent"] != float64(61.5) || detailPoint["networkRxBps"] != float64(125000) || detailPoint["networkTxBps"] != float64(64000) {
		t.Fatalf("node quality metrics missing: %#v", detailPoint)
	}
	badDetail := requestJSON(t, handler, http.MethodGet, "/api/v1/nodes/"+nodeID+"?hours=0", nil, cookies[0])
	if badDetail.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid node history range status=%d", badDetail.Code)
	}
	missingDetail := requestJSON(t, handler, http.MethodGet, "/api/v1/nodes/missing", nil, cookies[0])
	if missingDetail.Code != http.StatusNotFound {
		t.Fatalf("missing node detail status=%d", missingDetail.Code)
	}
	for _, historyRange := range []struct {
		hours, interval int
	}{{6, 300}, {168, 7200}, {720, 21600}} {
		response := requestJSON(t, handler, http.MethodGet, fmt.Sprintf("/api/v1/nodes/%s?hours=%d", nodeID, historyRange.hours), nil, cookies[0])
		if response.Code != http.StatusOK || responseObject(t, response)["intervalSeconds"] != float64(historyRange.interval) {
			t.Fatalf("node detail hours=%d status=%d body=%s", historyRange.hours, response.Code, response.Body.String())
		}
	}
	audit := requestJSON(t, handler, http.MethodGet, "/api/v1/audit-events?limit=20", nil, cookies[0])
	if audit.Code != http.StatusOK || len(responseObject(t, audit)["items"].([]interface{})) == 0 {
		t.Fatalf("audit list status=%d body=%s", audit.Code, audit.Body.String())
	}
	logs := requestJSON(t, handler, http.MethodGet, "/api/v1/agent-logs?level=warning&nodeId="+nodeID, nil, cookies[0])
	if logs.Code != http.StatusOK {
		t.Fatalf("agent log list status=%d body=%s", logs.Code, logs.Body.String())
	}
	logItems := responseObject(t, logs)["items"].([]interface{})
	if len(logItems) != 1 || logItems[0].(map[string]interface{})["id"] != "runtime-1" {
		t.Fatalf("agent logs were not deduplicated: %#v", logItems)
	}
	badLogs := requestJSON(t, handler, http.MethodGet, "/api/v1/agent-logs?level=debug", nil, cookies[0])
	if badLogs.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid agent log filter status=%d", badLogs.Code)
	}

	secondTokenResponse := requestJSON(t, handler, http.MethodPost, "/api/v1/enrollment-tokens", map[string]interface{}{
		"name": "Tokyo bootstrap", "expiresInMinutes": 30,
	}, cookies[0])
	secondToken := responseObject(t, secondTokenResponse)["token"].(string)
	secondEnroll := requestJSON(t, handler, http.MethodPost, "/api/v1/agent/enroll", map[string]string{
		"token": secondToken, "name": "Tokyo Exit 01", "region": "Tokyo", "architecture": "linux/amd64", "agentVersion": "0.1.1",
	}, nil)
	if secondEnroll.Code != http.StatusCreated {
		t.Fatalf("second enroll status=%d body=%s", secondEnroll.Code, secondEnroll.Body.String())
	}
	secondEnrollment := responseObject(t, secondEnroll)
	secondNodeID := secondEnrollment["node"].(map[string]interface{})["id"].(string)
	secondCredential := secondEnrollment["credential"].(string)
	relayPayload := map[string]interface{}{
		"name": "Shanghai to Tokyo draft", "protocol": "tcp", "mode": "relay", "ingressNodeId": nodeID, "egressNodeId": secondNodeID,
		"listenHost": "0.0.0.0", "listenPort": 24443, "targetHost": "198.51.100.20", "targetPort": 443, "enabled": true,
	}
	enabledRelay := requestJSON(t, handler, http.MethodPost, "/api/v1/forward-rules", relayPayload, cookies[0])
	if enabledRelay.Code != http.StatusUnprocessableEntity || responseObject(t, enabledRelay)["error"].(map[string]interface{})["code"] != "relay_not_provisioned" {
		t.Fatalf("enabled relay was not blocked: status=%d body=%s", enabledRelay.Code, enabledRelay.Body.String())
	}
	relayPayload["enabled"] = false
	relay := requestJSON(t, handler, http.MethodPost, "/api/v1/forward-rules", relayPayload, cookies[0])
	if relay.Code != http.StatusCreated {
		t.Fatalf("relay draft status=%d body=%s", relay.Code, relay.Body.String())
	}
	relayID := responseObject(t, relay)["id"].(string)
	for _, endpoint := range []struct{ id, credential string }{{nodeID, credential}, {secondNodeID, secondCredential}} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/config", nil)
		request.Header.Set("X-PortFlow-Node-ID", endpoint.id)
		request.Header.Set("Authorization", "Bearer "+endpoint.credential)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		body := responseObject(t, response)
		if response.Code != http.StatusOK || len(body["rules"].([]interface{})) != 1 || body["rules"].([]interface{})[0].(map[string]interface{})["mode"] != "relay" {
			t.Fatalf("relay was not distributed to endpoint %s: status=%d body=%#v", endpoint.id, response.Code, body)
		}
	}
	secondHeartbeat := httptest.NewRequest(http.MethodPost, "/api/v1/agent/heartbeat", bytes.NewBufferString(`{"agentVersion":"0.1.1","tunnelAddress":"10.203.0.2","configVersion":1,"cpuPercent":1,"memoryPercent":1,"loadOne":0}`))
	secondHeartbeat.Header.Set("Content-Type", "application/json")
	secondHeartbeat.Header.Set("X-PortFlow-Node-ID", secondNodeID)
	secondHeartbeat.Header.Set("Authorization", "Bearer "+secondCredential)
	secondHeartbeatResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondHeartbeatResponse, secondHeartbeat)
	if secondHeartbeatResponse.Code != http.StatusOK {
		t.Fatalf("second tunnel heartbeat status=%d body=%s", secondHeartbeatResponse.Code, secondHeartbeatResponse.Body.String())
	}
	relayPayload["enabled"] = true
	enableProvisionedRelay := requestJSON(t, handler, http.MethodPut, "/api/v1/forward-rules/"+relayID, relayPayload, cookies[0])
	if enableProvisionedRelay.Code != http.StatusOK {
		t.Fatalf("provisioned relay was not enabled: status=%d body=%s", enableProvisionedRelay.Code, enableProvisionedRelay.Body.String())
	}
	enabledRelayBody := responseObject(t, enableProvisionedRelay)
	if enabledRelayBody["relayHost"] != "10.203.0.2" || enabledRelayBody["egressConfigVersion"] == nil {
		t.Fatalf("relay transport metadata missing: %#v", enabledRelayBody)
	}
}

func TestProtectedEndpointRequiresAuthentication(t *testing.T) {
	for _, path := range []string{"/api/v1/nodes", "/api/v1/nodes/node-1"} {
		response := requestJSON(t, newTestServer(), http.MethodGet, path, nil, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("path=%s unexpected status: %d", path, response.Code)
		}
	}
}

func TestAgentLogsRequireAuthentication(t *testing.T) {
	response := requestJSON(t, newTestServer(), http.MethodGet, "/api/v1/agent-logs", nil, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status: %d", response.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	handler := newTestServer()
	setup := requestJSON(t, handler, http.MethodPost, "/api/v1/setup/admin", map[string]string{
		"username": "operator", "password": "a-long-test-password",
	}, nil)
	if setup.Code != http.StatusCreated {
		t.Fatal("setup failed")
	}
	for attempt := 0; attempt < 5; attempt++ {
		response := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"username": "operator", "password": "incorrect-password",
		}, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d", attempt, response.Code)
		}
	}
	limited := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "operator", "password": "a-long-test-password",
	}, nil)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "900" {
		t.Fatalf("rate limit status=%d headers=%v", limited.Code, limited.Header())
	}
}
