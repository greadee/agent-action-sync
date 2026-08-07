package sync

import (
	"regexp"
	"strings"
	"time"

	"syncgate/internal/core"
)

var authorizationDiagnosticValue = regexp.MustCompile(`(?i)\bauthorization\b\s*[:=]\s*(?:bearer\s+)?(?:"[^"]*"|'[^']*'|\S+)`)
var sensitiveDiagnosticValue = regexp.MustCompile(`(?i)\b(bearer|token|secret|password|api[_-]?key|private[_-]?key)\b(?:\s*[:=]\s*|\s+)(?:"[^"]*"|'[^']*'|\S+)`)

type ScanDiagnostic struct {
	ShareID     core.ShareID      `json:"share_id"`
	Trigger     ScanTriggerReason `json:"trigger"`
	Started     bool              `json:"started"`
	Skipped     bool              `json:"skipped"`
	Unavailable bool              `json:"unavailable"`
	Blocked     bool              `json:"blocked"`
	Committed   bool              `json:"committed"`
	Revisions   int               `json:"revisions"`
	Tombstones  int               `json:"tombstones"`
	ScannedAt   time.Time         `json:"scanned_at,omitempty"`
	FinishedAt  time.Time         `json:"finished_at"`
	Error       string            `json:"error,omitempty"`
	Reasons     []string          `json:"reasons,omitempty"`
}

type WorkDiagnostic struct {
	JobID         string              `json:"job_id"`
	TransferID    core.TransferID     `json:"transfer_id"`
	ShareID       core.ShareID        `json:"share_id"`
	RevisionID    core.RevisionID     `json:"revision_id,omitempty"`
	RelativePath  string              `json:"relative_path"`
	State         core.OneWayJobState `json:"state"`
	RetryCount    int                 `json:"retry_count"`
	NextAttemptAt time.Time           `json:"next_attempt_at,omitempty"`
	LastError     string              `json:"last_error,omitempty"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

type IgnoreDiagnostic struct {
	ShareID      core.ShareID `json:"share_id"`
	RelativePath string       `json:"relative_path"`
	Pattern      string       `json:"pattern"`
}

type DiagnosticReport struct {
	GeneratedAt  time.Time          `json:"generated_at"`
	RecentScans  []ScanDiagnostic   `json:"recent_scans"`
	Work         []WorkDiagnostic   `json:"work"`
	IgnoredPaths []IgnoreDiagnostic `json:"ignored_paths"`
}

func NewScanDiagnostic(shareID core.ShareID, outcome ScanOutcome, finishedAt time.Time) ScanDiagnostic {
	diagnostic := ScanDiagnostic{
		ShareID:     shareID,
		Trigger:     outcome.Trigger.Reason,
		Started:     outcome.Started,
		Skipped:     outcome.Skipped,
		Unavailable: outcome.Unavailable,
		Blocked:     outcome.Result.Blocked,
		Committed:   outcome.Result.Committed,
		Revisions:   len(outcome.Result.Revisions),
		Tombstones:  len(outcome.Result.Tombstones),
		ScannedAt:   outcome.Result.Plan.Scan.ScannedAt,
		FinishedAt:  finishedAt.UTC(),
		Reasons:     append([]string{}, outcome.Result.Plan.DeletionDecision.Reasons...),
	}
	if outcome.Err != nil {
		diagnostic.Error = sanitizeDiagnosticText(outcome.Err.Error())
	}
	return diagnostic
}

func PendingOrBlockedWork(jobs []core.OneWayJob) []WorkDiagnostic {
	diagnostics := make([]WorkDiagnostic, 0, len(jobs))
	for _, job := range jobs {
		switch job.State {
		case core.OneWayJobQueued, core.OneWayJobRunning, core.OneWayJobRetryWait, core.OneWayJobPaused, core.OneWayJobFailed:
			diagnostics = append(diagnostics, WorkDiagnostic{
				JobID:         job.ID,
				TransferID:    job.TransferID,
				ShareID:       job.ShareID,
				RevisionID:    job.RevisionID,
				RelativePath:  job.RelativePath,
				State:         job.State,
				RetryCount:    job.RetryCount,
				NextAttemptAt: job.NextAttemptAt,
				LastError:     sanitizeDiagnosticText(job.LastError),
				UpdatedAt:     job.UpdatedAt,
			})
		}
	}
	return diagnostics
}

func ExplainIgnoredPaths(shareID core.ShareID, relativePaths, customPatterns []string) []IgnoreDiagnostic {
	patterns := append([]string{}, DefaultIgnorePatterns...)
	patterns = append(patterns, customPatterns...)
	diagnostics := make([]IgnoreDiagnostic, 0)
	for _, relativePath := range relativePaths {
		relativePath = strings.TrimSpace(relativePath)
		if relativePath == "" {
			continue
		}
		for _, pattern := range patterns {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			if matchIgnorePattern(relativePath, pattern) {
				diagnostics = append(diagnostics, IgnoreDiagnostic{
					ShareID:      shareID,
					RelativePath: relativePath,
					Pattern:      pattern,
				})
				break
			}
		}
	}
	return diagnostics
}

func sanitizeDiagnosticText(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	value = authorizationDiagnosticValue.ReplaceAllString(value, "authorization=[redacted]")
	value = sensitiveDiagnosticValue.ReplaceAllString(value, "$1=[redacted]")
	fields := strings.Fields(value)
	for i, field := range fields {
		trimmed := strings.Trim(field, `"'()[]{}:,;`)
		if looksLikeAbsolutePath(trimmed) {
			fields[i] = strings.Replace(field, trimmed, "[redacted-path]", 1)
		}
	}
	return strings.Join(fields, " ")
}

func looksLikeAbsolutePath(value string) bool {
	if strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, `//`) {
		return true
	}
	if len(value) < 3 {
		return false
	}
	return ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}
