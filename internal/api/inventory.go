package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

type InventoryPage struct {
	Limit      int    `json:"limit"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

type ShareInventory struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	RootPath             string `json:"root_path"`
	Mode                 string `json:"mode"`
	CasePolicy           string `json:"case_policy"`
	VersionPolicy        string `json:"version_policy"`
	DeletionLimitCount   int    `json:"deletion_limit_count"`
	DeletionLimitPercent int    `json:"deletion_limit_percent"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
}

type DeviceInventory struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	State       string `json:"state"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`
}

type DeviceDetail struct {
	Device      DeviceInventory       `json:"device"`
	Permissions []PermissionInventory `json:"permissions"`
}

type PermissionInventory struct {
	ShareID      string          `json:"share_id"`
	Capabilities map[string]bool `json:"capabilities"`
	LANOnly      bool            `json:"lan_only"`
}

type JobInventory struct {
	ID            string `json:"id"`
	TransferID    string `json:"transfer_id"`
	PeerDeviceID  string `json:"peer_device_id"`
	ShareID       string `json:"share_id"`
	State         string `json:"state"`
	RetryCount    int    `json:"retry_count"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type AuditInventory struct {
	ID            string `json:"id"`
	EventName     string `json:"event_name"`
	DeviceID      string `json:"device_id,omitempty"`
	PeerDeviceID  string `json:"peer_device_id,omitempty"`
	ShareID       string `json:"share_id,omitempty"`
	TransferID    string `json:"transfer_id,omitempty"`
	RevisionID    string `json:"revision_id,omitempty"`
	TransportType string `json:"transport_type,omitempty"`
	Severity      string `json:"severity"`
	OccurredAt    string `json:"occurred_at"`
}

type ShareInventoryPage struct {
	Items []ShareInventory `json:"items"`
	Page  InventoryPage    `json:"page"`
}

type DeviceInventoryPage struct {
	Items []DeviceInventory `json:"items"`
	Page  InventoryPage     `json:"page"`
}

type JobInventoryPage struct {
	Items []JobInventory `json:"items"`
	Page  InventoryPage  `json:"page"`
}

type AuditInventoryPage struct {
	Items []AuditInventory `json:"items"`
	Page  InventoryPage    `json:"page"`
}

func NewAdminV1Handler(service AdministrationService) http.Handler {
	statusHandler, _ := service.(StatusReader)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/scans") {
			requester, ok := service.(ScanRequester)
			if !ok {
				writeError(writer, request, errUnavailable)
			} else {
				NewScanHandler(requester).ServeHTTP(writer, request)
			}
			return
		}
		if statusHandler != nil && (request.URL.Path == "/api/v1/status" || request.URL.Path == "/api/v1/diagnostics") {
			NewStatusDiagnosticsHandler(statusHandler).ServeHTTP(writer, request)
			return
		}
		if request.URL.Path == "/api/v1/shares" || strings.HasPrefix(request.URL.Path, "/api/v1/shares/") {
			handleInventoryShares(writer, request, service)
			return
		}
		if request.URL.Path == "/api/v1/devices" || strings.HasPrefix(request.URL.Path, "/api/v1/devices/") {
			handleInventoryDevices(writer, request, service)
			return
		}
		if request.URL.Path == "/api/v1/jobs" || strings.HasPrefix(request.URL.Path, "/api/v1/jobs/") {
			handleInventoryJobs(writer, request, service)
			return
		}
		if request.URL.Path == "/api/v1/audit-events" {
			handleInventoryAudit(writer, request, service)
			return
		}
		writeError(writer, request, errNotFound)
	})
}

func handleInventoryShares(writer http.ResponseWriter, request *http.Request, service AdministrationService) {
	if request.Method != http.MethodGet || request.URL.Path != "/api/v1/shares" {
		writeInventoryMethod(writer, request)
		return
	}
	pageRequest, err := parsePageRequest(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	page, err := service.ListShares(request.Context(), pageRequest)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	items := make([]ShareInventory, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, ShareInventory{ID: string(item.ID), Name: item.Name, RootPath: item.RootPath, Mode: string(item.Mode), CasePolicy: item.CasePolicy, VersionPolicy: item.VersionPolicy, DeletionLimitCount: item.DeletionLimitCount, DeletionLimitPercent: item.DeletionLimitPercent, CreatedAt: item.CreatedAt.UTC().Format(timeFormat), UpdatedAt: item.UpdatedAt.UTC().Format(timeFormat)})
	}
	writeJSON(writer, request, http.StatusOK, ShareInventoryPage{Items: items, Page: pageInfo(page, pageRequest.Limit)})
}

func handleInventoryDevices(writer http.ResponseWriter, request *http.Request, service AdministrationService) {
	if request.Method != http.MethodGet {
		writeInventoryMethod(writer, request)
		return
	}
	if request.URL.Path != "/api/v1/devices" {
		id := strings.TrimPrefix(request.URL.Path, "/api/v1/devices/")
		if id == "" || strings.Contains(id, "/") {
			writeError(writer, request, errNotFound)
			return
		}
		device, err := service.GetDevice(request.Context(), core.DeviceID(id))
		if err != nil {
			writeError(writer, request, err)
			return
		}
		permissions, err := service.ListPermissions(request.Context(), storage.PermissionQuery{Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit}, DeviceID: core.DeviceID(id)})
		if err != nil {
			writeError(writer, request, err)
			return
		}
		items := make([]PermissionInventory, 0, len(permissions.Items))
		for _, permission := range permissions.Items {
			capabilities := make(map[string]bool, len(permission.Capabilities))
			for capability, allowed := range permission.Capabilities {
				capabilities[string(capability)] = allowed
			}
			items = append(items, PermissionInventory{ShareID: string(permission.ShareID), Capabilities: capabilities, LANOnly: permission.LANOnly})
		}
		writeJSON(writer, request, http.StatusOK, DeviceDetail{Device: deviceInventory(device), Permissions: items})
		return
	}
	pageRequest, err := parsePageRequest(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	page, err := service.ListDevices(request.Context(), pageRequest)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	items := make([]DeviceInventory, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, deviceInventory(item))
	}
	writeJSON(writer, request, http.StatusOK, DeviceInventoryPage{Items: items, Page: pageInfo(page, pageRequest.Limit)})
}

func handleInventoryJobs(writer http.ResponseWriter, request *http.Request, service AdministrationService) {
	if request.Method != http.MethodGet {
		writeInventoryMethod(writer, request)
		return
	}
	if request.URL.Path != "/api/v1/jobs" {
		id := strings.TrimPrefix(request.URL.Path, "/api/v1/jobs/")
		if id == "" || strings.Contains(id, "/") {
			writeError(writer, request, errNotFound)
			return
		}
		job, err := service.GetJob(request.Context(), id)
		if err != nil {
			writeError(writer, request, err)
			return
		}
		writeJSON(writer, request, http.StatusOK, jobInventory(job))
		return
	}
	pageRequest, err := parsePageRequest(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	query := storage.JobQuery{Page: pageRequest, ShareID: core.ShareID(request.URL.Query().Get("share_id")), State: core.OneWayJobState(request.URL.Query().Get("state"))}
	page, err := service.ListJobs(request.Context(), query)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	items := make([]JobInventory, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, jobInventory(item))
	}
	writeJSON(writer, request, http.StatusOK, JobInventoryPage{Items: items, Page: pageInfo(page, pageRequest.Limit)})
}

func handleInventoryAudit(writer http.ResponseWriter, request *http.Request, service AdministrationService) {
	if request.Method != http.MethodGet {
		writeInventoryMethod(writer, request)
		return
	}
	pageRequest, err := parsePageRequest(request)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	query := storage.AuditEventQuery{Page: pageRequest, EventName: request.URL.Query().Get("event_name"), DeviceID: core.DeviceID(request.URL.Query().Get("device_id")), ShareID: core.ShareID(request.URL.Query().Get("share_id"))}
	page, err := service.ListAuditEvents(request.Context(), query)
	if err != nil {
		writeError(writer, request, err)
		return
	}
	items := make([]AuditInventory, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, AuditInventory{ID: item.ID, EventName: item.EventName, DeviceID: string(item.DeviceID), PeerDeviceID: string(item.PeerDeviceID), ShareID: string(item.ShareID), TransferID: string(item.TransferID), RevisionID: string(item.RevisionID), TransportType: item.TransportType, Severity: item.Severity, OccurredAt: item.OccurredAt.UTC().Format(timeFormat)})
	}
	writeJSON(writer, request, http.StatusOK, AuditInventoryPage{Items: items, Page: pageInfo(page, pageRequest.Limit)})
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func deviceInventory(item storage.AdminDevice) DeviceInventory {
	result := DeviceInventory{ID: string(item.ID), Name: item.DisplayName, Fingerprint: item.Fingerprint, State: string(item.TrustState), CreatedAt: item.CreatedAt.UTC().Format(timeFormat), UpdatedAt: item.UpdatedAt.UTC().Format(timeFormat)}
	if !item.LastSeenAt.IsZero() {
		result.LastSeenAt = item.LastSeenAt.UTC().Format(timeFormat)
	}
	return result
}
func jobInventory(item storage.AdminJob) JobInventory {
	result := JobInventory{ID: item.ID, TransferID: string(item.TransferID), PeerDeviceID: string(item.PeerDeviceID), ShareID: string(item.ShareID), State: string(item.State), RetryCount: item.RetryCount, CreatedAt: item.CreatedAt.UTC().Format(timeFormat), UpdatedAt: item.UpdatedAt.UTC().Format(timeFormat)}
	if !item.NextAttemptAt.IsZero() {
		result.NextAttemptAt = item.NextAttemptAt.UTC().Format(timeFormat)
	}
	return result
}

func parsePageRequest(request *http.Request) (storage.PageRequest, error) {
	limit := storage.DefaultAdminPageLimit
	if value := request.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return storage.PageRequest{}, fmt.Errorf("%w: invalid limit", errBadRequest)
		}
		limit = parsed
	}
	cursor, err := decodeAdminCursor(request.URL.Query().Get("cursor"))
	if err != nil {
		return storage.PageRequest{}, fmt.Errorf("%w: invalid cursor", errBadRequest)
	}
	page, err := storage.NormalizePageRequest(storage.PageRequest{Limit: limit, Cursor: cursor})
	if err != nil {
		return storage.PageRequest{}, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	return page, nil
}

func encodeAdminCursor(cursor storage.PageCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeAdminCursor(value string) (storage.PageCursor, error) {
	if value == "" {
		return storage.PageCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return storage.PageCursor{}, err
	}
	var cursor storage.PageCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return storage.PageCursor{}, err
	}
	return cursor, nil
}

func pageInfo[T any](page storage.Page[T], limit int) InventoryPage {
	result := InventoryPage{Limit: limit}
	if page.NextCursor != nil {
		result.HasMore = true
		result.NextCursor = encodeAdminCursor(*page.NextCursor)
	}
	return result
}
func writeInventoryMethod(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Allow", http.MethodGet)
	writeError(writer, request, errMethodNotAllowed)
}
