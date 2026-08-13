package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestAdminShareAndDevicePaginationIsStable(t *testing.T) {
	store := newTestStore(t)
	seedAdminQueryData(t, store)
	ctx := context.Background()

	shareIDs := collectPages(t, func(cursor storage.PageCursor) (storage.Page[storage.AdminShare], error) {
		return store.ListAdminShares(ctx, storage.PageRequest{Limit: 2, Cursor: cursor})
	}, func(item storage.AdminShare) string { return string(item.ID) })
	if want := []string{"share-1", "share-2", "share-3", "share-4", "share-5"}; !reflect.DeepEqual(shareIDs, want) {
		t.Fatalf("share ids = %v, want %v", shareIDs, want)
	}

	deviceIDs := collectPages(t, func(cursor storage.PageCursor) (storage.Page[storage.AdminDevice], error) {
		return store.ListAdminDevices(ctx, storage.PageRequest{Limit: 2, Cursor: cursor})
	}, func(item storage.AdminDevice) string { return string(item.ID) })
	if want := []string{"device-1", "device-2", "device-3", "device-4", "device-5"}; !reflect.DeepEqual(deviceIDs, want) {
		t.Fatalf("device ids = %v, want %v", deviceIDs, want)
	}
	revoked, err := store.GetAdminDevice(ctx, "device-3")
	if err != nil {
		t.Fatalf("get revoked device: %v", err)
	}
	if revoked.TrustState != storage.TrustRevoked {
		t.Fatalf("revoked device state = %q", revoked.TrustState)
	}
	revoked.PublicKey[0] = 'X'
	reloaded, err := store.GetAdminDevice(ctx, "device-3")
	if err != nil {
		t.Fatal(err)
	}
	if string(reloaded.PublicKey) != "public-3" {
		t.Fatalf("public key was not copied: %q", reloaded.PublicKey)
	}
}

func TestAdminPermissionPaginationUsesExplicitIndependentMaps(t *testing.T) {
	store := newTestStore(t)
	seedAdminQueryData(t, store)
	ctx := context.Background()
	page, err := store.ListAdminPermissions(ctx, storage.PermissionQuery{Page: storage.PageRequest{Limit: 2}})
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("permission page = %#v", page)
	}
	for _, permission := range page.Items {
		if len(permission.Capabilities) != len(core.ShareCapabilities()) {
			t.Fatalf("capability map is not explicit: %#v", permission.Capabilities)
		}
	}
	page.Items[0].Capabilities[core.CapabilityUpload] = false
	reloaded, err := store.ListAdminPermissions(ctx, storage.PermissionQuery{
		Page: storage.PageRequest{Limit: 10}, DeviceID: page.Items[0].DeviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Items) != 1 || !reloaded.Items[0].Capabilities[core.CapabilityUpload] {
		t.Fatal("permission capability map was aliased or filtering failed")
	}
}

func TestAdminJobAndAuditPaginationAndFilters(t *testing.T) {
	store := newTestStore(t)
	seedAdminQueryData(t, store)
	ctx := context.Background()

	jobIDs := collectPages(t, func(cursor storage.PageCursor) (storage.Page[storage.AdminJob], error) {
		return store.ListAdminJobs(ctx, storage.JobQuery{Page: storage.PageRequest{Limit: 2, Cursor: cursor}})
	}, func(item storage.AdminJob) string { return item.ID })
	if want := []string{"job-5", "job-4", "job-3", "job-2", "job-1"}; !reflect.DeepEqual(jobIDs, want) {
		t.Fatalf("job ids = %v, want %v", jobIDs, want)
	}
	filteredJobs, err := store.ListAdminJobs(ctx, storage.JobQuery{
		Page: storage.PageRequest{Limit: 10}, ShareID: "share-1", State: core.OneWayJobPaused,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filteredJobs.Items) != 1 || filteredJobs.Items[0].ID != "job-1" {
		t.Fatalf("filtered jobs = %#v", filteredJobs.Items)
	}

	auditIDs := collectPages(t, func(cursor storage.PageCursor) (storage.Page[storage.AdminAuditEvent], error) {
		return store.ListAdminAuditEvents(ctx, storage.AuditEventQuery{Page: storage.PageRequest{Limit: 2, Cursor: cursor}})
	}, func(item storage.AdminAuditEvent) string { return item.ID })
	if want := []string{"audit-5", "audit-4", "audit-3", "audit-2", "audit-1"}; !reflect.DeepEqual(auditIDs, want) {
		t.Fatalf("audit ids = %v, want %v", auditIDs, want)
	}
	filteredAudit, err := store.ListAdminAuditEvents(ctx, storage.AuditEventQuery{
		Page: storage.PageRequest{Limit: 10}, EventName: "pairing", DeviceID: "device-2", ShareID: "share-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filteredAudit.Items) != 1 || filteredAudit.Items[0].ID != "audit-2" {
		t.Fatalf("filtered audit = %#v", filteredAudit.Items)
	}
	if _, ok := reflect.TypeOf(storage.AdminAuditEvent{}).FieldByName("Metadata"); ok {
		t.Fatal("administration audit model exposes unrestricted metadata")
	}
}

func TestAdminQueriesBoundLimitsAndParameterizeFilters(t *testing.T) {
	store := newTestStore(t)
	seedAdminQueryData(t, store)
	ctx := context.Background()
	if _, err := store.ListAdminShares(ctx, storage.PageRequest{Limit: storage.MaxAdminPageLimit + 1}); err == nil {
		t.Fatal("oversized page limit was accepted")
	}
	injection := `share-1' OR 1=1 --`
	jobs, err := store.ListAdminJobs(ctx, storage.JobQuery{Page: storage.PageRequest{Limit: 10}, ShareID: core.ShareID(injection)})
	if err != nil {
		t.Fatalf("parameterized job filter: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Fatalf("filter changed query semantics: %#v", jobs.Items)
	}
	audit, err := store.ListAdminAuditEvents(ctx, storage.AuditEventQuery{Page: storage.PageRequest{Limit: 10}, EventName: `pairing' OR 1=1 --`})
	if err != nil {
		t.Fatalf("parameterized audit filter: %v", err)
	}
	if len(audit.Items) != 0 {
		t.Fatalf("audit filter changed query semantics: %#v", audit.Items)
	}
}

func TestAdminQueriesSupportConcurrentDaemonReads(t *testing.T) {
	store := newTestStore(t)
	seedAdminQueryData(t, store)
	ctx := context.Background()
	var wait sync.WaitGroup
	errorsChannel := make(chan error, 20)
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := store.ListAdminDevices(ctx, storage.PageRequest{Limit: 3}); err != nil {
				errorsChannel <- err
			}
			if _, err := store.ListAdminJobs(ctx, storage.JobQuery{Page: storage.PageRequest{Limit: 3}}); err != nil {
				errorsChannel <- err
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("concurrent query: %v", err)
	}
}

func seedAdminQueryData(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	stamp := time.Unix(1000, 0).UTC()
	for index := 1; index <= 5; index++ {
		deviceID := core.DeviceID(fmt.Sprintf("device-%d", index))
		trust := storage.TrustTrusted
		if index == 3 {
			trust = storage.TrustRevoked
		}
		if err := store.Devices().TrustDevice(ctx, storage.Device{
			ID: deviceID, DisplayName: fmt.Sprintf("Device %d", index), PublicKey: []byte(fmt.Sprintf("public-%d", index)),
			Fingerprint: fmt.Sprintf("fingerprint-%d", index), TrustState: trust, CreatedAt: stamp, UpdatedAt: stamp,
		}); err != nil {
			t.Fatalf("save device %d: %v", index, err)
		}
		shareID := core.ShareID(fmt.Sprintf("share-%d", index))
		if err := store.Shares().SaveShare(ctx, storage.Share{
			ID: shareID, Name: fmt.Sprintf("Share %d", index), RootPath: fmt.Sprintf("C:\\shares\\%d", index),
			Mode: storage.ShareOneWaySource, CreatedAt: stamp, UpdatedAt: stamp,
		}); err != nil {
			t.Fatalf("save share %d: %v", index, err)
		}
		if err := store.Shares().SetPermission(ctx, core.SharePermission{
			ShareID: shareID, DeviceID: deviceID,
			Capabilities: map[core.Capability]bool{core.CapabilityUpload: true, core.CapabilitySync: index%2 == 0},
			LANOnly:      true,
		}); err != nil {
			t.Fatalf("save permission %d: %v", index, err)
		}
		transferID := core.TransferID(fmt.Sprintf("transfer-%d", index))
		if err := store.Transfers().SaveTransfer(ctx, core.Transfer{
			ID: transferID, Direction: core.TransferSend, PeerDeviceID: deviceID, ShareID: shareID,
			RelativePath: fmt.Sprintf("private-%d.txt", index), State: core.TransferQueued, ChunkSize: 1024,
			CreatedAt: stamp, UpdatedAt: stamp, LastError: "secret=must-not-escape",
		}); err != nil {
			t.Fatalf("save transfer %d: %v", index, err)
		}
		state := core.OneWayJobQueued
		if index == 1 {
			state = core.OneWayJobPaused
		}
		if err := store.OneWayJobs().SaveOneWayJob(ctx, core.OneWayJob{
			ID: fmt.Sprintf("job-%d", index), TransferID: transferID, PeerDeviceID: deviceID, ShareID: shareID,
			RevisionID: core.RevisionID(fmt.Sprintf("revision-%d", index)), RelativePath: fmt.Sprintf("private-%d.txt", index),
			RequiredCapability: core.CapabilityUpload, State: state, CreatedAt: stamp, UpdatedAt: stamp,
			LastError: "open C:\\private\\file token=secret",
		}); err != nil {
			t.Fatalf("save job %d: %v", index, err)
		}
		if err := store.Audit().Record(ctx, storage.AuditEvent{
			ID: fmt.Sprintf("audit-%d", index), EventName: "pairing", DeviceID: deviceID, PeerDeviceID: deviceID,
			ShareID: shareID, Severity: "info", Metadata: map[string]string{"token": "secret", "path": "C:\\private"}, OccurredAt: stamp,
		}); err != nil {
			t.Fatalf("save audit event %d: %v", index, err)
		}
	}
}

func collectPages[T any](t *testing.T, fetch func(storage.PageCursor) (storage.Page[T], error), id func(T) string) []string {
	t.Helper()
	cursor := storage.PageCursor{}
	result := make([]string, 0)
	seen := make(map[string]bool)
	for {
		page, err := fetch(cursor)
		if err != nil {
			t.Fatalf("fetch page: %v", err)
		}
		for _, item := range page.Items {
			value := id(item)
			if seen[value] {
				t.Fatalf("duplicate item %q", value)
			}
			seen[value] = true
			result = append(result, value)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(result) == 0 {
		t.Fatal("empty pagination result")
	}
	return result
}
