package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syncgate/internal/api"
	"syncgate/internal/identity"
	"syncgate/internal/pairing"
	"syncgate/internal/storage"
)

type localAdminServer interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

type localAPIState struct {
	server    localAdminServer
	listener  net.Listener
	serveDone chan error
	lifecycle api.LifecycleState
	serving   bool
}

func (daemon *Daemon) ConfigureLocalAPI(options Options) error {
	if daemon == nil {
		return errors.New("daemon is required")
	}
	daemon.mu.Lock()
	if daemon.closed {
		daemon.mu.Unlock()
		return ErrClosed
	}
	if daemon.cancel != nil {
		daemon.mu.Unlock()
		return ErrAlreadyRunning
	}
	if daemon.localAPI != nil {
		daemon.mu.Unlock()
		return errors.New("local administration API is already configured")
	}
	daemon.mu.Unlock()

	queries, ok := daemon.Store.(storage.AdministrationQueryStore)
	if !ok {
		return errors.New("local storage does not support administration queries")
	}
	address := net.JoinHostPort(daemon.Config.LocalAPI.Host, strconv.Itoa(daemon.Config.LocalAPI.Port))
	listen := options.ListenLocalAPI
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp", address)
	if err != nil {
		return fmt.Errorf("bind local administration API %s: %w", address, err)
	}
	keepListener := false
	defer func() {
		if !keepListener {
			_ = listener.Close()
		}
	}()

	credentialStore := options.AdminCredentialStore
	if credentialStore == nil {
		credentialStore, err = api.NewAdminCredentialStore(api.AdminCredentialStoreOptions{
			DataDir: daemon.Config.DataDir, RuntimeMode: daemon.Config.RuntimeMode,
			AllowInsecureDevelopmentFile: daemon.Config.Identity.AllowInsecureDevelopmentFile,
		})
		if err != nil {
			return fmt.Errorf("configure local administration credential: %w", err)
		}
	}
	credential, err := api.LoadOrCreateAdminCredential(credentialStore, options.AdminCredentialRandom)
	if err != nil {
		return fmt.Errorf("initialize local administration credential: %w", err)
	}
	authenticator, err := api.NewAdminAuthenticator(credential)
	for index := range credential {
		credential[index] = 0
	}
	if err != nil {
		return fmt.Errorf("configure local administration authentication: %w", err)
	}

	pairingCoordinator := &api.PairingCoordinator{
		Service: pairing.Service{
			Pairings: daemon.Store.Pairings(), Audit: daemon.Store.Audit(), Now: daemon.currentTime,
		},
		CurrentIdentity: func() (identity.DeviceIdentity, string, error) {
			return daemon.Identity, daemon.Config.DeviceName, nil
		},
	}
	service, err := api.NewAdministrationService(api.AdministrationServiceOptions{
		Queries:     queries,
		Ready:       daemon.localAPIReady,
		Runtime:     daemon.localAPIRuntimeSnapshot,
		Diagnostics: daemon.Diagnostics,
		Scan:        daemon.RequestScan,
		Control:     api.ControlWithJobStore(daemon.Store.OneWayJobs(), daemon.currentTime),
		Pairing:     pairingCoordinator,
	})
	if err != nil {
		return fmt.Errorf("configure local administration service: %w", err)
	}
	server, err := api.NewServer(api.ServerOptions{
		Address: address, Service: service, Authenticator: authenticator,
		V1Handler: api.NewAdminV1Handler(service),
	})
	if err != nil {
		return fmt.Errorf("configure local administration server: %w", err)
	}

	daemon.mu.Lock()
	if daemon.closed || daemon.cancel != nil || daemon.localAPI != nil {
		daemon.mu.Unlock()
		return errors.New("daemon lifecycle changed while configuring local administration API")
	}
	daemon.localAPI = &localAPIState{
		server: server, listener: listener, serveDone: make(chan error, 1), lifecycle: api.LifecycleStarting,
	}
	daemon.mu.Unlock()
	keepListener = true
	return nil
}

func (daemon *Daemon) startLocalAPI() <-chan error {
	daemon.mu.Lock()
	state := daemon.localAPI
	if state == nil {
		daemon.mu.Unlock()
		return nil
	}
	state.lifecycle = api.LifecycleRunning
	state.serving = true
	daemon.mu.Unlock()

	go func() {
		err := state.server.Serve(state.listener)
		daemon.mu.Lock()
		draining := state.lifecycle == api.LifecycleDraining || daemon.closed
		daemon.mu.Unlock()
		if err == nil && !draining {
			err = errors.New("local administration server stopped unexpectedly")
		}
		state.serveDone <- err
	}()
	return state.serveDone
}

func (daemon *Daemon) markLocalAPIDrainingLocked() {
	if daemon.localAPI != nil {
		daemon.localAPI.lifecycle = api.LifecycleDraining
	}
}

func (daemon *Daemon) shutdownLocalAPI() error {
	daemon.mu.Lock()
	state := daemon.localAPI
	serving := state != nil && state.serving
	daemon.mu.Unlock()
	if state == nil {
		return nil
	}
	if !serving {
		return errors.Join(state.listener.Close(), state.server.Close())
	}
	if err := state.server.Shutdown(context.Background()); err != nil {
		return errors.Join(err, state.server.Close())
	}
	return nil
}

func (daemon *Daemon) localAPIReady() bool {
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	return !daemon.closed && daemon.localAPI != nil && daemon.localAPI.lifecycle == api.LifecycleRunning
}

func (daemon *Daemon) localAPIRuntimeSnapshot() api.RuntimeSnapshot {
	daemon.mu.Lock()
	lifecycle := api.LifecycleStarting
	if daemon.localAPI != nil {
		lifecycle = daemon.localAPI.lifecycle
	}
	daemon.mu.Unlock()
	return api.RuntimeSnapshot{
		DeviceID: string(daemon.Identity.DeviceID), Fingerprint: daemon.Identity.Fingerprint,
		StartedAt: daemon.startedAt, Lifecycle: lifecycle, ActiveShareCount: len(daemon.runtimes),
		PeerExecutorEnabled: daemon.jobExecutor != nil,
	}
}

func (daemon *Daemon) LocalAPIAddress() string {
	if daemon == nil {
		return ""
	}
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	if daemon.localAPI == nil || daemon.localAPI.listener == nil {
		return ""
	}
	return daemon.localAPI.listener.Addr().String()
}
