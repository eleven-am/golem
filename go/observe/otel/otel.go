// Package otel exports Golem observations as fixed-name OpenTelemetry spans
// and metrics with only closed, checked attributes.
package otel

import (
	"context"
	"errors"
	"time"

	"github.com/eleven-am/golem/go/observe"
	adapterinternal "github.com/eleven-am/golem/go/observe/internal/adapter"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	MeterProvider  metric.MeterProvider
	TracerProvider trace.TracerProvider
	QueueCapacity  int
}

type Adapter struct {
	dispatcher *observe.Dispatcher
}

func New(config Config) (*Adapter, error) {
	if config.MeterProvider == nil || config.TracerProvider == nil {
		return nil, errors.New("GOLEM_OBSERVE_OTEL_CONFIG: meter and tracer providers are required")
	}
	target, err := newSink(config)
	if err != nil {
		return nil, err
	}
	dispatcher, err := observe.NewDispatcher(target, observe.DispatcherConfig{QueueCapacity: config.QueueCapacity})
	if err != nil {
		return nil, err
	}
	return &Adapter{dispatcher: dispatcher}, nil
}

func (adapter *Adapter) ObserveGolem(ctx context.Context, value observe.Observation) {
	if adapter != nil {
		adapter.dispatcher.ObserveGolem(ctx, value)
	}
}

func (adapter *Adapter) Dropped() uint64 {
	if adapter == nil {
		return 0
	}
	return adapter.dispatcher.Dropped()
}

func (adapter *Adapter) Shutdown(ctx context.Context) error {
	if adapter == nil {
		return nil
	}
	return adapter.dispatcher.Shutdown(ctx)
}

type sink struct {
	tracer     trace.Tracer
	records    metric.Int64Counter
	duration   metric.Int64Histogram
	statements metric.Int64Histogram
	attempt    metric.Int64Histogram
	queueDepth metric.Int64Histogram
	queueLimit metric.Int64Histogram
	aggregate  metric.Int64Histogram
}

func newSink(config Config) (sink, error) {
	meter := config.MeterProvider.Meter(adapterinternal.InstrumentationScope)
	result := sink{tracer: config.TracerProvider.Tracer(adapterinternal.InstrumentationScope)}
	var err error
	if result.records, err = meter.Int64Counter(adapterinternal.MetricRecords, metric.WithUnit("{record}")); err != nil {
		return sink{}, errors.New("GOLEM_OBSERVE_OTEL_CONFIG: telemetry instruments are unavailable")
	}
	if result.duration, err = meter.Int64Histogram(adapterinternal.MetricDuration, metric.WithUnit("ns")); err != nil {
		return sink{}, errors.New("GOLEM_OBSERVE_OTEL_CONFIG: telemetry instruments are unavailable")
	}
	if result.statements, err = meter.Int64Histogram(adapterinternal.MetricStatements, metric.WithUnit("{statement}")); err != nil {
		return sink{}, errors.New("GOLEM_OBSERVE_OTEL_CONFIG: telemetry instruments are unavailable")
	}
	if result.attempt, err = meter.Int64Histogram(adapterinternal.MetricAttempt, metric.WithUnit("{attempt}")); err != nil {
		return sink{}, errors.New("GOLEM_OBSERVE_OTEL_CONFIG: telemetry instruments are unavailable")
	}
	if result.queueDepth, err = meter.Int64Histogram(adapterinternal.MetricQueueDepth, metric.WithUnit("{record}")); err != nil {
		return sink{}, errors.New("GOLEM_OBSERVE_OTEL_CONFIG: telemetry instruments are unavailable")
	}
	if result.queueLimit, err = meter.Int64Histogram(adapterinternal.MetricQueueLimit, metric.WithUnit("{record}")); err != nil {
		return sink{}, errors.New("GOLEM_OBSERVE_OTEL_CONFIG: telemetry instruments are unavailable")
	}
	if result.aggregate, err = meter.Int64Histogram(adapterinternal.MetricAggregate, metric.WithUnit("{item}")); err != nil {
		return sink{}, errors.New("GOLEM_OBSERVE_OTEL_CONFIG: telemetry instruments are unavailable")
	}
	return result, nil
}

func (target sink) ObserveGolem(ctx context.Context, value observe.Observation) {
	record := adapterinternal.Normalize(value)
	labels := []attribute.KeyValue{
		attribute.String(adapterinternal.AttributeNames[0], record.Kind),
		attribute.String(adapterinternal.AttributeNames[1], record.Phase),
		attribute.String(adapterinternal.AttributeNames[2], record.Outcome),
		attribute.String(adapterinternal.AttributeNames[3], record.Reason),
		attribute.String(adapterinternal.AttributeNames[4], record.Provider),
		attribute.String(adapterinternal.AttributeNames[5], record.Operation),
		attribute.String(adapterinternal.AttributeNames[6], record.ModelID),
		attribute.String(adapterinternal.AttributeNames[13], record.QueueType),
	}
	spanAttributes := append(append([]attribute.KeyValue(nil), labels...),
		attribute.Int64(adapterinternal.AttributeNames[7], record.DurationNS),
		attribute.Int64(adapterinternal.AttributeNames[8], record.StatementCount),
		attribute.Int64(adapterinternal.AttributeNames[9], record.Attempt),
		attribute.Int64(adapterinternal.AttributeNames[10], record.QueueDepth),
		attribute.Int64(adapterinternal.AttributeNames[11], record.QueueLimit),
		attribute.Int64(adapterinternal.AttributeNames[12], record.AggregateCount),
	)
	now := time.Now()
	_, span := target.tracer.Start(ctx, adapterinternal.SpanName,
		trace.WithTimestamp(now.Add(-value.Duration())),
		trace.WithAttributes(spanAttributes...),
	)
	span.End(trace.WithTimestamp(now))
	options := metric.WithAttributes(labels...)
	target.records.Add(ctx, 1, options)
	target.duration.Record(ctx, record.DurationNS, options)
	target.statements.Record(ctx, record.StatementCount, options)
	target.attempt.Record(ctx, record.Attempt, options)
	target.queueDepth.Record(ctx, record.QueueDepth, options)
	target.queueLimit.Record(ctx, record.QueueLimit, options)
	target.aggregate.Record(ctx, record.AggregateCount, options)
}
