package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/graphql"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
	"github.com/eleven-am/golem/go/runtime/testdata/v073facts"
)

const (
	schemaUnavailableRemedy = "which no active or historical bundle provides: deliver or drain events written before the schema changed, or supply the bundle that wrote them"
	regenerateContractV6    = "generated contract format 6 predates this golem (format 7): regenerate with golem generate"
)

func sqliteP10Fixture(t *testing.T) *p10OperationFixture {
	t.Helper()
	p10operations.Reset(nil)
	t.Cleanup(func() { p10operations.Reset(nil) })
	return newP10OperationFixture(t, p5ExtensionProviderProfiles()[0])
}

func insertOutboxRow(t *testing.T, fixture *p10OperationFixture, provider policyir.Provider, namespace string, row mutationfact.OutboxRow, withDelivery bool) {
	t.Helper()
	statements, err := mutationfact.RenderInsertsAt(provider, namespace, []mutationfact.OutboxRow{row}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if withDelivery {
		delivery, err := mutationfact.RenderDeliveryInsertAt(provider, namespace, []mutationfact.OutboxRow{row})
		if err != nil {
			t.Fatal(err)
		}
		statements = append(statements, delivery)
	}
	for _, statement := range statements {
		if _, err := fixture.database.ExecContext(context.Background(), statement.SQL(), statement.Args()...); err != nil {
			t.Fatal(err)
		}
	}
}

func p10CausationID(t *testing.T, text string) golem.CausationID {
	t.Helper()
	parsed, err := golem.ParseUUID(text)
	if err != nil {
		t.Fatal(err)
	}
	return golem.CausationID(parsed)
}

func runPublisherUntilBlocked(t *testing.T, fixture *p10OperationFixture, causation golem.CausationID) events.Delivery {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fixture.app.RunEventPublisher(ctx) }()
	var delivery events.Delivery
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		inspected, err := fixture.app.EventOperator().Inspect(context.Background(), causation)
		if err == nil && inspected.Status() == events.DeliveryBlocked {
			delivery = inspected
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if delivery == nil {
		t.Fatal("the delivery was never blocked")
	}
	return delivery
}

func activeTeamEventSchema(t *testing.T) (*schema.Registry, golem.SchemaDigest) {
	t.Helper()
	registry, err := schema.New(p10operations.GolemGeneratedSchemaBundle())
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range registry.EventModels() {
		if model.LogicalName() != "Team" {
			continue
		}
		fingerprint, _, _ := model.EventSchema()
		digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		return registry, digest
	}
	t.Fatal("Team has no event schema")
	return nil, golem.SchemaDigest{}
}

func requireUnavailableMessage(t *testing.T, err error, message string) {
	t.Helper()
	var failure *mutationfact.SchemaUnavailableError
	if !errors.As(err, &failure) {
		t.Fatalf("error is not *SchemaUnavailableError: %v", err)
	}
	if failure.Error() != message {
		t.Fatalf("message = %q, want %q", failure.Error(), message)
	}
}

func TestUndeliveredFactWithChangedEventSchemaFailsTyped(t *testing.T) {
	registry, active := activeTeamEventSchema(t)
	row := v073facts.ChangedEventSchemaV2()
	_, err := mutationfact.DecodeOutboxV2(row.Metadata, row.DeleteSnapshot, registry, active)
	requireUnavailableMessage(t, err, fmt.Sprintf("GOLEM_EVENT_SCHEMA_UNAVAILABLE: stored event fact (format V2) needs event schema %s, %s", v073facts.ChangedEventSchemaDigest, schemaUnavailableRemedy))

	fixture := sqliteP10Fixture(t)
	insertOutboxRow(t, fixture, policyir.ProviderSQLite, "main", row, true)
	delivery := runPublisherUntilBlocked(t, fixture, p10CausationID(t, row.CausationID))
	if delivery.FailureCode() != "schema-unavailable" {
		t.Fatalf("failure code = %q, want schema-unavailable", delivery.FailureCode())
	}
}

func TestUndeliveredV1FactFailsTyped(t *testing.T) {
	registry, _ := activeTeamEventSchema(t)
	row := v073facts.GenerationBoundV1()
	_, err := mutationfact.DecodeOutbox(row.Metadata, row.DeleteSnapshot, registry)
	requireUnavailableMessage(t, err, fmt.Sprintf("GOLEM_EVENT_SCHEMA_UNAVAILABLE: stored event fact (format V1) needs generation digest %s, %s", v073facts.GenerationDigest, schemaUnavailableRemedy))

	fixture := sqliteP10Fixture(t)
	insertOutboxRow(t, fixture, policyir.ProviderSQLite, "main", row, true)
	delivery := runPublisherUntilBlocked(t, fixture, p10CausationID(t, row.CausationID))
	if delivery.FailureCode() != "schema-unavailable" {
		t.Fatalf("failure code = %q, want schema-unavailable", delivery.FailureCode())
	}
}

func TestUndeliveredV2FactWithUnchangedEventSchemaDecodes(t *testing.T) {
	registry, active := activeTeamEventSchema(t)
	if v073facts.UnchangedEventSchemaDigest != active.String() {
		t.Fatalf("the frozen v0.7.3 Team event schema %s differs from the active %s", v073facts.UnchangedEventSchemaDigest, active)
	}
	row := v073facts.UnchangedEventSchemaV2()
	if _, err := mutationfact.ValidateStoredRow(row, activeResolver{registry: registry, digest: active}); err != nil {
		t.Fatalf("a v0.7.3 V2 fact whose event schema is unchanged no longer decodes: %v", err)
	}

	fixture := sqliteP10Fixture(t)
	insertOutboxRow(t, fixture, policyir.ProviderSQLite, "main", row, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fixture.app.RunEventPublisher(ctx) }()
	causation := p10CausationID(t, row.CausationID)
	var status events.DeliveryStatus
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		inspected, err := fixture.app.EventOperator().Inspect(context.Background(), causation)
		if err == nil {
			status = inspected.Status()
			if status == events.DeliveryDelivered || status == events.DeliveryBlocked {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if status != events.DeliveryDelivered {
		t.Fatalf("the v0.7.3 fact with an unchanged event schema ended %q, want delivered", status)
	}
}

type activeResolver struct {
	registry *schema.Registry
	digest   golem.SchemaDigest
}

func (resolver activeResolver) ResolveFactSchema(reference mutationfact.SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	return resolver.registry, resolver.digest, reference.FormatVersion == mutationfact.FormatVersionV2 && reference.EventSchema == resolver.digest
}

func openWithBundles(t *testing.T, bundle golem.SchemaBundle, historical []golem.SchemaBundle) error {
	t.Helper()
	profile := p5ExtensionProviderProfiles()[0]
	database := openP10OperationDatabase(t, profile, &p10CommitGate{})
	_, err := golemruntime.Open(context.Background(), golemruntime.Config[p10operations.Principal, p10operations.Actor]{
		Database:               p8AdoptTracedProviderHandle(database, profile),
		Bundle:                 bundle,
		HistoricalEventBundles: historical,
		ResolvePrincipal: func(context.Context, p10operations.Principal) (p10operations.Actor, error) {
			return p10operations.Actor{}, nil
		},
	})
	return err
}

func TestHistoricalEventBundleV6FailsStartup(t *testing.T) {
	err := openWithBundles(t, p10operations.GolemGeneratedSchemaBundle(), []golem.SchemaBundle{v073facts.SchemaBundle()})
	const want = "GOLEM_EVENT_CONFIG: historical event bundle 0: P2_SCHEMA_DOCUMENT: contract.formatVersion: historical contract format 6 is not supported by this golem: deliver or drain undelivered events before upgrading, then remove this bundle from HistoricalEventBundles"
	if err == nil || err.Error() != want {
		t.Fatalf("startup error = %v\nwant %s", err, want)
	}
}

func TestGeneratedExecutorAndRuntimeRejectOldBundleIdentically(t *testing.T) {
	old := v073facts.SchemaBundle()
	runtimeErr := openWithBundles(t, old, nil)
	_, executorErr := graphql.NewGeneratedExecutor(graphql.GeneratedExecutorConfig[p10operations.Principal]{
		Bundle:              old,
		BeginCaller:         func(context.Context, p10operations.Principal) (graphql.CallerExecution, error) { return nil, nil },
		ReportInternalError: func(context.Context, error) {},
	})
	const detail = "P2_SCHEMA_DOCUMENT: contract.formatVersion: " + regenerateContractV6
	if runtimeErr == nil || runtimeErr.Error() != "P3_RUNTIME_SCHEMA: "+detail {
		t.Fatalf("runtime error = %v", runtimeErr)
	}
	if executorErr == nil || executorErr.Error() != detail {
		t.Fatalf("executor error = %v", executorErr)
	}
}

type runbookState struct {
	name      string
	undone    bool
	transform func(table, time, causation string) string
}

func runbookStates() []runbookState {
	return []runbookState{
		{name: "no delivery row", undone: true},
		{name: "pending", undone: true, transform: func(string, string, string) string { return "" }},
		{name: "leased", undone: true, transform: func(table, at, causation string) string {
			return fmt.Sprintf(`UPDATE %s SET "status"='leased',"lease_token"='11111111-1111-4111-8111-111111111111',"lease_until"=%s WHERE "causation_id"='%s'`, table, at, causation)
		}},
		{name: "blocked", undone: true, transform: func(table, at, causation string) string {
			return fmt.Sprintf(`UPDATE %s SET "status"='blocked',"blocked_at"=%s,"last_failure_code"='schema-unavailable' WHERE "causation_id"='%s'`, table, at, causation)
		}},
		{name: "delivered", transform: func(table, at, causation string) string {
			return fmt.Sprintf(`UPDATE %s SET "status"='delivered',"delivered_at"=%s WHERE "causation_id"='%s'`, table, at, causation)
		}},
		{name: "retired", transform: func(table, at, causation string) string {
			return fmt.Sprintf(`UPDATE %s SET "status"='retired',"retired_at"=%s WHERE "causation_id"='%s'`, table, at, causation)
		}},
	}
}

func publishedDrainQuery(t *testing.T) string {
	t.Helper()
	notes, err := os.ReadFile(filepath.Join("..", "..", "docs", "golem-go", "RELEASE-NOTES.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(notes)
	if strings.Count(text, "SELECT COUNT(DISTINCT") != 1 {
		t.Fatalf("the release notes hold %d drain queries; want 1", strings.Count(text, "SELECT COUNT(DISTINCT"))
	}
	start := strings.Index(text, "SELECT COUNT(DISTINCT")
	end := strings.Index(text[start:], ";")
	if end < 0 {
		t.Fatal("the release-note drain query has no terminating semicolon")
	}
	return text[start : start+end]
}

func TestDrainRunbookQueryCountsUndelivered(t *testing.T) {
	published := publishedDrainQuery(t)
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		provider, namespace, instant := policyir.ProviderSQLite, "main", "1700000000000000"
		query := published
		qualifiedDelivery := `"main"."_golem_outbox_delivery"`
		if fixture.database.DriverName() != "sqlite" {
			provider, namespace, instant = policyir.ProviderPostgreSQL, string(p10OperationSystemNamespace), "to_timestamp(1700000000)"
			query = strings.ReplaceAll(query, `"_golem_outbox_delivery"`, `"`+namespace+`"."_golem_outbox_delivery"`)
			query = strings.ReplaceAll(query, `"_golem_outbox"`, `"`+namespace+`"."_golem_outbox"`)
			qualifiedDelivery = `"` + namespace + `"."_golem_outbox_delivery"`
		}
		count := func() int {
			var undelivered int
			if err := fixture.database.GetContext(ctx, &undelivered, query); err != nil {
				t.Fatalf("the published drain query failed: %v\n%s", err, query)
			}
			return undelivered
		}
		baseline := count()
		for index, state := range runbookStates() {
			row := v073facts.UnchangedEventSchemaV2()
			row.EventID = fmt.Sprintf("00000000-0000-4000-8000-%012d", 100+index)
			row.CausationID = fmt.Sprintf("00000000-0000-4000-8000-%012d", 200+index)
			insertOutboxRow(t, fixture, provider, namespace, row, state.transform != nil)
			if state.transform != nil {
				if statement := state.transform(qualifiedDelivery, instant, row.CausationID); statement != "" {
					if _, err := fixture.database.ExecContext(ctx, statement); err != nil {
						t.Fatalf("%s: %v", state.name, err)
					}
				}
			}
			want := baseline
			if state.undone {
				want++
			}
			if got := count(); got != want {
				t.Fatalf("after a %s causation the drain query counts %d, want %d", state.name, got, want)
			}
			baseline = want
		}
	})
}
