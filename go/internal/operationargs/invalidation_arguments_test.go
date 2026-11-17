package operationargs

import (
	"bytes"
	"errors"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func invalidationArgumentFixture(page bool) compilerir.CompilationIR {
	actor := compilerir.GoNamedTypeIR{PackagePath: "example.test/arguments", Name: "Actor"}
	model := compilerir.ModelID("00000000000000000000000000000001")
	field := compilerir.FieldID("00000000000000000000000000000002")
	item := canonicalScalar("Int", false)
	operation := compilerir.CustomOperationContractIR{ExtensionID: "feed", Operation: compilerir.CustomOperationQuery, Name: "feed", Result: item, Resolver: compilerir.AttachedMethodIR{PackagePath: actor.PackagePath, Name: "Feed", Kind: "customquery"}, Capability: compilerir.CustomOperationCallerOnly, Arguments: []compilerir.GraphQLArgumentContractIR{{Name: "values", Type: compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &item}}, {Name: "take", Type: canonicalScalar("Int", true)}, {Name: "after", Type: canonicalScalar("String", true)}}}
	operation.Invalidation = &compilerir.CustomInvalidationIR{SubscriptionField: "feedChanges", Dependencies: []compilerir.CustomInvalidationDependencyIR{{ID: compilerir.CustomInvalidationDependencyID(operation.ExtensionID, "rows"), Name: "rows", Kind: compilerir.CustomInvalidationReadableRows, ModelID: model, Fields: []compilerir.FieldID{field}, Binder: compilerir.AttachedMethodIR{PackagePath: actor.PackagePath, Name: "Rows", Kind: "invalidation", Actor: &actor}}}}
	var compilation compilerir.CompilationIR
	compilation.Model.Schema.PackagePath = actor.PackagePath
	compilation.Contract.Models = []compilerir.ModelContractIR{{ModelID: model, Subscriptions: true, Fields: []compilerir.FieldContractIR{{FieldID: field}}}}
	if page {
		compilation.Contract.DataIncarnation = &compilerir.DataIncarnationContractIR{Kind: "provider", Version: 1}
		pagination := compilerir.CustomPaginationIR{DefaultTake: 25, MaxTake: 100, MaxCandidates: 100, Revision: "r1", Item: item}
		operation.Pagination = &pagination
		operation.Result = compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeObject, Name: "FeedPage"}
		compilation.Contract.GraphQLTypes = []compilerir.GraphQLTypeDefinitionIR{compilerir.GraphQLPageDefinition("FeedPage", pagination)}
	}
	compilation.Contract.CustomOperations = []compilerir.CustomOperationContractIR{operation}
	return compilation
}

func TestPrepareInvalidationPreservesQueryArgumentsAndBoundsIngress(t *testing.T) {
	registry, err := New(invalidationArgumentFixture(false))
	if err != nil {
		t.Fatal(err)
	}
	values := []any{int32(1), int32(2)}
	request, err := registry.PrepareInvalidation("feed", map[string]any{"values": values, "take": int32(7), "after": "ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	values[0] = int32(99)
	args := request.Arguments()
	if args[0].Value().([]any)[0] != int32(1) || args[1].Value() != int32(7) || args[2].Value() != "ordinary" {
		t.Fatal("ordinary args lost ownership or paging-like names")
	}
	for _, prepare := range []func(map[string]any) (Request, error){func(v map[string]any) (Request, error) { return registry.PrepareInvalidation("feed", v) }, func(v map[string]any) (Request, error) {
		return registry.Prepare(compilerir.CustomOperationQuery, "feed", v)
	}} {
		if _, err := prepare(map[string]any{"values": make([]any, MaxCanonicalArgumentNodes+1)}); !errors.Is(err, ErrCanonicalArgumentsLimit) {
			t.Fatalf("opted query bypassed input bound: %v", err)
		}
	}
}

func TestPrepareInvalidationPageExcludesTraversalInputs(t *testing.T) {
	registry, err := New(invalidationArgumentFixture(true))
	if err != nil {
		t.Fatal(err)
	}
	request, err := registry.PrepareInvalidation("feed", map[string]any{"values": []any{int32(1)}})
	if err != nil {
		t.Fatal(err)
	}
	if request.Arguments()[1].Value() != int32(25) {
		t.Fatal("default take missing from preparation")
	}
	before, err := registry.CanonicalArguments(request, "take", "after")
	if err != nil {
		t.Fatal(err)
	}
	other, err := registry.PrepareCanonical(compilerir.CustomOperationQuery, "feed", map[string]any{"values": []any{int32(1)}, "take": int32(90), "after": "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := registry.CanonicalArguments(other, "take", "after")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("traversal entered operation identity: %v", err)
	}
	for _, name := range []string{"take", "after"} {
		if _, err := registry.PrepareInvalidation("feed", map[string]any{"values": []any{int32(1)}, name: nil}); err == nil {
			t.Fatalf("accepted page %s", name)
		}
	}
}

func TestInvalidationRegistryOwnsDeclarationInventory(t *testing.T) {
	input := invalidationArgumentFixture(false)
	registry, err := New(input)
	if err != nil {
		t.Fatal(err)
	}
	original := input.Contract.CustomOperations[0].Invalidation.Dependencies[0]
	input.Contract.CustomOperations[0].Invalidation.Dependencies[0].Fields[0] = "changed"
	input.Contract.CustomOperations[0].Invalidation.Dependencies[0].Binder.Actor.Name = "Changed"
	input.Contract.CustomOperations[0].Invalidation.SubscriptionField = "changed"
	for attempt := 0; attempt < 2; attempt++ {
		operation, _ := registry.Contract("feed")
		if operation.Invalidation.SubscriptionField != "feedChanges" || operation.Invalidation.Dependencies[0].Fields[0] == "changed" || operation.Invalidation.Dependencies[0].Binder.Actor.Name != "Actor" {
			t.Fatal("registry aliases input/output declaration")
		}
		operation.Invalidation.Dependencies[0].Fields[0] = "changed"
		operation.Invalidation.Dependencies[0].Binder.Actor.Name = "Changed"
		inventory := registry.Operations()
		inventory[0].Invalidation.SubscriptionField = "changed"
	}
	// Fact-template inventory is an independent owned slice in the same clone.
	operation, _ := registry.Contract("feed")
	operation.Invalidation.Dependencies[0].FactTemplateIDs = []compilerir.FactTemplateID{"a", "b"}
	cloned := cloneOperation(operation)
	cloned.Invalidation.Dependencies[0].FactTemplateIDs[0] = "c"
	if operation.Invalidation.Dependencies[0].FactTemplateIDs[0] != "a" || original.ID == "" {
		t.Fatal("fact template list aliases clone")
	}
}
