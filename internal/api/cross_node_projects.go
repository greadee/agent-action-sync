package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"syncgate/internal/nodestatus"
	"syncgate/internal/storage"
)

const federationStaleAfter = 2 * time.Minute

type CrossNodeProjectObservation struct {
	DeviceID                 string `json:"device_id"`
	DisplayName              string `json:"display_name"`
	ObservationKind          string `json:"observation_kind"`
	Compatibility            string `json:"compatibility"`
	CompatibilityMessage     string `json:"compatibility_message,omitempty"`
	Protocol                 string `json:"protocol"`
	Revision                 int64  `json:"revision"`
	SnapshotWatermark        string `json:"snapshot_watermark"`
	AcceptedHistoryWatermark string `json:"accepted_history_watermark,omitempty"`
	SchedulerState           string `json:"scheduler_state"`
	ObservedAt               string `json:"observed_at"`
	ExpiresAt                string `json:"expires_at"`
}

type CrossNodeProjectItem struct {
	ProjectID    string                        `json:"project_id"`
	DisplayName  string                        `json:"display_name"`
	Authority    string                        `json:"authority"`
	Observations []CrossNodeProjectObservation `json:"observations"`
	Insights     []string                      `json:"insights"`
}

type CrossNodeProjectPage struct {
	Items []CrossNodeProjectItem `json:"items"`
}

type crossNodeProjectAggregate struct {
	displayName  string
	authority    string
	observations []CrossNodeProjectObservation
}

// ListCrossNodeProjects projects independently signed remote summaries into a
// comparison view. It never merges remote stores or promotes a replica to
// scheduling authority.
func (service *LocalAdministrationService) ListCrossNodeProjects(ctx context.Context) (CrossNodeProjectPage, error) {
	if err := validateServiceContext(ctx); err != nil {
		return CrossNodeProjectPage{}, err
	}
	nodes, err := service.ListFederatedNodes(ctx)
	if err != nil {
		return CrossNodeProjectPage{}, err
	}
	aggregates := map[string]*crossNodeProjectAggregate{}
	if service.orchestration != nil {
		local, err := service.orchestration.ListLocalProjects(ctx, storage.PageRequest{Limit: storage.MaxAdminPageLimit})
		if err != nil && !unavailableLocalAuthority(err) {
			return CrossNodeProjectPage{}, fmt.Errorf("list local project authority: %w", err)
		}
		if err == nil {
			for _, project := range local.Items {
				aggregates[project.ProjectID] = &crossNodeProjectAggregate{displayName: project.DisplayName, authority: "local_control_store"}
			}
		}
	}
	now := service.currentTime()
	for _, node := range nodes.Items {
		for _, project := range node.Projects {
			aggregate := aggregates[project.ProjectID]
			if aggregate == nil {
				aggregate = &crossNodeProjectAggregate{displayName: project.DisplayName, authority: "not_observed_locally"}
				aggregates[project.ProjectID] = aggregate
			}
			if aggregate.displayName == "" {
				aggregate.displayName = project.DisplayName
			}
			kind, compatibility, message := classifyFederatedObservation(node, now)
			aggregate.observations = append(aggregate.observations, CrossNodeProjectObservation{
				DeviceID: node.DeviceID, DisplayName: node.DisplayName, ObservationKind: kind, Compatibility: compatibility,
				CompatibilityMessage: message, Protocol: node.Protocol, Revision: node.Revision, SnapshotWatermark: node.Watermark,
				AcceptedHistoryWatermark: project.AcceptedHistoryWatermark, SchedulerState: project.SchedulerState,
				ObservedAt: node.ObservedAt, ExpiresAt: node.ExpiresAt,
			})
		}
	}
	ids := make([]string, 0, len(aggregates))
	for id := range aggregates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]CrossNodeProjectItem, 0, len(ids))
	for _, id := range ids {
		aggregate := aggregates[id]
		sort.Slice(aggregate.observations, func(left, right int) bool {
			return aggregate.observations[left].DeviceID < aggregate.observations[right].DeviceID
		})
		items = append(items, CrossNodeProjectItem{ProjectID: id, DisplayName: aggregate.displayName, Authority: aggregate.authority, Observations: aggregate.observations, Insights: crossNodeInsights(aggregate.authority, aggregate.observations)})
	}
	return CrossNodeProjectPage{Items: items}, nil
}

func unavailableLocalAuthority(err error) bool {
	if errors.Is(err, errUnavailable) {
		return true
	}
	var apiError *APIError
	return errors.As(err, &apiError) && apiError.Status == http.StatusServiceUnavailable && apiError.Code == "unavailable"
}

func classifyFederatedObservation(node FederatedNodeItem, now time.Time) (string, string, string) {
	compatibility := "current"
	if node.Protocol != nodestatus.Protocol {
		return "incompatible", "downgraded", "This legacy status schema is shown only as a bounded read-only observation."
	}
	expiresAt, expiresErr := time.Parse(timeFormat, node.ExpiresAt)
	observedAt, observedErr := time.Parse(timeFormat, node.ObservedAt)
	if node.Connectivity == "offline" || expiresErr == nil && !expiresAt.After(now) {
		return "offline", compatibility, ""
	}
	if observedErr == nil && !observedAt.After(now.Add(-federationStaleAfter)) {
		return "stale", compatibility, ""
	}
	return "replica", compatibility, ""
}

func crossNodeInsights(authority string, observations []CrossNodeProjectObservation) []string {
	insights := []string{"remote_replicas_never_confer_execution_authority"}
	if authority == "local_control_store" {
		insights = append(insights, "local_control_store_is_scheduler_authority")
	} else {
		insights = append(insights, "no_local_scheduler_authority_observed")
	}
	watermarks := map[string]bool{}
	for _, observation := range observations {
		if observation.AcceptedHistoryWatermark != "" {
			watermarks[observation.AcceptedHistoryWatermark] = true
		}
		switch observation.ObservationKind {
		case "offline":
			insights = append(insights, "offline_replica_observation")
		case "stale":
			insights = append(insights, "stale_replica_observation")
		case "incompatible":
			insights = append(insights, "legacy_schema_downgrade")
		}
	}
	switch len(watermarks) {
	case 0:
		insights = append(insights, "accepted_history_watermark_unavailable")
	case 1:
		insights = append(insights, "accepted_history_watermarks_match")
	default:
		insights = append(insights, "accepted_history_watermarks_differ")
	}
	sort.Strings(insights)
	return compactStrings(insights)
}

func compactStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func NewNodeFederationHandler(service *LocalAdministrationService) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/federation/nodes" && request.URL.Path != "/api/v1/federation/projects" {
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
		if request.URL.Path == "/api/v1/federation/projects" {
			page, err := service.ListCrossNodeProjects(request.Context())
			if err != nil {
				writeError(writer, request, err)
				return
			}
			writeJSON(writer, request, http.StatusOK, page)
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
