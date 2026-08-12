package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	stdsync "sync"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/filesystem"
	"syncgate/internal/storage"
	"syncgate/internal/transfer"
)

const DefaultOneWayIncomingDir = ".sync-incoming"

var (
	ErrInvalidOneWayApply       = errors.New("invalid one-way apply request")
	ErrOneWayApplyState         = errors.New("one-way apply state changed")
	ErrOneWayApplyDrift         = errors.New("one-way apply filesystem drift")
	ErrOneWayApplyContent       = errors.New("one-way apply content verification failed")
	ErrOneWayApplyAuthorization = errors.New("one-way apply authorization failed")
)

var oneWayApplyPathLocks = struct {
	stdsync.Mutex
	locks map[string]*oneWayApplyPathLock
}{locks: make(map[string]*oneWayApplyPathLock)}

type oneWayApplyPathLock struct {
	mutex stdsync.Mutex
	refs  int
}

type OneWayApplyRequest struct {
	AuthenticatedPeerID core.DeviceID
	Prepared            PreparedOneWayChange
	ShareRoot           string
	TombstoneID         core.TombstoneID
	TombstoneExpiresAt  time.Time
}

type OneWayApplyResult struct {
	RevisionID      core.RevisionID
	DestinationPath string
	PreservedPath   string
	Tombstone       *storage.Tombstone
	AlreadyApplied  bool
	Recovered       bool
}

// OneWayApplyExecutor connects a committed, verified receive artifact to the
// revision-aware target state. The durable intent and deterministic history
// path make retries safe across interruption between filesystem and SQLite
// commits.
type OneWayApplyExecutor struct {
	Revisions  storage.RevisionStore
	Applier    storage.OneWayApplyStore
	Tombstones storage.TombstoneStore
	Shares     storage.ShareStore
	Now        func() time.Time
}

type oneWayApplyIntent struct {
	ShareID                   core.ShareID     `json:"share_id"`
	RevisionID                core.RevisionID  `json:"revision_id"`
	RelativePath              string           `json:"relative_path"`
	Action                    OneWayAction     `json:"action"`
	ExpectedCurrentRevisionID core.RevisionID  `json:"expected_current_revision_id,omitempty"`
	TombstoneID               core.TombstoneID `json:"tombstone_id,omitempty"`
	TombstoneExpiresAt        time.Time        `json:"tombstone_expires_at,omitempty"`
	AppliedAt                 time.Time        `json:"applied_at"`
}

// BuildOneWayReceiveSpec returns the deterministic in-share destination used
// by ReceiveWriter for a verified file. The executor accepts file content only
// from this path, keeping transfer staging on the destination volume.
func BuildOneWayReceiveSpec(shareRoot string, prepared PreparedOneWayChange) (transfer.ReceiveSpec, error) {
	if err := validateApplyPreparedChange(prepared); err != nil {
		return transfer.ReceiveSpec{}, err
	}
	if prepared.SourceRevision.EntryType != core.EntryFile || prepared.SourceRevision.IsDeleted {
		return transfer.ReceiveSpec{}, fmt.Errorf("%w: receive staging requires an active file revision", ErrInvalidOneWayApply)
	}
	if shareRoot == "" {
		return transfer.ReceiveSpec{}, fmt.Errorf("%w: share root is required", ErrInvalidOneWayApply)
	}
	return transfer.ReceiveSpec{
		ShareRoot:      shareRoot,
		RelativePath:   oneWayReadyRelativePath(prepared.SourceRevision),
		ExpectedSize:   prepared.SourceRevision.Size,
		ExpectedHash:   prepared.SourceRevision.ContentHash,
		HashAlgorithm:  prepared.SourceRevision.HashAlgorithm,
		PartialSuffix:  transfer.DefaultPartialSuffix,
		HistoryDirName: transfer.DefaultHistoryDir,
	}, nil
}

func (executor OneWayApplyExecutor) Apply(ctx context.Context, request OneWayApplyRequest) (OneWayApplyResult, error) {
	if executor.Revisions == nil || executor.Applier == nil || executor.Tombstones == nil || executor.Shares == nil {
		return OneWayApplyResult{}, fmt.Errorf("%w: revision, apply, tombstone, and share stores are required", ErrInvalidOneWayApply)
	}
	if err := validateOneWayApplyRequest(request); err != nil {
		return OneWayApplyResult{}, err
	}
	requiredCapability, err := oneWayActionCapability(request.Prepared.Change.Action)
	if err != nil {
		return OneWayApplyResult{}, err
	}
	for _, capability := range []core.Capability{core.CapabilitySync, requiredCapability} {
		if err := executor.Shares.Authorize(ctx, request.AuthenticatedPeerID, request.Prepared.Change.ShareID, capability, request.Prepared.Remote); err != nil {
			return OneWayApplyResult{}, fmt.Errorf("%w: %s capability: %v", ErrOneWayApplyAuthorization, capability, err)
		}
	}
	releasePath := lockOneWayApplyPath(request.ShareRoot, request.Prepared.SourceRevision)
	defer releasePath()

	revision := request.Prepared.SourceRevision
	destinationPath, _, _, err := filesystem.InspectInsideShare(request.ShareRoot, revision.RelativePath)
	if err != nil {
		return OneWayApplyResult{}, err
	}
	readyRelativePath := oneWayReadyRelativePath(revision)
	intentRelativePath := oneWayIntentRelativePath(revision)
	historyRelativePath := oneWayHistoryRelativePath(revision)

	current, currentExists, err := executor.currentRevision(ctx, revision.ShareID, revision.RelativePath)
	if err != nil {
		return OneWayApplyResult{}, err
	}
	if currentExists && current.ID == revision.ID {
		if err := verifyTerminalFilesystemState(request.ShareRoot, revision); err != nil {
			return OneWayApplyResult{}, err
		}
		result := OneWayApplyResult{RevisionID: revision.ID, DestinationPath: destinationPath, AlreadyApplied: true}
		if revision.IsDeleted {
			tombstone, err := executor.Tombstones.Get(ctx, revision.ShareID, revision.RelativePath)
			if err != nil {
				return OneWayApplyResult{}, err
			}
			result.Tombstone = &tombstone
		}
		if err := cleanupApplyArtifacts(request.ShareRoot, intentRelativePath, readyRelativePath); err != nil {
			return OneWayApplyResult{}, err
		}
		return result, nil
	}
	if currentExists {
		if current.ID != request.Prepared.TargetRevisionID {
			return OneWayApplyResult{}, fmt.Errorf("%w: current revision is %s, prepared target was %s", ErrOneWayApplyState, current.ID, request.Prepared.TargetRevisionID)
		}
	} else if request.Prepared.TargetRevisionID != "" {
		return OneWayApplyResult{}, fmt.Errorf("%w: prepared target revision %s is missing", ErrOneWayApplyState, request.Prepared.TargetRevisionID)
	}

	intent, intentExists, err := readOneWayApplyIntent(request.ShareRoot, intentRelativePath)
	if err != nil {
		return OneWayApplyResult{}, err
	}
	if intentExists {
		if err := validateOneWayApplyIntent(intent, request); err != nil {
			return OneWayApplyResult{}, err
		}
		if err := validateApplyTombstoneExpiry(revision, intent.AppliedAt, request.TombstoneExpiresAt); err != nil {
			return OneWayApplyResult{}, err
		}
	} else {
		if err := verifyInitialFilesystemState(request.ShareRoot, revision, current, currentExists, readyRelativePath); err != nil {
			return OneWayApplyResult{}, err
		}
		now := time.Now
		if executor.Now != nil {
			now = executor.Now
		}
		appliedAt := now().UTC()
		if appliedAt.IsZero() {
			return OneWayApplyResult{}, fmt.Errorf("%w: apply clock returned zero time", ErrInvalidOneWayApply)
		}
		if err := validateApplyTombstoneExpiry(revision, appliedAt, request.TombstoneExpiresAt); err != nil {
			return OneWayApplyResult{}, err
		}
		intent = oneWayApplyIntent{
			ShareID:                   revision.ShareID,
			RevisionID:                revision.ID,
			RelativePath:              revision.RelativePath,
			Action:                    request.Prepared.Change.Action,
			ExpectedCurrentRevisionID: request.Prepared.TargetRevisionID,
			TombstoneID:               request.TombstoneID,
			TombstoneExpiresAt:        request.TombstoneExpiresAt.UTC(),
			AppliedAt:                 appliedAt,
		}
		if err := writeOneWayApplyIntent(request.ShareRoot, intentRelativePath, intent); err != nil {
			return OneWayApplyResult{}, err
		}
	}

	preservedPath, err := applyOneWayFilesystem(request.ShareRoot, revision, current, currentExists, readyRelativePath, historyRelativePath)
	if err != nil {
		return OneWayApplyResult{}, err
	}
	if err := verifyTerminalFilesystemState(request.ShareRoot, revision); err != nil {
		return OneWayApplyResult{}, err
	}

	acceptedRevision := revision
	acceptedRevision.CreatedAt = intent.AppliedAt
	commit := storage.OneWayApplyCommit{
		ShareID:                   revision.ShareID,
		Revision:                  acceptedRevision,
		ExpectedCurrentRevisionID: intent.ExpectedCurrentRevisionID,
		AppliedAt:                 intent.AppliedAt,
		AllowTargetDrift:          request.Prepared.Decision.TargetDrift && request.Prepared.Decision.PreserveConflictCopy,
	}
	if acceptedRevision.IsDeleted {
		commit.Tombstone = &storage.TombstoneRequest{
			ID:                  request.TombstoneID,
			TombstoneRevisionID: acceptedRevision.ID,
			ExpiresAt:           request.TombstoneExpiresAt,
		}
	} else {
		entry, err := appliedFileIndexEntry(request.ShareRoot, acceptedRevision, intent.AppliedAt)
		if err != nil {
			return OneWayApplyResult{}, err
		}
		commit.Entry = &entry
	}
	commitResult, err := executor.Applier.CommitOneWayApply(ctx, commit)
	if err != nil {
		return OneWayApplyResult{}, err
	}
	if err := cleanupApplyArtifacts(request.ShareRoot, intentRelativePath, readyRelativePath); err != nil {
		return OneWayApplyResult{}, err
	}
	return OneWayApplyResult{
		RevisionID:      acceptedRevision.ID,
		DestinationPath: destinationPath,
		PreservedPath:   preservedPath,
		Tombstone:       commitResult.Tombstone,
		AlreadyApplied:  commitResult.AlreadyApplied,
		Recovered:       intentExists,
	}, nil
}

func lockOneWayApplyPath(root string, revision core.Revision) func() {
	key := filepath.Clean(root) + "\x00" + string(revision.ShareID) + "\x00" + revision.RelativePath
	oneWayApplyPathLocks.Lock()
	lock := oneWayApplyPathLocks.locks[key]
	if lock == nil {
		lock = &oneWayApplyPathLock{}
		oneWayApplyPathLocks.locks[key] = lock
	}
	lock.refs++
	oneWayApplyPathLocks.Unlock()

	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		oneWayApplyPathLocks.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(oneWayApplyPathLocks.locks, key)
		}
		oneWayApplyPathLocks.Unlock()
	}
}

func validateOneWayApplyRequest(request OneWayApplyRequest) error {
	if request.AuthenticatedPeerID == "" {
		return fmt.Errorf("%w: authenticated peer ID is required", ErrInvalidOneWayApply)
	}
	if request.ShareRoot == "" {
		return fmt.Errorf("%w: share root is required", ErrInvalidOneWayApply)
	}
	if err := validateApplyPreparedChange(request.Prepared); err != nil {
		return err
	}
	if request.AuthenticatedPeerID != request.Prepared.AuthenticatedPeerID {
		return fmt.Errorf("%w: apply peer does not match prepared peer", ErrInvalidOneWayApply)
	}
	if request.Prepared.SourceRevision.IsDeleted {
		if request.TombstoneID == "" {
			return fmt.Errorf("%w: deletion requires a tombstone ID", ErrInvalidOneWayApply)
		}
	} else if request.TombstoneID != "" || !request.TombstoneExpiresAt.IsZero() {
		return fmt.Errorf("%w: active revision cannot include tombstone metadata", ErrInvalidOneWayApply)
	}
	return nil
}

func validateApplyTombstoneExpiry(revision core.Revision, appliedAt, expiresAt time.Time) error {
	if !revision.IsDeleted || expiresAt.IsZero() {
		return nil
	}
	if expiresAt.Before(appliedAt) {
		return fmt.Errorf("%w: tombstone expires before the accepted deletion", ErrInvalidOneWayApply)
	}
	return nil
}

func validateApplyPreparedChange(prepared PreparedOneWayChange) error {
	if prepared.AuthenticatedPeerID == "" || prepared.AuthenticatedPeerID != prepared.Change.SourceDeviceID {
		return fmt.Errorf("%w: prepared authenticated peer does not match source", ErrInvalidOneWayApply)
	}
	if !prepared.Decision.Apply {
		return fmt.Errorf("%w: prepared decision does not permit filesystem work", ErrInvalidOneWayApply)
	}
	if prepared.Decision.Action != prepared.Change.Action || prepared.RelativePath != prepared.SourceRevision.RelativePath {
		return fmt.Errorf("%w: prepared descriptor fields do not agree", ErrInvalidOneWayApply)
	}
	targetDrift := prepared.TargetRevisionID != prepared.Change.ExpectedBaseRevisionID
	if prepared.Decision.TargetDrift != targetDrift {
		return fmt.Errorf("%w: prepared target revision and drift decision do not agree", ErrInvalidOneWayApply)
	}
	if targetDrift && !prepared.Decision.PreserveConflictCopy {
		return fmt.Errorf("%w: drifted apply must preserve a conflict copy", ErrInvalidOneWayApply)
	}
	if err := validateSourceRevision(prepared.SourceRevision, prepared.Change); err != nil {
		return err
	}
	switch prepared.SourceRevision.EntryType {
	case core.EntryFile, core.EntryDirectory, core.EntryDeleted:
		return nil
	default:
		return fmt.Errorf("%w: unsupported entry type %q", ErrInvalidOneWayApply, prepared.SourceRevision.EntryType)
	}
}

func (executor OneWayApplyExecutor) currentRevision(ctx context.Context, shareID core.ShareID, relativePath string) (core.Revision, bool, error) {
	revision, err := executor.Revisions.GetCurrentRevision(ctx, shareID, relativePath)
	if errors.Is(err, storage.ErrNotFound) {
		return core.Revision{}, false, nil
	}
	if err != nil {
		return core.Revision{}, false, err
	}
	return revision, true, nil
}

func verifyInitialFilesystemState(root string, source, current core.Revision, currentExists bool, readyRelativePath string) error {
	_, destinationInfo, destinationExists, err := filesystem.InspectInsideShare(root, source.RelativePath)
	if err != nil {
		return err
	}
	if currentExists && !current.IsDeleted {
		if !destinationExists {
			return fmt.Errorf("%w: current path %s is missing", ErrOneWayApplyDrift, source.RelativePath)
		}
		if err := verifyRevisionPath(root, current, destinationInfo); err != nil {
			return err
		}
	} else if destinationExists {
		return fmt.Errorf("%w: untracked destination exists at %s", ErrOneWayApplyDrift, source.RelativePath)
	}
	if source.EntryType == core.EntryFile && !source.IsDeleted {
		_, readyInfo, readyExists, err := filesystem.InspectInsideShare(root, readyRelativePath)
		if err != nil {
			return err
		}
		if !readyExists {
			return fmt.Errorf("%w: verified receive artifact is missing", ErrOneWayApplyContent)
		}
		if err := verifyRevisionPath(root, revisionAtPath(source, readyRelativePath), readyInfo); err != nil {
			return err
		}
	}
	return nil
}

func applyOneWayFilesystem(root string, source, current core.Revision, currentExists bool, readyRelativePath, historyRelativePath string) (string, error) {
	destinationPath, destinationInfo, destinationExists, err := filesystem.InspectInsideShare(root, source.RelativePath)
	if err != nil {
		return "", err
	}
	historyPath, historyInfo, historyExists, err := filesystem.InspectInsideShare(root, historyRelativePath)
	if err != nil {
		return "", err
	}
	preservedPath := ""

	if currentExists && !current.IsDeleted {
		if historyExists {
			if err := verifyRevisionPath(root, revisionAtPath(current, historyRelativePath), historyInfo); err != nil {
				return "", err
			}
			preservedPath = historyPath
		}
		if destinationExists {
			sourceMatches := revisionPathMatches(root, source, destinationInfo)
			if historyExists {
				if !sourceMatches {
					return "", fmt.Errorf("%w: current destination and preserved history both exist", ErrOneWayApplyState)
				}
				return preservedPath, nil
			}
			if err := verifyRevisionPath(root, current, destinationInfo); err != nil {
				if sourceMatches {
					return "", fmt.Errorf("%w: applied destination exists without preserved prior state", ErrOneWayApplyState)
				}
				return "", err
			}
			if !source.IsDeleted && source.EntryType == core.EntryDirectory && current.EntryType == core.EntryDirectory {
				return "", nil
			}
			if source.IsDeleted && current.EntryType == core.EntryDirectory {
				entries, err := os.ReadDir(destinationPath)
				if err != nil {
					return "", fmt.Errorf("inspect directory before deletion: %w", err)
				}
				if len(entries) != 0 {
					return "", fmt.Errorf("%w: directory %s must be empty before deletion", ErrOneWayApplyState, source.RelativePath)
				}
			}
			if _, err := filesystem.EnsureParentDirectoriesInsideShare(root, historyRelativePath, 0o700); err != nil {
				return "", err
			}
			if err := os.Rename(destinationPath, historyPath); err != nil {
				return "", fmt.Errorf("preserve current destination: %w", err)
			}
			preservedPath = historyPath
		} else if !historyExists {
			return "", fmt.Errorf("%w: current destination and recovery history are missing", ErrOneWayApplyState)
		}
	} else if destinationExists {
		if revisionPathMatches(root, source, destinationInfo) {
			return "", nil
		}
		return "", fmt.Errorf("%w: unexpected destination appeared during apply", ErrOneWayApplyDrift)
	}

	if source.IsDeleted {
		return preservedPath, nil
	}
	if source.EntryType == core.EntryDirectory {
		if _, err := filesystem.EnsureParentDirectoriesInsideShare(root, source.RelativePath, 0o700); err != nil {
			return "", err
		}
		if err := os.Mkdir(destinationPath, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create destination directory: %w", err)
		}
		return preservedPath, nil
	}

	readyPath, readyInfo, readyExists, err := filesystem.InspectInsideShare(root, readyRelativePath)
	if err != nil {
		return "", err
	}
	if !readyExists {
		return "", fmt.Errorf("%w: verified receive artifact is missing", ErrOneWayApplyContent)
	}
	if err := verifyRevisionPath(root, revisionAtPath(source, readyRelativePath), readyInfo); err != nil {
		return "", err
	}
	if _, err := filesystem.EnsureParentDirectoriesInsideShare(root, source.RelativePath, 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(readyPath, destinationPath); err != nil {
		return "", fmt.Errorf("commit verified receive artifact: %w", err)
	}
	return preservedPath, nil
}

func verifyTerminalFilesystemState(root string, revision core.Revision) error {
	_, info, exists, err := filesystem.InspectInsideShare(root, revision.RelativePath)
	if err != nil {
		return err
	}
	if revision.IsDeleted {
		if exists {
			return fmt.Errorf("%w: deleted destination still exists", ErrOneWayApplyState)
		}
		return nil
	}
	if !exists {
		return fmt.Errorf("%w: applied destination is missing", ErrOneWayApplyState)
	}
	return verifyRevisionPath(root, revision, info)
}

func verifyRevisionPath(root string, revision core.Revision, info fs.FileInfo) error {
	if revision.EntryType == core.EntryDirectory {
		if !info.IsDir() {
			return fmt.Errorf("%w: %s is not a directory", ErrOneWayApplyDrift, revision.RelativePath)
		}
		return nil
	}
	if revision.EntryType != core.EntryFile || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrOneWayApplyDrift, revision.RelativePath)
	}
	if info.Size() != revision.Size {
		return fmt.Errorf("%w: %s size is %d, want %d", ErrOneWayApplyContent, revision.RelativePath, info.Size(), revision.Size)
	}
	resolved, _, exists, err := filesystem.InspectInsideShare(root, revision.RelativePath)
	if err != nil || !exists {
		return fmt.Errorf("%w: inspect %s: %v", ErrOneWayApplyContent, revision.RelativePath, err)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return fmt.Errorf("%w: open %s: %v", ErrOneWayApplyContent, revision.RelativePath, err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fmt.Errorf("%w: hash %s: %v", ErrOneWayApplyContent, revision.RelativePath, err)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != revision.ContentHash {
		return fmt.Errorf("%w: %s hash is %s, want %s", ErrOneWayApplyContent, revision.RelativePath, actual, revision.ContentHash)
	}
	return nil
}

func revisionPathMatches(root string, revision core.Revision, info fs.FileInfo) bool {
	return verifyRevisionPath(root, revision, info) == nil
}

func revisionAtPath(revision core.Revision, relativePath string) core.Revision {
	revision.RelativePath = relativePath
	return revision
}

func appliedFileIndexEntry(root string, revision core.Revision, appliedAt time.Time) (core.FileIndexEntry, error) {
	_, info, exists, err := filesystem.InspectInsideShare(root, revision.RelativePath)
	if err != nil {
		return core.FileIndexEntry{}, err
	}
	if !exists {
		return core.FileIndexEntry{}, fmt.Errorf("applied destination %s is missing", revision.RelativePath)
	}
	return core.FileIndexEntry{
		ShareID:       revision.ShareID,
		RelativePath:  revision.RelativePath,
		EntryType:     revision.EntryType,
		Size:          revision.Size,
		ModifiedTime:  info.ModTime().UTC(),
		ContentHash:   revision.ContentHash,
		HashAlgorithm: revision.HashAlgorithm,
		LastScannedAt: appliedAt,
	}, nil
}

func readOneWayApplyIntent(root, relativePath string) (oneWayApplyIntent, bool, error) {
	resolved, info, exists, err := filesystem.InspectInsideShare(root, relativePath)
	if err != nil || !exists {
		return oneWayApplyIntent{}, false, err
	}
	if !info.Mode().IsRegular() {
		return oneWayApplyIntent{}, false, fmt.Errorf("%w: apply intent is not a regular file", ErrOneWayApplyState)
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return oneWayApplyIntent{}, false, fmt.Errorf("read one-way apply intent: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var intent oneWayApplyIntent
	if err := decoder.Decode(&intent); err != nil {
		return oneWayApplyIntent{}, false, fmt.Errorf("decode one-way apply intent: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return oneWayApplyIntent{}, false, fmt.Errorf("decode one-way apply intent: trailing content")
	}
	return intent, true, nil
}

func writeOneWayApplyIntent(root, relativePath string, intent oneWayApplyIntent) error {
	resolved, err := filesystem.EnsureParentDirectoriesInsideShare(root, relativePath, 0o700)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return fmt.Errorf("encode one-way apply intent: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(resolved), ".one-way-intent-")
	if err != nil {
		return fmt.Errorf("create one-way apply intent: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure one-way apply intent: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write one-way apply intent: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush one-way apply intent: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close one-way apply intent: %w", err)
	}
	if err := os.Link(temporaryPath, resolved); err != nil {
		return fmt.Errorf("commit one-way apply intent: %w", err)
	}
	return nil
}

func validateOneWayApplyIntent(intent oneWayApplyIntent, request OneWayApplyRequest) error {
	revision := request.Prepared.SourceRevision
	if intent.ShareID != revision.ShareID || intent.RevisionID != revision.ID || intent.RelativePath != revision.RelativePath ||
		intent.Action != request.Prepared.Change.Action || intent.ExpectedCurrentRevisionID != request.Prepared.TargetRevisionID ||
		intent.TombstoneID != request.TombstoneID || !intent.TombstoneExpiresAt.Equal(request.TombstoneExpiresAt) || intent.AppliedAt.IsZero() {
		return fmt.Errorf("%w: durable intent does not match retry request", ErrOneWayApplyState)
	}
	return nil
}

func cleanupApplyArtifacts(root string, relativePaths ...string) error {
	for _, relativePath := range relativePaths {
		resolved, info, exists, err := filesystem.InspectInsideShare(root, relativePath)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to remove non-file apply artifact %s", relativePath)
		}
		if err := os.Remove(resolved); err != nil {
			return fmt.Errorf("remove apply artifact %s: %w", relativePath, err)
		}
	}
	return nil
}

func oneWayReadyRelativePath(revision core.Revision) string {
	return path.Join(DefaultOneWayIncomingDir, "one-way", oneWayArtifactKey(revision)+".ready")
}

func oneWayIntentRelativePath(revision core.Revision) string {
	return path.Join(DefaultOneWayIncomingDir, "one-way", oneWayArtifactKey(revision)+".intent")
}

func oneWayHistoryRelativePath(revision core.Revision) string {
	return path.Join(transfer.DefaultHistoryDir, "one-way", oneWayArtifactKey(revision), revision.RelativePath)
}

func oneWayArtifactKey(revision core.Revision) string {
	digest := sha256.Sum256([]byte(string(revision.ShareID) + "\x00" + string(revision.ID)))
	return hex.EncodeToString(digest[:])
}
