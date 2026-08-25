package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"syncgate/internal/config"
	syncengine "syncgate/internal/sync"
)

const (
	PendingConfigFileName  = "config.pending.json"
	PreviousConfigFileName = "config.previous.json"
	maxConfigBytes         = 1 << 20
)

var (
	ErrNoPendingSettings = errors.New("no pending desktop settings")
	ErrNodeMustBeStopped = errors.New("desktop node must be stopped before settings activation")
	ErrUnsafeRelocation  = errors.New("mutable roots can only be relocated before control data or worktrees exist")
)

type SettingsMutation struct {
	DeviceName      *string
	APIHost         *string
	APIPort         *int
	DataDir         *string
	LogDir          *string
	RuntimeCacheDir *string
	WorktreeRoot    *string
}

type SettingsView struct {
	DeviceName string                `json:"device_name"`
	Roots      Roots                 `json:"roots"`
	LocalAPI   config.LocalAPIConfig `json:"local_api"`
	Identity   IdentitySettings      `json:"identity"`
	Execution  ExecutionSettings     `json:"execution"`
	Shares     []ShareSettings       `json:"shares"`
}

type IdentitySettings struct {
	Store string `json:"store"`
}

type ExecutionSettings struct {
	Enabled                    bool   `json:"enabled"`
	ProviderID                 string `json:"provider_id,omitempty"`
	RuntimeExecutable          string `json:"runtime_executable,omitempty"`
	PreflightReceiptConfigured bool   `json:"preflight_receipt_configured"`
}

type ShareSettings struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RootPath string `json:"root_path"`
	Mode     string `json:"mode"`
}

type StagedSettings struct {
	Confirmation string       `json:"confirmation"`
	Settings     SettingsView `json:"settings"`
}

type SettingsManager struct {
	Base Roots
}

func (manager SettingsManager) Active(ctx context.Context) (config.Config, Roots, error) {
	if ctx == nil {
		return config.Config{}, Roots{}, errors.New("settings context is required")
	}
	cfg, err := config.LoadFile(ctx, manager.Base.ConfigPath)
	if err != nil {
		return config.Config{}, Roots{}, err
	}
	roots, err := RootsFromConfig(manager.Base, cfg)
	if err != nil {
		return config.Config{}, Roots{}, err
	}
	return cfg, roots, nil
}

func (manager SettingsManager) View(ctx context.Context) (SettingsView, error) {
	cfg, roots, err := manager.Active(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	return settingsView(cfg, roots), nil
}

func (manager SettingsManager) Stage(ctx context.Context, mutation SettingsMutation) (StagedSettings, error) {
	cfg, _, err := manager.candidate(ctx)
	if err != nil {
		return StagedSettings{}, err
	}
	changed := false
	applyString := func(target *string, value *string) {
		if value != nil {
			*target = strings.TrimSpace(*value)
			changed = true
		}
	}
	applyString(&cfg.DeviceName, mutation.DeviceName)
	applyString(&cfg.LocalAPI.Host, mutation.APIHost)
	if mutation.APIPort != nil {
		cfg.LocalAPI.Port = *mutation.APIPort
		changed = true
	}
	applyString(&cfg.DataDir, mutation.DataDir)
	applyString(&cfg.Node.LogDir, mutation.LogDir)
	applyString(&cfg.Node.RuntimeCacheDir, mutation.RuntimeCacheDir)
	applyString(&cfg.Node.WorktreeRoot, mutation.WorktreeRoot)
	if !changed {
		return StagedSettings{}, errors.New("at least one settings change is required")
	}
	return manager.writePending(cfg)
}

func (manager SettingsManager) StageShare(ctx context.Context, share config.ShareConfig) (StagedSettings, error) {
	cfg, roots, err := manager.candidate(ctx)
	if err != nil {
		return StagedSettings{}, err
	}
	share.ID = strings.TrimSpace(share.ID)
	share.Name = strings.TrimSpace(share.Name)
	share.RootPath = filepath.Clean(strings.TrimSpace(share.RootPath))
	share.Mode = strings.TrimSpace(share.Mode)
	if !filepath.IsAbs(share.RootPath) {
		return StagedSettings{}, errors.New("share root must be absolute")
	}
	if err := syncengine.CheckShareRoot(share.RootPath); err != nil {
		return StagedSettings{}, err
	}
	if pathsOverlap(roots.DataDir, share.RootPath) || pathsOverlap(roots.LogDir, share.RootPath) ||
		pathsOverlap(roots.RuntimeCacheDir, share.RootPath) || pathsOverlap(roots.WorktreeRoot, share.RootPath) {
		return StagedSettings{}, errors.New("share root must be separate from node-owned mutable roots")
	}
	for _, existing := range cfg.Shares {
		if existing.ID == share.ID {
			return StagedSettings{}, fmt.Errorf("share %q is already registered", share.ID)
		}
	}
	cfg.Shares = append(cfg.Shares, share)
	sort.Slice(cfg.Shares, func(left, right int) bool { return cfg.Shares[left].ID < cfg.Shares[right].ID })
	return manager.writePending(cfg)
}

func (manager SettingsManager) Apply(ctx context.Context, confirmation string) (SettingsView, error) {
	active, activeRoots, err := manager.Active(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	if err := ensureNodeStopped(active); err != nil {
		return SettingsView{}, err
	}
	pendingPath := filepath.Join(manager.Base.ConfigDir, PendingConfigFileName)
	candidate, err := config.LoadFile(ctx, pendingPath)
	if errors.Is(err, os.ErrNotExist) {
		return SettingsView{}, ErrNoPendingSettings
	}
	if err != nil {
		return SettingsView{}, fmt.Errorf("load pending settings: %w", err)
	}
	digest, err := configDigest(candidate)
	if err != nil {
		return SettingsView{}, err
	}
	if confirmation != digest {
		return SettingsView{}, errors.New("settings confirmation does not match the validated candidate")
	}
	candidateRoots, err := RootsFromConfig(manager.Base, candidate)
	if err != nil {
		return SettingsView{}, err
	}
	if err := prepareRelocation(activeRoots, candidateRoots); err != nil {
		return SettingsView{}, err
	}
	if err := writeJSONAtomic(filepath.Join(manager.Base.ConfigDir, PreviousConfigFileName), active, 0o600); err != nil {
		return SettingsView{}, fmt.Errorf("write previous settings backup: %w", err)
	}
	if err := writeJSONAtomic(manager.Base.ConfigPath, candidate, 0o600); err != nil {
		return SettingsView{}, fmt.Errorf("activate settings: %w", err)
	}
	if err := os.Remove(pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return SettingsView{}, fmt.Errorf("remove activated pending settings: %w", err)
	}
	return settingsView(candidate, candidateRoots), nil
}

func (manager SettingsManager) Discard() error {
	path := filepath.Join(manager.Base.ConfigDir, PendingConfigFileName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("discard pending settings: %w", err)
	}
	return nil
}

func (manager SettingsManager) candidate(ctx context.Context) (config.Config, Roots, error) {
	pendingPath := filepath.Join(manager.Base.ConfigDir, PendingConfigFileName)
	cfg, err := config.LoadFile(ctx, pendingPath)
	if err == nil {
		roots, rootErr := RootsFromConfig(manager.Base, cfg)
		return cfg, roots, rootErr
	}
	if !errors.Is(err, os.ErrNotExist) {
		return config.Config{}, Roots{}, fmt.Errorf("load pending settings: %w", err)
	}
	return manager.Active(ctx)
}

func (manager SettingsManager) writePending(cfg config.Config) (StagedSettings, error) {
	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		return StagedSettings{}, fmt.Errorf("validate pending settings: %w", err)
	}
	roots, err := RootsFromConfig(manager.Base, cfg)
	if err != nil {
		return StagedSettings{}, err
	}
	digest, err := configDigest(cfg)
	if err != nil {
		return StagedSettings{}, err
	}
	if err := writeJSONAtomic(filepath.Join(manager.Base.ConfigDir, PendingConfigFileName), cfg, 0o600); err != nil {
		return StagedSettings{}, fmt.Errorf("write pending settings: %w", err)
	}
	return StagedSettings{Confirmation: digest, Settings: settingsView(cfg, roots)}, nil
}

func configDigest(cfg config.Config) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal settings confirmation: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func settingsView(cfg config.Config, roots Roots) SettingsView {
	shares := make([]ShareSettings, 0, len(cfg.Shares))
	for _, share := range cfg.Shares {
		shares = append(shares, ShareSettings{ID: share.ID, Name: share.Name, RootPath: share.RootPath, Mode: share.Mode})
	}
	return SettingsView{
		DeviceName: cfg.DeviceName, Roots: roots, LocalAPI: cfg.LocalAPI,
		Identity: IdentitySettings{Store: cfg.Identity.Store},
		Execution: ExecutionSettings{
			Enabled: cfg.Node.Execution.Enabled, ProviderID: cfg.Node.Execution.ProviderID,
			RuntimeExecutable:          cfg.Node.Execution.RuntimeExecutable,
			PreflightReceiptConfigured: cfg.Node.Execution.PreflightReceipt != "",
		},
		Shares: shares,
	}
}

func ensureNodeStopped(cfg config.Config) error {
	address := net.JoinHostPort(cfg.LocalAPI.Host, strconv.Itoa(cfg.LocalAPI.Port))
	connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
	if err != nil {
		return nil
	}
	_ = connection.Close()
	return ErrNodeMustBeStopped
}

func prepareRelocation(active, candidate Roots) error {
	for _, directory := range []string{candidate.DataDir, candidate.LogDir, candidate.RuntimeCacheDir, candidate.WorktreeRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create candidate mutable root: %w", err)
		}
	}
	pairs := []struct {
		active, candidate string
		allowControlState bool
	}{
		{active.DataDir, candidate.DataDir, true},
		{active.LogDir, candidate.LogDir, false},
		{active.RuntimeCacheDir, candidate.RuntimeCacheDir, false},
		{active.WorktreeRoot, candidate.WorktreeRoot, false},
	}
	for _, pair := range pairs {
		if sameDesktopPath(pair.active, pair.candidate) {
			continue
		}
		if err := requireRelocatable(pair.active, pair.allowControlState); err != nil {
			return err
		}
		if err := requireRelocatable(pair.candidate, false); err != nil {
			return err
		}
	}
	if !sameDesktopPath(active.DataDir, candidate.DataDir) {
		statePath := filepath.Join(active.DataDir, ControlStateFileName)
		raw, err := os.ReadFile(statePath)
		if err != nil {
			return fmt.Errorf("read control state for relocation: %w", err)
		}
		if len(raw) > maxConfigBytes {
			return errors.New("control state exceeds relocation bound")
		}
		if err := writeBytesAtomic(filepath.Join(candidate.DataDir, ControlStateFileName), raw, 0o600); err != nil {
			return fmt.Errorf("copy control state for relocation: %w", err)
		}
	}
	return nil
}

func requireRelocatable(directory string, allowControlState bool) error {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect mutable root for relocation: %w", err)
	}
	for _, entry := range entries {
		if allowControlState && entry.Name() == ControlStateFileName && !entry.IsDir() {
			continue
		}
		return fmt.Errorf("%w: %s is not empty", ErrUnsafeRelocation, filepath.Base(directory))
	}
	return nil
}

func sameDesktopPath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func writeBytesAtomic(path string, value []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".syncgate-bytes-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}
