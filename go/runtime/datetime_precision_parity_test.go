package runtime

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
)

type parityPost struct{}

type parityProbe struct {
	predicate golem.Predicate[parityPost]
	truth     func(*time.Time) bool
}

func TestSubMicrosecondDatetimeRewritesAgreeAcrossProvidersAndEvaluator(t *testing.T) {
	runExactValueProfiles(t, func(t *testing.T, profile exactValueProfile) {
		ctx := context.Background()
		fixture := profile.fixture
		resolver := policysql.SchemaResolver(fixture.Registry)
		var dialect policysql.Dialect = sqliteprovider.NewPolicyDialect()
		if profile.provider == policyir.ProviderPostgreSQL {
			dialect = postgresprovider.NewPolicyDialect()
		}
		descriptor, ok := resolver.Field(profile.provider, policyir.ModelID(fixture.Post), policyir.FieldID(fixture.PostDateTime))
		if !ok {
			t.Fatal("datetime field has no physical descriptor")
		}
		floor := time.Date(2026, 10, 25, 12, 0, 0, 123456000, time.UTC)
		stored := map[byte]*time.Time{1: parityTime(floor.Add(-time.Microsecond)), 2: parityTime(floor), 3: parityTime(floor.Add(time.Microsecond)), 4: nil}
		if _, err := profile.database.ExecContext(ctx, `INSERT INTO `+profile.prefix+`"users" ("id","name") VALUES (`+profile.placeholder(1)+`,`+profile.placeholder(2)+`)`, mutationResultUUIDText(200), "parity-author"); err != nil {
			t.Fatal(err)
		}
		for id, value := range stored {
			var argument any
			if value != nil {
				bound, err := policyir.NewDateTimeValue(value.Unix(), uint32(value.Nanosecond()))
				if err != nil {
					t.Fatal(err)
				}
				argument, err = policysql.EncodeOperand(dialect, resolver, bound, descriptor.Type)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := profile.database.ExecContext(ctx, `INSERT INTO `+profile.prefix+`"posts" ("id","author_id","title","big_int","decimal_value","datetime_value") VALUES (`+profile.placeholder(1)+`,`+profile.placeholder(2)+`,`+profile.placeholder(3)+`,`+profile.placeholder(4)+`,`+profile.placeholder(5)+`,`+profile.placeholder(6)+`)`, mutationResultUUIDText(id), mutationResultUUIDText(200), "parity", int64(0), int64(0), argument); err != nil {
				t.Fatal(err)
			}
		}
		field := golem.GeneratedNullableOrderedField[parityPost, time.Time](fixture.PostDateTime)
		v := floor.Add(time.Nanosecond)
		model := golem.GeneratedModelDescriptor[parityPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
		for name, probe := range parityProbes(field, v, floor.Add(time.Microsecond)) {
			frozen, err := probe.predicate.Freeze(model)
			if err != nil {
				t.Fatalf("%s freeze: %v", name, err)
			}
			condition, err := policybind.Predicate(frozen, fixture.Registry, policyir.PortableProviders())
			if err != nil {
				t.Fatalf("%s bind: %v", name, err)
			}
			fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: profile.provider, Resolver: resolver, Dialect: dialect, Capabilities: profile.proof, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: "root"})
			if err != nil {
				t.Fatalf("%s compile: %v", name, err)
			}
			var rows []string
			if err := profile.database.SelectContext(ctx, &rows, `SELECT "root"."id" FROM `+profile.prefix+`"posts" AS "root" WHERE `+fragment.SQL(), fragment.Args()...); err != nil {
				t.Fatalf("%s query: %v", name, err)
			}
			sort.Strings(rows)
			var evaluated, reference []string
			for id, value := range stored {
				record := parityRecord(t, fixture, value)
				matched, err := evaluate.Condition(condition, record, policyir.PortableProviders())
				if err != nil {
					t.Fatal(err)
				}
				if matched {
					evaluated = append(evaluated, mutationResultUUIDText(id))
				}
				if probe.truth(value) {
					reference = append(reference, mutationResultUUIDText(id))
				}
			}
			sort.Strings(evaluated)
			sort.Strings(reference)
			if !sameStrings(rows, reference) || !sameStrings(evaluated, reference) {
				t.Fatalf("%s sql=%v evaluator=%v reference=%v", name, rows, evaluated, reference)
			}
		}
	})
}

func TestReadBindPrecisionRefusalIsPublicBadUserInputNamingTheField(t *testing.T) {
	fixture := schematest.NewDateTimeKeyed(t)
	descriptor := golem.GeneratedModelDescriptor[parityPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	stamps := golem.GeneratedNullableListField[parityPost, time.Time](fixture.PostStamps)
	frozen, err := golem.FreezeFindMany(descriptor, golem.Where(stamps.Has(time.Date(2026, 10, 25, 12, 0, 0, 123456001, time.UTC))))
	if err != nil {
		t.Fatal(err)
	}
	_, cause := readbind.Request(frozen, fixture.Registry, policyir.PortableProviders())
	refused, ok := publicBindReasonError(fixture.Registry, "findMany", fixture.Post, cause)
	var failure *golem.Error
	if !ok || !errors.As(refused, &failure) || failure.Code != golem.CodeBadUserInput || failure.Reason != golem.ReasonValuePrecision || failure.Field != fixture.PostStamps || failure.Message != "value for Post.Stamps exceeds the declared precision" {
		t.Fatalf("public precision refusal=%#v ok=%t cause=%v", refused, ok, cause)
	}
}

func parityProbes(field golem.NullableOrderedField[parityPost, time.Time], v, onGrid time.Time) map[string]parityProbe {
	compare := func(test func(time.Time) bool, null bool) func(*time.Time) bool {
		return func(value *time.Time) bool {
			if value == nil {
				return null
			}
			return test(*value)
		}
	}
	member := func(values ...time.Time) func(time.Time) bool {
		return func(stored time.Time) bool {
			for _, value := range values {
				if stored.Equal(value) {
					return true
				}
			}
			return false
		}
	}
	base := map[string]parityProbe{
		"lt":           {field.LT(v), compare(func(s time.Time) bool { return s.Before(v) }, false)},
		"lte":          {field.LTE(v), compare(func(s time.Time) bool { return !s.After(v) }, false)},
		"gt":           {field.GT(v), compare(func(s time.Time) bool { return s.After(v) }, false)},
		"gte":          {field.GTE(v), compare(func(s time.Time) bool { return !s.Before(v) }, false)},
		"eq":           {field.Eq(v), compare(member(v), false)},
		"ne":           {field.Ne(v), compare(func(s time.Time) bool { return !s.Equal(v) }, true)},
		"in":           {field.In(v), compare(member(v), false)},
		"in-mixed":     {field.In(v, onGrid), compare(member(v, onGrid), false)},
		"not-in":       {field.NotIn(v), compare(func(s time.Time) bool { return !member(v)(s) }, true)},
		"not-in-mixed": {field.NotIn(v, onGrid), compare(func(s time.Time) bool { return !member(v, onGrid)(s) }, true)},
	}
	probes := make(map[string]parityProbe, len(base)*2)
	for name, probe := range base {
		probe := probe
		probes[name] = probe
		probes["not-"+name] = parityProbe{predicate: golem.Not(probe.predicate), truth: func(value *time.Time) bool { return !probe.truth(value) }}
	}
	return probes
}

func parityRecord(t *testing.T, fixture schematest.Fixture, value *time.Time) evaluate.Record {
	t.Helper()
	field := evaluate.NullField(policyir.FieldID(fixture.PostDateTime))
	if value != nil {
		bound, err := policyir.NewDateTimeValue(value.Unix(), uint32(value.Nanosecond()))
		if err != nil {
			t.Fatal(err)
		}
		field, err = evaluate.ValueField(policyir.FieldID(fixture.PostDateTime), bound)
		if err != nil {
			t.Fatal(err)
		}
	}
	record, err := evaluate.NewRecord(policyir.ModelID(fixture.Post), field)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func parityTime(value time.Time) *time.Time { return &value }

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
