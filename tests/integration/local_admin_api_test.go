package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/config"
	"syncgate/internal/core"
	"syncgate/internal/daemon"
	"syncgate/internal/identity"
	syncengine "syncgate/internal/sync"
)

func TestLocalAdminAPIAcceptanceAndRestart(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	writeAdminFile(t, filepath.Join(shareRoot, "one.txt"), "one")
	writeAdminFile(t, filepath.Join(shareRoot, "two.txt"), "two")
	credentialStore := &integrationCredentialStore{credential: integrationAdminCredential(0x61)}
	cfg := config.Config{
		DeviceName: "ADMIN-TEST", DataDir: dataDir, RuntimeMode: config.RuntimeModeDevelopment,
		Identity: config.IdentityConfig{Store: config.IdentityStoreDevelopment, AllowInsecureDevelopmentFile: true},
		LocalAPI: config.LocalAPIConfig{Host: "127.0.0.1", Port: integrationFreePort(t)},
		Shares: []config.ShareConfig{{
			ID: "source", Name: "Source", RootPath: shareRoot, Mode: "one_way_source",
			ScanIntervalSeconds: 3600, DeletionLimitCount: 1, DeletionLimitPercent: 100,
		}},
	}
	options := daemon.Options{
		AdminCredentialStore: credentialStore,
		WatcherFactory:       func(string) (syncengine.Watcher, error) { return newQuietWatcher(), nil },
	}
	instance, client, cancel, done := startAdminDaemon(t, cfg, options)
	firstIdentity := instance.Identity.DeviceID
	baseURL := "http://" + instance.LocalAPIAddress()
	defer client.Close()

	health := integrationRequest(t, http.MethodGet, baseURL+"/healthz", "", "", "", nil)
	if health.status != http.StatusOK || health.body != `{"status":"ok"}`+"\n" {
		t.Fatalf("health response = %d %q", health.status, health.body)
	}
	status, err := client.Status(ctx)
	if err != nil || status.Lifecycle != api.LifecycleRunning || status.DeviceID != string(firstIdentity) {
		t.Fatalf("status = %+v, err=%v", status, err)
	}
	shares, err := client.ListShares(ctx, 10)
	if err != nil || len(shares.Items) != 1 || shares.Items[0].RootPath != shareRoot {
		t.Fatalf("shares = %+v, err=%v", shares, err)
	}
	waitForAdminScan(t, client, 0, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerStartup && scan.Committed
	})

	seedAdminJob(t, instance, "job-paused")
	job, err := client.ControlJob(ctx, "job-paused", api.JobActionPause)
	if err != nil || job.State != string(core.OneWayJobPaused) {
		t.Fatalf("pause job = %+v, err=%v", job, err)
	}
	jobs, err := client.ListJobs(ctx, 10)
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].ID != "job-paused" {
		t.Fatalf("jobs = %+v, err=%v", jobs, err)
	}
	jobDetail, err := client.GetJob(ctx, "job-paused")
	if err != nil || jobDetail.TransferID != "transfer-job-paused" {
		t.Fatalf("job detail = %+v, err=%v", jobDetail, err)
	}
	createdInvitation, err := client.CreatePairingInvitation(ctx, api.InvitationRequest{TTLSeconds: 60, Capabilities: []string{"read"}})
	if err != nil || createdInvitation.Invitation == "" || createdInvitation.OneTimeCode == "" {
		t.Fatalf("created invitation = %+v, err=%v", createdInvitation, err)
	}

	peer := integrationIdentity(t, 0x62)
	invite, err := identity.NewPairingInviteAt(peer, "Peer", time.Minute, []core.Capability{core.CapabilityRead}, bytes.NewReader(bytes.Repeat([]byte{0x63}, identity.PairingCodeByteCount)), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := identity.EncodePairingInvite(invite)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := client.InspectPairingInvitation(ctx, encoded)
	if err != nil || inspection.DeviceID != string(peer.DeviceID) {
		t.Fatalf("inspection = %+v, err=%v", inspection, err)
	}
	accepted, err := client.AcceptPairingInvitation(ctx, api.AcceptanceRequest{
		Invitation: encoded, ExpectedFingerprint: peer.Fingerprint, OneTimeCode: invite.OneTimeCode,
		Grants: []api.PairingGrantRequest{{ShareID: "source", Capabilities: []string{"read"}}},
	})
	if err != nil || !accepted.Accepted || accepted.AlreadyAccepted {
		t.Fatalf("acceptance = %+v, err=%v", accepted, err)
	}
	detail, err := client.GetDevice(ctx, peer.DeviceID)
	if err != nil || len(detail.Permissions) != 1 || !detail.Permissions[0].LANOnly || !detail.Permissions[0].Capabilities["read"] {
		t.Fatalf("paired device detail = %+v, err=%v", detail, err)
	}
	revoked, err := client.RevokePairingDevice(ctx, peer.DeviceID)
	if err != nil || !revoked.Revoked {
		t.Fatalf("revocation = %+v, err=%v", revoked, err)
	}
	devices, err := client.ListDevices(ctx, 10)
	if err != nil || !containsAdminDevice(devices, peer.DeviceID, "revoked") {
		t.Fatalf("devices = %+v, err=%v", devices, err)
	}
	audit, err := client.ListAuditEvents(ctx, 10)
	if err != nil || len(audit.Items) < 2 {
		t.Fatalf("audit = %+v, err=%v", audit, err)
	}

	diagnostics, err := client.Diagnostics(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	manualBefore := countAdminScans(diagnostics.RecentScans, syncengine.ScanTriggerManual)
	badRead := integrationRequest(t, http.MethodGet, baseURL+"/api/v1/status", "wrong", "", "", nil)
	badMutation := integrationRequest(t, http.MethodPost, baseURL+"/api/v1/shares/source/scans", "wrong", "", "", []byte(`{}`))
	if badRead.status != http.StatusUnauthorized || badMutation.status != http.StatusUnauthorized || strings.Contains(badRead.body, string(firstIdentity)) {
		t.Fatalf("bad authentication responses = read %d %q, mutation %d %q", badRead.status, badRead.body, badMutation.status, badMutation.body)
	}
	time.Sleep(50 * time.Millisecond)
	diagnostics, err = client.Diagnostics(ctx, 50)
	if err != nil || countAdminScans(diagnostics.RecentScans, syncengine.ScanTriggerManual) != manualBefore {
		t.Fatalf("unauthorized scan mutated runtime: %+v, err=%v", diagnostics.RecentScans, err)
	}
	for _, rejected := range []adminHTTPResult{
		integrationRequest(t, http.MethodGet, baseURL+"/api/v1/status", string(credentialStore.credential), "http://127.0.0.1:3000", "", nil),
		integrationRequest(t, http.MethodGet, baseURL+"/api/v1/status", string(credentialStore.credential), "", "attacker.example", nil),
	} {
		if rejected.status != http.StatusForbidden {
			t.Fatalf("origin/host rejection = %d %q", rejected.status, rejected.body)
		}
	}

	seen := len(diagnostics.RecentScans)
	if err := os.RemoveAll(shareRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RequestScan(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	waitForAdminScan(t, client, seen, func(scan syncengine.ScanDiagnostic) bool { return scan.Unavailable })
	entries, err := instance.Store.FileIndex().List(ctx, "source")
	if err != nil || len(entries) != 2 {
		t.Fatalf("unavailable-root index = %+v, err=%v", entries, err)
	}
	if err := os.MkdirAll(shareRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	diagnostics, _ = client.Diagnostics(ctx, 50)
	seen = len(diagnostics.RecentScans)
	if _, err := client.RequestScan(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	waitForAdminScan(t, client, seen, func(scan syncengine.ScanDiagnostic) bool { return scan.Blocked })
	entries, err = instance.Store.FileIndex().List(ctx, "source")
	if err != nil || len(entries) != 2 || entries[0].IsDeleted || entries[1].IsDeleted {
		t.Fatalf("deletion guard changed index = %+v, err=%v", entries, err)
	}

	secretError := integrationRequest(t, http.MethodPost, baseURL+"/api/v1/pairing/inspect", string(credentialStore.credential), "", "", []byte(`{"invitation":"secret-invite"}`))
	for _, secret := range []string{"secret-invite", string(credentialStore.credential), dataDir, shareRoot} {
		if strings.Contains(secretError.body, secret) {
			t.Fatalf("sanitized error disclosed %q: %s", secret, secretError.body)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("first daemon shutdown: %v", err)
	}
	client.Close()
	instance, restartedClient, restartCancel, restartDone := startAdminDaemon(t, cfg, options)
	if instance.Identity.DeviceID != firstIdentity {
		t.Fatalf("identity changed across restart: %s != %s", instance.Identity.DeviceID, firstIdentity)
	}
	restartedStatus, err := restartedClient.Status(ctx)
	if err != nil || restartedStatus.Lifecycle != api.LifecycleRunning {
		t.Fatalf("restarted status = %+v, err=%v", restartedStatus, err)
	}
	restartedDevices, err := restartedClient.ListDevices(ctx, 10)
	if err != nil || !containsAdminDevice(restartedDevices, peer.DeviceID, "revoked") {
		t.Fatalf("restarted devices = %+v, err=%v", restartedDevices, err)
	}
	restartedClient.Close()
	restartCancel()
	if err := <-restartDone; err != nil {
		t.Fatalf("restarted daemon shutdown: %v", err)
	}
}

func startAdminDaemon(t *testing.T, cfg config.Config, options daemon.Options) (*daemon.Daemon, *api.Client, context.CancelFunc, <-chan error) {
	t.Helper()
	instance, err := daemon.Bootstrap(context.Background(), cfg, options)
	if err != nil {
		t.Fatalf("bootstrap admin daemon: %v", err)
	}
	if err := instance.ConfigureLocalAPI(options); err != nil {
		_ = instance.Close()
		t.Fatalf("configure admin API: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- instance.Run(ctx) }()
	client, err := api.NewClient(api.ClientOptions{Address: instance.LocalAPIAddress(), Credential: options.AdminCredentialStore.(*integrationCredentialStore).credential})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := client.Status(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("local administration API did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return instance, client, cancel, done
}

func seedAdminJob(t *testing.T, instance *daemon.Daemon, id string) {
	t.Helper()
	now := time.Now().UTC()
	transferID := core.TransferID("transfer-" + id)
	if err := instance.Store.Transfers().SaveTransfer(context.Background(), core.Transfer{
		ID: transferID, Direction: core.TransferSend, PeerDeviceID: instance.Identity.DeviceID,
		ShareID: "source", RelativePath: "one.txt", State: core.TransferPaused, ChunkSize: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Store.OneWayJobs().SaveOneWayJob(context.Background(), core.OneWayJob{
		ID: id, TransferID: transferID, PeerDeviceID: instance.Identity.DeviceID, ShareID: "source",
		RevisionID: "revision-job", RelativePath: "one.txt", RequiredCapability: core.CapabilityRead,
		State: core.OneWayJobPaused, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func waitForAdminScan(t *testing.T, client *api.Client, seen int, match func(syncengine.ScanDiagnostic) bool) syncengine.ScanDiagnostic {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		diagnostics, err := client.Diagnostics(context.Background(), 200)
		if err == nil {
			for _, scan := range diagnostics.RecentScans[minimum(seen, len(diagnostics.RecentScans)):] {
				if match(scan) {
					return scan
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for scan after %d entries: %+v, err=%v", seen, diagnostics.RecentScans, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type adminHTTPResult struct {
	status int
	body   string
}

func integrationRequest(t *testing.T, method, target, credential, origin, host string, body []byte) adminHTTPResult {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if host != "" {
		request.Host = host
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return adminHTTPResult{status: response.StatusCode, body: string(raw)}
}

func containsAdminDevice(page api.DeviceInventoryPage, deviceID core.DeviceID, state string) bool {
	for _, device := range page.Items {
		if device.ID == string(deviceID) && device.State == state {
			return true
		}
	}
	return false
}

func countAdminScans(scans []syncengine.ScanDiagnostic, trigger syncengine.ScanTriggerReason) int {
	count := 0
	for _, scan := range scans {
		if scan.Trigger == trigger {
			count++
		}
	}
	return count
}

func integrationIdentity(t *testing.T, value byte) identity.DeviceIdentity {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{value}, ed25519.SeedSize))
	deviceIdentity, err := identity.FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return deviceIdentity
}

type integrationCredentialStore struct {
	credential []byte
}

func (store *integrationCredentialStore) Load() ([]byte, error) {
	return bytes.Clone(store.credential), nil
}
func (store *integrationCredentialStore) Save(value []byte) error {
	store.credential = bytes.Clone(value)
	return nil
}

func integrationAdminCredential(value byte) []byte {
	return []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32)))
}

type quietWatcher struct {
	events chan syncengine.WatchEvent
	errors chan error
	once   sync.Once
}

func newQuietWatcher() *quietWatcher {
	return &quietWatcher{events: make(chan syncengine.WatchEvent), errors: make(chan error)}
}
func (watcher *quietWatcher) Events() <-chan syncengine.WatchEvent { return watcher.events }
func (watcher *quietWatcher) Errors() <-chan error                 { return watcher.errors }
func (watcher *quietWatcher) Close() error {
	watcher.once.Do(func() { close(watcher.events); close(watcher.errors) })
	return nil
}

func integrationFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func writeAdminFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func minimum(left, right int) int {
	if left < right {
		return left
	}
	return right
}

var _ api.AdminCredentialStore = (*integrationCredentialStore)(nil)
