package scheduler

import (
	"context"
	"testing"

	"syncgate/internal/storage"
)

// TestDeterministicPilotMatrix exercises the Phase 1 pilot's scheduling spine.
// Result review, integration, persistence rebuild, and replica projection have
// their own integration-gate and two-daemon tests; this test ensures their
// canonical acceptance boundary is the only way to unlock dependent work.
func TestDeterministicPilotMatrix(t *testing.T) {
	fixture := newSchedulerFixture(t, 2, 2)
	requests := fixture.requests(t, map[string][]string{
		"work:implement-a": nil,
		"work:implement-b": nil,
		"work:integrate":   {"work:implement-a", "work:implement-b"},
	}, nil)

	report, err := fixture.scheduler.Cycle(context.Background(), requests)
	if err != nil || report.Active != 2 || itemFor(report, "work:integrate").Outcome != OutcomeWaiting {
		t.Fatalf("initial pilot dispatch=%+v err=%v", report, err)
	}
	if err := fixture.scheduler.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.control.state("assignment:implement-a") != storage.AssignmentPaused || fixture.control.state("assignment:implement-b") != storage.AssignmentPaused {
		t.Fatal("pilot pause did not fence both independent attempts")
	}
	if err := fixture.scheduler.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}

	fixture.complete(t, "assignment:implement-a")
	fixture.complete(t, "assignment:implement-b")
	report, err = fixture.scheduler.Cycle(context.Background(), requests)
	if err != nil || report.Active != 0 || itemFor(report, "work:integrate").Outcome != OutcomeWaiting {
		t.Fatalf("runtime completion unlocked dependent work: %+v err=%v", report, err)
	}

	accepted := append(acceptedEvents("work:implement-a", fixture.now), acceptedEvents("work:implement-b", fixture.now)...)
	for index := range requests {
		requests[index].Events = append(createdEvents(requests[index].Graph, fixture.now), accepted...)
	}
	report, err = fixture.scheduler.Cycle(context.Background(), requests)
	if err != nil || report.Active != 1 || itemFor(report, "work:integrate").Outcome != OutcomeDispatched {
		t.Fatalf("canonical acceptance did not unlock integration: %+v err=%v", report, err)
	}
	if err := fixture.scheduler.Cancel(context.Background(), "assignment:integrate"); err != nil {
		t.Fatal(err)
	}
	if fixture.control.state("assignment:integrate") != storage.AssignmentCanceled || fixture.workspaces.releases != 0 {
		t.Fatalf("cancellation changed inspectable workspace state=%s releases=%d", fixture.control.state("assignment:integrate"), fixture.workspaces.releases)
	}
	if err := fixture.scheduler.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
