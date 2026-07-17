package agent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestEnroll(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://control.example/api/v1/agent/enroll" {
			t.Fatalf("unexpected URL: %s", request.URL)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatal("content type missing")
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(`{
			"node":{"id":"nod_1","name":"Edge","status":"online","lastHeartbeat":"2026-07-11T00:00:00Z"},
			"credential":"secret-agent-credential"
		}`)), Header: make(http.Header)}, nil
	})}
	result, err := Enroll(context.Background(), client, "https://control.example", EnrollmentRequest{
		Token: "one-time-token", Name: "Edge", Architecture: "linux/amd64", AgentVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Node.ID != "nod_1" || result.Credential != "secret-agent-credential" {
		t.Fatalf("unexpected enrollment: %#v", result)
	}
}

func TestEnrollRequiresHTTPSExceptLoopback(t *testing.T) {
	if _, err := validateControlURL("http://control.example"); err == nil {
		t.Fatal("insecure remote control URL accepted")
	}
	if _, err := validateControlURL("http://127.0.0.1:8080"); err != nil {
		t.Fatalf("loopback development URL rejected: %v", err)
	}
	if _, err := validateControlURL("https://user:password@control.example"); err == nil {
		t.Fatal("URL credentials accepted")
	}
}
