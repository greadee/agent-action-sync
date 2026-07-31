package sync

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"syncgate/internal/core"
	"syncgate/internal/filesystem"
)

const (
	RevisionManifestProtocolVersion = 1
	MaxRevisionManifestEntries      = 1024
	MaxRevisionManifestBytes        = 1024 * 1024
	maxProtocolIdentifierBytes      = 128
	maxProtocolErrorMessageBytes    = 512
)

type RevisionManifestLimits struct {
	MaxEntries int `json:"max_entries"`
	MaxBytes   int `json:"max_bytes"`
}

type RevisionManifestEntry struct {
	RevisionID       core.RevisionID `json:"revision_id"`
	ParentRevisionID core.RevisionID `json:"parent_revision_id,omitempty"`
	RelativePath     string          `json:"relative_path"`
	EntryType        core.EntryType  `json:"entry_type"`
	Size             int64           `json:"size"`
	ContentHash      string          `json:"content_hash,omitempty"`
	HashAlgorithm    string          `json:"hash_algorithm,omitempty"`
	Sequence         int64           `json:"sequence"`
	IsDeleted        bool            `json:"is_deleted"`
}

// RevisionAdvertisementRequest advertises authoritative source revisions. It
// is a transport-neutral value: framing and peer authentication are owned by
// the caller's authenticated session.
type RevisionAdvertisementRequest struct {
	ProtocolVersion int                     `json:"protocol_version"`
	RequestID       string                  `json:"request_id"`
	SourceDeviceID  core.DeviceID           `json:"source_device_id"`
	TargetDeviceID  core.DeviceID           `json:"target_device_id"`
	ShareID         core.ShareID            `json:"share_id"`
	Limits          RevisionManifestLimits  `json:"limits"`
	Revisions       []RevisionManifestEntry `json:"revisions"`
}

type RevisionAdvertisementStatus string

const (
	RevisionAdvertisementAccepted RevisionAdvertisementStatus = "accepted"
	RevisionAdvertisementRejected RevisionAdvertisementStatus = "rejected"
)

type RevisionAdvertisementResponse struct {
	ProtocolVersion     int                         `json:"protocol_version"`
	RequestID           string                      `json:"request_id"`
	ResponderDeviceID   core.DeviceID               `json:"responder_device_id"`
	ShareID             core.ShareID                `json:"share_id"`
	Limits              RevisionManifestLimits      `json:"limits"`
	Status              RevisionAdvertisementStatus `json:"status"`
	AcceptedRevisionIDs []core.RevisionID           `json:"accepted_revision_ids,omitempty"`
	Error               *ProtocolError              `json:"error,omitempty"`
}

// OneWayChangeRequest asks a target to prepare exactly one previously
// advertised authoritative revision. It does not authorize or apply it.
type OneWayChangeRequest struct {
	ProtocolVersion        int             `json:"protocol_version"`
	RequestID              string          `json:"request_id"`
	SourceDeviceID         core.DeviceID   `json:"source_device_id"`
	TargetDeviceID         core.DeviceID   `json:"target_device_id"`
	ShareID                core.ShareID    `json:"share_id"`
	RevisionID             core.RevisionID `json:"revision_id"`
	ExpectedBaseRevisionID core.RevisionID `json:"expected_base_revision_id,omitempty"`
	Action                 OneWayAction    `json:"action"`
}

type OneWayChangeStatus string

const (
	OneWayChangeAccepted  OneWayChangeStatus = "accepted"
	OneWayChangeRejected  OneWayChangeStatus = "rejected"
	OneWayChangeDuplicate OneWayChangeStatus = "duplicate"
)

type OneWayChangeResponse struct {
	ProtocolVersion   int                `json:"protocol_version"`
	RequestID         string             `json:"request_id"`
	ResponderDeviceID core.DeviceID      `json:"responder_device_id"`
	ShareID           core.ShareID       `json:"share_id"`
	RevisionID        core.RevisionID    `json:"revision_id"`
	Status            OneWayChangeStatus `json:"status"`
	Error             *ProtocolError     `json:"error,omitempty"`
}

type ProtocolErrorCode string

const (
	ProtocolErrorInvalidRequest     ProtocolErrorCode = "invalid_request"
	ProtocolErrorUnsupportedVersion ProtocolErrorCode = "unsupported_version"
	ProtocolErrorLimitExceeded      ProtocolErrorCode = "limit_exceeded"
	ProtocolErrorUnauthorized       ProtocolErrorCode = "unauthorized"
	ProtocolErrorPolicyRejected     ProtocolErrorCode = "policy_rejected"
	ProtocolErrorInternal           ProtocolErrorCode = "internal"
)

// ProtocolError is a stable, machine-readable rejection. It must never carry
// filesystem paths, credentials, or other implementation details.
type ProtocolError struct {
	Code      ProtocolErrorCode `json:"code"`
	Message   string            `json:"message"`
	Retryable bool              `json:"retryable"`
}

func (request RevisionAdvertisementRequest) Validate() error {
	if err := validateRequestEnvelope(request.ProtocolVersion, request.RequestID, request.SourceDeviceID, request.TargetDeviceID, request.ShareID); err != nil {
		return err
	}
	if err := request.Limits.Validate(); err != nil {
		return fmt.Errorf("manifest limits: %w", err)
	}
	if len(request.Revisions) == 0 {
		return fmt.Errorf("revision advertisement must contain at least one revision")
	}
	if len(request.Revisions) > request.Limits.MaxEntries {
		return fmt.Errorf("revision count %d exceeds advertised limit %d", len(request.Revisions), request.Limits.MaxEntries)
	}
	seen := make(map[core.RevisionID]struct{}, len(request.Revisions))
	for index, revision := range request.Revisions {
		if err := revision.Validate(); err != nil {
			return fmt.Errorf("revision %d: %w", index, err)
		}
		if _, exists := seen[revision.RevisionID]; exists {
			return fmt.Errorf("revision %d duplicates revision ID %q", index, revision.RevisionID)
		}
		seen[revision.RevisionID] = struct{}{}
	}
	return nil
}

func (response RevisionAdvertisementResponse) Validate() error {
	if err := validateResponseEnvelope(response.ProtocolVersion, response.RequestID, response.ResponderDeviceID, response.ShareID); err != nil {
		return err
	}
	if err := response.Limits.Validate(); err != nil {
		return fmt.Errorf("manifest limits: %w", err)
	}
	if len(response.AcceptedRevisionIDs) > response.Limits.MaxEntries {
		return fmt.Errorf("accepted revision count %d exceeds advertised limit %d", len(response.AcceptedRevisionIDs), response.Limits.MaxEntries)
	}
	if err := validateRevisionIDs(response.AcceptedRevisionIDs); err != nil {
		return fmt.Errorf("accepted revisions: %w", err)
	}
	switch response.Status {
	case RevisionAdvertisementAccepted:
		if response.Error != nil {
			return fmt.Errorf("accepted revision advertisement cannot include an error")
		}
	case RevisionAdvertisementRejected:
		if len(response.AcceptedRevisionIDs) != 0 {
			return fmt.Errorf("rejected revision advertisement cannot include accepted revisions")
		}
		if err := validateRequiredProtocolError(response.Error); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported revision advertisement status %q", response.Status)
	}
	return nil
}

func (request OneWayChangeRequest) Validate() error {
	if err := validateRequestEnvelope(request.ProtocolVersion, request.RequestID, request.SourceDeviceID, request.TargetDeviceID, request.ShareID); err != nil {
		return err
	}
	if err := validateRequiredIdentifier("revision ID", string(request.RevisionID)); err != nil {
		return err
	}
	if request.ExpectedBaseRevisionID != "" {
		if err := validateRequiredIdentifier("expected base revision ID", string(request.ExpectedBaseRevisionID)); err != nil {
			return err
		}
	}
	if _, err := oneWayActionCapability(request.Action); err != nil {
		return err
	}
	return nil
}

func (response OneWayChangeResponse) Validate() error {
	if err := validateResponseEnvelope(response.ProtocolVersion, response.RequestID, response.ResponderDeviceID, response.ShareID); err != nil {
		return err
	}
	if err := validateRequiredIdentifier("revision ID", string(response.RevisionID)); err != nil {
		return err
	}
	switch response.Status {
	case OneWayChangeAccepted, OneWayChangeDuplicate:
		if response.Error != nil {
			return fmt.Errorf("%s one-way change cannot include an error", response.Status)
		}
	case OneWayChangeRejected:
		if err := validateRequiredProtocolError(response.Error); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported one-way change status %q", response.Status)
	}
	return nil
}

func (limits RevisionManifestLimits) Validate() error {
	if limits.MaxEntries < 1 || limits.MaxEntries > MaxRevisionManifestEntries {
		return fmt.Errorf("max entries must be between 1 and %d", MaxRevisionManifestEntries)
	}
	if limits.MaxBytes < 1 || limits.MaxBytes > MaxRevisionManifestBytes {
		return fmt.Errorf("max bytes must be between 1 and %d", MaxRevisionManifestBytes)
	}
	return nil
}

func (entry RevisionManifestEntry) Validate() error {
	if err := validateRequiredIdentifier("revision ID", string(entry.RevisionID)); err != nil {
		return err
	}
	if entry.ParentRevisionID != "" {
		if err := validateRequiredIdentifier("parent revision ID", string(entry.ParentRevisionID)); err != nil {
			return err
		}
	}
	normalizedPath, err := filesystem.NormalizeRelativePath(entry.RelativePath)
	if err != nil {
		return fmt.Errorf("relative path: %w", err)
	}
	if normalizedPath != entry.RelativePath {
		return fmt.Errorf("relative path must already be normalized: %q", entry.RelativePath)
	}
	if entry.Sequence < 1 {
		return fmt.Errorf("sequence must be positive")
	}
	switch entry.EntryType {
	case core.EntryFile:
		if entry.IsDeleted || entry.Size < 0 {
			return fmt.Errorf("file revision has invalid deletion state or size")
		}
		if entry.HashAlgorithm != "sha256" {
			return fmt.Errorf("file revision has unsupported hash algorithm %q", entry.HashAlgorithm)
		}
		if err := validateSHA256(entry.ContentHash); err != nil {
			return fmt.Errorf("file revision content hash: %w", err)
		}
	case core.EntryDirectory:
		if entry.IsDeleted || entry.Size != 0 || entry.ContentHash != "" || entry.HashAlgorithm != "" {
			return fmt.Errorf("directory revision cannot include deletion or content metadata")
		}
	case core.EntryDeleted:
		if !entry.IsDeleted || entry.Size != 0 || entry.ContentHash != "" || entry.HashAlgorithm != "" {
			return fmt.Errorf("deletion revision has invalid metadata")
		}
	default:
		return fmt.Errorf("unsupported revision entry type %q", entry.EntryType)
	}
	return nil
}

func (protocolError ProtocolError) Validate() error {
	switch protocolError.Code {
	case ProtocolErrorInvalidRequest, ProtocolErrorUnsupportedVersion, ProtocolErrorLimitExceeded, ProtocolErrorUnauthorized, ProtocolErrorPolicyRejected, ProtocolErrorInternal:
	default:
		return fmt.Errorf("unsupported protocol error code %q", protocolError.Code)
	}
	if protocolError.Message == "" || strings.TrimSpace(protocolError.Message) != protocolError.Message || len(protocolError.Message) > maxProtocolErrorMessageBytes {
		return fmt.Errorf("protocol error message must be trimmed and no more than %d bytes", maxProtocolErrorMessageBytes)
	}
	return nil
}

func EncodeRevisionAdvertisementRequest(request RevisionAdvertisementRequest) ([]byte, error) {
	return encodeProtocolJSON(request, request.Limits)
}

func DecodeRevisionAdvertisementRequest(raw []byte) (RevisionAdvertisementRequest, error) {
	var request RevisionAdvertisementRequest
	if err := decodeProtocolJSON(raw, &request); err != nil {
		return RevisionAdvertisementRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return RevisionAdvertisementRequest{}, err
	}
	if err := validateManifestSize(raw, request.Limits); err != nil {
		return RevisionAdvertisementRequest{}, err
	}
	return request, nil
}

func EncodeRevisionAdvertisementResponse(response RevisionAdvertisementResponse) ([]byte, error) {
	return encodeProtocolJSON(response, response.Limits)
}

func DecodeRevisionAdvertisementResponse(raw []byte) (RevisionAdvertisementResponse, error) {
	var response RevisionAdvertisementResponse
	if err := decodeProtocolJSON(raw, &response); err != nil {
		return RevisionAdvertisementResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return RevisionAdvertisementResponse{}, err
	}
	if err := validateManifestSize(raw, response.Limits); err != nil {
		return RevisionAdvertisementResponse{}, err
	}
	return response, nil
}

func EncodeOneWayChangeRequest(request OneWayChangeRequest) ([]byte, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return encodeProtocolJSONUnchecked(request)
}

func DecodeOneWayChangeRequest(raw []byte) (OneWayChangeRequest, error) {
	var request OneWayChangeRequest
	if err := decodeProtocolJSON(raw, &request); err != nil {
		return OneWayChangeRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return OneWayChangeRequest{}, err
	}
	return request, nil
}

func EncodeOneWayChangeResponse(response OneWayChangeResponse) ([]byte, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	return encodeProtocolJSONUnchecked(response)
}

func DecodeOneWayChangeResponse(raw []byte) (OneWayChangeResponse, error) {
	var response OneWayChangeResponse
	if err := decodeProtocolJSON(raw, &response); err != nil {
		return OneWayChangeResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return OneWayChangeResponse{}, err
	}
	return response, nil
}

func encodeProtocolJSON(message interface{ Validate() error }, limits RevisionManifestLimits) ([]byte, error) {
	if err := message.Validate(); err != nil {
		return nil, err
	}
	raw, err := encodeProtocolJSONUnchecked(message)
	if err != nil {
		return nil, err
	}
	if err := validateManifestSize(raw, limits); err != nil {
		return nil, err
	}
	return raw, nil
}

func encodeProtocolJSONUnchecked(message any) ([]byte, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal revision manifest protocol message: %w", err)
	}
	if len(raw) > MaxRevisionManifestBytes {
		return nil, fmt.Errorf("revision manifest protocol message exceeds %d bytes", MaxRevisionManifestBytes)
	}
	return raw, nil
}

func decodeProtocolJSON(raw []byte, destination any) error {
	if len(raw) == 0 || len(raw) > MaxRevisionManifestBytes {
		return fmt.Errorf("revision manifest protocol message has invalid size %d", len(raw))
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode revision manifest protocol message: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("revision manifest protocol message must contain one JSON value")
	}
	return nil
}

func validateManifestSize(raw []byte, limits RevisionManifestLimits) error {
	if len(raw) > limits.MaxBytes {
		return fmt.Errorf("revision manifest protocol message size %d exceeds advertised limit %d", len(raw), limits.MaxBytes)
	}
	return nil
}

func validateRequestEnvelope(version int, requestID string, sourceDeviceID, targetDeviceID core.DeviceID, shareID core.ShareID) error {
	if version != RevisionManifestProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", version)
	}
	if err := validateRequiredIdentifier("request ID", requestID); err != nil {
		return err
	}
	if err := validateRequiredIdentifier("source device ID", string(sourceDeviceID)); err != nil {
		return err
	}
	if err := validateRequiredIdentifier("target device ID", string(targetDeviceID)); err != nil {
		return err
	}
	return validateRequiredIdentifier("share ID", string(shareID))
}

func validateResponseEnvelope(version int, requestID string, responderDeviceID core.DeviceID, shareID core.ShareID) error {
	if version != RevisionManifestProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", version)
	}
	if err := validateRequiredIdentifier("request ID", requestID); err != nil {
		return err
	}
	if err := validateRequiredIdentifier("responder device ID", string(responderDeviceID)); err != nil {
		return err
	}
	return validateRequiredIdentifier("share ID", string(shareID))
}

func validateRequiredProtocolError(protocolError *ProtocolError) error {
	if protocolError == nil {
		return fmt.Errorf("rejected response requires a structured error")
	}
	return protocolError.Validate()
}

func validateRevisionIDs(ids []core.RevisionID) error {
	seen := make(map[core.RevisionID]struct{}, len(ids))
	for _, id := range ids {
		if err := validateRequiredIdentifier("revision ID", string(id)); err != nil {
			return err
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate revision ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateRequiredIdentifier(name, value string) error {
	if value == "" || len(value) > maxProtocolIdentifierBytes || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be trimmed and no more than %d bytes", name, maxProtocolIdentifierBytes)
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return fmt.Errorf("%s contains unsupported character %q", name, character)
	}
	return nil
}

func validateSHA256(value string) error {
	if len(value) != 64 || strings.ToLower(value) != value {
		return fmt.Errorf("must be a lower-case SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("must be hexadecimal: %w", err)
	}
	return nil
}
