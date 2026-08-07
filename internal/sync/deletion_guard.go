package sync

import "fmt"

type DeletionGuard struct {
	MaxCount   int
	MaxPercent int
}

type DeletionGuardDecision struct {
	Allowed        bool
	DeletedCount   int
	PreviousActive int
	DeletedPercent int
	DeletedPaths   []string
	Reasons        []string
}

func CheckDeletionGuard(result ReconcileResult, guard DeletionGuard) (DeletionGuardDecision, error) {
	if guard.MaxCount < 0 {
		return DeletionGuardDecision{}, fmt.Errorf("deletion max count cannot be negative")
	}
	if guard.MaxPercent < 0 || guard.MaxPercent > 100 {
		return DeletionGuardDecision{}, fmt.Errorf("deletion max percent must be between 0 and 100")
	}

	decision := DeletionGuardDecision{Allowed: true}
	for _, change := range result.Changes {
		if change.Previous.RelativePath != "" && !change.Previous.IsDeleted {
			decision.PreviousActive++
		}
		if change.Kind == ChangeDeleted {
			decision.DeletedCount++
			decision.DeletedPaths = append(decision.DeletedPaths, change.RelativePath)
		}
	}

	if decision.PreviousActive > 0 {
		decision.DeletedPercent = (decision.DeletedCount * 100) / decision.PreviousActive
	}
	if guard.MaxCount > 0 && decision.DeletedCount > guard.MaxCount {
		decision.Allowed = false
		decision.Reasons = append(decision.Reasons,
			fmt.Sprintf("%d deletions exceeds max count %d", decision.DeletedCount, guard.MaxCount),
		)
	}
	if guard.MaxPercent > 0 && decision.PreviousActive > 0 && decision.DeletedCount*100 > guard.MaxPercent*decision.PreviousActive {
		decision.Allowed = false
		decision.Reasons = append(decision.Reasons,
			fmt.Sprintf("%d%% deletions exceeds max percent %d%%", decision.DeletedPercent, guard.MaxPercent),
		)
	}
	return decision, nil
}
