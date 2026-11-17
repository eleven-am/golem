package dependency

import (
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"testing"
)

// These low-level execution tests deliberately authorize the whole root domain.
// Actor isolation is covered separately with explicit restricted scopes.
func unrestrictedFactScope(t *testing.T, model ir.ModelID) ir.FactScope {
	t.Helper()
	truth, err := ir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := ir.NewFactScope(truth)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}
