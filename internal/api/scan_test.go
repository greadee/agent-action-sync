package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"syncgate/internal/core"
	"syncgate/internal/storage"
	syncengine "syncgate/internal/sync"
)

func TestScanHandlerAcceptsPromptlyAndPreservesShareScope(t *testing.T) {
	var requested core.ShareID
	requester := scanRequesterFunc(func(_ context.Context, shareID core.ShareID) error { requested = shareID; return nil })
	handler := NewScanHandler(requester)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/shares/source-1/scans", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || requested != "source-1" {
		t.Fatalf("scan response = %d requested=%q body=%s", recorder.Code, requested, recorder.Body.String())
	}
}

func TestScanHandlerRejectsUnknownShapesAndPropagatesUnavailable(t *testing.T) {
	for _, path := range []string{"/api/v1/shares/target-1/scans/extra", "/api/v1/shares//scans"} {
		recorder := httptest.NewRecorder()
		NewScanHandler(scanRequesterFunc(func(context.Context, core.ShareID) error { return nil })).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("path %s status = %d", path, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	NewScanHandler(scanRequesterFunc(func(context.Context, core.ShareID) error { return errUnavailable })).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/shares/source-1/scans", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status = %d", recorder.Code)
	}
}

func TestLocalAdministrationServiceScanRequiresConfiguredCallback(t *testing.T) {
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: &stubAdministrationQueries{}, Ready: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RequestScan(context.Background(), "source-1"); err != errUnavailable {
		t.Fatalf("missing scan callback error = %v", err)
	}
	if err := service.RequestScan(context.Background(), "bad/id"); err == nil {
		t.Fatal("invalid share id was accepted")
	}
}

type scanRequesterFunc func(context.Context, core.ShareID) error

func (function scanRequesterFunc) RequestScan(ctx context.Context, shareID core.ShareID) error {
	return function(ctx, shareID)
}

var _ storage.AdministrationQueryStore = (*stubAdministrationQueries)(nil)
var _ = syncengine.ScanDiagnostic{}
