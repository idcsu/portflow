package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"portflow/internal/forward"
)

func TestSynchronizeAppliesAndPersistsNewConfiguration(t *testing.T) {
	requests := 0
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Bearer credential" || request.Header.Get("X-PortFlow-Node-ID") != "node-1" {
			t.Fatal("agent authentication headers missing")
		}
		body := `{"serverTime":"2026-07-12T00:00:00Z","configVersion":2,"configChanged":true,"heartbeatIntervalSeconds":15}`
		if request.URL.Path == "/api/v1/agent/config" {
			body = `{"version":2,"rules":[]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	control, err := NewControlClient("https://control.example", "node-1", "credential", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewConfigStore(path)
	manager := forward.NewManager(forward.Options{})
	defer manager.Close()
	runtime := NewRuntime(store, control, manager, "test", nil, nil)
	current := Config{Version: 1, ControlURL: "https://control.example", NodeID: "node-1", Credential: "credential", Rules: nil}
	if _, err := runtime.synchronize(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || current.Version != 2 {
		t.Fatalf("requests=%d config=%#v", requests, current)
	}
	persisted, err := store.Load()
	if err != nil || persisted.Version != 2 {
		t.Fatalf("persisted config=%#v err=%v", persisted, err)
	}
}

func TestSynchronizeKeepsCurrentConfigWhenPersistenceFails(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"configVersion":2,"configChanged":true,"heartbeatIntervalSeconds":15}`
		if request.URL.Path == "/api/v1/agent/config" {
			body = `{"version":2,"rules":[]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	control, _ := NewControlClient("https://control.example", "node-1", "credential", httpClient)
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := forward.NewManager(forward.Options{})
	defer manager.Close()
	runtime := NewRuntime(NewConfigStore(filepath.Join(parentFile, "config.json")), control, manager, "test", nil, nil)
	current := Config{Version: 1, ControlURL: "https://control.example", NodeID: "node-1", Credential: "credential"}
	if _, err := runtime.synchronize(context.Background(), &current); err == nil {
		t.Fatal("persistence failure ignored")
	}
	if current.Version != 1 {
		t.Fatalf("current config advanced despite persistence failure: %#v", current)
	}
	if runtime.attemptedConfigVersion != 2 || runtime.lastConfigError == "" {
		t.Fatalf("configuration failure not retained: version=%d error=%q", runtime.attemptedConfigVersion, runtime.lastConfigError)
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	if retryDelay(1).Seconds() != 15 || retryDelay(20).Minutes() != 2 {
		t.Fatal("unexpected retry delay")
	}
}

func TestSynchronizeAcknowledgesLogsOnlyAfterSuccessfulHeartbeat(t *testing.T) {
	attempts := 0
	var eventIDs []string
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		var heartbeat Heartbeat
		if err := json.NewDecoder(request.Body).Decode(&heartbeat); err != nil {
			t.Fatal(err)
		}
		if len(heartbeat.Logs) != 1 {
			t.Fatalf("attempt %d logs=%#v", attempts, heartbeat.Logs)
		}
		if !heartbeat.ConnectionsComplete || heartbeat.Connections == nil {
			t.Fatalf("attempt %d missing bounded connection snapshot metadata: %#v", attempts, heartbeat)
		}
		eventIDs = append(eventIDs, heartbeat.Logs[0].ID)
		if attempts == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporary"}}`)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"configVersion":1,"configChanged":false,"heartbeatIntervalSeconds":15}`)), Header: make(http.Header)}, nil
	})}
	control, _ := NewControlClient("https://control.example", "node-1", "credential", httpClient)
	manager := forward.NewManager(forward.Options{})
	defer manager.Close()
	logs := NewLogBuffer(10)
	logs.Logger("agent", nil)("control retry")
	runtime := NewRuntime(NewConfigStore(filepath.Join(t.TempDir(), "config.json")), control, manager, "test", nil, logs)
	current := Config{Version: 1, ControlURL: "https://control.example", NodeID: "node-1", Credential: "credential"}
	if _, err := runtime.synchronize(context.Background(), &current); err == nil || len(logs.Pending(10)) != 1 {
		t.Fatalf("failed heartbeat acknowledged logs: err=%v pending=%#v", err, logs.Pending(10))
	}
	if _, err := runtime.synchronize(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	if len(logs.Pending(10)) != 0 || len(eventIDs) != 2 || eventIDs[0] != eventIDs[1] {
		t.Fatalf("log retry was not stable or acknowledged: ids=%#v pending=%#v", eventIDs, logs.Pending(10))
	}
}
