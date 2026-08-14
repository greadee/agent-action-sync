package projector

import (
	"context"
	"errors"

	"syncgate/internal/core"
	"syncgate/internal/project"
)

// LifecycleHook is the narrow application hook shared by later local writers
// and daemon scan/apply completion paths. Non-project shares are a no-op.
type LifecycleHook struct {
	Projector *Projector
}

func (hook LifecycleHook) AfterLocalWrite(ctx context.Context, rootPath string) (Report, error) {
	if hook.Projector == nil {
		return Report{}, errors.New("project lifecycle projector is required")
	}
	return hook.Projector.Ingest(ctx, rootPath, TriggerLocalWrite)
}

func (hook LifecycleHook) AfterShareUpdate(ctx context.Context, shareID core.ShareID, rootPath string) (Report, error) {
	if hook.Projector == nil {
		return Report{}, errors.New("project lifecycle projector is required")
	}
	report, err := hook.Projector.IngestShare(ctx, shareID, rootPath, TriggerShareScan)
	if errors.Is(err, project.ErrRecordNotFound) {
		return Report{}, nil
	}
	return report, err
}
