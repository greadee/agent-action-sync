package sync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

type ScanCommitService struct {
	Planner        ScanPlanner
	Committer      storage.AuthoritativeStateStore
	Now            func() time.Time
	NewRevisionID  func() (core.RevisionID, error)
	NewTombstoneID func() (core.TombstoneID, error)
}

type ScanCommitOptions struct {
	Plan               PlanScanOptions
	OriginDeviceID     core.DeviceID
	SequenceStart      int64
	TombstoneRetention time.Duration
}

type ScanCommitResult struct {
	Plan        ScanPlan
	Revisions   []core.Revision
	Tombstones  []storage.Tombstone
	Committed   bool
	Blocked     bool
	CommittedAt time.Time
}

func (service ScanCommitService) Run(ctx context.Context, options ScanCommitOptions) (ScanCommitResult, error) {
	if service.Committer == nil {
		return ScanCommitResult{}, fmt.Errorf("authoritative state committer is required")
	}
	if options.OriginDeviceID == "" {
		return ScanCommitResult{}, fmt.Errorf("origin device ID is required")
	}
	if options.SequenceStart < 0 {
		return ScanCommitResult{}, fmt.Errorf("sequence start cannot be negative")
	}
	if options.TombstoneRetention < 0 {
		return ScanCommitResult{}, fmt.Errorf("tombstone retention cannot be negative")
	}

	now := service.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	commitTime := now().UTC()
	planOptions := options.Plan
	if planOptions.ScanOptions.Now == nil {
		planOptions.ScanOptions.Now = func() time.Time { return commitTime }
	}
	plan, err := service.Planner.Plan(ctx, planOptions)
	if err != nil {
		return ScanCommitResult{}, err
	}
	result := ScanCommitResult{Plan: plan}
	if !plan.DeletionDecision.Allowed {
		result.Blocked = true
		return result, nil
	}

	newRevisionID := service.NewRevisionID
	if newRevisionID == nil {
		newRevisionID = newRevisionIDValue
	}
	revisions, err := BuildRevisions(plan.Reconciliation, RevisionBuildOptions{
		ShareID:        options.Plan.ShareID,
		OriginDeviceID: options.OriginDeviceID,
		SequenceStart:  options.SequenceStart,
		Now:            func() time.Time { return commitTime },
		NewID:          newRevisionID,
	})
	if err != nil {
		return result, fmt.Errorf("build scan revisions: %w", err)
	}
	result.Revisions = revisions

	newTombstoneID := service.NewTombstoneID
	if newTombstoneID == nil {
		newTombstoneID = newTombstoneIDValue
	}
	requests := make([]storage.TombstoneRequest, 0)
	for _, revision := range revisions {
		if !revision.IsDeleted {
			continue
		}
		tombstoneID, err := newTombstoneID()
		if err != nil {
			return result, fmt.Errorf("create tombstone ID: %w", err)
		}
		if tombstoneID == "" {
			return result, fmt.Errorf("create tombstone ID: empty ID")
		}
		expiresAt := time.Time{}
		if options.TombstoneRetention > 0 {
			expiresAt = commitTime.Add(options.TombstoneRetention)
		}
		requests = append(requests, storage.TombstoneRequest{
			ID:                  tombstoneID,
			TombstoneRevisionID: revision.ID,
			ExpiresAt:           expiresAt,
		})
	}

	tombstones, err := service.Committer.CommitSnapshotAndTombstones(
		ctx,
		options.Plan.ShareID,
		plan.Scan.Entries,
		revisions,
		requests,
		plan.Scan.ScannedAt,
	)
	if err != nil {
		return result, fmt.Errorf("commit scan state: %w", err)
	}
	result.Tombstones = tombstones
	result.Committed = true
	result.CommittedAt = commitTime
	return result, nil
}

func newRevisionIDValue() (core.RevisionID, error) {
	bytes, err := newRandomID()
	return core.RevisionID(bytes), err
}

func newTombstoneIDValue() (core.TombstoneID, error) {
	bytes, err := newRandomID()
	return core.TombstoneID(bytes), err
}

func newRandomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
