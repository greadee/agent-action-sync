package sync

import (
	"context"
	"fmt"
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
	outcomes := make(chan OneWayJobOutcome, queue.MaxConcurrent*2)
	go queue.loop(ctx, outcomes)
	return outcomes, nil
}

func (queue OneWayJobQueue) loop(ctx context.Context, outcomes chan<- OneWayJobOutcome) {
	defer close(outcomes)
	now := queue.Now().UTC()
	_ = queue.Jobs.RecoverRunningOneWayJobs(ctx, now)
	ticker := time.NewTicker(queue.PollInterval)
	defer ticker.Stop()
	active := make(chan struct{}, queue.MaxConcurrent)
	done := make(chan OneWayJobOutcome, queue.MaxConcurrent)

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
			go func(job core.OneWayJob) {
				err := queue.Execute(ctx, job)
				if ctx.Err() != nil {
					<-active
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
				done <- outcome
			}(job)
		}
	}

	poll()
	for {
		select {
		case <-ctx.Done():
			return
		case outcome := <-done:
			<-active
			select {
			case outcomes <- outcome:
			default:
			}
		case <-ticker.C:
			poll()
		}
	}
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
