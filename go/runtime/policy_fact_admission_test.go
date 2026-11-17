package runtime

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

// Run unchanged in both actual binary modes. Neither a static inventory alone
// nor a compiled writer alone can opt an application into named-fact policies.
func TestPolicyFactRuntimeAdmissionRequiresActualHistoricalWriterAndCapture(t *testing.T) {
	for _, captured := range []bool{false, true} {
		name := "without-capture"
		if captured {
			name = "registered-capture"
		}
		for _, variant := range []string{"related-body", "unused-definition", "scalar-body"} {
			t.Run(name+"/"+variant, func(t *testing.T) {
				options := policyFactRuntimeOptions{unusedFact: variant == "unused-definition", ordinaryDependency: variant == "scalar-body"}
				if captured {
					declarations := options
					options.schemaTransform = func(base schematest.GraphFixture) schematest.GraphFixture {
						return policyFactRuntimeCaptureSchema(t, base, declarations)
					}
				}
				// Deliberately use the raw fixture: no release-mode skip or
				// test-only policy-set constructor may hide a refusal case.
				fixture := newPolicyFactRuntimeFixture(t, false, options)
				if actual := fixture.app.registry.PolicyCaptureFingerprint() != (golem.SchemaDigest{}); actual != captured {
					t.Fatalf("capture inventory=%t want%t", actual, captured)
				}
				wantAdmitted := mutationHistoryWriterVersion == mutationfact.HistoryFactVersion && captured
				if mutationHistoryWriterRequired(fixture.app.registry) != wantAdmitted {
					t.Fatal("application historical mode differs from its actual writer and registry")
				}
				// Make every authored rule true. A refusal must be admission,
				// not an ordinary false relation/body or a missing source row.
				fixture.mutate(t, golem.EventCreated, 11, 1, "ready")
				caller := fixture.caller(t)
				rows, findErr := CallerFindMany(fixture.ctx, caller, fixture.users)
				count, countErr := CallerCount(fixture.ctx, caller, fixture.users)
				selector := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey,
					golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
				_, uniqueErr := CallerFindUnique(fixture.ctx, caller, fixture.users, selector)
				stream, streamErr := CallerEvents[policyFactRuntimePrincipal, policyFactRuntimeActor, policyFactRuntimeUser, golem.EventMetadata](fixture.ctx, caller, fixture.users)
				if stream != nil {
					defer stream.Close()
				}
				if wantAdmitted {
					if findErr != nil || countErr != nil || uniqueErr != nil || streamErr != nil || stream == nil || len(rows) != 1 || count != 1 {
						t.Fatalf("historical admission: rows=%d count=%d find=%v countErr=%v unique=%v stream=%v", len(rows), count, findErr, countErr, uniqueErr, streamErr)
					}
					return
				}
				if len(rows) != 0 || count != 0 || stream != nil {
					t.Fatalf("unreleased policy produced rows=%d count=%d stream=%t", len(rows), count, stream != nil)
				}
				for operation, err := range map[string]error{"findMany": findErr, "count": countErr, "findUnique": uniqueErr, "events": streamErr} {
					var public *golem.Error
					var evidence *policyir.FactScopeEvidenceError
					if !errors.As(err, &public) || public.Code != golem.CodeUnauthenticated || public.Message != policyUnavailableMessage || !errors.As(err, &evidence) || evidence.ModelID != policyir.ModelID(fixture.schema.User) {
						t.Errorf("%s did not refuse at historical policy admission: %v", operation, err)
					}
				}
			})
		}
	}
}

func TestPolicyFactRuntimeOrdinaryPolicyRetainsReaderReleaseAdmission(t *testing.T) {
	for _, captured := range []bool{false, true} {
		name := "without-capture"
		if captured {
			name = "registered-capture"
		}
		t.Run(name, func(t *testing.T) {
			options := policyFactRuntimeOptions{rootNoFacts: true}
			if captured {
				options.schemaTransform = func(base schematest.GraphFixture) schematest.GraphFixture {
					return policyFactRuntimeCaptureSchema(t, base, policyFactRuntimeOptions{})
				}
			}
			fixture := newPolicyFactRuntimeFixture(t, false, options)
			caller := fixture.caller(t)
			rows, err := CallerFindMany(fixture.ctx, caller, fixture.users)
			if err != nil || len(rows) != 1 {
				t.Fatalf("ordinary reader admission: rows=%d err=%v", len(rows), err)
			}
			if count, err := CallerCount(fixture.ctx, caller, fixture.users); err != nil || count != 1 {
				t.Fatalf("ordinary count=%d err=%v", count, err)
			}
			stream, err := CallerEvents[policyFactRuntimePrincipal, policyFactRuntimeActor, policyFactRuntimeUser, golem.EventMetadata](fixture.ctx, caller, fixture.users)
			if err != nil || stream == nil {
				t.Fatalf("ordinary stream admission: %v", err)
			}
			_ = stream.Close()
		})
	}
}
