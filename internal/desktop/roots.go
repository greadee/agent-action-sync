package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const productDirectory = "SyncGate"

type Roots struct {
	ConfigDir       string `json:"config_dir"`
	ConfigPath      string `json:"config_path"`
	DataDir         string `json:"data_dir"`
	LogDir          string `json:"log_dir"`
	RuntimeCacheDir string `json:"runtime_cache_dir"`
	WorktreeRoot    string `json:"worktree_root"`
}

// DefaultRoots resolves per-user state outside the application install
// directory. Windows releases install binaries below LocalAppData\Programs,
// while mutable roots live below AppData and LocalAppData\SyncGate.
func DefaultRoots() (Roots, error) {
	if runtime.GOOS == "windows" {
		return WindowsRoots(os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA"))
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return Roots{}, fmt.Errorf("resolve user config directory: %w", err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return Roots{}, fmt.Errorf("resolve user cache directory: %w", err)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return Roots{}, fmt.Errorf("resolve user home directory: %w", err)
	}
	return validateRoots(Roots{
		ConfigDir:       filepath.Join(configDir, "syncgate"),
		ConfigPath:      filepath.Join(configDir, "syncgate", "config.json"),
		DataDir:         filepath.Join(homeDir, ".local", "share", "syncgate"),
		LogDir:          filepath.Join(homeDir, ".local", "state", "syncgate", "logs"),
		RuntimeCacheDir: filepath.Join(cacheDir, "syncgate", "runtime"),
		WorktreeRoot:    filepath.Join(homeDir, ".local", "share", "syncgate-worktrees"),
	})
}

func WindowsRoots(roamingAppData, localAppData string) (Roots, error) {
	roamingAppData = strings.TrimSpace(roamingAppData)
	localAppData = strings.TrimSpace(localAppData)
	if roamingAppData == "" || localAppData == "" {
		return Roots{}, errors.New("APPDATA and LOCALAPPDATA are required")
	}
	configDir := filepath.Join(roamingAppData, productDirectory)
	mutableRoot := filepath.Join(localAppData, productDirectory)
	return validateRoots(Roots{
		ConfigDir:       configDir,
		ConfigPath:      filepath.Join(configDir, "config.json"),
		DataDir:         filepath.Join(mutableRoot, "data"),
		LogDir:          filepath.Join(mutableRoot, "logs"),
		RuntimeCacheDir: filepath.Join(mutableRoot, "cache"),
		WorktreeRoot:    filepath.Join(mutableRoot, "worktrees"),
	})
}

// RootsUnder is an explicit, test-friendly override. It retains the same
// separation guarantees while containing all mutable roots below base.
func RootsUnder(base string) (Roots, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return Roots{}, errors.New("desktop root override is required")
	}
	absolute, err := filepath.Abs(base)
	if err != nil {
		return Roots{}, fmt.Errorf("resolve desktop root override: %w", err)
	}
	return validateRoots(Roots{
		ConfigDir:       filepath.Join(absolute, "config"),
		ConfigPath:      filepath.Join(absolute, "config", "config.json"),
		DataDir:         filepath.Join(absolute, "data"),
		LogDir:          filepath.Join(absolute, "logs"),
		RuntimeCacheDir: filepath.Join(absolute, "cache"),
		WorktreeRoot:    filepath.Join(absolute, "worktrees"),
	})
}

func validateRoots(roots Roots) (Roots, error) {
	values := map[string]string{
		"config_dir": roots.ConfigDir, "config_path": roots.ConfigPath,
		"data_dir": roots.DataDir, "log_dir": roots.LogDir,
		"runtime_cache_dir": roots.RuntimeCacheDir, "worktree_root": roots.WorktreeRoot,
	}
	seen := make(map[string]string)
	for name, value := range values {
		if strings.TrimSpace(value) == "" {
			return Roots{}, fmt.Errorf("%s is required", name)
		}
		cleaned := filepath.Clean(value)
		if !filepath.IsAbs(cleaned) {
			return Roots{}, fmt.Errorf("%s must be absolute", name)
		}
		if previous, ok := seen[strings.ToLower(cleaned)]; ok {
			return Roots{}, fmt.Errorf("%s overlaps %s", name, previous)
		}
		seen[strings.ToLower(cleaned)] = name
		switch name {
		case "config_dir":
			roots.ConfigDir = cleaned
		case "config_path":
			roots.ConfigPath = cleaned
		case "data_dir":
			roots.DataDir = cleaned
		case "log_dir":
			roots.LogDir = cleaned
		case "runtime_cache_dir":
			roots.RuntimeCacheDir = cleaned
		case "worktree_root":
			roots.WorktreeRoot = cleaned
		}
	}
	relative, err := filepath.Rel(roots.ConfigDir, roots.ConfigPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Roots{}, errors.New("config_path must be inside config_dir")
	}
	directories := []struct {
		name string
		path string
	}{
		{name: "config_dir", path: roots.ConfigDir},
		{name: "data_dir", path: roots.DataDir},
		{name: "log_dir", path: roots.LogDir},
		{name: "runtime_cache_dir", path: roots.RuntimeCacheDir},
		{name: "worktree_root", path: roots.WorktreeRoot},
	}
	for left := 0; left < len(directories); left++ {
		for right := left + 1; right < len(directories); right++ {
			if pathsOverlap(directories[left].path, directories[right].path) {
				return Roots{}, fmt.Errorf("%s must be separate from %s", directories[right].name, directories[left].name)
			}
		}
	}
	return roots, nil
}

func pathsOverlap(left, right string) bool {
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
