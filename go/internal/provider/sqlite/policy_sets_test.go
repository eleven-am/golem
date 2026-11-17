package sqlite

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

func TestPolicySQLiteTwoThousandUUIDSetUsesOneBindAndIndexedReadCountUpdate(t *testing.T) {
	ctx := context.Background()
	database, _, err := New().Open(ctx, filepath.Join(t.TempDir(), "scalar-sets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Exec(`CREATE TABLE probe(id TEXT PRIMARY KEY, name TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	resolver := newSQLitePolicyTestResolver(t)
	resolver.intType, err = ir.NewTypeRef(ir.ValueUUID, false, 0, 0, ir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	value := func(number uint32) ir.Value {
		var id [16]byte
		binary.BigEndian.PutUint32(id[12:], number)
		return ir.UUIDValue(id)
	}
	var want []string
	for _, number := range []uint32{1, 2000, 2001} {
		encoded, err := policysql.EncodeOperand(NewPolicyDialect(), resolver, value(number), resolver.intType)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO probe(id,name) VALUES(?,?)`, encoded, "unchanged"); err != nil {
			t.Fatal(err)
		}
		if number <= 2000 {
			want = append(want, encoded.(string))
		}
	}
	values := make([]ir.Value, 2000)
	for i := range values {
		values[i] = value(uint32(i + 1))
	}
	operand, err := ir.ManyOperand(values)
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := operator.ValidateShape(ir.OperatorIn, operator.Shape{Node: ir.ConditionScalar, FieldType: resolver.intType, Operand: operand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := ir.NewScalar(resolver.modelID, resolver.idID, resolver.intType, ir.OperatorIn, ir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := New().PolicyCapabilityProof(ctx, database, resolver.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.fingerprint, RootAlias: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fragment.Args()) != 1 {
		t.Fatalf("2,000 IDs expanded to %d binds; want one typed set", len(fragment.Args()))
	}
	var got []string
	read := `SELECT id FROM probe AS "root" WHERE ` + fragment.SQL() + ` ORDER BY id`
	if err := database.Select(&got, read, fragment.Args()...); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected=%v want=%v", got, want)
	}
	var count int
	if err := database.Get(&count, `SELECT count(*) FROM probe AS "root" WHERE `+fragment.SQL(), fragment.Args()...); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	plan := queryPlan(t, database, read, fragment.Args())
	if !strings.Contains(plan, "SEARCH") || strings.Contains(plan, "SCAN root") {
		t.Fatalf("UUID set lost the indexed search: %s", plan)
	}
	updated, err := database.Exec(`UPDATE probe AS "root" SET name='changed' WHERE `+fragment.SQL(), fragment.Args()...)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := updated.RowsAffected(); err != nil || count != 2 {
		t.Fatalf("updated=%d err=%v", count, err)
	}
	arguments := fragment.Args()
	arguments[0] = "[]"
	values[0] = value(2001)
	if err := database.Get(&count, `SELECT count(*) FROM probe AS "root" WHERE `+fragment.SQL(), fragment.Args()...); err != nil || count != 2 {
		t.Fatalf("bound set was mutable: count=%d err=%v", count, err)
	}
}

type scalarSetResolver struct{ *sqlitePolicyTestResolver }

func (*scalarSetResolver) EnumWire(enum ir.EnumID, member ir.EnumValueID) (string, bool) {
	if enum != (ir.EnumID{1}) || member[0] < 1 || member[0] > 3 {
		return "", false
	}
	return []string{"wire-A", "wire-b", "wire-c"}[member[0]-1], true
}

func TestPolicySQLiteScalarSetsAgreeWithEvaluatorAndExpandedEquality(t *testing.T) {
	must := func(value ir.Value, err error) ir.Value {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	integer := func(kind ir.ValueKind, value int64) ir.Value { return must(ir.SignedValue(kind, value)) }
	text := func(value string) ir.Value { return must(ir.StringValue(value)) }
	cases := []struct {
		name             string
		precision, scale uint16
		values           []ir.Value
	}{
		{"bool", 0, 0, []ir.Value{ir.BoolValue(false), ir.BoolValue(true)}},
		{"int16", 0, 0, []ir.Value{integer(ir.ValueInt16, math.MinInt16), integer(ir.ValueInt16, 0), integer(ir.ValueInt16, math.MaxInt16)}},
		{"int32", 0, 0, []ir.Value{integer(ir.ValueInt32, math.MinInt32), integer(ir.ValueInt32, 0), integer(ir.ValueInt32, math.MaxInt32)}},
		{"int64", 0, 0, []ir.Value{integer(ir.ValueInt64, math.MinInt64), integer(ir.ValueInt64, 9007199254740992), integer(ir.ValueInt64, 9007199254740993), integer(ir.ValueInt64, math.MaxInt64)}},
		{"float32", 0, 0, []ir.Value{must(ir.Float32Value(0)), must(ir.Float32Value(math.SmallestNonzeroFloat32)), must(ir.Float32Value(math.MaxFloat32)), must(ir.Float32Value(0.1))}},
		{"float64", 0, 0, []ir.Value{must(ir.Float64Value(0)), must(ir.Float64Value(math.SmallestNonzeroFloat64)), must(ir.Float64Value(math.MaxFloat64)), must(ir.Float64Value(math.Nextafter(1, 2)))}},
		{"decimal", 18, 4, []ir.Value{must(ir.NewDecimalValue(-999999999999999999, 4)), must(ir.NewDecimalValue(10000, 4)), must(ir.NewDecimalValue(1, 4)), must(ir.NewDecimalValue(999999999999999999, 4))}},
		{"string", 0, 0, []ir.Value{text(""), text("Az"), text("az"), text("ÅZ"), text("åz"), text("a\x00z"), text("quote\"\\<%_")}},
		{"bytes", 0, 0, []ir.Value{ir.BytesValue([]byte{}), ir.BytesValue([]byte{0}), ir.BytesValue([]byte{0, 255}), ir.BytesValue([]byte{255})}},
		{"uuid", 0, 0, []ir.Value{ir.UUIDValue([16]byte{}), ir.UUIDValue([16]byte{1}), ir.UUIDValue([16]byte{255})}},
		{"date", 0, 0, []ir.Value{must(ir.NewDateValue(1, 1, 1)), must(ir.NewDateValue(2026, 11, 13)), must(ir.NewDateValue(9999, 12, 31))}},
		{"time", 6, 0, []ir.Value{must(ir.NewTimeValue(0)), must(ir.NewTimeValue(1000001)), must(ir.NewTimeValue(86399999999))}},
		{"datetime", 6, 0, []ir.Value{must(ir.NewDateTimeValue(-1, 999999000)), must(ir.NewDateTimeValue(0, 0)), must(ir.NewDateTimeValue(1700000000, 123456000))}},
		{"enum", 0, 0, []ir.Value{must(ir.NewEnumValue(ir.EnumID{1}, ir.EnumValueID{1})), must(ir.NewEnumValue(ir.EnumID{1}, ir.EnumValueID{2})), must(ir.NewEnumValue(ir.EnumID{1}, ir.EnumValueID{3}))}},
	}
	random := rand.New(rand.NewSource(71))
	for i := 0; i < 32; i++ {
		value := math.Float64frombits(random.Uint64())
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			cases[5].values = append(cases[5].values, must(ir.Float64Value(value)))
		}
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			database, _, err := New().Open(ctx, filepath.Join(t.TempDir(), "sets.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			// Exercise the affinities of generated columns, rather than ANY,
			// which would bypass SQLite's column comparison conversions.
			storage := "TEXT"
			switch test.values[0].Kind() {
			case ir.ValueBool, ir.ValueInt16, ir.ValueInt32, ir.ValueInt64, ir.ValueDecimal, ir.ValueDateTime:
				storage = "INTEGER"
			case ir.ValueFloat32, ir.ValueFloat64:
				storage = "REAL"
			case ir.ValueBytes:
				storage = "BLOB"
			}
			if _, err := database.Exec(`CREATE TABLE probe(id INTEGER PRIMARY KEY, name ` + storage + `) STRICT`); err != nil {
				t.Fatal(err)
			}
			resolver := &scalarSetResolver{newSQLitePolicyTestResolver(t)}
			enum := ir.EnumID{}
			if test.name == "enum" {
				enum = ir.EnumID{1}
			}
			resolver.textType, err = ir.NewTypeRef(test.values[0].Kind(), true, test.precision, test.scale, enum, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := New().PolicyCapabilityProof(ctx, database, resolver.fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			records := make([]evaluate.Record, len(test.values)+1)
			records[0], err = evaluate.NewRecord(resolver.modelID, evaluate.NullField(resolver.nameID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO probe(id,name) VALUES(0,NULL)`); err != nil {
				t.Fatal(err)
			}
			for i, value := range test.values {
				encoded, err := policysql.EncodeOperand(NewPolicyDialect(), resolver, value, resolver.textType)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`INSERT INTO probe(id,name) VALUES(?,?)`, i+1, encoded); err != nil {
					t.Fatal(err)
				}
				field, err := evaluate.ValueField(resolver.nameID, value)
				if err != nil {
					t.Fatal(err)
				}
				records[i+1], err = evaluate.NewRecord(resolver.modelID, field)
				if err != nil {
					t.Fatal(err)
				}
			}
			compile := func(condition ir.Condition) policysql.Fragment {
				fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.fingerprint, RootAlias: "root"})
				if err != nil {
					t.Fatal(err)
				}
				return fragment
			}
			modes := []ir.ComparisonMode{ir.ComparisonSensitive}
			if test.name == "string" {
				modes = append(modes, ir.ComparisonASCIIInsensitive)
			}
			for _, mode := range modes {
				for setIndex, values := range [][]ir.Value{nil, {test.values[0], test.values[0]}, test.values} {
					for _, operation := range []ir.OperatorID{ir.OperatorIn, ir.OperatorNotIn} {
						t.Run(fmt.Sprintf("mode=%d/set=%d/op=%d", mode, setIndex, operation), func(t *testing.T) {
							build := func(op ir.OperatorID, operand ir.Operand) ir.Condition {
								requirements, err := operator.ValidateShape(op, operator.Shape{Node: ir.ConditionScalar, FieldType: resolver.textType, Operand: operand, Mode: mode, Providers: ir.PortableProviders()})
								if err != nil {
									t.Fatal(err)
								}
								condition, err := ir.NewScalar(resolver.modelID, resolver.nameID, resolver.textType, op, mode, operand, requirements)
								if err != nil {
									t.Fatal(err)
								}
								return condition
							}
							operand, err := ir.ManyOperand(values)
							if err != nil {
								t.Fatal(err)
							}
							condition := build(operation, operand)
							expanded, err := ir.NewConstant(resolver.modelID, false)
							if err != nil {
								t.Fatal(err)
							}
							if len(values) != 0 {
								leaves := make([]ir.Condition, len(values))
								for i, value := range values {
									one, err := ir.OneOperand(value)
									if err != nil {
										t.Fatal(err)
									}
									leaves[i] = build(ir.OperatorEqual, one)
								}
								expanded, err = ir.NewLogical(resolver.modelID, ir.LogicalOr, leaves)
								if err != nil {
									t.Fatal(err)
								}
							}
							if operation == ir.OperatorNotIn {
								expanded, err = ir.NewLogical(resolver.modelID, ir.LogicalNot, []ir.Condition{expanded})
								if err != nil {
									t.Fatal(err)
								}
							}
							var want []int
							for i, record := range records {
								matched, err := evaluate.Condition(condition, record, ir.PortableProviders())
								if err != nil {
									t.Fatal(err)
								}
								if matched {
									want = append(want, i)
								}
							}
							for _, fragment := range []policysql.Fragment{compile(condition), compile(expanded)} {
								var got []int
								if err := database.Select(&got, `SELECT id FROM probe AS "root" WHERE `+fragment.SQL()+` ORDER BY id`, fragment.Args()...); err != nil {
									t.Fatal(err)
								}
								if !reflect.DeepEqual(got, want) {
									t.Fatalf("SQL differs from evaluator: got=%v want=%v SQL=%s", got, want, fragment.SQL())
								}
								var unknown int
								if err := database.Get(&unknown, `SELECT count(*) FROM probe AS "root" WHERE (`+fragment.SQL()+`) IS NULL`, fragment.Args()...); err != nil || unknown != 0 {
									t.Fatalf("SQL UNKNOWN=%d err=%v", unknown, err)
								}
							}
						})
					}
				}
			}
		})
	}
}

func TestPolicySQLitePackedSetPayloadIsBoundedByEncodedBytes(t *testing.T) {
	resolver := newSQLitePolicyTestResolver(t)
	proof, err := policysql.NewCapabilityProof(ir.ProviderSQLite, resolver.fingerprint, ir.CapabilityBinaryText)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, value string
		allowed     bool
	}{
		{"exact", strings.Repeat("x", policysql.MaxScalarSetBytes-4), true},
		{"overflow", strings.Repeat("x", policysql.MaxScalarSetBytes-3), false},
		{"escaped exact", strings.Repeat("<", policysql.MaxScalarSetBytes/6), true},
		{"escaped overflow", strings.Repeat("<", policysql.MaxScalarSetBytes/6+1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := ir.StringValue(test.value)
			if err != nil {
				t.Fatal(err)
			}
			operand, err := ir.ManyOperand([]ir.Value{value})
			if err != nil {
				t.Fatal(err)
			}
			requirements, err := operator.ValidateShape(ir.OperatorIn, operator.Shape{Node: ir.ConditionScalar, FieldType: resolver.textType, Operand: operand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
			if err != nil {
				t.Fatal(err)
			}
			condition, err := ir.NewScalar(resolver.modelID, resolver.nameID, resolver.textType, ir.OperatorIn, ir.ComparisonSensitive, operand, requirements)
			if err != nil {
				t.Fatal(err)
			}
			fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.fingerprint, RootAlias: "root"})
			if test.allowed {
				if err != nil || len(fragment.Args()) != 1 || len(fragment.Args()[0].(string)) != policysql.MaxScalarSetBytes {
					t.Fatalf("exact packed bound: err=%v", err)
				}
				return
			}
			var limit *policysql.ScalarSetLimitError
			if !errors.As(err, &limit) || limit.Maximum != policysql.MaxScalarSetBytes {
				t.Fatalf("unbounded packed set: %v", err)
			}
		})
	}
}
