package sync

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

func TestOneWayJobQueueBoundsConcurrencyAndCompletes(t *testing.T) {
	store := newMemoryJobStore(core.OneWayJob{ID: "j1", TransferID: "transfer-1", ShareID: "share-1", RelativePath: "one.txt", State: core.OneWayJobQueued}, core.OneWayJob{ID: "j2", TransferID: "transfer-2", ShareID: "share-1", RelativePath: "two.txt", State: core.OneWayJobQueued})
	var active, maxActive atomic.Int32
	queue, err := (OneWayJobQueue{Jobs: store, MaxConcurrent: 1, PollInterval: time.Millisecond, Execute: func(context.Context, core.OneWayJob) error {
		current := active.Add(1)
		for {
			old := maxActive.Load()
			if current <= old || maxActive.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		return nil
	}}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i := 0; i < 2; i++ {
		outcome := nextJobOutcome(t, queue)
		if !outcome.Completed || outcome.Err != nil {
			t.Fatalf("outcome = %+v", outcome)
		}
	}
	if maxActive.Load() != 1 {
		t.Fatalf("max active = %d", maxActive.Load())
	}
}

func TestOneWayJobQueueRetriesWithBackoffAndReusesTransferReference(t *testing.T) {
	job := core.OneWayJob{ID: "retry", TransferID: "transfer-existing", ShareID: "share-1", RelativePath: "retry.txt", State: core.OneWayJobQueued}
	store := newMemoryJobStore(job)
	var attempts atomic.Int32
	queue, err := (OneWayJobQueue{Jobs: store, MaxConcurrent: 1, PollInterval: time.Millisecond, RetryBase: time.Millisecond, MaxBackoff: 4 * time.Millisecond, Execute: func(_ context.Context, got core.OneWayJob) error {
		if got.TransferID != job.TransferID {
			t.Fatalf("transfer ID = %s", got.TransferID)
		}
		if attempts.Add(1) < 3 {
			return errors.New("temporary")
		}
		return nil
	}}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for {
		outcome := nextJobOutcome(t, queue)
		if outcome.Completed {
			break
		}
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestOneWayJobQueuePauseResumeAndRestartRecovery(t *testing.T) {
	job := core.OneWayJob{ID: "paused", TransferID: "transfer-paused", ShareID: "share-1", RelativePath: "paused.txt", State: core.OneWayJobPaused}
	store := newMemoryJobStore(job)
	queue, err := (OneWayJobQueue{Jobs: store, MaxConcurrent: 1, PollInterval: time.Millisecond, Execute: func(context.Context, core.OneWayJob) error { return nil }}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case outcome := <-queue:
		t.Fatalf("paused outcome = %+v", outcome)
	case <-time.After(10 * time.Millisecond):
	}
	job.State = core.OneWayJobQueued
	if err := store.UpdateOneWayJob(context.Background(), job); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if outcome := nextJobOutcome(t, queue); !outcome.Completed {
		t.Fatalf("resumed outcome = %+v", outcome)
	}

	running := core.OneWayJob{ID: "running", TransferID: "transfer-running", ShareID: "share-1", RelativePath: "running.txt", State: core.OneWayJobRunning}
	store = newMemoryJobStore(running)
	if err := store.RecoverRunningOneWayJobs(context.Background(), time.Now().UTC()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	queue, err = (OneWayJobQueue{Jobs: store, MaxConcurrent: 1, PollInterval: time.Millisecond, Execute: func(context.Context, core.OneWayJob) error { return nil }}).Run(context.Background())
	if err != nil {
		t.Fatalf("restart Run: %v", err)
	}
	if outcome := nextJobOutcome(t, queue); !outcome.Completed {
		t.Fatalf("recovered outcome = %+v", outcome)
	}
}

func nextJobOutcome(t *testing.T, outcomes <-chan OneWayJobOutcome) OneWayJobOutcome {
	t.Helper()
	select {
	case outcome := <-outcomes:
		return outcome
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job outcome")
		return OneWayJobOutcome{}
	}
}

type memoryJobStore struct {
	mu   sync.Mutex
	jobs map[string]core.OneWayJob
}

func newMemoryJobStore(jobs ...core.OneWayJob) *memoryJobStore {
	store := &memoryJobStore{jobs: make(map[string]core.OneWayJob)}
	for _, job := range jobs {
		store.jobs[job.ID] = job
	}
	return store
}
func (store *memoryJobStore) SaveOneWayJob(_ context.Context, job core.OneWayJob) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.jobs[job.ID] = job
	return nil
}
func (store *memoryJobStore) GetOneWayJob(_ context.Context, id string) (core.OneWayJob, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[id]
	if !ok {
		return core.OneWayJob{}, storage.ErrNotFound
	}
	return job, nil
}
func (store *memoryJobStore) ListRunnableOneWayJobs(_ context.Context, now time.Time) ([]core.OneWayJob, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var jobs []core.OneWayJob
	for _, job := range store.jobs {
		if (job.State == core.OneWayJobQueued || job.State == core.OneWayJobRetryWait) && (job.NextAttemptAt.IsZero() || !job.NextAttemptAt.After(now)) {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}
func (store *memoryJobStore) ClaimOneWayJob(_ context.Context, id string, now time.Time) (core.OneWayJob, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	job, ok := store.jobs[id]
	if !ok || (job.State != core.OneWayJobQueued && job.State != core.OneWayJobRetryWait) || (!job.NextAttemptAt.IsZero() && job.NextAttemptAt.After(now)) {
		return core.OneWayJob{}, storage.ErrNotFound
	}
	job.State = core.OneWayJobRunning
	job.UpdatedAt = now
	store.jobs[id] = job
	return job, nil
}
func (store *memoryJobStore) UpdateOneWayJob(_ context.Context, job core.OneWayJob) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.jobs[job.ID]; !ok {
		return storage.ErrNotFound
	}
	store.jobs[job.ID] = job
	return nil
}
func (store *memoryJobStore) RecoverRunningOneWayJobs(_ context.Context, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for id, job := range store.jobs {
		if job.State == core.OneWayJobRunning {
			job.State = core.OneWayJobQueued
			job.NextAttemptAt = now
			store.jobs[id] = job
		}
	}
	return nil
}
