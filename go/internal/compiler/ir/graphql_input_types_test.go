package ir

import "testing"

func TestInputPresenceAndOneOfAffectContractIdentityAndValidatePositions(t *testing.T) {
	base := objectContractFixture()
	base.GraphQLTypes[2].Fields[0].Type.Nullable = true
	before, err := ContractFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	base.GraphQLTypes[2].Fields[0].PresenceAware = true
	presence, err := ContractFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	base.GraphQLTypes[2].OneOf = true
	oneOf, err := ContractFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	if before == presence || presence == oneOf {
		t.Fatal("input presence or one-of semantics did not change the contract fingerprint")
	}
	for _, test := range []struct {
		name   string
		mutate func(*ContractIR)
	}{
		{"required presence field", func(c *ContractIR) { c.GraphQLTypes[2].Fields[0].PresenceAware = true }},
		{"required one-of member", func(c *ContractIR) { c.GraphQLTypes[2].OneOf = true }},
		{"output one-of", func(c *ContractIR) { c.GraphQLTypes[0].OneOf = true }},
		{"output presence", func(c *ContractIR) { c.GraphQLTypes[0].Fields[0].PresenceAware = true }},
		{"required presence argument", func(c *ContractIR) { c.CustomOperations[0].Arguments[0].PresenceAware = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := objectContractFixture()
			test.mutate(&c)
			if err := ValidateGraphQLTypeDefinitions(c); err == nil {
				t.Fatal("accepted invalid input metadata")
			}
		})
	}
}
