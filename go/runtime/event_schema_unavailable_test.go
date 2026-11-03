package runtime_test

import (
	"context"
	"crypto/sha256"
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
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
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

func insertForProfile(t *testing.T, fixture *p10OperationFixture, row mutationfact.OutboxRow) {
	t.Helper()
	provider, namespace := policyir.ProviderSQLite, "main"
	if fixture.database.DriverName() != "sqlite" {
		provider, namespace = policyir.ProviderPostgreSQL, string(p10OperationSystemNamespace)
	}
	insertOutboxRow(t, fixture, provider, namespace, row, true)
}

func requireRetiredFactIsRefusedAndRetirable(t *testing.T, row mutationfact.OutboxRow, version uint16) {
	t.Helper()
	registry, active := activeTeamEventSchema(t)
	want := fmt.Sprintf("GOLEM_EVENT_FORMAT_RETIRED: stored event fact (format V%d) is no longer readable: drain undelivered events on go/v0.8.0 before upgrading", version)
	_, err := mutationfact.DecodeOutbox(row.Metadata, row.DeleteSnapshot, registry, active)
	var retired *mutationfact.FormatRetiredError
	if !errors.As(err, &retired) || retired.Version() != version || err.Error() != want {
		t.Fatalf("decode error = %v, want retired V%d %q", err, version, want)
	}
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		insertForProfile(t, fixture, row)
		causation := p10CausationID(t, row.CausationID)
		delivery := runPublisherUntilBlocked(t, fixture, causation)
		if delivery.FailureCode() != "format-retired" {
			t.Fatalf("failure code = %q, want format-retired", delivery.FailureCode())
		}
		if err := fixture.app.EventOperator().Retire(context.Background(), causation); err != nil {
			t.Fatalf("a blocked retired-format delivery could not be retired: %v", err)
		}
		inspected, err := fixture.app.EventOperator().Inspect(context.Background(), causation)
		if err != nil || inspected.Status() != events.DeliveryRetired {
			t.Fatalf("after Retire the delivery is %v (%v), want retired", inspected, err)
		}
	})
}

func TestUndeliveredFactWithChangedEventSchemaFailsTyped(t *testing.T) {
	registry, _ := activeTeamEventSchema(t)
	var team schema.Model
	for _, model := range registry.EventModels() {
		if model.LogicalName() == "Team" {
			team = model
		}
	}
	changed := golem.SchemaDigest{0x99, 0x3c}
	key := team.PrimaryKey()
	if len(key) != 1 {
		t.Fatalf("Team primary key has %d fields, want 1", len(key))
	}
	image, err := mutationdecode.NewRow(registry, policyir.ModelID(team.ID()), []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(key[0]), policyir.UUIDValue([16]byte{15: 1}))})
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(key[0])}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err = requirement.WithEventSchema([32]byte(changed))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := mutationfact.New(registry, changed, mutationfact.EventID{15: 7}, requirement, mutationfact.CausationID{15: 8}, 1, 5, nil, &image)
	if err != nil {
		t.Fatal(err)
	}
	row, err := envelope.OutboxRow(time.Unix(1_700_000_000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, decodeErr := mutationfact.DecodeOutbox(row.Metadata, row.DeleteSnapshot, registry, golem.SchemaDigest{1})
	requireUnavailableMessage(t, decodeErr, fmt.Sprintf("GOLEM_EVENT_SCHEMA_UNAVAILABLE: stored event fact (format V3) needs event schema %s, %s", changed, schemaUnavailableRemedy))

	fixture := sqliteP10Fixture(t)
	insertOutboxRow(t, fixture, policyir.ProviderSQLite, "main", row, true)
	delivery := runPublisherUntilBlocked(t, fixture, p10CausationID(t, row.CausationID))
	if delivery.FailureCode() != "schema-unavailable" {
		t.Fatalf("failure code = %q, want schema-unavailable", delivery.FailureCode())
	}
}

func TestUndeliveredV1FactRefusedTyped(t *testing.T) {
	requireRetiredFactIsRefusedAndRetirable(t, v073facts.GenerationBoundV1(), 1)
}

func TestUndeliveredV2FactRefusedTyped(t *testing.T) {
	requireRetiredFactIsRefusedAndRetirable(t, v073facts.UnchangedEventSchemaV2(), 2)
}

func TestUndeliveredV2FactWithChangedEventSchemaIsRefusedAsRetired(t *testing.T) {
	requireRetiredFactIsRefusedAndRetirable(t, v073facts.ChangedEventSchemaV2(), 2)
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

func TestHistoricalEventBundleV4AndV5FailStartupWithTheDrainInstruction(t *testing.T) {
	old := v073facts.SchemaBundle()
	for _, version := range []uint32{4, 5} {
		payload := []byte(fmt.Sprintf(`{"formatVersion":%d,"graphqlAbiVersion":3,"models":[],"enums":[],"methods":[],"customOperations":[]}`, version))
		hash := sha256.New()
		_, _ = hash.Write([]byte("golem:contract-fingerprint:v1"))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(payload)
		var digest golem.SchemaDigest
		copy(digest[:], hash.Sum(nil))
		bundle := golem.GeneratedSchemaBundle(old.GenerationDigest(), old.GeneratorVersion(), old.TemplateABIVersion(), old.Model(), golem.GeneratedSchemaDocument(version, 1, digest, payload), old.Providers()...)
		err := openWithBundles(t, p10operations.GolemGeneratedSchemaBundle(), []golem.SchemaBundle{bundle})
		want := fmt.Sprintf("GOLEM_EVENT_CONFIG: historical event bundle 0: P2_SCHEMA_DOCUMENT: contract.formatVersion: historical contract format %d is not supported by this golem: deliver or drain undelivered events before upgrading, then remove this bundle from HistoricalEventBundles", version)
		if err == nil || err.Error() != want {
			t.Fatalf("v%d startup error = %v\nwant %s", version, err, want)
		}
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
