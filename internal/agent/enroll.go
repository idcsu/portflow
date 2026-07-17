package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"portflow/internal/domain"
)

type EnrollmentRequest struct {
	Token        string `json:"token"`
	Name         string `json:"name"`
	Region       string `json:"region"`
	Architecture string `json:"architecture"`
	AgentVersion string `json:"agentVersion"`
}

type EnrollmentResponse struct {
	Node       domain.Node `json:"node"`
	Credential string      `json:"credential"`
}

func Enroll(ctx context.Context, client *http.Client, controlURL string, request EnrollmentRequest) (EnrollmentResponse, error) {
	base, err := validateControlURL(controlURL)
	if err != nil {
		return EnrollmentResponse{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	contents, err := json.Marshal(request)
	if err != nil {
		return EnrollmentResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base.String(), "/")+"/api/v1/agent/enroll", bytes.NewReader(contents))
	if err != nil {
		return EnrollmentResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("User-Agent", "PortFlow-Agent/"+request.AgentVersion)
	response, err := client.Do(httpRequest)
	if err != nil {
		return EnrollmentResponse{}, fmt.Errorf("contact control server: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 1<<20)
	if response.StatusCode != http.StatusCreated {
		var failure struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(limited).Decode(&failure)
		if failure.Error.Message == "" {
			failure.Error.Message = response.Status
		}
		return EnrollmentResponse{}, fmt.Errorf("enrollment rejected: %s", failure.Error.Message)
	}
	var enrollment EnrollmentResponse
	if err := json.NewDecoder(limited).Decode(&enrollment); err != nil {
		return EnrollmentResponse{}, fmt.Errorf("decode enrollment response: %w", err)
	}
	if enrollment.Node.ID == "" || enrollment.Credential == "" {
		return EnrollmentResponse{}, errors.New("control server returned an incomplete agent identity")
	}
	return enrollment, nil
}

func validateControlURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("control URL is invalid")
	}
	if parsed.Scheme == "https" {
		return parsed, nil
	}
	if parsed.Scheme == "http" && isLoopback(parsed.Hostname()) {
		return parsed, nil
	}
	return nil, errors.New("control URL must use HTTPS; HTTP is only allowed for loopback development")
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
