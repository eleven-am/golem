package ir

import (
	"bytes"
	"testing"
)

func invalidationContractFixture() ContractIR {
	actor := GoNamedTypeIR{PackagePath: "example.test/app", Name: "Actor"}
	operation := CustomOperationContractIR{ExtensionID: "operation", Name: "feed", Operation: CustomOperationQuery, Resolver: AttachedMethodIR{PackagePath: actor.PackagePath, Name: "Feed", Kind: "customquery"}, Result: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "String"}, Arguments: []GraphQLArgumentContractIR{{Name: "take", Type: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "Int", Nullable: true}}}}
	operation.Invalidation = &CustomInvalidationIR{SubscriptionField: "feedChanges", Dependencies: []CustomInvalidationDependencyIR{{ID: CustomInvalidationDependencyID(operation.ExtensionID, "source"), Name: "source", Kind: CustomInvalidationReadableRows, ModelID: "model", Binder: AttachedMethodIR{PackagePath: actor.PackagePath, Name: "BindSource", Kind: "invalidation", Actor: &actor}, Fields: []FieldID{"first", "second"}}}}
	return ContractIR{FormatVersion: ContractFormatVersion, CustomOperations: []CustomOperationContractIR{operation}, Models: []ModelContractIR{{ModelID: "model", Subscriptions: true, Fields: []FieldContractIR{{FieldID: "first"}, {FieldID: "second"}}}}}
}

func TestInvalidationIdentityBoundsArgumentsAndCapabilities(t *testing.T) {
	base := invalidationContractFixture()
	if err := ValidateGraphQLInvalidations(base); err != nil {
		t.Fatal(err)
	}
	if base.RequiredClientManifestVersion() != 3 || len(base.RequiredClientCapabilities()) != 1 {
		t.Fatal("missing required capability")
	}
	if len(base.CustomOperations[0].InvalidationArguments()) != 1 {
		t.Fatal("ordinary take was dropped")
	}
	base.CustomOperations[0].Pagination = &CustomPaginationIR{}
	if len(base.CustomOperations[0].InvalidationArguments()) != 0 {
		t.Fatal("page take entered invalidation arguments")
	}
	for name, mutate := range map[string]func(*ContractIR){
		"mutation":       func(c *ContractIR) { c.CustomOperations[0].Operation = CustomOperationMutation },
		"unknown fields": func(c *ContractIR) { c.CustomOperations[0].Invalidation.Dependencies[0].Fields = []FieldID{"wrong"} },
		"duplicates": func(c *ContractIR) {
			c.CustomOperations[0].Invalidation.Dependencies = append(c.CustomOperations[0].Invalidation.Dependencies, c.CustomOperations[0].Invalidation.Dependencies[0])
		},
		"wrong identity": func(c *ContractIR) { c.CustomOperations[0].Invalidation.Dependencies[0].ID = "wrong" },
		"incomplete actor": func(c *ContractIR) {
			c.CustomOperations[0].Invalidation.Dependencies[0].Binder.Actor.Name = ""
		},
		"dependency ceiling": func(c *ContractIR) {
			dependency := c.CustomOperations[0].Invalidation.Dependencies[0]
			c.CustomOperations[0].Invalidation.Dependencies = make([]CustomInvalidationDependencyIR, 33)
			for i := range c.CustomOperations[0].Invalidation.Dependencies {
				c.CustomOperations[0].Invalidation.Dependencies[i] = dependency
			}
		},
		"field ceiling": func(c *ContractIR) {
			c.CustomOperations[0].Invalidation.Dependencies[0].Fields = make([]FieldID, 65)
		},
		"unobservable": func(c *ContractIR) { c.Models[0].Subscriptions = false },
		"missing scope": func(c *ContractIR) {
			d := &c.CustomOperations[0].Invalidation.Dependencies[0]
			d.Kind = CustomInvalidationPolicyFacts
			d.Fields = nil
			d.ScopeTemplateID = "absent"
			d.FactTemplateIDs = []FactTemplateID{"fact"}
		},
		"JSON": func(c *ContractIR) {
			c.CustomOperations[0].Arguments[0].Type = GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "JSON"}
		},
		"empty": func(c *ContractIR) { c.CustomOperations[0].Invalidation.Dependencies = nil },
	} {
		t.Run(name, func(t *testing.T) {
			c := invalidationContractFixture()
			mutate(&c)
			if ValidateGraphQLInvalidations(c) == nil {
				t.Fatal("invalid declaration admitted")
			}
		})
	}
}

func TestInvalidationCanonicalOrderingAndFingerprintOwnDependencyContract(t *testing.T) {
	left, right := invalidationContractFixture(), invalidationContractFixture()
	right.CustomOperations[0].Invalidation.Dependencies[0].Fields = []FieldID{"second", "first"}
	a, err := CanonicalContract(left)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalContract(right)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("field ordering changed canonical contract")
	}
	right.CustomOperations[0].Invalidation.Dependencies[0].Binder.Name = "OtherBinder"
	b, err = CanonicalContract(right)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("binder identity did not change canonical contract")
	}
	if right.CustomOperations[0].Invalidation.Dependencies[0].Fields[0] != "second" {
		t.Fatal("canonicalization mutated input")
	}
}
