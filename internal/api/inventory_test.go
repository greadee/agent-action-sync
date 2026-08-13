package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestAdminInventoryHandlerMapsAllReadModelsWithoutSecrets(t *testing.T) {
	service := &inventoryServiceStub{
		shares:      storage.Page[storage.AdminShare]{Items: []storage.AdminShare{{ID: "share-1", Name: "Drop", RootPath: `C:\private\Drop`, Mode: storage.ShareUploadOnly, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)}}},
		devices:     storage.Page[storage.AdminDevice]{Items: []storage.AdminDevice{{ID: "device-1", DisplayName: "Laptop", Fingerprint: "FP", TrustState: storage.TrustRevoked, PublicKey: []byte("private-key-bytes")}}},
		device:      storage.AdminDevice{ID: "device-1", DisplayName: "Laptop", Fingerprint: "FP", TrustState: storage.TrustRevoked},
		permissions: storage.Page[storage.AdminPermission]{Items: []storage.AdminPermission{{ShareID: "share-1", DeviceID: "device-1", Capabilities: map[core.Capability]bool{core.CapabilityRead: true}, LANOnly: true}}},
		jobs:        storage.Page[storage.AdminJob]{Items: []storage.AdminJob{{ID: "job-1", TransferID: "transfer-1", PeerDeviceID: "device-1", ShareID: "share-1", State: core.OneWayJobFailed, RetryCount: 2}}},
		job:         storage.AdminJob{ID: "job-1", TransferID: "transfer-1", PeerDeviceID: "device-1", ShareID: "share-1", State: core.OneWayJobFailed},
		audit:       storage.Page[storage.AdminAuditEvent]{Items: []storage.AdminAuditEvent{{ID: "audit-1", EventName: "pairing", DeviceID: "device-1", Severity: "info", OccurredAt: time.Unix(3, 0)}}},
	}
	handler := NewAdminV1Handler(service)
	for _, test := range []struct{ path, want string }{
		{path: "/api/v1/shares?limit=1", want: "private"},
		{path: "/api/v1/devices", want: "device-1"},
		{path: "/api/v1/devices/device-1", want: "share-1"},
		{path: "/api/v1/jobs", want: "job-1"},
		{path: "/api/v1/jobs/job-1", want: "transfer-1"},
		{path: "/api/v1/audit-events", want: "audit-1"},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), test.want) {
				t.Fatalf("response missing %q: %s", test.want, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "private-key-bytes") {
				t.Fatal("private key material crossed an inventory boundary")
			}
		})
	}
}

func TestAdminInventoryHandlerUsesOpaqueCursorsAndBoundedFilters(t *testing.T) {
	cursor := storage.PageCursor{ID: "share-1"}
	service := &inventoryServiceStub{shares: storage.Page[storage.AdminShare]{Items: []storage.AdminShare{{ID: "share-1"}}, NextCursor: &cursor}}
	handler := NewAdminV1Handler(service)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/shares?limit=1", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatal(recorder.Body.String())
	}
	var page ShareInventoryPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if !page.Page.HasMore || page.Page.NextCursor == "" || strings.Contains(page.Page.NextCursor, "share-1") {
		t.Fatalf("cursor is not opaque: %#v", page.Page)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/shares?limit=201", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized limit status = %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/shares?cursor=not-valid", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d", recorder.Code)
	}
}

func TestAdminInventoryHandlerUnknownResourcesAndMethods(t *testing.T) {
	handler := NewAdminV1Handler(&inventoryServiceStub{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/devices/missing/extra", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown resource status = %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/shares", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method status = %d headers=%#v", recorder.Code, recorder.Header())
	}
}

type inventoryServiceStub struct {
	shares      storage.Page[storage.AdminShare]
	devices     storage.Page[storage.AdminDevice]
	device      storage.AdminDevice
	permissions storage.Page[storage.AdminPermission]
	jobs        storage.Page[storage.AdminJob]
	job         storage.AdminJob
	audit       storage.Page[storage.AdminAuditEvent]
}

func (stub *inventoryServiceStub) Ready() bool { return true }
func (stub *inventoryServiceStub) ListShares(context.Context, storage.PageRequest) (storage.Page[storage.AdminShare], error) {
	return stub.shares, nil
}
func (stub *inventoryServiceStub) ListDevices(context.Context, storage.PageRequest) (storage.Page[storage.AdminDevice], error) {
	return stub.devices, nil
}
func (stub *inventoryServiceStub) GetDevice(context.Context, core.DeviceID) (storage.AdminDevice, error) {
	return stub.device, nil
}
func (stub *inventoryServiceStub) ListPermissions(context.Context, storage.PermissionQuery) (storage.Page[storage.AdminPermission], error) {
	return stub.permissions, nil
}
func (stub *inventoryServiceStub) ListJobs(context.Context, storage.JobQuery) (storage.Page[storage.AdminJob], error) {
	return stub.jobs, nil
}
func (stub *inventoryServiceStub) GetJob(context.Context, string) (storage.AdminJob, error) {
	return stub.job, nil
}
func (stub *inventoryServiceStub) ListAuditEvents(context.Context, storage.AuditEventQuery) (storage.Page[storage.AdminAuditEvent], error) {
	return stub.audit, nil
}
