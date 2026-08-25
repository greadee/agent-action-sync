package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/buildinfo"
	"syncgate/internal/config"
)

func TestInitializeCreatesSeparatedFirstRunState(t *testing.T) {
	roots, err := RootsUnder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	result, err := Initialize(context.Background(), roots, buildinfo.Current(), now)
	if err != nil {
		t.Fatalf("initialize desktop node: %v", err)
	}
	if !result.ConfigCreated || !result.StateCreated {
		t.Fatalf("initialization flags = config:%t state:%t", result.ConfigCreated, result.StateCreated)
	}
	for _, directory := range []string{roots.ConfigDir, roots.DataDir, roots.LogDir, roots.RuntimeCacheDir, roots.WorktreeRoot} {
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory %q was not created: %v", directory, err)
		}
	}
	cfg, err := config.LoadFile(context.Background(), roots.ConfigPath)
	if err != nil {
		t.Fatalf("load generated config: %v", err)
	}
	if cfg.DataDir != roots.DataDir || cfg.Node.LogDir != roots.LogDir || cfg.Node.RuntimeCacheDir != roots.RuntimeCacheDir || cfg.Node.WorktreeRoot != roots.WorktreeRoot {
		t.Fatalf("generated paths = %#v", cfg)
	}
	if cfg.Node.LifecycleMode != "foreground" || len(cfg.Shares) != 0 {
		t.Fatalf("generated lifecycle/shares = %q/%d", cfg.Node.LifecycleMode, len(cfg.Shares))
	}
}

func TestInitializeUpgradePreservesConfigControlDataAndWorktrees(t *testing.T) {
	roots, err := RootsUnder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstTime := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	if _, err := Initialize(context.Background(), roots, buildinfo.Current(), firstTime); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(context.Background(), roots.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DeviceName = "PRESERVED-NODE"
	if err := writeJSONAtomic(roots.ConfigPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{
		filepath.Join(roots.DataDir, "syncgate.db"):               "sqlite-control-state",
		filepath.Join(roots.WorktreeRoot, "project", "README.md"): "project-worktree",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := buildinfo.Current()
	manifest.Version = "0.1.0"
	result, err := Initialize(context.Background(), roots, manifest, firstTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("prepare upgrade: %v", err)
	}
	if result.ConfigCreated || result.StateCreated || result.State.InitializedAt != firstTime {
		t.Fatalf("upgrade result = %#v", result)
	}
	preserved, err := config.LoadFile(context.Background(), roots.ConfigPath)
	if err != nil || preserved.DeviceName != "PRESERVED-NODE" {
		t.Fatalf("config was not preserved: %#v err=%v", preserved, err)
	}
	for path, expected := range map[string]string{
		filepath.Join(roots.DataDir, "syncgate.db"):               "sqlite-control-state",
		filepath.Join(roots.WorktreeRoot, "project", "README.md"): "project-worktree",
	} {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != expected {
			t.Fatalf("preserved file %q = %q err=%v", path, actual, err)
		}
	}
}

func TestInitializeRejectsIncompatibleDowngradeWithoutChangingState(t *testing.T) {
	roots, err := RootsUnder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	if _, err := Initialize(context.Background(), roots, buildinfo.Current(), now); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(roots.DataDir, ControlStateFileName)
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state ControlState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.ControlLayoutVersion = buildinfo.ControlLayoutVersion + 1
	if err := writeJSONAtomic(statePath, state, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Initialize(context.Background(), roots, buildinfo.Current(), now.Add(time.Hour))
	if !errors.Is(err, ErrIncompatibleDowngrade) {
		t.Fatalf("downgrade error = %v", err)
	}
	after, readErr := os.ReadFile(statePath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("incompatible downgrade changed state: read=%v", readErr)
	}
}

func TestInitializeRejectsConfigRootDriftBeforePreparingState(t *testing.T) {
	roots, err := RootsUnder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	if _, err := Initialize(context.Background(), roots, buildinfo.Current(), now); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(context.Background(), roots.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Node.WorktreeRoot = filepath.Join(filepath.Dir(roots.WorktreeRoot), "moved-worktrees")
	if err := writeJSONAtomic(roots.ConfigPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(roots.DataDir, ControlStateFileName)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(context.Background(), roots, buildinfo.Current(), now.Add(time.Hour)); err == nil {
		t.Fatal("expected root drift to fail")
	}
	after, err := os.ReadFile(statePath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("root drift changed control state: %v", err)
	}
}
