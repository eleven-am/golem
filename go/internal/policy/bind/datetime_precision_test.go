package bind

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type precisionPost struct{}

func TestOffGridDateTimeOperandsRewriteToExactMicrosecondBounds(t *testing.T) {
	fixture := schematest.NewMutationExactValues(t)
	field := golem.GeneratedNullableOrderedField[precisionPost, time.Time](fixture.PostDateTime)
	v := time.Date(2026, 10, 25, 12, 0, 0, 123456001, time.UTC)
	other := time.Date(2026, 10, 25, 13, 0, 0, 5000, time.UTC)
	f := "1792929600.123456000"
	o := "1792933200.000005000"
	for name, check := range map[string]struct {
		predicate golem.Predicate[precisionPost]
		want      string
	}{
		"lt":             {field.LT(v), "lte(" + f + ")"},
		"lte":            {field.LTE(v), "lte(" + f + ")"},
		"gt":             {field.GT(v), "gt(" + f + ")"},
		"gte":            {field.GTE(v), "gt(" + f + ")"},
		"eq":             {field.Eq(v), "and(gt(" + f + "),lte(" + f + "))"},
		"ne":             {field.Ne(v), "not(and(gt(" + f + "),lte(" + f + ")))"},
		"in-mixed":       {field.In(v, other), "in(" + o + ")"},
		"in-off-grid":    {field.In(v), "and(gt(" + f + "),lte(" + f + "))"},
		"not-in-mixed":   {field.NotIn(v, other), "not_in(" + o + ")"},
		"not-in-offgrid": {field.NotIn(v), "not(and(gt(" + f + "),lte(" + f + ")))"},
		"not-lt":         {golem.Not(field.LT(v)), "not(lte(" + f + "))"},
		"not-eq":         {golem.Not(field.Eq(v)), "not(and(gt(" + f + "),lte(" + f + ")))"},
		"on-grid-lt":     {field.LT(other), "lt(" + o + ")"},
		"on-grid-eq":     {field.Eq(other), "eq(" + o + ")"},
	} {
		t.Run(name, func(t *testing.T) {
			condition := bindPrecisionPredicate(t, fixture, check.predicate)
			if got := describeCondition(condition); got != check.want {
				t.Fatalf("rewrite=%s want=%s", got, check.want)
			}
		})
	}
}

func TestOffGridDateTimeRewriteFloorsNegativeUnixAndYear9999(t *testing.T) {
	fixture := schematest.NewMutationExactValues(t)
	field := golem.GeneratedNullableOrderedField[precisionPost, time.Time](fixture.PostDateTime)
	for name, check := range map[string]struct {
		value time.Time
		want  string
	}{
		"just-before-epoch":   {time.Unix(-1, 500).UTC(), "-1.000000000"},
		"negative-high-nanos": {time.Unix(-2, 999_999_999).UTC(), "-2.999999000"},
		"year-0001":           {time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC), "-62135596800.000000000"},
		"year-9999":           {time.Date(9999, 12, 31, 23, 59, 59, 999_999_999, time.UTC), "253402300799.999999000"},
	} {
		t.Run(name, func(t *testing.T) {
			for operator, predicate := range map[string]golem.Predicate[precisionPost]{
				"lt": field.LT(check.value), "gte": field.GTE(check.value), "eq": field.Eq(check.value), "ne": field.Ne(check.value),
			} {
				condition := bindPrecisionPredicate(t, fixture, predicate)
				if got := describeCondition(condition); !strings.Contains(got, "("+check.want+")") {
					t.Fatalf("%s rewrite=%s want floor %s", operator, got, check.want)
				}
			}
		})
	}
	beyond := field.LT(time.Date(9999, 12, 31, 23, 59, 59, 999_999_999, time.UTC).Add(time.Nanosecond))
	frozen, err := beyond.Freeze(modelDescriptor[precisionPost](fixture.Post))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Predicate(frozen, fixture.Registry, ir.PortableProviders()); err == nil || !strings.Contains(err.Error(), "0001-9999") {
		t.Fatalf("year 10000 operand err=%v", err)
	}
}

func TestOffGridDateTimeRewriteMatchesTheOriginalUnderTwoValuedNullSemantics(t *testing.T) {
	fixture := schematest.NewMutationExactValues(t)
	field := golem.GeneratedNullableOrderedField[precisionPost, time.Time](fixture.PostDateTime)
	floor := time.Date(2026, 10, 25, 12, 0, 0, 123456000, time.UTC)
	v := floor.Add(time.Nanosecond)
	stored := []*time.Time{ref(floor.Add(-time.Microsecond)), ref(floor), ref(floor.Add(time.Microsecond)), nil}
	for name, probe := range datetimeProbes(field, v, floor.Add(time.Microsecond)) {
		condition := bindPrecisionPredicate(t, fixture, probe.predicate)
		for _, value := range stored {
			record := precisionRecord(t, fixture, value)
			got, err := evaluate.Condition(condition, record, ir.PortableProviders())
			if err != nil {
				t.Fatal(err)
			}
			if want := probe.truth(value); got != want {
				t.Fatalf("%s stored=%v evaluator=%t reference=%t", name, value, got, want)
			}
		}
	}
}

func TestSelectorAndListOperandsRefuseSubMicrosecondDateTimeNamingTheField(t *testing.T) {
	fixture := schematest.NewDateTimeKeyed(t)
	v := time.Date(2026, 10, 25, 12, 0, 0, 123456001, time.UTC)
	at := golem.GeneratedOrderedField[precisionPost, time.Time](fixture.PostAt)
	stamps := golem.GeneratedNullableListField[precisionPost, time.Time](fixture.PostStamps)
	descriptor := modelDescriptor[precisionPost](fixture.Post)
	exact, err := at.Eq(v).Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ExactPredicate(exact, fixture.Registry, ir.PortableProviders())
	assertPrecisionRefusal(t, err, fixture.PostAt)
	for name, predicate := range map[string]golem.Predicate[precisionPost]{
		"has":       stamps.Has(v),
		"has-every": stamps.HasEvery(v),
		"has-some":  stamps.HasSome(v),
		"equal":     stamps.Eq(golem.List[time.Time]{v}),
	} {
		t.Run(name, func(t *testing.T) {
			frozen, err := predicate.Freeze(descriptor)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Predicate(frozen, fixture.Registry, ir.PortableProviders())
			assertPrecisionRefusal(t, err, fixture.PostStamps)
		})
	}
	onGrid, err := at.Eq(v.Truncate(time.Microsecond)).Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExactPredicate(onGrid, fixture.Registry, ir.PortableProviders()); err != nil {
		t.Fatalf("on-grid selector refused: %v", err)
	}
}

func TestPolicyWithSubMicrosecondActorTimeFreezesAndBinds(t *testing.T) {
	fixture := schematest.NewMutationExactValues(t)
	field := golem.GeneratedNullableOrderedField[precisionPost, time.Time](fixture.PostDateTime)
	now := time.Date(2026, 10, 25, 12, 0, 0, 123456789, time.UTC)
	rules := golem.NewRules[precisionPost]()
	rules.CanRead(field.GTE(now))
	frozen, err := rules.Freeze(fixture.Post)
	if err != nil {
		t.Fatalf("policy with an actor time freeze err=%v", err)
	}
	policy, err := Policy(frozen, fixture.Registry, ir.PortableProviders())
	if err != nil {
		t.Fatalf("policy with an actor time bind err=%v", err)
	}
	condition, ok := policy.Rules()[0].Condition()
	if !ok {
		t.Fatal("policy rule has no condition")
	}
	if got := describeCondition(condition); got != "gt(1792929600.123456000)" {
		t.Fatalf("policy rewrite=%s", got)
	}
}

type datetimeProbe struct {
	predicate golem.Predicate[precisionPost]
	truth     func(*time.Time) bool
}

func datetimeProbes(field golem.NullableOrderedField[precisionPost, time.Time], v, onGrid time.Time) map[string]datetimeProbe {
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
	base := map[string]datetimeProbe{
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
	probes := make(map[string]datetimeProbe, len(base)*2)
	for name, probe := range base {
		probe := probe
		probes[name] = probe
		probes["not-"+name] = datetimeProbe{predicate: golem.Not(probe.predicate), truth: func(value *time.Time) bool { return !probe.truth(value) }}
	}
	return probes
}

func bindPrecisionPredicate(t *testing.T, fixture schematest.Fixture, predicate golem.Predicate[precisionPost]) ir.Condition {
	t.Helper()
	frozen, err := predicate.Freeze(modelDescriptor[precisionPost](fixture.Post))
	if err != nil {
		t.Fatalf("freeze sub-microsecond operand: %v", err)
	}
	condition, err := Predicate(frozen, fixture.Registry, ir.PortableProviders())
	if err != nil {
		t.Fatalf("bind sub-microsecond operand: %v", err)
	}
	return condition
}

func precisionRecord(t *testing.T, fixture schematest.Fixture, value *time.Time) evaluate.Record {
	t.Helper()
	field := evaluate.NullField(ir.FieldID(fixture.PostDateTime))
	if value != nil {
		bound, err := ir.NewDateTimeValue(value.Unix(), uint32(value.Nanosecond()))
		if err != nil {
			t.Fatal(err)
		}
		field, err = evaluate.ValueField(ir.FieldID(fixture.PostDateTime), bound)
		if err != nil {
			t.Fatal(err)
		}
	}
	record, err := evaluate.NewRecord(ir.ModelID(fixture.Post), field)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func assertPrecisionRefusal(t *testing.T, err error, field golem.FieldID) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Reason != golem.ReasonValuePrecision || failure.FieldID != ir.FieldID(field) {
		t.Fatalf("precision refusal err=%v", err)
	}
}

func describeCondition(condition ir.Condition) string {
	if logical, children, ok := condition.Logical(); ok {
		parts := make([]string, len(children))
		for index, child := range children {
			parts[index] = describeCondition(child)
		}
		name := map[ir.LogicalOperator]string{ir.LogicalAnd: "and", ir.LogicalOr: "or", ir.LogicalNot: "not"}[logical]
		return name + "(" + strings.Join(parts, ",") + ")"
	}
	operatorID, _ := condition.Operator()
	name := map[ir.OperatorID]string{
		ir.OperatorEqual: "eq", ir.OperatorNotEqual: "ne", ir.OperatorIn: "in", ir.OperatorNotIn: "not_in",
		ir.OperatorLessThan: "lt", ir.OperatorLessThanOrEqual: "lte", ir.OperatorGreaterThan: "gt", ir.OperatorGreaterThanOrEqual: "gte",
	}[operatorID]
	operand, _ := condition.Operand()
	values := []ir.Value{}
	if one, ok := operand.One(); ok {
		values = append(values, one)
	}
	if many, ok := operand.Many(); ok {
		values = append(values, many...)
	}
	parts := make([]string, len(values))
	for index, value := range values {
		seconds, nanoseconds, _ := value.DateTime()
		parts[index] = fmt.Sprintf("%d.%09d", seconds, nanoseconds)
	}
	return name + "(" + strings.Join(parts, ",") + ")"
}

func ref(value time.Time) *time.Time { return &value }
