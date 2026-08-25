package buildinfo

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	ApplicationName             = "SyncGate Desktop Node"
	ControlLayoutVersion        = 1
	MinimumControlLayoutVersion = 1
)

var (
	version  = "0.0.0-dev"
	commit   = "unknown"
	builtAt  = "unknown"
	channel  = "development"
	semverRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
)

// Manifest is embedded into release binaries and emitted beside Windows
// installer artifacts. The layout fields gate startup across upgrades and
// explicitly prevent an older binary from opening newer authority-local state.
type Manifest struct {
	Application                 string `json:"application"`
	Version                     string `json:"version"`
	Commit                      string `json:"commit"`
	BuiltAt                     string `json:"built_at"`
	Channel                     string `json:"channel"`
	GOOS                        string `json:"goos"`
	GOARCH                      string `json:"goarch"`
	ControlLayoutVersion        int    `json:"control_layout_version"`
	MinimumControlLayoutVersion int    `json:"minimum_control_layout_version"`
}

func Current() Manifest {
	return Manifest{
		Application:                 ApplicationName,
		Version:                     strings.TrimSpace(version),
		Commit:                      strings.TrimSpace(commit),
		BuiltAt:                     strings.TrimSpace(builtAt),
		Channel:                     strings.TrimSpace(channel),
		GOOS:                        runtime.GOOS,
		GOARCH:                      runtime.GOARCH,
		ControlLayoutVersion:        ControlLayoutVersion,
		MinimumControlLayoutVersion: MinimumControlLayoutVersion,
	}
}

func (manifest Manifest) Validate() error {
	if manifest.Application != ApplicationName {
		return fmt.Errorf("unexpected application %q", manifest.Application)
	}
	if !semverRE.MatchString(manifest.Version) {
		return fmt.Errorf("version must be semantic, got %q", manifest.Version)
	}
	if manifest.Commit == "" || manifest.BuiltAt == "" || manifest.Channel == "" {
		return errors.New("commit, build time, and channel are required")
	}
	if manifest.BuiltAt != "unknown" {
		if _, err := time.Parse(time.RFC3339, manifest.BuiltAt); err != nil {
			return fmt.Errorf("built_at must be RFC3339: %w", err)
		}
	}
	if manifest.ControlLayoutVersion < 1 {
		return errors.New("control layout version must be positive")
	}
	if manifest.MinimumControlLayoutVersion < 1 || manifest.MinimumControlLayoutVersion > manifest.ControlLayoutVersion {
		return errors.New("minimum control layout version is invalid")
	}
	return nil
}
