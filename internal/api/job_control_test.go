package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestJobControlHandlerRejectsInvalidCommandsAndNeverAcceptsScopeFields(t *testing.T) {
	var action JobActionName
	controller := jobControllerFunc(func(_ context.Context, jobID string, requested JobActionName) (storage.AdminJob, error) {
		if jobID != "job-1" {
			t.Fatalf("job id = %q", jobID)
		}
		action = requested
		return storage.AdminJob{ID: jobID, ShareID: "stored-share", TransferID: "stored-transfer", State: core.OneWayJobPaused}, nil
	})
	handler := NewJobControlHandler(controller)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-1/actions", strings.NewReader(`{"action":"pause","share_id":"caller-share","transfer_id":"caller-transfer"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || action != "" {
		t.Fatalf("scope mutation response = %d action=%q body=%s", recorder.Code, action, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-1/actions", strings.NewReader(`{"action":"pause"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || action != JobActionPause {
		t.Fatalf("valid action response = %d action=%q", recorder.Code, action)
	}
	var job JobInventory
	if err := json.Unmarshal(recorder.Body.Bytes(), &job); err != nil || job.ShareID != "stored-share" || job.TransferID != "stored-transfer" {
		t.Fatalf("stored scope was not returned: %s", recorder.Body.String())
	}
}

func TestJobControlHandlerMapsConflictAndMalformedRequests(t *testing.T) {
	handler := NewJobControlHandler(jobControllerFunc(func(context.Context, string, JobActionName) (storage.AdminJob, error) {
		return storage.AdminJob{}, &APIError{Status: http.StatusConflict, Code: "job_state_conflict", Message: "job cannot be controlled in its current state"}
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-1/actions", strings.NewReader(`{"action":"pause"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "job_state_conflict") {
		t.Fatalf("conflict response = %d body=%s", recorder.Code, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/jobs/job-1/actions", strings.NewReader(`{"action":"erase"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid enum status = %d", recorder.Code)
	}
}

func TestControlWithJobStorePreservesIdempotencyAndMapsActiveConflict(t *testing.T) {
	store := &controlJobStore{job: core.OneWayJob{ID: "job-1", TransferID: "transfer-1", PeerDeviceID: "device-1", ShareID: "share-1", RevisionID: "revision-1", RelativePath: "safe.txt", RequiredCapability: core.CapabilityUpload, State: core.OneWayJobQueued, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0)}}
	control := ControlWithJobStore(store, func() time.Time { return time.Unix(2, 0) })
	paused, err := control(context.Background(), "job-1", JobActionPause)
	if err != nil || paused.State != core.OneWayJobPaused {
		t.Fatalf("pause = %#v err=%v", paused, err)
	}
	pausedAgain, err := control(context.Background(), "job-1", JobActionPause)
	if err != nil || pausedAgain.State != core.OneWayJobPaused {
		t.Fatalf("duplicate pause = %#v err=%v", pausedAgain, err)
	}
	store.job.State = core.OneWayJobRunning
	if _, err := control(context.Background(), "job-1", JobActionPause); err == nil {
		t.Fatal("active pause was accepted")
	}
}

type jobControllerFunc func(context.Context, string, JobActionName) (storage.AdminJob, error)

func (function jobControllerFunc) ControlJob(ctx context.Context, jobID string, action JobActionName) (storage.AdminJob, error) {
	return function(ctx, jobID, action)
}

type controlJobStore struct{ job core.OneWayJob }

func (store *controlJobStore) SaveOneWayJob(context.Context, core.OneWayJob) error { return nil }
func (store *controlJobStore) GetOneWayJob(context.Context, string) (core.OneWayJob, error) {
	return store.job, nil
}
func (store *controlJobStore) ListOneWayJobs(context.Context) ([]core.OneWayJob, error) {
	return []core.OneWayJob{store.job}, nil
}
func (store *controlJobStore) ListRunnableOneWayJobs(context.Context, time.Time) ([]core.OneWayJob, error) {
	return nil, nil
}
func (store *controlJobStore) ClaimOneWayJob(context.Context, string, time.Time) (core.OneWayJob, error) {
	return store.job, nil
}
func (store *controlJobStore) UpdateOneWayJob(_ context.Context, job core.OneWayJob) error {
	store.job = job
	return nil
}
func (store *controlJobStore) RecoverRunningOneWayJobs(context.Context, time.Time) error { return nil }

var _ = errors.Is
