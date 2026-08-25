package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"syncgate/internal/buildinfo"
	"syncgate/internal/config"
)

const ControlStateFileName = "desktop-control-state.json"

var ErrIncompatibleDowngrade = errors.New("desktop control state was created by an incompatible newer node")

type ControlState struct {
	SchemaVersion        int       `json:"schema_version"`
	ControlLayoutVersion int       `json:"control_layout_version"`
	InitializedByVersion string    `json:"initialized_by_version"`
	LastPreparedVersion  string    `json:"last_prepared_version"`
	InitializedAt        time.Time `json:"initialized_at"`
	LastPreparedAt       time.Time `json:"last_prepared_at"`
}

type InitializationResult struct {
	Roots         Roots        `json:"roots"`
	ConfigCreated bool         `json:"config_created"`
	StateCreated  bool         `json:"state_created"`
	State         ControlState `json:"state"`
}

func Initialize(ctx context.Context, roots Roots, manifest buildinfo.Manifest, now time.Time) (InitializationResult, error) {
	if ctx == nil {
		return InitializationResult{}, errors.New("initialization context is required")
	}
	if err := ctx.Err(); err != nil {
		return InitializationResult{}, err
	}
	validated, err := validateRoots(roots)
	if err != nil {
		return InitializationResult{}, fmt.Errorf("validate desktop roots: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return InitializationResult{}, fmt.Errorf("validate build manifest: %w", err)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	for _, directory := range []string{validated.ConfigDir, validated.DataDir, validated.LogDir, validated.RuntimeCacheDir, validated.WorktreeRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return InitializationResult{}, fmt.Errorf("create desktop directory: %w", err)
		}
	}

	result := InitializationResult{Roots: validated}
	if _, err := os.Stat(validated.ConfigPath); errors.Is(err, os.ErrNotExist) {
		deviceName, hostErr := os.Hostname()
		if hostErr != nil || deviceName == "" {
			deviceName = "SyncGate Desktop"
		}
		cfg := config.Config{
			DeviceName: deviceName,
			DataDir:    validated.DataDir,
			Node: config.NodeConfig{
				LogDir: validated.LogDir, RuntimeCacheDir: validated.RuntimeCacheDir,
				WorktreeRoot: validated.WorktreeRoot, LifecycleMode: "foreground",
			},
			RuntimeMode: config.RuntimeModeProduction,
			Identity:    config.IdentityConfig{Store: config.IdentityStoreWindows},
			LocalAPI:    config.LocalAPIConfig{Host: config.DefaultLocalAPIHost, Port: config.DefaultLocalAPIPort},
			Transfer: config.TransferConfig{
				ChunkSizeBytes: 4 * 1024 * 1024, MaxParallelTransfers: config.DefaultParallelTransfers,
			},
			Shares: []config.ShareConfig{},
		}
		if err := cfg.ApplyDefaultsAndValidate(); err != nil {
			return InitializationResult{}, fmt.Errorf("build first-run config: %w", err)
		}
		if err := writeJSONAtomic(validated.ConfigPath, cfg, 0o600); err != nil {
			return InitializationResult{}, fmt.Errorf("write first-run config: %w", err)
		}
		result.ConfigCreated = true
	} else if err != nil {
		return InitializationResult{}, fmt.Errorf("inspect desktop config: %w", err)
	} else {
		cfg, err := config.LoadFile(ctx, validated.ConfigPath)
		if err != nil {
			return InitializationResult{}, fmt.Errorf("validate existing desktop config: %w", err)
		}
		if !samePath(cfg.DataDir, validated.DataDir) || !samePath(cfg.Node.LogDir, validated.LogDir) ||
			!samePath(cfg.Node.RuntimeCacheDir, validated.RuntimeCacheDir) || !samePath(cfg.Node.WorktreeRoot, validated.WorktreeRoot) {
			return InitializationResult{}, errors.New("existing desktop config roots do not match the selected node root; use the original root until an explicit relocation is completed")
		}
	}

	statePath := filepath.Join(validated.DataDir, ControlStateFileName)
	state, created, err := prepareControlState(statePath, manifest, now)
	if err != nil {
		return InitializationResult{}, err
	}
	result.State = state
	result.StateCreated = created
	return result, nil
}

func prepareControlState(path string, manifest buildinfo.Manifest, now time.Time) (ControlState, bool, error) {
	state := ControlState{}
	raw, err := os.ReadFile(path)
	created := errors.Is(err, os.ErrNotExist)
	if err != nil && !created {
		return ControlState{}, false, fmt.Errorf("read desktop control state: %w", err)
	}
	if created {
		state = ControlState{
			SchemaVersion: 1, ControlLayoutVersion: manifest.ControlLayoutVersion,
			InitializedByVersion: manifest.Version, InitializedAt: now,
		}
	} else {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&state); err != nil {
			return ControlState{}, false, fmt.Errorf("parse desktop control state: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return ControlState{}, false, errors.New("desktop control state contains trailing data")
		}
		if state.SchemaVersion != 1 || state.ControlLayoutVersion < 1 || state.InitializedByVersion == "" || state.InitializedAt.IsZero() {
			return ControlState{}, false, errors.New("desktop control state is invalid")
		}
		if state.ControlLayoutVersion > manifest.ControlLayoutVersion || state.ControlLayoutVersion < manifest.MinimumControlLayoutVersion {
			return ControlState{}, false, fmt.Errorf("%w: state layout=%d binary supports=%d..%d", ErrIncompatibleDowngrade, state.ControlLayoutVersion, manifest.MinimumControlLayoutVersion, manifest.ControlLayoutVersion)
		}
		if state.ControlLayoutVersion < manifest.ControlLayoutVersion {
			state.ControlLayoutVersion = manifest.ControlLayoutVersion
		}
	}
	state.LastPreparedVersion = manifest.Version
	state.LastPreparedAt = now
	if err := writeJSONAtomic(path, state, 0o600); err != nil {
		return ControlState{}, false, fmt.Errorf("write desktop control state: %w", err)
	}
	return state, created, nil
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".syncgate-*.tmp")
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
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
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
