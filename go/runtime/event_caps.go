package runtime

import (
	"sync"

	"github.com/eleven-am/golem/go/events"
)

type subscriptionCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (counter *subscriptionCounter) acquire(key string, maximum int) bool {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	if counter.counts[key] >= maximum {
		return false
	}
	if counter.counts == nil {
		counter.counts = make(map[string]int)
	}
	counter.counts[key]++
	return true
}

func (counter *subscriptionCounter) release(key string) {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	if counter.counts[key] <= 1 {
		delete(counter.counts, key)
		return
	}
	counter.counts[key]--
}

func (app *App[P, A]) reserveSubscription(principal P) (func(), error) {
	maximum := app.eventLimits.MaxSubscriptionsPerPrincipal
	if maximum == 0 || app.subscriptionPrincipalKey == nil {
		return func() {}, nil
	}
	key, counted := app.subscriptionPrincipalKey(principal)
	if !counted {
		return func() {}, nil
	}
	if !app.subscriptionCounts.acquire(key, maximum) {
		return nil, events.Failure(events.CodeSubscriptionLimit)
	}
	var once sync.Once
	return func() { once.Do(func() { app.subscriptionCounts.release(key) }) }, nil
}
