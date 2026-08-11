package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	syncengine "syncgate/internal/sync"
)

func TestPrintDiagnosticsIncludesRequiredSections(t *testing.T) {
	report := syncengine.DiagnosticReport{
		GeneratedAt: time.Unix(100, 0).UTC(),
		RecentScans: []syncengine.ScanDiagnostic{
			{ShareID: "share-old", Trigger: syncengine.ScanTriggerPeriodic, Committed: true, FinishedAt: time.Unix(101, 0).UTC()},
			{ShareID: "share-1", Trigger: syncengine.ScanTriggerManual, Blocked: true, Revisions: 2, Reasons: []string{"blocked by deletion guard"}, FinishedAt: time.Unix(102, 0).UTC()},
		},
		Work: []syncengine.WorkDiagnostic{{
			JobID:        "job-1",
			TransferID:   core.TransferID("transfer-1"),
			ShareID:      "share-1",
			RelativePath: "notes.txt",
			State:        core.OneWayJobRetryWait,
			RetryCount:   1,
			LastError:    "temporary network error",
		}},
		IgnoredPaths: []syncengine.IgnoreDiagnostic{{
			ShareID:      "share-1",
			RelativePath: "debug.log",
			Pattern:      "*.log",
		}},
	}

	var out bytes.Buffer
	printDiagnostics(&out, report, 1)
	got := out.String()
	for _, want := range []string{"recent_scans:", "pending_or_blocked_work:", "ignored_paths:", "share=share-1", "state=retry_wait", `pattern="*.log"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "share-old") {
		t.Fatalf("diagnostics output did not apply recent limit:\n%s", got)
	}
}

func TestParsePairingGrantRequiresExplicitSupportedCapabilities(t *testing.T) {
	grant, err := parsePairingGrant("drop=sync,upload", true)
	if err != nil {
		t.Fatalf("parsePairingGrant: %v", err)
	}
	if grant.ShareID != "drop" || !grant.LANOnly || len(grant.Capabilities) != 2 {
		t.Fatalf("grant = %+v", grant)
	}
	for _, invalid := range []string{"drop=", "=read", "drop=remote_access", "drop=read,read"} {
		if _, err := parsePairingGrant(invalid, true); err == nil {
			t.Fatalf("expected grant %q to fail", invalid)
		}
	}
}
