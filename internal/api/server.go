package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultReadHeaderTimeout = 2 * time.Second
	defaultReadTimeout       = 10 * time.Second
	defaultWriteTimeout      = 15 * time.Second
	defaultIdleTimeout       = 30 * time.Second
	defaultShutdownTimeout   = 5 * time.Second
	defaultMaxHeaderBytes    = 16 << 10
)

// AdministrationReadiness is the lifecycle subset the HTTP server needs.
// The full AdministrationService adds bounded read and command operations.
type AdministrationReadiness interface {
	Ready() bool
}

type HealthResponse struct {
	Status string `json:"status"`
}

type ServerOptions struct {
	Address           string
	Service           AdministrationReadiness
	Authenticator     *AdminAuthenticator
	V1Handler         http.Handler
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
}

type Server struct {
	httpServer      *http.Server
	service         AdministrationReadiness
	authenticator   *AdminAuthenticator
	v1Handler       http.Handler
	shutdownTimeout time.Duration
	draining        atomic.Bool
}

func NewServer(options ServerOptions) (*Server, error) {
	if err := validateLoopbackAddress(options.Address); err != nil {
		return nil, err
	}
	if options.Service == nil {
		return nil, errors.New("administration service is required")
	}
	if options.Authenticator == nil {
		return nil, errors.New("administration credential verifier is required")
	}
	if options.V1Handler == nil {
		return nil, errors.New("v1 administration handler is required")
	}
	applyServerDefaults(&options)
	server := &Server{
		service: options.Service, authenticator: options.Authenticator,
		v1Handler: options.V1Handler, shutdownTimeout: options.ShutdownTimeout,
	}
	server.httpServer = &http.Server{
		Addr:              options.Address,
		Handler:           http.HandlerFunc(server.serveHTTP),
		ReadHeaderTimeout: options.ReadHeaderTimeout,
		ReadTimeout:       options.ReadTimeout,
		WriteTimeout:      options.WriteTimeout,
		IdleTimeout:       options.IdleTimeout,
		MaxHeaderBytes:    options.MaxHeaderBytes,
	}
	return server, nil
}

func (server *Server) ListenAndServe() error {
	listener, err := net.Listen("tcp", server.httpServer.Addr)
	if err != nil {
		return err
	}
	return server.Serve(listener)
}

func (server *Server) Serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("administration listener is required")
	}
	if err := validateLoopbackListener(listener); err != nil {
		_ = listener.Close()
		return err
	}
	err := server.httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (server *Server) Shutdown(ctx context.Context) error {
	server.draining.Store(true)
	if ctx == nil {
		ctx = context.Background()
	}
	bounded, cancel := context.WithTimeout(ctx, server.shutdownTimeout)
	defer cancel()
	return server.httpServer.Shutdown(bounded)
}

func (server *Server) Close() error {
	server.draining.Store(true)
	return server.httpServer.Close()
}

func (server *Server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if !validRequestHost(request.Host) {
		writeError(writer, request, errForbidden)
		return
	}
	if request.Header.Get("Origin") != "" {
		writeError(writer, request, errForbidden)
		return
	}

	switch {
	case request.URL.Path == "/healthz":
		methodHandler(http.MethodGet, server.health)(writer, request)
	case request.URL.Path == "/api/v1" || strings.HasPrefix(request.URL.Path, "/api/v1/"):
		if server.draining.Load() {
			writeError(writer, request, errUnavailable)
			return
		}
		server.authenticator.Authenticate(server.limitBody(server.v1Handler)).ServeHTTP(writer, request)
	default:
		writeError(writer, request, errNotFound)
	}
}

func (server *Server) health(writer http.ResponseWriter, request *http.Request) {
	if server.draining.Load() || !server.service.Ready() {
		writeError(writer, request, errUnavailable)
		return
	}
	writeJSON(writer, request, http.StatusOK, HealthResponse{Status: "ok"})
}

func (server *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.ContentLength > maxJSONBodyBytes {
			writeError(writer, request, errPayloadTooLarge)
			return
		}
		if request.Body != nil {
			request.Body = http.MaxBytesReader(writer, request.Body, maxJSONBodyBytes)
		}
		next.ServeHTTP(writer, request)
	})
}

func applyServerDefaults(options *ServerOptions) {
	if options.ReadHeaderTimeout <= 0 {
		options.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if options.ReadTimeout <= 0 {
		options.ReadTimeout = defaultReadTimeout
	}
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = defaultWriteTimeout
	}
	if options.IdleTimeout <= 0 {
		options.IdleTimeout = defaultIdleTimeout
	}
	if options.ShutdownTimeout <= 0 {
		options.ShutdownTimeout = defaultShutdownTimeout
	}
	if options.MaxHeaderBytes <= 0 {
		options.MaxHeaderBytes = defaultMaxHeaderBytes
	}
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parse administration address: %w", err)
	}
	if !isLoopback(host) {
		return fmt.Errorf("administration server must bind to loopback, got %q", host)
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return fmt.Errorf("administration server port must be between 1 and 65535, got %q", port)
	}
	return nil
}

func validateLoopbackListener(listener net.Listener) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.IP == nil || !address.IP.IsLoopback() {
		return fmt.Errorf("administration listener must be TCP loopback, got %q", listener.Addr())
	}
	return nil
}

func validRequestHost(authority string) bool {
	authority = strings.TrimSpace(authority)
	if authority == "" || strings.ContainsAny(authority, "@/\\") {
		return false
	}
	host := authority
	if parsedHost, port, err := net.SplitHostPort(authority); err == nil {
		host = parsedHost
		parsedPort, portErr := strconv.Atoi(port)
		if portErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return false
		}
	} else if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(authority, "["), "]")
	} else if strings.Count(authority, ":") == 1 {
		return false
	}
	return isLoopback(host)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
