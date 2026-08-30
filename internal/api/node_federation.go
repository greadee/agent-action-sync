package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"syncgate/internal/nodestatus"
	"syncgate/internal/storage"
)

type FederatedProjectSummary struct {
	ProjectID                string                   `json:"project_id"`
	DisplayName              string                   `json:"display_name"`
	SchedulerState           string                   `json:"scheduler_state"`
	AssignmentCounts         []nodestatus.StatusCount `json:"assignment_counts,omitempty"`
	GateCounts               []nodestatus.StatusCount `json:"gate_counts,omitempty"`
	AcceptedHistoryWatermark string                   `json:"accepted_history_watermark,omitempty"`
}

type FederatedNodeItem struct {
	DeviceID       string                    `json:"device_id"`
	DisplayName    string                    `json:"display_name"`
	Protocol       string                    `json:"protocol"`
	Revision       int64                     `json:"revision"`
	Watermark      string                    `json:"watermark"`
	Health         string                    `json:"health"`
	Lifecycle      string                    `json:"lifecycle"`
	Connectivity   string                    `json:"connectivity"`
	ObservedAt     string                    `json:"observed_at"`
	ExpiresAt      string                    `json:"expires_at"`
	GrantExpiresAt string                    `json:"grant_expires_at"`
	Projects       []FederatedProjectSummary `json:"projects"`
}

type FederatedNodePage struct {
	Items []FederatedNodeItem `json:"items"`
}

func (service *LocalAdministrationService) ListFederatedNodes(ctx context.Context) (FederatedNodePage, error) {
	if err := validateServiceContext(ctx); err != nil {
		return FederatedNodePage{}, err
	}
	if service.nodeStatus == nil {
		return FederatedNodePage{}, errUnavailable
	}
	now := service.currentTime()
	replicas, err := service.nodeStatus.ListVisibleNodeStatusReplicas(ctx, now, storage.MaxAdminPageLimit)
	if err != nil {
		return FederatedNodePage{}, fmt.Errorf("list federated nodes: %w", err)
	}
	items := make([]FederatedNodeItem, 0, len(replicas))
	for _, replica := range replicas {
		snapshot, err := nodestatus.DecodeSnapshot(replica.SnapshotJSON)
		if err != nil || snapshot.SourceDeviceID != replica.DeviceID || snapshot.Revision != replica.Revision || snapshot.Watermark != replica.Watermark || snapshot.Protocol != replica.Protocol {
			continue
		}
		device, err := service.queries.GetAdminDevice(ctx, replica.DeviceID)
		if err != nil || device.TrustState != storage.TrustTrusted {
			continue
		}
		connectivity := "online"
		if !replica.ExpiresAt.After(now) {
			connectivity = "offline"
		}
		projects := make([]FederatedProjectSummary, 0, len(snapshot.Projects))
		for _, project := range snapshot.Projects {
			projects = append(projects, FederatedProjectSummary{
				ProjectID: project.ProjectID, DisplayName: project.DisplayName, SchedulerState: project.SchedulerState,
				AssignmentCounts: append([]nodestatus.StatusCount(nil), project.AssignmentCounts...), GateCounts: append([]nodestatus.StatusCount(nil), project.GateCounts...),
				AcceptedHistoryWatermark: project.AcceptedHistoryWatermark,
			})
		}
		items = append(items, FederatedNodeItem{
			DeviceID: string(replica.DeviceID), DisplayName: device.DisplayName, Protocol: replica.Protocol,
			Revision: replica.Revision, Watermark: replica.Watermark, Health: snapshot.Health, Lifecycle: snapshot.Lifecycle,
			Connectivity: connectivity, ObservedAt: replica.ObservedAt.UTC().Format(timeFormat), ExpiresAt: replica.ExpiresAt.UTC().Format(timeFormat),
			GrantExpiresAt: replica.GrantExpiresAt.UTC().Format(timeFormat), Projects: projects,
		})
	}
	return FederatedNodePage{Items: items}, nil
}

func NewNodeFederationHandler(service *LocalAdministrationService) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/federation/nodes" {
			writeError(writer, request, errNotFound)
			return
		}
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writeError(writer, request, errMethodNotAllowed)
			return
		}
		if service == nil {
			writeError(writer, request, errUnavailable)
			return
		}
		page, err := service.ListFederatedNodes(request.Context())
		if err != nil {
			writeError(writer, request, err)
			return
		}
		writeJSON(writer, request, http.StatusOK, page)
	})
}

func (service *LocalAdministrationService) currentTime() time.Time {
	if service.now == nil {
		return time.Now().UTC()
	}
	return service.now().UTC()
}
