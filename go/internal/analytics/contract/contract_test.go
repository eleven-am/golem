package contract

import (
	"context"
	"fmt"
	"strings"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

const (
	p6PostID     compilerir.ModelID    = "11111111111111111111111111111111"
	p6UserID     compilerir.ModelID    = "22222222222222222222222222222222"
	p6AuthorID   compilerir.FieldID    = "33333333333333333333333333333333"
	p6ViewsID    compilerir.FieldID    = "44444444444444444444444444444444"
	p6PayloadID  compilerir.FieldID    = "55555555555555555555555555555555"
	p6RelationID compilerir.RelationID = "66666666666666666666666666666666"
	p6UserKeyID  compilerir.FieldID    = "77777777777777777777777777777777"
	p6CountryID  compilerir.FieldID    = "88888888888888888888888888888888"
)

func TestP6ContractNormalizationMaterializesOperationsAllowlistsPathsLimitsAndScopedRoots(t *testing.T) {
	compilation := p6ContractFixture()
	dimensions := []compilerir.FieldID{p6AuthorID}
	measures := []compilerir.FieldID{p6ViewsID}
	graphqlMax, relationMax := uint32(75), uint32(5000)
	patch := ModelPatch{
		ModelID: p6PostID, Enabled: true, ScopedReads: true,
		Dimensions: &dimensions, Measures: &measures,
		GraphQLMaxGroups: &graphqlMax, RelationMaxIntermediateGroups: &relationMax,
		RelationDimensions: []compilerir.RelationDimensionContractIR{{Name: "authorCountry", Path: []compilerir.RelationID{p6RelationID}, TerminalField: p6CountryID}},
	}
	if diagnostics := Normalize(&compilation, []ModelPatch{patch}); len(diagnostics) != 0 {
		t.Fatalf("analytics diagnostics = %#v", diagnostics)
	}
	if diagnostics := graphqlcontract.Normalize(&compilation, nil); len(diagnostics) != 0 {
		t.Fatalf("GraphQL diagnostics = %#v", diagnostics)
	}
	contract := compilation.Contract.Models[0]
	if !contract.ScopedReads || contract.Aggregation == nil || !contract.Aggregation.Enabled || !contract.Aggregation.DimensionsExplicit || !contract.Aggregation.MeasuresExplicit {
		t.Fatalf("normalized contract = %#v", contract)
	}
	if contract.Aggregation.GraphQLMaxGroups != 75 || contract.Aggregation.RelationMaxIntermediateGroups != 5000 || len(contract.Aggregation.RelationDimensions) != 1 {
		t.Fatalf("normalized analytics = %#v", contract.Aggregation)
	}
	if contract.Roots.Aggregate != "aggregatePosts" || contract.Roots.GroupBy != "groupByPosts" || contract.Roots.RelationGroupBy != "relationGroupByPosts" {
		t.Fatalf("normalized analytics roots = %#v", contract.Roots)
	}
}

func TestAnalyticsOnlyChangesContractFingerprintAndNeverModelOrMigration(t *testing.T) {
	before := p6ContractFixture()
	after := p6ContractFixture()
	dimensions := []compilerir.FieldID{p6AuthorID}
	if diagnostics := Normalize(&after, []ModelPatch{{ModelID: p6PostID, Enabled: true, ScopedReads: true, Dimensions: &dimensions}}); len(diagnostics) != 0 {
		t.Fatalf("analytics diagnostics = %#v", diagnostics)
	}
	beforeModel, _ := compilerir.ModelFingerprint(before.Model)
	afterModel, _ := compilerir.ModelFingerprint(after.Model)
	beforeContract, _ := compilerir.ContractFingerprint(before.Contract)
	afterContract, _ := compilerir.ContractFingerprint(after.Contract)
	if beforeModel != afterModel || beforeContract == afterContract {
		t.Fatalf("fingerprints model=%s/%s contract=%s/%s", beforeModel, afterModel, beforeContract, afterContract)
	}
	providers := []struct {
		name      string
		namespace physical.PhysicalName
		lower     physical.Lowerer
	}{{"sqlite", "main", sqlite.New()}, {"postgresql", "p6_contract", postgresql.New()}}
	for _, provider := range providers {
		t.Run(provider.name, func(t *testing.T) {
			left, err := provider.lower.Lower(context.Background(), before.Model, physical.LowerOptions{Namespace: provider.namespace})
			if err != nil {
				t.Fatal(err)
			}
			right, err := provider.lower.Lower(context.Background(), after.Model, physical.LowerOptions{Namespace: provider.namespace})
			if err != nil {
				t.Fatal(err)
			}
			leftFingerprint, _ := physical.PhysicalFingerprint(left)
			rightFingerprint, _ := physical.PhysicalFingerprint(right)
			plan, err := migration.Diff(left, right)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range plan.Operations {
				if operation.Kind != migration.RecordSchemaVersion {
					t.Fatalf("analytics generated migration operation %#v", operation)
				}
			}
			if leftFingerprint != rightFingerprint {
				t.Fatal("analytics changed physical fingerprint")
			}
		})
	}
}

func TestP6ContractRejectsNamesTypesPathsLimitsAndCollisions(t *testing.T) {
	compilation := p6ContractFixture()
	emptyDimensions := []compilerir.FieldID{}
	emptyMeasures := []compilerir.FieldID{}
	zero := uint32(0)
	patch := ModelPatch{
		ModelID: p6PostID, Enabled: true, Dimensions: &emptyDimensions, Measures: &emptyMeasures,
		GraphQLMaxGroups: &zero, RelationMaxIntermediateGroups: &zero,
		RelationDimensions: []compilerir.RelationDimensionContractIR{
			{Name: "BadName", Path: []compilerir.RelationID{"99999999999999999999999999999999"}, TerminalField: p6CountryID},
			{Name: "BadName", Path: []compilerir.RelationID{p6RelationID}, TerminalField: p6ViewsID},
		},
	}
	diagnostics := Normalize(&compilation, []ModelPatch{patch})
	for _, code := range []string{"P6_ANALYTICS_LIMIT", "P6_RELATION_DIMENSION_NAME", "P6_RELATION_DIMENSION_PATH", "P6_RELATION_DIMENSION_TERMINAL"} {
		if !p6HasDiagnostic(diagnostics, code) {
			t.Errorf("missing %s in %#v", code, diagnostics)
		}
	}
	diagnostics = append(diagnostics, graphqlcontract.Normalize(&compilation, nil)...)
	for _, code := range []string{"P6_GRAPHQL_DIMENSION_ALLOWLIST_EMPTY", "P6_GRAPHQL_MEASURE_ALLOWLIST_EMPTY"} {
		if !p6HasDiagnostic(diagnostics, code) {
			t.Errorf("missing %s in %#v", code, diagnostics)
		}
	}

	unsupported := p6ContractFixture()
	bytes := []compilerir.FieldID{p6PayloadID}
	diagnostics = Normalize(&unsupported, []ModelPatch{{ModelID: p6PostID, Enabled: true, Dimensions: &bytes}})
	if !p6HasDiagnostic(diagnostics, "P6_ANALYTICS_FIELD") {
		t.Fatalf("unsupported dimension diagnostics = %#v", diagnostics)
	}
}

func TestP6ContractRejectsAnalyticsLimitsAboveFrozenHardMaxima(t *testing.T) {
	for _, test := range []struct {
		name       string
		graphql    uint32
		relation   uint32
		wantDetail string
	}{
		{"graphql", HardMaxGraphQLMaxGroups + 1, DefaultRelationMaxIntermediateGroups, "GraphQL maxGroups"},
		{"relation", DefaultGraphQLMaxGroups, HardMaxRelationIntermediateGroups + 1, "relation intermediate-group"},
	} {
		t.Run(test.name, func(t *testing.T) {
			compilation := p6ContractFixture()
			diagnostics := Normalize(&compilation, []ModelPatch{{
				ModelID:                       p6PostID,
				Enabled:                       true,
				GraphQLMaxGroups:              &test.graphql,
				RelationMaxIntermediateGroups: &test.relation,
			}})
			found := false
			for _, diagnostic := range diagnostics {
				if diagnostic.Code == "P6_ANALYTICS_LIMIT" && strings.Contains(diagnostic.Message, test.wantDetail) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %q hard-limit diagnostic in %#v", test.wantDetail, diagnostics)
			}
		})
	}
}

func TestP6RelationDeclarationRejectsToManyReverseAndMultiplePaths(t *testing.T) {
	patch := func(paths ...[]compilerir.RelationID) []ModelPatch {
		dimensions := make([]compilerir.RelationDimensionContractIR, len(paths))
		for index, path := range paths {
			dimensions[index] = compilerir.RelationDimensionContractIR{
				Name:          fmt.Sprintf("authorCountry%d", index),
				Path:          path,
				TerminalField: p6CountryID,
			}
		}
		return []ModelPatch{{ModelID: p6PostID, Enabled: true, RelationDimensions: dimensions}}
	}
	for _, test := range []struct {
		name   string
		mutate func(*compilerir.CompilationIR)
	}{
		{
			name: "to-many",
			mutate: func(compilation *compilerir.CompilationIR) {
				compilation.Model.Models[0].Fields[3].Relation.Kind = compilerir.RelationHasMany
			},
		},
		{
			name: "reverse",
			mutate: func(compilation *compilerir.CompilationIR) {
				compilation.Model.Models[0].Fields[3].Relation.Role = compilerir.RelationInverse
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			compilation := p6ContractFixture()
			test.mutate(&compilation)
			diagnostics := Normalize(&compilation, patch([]compilerir.RelationID{p6RelationID}))
			if !p6HasDiagnostic(diagnostics, "P6_RELATION_DIMENSION_PATH") {
				t.Fatalf("%s relation path diagnostics=%#v", test.name, diagnostics)
			}
		})
	}

	const (
		secondRelation compilerir.RelationID = "99999999999999999999999999999999"
		secondField    compilerir.FieldID    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	compilation := p6ContractFixture()
	compilation.Model.Models[0].Fields = append(compilation.Model.Models[0].Fields, compilerir.FieldIR{
		ID: secondField, GoName: "Editor", LogicalName: "editor", Kind: compilerir.FieldRelation,
		Relation: &compilerir.RelationFieldIR{RelationID: secondRelation, Role: compilerir.RelationSource, Kind: compilerir.RelationBelongsTo},
	})
	compilation.Model.Relations = append(compilation.Model.Relations, compilerir.RelationIR{
		ID: secondRelation, Name: "editor", SourceModel: p6PostID, TargetModel: p6UserID,
		SourceField: secondField, Cardinality: compilerir.RelationOne,
		LocalFields: []compilerir.FieldID{p6AuthorID}, RemoteFields: []compilerir.FieldID{p6UserKeyID},
	})
	diagnostics := Normalize(&compilation, patch([]compilerir.RelationID{p6RelationID}, []compilerir.RelationID{secondRelation}))
	if !p6HasDiagnostic(diagnostics, "P6_RELATION_DIMENSION_PATH") {
		t.Fatalf("multiple relation paths diagnostics=%#v", diagnostics)
	}
}

func p6HasDiagnostic(values []compilerir.Diagnostic, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}

func p6ContractFixture() compilerir.CompilationIR {
	postRelationField := compilerir.FieldIR{ID: compilerir.FieldID(p6RelationID), GoName: "Author", LogicalName: "author", Kind: compilerir.FieldRelation, Relation: &compilerir.RelationFieldIR{RelationID: p6RelationID, Role: compilerir.RelationSource, Kind: compilerir.RelationBelongsTo}}
	post := compilerir.ModelDeclIR{ID: p6PostID, LogicalName: "Post", Table: compilerir.TableBindingIR{PhysicalName: "posts"}, Fields: []compilerir.FieldIR{
		{ID: p6AuthorID, GoName: "AuthorID", LogicalName: "authorID", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "author_id", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}}},
		{ID: p6ViewsID, GoName: "Views", LogicalName: "views", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "views", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}}},
		{ID: p6PayloadID, GoName: "Payload", LogicalName: "payload", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "payload", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeBytes}}},
		postRelationField,
	}}
	user := compilerir.ModelDeclIR{ID: p6UserID, LogicalName: "User", Table: compilerir.TableBindingIR{PhysicalName: "users"}, Fields: []compilerir.FieldIR{
		{ID: p6UserKeyID, GoName: "ID", LogicalName: "id", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "id", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}}},
		{ID: p6CountryID, GoName: "Country", LogicalName: "country", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "country", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}}},
	}}
	return compilerir.CompilationIR{
		Model: compilerir.ModelIR{FormatVersion: compilerir.ModelFormatVersion, Providers: []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL}, Models: []compilerir.ModelDeclIR{post, user}, Relations: []compilerir.RelationIR{{ID: p6RelationID, Name: "author", SourceModel: p6PostID, TargetModel: p6UserID, SourceField: compilerir.FieldID(p6RelationID), Cardinality: compilerir.RelationOne, LocalFields: []compilerir.FieldID{p6AuthorID}, RemoteFields: []compilerir.FieldID{p6UserKeyID}}}},
		Contract: compilerir.ContractIR{FormatVersion: compilerir.ContractFormatVersion, Models: []compilerir.ModelContractIR{
			{ModelID: p6PostID, GraphQLName: "Post", GraphQLPlural: "Posts", Exposed: true, Operations: []compilerir.Operation{compilerir.OperationAggregate, compilerir.OperationGroupBy, compilerir.OperationRelationGroupBy}, Fields: []compilerir.FieldContractIR{{FieldID: p6AuthorID, GraphQLName: "authorID"}, {FieldID: p6ViewsID, GraphQLName: "views"}, {FieldID: p6PayloadID, GraphQLName: "payload"}}},
			{ModelID: p6UserID, GraphQLName: "User", GraphQLPlural: "Users", Exposed: true, Fields: []compilerir.FieldContractIR{{FieldID: p6UserKeyID, GraphQLName: "id"}, {FieldID: p6CountryID, GraphQLName: "country"}}},
		}},
	}
}
