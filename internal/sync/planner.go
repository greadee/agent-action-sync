package sync

import (
	"context"
	"fmt"

	"syncgate/internal/core"
)

type FileIndexReader interface {
	List(ctx context.Context, shareID core.ShareID) ([]core.FileIndexEntry, error)
}

type ScanPlanner struct {
	Index FileIndexReader
	Scan  func(ScanOptions) (ScanResult, error)
}

type PlanScanOptions struct {
	ScanOptions
	DeletionGuard DeletionGuard
}

type ScanPlan struct {
	Scan             ScanResult
	Reconciliation   ReconcileResult
	DeletionDecision DeletionGuardDecision
}

func (planner ScanPlanner) Plan(ctx context.Context, options PlanScanOptions) (ScanPlan, error) {
	if planner.Index == nil {
		return ScanPlan{}, fmt.Errorf("file index store is required")
	}
	if options.ShareID == core.ShareID("") {
		return ScanPlan{}, fmt.Errorf("share ID is required")
	}

	previous, err := planner.Index.List(ctx, options.ShareID)
	if err != nil {
		return ScanPlan{}, fmt.Errorf("load previous file index: %w", err)
	}
	scan := planner.Scan
	if scan == nil {
		scan = ScanShare
	}
	result, err := scan(options.ScanOptions)
	if err != nil {
		return ScanPlan{}, fmt.Errorf("scan share: %w", err)
	}
	reconciliation, err := ReconcileScan(previous, result.Entries)
	if err != nil {
		return ScanPlan{}, fmt.Errorf("reconcile scan: %w", err)
	}
	decision, err := CheckDeletionGuard(reconciliation, options.DeletionGuard)
	if err != nil {
		return ScanPlan{}, err
	}

	plan := ScanPlan{
		Scan:             result,
		Reconciliation:   reconciliation,
		DeletionDecision: decision,
	}
	if !decision.Allowed {
		return plan, nil
	}
	return plan, nil
}
