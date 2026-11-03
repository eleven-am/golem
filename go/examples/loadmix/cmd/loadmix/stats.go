package main

import (
	"math"
	"sort"
	"time"
)

type Class string

const (
	ClassArticle      Class = "Article"
	ClassArticleAudio Class = "ArticleAudio"
	ClassChatMessage  Class = "ChatMessage"
	ClassControl      Class = "ReadingSession"
)

var allClasses = []Class{ClassArticle, ClassArticleAudio, ClassChatMessage, ClassControl}

type Failure string

const (
	FailureNone     Failure = ""
	FailureDeadlock Failure = "deadlock"
	FailureConflict Failure = "conflict"
	FailureOther    Failure = "error"
)

type Sample struct {
	Class   Class
	Arrived time.Duration
	Done    time.Duration
	Failure Failure
}

func (sample Sample) Latency() time.Duration { return sample.Done - sample.Arrived }

type Quantiles struct {
	P50 float64 `json:"p50"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

func milliseconds(value time.Duration) float64 { return float64(value) / float64(time.Millisecond) }

func quantile(sorted []float64, fraction float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(fraction*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	return sorted[rank]
}

func quantilesOf(values []float64) Quantiles {
	if len(values) == 0 {
		return Quantiles{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return Quantiles{P50: quantile(sorted, 0.50), P99: quantile(sorted, 0.99), Max: sorted[len(sorted)-1]}
}

type Span struct {
	From time.Duration
	To   time.Duration
}

type Spans []Span

func (spans Spans) contains(at time.Duration) bool {
	for _, span := range spans {
		if at >= span.From && at < span.To {
			return true
		}
	}
	return false
}

func (spans Spans) Seconds() float64 {
	var total time.Duration
	for _, span := range spans {
		total += span.To - span.From
	}
	return total.Seconds()
}

type ClassStats struct {
	Offered      int       `json:"offered"`
	Achieved     int       `json:"achieved"`
	PerSecond    float64   `json:"perSecond"`
	LatencyMilli Quantiles `json:"latencyMs"`
}

func summarize(class Class, samples []Sample, arrivals []time.Duration, span Spans) ClassStats {
	stats := ClassStats{}
	for _, arrival := range arrivals {
		if span.contains(arrival) {
			stats.Offered++
		}
	}
	var latency []float64
	for _, sample := range samples {
		if sample.Class != class || !span.contains(sample.Done) || sample.Failure != FailureNone {
			continue
		}
		stats.Achieved++
		latency = append(latency, milliseconds(sample.Latency()))
	}
	if seconds := span.Seconds(); seconds > 0 {
		stats.PerSecond = float64(stats.Achieved) / seconds
	}
	stats.LatencyMilli = quantilesOf(latency)
	return stats
}

type Failures struct {
	Errors    int `json:"errors"`
	Deadlocks int `json:"deadlocks"`
	Conflicts int `json:"conflicts"`
}

func countFailures(samples []Sample) Failures {
	var result Failures
	for _, sample := range samples {
		switch sample.Failure {
		case FailureNone:
		case FailureDeadlock:
			result.Deadlocks++
			result.Errors++
		case FailureConflict:
			result.Conflicts++
			result.Errors++
		default:
			result.Errors++
		}
	}
	return result
}
