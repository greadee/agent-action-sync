package api

import (
	"context"
	"errors"
	"testing"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestAdministrationServiceRequiresQueriesAndReadiness(t *testing.T) {
	if _, err := NewAdministrationService(AdministrationServiceOptions{}); err == nil {
		t.Fatal("missing query store was accepted")
	}
	if _, err := NewAdministrationService(AdministrationServiceOptions{Queries: &stubAdministrationQueries{}}); err == nil {
		t.Fatal("missing readiness function was accepted")
	}
}

func TestAdministrationServiceDelegatesBoundedQueriesAndCopiesMutableValues(t *testing.T) {
	publicKey := []byte("public-key")
	capabilities := map[core.Capability]bool{core.CapabilityRead: true}
	cursor := storage.PageCursor{ID: "device-1"}
	queries := &stubAdministrationQueries{
		devices: storage.Page[storage.AdminDevice]{
			Items: []storage.AdminDevice{{ID: "device-1", PublicKey: publicKey}}, NextCursor: &cursor,
		},
		device: storage.AdminDevice{ID: "device-1", PublicKey: publicKey},
		permissions: storage.Page[storage.AdminPermission]{Items: []storage.AdminPermission{{
			ShareID: "share-1", DeviceID: "device-1", Capabilities: capabilities,
		}}},
	}
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: queries, Ready: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListDevices(context.Background(), storage.PageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	page.Items[0].PublicKey[0] = 'X'
	page.NextCursor.ID = "changed"
	if string(publicKey) != "public-key" || cursor.ID != "device-1" {
		t.Fatal("device page aliases query-store memory")
	}
	device, err := service.GetDevice(context.Background(), "device-1")
	if err != nil {
		t.Fatal(err)
	}
	device.PublicKey[0] = 'Y'
	if string(publicKey) != "public-key" {
		t.Fatal("device detail aliases query-store bytes")
	}
	permissions, err := service.ListPermissions(context.Background(), storage.PermissionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	permissions.Items[0].Capabilities[core.CapabilityRead] = false
	if !capabilities[core.CapabilityRead] {
		t.Fatal("permission result aliases query-store map")
	}
}

func TestAdministrationServiceReadinessAndContextCancellation(t *testing.T) {
	ready := false
	service, err := NewAdministrationService(AdministrationServiceOptions{
		Queries: &stubAdministrationQueries{}, Ready: func() bool { return ready },
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.Ready() {
		t.Fatal("service reported ready")
	}
	ready = true
	if !service.Ready() {
		t.Fatal("service did not report ready")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ListShares(ctx, storage.PageRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}
	if queriesCalled := service.queries.(*stubAdministrationQueries).calls; queriesCalled != 0 {
		t.Fatalf("query store called after cancellation: %d", queriesCalled)
	}
}

func TestAdministrationServicePreservesStorageErrors(t *testing.T) {
	queries := &stubAdministrationQueries{err: storage.ErrNotFound}
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: queries, Ready: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetJob(context.Background(), "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("storage error identity was lost: %v", err)
	}
}

type stubAdministrationQueries struct {
	devices     storage.Page[storage.AdminDevice]
	device      storage.AdminDevice
	permissions storage.Page[storage.AdminPermission]
	err         error
	calls       int
}

func (queries *stubAdministrationQueries) ListAdminShares(context.Context, storage.PageRequest) (storage.Page[storage.AdminShare], error) {
	queries.calls++
	return storage.Page[storage.AdminShare]{}, queries.err
}

func (queries *stubAdministrationQueries) ListAdminDevices(context.Context, storage.PageRequest) (storage.Page[storage.AdminDevice], error) {
	queries.calls++
	return queries.devices, queries.err
}

func (queries *stubAdministrationQueries) GetAdminDevice(context.Context, core.DeviceID) (storage.AdminDevice, error) {
	queries.calls++
	return queries.device, queries.err
}

func (queries *stubAdministrationQueries) ListAdminPermissions(context.Context, storage.PermissionQuery) (storage.Page[storage.AdminPermission], error) {
	queries.calls++
	return queries.permissions, queries.err
}

func (queries *stubAdministrationQueries) ListAdminJobs(context.Context, storage.JobQuery) (storage.Page[storage.AdminJob], error) {
	queries.calls++
	return storage.Page[storage.AdminJob]{}, queries.err
}

func (queries *stubAdministrationQueries) GetAdminJob(context.Context, string) (storage.AdminJob, error) {
	queries.calls++
	return storage.AdminJob{}, queries.err
}

func (queries *stubAdministrationQueries) ListAdminAuditEvents(context.Context, storage.AuditEventQuery) (storage.Page[storage.AdminAuditEvent], error) {
	queries.calls++
	return storage.Page[storage.AdminAuditEvent]{}, queries.err
}
