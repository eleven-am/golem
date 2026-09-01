// Package adapter owns the one safe, closed normalization shared by public
// observation adapters. It deliberately has no field for arbitrary text.
package adapter

import (
	"encoding/hex"

	"github.com/eleven-am/golem/go/observe"
)

const (
	SlogMessage          = "golem.observation.v1"
	InstrumentationScope = "github.com/eleven-am/golem/go/observe/otel"
	SpanName             = "golem.operation.v1"
	MetricRecords        = "golem.observation.records"
	MetricDuration       = "golem.observation.duration_ns"
	MetricStatements     = "golem.observation.statement_count"
	MetricAttempt        = "golem.observation.attempt"
	MetricQueueDepth     = "golem.observation.queue_depth"
	MetricQueueLimit     = "golem.observation.queue_limit"
	MetricAggregate      = "golem.observation.aggregate_count"
)

var AttributeNames = [...]string{
	"golem.kind",
	"golem.phase",
	"golem.outcome",
	"golem.reason",
	"golem.provider",
	"golem.operation",
	"golem.model_id",
	"golem.duration_ns",
	"golem.statement_count",
	"golem.attempt",
	"golem.queue_depth",
	"golem.queue_limit",
	"golem.aggregate_count",
	"golem.queue.type",
}

type Record struct {
	Kind           string
	Phase          string
	Outcome        string
	Reason         string
	Provider       string
	Operation      string
	ModelID        string
	DurationNS     int64
	StatementCount int64
	Attempt        int64
	QueueDepth     int64
	QueueLimit     int64
	AggregateCount int64
	QueueType      string
}

func Normalize(value observe.Observation) Record {
	model := value.ModelID()
	return Record{
		Kind:           string(value.Kind()),
		Phase:          string(value.Phase()),
		Outcome:        string(value.Outcome()),
		Reason:         string(value.Reason()),
		Provider:       string(value.Provider()),
		Operation:      string(value.Operation()),
		ModelID:        hex.EncodeToString(model[:]),
		DurationNS:     int64(value.Duration()),
		StatementCount: int64(value.StatementCount()),
		Attempt:        int64(value.Attempt()),
		QueueDepth:     int64(value.QueueDepth()),
		QueueLimit:     int64(value.QueueLimit()),
		AggregateCount: value.AggregateCount(),
		QueueType:      value.QueueType(),
	}
}
