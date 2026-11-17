package postgresql

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/queue/provider/providertest"
)

func TestPriorityPromotion(t *testing.T) {
	runQueueGate(t, providertest.PriorityPromotion)
}

func TestPriorityPromotionRollsBack(t *testing.T) {
	runQueueGate(t, providertest.PriorityPromotionRollsBack)
}
