package sqlite

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/queue/provider/providertest"
)

func TestPriorityPromotion(t *testing.T) {
	providertest.PriorityPromotion(t, newQueueFixture(t))
}

func TestPriorityPromotionRollsBack(t *testing.T) {
	providertest.PriorityPromotionRollsBack(t, newQueueFixture(t))
}
