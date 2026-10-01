package runtime_test

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

func p10HookWriteIsWaiting() bool {
	buffer := make([]byte, 1<<22)
	stacks := string(buffer[:goruntime.Stack(buffer, true)])
	for _, stack := range strings.Split(stacks, "\n\n") {
		if strings.Contains(stack, "(*executionBinding).acquire(") && strings.Contains(stack, "sync.(*Mutex).Lock") {
			return true
		}
	}
	return false
}

func p10Closed(channel chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func (fixture *p10OperationFixture) createTeam(ctx context.Context, executor golem.HookExecutor, id golem.UUID) error {
	_, err := golem.HookCreateRow(ctx, executor, p10operations.GolemGeneratedTeamDescriptor, p10operations.Teams.Create(p10operations.Teams.ID.Create(id), p10operations.Teams.Owner.Create("alpha")))
	return err
}

func TestEscapedHookExecutorIsRefusedAfterItsHookReturnsAcrossProviders(t *testing.T) {
	for _, transaction := range []bool{false, true} {
		transaction := transaction
		name := "standalone"
		if transaction {
			name = "transaction"
		}
		t.Run(name, func(t *testing.T) {
			forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
				outer, invite := p10OperationID(t, 6000), p10OperationID(t, 6001)
				escaped := make(chan golem.HookExecutor, 1)
				lateWrite := make(chan error, 1)
				hookReturned := make(chan struct{})
				operationEnded := make(chan struct{})
				attempted := make(chan struct{})
				result := make(chan error, 1)
				p10operations.Reset(func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
					if operation != "inviteMember" {
						return nil
					}
					go func() {
						create := func(ctx context.Context, create func(p10operations.TeamCreateInput) error) error {
							err := create(p10operations.Teams.Create(p10operations.Teams.ID.Create(outer), p10operations.Teams.Owner.Create("alpha")))
							close(hookReturned)
							if err != nil {
								return err
							}
							if err := <-lateWrite; err == nil || !strings.Contains(err.Error(), "after its hook returned") {
								return fmt.Errorf("escaped executor write = %v", err)
							}
							return nil
						}
						if !transaction {
							result <- create(context.Background(), func(input p10operations.TeamCreateInput) error {
								_, err := scoped.Teams.Create(context.Background(), input)
								return err
							})
							return
						}
						result <- scoped.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
							err := create(context.Background(), func(input p10operations.TeamCreateInput) error {
								_, err := tx.Teams.Create(context.Background(), input)
								return err
							})
							<-operationEnded
							return err
						})
					}()
					go func() {
						executor := <-escaped
						<-hookReturned
						_, err := golem.HookCreateRow(context.Background(), executor, p10operations.GolemGeneratedInviteDescriptor, p10operations.Invites.Create(
							p10operations.Invites.ID.Create(invite), p10operations.Invites.TeamID.Create(fixture.alpha),
							p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("escaped@example.test"), p10operations.Invites.Status.Create("pending"),
						))
						lateWrite <- err
						close(attempted)
					}()
					<-attempted
					return errP10StopOperation
				})
				p10operations.SetTeamHook(func(_ context.Context, executor golem.HookExecutor) error {
					escaped <- executor
					return nil
				})
				if _, err := p10operations.Mutate(context.Background(), fixture.caller(t, "alpha"), p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 6002), "stop@example.test")); !errors.Is(err, errP10StopOperation) {
					t.Fatalf("operation = %v", err)
				}
				close(operationEnded)
				if err := <-result; err != nil {
					t.Fatalf("outer write = %v", err)
				}
				if _, ok := fixture.teamOwner(t, outer); !ok {
					t.Fatal("the outer write did not commit")
				}
				if exists, _, _ := fixture.inviteExists(t, invite); exists {
					t.Fatal("an escaped executor's grant write persisted")
				}
			})
		})
	}
}

func TestConcurrentHookWritesOnOneTransactionAcrossProviders(t *testing.T) {
	for _, secondFails := range []bool{false, true} {
		secondFails := secondFails
		name := "both succeed"
		if secondFails {
			name = "one fails"
		}
		t.Run(name, func(t *testing.T) {
			forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
				caller := fixture.caller(t, "alpha")
				outer, first, second := p10OperationID(t, 6100), p10OperationID(t, 6101), p10OperationID(t, 6102)
				firstArrived, secondArrived, firstDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var firstErr, secondErr error
				p10operations.Reset(nil)
				p10operations.SetTeamHook(func(ctx context.Context, executor golem.HookExecutor) error {
					switch ctx.Value(p10AttemptDepthKey{}) {
					case nil:
						var wait sync.WaitGroup
						wait.Add(2)
						go func() {
							defer wait.Done()
							firstErr = fixture.createTeam(context.WithValue(ctx, p10AttemptDepthKey{}, "first"), executor, first)
							close(firstDone)
						}()
						go func() {
							defer wait.Done()
							secondErr = fixture.createTeam(context.WithValue(ctx, p10AttemptDepthKey{}, "second"), executor, second)
						}()
						wait.Wait()
						return nil
					case "first":
						close(firstArrived)
						p10AwaitCondition(func() bool { return p10Closed(secondArrived) || p10HookWriteIsWaiting() })
						return nil
					case "second":
						if p10Closed(firstArrived) && !p10Closed(firstDone) && !p10HookWriteIsWaiting() {
							close(secondArrived)
							<-firstDone
						} else {
							close(secondArrived)
						}
						if secondFails {
							return errP10FailingInnerHook
						}
						return nil
					}
					return nil
				})
				_, err := caller.Teams.Create(context.Background(), p10operations.Teams.Create(p10operations.Teams.ID.Create(outer), p10operations.Teams.Owner.Create("alpha")))
				if err != nil {
					t.Fatalf("outer write with concurrent hook writes = %v", err)
				}
				if firstErr != nil {
					t.Fatalf("first concurrent hook write = %v", firstErr)
				}
				if secondFails != (secondErr != nil) {
					t.Fatalf("second concurrent hook write = %v", secondErr)
				}
				for id, want := range map[golem.UUID]bool{outer: true, first: true, second: !secondFails} {
					if _, ok := fixture.teamOwner(t, id); ok != want {
						t.Fatalf("team %s persisted=%t want %t", id, ok, want)
					}
				}
			})
		})
	}
}

func TestReentrantHookExecutorWriteFromANestedHookAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		caller := fixture.caller(t, "alpha")
		outer, middle, reentrant := p10OperationID(t, 6200), p10OperationID(t, 6201), p10OperationID(t, 6202)
		var outerExecutor golem.HookExecutor
		var reentrantErr error
		p10operations.Reset(nil)
		p10operations.SetTeamHook(func(ctx context.Context, executor golem.HookExecutor) error {
			switch p10AttemptDepth(ctx) {
			case 0:
				outerExecutor = executor
				return fixture.createTeam(context.WithValue(ctx, p10AttemptDepthKey{}, 1), executor, middle)
			case 1:
				reentrantErr = fixture.createTeam(context.WithValue(ctx, p10AttemptDepthKey{}, 2), outerExecutor, reentrant)
				return reentrantErr
			}
			return nil
		})
		if _, err := caller.Teams.Create(context.Background(), p10operations.Teams.Create(p10operations.Teams.ID.Create(outer), p10operations.Teams.Owner.Create("alpha"))); err != nil {
			t.Fatalf("outer write = %v (re-entrant write %v)", err, reentrantErr)
		}
		for _, id := range []golem.UUID{outer, middle, reentrant} {
			if _, ok := fixture.teamOwner(t, id); !ok {
				t.Fatalf("team %s did not persist", id)
			}
		}
	})
}
