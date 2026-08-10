package otel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	internalvalue "github.com/eleven-am/golem/go/internal/observation"
	"github.com/eleven-am/golem/go/observe"
	metricapi "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type failingMeterProvider struct {
	metricapi.MeterProvider
	meter metricapi.Meter
}

func (provider failingMeterProvider) Meter(string, ...metricapi.MeterOption) metricapi.Meter {
	return provider.meter
}

type failingMeter struct{ metricapi.Meter }

func (failingMeter) Int64Counter(string, ...metricapi.Int64CounterOption) (metricapi.Int64Counter, error) {
	return nil, errors.New("p8-otel-credential-canary")
}

func TestOTelAdapterEmitsFixedSpanMetricsAndClosedAttributes(t *testing.T) {
	reader := metric.NewManualReader()
	meterProvider := metric.NewMeterProvider(metric.WithReader(reader))
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	adapter, err := New(Config{MeterProvider: meterProvider, TracerProvider: tracerProvider, QueueCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	model := golem.ModelID{15: 9}
	internalvalue.Emit(adapter, internalvalue.Value{
		KindValue: string(observe.KindMutation), PhaseValue: string(observe.PhaseFinish),
		OutcomeValue: string(observe.OutcomeRefused), ReasonValue: string(observe.ReasonAuthorization),
		ProviderValue: golem.PostgreSQL, ModelIDValue: model, OperationValue: string(observe.OperationMutationUpdate),
		DurationValue: 7 * time.Millisecond, StatementCountValue: 3, AttemptValue: 2,
		QueueDepthValue: 4, QueueLimitValue: 8, AggregateCountValue: 5,
	})
	deadline := time.Now().Add(time.Second)
	for len(recorder.Ended()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := adapter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "golem.operation.v1" || spans[0].InstrumentationScope().Name != "github.com/eleven-am/golem/go/observe/otel" {
		t.Fatalf("spans=%v", spans)
	}
	attributes := make(map[string]any)
	for _, value := range spans[0].Attributes() {
		attributes[string(value.Key)] = value.Value.AsInterface()
	}
	if len(attributes) != 13 || attributes["golem.operation"] != "mutation.update" || attributes["golem.model_id"] != "00000000000000000000000000000009" || attributes["golem.duration_ns"] != int64(7*time.Millisecond) {
		t.Fatalf("span attributes=%v", attributes)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &metrics); err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, scope := range metrics.ScopeMetrics {
		if scope.Scope.Name != "github.com/eleven-am/golem/go/observe/otel" {
			t.Fatalf("metric scope=%q", scope.Scope.Name)
		}
		for _, value := range scope.Metrics {
			names[value.Name] = true
		}
	}
	for _, name := range []string{
		"golem.observation.records", "golem.observation.duration_ns",
		"golem.observation.statement_count", "golem.observation.attempt",
		"golem.observation.queue_depth", "golem.observation.queue_limit",
		"golem.observation.aggregate_count",
	} {
		if !names[name] {
			t.Fatalf("missing metric %q in %v", name, names)
		}
	}
	if len(names) != 7 {
		t.Fatalf("metric names=%v", names)
	}
}

func TestOTelAdapterRequiresBothProviders(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty providers accepted")
	}
}

func TestOTelAdapterCollapsesAdversarialProviderErrors(t *testing.T) {
	tracerProvider := sdktrace.NewTracerProvider()
	adapter, err := New(Config{MeterProvider: failingMeterProvider{meter: failingMeter{}}, TracerProvider: tracerProvider})
	if adapter != nil || err == nil {
		t.Fatalf("adversarial provider adapter=%v error=%v", adapter, err)
	}
	if strings.Contains(err.Error(), "credential-canary") || err.Error() != "GOLEM_OBSERVE_OTEL_CONFIG: telemetry instruments are unavailable" {
		t.Fatalf("provider error was disclosed: %v", err)
	}
}
