package ir

import "testing"

func pageContractFixture() ContractIR {
	page := CustomPaginationIR{DefaultTake: 25, MaxTake: 100, MaxCandidates: 200, Revision: "rank-v1", Item: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "String"}}
	return ContractIR{DataIncarnation: &DataIncarnationContractIR{Kind: "provider", Version: 1}, GraphQLTypes: []GraphQLTypeDefinitionIR{GraphQLPageDefinition("FeedPage", page)}, CustomOperations: []CustomOperationContractIR{{
		Name: "feed", Operation: CustomOperationQuery, Pagination: &page, Result: GraphQLTypeIR{Kind: GraphQLTypeObject, Name: "FeedPage"},
		Arguments: []GraphQLArgumentContractIR{
			{Name: "take", Type: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "Int", Nullable: true}},
			{Name: "after", Type: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "String", Nullable: true}},
		},
	}}}
}

func TestGraphQLPagesAreExplicitOwnedDefinitions(t *testing.T) {
	contract := pageContractFixture()
	if err := ValidateGraphQLTypeDefinitions(contract); err != nil {
		t.Fatal(err)
	}
	if contract.RequiredClientManifestVersion() != 3 {
		t.Fatal("page requires manifest 3")
	}
	for name, mutate := range map[string]func(*ContractIR){
		"orphan":             func(c *ContractIR) { c.CustomOperations = nil },
		"ordinary operation": func(c *ContractIR) { c.CustomOperations[0].Pagination = nil },
		"mutation":           func(c *ContractIR) { c.CustomOperations[0].Operation = CustomOperationMutation },
		"nullable wrapper":   func(c *ContractIR) { c.CustomOperations[0].Result.Nullable = true },
		"fake Go witness": func(c *ContractIR) {
			c.GraphQLTypes[0].Go = GoNamedTypeIR{PackagePath: "example.test/feed", Name: "FeedPage"}
		},
		"wrong items bound": func(c *ContractIR) { c.GraphQLTypes[0].Fields[0].Type.MaxItems++ },
		"wrong next":        func(c *ContractIR) { c.GraphQLTypes[0].Fields[1].Type.Nullable = false },
		"missing after":     func(c *ContractIR) { c.CustomOperations[0].Arguments = c.CustomOperations[0].Arguments[:1] },
		"wrong take type":   func(c *ContractIR) { c.CustomOperations[0].Arguments[0].Type.Name = "String" },
		"computed result": func(c *ContractIR) {
			c.Models = []ModelContractIR{{Computed: []ComputedFieldContractIR{{Result: c.CustomOperations[0].Result}}}}
		},
		"nested page": func(c *ContractIR) {
			c.GraphQLTypes = append(c.GraphQLTypes, GraphQLTypeDefinitionIR{Kind: GraphQLTypeObject, Name: "Envelope", Go: GoNamedTypeIR{PackagePath: "example.test/feed", Name: "Envelope"}, Fields: []GraphQLObjectFieldIR{{Name: "page", GoName: "Page", Type: c.CustomOperations[0].Result}}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := pageContractFixture()
			mutate(&value)
			if err := ValidateGraphQLTypeDefinitions(value); err == nil {
				t.Fatal("invalid page accepted")
			}
		})
	}
}

func TestPageBoundsAndArgumentCanonicalizationAdmission(t *testing.T) {
	for _, limits := range [][3]uint32{{0, 1, 1}, {2, 1, 1}, {1, 1001, 1001}, {1, 2, 1}, {1, 1, 10001}} {
		page := *pageContractFixture().CustomOperations[0].Pagination
		page.DefaultTake, page.MaxTake, page.MaxCandidates = limits[0], limits[1], limits[2]
		if err := page.Validate(); err == nil {
			t.Fatalf("accepted limits %v", limits)
		}
	}
	definitions := []GraphQLTypeDefinitionIR{{Name: "Filter", Kind: GraphQLTypeInputObject, Fields: []GraphQLObjectFieldIR{{Name: "metadata", Type: GraphQLTypeIR{Kind: GraphQLTypeScalar, Name: "JSON"}}}}}
	args := []GraphQLArgumentContractIR{{Name: "filter", Type: GraphQLTypeIR{Kind: GraphQLTypeList, Element: &GraphQLTypeIR{Kind: GraphQLTypeInputObject, Name: "Filter"}}}}
	if err := ValidatePageArguments(args, definitions); err == nil {
		t.Fatal("nested JSON admitted without canonical codec")
	}
	definitions[0].Fields[0].Type.Name = "BigInt"
	if err := ValidatePageArguments(args, definitions); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []GraphQLTypeKind{GraphQLTypePredicate, GraphQLTypeSelector, GraphQLTypeCreateInput, GraphQLTypeUpdateInput, GraphQLTypeUpdateManyInput} {
		if IsPageArgumentType(GraphQLTypeIR{Kind: kind, Name: "User"}) {
			t.Fatalf("admitted %s", kind)
		}
	}
}

func TestPageRevisionAndLimitsParticipateInContractFingerprint(t *testing.T) {
	left := pageContractFixture()
	left.FormatVersion = ContractFormatVersion
	first, err := ContractFingerprint(left)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ContractIR){
		"revision":          func(c *ContractIR) { c.CustomOperations[0].Pagination.Revision = "rank-v2" },
		"candidate ceiling": func(c *ContractIR) { c.CustomOperations[0].Pagination.MaxCandidates++ },
		"default take":      func(c *ContractIR) { c.CustomOperations[0].Pagination.DefaultTake++ },
	} {
		t.Run(name, func(t *testing.T) {
			value := pageContractFixture()
			value.FormatVersion = ContractFormatVersion
			mutate(&value)
			fingerprint, err := ContractFingerprint(value)
			if err != nil {
				t.Fatal(err)
			}
			if fingerprint == first {
				t.Fatal("page contract change did not change fingerprint")
			}
		})
	}
	ordered := pageContractFixture()
	ordered.FormatVersion = ContractFormatVersion
	ordered.GraphQLTypes[0].Fields[0], ordered.GraphQLTypes[0].Fields[1] = ordered.GraphQLTypes[0].Fields[1], ordered.GraphQLTypes[0].Fields[0]
	ordered.CustomOperations[0].Arguments[0], ordered.CustomOperations[0].Arguments[1] = ordered.CustomOperations[0].Arguments[1], ordered.CustomOperations[0].Arguments[0]
	second, err := ContractFingerprint(ordered)
	if err != nil || first != second {
		t.Fatalf("field order changed canonical page: %s %s %v", first, second, err)
	}
	if left.CustomOperations[0].Arguments[0].Name != "take" {
		t.Fatal("canonicalization mutated source arguments")
	}
}
