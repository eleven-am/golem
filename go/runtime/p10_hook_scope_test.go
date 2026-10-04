package runtime_test

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

func p10ErrorChain(err error) string {
	var parts []string
	for current := err; current != nil; current = errors.Unwrap(current) {
		parts = append(parts, fmt.Sprintf("%T(%v)", current, current))
	}
	return strings.Join(parts, " <- ")
}

func (fixture *p10OperationFixture) inviteNote(t *testing.T, id golem.UUID) string {
	t.Helper()
	row, err := fixture.app.System().Invites.FindUnique(context.Background(), p10operations.Invites.ByID.Value(id), p10operations.Invites.Select(p10operations.Invites.Note))
	if err != nil {
		t.Fatal(err)
	}
	note, _ := golem.Value(row, p10operations.Invites.Note).Get()
	return note
}

func p10ParentIsWaitingForItsNestedCall() bool {
	buffer := make([]byte, 1<<22)
	stacks := string(buffer[:goruntime.Stack(buffer, true)])
	for _, stack := range strings.Split(stacks, "\n\n") {
		if strings.Contains(stack, "runtime.openHookScope.func") && strings.Contains(stack, "sync.(*Cond).Wait") {
			return true
		}
	}
	return false
}

type p10HookParent struct {
	name        string
	facts       int
	batch       bool
	inOperation bool
	prepare     func(*testing.T, *p10OperationFixture, golem.UUID)
	write       func(*p10OperationFixture, *p10operations.CallerTx[p10operations.Principal], golem.UUID) error
	exists      func(*testing.T, *p10OperationFixture, golem.UUID) bool
}

func p10HookParents() []p10HookParent {
	teamExists := func(t *testing.T, fixture *p10OperationFixture, id golem.UUID) bool {
		_, ok := fixture.teamOwner(t, id)
		return ok
	}
	return []p10HookParent{
		{
			name:  "scalar create",
			facts: 1,
			write: func(_ *p10OperationFixture, tx *p10operations.CallerTx[p10operations.Principal], id golem.UUID) error {
				_, err := tx.Teams.Create(context.Background(), p10TeamInput(id))
				return err
			},
			exists: teamExists,
		},
		{
			name:  "upsert",
			facts: 1,
			write: func(_ *p10OperationFixture, tx *p10operations.CallerTx[p10operations.Principal], id golem.UUID) error {
				_, err := tx.Teams.Upsert(context.Background(), p10operations.Teams.ByID.Value(id), p10TeamInput(id), p10operations.Teams.Update(p10operations.Teams.Owner.Set("alpha")))
				return err
			},
			exists: teamExists,
		},
		{
			name:        "nested relation create",
			facts:       2,
			inOperation: true,
			write: func(_ *p10OperationFixture, tx *p10operations.CallerTx[p10operations.Principal], id golem.UUID) error {
				_, err := tx.Teams.Create(context.Background(), p10operations.Teams.Create(
					p10operations.Teams.ID.Create(id), p10operations.Teams.Owner.Create("alpha"),
					p10operations.Teams.Invites.Create(p10operations.Invites.Create(
						p10operations.Invites.ID.Create(id), p10operations.Invites.Owner.Create("alpha"),
						p10operations.Invites.Email.Create("nested@example.test"), p10operations.Invites.Status.Create("pending"),
					)),
				))
				return err
			},
			exists: func(t *testing.T, fixture *p10OperationFixture, id golem.UUID) bool {
				invite, _, _ := fixture.inviteExists(t, id)
				return invite && teamExists(t, fixture, id)
			},
		},
		{
			name:    "batch update",
			facts:   1,
			batch:   true,
			prepare: func(t *testing.T, fixture *p10OperationFixture, id golem.UUID) { fixture.seedInvite(t, id) },
			write: func(fixture *p10OperationFixture, tx *p10operations.CallerTx[p10operations.Principal], id golem.UUID) error {
				_, err := tx.Invites.UpdateMany(context.Background(), p10operations.Invites.ID.Eq(id), p10operations.Invites.UpdateMany(p10operations.Invites.Note.Set("parent")))
				return err
			},
			exists: func(t *testing.T, fixture *p10OperationFixture, id golem.UUID) bool {
				return fixture.inviteNote(t, id) == "parent"
			},
		},
	}
}

func TestDirectCallNestedThroughAHookContextFinishesBeforeItsParentContinuesAcrossProviders(t *testing.T) {
	for index, parent := range p10HookParents() {
		for _, nestedFails := range []bool{false, true} {
			index, parent, nestedFails := index, parent, nestedFails
			outcome := "nested succeeds"
			if nestedFails {
				outcome = "nested fails"
			}
			t.Run(parent.name+"/"+outcome, func(t *testing.T) {
				forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
					caller := fixture.caller(t, "alpha")
					parentID, nested := p10OperationID(t, 7100+index*10), p10OperationID(t, 7101+index*10)
					if parent.prepare != nil {
						parent.prepare(t, fixture, parentID)
					}
					fixture.seedInvite(t, nested)
					childStarted := make(chan struct{})
					childResult := make(chan error, 1)
					var tx *p10operations.CallerTx[p10operations.Principal]
					var childEarly error
					spawn := func(ctx context.Context) error {
						go func() {
							_, err := tx.Invites.UpdateMany(context.WithValue(ctx, p10AttemptDepthKey{}, 1), p10operations.Invites.ID.Eq(nested), p10operations.Invites.UpdateMany(p10operations.Invites.Note.Set("nested")))
							childResult <- err
						}()
						select {
						case <-childStarted:
							return nil
						case err := <-childResult:
							childEarly = err
							childResult <- err
							return fmt.Errorf("nested write finished before its hook ran: %w", err)
						}
					}
					var operationResult error
					body := func(inner *p10operations.CallerTx[p10operations.Principal]) error {
						tx = inner
						if err := parent.write(fixture, tx, parentID); err != nil {
							return fmt.Errorf("parent write = %w", err)
						}
						childErr := <-childResult
						childResult <- childErr
						return nil
					}
					var probe p10operations.Probe
					if parent.inOperation {
						probe = func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
							if operation != "inviteMember" {
								return nil
							}
							operationResult = scoped.Transaction(context.Background(), body)
							return errP10StopOperation
						}
					}
					p10operations.Reset(probe)
					p10operations.SetTeamHook(func(ctx context.Context, _ golem.HookExecutor) error {
						if parent.batch {
							return nil
						}
						return spawn(ctx)
					})
					p10operations.SetInviteUpdateManyExecutorHook(func(ctx context.Context, _ golem.HookExecutor) error {
						if p10AttemptDepth(ctx) == 0 {
							return spawn(ctx)
						}
						close(childStarted)
						p10AwaitCondition(p10ParentIsWaitingForItsNestedCall)
						if nestedFails {
							return errP10FailingInnerHook
						}
						return nil
					})
					before := fixture.outboxRows(t)
					var childErr, err error
					p10FinishesWithin(t, 20*time.Second, func() {
						if !parent.inOperation {
							err = caller.Transaction(context.Background(), body)
							return
						}
						if _, mutateErr := p10operations.Mutate(context.Background(), caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 7199), "stop@example.test")); !errors.Is(mutateErr, errP10StopOperation) {
							err = fmt.Errorf("operation = %w", mutateErr)
							return
						}
						err = operationResult
					})
					if err == nil {
						childErr = <-childResult
					}
					if err != nil {
						t.Fatalf("transaction = %s\n  nested write before its hook: %s", p10ErrorChain(err), p10ErrorChain(childEarly))
					}
					if nestedFails != (childErr != nil) {
						t.Fatalf("nested write = %s", p10ErrorChain(childErr))
					}
					if !parent.exists(t, fixture, parentID) {
						t.Fatal("the parent write did not commit")
					}
					if applied := fixture.inviteNote(t, nested) == "nested"; applied == nestedFails {
						t.Fatalf("nested write applied=%t with a failing hook=%t", applied, nestedFails)
					}
					want := parent.facts
					if !nestedFails {
						want++
					}
					if got := fixture.outboxRows(t) - before; got != want {
						t.Fatalf("outbox rows=%d want %d (nested write error: %s)", got, want, p10ErrorChain(childErr))
					}
				})
			})
		}
	}
}

func TestSystemEscapeAndExecutorReadAreRefusedAfterTheirScopeAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		caller := fixture.caller(t, "alpha")
		team, late := p10OperationID(t, 7300), p10OperationID(t, 7301)
		var escape *p10operations.SystemTx[p10operations.Principal]
		var escaped golem.HookExecutor
		p10operations.Reset(nil)
		p10operations.SetTeamHook(func(_ context.Context, executor golem.HookExecutor) error {
			escaped = executor
			return nil
		})
		if err := caller.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
			escape = p10operations.SystemEscape(tx)
			_, err := tx.Teams.Create(context.Background(), p10TeamInput(team))
			return err
		}); err != nil {
			t.Fatalf("transaction = %v", err)
		}
		if _, err := escape.Teams.Create(context.Background(), p10TeamInput(late)); err == nil || !strings.Contains(err.Error(), "after its callback returned") {
			t.Fatalf("late system escape write = %v", err)
		}
		if _, ok := fixture.teamOwner(t, late); ok {
			t.Fatal("a write through a retained system escape persisted")
		}
		if _, err := golem.HookFindManyRows(context.Background(), escaped, p10operations.GolemGeneratedTeamDescriptor, golem.Where(p10operations.Teams.Owner.Eq("alpha"))); err == nil || !strings.Contains(err.Error(), "after its hook returned") {
			t.Fatalf("late executor read = %v", err)
		}
	})
}

func TestTransactionRefusalNamesTheWriteThatUsedTheEndedGrantAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		ctx := context.Background()
		caller := fixture.caller(t, "alpha")
		team, created := p10OperationID(t, 7400), p10OperationID(t, 7401)
		written := make(chan struct{})
		proceed := make(chan struct{})
		late := make(chan error, 1)
		p10operations.Reset(func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
			if operation != "inviteMember" {
				return nil
			}
			go func() {
				late <- scoped.Transaction(context.Background(), func(transaction *p10operations.CallerTx[p10operations.Principal]) error {
					if _, err := transaction.Teams.Create(context.Background(), p10TeamInput(team)); err != nil {
						return err
					}
					if _, err := transaction.Invites.Create(context.Background(), p10operations.Invites.Create(
						p10operations.Invites.ID.Create(created), p10operations.Invites.TeamID.Create(fixture.alpha),
						p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("tx@example.test"), p10operations.Invites.Status.Create("pending"),
					)); err != nil {
						return err
					}
					close(written)
					<-proceed
					return nil
				})
			}()
			<-written
			return errP10StopOperation
		})
		if _, err := p10operations.Mutate(ctx, caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 7402), "stop@example.test")); !errors.Is(err, errP10StopOperation) {
			t.Fatalf("operation = %v", err)
		}
		close(proceed)
		assertP10Conflict(t, "transaction whose second write used the ended grant", p10Conflict{operation: "create", model: p10InviteModel(), message: "mutation conflicted"}, <-late)
		if _, ok := fixture.teamOwner(t, team); ok {
			t.Fatal("a refused transaction committed its first write")
		}
	})
}
