package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/runtime/testdata/hookvalues"
	"github.com/jmoiron/sqlx"
)

type hookValuesHarness struct {
	app    *hookvalues.App[hookvalues.Principal]
	server *hookvalues.GraphQLServer
	caller *hookvalues.Caller[hookvalues.Principal]
}

func forEachHookValuesProvider(t *testing.T, body func(t *testing.T, harness *hookValuesHarness)) {
	t.Helper()
	for _, profile := range p5ExtensionProviderProfiles()[:2] {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			body(t, newHookValuesHarness(t, profile))
		})
	}
}

func newHookValuesHarness(t *testing.T, profile p5ExtensionProviderProfile) *hookValuesHarness {
	t.Helper()
	ctx := context.Background()
	var database *sqlx.DB
	var apply func(context.Context, *sqlx.DB, physical.PhysicalSchema) error
	var err error
	if profile.provider == golem.SQLite {
		provider := sqliteprovider.New()
		database, _, err = provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "hook-values.sqlite"))
		apply = provider.ApplyInitial
	} else {
		p5ExtensionPostgreSQLFixtureLock.Lock()
		t.Cleanup(p5ExtensionPostgreSQLFixtureLock.Unlock)
		provider := postgresprovider.New()
		database, _, err = provider.Open(ctx, testenv.DisposablePostgreSQL(t, profile.env))
		apply = provider.ApplyInitial
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var encoded []byte
	for _, document := range hookvalues.GolemGeneratedSchemaBundle().Providers() {
		if document.Provider() == profile.provider {
			encoded = document.Schema().Bytes()
		}
	}
	schema, err := physical.CanonicalDecode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	app, err := hookvalues.Open(ctx, hookvalues.Config[hookvalues.Principal]{
		Database: p8AdoptTracedProviderHandle(database, profile),
		ResolvePrincipal: func(context.Context, hookvalues.Principal) (hookvalues.Actor, error) {
			return hookvalues.Actor{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := app.GraphQL(hookvalues.GraphQLConfig[hookvalues.Principal]{
		PrincipalFromContext: func(context.Context) (hookvalues.Principal, bool) { return hookvalues.Principal{}, true },
		ReportInternalError:  func(_ context.Context, err error) { t.Errorf("reported GraphQL error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(ctx, hookvalues.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	return &hookValuesHarness{app: app, server: server, caller: caller}
}

func (harness *hookValuesHarness) graphql(t *testing.T, query string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(recorder, request)
	var response struct {
		Errors []struct {
			Message    string         `json:"message"`
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GraphQL status=%d body=%q: %v", recorder.Code, recorder.Body.String(), err)
	}
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL errors=%+v query=%s", response.Errors, query)
	}
}

func hookValueUUIDText(id byte) string {
	return golem.UUID{15: id}.String()
}

func hookValueFieldNames(t *testing.T) map[golem.FieldID]string {
	t.Helper()
	fields := map[string]golem.Field[hookvalues.Entry]{
		"id": hookvalues.Entries.ID, "ownerId": hookvalues.Entries.OwnerID, "text": hookvalues.Entries.Text,
		"flag": hookvalues.Entries.Flag, "small": hookvalues.Entries.Small, "integer": hookvalues.Entries.Integer,
		"big": hookvalues.Entries.Big, "single": hookvalues.Entries.Single, "double": hookvalues.Entries.Double,
		"amount": hookvalues.Entries.Amount, "ref": hookvalues.Entries.Ref, "day": hookvalues.Entries.Day,
		"clock": hookvalues.Entries.Clock, "instant": hookvalues.Entries.Instant, "payload": hookvalues.Entries.Payload,
		"state": hookvalues.Entries.State, "tags": hookvalues.Entries.Tags, "states": hookvalues.Entries.States,
		"nick": hookvalues.Entries.Nick, "rank": hookvalues.Entries.Rank,
	}
	names := make(map[golem.FieldID]string, len(fields))
	for name, field := range fields {
		identity, ok := golem.FieldIdentity(field)
		if !ok {
			t.Fatalf("field %s has no identity", name)
		}
		names[identity] = name
	}
	return names
}

type hookValueSeen struct {
	Operation golem.MutationFieldOperation
	Type      string
	Value     any
	HasValue  bool
}

func observedHookValues(t *testing.T, hook string) map[string]hookValueSeen {
	t.Helper()
	names := hookValueFieldNames(t)
	seen := map[string]hookValueSeen{}
	for _, observation := range hookvalues.Observations() {
		if observation.Hook != hook {
			continue
		}
		name, known := names[observation.Field]
		if !known {
			t.Fatalf("%s hook saw an unnamed field %x", hook, observation.Field)
		}
		if _, duplicate := seen[name]; duplicate {
			t.Fatalf("%s hook saw %s twice", hook, name)
		}
		seen[name] = hookValueSeen{Operation: observation.Operation, Type: fmt.Sprintf("%T", observation.Value), Value: observation.Value, HasValue: observation.HasValue}
	}
	return seen
}

func hookValueKeys(values map[string]hookValueSeen) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var hookValuePublicTypes = map[string]string{
	"id": "golem.UUID", "ownerId": "golem.UUID", "text": "string", "flag": "bool", "small": "int16", "integer": "int32",
	"big": "int64", "single": "float32", "double": "float64", "amount": "golem.Decimal", "ref": "golem.UUID",
	"day": "golem.Date", "clock": "golem.Time", "instant": "time.Time", "payload": "[]uint8", "state": "string",
	"tags": "[]string", "states": "[]string", "nick": "string", "rank": "int64",
}

var hookValueIdentityFields = map[string]bool{"id": true, "ownerId": true}

func assertHookValuesAgree(t *testing.T, label string, typed, graphql map[string]hookValueSeen) {
	t.Helper()
	if !reflect.DeepEqual(hookValueKeys(typed), hookValueKeys(graphql)) {
		t.Fatalf("%s: typed hook saw fields %v, GraphQL hook saw %v", label, hookValueKeys(typed), hookValueKeys(graphql))
	}
	for _, name := range hookValueKeys(typed) {
		fromTyped, fromGraphQL := typed[name], graphql[name]
		if fromTyped.Operation != fromGraphQL.Operation || fromTyped.HasValue != fromGraphQL.HasValue {
			t.Fatalf("%s: %s operation typed=%d/%v GraphQL=%d/%v", label, name, fromTyped.Operation, fromTyped.HasValue, fromGraphQL.Operation, fromGraphQL.HasValue)
		}
		if !fromTyped.HasValue {
			continue
		}
		want := hookValuePublicTypes[name]
		if fromTyped.Type != want {
			t.Errorf("%s: %s hook value type on the typed path = %s, want %s", label, name, fromTyped.Type, want)
		}
		if fromGraphQL.Type != want {
			t.Errorf("%s: %s hook value type on the GraphQL path = %s, want %s", label, name, fromGraphQL.Type, want)
		}
		if fromTyped.Type != want || fromGraphQL.Type != want {
			continue
		}
		if !hookValueIdentityFields[name] && !reflect.DeepEqual(fromTyped.Value, fromGraphQL.Value) {
			t.Errorf("%s: %s hook value typed=%#v GraphQL=%#v", label, name, fromTyped.Value, fromGraphQL.Value)
		}
	}
}

func hookValueDecimal(t *testing.T, text string) golem.Decimal {
	t.Helper()
	decimal, err := golem.ParseDecimal(text)
	if err != nil {
		t.Fatal(err)
	}
	return decimal
}

func hookValueDay(t *testing.T) golem.Date {
	t.Helper()
	day, err := golem.NewDate(2026, time.November, 3)
	if err != nil {
		t.Fatal(err)
	}
	return day
}

func hookValueClock(t *testing.T) golem.Time {
	t.Helper()
	clock, err := golem.NewTime(12, 34, 56, 789012)
	if err != nil {
		t.Fatal(err)
	}
	return clock
}

func typedEntryValues(t *testing.T, id byte, nick golem.CreateValue[hookvalues.Entry], rank golem.CreateValue[hookvalues.Entry]) []golem.CreateValue[hookvalues.Entry] {
	t.Helper()
	return []golem.CreateValue[hookvalues.Entry]{
		hookvalues.Entries.ID.Create(golem.UUID{15: id}),
		hookvalues.Entries.Text.Create("alpha text"),
		hookvalues.Entries.Flag.Create(true),
		hookvalues.Entries.Small.Create(7),
		hookvalues.Entries.Integer.Create(70000),
		hookvalues.Entries.Big.Create(5_000_000_000),
		hookvalues.Entries.Single.Create(1.5),
		hookvalues.Entries.Double.Create(2.25),
		hookvalues.Entries.Amount.Create(hookValueDecimal(t, "12.34")),
		hookvalues.Entries.Ref.Create(golem.UUID{15: 99}),
		hookvalues.Entries.Day.Create(hookValueDay(t)),
		hookvalues.Entries.Clock.Create(hookValueClock(t)),
		hookvalues.Entries.Instant.Create(time.Date(2026, time.November, 3, 12, 34, 56, 123456000, time.UTC)),
		hookvalues.Entries.Payload.Create([]byte{1, 2, 3}),
		hookvalues.Entries.State.Create(hookvalues.KindAlpha),
		hookvalues.Entries.Tags.Create(golem.List[string]{"x", "y"}),
		hookvalues.Entries.States.Create(golem.List[hookvalues.Kind]{hookvalues.KindAlpha, hookvalues.KindOmega}),
		nick,
		rank,
	}
}

func graphqlEntryData(id byte, nick, rank string) string {
	return fmt.Sprintf(`{id: %q, text: "alpha text", flag: true, small: 7, integer: 70000, big: "5000000000", single: 1.5, double: 2.25,
		amount: "12.34", ref: %q, day: "2026-11-03", clock: "12:34:56.789012", instant: "2026-11-03T12:34:56.123456Z", payload: "AQID",
		state: KindAlpha, tags: ["x", "y"], states: [KindAlpha, KindOmega], nick: %s, rank: %s}`, hookValueUUIDText(id), hookValueUUIDText(99), nick, rank)
}

func TestMutationHookValueTypesMatchAcrossTypedAndGraphQLCreate(t *testing.T) {
	forEachHookValuesProvider(t, func(t *testing.T, harness *hookValuesHarness) {
		ctx := context.Background()
		hookvalues.ResetObservations()
		_, err := harness.caller.Entries.Create(ctx, hookvalues.Entries.Create(typedEntryValues(t, 1, hookvalues.Entries.Nick.Create("n"), hookvalues.Entries.Rank.Create(9))...))
		if err != nil {
			t.Fatal(err)
		}
		typed := observedHookValues(t, "entry.create")

		hookvalues.ResetObservations()
		harness.graphql(t, fmt.Sprintf(`mutation { createEntry(data: %s) { id } }`, graphqlEntryData(2, `"n"`, `"9"`)))
		assertHookValuesAgree(t, "top-level create", typed, observedHookValues(t, "entry.create"))

		hookvalues.ResetObservations()
		_, err = harness.caller.Entries.Create(ctx, hookvalues.Entries.Create(typedEntryValues(t, 3, hookvalues.Entries.Nick.CreateNull(), hookvalues.Entries.Rank.CreateNull())...))
		if err != nil {
			t.Fatal(err)
		}
		typedNull := observedHookValues(t, "entry.create")
		hookvalues.ResetObservations()
		harness.graphql(t, fmt.Sprintf(`mutation { createEntry(data: %s) { id } }`, graphqlEntryData(4, `null`, `null`)))
		graphqlNull := observedHookValues(t, "entry.create")
		assertHookValuesAgree(t, "explicit-null create", typedNull, graphqlNull)
		for _, name := range []string{"nick", "rank"} {
			if graphqlNull[name].Operation != golem.MutationFieldNull || graphqlNull[name].HasValue {
				t.Fatalf("GraphQL explicit null %s = %+v", name, graphqlNull[name])
			}
		}
	})
}

func TestMutationHookValueTypesMatchAcrossTypedAndGraphQLNestedCreate(t *testing.T) {
	forEachHookValuesProvider(t, func(t *testing.T, harness *hookValuesHarness) {
		ctx := context.Background()
		hookvalues.ResetObservations()
		child := hookvalues.Entries.Create(typedEntryValues(t, 11, hookvalues.Entries.Nick.Create("n"), hookvalues.Entries.Rank.Create(9))...)
		_, err := harness.caller.Owners.Create(ctx, hookvalues.Owners.Create(
			hookvalues.Owners.ID.Create(golem.UUID{15: 1}), hookvalues.Owners.Name.Create("owner"), hookvalues.Owners.Entries.Create(child),
		))
		if err != nil {
			t.Fatal(err)
		}
		typed := observedHookValues(t, "entry.create")

		hookvalues.ResetObservations()
		data := graphqlEntryData(12, `"n"`, `"9"`)
		harness.graphql(t, fmt.Sprintf(`mutation { createOwner(data: {id: %q, name: "owner", entries: {create: [%s]}}) { id } }`, hookValueUUIDText(2), data))
		assertHookValuesAgree(t, "nested create", typed, observedHookValues(t, "entry.create"))
	})
}

func TestMutationHookValueTypesMatchAcrossTypedAndGraphQLUpdate(t *testing.T) {
	forEachHookValuesProvider(t, func(t *testing.T, harness *hookValuesHarness) {
		ctx := context.Background()
		for _, id := range []byte{1, 2} {
			if _, err := harness.caller.Entries.Create(ctx, hookvalues.Entries.Create(typedEntryValues(t, id, hookvalues.Entries.Nick.Create("before"), hookvalues.Entries.Rank.Create(1))...)); err != nil {
				t.Fatal(err)
			}
		}

		hookvalues.ResetObservations()
		_, err := harness.caller.Entries.Update(ctx, hookvalues.Entries.ByID.Value(golem.UUID{15: 1}), hookvalues.Entries.Update(
			hookvalues.Entries.Text.Set("omega text"),
			hookvalues.Entries.Flag.Set(false),
			hookvalues.Entries.Small.Set(8),
			hookvalues.Entries.Integer.Set(80000),
			hookvalues.Entries.Big.Set(6_000_000_000),
			hookvalues.Entries.Single.Set(2.5),
			hookvalues.Entries.Double.Set(3.25),
			hookvalues.Entries.Amount.Set(hookValueDecimal(t, "56.78")),
			hookvalues.Entries.Ref.Set(golem.UUID{15: 98}),
			hookvalues.Entries.Day.Set(hookValueDay(t)),
			hookvalues.Entries.Clock.Set(hookValueClock(t)),
			hookvalues.Entries.Instant.Set(time.Date(2026, time.November, 4, 1, 2, 3, 456000000, time.UTC)),
			hookvalues.Entries.Payload.Set([]byte{4, 5}),
			hookvalues.Entries.State.Set(hookvalues.KindOmega),
			hookvalues.Entries.Tags.Set(golem.List[string]{"z"}),
			hookvalues.Entries.States.Set(golem.List[hookvalues.Kind]{hookvalues.KindOmega}),
			hookvalues.Entries.Nick.Set("after"),
			hookvalues.Entries.Rank.Set(5),
		))
		if err != nil {
			t.Fatal(err)
		}
		typed := observedHookValues(t, "entry.update")

		hookvalues.ResetObservations()
		harness.graphql(t, fmt.Sprintf(`mutation { updateEntry(where: {ID: %q}, data: {
			text: {set: "omega text"}, flag: {set: false}, small: {set: 8}, integer: {set: 80000}, big: {set: "6000000000"},
			single: {set: 2.5}, double: {set: 3.25}, amount: {set: "56.78"}, ref: {set: %q}, day: {set: "2026-11-03"},
			clock: {set: "12:34:56.789012"}, instant: {set: "2026-11-04T01:02:03.456Z"}, payload: {set: "BAU="},
			state: {set: KindOmega}, tags: {set: ["z"]}, states: {set: [KindOmega]}, nick: {set: "after"}, rank: {set: "5"}
		}) { id } }`, hookValueUUIDText(2), hookValueUUIDText(98)))
		assertHookValuesAgree(t, "update set", typed, observedHookValues(t, "entry.update"))

		hookvalues.ResetObservations()
		if _, err := harness.caller.Entries.Update(ctx, hookvalues.Entries.ByID.Value(golem.UUID{15: 1}), hookvalues.Entries.Update(
			hookvalues.Entries.Nick.Null(), hookvalues.Entries.Rank.Increment(3), hookvalues.Entries.Big.Decrement(2),
		)); err != nil {
			t.Fatal(err)
		}
		typedOps := observedHookValues(t, "entry.update")
		hookvalues.ResetObservations()
		harness.graphql(t, fmt.Sprintf(`mutation { updateEntry(where: {ID: %q}, data: {nick: {setNull: true}, rank: {increment: "3"}, big: {decrement: "2"}}) { id } }`, hookValueUUIDText(2)))
		graphqlOps := observedHookValues(t, "entry.update")
		assertHookValuesAgree(t, "update null/increment/decrement", typedOps, graphqlOps)
		if graphqlOps["nick"].Operation != golem.MutationFieldNull || graphqlOps["rank"].Operation != golem.MutationFieldIncrement || graphqlOps["big"].Operation != golem.MutationFieldDecrement {
			t.Fatalf("GraphQL update operations nick=%d rank=%d big=%d", graphqlOps["nick"].Operation, graphqlOps["rank"].Operation, graphqlOps["big"].Operation)
		}
	})
}

func TestMutationHookValueTypesMatchForConnectDrivenAssignments(t *testing.T) {
	forEachHookValuesProvider(t, func(t *testing.T, harness *hookValuesHarness) {
		ctx := context.Background()
		for _, id := range []byte{1, 2} {
			if _, err := harness.caller.Entries.Create(ctx, hookvalues.Entries.Create(typedEntryValues(t, id, hookvalues.Entries.Nick.CreateNull(), hookvalues.Entries.Rank.CreateNull())...)); err != nil {
				t.Fatal(err)
			}
		}
		for _, owner := range []byte{1, 2} {
			if _, err := harness.caller.Owners.Create(ctx, hookvalues.Owners.Create(hookvalues.Owners.ID.Create(golem.UUID{15: owner}), hookvalues.Owners.Name.Create("owner"))); err != nil {
				t.Fatal(err)
			}
		}

		hookvalues.ResetObservations()
		if _, err := harness.caller.Owners.Update(ctx, hookvalues.Owners.ByID.Value(golem.UUID{15: 1}), hookvalues.Owners.Update(
			hookvalues.Owners.Entries.Connect(hookvalues.Entries.ByID.Value(golem.UUID{15: 1})),
		)); err != nil {
			t.Fatal(err)
		}
		typed := observedHookValues(t, "entry.update")

		hookvalues.ResetObservations()
		harness.graphql(t, fmt.Sprintf(`mutation { updateOwner(where: {ID: %q}, data: {entries: {connect: [{ID: %q}]}}) { id } }`, hookValueUUIDText(2), hookValueUUIDText(2)))
		graphql := observedHookValues(t, "entry.update")
		if len(typed) == 0 {
			t.Fatal("connect did not reach the entry update hook")
		}
		assertHookValuesAgree(t, "connect-driven assignment", typed, graphql)
		if typed["ownerId"].Type != "golem.UUID" {
			t.Fatalf("connect-driven owner assignment type = %s", typed["ownerId"].Type)
		}
	})
}
