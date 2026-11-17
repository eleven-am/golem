package schematest

import (
	"context"
	"encoding/hex"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"testing"
)

// OrdinaryGuardGraphFixture adds an unsubscribed Watch source and a captured
// User -> Posts -> Comments fact, shared by matcher and real runtime tests.
type OrdinaryGuardGraphFixture struct {
	Graph                                      GraphFixture
	Watch                                      golem.ModelID
	Key                                        golem.KeyID
	ID, Profile, Video, RelationField, Inverse golem.FieldID
	Relation                                   golem.RelationID
}

func NewPolicyCaptureOrdinaryGuardGraph(t testing.TB) OrdinaryGuardGraphFixture {
	t.Helper()
	f := NewPolicyCaptureGraph(t)
	compilation := ordinaryGuardMust(schema.DecodeActive(f.Bundle))
	modelID := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	fieldID := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	relationID := func(id golem.RelationID) compilerir.RelationID {
		return compilerir.RelationID(hex.EncodeToString(id[:]))
	}
	fixture := OrdinaryGuardGraphFixture{Graph: f, Watch: golem.ModelID{14: 4, 15: 1}, ID: golem.FieldID{14: 4, 15: 2}, Profile: golem.FieldID{14: 4, 15: 3}, Video: golem.FieldID{14: 4, 15: 4}, RelationField: golem.FieldID{14: 4, 15: 5}, Inverse: golem.FieldID{14: 4, 15: 6}, Relation: golem.RelationID{14: 4, 15: 7}}
	watchKey := compilerir.KeyID("00000000000000000000000000000408")
	fixture.Key = golem.KeyID(mustFixed(t, string(watchKey)))
	scalar := func(id golem.FieldID, name, column string) compilerir.FieldIR {
		return compilerir.FieldIR{ID: fieldID(id), GoName: name, LogicalName: name, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: compilerir.SQLIdentifier(column), Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}}}
	}
	compilation.Model.Models = append(compilation.Model.Models, compilerir.ModelDeclIR{ID: modelID(fixture.Watch), LogicalName: "Watch", Table: compilerir.TableBindingIR{PhysicalName: "watches"}, PrimaryKey: &compilerir.KeyIR{ID: watchKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_watches", Fields: []compilerir.FieldID{fieldID(fixture.ID)}}, Fields: []compilerir.FieldIR{scalar(fixture.ID, "ID", "id"), scalar(fixture.Profile, "ProfileID", "profile_id"), scalar(fixture.Video, "VideoID", "video_id"), {ID: fieldID(fixture.RelationField), GoName: "Video", LogicalName: "Video", Kind: compilerir.FieldRelation, Relation: &compilerir.RelationFieldIR{RelationID: relationID(fixture.Relation), Role: compilerir.RelationSource, Kind: compilerir.RelationBelongsTo}}}})
	compilation.Contract.Models = append(compilation.Contract.Models, compilerir.ModelContractIR{ModelID: modelID(fixture.Watch), Fields: []compilerir.FieldContractIR{{FieldID: fieldID(fixture.ID)}, {FieldID: fieldID(fixture.Profile)}, {FieldID: fieldID(fixture.Video)}, {FieldID: fieldID(fixture.RelationField)}}})
	inverse := fieldID(fixture.Inverse)
	for index := range compilation.Model.Models {
		model := &compilation.Model.Models[index]
		if model.ID == modelID(f.Post) {
			model.Fields = append(model.Fields, compilerir.FieldIR{ID: inverse, GoName: "Watches", LogicalName: "Watches", Kind: compilerir.FieldRelation, Relation: &compilerir.RelationFieldIR{RelationID: relationID(fixture.Relation), Role: compilerir.RelationInverse, Kind: compilerir.RelationHasMany}})
		}
	}
	for index := range compilation.Contract.Models {
		model := &compilation.Contract.Models[index]
		if model.ModelID == modelID(f.Post) {
			model.Fields = append(model.Fields, compilerir.FieldContractIR{FieldID: inverse})
		}
	}
	compilation.Model.Relations = append(compilation.Model.Relations, compilerir.RelationIR{ID: relationID(fixture.Relation), SourceModel: modelID(fixture.Watch), TargetModel: modelID(f.Post), SourceField: fieldID(fixture.RelationField), InverseField: &inverse, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{fieldID(fixture.Video)}, RemoteFields: []compilerir.FieldID{fieldID(f.PostID)}})
	var declarations []scopecontract.Declaration
	for index := range compilation.Contract.Models {
		owner := &compilation.Contract.Models[index]
		for _, template := range owner.ScopeTemplates {
			declarations = append(declarations, scopecontract.Declaration{ModelID: owner.ModelID, Template: template})
		}
		owner.ScopeTemplates, owner.FactTemplates = nil, nil
	}
	if diagnostics := scopecontract.Normalize(&compilation, declarations); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	args := compilerir.GoNamedTypeIR{PackagePath: "example.test/history", Name: "HistoryArgs"}
	body, _ := f.Registry.Field(f.Comment, f.CommentBody)
	expression := compilerir.ScopeExpressionIR{ModelID: modelID(f.User), Operator: "some", FieldID: fieldID(f.UserPosts), RelationID: relationID(f.Authorship), TargetModelID: modelID(f.Post), Children: []compilerir.ScopeExpressionIR{{ModelID: modelID(f.Post), Operator: "and", Children: []compilerir.ScopeExpressionIR{{ModelID: modelID(f.Post), Operator: "eq", FieldID: fieldID(f.PostTitle), Literals: []compilerir.TypedLiteralIR{{Kind: compilerir.LiteralString, Canonical: "video"}}}, {ModelID: modelID(f.Post), Operator: "some", FieldID: fieldID(f.PostComments), RelationID: relationID(f.Commenting), TargetModelID: modelID(f.Comment), Children: []compilerir.ScopeExpressionIR{{ModelID: modelID(f.Comment), Operator: "eq", FieldID: fieldID(f.CommentBody), Parameter: "State"}}}}}}}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{{ModelID: modelID(f.User), ScopeName: "named", Template: compilerir.FactTemplateIR{Name: "ready", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: body.LogicalType()}}, Expression: expression}}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	f.SQLite = ordinaryGuardMust(sqliteprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{}))
	f.PostgreSQL = ordinaryGuardMust(postgresprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{}))
	modelBytes := ordinaryGuardMust(compilerir.CanonicalModel(compilation.Model))
	contractBytes := ordinaryGuardMust(compilerir.CanonicalContract(compilation.Contract))
	model := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), ordinaryGuardDigest(t, string(ordinaryGuardMust(compilerir.ModelFingerprint(compilation.Model)))), modelBytes)
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), ordinaryGuardDigest(t, string(ordinaryGuardMust(compilerir.ContractFingerprint(compilation.Contract)))), contractBytes)
	f.Bundle = golem.GeneratedSchemaBundle(f.Bundle.GenerationDigest(), f.Bundle.GeneratorVersion(), f.Bundle.TemplateABIVersion(), model, contract, ProviderDocument(t, golem.SQLite, f.SQLite), ProviderDocument(t, golem.PostgreSQL, f.PostgreSQL))
	f.Registry = ordinaryGuardMust(schema.New(f.Bundle))
	fixture.Graph = f
	return fixture
}

func ordinaryGuardMust[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
func ordinaryGuardDigest(t testing.TB, text string) golem.SchemaDigest {
	t.Helper()
	data, err := hex.DecodeString(text)
	if err != nil || len(data) != 32 {
		t.Fatalf("invalid schema digest: %v", err)
	}
	var result golem.SchemaDigest
	copy(result[:], data)
	return result
}
