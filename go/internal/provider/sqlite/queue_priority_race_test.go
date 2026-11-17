package sqlite

import (
	"testing"

	"github.com/eleven-am/golem/go/internal/queue/provider/providertest"
)

func TestPriorityPromotionRacesClaim(t *testing.T) {
	providertest.PriorityPromotionRacesClaim(t, newQueueFixture(t))
}

func TestPriorityPromotionRacesCancel(t *testing.T) {
	providertest.PriorityPromotionRacesCancel(t, newQueueFixture(t))
}

func TestConcurrentPriorityPromotionsAreMonotonic(t *testing.T) {
	providertest.ConcurrentPriorityPromotionsAreMonotonic(t, newQueueFixture(t))
}
