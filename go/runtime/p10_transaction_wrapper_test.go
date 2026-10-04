package runtime

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/queue"
)

type transactionWrapper struct {
	name   string
	seamed bool
}

func transactionHandleParameter(function *ast.FuncDecl) (string, string, bool) {
	for _, field := range function.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		var base ast.Expr
		switch indexed := star.X.(type) {
		case *ast.IndexListExpr:
			base = indexed.X
		case *ast.IndexExpr:
			base = indexed.X
		default:
			continue
		}
		ident, ok := base.(*ast.Ident)
		if !ok || (ident.Name != "CallerTx" && ident.Name != "SystemTx") || len(field.Names) != 1 {
			continue
		}
		member := "caller"
		if ident.Name == "SystemTx" {
			member = "system"
		}
		return field.Names[0].Name, member, true
	}
	return "", "", false
}

func returnsError(function *ast.FuncDecl) bool {
	results := function.Type.Results
	if results == nil || len(results.List) == 0 {
		return false
	}
	ident, ok := results.List[len(results.List)-1].Type.(*ast.Ident)
	return ok && ident.Name == "error"
}

func seamCall(expression ast.Expr, handle string) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	if unpack, ok := call.Fun.(*ast.Ident); ok && unpack.Name == "foundRow" && len(call.Args) == 1 {
		return seamCall(call.Args[0], handle)
	}
	name, ok := call.Fun.(*ast.Ident)
	if !ok || name.Name != "transactionOperation" || len(call.Args) != 3 {
		return false
	}
	if context, ok := call.Args[0].(*ast.Ident); !ok || context.Name != "ctx" {
		return false
	}
	accessor, ok := call.Args[1].(*ast.CallExpr)
	if !ok || len(accessor.Args) != 0 {
		return false
	}
	selector, ok := accessor.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "binding" {
		return false
	}
	variable, ok := selector.X.(*ast.Ident)
	if !ok || variable.Name != handle {
		return false
	}
	_, ok = call.Args[2].(*ast.FuncLit)
	return ok
}

func delegatesThroughTheSeam(function *ast.FuncDecl, handle, _ string) bool {
	if function.Body == nil || len(function.Body.List) != 1 {
		return false
	}
	statement, ok := function.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(statement.Results) != 1 {
		return false
	}
	return seamCall(statement.Results[0], handle)
}

func exportedTransactionWrappers(t testing.TB) []transactionWrapper {
	t.Helper()
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var wrappers []transactionWrapper
	for _, parsed := range packages {
		for _, file := range parsed.Files {
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Recv != nil || !function.Name.IsExported() || !returnsError(function) {
					continue
				}
				handle, member, ok := transactionHandleParameter(function)
				if !ok {
					continue
				}
				wrappers = append(wrappers, transactionWrapper{name: function.Name.Name, seamed: delegatesThroughTheSeam(function, handle, member)})
			}
		}
	}
	sort.Slice(wrappers, func(i, j int) bool { return wrappers[i].name < wrappers[j].name })
	return wrappers
}

type transactionWrapperEnvironment struct {
	owner    *testing.T
	harness  *semanticEntryPointHarness
	fullText *mutationResultFixture
	queue    *transactionFixture
	jobType  queue.Type[queueGatePayload]
}

func (environment *transactionWrapperEnvironment) semantic(*testing.T) *semanticEntryPointHarness {
	t := environment.owner
	if environment.harness == nil {
		environment.harness = newSemanticEntryPointHarness(t)
		ctx := context.Background()
		for _, id := range []byte{65, 66} {
			if _, err := SystemCreate(ctx, environment.harness.plain.app.System(), environment.harness.plain.postDescriptor, environment.harness.plain.createPost(id, golem.UUID{15: 1}, "ranked")); err != nil {
				t.Fatal(err)
			}
		}
		if err := environment.harness.plain.app.RefreshSemanticIndexes(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return environment.harness
}

func (environment *transactionWrapperEnvironment) text(*testing.T) mutationResultFixture {
	t := environment.owner
	if environment.fullText == nil {
		fixture := newFullTextMutationFixture(t)
		environment.fullText = &fixture
	}
	return *environment.fullText
}

func (environment *transactionWrapperEnvironment) queued(*testing.T) (transactionFixture, queue.Type[queueGatePayload]) {
	t := environment.owner
	if environment.queue == nil {
		fixture, jobType := openQueueFixture(t)
		environment.queue, environment.jobType = &fixture, jobType
	}
	return *environment.queue, environment.jobType
}

type wrapperCallerTx = CallerTx[mutationResultPrincipal, mutationResultActor]
type wrapperSystemTx = SystemTx[mutationResultPrincipal, mutationResultActor]

func inWrapperCallerTransaction(t *testing.T, fixture mutationResultFixture, body func(context.Context, *wrapperCallerTx) error) {
	t.Helper()
	caller := mustMutationResultCaller(t, fixture)
	if err := CallerTransaction(context.Background(), caller, func(tx *wrapperCallerTx) error {
		return body(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func inWrapperSystemTransaction(t *testing.T, fixture mutationResultFixture, body func(context.Context, *wrapperSystemTx) error) {
	t.Helper()
	if err := SystemTransaction(context.Background(), fixture.app.System(), func(tx *wrapperSystemTx) error {
		return body(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func semanticSource(fixture mutationResultFixture) golem.UniqueSelectorValue[mutationResultPost] {
	return golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 65}))
}

func transactionWrapperRuns() map[string]func(*testing.T, *transactionWrapperEnvironment) {
	runs := map[string]func(*testing.T, *transactionWrapperEnvironment){
		"CallerTxFindMany": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperCallerTransaction(t, fixture, func(ctx context.Context, tx *wrapperCallerTx) error {
				_, err := CallerTxFindMany(ctx, tx, fixture.postDescriptor)
				return err
			})
		},
		"CallerTxFindFirst": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperCallerTransaction(t, fixture, func(ctx context.Context, tx *wrapperCallerTx) error {
				_, _, err := CallerTxFindFirst(ctx, tx, fixture.postDescriptor)
				return err
			})
		},
		"CallerTxFindUnique": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperCallerTransaction(t, fixture, func(ctx context.Context, tx *wrapperCallerTx) error {
				_, err := CallerTxFindUnique(ctx, tx, fixture.postDescriptor, semanticSource(fixture))
				return err
			})
		},
		"CallerTxCount": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperCallerTransaction(t, fixture, func(ctx context.Context, tx *wrapperCallerTx) error {
				_, err := CallerTxCount(ctx, tx, fixture.postDescriptor)
				return err
			})
		},
		"SystemTxFindMany": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperSystemTransaction(t, fixture, func(ctx context.Context, tx *wrapperSystemTx) error {
				_, err := SystemTxFindMany(ctx, tx, fixture.postDescriptor)
				return err
			})
		},
		"SystemTxFindFirst": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperSystemTransaction(t, fixture, func(ctx context.Context, tx *wrapperSystemTx) error {
				_, _, err := SystemTxFindFirst(ctx, tx, fixture.postDescriptor)
				return err
			})
		},
		"SystemTxFindUnique": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperSystemTransaction(t, fixture, func(ctx context.Context, tx *wrapperSystemTx) error {
				_, err := SystemTxFindUnique(ctx, tx, fixture.postDescriptor, semanticSource(fixture))
				return err
			})
		},
		"SystemTxCount": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperSystemTransaction(t, fixture, func(ctx context.Context, tx *wrapperSystemTx) error {
				_, err := SystemTxCount(ctx, tx, fixture.postDescriptor)
				return err
			})
		},
		"CallerTxSearch": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperCallerTransaction(t, fixture, func(ctx context.Context, tx *wrapperCallerTx) error {
				_, err := CallerTxSearch(ctx, tx, fixture.postDescriptor, schematest.SemanticIndexName, "ranked", 10)
				return err
			})
		},
		"CallerTxSimilar": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperCallerTransaction(t, fixture, func(ctx context.Context, tx *wrapperCallerTx) error {
				_, err := CallerTxSimilar(ctx, tx, fixture.postDescriptor, schematest.SemanticIndexName, semanticSource(fixture), 10)
				return err
			})
		},
		"SystemTxSearch": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperSystemTransaction(t, fixture, func(ctx context.Context, tx *wrapperSystemTx) error {
				_, err := SystemTxSearch(ctx, tx, fixture.postDescriptor, schematest.SemanticIndexName, "ranked", 10)
				return err
			})
		},
		"SystemTxSimilar": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.semantic(t).plain
			inWrapperSystemTransaction(t, fixture, func(ctx context.Context, tx *wrapperSystemTx) error {
				_, err := SystemTxSimilar(ctx, tx, fixture.postDescriptor, schematest.SemanticIndexName, semanticSource(fixture), 10)
				return err
			})
		},
		"CallerTxTextSearch": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.text(t)
			inWrapperCallerTransaction(t, fixture, func(ctx context.Context, tx *wrapperCallerTx) error {
				_, err := CallerTxTextSearch(ctx, tx, fixture.postDescriptor, runtimeFullTextIndexName, "pending", 10)
				return err
			})
		},
		"CallerTxTextSearchSelect": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.text(t)
			inWrapperCallerTransaction(t, fixture, func(ctx context.Context, tx *wrapperCallerTx) error {
				_, err := CallerTxTextSearchSelect(ctx, tx, fixture.postDescriptor, runtimeFullTextIndexName, "pending", 10, golem.Select[mutationResultPost](fixture.postID))
				return err
			})
		},
		"SystemTxTextSearch": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.text(t)
			inWrapperSystemTransaction(t, fixture, func(ctx context.Context, tx *wrapperSystemTx) error {
				_, err := SystemTxTextSearch(ctx, tx, fixture.postDescriptor, runtimeFullTextIndexName, "pending", 10)
				return err
			})
		},
		"SystemTxTextSearchSelect": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture := environment.text(t)
			inWrapperSystemTransaction(t, fixture, func(ctx context.Context, tx *wrapperSystemTx) error {
				_, err := SystemTxTextSearchSelect(ctx, tx, fixture.postDescriptor, runtimeFullTextIndexName, "pending", 10, golem.Select[mutationResultPost](fixture.postID))
				return err
			})
		},
		"CallerTxEnqueue": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture, jobType := environment.queued(t)
			caller, err := fixture.app.ForPrincipal(context.Background(), testPrincipal{Allow: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := CallerTransaction(context.Background(), caller, func(tx *CallerTx[testPrincipal, testActor]) error {
				_, err := CallerTxEnqueue(context.Background(), tx, newQueueGatePending(t, jobType))
				return err
			}); err != nil {
				t.Fatal(err)
			}
		},
		"SystemTxEnqueue": func(t *testing.T, environment *transactionWrapperEnvironment) {
			fixture, jobType := environment.queued(t)
			if err := SystemTransaction(context.Background(), fixture.app.System(), func(tx *SystemTx[testPrincipal, testActor]) error {
				_, err := SystemTxEnqueue(context.Background(), tx, newQueueGatePending(t, jobType))
				return err
			}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, entry := range mutationEntryPointManifest() {
		if !strings.HasPrefix(name, "CallerTx") && !strings.HasPrefix(name, "SystemTx") {
			continue
		}
		entry := entry
		runs[name] = func(t *testing.T, environment *transactionWrapperEnvironment) {
			entry(t, environment.semantic(t))
		}
	}
	return runs
}

func transactionWrappersRunByTheAnalyticsParityTest() map[string]struct{} {
	return map[string]struct{}{
		"CallerTxAggregate": {}, "SystemTxAggregate": {}, "CallerTxGroupBy": {}, "SystemTxGroupBy": {},
		"CallerTxRelationGroupBy": {}, "SystemTxRelationGroupBy": {}, "CallerTxScoped": {}, "SystemTxScoped": {},
	}
}

func TestEveryTransactionWrapperRunsAsOneAdmittedOperation(t *testing.T) {
	wrappers := exportedTransactionWrappers(t)
	if len(wrappers) == 0 {
		t.Fatal("found no exported transaction wrappers")
	}
	runs := transactionWrapperRuns()
	external := transactionWrappersRunByTheAnalyticsParityTest()
	environment := &transactionWrapperEnvironment{owner: t}
	for _, wrapper := range wrappers {
		wrapper := wrapper
		t.Run(wrapper.name, func(t *testing.T) {
			if !wrapper.seamed {
				t.Fatalf("%s must be exactly one return of transactionOperation(ctx, handle.binding(), func...) with all of its work inside the seam", wrapper.name)
			}
			run, ok := runs[wrapper.name]
			if _, covered := external[wrapper.name]; covered {
				if ok {
					t.Fatalf("%s is both run here and delegated to the analytics parity test", wrapper.name)
				}
				return
			}
			if !ok {
				t.Fatalf("%s has no run inside a transaction; add one", wrapper.name)
			}
			run(t, environment)
		})
	}
}

func TestStatementOutsideAnAdmittedOperationFailsLoudly(t *testing.T) {
	fixture := newFullTextMutationFixture(t)
	inWrapperCallerTransaction(t, fixture, func(ctx context.Context, tx *wrapperCallerTx) error {
		if _, err := CallerFindMany(ctx, tx.caller, fixture.postDescriptor); err != errTransactionOperationNotAdmitted {
			t.Fatalf("read skipping the transaction seam = %v, want the not-admitted refusal", err)
		}
		if _, err := CallerCreate(ctx, tx.caller, fixture.postDescriptor, fixture.createPost(90, golem.UUID{15: 1}, "unadmitted")); err != errTransactionOperationNotAdmitted {
			t.Fatalf("write skipping the transaction seam = %v, want the not-admitted refusal", err)
		}
		return nil
	})
}
