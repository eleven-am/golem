package runtime_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

type p10ChainCall func(context.Context, *p10operations.CallerTx[p10operations.Principal], golem.UUID) error

func p10ChainCalls() map[string]p10ChainCall {
	return map[string]p10ChainCall{
		"caller write": func(ctx context.Context, tx *p10operations.CallerTx[p10operations.Principal], id golem.UUID) error {
			_, err := tx.Teams.Create(ctx, p10TeamInput(id))
			return err
		},
		"system escape write": func(ctx context.Context, tx *p10operations.CallerTx[p10operations.Principal], id golem.UUID) error {
			_, err := p10operations.SystemEscape(tx).Teams.Create(ctx, p10TeamInput(id))
			return err
		},
		"read": func(ctx context.Context, tx *p10operations.CallerTx[p10operations.Principal], _ golem.UUID) error {
			_, err := tx.Teams.FindMany(ctx, golem.Where(p10operations.Teams.Owner.Eq("alpha")))
			return err
		},
	}
}

func p10ConcurrentUse(err error) bool {
	return err != nil && strings.Contains(err.Error(), "transaction used concurrently; a transaction serves one call chain at a time")
}

func TestHookCallWithAnUnrelatedContextFailsImmediatelyAcrossProviders(t *testing.T) {
	sources := map[string]func(retained context.Context) context.Context{
		"fresh":    func(context.Context) context.Context { return context.Background() },
		"retained": func(retained context.Context) context.Context { return retained },
	}
	for sourceName, source := range sources {
		for callName, call := range p10ChainCalls() {
			source, call := source, call
			t.Run(sourceName+"/"+callName, func(t *testing.T) {
				forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
					caller := fixture.caller(t, "alpha")
					first, second, foreign := p10OperationID(t, 8000), p10OperationID(t, 8001), p10OperationID(t, 8002)
					var tx *p10operations.CallerTx[p10operations.Principal]
					var retained context.Context
					var foreignErr error
					p10operations.Reset(nil)
					p10operations.SetTeamHook(func(ctx context.Context, _ golem.HookExecutor) error {
						switch ctx.Value(p10RetainKey{}) {
						case "first":
							retained = ctx
						case "second":
							foreignErr = call(source(retained), tx, foreign)
						}
						return nil
					})
					var err error
					p10FinishesWithin(t, 20*time.Second, func() {
						err = caller.Transaction(context.Background(), func(inner *p10operations.CallerTx[p10operations.Principal]) error {
							tx = inner
							if _, err := tx.Teams.Create(context.WithValue(context.Background(), p10RetainKey{}, "first"), p10TeamInput(first)); err != nil {
								return err
							}
							_, err := tx.Teams.Create(context.WithValue(context.Background(), p10RetainKey{}, "second"), p10TeamInput(second))
							return err
						})
					})
					if err != nil {
						t.Fatalf("transaction = %v", err)
					}
					if !p10ConcurrentUse(foreignErr) {
						t.Fatalf("hook call with an unrelated context = %v, want the concurrent-use error", foreignErr)
					}
					for id, want := range map[golem.UUID]bool{first: true, second: true, foreign: false} {
						if _, ok := fixture.teamOwner(t, id); ok != want {
							t.Fatalf("team %s persisted=%t want %t", id, ok, want)
						}
					}
				})
			})
		}
	}
}

func TestSiblingCallsInOneCallbackServeOneChainAtATimeAcrossProviders(t *testing.T) {
	for callName, call := range p10ChainCalls() {
		call := call
		t.Run(callName, func(t *testing.T) {
			forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
				caller := fixture.caller(t, "alpha")
				active, overlapping, sequential := p10OperationID(t, 8100), p10OperationID(t, 8101), p10OperationID(t, 8102)
				started, release := make(chan struct{}), make(chan struct{})
				activeResult := make(chan error, 1)
				var overlapErr, sequentialErr error
				p10operations.Reset(nil)
				p10operations.SetTeamHook(func(ctx context.Context, _ golem.HookExecutor) error {
					if p10AttemptDepth(ctx) == 7 {
						close(started)
						<-release
					}
					return nil
				})
				var err error
				p10FinishesWithin(t, 20*time.Second, func() {
					err = caller.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
						go func() {
							_, err := tx.Teams.Create(context.WithValue(context.Background(), p10AttemptDepthKey{}, 7), p10TeamInput(active))
							activeResult <- err
						}()
						<-started
						overlapErr = call(context.Background(), tx, overlapping)
						close(release)
						if err := <-activeResult; err != nil {
							return err
						}
						sequentialErr = call(context.Background(), tx, sequential)
						return nil
					})
				})
				if err != nil {
					t.Fatalf("transaction = %v", err)
				}
				if !p10ConcurrentUse(overlapErr) {
					t.Fatalf("sibling call overlapping an active call = %v, want the concurrent-use error", overlapErr)
				}
				if sequentialErr != nil {
					t.Fatalf("sequential call after the active call finished = %v", sequentialErr)
				}
				writes := callName != "read"
				for id, want := range map[golem.UUID]bool{active: true, overlapping: false, sequential: writes} {
					if _, ok := fixture.teamOwner(t, id); ok != want {
						t.Fatalf("team %s persisted=%t want %t", id, ok, want)
					}
				}
			})
		})
	}
}

func TestOverlappingCallsInsideOneHookServeOneChainAtATimeAcrossProviders(t *testing.T) {
	variants := []struct {
		name        string
		transaction bool
		hookContext bool
	}{
		{name: "standalone executor writes", transaction: false},
		{name: "transaction executor writes", transaction: true},
		{name: "transaction hook context write", transaction: true, hookContext: true},
	}
	for _, variant := range variants {
		variant := variant
		t.Run(variant.name, func(t *testing.T) {
			forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
				caller := fixture.caller(t, "alpha")
				outer, first, second := p10OperationID(t, 8200), p10OperationID(t, 8201), p10OperationID(t, 8202)
				firstArrived, release := make(chan struct{}), make(chan struct{})
				var tx *p10operations.CallerTx[p10operations.Principal]
				var firstErr, secondErr error
				p10operations.Reset(nil)
				p10operations.SetTeamHook(func(ctx context.Context, executor golem.HookExecutor) error {
					switch ctx.Value(p10AttemptDepthKey{}) {
					case nil:
						firstResult := make(chan error, 1)
						go func() {
							firstResult <- fixture.createTeam(context.WithValue(ctx, p10AttemptDepthKey{}, "first"), executor, first)
						}()
						<-firstArrived
						secondContext := context.WithValue(ctx, p10AttemptDepthKey{}, "second")
						if variant.hookContext {
							_, secondErr = tx.Teams.Create(secondContext, p10TeamInput(second))
						} else {
							secondErr = fixture.createTeam(secondContext, executor, second)
						}
						close(release)
						firstErr = <-firstResult
					case "first":
						close(firstArrived)
						<-release
					}
					return nil
				})
				var err error
				p10FinishesWithin(t, 20*time.Second, func() {
					if !variant.transaction {
						_, err = caller.Teams.Create(context.Background(), p10TeamInput(outer))
						return
					}
					err = caller.Transaction(context.Background(), func(inner *p10operations.CallerTx[p10operations.Principal]) error {
						tx = inner
						_, err := tx.Teams.Create(context.Background(), p10TeamInput(outer))
						return err
					})
				})
				if err != nil {
					t.Fatalf("outer write = %v", err)
				}
				if firstErr != nil {
					t.Fatalf("first hook write = %v", firstErr)
				}
				if !p10ConcurrentUse(secondErr) {
					t.Fatalf("hook write overlapping the first = %v, want the concurrent-use error", secondErr)
				}
				for id, want := range map[golem.UUID]bool{outer: true, first: true, second: false} {
					if _, ok := fixture.teamOwner(t, id); ok != want {
						t.Fatalf("team %s persisted=%t want %t", id, ok, want)
					}
				}
			})
		})
	}
}
