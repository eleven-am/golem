package compile

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestCompileFactTemplateUsesCompleteSurfaceRetryAndIndependentCapture(t *testing.T) {
	calls := recordInterpretations(t)
	result := Compile(context.Background(), Config{Dir: "testdata/policyfacts", Pattern: "."})
	if result.Compilation == nil || len(result.Diagnostics) != 0 {
		t.Fatalf("fact template diagnostics: %+v", result.Diagnostics)
	}
	if len(*calls) != 2 {
		t.Fatalf("clean generated scope/fact policy surface loaded %d times, want tolerant discovery plus exact retry", len(*calls))
	}
	var fact ir.FactTemplateIR
	var authority ir.ScopeTemplateIR
	for _, owner := range result.Compilation.Contract.Models {
		for _, candidate := range owner.FactTemplates {
			if candidate.Name == "ready" {
				fact = candidate
			}
		}
		for _, candidate := range owner.ScopeTemplates {
			if candidate.Name == "owner" {
				authority = candidate
			}
		}
	}
	if fact.ID == "" || fact.ScopeTemplateID != authority.ID || fact.Fingerprint == "" || len(fact.Parameters) != 2 {
		t.Fatalf("fact body lost static authority/typed ownership: %+v", fact)
	}
	if fact.Expression.Operator != "none" {
		t.Fatal("negative data quantifier was replaced by authority or a truth approximation")
	}
	scopePaths, err := scopecontract.Projection(result.Compilation.Model, authority)
	if err != nil {
		t.Fatal(err)
	}
	bodyPaths, err := scopecontract.FactProjection(result.Compilation.Model, fact)
	if err != nil || len(scopePaths) != 1 || len(bodyPaths) != 3 {
		t.Fatalf("owner All supplied invented data paths or data capture was lost: scope=%d body=%d err=%v", len(scopePaths), len(bodyPaths), err)
	}
	if err := scopecontract.Validate(result.Compilation.Model, result.Compilation.Contract); err != nil {
		t.Fatal(err)
	}
}

func TestCompileFactTemplateRejectsMissingAuthorityCapturesAndHiddenUncapturedModels(t *testing.T) {
	payload, err := os.ReadFile("testdata/policyfacts/schema.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, from, to, extra string }{
		{"missing-authority", `golem.FactTemplate("ready", "owner",`, `golem.FactTemplate("ready", "missing",`, ""},
		{"captured-scalar", "Videos.Height.GT(args.MaxHeight)", "Videos.Height.GT(capturedHeight)", "\nvar capturedHeight int64\n"},
		{"captured-set", "StorageObjects.State.NotIn(args.States...)", "StorageObjects.State.NotIn(capturedStates...)", "\nvar capturedStates []string\n"},
		{"missing-body-subscription", "golem.Subscriptions[StorageObject]()", "golem.ScopedReads[StorageObject]()", ""},
		{"helper", "Videos.Height.GT(args.MaxHeight)", "identityVideo(Videos.Height.GT(args.MaxHeight))", "\nfunc identityVideo(value golem.Predicate[Video]) golem.Predicate[Video] { return value }\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := string(payload)
			if !strings.Contains(source, test.from) {
				t.Fatal("test rewrite did not match")
			}
			source = strings.Replace(source, test.from, test.to, 1) + test.extra
			result := Compile(context.Background(), Config{Dir: writePolicyScopeFixture(t, source), Pattern: "."})
			if result.Compilation != nil || len(result.Diagnostics) == 0 {
				t.Fatal("unsupported fact template compiled")
			}
			for _, diagnostic := range result.Diagnostics {
				if diagnostic.Code == "P1_METHOD_TYPECHECK" {
					t.Fatalf("fixture failed source type checking instead of static template validation: %+v", result.Diagnostics)
				}
			}
		})
	}
}
