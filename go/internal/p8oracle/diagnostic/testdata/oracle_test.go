package diagnosticconsumer

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	standardslog "log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	observeotel "github.com/eleven-am/golem/go/observe/otel"
	observeslog "github.com/eleven-am/golem/go/observe/slog"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const diagnosticUserID = "a5000000-0000-0000-0000-000000000001"
const diagnosticPostID = "a6000000-0000-0000-0000-000000000001"

type closedObservation struct {
	Kind       observe.Kind      `json:"kind"`
	Operation  observe.Operation `json:"operation"`
	Outcome    observe.Outcome   `json:"outcome"`
	Reason     observe.Reason    `json:"reason"`
	Statements int               `json:"statements"`
	Aggregate  int64             `json:"aggregate"`
}

type closedOperatorAudit struct {
	Action     events.OperatorAuditAction  `json:"action"`
	Outcome    events.OperatorAuditOutcome `json:"outcome"`
	Causations int                         `json:"causations"`
	Facts      int                         `json:"facts"`
}

type recorder struct {
	mu     sync.Mutex
	values []closedObservation
}

type safeBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

type observerFanout []observe.Observer

func (fanout observerFanout) ObserveGolem(ctx context.Context, value observe.Observation) {
	for _, observer := range fanout {
		observer.ObserveGolem(ctx, value)
	}
}

func (buffer *safeBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(value)
}

func (buffer *safeBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func (buffer *safeBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.buffer.Bytes()...)
}

func (recorder *recorder) ObserveGolem(_ context.Context, value observe.Observation) {
	recorder.mu.Lock()
	recorder.values = append(recorder.values, closedObservation{
		Kind: value.Kind(), Operation: value.Operation(), Outcome: value.Outcome(), Reason: value.Reason(),
		Statements: value.StatementCount(), Aggregate: value.AggregateCount(),
	})
	recorder.mu.Unlock()
}

func (recorder *recorder) snapshot() []closedObservation {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]closedObservation(nil), recorder.values...)
}

func TestP8ExternalOracleScenario(t *testing.T) {
	switch os.Getenv("P8_ORACLE_SCENARIO") {
	case "diagnostic-telemetry":
		diagnosticTelemetry(t)
	case "raw-provider-error":
		rawProviderError(t)
	case "health-safe-shape":
		healthSafeShape(t)
	default:
		t.Fatalf("unknown diagnostic scenario %q", os.Getenv("P8_ORACLE_SCENARIO"))
	}
}

func healthSafeShape(t *testing.T) {
	canary := randomCanary(t, "HEALTH_DSN_CREDENTIAL_SCHEMA_BACKLOG_PRINCIPAL")
	dsn := canaryDataSourceName(t, os.Getenv("P8_ORACLE_DSN"), canary)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	binary := t.TempDir() + "/social"
	build := exec.Command("go", "build", "-o", binary, "./cmd/social")
	build.Dir = os.Getenv("P8_ORACLE_EXAMPLE")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build social host: %v\n%s", err, output)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, binary)
	command.Env = append(os.Environ(),
		"GOLEM_PROVIDER="+os.Getenv("P8_ORACLE_PROVIDER"),
		"GOLEM_DATABASE_DSN="+dsn,
		"GOLEM_HTTP_ADDRESS="+address,
	)
	var output safeBuffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- command.Wait() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		default:
		}
	})

	client := &http.Client{Timeout: 300 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, requestErr := client.Get("http://" + address + "/health/ready")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("health host did not become ready; output=%q", output.String())
		}
		select {
		case err := <-stopped:
			t.Fatalf("health host stopped early: %v output=%q", err, output.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	for _, path := range []string{"/health/live", "/health/ready"} {
		response, err := client.Get("http://" + address + path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1025))
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusNoContent || len(body) != 0 {
			t.Fatalf("%s status=%d body=%q", path, response.StatusCode, body)
		}
		encodedHeaders, _ := json.Marshal(response.Header)
		assertClosedPublic(t, path+" headers", encodedHeaders, canary)
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("health host shutdown: %v output=%q", err, output.String())
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("health host did not stop")
	}
	assertClosedPublic(t, "health host logs", output.Bytes(), canary)
	if strings.Contains(output.String(), dsn) {
		t.Fatal("health host log disclosed its DSN")
	}
}

func canaryDataSourceName(t *testing.T, source, canary string) string {
	t.Helper()
	parsed, err := url.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if os.Getenv("P8_ORACLE_PROVIDER") == "postgresql" {
		query.Set("application_name", canary)
	} else {
		query.Set("p8_health_label", canary)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func rawProviderError(t *testing.T) {
	canary := randomCanary(t, "RAW_PROVIDER_DSN_SQL_BIND_ROW")
	assertProviderOpenErrorClosed(t, canary)
	database := openDatabase(t)
	defer database.Close()
	observer := &recorder{}
	app, caller, graph, trusted := openApplication(t, database, observer)
	defer graph.Shutdown(context.Background())
	seedMutationTarget(t, app)
	installCanaryUpdateTrigger(t, database, canary)

	postID := mustUUID(t, diagnosticPostID)
	_, callerErr := caller.Posts.Update(context.Background(), social.Posts.ByID.Value(postID),
		social.Posts.Update(social.Posts.Title.Set("caller update must abort")),
	)
	if callerErr == nil {
		t.Fatal("Caller provider mutation failure unexpectedly succeeded")
	}
	assertClosedPublic(t, "Caller provider error", []byte(callerErr.Error()), canary)
	// Unwrap is a deliberate trusted-logging escape retained by P3. It proves
	// the database really returned our canary; Error(), GraphQL serialization,
	// observations, and adapters below are the public/operational channels that
	// must remain closed.
	if !errorChainContains(callerErr, canary) {
		t.Fatalf("trusted Caller chain does not prove injected provider canary: %v", callerErr)
	}

	response := graphRequest(t, graph.Handler(), `mutation { updatePost(where: {ID: "`+diagnosticPostID+`"}, data: {title: {set: "GraphQL update must abort"}}) { id title } }`)
	if len(response.Errors) == 0 {
		t.Fatal("GraphQL provider failure has no public error")
	}
	assertClosedPublic(t, "GraphQL provider error", mustJSON(response), canary)

	trustedErrors := trusted()
	// A recognized transport-neutral Golem error is deliberately not forwarded
	// to GraphQL's unknown-error reporter; its wrapped provider cause stays on
	// the trusted Caller error chain instead.
	if len(trustedErrors) != 0 {
		t.Fatalf("recognized provider failure reached GraphQL trusted callback=%v", trustedErrors)
	}
	encoded, err := json.Marshal(observer.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	assertClosedPublic(t, "observations", encoded, canary)
	closedFailures := 0
	for _, observation := range observer.snapshot() {
		if (observation.Outcome == observe.OutcomeFailure || observation.Outcome == observe.OutcomeRefused) &&
			(observation.Operation == observe.OperationMutationUpdate || observation.Operation == observe.OperationGraphQLMutation) {
			closedFailures++
		}
	}
	if closedFailures < 3 {
		t.Fatalf("closed mutation failure observations=%d values=%v", closedFailures, observer.snapshot())
	}
}

func errorChainContains(err error, canary string) bool {
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), canary) {
			return true
		}
	}
	return false
}

func seedMutationTarget(t *testing.T, app *social.App[social.Principal]) {
	t.Helper()
	ctx := context.Background()
	userID := mustUUID(t, diagnosticUserID)
	if _, err := app.System().Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(userID), social.Users.Handle.Create("diagnostic-user"),
		social.Users.Email.Create("diagnostic-user@example.test"),
	)); err != nil {
		t.Fatal(err)
	}
	date, err := golem.ParseDate("2026-08-09")
	if err != nil {
		t.Fatal(err)
	}
	clock, err := golem.ParseTime("12:34:56")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := golem.NewJSONDocument[any]([]byte(`{"language":"en","pinned":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.System().Posts.Create(ctx, social.Posts.Create(
		social.Posts.ID.Create(mustUUID(t, diagnosticPostID)), social.Posts.AuthorID.Create(userID),
		social.Posts.Title.Create("provider failure target"), social.Posts.Body.Create("protected body"),
		social.Posts.Published.Create(false), social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock),
		social.Posts.Metadata.Create(metadata), social.Posts.Topics.Create(golem.List[string]{"diagnostic"}),
	)); err != nil {
		t.Fatal(err)
	}
}

func installCanaryUpdateTrigger(t *testing.T, database *provider.Database, canary string) {
	t.Helper()
	var statements []string
	switch database.Provider() {
	case golem.SQLite:
		statements = []string{`CREATE TRIGGER p8_canary_update BEFORE UPDATE ON posts BEGIN SELECT RAISE(ABORT, '` + canary + `'); END`}
	case golem.PostgreSQL:
		statements = []string{
			`CREATE FUNCTION p8_canary_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION '` + canary + `'; END $$`,
			`CREATE TRIGGER p8_canary_update BEFORE UPDATE ON posts FOR EACH ROW EXECUTE FUNCTION p8_canary_update()`,
		}
	default:
		t.Fatalf("unsupported provider %q", database.Provider())
	}
	for _, statement := range statements {
		if _, err := database.UnsafeSQLX().ExecContext(context.Background(), statement); err != nil {
			t.Fatal(err)
		}
	}
}

func diagnosticTelemetry(t *testing.T) {
	canary := randomCanary(t, "CLI_DIAGNOSTIC_CREDENTIAL_DSN_SQL_ROW_PRINCIPAL")
	cli := os.Getenv("P8_ORACLE_CLI")
	if cli == "" {
		t.Fatal("P8_ORACLE_CLI is required")
	}
	dsn := "postgresql://" + canary + ":" + canary + "@127.0.0.1:1/" + canary + "?connect_timeout=1"
	for _, arguments := range [][]string{{"version", "--json"}, {"doctor", "--provider", "postgresql", "--dsn", dsn, "--json"}} {
		command := exec.Command(cli, arguments...)
		command.Dir = os.Getenv("P8_ORACLE_EXAMPLE")
		output, err := command.CombinedOutput()
		if arguments[0] == "version" && err != nil {
			t.Fatalf("version failed: %v output=%s", err, output)
		}
		if arguments[0] == "doctor" && err == nil {
			t.Fatal("doctor unexpectedly accepted the invalid canary DSN")
		}
		if len(output) > 8192 {
			t.Fatalf("CLI diagnostic output is unbounded: %d", len(output))
		}
		assertClosedPublic(t, "CLI "+arguments[0], output, canary)
	}

	var slogOutput safeBuffer
	logger := standardslog.New(standardslog.NewJSONHandler(&slogOutput, &standardslog.HandlerOptions{
		ReplaceAttr: func(_ []string, attribute standardslog.Attr) standardslog.Attr {
			if attribute.Key == standardslog.TimeKey {
				return standardslog.Attr{}
			}
			return attribute
		},
	}))
	slogAdapter, err := observeslog.New(observeslog.Config{Logger: logger, QueueCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	otelAdapter, err := observeotel.New(observeotel.Config{
		MeterProvider: meterProvider, TracerProvider: tracerProvider, QueueCapacity: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := &recorder{}
	database := openDatabase(t)
	defer database.Close()
	var operatorMu sync.Mutex
	operatorAudits := []closedOperatorAudit{}
	app, caller, graph, _ := openApplication(t, database, observerFanout{closed, slogAdapter, otelAdapter}, func(_ context.Context, record events.OperatorAuditRecord) {
		operatorMu.Lock()
		operatorAudits = append(operatorAudits, closedOperatorAudit{
			Action: record.Action(), Outcome: record.Outcome(), Causations: record.Causations(), Facts: record.Facts(),
		})
		operatorMu.Unlock()
	})
	defer graph.Shutdown(context.Background())
	userID := mustUUID(t, diagnosticUserID)
	if _, err := app.System().Users.Create(context.Background(), social.Users.Create(
		social.Users.ID.Create(userID), social.Users.Handle.Create("telemetry-user"), social.Users.Email.Create(canary),
	)); err != nil {
		t.Fatal(err)
	}
	rows, err := caller.Users.FindMany(context.Background(),
		social.Users.Where(social.Users.ID.Eq(userID)), social.Users.Select(social.Users.ID, social.Users.Email), social.Users.Take(1),
	)
	if err != nil || len(rows) != 1 {
		t.Fatalf("telemetry source read rows=%d error=%v", len(rows), err)
	}
	retention, err := events.NewRetentionPolicy(time.Now().Add(-24*time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.EventOperator().RunRetention(context.Background(), retention); err != nil {
		t.Fatal(err)
	}
	operatorMu.Lock()
	operatorCount := len(operatorAudits)
	operatorMu.Unlock()
	if operatorCount != 1 {
		t.Fatalf("event operator audit records=%d want=1", operatorCount)
	}
	wantObservations := len(closed.snapshot())
	deliveryDeadline := time.Now().Add(3 * time.Second)
	for (slogLineCount(slogOutput.Bytes()) < wantObservations || len(spanRecorder.Ended()) < wantObservations) && time.Now().Before(deliveryDeadline) {
		time.Sleep(time.Millisecond)
	}
	if gotSlog, gotOTel := slogLineCount(slogOutput.Bytes()), len(spanRecorder.Ended()); gotSlog != wantObservations || gotOTel != wantObservations {
		t.Fatalf("telemetry delivery slog=%d otel=%d want=%d", gotSlog, gotOTel, wantObservations)
	}
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelShutdown()
	if err := slogAdapter.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	if err := otelAdapter.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}
	if slogAdapter.Dropped() != 0 || otelAdapter.Dropped() != 0 {
		t.Fatalf("telemetry dropped observations slog=%d otel=%d", slogAdapter.Dropped(), otelAdapter.Dropped())
	}
	assertClosedPublic(t, "slog telemetry", slogOutput.Bytes(), canary)
	assertSlogShape(t, slogOutput.Bytes())
	assertClosedPublic(t, "closed observations", mustJSON(closed.snapshot()), canary)
	operatorMu.Lock()
	encodedOperatorAudits := mustJSON(operatorAudits)
	operatorMu.Unlock()
	assertClosedPublic(t, "event operator audits", encodedOperatorAudits, canary)
	assertOTelShape(t, reader, spanRecorder, canary)
}

func slogLineCount(encoded []byte) int {
	trimmed := bytes.TrimSpace(encoded)
	if len(trimmed) == 0 {
		return 0
	}
	return len(bytes.Split(trimmed, []byte{'\n'}))
}

func assertSlogShape(t *testing.T, encoded []byte) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(encoded), []byte{'\n'})
	if len(lines) < 3 {
		t.Fatalf("slog observation count=%d output=%s", len(lines), encoded)
	}
	allowed := map[string]bool{
		"level": true, "msg": true, "golem.kind": true, "golem.phase": true, "golem.outcome": true,
		"golem.reason": true, "golem.provider": true, "golem.operation": true, "golem.model_id": true,
		"golem.duration_ns": true, "golem.statement_count": true, "golem.attempt": true,
		"golem.queue_depth": true, "golem.queue_limit": true, "golem.aggregate_count": true,
		"golem.queue.type": true,
	}
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode slog record=%q: %v", line, err)
		}
		if record["msg"] != "golem.observation.v1" || record["level"] != "INFO" || len(record) != 16 {
			t.Fatalf("unexpected slog shape=%v", record)
		}
		for key := range record {
			if !allowed[key] {
				t.Fatalf("unexpected slog attribute %q", key)
			}
		}
	}
}

func assertOTelShape(t *testing.T, reader *sdkmetric.ManualReader, recorder *tracetest.SpanRecorder, canary string) {
	t.Helper()
	spans := recorder.Ended()
	if len(spans) < 3 {
		t.Fatalf("OTel span count=%d", len(spans))
	}
	allowed := telemetryAttributeNames()
	for _, span := range spans {
		if span.Name() != "golem.operation.v1" || span.InstrumentationScope().Name != "github.com/eleven-am/golem/go/observe/otel" || len(span.Attributes()) != 14 {
			t.Fatalf("unexpected OTel span name=%q scope=%q attrs=%v", span.Name(), span.InstrumentationScope().Name, span.Attributes())
		}
		assertOTelAttributes(t, span.Attributes(), allowed, canary)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	wantMetrics := map[string]bool{
		"golem.observation.records": true, "golem.observation.duration_ns": true,
		"golem.observation.statement_count": true, "golem.observation.attempt": true,
		"golem.observation.queue_depth": true, "golem.observation.queue_limit": true,
		"golem.observation.aggregate_count": true,
	}
	gotMetrics := map[string]bool{}
	for _, scope := range metrics.ScopeMetrics {
		if scope.Scope.Name != "github.com/eleven-am/golem/go/observe/otel" {
			t.Fatalf("unexpected metric scope=%q", scope.Scope.Name)
		}
		for _, metric := range scope.Metrics {
			if !wantMetrics[metric.Name] {
				t.Fatalf("unexpected metric name=%q", metric.Name)
			}
			gotMetrics[metric.Name] = true
			switch data := metric.Data.(type) {
			case metricdata.Sum[int64]:
				for _, point := range data.DataPoints {
					assertOTelAttributes(t, point.Attributes.ToSlice(), allowed, canary)
				}
			case metricdata.Histogram[int64]:
				for _, point := range data.DataPoints {
					assertOTelAttributes(t, point.Attributes.ToSlice(), allowed, canary)
				}
			default:
				t.Fatalf("unexpected metric data type %T", metric.Data)
			}
		}
	}
	if len(gotMetrics) != len(wantMetrics) {
		t.Fatalf("metric inventory=%v", gotMetrics)
	}
}

func telemetryAttributeNames() map[string]bool {
	return map[string]bool{
		"golem.kind": true, "golem.phase": true, "golem.outcome": true, "golem.reason": true,
		"golem.provider": true, "golem.operation": true, "golem.model_id": true,
		"golem.duration_ns": true, "golem.statement_count": true, "golem.attempt": true,
		"golem.queue_depth": true, "golem.queue_limit": true, "golem.aggregate_count": true,
		"golem.queue.type": true,
	}
}

func assertOTelAttributes(t *testing.T, attributes []attribute.KeyValue, allowed map[string]bool, canary string) {
	t.Helper()
	for _, value := range attributes {
		if !allowed[string(value.Key)] {
			t.Fatalf("unexpected OTel attribute=%q", value.Key)
		}
		assertClosedPublic(t, "OTel attribute", []byte(value.Value.Emit()), canary)
	}
}

func assertProviderOpenErrorClosed(t *testing.T, canary string) {
	t.Helper()
	var err error
	switch os.Getenv("P8_ORACLE_PROVIDER") {
	case "sqlite":
		_, err = sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:" + canary + ".sqlite?_pragma=foreign_keys(OFF)"})
	case "postgresql":
		_, err = postgresql.Open(context.Background(), postgresql.Config{DataSourceName: "postgresql://" + canary + ":" + canary + "@127.0.0.1:1/" + canary + "?connect_timeout=1"})
	default:
		t.Fatalf("unsupported provider %q", os.Getenv("P8_ORACLE_PROVIDER"))
	}
	if err == nil {
		t.Fatal("invalid provider source unexpectedly opened")
	}
	if _, ok := provider.CodeOf(err); !ok {
		t.Fatalf("provider failure lacks closed code: %T %v", err, err)
	}
	assertClosedPublic(t, "provider lifecycle error", []byte(err.Error()), canary)
}

func openDatabase(t *testing.T) *provider.Database {
	t.Helper()
	var database *provider.Database
	var err error
	switch os.Getenv("P8_ORACLE_PROVIDER") {
	case "sqlite":
		database, err = sqlite.Open(context.Background(), sqlite.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	case "postgresql":
		database, err = postgresql.Open(context.Background(), postgresql.Config{DataSourceName: os.Getenv("P8_ORACLE_DSN")})
	default:
		t.Fatalf("unsupported provider %q", os.Getenv("P8_ORACLE_PROVIDER"))
	}
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func openApplication(t *testing.T, database *provider.Database, observer observe.Observer, operatorReports ...func(context.Context, events.OperatorAuditRecord)) (*social.App[social.Principal], *social.Caller[social.Principal], *social.GraphQLServer, func() []string) {
	t.Helper()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	userID := mustUUID(t, diagnosticUserID)
	app, err := social.Open(context.Background(), social.Config[social.Principal]{
		Database: database, EventTransport: transport, Observer: observer,
		ResolvePrincipal: func(_ context.Context, principal social.Principal) (social.Actor, error) {
			return social.Actor{UserID: principal.DevUserID, Authenticated: principal.Development}, nil
		},
		SnapshotPrincipal: func(value social.Principal) (social.Principal, error) { return value, nil },
		SnapshotActor:     func(value social.Actor) (social.Actor, error) { return value, nil },
		AuditPrincipal: func(principal social.Principal) string {
			identity := principal.DevUserID.Bytes()
			digest := sha256.Sum256(identity[:])
			return hex.EncodeToString(digest[:8])
		},
		ReportScopedQuery: func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(ctx context.Context, record events.OperatorAuditRecord) {
			for _, report := range operatorReports {
				if report != nil {
					report(ctx, record)
				}
			}
		},
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(context.Background(), social.Principal{Development: true, DevUserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	trusted := []string{}
	graph, err := app.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: func(context.Context) (social.Principal, bool) {
			return social.Principal{Development: true, DevUserID: userID}, true
		},
		ReportInternalError: func(_ context.Context, err error) {
			mu.Lock()
			trusted = append(trusted, err.Error())
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, caller, graph, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), trusted...)
	}
}

type graphResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func graphRequest(t *testing.T, handler http.Handler, query string) graphResponse {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"query": query})
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var decoded graphResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode GraphQL response=%q: %v", response.Body.String(), err)
	}
	return decoded
}

func assertClosedPublic(t *testing.T, channel string, value []byte, canary string) {
	t.Helper()
	lower := strings.ToLower(string(value))
	for _, forbidden := range []string{canary, "select ", " from ", "no such table", "does not exist", "connection refused", "connect_timeout", "127.0.0.1"} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Fatalf("%s disclosed %q: %s", channel, forbidden, value)
		}
	}
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func randomCanary(t *testing.T, family string) string {
	t.Helper()
	var value [16]byte
	if _, err := cryptorand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	return "P8_" + family + "_" + hex.EncodeToString(value[:])
}

func mustUUID(t *testing.T, value string) golem.UUID {
	t.Helper()
	result, err := golem.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
