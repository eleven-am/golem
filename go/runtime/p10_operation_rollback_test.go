package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

var errP10FailingInnerHook = errors.New("inner write's own hook failed")

func (fixture *p10OperationFixture) outboxRows(t *testing.T) int {
	t.Helper()
	table := `"_golem_outbox"`
	if fixture.database.DriverName() != "sqlite" {
		table = `"` + string(p10OperationSystemNamespace) + `"."_golem_outbox"`
	}
	var count int
	if err := fixture.database.Get(&count, "SELECT COUNT(*) FROM "+table); err != nil {
		t.Fatal(err)
	}
	return count
}

type p10FailingInner struct {
	name    string
	prepare func(*testing.T, *p10OperationFixture, golem.UUID)
	write   func(context.Context, *p10OperationFixture, golem.HookExecutor, golem.UUID) error
	absent  func(*testing.T, *p10OperationFixture, golem.UUID) bool
}

func p10FailingInners() []p10FailingInner {
	teamAbsent := func(t *testing.T, fixture *p10OperationFixture, id golem.UUID) bool {
		_, ok := fixture.teamOwner(t, id)
		return !ok
	}
	return []p10FailingInner{
		{
			name: "nested create",
			write: func(ctx context.Context, fixture *p10OperationFixture, executor golem.HookExecutor, id golem.UUID) error {
				_, err := golem.HookCreateRow(ctx, executor, p10operations.GolemGeneratedTeamDescriptor, p10operations.Teams.Create(
					p10operations.Teams.ID.Create(id), p10operations.Teams.Owner.Create("alpha"),
					p10operations.Teams.Invites.Create(p10operations.Invites.Create(
						p10operations.Invites.ID.Create(id), p10operations.Invites.Owner.Create("alpha"),
						p10operations.Invites.Email.Create("nested@example.test"), p10operations.Invites.Status.Create("pending"),
					)),
				))
				return err
			},
			absent: func(t *testing.T, fixture *p10OperationFixture, id golem.UUID) bool {
				exists, _, _ := fixture.inviteExists(t, id)
				return !exists && teamAbsent(t, fixture, id)
			},
		},
		{
			name: "scalar create",
			write: func(ctx context.Context, fixture *p10OperationFixture, executor golem.HookExecutor, id golem.UUID) error {
				_, err := golem.HookCreateRow(ctx, executor, p10operations.GolemGeneratedTeamDescriptor, p10operations.Teams.Create(p10operations.Teams.ID.Create(id), p10operations.Teams.Owner.Create("alpha")))
				return err
			},
			absent: teamAbsent,
		},
		{
			name: "upsert",
			write: func(ctx context.Context, fixture *p10OperationFixture, executor golem.HookExecutor, id golem.UUID) error {
				_, err := golem.HookUpsertRow(ctx, executor, p10operations.GolemGeneratedTeamDescriptor, p10operations.Teams.ByID.Value(id),
					p10operations.Teams.Create(p10operations.Teams.ID.Create(id), p10operations.Teams.Owner.Create("alpha")),
					p10operations.Teams.Update(p10operations.Teams.Owner.Set("alpha")),
				)
				return err
			},
			absent: teamAbsent,
		},
		{
			name: "batch update",
			prepare: func(t *testing.T, fixture *p10OperationFixture, id golem.UUID) {
				fixture.seedInvite(t, id)
				p10operations.SetInviteUpdateManyHook(func(ctx context.Context) error {
					if p10AttemptDepth(ctx) == 1 {
						return errP10FailingInnerHook
					}
					return nil
				})
			},
			write: func(ctx context.Context, fixture *p10OperationFixture, executor golem.HookExecutor, id golem.UUID) error {
				_, err := golem.HookUpdateManyRows(ctx, executor, p10operations.GolemGeneratedInviteDescriptor, p10operations.Invites.ID.Eq(id), p10operations.Invites.UpdateMany(p10operations.Invites.TeamID.Set(fixture.beta)))
				return err
			},
			absent: func(t *testing.T, fixture *p10OperationFixture, id golem.UUID) bool {
				row, err := fixture.app.System().Invites.FindUnique(context.Background(), p10operations.Invites.ByID.Value(id), p10operations.Invites.Select(p10operations.Invites.TeamID))
				if err != nil {
					t.Fatal(err)
				}
				team, _ := golem.Value(row, p10operations.Invites.TeamID).Get()
				return team == fixture.alpha
			},
		},
	}
}

func TestFailedHookWriteLeavesNothingBehindAcrossProviders(t *testing.T) {
	for index, inner := range p10FailingInners() {
		inner := inner
		for _, variant := range []struct{ transaction, late bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
			transaction, late := variant.transaction, variant.late
			mode := "standalone"
			if transaction {
				mode = "transaction"
			}
			timing := "live"
			if late {
				timing = "late"
			}
			t.Run(fmt.Sprintf("%s/%s/%s", inner.name, mode, timing), func(t *testing.T) {
				forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
					outer, innerID := p10OperationID(t, 5000+index*10), p10OperationID(t, 5001+index*10)
					result := make(chan error, 1)
					reached := make(chan struct{})
					proceed := make(chan struct{})
					probe := func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
						if operation != "inviteMember" {
							return nil
						}
						create := func(ctx context.Context, create func(context.Context, p10operations.TeamCreateInput) error) error {
							return create(ctx, p10operations.Teams.Create(p10operations.Teams.ID.Create(outer), p10operations.Teams.Owner.Create("alpha")))
						}
						write := func() error {
							if !transaction {
								return create(context.Background(), func(ctx context.Context, input p10operations.TeamCreateInput) error {
									_, err := scoped.Teams.Create(ctx, input)
									return err
								})
							}
							return scoped.Transaction(context.Background(), func(tx *p10operations.CallerTx[p10operations.Principal]) error {
								return create(context.Background(), func(ctx context.Context, input p10operations.TeamCreateInput) error {
									_, err := tx.Teams.Create(ctx, input)
									return err
								})
							})
						}
						if !late {
							result <- write()
							return errP10StopOperation
						}
						go func() { result <- write() }()
						select {
						case <-reached:
						case err := <-result:
							result <- err
						}
						return errP10StopOperation
					}
					p10operations.Reset(probe)
					if inner.prepare != nil {
						inner.prepare(t, fixture, innerID)
					}
					before := fixture.outboxRows(t)
					var innerErr error
					p10operations.SetTeamHook(func(ctx context.Context, executor golem.HookExecutor) error {
						switch p10AttemptDepth(ctx) {
						case 0:
							innerErr = inner.write(context.WithValue(ctx, p10AttemptDepthKey{}, 1), fixture, executor, innerID)
							if late {
								close(reached)
								<-proceed
							}
							return nil
						case 1:
							return errP10FailingInnerHook
						}
						return nil
					})
					p10operations.SetInviteExecutorHook(nil)
					if _, err := p10operations.Mutate(context.Background(), fixture.caller(t, "alpha"), p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 5009+index*10), "stop@example.test")); !errors.Is(err, errP10StopOperation) {
						t.Fatalf("operation = %v", err)
					}
					close(proceed)
					if err := <-result; err != nil {
						t.Fatalf("the outer write did not commit after its hook handled the inner failure: %v", err)
					}
					if innerErr == nil {
						t.Fatal("the inner write whose own hook failed reported success")
					}
					if _, ok := fixture.teamOwner(t, outer); !ok {
						t.Fatal("the outer write was not committed")
					}
					if !inner.absent(t, fixture, innerID) {
						t.Fatal("a write that returned an error left its effects behind")
					}
					if got := fixture.outboxRows(t) - before; got != 1 {
						t.Fatalf("outbox rows written=%d want 1 for the outer write alone", got)
					}
					for _, hook := range p10operations.Snapshot().Hooks {
						if hook == "after_commit_create" {
							t.Fatal("the failed write's after-commit hook ran")
						}
					}
				})
			})
		}
	}
}
