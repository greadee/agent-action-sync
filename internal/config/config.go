package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"syncgate/internal/transfer"
)

const (
	DefaultLocalAPIHost         = "127.0.0.1"
	DefaultLocalAPIPort         = 47820
	DefaultParallelTransfers    = 2
	DefaultScanInterval         = time.Minute
	DefaultDeletionLimitCount   = 100
	DefaultDeletionLimitPercent = 10
	DefaultTargetDriftPolicy    = "reject"
	RuntimeModeProduction       = "production"
	RuntimeModeDevelopment      = "development"
	IdentityStoreWindows        = "windows_credential_manager"
	IdentityStoreDevelopment    = "development_file"
	MaxConfigBytes              = 1 << 20
	MaxConfiguredShares         = 1000
)

type Config struct {
	DeviceName  string         `json:"device_name"`
	DataDir     string         `json:"data_dir"`
	Node        NodeConfig     `json:"node,omitempty"`
	RuntimeMode string         `json:"runtime_mode"`
	Identity    IdentityConfig `json:"identity"`
	LocalAPI    LocalAPIConfig `json:"local_api"`
	Transfer    TransferConfig `json:"transfer"`
	Shares      []ShareConfig  `json:"shares"`
}

type NodeConfig struct {
	LogDir          string          `json:"log_dir,omitempty"`
	RuntimeCacheDir string          `json:"runtime_cache_dir,omitempty"`
	WorktreeRoot    string          `json:"worktree_root,omitempty"`
	LifecycleMode   string          `json:"lifecycle_mode,omitempty"`
	Execution       ExecutionConfig `json:"execution,omitempty"`
}

type ExecutionConfig struct {
	Enabled           bool   `json:"enabled"`
	ProviderID        string `json:"provider_id,omitempty"`
	RuntimeExecutable string `json:"runtime_executable,omitempty"`
	PreflightReceipt  string `json:"preflight_receipt,omitempty"`
}

type IdentityConfig struct {
	Store                        string `json:"store"`
	AllowInsecureDevelopmentFile bool   `json:"allow_insecure_development_file"`
}

type LocalAPIConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type TransferConfig struct {
	ChunkSizeBytes       int64 `json:"chunk_size_bytes"`
	MaxParallelTransfers int   `json:"max_parallel_transfers"`
}

type ShareConfig struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	RootPath             string   `json:"root_path"`
	Mode                 string   `json:"mode"`
	IgnorePatterns       []string `json:"ignore_patterns"`
	ScanIntervalSeconds  int      `json:"scan_interval_seconds"`
	DeletionLimitCount   int      `json:"deletion_limit_count"`
	DeletionLimitPercent int      `json:"deletion_limit_percent"`
	TargetDriftPolicy    string   `json:"target_drift_policy"`
}

func LoadFile(ctx context.Context, path string) (Config, error) {
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}

	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if len(raw) > MaxConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d-byte limit", MaxConfigBytes)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config JSON: %w", err)
	}
	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg *Config) ApplyDefaultsAndValidate() error {
	cfg.DeviceName = strings.TrimSpace(cfg.DeviceName)
	cfg.DataDir = strings.TrimSpace(cfg.DataDir)
	cfg.Node.LogDir = strings.TrimSpace(cfg.Node.LogDir)
	cfg.Node.RuntimeCacheDir = strings.TrimSpace(cfg.Node.RuntimeCacheDir)
	cfg.Node.WorktreeRoot = strings.TrimSpace(cfg.Node.WorktreeRoot)
	cfg.Node.LifecycleMode = strings.TrimSpace(cfg.Node.LifecycleMode)
	cfg.Node.Execution.ProviderID = strings.TrimSpace(cfg.Node.Execution.ProviderID)
	cfg.Node.Execution.RuntimeExecutable = strings.TrimSpace(cfg.Node.Execution.RuntimeExecutable)
	cfg.Node.Execution.PreflightReceipt = strings.TrimSpace(cfg.Node.Execution.PreflightReceipt)
	cfg.RuntimeMode = strings.TrimSpace(cfg.RuntimeMode)
	cfg.Identity.Store = strings.TrimSpace(cfg.Identity.Store)

	if cfg.RuntimeMode == "" {
		cfg.RuntimeMode = RuntimeModeProduction
	}
	if cfg.Identity.Store == "" {
		cfg.Identity.Store = IdentityStoreWindows
	}

	if cfg.LocalAPI.Host == "" {
		cfg.LocalAPI.Host = DefaultLocalAPIHost
	}
	if cfg.LocalAPI.Port == 0 {
		cfg.LocalAPI.Port = DefaultLocalAPIPort
	}
	if cfg.Transfer.ChunkSizeBytes == 0 {
		cfg.Transfer.ChunkSizeBytes = transfer.DefaultChunkSize
	}
	if cfg.Transfer.MaxParallelTransfers == 0 {
		cfg.Transfer.MaxParallelTransfers = DefaultParallelTransfers
	}

	if cfg.DeviceName == "" {
		return errors.New("device_name is required")
	}
	if cfg.DataDir == "" {
		return errors.New("data_dir is required")
	}
	if err := cfg.Node.validate(cfg.DataDir); err != nil {
		return fmt.Errorf("node: %w", err)
	}
	switch cfg.RuntimeMode {
	case RuntimeModeProduction:
		if cfg.Identity.Store != IdentityStoreWindows {
			return fmt.Errorf("production runtime requires identity.store %q", IdentityStoreWindows)
		}
		if cfg.Identity.AllowInsecureDevelopmentFile {
			return errors.New("production runtime cannot allow insecure development identity storage")
		}
	case RuntimeModeDevelopment:
		switch cfg.Identity.Store {
		case IdentityStoreWindows:
		case IdentityStoreDevelopment:
			if !cfg.Identity.AllowInsecureDevelopmentFile {
				return errors.New("development file identity storage requires allow_insecure_development_file=true")
			}
		default:
			return fmt.Errorf("unsupported identity.store %q", cfg.Identity.Store)
		}
	default:
		return fmt.Errorf("unsupported runtime_mode %q", cfg.RuntimeMode)
	}
	if !isLoopbackHost(cfg.LocalAPI.Host) {
		return fmt.Errorf("local_api.host must be loopback, got %q", cfg.LocalAPI.Host)
	}
	if cfg.LocalAPI.Port < 1 || cfg.LocalAPI.Port > 65535 {
		return fmt.Errorf("local_api.port must be between 1 and 65535, got %d", cfg.LocalAPI.Port)
	}
	if cfg.Transfer.ChunkSizeBytes <= 0 {
		return fmt.Errorf("transfer.chunk_size_bytes must be positive, got %d", cfg.Transfer.ChunkSizeBytes)
	}
	if cfg.Transfer.MaxParallelTransfers < 1 {
		return fmt.Errorf("transfer.max_parallel_transfers must be positive, got %d", cfg.Transfer.MaxParallelTransfers)
	}
	seenShares := map[string]bool{}
	if len(cfg.Shares) > MaxConfiguredShares {
		return fmt.Errorf("shares exceeds %d-entry limit", MaxConfiguredShares)
	}
	for i := range cfg.Shares {
		if err := cfg.Shares[i].validate(seenShares); err != nil {
			return fmt.Errorf("shares[%d]: %w", i, err)
		}
	}

	return nil
}

func (node *NodeConfig) validate(dataDir string) error {
	configuredPaths := 0
	for _, path := range []string{node.LogDir, node.RuntimeCacheDir, node.WorktreeRoot} {
		if path != "" {
			configuredPaths++
		}
	}
	if configuredPaths != 0 && configuredPaths != 3 {
		return errors.New("log_dir, runtime_cache_dir, and worktree_root must be configured together")
	}
	if configuredPaths == 3 {
		cleanedDataDir := filepath.Clean(dataDir)
		if !filepath.IsAbs(cleanedDataDir) {
			return errors.New("data_dir must be absolute when desktop paths are configured")
		}
		configured := []struct {
			name string
			path string
		}{{name: "data_dir", path: cleanedDataDir}}
		paths := []struct {
			name  string
			value *string
		}{
			{name: "log_dir", value: &node.LogDir},
			{name: "runtime_cache_dir", value: &node.RuntimeCacheDir},
			{name: "worktree_root", value: &node.WorktreeRoot},
		}
		for _, path := range paths {
			cleaned := filepath.Clean(*path.value)
			if !filepath.IsAbs(cleaned) {
				return fmt.Errorf("%s must be absolute", path.name)
			}
			for _, previous := range configured {
				if configPathsOverlap(previous.path, cleaned) {
					return fmt.Errorf("%s must be separate from %s", path.name, previous.name)
				}
			}
			configured = append(configured, struct {
				name string
				path string
			}{name: path.name, path: cleaned})
			*path.value = cleaned
		}
	}
	if node.LifecycleMode == "" && configuredPaths == 3 {
		node.LifecycleMode = "foreground"
	}
	if node.LifecycleMode != "" && node.LifecycleMode != "foreground" {
		return fmt.Errorf("unsupported lifecycle_mode %q", node.LifecycleMode)
	}
	if err := node.Execution.validate(); err != nil {
		return fmt.Errorf("execution: %w", err)
	}
	return nil
}

func (execution *ExecutionConfig) validate() error {
	configured := execution.ProviderID != "" || execution.RuntimeExecutable != "" || execution.PreflightReceipt != ""
	if !configured && !execution.Enabled {
		return nil
	}
	if !configIdentifier(execution.ProviderID) {
		return errors.New("provider_id must be a lowercase identifier")
	}
	if !filepath.IsAbs(filepath.Clean(execution.RuntimeExecutable)) {
		return errors.New("runtime_executable must be absolute")
	}
	execution.RuntimeExecutable = filepath.Clean(execution.RuntimeExecutable)
	if len(execution.PreflightReceipt) != 64 || strings.Trim(execution.PreflightReceipt, "0123456789abcdef") != "" {
		return errors.New("preflight_receipt must be a lowercase SHA-256 digest")
	}
	return nil
}

func configIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || index > 0 && character == '-' {
			continue
		}
		return false
	}
	return true
}

func configPathsOverlap(left, right string) bool {
	if strings.EqualFold(filepath.Clean(left), filepath.Clean(right)) {
		return true
	}
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (share *ShareConfig) validate(seen map[string]bool) error {
	share.ID = strings.TrimSpace(share.ID)
	share.Name = strings.TrimSpace(share.Name)
	share.RootPath = strings.TrimSpace(share.RootPath)
	share.Mode = strings.TrimSpace(share.Mode)
	share.TargetDriftPolicy = strings.TrimSpace(share.TargetDriftPolicy)

	if share.ScanIntervalSeconds == 0 {
		share.ScanIntervalSeconds = int(DefaultScanInterval.Seconds())
	}
	if share.DeletionLimitCount == 0 {
		share.DeletionLimitCount = DefaultDeletionLimitCount
	}
	if share.DeletionLimitPercent == 0 {
		share.DeletionLimitPercent = DefaultDeletionLimitPercent
	}
	if share.TargetDriftPolicy == "" {
		share.TargetDriftPolicy = DefaultTargetDriftPolicy
	}

	if share.ID == "" {
		return errors.New("id is required")
	}
	if seen[share.ID] {
		return fmt.Errorf("duplicate id %q", share.ID)
	}
	seen[share.ID] = true
	if share.Name == "" {
		return errors.New("name is required")
	}
	if share.RootPath == "" {
		return errors.New("root_path is required")
	}
	switch share.Mode {
	case "send_once", "one_way_source", "one_way_target", "upload_only", "read_only":
	default:
		return fmt.Errorf("unsupported mode %q", share.Mode)
	}
	if share.ScanIntervalSeconds < 1 {
		return fmt.Errorf("scan_interval_seconds must be positive, got %d", share.ScanIntervalSeconds)
	}
	if share.DeletionLimitCount < 0 {
		return fmt.Errorf("deletion_limit_count cannot be negative, got %d", share.DeletionLimitCount)
	}
	if share.DeletionLimitPercent < 0 || share.DeletionLimitPercent > 100 {
		return fmt.Errorf("deletion_limit_percent must be between 0 and 100, got %d", share.DeletionLimitPercent)
	}
	switch share.TargetDriftPolicy {
	case "reject", "preserve_conflict_copy", "report_only":
	default:
		return fmt.Errorf("unsupported target_drift_policy %q", share.TargetDriftPolicy)
	}
	for i := range share.IgnorePatterns {
		pattern := filepath.ToSlash(strings.TrimSpace(share.IgnorePatterns[i]))
		if pattern == "" {
			return fmt.Errorf("ignore_patterns[%d] is empty", i)
		}
		if filepath.IsAbs(pattern) || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "../") || pattern == ".." {
			return fmt.Errorf("ignore_patterns[%d] must be relative to the share", i)
		}
		if _, err := filepath.Match(pattern, "probe"); err != nil && !strings.Contains(pattern, "/") {
			return fmt.Errorf("ignore_patterns[%d] is invalid: %w", i, err)
		}
		share.IgnorePatterns[i] = pattern
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
