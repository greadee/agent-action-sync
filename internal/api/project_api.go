package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"syncgate/internal/insights"
	"syncgate/internal/storage"
)

type ProjectQueryStore interface {
	ProjectRegistrations() storage.ProjectRegistrationStore
	ProjectEvents() storage.ProjectEventStore
	ProjectArtifacts() storage.ProjectArtifactStore
	ProjectInsights() storage.ProjectInsightStore
	ProjectCheckpoints() storage.ProjectCheckpointStore
	ProjectRejections() storage.ProjectRejectionStore
}

type ProjectRebuildResult struct {
	EventWatermark string
	InsightCount   int
	EventCount     int
	ArtifactCount  int
}

type ProjectRebuildFunc func(context.Context, string) (ProjectRebuildResult, error)

type ProjectProjectionState struct {
	History      string `json:"history"`
	Insights     string `json:"insights"`
	CheckpointAt string `json:"checkpoint_at,omitempty"`
}

type ProjectAdministration interface {
	ListProjects(context.Context, storage.PageRequest) (storage.Page[storage.ProjectRegistration], error)
	GetProject(context.Context, string) (storage.ProjectRegistration, error)
	ListProjectHistory(context.Context, storage.ProjectEventQuery) (storage.Page[storage.ProjectEventProjection], error)
	ListProjectArtifacts(context.Context, storage.ProjectArtifactQuery) (storage.Page[storage.ProjectArtifactProjection], error)
	ListProjectInsights(context.Context, storage.ProjectInsightQuery) (storage.Page[storage.ProjectInsightProjection], error)
	ListProjectRejections(context.Context, storage.ProjectRejectionQuery) (storage.Page[storage.ProjectProjectionRejection], error)
	ProjectProjectionState(context.Context, string) (ProjectProjectionState, error)
	RebuildProject(context.Context, string) (ProjectRebuildResult, error)
}

func (service *LocalAdministrationService) ListProjects(ctx context.Context, page storage.PageRequest) (storage.Page[storage.ProjectRegistration], error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.Page[storage.ProjectRegistration]{}, err
	}
	if err := validateProjectAPIPage(page, projectCursorByID); err != nil {
		return storage.Page[storage.ProjectRegistration]{}, err
	}
	return service.projectStore.ProjectRegistrations().ListProjects(ctx, page)
}

func (service *LocalAdministrationService) GetProject(ctx context.Context, projectID string) (storage.ProjectRegistration, error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.ProjectRegistration{}, err
	}
	if err := validateProjectAPIID(projectID); err != nil {
		return storage.ProjectRegistration{}, err
	}
	return service.projectStore.ProjectRegistrations().GetProject(ctx, projectID)
}

func (service *LocalAdministrationService) ListProjectHistory(ctx context.Context, query storage.ProjectEventQuery) (storage.Page[storage.ProjectEventProjection], error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.Page[storage.ProjectEventProjection]{}, err
	}
	if err := validateProjectAPIID(query.ProjectID); err != nil {
		return storage.Page[storage.ProjectEventProjection]{}, err
	}
	if _, err := service.projectStore.ProjectRegistrations().GetProject(ctx, query.ProjectID); err != nil {
		return storage.Page[storage.ProjectEventProjection]{}, err
	}
	if err := validateProjectAPIPage(query.Page, projectCursorByTime); err != nil {
		return storage.Page[storage.ProjectEventProjection]{}, err
	}
	if err := validateProjectAPIFilters(query.EventType, query.WorkPackageID, query.ExecutionID); err != nil {
		return storage.Page[storage.ProjectEventProjection]{}, err
	}
	return service.projectStore.ProjectEvents().ListProjectEvents(ctx, query)
}

func (service *LocalAdministrationService) ListProjectArtifacts(ctx context.Context, query storage.ProjectArtifactQuery) (storage.Page[storage.ProjectArtifactProjection], error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.Page[storage.ProjectArtifactProjection]{}, err
	}
	if err := validateProjectAPIID(query.ProjectID); err != nil {
		return storage.Page[storage.ProjectArtifactProjection]{}, err
	}
	if _, err := service.projectStore.ProjectRegistrations().GetProject(ctx, query.ProjectID); err != nil {
		return storage.Page[storage.ProjectArtifactProjection]{}, err
	}
	if err := validateProjectAPIPage(query.Page, projectCursorByTime); err != nil {
		return storage.Page[storage.ProjectArtifactProjection]{}, err
	}
	if err := validateProjectAPIFilters(query.WorkPackageID, query.ExecutionID, query.MediaType); err != nil {
		return storage.Page[storage.ProjectArtifactProjection]{}, err
	}
	return service.projectStore.ProjectArtifacts().ListProjectArtifacts(ctx, query)
}

func (service *LocalAdministrationService) ListProjectInsights(ctx context.Context, query storage.ProjectInsightQuery) (storage.Page[storage.ProjectInsightProjection], error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.Page[storage.ProjectInsightProjection]{}, err
	}
	if err := validateProjectAPIID(query.ProjectID); err != nil {
		return storage.Page[storage.ProjectInsightProjection]{}, err
	}
	if _, err := service.projectStore.ProjectRegistrations().GetProject(ctx, query.ProjectID); err != nil {
		return storage.Page[storage.ProjectInsightProjection]{}, err
	}
	if err := validateProjectAPIPage(query.Page, projectCursorByMetric); err != nil {
		return storage.Page[storage.ProjectInsightProjection]{}, err
	}
	if err := validateProjectAPIFilters(query.Scope, query.MetricName); err != nil {
		return storage.Page[storage.ProjectInsightProjection]{}, err
	}
	return service.projectStore.ProjectInsights().ListProjectInsights(ctx, query)
}

func (service *LocalAdministrationService) ListProjectRejections(ctx context.Context, query storage.ProjectRejectionQuery) (storage.Page[storage.ProjectProjectionRejection], error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.Page[storage.ProjectProjectionRejection]{}, err
	}
	if err := validateProjectAPIID(query.ProjectID); err != nil {
		return storage.Page[storage.ProjectProjectionRejection]{}, err
	}
	if _, err := service.projectStore.ProjectRegistrations().GetProject(ctx, query.ProjectID); err != nil {
		return storage.Page[storage.ProjectProjectionRejection]{}, err
	}
	if err := validateProjectAPIPage(query.Page, projectCursorByTime); err != nil {
		return storage.Page[storage.ProjectProjectionRejection]{}, err
	}
	return service.projectStore.ProjectRejections().ListProjectRejections(ctx, query)
}

func (service *LocalAdministrationService) ProjectProjectionState(ctx context.Context, projectID string) (ProjectProjectionState, error) {
	if err := service.requireProjects(ctx); err != nil {
		return ProjectProjectionState{}, err
	}
	if err := validateProjectAPIID(projectID); err != nil {
		return ProjectProjectionState{}, err
	}
	if _, err := service.projectStore.ProjectRegistrations().GetProject(ctx, projectID); err != nil {
		return ProjectProjectionState{}, err
	}
	service.projectMu.Lock()
	rebuilding := service.rebuilding[projectID]
	service.projectMu.Unlock()
	if rebuilding {
		return ProjectProjectionState{History: "rebuilding", Insights: "rebuilding"}, nil
	}
	state := ProjectProjectionState{History: "unavailable", Insights: "unavailable"}
	checkpoint, err := service.projectStore.ProjectCheckpoints().GetProjectCheckpoint(ctx, projectID, "portable-record-set-v1")
	if err == nil {
		state.History = "available"
		state.CheckpointAt = checkpoint.UpdatedAt.UTC().Format(timeFormat)
	} else if !errors.Is(err, storage.ErrNotFound) {
		return ProjectProjectionState{}, err
	}
	page, err := service.projectStore.ProjectInsights().ListProjectInsights(ctx, storage.ProjectInsightQuery{ProjectID: projectID, Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit}})
	if err != nil {
		return ProjectProjectionState{}, err
	}
	if len(page.Items) > 0 {
		state.Insights = "available"
		if insightsStale(page.Items) {
			state.Insights = "stale"
		}
	}
	return state, nil
}

func (service *LocalAdministrationService) RebuildProject(ctx context.Context, projectID string) (ProjectRebuildResult, error) {
	if err := service.requireProjects(ctx); err != nil {
		return ProjectRebuildResult{}, err
	}
	if err := validateProjectAPIID(projectID); err != nil {
		return ProjectRebuildResult{}, err
	}
	if service.projectRebuild == nil {
		return ProjectRebuildResult{}, errUnavailable
	}
	if _, err := service.projectStore.ProjectRegistrations().GetProject(ctx, projectID); err != nil {
		return ProjectRebuildResult{}, err
	}
	service.projectMu.Lock()
	if service.rebuilding[projectID] {
		service.projectMu.Unlock()
		return ProjectRebuildResult{}, errConflict
	}
	service.rebuilding[projectID] = true
	service.projectMu.Unlock()
	defer func() { service.projectMu.Lock(); delete(service.rebuilding, projectID); service.projectMu.Unlock() }()
	if !service.Ready() {
		return ProjectRebuildResult{}, errUnavailable
	}
	return service.projectRebuild(ctx, projectID)
}

func (service *LocalAdministrationService) requireProjects(ctx context.Context) error {
	if err := validateServiceContext(ctx); err != nil {
		return err
	}
	if service == nil || service.projectStore == nil {
		return errUnavailable
	}
	return nil
}

type projectCursorKind int

const (
	projectCursorByID projectCursorKind = iota
	projectCursorByTime
	projectCursorByMetric
)

func validateProjectAPIPage(page storage.PageRequest, kind projectCursorKind) error {
	if _, err := storage.NormalizePageRequest(page); err != nil {
		return errBadRequest
	}
	switch kind {
	case projectCursorByID:
		if !page.Cursor.Timestamp.IsZero() || page.Cursor.SecondaryID != "" || page.Cursor.Version != 0 {
			return errBadRequest
		}
	case projectCursorByTime:
		if page.Cursor.Timestamp.IsZero() != (page.Cursor.ID == "") || page.Cursor.SecondaryID != "" || page.Cursor.Version != 0 {
			return errBadRequest
		}
	case projectCursorByMetric:
		if !page.Cursor.Timestamp.IsZero() || page.Cursor.Version < 0 || (page.Cursor.ID == "") != (page.Cursor.SecondaryID == "") || (page.Cursor.ID == "") != (page.Cursor.Version == 0) {
			return errBadRequest
		}
	}
	return nil
}

func validateProjectAPIID(value string) error {
	if err := storage.ValidateProjectProjectionID(value); err != nil {
		return errBadRequest
	}
	return nil
}

func validateProjectAPIFilters(values ...string) error {
	for _, value := range values {
		if err := storage.ValidateProjectQueryFilter(value); err != nil {
			return errBadRequest
		}
	}
	return nil
}

func insightsStale(rows []storage.ProjectInsightProjection) bool {
	if len(rows) != len(insights.Definitions) {
		return true
	}
	versions := map[string]int{}
	for _, row := range rows {
		versions[row.MetricName] = row.DefinitionVersion
	}
	if len(versions) != len(insights.Definitions) {
		return true
	}
	for _, definition := range insights.Definitions {
		if versions[definition.Name] != definition.Version {
			return true
		}
	}
	return false
}

type ProjectSummary struct {
	ID                string                 `json:"id"`
	ShareID           string                 `json:"share_id"`
	Name              string                 `json:"name"`
	AuthorityDeviceID string                 `json:"authority_device_id"`
	RegisteredAt      string                 `json:"registered_at"`
	Projection        ProjectProjectionState `json:"projection"`
}
type ProjectPage struct {
	Items []ProjectSummary `json:"items"`
	Page  InventoryPage    `json:"page"`
}
type ProjectHistoryItem struct {
	EventID       string `json:"event_id"`
	EventType     string `json:"event_type"`
	OccurredAt    string `json:"occurred_at"`
	WorkPackageID string `json:"work_package_id,omitempty"`
	ExecutionID   string `json:"execution_id,omitempty"`
	WorkerID      string `json:"worker_id,omitempty"`
	DeviceID      string `json:"device_id"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	Status        string `json:"status"`
	RecordPath    string `json:"record_path"`
	RecordHash    string `json:"record_hash"`
}
type ProjectHistoryPage struct {
	Items []ProjectHistoryItem `json:"items"`
	Page  InventoryPage        `json:"page"`
}
type ProjectArtifactItem struct {
	ArtifactID    string `json:"artifact_id"`
	Name          string `json:"name"`
	MediaType     string `json:"media_type"`
	Size          int64  `json:"size"`
	ContentHash   string `json:"content_hash"`
	WorkPackageID string `json:"work_package_id,omitempty"`
	ExecutionID   string `json:"execution_id,omitempty"`
	CreatedAt     string `json:"created_at"`
	RecordPath    string `json:"record_path"`
}
type ProjectArtifactPage struct {
	Items []ProjectArtifactItem `json:"items"`
	Page  InventoryPage         `json:"page"`
}
type ProjectInsightItem struct {
	Scope                string          `json:"scope"`
	MetricName           string          `json:"metric_name"`
	DefinitionVersion    int             `json:"definition_version"`
	SourceEventWatermark string          `json:"source_event_watermark"`
	Value                json.RawMessage `json:"value"`
	SampleCount          int64           `json:"sample_count"`
	Completeness         string          `json:"completeness"`
	Evidence             string          `json:"evidence"`
	CalculatedAt         string          `json:"calculated_at"`
}
type ProjectInsightPage struct {
	Items  []ProjectInsightItem `json:"items"`
	Page   InventoryPage        `json:"page"`
	Status string               `json:"status"`
}
type ProjectRejectionItem struct {
	RecordPath   string `json:"record_path"`
	ObservedHash string `json:"observed_hash,omitempty"`
	ReasonCode   string `json:"reason_code"`
	RejectedAt   string `json:"rejected_at"`
}
type ProjectRejectionPage struct {
	Items []ProjectRejectionItem `json:"items"`
	Page  InventoryPage          `json:"page"`
}
type ProjectRebuildResponse struct {
	Status         string `json:"status"`
	EventWatermark string `json:"event_watermark"`
	EventCount     int    `json:"event_count"`
	ArtifactCount  int    `json:"artifact_count"`
	InsightCount   int    `json:"insight_count"`
}

func NewProjectHandler(service ProjectAdministration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handleProjects(w, r, service) })
}
func handleProjects(w http.ResponseWriter, r *http.Request, service ProjectAdministration) {
	if service == nil {
		writeError(w, r, errUnavailable)
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/api/v1/projects")
	if suffix == "" || suffix == "/" {
		if r.URL.Path != "/api/v1/projects" || r.Method != http.MethodGet {
			writeProjectMethod(w, r, http.MethodGet)
			return
		}
		handleProjectList(w, r, service)
		return
	}
	parts := strings.Split(strings.TrimPrefix(suffix, "/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		writeError(w, r, errNotFound)
		return
	}
	projectID := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeProjectMethod(w, r, http.MethodGet)
			return
		}
		handleProjectDetail(w, r, service, projectID)
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "history":
			if r.Method != http.MethodGet {
				writeProjectMethod(w, r, http.MethodGet)
				return
			}
			handleProjectHistory(w, r, service, projectID)
			return
		case "artifacts":
			if r.Method != http.MethodGet {
				writeProjectMethod(w, r, http.MethodGet)
				return
			}
			handleProjectArtifacts(w, r, service, projectID)
			return
		case "insights":
			if r.Method != http.MethodGet {
				writeProjectMethod(w, r, http.MethodGet)
				return
			}
			handleProjectInsights(w, r, service, projectID)
			return
		case "rejections":
			if r.Method != http.MethodGet {
				writeProjectMethod(w, r, http.MethodGet)
				return
			}
			handleProjectRejections(w, r, service, projectID)
			return
		default:
			writeError(w, r, errNotFound)
			return
		}
	}
	if len(parts) == 3 && parts[1] == "projections" && parts[2] == "rebuild" {
		if r.Method != http.MethodPost {
			writeProjectMethod(w, r, http.MethodPost)
			return
		}
		result, err := service.RebuildProject(r.Context(), projectID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, ProjectRebuildResponse{Status: "rebuilt", EventWatermark: result.EventWatermark, EventCount: result.EventCount, ArtifactCount: result.ArtifactCount, InsightCount: result.InsightCount})
		return
	}
	writeError(w, r, errNotFound)
}

func handleProjectList(w http.ResponseWriter, r *http.Request, s ProjectAdministration) {
	pageRequest, err := parsePageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := s.ListProjects(r.Context(), pageRequest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]ProjectSummary, 0, len(page.Items))
	for _, item := range page.Items {
		state, stateErr := s.ProjectProjectionState(r.Context(), item.ProjectID)
		if stateErr != nil {
			writeError(w, r, stateErr)
			return
		}
		items = append(items, projectSummary(item, state))
	}
	writeJSON(w, r, http.StatusOK, ProjectPage{Items: items, Page: pageInfo(page, pageRequest.Limit)})
}
func handleProjectDetail(w http.ResponseWriter, r *http.Request, s ProjectAdministration, id string) {
	item, err := s.GetProject(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	state, err := s.ProjectProjectionState(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, projectSummary(item, state))
}
func handleProjectHistory(w http.ResponseWriter, r *http.Request, s ProjectAdministration, id string) {
	pageRequest, err := parsePageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := s.ListProjectHistory(r.Context(), storage.ProjectEventQuery{Page: pageRequest, ProjectID: id, EventType: r.URL.Query().Get("event_type"), WorkPackageID: r.URL.Query().Get("work_package_id"), ExecutionID: r.URL.Query().Get("execution_id")})
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]ProjectHistoryItem, 0, len(page.Items))
	for _, v := range page.Items {
		items = append(items, ProjectHistoryItem{EventID: v.EventID, EventType: v.EventType, OccurredAt: v.OccurredAt.UTC().Format(timeFormat), WorkPackageID: v.WorkPackageID, ExecutionID: v.ExecutionID, WorkerID: v.ProducerWorkerID, DeviceID: string(v.ProducerDeviceID), Provider: sanitizeMessage(v.ProducerProvider), Model: sanitizeMessage(v.ProducerModel), Status: v.Status, RecordPath: v.RecordPath, RecordHash: v.RecordHash})
	}
	writeJSON(w, r, http.StatusOK, ProjectHistoryPage{Items: items, Page: pageInfo(page, pageRequest.Limit)})
}
func handleProjectArtifacts(w http.ResponseWriter, r *http.Request, s ProjectAdministration, id string) {
	pageRequest, err := parsePageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := s.ListProjectArtifacts(r.Context(), storage.ProjectArtifactQuery{Page: pageRequest, ProjectID: id, WorkPackageID: r.URL.Query().Get("work_package_id"), ExecutionID: r.URL.Query().Get("execution_id"), MediaType: r.URL.Query().Get("media_type")})
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]ProjectArtifactItem, 0, len(page.Items))
	for _, v := range page.Items {
		items = append(items, ProjectArtifactItem{ArtifactID: v.ArtifactID, Name: sanitizeMessage(v.Name), MediaType: v.MediaType, Size: v.Size, ContentHash: v.ContentHash, WorkPackageID: v.WorkPackageID, ExecutionID: v.ExecutionID, CreatedAt: v.CreatedAt.UTC().Format(timeFormat), RecordPath: v.RecordPath})
	}
	writeJSON(w, r, http.StatusOK, ProjectArtifactPage{Items: items, Page: pageInfo(page, pageRequest.Limit)})
}
func handleProjectInsights(w http.ResponseWriter, r *http.Request, s ProjectAdministration, id string) {
	pageRequest, err := parsePageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := s.ListProjectInsights(r.Context(), storage.ProjectInsightQuery{Page: pageRequest, ProjectID: id, Scope: r.URL.Query().Get("scope"), MetricName: r.URL.Query().Get("metric_name")})
	if err != nil {
		writeError(w, r, err)
		return
	}
	state, err := s.ProjectProjectionState(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]ProjectInsightItem, 0, len(page.Items))
	for _, v := range page.Items {
		items = append(items, ProjectInsightItem{Scope: v.Scope, MetricName: v.MetricName, DefinitionVersion: v.DefinitionVersion, SourceEventWatermark: v.SourceEventWatermark, Value: append(json.RawMessage(nil), v.ValueJSON...), SampleCount: v.SampleCount, Completeness: v.Completeness, Evidence: v.Evidence, CalculatedAt: v.CalculatedAt.UTC().Format(timeFormat)})
	}
	writeJSON(w, r, http.StatusOK, ProjectInsightPage{Items: items, Page: pageInfo(page, pageRequest.Limit), Status: state.Insights})
}
func handleProjectRejections(w http.ResponseWriter, r *http.Request, s ProjectAdministration, id string) {
	pageRequest, err := parsePageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := s.ListProjectRejections(r.Context(), storage.ProjectRejectionQuery{Page: pageRequest, ProjectID: id})
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]ProjectRejectionItem, 0, len(page.Items))
	for _, value := range page.Items {
		items = append(items, ProjectRejectionItem{RecordPath: value.RecordPath, ObservedHash: value.ObservedHash, ReasonCode: value.ReasonCode, RejectedAt: value.RejectedAt.UTC().Format(timeFormat)})
	}
	writeJSON(w, r, http.StatusOK, ProjectRejectionPage{Items: items, Page: pageInfo(page, pageRequest.Limit)})
}
func projectSummary(v storage.ProjectRegistration, state ProjectProjectionState) ProjectSummary {
	return ProjectSummary{ID: v.ProjectID, ShareID: string(v.ShareID), Name: sanitizeMessage(v.Name), AuthorityDeviceID: string(v.AuthorityDeviceID), RegisteredAt: v.RegisteredAt.UTC().Format(timeFormat), Projection: state}
}
func writeProjectMethod(w http.ResponseWriter, r *http.Request, method string) {
	w.Header().Set("Allow", method)
	writeError(w, r, errMethodNotAllowed)
}

var _ ProjectAdministration = (*LocalAdministrationService)(nil)
