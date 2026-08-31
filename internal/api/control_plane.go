package api

import (
	"context"
	"embed"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const browserCSRFHeader = "X-SyncGate-CSRF"

var browserCapabilities = []string{
	"node.health.read",
	"node.status.read",
	"project.visibility.read",
	"task.readiness.read",
	"assignment.visibility.read",
	"result-budget-incident.read",
	"paired-node-status.read",
	"cross-node-project.read",
	"worker-node.inventory.read",
	"operator.controls.write",
}

//go:embed controlplane/*
var controlPlaneAssets embed.FS

type BrowserSessionTicketResponse struct {
	BootstrapToken string    `json:"bootstrap_token"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type BrowserSessionBootstrapInput struct {
	BootstrapToken string `json:"bootstrap_token"`
}

func (input BrowserSessionBootstrapInput) Validate() error {
	if len(input.BootstrapToken) < 32 || len(input.BootstrapToken) > 128 || strings.TrimSpace(input.BootstrapToken) != input.BootstrapToken {
		return errors.New("bootstrap token is invalid")
	}
	return nil
}

type BrowserSessionDocument struct {
	APIVersion       string      `json:"api_version"`
	Health           string      `json:"health"`
	Status           AdminStatus `json:"status"`
	Capabilities     []string    `json:"capabilities"`
	CSRFToken        string      `json:"csrf_token"`
	SessionExpiresAt time.Time   `json:"session_expires_at"`
}

func (server *Server) serveControlPlane(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		writeError(writer, request, errMethodNotAllowed)
		return
	}
	asset := "controlplane/index.html"
	contentType := "text/html; charset=utf-8"
	switch request.URL.Path {
	case "/", "/ui", "/ui/":
	case "/ui/app.css":
		asset, contentType = "controlplane/app.css", "text/css; charset=utf-8"
	case "/ui/app.js":
		asset, contentType = "controlplane/app.js", "text/javascript; charset=utf-8"
	default:
		writeError(writer, request, errNotFound)
		return
	}
	body, err := controlPlaneAssets.ReadFile(asset)
	if err != nil {
		writeError(writer, request, errUnavailable)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(body)
	}
}

func (server *Server) issueBrowserSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeError(writer, request, errMethodNotAllowed)
		return
	}
	ticket, err := server.browserSessions.Issue(request.Context())
	if err != nil {
		writeError(writer, request, errUnavailable)
		return
	}
	writeJSON(writer, request, http.StatusCreated, BrowserSessionTicketResponse(ticket))
}

func (server *Server) exchangeBrowserSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeError(writer, request, errMethodNotAllowed)
		return
	}
	if !sameOriginRequest(request) {
		writeError(writer, request, errForbidden)
		return
	}
	var input BrowserSessionBootstrapInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, request, err)
		return
	}
	exchange, err := server.browserSessions.Exchange(request.Context(), input.BootstrapToken)
	if err != nil {
		writeError(writer, request, errUnauthorized)
		return
	}
	setBrowserSessionCookie(writer, exchange)
	context := withAdminAuthentication(request)
	document, err := server.browserSessionDocument(context, exchange.CSRFToken, exchange.ExpiresAt)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, document)
}

func (server *Server) readBrowserSession(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeError(writer, request, errMethodNotAllowed)
		return
	}
	context, err := server.browserSessions.Authenticate(request)
	if err != nil {
		writeError(writer, request, errUnauthorized)
		return
	}
	csrfToken, expiresAt, err := server.browserSessions.RotateCSRF(request)
	if err != nil {
		writeError(writer, request, errUnauthorized)
		return
	}
	document, err := server.browserSessionDocument(request.WithContext(context), csrfToken, expiresAt)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	writeJSON(writer, request, http.StatusOK, document)
}

func (server *Server) browserSessionDocument(request *http.Request, csrfToken string, expiresAt time.Time) (BrowserSessionDocument, error) {
	reader, ok := server.service.(StatusReader)
	if !ok {
		return BrowserSessionDocument{}, errUnavailable
	}
	status, err := reader.Status(request.Context())
	if err != nil {
		return BrowserSessionDocument{}, err
	}
	health := "unavailable"
	if !server.draining.Load() && server.service.Ready() {
		health = "ok"
	}
	return BrowserSessionDocument{
		APIVersion: APIVersion, Health: health, Status: status,
		Capabilities: append([]string(nil), browserCapabilities...),
		CSRFToken:    csrfToken, SessionExpiresAt: expiresAt,
	}, nil
}

func (server *Server) authenticateBrowserRequest(writer http.ResponseWriter, request *http.Request) (*http.Request, bool) {
	context, err := server.browserSessions.Authenticate(request)
	if err != nil {
		writeError(writer, request, errUnauthorized)
		return nil, false
	}
	if !browserSessionAllows(request) {
		writeError(writer, request, errForbidden)
		return nil, false
	}
	if unsafeBrowserMethod(request.Method) {
		if !sameOriginRequest(request) || server.browserSessions.ValidateCSRF(request) != nil {
			writeError(writer, request, errForbidden)
			return nil, false
		}
	}
	return request.WithContext(context), true
}

func browserSessionAllows(request *http.Request) bool {
	if request == nil {
		return false
	}
	if request.Method == http.MethodGet {
		switch request.URL.Path {
		case "/api/v1/status", "/api/v1/orchestration/projects", "/api/v1/orchestration/workers", "/api/v1/orchestration/nodes", "/api/v1/federation/nodes", "/api/v1/federation/projects":
			return true
		}
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/api/v1/projects/"), "/")
	if len(parts) < 2 || !validSetupID(parts[0]) {
		return false
	}
	switch {
	case request.Method == http.MethodGet && len(parts) == 2 && parts[1] == "tasks":
		return true
	case request.Method == http.MethodGet && len(parts) == 4 && parts[1] == "tasks" && namespacedSetup(parts[2], "task:") && parts[3] == "readiness":
		return true
	case request.Method == http.MethodGet && len(parts) == 2 && parts[1] == "assignments":
		return true
	case request.Method == http.MethodGet && len(parts) == 3 && parts[1] == "assignments" && namespacedSetup(parts[2], "assignment:"):
		return true
	case request.Method == http.MethodPost && len(parts) == 4 && parts[1] == "tasks" && namespacedSetup(parts[2], "task:") && parts[3] == "approve":
		return true
	case request.Method == http.MethodPost && len(parts) == 3 && parts[1] == "dispatch" && parts[2] == "preview":
		return true
	case request.Method == http.MethodPost && len(parts) == 3 && parts[1] == "scheduler" && (parts[2] == "start" || parts[2] == "disable"):
		return true
	case request.Method == http.MethodPost && len(parts) == 4 && parts[1] == "assignments" && namespacedSetup(parts[2], "assignment:") && (parts[3] == "controls" || parts[3] == "integration"):
		return true
	default:
		return false
	}
}

func unsafeBrowserMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead
}

func sameOriginRequest(request *http.Request) bool {
	if request == nil || request.Header.Get("Origin") == "" || !validRequestHost(request.Host) {
		return false
	}
	if site := request.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	origin, err := url.Parse(request.Header.Get("Origin"))
	if err != nil || origin.Scheme != "http" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return false
	}
	if !strings.EqualFold(origin.Host, request.Host) {
		return false
	}
	host := origin.Hostname()
	return isLoopback(host) && origin.Port() != ""
}

func withAdminAuthentication(request *http.Request) *http.Request {
	return request.WithContext(contextWithAdminAuthentication(request))
}

func contextWithAdminAuthentication(request *http.Request) context.Context {
	return context.WithValue(request.Context(), adminAuthenticationContextKey{}, true)
}

func controlPlaneSecurityHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	writer.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	writer.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
}
