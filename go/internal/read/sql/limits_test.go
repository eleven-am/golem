package sql

import (
	"errors"
	"strings"
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestStatementComplexityBoundsAreExactAndProviderNeutral(t *testing.T) {
	model := policyir.ModelID{1}
	if err := enforceStatementComplexity(model, strings.Repeat("x", MaxStatementBytes)); err != nil {
		t.Fatalf("exact byte ceiling rejected: %v", err)
	}
	if err := enforceStatementComplexity(model, strings.Repeat("x", MaxStatementBytes+1)); !isRenderFailure(err, "read statement exceeds the provider-neutral byte ceiling") {
		t.Fatalf("byte overflow error=%v", err)
	}
	if err := enforceStatementComplexity(model, strings.Repeat(" AS x", MaxStatementAliases)); err != nil {
		t.Fatalf("exact alias ceiling rejected: %v", err)
	}
	if err := enforceStatementComplexity(model, strings.Repeat(" AS x", MaxStatementAliases+1)); !isRenderFailure(err, "read statement exceeds the provider-neutral alias ceiling") {
		t.Fatalf("alias overflow error=%v", err)
	}
}

func TestConfiguredStatementBoundsAcceptExactBoundaryAndRefuseOverflow(t *testing.T) {
	model := policyir.ModelID{2}
	if err := enforceStatementParameterLimitWith(model, []any{1, 2, 3}, 3); err != nil {
		t.Fatalf("exact configured parameter limit rejected: %v", err)
	}
	if err := enforceStatementParameterLimitWith(model, []any{1, 2, 3, 4}, 3); err == nil {
		t.Fatal("configured parameter overflow accepted")
	}
	if err := enforceStatementComplexityWith(model, strings.Repeat("x", 20), 20, 2); err != nil {
		t.Fatalf("exact configured byte limit rejected: %v", err)
	}
	if err := enforceStatementComplexityWith(model, strings.Repeat("x", 21), 20, 2); err == nil {
		t.Fatal("configured byte overflow accepted")
	}
	if err := enforceStatementComplexityWith(model, "x AS y AS z", 20, 2); err != nil {
		t.Fatalf("exact configured alias limit rejected: %v", err)
	}
	if err := enforceStatementComplexityWith(model, "x AS y AS z AS q", 20, 2); err == nil {
		t.Fatal("configured alias overflow accepted")
	}
}

func isRenderFailure(err error, detail string) bool {
	var failure *Error
	return errors.As(err, &failure) && failure.Code == CodeRender && failure.Detail == detail
}
