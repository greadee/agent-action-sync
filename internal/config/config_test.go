package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDefaultsAndValidate(t *testing.T) {
	cfg := Config{
		DeviceName: "HOME-DESKTOP",
		DataDir:    `C:\SyncGate`,
		Shares: []ShareConfig{{
			ID:       "drop",
			Name:     "Drop",
			RootPath: `C:\SyncGate\Drop`,
			Mode:     "upload_only",
		}},
	}

	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
	if cfg.LocalAPI.Host != DefaultLocalAPIHost {
		t.Fatalf("default local API host = %q", cfg.LocalAPI.Host)
	}
	if cfg.RuntimeMode != RuntimeModeProduction || cfg.Identity.Store != IdentityStoreWindows {
		t.Fatalf("identity defaults = runtime %q store %q", cfg.RuntimeMode, cfg.Identity.Store)
	}
	if cfg.Transfer.ChunkSizeBytes != 4*1024*1024 {
		t.Fatalf("default chunk size = %d", cfg.Transfer.ChunkSizeBytes)
	}
	share := cfg.Shares[0]
	if share.ScanIntervalSeconds != int(DefaultScanInterval.Seconds()) {
		t.Fatalf("default scan interval = %d", share.ScanIntervalSeconds)
	}
	if share.DeletionLimitCount != DefaultDeletionLimitCount || share.DeletionLimitPercent != DefaultDeletionLimitPercent {
		t.Fatalf("default deletion limits = %d/%d", share.DeletionLimitCount, share.DeletionLimitPercent)
	}
	if share.TargetDriftPolicy != DefaultTargetDriftPolicy {
		t.Fatalf("default target drift policy = %q", share.TargetDriftPolicy)
	}
}

func TestConfigRequiresExplicitDevelopmentIdentityOptIn(t *testing.T) {
	cfg := Config{
		DeviceName:  "DEV",
		DataDir:     t.TempDir(),
		RuntimeMode: RuntimeModeDevelopment,
		Identity:    IdentityConfig{Store: IdentityStoreDevelopment},
		Shares: []ShareConfig{{
			ID: "drop", Name: "Drop", RootPath: t.TempDir(), Mode: "one_way_source",
		}},
	}
	if err := cfg.ApplyDefaultsAndValidate(); err == nil {
		t.Fatal("expected development file identity store without explicit opt-in to fail")
	}
	cfg.Identity.AllowInsecureDevelopmentFile = true
	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		t.Fatalf("validate explicit development identity store: %v", err)
	}
}

func TestConfigRejectsDevelopmentIdentityInProduction(t *testing.T) {
	cfg := Config{
		DeviceName:  "PROD",
		DataDir:     t.TempDir(),
		RuntimeMode: RuntimeModeProduction,
		Identity: IdentityConfig{
			Store:                        IdentityStoreDevelopment,
			AllowInsecureDevelopmentFile: true,
		},
		Shares: []ShareConfig{{
			ID: "drop", Name: "Drop", RootPath: t.TempDir(), Mode: "one_way_source",
		}},
	}
	if err := cfg.ApplyDefaultsAndValidate(); err == nil {
		t.Fatal("expected production development identity store to fail")
	}
}

func TestConfigRejectsNonLoopbackLocalAPI(t *testing.T) {
	cfg := Config{
		DeviceName: "HOME-DESKTOP",
		DataDir:    `C:\SyncGate`,
		LocalAPI:   LocalAPIConfig{Host: "0.0.0.0", Port: 47820},
		Shares: []ShareConfig{{
			ID:       "drop",
			Name:     "Drop",
			RootPath: `C:\SyncGate\Drop`,
			Mode:     "upload_only",
		}},
	}

	if err := cfg.ApplyDefaultsAndValidate(); err == nil {
		t.Fatal("expected non-loopback local API host to be rejected")
	}
}

func TestConfigAllowsUnregisteredFirstRunNode(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		DeviceName: "NEW-DESKTOP",
		DataDir:    filepath.Join(root, "data"),
		Node: NodeConfig{
			LogDir:          filepath.Join(root, "logs"),
			RuntimeCacheDir: filepath.Join(root, "cache"),
			WorktreeRoot:    filepath.Join(root, "worktrees"),
			LifecycleMode:   "foreground",
		},
	}
	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		t.Fatalf("validate empty first-run node: %v", err)
	}
	if len(cfg.Shares) != 0 {
		t.Fatalf("first-run shares = %d, want 0", len(cfg.Shares))
	}
}

func TestConfigRequiresCompleteDesktopPathSet(t *testing.T) {
	root := t.TempDir()
	cfg := Config{DeviceName: "DESKTOP", DataDir: filepath.Join(root, "data"), Node: NodeConfig{LogDir: filepath.Join(root, "logs")}}
	if err := cfg.ApplyDefaultsAndValidate(); err == nil {
		t.Fatal("expected partial desktop path configuration to fail")
	}
}

func TestConfigRejectsOverlappingDesktopRoots(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	cfg := Config{
		DeviceName: "DESKTOP", DataDir: dataDir,
		Node: NodeConfig{
			LogDir: dataDir, RuntimeCacheDir: filepath.Join(root, "cache"),
			WorktreeRoot: filepath.Join(root, "worktrees"), LifecycleMode: "foreground",
		},
	}
	if err := cfg.ApplyDefaultsAndValidate(); err == nil {
		t.Fatal("expected overlapping data and log roots to fail")
	}
}

func TestConfigExecutionIsDisabledByDefaultAndRequiresPreflight(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		DeviceName: "DESKTOP", DataDir: filepath.Join(root, "data"),
		Node: NodeConfig{
			LogDir: filepath.Join(root, "logs"), RuntimeCacheDir: filepath.Join(root, "cache"),
			WorktreeRoot: filepath.Join(root, "worktrees"), LifecycleMode: "foreground",
		},
	}
	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		t.Fatalf("validate disabled execution: %v", err)
	}
	if cfg.Node.Execution.Enabled {
		t.Fatal("execution must be disabled by default")
	}
	if cfg.Node.Execution.MaxConcurrent != 1 {
		t.Fatalf("default execution ceiling = %d", cfg.Node.Execution.MaxConcurrent)
	}
	cfg.Node.Execution.MaxConcurrent = 3
	if err := cfg.ApplyDefaultsAndValidate(); err == nil {
		t.Fatal("expected disabled execution with an unsafe ceiling to fail")
	}
	cfg.Node.Execution.MaxConcurrent = 1
	cfg.Node.Execution.Enabled = true
	if err := cfg.ApplyDefaultsAndValidate(); err == nil {
		t.Fatal("expected execution without provider/runtime/preflight to fail")
	}
	cfg.Node.Execution = ExecutionConfig{
		Enabled: true, ProviderID: "codex", RuntimeExecutable: filepath.Join(root, "codex.exe"),
		PreflightReceipt: strings.Repeat("a", 64),
	}
	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		t.Fatalf("validate preflight-bound execution: %v", err)
	}
}

func TestConfigRejectsOversizedFilesAndShareInventories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", MaxConfigBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(context.Background(), path); err == nil {
		t.Fatal("expected oversized config file to fail")
	}

	cfg := Config{DeviceName: "DESKTOP", DataDir: t.TempDir(), Shares: make([]ShareConfig, MaxConfiguredShares+1)}
	if err := cfg.ApplyDefaultsAndValidate(); err == nil {
		t.Fatal("expected oversized share inventory to fail")
	}
}

func TestConfigValidatesPerShareOneWaySettings(t *testing.T) {
	cfg := Config{
		DeviceName: "HOME-DESKTOP",
		DataDir:    `C:\SyncGate`,
		Shares: []ShareConfig{{
			ID:                   "drop",
			Name:                 "Drop",
			RootPath:             `C:\SyncGate\Drop`,
			Mode:                 "one_way_target",
			IgnorePatterns:       []string{" *.tmp ", "cache/**"},
			ScanIntervalSeconds:  30,
			DeletionLimitCount:   4,
			DeletionLimitPercent: 50,
			TargetDriftPolicy:    "preserve_conflict_copy",
		}},
	}

	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
	if cfg.Shares[0].IgnorePatterns[0] != "*.tmp" {
		t.Fatalf("ignore pattern was not normalized: %#v", cfg.Shares[0].IgnorePatterns)
	}
}

func TestConfigRejectsUnsafePerShareSettings(t *testing.T) {
	tests := []struct {
		name  string
		share ShareConfig
	}{
		{
			name: "bad scan interval",
			share: ShareConfig{
				ScanIntervalSeconds: -1,
			},
		},
		{
			name: "bad deletion percent",
			share: ShareConfig{
				DeletionLimitPercent: 101,
			},
		},
		{
			name: "bad drift policy",
			share: ShareConfig{
				TargetDriftPolicy: "merge",
			},
		},
		{
			name: "unsafe ignore pattern",
			share: ShareConfig{
				IgnorePatterns: []string{"../private/**"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			share := ShareConfig{
				ID:       "drop",
				Name:     "Drop",
				RootPath: `C:\SyncGate\Drop`,
				Mode:     "one_way_source",
			}
			if test.share.ScanIntervalSeconds != 0 {
				share.ScanIntervalSeconds = test.share.ScanIntervalSeconds
			}
			if test.share.DeletionLimitPercent != 0 {
				share.DeletionLimitPercent = test.share.DeletionLimitPercent
			}
			if test.share.TargetDriftPolicy != "" {
				share.TargetDriftPolicy = test.share.TargetDriftPolicy
			}
			if test.share.IgnorePatterns != nil {
				share.IgnorePatterns = test.share.IgnorePatterns
			}

			cfg := Config{
				DeviceName: "HOME-DESKTOP",
				DataDir:    `C:\SyncGate`,
				Shares:     []ShareConfig{share},
			}
			if err := cfg.ApplyDefaultsAndValidate(); err == nil {
				t.Fatal("expected invalid per-share settings to be rejected")
			}
		})
	}
}
