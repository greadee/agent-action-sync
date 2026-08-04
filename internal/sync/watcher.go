package sync

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrWatcherOverflow = errors.New("filesystem watcher overflow")

// WatchEvent is a notification that something may have changed below a share
// root. It is never interpreted as authoritative file state.
type WatchEvent struct {
	Path     string
	Overflow bool
}

// Watcher owns the platform-specific event source. Implementations should
// bound their event queue and report queue loss as ErrWatcherOverflow or an
// event with Overflow set.
type Watcher interface {
	Events() <-chan WatchEvent
	Errors() <-chan error
	Close() error
}

type ScanTriggerReason string

const (
	ScanTriggerEvents   ScanTriggerReason = "watcher_events"
	ScanTriggerOverflow ScanTriggerReason = "watcher_overflow"
	ScanTriggerError    ScanTriggerReason = "watcher_error"
)

// ScanRequest asks the scheduler to run an authoritative scan. The trigger
// deliberately carries no file contents and never performs a scan itself.
type ScanRequest struct {
	Reason   ScanTriggerReason
	FullScan bool
}

// WatchTrigger converts a bounded watcher stream into debounced scan
// requests. MaxEvents bounds the number of notifications represented by one
// debounce window; exceeding it requests a full recovery scan.
type WatchTrigger struct {
	Watcher    Watcher
	Debounce   time.Duration
	MaxEvents  int
	OutputSize int
}

func (trigger WatchTrigger) Run(ctx context.Context) (<-chan ScanRequest, error) {
	if trigger.Watcher == nil {
		return nil, fmt.Errorf("watcher is required")
	}
	if trigger.Debounce <= 0 {
		return nil, fmt.Errorf("watcher debounce must be positive")
	}
	if trigger.MaxEvents <= 0 {
		return nil, fmt.Errorf("watcher max events must be positive")
	}
	outputSize := trigger.OutputSize
	if outputSize <= 0 {
		outputSize = 1
	}
	requests := make(chan ScanRequest, outputSize)
	go trigger.loop(ctx, requests)
	return requests, nil
}

func (trigger WatchTrigger) loop(ctx context.Context, requests chan<- ScanRequest) {
	defer close(requests)
	defer trigger.Watcher.Close()
	events := trigger.Watcher.Events()
	errorsCh := trigger.Watcher.Errors()
	var timer *time.Timer
	var timerCh <-chan time.Time
	pending := 0
	fullScan := false
	reason := ScanTriggerEvents

	flush := func() {
		if pending == 0 && !fullScan {
			return
		}
		request := ScanRequest{Reason: reason, FullScan: fullScan}
		// A trigger must not let a slow scheduler stop event ingestion. A full
		// scan request is safe to coalesce with the next request.
		select {
		case requests <- request:
		default:
		}
		pending = 0
		fullScan = false
		reason = ScanTriggerEvents
		if timer != nil {
			timer.Stop()
		}
		timerCh = nil
	}

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-timerCh:
			flush()
		case event, ok := <-events:
			if !ok {
				flush()
				return
			}
			pending++
			if event.Overflow || pending > trigger.MaxEvents {
				fullScan = true
				reason = ScanTriggerOverflow
			}
			if timer == nil {
				timer = time.NewTimer(trigger.Debounce)
			} else {
				timer.Reset(trigger.Debounce)
			}
			timerCh = timer.C
		case err, ok := <-errorsCh:
			if !ok {
				errorsCh = nil
				continue
			}
			pending++
			fullScan = true
			reason = ScanTriggerError
			if errors.Is(err, ErrWatcherOverflow) {
				reason = ScanTriggerOverflow
			}
			if timer == nil {
				timer = time.NewTimer(trigger.Debounce)
			} else {
				timer.Reset(trigger.Debounce)
			}
			timerCh = timer.C
		}
	}
}
