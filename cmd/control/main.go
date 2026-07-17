package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"portflow/internal/control"
	"portflow/internal/store"
	"portflow/internal/store/postgres"
)

var version = "dev"

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	healthcheckURL := flag.String("healthcheck", "", "check a control health URL and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("portflow-control %s\n", version)
		return
	}
	if *healthcheckURL != "" {
		if err := runHealthcheck(*healthcheckURL); err != nil {
			log.Fatal(err)
		}
		return
	}

	storage, storageMode, err := openStore()
	if err != nil {
		log.Fatalf("initialize storage: %v", err)
	}
	defer storage.Close()

	secureCookies := !strings.EqualFold(os.Getenv("PORTFLOW_SECURE_COOKIES"), "false")
	server := &http.Server{
		Addr: *listen,
		Handler: control.NewServer(control.Options{
			Build: control.BuildInfo{Version: version}, Store: storage, StorageMode: storageMode, SecureCookies: secureCookies,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("control server listening on %s", *listen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("control server failed: %v", err)
		}
	}()

	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}

func runHealthcheck(address string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(address)
	if err != nil {
		return fmt.Errorf("healthcheck request: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck returned %s", response.Status)
	}
	return nil
}

func openStore() (store.Store, string, error) {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		log.Printf("WARNING: DATABASE_URL is empty; using volatile in-memory development storage")
		return store.NewMemory(), "memory", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	storage, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return nil, "", err
	}
	return storage, "postgres", nil
}
