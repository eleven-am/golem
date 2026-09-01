package runtime

import (
	"context"
	standardslog "log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	observeotel "github.com/eleven-am/golem/go/observe/otel"
	observeslog "github.com/eleven-am/golem/go/observe/slog"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type p8ObservationFanout []observe.Observer

func (targets p8ObservationFanout) ObserveGolem(ctx context.Context, value observe.Observation) {
	for _, target := range targets {
		target.ObserveGolem(ctx, value)
	}
}

type p8SlogCapture struct {
	mu      sync.Mutex
	records []standardslog.Record
}

func (*p8SlogCapture) Enabled(context.Context, standardslog.Level) bool { return true }
func (capture *p8SlogCapture) Handle(_ context.Context, record standardslog.Record) error {
	capture.mu.Lock()
	capture.records = append(capture.records, record.Clone())
	capture.mu.Unlock()
	return nil
}
func (capture *p8SlogCapture) WithAttrs([]standardslog.Attr) standardslog.Handler { return capture }
func (capture *p8SlogCapture) WithGroup(string) standardslog.Handler              { return capture }

func TestSlogAndOpenTelemetryAdapterAgreement(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, acceptance mutationProviderAcceptanceFixture) {
		assertP8AdapterAgreement(t, acceptance.fixture)
	})
}

func assertP8AdapterAgreement(t *testing.T, fixture mutationResultFixture) {
	t.Helper()
	raw := &p8ObservationCollector{}
	slogCapture := &p8SlogCapture{}
	slogAdapter, err := observeslog.New(observeslog.Config{Logger: standardslog.New(slogCapture), QueueCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	otelAdapter, err := observeotel.New(observeotel.Config{MeterProvider: meterProvider, TracerProvider: tracerProvider, QueueCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app.observer = p8ObservationFanout{raw, slogAdapter, otelAdapter}
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, fixture.createPost(91, golem.UUID{15: 1}, "adapter-agreement")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(raw.matching(observe.KindMutation, observe.OperationMutationCreate)) == 1 && p8SlogOperationCount(slogCapture, "mutation.create") == 1 && p8OTelOperationCount(spanRecorder, "mutation.create") == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := slogAdapter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := otelAdapter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	want := raw.matching(observe.KindMutation, observe.OperationMutationCreate)
	if len(want) != 1 {
		t.Fatalf("raw mutation observations=%v", want)
	}
	slogValues := p8SlogOperation(slogCapture, "mutation.create")
	otelValues := p8OTelOperation(spanRecorder, "mutation.create")
	if len(slogValues) != 1 || len(otelValues) != 1 {
		t.Fatalf("adapter records slog=%v otel=%v", slogValues, otelValues)
	}
	if !reflect.DeepEqual(slogValues[0], otelValues[0]) || len(slogValues[0]) != 14 {
		t.Fatalf("slog/OTel closed records disagree: slog=%v otel=%v", slogValues[0], otelValues[0])
	}
	for _, values := range []map[string]any{slogValues[0], otelValues[0]} {
		if values["golem.kind"] != string(want[0].kind) || values["golem.phase"] != string(want[0].phase) || values["golem.outcome"] != string(want[0].outcome) || values["golem.reason"] != string(want[0].reason) || values["golem.provider"] != string(want[0].provider) || values["golem.operation"] != string(want[0].operation) || values["golem.statement_count"] != int64(want[0].statements) || values["golem.aggregate_count"] != want[0].aggregate {
			t.Fatalf("adapter disagrees with immutable record: got=%v want=%+v", values, want[0])
		}
	}
	p8AssertOTelMetricAgreement(t, reader, slogValues[0])
}

func p8AssertOTelMetricAgreement(t *testing.T, reader *sdkmetric.ManualReader, record map[string]any) {
	t.Helper()
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		"golem.observation.records":         1,
		"golem.observation.duration_ns":     record["golem.duration_ns"].(int64),
		"golem.observation.statement_count": record["golem.statement_count"].(int64),
		"golem.observation.attempt":         record["golem.attempt"].(int64),
		"golem.observation.queue_depth":     record["golem.queue_depth"].(int64),
		"golem.observation.queue_limit":     record["golem.queue_limit"].(int64),
		"golem.observation.aggregate_count": record["golem.aggregate_count"].(int64),
	}
	seen := make(map[string]bool)
	for _, scope := range metrics.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			expected, ok := want[instrument.Name]
			if !ok {
				t.Fatalf("unexpected OTel metric %q", instrument.Name)
			}
			switch data := instrument.Data.(type) {
			case metricdata.Sum[int64]:
				if len(data.DataPoints) != 1 || data.DataPoints[0].Value != expected {
					t.Fatalf("metric %s datapoints=%v want %d", instrument.Name, data.DataPoints, expected)
				}
			case metricdata.Histogram[int64]:
				if len(data.DataPoints) != 1 || data.DataPoints[0].Count != 1 || data.DataPoints[0].Sum != expected {
					t.Fatalf("metric %s datapoints=%v want %d", instrument.Name, data.DataPoints, expected)
				}
			default:
				t.Fatalf("metric %s has type %T", instrument.Name, instrument.Data)
			}
			seen[instrument.Name] = true
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("OTel metrics=%v want=%v", seen, want)
	}
}

func p8SlogOperationCount(capture *p8SlogCapture, operation string) int {
	return len(p8SlogOperation(capture, operation))
}

func p8SlogOperation(capture *p8SlogCapture, operation string) []map[string]any {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	var result []map[string]any
	for _, record := range capture.records {
		values := make(map[string]any)
		record.Attrs(func(attribute standardslog.Attr) bool { values[attribute.Key] = attribute.Value.Any(); return true })
		if values["golem.operation"] == operation {
			result = append(result, values)
		}
	}
	return result
}

func p8OTelOperationCount(recorder *tracetest.SpanRecorder, operation string) int {
	return len(p8OTelOperation(recorder, operation))
}

func p8OTelOperation(recorder *tracetest.SpanRecorder, operation string) []map[string]any {
	var result []map[string]any
	for _, span := range recorder.Ended() {
		values := make(map[string]any)
		for _, attribute := range span.Attributes() {
			values[string(attribute.Key)] = attribute.Value.AsInterface()
		}
		if values["golem.operation"] == operation {
			result = append(result, values)
		}
	}
	return result
}

type p8ObserverFunc func(context.Context, observe.Observation)

func (observer p8ObserverFunc) ObserveGolem(ctx context.Context, value observe.Observation) {
	observer(ctx, value)
}

func TestObserverPanicBlockAndOutageCannotAlterCorrectness(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, acceptance mutationProviderAcceptanceFixture) {
		fixture := acceptance.fixture
		t.Run("panic", func(t *testing.T) {
			fixture.app.observer = p8ObserverFunc(func(context.Context, observe.Observation) { panic("observer outage") })
			if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, fixture.createPost(92, golem.UUID{15: 1}, "panic-safe")); err != nil {
				t.Fatalf("observer panic altered result: %v", err)
			}
		})
		t.Run("block-after-commit", func(t *testing.T) {
			observer := &p8BlockingObserver{kind: observe.KindMutation, operation: observe.OperationMutationCreate, entered: make(chan p8ObservationSnapshot, 1), release: make(chan struct{})}
			fixture.app.observer = observer
			done := make(chan error, 1)
			go func() {
				_, err := SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, fixture.createPost(93, golem.UUID{15: 1}, "blocked-observer"))
				done <- err
			}()
			p8AssertPoolReleasedWhileObserverBlocked(t, fixture.app, observer.entered)
			selector := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 93}))
			if _, err := SystemFindUnique(context.Background(), fixture.app.System(), fixture.postDescriptor, selector); err != nil {
				t.Fatalf("committed result was not readable while observer blocked: %v", err)
			}
			close(observer.release)
			if err := <-done; err != nil {
				t.Fatalf("observer return altered committed result: %v", err)
			}
		})
	})
}
