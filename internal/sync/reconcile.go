package sync

import (
	"fmt"
	"sort"
	"strings"

	"syncgate/internal/core"
)

type ChangeKind string

const (
	ChangeAdded     ChangeKind = "added"
	ChangeModified  ChangeKind = "modified"
	ChangeDeleted   ChangeKind = "deleted"
	ChangeUnchanged ChangeKind = "unchanged"
)

type Change struct {
	Kind         ChangeKind
	RelativePath string
	Previous     core.FileIndexEntry
	Current      core.FileIndexEntry
}

type ReconcileResult struct {
	Changes []Change
}

func (result ReconcileResult) ByKind(kind ChangeKind) []Change {
	var changes []Change
	for _, change := range result.Changes {
		if change.Kind == kind {
			changes = append(changes, change)
		}
	}
	return changes
}

func ReconcileScan(previous, current []core.FileIndexEntry) (ReconcileResult, error) {
	previousByPath, err := indexEntries(previous, "previous")
	if err != nil {
		return ReconcileResult{}, err
	}
	currentByPath, err := indexEntries(current, "current")
	if err != nil {
		return ReconcileResult{}, err
	}

	paths := make(map[string]struct{}, len(previousByPath)+len(currentByPath))
	for path := range previousByPath {
		paths[path] = struct{}{}
	}
	for path := range currentByPath {
		paths[path] = struct{}{}
	}

	orderedPaths := make([]string, 0, len(paths))
	for path := range paths {
		orderedPaths = append(orderedPaths, path)
	}
	sort.Strings(orderedPaths)

	changes := make([]Change, 0, len(orderedPaths))
	for _, path := range orderedPaths {
		prior, hadPrior := previousByPath[path]
		now, hasNow := currentByPath[path]
		change := Change{RelativePath: path, Previous: prior, Current: now}

		switch {
		case !hadPrior && hasNow:
			change.Kind = ChangeAdded
		case hadPrior && !hasNow && !prior.IsDeleted:
			change.Kind = ChangeDeleted
		case hadPrior && !hasNow && prior.IsDeleted:
			change.Kind = ChangeUnchanged
		case prior.IsDeleted && hasNow:
			change.Kind = ChangeAdded
		case entriesEquivalent(prior, now):
			change.Kind = ChangeUnchanged
		default:
			change.Kind = ChangeModified
		}
		changes = append(changes, change)
	}

	return ReconcileResult{Changes: changes}, nil
}

func indexEntries(entries []core.FileIndexEntry, label string) (map[string]core.FileIndexEntry, error) {
	byPath := make(map[string]core.FileIndexEntry, len(entries))
	for _, entry := range entries {
		path := strings.TrimSpace(entry.RelativePath)
		if path == "" {
			return nil, fmt.Errorf("%s file index entry has empty relative path", label)
		}
		if _, exists := byPath[path]; exists {
			return nil, fmt.Errorf("%s file index entries contain duplicate path %s", label, path)
		}
		entry.RelativePath = path
		byPath[path] = entry
	}
	return byPath, nil
}

func entriesEquivalent(previous, current core.FileIndexEntry) bool {
	if previous.EntryType != current.EntryType {
		return false
	}
	if previous.IsDeleted != current.IsDeleted {
		return false
	}
	if previous.EntryType == core.EntryFile {
		if previous.Size != current.Size {
			return false
		}
		if previous.HashAlgorithm != current.HashAlgorithm {
			return false
		}
		return previous.ContentHash == current.ContentHash
	}
	if previous.EntryType == core.EntrySymlinkUnsupported {
		return previous.FileIdentity == current.FileIdentity
	}
	return true
}
