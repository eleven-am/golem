package schema

import (
	"strconv"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestHistoricalPhysicalV1PostgreSQLBoundedStringAgreementIsExact(t *testing.T) {
	modelID := compilerir.ModelID("11111111111111111111111111111111")
	fieldID := compilerir.FieldID("22222222222222222222222222222222")
	maxLength := uint32(80)
	logical := compilerir.ModelDeclIR{ID: modelID, Fields: []compilerir.FieldIR{{
		ID: fieldID, Kind: compilerir.FieldScalar,
		Scalar: &compilerir.ScalarFieldIR{Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeString, MaxLength: &maxLength}},
	}}}
	column := physical.PhysicalColumn{ID: fieldID, Name: "title", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}
	table := physical.PhysicalTable{ID: modelID, Name: "items", Columns: []physical.PhysicalColumn{column}}
	table.Checks = []physical.PhysicalCheck{historicalV1BoundedStringCheckForTest(modelID, column, maxLength)}
	schema := physical.PhysicalSchema{Version: 1, CanonicalVersion: 1, Provider: physical.PostgreSQLManifest(), Namespace: physical.Namespace{Name: "public"}, Tables: []physical.PhysicalTable{table}}

	if err := historicalAgreementBuilder(logical).indexPhysical(golem.PostgreSQL, schema); err != nil {
		t.Fatalf("exact historical v1 bounded string rejected: %v", err)
	}
	for _, version := range []uint32{2, 3} {
		later := schema
		later.Version, later.CanonicalVersion = version, version
		if historicalV1PostgreSQLBoundedStringAgreement(later, table, column, logical.Fields[0]) {
			t.Fatalf("physical v%d bounded PostgreSQL text reused the frozen v1 agreement", version)
		}
	}

	for _, test := range []struct {
		name   string
		mutate func(*physical.PhysicalSchema)
	}{
		{name: "missing check", mutate: func(value *physical.PhysicalSchema) { value.Tables[0].Checks = nil }},
		{name: "wrong check identity", mutate: func(value *physical.PhysicalSchema) {
			value.Tables[0].Checks[0].ID = "33333333333333333333333333333333"
		}},
		{name: "wrong check name", mutate: func(value *physical.PhysicalSchema) { value.Tables[0].Checks[0].Name = "ck_other" }},
		{name: "wrong field", mutate: func(value *physical.PhysicalSchema) {
			other := compilerir.FieldID("44444444444444444444444444444444")
			value.Tables[0].Checks[0].Expression.Operands[0].Operands[0].Column = &other
		}},
		{name: "wrong length", mutate: func(value *physical.PhysicalSchema) {
			value.Tables[0].Checks[0].Expression.Operands[1].Literal.Canonical = "81"
		}},
		{name: "wrong operator", mutate: func(value *physical.PhysicalSchema) {
			value.Tables[0].Checks[0].Expression.Symbol.Identity = "golem.schema.predicate.less-than.v1"
		}},
		{name: "required capability", mutate: func(value *physical.PhysicalSchema) {
			value.Tables[0].Checks[0].RequiredCapabilities = []physical.CapabilityRequirement{{Capability: "forged"}}
		}},
		{name: "v2 text", mutate: func(value *physical.PhysicalSchema) { value.Version, value.CanonicalVersion = 2, 2 }},
		{name: "v3 text", mutate: func(value *physical.PhysicalSchema) { value.Version, value.CanonicalVersion = 3, 3 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := schema
			forged.Tables = append([]physical.PhysicalTable(nil), schema.Tables...)
			forged.Tables[0].Columns = append([]physical.PhysicalColumn(nil), schema.Tables[0].Columns...)
			forged.Tables[0].Checks = cloneChecksForAgreementTest(schema.Tables[0].Checks)
			test.mutate(&forged)
			if err := historicalAgreementBuilder(logical).indexPhysical(golem.PostgreSQL, forged); err == nil || !strings.Contains(err.Error(), "requires") {
				t.Fatalf("forged historical agreement accepted: %v", err)
			}
		})
	}
}

func historicalAgreementBuilder(model compilerir.ModelDeclIR) *registryBuilder {
	return &registryBuilder{
		registry: Registry{
			physicalModels: map[golem.Provider]map[golem.ModelID]PhysicalModel{}, physicalFields: map[golem.Provider]map[golem.ModelID]map[golem.FieldID]PhysicalField{}, capabilities: map[golem.Provider]map[compilerir.CapabilityID]physical.CapabilityFact{},
			physicalModelNames: map[golem.Provider]map[physical.PhysicalName]golem.ModelID{}, physicalAccessObjects: map[golem.Provider]map[physical.PhysicalName]PhysicalAccessObject{}, physicalKeyAccessObjects: map[golem.Provider]map[golem.ModelID][]PhysicalAccessObject{}, physicalNamespaces: map[golem.Provider]physical.PhysicalName{}, physicalSystemNamespaces: map[golem.Provider]physical.PhysicalName{},
		},
		logicalModels: map[compilerir.ModelID]compilerir.ModelDeclIR{model.ID: model}, logicalFields: map[compilerir.ModelID]map[compilerir.FieldID]compilerir.FieldIR{},
	}
}

func historicalV1BoundedStringCheckForTest(modelID compilerir.ModelID, column physical.PhysicalColumn, length uint32) physical.PhysicalCheck {
	id, name := physical.HistoricalV1MaxLengthCheckIdentity(modelID, column.ID)
	integer := physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
	boolean := physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}
	fieldID := column.ID
	literal := compilerir.TypedLiteralIR{Kind: compilerir.LiteralInteger, Canonical: strconv.FormatUint(uint64(length), 10)}
	return physical.PhysicalCheck{ID: id, Name: name, Expression: physical.Expression{
		Kind: physical.ExpressionOperator, Type: boolean, Nullable: column.Nullable, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.predicate.less-equal.v1", Kind: compilerir.SchemaSymbolOperator, Version: 1, Provider: compilerir.ProviderScopePortable},
		Operands: []physical.Expression{{Kind: physical.ExpressionFunction, Type: integer, Nullable: column.Nullable, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.function.length.v1", Kind: compilerir.SchemaSymbolFunction, Version: 1, Provider: compilerir.ProviderScopePortable}, Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: column.Storage, Nullable: column.Nullable, Column: &fieldID, Operands: []physical.Expression{}}}}, {Kind: physical.ExpressionLiteral, Type: integer, Literal: &literal, Operands: []physical.Expression{}}},
	}}
}

func cloneChecksForAgreementTest(values []physical.PhysicalCheck) []physical.PhysicalCheck {
	result := append([]physical.PhysicalCheck(nil), values...)
	for index := range result {
		root := result[index].Expression
		root.Operands = append([]physical.Expression(nil), root.Operands...)
		length := root.Operands[0]
		length.Operands = append([]physical.Expression(nil), length.Operands...)
		column := *length.Operands[0].Column
		length.Operands[0].Column = &column
		root.Operands[0] = length
		literal := *root.Operands[1].Literal
		root.Operands[1].Literal = &literal
		result[index].Expression = root
	}
	return result
}
