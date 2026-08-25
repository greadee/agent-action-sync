package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"syncgate/internal/buildinfo"
)

const DiagnosticsSchema = "syncgate.desktop-diagnostics.v1"

type SanitizedDiagnostics struct {
	Schema       string                `json:"schema"`
	GeneratedAt  time.Time             `json:"generated_at"`
	Build        DiagnosticsBuild      `json:"build"`
	Node         DiagnosticsNode       `json:"node"`
	Storage      DiagnosticsStorage    `json:"storage"`
	Preflight    DiagnosticsPreflight  `json:"preflight"`
	Credential   DiagnosticsCredential `json:"credential"`
	WarningCodes []string              `json:"warning_codes"`
}

type DiagnosticsBuild struct {
	Version              string `json:"version"`
	Commit               string `json:"commit"`
	Channel              string `json:"channel"`
	ControlLayoutVersion int    `json:"control_layout_version"`
}

type DiagnosticsNode struct {
	HealthStatus     string   `json:"health_status"`
	IdentityState    string   `json:"identity_state"`
	OpaqueNodeID     string   `json:"opaque_node_id,omitempty"`
	ConfigurationID  string   `json:"configuration_id"`
	ShareCount       int      `json:"share_count"`
	OpaqueShareIDs   []string `json:"opaque_share_ids"`
	ExecutionEnabled bool     `json:"execution_enabled"`
}

type DiagnosticsStorage struct {
	ControlStatePresent bool `json:"control_state_present"`
	DatabasePresent     bool `json:"database_present"`
	WorktreeCount       int  `json:"worktree_count"`
	WorktreeCountCapped bool `json:"worktree_count_capped"`
}

type DiagnosticsPreflight struct {
	Status     string   `json:"status"`
	CheckCodes []string `json:"check_codes"`
}

type DiagnosticsCredential struct {
	ProviderID string `json:"provider_id,omitempty"`
	Configured bool   `json:"configured"`
	Storage    string `json:"storage,omitempty"`
}

type DiagnosticsExporter struct {
	Base        Roots
	Credentials ProviderCredentials
	Now         func() time.Time
}

func (exporter DiagnosticsExporter) Build(ctx context.Context) (SanitizedDiagnostics, error) {
	cfg, roots, err := SettingsManager{Base: exporter.Base}.Active(ctx)
	if err != nil {
		return SanitizedDiagnostics{}, err
	}
	manifest := buildinfo.Current()
	if err := manifest.Validate(); err != nil {
		return SanitizedDiagnostics{}, err
	}
	now := time.Now().UTC()
	if exporter.Now != nil {
		now = exporter.Now().UTC()
	}
	configID, err := configDigest(cfg)
	if err != nil {
		return SanitizedDiagnostics{}, err
	}
	identity, identityErr := ReadIdentityStatus(roots.DataDir, cfg.Identity.Store)
	identityState := "invalid"
	opaqueNodeID := ""
	warnings := []string{}
	if identityErr == nil {
		identityState = identity.State
		if identity.DeviceID != "" {
			opaqueNodeID = opaqueDiagnosticsID("node", identity.DeviceID)
		}
	} else {
		warnings = append(warnings, "identity_metadata_invalid")
	}
	opaqueShares := make([]string, 0, len(cfg.Shares))
	for _, share := range cfg.Shares {
		opaqueShares = append(opaqueShares, opaqueDiagnosticsID("share", share.ID))
	}
	sort.Strings(opaqueShares)
	healthStatus := "stopped_or_unhealthy"
	if _, err := CheckHealth(ctx, cfg.LocalAPI.Host, cfg.LocalAPI.Port, 300*time.Millisecond); err == nil {
		healthStatus = "healthy"
	}
	worktreeCount, capped, err := boundedDirectoryCount(roots.WorktreeRoot, 256)
	if err != nil {
		warnings = append(warnings, "worktree_inventory_unavailable")
	}
	preflight := DiagnosticsPreflight{Status: "none", CheckCodes: []string{}}
	record, preflightErr := readExecutionPreflight(filepath.Join(roots.DataDir, ExecutionPreflightFileName))
	if preflightErr == nil {
		preflight.Status = "passed"
		if !now.Before(record.ExpiresAt) {
			preflight.Status = "expired"
		}
		preflight.CheckCodes = append([]string(nil), record.CheckCodes...)
	} else if !errors.Is(preflightErr, os.ErrNotExist) {
		preflight.Status = "invalid"
		warnings = append(warnings, "execution_preflight_invalid")
	}
	credential := DiagnosticsCredential{ProviderID: cfg.Node.Execution.ProviderID}
	if cfg.Node.Execution.ProviderID != "" {
		exporter.Credentials.ScopeRoot = roots.ConfigDir
		status, statusErr := exporter.Credentials.Status(cfg.Node.Execution.ProviderID)
		if statusErr == nil {
			credential.Configured = status.Configured
			credential.Storage = status.Storage
		} else {
			warnings = append(warnings, "provider_credential_status_unavailable")
		}
	}
	sort.Strings(warnings)
	return SanitizedDiagnostics{
		Schema: DiagnosticsSchema, GeneratedAt: now,
		Build: DiagnosticsBuild{Version: manifest.Version, Commit: manifest.Commit, Channel: manifest.Channel, ControlLayoutVersion: manifest.ControlLayoutVersion},
		Node: DiagnosticsNode{
			HealthStatus: healthStatus, IdentityState: identityState, OpaqueNodeID: opaqueNodeID,
			ConfigurationID: "config:" + configID[:24], ShareCount: len(cfg.Shares),
			OpaqueShareIDs: opaqueShares, ExecutionEnabled: cfg.Node.Execution.Enabled,
		},
		Storage: DiagnosticsStorage{
			ControlStatePresent: fileExists(filepath.Join(roots.DataDir, ControlStateFileName)),
			DatabasePresent:     fileExists(filepath.Join(roots.DataDir, "syncgate.db")),
			WorktreeCount:       worktreeCount, WorktreeCountCapped: capped,
		},
		Preflight: preflight, Credential: credential, WarningCodes: warnings,
	}, nil
}

func (exporter DiagnosticsExporter) Export(ctx context.Context, outputPath string) (SanitizedDiagnostics, error) {
	outputPath = filepath.Clean(strings.TrimSpace(outputPath))
	if !filepath.IsAbs(outputPath) || !strings.EqualFold(filepath.Ext(outputPath), ".json") {
		return SanitizedDiagnostics{}, errors.New("diagnostics output must be an absolute .json path")
	}
	report, err := exporter.Build(ctx)
	if err != nil {
		return SanitizedDiagnostics{}, err
	}
	if err := writeJSONAtomic(outputPath, report, 0o600); err != nil {
		return SanitizedDiagnostics{}, fmt.Errorf("write sanitized diagnostics: %w", err)
	}
	return report, nil
}

func opaqueDiagnosticsID(kind, value string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + value))
	return kind + ":" + hex.EncodeToString(digest[:12])
}

func boundedDirectoryCount(path string, limit int) (int, bool, error) {
	directory, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer directory.Close()
	names, err := directory.Readdirnames(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, false, err
	}
	if len(names) > limit {
		return limit, true, nil
	}
	return len(names), false, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
