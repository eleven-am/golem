package main

import (
	"context"
	"fmt"
	"time"
)

const (
	steadyWarmup      = 30 * time.Second
	steadyMeasure     = 180 * time.Second
	burstUsers        = 100
	burstFirstOffset  = 60 * time.Second
	burstSpacing      = 60 * time.Second
	burstCount        = 3
	burstTail         = 40 * time.Second
	saturationWarmup  = 5 * time.Second
	saturationMeasure = 55 * time.Second
	drainBound        = 30 * time.Second
	drainPoll         = 100 * time.Millisecond
)

type Kind string

const (
	KindSteady     Kind = "steady"
	KindBurst      Kind = "burst"
	KindSaturation Kind = "saturation"
)

type RunSpec struct {
	Kind    Kind
	Users   int
	Seed    int64
	Warmup  time.Duration
	Measure time.Duration
}

type RunDetail struct {
	Kind           Kind                 `json:"kind"`
	Users          int                  `json:"users"`
	Seed           int64                `json:"seed"`
	MeasuredSecs   float64              `json:"measuredSeconds"`
	RanSecs        float64              `json:"ranSeconds"`
	DrainSecs      float64              `json:"drainSeconds"`
	Classes        map[Class]ClassStats `json:"classes"`
	Failures       Failures             `json:"failures"`
	DeliveryLagP99 float64              `json:"deliveryLagP99Ms"`
	Delivery       DeliveryReport       `json:"delivery"`
	Counters       CounterReport        `json:"counters"`
	Violations     []string             `json:"violations"`
}

func (detail RunDetail) Passed() bool { return len(detail.Violations) == 0 }

func (harness *Harness) begin(ctx context.Context) {
	runContext, cancel := context.WithCancel(ctx)
	harness.mu.Lock()
	harness.ctx, harness.cancel = runContext, cancel
	harness.origin = time.Now()
	harness.mu.Unlock()
}

func (harness *Harness) stopLoad() time.Duration {
	harness.cancel()
	harness.background.Wait()
	return time.Since(harness.origin)
}

func (harness *Harness) settle(ctx context.Context) time.Duration {
	started := time.Now()
	deadline := started.Add(drainBound)
	for !harness.tracker.settled() && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return time.Since(started)
		case <-time.After(drainPoll):
		}
	}
	return time.Since(started)
}

func lagP99(lags []lagSample, spans Spans) float64 {
	var values []float64
	for _, lag := range lags {
		if spans.contains(lag.at) {
			values = append(values, milliseconds(lag.lag))
		}
	}
	return quantilesOf(values).P99
}

func (harness *Harness) detail(spec RunSpec, spans Spans, ran, drain time.Duration) RunDetail {
	samples, arrivals, lags := harness.Samples()
	detail := RunDetail{Kind: spec.Kind, Users: spec.Users, Seed: spec.Seed, MeasuredSecs: spans.Seconds(), RanSecs: ran.Seconds(), DrainSecs: drain.Seconds(), Classes: map[Class]ClassStats{}}
	for _, class := range allClasses {
		detail.Classes[class] = summarize(class, samples, arrivals[class], spans)
	}
	detail.Failures = countFailures(samples)
	detail.DeliveryLagP99 = lagP99(lags, spans)
	detail.Delivery = harness.tracker.report()
	detail.Counters = harness.counters.report()
	detail.Violations = detail.Delivery.Violations()
	if detail.Failures.Errors != 0 {
		detail.Violations = append(detail.Violations, fmt.Sprintf("%d mutation errors (%d deadlocks, %d conflicts)", detail.Failures.Errors, detail.Failures.Deadlocks, detail.Failures.Conflicts))
	}
	return detail
}

func burstStarts() []time.Duration {
	starts := make([]time.Duration, burstCount)
	for index := range starts {
		starts[index] = burstFirstOffset + time.Duration(index)*burstSpacing
	}
	return starts
}

func (harness *Harness) schedule(spec RunSpec) (total time.Duration, spans Spans, err error) {
	switch spec.Kind {
	case KindSteady:
		harness.startSteady(spec.Users, spec.Seed)
		total = spec.Warmup + spec.Measure
		return total, Spans{{From: spec.Warmup, To: total}}, nil
	case KindBurst:
		harness.startSteady(spec.Users, spec.Seed)
		starts := burstStarts()
		for _, start := range starts {
			if err := harness.scheduleBurst(start); err != nil {
				return 0, nil, err
			}
			spans = append(spans, Span{From: start, To: start + burstDuration})
		}
		return starts[len(starts)-1] + burstTail, spans, nil
	case KindSaturation:
		harness.startSaturation(spec.Users)
		total = saturationWarmup + saturationMeasure
		return total, Spans{{From: saturationWarmup, To: total}}, nil
	default:
		return 0, nil, fmt.Errorf("unknown run kind %q", spec.Kind)
	}
}

func (harness *Harness) run(ctx context.Context, spec RunSpec) (RunDetail, error) {
	if err := harness.StartEvents(ctx); err != nil {
		return RunDetail{}, err
	}
	harness.begin(ctx)
	total, spans, err := harness.schedule(spec)
	if err != nil {
		harness.stopLoad()
		return RunDetail{}, err
	}
	harness.sleepUntil(total)
	ran := harness.stopLoad()
	drain := harness.settle(ctx)
	return harness.detail(spec, spans, ran, drain), nil
}

func Execute(ctx context.Context, environment Environment, spec RunSpec) (RunDetail, error) {
	regenerate := 0
	if spec.Kind == KindBurst {
		regenerate = regenerateRows
	}
	harness, err := NewHarness(ctx, environment, spec.Users, regenerate)
	if err != nil {
		return RunDetail{}, err
	}
	detail, err := harness.run(ctx, spec)
	closeErr := harness.Close()
	if err != nil {
		return RunDetail{}, err
	}
	if closeErr != nil {
		return RunDetail{}, closeErr
	}
	return detail, nil
}
