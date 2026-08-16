package api

import (
	"context"
	"net/http"
	"strings"
)

type ProjectMigrationInput struct {
	ShareID   string `json:"share_id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

type ProjectMigrationApplyInput struct {
	ProjectMigrationInput
	Confirmation string `json:"confirmation"`
}

type ProjectMigrationIssue struct {
	RelativePath string `json:"relative_path"`
	Reason       string `json:"reason"`
}

type ProjectMigrationPreflight struct {
	Status                          string                  `json:"status"`
	ShareID                         string                  `json:"share_id"`
	ProjectID                       string                  `json:"project_id"`
	Name                            string                  `json:"name"`
	RootIdentity                    string                  `json:"root_identity"`
	Issues                          []ProjectMigrationIssue `json:"issues"`
	ExcludedPaths                   []string                `json:"excluded_paths"`
	RequiredIgnorePatterns          []string                `json:"required_ignore_patterns"`
	MissingConfiguredIgnorePatterns []string                `json:"missing_configured_ignore_patterns"`
	ConfigurationChangeRequired     bool                    `json:"configuration_change_required"`
	ExpectedPortableRecords         []string                `json:"expected_portable_records"`
	ExistingProjectID               string                  `json:"existing_project_id,omitempty"`
	Confirmation                    string                  `json:"confirmation"`
}

type ProjectMigrationApplyResult struct {
	Status             string `json:"status"`
	ProjectID          string `json:"project_id"`
	ShareID            string `json:"share_id"`
	CreatedRecords     int    `json:"created_records"`
	ProjectedEvents    int    `json:"projected_events"`
	ProjectedArtifacts int    `json:"projected_artifacts"`
	InsightCount       int    `json:"insight_count"`
	EventWatermark     string `json:"event_watermark"`
	AuditEventID       string `json:"audit_event_id"`
	ScanRequested      bool   `json:"scan_requested"`
}

type ProjectMigrationPreflightFunc func(context.Context, ProjectMigrationInput) (ProjectMigrationPreflight, error)
type ProjectMigrationApplyFunc func(context.Context, ProjectMigrationApplyInput) (ProjectMigrationApplyResult, error)

type ProjectMigrationAdministration interface {
	PreflightProjectMigration(context.Context, ProjectMigrationInput) (ProjectMigrationPreflight, error)
	ApplyProjectMigration(context.Context, ProjectMigrationApplyInput) (ProjectMigrationApplyResult, error)
}

func (service *LocalAdministrationService) PreflightProjectMigration(ctx context.Context, input ProjectMigrationInput) (ProjectMigrationPreflight, error) {
	if err := validateServiceContext(ctx); err != nil {
		return ProjectMigrationPreflight{}, err
	}
	input = normalizeMigrationInput(input)
	if err := validateMigrationInput(input); err != nil {
		return ProjectMigrationPreflight{}, err
	}
	if service == nil || service.projectMigrationPreflight == nil || !service.Ready() {
		return ProjectMigrationPreflight{}, errUnavailable
	}
	return service.projectMigrationPreflight(ctx, input)
}

func (service *LocalAdministrationService) ApplyProjectMigration(ctx context.Context, input ProjectMigrationApplyInput) (ProjectMigrationApplyResult, error) {
	if err := validateServiceContext(ctx); err != nil {
		return ProjectMigrationApplyResult{}, err
	}
	input.ProjectMigrationInput = normalizeMigrationInput(input.ProjectMigrationInput)
	input.Confirmation = strings.TrimSpace(input.Confirmation)
	if err := validateMigrationInput(input.ProjectMigrationInput); err != nil || !validMigrationConfirmation(input.Confirmation) {
		return ProjectMigrationApplyResult{}, errBadRequest
	}
	if service == nil || service.projectMigrationApply == nil || !service.Ready() {
		return ProjectMigrationApplyResult{}, errUnavailable
	}
	service.projectMu.Lock()
	if service.migrating[input.ShareID] {
		service.projectMu.Unlock()
		return ProjectMigrationApplyResult{}, errConflict
	}
	service.migrating[input.ShareID] = true
	service.projectMu.Unlock()
	defer func() {
		service.projectMu.Lock()
		delete(service.migrating, input.ShareID)
		service.projectMu.Unlock()
	}()
	return service.projectMigrationApply(ctx, input)
}

func normalizeMigrationInput(input ProjectMigrationInput) ProjectMigrationInput {
	input.ShareID = strings.TrimSpace(input.ShareID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.Name = strings.TrimSpace(input.Name)
	return input
}

func validateMigrationInput(input ProjectMigrationInput) error {
	if input.ShareID == "" || len(input.ShareID) > 128 || strings.ContainsAny(input.ShareID, "/\\") ||
		input.ProjectID == "" || len(input.ProjectID) > 128 || strings.ContainsAny(input.ProjectID, "/\\") ||
		input.Name == "" || len(input.Name) > 256 {
		return errBadRequest
	}
	return nil
}

func validMigrationConfirmation(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func NewProjectMigrationHandler(service ProjectMigrationAdministration) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if service == nil {
			writeError(writer, request, errUnavailable)
			return
		}
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeError(writer, request, errMethodNotAllowed)
			return
		}
		switch request.URL.Path {
		case "/api/v1/project-migrations/preflight":
			var input ProjectMigrationInput
			if err := decodeJSON(request, &input); err != nil {
				writeError(writer, request, err)
				return
			}
			result, err := service.PreflightProjectMigration(request.Context(), input)
			if err != nil {
				writeError(writer, request, err)
				return
			}
			writeJSON(writer, request, http.StatusOK, result)
		case "/api/v1/project-migrations/apply":
			var input ProjectMigrationApplyInput
			if err := decodeJSON(request, &input); err != nil {
				writeError(writer, request, err)
				return
			}
			result, err := service.ApplyProjectMigration(request.Context(), input)
			if err != nil {
				writeError(writer, request, err)
				return
			}
			writeJSON(writer, request, http.StatusOK, result)
		default:
			writeError(writer, request, errNotFound)
		}
	})
}

var _ ProjectMigrationAdministration = (*LocalAdministrationService)(nil)
