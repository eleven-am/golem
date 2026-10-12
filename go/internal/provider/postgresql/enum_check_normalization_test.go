package postgresql

import (
	"context"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
	"github.com/eleven-am/golem/go/internal/physical"
)

func enumCheckFixtureTable(t *testing.T) physical.PhysicalTable {
	t.Helper()
	schema, err := New().Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "public"})
	if err != nil {
		t.Fatal(err)
	}
	return physicalTable(t, schema, "posts")
}

func enumCheckColumn(field ir.FieldID, nullable bool, storage physical.StorageKind) physical.Expression {
	return physical.Expression{Kind: physical.ExpressionColumn, Type: physical.StorageType{Kind: storage}, Nullable: nullable, Column: &field}
}

func enumCheckLiteral(kind ir.LiteralKind, value string) physical.Expression {
	return physical.Expression{Kind: physical.ExpressionLiteral, Type: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Literal: &ir.TypedLiteralIR{Kind: kind, Canonical: value}}
}

func normalizedCatalogCheck(t *testing.T, table physical.PhysicalTable, source string, reviewed physical.Expression) physical.Expression {
	t.Helper()
	parsed, err := parseCatalogExpression(source, table)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeCatalogExpressionAgainstReviewed(parsed, reviewed)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func TestCatalogEnumChecksNormalizeOnlyToTheIdenticalReviewedExpression(t *testing.T) {
	table := enumCheckFixtureTable(t)
	status := enumCheckColumn(ir.FieldID(id(30)), false, physical.StoragePostgreSQLText)
	metadata := enumCheckColumn(ir.FieldID(id(27)), true, physical.StoragePostgreSQLJSONB)
	other := enumCheckColumn(ir.FieldID(id(99)), false, physical.StoragePostgreSQLText)
	notNull := operator(schemaexpr.IsNotNull, metadata)
	notNull.Nullable = false
	combined := operator("golem.schema.predicate.or.v1", operator(schemaexpr.NotEqual, status, enumCheckLiteral(ir.LiteralEnum, "live")), notNull)
	cases := []struct {
		name     string
		source   string
		reviewed physical.Expression
		matches  bool
	}{
		{"single value IN stored as equality", `(status = 'draft'::text)`, operator(schemaexpr.In, status, enumCheckLiteral(ir.LiteralString, "draft")), true},
		{"single value IN with a different live value", `(status = 'live'::text)`, operator(schemaexpr.In, status, enumCheckLiteral(ir.LiteralString, "draft")), false},
		{"equality where the reviewed IN has two values", `(status = 'draft'::text)`, operator(schemaexpr.In, status, enumCheckLiteral(ir.LiteralString, "draft"), enumCheckLiteral(ir.LiteralString, "live")), false},
		{"two value IN stored as ANY", `(status = ANY (ARRAY['draft'::text, 'live'::text]))`, operator(schemaexpr.In, status, enumCheckLiteral(ir.LiteralString, "draft"), enumCheckLiteral(ir.LiteralString, "live")), true},
		{"enum literal comparison", `(status = 'live'::text)`, operator(schemaexpr.Equal, status, enumCheckLiteral(ir.LiteralEnum, "live")), true},
		{"enum literal comparison with a different value", `(status = 'draft'::text)`, operator(schemaexpr.Equal, status, enumCheckLiteral(ir.LiteralEnum, "live")), false},
		{"enum literal comparison on a different column", `(status = 'live'::text)`, operator(schemaexpr.Equal, other, enumCheckLiteral(ir.LiteralEnum, "live")), false},
		{"single value IN of enum literals", `(status = 'live'::text)`, operator(schemaexpr.In, status, enumCheckLiteral(ir.LiteralEnum, "live")), true},
		{"enum comparison combined with a null test", `((status <> 'live'::text) OR (metadata IS NOT NULL))`, combined, true},
		{"enum comparison combined with a null test and a different value", `((status <> 'draft'::text) OR (metadata IS NOT NULL))`, combined, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			normalized := normalizedCatalogCheck(t, table, testCase.source, testCase.reviewed)
			if matches := reflect.DeepEqual(normalized, testCase.reviewed); matches != testCase.matches {
				t.Fatalf("normalized %s matches reviewed = %v; want %v\nnormalized=%#v\nreviewed=%#v", testCase.source, matches, testCase.matches, normalized, testCase.reviewed)
			}
		})
	}
}
