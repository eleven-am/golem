package phase0

import "fmt"

type Provider string

const (
	SQLite     Provider = "sqlite"
	PostgreSQL Provider = "postgresql"
)

// Phase0Operators is the complete expression vocabulary accepted by this
// spike. Every entry is required to have equivalent SQLite and PostgreSQL
// semantics before a later phase may expose its SQL compiler.
var Phase0Operators = []Operator{
	OpAll,
	OpNone,
	OpEqual,
	OpNotEqual,
	OpIn,
	OpAnd,
	OpOr,
	OpNot,
	OpRelationIs,
	OpRelationIsNot,
	OpRelationSome,
	OpRelationEvery,
	OpRelationNone,
}

var phase0Support = map[Provider]map[Operator]bool{
	SQLite:     {},
	PostgreSQL: {},
}

func init() {
	for _, operator := range Phase0Operators {
		phase0Support[SQLite][operator] = true
		phase0Support[PostgreSQL][operator] = true
	}
}

func Supports(provider Provider, operator Operator) bool {
	return phase0Support[provider][operator]
}

func ValidateProviderParity() error {
	for _, operator := range Phase0Operators {
		for _, provider := range []Provider{SQLite, PostgreSQL} {
			if !Supports(provider, operator) {
				return fmt.Errorf("phase 0 operator %q is not supported by %s", operator, provider)
			}
		}
	}
	return nil
}
