package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

type p10SoakKey struct{}

type p10SoakWrite struct {
	child golem.UUID
	fails bool
}

var errP10CallbackStopped = errors.New("callback stopped by the test")

func p10TeamInput(id golem.UUID) p10operations.TeamCreateInput {
	return p10operations.Teams.Create(p10operations.Teams.ID.Create(id), p10operations.Teams.Owner.Create("alpha"))
}

func TestBatchAfterHookPanicLeavesItsCapturedExecutorRefusedAcrossProviders(t *testing.T) {
	for _, transaction := range []bool{false, true} {
		transaction := transaction
		name := "standalone"
		if transaction {
			name = "transaction"
		}
		t.Run(name, func(t *testing.T) {
			forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
				caller := fixture.caller(t, "alpha")
				invite, late := p10OperationID(t, 6700), p10OperationID(t, 6701)
				fixture.seedInvite(t, invite)
				var captured golem.HookExecutor
				hookRan := false
				p10operations.Reset(nil)
				p10operations.SetInviteUpdateManyExecutorHook(func(_ context.Context, executor golem.HookExecutor) error {
					captured, hookRan = executor, true
					panic("batch after hook panicked")
				})
				update := p10operations.Invites.UpdateMany(p10operations.Invites.TeamID.Set(fixture.alpha))
				var batchErr, lateErr error
				if transaction {
					err := caller.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
						_, batchErr = tx.Invites.UpdateMany(context.Background(), p10operations.Invites.ID.Eq(invite), update)
						lateErr = fixture.createTeam(context.Background(), captured, late)
						return errP10CallbackStopped
					})
					if !errors.Is(err, errP10CallbackStopped) {
						t.Fatalf("transaction = %v", err)
					}
				} else {
					_, batchErr = caller.Invites.UpdateMany(context.Background(), p10operations.Invites.ID.Eq(invite), update)
					lateErr = fixture.createTeam(context.Background(), captured, late)
				}
				if batchErr == nil {
					t.Fatal("a panicking batch after hook did not fail its write")
				}
				if !hookRan {
					t.Fatalf("the batch after hook did not run: %v", batchErr)
				}
				if lateErr == nil || !strings.Contains(lateErr.Error(), "after its hook returned") {
					t.Fatalf("captured executor after a panicking hook = %v", lateErr)
				}
				if _, ok := fixture.teamOwner(t, late); ok {
					t.Fatal("a captured executor's write persisted")
				}
			})
		})
	}
}

func TestCallbackPanicDrainsAnInFlightWriteBeforeRollbackAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		caller := fixture.caller(t, "alpha")
		inFlight := p10OperationID(t, 6800)
		started, unwound := make(chan struct{}), make(chan struct{})
		inFlightResult := make(chan error, 1)
		drainedFirst := make(chan bool, 1)
		p10operations.Reset(nil)
		p10operations.SetTeamHook(func(ctx context.Context, _ golem.HookExecutor) error {
			if p10AttemptDepth(ctx) == 7 {
				close(started)
				p10AwaitCondition(func() bool { return p10Closed(unwound) || p10UsageCloseIsWaiting() })
			}
			return nil
		})
		before := fixture.outboxRows(t)
		recovered := make(chan any, 1)
		go func() {
			defer func() {
				recovered <- recover()
				close(unwound)
			}()
			_ = caller.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
				go func() {
					_, err := tx.Teams.Create(context.WithValue(context.Background(), p10AttemptDepthKey{}, 7), p10TeamInput(inFlight))
					drainedFirst <- !p10Closed(unwound)
					inFlightResult <- err
				}()
				<-started
				panic("callback panicked")
			})
		}()
		if value := <-recovered; value != "callback panicked" {
			t.Fatalf("recovered panic = %v", value)
		}
		if !<-drainedFirst {
			t.Fatal("the transaction rolled back while a write was in flight")
		}
		if err := <-inFlightResult; err != nil {
			t.Fatalf("in-flight write drained before rollback = %v", err)
		}
		if _, ok := fixture.teamOwner(t, inFlight); ok {
			t.Fatal("a write in a panicking transaction committed")
		}
		if got := fixture.outboxRows(t) - before; got != 0 {
			t.Fatalf("outbox rows=%d want 0 after a panicking transaction", got)
		}
	})
}

func TestOverlappingWritesAndReadsOnOneTransactionAreRefusedAndStayConsistentAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		caller := fixture.caller(t, "alpha")
		const workers, rounds = 8, 4
		p10operations.Reset(nil)
		p10operations.SetTeamHook(func(ctx context.Context, executor golem.HookExecutor) error {
			switch value := ctx.Value(p10SoakKey{}).(type) {
			case p10SoakWrite:
				child := context.WithValue(ctx, p10SoakKey{}, value.fails)
				if _, err := golem.HookFindManyRows(child, executor, p10operations.GolemGeneratedTeamDescriptor, golem.Where(p10operations.Teams.Owner.Eq("alpha"))); err != nil {
					return err
				}
				_ = fixture.createTeam(child, executor, value.child)
			case bool:
				if value {
					return errP10FailingInnerHook
				}
			}
			return nil
		})
		before := fixture.outboxRows(t)
		var mu sync.Mutex
		persisted := map[golem.UUID]bool{}
		var all []golem.UUID
		err := caller.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
			var wait sync.WaitGroup
			failures := make(chan error, workers*rounds)
			for worker := 0; worker < workers; worker++ {
				wait.Add(1)
				go func(worker int) {
					defer wait.Done()
					for round := 0; round < rounds; round++ {
						index := worker*rounds + round
						id, child := p10OperationID(t, 7000+index*2), p10OperationID(t, 7001+index*2)
						mu.Lock()
						all = append(all, id, child)
						mu.Unlock()
						switch index % 4 {
						case 0, 1:
							spec := p10SoakWrite{child: child, fails: index%4 == 1}
							_, err := tx.Teams.Create(context.WithValue(context.Background(), p10SoakKey{}, spec), p10TeamInput(id))
							mu.Lock()
							persisted[id] = err == nil
							persisted[child] = err == nil && !spec.fails
							mu.Unlock()
							if err != nil && !p10ConcurrentUse(err) {
								failures <- fmt.Errorf("direct write %d = %w", index, err)
							}
						case 2:
							_, err := p10operations.SystemEscape(tx).Teams.Create(context.Background(), p10TeamInput(id))
							mu.Lock()
							persisted[id] = err == nil
							persisted[child] = false
							mu.Unlock()
							if err != nil && !p10ConcurrentUse(err) {
								failures <- fmt.Errorf("system write %d = %w", index, err)
							}
						case 3:
							mu.Lock()
							persisted[id], persisted[child] = false, false
							mu.Unlock()
							if _, err := tx.Teams.Count(context.Background(), golem.Where(p10operations.Teams.Owner.Eq("alpha"))); err != nil && !p10ConcurrentUse(err) {
								failures <- fmt.Errorf("count %d = %w", index, err)
							}
							if _, err := tx.Teams.FindMany(context.Background(), golem.Where(p10operations.Teams.Owner.Eq("alpha"))); err != nil && !p10ConcurrentUse(err) {
								failures <- fmt.Errorf("read %d = %w", index, err)
							}
						}
					}
				}(worker)
			}
			wait.Wait()
			close(failures)
			for failure := range failures {
				return failure
			}
			return nil
		})
		if err != nil {
			t.Fatalf("transaction = %v", err)
		}
		expected := 0
		for _, id := range all {
			if persisted[id] {
				expected++
			}
			if _, ok := fixture.teamOwner(t, id); ok != persisted[id] {
				t.Fatalf("team %s persisted=%t want %t", id, ok, persisted[id])
			}
		}
		if got := fixture.outboxRows(t) - before; got != expected {
			t.Fatalf("outbox rows=%d want %d", got, expected)
		}
	})
}
