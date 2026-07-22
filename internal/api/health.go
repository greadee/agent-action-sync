package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type HealthServer struct {
	server *http.Server
}

type HealthResponse struct {
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
}

func NewHealthServer(address string, startedAt time.Time) (*HealthServer, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse health address: %w", err)
	}
	if !isLoopback(host) {
		return nil, fmt.Errorf("health server must bind to loopback, got %q", host)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(HealthResponse{Status: "ok", StartedAt: startedAt.UTC()})
	})
	return &HealthServer{server: &http.Server{Addr: address, Handler: mux}}, nil
}

func (health *HealthServer) ListenAndServe() error {
	err := health.server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (health *HealthServer) Close() error {
	return health.server.Close()
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
