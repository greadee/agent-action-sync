package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	syncengine "syncgate/internal/sync"
)

const (
	APIVersion                  = "v1"
	DefaultDiagnosticsItemLimit = 50
	MaxDiagnosticsItemLimit     = 200
)

type LifecycleState string

const (
	LifecycleStarting LifecycleState = "starting"
	LifecycleRunning  LifecycleState = "running"
	LifecycleDraining LifecycleState = "draining"
)

type RuntimeSnapshot struct {
	DeviceID            string
	Fingerprint         string
	StartedAt           time.Time
	Lifecycle           LifecycleState
	ActiveShareCount    int
	PeerExecutorEnabled bool
}

type QueueSummary struct {
	Pending int `json:"pending"`
	Running int `json:"running"`
	Paused  int `json:"paused"`
	Failed  int `json:"failed"`
}

type AdminStatus struct {
	Status              string         `json:"status"`
	APIVersion          string         `json:"api_version"`
	DeviceID            string         `json:"device_id"`
	Fingerprint         string         `json:"fingerprint"`
	StartedAt           time.Time      `json:"started_at"`
	Lifecycle           LifecycleState `json:"lifecycle"`
	ActiveShareCount    int            `json:"active_share_count"`
	PeerExecutorEnabled bool           `json:"peer_executor_enabled"`
	Queue               QueueSummary   `json:"queue"`
}

type AdminDiagnostics struct {
	GeneratedAt  time.Time                     `json:"generated_at"`
	RecentScans  []syncengine.ScanDiagnostic   `json:"recent_scans"`
	Work         []syncengine.WorkDiagnostic   `json:"work"`
	IgnoredPaths []syncengine.IgnoreDiagnostic `json:"ignored_paths"`
}

type StatusReader interface {
	Status(ctx context.Context) (AdminStatus, error)
	Diagnostics(ctx context.Context, limit int) (AdminDiagnostics, error)
}

func (service *LocalAdministrationService) Status(ctx context.Context) (AdminStatus, error) {
	if err := validateServiceContext(ctx); err != nil {
		return AdminStatus{}, err
	}
	if service.runtime == nil {
		return AdminStatus{}, errors.New("administration runtime snapshot is unavailable")
	}
	snapshot := service.runtime()
	if snapshot.Lifecycle == "" {
		snapshot.Lifecycle = LifecycleStarting
	}
	report, err := service.diagnosticsReport(ctx)
	if err != nil {
		return AdminStatus{}, err
	}
	return AdminStatus{
		Status: string(snapshot.Lifecycle), APIVersion: APIVersion,
		DeviceID: snapshot.DeviceID, Fingerprint: snapshot.Fingerprint,
		StartedAt: snapshot.StartedAt, Lifecycle: snapshot.Lifecycle,
		ActiveShareCount:    snapshot.ActiveShareCount,
		PeerExecutorEnabled: snapshot.PeerExecutorEnabled,
		Queue:               queueSummary(report.Work),
	}, nil
}

func (service *LocalAdministrationService) Diagnostics(ctx context.Context, limit int) (AdminDiagnostics, error) {
	if err := validateServiceContext(ctx); err != nil {
		return AdminDiagnostics{}, err
	}
	if limit == 0 {
		limit = DefaultDiagnosticsItemLimit
	}
	if limit < 1 || limit > MaxDiagnosticsItemLimit {
		return AdminDiagnostics{}, fmt.Errorf("diagnostics limit must be between 1 and %d", MaxDiagnosticsItemLimit)
	}
	report, err := service.diagnosticsReport(ctx)
	if err != nil {
		return AdminDiagnostics{}, err
	}
	if len(report.RecentScans) > limit {
		report.RecentScans = report.RecentScans[len(report.RecentScans)-limit:]
	}
	if len(report.Work) > limit {
		report.Work = report.Work[:limit]
	}
	if len(report.IgnoredPaths) > limit {
		report.IgnoredPaths = report.IgnoredPaths[:limit]
	}
	return AdminDiagnostics{GeneratedAt: report.GeneratedAt, RecentScans: append([]syncengine.ScanDiagnostic(nil), report.RecentScans...), Work: append([]syncengine.WorkDiagnostic(nil), report.Work...), IgnoredPaths: append([]syncengine.IgnoreDiagnostic(nil), report.IgnoredPaths...)}, nil
}

func (service *LocalAdministrationService) diagnosticsReport(ctx context.Context) (syncengine.DiagnosticReport, error) {
	if service.diagnostics == nil {
		return syncengine.DiagnosticReport{}, errors.New("administration diagnostics provider is unavailable")
	}
	report := service.diagnostics()
	if err := ctx.Err(); err != nil {
		return syncengine.DiagnosticReport{}, err
	}
	return report, nil
}

func queueSummary(work []syncengine.WorkDiagnostic) QueueSummary {
	var summary QueueSummary
	for _, item := range work {
		switch item.State {
		case "queued", "retry_wait":
			summary.Pending++
		case "running":
			summary.Running++
		case "paused":
			summary.Paused++
		case "failed":
			summary.Failed++
		}
	}
	return summary
}

func NewStatusDiagnosticsHandler(reader StatusReader) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if reader == nil {
			writeError(writer, request, errUnavailable)
			return
		}
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writeError(writer, request, errMethodNotAllowed)
			return
		}
		var response any
		var err error
		switch request.URL.Path {
		case "/api/v1/status":
			response, err = reader.Status(request.Context())
		case "/api/v1/diagnostics":
			limit, parseErr := diagnosticsLimit(request.URL.Query().Get("limit"))
			if parseErr != nil {
				err = fmt.Errorf("%w: %v", errBadRequest, parseErr)
			} else {
				response, err = reader.Diagnostics(request.Context(), limit)
			}
		default:
			writeError(writer, request, errNotFound)
			return
		}
		if err != nil {
			writeError(writer, request, err)
			return
		}
		writeJSON(writer, request, http.StatusOK, response)
	})
}

func diagnosticsLimit(value string) (int, error) {
	if value == "" {
		return DefaultDiagnosticsItemLimit, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > MaxDiagnosticsItemLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", MaxDiagnosticsItemLimit)
	}
	return limit, nil
}
