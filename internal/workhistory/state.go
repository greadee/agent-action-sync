package workhistory

import (
	"context"
	"encoding/json"
	"fmt"

	"syncgate/internal/project"
	"syncgate/internal/storage"
)

type historyState struct {
	workPackages map[string]project.WorkPackageState
	executions   map[string]project.ExecutionState
	approved     map[string]bool
}

func (service *Service) loadState(ctx context.Context, projectID string) (historyState, error) {
	state := historyState{workPackages: map[string]project.WorkPackageState{}, executions: map[string]project.ExecutionState{}, approved: map[string]bool{}}
	query := storage.ProjectEventQuery{ProjectID: projectID, Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit}}
	count := 0
	for {
		page, err := service.Store.ProjectEvents().ListProjectEvents(ctx, query)
		if err != nil {
			return historyState{}, err
		}
		for _, event := range page.Items {
			count++
			if count > MaxStateEvents {
				return historyState{}, fmt.Errorf("work-history state exceeds %d events", MaxStateEvents)
			}
			if event.Status != "accepted" {
				continue
			}
			applyEvent(&state, event)
		}
		if page.NextCursor == nil {
			return state, nil
		}
		query.Page.Cursor = *page.NextCursor
	}
}

func applyEvent(state *historyState, event storage.ProjectEventProjection) {
	switch project.EventType(event.EventType) {
	case project.EventWorkPackageCreated:
		state.workPackages[event.WorkPackageID] = project.WorkPackagePlanned
	case project.EventWorkPackageStateChanged:
		var payload project.WorkPackageStateChangedPayload
		if json.Unmarshal(event.PayloadJSON, &payload) == nil {
			state.workPackages[event.WorkPackageID] = payload.To
			if payload.To != project.WorkPackageReview {
				state.approved[event.WorkPackageID] = false
			}
		}
	case project.EventExecutionStarted:
		state.executions[event.ExecutionID] = project.ExecutionRunning
	case project.EventExecutionPaused:
		state.executions[event.ExecutionID] = project.ExecutionPaused
	case project.EventExecutionFailed:
		state.executions[event.ExecutionID] = project.ExecutionFailed
	case project.EventExecutionCompleted:
		state.executions[event.ExecutionID] = project.ExecutionCompleted
	case project.EventReviewRecorded:
		var payload project.ReviewRecordedPayload
		if json.Unmarshal(event.PayloadJSON, &payload) == nil {
			state.approved[event.WorkPackageID] = payload.Outcome == project.ReviewApproved
		}
	case project.EventWorkAccepted:
		state.workPackages[event.WorkPackageID] = project.WorkPackageAccepted
	}
}

func allowedWorkTransition(from, to project.WorkPackageState) bool {
	allowed := map[project.WorkPackageState]map[project.WorkPackageState]bool{
		project.WorkPackagePlanned:    {project.WorkPackageReady: true, project.WorkPackageCanceled: true},
		project.WorkPackageReady:      {project.WorkPackageInProgress: true, project.WorkPackageCanceled: true},
		project.WorkPackageInProgress: {project.WorkPackageBlocked: true, project.WorkPackageReview: true, project.WorkPackageFailed: true, project.WorkPackageCanceled: true},
		project.WorkPackageBlocked:    {project.WorkPackageInProgress: true, project.WorkPackageFailed: true, project.WorkPackageCanceled: true},
		project.WorkPackageReview:     {project.WorkPackageInProgress: true, project.WorkPackageFailed: true},
		project.WorkPackageFailed:     {project.WorkPackageReady: true, project.WorkPackageCanceled: true},
	}
	return allowed[from][to]
}
