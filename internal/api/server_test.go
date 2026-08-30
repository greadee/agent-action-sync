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

func (service *testAdministrationService) Status(context.Context) (AdminStatus, error) {
	return AdminStatus{Status: "running", APIVersion: APIVersion, DeviceID: "device:test", Fingerprint: "sha256:test", Lifecycle: LifecycleRunning}, nil
}

func (service *testAdministrationService) Diagnostics(context.Context, int) (AdminDiagnostics, error) {
	return AdminDiagnostics{}, nil
}

func TestServerServesHardenedControlPlaneShell(t *testing.T) {
	credential := testAdminCredential(0x60)
	server := newTestServer(t, "127.0.0.1:47820", credential, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47820/ui/", nil)
	recorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "SyncGate Control Plane") {
		t.Fatalf("control plane response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Security-Policy") == "" || recorder.Header().Get("X-Frame-Options") != "DENY" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("security headers = %#v", recorder.Header())
	}
	if strings.Contains(recorder.Body.String(), string(credential)) || recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("control plane disclosed a credential or enabled CORS")
	}
	for _, marker := range []string{"project-picker", "readiness-graph", "assignment-detail", "worker-list", "node-list", "control-dialog", "dispatch-preview", "assignment-evidence", "incident-list", "federated-node-list"} {
		if !strings.Contains(recorder.Body.String(), marker) {
			t.Fatalf("control plane shell missing Slice 6 marker %q", marker)
		}
	}
}

func TestControlPlaneScriptUsesOnlyBoundedExplicitControls(t *testing.T) {
	asset, err := controlPlaneAssets.ReadFile("controlplane/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(asset)
	for _, route := range []string{"/api/v1/orchestration/projects", "/tasks", "/assignments", "/api/v1/orchestration/workers", "/api/v1/orchestration/nodes", "/approve", "/dispatch/preview", "/scheduler/", "/controls", "/integration"} {
		if !strings.Contains(script, route) {
			t.Fatalf("visibility script is missing route marker %q", route)
		}
	}
	if !strings.Contains(script, "/api/v1/browser-session/bootstrap") || !strings.Contains(script, browserCSRFHeader) {
		t.Fatal("control script is missing protected bootstrap or CSRF submission")
	}
	for _, marker := range []string{"showModal()", "crypto.randomUUID()", "Submit same key again", "already_present", "observed_budget", "Gate summary", "result-evidence", "telemetry-evidence", "incident-item", "await_reconciliation", "/api/v1/federation/nodes", "federated-node-item"} {
		if !strings.Contains(script, marker) {
			t.Fatalf("control script is missing confirmation or replay marker %q", marker)
		}
	}
	if strings.Contains(script, "localStorage") || strings.Contains(script, "sessionStorage") {
		t.Fatal("control script persisted browser-session control state")
	}
	if strings.Contains(script, `method: "PUT"`) || strings.Contains(script, `method: "DELETE"`) {
		t.Fatal("visibility script added an unsafe control method")
	}
}

func TestServerBrowserSessionBootstrapAndCSRFBoundary(t *testing.T) {
	credential := testAdminCredential(0x5f)
	called := false
	server := newTestServer(t, "127.0.0.1:47820", credential, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		called = true
		if !IsAdminAuthenticated(request.Context()) {
			t.Fatal("browser request did not receive authenticated context")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))

	unauthenticatedIssue := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47820/api/v1/browser-sessions", nil)
	unauthenticatedRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(unauthenticatedRecorder, unauthenticatedIssue)
	if unauthenticatedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated issue status = %d", unauthenticatedRecorder.Code)
	}

	issue := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47820/api/v1/browser-sessions", nil)
	issue.Header.Set("Authorization", "Bearer "+string(credential))
	issueRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(issueRecorder, issue)
	if issueRecorder.Code != http.StatusCreated {
		t.Fatalf("issue status = %d body=%s", issueRecorder.Code, issueRecorder.Body.String())
	}
	var ticket BrowserSessionTicketResponse
	if err := json.Unmarshal(issueRecorder.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"bootstrap_token":%q}`, ticket.BootstrapToken)
	badExchange := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47820/api/v1/browser-session/bootstrap", strings.NewReader(body))
	badExchange.Header.Set("Content-Type", "application/json")
	badExchange.Header.Set("Origin", "http://127.0.0.1:3000")
	badRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(badRecorder, badExchange)
	if badRecorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin exchange status = %d", badRecorder.Code)
	}

	exchange := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47820/api/v1/browser-session/bootstrap", strings.NewReader(body))
	exchange.Header.Set("Content-Type", "application/json")
	exchange.Header.Set("Origin", "http://127.0.0.1:47820")
	exchange.Header.Set("Sec-Fetch-Site", "same-origin")
	exchangeRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(exchangeRecorder, exchange)
	if exchangeRecorder.Code != http.StatusOK {
		t.Fatalf("exchange status = %d body=%s", exchangeRecorder.Code, exchangeRecorder.Body.String())
	}
	var document BrowserSessionDocument
	if err := json.Unmarshal(exchangeRecorder.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Health != "ok" || document.Status.DeviceID != "device:test" || len(document.Capabilities) != len(browserCapabilities) || document.CSRFToken == "" {
		t.Fatalf("session document = %#v", document)
	}
	for index, capability := range browserCapabilities {
		if document.Capabilities[index] != capability {
			t.Fatalf("capability %d = %q, want %q", index, document.Capabilities[index], capability)
		}
	}
	cookies := exchangeRecorder.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookies = %#v", cookies)
	}

	called = false
	visibility := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47820/api/v1/orchestration/projects", nil)
	visibility.AddCookie(cookies[0])
	visibilityRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(visibilityRecorder, visibility)
	if visibilityRecorder.Code != http.StatusNoContent || !called {
		t.Fatalf("browser visibility response = %d, called=%t", visibilityRecorder.Code, called)
	}

	called = false
	federation := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47820/api/v1/federation/nodes", nil)
	federation.AddCookie(cookies[0])
	federationRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(federationRecorder, federation)
	if federationRecorder.Code != http.StatusNoContent || !called {
		t.Fatalf("browser federation response = %d, called=%t", federationRecorder.Code, called)
	}

	called = false
	remoteMutation := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47820/api/v1/federation/nodes", strings.NewReader(`{}`))
	remoteMutation.AddCookie(cookies[0])
	remoteMutation.Header.Set("Origin", "http://127.0.0.1:47820")
	remoteMutation.Header.Set("Sec-Fetch-Site", "same-origin")
	remoteMutation.Header.Set(browserCSRFHeader, document.CSRFToken)
	remoteMutationRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(remoteMutationRecorder, remoteMutation)
	if remoteMutationRecorder.Code != http.StatusForbidden || called {
		t.Fatalf("browser remote mutation response = %d, called=%t", remoteMutationRecorder.Code, called)
	}

	called = false
	outsideScope := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47820/api/v1/shares", nil)
	outsideScope.AddCookie(cookies[0])
	outsideScopeRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(outsideScopeRecorder, outsideScope)
	if outsideScopeRecorder.Code != http.StatusForbidden || called {
		t.Fatalf("out-of-scope browser response = %d, called=%t", outsideScopeRecorder.Code, called)
	}

	called = false
	missingCSRF := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47820/api/v1/projects/project-one/scheduler/disable", strings.NewReader(`{}`))
	missingCSRF.AddCookie(cookies[0])
	missingCSRF.Header.Set("Origin", "http://127.0.0.1:47820")
	missingCSRFRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(missingCSRFRecorder, missingCSRF)
	if missingCSRFRecorder.Code != http.StatusForbidden || called {
		t.Fatalf("missing-CSRF control response = %d, called=%t", missingCSRFRecorder.Code, called)
	}

	called = false
	control := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47820/api/v1/projects/project-one/scheduler/disable", strings.NewReader(`{}`))
	control.AddCookie(cookies[0])
	control.Header.Set("Origin", "http://127.0.0.1:47820")
	control.Header.Set("Sec-Fetch-Site", "same-origin")
	control.Header.Set(browserCSRFHeader, document.CSRFToken)
	controlRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(controlRecorder, control)
	if controlRecorder.Code != http.StatusNoContent || !called {
		t.Fatalf("CSRF-authenticated control response = %d, called=%t", controlRecorder.Code, called)
	}

	called = false
	mutation := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:47820/api/v1/jobs/job-1/actions", strings.NewReader(`{}`))
	mutation.AddCookie(cookies[0])
	mutation.Header.Set("Origin", "http://127.0.0.1:47820")
	mutation.Header.Set(browserCSRFHeader, document.CSRFToken)
	mutationRecorder := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(mutationRecorder, mutation)
	if mutationRecorder.Code != http.StatusForbidden || called {
		t.Fatalf("out-of-scope browser mutation = %d, called=%t", mutationRecorder.Code, called)
	}
}

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
