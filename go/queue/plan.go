package queue

import "time"

type Plan struct {
	Concurrency   int
	ClaimBatch    int
	MaxConcurrent map[string]int
	Capacities    map[string]int
}

type PlanStatus struct {
	Generation    uint64
	Concurrency   int
	ClaimBatch    int
	MaxConcurrent map[string]int
	Capacities    map[string]int
	AppliedAt     time.Time
}
