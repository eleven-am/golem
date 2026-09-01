package registry

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestOptimisticConcurrencyNestedRelationSurfaceIsExact(t *testing.T) {
	unversioned := nestedRelationSurface(t, nestedConcurrencyCompilation(""))
	versioned := nestedRelationSurface(t, nestedConcurrencyCompilation("Document"))

	wantUnversioned := []string{
		"golemGeneratedDocumentOwnerMutationRelation.Connect(golem.MutationTarget[User]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.ConnectOrCreate(golem.MutationTarget[User],golem.CreateInput[User]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Create(golem.CreateInput[User]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Delete() (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Disconnect() (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Update(golem.UpdateInput[User]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Upsert(golem.CreateInput[User],golem.UpdateInput[User]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.Connect(golem.MutationTarget[Revision],...golem.MutationTarget[Revision]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.ConnectOrCreate(golem.MutationTarget[Revision],golem.CreateInput[Revision]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.Create(golem.CreateInput[Revision],...golem.CreateInput[Revision]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.CreateMany(golem.CreateInput[Revision],...golem.CreateInput[Revision]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.Delete(golem.MutationTarget[Revision]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.DeleteMany(golem.Predicate[Revision]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.Disconnect(golem.MutationTarget[Revision],...golem.MutationTarget[Revision]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.Set(...golem.MutationTarget[Revision]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.Update(golem.MutationTarget[Revision],golem.UpdateInput[Revision]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.UpdateMany(golem.Predicate[Revision],golem.UpdateManyInput[Revision]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.Upsert(golem.MutationTarget[Revision],golem.CreateInput[Revision],golem.UpdateInput[Revision]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedRevisionDocumentMutationRelation.Connect(golem.MutationTarget[Document]) (golem.NestedValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.ConnectOrCreate(golem.MutationTarget[Document],golem.CreateInput[Document]) (golem.NestedValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.Create(golem.CreateInput[Document]) (golem.NestedValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.Delete() (golem.NestedUpdateValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.Disconnect() (golem.NestedUpdateValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.Update(golem.UpdateInput[Document]) (golem.NestedUpdateValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.Upsert(golem.CreateInput[Document],golem.UpdateInput[Document]) (golem.NestedUpdateValue[Revision])",
	}
	wantVersioned := []string{
		"golemGeneratedDocumentOwnerMutationRelation.Connect(golem.MutationTarget[User]) (golem.NestedCreateValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.ConnectOrCreate(golem.MutationTarget[User],golem.CreateInput[User]) (golem.NestedCreateValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Create(golem.CreateInput[User]) (golem.NestedCreateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.Connect(golem.MutationTarget[Revision],...golem.MutationTarget[Revision]) (golem.NestedCreateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.ConnectOrCreate(golem.MutationTarget[Revision],golem.CreateInput[Revision]) (golem.NestedCreateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.Create(golem.CreateInput[Revision],...golem.CreateInput[Revision]) (golem.NestedCreateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.CreateMany(golem.CreateInput[Revision],...golem.CreateInput[Revision]) (golem.NestedCreateValue[Document])",
		"golemGeneratedRevisionDocumentMutationRelation.Connect(golem.MutationTarget[Document]) (golem.NestedValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.ConnectOrCreate(golem.MutationTarget[Document],golem.CreateInput[Document]) (golem.NestedValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.Create(golem.CreateInput[Document]) (golem.NestedValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.Disconnect() (golem.NestedUpdateValue[Revision])",
	}
	if difference := nestedSurfaceDifference(wantUnversioned, unversioned); difference != "" {
		t.Fatalf("token-free nested relation vocabulary is no longer the full vocabulary:\n%s", difference)
	}
	if difference := nestedSurfaceDifference(wantVersioned, versioned); difference != "" {
		t.Errorf("versioned nested relation surface changed:\n%s", difference)
	}

	wantWithdrawn := map[string][]string{
		"golemGeneratedDocumentOwnerMutationRelation":     {"Delete", "Disconnect", "Update", "Upsert"},
		"golemGeneratedDocumentRevisionsMutationRelation": {"Delete", "DeleteMany", "Disconnect", "Set", "Update", "UpdateMany", "Upsert"},
		"golemGeneratedRevisionDocumentMutationRelation":  {"Delete", "Update", "Upsert"},
	}
	gotWithdrawn := nestedWithdrawnMethods(unversioned, versioned)
	if fmt.Sprint(gotWithdrawn) != fmt.Sprint(wantWithdrawn) {
		t.Errorf("methods withdrawn by the version token changed\ngot:  %v\nwant: %v", gotWithdrawn, wantWithdrawn)
	}
	wantUnion := []string{"Delete", "DeleteMany", "Disconnect", "Set", "Update", "UpdateMany", "Upsert"}
	union := map[string]bool{}
	for _, withdrawn := range gotWithdrawn {
		for _, name := range withdrawn {
			union[name] = true
		}
	}
	names := make([]string, 0, len(union))
	for name := range union {
		names = append(names, name)
	}
	sort.Strings(names)
	if fmt.Sprint(names) != fmt.Sprint(wantUnion) {
		t.Errorf("nested methods withdrawn by a version token = %v; want %v", names, wantUnion)
	}
}

func TestOptimisticConcurrencyRelationOwnerTokenWithdrawsForeignKeyWrites(t *testing.T) {
	unversioned := nestedRelationSurface(t, nestedConcurrencyCompilation(""))
	owned := nestedRelationSurface(t, nestedConcurrencyCompilation("Revision"))

	want := []string{
		"golemGeneratedDocumentOwnerMutationRelation.Connect(golem.MutationTarget[User]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.ConnectOrCreate(golem.MutationTarget[User],golem.CreateInput[User]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Create(golem.CreateInput[User]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Delete() (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Disconnect() (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Update(golem.UpdateInput[User]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentOwnerMutationRelation.Upsert(golem.CreateInput[User],golem.UpdateInput[User]) (golem.NestedUpdateValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.Create(golem.CreateInput[Revision],...golem.CreateInput[Revision]) (golem.NestedValue[Document])",
		"golemGeneratedDocumentRevisionsMutationRelation.CreateMany(golem.CreateInput[Revision],...golem.CreateInput[Revision]) (golem.NestedValue[Document])",
		"golemGeneratedRevisionDocumentMutationRelation.Connect(golem.MutationTarget[Document]) (golem.NestedCreateValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.ConnectOrCreate(golem.MutationTarget[Document],golem.CreateInput[Document]) (golem.NestedCreateValue[Revision])",
		"golemGeneratedRevisionDocumentMutationRelation.Create(golem.CreateInput[Document]) (golem.NestedCreateValue[Revision])",
	}
	if difference := nestedSurfaceDifference(want, owned); difference != "" {
		t.Errorf("owner-token nested relation surface changed:\n%s", difference)
	}

	wantWithdrawn := map[string][]string{
		"golemGeneratedDocumentRevisionsMutationRelation": {"Connect", "ConnectOrCreate", "Delete", "DeleteMany", "Disconnect", "Set", "Update", "UpdateMany", "Upsert"},
		"golemGeneratedRevisionDocumentMutationRelation":  {"Delete", "Disconnect", "Update", "Upsert"},
	}
	gotWithdrawn := nestedWithdrawnMethods(unversioned, owned)
	if fmt.Sprint(gotWithdrawn) != fmt.Sprint(wantWithdrawn) {
		t.Errorf("methods withdrawn by a relation-owner token changed\ngot:  %v\nwant: %v", gotWithdrawn, wantWithdrawn)
	}
	for _, entry := range owned {
		if strings.HasPrefix(entry, "golemGeneratedDocumentOwnerMutationRelation") && strings.Contains(entry, "NestedCreateValue") {
			t.Errorf("token-free relation handle narrowed to a create-only value: %s", entry)
		}
	}
}

func nestedSurfaceDifference(want, got []string) string {
	if fmt.Sprint(want) == fmt.Sprint(got) {
		return ""
	}
	present := map[string]bool{}
	for _, entry := range got {
		present[entry] = true
	}
	var lines []string
	for _, entry := range want {
		if !present[entry] {
			lines = append(lines, "missing: "+entry)
		}
		delete(present, entry)
	}
	unexpected := make([]string, 0, len(present))
	for entry := range present {
		unexpected = append(unexpected, "unexpected: "+entry)
	}
	sort.Strings(unexpected)
	return strings.Join(append(lines, unexpected...), "\n")
}

func nestedWithdrawnMethods(unversioned, versioned []string) map[string][]string {
	kept := map[string]bool{}
	for _, entry := range versioned {
		kept[nestedHandleMethod(entry)] = true
	}
	withdrawn := map[string][]string{}
	for _, entry := range unversioned {
		key := nestedHandleMethod(entry)
		if kept[key] {
			continue
		}
		handle, method, _ := strings.Cut(key, ".")
		withdrawn[handle] = append(withdrawn[handle], method)
	}
	for handle := range withdrawn {
		sort.Strings(withdrawn[handle])
	}
	return withdrawn
}

func nestedHandleMethod(entry string) string {
	if index := strings.IndexByte(entry, '('); index >= 0 {
		return entry[:index]
	}
	return entry
}

func nestedRelationSurface(t *testing.T, compilation ir.CompilationIR) []string {
	t.Helper()
	result, err := modelcodegen.Emit(modelcodegen.Request{
		Compilation: compilation,
		Packages:    []modelcodegen.PackageSpec{{ImportPath: "example.test/app/models", PackageName: "models"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("model files=%d; want 1", len(result.Files))
	}
	file, err := parser.ParseFile(token.NewFileSet(), "models.go", result.Files[0].Source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || len(function.Recv.List) != 1 {
			continue
		}
		receiver := strings.TrimPrefix(registryExpr(t, function.Recv.List[0].Type), "*")
		if !strings.HasPrefix(receiver, "golemGenerated") || !strings.HasSuffix(receiver, "MutationRelation") {
			continue
		}
		methods = append(methods, fmt.Sprintf("%s.%s%s %s", receiver, function.Name.Name,
			registryFieldList(t, function.Type.Params), registryFieldList(t, function.Type.Results)))
	}
	sort.Strings(methods)
	return methods
}

func nestedConcurrencyCompilation(tokenOwner string) ir.CompilationIR {
	documentID, userID, revisionID := ir.ModelID(nestedID(0x101)), ir.ModelID(nestedID(0x102)), ir.ModelID(nestedID(0x103))
	ownerRelation, revisionRelation := ir.RelationID(nestedID(0x110)), ir.RelationID(nestedID(0x111))
	documentVersion := ir.FieldID(nestedID(0x126))
	revisionVersion := ir.FieldID(nestedID(0x144))
	revisionsField := ir.FieldID(nestedID(0x125))
	ownerFK := nestedScalar(0x123, "OwnerID", 2, ir.TypeUUID, true)
	documentFK := nestedScalar(0x142, "DocumentID", 1, ir.TypeUUID, true)
	compilation := ir.CompilationIR{
		Model: ir.ModelIR{
			Models: []ir.ModelDeclIR{
				{ID: documentID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/models", Name: "Document"}, LogicalName: "Document", Fields: []ir.FieldIR{
					nestedScalar(0x121, "ID", 0, ir.TypeUUID, false),
					nestedScalar(0x122, "Title", 1, ir.TypeString, false),
					ownerFK,
					nestedRelation(0x124, "Owner", 3, ownerRelation, ir.RelationSource, ir.RelationBelongsTo),
					nestedRelation(0x125, "Revisions", 4, revisionRelation, ir.RelationInverse, ir.RelationHasMany),
					nestedScalar(0x126, "Version", 5, ir.TypeInt64, false),
				}},
				{ID: userID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/models", Name: "User"}, LogicalName: "User", Fields: []ir.FieldIR{
					nestedScalar(0x131, "ID", 0, ir.TypeUUID, false),
				}},
				{ID: revisionID, Go: ir.GoNamedTypeIR{PackagePath: "example.test/app/models", Name: "Revision"}, LogicalName: "Revision", Fields: []ir.FieldIR{
					nestedScalar(0x141, "ID", 0, ir.TypeUUID, false),
					documentFK,
					nestedRelation(0x143, "Document", 2, revisionRelation, ir.RelationSource, ir.RelationBelongsTo),
					nestedScalar(0x144, "Version", 3, ir.TypeInt64, false),
				}},
			},
			Relations: []ir.RelationIR{
				{ID: ownerRelation, SourceModel: documentID, TargetModel: userID, SourceField: ir.FieldID(nestedID(0x124)), LocalFields: []ir.FieldID{ownerFK.ID}, RemoteFields: []ir.FieldID{ir.FieldID(nestedID(0x131))}},
				{ID: revisionRelation, SourceModel: revisionID, TargetModel: documentID, SourceField: ir.FieldID(nestedID(0x143)), InverseField: &revisionsField, LocalFields: []ir.FieldID{documentFK.ID}, RemoteFields: []ir.FieldID{ir.FieldID(nestedID(0x121))}},
			},
		},
		Contract: ir.ContractIR{Models: []ir.ModelContractIR{
			{ModelID: documentID, Fields: []ir.FieldContractIR{{FieldID: documentVersion, Modes: []ir.FieldMode{ir.ModeVisible}}}, Selectors: []ir.SelectorContractIR{{KeyID: ir.KeyID(nestedID(0x151)), Kind: ir.KeyPrimary, Name: "ByID", Fields: []ir.FieldID{ir.FieldID(nestedID(0x121))}}}},
			{ModelID: userID, Selectors: []ir.SelectorContractIR{{KeyID: ir.KeyID(nestedID(0x152)), Kind: ir.KeyPrimary, Name: "ByID", Fields: []ir.FieldID{ir.FieldID(nestedID(0x131))}}}},
			{ModelID: revisionID, Fields: []ir.FieldContractIR{{FieldID: revisionVersion, Modes: []ir.FieldMode{ir.ModeVisible}}}, Selectors: []ir.SelectorContractIR{{KeyID: ir.KeyID(nestedID(0x153)), Kind: ir.KeyPrimary, Name: "ByID", Fields: []ir.FieldID{ir.FieldID(nestedID(0x141))}}}},
		}},
	}
	switch tokenOwner {
	case "Document":
		compilation.Model.Models[0].OptimisticConcurrency = &documentVersion
		compilation.Contract.Models[0].OptimisticConcurrency = &documentVersion
	case "Revision":
		compilation.Model.Models[2].OptimisticConcurrency = &revisionVersion
		compilation.Contract.Models[2].OptimisticConcurrency = &revisionVersion
	}
	return compilation
}

func nestedScalar(identifier int, name string, order uint32, kind ir.LogicalTypeKind, nullable bool) ir.FieldIR {
	return ir.FieldIR{ID: ir.FieldID(nestedID(identifier)), GoName: name, DeclarationOrder: order, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Type: ir.LogicalTypeIR{Kind: kind}, Nullable: nullable}}
}

func nestedRelation(identifier int, name string, order uint32, relation ir.RelationID, role ir.RelationEndpointRole, kind ir.RelationKind) ir.FieldIR {
	return ir.FieldIR{ID: ir.FieldID(nestedID(identifier)), GoName: name, DeclarationOrder: order, Kind: ir.FieldRelation, Relation: &ir.RelationFieldIR{RelationID: relation, Role: role, Kind: kind}}
}

func nestedID(value int) string { return fmt.Sprintf("%032x", value) }
