package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type parityKindPost struct{}
type parityKindUser struct{}
type parityStatus string

type parityKindFields struct {
	id       golem.EqualField[parityKindPost, golem.UUID]
	bigInt   golem.OrderedField[parityKindPost, int64]
	decimal  golem.OrderedField[parityKindPost, golem.Decimal]
	int32    golem.NullableOrderedField[parityKindPost, int32]
	datetime golem.NullableOrderedField[parityKindPost, time.Time]
	boolean  golem.NullableEqualField[parityKindPost, bool]
	status   golem.NullableEqualField[parityKindPost, parityStatus]
	bytes    golem.BytesField[parityKindPost]
	json     golem.NullableJSONField[parityKindPost]
	list     golem.ListField[parityKindPost, string]
}

var parityKindEpoch = time.Date(2024, 3, 5, 10, 30, 0, 123456000, time.UTC)

func parityDecimal(t *testing.T, coefficient int64) golem.Decimal {
	t.Helper()
	value, err := golem.NewDecimal(coefficient, 13)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func parityKindPolicies(t *testing.T, f parityKindFields) []golem.Predicate[parityKindPost] {
	one, two := parityDecimal(t, 15000000000000), parityDecimal(t, 22500000000000)
	late := parityKindEpoch.Add(time.Microsecond)
	isTrue := f.boolean.Eq(true)
	return []golem.Predicate[parityKindPost]{
		f.bigInt.Eq(5), f.bigInt.Ne(5), f.bigInt.GT(3), f.bigInt.LTE(5), f.bigInt.In(1, 5), f.bigInt.NotIn(5),
		f.int32.Eq(7), f.int32.Ne(7), f.int32.IsNull(), f.int32.GT(0), f.int32.NotIn(7), golem.Not(f.int32.Eq(7)), f.int32.LT(0),
		f.decimal.Eq(one), f.decimal.GT(one), f.decimal.LT(two), f.decimal.In(one, two), f.decimal.GTE(two),
		f.datetime.Eq(parityKindEpoch), f.datetime.GT(parityKindEpoch), f.datetime.LTE(parityKindEpoch), f.datetime.IsNull(), f.datetime.Ne(parityKindEpoch), f.datetime.LT(late),
		isTrue, f.boolean.Ne(true), f.boolean.IsNull(), golem.Not(isTrue), f.boolean.Eq(false),
		f.status.Eq("draft"), f.status.Ne("draft"), f.status.In("draft", "published"), f.status.IsNull(), f.status.NotIn("published"),
		golem.Or(isTrue, f.int32.Eq(7)), golem.And(golem.Not(f.status.Eq("draft")), f.datetime.IsNotNull()), golem.Or(f.bigInt.GT(3), golem.Not(f.decimal.Eq(one))),
	}
}

func parityUnprovenKindPolicies(f parityKindFields) []golem.Predicate[parityKindPost] {
	return []golem.Predicate[parityKindPost]{
		f.bytes.Eq([]byte{1}), f.bytes.Ne([]byte{1}), f.json.IsNull(), f.json.IsNotNull(), f.list.Has("x"), f.list.IsEmpty(true),
		golem.Not(f.bytes.Eq([]byte{1})), golem.Or(f.json.IsNull(), f.bigInt.Eq(5)), golem.And(f.json.IsNull(), f.bigInt.Eq(5)),
	}
}

func parityKindWheres(t *testing.T, f parityKindFields) []parityFilter[parityKindPost] {
	return []parityFilter[parityKindPost]{
		parityNoFilter[parityKindPost](),
		parityFilterOf(f.boolean.Eq(true)),
		parityFilterOf(f.int32.Ne(7)),
		parityFilterOf(golem.Or(f.status.IsNull(), f.datetime.GT(parityKindEpoch))),
	}
}

func TestEventJudgeAgreesWithExactReadOverEveryComparisonKind(t *testing.T) {
	runExactValueProfilesFor(t, schematest.NewSubscribedMutationExactValues, func(t *testing.T, profile exactValueProfile) {
		fixture := profile.fixture
		provider := golem.SQLite
		if profile.provider == policyir.ProviderPostgreSQL {
			provider = golem.PostgreSQL
		}
		fields := parityKindFields{
			id:       golem.GeneratedEqualField[parityKindPost, golem.UUID](fixture.PostID),
			bigInt:   golem.GeneratedOrderedField[parityKindPost, int64](fixture.PostBigInt),
			decimal:  golem.GeneratedOrderedField[parityKindPost, golem.Decimal](fixture.PostDecimal),
			int32:    golem.GeneratedNullableOrderedField[parityKindPost, int32](fixture.PostInt32),
			datetime: golem.GeneratedNullableOrderedField[parityKindPost, time.Time](fixture.PostDateTime),
			boolean:  golem.GeneratedNullableEqualField[parityKindPost, bool](fixture.PostBool),
			status:   golem.GeneratedNullableEqualField[parityKindPost, parityStatus](fixture.PostEnum),
			bytes:    golem.GeneratedBytesField[parityKindPost](fixture.PostBytes),
			json:     golem.GeneratedNullableJSONField[parityKindPost](fixture.PostJSON),
			list:     golem.GeneratedListField[parityKindPost, string](fixture.PostList),
		}
		userIdentity := golem.GeneratedIdentityMetadata(fixture.User, fixture.UserKey, golem.PrimaryIdentity, fixture.UserID)
		postIdentity := golem.GeneratedIdentityMetadata(fixture.Post, fixture.PostKey, golem.PrimaryIdentity, fixture.PostID)
		userDescriptor := golem.GeneratedModelDescriptor[parityKindUser](fixture.User, golem.GeneratedDescriptorShape(
			[]golem.FieldID{fixture.UserID, fixture.UserName}, nil, []golem.IdentityMetadata{userIdentity}, nil))
		postDescriptor := golem.GeneratedModelDescriptor[parityKindPost](fixture.Post, golem.GeneratedDescriptorShape(
			[]golem.FieldID{fixture.PostID, fixture.PostBigInt, fixture.PostDecimal, fixture.PostInt32, fixture.PostDateTime, fixture.PostBool, fixture.PostEnum, fixture.PostBytes, fixture.PostJSON, fixture.PostList}, nil, []golem.IdentityMetadata{postIdentity}, nil))
		generation := fixture.Bundle.GenerationDigest()
		descriptors, err := golem.GeneratedApplicationDescriptors(generation, golem.GeneratedStampedPackageDescriptors(generation, userDescriptor.Metadata(), postDescriptor.Metadata()))
		if err != nil {
			t.Fatal(err)
		}
		provenPolicies := parityKindPolicies(t, fields)
		unprovenPolicies := parityUnprovenKindPolicies(fields)
		policies := append(append([]golem.Predicate[parityKindPost]{}, provenPolicies...), unprovenPolicies...)
		userBinding := golem.GeneratedPolicyBinding[parityOptActor, parityKindUser](fixture.User, func(parityOptActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[parityKindUser]()
			rules.CanRead(golem.All[parityKindUser]())
			return rules.Freeze(fixture.User)
		})
		postBinding := golem.GeneratedPolicyBinding[parityOptActor, parityKindPost](fixture.Post, func(actor parityOptActor) (golem.FrozenPolicy, error) {
			var index int
			if _, err := fmt.Sscanf(actor.Name, "k%d", &index); err != nil {
				return golem.FrozenPolicy{}, err
			}
			rules := golem.NewRules[parityKindPost]()
			rules.CanRead(policies[index])
			return rules.Freeze(fixture.Post)
		})
		bindings, err := golem.GeneratedApplicationBindings(generation, golem.GeneratedStampedPackageBindings(generation, []golem.PolicyBinding[parityOptActor]{userBinding, postBinding}, nil))
		if err != nil {
			t.Fatal(err)
		}
		model, _ := fixture.Registry.Model(fixture.Post)
		fingerprint, _, _ := model.EventSchema()
		digest, err := parityEventSchemaDigest(fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		eventRegistry, err := golem.GeneratedEventRegistry(generation, golem.GeneratedPackageEventRegistry(generation, golem.GeneratedEventModelMetadata(fixture.Post, digest, []golem.FieldID{fixture.PostID}, "p7EventOracleValue", "golem.UUID")))
		if err != nil {
			t.Fatal(err)
		}
		factories, err := GeneratedEventFactoryRegistry(generation, GeneratedPackageEventFactories(generation, p7EventOracleFactory{model: fixture.Post, schema: digest}))
		if err != nil {
			t.Fatal(err)
		}
		memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
		if err != nil {
			t.Fatal(err)
		}
		app, err := Open(context.Background(), Config[parityOptPrincipal, parityOptActor]{
			Database: p8RuntimeTestDatabase(profile.database, provider), Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
			EventRegistry: eventRegistry, EventFactories: factories, EventTransport: memory,
			ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
			ResolvePrincipal: func(_ context.Context, principal parityOptPrincipal) (parityOptActor, error) {
				return parityOptActor(principal), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		metadata, _ := app.eventRegistry.Lookup(fixture.Post)

		profile.exec(t, `INSERT INTO `+profile.prefix+`"users" ("id","name") VALUES (`+profile.placeholder(1)+`,`+profile.placeholder(2)+`)`, mutationResultUUIDText(201), "author")
		bigInts, decimals := []int64{5, 3, -1}, []int64{15000000000000, 22500000000000, 0}
		int32s, datetimes := []any{int32(7), nil, int32(-2)}, []time.Time{parityKindEpoch, parityKindEpoch.Add(time.Microsecond), {}}
		bools, statuses := []any{true, false, nil}, []any{"draft", "published", nil}
		var rows []golem.UUID
		for index := 0; index < 27; index++ {
			var datetime any
			if value := datetimes[(index/4)%3]; !value.IsZero() {
				datetime = value
				if profile.provider == policyir.ProviderSQLite {
					datetime = value.UnixMicro()
				}
			}
			id := deleteEvalUUID(byte(10 + index))
			rows = append(rows, id)
			query := `INSERT INTO ` + profile.prefix + `"posts" ("id","author_id","title","big_int","decimal_value","int32_value","datetime_value","bool_value","enum_value") VALUES (`
			for column := 1; column <= 9; column++ {
				if column > 1 {
					query += ","
				}
				query += profile.placeholder(column)
			}
			query += ")"
			decimal := any(decimals[(index/3)%3])
			if profile.provider == policyir.ProviderPostgreSQL {
				decimal = fmt.Sprintf("%d.%013d", decimals[(index/3)%3]/10000000000000, decimals[(index/3)%3]%10000000000000)
			}
			profile.exec(t, query, id.String(), mutationResultUUIDText(201), "row", bigInts[index%3], decimal, int32s[(index/2)%3], datetime, bools[(index/5)%3], statuses[(index/7)%3])
		}
		callers := make([]*Caller[parityOptPrincipal, parityOptActor], len(policies))
		for index := range callers {
			caller, err := app.ForPrincipal(context.Background(), parityOptPrincipal{Name: fmt.Sprintf("k%d", index)})
			if err != nil {
				t.Fatal(err)
			}
			callers[index] = caller
		}
		wheres := parityKindWheres(t, fields)
		stats := runParityCorpus(t, app, parityCorpus{
			model: fixture.Post, metadata: metadata, key: fixture.PostKey, idField: fixture.PostID,
			rows: rows, policies: len(provenPolicies), wheres: len(wheres),
			spec: func(policy, where int) *eventJudgeSpec {
				return app.newEventJudgeSpec(callers[policy], fixture.Post, metadata, parityBoundWhere(t, app, callers[policy], postDescriptor, metadata, wheres[where]))
			},
			exact: func(policy, where int, row golem.UUID) bool {
				return parityCount(t, callers[policy], postDescriptor, fields.id.Eq(row), wheres[where])
			},
		})
		stats.require(t, "integer, decimal, datetime, boolean and enum", false)

		covered := map[policyir.ValueKind]bool{policyir.ValueUUID: true, policyir.ValueString: true}
		for policy := range provenPolicies {
			if spec := app.newEventJudgeSpec(callers[policy], fixture.Post, metadata, nil); spec != nil {
				parityLeafKinds(spec.row, covered)
			}
		}
		for kind := range judgeProvenKinds {
			if !covered[kind] {
				t.Fatalf("judgeProvenKinds lists kind %d that no corpus exercises", kind)
			}
		}
		for kind := range covered {
			if !judgeProvenKinds[kind] {
				t.Fatalf("the corpus exercises kind %d that judgeProvenKinds does not list", kind)
			}
		}

		offset := len(provenPolicies)
		const pureUnproven = 7
		unproven := runParityCorpus(t, app, parityCorpus{
			model: fixture.Post, metadata: metadata, key: fixture.PostKey, idField: fixture.PostID,
			rows: rows, policies: pureUnproven, wheres: 1,
			spec: func(policy, where int) *eventJudgeSpec {
				return app.newEventJudgeSpec(callers[offset+policy], fixture.Post, metadata, parityBoundWhere(t, app, callers[offset+policy], postDescriptor, metadata, wheres[where]))
			},
			exact: func(policy, where int, row golem.UUID) bool {
				return parityCount(t, callers[offset+policy], postDescriptor, fields.id.Eq(row), wheres[where])
			},
		})
		t.Logf("kinds the corpus does not prove: %+v", unproven)
		if unproven.decidedTrue != 0 || unproven.decidedFalse != 0 {
			t.Fatalf("the judge decided cells over bytes, JSON and list: %+v", unproven)
		}
		mixedOffset := offset + pureUnproven
		mixed := runParityCorpus(t, app, parityCorpus{
			model: fixture.Post, metadata: metadata, key: fixture.PostKey, idField: fixture.PostID,
			rows: rows, policies: len(unprovenPolicies) - pureUnproven, wheres: len(wheres),
			spec: func(policy, where int) *eventJudgeSpec {
				return app.newEventJudgeSpec(callers[mixedOffset+policy], fixture.Post, metadata, parityBoundWhere(t, app, callers[mixedOffset+policy], postDescriptor, metadata, wheres[where]))
			},
			exact: func(policy, where int, row golem.UUID) bool {
				return parityCount(t, callers[mixedOffset+policy], postDescriptor, fields.id.Eq(row), wheres[where])
			},
		})
		t.Logf("unproven leaf beside a proven one (agreement only): %+v", mixed)
	})
}

func parityLeafKinds(condition policyir.Condition, into map[policyir.ValueKind]bool) {
	switch condition.Kind() {
	case policyir.ConditionLogical:
		_, children, _ := condition.Logical()
		for _, child := range children {
			parityLeafKinds(child, into)
		}
	case policyir.ConditionScalar:
		if typ, ok := condition.FieldType(); ok {
			into[typ.Kind()] = true
		}
	}
}

func (profile exactValueProfile) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := profile.database.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
