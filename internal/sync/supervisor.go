package sync

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type WatcherFactory func(string) (Watcher, error)

type ShareWatcherSupervisor struct {
	RootPath   string
	Create     WatcherFactory
	Debounce   time.Duration
	MaxEvents  int
	RetryBase  time.Duration
	MaxBackoff time.Duration
	OutputSize int
}

func (supervisor ShareWatcherSupervisor) Run(ctx context.Context) (<-chan ScanRequest, error) {
	if ctx == nil {
		return nil, errors.New("watcher supervisor context is required")
	}
	if supervisor.RootPath == "" {
		return nil, fmt.Errorf("watcher supervisor root path is required")
	}
	if supervisor.Create == nil {
		supervisor.Create = NewPlatformWatcher
	}
	if supervisor.Debounce <= 0 {
		supervisor.Debounce = 100 * time.Millisecond
	}
	if supervisor.MaxEvents <= 0 {
		supervisor.MaxEvents = 256
	}
	if supervisor.RetryBase <= 0 {
		supervisor.RetryBase = 250 * time.Millisecond
	}
	if supervisor.MaxBackoff <= 0 {
		supervisor.MaxBackoff = 30 * time.Second
	}
	if supervisor.MaxBackoff < supervisor.RetryBase {
		supervisor.MaxBackoff = supervisor.RetryBase
	}
	if supervisor.OutputSize <= 0 {
		supervisor.OutputSize = 8
	}

	requests := make(chan ScanRequest, supervisor.OutputSize)
	go supervisor.loop(ctx, requests)
	return requests, nil
}

func (supervisor ShareWatcherSupervisor) loop(ctx context.Context, requests chan<- ScanRequest) {
	defer close(requests)
	backoff := supervisor.RetryBase
	for {
		if ctx.Err() != nil {
			return
		}

		watcher, err := supervisor.Create(supervisor.RootPath)
		if err != nil {
			sendScanRequest(ctx, requests, ScanRequest{Reason: ScanTriggerError, FullScan: true})
			if !waitForRetry(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, supervisor.MaxBackoff)
			continue
		}

		trigger, err := (WatchTrigger{
			Watcher:    watcher,
			Debounce:   supervisor.Debounce,
			MaxEvents:  supervisor.MaxEvents,
			OutputSize: supervisor.OutputSize,
		}).Run(ctx)
		if err != nil {
			sendScanRequest(ctx, requests, ScanRequest{Reason: ScanTriggerError, FullScan: true})
			_ = watcher.Close()
		} else {
			backoff = supervisor.RetryBase
			for request := range trigger {
				if !sendScanRequest(ctx, requests, request) {
					return
				}
			}
		}

		if !waitForRetry(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, supervisor.MaxBackoff)
	}
}

func sendScanRequest(ctx context.Context, requests chan<- ScanRequest, request ScanRequest) bool {
	select {
	case requests <- request:
		return true
	case <-ctx.Done():
		return false
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum/2 {
		return maximum
	}
	return current * 2
}

// ShareRuntime wires one supervised watcher to the existing authoritative
// scheduler. Watcher notifications can request scans, but never provide scan
// contents or bypass the scheduler's root preflight.
type ShareRuntime struct {
	Watcher  ShareWatcherSupervisor
	Schedule ScanSchedule
}

func (runtime ShareRuntime) Run(ctx context.Context) (<-chan ScanOutcome, error) {
	requests, err := runtime.Watcher.Run(ctx)
	if err != nil {
		return nil, err
	}
	runtime.Schedule.Watcher = requests
	return runtime.Schedule.Run(ctx)
}
