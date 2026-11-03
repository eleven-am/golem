package bind

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func hookValuesBinder(t *testing.T) (*Binder, compilerir.ModelDeclIR) {
	t.Helper()
	result := compile.Compile(context.Background(), compile.Config{Dir: "../../../runtime/testdata/hookvalues", Pattern: ".", Root: "DefineSchema"})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", result.Diagnostics)
	}
	binder, err := New(*result.Compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	model, _ := modelNamed(t, *result.Compilation, "Entry")
	return binder, model
}

func entryField(t *testing.T, model compilerir.ModelDeclIR, goName string) compilerir.FieldIR {
	t.Helper()
	for _, field := range model.Fields {
		if field.GoName == goName {
			return field
		}
	}
	t.Fatalf("missing field %s", goName)
	return compilerir.FieldIR{}
}

func TestMutationValueReturnsPublicGoValues(t *testing.T) {
	binder, model := hookValuesBinder(t)
	decimal, err := golem.ParseDecimal("12.34")
	if err != nil {
		t.Fatal(err)
	}
	date, _ := golem.NewDate(2026, time.November, 3)
	clock, _ := golem.NewTime(12, 34, 56, 789012)
	cases := []struct {
		field string
		raw   any
		want  any
	}{
		{"Text", "alpha", "alpha"},
		{"Flag", true, true},
		{"Small", int64(7), int16(7)},
		{"Integer", int64(70000), int32(70000)},
		{"Big", "5000000000", int64(5_000_000_000)},
		{"Single", 1.5, float32(1.5)},
		{"Double", 2.25, 2.25},
		{"Amount", "12.34", decimal},
		{"Ref", "00000000-0000-0000-0000-000000000009", golem.UUID{15: 9}},
		{"Day", "2026-11-03", date},
		{"Clock", "12:34:56.789012", clock},
		{"Instant", "2026-11-03T12:34:56.123456Z", time.Date(2026, time.November, 3, 12, 34, 56, 123456000, time.UTC)},
		{"Payload", "AQID", []byte{1, 2, 3}},
		{"State", "KindOmega", "omega"},
		{"Tags", []any{"x", "y"}, []string{"x", "y"}},
		{"Tags", []any{}, []string{}},
		{"States", []any{"KindAlpha", "KindOmega"}, []string{"alpha", "omega"}},
		{"States", []any{}, []string{}},
		{"Nick", "n", "n"},
		{"Rank", "9", int64(9)},
	}
	for _, test := range cases {
		t.Run(test.field, func(t *testing.T) {
			field := entryField(t, model, test.field)
			got, err := binder.MutationValue(model.ID, field.ID, test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.TypeOf(got) != reflect.TypeOf(test.want) || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %T(%#v), want %T(%#v)", got, got, test.want, test.want)
			}
		})
	}
}

func TestMutationValueRefusesWhatTheFieldCannotHold(t *testing.T) {
	binder, model := hookValuesBinder(t)
	text := entryField(t, model, "Text")
	if _, err := binder.MutationValue(model.ID, text.ID, int64(3)); err == nil || !strings.Contains(err.Error(), "P5_BIND_MUTATION_VALUE") {
		t.Fatalf("wrong scalar kind error=%v", err)
	}
	state := entryField(t, model, "State")
	if _, err := binder.MutationValue(model.ID, state.ID, "NotAMember"); err == nil {
		t.Fatal("unknown enum member was accepted")
	}
	binder.enumLabels = map[enumMember]string{}
	if _, err := binder.MutationValue(model.ID, state.ID, "KindAlpha"); err == nil || !strings.Contains(err.Error(), "wire value") {
		t.Fatalf("enum without a wire value error=%v", err)
	}
	broken := binder.models[model.ID]
	fields := append([]compilerir.FieldIR(nil), broken.Fields...)
	for index := range fields {
		if fields[index].ID == text.ID {
			scalar := *fields[index].Scalar
			scalar.Type.Kind = "bogus"
			fields[index].Scalar = &scalar
		}
	}
	broken.Fields = fields
	binder.models[model.ID] = broken
	if _, err := binder.MutationValue(model.ID, text.ID, "x"); err == nil || !strings.Contains(err.Error(), "unsupported logical type") {
		t.Fatalf("unsupported logical type error=%v", err)
	}
}
