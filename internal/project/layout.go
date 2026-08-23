package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"syncgate/internal/filesystem"
)

const (
	WorkspaceDirectory         = "workspace"
	ControlDirectory           = ".agent-project"
	ManifestRelativePath       = ControlDirectory + "/manifest.json"
	HistoryEventsDirectory     = ControlDirectory + "/history/events"
	TasksDirectory             = ControlDirectory + "/tasks"
	WorkPackagesDirectory      = ControlDirectory + "/work-packages"
	ExecutionsDirectory        = ControlDirectory + "/executions"
	ArtifactManifestsDirectory = ControlDirectory + "/artifacts/manifests"
	ArtifactBlobsDirectory     = ControlDirectory + "/artifacts/blobs/sha256"
	LocalDirectory             = ControlDirectory + "/local"
	QuarantineDirectory        = LocalDirectory + "/quarantine"
	TemporaryRecordSuffix      = ".sync-part"
)

type Layout struct {
	root string
}

// NewLayout validates an existing project sync root. It does not create the
// project layout; bootstrap owns that operation in Stage 4.
func NewLayout(root string) (Layout, error) {
	if strings.TrimSpace(root) == "" {
		return Layout{}, domainError(ErrInvalidRoot, "validate project root", errors.New("root is required"))
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Layout{}, domainError(ErrInvalidRoot, "validate project root", err)
	}
	clean := filepath.Clean(absolute)
	info, err := os.Lstat(clean)
	if err != nil {
		return Layout{}, domainError(ErrInvalidRoot, "validate project root", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return Layout{}, domainError(ErrInvalidRoot, "validate project root", errors.New("root must be a real directory"))
	}
	return Layout{root: clean}, nil
}

func (layout Layout) Root() string { return layout.root }

func (layout Layout) WorkspacePath() string {
	return filepath.Join(layout.root, WorkspaceDirectory)
}

func (layout Layout) ControlPath() string {
	return filepath.Join(layout.root, ControlDirectory)
}

func (layout Layout) ManifestPath() string {
	return filepath.Join(layout.root, filepath.FromSlash(ManifestRelativePath))
}

// ValidateProjectRelativePath requires the exact cross-platform canonical form
// used by portable records. It does not silently normalize caller input.
func ValidateProjectRelativePath(relativePath string) error {
	if len(relativePath) > MaxRelativePathBytes {
		return domainError(ErrUnsafePath, "validate project-relative path", fmt.Errorf("path exceeds %d bytes", MaxRelativePathBytes))
	}
	normalized, err := filesystem.NormalizeRelativePath(relativePath)
	if err != nil {
		return domainError(ErrUnsafePath, "validate project-relative path", err)
	}
	if normalized != relativePath {
		return domainError(ErrUnsafePath, "validate project-relative path", fmt.Errorf("path is not canonical; expected %q", normalized))
	}
	return nil
}

func (layout Layout) Resolve(relativePath string) (string, error) {
	if layout.root == "" {
		return "", domainError(ErrInvalidRoot, "resolve project path", errors.New("layout is uninitialized"))
	}
	if err := ValidateProjectRelativePath(relativePath); err != nil {
		return "", err
	}
	resolved, err := filesystem.ResolveInsideShare(layout.root, relativePath)
	if err != nil {
		return "", domainError(ErrUnsafePath, "resolve project path", err)
	}
	return resolved, nil
}

func (layout Layout) ResolveManaged(relativePath string) (string, error) {
	if !isUnderDirectory(relativePath, ControlDirectory) {
		return "", domainError(ErrUnsafePath, "resolve managed project path", errors.New("path is outside the project control directory"))
	}
	return layout.Resolve(relativePath)
}

func (layout Layout) ResolvePortable(relativePath string) (string, error) {
	if isUnderDirectory(relativePath, LocalDirectory) {
		return "", domainError(ErrUnsafePath, "resolve portable project path", errors.New("local project state is not portable"))
	}
	return layout.ResolveManaged(relativePath)
}

func (layout Layout) InspectManaged(relativePath string) (string, fs.FileInfo, bool, error) {
	if _, err := layout.ResolveManaged(relativePath); err != nil {
		return "", nil, false, err
	}
	resolved, info, exists, err := filesystem.InspectInsideShare(layout.root, relativePath)
	if err != nil {
		return "", nil, false, domainError(ErrUnsafePath, "inspect managed project path", err)
	}
	return resolved, info, exists, nil
}

func WorkPackageDefinitionRelativePath(workPackageID string) (string, error) {
	if err := validateIdentifier("work_package_id", workPackageID, true); err != nil {
		return "", domainError(ErrUnsafePath, "build work package path", err)
	}
	return validatedBuiltPath("build work package path", WorkPackagesDirectory+"/"+pathKey(workPackageID)+"/definition.json")
}

func TaskRevisionRelativePath(taskID string, revision int64) (string, error) {
	if err := validateNamespacedIdentifier("task_id", taskID, "task:"); err != nil {
		return "", domainError(ErrUnsafePath, "build task revision path", err)
	}
	if revision < 1 || revision > MaxAggregateRevision {
		return "", domainError(ErrUnsafePath, "build task revision path", fmt.Errorf("task revision must be between 1 and %d", MaxAggregateRevision))
	}
	return validatedBuiltPath("build task revision path", fmt.Sprintf("%s/%s/revisions/%d/task.json", TasksDirectory, taskPathKey(taskID), revision))
}

func DependencyGraphRevisionRelativePath(taskID string, taskRevision, graphRevision int64) (string, error) {
	if err := validateNamespacedIdentifier("task_id", taskID, "task:"); err != nil {
		return "", domainError(ErrUnsafePath, "build dependency graph path", err)
	}
	if taskRevision < 1 || taskRevision > MaxAggregateRevision || graphRevision < 1 || graphRevision > MaxAggregateRevision {
		return "", domainError(ErrUnsafePath, "build dependency graph path", fmt.Errorf("task and graph revisions must be between 1 and %d", MaxAggregateRevision))
	}
	return validatedBuiltPath("build dependency graph path", fmt.Sprintf("%s/%s/revisions/%d/graphs/%d.json", TasksDirectory, taskPathKey(taskID), taskRevision, graphRevision))
}

func ExecutionManifestRelativePath(executionID string) (string, error) {
	if err := validateIdentifier("execution_id", executionID, true); err != nil {
		return "", domainError(ErrUnsafePath, "build execution path", err)
	}
	return validatedBuiltPath("build execution path", ExecutionsDirectory+"/"+pathKey(executionID)+"/manifest.json")
}

func HandoffRelativePath(executionID string) (string, error) {
	if err := validateIdentifier("execution_id", executionID, true); err != nil {
		return "", domainError(ErrUnsafePath, "build handoff path", err)
	}
	return validatedBuiltPath("build handoff path", ExecutionsDirectory+"/"+pathKey(executionID)+"/handoff.json")
}

func ExecutionResultRelativePath(executionID, name string) (string, error) {
	if err := validateIdentifier("execution_id", executionID, true); err != nil {
		return "", domainError(ErrUnsafePath, "build execution result path", err)
	}
	if err := ValidateProjectRelativePath(name); err != nil {
		return "", err
	}
	return validatedBuiltPath("build execution result path", ExecutionsDirectory+"/"+pathKey(executionID)+"/results/"+name)
}

func WorkEventRelativePath(eventID string, occurredAt time.Time) (string, error) {
	if err := validateIdentifier("event_id", eventID, true); err != nil {
		return "", domainError(ErrUnsafePath, "build work event path", err)
	}
	if err := validateUTCTime("occurred_at", occurredAt); err != nil {
		return "", domainError(ErrUnsafePath, "build work event path", err)
	}
	date := occurredAt.UTC().Format("2006/01/02")
	return validatedBuiltPath("build work event path", HistoryEventsDirectory+"/"+date+"/"+pathKey(eventID)+".json")
}

func ArtifactManifestRelativePath(artifactID string) (string, error) {
	if err := validateIdentifier("artifact_id", artifactID, true); err != nil {
		return "", domainError(ErrUnsafePath, "build artifact manifest path", err)
	}
	return validatedBuiltPath("build artifact manifest path", ArtifactManifestsDirectory+"/"+pathKey(artifactID)+".json")
}

func ArtifactBlobRelativePath(contentHash string) (string, error) {
	if !validSHA256(contentHash) {
		return "", domainError(ErrUnsafePath, "build artifact blob path", errors.New("content hash must be a lowercase SHA-256 digest"))
	}
	return validatedBuiltPath("build artifact blob path", ArtifactBlobsDirectory+"/"+contentHash)
}

func RecordRelativePath(record any) (string, error) {
	switch value := record.(type) {
	case ProjectManifest:
		return ManifestRelativePath, nil
	case *ProjectManifest:
		if value == nil {
			return "", domainError(ErrUnsafePath, "build project record path", errors.New("record is nil"))
		}
		return ManifestRelativePath, nil
	case WorkPackageDefinition:
		return WorkPackageDefinitionRelativePath(value.WorkPackageID)
	case TaskRevision:
		return TaskRevisionRelativePath(value.TaskID, value.Revision)
	case *TaskRevision:
		if value == nil {
			return "", domainError(ErrUnsafePath, "build project record path", errors.New("record is nil"))
		}
		return TaskRevisionRelativePath(value.TaskID, value.Revision)
	case DependencyGraphRevision:
		return DependencyGraphRevisionRelativePath(value.TaskID, value.TaskRevision, value.Revision)
	case *DependencyGraphRevision:
		if value == nil {
			return "", domainError(ErrUnsafePath, "build project record path", errors.New("record is nil"))
		}
		return DependencyGraphRevisionRelativePath(value.TaskID, value.TaskRevision, value.Revision)
	case *WorkPackageDefinition:
		if value == nil {
			return "", domainError(ErrUnsafePath, "build project record path", errors.New("record is nil"))
		}
		return WorkPackageDefinitionRelativePath(value.WorkPackageID)
	case ExecutionManifest:
		return ExecutionManifestRelativePath(value.ExecutionID)
	case *ExecutionManifest:
		if value == nil {
			return "", domainError(ErrUnsafePath, "build project record path", errors.New("record is nil"))
		}
		return ExecutionManifestRelativePath(value.ExecutionID)
	case WorkEvent:
		return WorkEventRelativePath(value.RecordID, value.OccurredAt)
	case *WorkEvent:
		if value == nil {
			return "", domainError(ErrUnsafePath, "build project record path", errors.New("record is nil"))
		}
		return WorkEventRelativePath(value.RecordID, value.OccurredAt)
	case Handoff:
		return HandoffRelativePath(value.ExecutionID)
	case *Handoff:
		if value == nil {
			return "", domainError(ErrUnsafePath, "build project record path", errors.New("record is nil"))
		}
		return HandoffRelativePath(value.ExecutionID)
	case ArtifactManifest:
		return ArtifactManifestRelativePath(value.ArtifactID)
	case *ArtifactManifest:
		if value == nil {
			return "", domainError(ErrUnsafePath, "build project record path", errors.New("record is nil"))
		}
		return ArtifactManifestRelativePath(value.ArtifactID)
	default:
		return "", domainError(ErrUnsafePath, "build project record path", fmt.Errorf("unsupported project record type %T", record))
	}
}

func pathKey(identifier string) string {
	// Namespaced IDs are portable record identities, but ':' is not a valid
	// Windows path character. Keep the identity losslessly encoded on disk.
	return strings.ReplaceAll(strings.ToLower(identifier), ":", "%3a")
}

func taskPathKey(taskID string) string {
	return strings.ReplaceAll(pathKey(taskID), ":", "%3a")
}

func validatedBuiltPath(operation, relativePath string) (string, error) {
	if err := ValidateProjectRelativePath(relativePath); err != nil {
		return "", domainError(ErrUnsafePath, operation, err)
	}
	return relativePath, nil
}

func isUnderDirectory(relativePath, directory string) bool {
	return relativePath == directory || strings.HasPrefix(relativePath, directory+"/")
}
