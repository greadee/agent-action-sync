// Package taskspec stores immutable, authority-local task specifications.
// Specifications are operator inputs; portable task history remains owned by
// workhistory after a specification has been resolved and validated.
package taskspec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/workhistory"
)

const (
	Schema   = "syncgate.task-specification.v1"
	MaxBytes = 1 << 20
)

var (
	ErrInvalid  = errors.New("invalid task specification")
	ErrConflict = errors.New("task specification conflict")
	ErrNotFound = errors.New("task specification not found")
)

type Specification struct {
	Schema          string                         `json:"schema"`
	SpecificationID string                         `json:"specification_id"`
	ProjectID       string                         `json:"project_id"`
	CreatedAt       time.Time                      `json:"created_at"`
	TaskID          string                         `json:"task_id"`
	TaskRevision    int64                          `json:"task_revision"`
	GraphRevision   int64                          `json:"graph_revision"`
	Objective       string                         `json:"objective"`
	Priority        project.TaskPriority           `json:"priority"`
	Risk            []project.RiskDimension        `json:"risk,omitempty"`
	Resources       *project.ResourceConstraints   `json:"resources,omitempty"`
	QualityGates    []project.QualityGateReference `json:"quality_gates,omitempty"`
	Barriers        []string                       `json:"barriers,omitempty"`
	WorkPackages    []WorkPackage                  `json:"work_packages"`
}

type WorkPackage struct {
	WorkPackageID      string                         `json:"work_package_id"`
	Objective          string                         `json:"objective"`
	Trade              string                         `json:"trade"`
	Specialization     string                         `json:"specialization,omitempty"`
	Scope              project.WorkScope              `json:"scope"`
	Dependencies       []string                       `json:"dependencies,omitempty"`
	Deliverables       []string                       `json:"deliverables"`
	AcceptanceCriteria []string                       `json:"acceptance_criteria"`
	ReviewRequired     bool                           `json:"review_required"`
	Priority           project.TaskPriority           `json:"priority,omitempty"`
	Risk               []project.RiskDimension        `json:"risk,omitempty"`
	Resources          *project.ResourceConstraints   `json:"resources,omitempty"`
	QualityGates       []project.QualityGateReference `json:"quality_gates,omitempty"`
	TradeReference     *project.RegistryReference     `json:"trade_reference,omitempty"`
}

type Stored struct {
	Specification  Specification
	Digest         string
	AlreadyPresent bool
}

type FileStore struct{ Root string }

func Decode(ctx context.Context, raw []byte) (Specification, []byte, string, error) {
	if ctx == nil || ctx.Err() != nil || len(raw) == 0 || len(raw) > MaxBytes {
		return Specification{}, nil, "", ErrInvalid
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return Specification{}, nil, "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value Specification
	if err := decoder.Decode(&value); err != nil {
		return Specification{}, nil, "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Specification{}, nil, "", fmt.Errorf("%w: trailing JSON data", ErrInvalid)
	}
	normalize(&value)
	if err := Validate(ctx, value); err != nil {
		return Specification{}, nil, "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) == 0 || len(canonical) > MaxBytes {
		return Specification{}, nil, "", ErrInvalid
	}
	sum := sha256.Sum256(canonical)
	return value, canonical, hex.EncodeToString(sum[:]), nil
}

func Validate(ctx context.Context, value Specification) error {
	if ctx == nil || value.Schema != Schema || !identifier(value.SpecificationID) || value.TaskRevision != 1 || value.GraphRevision != 1 || len(value.WorkPackages) == 0 || len(value.WorkPackages) > project.MaxListItems {
		return ErrInvalid
	}
	when := value.CreatedAt
	producer := project.Producer{DeviceID: "device:task-specification-validation"}
	task := project.TaskRevision{
		RecordHeader: project.NewRecordHeader(project.RecordTaskRevision, "record:task-specification-validation", value.ProjectID),
		TaskID:       value.TaskID, Revision: value.TaskRevision, Objective: value.Objective, Priority: value.Priority,
		Risk: append([]project.RiskDimension(nil), value.Risk...), Resources: cloneResources(value.Resources), GraphRevision: value.GraphRevision,
		QualityGates: append([]project.QualityGateReference(nil), value.QualityGates...), CreatedAt: when,
		Provenance: project.Provenance{Producer: producer, CreatedAt: when},
	}
	if _, err := project.MarshalRecord(task); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	definitions := make(map[string]orchestration.WorkPackageDefinition, len(value.WorkPackages))
	members := make([]project.DependencyGraphMember, 0, len(value.WorkPackages))
	for _, item := range value.WorkPackages {
		if _, exists := definitions[item.WorkPackageID]; exists {
			return fmt.Errorf("%w: duplicate work package %s", ErrInvalid, item.WorkPackageID)
		}
		recordID := validationRecordID(item.WorkPackageID)
		record := project.WorkPackageDefinition{
			RecordHeader:  project.NewRecordHeader(project.RecordWorkPackage, recordID, value.ProjectID),
			WorkPackageID: item.WorkPackageID, Objective: item.Objective, Trade: item.Trade, Specialization: item.Specialization,
			Scope: item.Scope, Dependencies: append([]string(nil), item.Dependencies...), Deliverables: append([]string(nil), item.Deliverables...),
			AcceptanceCriteria: append([]string(nil), item.AcceptanceCriteria...), ReviewRequired: item.ReviewRequired,
			TaskID: value.TaskID, TaskRevision: value.TaskRevision, GraphRevision: value.GraphRevision, Priority: item.Priority,
			Risk: append([]project.RiskDimension(nil), item.Risk...), Resources: cloneResources(item.Resources),
			QualityGates: append([]project.QualityGateReference(nil), item.QualityGates...), TradeReference: cloneReference(item.TradeReference),
			CreatedAt: when, Provenance: project.Provenance{Producer: producer, WorkPackageID: item.WorkPackageID, CreatedAt: when},
		}
		raw, err := project.MarshalRecord(record)
		if err != nil {
			return fmt.Errorf("%w: work package %s: %v", ErrInvalid, item.WorkPackageID, err)
		}
		decoded, err := project.DecodeRecord(raw)
		if err != nil {
			return fmt.Errorf("%w: work package %s: %v", ErrInvalid, item.WorkPackageID, err)
		}
		definitions[item.WorkPackageID] = orchestration.WorkPackageDefinition{Record: record, RecordDigest: decoded.Digest}
		members = append(members, project.DependencyGraphMember{WorkPackageID: item.WorkPackageID, DefinitionRecordID: recordID, DefinitionDigest: decoded.Digest})
	}
	dependencyDigest, err := orchestration.DependencySetDigest(ctx, definitions)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	graph := project.DependencyGraphRevision{
		RecordHeader: project.NewRecordHeader(project.RecordDependencyGraph, "record:task-specification-graph-validation", value.ProjectID),
		TaskID:       value.TaskID, TaskRevision: value.TaskRevision, Revision: value.GraphRevision, Members: members,
		DependencySetDigest: dependencyDigest, Barriers: append([]string(nil), value.Barriers...), CreatedAt: when,
		Provenance: project.Provenance{Producer: producer, CreatedAt: when},
	}
	if _, err := project.MarshalRecord(graph); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := orchestration.ValidateGraph(ctx, orchestration.Graph{Task: task, Revision: graph, Definitions: definitions}); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

func (value Specification) CreateRequest(metadata workhistory.Metadata) workhistory.CreateTaskRequest {
	items := make([]workhistory.TaskWorkPackageRequest, 0, len(value.WorkPackages))
	for _, item := range value.WorkPackages {
		items = append(items, workhistory.TaskWorkPackageRequest{
			WorkPackageID: item.WorkPackageID, Objective: item.Objective, Trade: item.Trade, Specialization: item.Specialization,
			Scope: item.Scope, Dependencies: append([]string(nil), item.Dependencies...), Deliverables: append([]string(nil), item.Deliverables...),
			AcceptanceCriteria: append([]string(nil), item.AcceptanceCriteria...), ReviewRequired: item.ReviewRequired,
			Priority: item.Priority, Risk: append([]project.RiskDimension(nil), item.Risk...), Resources: cloneResources(item.Resources),
			QualityGates: append([]project.QualityGateReference(nil), item.QualityGates...), TradeReference: cloneReference(item.TradeReference),
		})
	}
	return workhistory.CreateTaskRequest{
		Metadata: metadata, TaskID: value.TaskID, TaskRevision: value.TaskRevision, GraphRevision: value.GraphRevision,
		Objective: value.Objective, Priority: value.Priority, Risk: append([]project.RiskDimension(nil), value.Risk...),
		Resources: cloneResources(value.Resources), QualityGates: append([]project.QualityGateReference(nil), value.QualityGates...),
		Barriers: append([]string(nil), value.Barriers...), WorkPackages: items,
	}
}

func (store FileStore) Put(ctx context.Context, raw []byte) (Stored, error) {
	value, canonical, digest, err := Decode(ctx, raw)
	if err != nil {
		return Stored{}, err
	}
	path, err := store.path(value.SpecificationID)
	if err != nil {
		return Stored{}, err
	}
	if existing, readErr := store.read(ctx, path); readErr == nil {
		if !bytes.Equal(existing, canonical) {
			return Stored{}, ErrConflict
		}
		return Stored{Specification: value, Digest: digest, AlreadyPresent: true}, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return Stored{}, readErr
	}
	if err := ensureRealDirectory(filepath.Dir(path), 0o700); err != nil {
		return Stored{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".task-spec-*.tmp")
	if err != nil {
		return Stored{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return Stored{}, err
	}
	if _, err := temporary.Write(canonical); err != nil {
		_ = temporary.Close()
		return Stored{}, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return Stored{}, err
	}
	if err := temporary.Close(); err != nil {
		return Stored{}, err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if existing, readErr := store.read(ctx, path); readErr == nil {
			if bytes.Equal(existing, canonical) {
				return Stored{Specification: value, Digest: digest, AlreadyPresent: true}, nil
			}
			return Stored{}, ErrConflict
		}
		return Stored{}, err
	}
	return Stored{Specification: value, Digest: digest}, nil
}

func (store FileStore) Get(ctx context.Context, specificationID, expectedDigest string) (Stored, error) {
	path, err := store.path(specificationID)
	if err != nil {
		return Stored{}, err
	}
	raw, err := store.read(ctx, path)
	if errors.Is(err, os.ErrNotExist) {
		return Stored{}, ErrNotFound
	}
	if err != nil {
		return Stored{}, err
	}
	value, canonical, digest, err := Decode(ctx, raw)
	if err != nil || value.SpecificationID != specificationID || !bytes.Equal(raw, canonical) {
		return Stored{}, ErrInvalid
	}
	if expectedDigest != "" && digest != expectedDigest {
		return Stored{}, ErrConflict
	}
	return Stored{Specification: value, Digest: digest, AlreadyPresent: true}, nil
}

func (store FileStore) read(ctx context.Context, path string) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrInvalid
	}
	if err := inspectRealDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > MaxBytes {
		return nil, ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil || len(raw) > MaxBytes {
		return nil, ErrInvalid
	}
	return raw, ctx.Err()
}

// ensureRealDirectory creates missing directories one component at a time and
// verifies every existing ancestor with Lstat. This keeps a configured store
// root from being redirected through a symlink or junction-like file entry.
func ensureRealDirectory(path string, permission os.FileMode) error {
	cleaned := filepath.Clean(path)
	info, err := os.Lstat(cleaned)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrInvalid
		}
		parent := filepath.Dir(cleaned)
		if parent != cleaned {
			return inspectRealDirectory(parent)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(cleaned)
	if parent == cleaned {
		return ErrInvalid
	}
	if err := ensureRealDirectory(parent, permission); err != nil {
		return err
	}
	if err := os.Mkdir(cleaned, permission); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err = os.Lstat(cleaned)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrInvalid
	}
	return nil
}

func inspectRealDirectory(path string) error {
	cleaned := filepath.Clean(path)
	parent := filepath.Dir(cleaned)
	if parent != cleaned {
		if err := inspectRealDirectory(parent); err != nil {
			return err
		}
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrInvalid
	}
	return nil
}

func (store FileStore) path(id string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(store.Root))
	if !filepath.IsAbs(root) || !identifier(id) {
		return "", ErrInvalid
	}
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(root, hex.EncodeToString(sum[:])+".json"), nil
}

func normalize(value *Specification) {
	value.Schema = strings.TrimSpace(value.Schema)
	value.SpecificationID = strings.TrimSpace(value.SpecificationID)
	value.ProjectID = strings.TrimSpace(value.ProjectID)
	value.TaskID = strings.TrimSpace(value.TaskID)
	value.Risk, value.Resources, value.QualityGates, value.Barriers = orchestration.NormalizeTaskInputs(value.Risk, value.Resources, value.QualityGates, value.Barriers)
	for index := range value.WorkPackages {
		item := &value.WorkPackages[index]
		item.WorkPackageID = strings.TrimSpace(item.WorkPackageID)
		item.Dependencies = sortedTrimmed(item.Dependencies)
		item.Scope.Allowed = sortedTrimmed(item.Scope.Allowed)
		item.Scope.Inspect = sortedTrimmed(item.Scope.Inspect)
		item.Scope.Forbidden = sortedTrimmed(item.Scope.Forbidden)
		item.Risk, item.Resources, item.QualityGates, _ = orchestration.NormalizeTaskInputs(item.Risk, item.Resources, item.QualityGates, nil)
		if item.Priority == "" {
			item.Priority = value.Priority
		}
	}
	sort.Slice(value.WorkPackages, func(i, j int) bool { return value.WorkPackages[i].WorkPackageID < value.WorkPackages[j].WorkPackageID })
}

func sortedTrimmed(values []string) []string {
	result := append([]string(nil), values...)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
	}
	sort.Strings(result)
	return result
}

func cloneResources(value *project.ResourceConstraints) *project.ResourceConstraints {
	if value == nil {
		return nil
	}
	copy := *value
	copy.RequiredCapabilities = append([]string(nil), value.RequiredCapabilities...)
	copy.RequiredTools = append([]string(nil), value.RequiredTools...)
	copy.AllowedOS = append([]string(nil), value.AllowedOS...)
	copy.AllowedArchitectures = append([]string(nil), value.AllowedArchitectures...)
	return &copy
}

func cloneReference(value *project.RegistryReference) *project.RegistryReference {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validationRecordID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "record:task-specification-work-" + hex.EncodeToString(sum[:12])
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func identifier(value string) bool { return identifierPattern.MatchString(value) }

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, structured := token.(json.Delim)
	if !structured {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object key is invalid")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate JSON key %q", name)
			}
			seen[name] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is incomplete")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is incomplete")
		}
	default:
		return errors.New("JSON delimiter is invalid")
	}
	return nil
}
