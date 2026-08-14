package project

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxJSONNestingDepth = 32

var canonicalIntegerPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// DecodedRecord preserves the canonical input bytes, including compatible
// unknown additive fields, while exposing the supported typed contract.
type DecodedRecord struct {
	Value     any
	Canonical []byte
	Digest    string
}

// MarshalRecord validates a supported record, calculates its integrity over
// canonical JSON without the top-level integrity field, and returns canonical
// JSON containing the resulting SHA-256 digest.
func MarshalRecord(record any) ([]byte, error) {
	if err := validateRecord(record, false); err != nil {
		return nil, fmt.Errorf("validate project record: %w", err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal project record: %w", err)
	}
	object, err := decodeJSONObject(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal project record: %w", err)
	}
	delete(object, "integrity")
	digest, err := digestJSONObject(object)
	if err != nil {
		return nil, err
	}
	object["integrity"] = map[string]any{"algorithm": HashAlgorithmSHA256, "digest": digest}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical project record: %w", err)
	}
	if len(canonical) > MaxRecordBytes {
		return nil, fmt.Errorf("project record exceeds %d bytes", MaxRecordBytes)
	}
	return canonical, nil
}

// DecodeRecord validates one complete portable record. Compatible unknown
// top-level fields are retained in Canonical and included in integrity checks.
func DecodeRecord(raw []byte) (DecodedRecord, error) {
	if len(raw) == 0 {
		return DecodedRecord{}, errors.New("project record is empty")
	}
	if len(raw) > MaxRecordBytes {
		return DecodedRecord{}, fmt.Errorf("project record exceeds %d bytes", MaxRecordBytes)
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return DecodedRecord{}, fmt.Errorf("decode project record: %w", err)
	}
	object, err := decodeJSONObject(raw)
	if err != nil {
		return DecodedRecord{}, fmt.Errorf("decode project record: %w", err)
	}

	canonical, err := json.Marshal(object)
	if err != nil {
		return DecodedRecord{}, fmt.Errorf("canonicalize project record: %w", err)
	}
	var header RecordHeader
	if err := json.Unmarshal(canonical, &header); err != nil {
		return DecodedRecord{}, fmt.Errorf("decode project record header: %w", err)
	}
	if header.Integrity.Algorithm != HashAlgorithmSHA256 || !validSHA256(header.Integrity.Digest) {
		return DecodedRecord{}, errors.New("record integrity must be a lowercase SHA-256 digest")
	}
	if err := validateRequiredRecordFields(header.RecordKind, object); err != nil {
		return DecodedRecord{}, err
	}

	digestObject := cloneJSONObject(object)
	delete(digestObject, "integrity")
	digest, err := digestJSONObject(digestObject)
	if err != nil {
		return DecodedRecord{}, err
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(header.Integrity.Digest)) != 1 {
		return DecodedRecord{}, errors.New("project record integrity mismatch")
	}

	value, err := decodeTypedRecord(header.RecordKind, canonical)
	if err != nil {
		return DecodedRecord{}, err
	}
	if err := validateRecord(value, true); err != nil {
		return DecodedRecord{}, fmt.Errorf("validate project record: %w", err)
	}
	if err := validateCanonicalRecordTimes(value, object); err != nil {
		return DecodedRecord{}, err
	}
	return DecodedRecord{Value: value, Canonical: canonical, Digest: digest}, nil
}

func validateRequiredRecordFields(kind RecordKind, object map[string]any) error {
	if err := requireJSONFields(object, "record", "schema", "record_kind", "record_id", "project_id", "integrity"); err != nil {
		return err
	}
	schema, err := requireJSONObject(object, "schema")
	if err != nil {
		return err
	}
	if err := requireJSONFields(schema, "schema", "family", "major", "minor"); err != nil {
		return err
	}
	integrity, err := requireJSONObject(object, "integrity")
	if err != nil {
		return err
	}
	if err := requireJSONFields(integrity, "integrity", "algorithm", "digest"); err != nil {
		return err
	}

	requireProvenance := func() error {
		provenance, err := requireJSONObject(object, "provenance")
		if err != nil {
			return err
		}
		if err := requireJSONFields(provenance, "provenance", "producer", "created_at"); err != nil {
			return err
		}
		producer, err := requireJSONObject(provenance, "producer")
		if err != nil {
			return err
		}
		return requireJSONFields(producer, "provenance.producer", "device_id")
	}
	requireProducer := func() error {
		producer, err := requireJSONObject(object, "producer")
		if err != nil {
			return err
		}
		return requireJSONFields(producer, "producer", "device_id")
	}

	switch kind {
	case RecordProjectManifest:
		if err := requireJSONFields(object, "project_manifest", "name", "authority", "created_at"); err != nil {
			return err
		}
		authority, err := requireJSONObject(object, "authority")
		if err != nil {
			return err
		}
		return requireJSONFields(authority, "authority", "device_id", "share_id")
	case RecordWorkPackage:
		if err := requireJSONFields(object, "work_package_definition", "work_package_id", "objective", "trade", "scope", "deliverables", "acceptance_criteria", "review_required", "created_at", "provenance"); err != nil {
			return err
		}
		if _, err := requireJSONObject(object, "scope"); err != nil {
			return err
		}
		return requireProvenance()
	case RecordExecution:
		if err := requireJSONFields(object, "execution_manifest", "execution_id", "work_package_id", "state", "producer", "created_at", "provenance"); err != nil {
			return err
		}
		if err := requireProducer(); err != nil {
			return err
		}
		return requireProvenance()
	case RecordWorkEvent:
		if err := requireJSONFields(object, "work_event", "event_type", "occurred_at", "producer", "payload"); err != nil {
			return err
		}
		return requireProducer()
	case RecordHandoff:
		if err := requireJSONFields(object, "handoff", "handoff_id", "work_package_id", "execution_id", "completed_work", "confidence", "created_at", "provenance"); err != nil {
			return err
		}
		return requireProvenance()
	case RecordArtifact:
		if err := requireJSONFields(object, "artifact_manifest", "artifact_id", "name", "media_type", "size", "hash_algorithm", "content_hash", "created_at", "provenance"); err != nil {
			return err
		}
		return requireProvenance()
	default:
		return fmt.Errorf("unsupported project record kind %q", kind)
	}
}

func requireJSONFields(object map[string]any, name string, fields ...string) error {
	for _, field := range fields {
		if _, exists := object[field]; !exists {
			return fmt.Errorf("%s is missing required field %q", name, field)
		}
	}
	return nil
}

func requireJSONObject(parent map[string]any, field string) (map[string]any, error) {
	value, exists := parent[field]
	if !exists {
		return nil, fmt.Errorf("record is missing required field %q", field)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a JSON object", field)
	}
	return object, nil
}

// DecodeRecordForProject additionally binds a portable record to the manifest
// identity selected by its caller.
func DecodeRecordForProject(raw []byte, projectID string) (DecodedRecord, error) {
	if err := validateIdentifier("expected project_id", projectID, true); err != nil {
		return DecodedRecord{}, err
	}
	decoded, err := DecodeRecord(raw)
	if err != nil {
		return DecodedRecord{}, err
	}
	var header RecordHeader
	if err := json.Unmarshal(decoded.Canonical, &header); err != nil {
		return DecodedRecord{}, fmt.Errorf("decode project record header: %w", err)
	}
	if header.ProjectID != projectID {
		return DecodedRecord{}, fmt.Errorf("project record belongs to %q, expected %q", header.ProjectID, projectID)
	}
	return decoded, nil
}

func decodeTypedRecord(kind RecordKind, raw []byte) (any, error) {
	var destination any
	switch kind {
	case RecordProjectManifest:
		destination = &ProjectManifest{}
	case RecordWorkPackage:
		destination = &WorkPackageDefinition{}
	case RecordExecution:
		destination = &ExecutionManifest{}
	case RecordWorkEvent:
		destination = &WorkEvent{}
	case RecordHandoff:
		destination = &Handoff{}
	case RecordArtifact:
		destination = &ArtifactManifest{}
	default:
		return nil, fmt.Errorf("unsupported project record kind %q", kind)
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return nil, fmt.Errorf("decode %s: %w", kind, err)
	}
	return destination, nil
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("project record must be a JSON object")
	}
	if err := validateCanonicalJSONValue(object, 0); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("project record must contain one JSON value")
		}
		return nil, fmt.Errorf("project record has trailing data: %w", err)
	}
	return object, nil
}

func validateCanonicalJSONValue(value any, depth int) error {
	if depth > maxJSONNestingDepth {
		return fmt.Errorf("project record exceeds JSON nesting depth %d", maxJSONNestingDepth)
	}
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > MaxListItems {
			return fmt.Errorf("JSON object has more than %d fields", MaxListItems)
		}
		for key, child := range typed {
			if !utf8.ValidString(key) || strings.ContainsRune(key, 0) || len(key) == 0 || len(key) > MaxIdentifierBytes {
				return fmt.Errorf("JSON object key must contain 1 to %d valid UTF-8 bytes", MaxIdentifierBytes)
			}
			if err := validateCanonicalJSONValue(child, depth+1); err != nil {
				return fmt.Errorf("field %q: %w", key, err)
			}
		}
	case []any:
		if len(typed) > MaxListItems {
			return fmt.Errorf("JSON array has more than %d items", MaxListItems)
		}
		for index, child := range typed {
			if err := validateCanonicalJSONValue(child, depth+1); err != nil {
				return fmt.Errorf("array item %d: %w", index, err)
			}
		}
	case json.Number:
		text := typed.String()
		if !canonicalIntegerPattern.MatchString(text) {
			return fmt.Errorf("JSON number %q is not a canonical integer", text)
		}
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return fmt.Errorf("JSON integer %q is outside signed 64-bit range", text)
		}
	case string:
		if !utf8.ValidString(typed) || strings.ContainsRune(typed, 0) {
			return errors.New("JSON string must be valid UTF-8 without NUL")
		}
	case bool, nil:
		return nil
	default:
		return fmt.Errorf("unsupported JSON value type %T", value)
	}
	return nil
}

func validateCanonicalRecordTimes(record any, object map[string]any) error {
	check := func(name string, value any, expected string) error {
		text, ok := value.(string)
		if !ok || text != expected {
			return fmt.Errorf("%s must use canonical UTC timestamp %q", name, expected)
		}
		return nil
	}
	timeText := func(value time.Time) (string, error) {
		raw, err := value.MarshalJSON()
		if err != nil {
			return "", err
		}
		return strings.Trim(string(raw), `"`), nil
	}
	provenanceTime := func(expected string) error {
		provenance, ok := object["provenance"].(map[string]any)
		if !ok {
			return errors.New("provenance must be an object")
		}
		return check("provenance.created_at", provenance["created_at"], expected)
	}

	switch typed := record.(type) {
	case *ProjectManifest:
		expected, err := timeText(typed.CreatedAt)
		if err != nil {
			return err
		}
		return check("created_at", object["created_at"], expected)
	case *WorkPackageDefinition:
		expected, err := timeText(typed.CreatedAt)
		if err != nil {
			return err
		}
		if err := check("created_at", object["created_at"], expected); err != nil {
			return err
		}
		provenanceExpected, err := timeText(typed.Provenance.CreatedAt)
		if err != nil {
			return err
		}
		return provenanceTime(provenanceExpected)
	case *ExecutionManifest:
		expected, err := timeText(typed.CreatedAt)
		if err != nil {
			return err
		}
		if err := check("created_at", object["created_at"], expected); err != nil {
			return err
		}
		provenanceExpected, err := timeText(typed.Provenance.CreatedAt)
		if err != nil {
			return err
		}
		return provenanceTime(provenanceExpected)
	case *WorkEvent:
		expected, err := timeText(typed.OccurredAt)
		if err != nil {
			return err
		}
		return check("occurred_at", object["occurred_at"], expected)
	case *Handoff:
		expected, err := timeText(typed.CreatedAt)
		if err != nil {
			return err
		}
		if err := check("created_at", object["created_at"], expected); err != nil {
			return err
		}
		provenanceExpected, err := timeText(typed.Provenance.CreatedAt)
		if err != nil {
			return err
		}
		return provenanceTime(provenanceExpected)
	case *ArtifactManifest:
		expected, err := timeText(typed.CreatedAt)
		if err != nil {
			return err
		}
		if err := check("created_at", object["created_at"], expected); err != nil {
			return err
		}
		provenanceExpected, err := timeText(typed.Provenance.CreatedAt)
		if err != nil {
			return err
		}
		return provenanceTime(provenanceExpected)
	default:
		return fmt.Errorf("unsupported project record type %T", record)
	}
}

func digestJSONObject(object map[string]any) (string, error) {
	canonical, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("marshal project record integrity input: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func cloneJSONObject(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := consumeJSONValue(decoder, first); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("JSON contains trailing values")
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, token json.Token) error {
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("JSON object contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
			valueToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := consumeJSONValue(decoder, valueToken); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			valueToken, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := consumeJSONValue(decoder, valueToken); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}
