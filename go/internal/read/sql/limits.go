package sql

import (
	"strings"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// MaxStatementParameters is the shared provider-neutral ceiling for every P3
// read statement. It deliberately targets legacy SQLite's 999-variable limit;
// PostgreSQL's higher protocol ceiling must not make a plan non-portable.
const MaxStatementParameters = 999

// MaxStatementBytes and MaxStatementAliases bound the two pieces of statement
// complexity that are independent of bind count. The byte ceiling prevents a
// deeply expanded authorized predicate from producing an arbitrarily large SQL
// string, while the alias ceiling prevents pathological relation/policy trees
// from exhausting provider parser resources. Both limits are deliberately
// provider-neutral so a request accepted for SQLite cannot fail only because it
// was rendered for PostgreSQL (or vice versa).
const (
	MaxStatementBytes   = 1 << 20
	MaxStatementAliases = 2_048
)

func enforceStatementParameterLimitWith(model policyir.ModelID, args []any, maximum int) error {
	if maximum > 0 && maximum <= MaxStatementParameters && len(args) <= maximum {
		return nil
	}
	return fail(CodeRender, model, policyir.FieldID{}, "read statement exceeds the provider-neutral parameter ceiling", nil)
}

func enforceStatementComplexity(model policyir.ModelID, statement string) error {
	return ValidateStatementComplexity(model, statement, MaxStatementBytes, MaxStatementAliases)
}

// ValidateStatementComplexity applies the configured provider-neutral SQL size limits.
func ValidateStatementComplexity(model policyir.ModelID, statement string, maximumBytes, maximumAliases int) error {
	if maximumBytes <= 0 || maximumBytes > MaxStatementBytes || len(statement) > maximumBytes {
		return fail(CodeRender, model, policyir.FieldID{}, "read statement exceeds the provider-neutral byte ceiling", nil)
	}
	// Every table/subquery and projected decode column emitted by the closed P3
	// renderer introduces an AS token. CAST expressions do too, making this a
	// deliberately conservative SQL-complexity bound rather than a parser-based
	// count of aliases. It remains deterministic and independent of quoting.
	if maximumAliases <= 0 || maximumAliases > MaxStatementAliases || strings.Count(statement, " AS ") > maximumAliases {
		return fail(CodeRender, model, policyir.FieldID{}, "read statement exceeds the provider-neutral alias ceiling", nil)
	}
	return nil
}
