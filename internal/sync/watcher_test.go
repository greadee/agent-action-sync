package sync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestWatchTriggerDebouncesEventsIntoOneScanRequest(t *testing.T) {
	watcher := newFakeWatcher(8)
	requests, err := (WatchTrigger{Watcher: watcher, Debounce: 20 * time.Millisecond, MaxEvents: 8}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	watcher.events <- WatchEvent{Path: "one.txt"}
	watcher.events <- WatchEvent{Path: "two.txt"}
	request := nextScanRequest(t, requests)
	if request.FullScan || request.Reason != ScanTriggerEvents {
		t.Fatalf("request = %+v", request)
	}
	select {
	case duplicate := <-requests:
		t.Fatalf("unexpected duplicate request: %+v", duplicate)
	case <-time.After(40 * time.Millisecond):
	}
	watcher.close()
}

func TestWatchTriggerOverflowRequestsFullScan(t *testing.T) {
	watcher := newFakeWatcher(8)
	requests, err := (WatchTrigger{Watcher: watcher, Debounce: 20 * time.Millisecond, MaxEvents: 2}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, path := range []string{"one", "two", "three"} {
		watcher.events <- WatchEvent{Path: path}
	}
	request := nextScanRequest(t, requests)
	if !request.FullScan || request.Reason != ScanTriggerOverflow {
		t.Fatalf("overflow request = %+v", request)
	}
	watcher.close()
}

func TestWatchTriggerExplicitOverflowAndWatcherErrorRecoverWithFullScan(t *testing.T) {
	watcher := newFakeWatcher(4)
	requests, err := (WatchTrigger{Watcher: watcher, Debounce: 15 * time.Millisecond, MaxEvents: 4}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	watcher.events <- WatchEvent{Overflow: true}
	request := nextScanRequest(t, requests)
	if !request.FullScan || request.Reason != ScanTriggerOverflow {
		t.Fatalf("explicit overflow request = %+v", request)
	}
	watcher.errs <- errors.New("backend unavailable")
	request = nextScanRequest(t, requests)
	if !request.FullScan || request.Reason != ScanTriggerError {
		t.Fatalf("error request = %+v", request)
	}
	watcher.close()
}

func TestWatchTriggerOverflowErrorUsesOverflowReason(t *testing.T) {
	watcher := newFakeWatcher(2)
	requests, err := (WatchTrigger{Watcher: watcher, Debounce: 15 * time.Millisecond, MaxEvents: 2}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	watcher.errs <- ErrWatcherOverflow
	request := nextScanRequest(t, requests)
	if !request.FullScan || request.Reason != ScanTriggerOverflow {
		t.Fatalf("overflow error request = %+v", request)
	}
	watcher.close()
}

func TestWatchTriggerValidatesConfiguration(t *testing.T) {
	watcher := newFakeWatcher(1)
	for name, trigger := range map[string]WatchTrigger{
		"missing watcher":    {Debounce: time.Second, MaxEvents: 1},
		"missing debounce":   {Watcher: watcher, MaxEvents: 1},
		"missing max events": {Watcher: watcher, Debounce: time.Second},
	} {
		if _, err := trigger.Run(context.Background()); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
	watcher.close()
}

func nextScanRequest(t *testing.T, requests <-chan ScanRequest) ScanRequest {
	t.Helper()
	select {
	case request, ok := <-requests:
		if !ok {
			t.Fatal("scan request channel closed")
		}
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scan request")
		return ScanRequest{}
	}
}

type fakeWatcher struct {
	events chan WatchEvent
	errs   chan error
	once   sync.Once
}

func newFakeWatcher(size int) *fakeWatcher {
	return &fakeWatcher{events: make(chan WatchEvent, size), errs: make(chan error, size)}
}

func (watcher *fakeWatcher) Events() <-chan WatchEvent { return watcher.events }
func (watcher *fakeWatcher) Errors() <-chan error      { return watcher.errs }
func (watcher *fakeWatcher) Close() error {
	watcher.close()
	return nil
}
func (watcher *fakeWatcher) close() {
	watcher.once.Do(func() {
		close(watcher.events)
		close(watcher.errs)
	})
}
