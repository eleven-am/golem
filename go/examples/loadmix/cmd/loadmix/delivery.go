package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
)

const unclassifiedTermination = "UNCLASSIFIED"

type eventKey struct {
	row        golem.UUID
	id         golem.EventID
	recordedAt time.Time
}

type subscription struct {
	owner      golem.UUID
	class      Class
	expected   atomic.Int64
	mu         sync.Mutex
	seen       map[golem.EventID]struct{}
	duplicates int
	terminated string
}

func (subscription *subscription) delivered() int64 {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	return int64(len(subscription.seen))
}

func (subscription *subscription) endedBy() string {
	subscription.mu.Lock()
	defer subscription.mu.Unlock()
	return subscription.terminated
}

type tracker struct {
	owners  sync.Map
	closing atomic.Bool
	fault   func()
	lag     func(time.Time)

	mu           sync.Mutex
	subs         []*subscription
	terminations map[string]int
	foreign      int
}

func newTracker(fault func(), lag func(time.Time)) *tracker {
	return &tracker{fault: fault, lag: lag, terminations: map[string]int{}}
}

func (tracker *tracker) register(row, owner golem.UUID) { tracker.owners.Store(row, owner) }

func (tracker *tracker) subscribe(owner golem.UUID, class Class) *subscription {
	created := &subscription{owner: owner, class: class, seen: map[golem.EventID]struct{}{}}
	tracker.mu.Lock()
	tracker.subs = append(tracker.subs, created)
	tracker.mu.Unlock()
	return created
}

func (tracker *tracker) stopListening() { tracker.closing.Store(true) }

func (tracker *tracker) terminated(subscription *subscription, err error) {
	if tracker.closing.Load() {
		return
	}
	code := unclassifiedTermination
	if classified, ok := events.CodeOf(err); ok {
		code = string(classified)
	}
	subscription.mu.Lock()
	subscription.terminated = code
	subscription.mu.Unlock()
	tracker.mu.Lock()
	tracker.terminations[code]++
	tracker.mu.Unlock()
	tracker.fault()
}

func (tracker *tracker) delivery(subscription *subscription, key eventKey) {
	owner, known := tracker.owners.Load(key.row)
	if !known || owner.(golem.UUID) != subscription.owner {
		tracker.mu.Lock()
		tracker.foreign++
		tracker.mu.Unlock()
		tracker.fault()
		return
	}
	subscription.mu.Lock()
	_, repeated := subscription.seen[key.id]
	if repeated {
		subscription.duplicates++
	} else {
		subscription.seen[key.id] = struct{}{}
	}
	subscription.mu.Unlock()
	if !repeated && tracker.lag != nil {
		tracker.lag(key.recordedAt)
	}
}

func drain[E any](ctx context.Context, tracker *tracker, subscription *subscription, stream golem.EventStream[E], key func(E) eventKey) {
	for {
		event, err := stream.Recv(ctx)
		if err != nil {
			tracker.terminated(subscription, err)
			return
		}
		tracker.delivery(subscription, key(event))
	}
}

func (tracker *tracker) settled() bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for _, subscription := range tracker.subs {
		if subscription.endedBy() == "" && subscription.delivered() < subscription.expected.Load() {
			return false
		}
	}
	return true
}

type DeliveryReport struct {
	Expected            int64          `json:"expected"`
	Delivered           int64          `json:"delivered"`
	Foreign             int            `json:"foreign"`
	Duplicates          int            `json:"duplicates"`
	Surplus             int64          `json:"surplus"`
	Shortfall           int64          `json:"shortfall"`
	ShortSubscriptions  int            `json:"shortSubscriptions"`
	TerminatedShortfall int64          `json:"terminatedShortfall"`
	Terminations        map[string]int `json:"terminations"`
}

func (tracker *tracker) report() DeliveryReport {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	report := DeliveryReport{Foreign: tracker.foreign, Terminations: map[string]int{}}
	for code, count := range tracker.terminations {
		report.Terminations[code] = count
	}
	for _, subscription := range tracker.subs {
		expected, delivered := subscription.expected.Load(), subscription.delivered()
		report.Expected += expected
		report.Delivered += delivered
		subscription.mu.Lock()
		report.Duplicates += subscription.duplicates
		subscription.mu.Unlock()
		if delivered > expected {
			report.Surplus += delivered - expected
		}
		if delivered < expected {
			report.Shortfall += expected - delivered
			if subscription.endedBy() == "" {
				report.ShortSubscriptions++
			} else {
				report.TerminatedShortfall += expected - delivered
			}
		}
	}
	return report
}

func (report DeliveryReport) Violations() []string {
	var violations []string
	for code, count := range report.Terminations {
		violations = append(violations, fmt.Sprintf("%d stream(s) terminated with %s", count, code))
	}
	if report.Foreign != 0 {
		violations = append(violations, fmt.Sprintf("%d foreign deliveries", report.Foreign))
	}
	if report.Duplicates != 0 {
		violations = append(violations, fmt.Sprintf("%d duplicate deliveries", report.Duplicates))
	}
	if report.Surplus != 0 {
		violations = append(violations, fmt.Sprintf("%d deliveries beyond the expected", report.Surplus))
	}
	if report.Shortfall != 0 {
		violations = append(violations, fmt.Sprintf("shortfall %d of %d expected events (%d subscriptions still short, %d events lost on terminated streams)", report.Shortfall, report.Expected, report.ShortSubscriptions, report.TerminatedShortfall))
	}
	return violations
}
