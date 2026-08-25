package desktop

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestCheckHealthUsesBoundedLoopbackEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/healthz" {
			t.Errorf("health request = %s %s", request.Method, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"status":"ok"}`)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	address := listener.Addr().(*net.TCPAddr)
	health, err := CheckHealth(context.Background(), "127.0.0.1", address.Port, time.Second)
	if err != nil {
		t.Fatalf("check health: %v", err)
	}
	if health.Status != "ok" {
		t.Fatalf("health = %#v", health)
	}
}

func TestCheckHealthRejectsNonLoopbackAddress(t *testing.T) {
	if _, err := CheckHealth(context.Background(), "192.0.2.1", 47820, time.Second); err == nil {
		t.Fatal("expected non-loopback address to fail")
	}
}
