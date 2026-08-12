package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMapErrorStatusAndStableMessages(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "bad request", err: errBadRequest, status: 400, code: "invalid_request"},
		{name: "unauthorized", err: errUnauthorized, status: 401, code: "unauthorized"},
		{name: "not found", err: errNotFound, status: 404, code: "not_found"},
		{name: "conflict", err: errConflict, status: 409, code: "conflict"},
		{name: "too large", err: errPayloadTooLarge, status: 413, code: "payload_too_large"},
		{name: "method", err: errMethodNotAllowed, status: 405, code: "method_not_allowed"},
		{name: "unavailable", err: errUnavailable, status: 503, code: "unavailable"},
		{name: "unknown", err: errors.New("database secret=/tmp/private.sqlite"), status: 500, code: "internal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := mapError(test.err)
			if mapped.Status != test.status || mapped.Code != test.code {
				t.Fatalf("mapError = %#v, want status %d code %q", mapped, test.status, test.code)
			}
			if strings.Contains(mapped.Message, "secret") || strings.Contains(mapped.Message, "sqlite") {
				t.Fatalf("internal detail leaked: %q", mapped.Message)
			}
		})
	}
}

func TestDecodeJSONRejectsMalformedTrailingUnknownAndOversizedBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{name: "malformed", body: `{"action":`, want: errBadRequest},
		{name: "trailing", body: `{"action":"pause"} {}`, want: errBadRequest},
		{name: "unknown field", body: `{"action":"pause","unexpected":true}`, want: errBadRequest},
		{name: "oversized", body: strings.Repeat("x", int(maxJSONBodyBytes)+1), want: errPayloadTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			var destination JobAction
			err := decodeJSON(request, &destination)
			if !errors.Is(err, test.want) {
				t.Fatalf("decodeJSON error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeJSONRequiresJSONAndRejectsInvalidEnums(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/problem+json"} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"action":"pause"}`))
		request.Header.Set("Content-Type", contentType)
		if err := decodeJSON(request, &JobAction{}); !errors.Is(err, errBadRequest) {
			t.Errorf("content type %q returned %v", contentType, err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"action":"erase"}`))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	if err := decodeJSON(request, &JobAction{}); !errors.Is(err, errBadRequest) {
		t.Fatalf("invalid enum returned %v", err)
	}
}

func TestResponseHeadersAndRequestID(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", "trace-123")
	recorder := httptest.NewRecorder()
	writeJSON(recorder, request, http.StatusOK, map[string]string{"status": "ok"})
	if recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Request-ID") != "trace-123" {
		t.Fatalf("headers = %#v", recorder.Header())
	}
}

func TestWriteErrorSanitizesDetailsAndMethodHandler(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	writeError(recorder, request, &APIError{Status: 400, Code: "invalid_request", Message: `open C:\Users\owner\secret.txt token=topsecret`})
	body := recorder.Body.String()
	if strings.Contains(body, "Users/owner") || strings.Contains(body, "topsecret") {
		t.Fatalf("sensitive detail leaked: %s", body)
	}
	if !strings.Contains(body, "[redacted-path]") || !strings.Contains(body, "[redacted]") {
		t.Fatalf("expected redaction markers: %s", body)
	}

	recorder = httptest.NewRecorder()
	methodHandler(http.MethodPost, func(http.ResponseWriter, *http.Request) {})(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("method response = %d headers %#v", recorder.Code, recorder.Header())
	}
}

func TestInvitationDTOValidation(t *testing.T) {
	if err := (InvitationRequest{TTLSeconds: 0}).Validate(); err == nil {
		t.Fatal("expected invalid invitation TTL")
	}
	if err := (EncodedInvitation{Invitation: strings.Repeat("x", 65537)}).Validate(); err == nil {
		t.Fatal("expected oversized invitation rejection")
	}
}
