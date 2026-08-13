package api

import (
	"context"
	"net/http"
	"strings"

	"syncgate/internal/core"
)

type ScanAccepted struct {
	Accepted bool   `json:"accepted"`
	ShareID  string `json:"share_id"`
}

type ScanRequester interface {
	RequestScan(ctx context.Context, shareID core.ShareID) error
}

func (service *LocalAdministrationService) RequestScan(ctx context.Context, shareID core.ShareID) error {
	if err := validateServiceContext(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(string(shareID)) == "" || strings.ContainsAny(string(shareID), "/\\") {
		return &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "share was not found"}
	}
	if service.scan == nil {
		return errUnavailable
	}
	return service.scan(ctx, shareID)
}

func NewScanHandler(requester ScanRequester) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeError(writer, request, errMethodNotAllowed)
			return
		}
		shareID := strings.TrimPrefix(request.URL.Path, "/api/v1/shares/")
		shareID = strings.TrimSuffix(shareID, "/scans")
		if !strings.HasSuffix(request.URL.Path, "/scans") || shareID == "" || strings.ContainsAny(shareID, "/\\") {
			writeError(writer, request, errNotFound)
			return
		}
		if requester == nil {
			writeError(writer, request, errUnavailable)
			return
		}
		if err := requester.RequestScan(request.Context(), core.ShareID(shareID)); err != nil {
			writeError(writer, request, err)
			return
		}
		writeJSON(writer, request, http.StatusAccepted, ScanAccepted{Accepted: true, ShareID: shareID})
	})
}
