package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testAdministrationService struct {
	ready atomic.Bool
}

func (service *testAdministrationService) Ready() bool { return service.ready.Load() }

func TestServerHealthzIsMinimalAndV1RequiresAuthentication(t *testing.T) {
	credential := testAdminCredential(0x61)
	called := false
	server := newTestServer(t, "127.0.0.1:47820", credential, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		called = true
		writer.WriteHeader(http.StatusNoContent)
	}))

	healthRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47820/healthz", nil)
	healthRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(healthRecorder, healthRequest)
	if healthRecorder.Code != http.StatusOK {
		t.Fatalf("health status = %d", healthRecorder.Code)
	}
	var health map[string]any
	if err := json.Unmarshal(healthRecorder.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if len(health) != 1 || health["status"] != "ok" {
		t.Fatalf("health exposed more than liveness: %#v", health)
	}

	protectedRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47820/api/v1/status", nil)
	protectedRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(protectedRecorder, protectedRequest)
	if protectedRecorder.Code != http.StatusUnauthorized || called {
		t.Fatalf("unauthenticated v1 response = %d, handler called = %t", protectedRecorder.Code, called)
	}

	protectedRequest = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47820/api/v1/status", nil)
	protectedRequest.Header.Set("Authorization", "Bearer "+string(credential))
	protectedRecorder = httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(protectedRecorder, protectedRequest)
	if protectedRecorder.Code != http.StatusNoContent || !called {
		t.Fatalf("authenticated v1 response = %d, handler called = %t", protectedRecorder.Code, called)
	}
}

func TestServerRejectsUnsafeBindingsAndDependencies(t *testing.T) {
	credential := testAdminCredential(0x62)
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatal(err)
	}
	service := &testAdministrationService{}
	service.ready.Store(true)
	valid := ServerOptions{Address: "127.0.0.1:47820", Service: service, Authenticator: authenticator, V1Handler: http.NotFoundHandler()}

	for _, address := range []string{"0.0.0.0:47820", "[::]:47820", "192.0.2.1:47820", ":47820", "example.com:47820", "127.0.0.1:0"} {
		options := valid
		options.Address = address
		if _, err := NewServer(options); err == nil {
			t.Errorf("unsafe address %q was accepted", address)
		}
	}
	for name, mutate := range map[string]func(*ServerOptions){
		"service":       func(options *ServerOptions) { options.Service = nil },
		"authenticator": func(options *ServerOptions) { options.Authenticator = nil },
		"handler":       func(options *ServerOptions) { options.V1Handler = nil },
	} {
		t.Run(name, func(t *testing.T) {
			options := valid
			mutate(&options)
			if _, err := NewServer(options); err == nil {
				t.Fatal("missing dependency was accepted")
			}
		})
	}

	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("wildcard listener unavailable: %v", err)
	}
	server, err := NewServer(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(listener); err == nil {
		t.Fatal("wildcard listener was accepted")
	}
}

func TestServerAcceptsIPv4AndIPv6Loopback(t *testing.T) {
	for _, test := range []struct {
		name, network, listen, configured string
	}{
		{name: "ipv4", network: "tcp4", listen: "127.0.0.1:0", configured: "127.0.0.1:47820"},
		{name: "ipv6", network: "tcp6", listen: "[::1]:0", configured: "[::1]:47820"},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen(test.network, test.listen)
			if err != nil {
				if test.name == "ipv6" {
					t.Skipf("IPv6 loopback unavailable: %v", err)
				}
				t.Fatal(err)
			}
			server := newTestServer(t, test.configured, testAdminCredential(0x63), http.NotFoundHandler())
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() {
				_ = server.Shutdown(context.Background())
				if err := <-done; err != nil {
					t.Errorf("serve: %v", err)
				}
			})
			response, err := http.Get("http://" + listener.Addr().String() + "/healthz")
			if err != nil {
				t.Fatalf("health request: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("health status = %d", response.StatusCode)
			}
		})
	}
}

func TestServerRejectsRebindingHostsAndBrowserOrigins(t *testing.T) {
	credential := testAdminCredential(0x64)
	server := newTestServer(t, "127.0.0.1:47820", credential, http.NotFoundHandler())
	for _, test := range []struct {
		name, host, origin string
	}{
		{name: "domain host", host: "attacker.example:47820"},
		{name: "userinfo host", host: "127.0.0.1@attacker.example"},
		{name: "invalid port", host: "127.0.0.1:not-a-port"},
		{name: "local browser origin", host: "127.0.0.1:47820", origin: "http://127.0.0.1:3000"},
		{name: "null browser origin", host: "127.0.0.1:47820", origin: "null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47820/healthz", nil)
			request.Host = test.host
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			recorder := httptest.NewRecorder()
			server.httpServer.Handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d", recorder.Code)
			}
			if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("server emitted a CORS allow header")
			}
		})
	}
}

func TestServerEnforcesBodyAndHeaderLimits(t *testing.T) {
	credential := testAdminCredential(0x65)
	called := false
	server := newTestServer(t, "127.0.0.1:47820", credential, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47820/api/v1/pairing/inspect", strings.NewReader(strings.Repeat("x", int(maxJSONBodyBytes)+1)))
	request.Header.Set("Authorization", "Bearer "+string(credential))
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge || called {
		t.Fatalf("oversized response = %d, handler called = %t", recorder.Code, called)
	}
	if server.httpServer.MaxHeaderBytes != defaultMaxHeaderBytes || server.httpServer.ReadHeaderTimeout != defaultReadHeaderTimeout {
		t.Fatalf("server limits = headers %d timeout %s", server.httpServer.MaxHeaderBytes, server.httpServer.ReadHeaderTimeout)
	}
}

func TestServerSlowHeadersFailSafely(t *testing.T) {
	credential := testAdminCredential(0x66)
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatal(err)
	}
	service := &testAdministrationService{}
	service.ready.Store(true)
	server, err := NewServer(ServerOptions{
		Address: "127.0.0.1:47820", Service: service, Authenticator: authenticator,
		V1Handler: http.NotFoundHandler(), ReadHeaderTimeout: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})
	connection, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := fmt.Fprintf(connection, "GET /healthz HTTP/1.1\r\nHost: 127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	statusLine, readErr := bufio.NewReader(connection).ReadString('\n')
	if readErr == nil && strings.Contains(statusLine, " 200 ") {
		t.Fatalf("slow incomplete headers received success: %q", statusLine)
	}
}

func TestServerGracefulDrainWaitsForInflightRequest(t *testing.T) {
	credential := testAdminCredential(0x67)
	started := make(chan struct{})
	release := make(chan struct{})
	server := newTestServer(t, "127.0.0.1:47820", credential, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/api/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+string(credential))
	responseDone := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		responseDone <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown(context.Background()) }()

	deadline := time.Now().Add(time.Second)
	for !server.draining.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	healthRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47820/healthz", nil)
	healthRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(healthRecorder, healthRequest)
	if healthRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("draining health status = %d", healthRecorder.Code)
	}
	drainingRequest := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47820/api/v1/jobs/job-1/actions", nil)
	drainingRequest.Header.Set("Authorization", "Bearer "+string(credential))
	drainingRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(drainingRecorder, drainingRequest)
	if drainingRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("draining v1 status = %d", drainingRecorder.Code)
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before in-flight request completed: %v", err)
	default:
	}
	close(release)
	if err := <-responseDone; err != nil {
		t.Fatalf("in-flight request: %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestServerHealthReportsServiceNotReady(t *testing.T) {
	credential := testAdminCredential(0x68)
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatal(err)
	}
	service := &testAdministrationService{}
	server, err := NewServer(ServerOptions{Address: "127.0.0.1:47820", Service: service, Authenticator: authenticator, V1Handler: http.NotFoundHandler()})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47820/healthz", nil)
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status = %d", recorder.Code)
	}
}

func newTestServer(t *testing.T, address string, credential []byte, handler http.Handler) *Server {
	t.Helper()
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	service := &testAdministrationService{}
	service.ready.Store(true)
	server, err := NewServer(ServerOptions{Address: address, Service: service, Authenticator: authenticator, V1Handler: handler})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return server
}

func TestServerServeRejectsNilListener(t *testing.T) {
	server := newTestServer(t, "127.0.0.1:47820", testAdminCredential(0x69), http.NotFoundHandler())
	if err := server.Serve(nil); err == nil {
		t.Fatal("nil listener was accepted")
	}
}

func TestServerShutdownHonorsDeadline(t *testing.T) {
	server := newTestServer(t, "127.0.0.1:47820", testAdminCredential(0x70), http.NotFoundHandler())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := server.Shutdown(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v", err)
	}
}
