package sync

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestShareWatcherSupervisorReconnectsAfterWatcherFailure(t *testing.T) {
	first := newFakeWatcher(4)
	second := newFakeWatcher(4)
	var mu sync.Mutex
	watchers := []Watcher{first, second}
	calls := 0
	factory := func(string) (Watcher, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if len(watchers) == 0 {
			return nil, errors.New("factory exhausted")
		}
		watcher := watchers[0]
		watchers = watchers[1:]
		return watcher, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests, err := (ShareWatcherSupervisor{
		RootPath:  t.TempDir(),
		Create:    factory,
		Debounce:  5 * time.Millisecond,
		RetryBase: 5 * time.Millisecond,
	}).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	first.events <- WatchEvent{Path: filepath.Join("root", "first.txt")}
	request := nextScanRequest(t, requests)
	if request.Reason != ScanTriggerEvents || request.FullScan {
		t.Fatalf("first request = %+v", request)
	}
	first.close()

	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		started := calls >= 2
		mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("supervisor did not create replacement watcher")
		}
		time.Sleep(time.Millisecond)
	}
	second.events <- WatchEvent{Path: filepath.Join("root", "second.txt")}
	request = nextScanRequest(t, requests)
	if request.Reason != ScanTriggerEvents || request.FullScan {
		t.Fatalf("replacement request = %+v", request)
	}
	second.close()
}

func TestShareWatcherSupervisorRootLossRequestsRecoveryScan(t *testing.T) {
	watcher := newFakeWatcher(2)
	var mu sync.Mutex
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests, err := (ShareWatcherSupervisor{
		RootPath:  t.TempDir(),
		RetryBase: 5 * time.Millisecond,
		Create: func(string) (Watcher, error) {
			mu.Lock()
			calls++
			call := calls
			mu.Unlock()
			if call == 1 {
				return nil, ErrUnavailableRoot
			}
			return watcher, nil
		},
	}).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	request := nextScanRequest(t, requests)
	if request.Reason != ScanTriggerError || !request.FullScan {
		t.Fatalf("root recovery request = %+v", request)
	}
	watcher.close()
}

func TestShareRuntimeFeedsWatcherRequestsIntoScheduler(t *testing.T) {
	watcher := newFakeWatcher(4)
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcomes, err := (ShareRuntime{
		Watcher: ShareWatcherSupervisor{
			RootPath:  root,
			Create:    func(string) (Watcher, error) { return watcher, nil },
			Debounce:  5 * time.Millisecond,
			RetryBase: 5 * time.Millisecond,
		},
		Schedule: ScanSchedule{
			RootPath: root,
			Interval: time.Hour,
			Scan: func(context.Context) (ScanCommitResult, error) {
				return ScanCommitResult{Committed: true}, nil
			},
		},
	}).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	startup := nextScanOutcome(t, outcomes)
	if startup.Trigger.Reason != ScanTriggerStartup || !startup.Result.Committed {
		t.Fatalf("startup outcome = %+v", startup)
	}
	watcher.events <- WatchEvent{Path: filepath.Join(root, "file.txt")}
	event := nextScanOutcome(t, outcomes)
	if event.Trigger.Reason != ScanTriggerEvents || !event.Result.Committed {
		t.Fatalf("event outcome = %+v", event)
	}
	watcher.close()
}

func nextScanOutcome(t *testing.T, outcomes <-chan ScanOutcome) ScanOutcome {
	t.Helper()
	select {
	case outcome, ok := <-outcomes:
		if !ok {
			t.Fatal("scan outcome channel closed")
		}
		return outcome
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scan outcome")
		return ScanOutcome{}
	}
}
