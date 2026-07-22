package api

import (
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHealthServerResponds(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	server, err := NewHealthServer(address, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatalf("NewHealthServer: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})

	var response *http.Response
	for i := 0; i < 20; i++ {
		response, err = http.Get("http://" + address + "/health")
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer response.Body.Close()
	var health HealthResponse
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if health.Status != "ok" {
		t.Fatalf("status = %q", health.Status)
	}
}

func TestHealthServerRejectsNonLoopback(t *testing.T) {
	if _, err := NewHealthServer("0.0.0.0:47820", time.Now()); err == nil {
		t.Fatal("expected non-loopback bind to be rejected")
	}
}
