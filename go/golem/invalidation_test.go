package golem

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func invalidationTestBinder(context.Context, struct{}, struct{}) (Predicate[freezeModel], error) {
	return All[freezeModel](), nil
}

func TestInvalidationBindingsRetainActualIdentityWithoutOperationAuthority(t *testing.T) {
	bind := func(context.Context, struct{}, map[string]any) (RuntimeInvalidationDependency, error) {
		return RuntimeInvalidationRowsValue(freezeDescriptor, All[freezeModel]())
	}
	entry, err := GeneratedInvalidationBinder("query", "dependency", invalidationTestBinder, bind)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(entry.Identity().BinderName, "/golem.invalidationTestBinder") {
		t.Fatalf("identity=%#v", entry.Identity())
	}
	base, err := GeneratedApplicationBindings[struct{}](SchemaDigest{1})
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := GeneratedApplicationInvalidations(base, entry)
	if err != nil {
		t.Fatal(err)
	}
	bound, present := bindings.Invalidation("query", "dependency")
	if !present {
		t.Fatal("missing exact pair")
	}
	value, err := bound.Bind(context.Background(), struct{}{}, nil)
	if err != nil || value.ModelID() != freezeModelID {
		t.Fatalf("value=%#v error=%v", value, err)
	}
	if _, present := value.Predicate(); !present {
		t.Fatal("readable predicate lost")
	}
	if _, _, present := value.FactScope(); present {
		t.Fatal("readable binding acquired fact authority")
	}
	if _, present := bindings.Operation(invalidationTestBinder); present {
		t.Fatal("invalidation granted mutation authority")
	}
	if _, present := bindings.Invalidation("other", "dependency"); present {
		t.Fatal("dependency escaped operation ownership")
	}
	inventory := bindings.InvalidationInventory()
	inventory[0].BinderName = "forged"
	if bindings.InvalidationInventory()[0].BinderName == "forged" {
		t.Fatal("inventory was borrowed")
	}
	if _, err := GeneratedApplicationInvalidations(base, entry, entry); err == nil {
		t.Fatal("duplicate tuple admitted")
	}
	if _, err := GeneratedApplicationInvalidations(ApplicationBindings[struct{}]{}, entry); err == nil {
		t.Fatal("unstamped binding admitted")
	}
	if _, err := GeneratedInvalidationBinder("query", "dependency", nil, bind); err == nil {
		t.Fatal("nil binder admitted")
	}
	if _, err := GeneratedInvalidationBinder[struct{}]("query", "dependency", invalidationTestBinder, nil); err == nil {
		t.Fatal("nil closure admitted")
	}
}

func TestInvalidationFactScopeFreezesWithoutConstructingPolicy(t *testing.T) {
	scope := GeneratedBoundFactScope(freezeModelID, scopeBindingTestID(), scopeBindingTestDigest(), All[freezeModel](), GeneratedTemplateSetArgument("IDs", []int64{1, 2}))
	value, err := RuntimeInvalidationFactsValue(freezeDescriptor, scope)
	if err != nil {
		t.Fatal(err)
	}
	predicate, binding, present := value.FactScope()
	if !present || binding.TemplateID() != scopeBindingTestID() || value.ModelID() != freezeModelID {
		t.Fatal("scope identity was lost")
	}
	if _, present := value.Predicate(); present {
		t.Fatal("scope became caller predicate")
	}
	before := binding.CanonicalBytes()
	copy := binding.CanonicalBytes()
	copy[0] ^= 0xff
	if !bytes.Equal(before, binding.CanonicalBytes()) || len(predicate.CanonicalBytes()) == 0 {
		t.Fatal("scope storage was borrowed")
	}
	if _, err := RuntimeInvalidationFactsValue(freezeDescriptor, BoundFactScope[freezeModel]{}); err == nil {
		t.Fatal("unbound scope admitted")
	}
	if _, err := RuntimeInvalidationRowsValue(ModelDescriptor[freezeModel]{}, All[freezeModel]()); err == nil {
		t.Fatal("zero model admitted")
	}
}
