package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

type OneWayJobExecutor func(context.Context, core.OneWayJob) error

type OneWayJobQueue struct {
	Jobs          storage.OneWayJobStore
	Execute       OneWayJobExecutor
	MaxConcurrent int
	PollInterval  time.Duration
	RetryBase     time.Duration
	MaxBackoff    time.Duration
	Now           func() time.Time
}

type OneWayJobOutcome struct {
	Job       core.OneWayJob
	Completed bool
	Retried   bool
	Err       error
}

type OneWayJobControl string

const (
	OneWayJobControlPause  OneWayJobControl = "pause"
	OneWayJobControlResume OneWayJobControl = "resume"
	OneWayJobControlRetry  OneWayJobControl = "retry"
)

var ErrActiveOneWayJob = errors.New("one-way job is active")

func (queue OneWayJobQueue) Run(ctx context.Context) (<-chan OneWayJobOutcome, error) {
	if queue.Jobs == nil || queue.Execute == nil {
		return nil, fmt.Errorf("job store and executor are required")
	}
	if queue.MaxConcurrent <= 0 {
		return nil, fmt.Errorf("max concurrent jobs must be positive")
	}
	if queue.PollInterval <= 0 {
		queue.PollInterval = 25 * time.Millisecond
	}
	if queue.RetryBase <= 0 {
		queue.RetryBase = queue.PollInterval
	}
	if queue.MaxBackoff <= 0 {
		queue.MaxBackoff = 5 * time.Minute
	}
	if queue.MaxBackoff < queue.RetryBase {
		queue.MaxBackoff = queue.RetryBase
	}
	if queue.Now == nil {
		queue.Now = func() time.Time { return time.Now().UTC() }
	}
	if err := queue.Jobs.RecoverRunningOneWayJobs(ctx, queue.Now().UTC()); err != nil {
		return nil, fmt.Errorf("recover running one-way jobs: %w", err)
	}
	outcomes := make(chan OneWayJobOutcome, queue.MaxConcurrent*2)
	go queue.loop(ctx, outcomes)
	return outcomes, nil
}

func (queue OneWayJobQueue) loop(ctx context.Context, outcomes chan<- OneWayJobOutcome) {
	defer close(outcomes)
	ticker := time.NewTicker(queue.PollInterval)
	defer ticker.Stop()
	active := make(chan struct{}, queue.MaxConcurrent)
	done := make(chan OneWayJobOutcome, queue.MaxConcurrent)
	var workers sync.WaitGroup

	poll := func() {
		jobs, err := queue.Jobs.ListRunnableOneWayJobs(ctx, queue.Now().UTC())
		if err != nil {
			return
		}
		for _, candidate := range jobs {
			select {
			case active <- struct{}{}:
			default:
				return
			}
			job, claimErr := queue.Jobs.ClaimOneWayJob(ctx, candidate.ID, queue.Now().UTC())
			if claimErr != nil {
				<-active
				continue
			}
			workers.Add(1)
			go func(job core.OneWayJob) {
				defer workers.Done()
				defer func() { <-active }()
				err := queue.Execute(ctx, job)
				if ctx.Err() != nil {
					return
				}
				outcome := OneWayJobOutcome{Job: job, Err: err}
				updated := job
				updated.UpdatedAt = queue.Now().UTC()
				if err == nil {
					updated.State = core.OneWayJobCompleted
					outcome.Completed = true
				} else {
					updated.RetryCount++
					updated.LastError = err.Error()
					if updated.RetryCount < 3 {
						updated.State = core.OneWayJobRetryWait
						updated.NextAttemptAt = updated.UpdatedAt.Add(queue.backoff(updated.RetryCount))
						outcome.Retried = true
					} else {
						updated.State = core.OneWayJobFailed
					}
				}
				if updateErr := queue.Jobs.UpdateOneWayJob(ctx, updated); updateErr != nil {
					outcome.Err = fmt.Errorf("update job %s: %w", job.ID, updateErr)
				}
				outcome.Job = updated
				select {
				case done <- outcome:
				case <-ctx.Done():
				}
			}(job)
		}
	}

	poll()
	for {
		select {
		case <-ctx.Done():
			workers.Wait()
			return
		case outcome := <-done:
			select {
			case outcomes <- outcome:
			default:
			}
		case <-ticker.C:
			poll()
		}
	}
}

func ControlOneWayJob(ctx context.Context, jobs storage.OneWayJobStore, id string, control OneWayJobControl, now time.Time) (core.OneWayJob, error) {
	if jobs == nil {
		return core.OneWayJob{}, errors.New("one-way job store is required")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return core.OneWayJob{}, errors.New("one-way job ID is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	job, err := jobs.GetOneWayJob(ctx, id)
	if err != nil {
		return core.OneWayJob{}, err
	}
	updated := job
	switch control {
	case OneWayJobControlPause:
		switch job.State {
		case core.OneWayJobPaused:
			return job, nil
		case core.OneWayJobQueued, core.OneWayJobRetryWait:
			updated.State = core.OneWayJobPaused
			updated.NextAttemptAt = time.Time{}
		default:
			return core.OneWayJob{}, fmt.Errorf("%w: cannot pause %s", ErrActiveOneWayJob, job.State)
		}
	case OneWayJobControlResume:
		switch job.State {
		case core.OneWayJobQueued:
			return job, nil
		case core.OneWayJobPaused:
			updated.State = core.OneWayJobQueued
			updated.NextAttemptAt = time.Time{}
		default:
			return core.OneWayJob{}, fmt.Errorf("cannot resume one-way job in %s state", job.State)
		}
	case OneWayJobControlRetry:
		switch job.State {
		case core.OneWayJobQueued:
			return job, nil
		case core.OneWayJobRetryWait, core.OneWayJobFailed:
			updated.State = core.OneWayJobQueued
			updated.RetryCount = 0
			updated.NextAttemptAt = time.Time{}
			updated.LastError = ""
		default:
			return core.OneWayJob{}, fmt.Errorf("cannot retry one-way job in %s state", job.State)
		}
	default:
		return core.OneWayJob{}, fmt.Errorf("unsupported one-way job control %q", control)
	}
	updated.UpdatedAt = now.UTC()
	if err := jobs.UpdateOneWayJob(ctx, updated); err != nil {
		return core.OneWayJob{}, err
	}
	return updated, nil
}

func (queue OneWayJobQueue) backoff(retry int) time.Duration {
	backoff := queue.RetryBase
	for i := 1; i < retry; i++ {
		if backoff >= queue.MaxBackoff/2 {
			return queue.MaxBackoff
		}
		backoff *= 2
	}
	if backoff > queue.MaxBackoff {
		return queue.MaxBackoff
	}
	return backoff
}
