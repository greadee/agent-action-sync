package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/buildinfo"
	"syncgate/internal/config"
)

func TestSettingsAreValidatedBeforeExplicitActivation(t *testing.T) {
	manager := initializedSettingsManager(t)
	badHost := "0.0.0.0"
	if _, err := manager.Stage(context.Background(), SettingsMutation{APIHost: &badHost}); err == nil {
		t.Fatal("expected non-loopback staged host to fail")
	}
	name := "STAGED-DESKTOP"
	port := 49123
	staged, err := manager.Stage(context.Background(), SettingsMutation{DeviceName: &name, APIPort: &port})
	if err != nil {
		t.Fatalf("stage settings: %v", err)
	}
	active, _, err := manager.Active(context.Background())
	if err != nil || active.DeviceName == name || active.LocalAPI.Port == port {
		t.Fatalf("staging changed active config: %#v err=%v", active, err)
	}
	if _, err := manager.Apply(context.Background(), strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected wrong confirmation to fail")
	}
	view, err := manager.Apply(context.Background(), staged.Confirmation)
	if err != nil {
		t.Fatalf("apply settings: %v", err)
	}
	if view.DeviceName != name || view.LocalAPI.Port != port {
		t.Fatalf("activated settings = %#v", view)
	}
	if _, err := os.Stat(filepath.Join(manager.Base.ConfigDir, PreviousConfigFileName)); err != nil {
		t.Fatalf("previous settings backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(manager.Base.ConfigDir, PendingConfigFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending settings were not consumed: %v", err)
	}
}

func TestShareRegistrationIsBoundedAndStaged(t *testing.T) {
	manager := initializedSettingsManager(t)
	shareRoot := t.TempDir()
	staged, err := manager.StageShare(context.Background(), config.ShareConfig{
		ID: "project-one", Name: "Project One", RootPath: shareRoot, Mode: "one_way_source",
	})
	if err != nil {
		t.Fatalf("stage share: %v", err)
	}
	active, _, _ := manager.Active(context.Background())
	if len(active.Shares) != 0 {
		t.Fatal("share registration activated before confirmation")
	}
	view, err := manager.Apply(context.Background(), staged.Confirmation)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Shares) != 1 || view.Shares[0].ID != "project-one" || view.Shares[0].RootPath != shareRoot {
		t.Fatalf("share settings = %#v", view.Shares)
	}
	if _, err := manager.StageShare(context.Background(), config.ShareConfig{ID: "project-one", Name: "Duplicate", RootPath: shareRoot, Mode: "read_only"}); err == nil {
		t.Fatal("expected duplicate share ID to fail")
	}
}

func TestPreflightRootRelocationPreservesControlState(t *testing.T) {
	manager := initializedSettingsManager(t)
	newData := filepath.Join(t.TempDir(), "data")
	staged, err := manager.Stage(context.Background(), SettingsMutation{DataDir: &newData})
	if err != nil {
		t.Fatal(err)
	}
	view, err := manager.Apply(context.Background(), staged.Confirmation)
	if err != nil {
		t.Fatalf("apply data relocation: %v", err)
	}
	if view.Roots.DataDir != newData {
		t.Fatalf("relocated data root = %q", view.Roots.DataDir)
	}
	if _, err := os.Stat(filepath.Join(newData, ControlStateFileName)); err != nil {
		t.Fatalf("relocated control state: %v", err)
	}
	result, err := Initialize(context.Background(), manager.Base, buildinfo.Current(), time.Now().UTC())
	if err != nil || result.Roots.DataDir != newData {
		t.Fatalf("initialize relocated node = %#v err=%v", result, err)
	}
}

func TestRelocationRefusesExistingControlDatabase(t *testing.T) {
	manager := initializedSettingsManager(t)
	_, activeRoots, err := manager.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeRoots.DataDir, "syncgate.db"), []byte("control-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	newData := filepath.Join(t.TempDir(), "data")
	staged, err := manager.Stage(context.Background(), SettingsMutation{DataDir: &newData})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(context.Background(), staged.Confirmation); !errors.Is(err, ErrUnsafeRelocation) {
		t.Fatalf("relocation error = %v", err)
	}
	active, roots, err := manager.Active(context.Background())
	if err != nil || active.DataDir != activeRoots.DataDir || roots.DataDir != activeRoots.DataDir {
		t.Fatalf("unsafe relocation changed active roots: %#v %#v err=%v", active, roots, err)
	}
}

func initializedSettingsManager(t *testing.T) SettingsManager {
	t.Helper()
	base, err := RootsUnder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(context.Background(), base, buildinfo.Current(), time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	return SettingsManager{Base: base}
}
