package operationargs

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlscalar "github.com/eleven-am/golem/go/internal/graphql/scalar"
)

func canonicalScalar(name string, nullable bool) compilerir.GraphQLTypeIR {
	return compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeScalar, Name: name, Nullable: nullable}
}
func canonicalRegistry(t *testing.T, arguments []compilerir.GraphQLArgumentContractIR, definitions ...compilerir.GraphQLTypeDefinitionIR) *Registry {
	t.Helper()
	var compilation compilerir.CompilationIR
	compilation.Model.Schema.PackagePath = "example.test/arguments"
	compilation.Model.Enums = []compilerir.EnumIR{{ID: "role", Values: []compilerir.EnumValueIR{{ID: "from", WireValue: "from"}, {ID: "to", WireValue: "to"}}}}
	compilation.Contract.Enums = []compilerir.EnumContractIR{{EnumID: "role", GraphQLName: "AddressRole", Values: []compilerir.EnumValueContractIR{{ValueID: "from", GraphQLName: "AddressRoleFrom"}, {ValueID: "to", GraphQLName: "AddressRoleTo"}}}}
	compilation.Contract.Models = []compilerir.ModelContractIR{{ModelID: "00000000000000000000000000000001", GraphQLName: "User", Exposed: true}}
	compilation.Contract.GraphQLTypes = definitions
	compilation.Contract.CustomOperations = []compilerir.CustomOperationContractIR{{ExtensionID: "page", Operation: compilerir.CustomOperationQuery, Name: "page", Arguments: arguments, Result: canonicalScalar("String", false), Resolver: compilerir.AttachedMethodIR{PackagePath: compilation.Model.Schema.PackagePath, Name: "Page", Kind: "customquery"}, Capability: compilerir.CustomOperationCallerOnly}}
	registry, err := New(compilation)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
func canonicalPrepare(t *testing.T, registry *Registry, supplied map[string]any) Request {
	t.Helper()
	request, err := registry.Prepare(compilerir.CustomOperationQuery, "page", supplied)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
func canonicalBytes(t *testing.T, registry *Registry, supplied map[string]any, excluded ...string) []byte {
	t.Helper()
	result, err := registry.CanonicalArguments(canonicalPrepare(t, registry, supplied), excluded...)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func canonicalInput(name string, fields ...compilerir.GraphQLObjectFieldIR) compilerir.GraphQLTypeDefinitionIR {
	return compilerir.GraphQLTypeDefinitionIR{Kind: compilerir.GraphQLTypeInputObject, Name: name, Go: compilerir.GoNamedTypeIR{PackagePath: "example.test/arguments", Name: name}, Fields: fields}
}

func TestPrepareCanonicalBoundsBeforeValidationAndCloning(t *testing.T) {
	integer := canonicalScalar("Int", false)
	list := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &integer}
	registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: list}})
	t.Run("list length before elements", func(t *testing.T) {
		items := make([]any, MaxCanonicalArgumentNodes+1)
		// Ordinary validation would reject this first element, while the bounded
		// preflight must stop at the container length without visiting elements.
		items[0] = "wrong scalar representation"
		if _, err := registry.PrepareCanonical(compilerir.CustomOperationQuery, "page", map[string]any{"value": items}); !errors.Is(err, ErrCanonicalArgumentsLimit) {
			t.Fatalf("list was traversed before its bound: %v", err)
		}
	})
	t.Run("cyclic list", func(t *testing.T) {
		items := make([]any, 1)
		items[0] = items
		nested := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &list}}})
		if _, err := nested.PrepareCanonical(compilerir.CustomOperationQuery, "page", map[string]any{"value": items}); !errors.Is(err, ErrCanonicalArgumentsInvalid) {
			t.Fatalf("cyclic list escaped the closed schema: %v", err)
		}
	})
	t.Run("cyclic object", func(t *testing.T) {
		leaf := canonicalInput("Leaf", compilerir.GraphQLObjectFieldIR{Name: "next", GoName: "Next", Type: canonicalScalar("String", true)})
		leafType := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "Leaf"}
		outer := canonicalInput("Outer", compilerir.GraphQLObjectFieldIR{Name: "next", GoName: "Next", Type: leafType})
		objects := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "Outer"}}}, leaf, outer)
		value := map[string]any{}
		value["next"] = value
		if _, err := objects.PrepareCanonical(compilerir.CustomOperationQuery, "page", map[string]any{"value": value}); !errors.Is(err, ErrCanonicalArgumentsInvalid) {
			t.Fatalf("cyclic object escaped the closed schema: %v", err)
		}
	})
	t.Run("unsupported omitted declaration before input", func(t *testing.T) {
		args := []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: list}, {Name: "omitted", Type: canonicalScalar("JSON", true)}}
		unsupported := canonicalRegistry(t, args)
		items := make([]any, MaxCanonicalArgumentNodes+1)
		if _, err := unsupported.PrepareCanonical(compilerir.CustomOperationQuery, "page", map[string]any{"value": items}); !errors.Is(err, ErrCanonicalArgumentsUnsupported) {
			t.Fatalf("input visited before unsupported omitted declaration: %v", err)
		}
	})
	t.Run("unsupported values never invoke user code", func(t *testing.T) {
		unsupported := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: canonicalScalar("JSON", false)}})
		if _, err := unsupported.PrepareCanonical(compilerir.CustomOperationQuery, "page", map[string]any{"value": canonicalMarshalTrap{}}); !errors.Is(err, ErrCanonicalArgumentsUnsupported) {
			t.Fatalf("unsupported value reached validation: %v", err)
		}
	})
}

type canonicalMarshalTrap struct{}

func (canonicalMarshalTrap) MarshalJSON() ([]byte, error) {
	panic("canonical preflight must not call user JSON marshalers")
}

func TestPrepareCanonicalOwnsTheSamePreparedArguments(t *testing.T) {
	element := canonicalScalar("BigInt", false)
	registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{
		{Name: "values", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &element}},
		{Name: "after", Type: canonicalScalar("String", true), PresenceAware: true},
	})
	values := []any{int64(9007199254740993), int64(math.MaxInt64)}
	supplied := map[string]any{"values": values}
	ordinary := canonicalBytes(t, registry, supplied)
	request, err := registry.PrepareCanonical(compilerir.CustomOperationQuery, "page", supplied)
	if err != nil {
		t.Fatal(err)
	}
	values[0] = int64(0)
	supplied["after"] = "later mutation"
	bounded, err := registry.CanonicalArguments(request)
	if err != nil || !bytes.Equal(ordinary, bounded) {
		t.Fatalf("bounded preparation changed ownership or canonical semantics: %v", err)
	}
	if request.Arguments()[1].Present() {
		t.Fatal("bounded preparation lost omitted argument state")
	}
	for _, value := range []map[string]any{{"unknown": nil}, {"values": []any{int64(1)}, "after": nil, "unknown": nil}} {
		if _, err := registry.PrepareCanonical(compilerir.CustomOperationQuery, "page", value); !errors.Is(err, ErrCanonicalArgumentsInvalid) {
			t.Fatalf("unknown supplied argument accepted: %v", err)
		}
	}
}

func TestOrdinaryPrepareAlsoBoundsRegisteredPageArguments(t *testing.T) {
	integer := canonicalScalar("Int", false)
	page := compilerir.CustomPaginationIR{DefaultTake: 1, MaxTake: 1, MaxCandidates: 1, Revision: "bounded-page", Item: canonicalScalar("String", false)}
	var compilation compilerir.CompilationIR
	compilation.Model.Schema.PackagePath = "example.test/arguments"
	compilation.Contract.DataIncarnation = &compilerir.DataIncarnationContractIR{Kind: "provider", Version: 1}
	compilation.Contract.GraphQLTypes = []compilerir.GraphQLTypeDefinitionIR{compilerir.GraphQLPageDefinition("BoundedPage", page)}
	compilation.Contract.CustomOperations = []compilerir.CustomOperationContractIR{{
		ExtensionID: "bounded-page", Operation: compilerir.CustomOperationQuery, Name: "bounded", Pagination: &page,
		Result: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "BoundedPage"}, Capability: compilerir.CustomOperationCallerOnly,
		Resolver: compilerir.AttachedMethodIR{PackagePath: compilation.Model.Schema.PackagePath, Name: "Bounded", Kind: "customquery"},
		Arguments: []compilerir.GraphQLArgumentContractIR{
			{Name: "values", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &integer}},
			{Name: "take", Type: canonicalScalar("Int", true)},
			{Name: "after", Type: canonicalScalar("String", true)},
		},
	}}
	registry, err := New(compilation)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]any, MaxCanonicalArgumentNodes+1)
	for index := range values {
		values[index] = int32(index)
	}
	if _, err := registry.Prepare(compilerir.CustomOperationQuery, "bounded", map[string]any{"values": values}); !errors.Is(err, ErrCanonicalArgumentsLimit) {
		t.Fatalf("ordinary preparation traversed/cloned an oversized page input before admission: %v", err)
	}
	prepared, err := registry.Prepare(compilerir.CustomOperationQuery, "bounded", map[string]any{"values": []any{int32(1)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.CanonicalArguments(prepared, "after"); err != nil {
		t.Fatalf("valid page input lost its owned request: %v", err)
	}
}

func TestCanonicalArgumentsUseClosedInputSchemaAndExplicitExclusions(t *testing.T) {
	leaf := canonicalInput("Range", compilerir.GraphQLObjectFieldIR{Name: "low", GoName: "Low", Type: canonicalScalar("BigInt", false)}, compilerir.GraphQLObjectFieldIR{Name: "high", GoName: "High", Type: canonicalScalar("BigInt", false)})
	input := canonicalInput("Input", compilerir.GraphQLObjectFieldIR{Name: "range", GoName: "Range", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "Range"}}, compilerir.GraphQLObjectFieldIR{Name: "role", GoName: "Role", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeEnum, Name: "AddressRole"}})
	args := []compilerir.GraphQLArgumentContractIR{{Name: "input", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "Input"}}, {Name: "take", Type: canonicalScalar("Int", false)}, {Name: "after", Type: canonicalScalar("String", true)}}
	registry := canonicalRegistry(t, args, leaf, input)
	fromLiteral, err := graphqlscalar.BigInt(json.Number("9007199254740993"))
	if err != nil {
		t.Fatal(err)
	}
	fromVariable, err := graphqlscalar.BigInt("9007199254740993")
	if err != nil {
		t.Fatal(err)
	}
	left := map[string]any{"take": int32(25), "input": map[string]any{"role": "from", "range": map[string]any{"low": fromLiteral, "high": int64(math.MaxInt64)}}}
	right := map[string]any{"after": "different-token", "input": map[string]any{"range": map[string]any{"high": int64(math.MaxInt64), "low": fromVariable}, "role": "from"}, "take": int32(25)}
	baseline := canonicalBytes(t, registry, left, "after")
	if !bytes.Equal(baseline, canonicalBytes(t, registry, right, "after")) {
		t.Fatal("input map order or equivalent coerced literal/variable changed identity")
	}
	if bytes.Equal(canonicalBytes(t, registry, left), canonicalBytes(t, registry, right)) {
		t.Fatal("unexcluded argument was discarded")
	}
	right["take"] = int32(24)
	if bytes.Equal(baseline, canonicalBytes(t, registry, right, "after")) {
		t.Fatal("effective take disappeared")
	}
	right["take"] = int32(25)
	right["input"].(map[string]any)["role"] = "to"
	if bytes.Equal(baseline, canonicalBytes(t, registry, right, "after")) {
		t.Fatal("enum wire identity disappeared")
	}
	request := canonicalPrepare(t, registry, left)
	for _, excluded := range [][]string{{"unknown"}, {"after", "after"}} {
		if _, err := registry.CanonicalArguments(request, excluded...); !errors.Is(err, ErrCanonicalArgumentsInvalid) {
			t.Fatalf("invalid exclusion: %v", err)
		}
	}
	other := canonicalRegistry(t, args, leaf, input)
	if _, err := other.CanonicalArguments(request); !errors.Is(err, ErrCanonicalArgumentsInvalid) {
		t.Fatalf("foreign registry request accepted: %v", err)
	}
	if _, err := registry.CanonicalArguments(Request{}); !errors.Is(err, ErrCanonicalArgumentsInvalid) {
		t.Fatal("unprepared request accepted")
	}
	left["input"].(map[string]any)["role"] = "to"
	again, err := registry.CanonicalArguments(request, "after")
	if err != nil || !bytes.Equal(again, baseline) {
		t.Fatal("prepared request retained caller map aliases")
	}
	again[0] ^= 1
	again, err = registry.CanonicalArguments(request, "after")
	if err != nil || !bytes.Equal(again, baseline) {
		t.Fatal("canonical output aliased later encodings")
	}
}

func TestCanonicalArgumentsPreserveOmissionNullAndListOrder(t *testing.T) {
	element := canonicalScalar("Int", false)
	list := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Nullable: true, Element: &element}
	input := canonicalInput("Patch", compilerir.GraphQLObjectFieldIR{Name: "items", GoName: "Items", Type: list, PresenceAware: true})
	registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "optional", Type: canonicalScalar("String", true)}, {Name: "items", Type: list, PresenceAware: true}, {Name: "patch", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "Patch", Nullable: true}}}, input)
	cases := []map[string]any{{}, {"optional": nil}, {"items": nil}, {"items": []any{}}, {"items": []any{int32(1), int32(2)}}, {"items": []any{int32(2), int32(1)}}, {"patch": map[string]any{}}, {"patch": map[string]any{"items": nil}}, {"patch": map[string]any{"items": []any{}}}}
	seen := map[string]bool{}
	for _, value := range cases {
		encoded := string(canonicalBytes(t, registry, value))
		if seen[encoded] {
			t.Fatal("omission, null, empty list, or list order collided")
		}
		seen[encoded] = true
	}
	omitted := canonicalPrepare(t, registry, nil).Arguments()
	explicit := canonicalPrepare(t, registry, map[string]any{"items": nil}).Arguments()
	if omitted[0].Present() || omitted[1].Present() || !explicit[1].Present() {
		t.Fatal("argument clones lost presence")
	}
	var unset, null golem.InputValue[[]int32]
	if recognized, err := golem.RuntimeDecodeInputValue(&unset, omitted[1].Value(), nil); !recognized || err != nil || unset.IsSet() {
		t.Fatal("omitted InputValue list bridge changed")
	}
	if recognized, err := golem.RuntimeDecodeInputValue(&null, explicit[1].Value(), nil); !recognized || err != nil || !null.IsNull() {
		t.Fatal("null InputValue list bridge changed")
	}
}

func TestCanonicalArgumentsKeepExactScalarValues(t *testing.T) {
	decimal, _ := golem.ParseDecimal("999999999999999.999")
	otherDecimal, _ := golem.ParseDecimal("999999999999999.998")
	date, _ := golem.ParseDate("0001-01-01")
	otherDate, _ := golem.ParseDate("9999-12-31")
	clock, _ := golem.ParseTime("23:59:59.999999")
	tests := []struct {
		name          string
		first, second any
	}{
		{"Boolean", true, false}, {"Int", int32(math.MaxInt32), int32(math.MinInt32)}, {"BigInt", int64(9007199254740993), int64(9007199254740992)},
		{"Float", math.Copysign(0, -1), float64(0)}, {"String", "a\x00b\n", "a\x00b"}, {"Bytes", []byte{0, 1, 255}, []byte{0, 1, 254}},
		{"Decimal", decimal, otherDecimal}, {"UUID", golem.UUID{15: 1}, golem.UUID{15: 2}}, {"Date", date, otherDate}, {"Time", clock, golem.Time{}},
		{"DateTime", time.Date(2026, 11, 14, 0, 0, 0, 999999000, time.UTC), time.Date(2026, 11, 14, 0, 0, 0, 999998000, time.UTC)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: canonicalScalar(test.name, false)}})
			if bytes.Equal(canonicalBytes(t, registry, map[string]any{"value": test.first}), canonicalBytes(t, registry, map[string]any{"value": test.second})) {
				t.Fatal("exact scalar distinction collapsed")
			}
		})
	}
	registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: canonicalScalar("DateTime", false)}})
	utc, _ := graphqlscalar.DateTime("2026-11-14T00:00:00.123456Z")
	offset, _ := graphqlscalar.DateTime("2026-11-14T02:00:00.123456+02:00")
	if !bytes.Equal(canonicalBytes(t, registry, map[string]any{"value": utc}), canonicalBytes(t, registry, map[string]any{"value": offset})) {
		t.Fatal("equivalent normalized instants differ")
	}
	for _, invalid := range []time.Time{time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC)} {
		if _, err := registry.CanonicalArguments(canonicalPrepare(t, registry, map[string]any{"value": invalid})); !errors.Is(err, ErrCanonicalArgumentsInvalid) {
			t.Fatalf("invalid DateTime accepted: %v", err)
		}
	}
}

func TestCanonicalArgumentsRefuseExecutableAndUndefinedEncodings(t *testing.T) {
	for _, kind := range []compilerir.GraphQLTypeKind{compilerir.GraphQLTypePredicate, compilerir.GraphQLTypeSelector, compilerir.GraphQLTypeCreateInput, compilerir.GraphQLTypeUpdateInput, compilerir.GraphQLTypeUpdateManyInput} {
		for _, emptyList := range []bool{false, true} {
			typ := compilerir.GraphQLTypeIR{Kind: kind, Name: "User", Nullable: true}
			value := map[string]any{}
			if emptyList {
				child := typ
				typ = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &child}
				value["value"] = []any{}
			}
			registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: typ}})
			if _, err := registry.CanonicalArguments(canonicalPrepare(t, registry, value)); !errors.Is(err, ErrCanonicalArgumentsUnsupported) {
				t.Fatalf("%s empty=%v admitted: %v", kind, emptyList, err)
			}
		}
	}
	registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: canonicalScalar("JSON", false)}})
	document, err := golem.NewJSONDocument[any]([]byte(`{"exact":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.CanonicalArguments(canonicalPrepare(t, registry, map[string]any{"value": document})); !errors.Is(err, ErrCanonicalArgumentsUnsupported) {
		t.Fatalf("undefined JSON identity admitted: %v", err)
	}
}

func TestCanonicalArgumentsBoundSchemaValueAndOutputWork(t *testing.T) {
	t.Run("bytes", func(t *testing.T) {
		registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: canonicalScalar("String", false)}})
		if _, err := registry.CanonicalArguments(canonicalPrepare(t, registry, map[string]any{"value": strings.Repeat("x", MaxCanonicalArgumentBytes+1)})); !errors.Is(err, ErrCanonicalArgumentsLimit) {
			t.Fatalf("oversize text: %v", err)
		}
	})
	t.Run("list", func(t *testing.T) {
		element := canonicalScalar("Int", false)
		registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &element}}})
		items := make([]any, MaxCanonicalArgumentNodes+1)
		for index := range items {
			items[index] = int32(index)
		}
		if _, err := registry.CanonicalArguments(canonicalPrepare(t, registry, map[string]any{"value": items})); !errors.Is(err, ErrCanonicalArgumentsLimit) {
			t.Fatalf("oversize list: %v", err)
		}
	})
	t.Run("wide empty objects", func(t *testing.T) {
		var fields []compilerir.GraphQLObjectFieldIR
		for index := 0; index < 64; index++ {
			name := string(rune('A'+index/26)) + string(rune('A'+index%26))
			fields = append(fields, compilerir.GraphQLObjectFieldIR{Name: name, GoName: name, Type: canonicalScalar("String", true)})
		}
		element := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeInputObject, Name: "Wide"}
		registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &element}}}, canonicalInput("Wide", fields...))
		items := make([]any, 300)
		for index := range items {
			items[index] = map[string]any{}
		}
		if _, err := registry.CanonicalArguments(canonicalPrepare(t, registry, map[string]any{"value": items})); !errors.Is(err, ErrCanonicalArgumentsLimit) {
			t.Fatalf("unaccounted schema fields per object: %v", err)
		}
	})
	t.Run("depth", func(t *testing.T) {
		typ := canonicalScalar("Int", false)
		var value any = int32(1)
		for index := 0; index < MaxCanonicalArgumentDepth; index++ {
			child := typ
			typ = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &child}
			value = []any{value}
		}
		registry := canonicalRegistry(t, []compilerir.GraphQLArgumentContractIR{{Name: "value", Type: typ}})
		if _, err := registry.CanonicalArguments(canonicalPrepare(t, registry, map[string]any{"value": value})); !errors.Is(err, ErrCanonicalArgumentsLimit) {
			t.Fatalf("unbounded nesting: %v", err)
		}
	})
}
