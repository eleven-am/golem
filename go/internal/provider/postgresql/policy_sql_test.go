package postgresql

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

func TestPolicyDialectSupportInventoryIsClosedAndComplete(t *testing.T) {
	dialect := NewPolicyDialect()
	for _, entry := range policyoperator.Entries() {
		if !dialect.Supports(entry.ID()) {
			t.Errorf("operator %d (%s) is absent", entry.ID(), entry.Name())
		}
	}
	for _, unknown := range []policyir.OperatorID{0, 14, 100, 108, 200, 215, 300, 308, 65535} {
		if dialect.Supports(unknown) {
			t.Errorf("unknown operator %d is supported", unknown)
		}
	}
}

func TestPolicyDialectRendersEveryLeafOperatorAsTwoValuedSQL(t *testing.T) {
	resolver := newPolicyTestResolver("policy_test")
	for _, entry := range policyoperator.Entries() {
		if entry.NodeKind() == policyir.ConditionRelation {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			condition := policyTestCondition(t, entry.ID(), resolver)
			fragment := compilePolicyTest(t, resolver, condition)
			if strings.TrimSpace(fragment.SQL()) == "" {
				t.Fatal("empty SQL")
			}
			if !strings.Contains(fragment.SQL(), "COALESCE(") &&
				!strings.Contains(fragment.SQL(), " IS NULL") && !strings.Contains(fragment.SQL(), " IS NOT NULL") &&
				!strings.Contains(fragment.SQL(), "TRUE") && !strings.Contains(fragment.SQL(), "FALSE") {
				t.Fatalf("leaf has no measurable two-valued construction: %s", fragment.SQL())
			}
		})
	}
}

func TestPolicyDialectReviewedGoldens(t *testing.T) {
	resolver := newPolicyTestResolver("odd-schema")
	tests := []struct {
		name      string
		condition policyir.Condition
		wantSQL   string
		wantArgs  []any
	}{
		{
			name:      "not_exact_complement_nullable",
			condition: policyScalar(t, resolver, policyir.OperatorNotEqual, policyir.ComparisonSensitive, oneString(t, "A%_\\z")),
			wantSQL:   `(NOT (COALESCE(("root"."text" COLLATE "C") = ($1::text COLLATE "C"), FALSE)))`,
			wantArgs:  []any{"A%_\\z"},
		},
		{
			name:      "literal_like_metacharacters_insensitive",
			condition: policyScalar(t, resolver, policyir.OperatorContains, policyir.ComparisonASCIIInsensitive, oneString(t, "%_\\")),
			wantSQL:   `(COALESCE(pg_catalog.strpos((pg_catalog.translate(("root"."text" COLLATE "C"), 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz') COLLATE "C"), (pg_catalog.translate(($1::text COLLATE "C"), 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz') COLLATE "C")) > 0, FALSE))`,
			wantArgs:  []any{"%_\\"},
		},
		{
			name:      "list_has_null_column_unknown_count",
			condition: policyList(t, resolver, policyir.OperatorListHas, oneString(t, "x")),
			wantSQL:   `(COALESCE(pg_catalog.jsonb_typeof("root"."items") = 'array' AND EXISTS (SELECT 1 FROM pg_catalog.jsonb_array_elements("root"."items") AS golem_e(value) WHERE pg_catalog.jsonb_typeof(golem_e.value) = 'string' AND golem_e.value = ($1::jsonb -> 0)), FALSE))`,
			wantArgs:  []any{`["x"]`},
		},
		{
			name:      "json_typed_path",
			condition: policyJSON(t, resolver, policyir.OperatorJSONStringStartsWith, policyir.ComparisonSensitive, oneJSON(t, jsonString(t, "A"))),
			wantSQL:   `(COALESCE(pg_catalog.jsonb_typeof(("root"."document" #> ARRAY[$1::text, $2::text]::text[])) = 'string' AND pg_catalog.left(((("root"."document" #> ARRAY[$1::text, $2::text]::text[]) #>> '{}') COLLATE "C"), pg_catalog.char_length((($3::jsonb #>> '{}') COLLATE "C"))) = (($3::jsonb #>> '{}') COLLATE "C"), FALSE))`,
			wantArgs:  []any{"a.b", "1", `"A"`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fragment := compilePolicyTest(t, resolver, test.condition)
			if fragment.SQL() != test.wantSQL {
				t.Fatalf("SQL:\n%s\nwant:\n%s", fragment.SQL(), test.wantSQL)
			}
			if !reflect.DeepEqual(fragment.Args(), test.wantArgs) {
				t.Fatalf("args=%#v want=%#v", fragment.Args(), test.wantArgs)
			}
		})
	}
}

func TestPolicyPostgreSQLM12InsensitiveOrderingPinsInnerBinaryCollation(t *testing.T) {
	resolver := newPolicyTestResolver("policy_m12")
	fragment := compilePolicyTest(t, resolver, policyScalar(t, resolver, policyir.OperatorGreaterThan, policyir.ComparisonASCIIInsensitive, oneString(t, "école")))
	if !strings.Contains(fragment.SQL(), `pg_catalog.translate(("root"."text" COLLATE "C"),`) {
		t.Fatalf("column is folded before its inner binary collation: %s", fragment.SQL())
	}
	if !strings.Contains(fragment.SQL(), `pg_catalog.translate(($1::text COLLATE "C"),`) {
		t.Fatalf("operand is folded before its inner binary collation: %s", fragment.SQL())
	}
	if strings.Count(fragment.SQL(), `COLLATE "C"`) != 4 || !strings.Contains(fragment.SQL(), " > ") {
		t.Fatalf("insensitive ordering lost its inner/outer binary-collation fence: %s", fragment.SQL())
	}
	if !reflect.DeepEqual(fragment.Args(), []any{"école"}) {
		t.Fatalf("args=%#v", fragment.Args())
	}
}

func TestPolicyCodecCoversEveryPhysicalValueFamily(t *testing.T) {
	dialect := NewPolicyDialect()
	typeCase := func(kind policyir.ValueKind, precision, scale uint16, element *policyir.TypeRef) policyir.TypeRef {
		typ, err := policyir.NewTypeRef(kind, false, precision, scale, policyir.EnumID{}, element, func() policyir.Capability {
			if kind == policyir.ValueScalarList {
				return policyir.CapabilityScalarListJSON
			}
			return 0
		}())
		if err != nil {
			t.Fatal(err)
		}
		return typ
	}
	intValue, _ := policyir.SignedValue(policyir.ValueInt64, 9007199254740993)
	floatValue, _ := policyir.Float64Value(math.SmallestNonzeroFloat64)
	decimalValue, _ := policyir.NewDecimalValue(-123, 2)
	textValue, _ := policyir.StringValue("Å\x00z")
	uuidValue := policyir.UUIDValue([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	dateValue, _ := policyir.NewDateValue(2026, 8, 5)
	timeValue, _ := policyir.NewTimeValue(45_296_123_456)
	instantValue, _ := policyir.NewDateTimeValue(1_722_858_896, 123456000)
	jsonNumber, _ := policyir.NewJSONNumber(false, []byte("9007199254740993"), 0)
	jsonValue, _ := policyir.JSONNumberValueOf(jsonNumber)
	wrappedJSON, _ := policyir.NewJSONValue(jsonValue)
	stringElement := typeCase(policyir.ValueString, 0, 0, nil)
	listValue, _ := policyir.NewListValue([]policyir.Value{textValue})
	tests := []struct {
		name  string
		bound policysql.BoundValue
		want  any
	}{
		{"bool", policysql.BoundValue{Value: policyir.BoolValue(true), Type: typeCase(policyir.ValueBool, 0, 0, nil)}, true},
		{"int64", policysql.BoundValue{Value: intValue, Type: typeCase(policyir.ValueInt64, 0, 0, nil)}, int64(9007199254740993)},
		{"float64", policysql.BoundValue{Value: floatValue, Type: typeCase(policyir.ValueFloat64, 0, 0, nil)}, math.SmallestNonzeroFloat64},
		{"decimal", policysql.BoundValue{Value: decimalValue, Type: typeCase(policyir.ValueDecimal, 18, 4, nil)}, "-1.2300"},
		{"string", policysql.BoundValue{Value: textValue, Type: typeCase(policyir.ValueString, 0, 0, nil)}, "Å\x00z"},
		{"uuid", policysql.BoundValue{Value: uuidValue, Type: typeCase(policyir.ValueUUID, 0, 0, nil)}, "01020304-0506-0708-090a-0b0c0d0e0f10"},
		{"date", policysql.BoundValue{Value: dateValue, Type: typeCase(policyir.ValueDate, 0, 0, nil)}, "2026-08-05"},
		{"time", policysql.BoundValue{Value: timeValue, Type: typeCase(policyir.ValueTime, 6, 0, nil)}, "12:34:56.123456"},
		{"datetime", policysql.BoundValue{Value: instantValue, Type: typeCase(policyir.ValueDateTime, 6, 0, nil)}, nil},
		{"json", policysql.BoundValue{Value: wrappedJSON, Type: typeCase(policyir.ValueJSON, 0, 0, nil)}, "9007199254740993"},
		{"list", policysql.BoundValue{Value: listValue, Type: typeCase(policyir.ValueScalarList, 0, 0, &stringElement)}, `["Å\u0000z"]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := dialect.Encode(test.bound)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "datetime" {
				if !reflect.ValueOf(got).IsValid() || reflect.TypeOf(got).String() != "time.Time" {
					t.Fatalf("datetime codec type=%T", got)
				}
				return
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got=%#v want=%#v", got, test.want)
			}
		})
	}
}

func TestPolicyCodecRejectsJSONOutsidePostgreSQLJSONBNumericRange(t *testing.T) {
	number, _ := policyir.NewJSONNumber(false, []byte("1"), 131072)
	jsonValue, _ := policyir.JSONNumberValueOf(number)
	value, _ := policyir.NewJSONValue(jsonValue)
	typ, _ := policyir.NewTypeRef(policyir.ValueJSON, false, 0, 0, policyir.EnumID{}, nil, 0)
	if _, err := NewPolicyDialect().Encode(policysql.BoundValue{Value: value, Type: typ}); err == nil {
		t.Fatal("out-of-range jsonb number accepted")
	}
}

func TestPolicyScalarCastAndCodecInventory(t *testing.T) {
	resolver := newPolicyTestResolver("policy_cast")
	typeRef := func(kind policyir.ValueKind, precision, scale uint16, enum policyir.EnumID) policyir.TypeRef {
		typ, err := policyir.NewTypeRef(kind, true, precision, scale, enum, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return typ
	}
	int16Value, _ := policyir.SignedValue(policyir.ValueInt16, -32768)
	int32Value, _ := policyir.SignedValue(policyir.ValueInt32, 2147483647)
	int64Value, _ := policyir.SignedValue(policyir.ValueInt64, 9007199254740993)
	float32Value, _ := policyir.Float32Value(0.1)
	float64Value, _ := policyir.Float64Value(0.1)
	decimalValue, _ := policyir.NewDecimalValue(123, 2)
	stringValue, _ := policyir.StringValue("x")
	uuidValue := policyir.UUIDValue([16]byte{1})
	dateValue, _ := policyir.NewDateValue(2026, 8, 5)
	timeValue, _ := policyir.NewTimeValue(1_000_001)
	datetimeValue, _ := policyir.NewDateTimeValue(1_700_000_000, 123456000)
	enumID, enumMember := policyir.EnumID{9}, policyir.EnumValueID{10}
	enumValue, _ := policyir.NewEnumValue(enumID, enumMember)
	tests := []struct {
		name  string
		typ   policyir.TypeRef
		value policyir.Value
		cast  string
	}{
		{"bool", typeRef(policyir.ValueBool, 0, 0, policyir.EnumID{}), policyir.BoolValue(true), "$1::boolean"},
		{"int16", typeRef(policyir.ValueInt16, 0, 0, policyir.EnumID{}), int16Value, "$1::smallint"},
		{"int32", typeRef(policyir.ValueInt32, 0, 0, policyir.EnumID{}), int32Value, "$1::integer"},
		{"int64", typeRef(policyir.ValueInt64, 0, 0, policyir.EnumID{}), int64Value, "$1::bigint"},
		{"float32", typeRef(policyir.ValueFloat32, 0, 0, policyir.EnumID{}), float32Value, "$1::real"},
		{"float64", typeRef(policyir.ValueFloat64, 0, 0, policyir.EnumID{}), float64Value, "$1::double precision"},
		{"decimal", typeRef(policyir.ValueDecimal, 18, 4, policyir.EnumID{}), decimalValue, "$1::numeric(18,4)"},
		{"string", typeRef(policyir.ValueString, 0, 0, policyir.EnumID{}), stringValue, "$1::text"},
		{"bytes", typeRef(policyir.ValueBytes, 0, 0, policyir.EnumID{}), policyir.BytesValue([]byte{0, 255}), "$1::bytea"},
		{"uuid", typeRef(policyir.ValueUUID, 0, 0, policyir.EnumID{}), uuidValue, "$1::uuid"},
		{"date", typeRef(policyir.ValueDate, 0, 0, policyir.EnumID{}), dateValue, "$1::date"},
		{"time", typeRef(policyir.ValueTime, 6, 0, policyir.EnumID{}), timeValue, "$1::time(6) without time zone"},
		{"datetime", typeRef(policyir.ValueDateTime, 6, 0, policyir.EnumID{}), datetimeValue, "$1::timestamp(6) with time zone"},
		{"enum", typeRef(policyir.ValueEnum, 0, 0, enumID), enumValue, "$1::text"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			field := policysql.Field{Model: policyModel, ID: policyText, Column: "text", Type: test.typ, Nullable: true}
			resolver.fields[policyFieldKey{policyModel, policyText}] = field
			operand, err := policyir.OneOperand(test.value)
			if err != nil {
				t.Fatal(err)
			}
			requirements, err := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: test.typ, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: resolver.Providers()})
			if err != nil {
				t.Fatal(err)
			}
			condition, err := policyir.NewScalar(policyModel, policyText, test.typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
			if err != nil {
				t.Fatal(err)
			}
			fragment := compilePolicyTest(t, resolver, condition)
			if !strings.Contains(fragment.SQL(), test.cast) || len(fragment.Args()) != 1 {
				t.Fatalf("cast/codec not exercised: SQL=%s args=%#v", fragment.SQL(), fragment.Args())
			}
		})
	}
}

func TestPolicyListCodecInventoryProducesTypedJSONArray(t *testing.T) {
	intValue, _ := policyir.SignedValue(policyir.ValueInt64, 9007199254740993)
	floatValue, _ := policyir.Float64Value(0.1)
	decimalValue, _ := policyir.NewDecimalValue(123, 2)
	stringValue, _ := policyir.StringValue("Å")
	uuidValue := policyir.UUIDValue([16]byte{1})
	dateValue, _ := policyir.NewDateValue(2026, 8, 5)
	timeValue, _ := policyir.NewTimeValue(1_000_001)
	datetimeValue, _ := policyir.NewDateTimeValue(1_700_000_000, 123456000)
	enumID, enumMember := policyir.EnumID{9}, policyir.EnumValueID{10}
	enumValue, _ := policyir.NewEnumValue(enumID, enumMember)
	tests := []struct {
		name             string
		value            policyir.Value
		precision, scale uint16
		enum             policyir.EnumID
		wire             string
	}{
		{"bool", policyir.BoolValue(true), 0, 0, policyir.EnumID{}, ""},
		{"int", intValue, 0, 0, policyir.EnumID{}, ""},
		{"float", floatValue, 0, 0, policyir.EnumID{}, ""},
		{"decimal", decimalValue, 18, 4, policyir.EnumID{}, ""},
		{"string", stringValue, 0, 0, policyir.EnumID{}, ""},
		{"uuid", uuidValue, 0, 0, policyir.EnumID{}, ""},
		{"date", dateValue, 0, 0, policyir.EnumID{}, ""},
		{"time", timeValue, 6, 0, policyir.EnumID{}, ""},
		{"datetime", datetimeValue, 6, 0, policyir.EnumID{}, ""},
		{"enum", enumValue, 0, 0, enumID, "wire"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			element, err := policyir.NewTypeRef(test.value.Kind(), false, test.precision, test.scale, test.enum, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			listType, err := policyir.NewTypeRef(policyir.ValueScalarList, false, 0, 0, policyir.EnumID{}, &element, policyir.CapabilityScalarListJSON)
			if err != nil {
				t.Fatal(err)
			}
			list, err := policyir.NewListValue([]policyir.Value{test.value})
			if err != nil {
				t.Fatal(err)
			}
			wires := []string(nil)
			if test.wire != "" {
				wires = []string{test.wire}
			}
			encoded, err := NewPolicyDialect().Encode(policysql.BoundValue{Value: list, Type: listType, EnumWires: wires})
			if err != nil {
				t.Fatal(err)
			}
			text, ok := encoded.(string)
			if !ok || !json.Valid([]byte(text)) || !strings.HasPrefix(text, "[") {
				t.Fatalf("invalid encoded list: %#v", encoded)
			}
		})
	}
}

func TestPolicyJSONCodecCanonicalKindsAndSentinels(t *testing.T) {
	number, _ := policyir.NewJSONNumber(true, []byte("123"), -2)
	numberValue, _ := policyir.JSONNumberValueOf(number)
	stringValue := jsonString(t, "x")
	arrayValue, _ := policyir.JSONArrayValue([]policyir.JSONValue{policyir.JSONNullValue(), numberValue})
	member, _ := policyir.NewJSONMember("z", stringValue)
	objectValue, _ := policyir.JSONObjectValue([]policyir.JSONMember{member})
	values := []policyir.JSONValue{policyir.JSONNullValue(), policyir.JSONBoolValue(true), numberValue, stringValue, arrayValue, objectValue}
	typ, _ := policyir.NewTypeRef(policyir.ValueJSON, true, 0, 0, policyir.EnumID{}, nil, 0)
	for _, value := range values {
		wrapped, _ := policyir.NewJSONValue(value)
		encoded, err := NewPolicyDialect().Encode(policysql.BoundValue{Value: wrapped, Type: typ})
		if err != nil {
			t.Fatal(err)
		}
		if text, ok := encoded.(string); !ok || !json.Valid([]byte(text)) {
			t.Fatalf("kind %d encoded as %#v", value.Kind(), encoded)
		}
	}
	resolver := newPolicyTestResolver("policy_json_null")
	for _, nullKind := range []policyir.JSONNullKind{policyir.JSONDbNull, policyir.JSONDocumentNull, policyir.JSONAnyNull} {
		operand, _ := policyir.JSONNullOperand(nullKind)
		for _, operatorID := range []policyir.OperatorID{policyir.OperatorJSONEqual, policyir.OperatorJSONNotEqual} {
			fragment := compilePolicyTest(t, resolver, policyJSON(t, resolver, operatorID, policyir.ComparisonSensitive, operand))
			if len(fragment.Args()) != 2 || strings.TrimSpace(fragment.SQL()) == "" {
				t.Fatalf("sentinel %d operator %d: SQL=%s args=%#v", nullKind, operatorID, fragment.SQL(), fragment.Args())
			}
		}
	}
}

func TestPostgreSQLPolicyCapabilitiesAreManifestedAndFingerprintBound(t *testing.T) {
	manifest := New().Manifest()
	want := map[string]bool{
		CapabilityPolicyBinaryText: false, CapabilityPolicyASCIIInsensitive: false, CapabilityPolicyExactJSON: false,
		CapabilityPolicyScalarListJSON: false, CapabilityPolicyRelationCorrelation: false, string(CapabilityAnalyticsExact): false,
	}
	for _, fact := range manifest.Capabilities {
		if _, ok := want[string(fact.ID)]; ok {
			want[string(fact.ID)] = fact.Verification == physical.VerificationRuntimeProbe
		}
	}
	for capability, proved := range want {
		if !proved {
			t.Errorf("runtime manifest fact missing: %s", capability)
		}
	}
	fingerprint := [32]byte{9}
	report := CapabilityReport{Version: physical.Version{Major: 15}, JSONB: true, BinaryText: true, ASCIIInsensitive: true, ExactJSON: true, ScalarListJSON: true, RelationCorrelation: true, AnalyticsExact: true}
	proof, err := New().policyCapabilityProof(fingerprint, report)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Provider() != policyir.ProviderPostgreSQL || proof.SchemaFingerprint() != fingerprint {
		t.Fatalf("proof is not fingerprint-bound: provider=%d fingerprint=%x", proof.Provider(), proof.SchemaFingerprint())
	}
	report.ExactJSON = false
	if _, err := New().policyCapabilityProof(fingerprint, report); err == nil {
		t.Fatal("incomplete runtime report produced proof")
	}
	report.ExactJSON = true
	report.AnalyticsExact = false
	if _, err := New().policyCapabilityProof(fingerprint, report); err == nil {
		t.Fatal("analytics-exact capability loss produced an application proof")
	}
}

func TestPostgreSQLPolicyNamedMutationsLive(t *testing.T) {
	dsn := os.Getenv("GOLEM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOLEM_TEST_POSTGRES_DSN is not configured")
	}
	ctx := context.Background()
	database, _, err := New().Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	namespace := physical.PhysicalName("golem_p2_pg_" + hex.EncodeToString(suffix[:]))
	if _, err := database.ExecContext(ctx, "CREATE SCHEMA "+quote(namespace)); err != nil {
		t.Fatal(err)
	}
	defer database.ExecContext(ctx, "DROP SCHEMA "+quote(namespace)+" CASCADE")
	if _, err := database.ExecContext(ctx, "CREATE TABLE "+qualified(namespace, "probe")+` (id bigint PRIMARY KEY, text_value text, int_value bigint, items jsonb, document jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO "+qualified(namespace, "probe")+` (id,text_value,int_value,items,document) VALUES
		(1,NULL,NULL,NULL,NULL),
		(2,'A%_\\z',9007199254740992,'["x"]'::jsonb,'{"a.b":[0,"Ab%_"]}'::jsonb),
		(3,'a%_\\z',9007199254740993,'["x",null]'::jsonb,'{"a.b":[0,null]}'::jsonb),
		(4,'Å',0,'[1]'::jsonb,'null'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	resolver := newPolicyTestResolver(string(namespace))
	resolver.models[policyModel] = policysql.Model{ID: policyModel, Namespace: namespace, Table: "probe"}
	textField := resolver.fields[policyFieldKey{policyModel, policyText}]
	textField.Column = "text_value"
	resolver.fields[policyFieldKey{policyModel, policyText}] = textField
	proof, err := New().PolicyCapabilityProof(ctx, database, resolver.SchemaFingerprint())
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		condition policyir.Condition
	}{
		{"equals_null_safe_unknown_count", policyScalar(t, resolver, policyir.OperatorEqual, policyir.ComparisonSensitive, oneString(t, "A%_\\z"))},
		{"not_exact_complement_nullable", policyScalar(t, resolver, policyir.OperatorNotEqual, policyir.ComparisonSensitive, oneString(t, "A%_\\z"))},
		{"adjacent_integer_above_2pow53", policyInt(t, resolver, policyir.OperatorEqual, 9007199254740993)},
		{"literal_like_metacharacters_insensitive", policyScalar(t, resolver, policyir.OperatorContains, policyir.ComparisonASCIIInsensitive, oneString(t, "%_\\"))},
		{"non_ascii_uppercase_is_not_folded", policyScalar(t, resolver, policyir.OperatorEqual, policyir.ComparisonASCIIInsensitive, oneString(t, "å"))},
		{"list_has_null_column_unknown_count", policyList(t, resolver, policyir.OperatorListHas, oneString(t, "x"))},
		{"json_unknown_count_zero", policyJSON(t, resolver, policyir.OperatorJSONStringContains, policyir.ComparisonSensitive, oneJSON(t, jsonString(t, "%_")))},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			fragment, err := policysql.Compile(policysql.Request{Condition: check.condition, Provider: policyir.ProviderPostgreSQL, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: "root"})
			if err != nil {
				t.Fatal(err)
			}
			from := " FROM " + qualified(namespace, "probe") + ` AS "root"`
			var unknown int
			if err := database.GetContext(ctx, &unknown, "SELECT count(*)"+from+" WHERE ("+fragment.SQL()+") IS NULL", fragment.Args()...); err != nil {
				t.Fatal(err)
			}
			if unknown != 0 {
				t.Fatalf("unknown count=%d SQL=%s", unknown, fragment.SQL())
			}
			var left, right []int64
			if err := database.SelectContext(ctx, &left, "SELECT id"+from+" WHERE ("+fragment.SQL()+") IS NOT TRUE ORDER BY id", fragment.Args()...); err != nil {
				t.Fatal(err)
			}
			if err := database.SelectContext(ctx, &right, "SELECT id"+from+" WHERE NOT ("+fragment.SQL()+") ORDER BY id", fragment.Args()...); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(left, right) {
				t.Fatalf("polarity mismatch: IS NOT TRUE=%v NOT=%v", left, right)
			}
		})
	}
}

var (
	policyModel    = policyir.ModelID{1}
	policyText     = policyir.FieldID{2}
	policyItems    = policyir.FieldID{3}
	policyDocument = policyir.FieldID{4}
	policyInteger  = policyir.FieldID{5}
)

type policyFieldKey struct {
	model policyir.ModelID
	field policyir.FieldID
}
type policyTestResolver struct {
	models map[policyir.ModelID]policysql.Model
	fields map[policyFieldKey]policysql.Field
}

func newPolicyTestResolver(namespace string) *policyTestResolver {
	stringType, _ := policyir.NewTypeRef(policyir.ValueString, true, 0, 0, policyir.EnumID{}, nil, 0)
	element, _ := policyir.NewTypeRef(policyir.ValueString, false, 0, 0, policyir.EnumID{}, nil, 0)
	listType, _ := policyir.NewTypeRef(policyir.ValueScalarList, true, 0, 0, policyir.EnumID{}, &element, policyir.CapabilityScalarListJSON)
	jsonType, _ := policyir.NewTypeRef(policyir.ValueJSON, true, 0, 0, policyir.EnumID{}, nil, 0)
	intType, _ := policyir.NewTypeRef(policyir.ValueInt64, true, 0, 0, policyir.EnumID{}, nil, 0)
	resolver := &policyTestResolver{models: map[policyir.ModelID]policysql.Model{policyModel: {ID: policyModel, Namespace: physical.PhysicalName(namespace), Table: "probe"}}, fields: map[policyFieldKey]policysql.Field{}}
	resolver.fields[policyFieldKey{policyModel, policyText}] = policysql.Field{Model: policyModel, ID: policyText, Column: "text", Type: stringType, Nullable: true}
	resolver.fields[policyFieldKey{policyModel, policyItems}] = policysql.Field{Model: policyModel, ID: policyItems, Column: "items", Type: listType, Nullable: true}
	resolver.fields[policyFieldKey{policyModel, policyDocument}] = policysql.Field{Model: policyModel, ID: policyDocument, Column: "document", Type: jsonType, Nullable: true}
	resolver.fields[policyFieldKey{policyModel, policyInteger}] = policysql.Field{Model: policyModel, ID: policyInteger, Column: "int_value", Type: intType, Nullable: true}
	return resolver
}

func (*policyTestResolver) Providers() policyir.ProviderSet { return policyir.PortableProviders() }
func (*policyTestResolver) SchemaFingerprint() [32]byte     { return [32]byte{1, 9} }
func (resolver *policyTestResolver) Model(_ policyir.Provider, model policyir.ModelID) (policysql.Model, bool) {
	value, ok := resolver.models[model]
	return value, ok
}
func (resolver *policyTestResolver) Field(_ policyir.Provider, model policyir.ModelID, field policyir.FieldID) (policysql.Field, bool) {
	value, ok := resolver.fields[policyFieldKey{model, field}]
	return value, ok
}
func (*policyTestResolver) Relation(policyir.ModelID, policyir.FieldID, policyir.RelationID) (policysql.Relation, bool) {
	return policysql.Relation{}, false
}
func (*policyTestResolver) EnumWire(policyir.EnumID, policyir.EnumValueID) (string, bool) {
	return "wire", true
}
func (*policyTestResolver) Capability(policyir.Provider, policyir.Capability) bool { return true }

func compilePolicyTest(t *testing.T, resolver *policyTestResolver, condition policyir.Condition) policysql.Fragment {
	t.Helper()
	proof, err := policysql.NewCapabilityProof(policyir.ProviderPostgreSQL, resolver.SchemaFingerprint(), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: policyir.ProviderPostgreSQL, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: "root"})
	if err != nil {
		t.Fatal(err)
	}
	return fragment
}

func policyTestCondition(t *testing.T, operatorID policyir.OperatorID, resolver *policyTestResolver) policyir.Condition {
	t.Helper()
	switch {
	case operatorID >= policyir.OperatorEqual && operatorID <= policyir.OperatorIsNotNull:
		operand := oneString(t, "x")
		if operatorID == policyir.OperatorIn || operatorID == policyir.OperatorNotIn {
			value, _ := policyir.StringValue("x")
			operand, _ = policyir.ManyOperand([]policyir.Value{value})
		} else if operatorID == policyir.OperatorIsNull || operatorID == policyir.OperatorIsNotNull {
			operand = policyir.NoOperand()
		}
		return policyScalar(t, resolver, operatorID, policyir.ComparisonSensitive, operand)
	case operatorID >= policyir.OperatorListEqual && operatorID <= policyir.OperatorListIsNotNull:
		operand := oneString(t, "x")
		if operatorID == policyir.OperatorListEqual {
			value, _ := policyir.StringValue("x")
			list, _ := policyir.NewListValue([]policyir.Value{value})
			operand, _ = policyir.OneOperand(list)
		} else if operatorID == policyir.OperatorListHasEvery || operatorID == policyir.OperatorListHasSome {
			value, _ := policyir.StringValue("x")
			operand, _ = policyir.ManyOperand([]policyir.Value{value})
		} else if operatorID == policyir.OperatorListIsEmpty {
			operand = policyir.FlagOperand(true)
		} else if operatorID == policyir.OperatorListIsNull || operatorID == policyir.OperatorListIsNotNull {
			operand = policyir.NoOperand()
		}
		return policyList(t, resolver, operatorID, operand)
	default:
		operand := oneJSON(t, jsonString(t, "x"))
		if operatorID == policyir.OperatorJSONIsNull || operatorID == policyir.OperatorJSONIsNotNull {
			operand = policyir.NoOperand()
		} else if operatorID >= policyir.OperatorJSONLessThan && operatorID <= policyir.OperatorJSONGreaterThanOrEqual {
			number, _ := policyir.NewJSONNumber(false, []byte("1"), 0)
			value, _ := policyir.JSONNumberValueOf(number)
			operand = oneJSON(t, value)
		}
		return policyJSON(t, resolver, operatorID, policyir.ComparisonSensitive, operand)
	}
}

func policyScalar(t *testing.T, resolver *policyTestResolver, operatorID policyir.OperatorID, mode policyir.ComparisonMode, operand policyir.Operand) policyir.Condition {
	t.Helper()
	field := resolver.fields[policyFieldKey{policyModel, policyText}]
	requirements, err := policyoperator.ValidateShape(operatorID, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: field.Type, Operand: operand, Mode: mode, Providers: resolver.Providers()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewScalar(policyModel, policyText, field.Type, operatorID, mode, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func policyInt(t *testing.T, resolver *policyTestResolver, operatorID policyir.OperatorID, value int64) policyir.Condition {
	t.Helper()
	field := resolver.fields[policyFieldKey{policyModel, policyInteger}]
	wanted, _ := policyir.SignedValue(policyir.ValueInt64, value)
	operand, _ := policyir.OneOperand(wanted)
	requirements, err := policyoperator.ValidateShape(operatorID, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: field.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: resolver.Providers()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewScalar(policyModel, policyInteger, field.Type, operatorID, policyir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func policyList(t *testing.T, resolver *policyTestResolver, operatorID policyir.OperatorID, operand policyir.Operand) policyir.Condition {
	t.Helper()
	field := resolver.fields[policyFieldKey{policyModel, policyItems}]
	requirements, err := policyoperator.ValidateShape(operatorID, policyoperator.Shape{Node: policyir.ConditionList, FieldType: field.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: resolver.Providers()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewList(policyModel, policyItems, field.Type, operatorID, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func policyJSON(t *testing.T, resolver *policyTestResolver, operatorID policyir.OperatorID, mode policyir.ComparisonMode, operand policyir.Operand) policyir.Condition {
	t.Helper()
	field := resolver.fields[policyFieldKey{policyModel, policyDocument}]
	key, _ := policyir.JSONKeySegment("a.b")
	path, _ := policyir.NewJSONPath(key, policyir.JSONIndexSegment(1))
	if operatorID == policyir.OperatorJSONIsNull || operatorID == policyir.OperatorJSONIsNotNull {
		path, _ = policyir.NewJSONPath()
	}
	requirements, err := policyoperator.ValidateShape(operatorID, policyoperator.Shape{Node: policyir.ConditionJSON, FieldType: field.Type, Operand: operand, Mode: mode, Path: path, Providers: resolver.Providers()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewJSON(policyModel, policyDocument, field.Type, operatorID, mode, path, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func oneString(t *testing.T, value string) policyir.Operand {
	t.Helper()
	wanted, err := policyir.StringValue(value)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := policyir.OneOperand(wanted)
	if err != nil {
		t.Fatal(err)
	}
	return operand
}

func jsonString(t *testing.T, value string) policyir.JSONValue {
	t.Helper()
	wanted, err := policyir.JSONStringValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return wanted
}

func oneJSON(t *testing.T, value policyir.JSONValue) policyir.Operand {
	t.Helper()
	wrapper, err := policyir.NewJSONValue(value)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := policyir.OneOperand(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	return operand
}
