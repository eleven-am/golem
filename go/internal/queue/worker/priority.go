package worker

import (
	"context"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/queue"
)

func (control operator) Promote(ctx context.Context, id queue.JobID, priority queue.Priority, options ...queue.PromotionOption) (bool, error) {
	if err := queueprovider.ValidateJobIdentity(string(id)); err != nil {
		return false, queue.Fail(queue.CodeConfigInvalid, "%v", err)
	}
	promotion, err := queue.ResolvePromotion(priority, options...)
	if err != nil {
		return false, err
	}
	changed, err := control.store.Promote(ctx, nil, string(id), int(promotion.Priority()), promotion.ReadyNow())
	if err != nil {
		return false, queue.Fail(queue.CodeStoreFailure, "promote job %s: %v", id, err)
	}
	return changed, nil
}
