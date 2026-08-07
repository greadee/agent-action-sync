package sync

import (
	"context"
	"fmt"
	"time"
)

// ScheduledScan is the one scan operation owned by the scheduler. A normal
// implementation calls ScanCommitService.Run; the scheduler only decides
// when it is safe to call it.
type ScheduledScan func(context.Context) (ScanCommitResult, error)

type ScanSchedule struct {
	RootPath   string
	Scan       ScheduledScan
	RootCheck  func(string) error
	Interval   time.Duration
	MaxBackoff time.Duration
	OutputSize int
	Manual     <-chan struct{}
	Watcher    <-chan ScanRequest
}

type ScanOutcome struct {
	Trigger     ScanRequest
	Started     bool
	Skipped     bool
	Unavailable bool
	Result      ScanCommitResult
	Err         error
}

func (schedule ScanSchedule) Run(ctx context.Context) (<-chan ScanOutcome, error) {
	if schedule.Scan == nil {
		return nil, fmt.Errorf("scheduled scan function is required")
	}
	if schedule.Interval <= 0 {
		return nil, fmt.Errorf("scan interval must be positive")
	}
	if schedule.RootPath == "" {
		return nil, fmt.Errorf("scan root path is required")
	}
	if schedule.MaxBackoff <= 0 {
		schedule.MaxBackoff = 30 * time.Minute
	}
	if schedule.MaxBackoff < schedule.Interval {
		schedule.MaxBackoff = schedule.Interval
	}
	if schedule.RootCheck == nil {
		schedule.RootCheck = CheckShareRoot
	}
	outputSize := schedule.OutputSize
	if outputSize <= 0 {
		outputSize = 8
	}
	outcomes := make(chan ScanOutcome, outputSize)
	go schedule.loop(ctx, outcomes)
	return outcomes, nil
}

func (schedule ScanSchedule) loop(ctx context.Context, outcomes chan<- ScanOutcome) {
	defer close(outcomes)
	timer := time.NewTimer(schedule.Interval)
	timerCh := timer.C
	done := make(chan ScanOutcome, 1)
	running := true
	pending := false
	var pendingTrigger ScanRequest
	backoff := schedule.Interval

	start := func(trigger ScanRequest) {
		running = true
		go func() {
			if err := schedule.RootCheck(schedule.RootPath); err != nil {
				done <- ScanOutcome{Trigger: trigger, Skipped: true, Unavailable: true, Err: err}
				return
			}
			result, err := schedule.Scan(ctx)
			done <- ScanOutcome{Trigger: trigger, Started: true, Result: result, Err: err}
		}()
	}
	start(ScanRequest{Reason: ScanTriggerStartup})

	resetTimer := func(duration time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(duration)
		timerCh = timer.C
	}

	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case outcome := <-done:
			running = false
			select {
			case outcomes <- outcome:
			default:
			}
			if outcome.Err != nil {
				backoff *= 2
				if backoff > schedule.MaxBackoff {
					backoff = schedule.MaxBackoff
				}
			} else {
				backoff = schedule.Interval
			}
			resetTimer(backoff)
			if pending {
				pending = false
				start(pendingTrigger)
			}
		case <-timerCh:
			timerCh = nil
			trigger := ScanRequest{Reason: ScanTriggerPeriodic}
			if running {
				pending = true
				pendingTrigger = trigger
			} else {
				start(trigger)
			}
		case _, ok := <-schedule.Manual:
			if !ok {
				schedule.Manual = nil
				continue
			}
			trigger := ScanRequest{Reason: ScanTriggerManual}
			if running {
				pending = true
				pendingTrigger = trigger
			} else {
				start(trigger)
			}
		case trigger, ok := <-schedule.Watcher:
			if !ok {
				schedule.Watcher = nil
				continue
			}
			if trigger.Reason == "" {
				trigger.Reason = ScanTriggerEvents
			}
			if running {
				pending = true
				pendingTrigger = trigger
			} else {
				start(trigger)
			}
		}
	}
}
