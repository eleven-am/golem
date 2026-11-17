package postgresql

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/eleven-am/golem/go/internal/testenv"
)

type scalarSetTestResolver struct{ *policyTestResolver }

func (*scalarSetTestResolver) EnumWire(enum policyir.EnumID, member policyir.EnumValueID) (string, bool) {
	if enum != (policyir.EnumID{1}) || member[0] < 1 || member[0] > 3 {
		return "", false
	}
	return []string{"wire-A", "wire-b", "wire-c"}[member[0]-1], true
}

// This gate requires a disposable PostgreSQL database. Compiler-only checks
// cannot establish the jsonb -> typed array casts' execution semantics.
func TestPolicyPostgreSQLScalarSetsAgreeWithEvaluatorAndExpandedEqualityLive(t *testing.T) {
	ctx := context.Background()
	database, _, err := New().Open(ctx, testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	// Temporary tables and the renderer must use the same database session.
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	must := func(value policyir.Value, err error) policyir.Value {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	integer := func(kind policyir.ValueKind, value int64) policyir.Value {
		return must(policyir.SignedValue(kind, value))
	}
	text := func(value string) policyir.Value { return must(policyir.StringValue(value)) }
	cases := []struct {
		name, columnType string
		precision, scale uint16
		values           []policyir.Value
	}{
		{"bool", "boolean", 0, 0, []policyir.Value{policyir.BoolValue(false), policyir.BoolValue(true)}},
		{"int16", "smallint", 0, 0, []policyir.Value{integer(policyir.ValueInt16, math.MinInt16), integer(policyir.ValueInt16, 0), integer(policyir.ValueInt16, math.MaxInt16)}},
		{"int32", "integer", 0, 0, []policyir.Value{integer(policyir.ValueInt32, math.MinInt32), integer(policyir.ValueInt32, 0), integer(policyir.ValueInt32, math.MaxInt32)}},
		{"int64", "bigint", 0, 0, []policyir.Value{integer(policyir.ValueInt64, math.MinInt64), integer(policyir.ValueInt64, 9007199254740992), integer(policyir.ValueInt64, 9007199254740993), integer(policyir.ValueInt64, math.MaxInt64)}},
		{"float32", "real", 0, 0, []policyir.Value{must(policyir.Float32Value(0)), must(policyir.Float32Value(math.SmallestNonzeroFloat32)), must(policyir.Float32Value(math.MaxFloat32)), must(policyir.Float32Value(0.1))}},
		{"float64", "double precision", 0, 0, []policyir.Value{must(policyir.Float64Value(0)), must(policyir.Float64Value(math.SmallestNonzeroFloat64)), must(policyir.Float64Value(math.MaxFloat64)), must(policyir.Float64Value(math.Nextafter(1, 2)))}},
		{"decimal", "numeric(18,4)", 18, 4, []policyir.Value{must(policyir.NewDecimalValue(-999999999999999999, 4)), must(policyir.NewDecimalValue(10000, 4)), must(policyir.NewDecimalValue(1, 4)), must(policyir.NewDecimalValue(999999999999999999, 4))}},
		{"string", "text", 0, 0, []policyir.Value{text(""), text("Az"), text("az"), text("ÅZ"), text("åz"), text("quote\"\\<%_")}},
		{"bytes", "bytea", 0, 0, []policyir.Value{policyir.BytesValue([]byte{}), policyir.BytesValue([]byte{0}), policyir.BytesValue([]byte{0, 255}), policyir.BytesValue([]byte{255})}},
		{"uuid", "uuid", 0, 0, []policyir.Value{policyir.UUIDValue([16]byte{}), policyir.UUIDValue([16]byte{1}), policyir.UUIDValue([16]byte{255})}},
		{"date", "date", 0, 0, []policyir.Value{must(policyir.NewDateValue(1, 1, 1)), must(policyir.NewDateValue(2026, 11, 13)), must(policyir.NewDateValue(9999, 12, 31))}},
		{"time", "time(6) without time zone", 6, 0, []policyir.Value{must(policyir.NewTimeValue(0)), must(policyir.NewTimeValue(1000001)), must(policyir.NewTimeValue(86399999999))}},
		{"datetime", "timestamp(6) with time zone", 6, 0, []policyir.Value{must(policyir.NewDateTimeValue(-1, 999999000)), must(policyir.NewDateTimeValue(0, 0)), must(policyir.NewDateTimeValue(1700000000, 123456000))}},
		{"enum", "text", 0, 0, []policyir.Value{must(policyir.NewEnumValue(policyir.EnumID{1}, policyir.EnumValueID{1})), must(policyir.NewEnumValue(policyir.EnumID{1}, policyir.EnumValueID{2})), must(policyir.NewEnumValue(policyir.EnumID{1}, policyir.EnumValueID{3}))}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := database.ExecContext(ctx, `CREATE TEMP TABLE probe(id integer PRIMARY KEY, text `+test.columnType+`)`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := database.ExecContext(ctx, `DROP TABLE pg_temp.probe`); err != nil {
					t.Error(err)
				}
			})
			resolver := &scalarSetTestResolver{newPolicyTestResolver("pg_temp")}
			resolver.models[policyModel] = policysql.Model{ID: policyModel, Namespace: physical.PhysicalName("pg_temp"), Table: "probe"}
			field := resolver.fields[policyFieldKey{policyModel, policyText}]
			enum := policyir.EnumID{}
			if test.name == "enum" {
				enum = policyir.EnumID{1}
			}
			field.Type, err = policyir.NewTypeRef(test.values[0].Kind(), true, test.precision, test.scale, enum, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			resolver.fields[policyFieldKey{policyModel, policyText}] = field
			proof, err := New().PolicyCapabilityProof(ctx, database, resolver.SchemaFingerprint())
			if err != nil {
				t.Fatal(err)
			}
			records := make([]evaluate.Record, len(test.values)+1)
			records[0], err = evaluate.NewRecord(policyModel, evaluate.NullField(policyText))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.ExecContext(ctx, `INSERT INTO pg_temp.probe(id,text) VALUES(0,NULL)`); err != nil {
				t.Fatal(err)
			}
			for i, value := range test.values {
				encoded, err := policysql.EncodeOperand(NewPolicyDialect(), resolver, value, field.Type)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.ExecContext(ctx, `INSERT INTO pg_temp.probe(id,text) VALUES($1,`+policyCast("$2", field.Type)+`)`, i+1, encoded); err != nil {
					t.Fatal(err)
				}
				subject, err := evaluate.ValueField(policyText, value)
				if err != nil {
					t.Fatal(err)
				}
				records[i+1], err = evaluate.NewRecord(policyModel, subject)
				if err != nil {
					t.Fatal(err)
				}
			}
			compile := func(condition policyir.Condition) policysql.Fragment {
				t.Helper()
				fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: policyir.ProviderPostgreSQL, Resolver: resolver, Dialect: NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: "root"})
				if err != nil {
					t.Fatal(err)
				}
				return fragment
			}
			modes := []policyir.ComparisonMode{policyir.ComparisonSensitive}
			if test.name == "string" {
				modes = append(modes, policyir.ComparisonASCIIInsensitive)
			}
			for _, mode := range modes {
				for setIndex, values := range [][]policyir.Value{nil, {test.values[0], test.values[0]}, test.values} {
					for _, operation := range []policyir.OperatorID{policyir.OperatorIn, policyir.OperatorNotIn} {
						t.Run(fmt.Sprintf("mode=%d/set=%d/op=%d", mode, setIndex, operation), func(t *testing.T) {
							operand, err := policyir.ManyOperand(values)
							if err != nil {
								t.Fatal(err)
							}
							condition := policyScalar(t, resolver.policyTestResolver, operation, mode, operand)
							expanded, err := policyir.NewConstant(policyModel, false)
							if err != nil {
								t.Fatal(err)
							}
							if len(values) != 0 {
								leaves := make([]policyir.Condition, len(values))
								for i, value := range values {
									one, err := policyir.OneOperand(value)
									if err != nil {
										t.Fatal(err)
									}
									leaves[i] = policyScalar(t, resolver.policyTestResolver, policyir.OperatorEqual, mode, one)
								}
								expanded, err = policyir.NewLogical(policyModel, policyir.LogicalOr, leaves)
								if err != nil {
									t.Fatal(err)
								}
							}
							if operation == policyir.OperatorNotIn {
								expanded, err = policyir.NewLogical(policyModel, policyir.LogicalNot, []policyir.Condition{expanded})
								if err != nil {
									t.Fatal(err)
								}
							}
							var want []int
							for i, record := range records {
								matched, err := evaluate.Condition(condition, record, policyir.PortableProviders())
								if err != nil {
									t.Fatal(err)
								}
								if matched {
									want = append(want, i)
								}
							}
							for _, fragment := range []policysql.Fragment{compile(condition), compile(expanded)} {
								var got []int
								if err := database.SelectContext(ctx, &got, `SELECT id FROM pg_temp.probe AS "root" WHERE `+fragment.SQL()+` ORDER BY id`, fragment.Args()...); err != nil {
									t.Fatal(err)
								}
								if !reflect.DeepEqual(got, want) {
									t.Fatalf("SQL differs from evaluator: got=%v want=%v SQL=%s", got, want, fragment.SQL())
								}
								var unknown int
								if err := database.GetContext(ctx, &unknown, `SELECT count(*) FROM pg_temp.probe AS "root" WHERE (`+fragment.SQL()+`) IS NULL`, fragment.Args()...); err != nil || unknown != 0 {
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
