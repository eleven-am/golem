package ir

import "testing"

func TestObjectRowGateRequiresOneModelMemberAndChangesContractIdentity(t *testing.T) {
	base := ContractIR{FormatVersion: ContractFormatVersion, Models: []ModelContractIR{{GraphQLName: "Movie", Exposed: true}}, GraphQLTypes: []GraphQLTypeDefinitionIR{{Kind: GraphQLTypeObject, Name: "Item", Go: GoNamedTypeIR{PackagePath: "example.test/app", Name: "Item"}, Fields: []GraphQLObjectFieldIR{{Name: "media", GoName: "Media", Type: GraphQLTypeIR{Kind: GraphQLTypeModel, Name: "Movie"}}, {Name: "label", GoName: "Label", Type: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "String"}}}}}}
	if err := ValidateGraphQLTypeDefinitions(base); err != nil {
		t.Fatal(err)
	}
	before, err := ContractFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, gate := range []string{"missing", "label"} {
		contract := base
		contract.GraphQLTypes = append([]GraphQLTypeDefinitionIR(nil), base.GraphQLTypes...)
		contract.GraphQLTypes[0].RowGate = gate
		if err := ValidateGraphQLTypeDefinitions(contract); err == nil {
			t.Fatalf("accepted invalid gate %q", gate)
		}
	}
	base.GraphQLTypes[0].RowGate = "media"
	if err := ValidateGraphQLTypeDefinitions(base); err != nil {
		t.Fatal(err)
	}
	after, err := ContractFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("row gate did not change the contract fingerprint")
	}
	base.GraphQLTypes[0].Kind = GraphQLTypeInputObject
	if err := ValidateGraphQLTypeDefinitions(base); err == nil {
		t.Fatal("input object accepted a row gate")
	}
}
