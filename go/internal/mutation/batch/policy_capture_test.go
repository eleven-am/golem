package batch

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

func TestPolicyCaptureReaderRefusesInvalidRequestsAndPreservesBounds(t *testing.T) {
	fixture := captureReaderFixture(t)
	model := policyir.ModelID(fixture.Post)
	text, _ := policyir.StringValue("secret\n' OR TRUE --")
	valid := mutationdecode.Value(policyir.FieldID(fixture.PostTitle), text)
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		for name, cells := range map[string][]mutationdecode.Cell{
			"duplicate":     {valid, valid},
			"unknown":       {mutationdecode.Value(policyir.FieldID{255}, text)},
			"foreign":       {mutationdecode.Value(policyir.FieldID(fixture.UserName), text)},
			"relation":      {mutationdecode.Value(policyir.FieldID(fixture.PostAuthor), text)},
			"wrong type":    {mutationdecode.Value(policyir.FieldID(fixture.AuthorID), text)},
			"absent value":  {mutationdecode.Value(policyir.FieldID(fixture.PostTitle), policyir.Value{})},
			"required null": {mutationdecode.Null(policyir.FieldID(fixture.PostTitle))},
		} {
			if _, err := RenderPolicyCapture(fixture.Registry, provider, model, cells, 10, 10); err == nil {
				t.Fatalf("provider=%v accepted %s", provider, name)
			}
		}
		for _, bounds := range [][2]uint32{{0, 10}, {10, 0}} {
			if _, err := RenderPolicyCapture(fixture.Registry, provider, model, nil, bounds[0], bounds[1]); err == nil {
				t.Fatal("zero capture bound accepted")
			}
		}
		if _, err := RenderPolicyCapture(fixture.Registry, provider, policyir.ModelID(fixture.User), nil, 10, 10); err == nil {
			t.Fatal("model without static capture authority was accepted")
		}
		if _, err := RenderPolicyCapture(nil, provider, model, nil, 10, 10); err == nil {
			t.Fatal("nil registry accepted")
		}
		id := mutationdecode.Value(policyir.FieldID(fixture.PostID), uuidValue(1))
		if _, err := RenderPolicyCapture(fixture.Registry, provider, model, []mutationdecode.Cell{valid, id}, 10, 1); err == nil {
			t.Fatal("parameter overflow accepted")
		}
		left, err := RenderPolicyCapture(fixture.Registry, provider, model, []mutationdecode.Cell{valid, id}, math.MaxUint32, 2)
		if err != nil {
			t.Fatal(err)
		}
		right, err := RenderPolicyCapture(fixture.Registry, provider, model, []mutationdecode.Cell{id, valid}, math.MaxUint32, 2)
		if err != nil || !reflect.DeepEqual(left, right) {
			t.Fatal("equality order changed the private reader")
		}
		if !strings.HasSuffix(left.SQL(), "LIMIT 4294967296") || left.Role() != RehydrateAfterImage || left.Cardinality() != AtMostSentinelRows || left.ExpectedRows() != 0 || len(left.Bindings()) != 2 {
			t.Fatal("capture reader lost its non-wrapping sentinel contract")
		}
		if strings.Contains(left.SQL(), "secret") {
			t.Fatal("capture equality was interpolated into SQL")
		}
		if _, locking := left.LockReader(nil); locking {
			t.Fatal("capture reader invented a locking or snapshot certificate")
		}
	}
}

func TestPolicyCaptureReaderExactValuesAndOverflowSentinelLive(t *testing.T) {
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		t.Run(string(provider), func(t *testing.T) {
			fixture := captureReaderFixture(t)
			ctx := context.Background()
			var database *sqlx.DB
			var err error
			if provider == policyir.ProviderSQLite {
				database, _, err = sqliteprovider.New().Open(ctx, filepath.Join(t.TempDir(), "capture.db"))
			} else {
				database, _, err = postgresprovider.New().Open(ctx, testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable))
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if provider == policyir.ProviderSQLite {
				err = sqliteprovider.New().ApplyInitial(ctx, database, fixture.SQLite)
			} else {
				err = postgresprovider.New().ApplyInitial(ctx, database, fixture.PostgreSQL)
			}
			if err != nil {
				t.Fatal(err)
			}
			model := policyir.ModelID(fixture.Post)
			integer, _ := policyir.SignedValue(policyir.ValueInt64, 9_007_199_254_740_993)
			decimal, _ := policyir.NewDecimalValue(123456789012345, 13)
			instant, _ := policyir.NewDateTimeValue(-1, 123456000)
			text, _ := policyir.StringValue("private\n'); DELETE FROM posts; --")
			cells := []mutationdecode.Cell{
				mutationdecode.Value(policyir.FieldID(fixture.AuthorID), uuidValue(9)),
				mutationdecode.Value(policyir.FieldID(fixture.PostTitle), text),
				mutationdecode.Value(policyir.FieldID(fixture.PostBigInt), integer),
				mutationdecode.Value(policyir.FieldID(fixture.PostDecimal), decimal),
				mutationdecode.Value(policyir.FieldID(fixture.PostBytes), policyir.BytesValue([]byte{})),
				mutationdecode.Value(policyir.FieldID(fixture.PostDateTime), instant),
				mutationdecode.Null(policyir.FieldID(fixture.PostNullableText)),
			}
			name, _ := policyir.StringValue("owner")
			insertCaptureCells(t, ctx, database, fixture.Registry, provider, policyir.ModelID(fixture.User), []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.UserID), uuidValue(9)), mutationdecode.Value(policyir.FieldID(fixture.UserName), name)})
			for id := byte(1); id <= 3; id++ {
				insertCaptureCells(t, ctx, database, fixture.Registry, provider, model, append(append([]mutationdecode.Cell(nil), cells...), mutationdecode.Value(policyir.FieldID(fixture.PostID), uuidValue(id))))
			}
			query := func(equalities []mutationdecode.Cell, maximum uint32) []mutationdecode.Row {
				t.Helper()
				statement, err := RenderPolicyCapture(fixture.Registry, provider, model, equalities, maximum, 20)
				if err != nil {
					t.Fatal(err)
				}
				fields := make([]policyir.FieldID, len(statement.Columns()))
				for index, column := range statement.Columns() {
					fields[index] = column.FieldID()
				}
				decoder, err := readdecode.NewFields(model, fixture.Registry, provider, fields)
				if err != nil {
					t.Fatal(err)
				}
				arguments := make([]any, len(statement.Bindings()))
				for index, binding := range statement.Bindings() {
					arguments[index] = binding.Value()
				}
				rows, err := database.QueryxContext(ctx, statement.SQL(), arguments...)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				var result []mutationdecode.Row
				for rows.Next() {
					scan := decoder.NewScan()
					if err := rows.Scan(scan.Destinations()...); err != nil {
						t.Fatal(err)
					}
					decoded, err := scan.Decode()
					if err != nil {
						t.Fatal(err)
					}
					for _, cell := range decoded {
						if cell.Public() {
							t.Fatal("private evidence became a public selection")
						}
					}
					row, err := mutationdecode.FromReadCells(fixture.Registry, model, decoded)
					if err != nil {
						t.Fatal(err)
					}
					result = append(result, row)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return result
			}
			if len(query(cells, 1)) != 2 || len(query(nil, 1)) != 2 {
				t.Fatal("overflow was silently truncated instead of returning a sentinel")
			}
			found := query(cells, 3)
			if len(found) != 3 {
				t.Fatal("typed equality lost exact physical rows")
			}
			for index, row := range found {
				for _, expected := range append(append([]mutationdecode.Cell(nil), cells...), mutationdecode.Value(policyir.FieldID(fixture.PostID), uuidValue(byte(index+1)))) {
					actual, present := row.Cell(expected.FieldID())
					if !present || !mutationdecode.EqualCell(actual, expected) {
						t.Fatalf("exact private scalar changed: field=%x", expected.FieldID())
					}
				}
			}
			if len(query([]mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.PostID), uuidValue(2))}, 3)) != 1 {
				t.Fatal("primary identity equality did not isolate the exact row")
			}
		})
	}
}

func insertCaptureCells(t *testing.T, ctx context.Context, database *sqlx.DB, registry *schema.Registry, provider policyir.Provider, model policyir.ModelID, cells []mutationdecode.Cell) {
	t.Helper()
	renderer, err := standaloneContext(registry, provider, model, "insert_capture")
	if err != nil {
		t.Fatal(err)
	}
	var columns, placeholders []string
	var arguments []any
	for _, cell := range cells {
		field, _ := renderer.resolver.Field(provider, model, cell.FieldID())
		columns = append(columns, renderer.dialect.Quote(field.Column))
		var encoded any
		if value, present := cell.PolicyValue(); present {
			encoded, err = renderer.encode(value, field.Type)
			if err != nil {
				t.Fatal(err)
			}
		}
		arguments = append(arguments, encoded)
		placeholders = append(placeholders, renderer.dialect.Placeholder(len(arguments)))
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO "+renderer.dialect.Table(renderer.model)+" ("+strings.Join(columns, ",")+") VALUES ("+strings.Join(placeholders, ",")+")", arguments...); err != nil {
		t.Fatal(err)
	}
}

func captureReaderFixture(t *testing.T) schematest.Fixture {
	t.Helper()
	fixture := schematest.NewSubscribedMutationExactValues(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	root := compilerir.ModelID(hex.EncodeToString(fixture.Post[:]))
	args := compilerir.GoNamedTypeIR{PackagePath: "example.test/capture", Name: "Args"}
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{{ModelID: root, Template: compilerir.ScopeTemplateIR{Name: "authority", Arguments: args, Expression: compilerir.ScopeExpressionIR{ModelID: root, Operator: "true"}}}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	fields := []golem.FieldID{fixture.AuthorID, fixture.PostTitle, fixture.PostBigInt, fixture.PostDecimal, fixture.PostBytes, fixture.PostDateTime, fixture.PostNullableText}
	body := compilerir.ScopeExpressionIR{ModelID: root, Operator: "and"}
	var parameters []compilerir.ScopeParameterIR
	for index, id := range fields {
		field, _ := fixture.Registry.Field(fixture.Post, id)
		parameter := fmt.Sprintf("Value%d", index)
		parameters = append(parameters, compilerir.ScopeParameterIR{Name: parameter, Type: field.LogicalType()})
		body.Children = append(body.Children, compilerir.ScopeExpressionIR{ModelID: root, Operator: "eq", FieldID: compilerir.FieldID(hex.EncodeToString(id[:])), Parameter: parameter})
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{{ModelID: root, ScopeName: "authority", Template: compilerir.FactTemplateIR{Name: "exact", Arguments: args, Parameters: parameters, Expression: body}}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(encoded) != len(golem.SchemaDigest{}) {
		t.Fatalf("invalid fingerprint: %v", err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], encoded)
	document := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	old := fixture.Bundle
	fixture.Bundle = golem.GeneratedSchemaBundle(old.GenerationDigest(), old.GeneratorVersion(), old.TemplateABIVersion(), old.Model(), document, old.Providers()...)
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}
