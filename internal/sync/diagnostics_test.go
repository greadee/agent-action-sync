package sync

import (
	"errors"
	"testing"
	"time"

	"syncgate/internal/core"
)

func TestNewScanDiagnosticSummarizesBlockedScanWithoutSecrets(t *testing.T) {
	finishedAt := time.Unix(200, 0).UTC()
	outcome := ScanOutcome{
		Trigger: ScanRequest{Reason: ScanTriggerManual},
		Started: true,
		Result: ScanCommitResult{
			Blocked: true,
			Plan: ScanPlan{
				Scan: ScanResult{ScannedAt: time.Unix(100, 0).UTC()},
				DeletionDecision: DeletionGuardDecision{
					Reasons: []string{"3 deletions exceeds max count 2"},
				},
			},
			Revisions:  []core.Revision{{ID: "rev-1"}},
			Tombstones: nil,
		},
		Err: errors.New(`scan C:\Users\alex\SyncGate\Drop failed`),
	}

	got := NewScanDiagnostic("share-1", outcome, finishedAt)
	if got.ShareID != "share-1" || got.Trigger != ScanTriggerManual || !got.Blocked {
		t.Fatalf("unexpected scan diagnostic: %#v", got)
	}
	if got.Revisions != 1 || len(got.Reasons) != 1 {
		t.Fatalf("missing scan counts or reasons: %#v", got)
	}
	if got.Error != "scan [redacted-path] failed" {
		t.Fatalf("error was not sanitized: %q", got.Error)
	}
}

func TestPendingOrBlockedWorkFiltersCompletedAndSanitizesErrors(t *testing.T) {
	jobs := []core.OneWayJob{
		{ID: "queued", TransferID: "transfer-1", ShareID: "share-1", RelativePath: "queued.txt", State: core.OneWayJobQueued},
		{ID: "failed", TransferID: "transfer-2", ShareID: "share-1", RelativePath: "failed.txt", State: core.OneWayJobFailed, LastError: `open C:\secret\file.txt`},
		{ID: "done", TransferID: "transfer-3", ShareID: "share-1", RelativePath: "done.txt", State: core.OneWayJobCompleted},
	}

	got := PendingOrBlockedWork(jobs)
	if len(got) != 2 {
		t.Fatalf("pending/blocked work count = %d", len(got))
	}
	if got[1].LastError != "open [redacted-path]" {
		t.Fatalf("last error was not sanitized: %q", got[1].LastError)
	}
}

func TestExplainIgnoredPathsReportsMatchingPatterns(t *testing.T) {
	got := ExplainIgnoredPaths("share-1", []string{".sync-history/state.json", "notes.tmp", "keep.txt"}, []string{"*.tmp"})
	if len(got) != 2 {
		t.Fatalf("ignored path count = %d", len(got))
	}
	if got[0].Pattern != ".sync-history/**" || got[1].Pattern != "*.tmp" {
		t.Fatalf("unexpected ignore diagnostics: %#v", got)
	}
}
