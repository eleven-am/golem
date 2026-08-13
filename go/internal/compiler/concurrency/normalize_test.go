package concurrency

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	testModelID    ir.ModelID = "10000000000000000000000000000000"
	idFieldID      ir.FieldID = "11000000000000000000000000000000"
	versionFieldID ir.FieldID = "12000000000000000000000000000000"
	otherModelID   ir.ModelID = "20000000000000000000000000000000"
	otherFieldID   ir.FieldID = "21000000000000000000000000000000"
)

func TestApplyOwnsExactManifestFieldWithoutNameInference(t *testing.T) {
	compilation := eligibleCompilation()
	declaration := Declaration{ModelID: testModelID, FieldID: versionFieldID, Span: ir.SourceSpan{RelativeFile: "model.go", StartLine: 7}}
	if diagnostics := Apply(&compilation, []Declaration{declaration}); len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	model := compilation.Model.Models[0]
	if model.OptimisticConcurrency == nil || *model.OptimisticConcurrency != versionFieldID {
		t.Fatalf("ModelIR concurrency owner = %#v", model.OptimisticConcurrency)
	}
	contract := compilation.Contract.Models[0]
	if contract.OptimisticConcurrency == nil || *contract.OptimisticConcurrency != versionFieldID {
		t.Fatalf("ContractIR concurrency projection = %#v", contract.OptimisticConcurrency)
	}
	if model.OptimisticConcurrency == contract.OptimisticConcurrency {
		t.Fatal("ModelIR owner and ContractIR projection alias the same pointer")
	}

	undeclared := eligibleCompilation()
	undeclared.Model.Models[0].Fields[1].GoName = "Version"
	if diagnostics := Apply(&undeclared, nil); len(diagnostics) != 0 {
		t.Fatalf("undeclared diagnostics = %#v", diagnostics)
	}
	if undeclared.Model.Models[0].OptimisticConcurrency != nil {
		t.Fatal("field named Version was inferred without an explicit declaration")
	}
}

func TestApplyRejectsEveryIneligibleConcurrencyFieldFact(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ir.CompilationIR, *[]Declaration)
		code   string
	}{
		{name: "unknown model", code: "P1_CONCURRENCY_MODEL", mutate: func(_ *ir.CompilationIR, declarations *[]Declaration) {
			(*declarations)[0].ModelID = "ffffffffffffffffffffffffffffffff"
		}},
		{name: "foreign or missing field", code: "P1_CONCURRENCY_FIELD", mutate: func(_ *ir.CompilationIR, declarations *[]Declaration) { (*declarations)[0].FieldID = otherFieldID }},
		{name: "not scalar", code: "P1_CONCURRENCY_SCALAR", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			field(compilation).Kind, field(compilation).Scalar = ir.FieldRelation, nil
		}},
		{name: "not int64", code: "P1_CONCURRENCY_TYPE", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			field(compilation).Scalar.Type.Kind = ir.TypeString
		}},
		{name: "nullable", code: "P1_CONCURRENCY_NULLABLE", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) { field(compilation).Scalar.Nullable = true }},
		{name: "authored default", code: "P1_CONCURRENCY_DEFAULT", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			field(compilation).Scalar.Default = &ir.DefaultIR{Kind: ir.DefaultLiteral, Producer: ir.ProducerApplication}
		}},
		{name: "generated", code: "P1_CONCURRENCY_GENERATED", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			field(compilation).Scalar.Generation = &ir.GeneratedColumnIR{Storage: ir.GeneratedStored, Provider: ir.ProviderScopePortable}
		}},
		{name: "updated", code: "P1_CONCURRENCY_UPDATED", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) { field(compilation).Scalar.Updated = true }},
		{name: "database read only", code: "P1_CONCURRENCY_DATABASE_READ_ONLY", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			field(compilation).Scalar.DatabaseReadOnly = true
		}},
		{name: "hidden", code: "P1_CONCURRENCY_HIDDEN", mutate: fieldMode(ir.ModeHidden)},
		{name: "write only", code: "P1_CONCURRENCY_WRITE_ONLY", mutate: fieldMode(ir.ModeWriteOnly)},
		{name: "immutable", code: "P1_CONCURRENCY_IMMUTABLE", mutate: fieldMode(ir.ModeImmutable)},
		{name: "read only", code: "P1_CONCURRENCY_READ_ONLY", mutate: fieldMode(ir.ModeReadOnly)},
		{name: "missing read exposure", code: "P1_CONCURRENCY_READ_EXPOSURE", mutate: fieldMode()},
		{name: "missing field contract", code: "P1_CONCURRENCY_CONTRACT_FIELD", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			compilation.Contract.Models[0].Fields = compilation.Contract.Models[0].Fields[:1]
		}},
		{name: "duplicate field contract", code: "P1_CONCURRENCY_CONTRACT_FIELD", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			compilation.Contract.Models[0].Fields = append(compilation.Contract.Models[0].Fields, compilation.Contract.Models[0].Fields[1])
		}},
		{name: "primary identity", code: "P1_CONCURRENCY_KEY", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			compilation.Model.Models[0].PrimaryKey.Fields = []ir.FieldID{versionFieldID}
		}},
		{name: "unique key", code: "P1_CONCURRENCY_KEY", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			compilation.Model.Models[0].Uniques = []ir.KeyIR{{ID: "13000000000000000000000000000000", Kind: ir.KeyUnique, Fields: []ir.FieldID{versionFieldID}}}
		}},
		{name: "foreign key local", code: "P1_CONCURRENCY_FOREIGN_KEY", mutate: localForeignKey},
		{name: "foreign key remote", code: "P1_CONCURRENCY_FOREIGN_KEY", mutate: remoteForeignKey},
		{name: "duplicate declaration", code: "P1_CONCURRENCY_DUPLICATE", mutate: func(_ *ir.CompilationIR, declarations *[]Declaration) {
			*declarations = append(*declarations, (*declarations)[0])
		}},
		{name: "already owned", code: "P1_CONCURRENCY_ALREADY_OWNED", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			value := idFieldID
			compilation.Model.Models[0].OptimisticConcurrency = &value
		}},
		{name: "contract cannot author ownership", code: "P1_CONCURRENCY_CONTRACT_PREOWNED", mutate: func(compilation *ir.CompilationIR, _ *[]Declaration) {
			value := idFieldID
			compilation.Contract.Models[0].OptimisticConcurrency = &value
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compilation := eligibleCompilation()
			declarations := []Declaration{{ModelID: testModelID, FieldID: versionFieldID}}
			test.mutate(&compilation, &declarations)
			diagnostics := Apply(&compilation, declarations)
			if !hasCode(diagnostics, test.code) {
				t.Fatalf("missing %s in %#v", test.code, diagnostics)
			}
		})
	}
}

func TestApplyIsAtomicAndStableAcrossRenameAndReorder(t *testing.T) {
	compilation := eligibleCompilation()
	second := ir.ModelDeclIR{
		ID: otherModelID, Go: ir.GoNamedTypeIR{Name: "Other"}, LogicalName: "Other",
		Fields: []ir.FieldIR{
			{ID: otherFieldID, GoName: "Version", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}},
		},
	}
	compilation.Model.Models = append(compilation.Model.Models, second)
	compilation.Contract.Models = append(compilation.Contract.Models, ir.ModelContractIR{ModelID: otherModelID, Fields: []ir.FieldContractIR{{FieldID: otherFieldID, Modes: []ir.FieldMode{ir.ModeVisible}}}})
	declarations := []Declaration{{ModelID: testModelID, FieldID: versionFieldID}, {ModelID: otherModelID, FieldID: otherFieldID}}
	compilation.Model.Models[1].Fields[0].Scalar.Nullable = true
	if diagnostics := Apply(&compilation, declarations); !hasCode(diagnostics, "P1_CONCURRENCY_NULLABLE") {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	for _, model := range compilation.Model.Models {
		if model.OptimisticConcurrency != nil {
			t.Fatalf("partial ModelIR ownership published for model %s", model.ID)
		}
	}
	for _, contract := range compilation.Contract.Models {
		if contract.OptimisticConcurrency != nil {
			t.Fatalf("partial ContractIR projection published for model %s", contract.ModelID)
		}
	}

	stable := eligibleCompilation()
	stable.Model.Models[0].Fields[0], stable.Model.Models[0].Fields[1] = stable.Model.Models[0].Fields[1], stable.Model.Models[0].Fields[0]
	stable.Model.Models[0].Fields[0].GoName = "Revision"
	if diagnostics := Apply(&stable, []Declaration{{ModelID: testModelID, FieldID: versionFieldID}}); len(diagnostics) != 0 {
		t.Fatalf("rename/reorder diagnostics = %#v", diagnostics)
	}
	if stable.Model.Models[0].OptimisticConcurrency == nil || *stable.Model.Models[0].OptimisticConcurrency != versionFieldID {
		t.Fatalf("rename/reorder changed stable ownership: %#v", stable.Model.Models[0].OptimisticConcurrency)
	}
}

func TestValidateAgreementRejectsMissingDuplicateOrMismatchedProjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ir.CompilationIR)
		code   string
	}{
		{name: "missing", code: "P1_CONCURRENCY_CONTRACT_MISSING", mutate: func(compilation *ir.CompilationIR) {
			compilation.Contract.Models[0].OptimisticConcurrency = nil
		}},
		{name: "orphan", code: "P1_CONCURRENCY_CONTRACT_ORPHAN", mutate: func(compilation *ir.CompilationIR) {
			compilation.Model.Models[0].OptimisticConcurrency = nil
		}},
		{name: "mismatch", code: "P1_CONCURRENCY_CONTRACT_MISMATCH", mutate: func(compilation *ir.CompilationIR) {
			value := idFieldID
			compilation.Contract.Models[0].OptimisticConcurrency = &value
		}},
		{name: "duplicate", code: "P1_CONCURRENCY_CONTRACT_DUPLICATE", mutate: func(compilation *ir.CompilationIR) {
			compilation.Contract.Models = append(compilation.Contract.Models, compilation.Contract.Models[0])
		}},
		{name: "missing contract model", code: "P1_CONCURRENCY_CONTRACT_MISSING", mutate: func(compilation *ir.CompilationIR) {
			compilation.Contract.Models = nil
		}},
		{name: "duplicate model owner", code: "P1_CONCURRENCY_MODEL_PROJECTION", mutate: func(compilation *ir.CompilationIR) {
			compilation.Model.Models = append(compilation.Model.Models, compilation.Model.Models[0])
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compilation := eligibleCompilation()
			if diagnostics := Apply(&compilation, []Declaration{{ModelID: testModelID, FieldID: versionFieldID}}); len(diagnostics) != 0 {
				t.Fatalf("setup diagnostics = %#v", diagnostics)
			}
			test.mutate(&compilation)
			diagnostics := ValidateAgreement(compilation)
			if !hasCode(diagnostics, test.code) {
				t.Fatalf("missing %s in %#v", test.code, diagnostics)
			}
		})
	}

	compilation := eligibleCompilation()
	if diagnostics := Apply(&compilation, []Declaration{{ModelID: testModelID, FieldID: versionFieldID}}); len(diagnostics) != 0 {
		t.Fatalf("setup diagnostics = %#v", diagnostics)
	}
	if diagnostics := ValidateAgreement(compilation); len(diagnostics) != 0 {
		t.Fatalf("exact agreement diagnostics = %#v", diagnostics)
	}
}

func eligibleCompilation() ir.CompilationIR {
	return ir.CompilationIR{
		Model: ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Models: []ir.ModelDeclIR{{
			ID: testModelID, Go: ir.GoNamedTypeIR{Name: "Post"}, LogicalName: "Post",
			Fields: []ir.FieldIR{
				{ID: idFieldID, GoName: "ID", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}},
				{ID: versionFieldID, GoName: "ConcurrencyToken", Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}}},
			},
			PrimaryKey: &ir.KeyIR{ID: "14000000000000000000000000000000", Kind: ir.KeyPrimary, Fields: []ir.FieldID{idFieldID}},
		}}},
		Contract: ir.ContractIR{FormatVersion: ir.ContractFormatVersion, Models: []ir.ModelContractIR{{
			ModelID: testModelID,
			Fields: []ir.FieldContractIR{
				{FieldID: idFieldID, Modes: []ir.FieldMode{ir.ModeVisible}},
				{FieldID: versionFieldID, Modes: []ir.FieldMode{ir.ModeVisible}},
			},
		}}},
	}
}

func field(compilation *ir.CompilationIR) *ir.FieldIR {
	for index := range compilation.Model.Models[0].Fields {
		if compilation.Model.Models[0].Fields[index].ID == versionFieldID {
			return &compilation.Model.Models[0].Fields[index]
		}
	}
	panic("version fixture field missing")
}

func fieldMode(modes ...ir.FieldMode) func(*ir.CompilationIR, *[]Declaration) {
	return func(compilation *ir.CompilationIR, _ *[]Declaration) {
		compilation.Contract.Models[0].Fields[1].Modes = append([]ir.FieldMode(nil), modes...)
	}
}

func localForeignKey(compilation *ir.CompilationIR, _ *[]Declaration) {
	compilation.Model.Relations = []ir.RelationIR{{
		ID: "30000000000000000000000000000000", SourceModel: testModelID, TargetModel: otherModelID,
		LocalFields: []ir.FieldID{versionFieldID}, RemoteFields: []ir.FieldID{otherFieldID}, ForeignKey: &ir.ForeignKeyIR{ID: "31000000000000000000000000000000"},
	}}
}

func remoteForeignKey(compilation *ir.CompilationIR, _ *[]Declaration) {
	compilation.Model.Relations = []ir.RelationIR{{
		ID: "30000000000000000000000000000000", SourceModel: otherModelID, TargetModel: testModelID,
		LocalFields: []ir.FieldID{otherFieldID}, RemoteFields: []ir.FieldID{versionFieldID}, ForeignKey: &ir.ForeignKeyIR{ID: "31000000000000000000000000000000"},
	}}
}

func hasCode(diagnostics []ir.Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestDeclarationContainsOnlyStableIdentityAndSourceEvidence(t *testing.T) {
	typeOf := reflect.TypeOf(Declaration{})
	want := []string{"ModelID", "FieldID", "Span"}
	if typeOf.NumField() != len(want) {
		t.Fatalf("Declaration fields = %d, want %v", typeOf.NumField(), want)
	}
	for index, name := range want {
		if typeOf.Field(index).Name != name {
			t.Fatalf("Declaration field %d = %s, want %s", index, typeOf.Field(index).Name, name)
		}
	}
}
