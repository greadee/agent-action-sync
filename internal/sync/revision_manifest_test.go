package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"syncgate/internal/core"
)

func TestRevisionManifestFixturesDecodeAndRoundTrip(t *testing.T) {
	requestRaw := readManifestFixture(t, "revision_advertisement_request.json")
	request, err := DecodeRevisionAdvertisementRequest(requestRaw)
	if err != nil {
		t.Fatalf("DecodeRevisionAdvertisementRequest: %v", err)
	}
	if len(request.Revisions) != 2 || request.Revisions[0].RevisionID != "revision-100" {
		t.Fatalf("request = %+v", request)
	}
	encodedRequest, err := EncodeRevisionAdvertisementRequest(request)
	if err != nil {
		t.Fatalf("EncodeRevisionAdvertisementRequest: %v", err)
	}
	if _, err := DecodeRevisionAdvertisementRequest(encodedRequest); err != nil {
		t.Fatalf("round-trip request: %v", err)
	}

	responseRaw := readManifestFixture(t, "revision_advertisement_response.json")
	response, err := DecodeRevisionAdvertisementResponse(responseRaw)
	if err != nil {
		t.Fatalf("DecodeRevisionAdvertisementResponse: %v", err)
	}
	if response.Status != RevisionAdvertisementRejected || response.Error == nil || response.Error.Code != ProtocolErrorPolicyRejected {
		t.Fatalf("response = %+v", response)
	}
	if _, err := EncodeRevisionAdvertisementResponse(response); err != nil {
		t.Fatalf("EncodeRevisionAdvertisementResponse: %v", err)
	}

	changeRequestRaw := readManifestFixture(t, "one_way_change_request.json")
	changeRequest, err := DecodeOneWayChangeRequest(changeRequestRaw)
	if err != nil {
		t.Fatalf("DecodeOneWayChangeRequest: %v", err)
	}
	if changeRequest.Action != OneWayActionModify || changeRequest.ExpectedBaseRevisionID != "revision-099" {
		t.Fatalf("change request = %+v", changeRequest)
	}
	if _, err := EncodeOneWayChangeRequest(changeRequest); err != nil {
		t.Fatalf("EncodeOneWayChangeRequest: %v", err)
	}

	changeResponseRaw := readManifestFixture(t, "one_way_change_response.json")
	changeResponse, err := DecodeOneWayChangeResponse(changeResponseRaw)
	if err != nil {
		t.Fatalf("DecodeOneWayChangeResponse: %v", err)
	}
	if changeResponse.Status != OneWayChangeAccepted {
		t.Fatalf("change response = %+v", changeResponse)
	}
	if _, err := EncodeOneWayChangeResponse(changeResponse); err != nil {
		t.Fatalf("EncodeOneWayChangeResponse: %v", err)
	}
}

func TestRevisionAdvertisementRejectsMalformedInput(t *testing.T) {
	valid := validRevisionAdvertisementRequest()
	validRaw, err := EncodeRevisionAdvertisementRequest(valid)
	if err != nil {
		t.Fatalf("EncodeRevisionAdvertisementRequest: %v", err)
	}

	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "unknown field", raw: appendJSONField(t, validRaw, `"unexpected":true`)},
		{name: "trailing JSON", raw: append(validRaw, []byte(` {}`)...)},
		{name: "oversized raw", raw: make([]byte, MaxRevisionManifestBytes+1)},
		{name: "empty raw", raw: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeRevisionAdvertisementRequest(test.raw); err == nil {
				t.Fatal("expected malformed message to fail")
			}
		})
	}
}

func TestRevisionManifestValidationRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		change func(*RevisionAdvertisementRequest)
	}{
		{name: "unsupported version", change: func(request *RevisionAdvertisementRequest) { request.ProtocolVersion = 2 }},
		{name: "invalid identifier", change: func(request *RevisionAdvertisementRequest) { request.RequestID = "request id" }},
		{name: "entry count limit", change: func(request *RevisionAdvertisementRequest) { request.Limits.MaxEntries = 1 }},
		{name: "duplicate revision ID", change: func(request *RevisionAdvertisementRequest) {
			request.Revisions = append(request.Revisions, request.Revisions[0])
		}},
		{name: "noncanonical path", change: func(request *RevisionAdvertisementRequest) { request.Revisions[0].RelativePath = `docs\report.txt` }},
		{name: "path traversal", change: func(request *RevisionAdvertisementRequest) { request.Revisions[0].RelativePath = "../report.txt" }},
		{name: "unsupported entry", change: func(request *RevisionAdvertisementRequest) {
			request.Revisions[0].EntryType = core.EntrySymlinkUnsupported
		}},
		{name: "bad hash", change: func(request *RevisionAdvertisementRequest) { request.Revisions[0].ContentHash = "not-a-hash" }},
		{name: "invalid deletion metadata", change: func(request *RevisionAdvertisementRequest) { request.Revisions[1].IsDeleted = false }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validRevisionAdvertisementRequest()
			test.change(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("expected invalid request to fail")
			}
		})
	}
}

func TestRevisionManifestEnforcesAdvertisedByteLimit(t *testing.T) {
	request := validRevisionAdvertisementRequest()
	request.Limits.MaxBytes = 1
	if _, err := EncodeRevisionAdvertisementRequest(request); err == nil {
		t.Fatal("expected byte limit error")
	}
}

func TestResponsesRequireConsistentStructuredErrors(t *testing.T) {
	advertisement := RevisionAdvertisementResponse{
		ProtocolVersion:   RevisionManifestProtocolVersion,
		RequestID:         "request-1",
		ResponderDeviceID: "DEVICE-2",
		ShareID:           "share-1",
		Limits:            RevisionManifestLimits{MaxEntries: 2, MaxBytes: 4096},
		Status:            RevisionAdvertisementRejected,
	}
	if err := advertisement.Validate(); err == nil {
		t.Fatal("expected rejected advertisement without error to fail")
	}
	advertisement.Error = &ProtocolError{Code: ProtocolErrorPolicyRejected, Message: "target drift", Retryable: false}
	if err := advertisement.Validate(); err != nil {
		t.Fatalf("advertisement.Validate: %v", err)
	}
	advertisement.AcceptedRevisionIDs = []core.RevisionID{"revision-1"}
	if err := advertisement.Validate(); err == nil {
		t.Fatal("expected rejected advertisement with accepted revisions to fail")
	}
	advertisement.AcceptedRevisionIDs = nil
	advertisement.Status = RevisionAdvertisementAccepted
	if err := advertisement.Validate(); err == nil {
		t.Fatal("expected accepted advertisement with error to fail")
	}

	change := validOneWayChangeResponse()
	change.Status = OneWayChangeRejected
	if err := change.Validate(); err == nil {
		t.Fatal("expected rejected change without error to fail")
	}
	change.Error = &ProtocolError{Code: ProtocolErrorUnauthorized, Message: "delete capability is required", Retryable: false}
	if err := change.Validate(); err != nil {
		t.Fatalf("change.Validate: %v", err)
	}
	change.Error.Code = "unknown"
	if err := change.Validate(); err == nil {
		t.Fatal("expected unknown error code to fail")
	}
}

func TestOneWayChangeMessagesRejectMalformedFields(t *testing.T) {
	request := validOneWayChangeRequest()
	request.Action = "rename"
	if err := request.Validate(); err == nil {
		t.Fatal("expected unsupported action to fail")
	}
	request = validOneWayChangeRequest()
	request.RevisionID = "revision id"
	if err := request.Validate(); err == nil {
		t.Fatal("expected invalid revision ID to fail")
	}

	response := validOneWayChangeResponse()
	response.Status = "prepared"
	if err := response.Validate(); err == nil {
		t.Fatal("expected unsupported response status to fail")
	}
}

func TestOneWayChangeDecodersRejectMalformedJSON(t *testing.T) {
	requestRaw, err := EncodeOneWayChangeRequest(validOneWayChangeRequest())
	if err != nil {
		t.Fatalf("EncodeOneWayChangeRequest: %v", err)
	}
	responseRaw, err := EncodeOneWayChangeResponse(validOneWayChangeResponse())
	if err != nil {
		t.Fatalf("EncodeOneWayChangeResponse: %v", err)
	}

	tests := []struct {
		name   string
		decode func([]byte) error
		raw    []byte
	}{
		{
			name: "request unknown field",
			decode: func(raw []byte) error {
				_, err := DecodeOneWayChangeRequest(raw)
				return err
			},
			raw: appendJSONField(t, requestRaw, `"unexpected":true`),
		},
		{
			name: "response trailing JSON",
			decode: func(raw []byte) error {
				_, err := DecodeOneWayChangeResponse(raw)
				return err
			},
			raw: append(responseRaw, []byte(` {}`)...),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.decode(test.raw); err == nil {
				t.Fatal("expected malformed message to fail")
			}
		})
	}
}

func validRevisionAdvertisementRequest() RevisionAdvertisementRequest {
	return RevisionAdvertisementRequest{
		ProtocolVersion: RevisionManifestProtocolVersion,
		RequestID:       "request-1",
		SourceDeviceID:  "DEVICE-1",
		TargetDeviceID:  "DEVICE-2",
		ShareID:         "share-1",
		Limits:          RevisionManifestLimits{MaxEntries: 4, MaxBytes: 4096},
		Revisions: []RevisionManifestEntry{
			{
				RevisionID:    "revision-1",
				RelativePath:  "docs/report.txt",
				EntryType:     core.EntryFile,
				Size:          5,
				ContentHash:   strings.Repeat("a", 64),
				HashAlgorithm: "sha256",
				Sequence:      1,
			},
			{
				RevisionID:       "revision-2",
				ParentRevisionID: "revision-1",
				RelativePath:     "docs/obsolete.txt",
				EntryType:        core.EntryDeleted,
				Sequence:         2,
				IsDeleted:        true,
			},
		},
	}
}

func validOneWayChangeRequest() OneWayChangeRequest {
	return OneWayChangeRequest{
		ProtocolVersion:        RevisionManifestProtocolVersion,
		RequestID:              "request-2",
		SourceDeviceID:         "DEVICE-1",
		TargetDeviceID:         "DEVICE-2",
		ShareID:                "share-1",
		RevisionID:             "revision-1",
		ExpectedBaseRevisionID: "revision-0",
		Action:                 OneWayActionModify,
	}
}

func validOneWayChangeResponse() OneWayChangeResponse {
	return OneWayChangeResponse{
		ProtocolVersion:   RevisionManifestProtocolVersion,
		RequestID:         "request-2",
		ResponderDeviceID: "DEVICE-2",
		ShareID:           "share-1",
		RevisionID:        "revision-1",
		Status:            OneWayChangeAccepted,
	}
}

func readManifestFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

func appendJSONField(t *testing.T, raw []byte, field string) []byte {
	t.Helper()
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	fieldParts := strings.SplitN(field, ":", 2)
	if len(fieldParts) != 2 {
		t.Fatalf("invalid field %q", field)
	}
	value[strings.Trim(fieldParts[0], `"`)] = json.RawMessage(fieldParts[1])
	updated, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return updated
}
