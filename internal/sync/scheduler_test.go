package sync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestScanScheduleRunsStartupPeriodicManualAndWatcherScans(t *testing.T) {
	manual := make(chan struct{}, 1)
	watcher := make(chan ScanRequest, 1)
	var calls atomic.Int32
	schedule, err := (ScanSchedule{
		RootPath: ".", Interval: 30 * time.Millisecond, Manual: manual, Watcher: watcher,
		RootCheck: func(string) error { return nil },
		Scan: func(context.Context) (ScanCommitResult, error) {
			calls.Add(1)
			return ScanCommitResult{Committed: true}, nil
		},
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if outcome := nextOutcome(t, schedule); outcome.Trigger.Reason != ScanTriggerStartup || !outcome.Started {
		t.Fatalf("startup outcome = %+v", outcome)
	}
	manual <- struct{}{}
	if outcome := nextOutcome(t, schedule); outcome.Trigger.Reason != ScanTriggerManual {
		t.Fatalf("manual outcome = %+v", outcome)
	}
	watcher <- ScanRequest{Reason: ScanTriggerOverflow, FullScan: true}
	outcome := nextOutcome(t, schedule)
	if outcome.Trigger.Reason != ScanTriggerOverflow || !outcome.Trigger.FullScan {
		t.Fatalf("watcher outcome = %+v", outcome)
	}
	if outcome := nextOutcome(t, schedule); outcome.Trigger.Reason != ScanTriggerPeriodic {
		t.Fatalf("periodic outcome = %+v", outcome)
	}
	if calls.Load() < 4 {
		t.Fatalf("scan calls = %d", calls.Load())
	}
}

func TestScanScheduleUnavailableRootSkipsScanAndDoesNotPropagateDeletion(t *testing.T) {
	var called atomic.Bool
	schedule, err := (ScanSchedule{
		RootPath: "missing", Interval: time.Hour,
		RootCheck: func(string) error { return errors.New("root disappeared") },
		Scan: func(context.Context) (ScanCommitResult, error) {
			called.Store(true)
			return ScanCommitResult{Committed: true}, nil
		},
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	outcome := nextOutcome(t, schedule)
	if !outcome.Skipped || !outcome.Unavailable || outcome.Started || outcome.Err == nil {
		t.Fatalf("unavailable outcome = %+v", outcome)
	}
	if called.Load() {
		t.Fatal("scan ran for unavailable root")
	}
}

func TestScanScheduleDoesNotOverlapAndQueuesOnePendingTrigger(t *testing.T) {
	manual := make(chan struct{}, 4)
	started := make(chan struct{}, 1)
	finish := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32
	schedule, err := (ScanSchedule{
		RootPath: ".", Interval: time.Hour, Manual: manual, RootCheck: func(string) error { return nil },
		Scan: func(context.Context) (ScanCommitResult, error) {
			current := active.Add(1)
			for {
				old := maxActive.Load()
				if current <= old || maxActive.CompareAndSwap(old, current) {
					break
				}
			}
			started <- struct{}{}
			<-finish
			active.Add(-1)
			return ScanCommitResult{}, nil
		},
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	<-started
	manual <- struct{}{}
	manual <- struct{}{}
	close(finish)
	if outcome := nextOutcome(t, schedule); !outcome.Started {
		t.Fatalf("first outcome = %+v", outcome)
	}
	if outcome := nextOutcome(t, schedule); outcome.Trigger.Reason != ScanTriggerManual {
		t.Fatalf("queued outcome = %+v", outcome)
	}
	if maxActive.Load() != 1 {
		t.Fatalf("max active scans = %d", maxActive.Load())
	}
}

func TestScanSchedulePassesCancellationToScan(t *testing.T) {
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	schedule, err := (ScanSchedule{RootPath: ".", Interval: time.Hour, RootCheck: func(string) error { return nil }, Scan: func(scanCtx context.Context) (ScanCommitResult, error) {
		started <- struct{}{}
		<-scanCtx.Done()
		canceled <- struct{}{}
		return ScanCommitResult{}, scanCtx.Err()
	}}).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	<-started
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("scan did not receive cancellation")
	}
	select {
	case _, ok := <-schedule:
		if ok {
			t.Fatal("scheduler emitted after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not close")
	}
}

func nextOutcome(t *testing.T, outcomes <-chan ScanOutcome) ScanOutcome {
	t.Helper()
	select {
	case outcome, ok := <-outcomes:
		if !ok {
			t.Fatal("outcome channel closed")
		}
		return outcome
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scan outcome")
		return ScanOutcome{}
	}
}
