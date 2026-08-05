package compile

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/methods"
	"github.com/eleven-am/golem/go/internal/compiler/schema"
)

func TestCompleteSocialFixture(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/social", Pattern: ".", Root: "DefineSchema"})
	if len(result.Diagnostics) != 0 {
		t.Fatalf("diagnostics: %#v", result.Diagnostics)
	}
	if result.Compilation == nil {
		t.Fatal("successful compile returned nil CompilationIR")
	}
	model := result.Compilation.Model
	if len(model.Models) != 6 || len(model.Relations) != 8 {
		t.Fatalf("models=%d relations=%d", len(model.Models), len(model.Relations))
	}
	if len(model.Providers) != 2 || model.Providers[0] != ir.SQLite || model.Providers[1] != ir.PostgreSQL {
		t.Fatalf("providers: %#v", model.Providers)
	}
	assertCompositePrimary(t, model, "Friendship")
	assertCompositePrimary(t, model, "PostTag")
	var recursive bool
	for _, relation := range model.Relations {
		if relation.SourceModel == relation.TargetModel && relation.Name == "ReplyTree" {
			recursive = true
		}
	}
	if !recursive {
		t.Fatal("missing recursive comment relation")
	}
	var advanced, cascade bool
	var postID ir.ModelID
	for _, entry := range model.Models {
		if entry.LogicalName != "Post" {
			continue
		}
		postID = entry.ID
		var titleID ir.FieldID
		for _, field := range entry.Fields {
			if field.GoName == "Title" {
				titleID = field.ID
			}
			if field.GoName == "Search" && field.Scalar != nil && field.Scalar.Generation != nil && field.Scalar.DatabaseReadOnly {
				expr := field.Scalar.Generation.Expr
				advanced = expr.ResultType.Kind == ir.TypeString && expr.Deterministic && expr.Volatility == ir.SchemaVolatilityImmutable && len(expr.ReferencedFields) == 1
			}
		}
		if len(entry.Checks) != 1 || len(entry.Uniques) == 0 || len(entry.Indexes) < 3 {
			t.Fatalf("advanced Post declarations missing: %#v", entry)
		}
		foundExpression, foundPostgres := false, false
		for _, index := range entry.Indexes {
			if index.PhysicalName == "idx_posts_lower_title" {
				foundExpression = len(index.Keys) == 1 && index.Keys[0].Expr != nil && index.Predicate != nil && len(index.Predicate.ReferencedFields) == 1 && index.Predicate.ReferencedFields[0] == titleID
			}
			foundPostgres = foundPostgres || index.PhysicalName == "idx_posts_title_pg" && index.Provider == ir.ProviderScopePostgreSQL
		}
		if !foundExpression || !foundPostgres || entry.Checks[0].Predicate.CanonicalIdentity == "" || len(entry.Checks[0].Predicate.ReferencedFields) != 1 || entry.Checks[0].Predicate.ReferencedFields[0] != titleID {
			t.Fatal("missing normalized advanced index/check nodes")
		}
	}
	for _, edge := range model.Relations {
		cascade = cascade || edge.SourceModel == postID && edge.Name == "Author" && edge.ForeignKey != nil && edge.ForeignKey.OnDelete == ir.ActionCascade
	}
	if !advanced || !cascade {
		t.Fatalf("advanced generated/action nodes missing: generated=%v cascade=%v", advanced, cascade)
	}

	golden, err := os.ReadFile("testdata/social.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := json.MarshalIndent(struct {
		Models              int            `json:"models"`
		Relations           int            `json:"relations"`
		ModelFingerprint    ir.Fingerprint `json:"modelFingerprint"`
		ContractFingerprint ir.Fingerprint `json:"contractFingerprint"`
	}{len(model.Models), len(model.Relations), result.ModelFingerprint, result.ContractFingerprint}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	summary = append(summary, '\n')
	if string(summary) != string(golden) {
		t.Fatalf("golden mismatch\nwant: %s\n got: %s", golden, summary)
	}
}

func TestCompileRawDeterministicUnderShuffledDeclarations(t *testing.T) {
	extracted := schema.Extract(context.Background(), schema.Config{Dir: "testdata/social", Pattern: ".", Root: "DefineSchema"})
	if len(extracted.Diagnostics) != 0 {
		t.Fatalf("extract diagnostics: %#v", extracted.Diagnostics)
	}
	// CompileRaw is source-independent and intentionally does not interpret
	// GolemModel; supply the Tag relation identity as a common declaration.
	for modelIndex := range extracted.Raw.Models {
		if extracted.Raw.Models[modelIndex].GoName != "Tag" {
			continue
		}
		for fieldIndex := range extracted.Raw.Models[modelIndex].Fields {
			if extracted.Raw.Models[modelIndex].Fields[fieldIndex].GoName == "Name" {
				extracted.Raw.Models[modelIndex].Fields[fieldIndex].GolemAttrs = append(extracted.Raw.Models[modelIndex].Fields[fieldIndex].GolemAttrs, ir.RawAttribute{Name: "unique"})
			}
		}
	}
	var baseline []byte
	for seed := int64(0); seed < 30; seed++ {
		raw := extracted.Raw
		raw.Models = append([]ir.RawModelDecl(nil), raw.Models...)
		raw.Root.Models = append([]ir.RawModelRef(nil), raw.Root.Models...)
		random := rand.New(rand.NewSource(seed))
		random.Shuffle(len(raw.Models), func(i, j int) { raw.Models[i], raw.Models[j] = raw.Models[j], raw.Models[i] })
		random.Shuffle(len(raw.Root.Models), func(i, j int) { raw.Root.Models[i], raw.Root.Models[j] = raw.Root.Models[j], raw.Root.Models[i] })
		result := CompileRaw(raw)
		if len(result.Diagnostics) != 0 {
			t.Fatalf("seed %d diagnostics: %#v", seed, result.Diagnostics)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if seed == 0 {
			baseline = encoded
		} else if string(encoded) != string(baseline) {
			t.Fatalf("seed %d changed canonical output", seed)
		}
	}
}

func TestAdvancedSourceCompileIsByteStable(t *testing.T) {
	left := Compile(context.Background(), Config{Dir: "testdata/social", Pattern: "."})
	right := Compile(context.Background(), Config{Dir: "testdata/social", Pattern: "."})
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	if string(leftJSON) != string(rightJSON) {
		t.Fatal("advanced compile/inspect payload is not byte stable")
	}
}

func TestProviderScopedRelationOptionsAreRejected(t *testing.T) {
	model := ir.ModelIR{Relations: []ir.RelationIR{{ID: "relation", ForeignKey: &ir.ForeignKeyIR{ID: "fk"}}}}
	action := ir.ActionCascade
	diagnostics := applyRelationOptions(&model, []methods.RelationOptionDeclaration{{RelationID: "relation", OnDelete: &action, Provider: ir.ProviderScopePostgreSQL}})
	if !containsCode(diagnostics, "P1_RELATION_OPTION_PROVIDER_UNSUPPORTED") || model.Relations[0].ForeignKey.OnDelete == action {
		t.Fatalf("provider-specific relation option was not atomically rejected: %#v", diagnostics)
	}
}

func TestRelationActionRequirementsAndAtomicity(t *testing.T) {
	model := ir.ModelIR{
		Models:    []ir.ModelDeclIR{{ID: "source", Fields: []ir.FieldIR{{ID: "local", Scalar: &ir.ScalarFieldIR{Nullable: false}}}}},
		Relations: []ir.RelationIR{{ID: "relation", SourceModel: "source", SourceField: "relation-field", LocalFields: []ir.FieldID{"local"}, ForeignKey: &ir.ForeignKeyIR{ID: "fk", OnDelete: ir.ActionNoAction}}},
	}
	setNull, cascade := ir.ActionSetNull, ir.ActionCascade
	options := []methods.RelationOptionDeclaration{
		{ModelID: "source", RelationID: "relation", RelationField: "relation-field", OnDelete: &cascade, Provider: ir.ProviderScopePortable},
		{ModelID: "source", RelationID: "relation", RelationField: "relation-field", OnUpdate: &setNull, Provider: ir.ProviderScopePortable},
	}
	diagnostics := applyRelationOptions(&model, options)
	if !containsCode(diagnostics, "P1_RELATION_OPTION_DUPLICATE") || !containsCode(diagnostics, "P1_RELATION_SET_NULL") || model.Relations[0].ForeignKey.OnDelete != ir.ActionNoAction {
		t.Fatalf("invalid batch was not rejected atomically: %#v", diagnostics)
	}
}

func TestInvalidCompilationIsAtomic(t *testing.T) {
	result := Compile(context.Background(), Config{Dir: "testdata/invalid", Pattern: ".", Root: "DefineSchema"})
	if result.Compilation != nil || result.ModelFingerprint != "" || result.ContractFingerprint != "" {
		t.Fatalf("invalid input exposed partial accepted output: %#v", result)
	}
	if !containsCode(result.Diagnostics, "P1_PRIMARY_KEY_MISSING") {
		t.Fatalf("missing complete-validation diagnostic: %#v", result.Diagnostics)
	}
}

func assertCompositePrimary(t *testing.T, model ir.ModelIR, logicalName string) {
	t.Helper()
	for _, entry := range model.Models {
		if entry.LogicalName == logicalName {
			if entry.PrimaryKey == nil || len(entry.PrimaryKey.Fields) != 2 {
				t.Fatalf("%s primary key: %#v", logicalName, entry.PrimaryKey)
			}
			return
		}
	}
	t.Fatalf("missing model %s", logicalName)
}

func containsCode(diagnostics []ir.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
