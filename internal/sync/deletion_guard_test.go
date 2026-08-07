package sync

import (
	"strings"
	"testing"

	"syncgate/internal/core"
)

func TestCheckDeletionGuardAllowsDeletesWithinLimits(t *testing.T) {
	result := deletionResult(10, "a.txt")
	decision, err := CheckDeletionGuard(result, DeletionGuard{MaxCount: 2, MaxPercent: 20})
	if err != nil {
		t.Fatalf("CheckDeletionGuard: %v", err)
	}

	if !decision.Allowed {
		t.Fatalf("decision should allow deletes: %+v", decision)
	}
	if decision.DeletedCount != 1 || decision.PreviousActive != 10 || decision.DeletedPercent != 10 {
		t.Fatalf("decision counts = %+v", decision)
	}
	if len(decision.DeletedPaths) != 1 || decision.DeletedPaths[0] != "a.txt" {
		t.Fatalf("deleted paths = %+v", decision.DeletedPaths)
	}
}

func TestCheckDeletionGuardBlocksDeletesOverCountLimit(t *testing.T) {
	result := deletionResult(10, "a.txt", "b.txt", "c.txt")
	decision, err := CheckDeletionGuard(result, DeletionGuard{MaxCount: 2, MaxPercent: 100})
	if err != nil {
		t.Fatalf("CheckDeletionGuard: %v", err)
	}

	if decision.Allowed {
		t.Fatalf("decision should block deletes: %+v", decision)
	}
	if !strings.Contains(strings.Join(decision.Reasons, ","), "3 deletions exceeds max count 2") {
		t.Fatalf("reasons = %+v", decision.Reasons)
	}
}

func TestCheckDeletionGuardBlocksDeletesOverPercentLimit(t *testing.T) {
	result := deletionResult(10, "a.txt", "b.txt")
	decision, err := CheckDeletionGuard(result, DeletionGuard{MaxCount: 10, MaxPercent: 10})
	if err != nil {
		t.Fatalf("CheckDeletionGuard: %v", err)
	}

	if decision.Allowed {
		t.Fatalf("decision should block deletes: %+v", decision)
	}
	if decision.DeletedPercent != 20 {
		t.Fatalf("deleted percent = %d", decision.DeletedPercent)
	}
	if !strings.Contains(strings.Join(decision.Reasons, ","), "20% deletions exceeds max percent 10%") {
		t.Fatalf("reasons = %+v", decision.Reasons)
	}
}

func TestCheckDeletionGuardTreatsZeroLimitsAsDisabled(t *testing.T) {
	result := deletionResult(2, "a.txt", "b.txt")
	decision, err := CheckDeletionGuard(result, DeletionGuard{})
	if err != nil {
		t.Fatalf("CheckDeletionGuard: %v", err)
	}

	if !decision.Allowed {
		t.Fatalf("zero limits should be disabled: %+v", decision)
	}
}

func TestCheckDeletionGuardRejectsInvalidLimits(t *testing.T) {
	if _, err := CheckDeletionGuard(ReconcileResult{}, DeletionGuard{MaxCount: -1}); err == nil {
		t.Fatal("expected negative count to fail")
	}
	if _, err := CheckDeletionGuard(ReconcileResult{}, DeletionGuard{MaxPercent: 101}); err == nil {
		t.Fatal("expected percent over 100 to fail")
	}
}

func deletionResult(previousActive int, deletedPaths ...string) ReconcileResult {
	deleted := map[string]struct{}{}
	for _, path := range deletedPaths {
		deleted[path] = struct{}{}
	}

	changes := make([]Change, 0, previousActive)
	for i := 0; i < previousActive; i++ {
		path := string(rune('a'+i)) + ".txt"
		if i < len(deletedPaths) {
			path = deletedPaths[i]
		}
		previous := core.FileIndexEntry{RelativePath: path, EntryType: core.EntryFile}
		if _, ok := deleted[path]; ok {
			changes = append(changes, Change{Kind: ChangeDeleted, RelativePath: path, Previous: previous})
			continue
		}
		changes = append(changes, Change{Kind: ChangeUnchanged, RelativePath: path, Previous: previous, Current: previous})
	}
	return ReconcileResult{Changes: changes}
}
