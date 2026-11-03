package main

import (
	"context"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/observe"
)

type Counters struct {
	mu           sync.Mutex
	claimSizes   []float64
	evaluations  int64
	evaluationNs time.Duration
	suppressions map[string]int64
}

func newCounters() *Counters { return &Counters{suppressions: map[string]int64{}} }

func (counters *Counters) ObserveGolem(_ context.Context, observation observe.Observation) {
	if observation.Kind() != observe.KindEvent && observation.Kind() != observe.KindSubscription {
		return
	}
	counters.mu.Lock()
	defer counters.mu.Unlock()
	switch observation.Operation() {
	case observe.OperationEventPublisherClaim:
		if observation.Outcome() == observe.OutcomeSuccess {
			counters.claimSizes = append(counters.claimSizes, float64(observation.AggregateCount()))
		}
	case observe.OperationSubscriptionEvaluation:
		counters.evaluations += observation.AggregateCount()
		counters.evaluationNs += observation.Duration()
	case observe.OperationSubscriptionSuppression:
		counters.suppressions[string(observation.Reason())] += observation.AggregateCount()
	}
}

type CounterReport struct {
	Claims               int              `json:"claims"`
	Claimed              int64            `json:"claimedNotices"`
	ClaimSize            Quantiles        `json:"claimSize"`
	Evaluations          int64            `json:"evaluations"`
	EvaluationsPerNotice float64          `json:"evaluationsPerNotice"`
	JudgeMsPerNotice     float64          `json:"judgeMsPerNotice"`
	Suppressions         map[string]int64 `json:"suppressions"`
}

func (counters *Counters) report() CounterReport {
	counters.mu.Lock()
	defer counters.mu.Unlock()
	report := CounterReport{Claims: len(counters.claimSizes), ClaimSize: quantilesOf(counters.claimSizes), Evaluations: counters.evaluations, Suppressions: map[string]int64{}}
	for _, size := range counters.claimSizes {
		report.Claimed += int64(size)
	}
	for reason, count := range counters.suppressions {
		report.Suppressions[reason] = count
	}
	if report.Claimed != 0 {
		report.EvaluationsPerNotice = float64(counters.evaluations) / float64(report.Claimed)
		report.JudgeMsPerNotice = milliseconds(counters.evaluationNs) / float64(report.Claimed)
	}
	return report
}
