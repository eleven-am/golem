package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

var errP10InnerAfterHook = errors.New("inner after hook failed")

func TestFailedHookWriteThatRanStatementsStillCountsAcrossProviders(t *testing.T) {
	for _, late := range []bool{false, true} {
		late := late
		name := "live"
		if late {
			name = "late"
		}
		t.Run(name, func(t *testing.T) {
			forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
				outer, innerTeam, innerInvite := p10OperationID(t, 4000), p10OperationID(t, 4001), p10OperationID(t, 4002)
				reached := make(chan struct{})
				proceed := make(chan struct{})
				var innerErr error
				result := make(chan error, 1)
				caller := fixture.caller(t, "alpha")
				p10operations.Reset(func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
					if operation != "inviteMember" {
						return nil
					}
					write := func() error {
						_, err := scoped.Teams.Create(context.Background(), p10operations.Teams.Create(p10operations.Teams.ID.Create(outer), p10operations.Teams.Owner.Create("alpha")))
						return err
					}
					if !late {
						result <- write()
						return errP10StopOperation
					}
					go func() { result <- write() }()
					<-reached
					return errP10StopOperation
				})
				p10operations.SetTeamHook(func(ctx context.Context, executor golem.HookExecutor) error {
					switch p10AttemptDepth(ctx) {
					case 0:
						_, innerErr = golem.HookCreateRow(context.WithValue(ctx, p10AttemptDepthKey{}, 1), executor, p10operations.GolemGeneratedTeamDescriptor, p10operations.Teams.Create(
							p10operations.Teams.ID.Create(innerTeam), p10operations.Teams.Owner.Create("alpha"),
							p10operations.Teams.Invites.Create(p10operations.Invites.Create(
								p10operations.Invites.ID.Create(innerInvite), p10operations.Invites.Owner.Create("alpha"),
								p10operations.Invites.Email.Create("nested@example.test"), p10operations.Invites.Status.Create("pending"),
							)),
						))
						if late {
							close(reached)
							<-proceed
						}
						return nil
					case 1:
						return errP10InnerAfterHook
					}
					return nil
				})
				if _, err := p10operations.Mutate(context.Background(), caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 4003), "stop@example.test")); !errors.Is(err, errP10StopOperation) {
					t.Fatalf("operation = %v", err)
				}
				close(proceed)
				err := <-result
				if innerErr == nil {
					t.Fatal("the inner hook write did not fail")
				}
				innerPersisted, _, _ := fixture.inviteExists(t, innerInvite)
				_, outerPersisted := fixture.teamOwner(t, outer)
				if !late {
					if err != nil || !outerPersisted || !innerPersisted {
						t.Fatalf("live outer write = %v outer=%t inner=%t", err, outerPersisted, innerPersisted)
					}
					return
				}
				var failure *golem.Error
				if !errors.As(err, &failure) || failure.Code != golem.CodeConflict || failure.Operation != "create" || failure.Model != p10TeamModel() {
					t.Fatalf("a failed attempt whose grant-authorised rows persisted let the late write commit: %v", err)
				}
				if outerPersisted || innerPersisted {
					t.Fatalf("late refused write persisted outer=%t inner=%t", outerPersisted, innerPersisted)
				}
			})
		})
	}
}
