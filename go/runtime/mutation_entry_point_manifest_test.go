package runtime

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

// semanticEntryPointHarness owns the two fixtures every mutation entry point
// needs: the ordinary subscribed semantic model, and the same model with an
// engine-owned version token for the optimistic-concurrency families.
type semanticEntryPointHarness struct {
	plain     mutationResultFixture
	versioned mutationVocabularyFixture
	caller    *Caller[mutationResultPrincipal, mutationResultActor]
	versioner *Caller[mutationResultPrincipal, mutationResultActor]
	next      byte
}

func newSemanticEntryPointHarness(t *testing.T) *semanticEntryPointHarness {
	t.Helper()
	ctx := context.Background()
	plain, _ := newSemanticMarkFixtureWithEmbedder(t, MutationLimits{})
	versioned := newSemanticVersionedFixture(t)
	caller, err := plain.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	versioner, err := versioned.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	return &semanticEntryPointHarness{plain: plain, versioned: versioned, caller: caller, versioner: versioner, next: 100}
}

func (harness *semanticEntryPointHarness) id() byte {
	harness.next++
	return harness.next
}

func (harness *semanticEntryPointHarness) seedPlain(t testing.TB, id byte) {
	t.Helper()
	if _, err := SystemCreate(context.Background(), harness.plain.app.System(), harness.plain.postDescriptor, harness.plain.createPost(id, golem.UUID{15: 1}, "seeded")); err != nil {
		t.Fatal(err)
	}
}

func (harness *semanticEntryPointHarness) versionedCreate(id byte, title string) golem.CreateInput[mutationResultPost] {
	decimal, _ := golem.ParseDecimal("1.25")
	decimalField := golem.GeneratedEqualField[mutationResultPost, golem.Decimal](harness.versioned.schema.PostDecimal)
	return golem.GeneratedCreateInput(harness.versioned.schema.Post,
		golem.GeneratedCreateFieldValue(harness.versioned.schema.Post, harness.versioned.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(harness.versioned.schema.Post, harness.versioned.authorID, golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(harness.versioned.schema.Post, harness.versioned.title, title),
		golem.GeneratedCreateFieldValue(harness.versioned.schema.Post, decimalField, decimal),
	)
}

func (harness *semanticEntryPointHarness) seedVersioned(t testing.TB, id byte) {
	t.Helper()
	if _, err := SystemCreate(context.Background(), harness.versioned.app.System(), harness.versioned.postDescriptor, harness.versionedCreate(id, "seeded")); err != nil {
		t.Fatal(err)
	}
}

type semanticEntryPointOutcome struct {
	marks []string
	facts int
}

func runInCallerTransaction(t testing.TB, caller *Caller[mutationResultPrincipal, mutationResultActor], body func(context.Context, *CallerTx[mutationResultPrincipal, mutationResultActor]) error) semanticEntryPointOutcome {
	t.Helper()
	ctx := context.Background()
	var outcome semanticEntryPointOutcome
	if err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		if err := body(ctx, transaction); err != nil {
			return err
		}
		outcome.marks = semanticMarkKeys(t, transaction.caller.executor)
		outcome.facts = semanticFactCount(t, transaction.caller.executor)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return outcome
}

func runInSystemTransaction[P, A any](t testing.TB, system System[mutationResultPrincipal, mutationResultActor], body func(context.Context, *SystemTx[mutationResultPrincipal, mutationResultActor]) error) semanticEntryPointOutcome {
	t.Helper()
	ctx := context.Background()
	var outcome semanticEntryPointOutcome
	if err := SystemTransaction(ctx, system, func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
		if err := body(ctx, transaction); err != nil {
			return err
		}
		outcome.marks = semanticMarkKeys(t, transaction.system.executor)
		outcome.facts = semanticFactCount(t, transaction.system.executor)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return outcome
}

// mutationEntryPointManifest names every exported mutation entry point in the
// runtime package. Each entry drives that exact function and reports the marks
// and durable facts its transaction accumulated; the AST scan below fails when
// a new exported entry point is added without one.
func mutationEntryPointManifest() map[string]func(testing.TB, *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
	return map[string]func(testing.TB, *semanticEntryPointHarness) (semanticEntryPointOutcome, byte){
		"CallerCreate": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerCreate(ctx, transaction.caller, harness.plain.postDescriptor, harness.plain.createPost(id, golem.UUID{15: 1}, "entry"))
				return err
			}), id
		},
		"CallerTxCreate": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxCreate(ctx, transaction, harness.plain.postDescriptor, harness.plain.createPost(id, golem.UUID{15: 1}, "entry"))
				return err
			}), id
		},
		"SystemCreate": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemCreate(ctx, transaction.system, harness.plain.postDescriptor, harness.plain.createPost(id, golem.UUID{15: 1}, "entry"))
				return err
			}), id
		},
		"SystemTxCreate": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemTxCreate(ctx, transaction, harness.plain.postDescriptor, harness.plain.createPost(id, golem.UUID{15: 1}, "entry"))
				return err
			}), id
		},
		"CallerUpdate": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerUpdate(ctx, transaction.caller, harness.plain.postDescriptor, harness.plain.target(id), harness.plain.updateTitle("entry"))
				return err
			}), id
		},
		"CallerTxUpdate": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxUpdate(ctx, transaction, harness.plain.postDescriptor, harness.plain.target(id), harness.plain.updateTitle("entry"))
				return err
			}), id
		},
		"SystemUpdate": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemUpdate(ctx, transaction.system, harness.plain.postDescriptor, harness.plain.target(id), harness.plain.updateTitle("entry"))
				return err
			}), id
		},
		"SystemTxUpdate": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemTxUpdate(ctx, transaction, harness.plain.postDescriptor, harness.plain.target(id), harness.plain.updateTitle("entry"))
				return err
			}), id
		},
		"CallerDelete": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerDelete(ctx, transaction.caller, harness.plain.postDescriptor, harness.plain.target(id))
				return err
			}), id
		},
		"CallerTxDelete": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxDelete(ctx, transaction, harness.plain.postDescriptor, harness.plain.target(id))
				return err
			}), id
		},
		"SystemDelete": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemDelete(ctx, transaction.system, harness.plain.postDescriptor, harness.plain.target(id))
				return err
			}), id
		},
		"SystemTxDelete": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemTxDelete(ctx, transaction, harness.plain.postDescriptor, harness.plain.target(id))
				return err
			}), id
		},
		"CallerUpsert": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerUpsert(ctx, transaction.caller, harness.plain.postDescriptor, harness.plain.target(id), harness.plain.createPost(id, golem.UUID{15: 1}, "entry"), harness.plain.updateTitle("entry"))
				return err
			}), id
		},
		"CallerTxUpsert": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxUpsert(ctx, transaction, harness.plain.postDescriptor, harness.plain.target(id), harness.plain.createPost(id, golem.UUID{15: 1}, "entry"), harness.plain.updateTitle("entry"))
				return err
			}), id
		},
		"SystemUpsert": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemUpsert(ctx, transaction.system, harness.plain.postDescriptor, harness.plain.target(id), harness.plain.createPost(id, golem.UUID{15: 1}, "entry"), harness.plain.updateTitle("entry"))
				return err
			}), id
		},
		"SystemTxUpsert": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemTxUpsert(ctx, transaction, harness.plain.postDescriptor, harness.plain.target(id), harness.plain.createPost(id, golem.UUID{15: 1}, "entry"), harness.plain.updateTitle("entry"))
				return err
			}), id
		},
		"CallerUpdateMany": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerUpdateMany(ctx, transaction.caller, harness.plain.postDescriptor, harness.plain.postID.In(golem.UUID{15: id}), harness.plain.updateManyTitle("entry"))
				return err
			}), id
		},
		"CallerTxUpdateMany": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxUpdateMany(ctx, transaction, harness.plain.postDescriptor, harness.plain.postID.In(golem.UUID{15: id}), harness.plain.updateManyTitle("entry"))
				return err
			}), id
		},
		"SystemUpdateMany": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemUpdateMany(ctx, transaction.system, harness.plain.postDescriptor, harness.plain.postID.In(golem.UUID{15: id}), harness.plain.updateManyTitle("entry"))
				return err
			}), id
		},
		"SystemTxUpdateMany": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemTxUpdateMany(ctx, transaction, harness.plain.postDescriptor, harness.plain.postID.In(golem.UUID{15: id}), harness.plain.updateManyTitle("entry"))
				return err
			}), id
		},
		"CallerDeleteMany": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerDeleteMany(ctx, transaction.caller, harness.plain.postDescriptor, harness.plain.postID.In(golem.UUID{15: id}))
				return err
			}), id
		},
		"CallerTxDeleteMany": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInCallerTransaction(t, harness.caller, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxDeleteMany(ctx, transaction, harness.plain.postDescriptor, harness.plain.postID.In(golem.UUID{15: id}))
				return err
			}), id
		},
		"SystemDeleteMany": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemDeleteMany(ctx, transaction.system, harness.plain.postDescriptor, harness.plain.postID.In(golem.UUID{15: id}))
				return err
			}), id
		},
		"SystemTxDeleteMany": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedPlain(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.plain.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemTxDeleteMany(ctx, transaction, harness.plain.postDescriptor, harness.plain.postID.In(golem.UUID{15: id}))
				return err
			}), id
		},
		"CallerUpdateVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedVersioned(t, id)
			return runInCallerTransaction(t, harness.versioner, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerUpdateVersioned(ctx, transaction.caller, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectVersion(1), harness.versioned.updateTitle("entry"))
				return err
			}), id
		},
		"CallerTxUpdateVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedVersioned(t, id)
			return runInCallerTransaction(t, harness.versioner, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxUpdateVersioned(ctx, transaction, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectVersion(1), harness.versioned.updateTitle("entry"))
				return err
			}), id
		},
		"SystemUpdateVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedVersioned(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.versioned.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemUpdateVersioned(ctx, transaction.system, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectVersion(1), harness.versioned.updateTitle("entry"))
				return err
			}), id
		},
		"SystemTxUpdateVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedVersioned(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.versioned.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemTxUpdateVersioned(ctx, transaction, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectVersion(1), harness.versioned.updateTitle("entry"))
				return err
			}), id
		},
		"CallerDeleteVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedVersioned(t, id)
			return runInCallerTransaction(t, harness.versioner, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerDeleteVersioned(ctx, transaction.caller, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectVersion(1))
				return err
			}), id
		},
		"CallerTxDeleteVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedVersioned(t, id)
			return runInCallerTransaction(t, harness.versioner, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxDeleteVersioned(ctx, transaction, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectVersion(1))
				return err
			}), id
		},
		"SystemDeleteVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedVersioned(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.versioned.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemDeleteVersioned(ctx, transaction.system, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectVersion(1))
				return err
			}), id
		},
		"SystemTxDeleteVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			harness.seedVersioned(t, id)
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.versioned.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemTxDeleteVersioned(ctx, transaction, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectVersion(1))
				return err
			}), id
		},
		"CallerUpsertVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInCallerTransaction(t, harness.versioner, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerUpsertVersioned(ctx, transaction.caller, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectAbsent(), harness.versionedCreate(id, "entry"), harness.versioned.updateTitle("unused"))
				return err
			}), id
		},
		"CallerTxUpsertVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInCallerTransaction(t, harness.versioner, func(ctx context.Context, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := CallerTxUpsertVersioned(ctx, transaction, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectAbsent(), harness.versionedCreate(id, "entry"), harness.versioned.updateTitle("unused"))
				return err
			}), id
		},
		"SystemUpsertVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.versioned.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemUpsertVersioned(ctx, transaction.system, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectAbsent(), harness.versionedCreate(id, "entry"), harness.versioned.updateTitle("unused"))
				return err
			}), id
		},
		"SystemTxUpsertVersioned": func(t testing.TB, harness *semanticEntryPointHarness) (semanticEntryPointOutcome, byte) {
			id := harness.id()
			return runInSystemTransaction[mutationResultPrincipal, mutationResultActor](t, harness.versioned.app.System(), func(ctx context.Context, transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
				_, err := SystemTxUpsertVersioned(ctx, transaction, harness.versioned.postDescriptor, harness.versioned.target(id), golem.ExpectAbsent(), harness.versionedCreate(id, "entry"), harness.versioned.updateTitle("unused"))
				return err
			}), id
		},
	}
}

// nonMutationRuntimeEntryPoints are the exported Caller*/System* functions that
// perform no write. Listing them by hand is the point: a new mutation family
// added to the runtime lands in neither list and fails the scan.
func nonMutationRuntimeEntryPoints() map[string]struct{} {
	names := []string{
		"CallerAggregate", "CallerCount", "CallerEvents", "CallerExecuteFrozenAnalytics", "CallerExecuteFrozenRead",
		"CallerExplainAggregate", "CallerExplainCount", "CallerExplainFindFirst", "CallerExplainFindMany",
		"CallerExplainFindUnique", "CallerExplainGroupBy", "CallerExplainRelationGroupBy", "CallerExplainScoped",
		"CallerFindFirst", "CallerFindMany", "CallerFindUnique", "CallerFrozenEvents", "CallerFrozenReadEvents",
		"CallerGroupBy", "CallerMutationModel", "CallerRelationGroupBy", "CallerScoped", "CallerSearch",
		"CallerSimilar", "CallerTransaction", "CallerTxAggregate", "CallerTxCount", "CallerTxEnqueue",
		"CallerTxFindFirst", "CallerTxFindMany", "CallerTxFindUnique", "CallerTxGroupBy",
		"CallerTxRelationGroupBy", "CallerTxScoped", "CallerTxSystem",
		"SystemAggregate", "SystemCount", "SystemFindFirst", "SystemFindMany", "SystemFindUnique",
		"SystemGroupBy", "SystemRelationGroupBy", "SystemScoped", "SystemSearch", "SystemSimilar",
		"SystemTransaction", "SystemTxAggregate", "SystemTxCount", "SystemTxEnqueue", "SystemTxFindFirst",
		"SystemTxFindMany", "SystemTxFindUnique", "SystemTxGroupBy", "SystemTxRelationGroupBy", "SystemTxScoped",
	}
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}

func exportedRuntimeCapabilityFunctions(t testing.TB) []string {
	t.Helper()
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, parsed := range packages {
		for path, file := range parsed.Files {
			if strings.Contains(filepath.ToSlash(path), "/testdata/") {
				continue
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Recv != nil || !function.Name.IsExported() {
					continue
				}
				if strings.HasPrefix(function.Name.Name, "Caller") || strings.HasPrefix(function.Name.Name, "System") {
					names = append(names, function.Name.Name)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// TestEveryMutationEntryPointMarksAndCapturesFacts scans the runtime package
// for exported caller and system capability functions and refuses any that no
// manifest classifies. Every mutation entry point then runs its own two
// assertions: a semantic mark, and separately a durable fact.
func TestEveryMutationEntryPointMarksAndCapturesFacts(t *testing.T) {
	manifest := mutationEntryPointManifest()
	reads := nonMutationRuntimeEntryPoints()
	for _, name := range exportedRuntimeCapabilityFunctions(t) {
		_, isMutation := manifest[name]
		_, isRead := reads[name]
		if isMutation == isRead {
			t.Fatalf("exported runtime entry point %q is unclassified or double-classified; add it to exactly one manifest", name)
		}
	}
	present := make(map[string]struct{}, len(manifest))
	for _, name := range exportedRuntimeCapabilityFunctions(t) {
		present[name] = struct{}{}
	}
	for name := range manifest {
		if _, ok := present[name]; !ok {
			t.Fatalf("manifest names %q, which the runtime package no longer exports", name)
		}
	}

	names := make([]string, 0, len(manifest))
	for name := range manifest {
		names = append(names, name)
	}
	sort.Strings(names)
	harness := newSemanticEntryPointHarness(t)
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			outcome, id := manifest[name](t, harness)
			assertSemanticMarkKeys(t, outcome.marks, []byte{id})
			if outcome.facts != 1 {
				t.Fatalf("%s durable facts=%d want=1", name, outcome.facts)
			}
		})
	}
}
