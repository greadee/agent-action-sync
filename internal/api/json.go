package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"syncgate/internal/storage"
)

const maxJSONBodyBytes int64 = 1 << 20

var (
	errBadRequest       = errors.New("bad request")
	errUnauthorized     = errors.New("unauthorized")
	errForbidden        = errors.New("forbidden")
	errNotFound         = errors.New("not found")
	errConflict         = errors.New("conflict")
	errUnavailable      = errors.New("unavailable")
	errPayloadTooLarge  = errors.New("payload too large")
	errMethodNotAllowed = errors.New("method not allowed")
)

type APIError struct {
	Status   int
	Code     string
	Message  string
	Internal error
}

func (apiError *APIError) Error() string {
	if apiError.Internal == nil {
		return apiError.Message
	}
	return fmt.Sprintf("%s: %v", apiError.Message, apiError.Internal)
}

func (apiError *APIError) Unwrap() error { return apiError.Internal }

func mapError(err error) *APIError {
	if err == nil {
		return nil
	}
	if apiError, ok := err.(*APIError); ok {
		return apiError
	}
	switch {
	case errors.Is(err, errBadRequest):
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_request", Message: "request is invalid", Internal: err}
	case errors.Is(err, errUnauthorized):
		return &APIError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "authentication is required", Internal: err}
	case errors.Is(err, errForbidden):
		return &APIError{Status: http.StatusForbidden, Code: "forbidden", Message: "request is not allowed", Internal: err}
	case errors.Is(err, errNotFound):
		return &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "resource was not found", Internal: err}
	case errors.Is(err, storage.ErrNotFound):
		return &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "resource was not found", Internal: err}
	case errors.Is(err, errConflict):
		return &APIError{Status: http.StatusConflict, Code: "conflict", Message: "request conflicts with current state", Internal: err}
	case errors.Is(err, ErrJobStateConflict):
		return &APIError{Status: http.StatusConflict, Code: "job_state_conflict", Message: "job cannot be controlled in its current state", Internal: err}
	case errors.Is(err, errUnavailable):
		return &APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "service is unavailable", Internal: err}
	case errors.Is(err, errPayloadTooLarge):
		return &APIError{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", Message: "request body is too large", Internal: err}
	case errors.Is(err, errMethodNotAllowed):
		return &APIError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: "method is not allowed", Internal: err}
	default:
		return &APIError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal server error", Internal: err}
	}
}

func requestID(request *http.Request) string {
	candidate := request.Header.Get("X-Request-ID")
	if len(candidate) > 0 && len(candidate) <= 128 && validRequestID(candidate) {
		return candidate
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

func prepareResponse(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Request-ID", requestID(request))
}

func writeJSON(writer http.ResponseWriter, request *http.Request, status int, value any) {
	prepareResponse(writer, request)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, request *http.Request, err error) {
	apiError := mapError(err)
	prepareResponse(writer, request)
	writer.WriteHeader(apiError.Status)
	_ = json.NewEncoder(writer).Encode(ErrorEnvelope{Error: ErrorBody{
		Code: apiError.Code, Message: sanitizeMessage(apiError.Message), RequestID: writer.Header().Get("X-Request-ID"),
	}})
}

func decodeJSON(request *http.Request, destination any) error {
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return fmt.Errorf("%w: content type must be application/json", errBadRequest)
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxJSONBodyBytes+1))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return errPayloadTooLarge
		}
		return fmt.Errorf("%w: read request body", errBadRequest)
	}
	if int64(len(body)) > maxJSONBodyBytes {
		return errPayloadTooLarge
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: malformed JSON", errBadRequest)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: body must contain one JSON value", errBadRequest)
	}
	if validator, ok := destination.(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return fmt.Errorf("%w: %v", errBadRequest, err)
		}
	}
	return nil
}

func methodHandler(method string, handler http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != method {
			writer.Header().Set("Allow", method)
			writeError(writer, request, errMethodNotAllowed)
			return
		}
		handler(writer, request)
	}
}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

func validRequestID(value string) bool { return requestIDPattern.MatchString(value) }

func sanitizeMessage(message string) string {
	message = strings.ReplaceAll(message, "\\", "/")
	for _, prefix := range []string{"C:/", "/Users/", "/home/", "/tmp/", "/var/"} {
		if index := strings.Index(message, prefix); index >= 0 {
			end := strings.IndexAny(message[index:], " ,;\n\r\t")
			if end < 0 {
				end = len(message) - index
			}
			message = message[:index] + "[redacted-path]" + message[index+end:]
		}
	}
	for _, key := range []string{"token=", "secret=", "password="} {
		lower := strings.ToLower(message)
		if index := strings.Index(lower, key); index >= 0 {
			end := strings.IndexAny(message[index+len(key):], " ,;\n\r\t")
			if end < 0 {
				end = len(message) - index - len(key)
			}
			message = message[:index] + key + "[redacted]" + message[index+len(key)+end:]
		}
	}
	return message
}
