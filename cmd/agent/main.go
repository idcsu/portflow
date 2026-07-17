package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"portflow/internal/agent"
	"portflow/internal/forward"
)

var agentVersion = "dev"

func main() {
	configPath := flag.String("config", "data/agent/config.json", "path to the local agent configuration")
	controlURL := flag.String("control-url", "", "control server URL used for first enrollment")
	enrollmentToken := flag.String("enrollment-token", "", "one-time enrollment token")
	enrollmentTokenFile := flag.String("enrollment-token-file", "", "read the one-time enrollment token from a permission-restricted file")
	enrollOnly := flag.Bool("enroll-only", false, "save enrollment identity and exit without starting forwarding")
	configureOnly := flag.Bool("configure-only", false, "update existing local configuration from flags and exit")
	nodeName := flag.String("name", "", "node name used for first enrollment")
	region := flag.String("region", "", "optional node region")
	tunnelAddress := flag.String("tunnel-address", "", "private IPv4 address already assigned to the WireGuard interface")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("portflow-agent %s\n", agentVersion)
		return
	}

	configStore := agent.NewConfigStore(*configPath)
	var config agent.Config
	token := strings.TrimSpace(*enrollmentToken)
	if *enrollmentTokenFile != "" {
		if token != "" {
			log.Fatal("enrollment-token and enrollment-token-file cannot be used together")
		}
		var err error
		token, err = agent.ReadEnrollmentToken(*enrollmentTokenFile)
		if err != nil {
			log.Fatalf("read enrollment token: %v", err)
		}
	}
	if token != "" {
		if *configureOnly {
			log.Fatal("configure-only cannot be used during enrollment")
		}
		if strings.TrimSpace(*controlURL) == "" || strings.TrimSpace(*nodeName) == "" {
			log.Fatal("control-url and name are required with enrollment-token")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		enrollment, err := agent.Enroll(ctx, &http.Client{Timeout: 20 * time.Second}, *controlURL, agent.EnrollmentRequest{
			Token: token, Name: *nodeName, Region: *region,
			Architecture: runtime.GOOS + "/" + runtime.GOARCH, AgentVersion: agentVersion,
		})
		if err != nil {
			log.Fatalf("enroll agent: %v", err)
		}
		config = agent.Config{Version: 1, ControlURL: strings.TrimRight(*controlURL, "/"), NodeID: enrollment.Node.ID, Credential: enrollment.Credential, TunnelAddress: strings.TrimSpace(*tunnelAddress), Rules: nil}
		if err := configStore.Save(config); err != nil {
			log.Fatalf("save agent identity: %v", err)
		}
		log.Printf("node %s enrolled successfully; identity saved to %s", enrollment.Node.ID, *configPath)
		if *enrollOnly {
			return
		}
	} else {
		if *enrollOnly {
			log.Fatal("enroll-only requires enrollment-token or enrollment-token-file")
		}
		if *configureOnly && strings.TrimSpace(*tunnelAddress) == "" {
			log.Fatal("configure-only requires tunnel-address")
		}
		var err error
		config, err = configStore.Load()
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("no local configuration at %s; waiting for enrollment", *configPath)
				return
			}
			log.Fatalf("load agent configuration: %v", err)
		}
		if value := strings.TrimSpace(*tunnelAddress); value != "" && value != config.TunnelAddress {
			config.TunnelAddress = value
			if err := configStore.Save(config); err != nil {
				log.Fatalf("save tunnel address: %v", err)
			}
		}
		if *configureOnly {
			log.Printf("updated local tunnel address to %s", config.TunnelAddress)
			return
		}
	}

	log.Printf("loaded agent configuration version %d with %d forwarding rules", config.Version, len(config.Rules))
	control, err := agent.NewControlClient(config.ControlURL, config.NodeID, config.Credential, nil)
	if err != nil {
		log.Fatalf("initialize control client: %v", err)
	}
	logBuffer := agent.NewLogBuffer(1000)
	agentLogf := logBuffer.Logger("agent", log.Printf)
	forwardLogf := logBuffer.Logger("forward", log.Printf)
	manager := forward.NewManager(forward.Options{NodeID: config.NodeID, Logf: forwardLogf})
	runner := agent.NewRuntime(configStore, control, manager, agentVersion, agentLogf, logBuffer)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runner.Run(ctx, config); err != nil {
		log.Fatalf("agent stopped: %v", err)
	}
	log.Printf("agent stopped cleanly")
}
