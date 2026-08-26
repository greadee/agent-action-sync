package desktop

import (
	"errors"
	"path/filepath"
	"runtime"
	"time"
)

type MachineResources struct {
	CPUMillis             int64     `json:"cpu_millis"`
	LogicalCPUs           int       `json:"logical_cpus"`
	DiskTotalBytes        int64     `json:"disk_total_bytes"`
	DiskAvailableBytes    int64     `json:"disk_available_bytes"`
	ConfiguredConcurrency int       `json:"configured_concurrency"`
	ObservedAt            time.Time `json:"observed_at"`
	ExpiresAt             time.Time `json:"expires_at"`
	WarningCodes          []string  `json:"warning_codes"`
}

type ResourceObserver func(string, int, time.Time) (MachineResources, error)

func ObserveMachineResources(root string, configuredConcurrency int, now time.Time) (MachineResources, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || configuredConcurrency < 1 || configuredConcurrency > 2 || now.IsZero() {
		return MachineResources{}, errors.New("resource observation input is invalid")
	}
	total, available, err := diskCapacity(root)
	if err != nil || total < 0 || available < 0 || available > total {
		return MachineResources{}, errors.New("disk resource observation is unavailable")
	}
	logical := runtime.NumCPU()
	if logical < 1 {
		logical = 1
	}
	now = now.UTC()
	return MachineResources{
		CPUMillis: int64(logical) * 1000, LogicalCPUs: logical,
		DiskTotalBytes: total, DiskAvailableBytes: available,
		ConfiguredConcurrency: configuredConcurrency,
		ObservedAt:            now, ExpiresAt: now.Add(30 * time.Second), WarningCodes: []string{},
	}, nil
}
