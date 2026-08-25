package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"syncgate/internal/api"
	syncengine "syncgate/internal/sync"
)

func TestDaemonSupervisesAuthenticatedLocalAPI(t *testing.T) {
	cfg := testConfigWithInterval(t.TempDir(), t.TempDir(), 3600)
	cfg.LocalAPI.Port = freeLocalAPIPort(t)
	credentialStore := &daemonCredentialStore{credential: daemonAdminCredential(0x41)}
	options := Options{
		AdminCredentialStore: credentialStore,
		WatcherFactory:       func(string) (syncengine.Watcher, error) { return newDaemonWatcher(2), nil },
	}
	instance, err := Bootstrap(context.Background(), cfg, options)
	if err != nil {
		t.Fatalf("bootstrap daemon: %v", err)
	}
	if err := instance.ConfigureLocalAPI(options); err != nil {
		t.Fatalf("configure local API: %v", err)
	}
	if instance.LocalAPIAddress() == "" {
		t.Fatal("local API did not bind during bootstrap composition")
	}
	if listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.LocalAPI.Port))); err == nil {
		_ = listener.Close()
		t.Fatal("local API port was not reserved before workers started")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := runDaemon(t, instance, ctx)
	baseURL := "http://" + instance.LocalAPIAddress()
	waitForAPIStatus(t, baseURL+"/healthz", "", http.StatusOK)
	statusResponse := waitForAPIStatus(t, baseURL+"/api/v1/status", string(credentialStore.credential), http.StatusOK)
	defer statusResponse.Body.Close()
	var status api.AdminStatus
	if err := json.NewDecoder(statusResponse.Body).Decode(&status); err != nil {
		t.Fatalf("decode API status: %v", err)
	}
	if status.Lifecycle != api.LifecycleRunning || status.DeviceID != string(instance.Identity.DeviceID) || status.PeerExecutorEnabled {
		t.Fatalf("runtime status = %+v", status)
	}

	cancel()
	if err := waitForDaemon(t, done); err != nil {
		t.Fatalf("shutdown daemon: %v", err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.LocalAPI.Port)))
	if err != nil {
		t.Fatalf("local API listener remained open after shutdown: %v", err)
	}
	_ = listener.Close()
	if _, err := instance.Store.OneWayJobs().ListOneWayJobs(context.Background()); err == nil {
		t.Fatal("SQLite remained open after API and workers stopped")
	}
}

func TestDaemonProjectMigrationAPIUsesConfiguredShareAndPersistsAudit(t *testing.T) {
	root := t.TempDir()
	workspaceFile := filepath.Join(root, "workspace", "existing.txt")
	if err := os.MkdirAll(filepath.Dir(workspaceFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfigWithInterval(t.TempDir(), root, 3600)
	cfg.LocalAPI.Port = freeLocalAPIPort(t)
	credentialStore := &daemonCredentialStore{credential: daemonAdminCredential(0x49)}
	options := Options{AdminCredentialStore: credentialStore, WatcherFactory: func(string) (syncengine.Watcher, error) { return newDaemonWatcher(2), nil }}
	instance, err := Bootstrap(context.Background(), cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.ConfigureLocalAPI(options); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runDaemon(t, instance, ctx)
	client, err := api.NewClient(api.ClientOptions{Address: instance.LocalAPIAddress(), Credential: credentialStore.credential})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	waitForAPIStatus(t, "http://"+instance.LocalAPIAddress()+"/healthz", "", http.StatusOK).Body.Close()
	input := api.ProjectMigrationInput{ShareID: "share-1", ProjectID: "project-api-migration", Name: "API Migration"}
	preflight, err := client.PreflightProjectMigration(context.Background(), input)
	if err != nil || preflight.Status != "ready" || preflight.Confirmation == "" {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	applied, err := client.ApplyProjectMigration(context.Background(), api.ProjectMigrationApplyInput{ProjectMigrationInput: input, Confirmation: preflight.Confirmation})
	if err != nil || applied.Status != "applied" || !applied.ScanRequested || applied.AuditEventID == "" {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
	registration, err := instance.Store.ProjectRegistrations().GetProject(context.Background(), input.ProjectID)
	if err != nil || registration.ShareID != "share-1" || registration.RootPath != root {
		t.Fatalf("registration=%+v err=%v", registration, err)
	}
	if got, err := os.ReadFile(workspaceFile); err != nil || string(got) != "keep" {
		t.Fatalf("workspace changed: %q err=%v", got, err)
	}
	audit, err := instance.Store.Audit().Get(context.Background(), applied.AuditEventID)
	if err != nil || audit.Metadata["project_id"] != input.ProjectID {
		t.Fatalf("audit=%+v err=%v", audit, err)
	}
	cancel()
	if err := waitForDaemon(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonSetupFacadeKeepsExecutionDisabled(t *testing.T) {
	cfg := testConfigWithInterval(t.TempDir(), t.TempDir(), 3600)
	cfg.LocalAPI.Port = freeLocalAPIPort(t)
	credentialStore := &daemonCredentialStore{credential: daemonAdminCredential(0x4a)}
	options := Options{AdminCredentialStore: credentialStore, WatcherFactory: func(string) (syncengine.Watcher, error) { return newDaemonWatcher(2), nil }}
	instance, err := Bootstrap(context.Background(), cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.ConfigureLocalAPI(options); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runDaemon(t, instance, ctx)
	defer func() {
		cancel()
		if err := waitForDaemon(t, done); err != nil {
			t.Fatal(err)
		}
	}()
	client, err := api.NewClient(api.ClientOptions{Address: instance.LocalAPIAddress(), Credential: credentialStore.credential})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	capabilities, err := client.CapabilityInventory(context.Background())
	if err != nil || capabilities.RuntimeExecutionEnabled || capabilities.WorkspaceAllocationEnabled || len(capabilities.Runtimes) != 0 || len(capabilities.Nodes) != 0 {
		t.Fatalf("capabilities=%+v err=%v", capabilities, err)
	}
	_, err = client.CreateTaskGraph(context.Background(), api.TaskGraphCreateInput{
		ProjectID: "project-one", TaskID: "task:one", TaskRevision: 1, GraphRevision: 1,
		SpecificationID: "spec-one", SpecificationDigest: strings.Repeat("a", 64), IdempotencyKey: "setup-disabled",
	})
	var clientError *api.ClientError
	if !errors.As(err, &clientError) || clientError.StatusCode != http.StatusServiceUnavailable || clientError.Code != "unavailable" {
		t.Fatalf("disabled graph publication error=%v", err)
	}
}

func TestLocalAPIConfigurationFailuresLeaveNoRunningDaemon(t *testing.T) {
	t.Run("port conflict", func(t *testing.T) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		cfg := testConfig(t.TempDir(), t.TempDir())
		cfg.LocalAPI.Port = listener.Addr().(*net.TCPAddr).Port
		instance, err := Bootstrap(context.Background(), cfg, Options{})
		if err != nil {
			t.Fatal(err)
		}
		options := Options{AdminCredentialStore: &daemonCredentialStore{credential: daemonAdminCredential(0x42)}}
		if err := instance.ConfigureLocalAPI(options); err == nil || !strings.Contains(err.Error(), "bind local administration API") {
			t.Fatalf("port conflict error = %v", err)
		}
		if err := instance.Close(); err != nil {
			t.Fatalf("close after port conflict: %v", err)
		}
	})

	t.Run("credential failure", func(t *testing.T) {
		cfg := testConfig(t.TempDir(), t.TempDir())
		cfg.LocalAPI.Port = freeLocalAPIPort(t)
		instance, err := Bootstrap(context.Background(), cfg, Options{})
		if err != nil {
			t.Fatal(err)
		}
		failure := errors.New("credential backend failed")
		options := Options{AdminCredentialStore: &daemonCredentialStore{loadErr: failure}}
		if err := instance.ConfigureLocalAPI(options); !errors.Is(err, failure) {
			t.Fatalf("credential failure error = %v", err)
		}
		if err := instance.Close(); err != nil {
			t.Fatalf("close after credential failure: %v", err)
		}
		listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.LocalAPI.Port)))
		if err != nil {
			t.Fatalf("credential failure leaked listener: %v", err)
		}
		_ = listener.Close()
	})
}

func TestLocalAPIServeFailureStopsWorkersAndStorage(t *testing.T) {
	cfg := testConfigWithInterval(t.TempDir(), t.TempDir(), 3600)
	cfg.LocalAPI.Port = freeLocalAPIPort(t)
	options := Options{
		AdminCredentialStore: &daemonCredentialStore{credential: daemonAdminCredential(0x43)},
		WatcherFactory:       func(string) (syncengine.Watcher, error) { return newDaemonWatcher(2), nil },
	}
	instance, err := Bootstrap(context.Background(), cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.ConfigureLocalAPI(options); err != nil {
		t.Fatal(err)
	}
	if err := instance.localAPI.listener.Close(); err != nil {
		t.Fatal(err)
	}
	err = instance.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "serve local administration API") {
		t.Fatalf("serve failure = %v", err)
	}
	if _, err := instance.Store.OneWayJobs().ListOneWayJobs(context.Background()); err == nil {
		t.Fatal("serve failure left SQLite open")
	}
}

type daemonCredentialStore struct {
	credential []byte
	loadErr    error
}

func (store *daemonCredentialStore) Load() ([]byte, error) {
	if store.loadErr != nil {
		return nil, store.loadErr
	}
	if len(store.credential) == 0 {
		return nil, api.ErrAdminCredentialNotFound
	}
	return bytes.Clone(store.credential), nil
}

func (store *daemonCredentialStore) Save(credential []byte) error {
	store.credential = bytes.Clone(credential)
	return nil
}

func daemonAdminCredential(value byte) []byte {
	return []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32)))
}

func freeLocalAPIPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func waitForAPIStatus(t *testing.T, target, credential string, want int) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 300 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if credential != "" {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
		response, err := client.Do(request)
		if err == nil {
			if response.StatusCode == want {
				return response
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s status %d: %v", target, want, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
