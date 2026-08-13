package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"syncgate/internal/storage"
	syncengine "syncgate/internal/sync"
)

type JobController interface {
	ControlJob(ctx context.Context, jobID string, action JobActionName) (storage.AdminJob, error)
}

var ErrJobStateConflict = errors.New("job state conflicts with requested action")

func (service *LocalAdministrationService) ControlJob(ctx context.Context, jobID string, action JobActionName) (storage.AdminJob, error) {
	if err := validateServiceContext(ctx); err != nil {
		return storage.AdminJob{}, err
	}
	if strings.TrimSpace(jobID) == "" || strings.ContainsAny(jobID, "/\\") {
		return storage.AdminJob{}, &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "job was not found"}
	}
	if service.control == nil {
		return storage.AdminJob{}, errUnavailable
	}
	return service.control(ctx, jobID, action)
}

func NewJobControlHandler(controller JobController) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeError(writer, request, errMethodNotAllowed)
			return
		}
		jobID := strings.TrimPrefix(request.URL.Path, "/api/v1/jobs/")
		jobID = strings.TrimSuffix(jobID, "/actions")
		if !strings.HasSuffix(request.URL.Path, "/actions") || jobID == "" || strings.ContainsAny(jobID, "/\\") {
			writeError(writer, request, errNotFound)
			return
		}
		var command JobAction
		if err := decodeJSON(request, &command); err != nil {
			writeError(writer, request, err)
			return
		}
		if controller == nil {
			writeError(writer, request, errUnavailable)
			return
		}
		job, err := controller.ControlJob(request.Context(), jobID, command.Action)
		if err != nil {
			writeError(writer, request, err)
			return
		}
		writeJSON(writer, request, http.StatusOK, jobInventory(job))
	})
}

func ControlWithJobStore(store storage.OneWayJobStore, now func() time.Time) func(context.Context, string, JobActionName) (storage.AdminJob, error) {
	return func(ctx context.Context, jobID string, action JobActionName) (storage.AdminJob, error) {
		control := syncengine.OneWayJobControl(action)
		current := time.Now().UTC()
		if now != nil {
			current = now().UTC()
		}
		job, err := syncengine.ControlOneWayJob(ctx, store, jobID, control, current)
		if err != nil {
			if errors.Is(err, syncengine.ErrActiveOneWayJob) {
				return storage.AdminJob{}, &APIError{Status: http.StatusConflict, Code: "job_state_conflict", Message: "job cannot be controlled in its current state", Internal: err}
			}
			return storage.AdminJob{}, err
		}
		return storage.AdminJob{ID: job.ID, TransferID: job.TransferID, PeerDeviceID: job.PeerDeviceID, ShareID: job.ShareID, State: job.State, RetryCount: job.RetryCount, NextAttemptAt: job.NextAttemptAt, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt}, nil
	}
}
