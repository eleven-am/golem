package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

func (fixture *p10OperationFixture) lateWrite(t *testing.T, write func(context.Context, *p10operations.Caller[p10operations.Principal]) error) error {
	t.Helper()
	caller := fixture.caller(t, "alpha")
	blocked, proceed := fixture.holdConnectionPool(t)
	late := make(chan error, 1)
	p10operations.Reset(func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
		if operation != "inviteMember" {
			return nil
		}
		go func() { late <- write(context.Background(), scoped) }()
		p10AwaitCondition(func() bool { return blocked() || len(late) != 0 })
		return errP10StopOperation
	})
	if _, err := p10operations.Mutate(context.Background(), caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 1999), "stop@example.test")); !errors.Is(err, errP10StopOperation) {
		t.Fatalf("operation = %v", err)
	}
	proceed()
	return <-late
}

func (fixture *p10OperationFixture) teamOwner(t *testing.T, id golem.UUID) (string, bool) {
	t.Helper()
	row, err := fixture.app.System().Teams.FindUnique(context.Background(), p10operations.Teams.ByID.Value(id), p10operations.Teams.Select(p10operations.Teams.Owner))
	if err != nil {
		return "", false
	}
	owner, _ := golem.Value(row, p10operations.Teams.Owner).Get()
	return owner, true
}

func TestOperationWriteTheCallersPolicyAllowsCommitsAfterTheOperationAcrossProviders(t *testing.T) {
	t.Run("create on a model the operation grants no create on", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			team := p10OperationID(t, 1100)
			err := fixture.lateWrite(t, func(ctx context.Context, caller *p10operations.Caller[p10operations.Principal]) error {
				_, err := caller.Teams.Create(ctx, p10operations.Teams.Create(p10operations.Teams.ID.Create(team), p10operations.Teams.Owner.Create("alpha")))
				return err
			})
			if err != nil {
				t.Fatalf("a write the caller's own policy allows was refused after the operation: %v", err)
			}
			if owner, ok := fixture.teamOwner(t, team); !ok || owner != "alpha" {
				t.Fatal("a write the caller's own policy allows did not commit after the operation")
			}
		})
	})
	t.Run("update of a field no Within grant names", func(t *testing.T) {
		forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
			invite := p10OperationID(t, 1101)
			fixture.seedInvite(t, invite)
			other := p10OperationID(t, 1102)
			if _, err := fixture.app.System().Teams.Create(context.Background(), p10operations.Teams.Create(p10operations.Teams.ID.Create(other), p10operations.Teams.Owner.Create("alpha"))); err != nil {
				t.Fatal(err)
			}
			err := fixture.lateWrite(t, func(ctx context.Context, caller *p10operations.Caller[p10operations.Principal]) error {
				_, err := caller.Invites.Update(ctx, p10operations.Invites.ByID.Value(invite), p10operations.Invites.Update(p10operations.Invites.TeamID.Set(other)))
				return err
			})
			if err != nil {
				t.Fatalf("a write the caller's own policy allows was refused after the operation: %v", err)
			}
			row, readErr := fixture.app.System().Invites.FindUnique(context.Background(), p10operations.Invites.ByID.Value(invite), p10operations.Invites.Select(p10operations.Invites.TeamID))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if team, _ := golem.Value(row, p10operations.Invites.TeamID).Get(); team != other {
				t.Fatal("a write the caller's own policy allows did not commit after the operation")
			}
		})
	})
}

func TestOperationWriteWhoseGrantCannotBeProvedRedundantIsRefusedLateAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		err := fixture.lateWrite(t, func(ctx context.Context, caller *p10operations.Caller[p10operations.Principal]) error {
			_, err := caller.Teams.Delete(ctx, p10operations.Teams.ByID.Value(fixture.alpha))
			return err
		})
		assertP10Conflict(t, "delete the Within grant widens", p10Conflict{operation: "delete", model: p10TeamModel(), message: "mutation conflicted", hooks: "[]"}, err)
		if _, ok := fixture.teamOwner(t, fixture.alpha); !ok {
			t.Fatal("a write whose grant could not be proved redundant committed after the operation")
		}
		if _, err := fixture.caller(t, "alpha").Teams.Delete(context.Background(), p10operations.Teams.ByID.Value(fixture.alpha)); err != nil {
			t.Fatalf("the caller's own policy refuses the retried delete: %v", err)
		}
	})
}

func TestLateTransactionCommitsWhenItsOnlyGrantWriteWasRefusedAcrossProviders(t *testing.T) {
	forEachP10OperationProfile(t, func(t *testing.T, fixture *p10OperationFixture) {
		caller := fixture.caller(t, "alpha")
		invite := p10OperationID(t, 1200)
		team := p10OperationID(t, 1201)
		written := make(chan struct{})
		proceed := make(chan struct{})
		late := make(chan error, 1)
		refused := make(chan error, 1)
		p10operations.Reset(func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
			if operation != "reviewInvite" {
				return nil
			}
			go func() {
				late <- scoped.Transaction(context.Background(), func(transaction *p10operations.CallerTx[p10operations.Principal]) error {
					_, err := transaction.Invites.Create(context.Background(), p10operations.Invites.Create(
						p10operations.Invites.ID.Create(invite), p10operations.Invites.TeamID.Create(fixture.alpha),
						p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("refused@example.test"), p10operations.Invites.Status.Create("pending"),
					))
					refused <- err
					if _, err := transaction.Teams.Create(context.Background(), p10operations.Teams.Create(p10operations.Teams.ID.Create(team), p10operations.Teams.Owner.Create("alpha"))); err != nil {
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
		if _, err := p10operations.Mutate(context.Background(), caller, p10operations.ReviewInvite, p10operations.AcceptArgs{ID: invite}); !errors.Is(err, errP10StopOperation) {
			t.Fatalf("operation = %v", err)
		}
		close(proceed)
		if err := <-refused; err == nil {
			t.Fatal("the grant-scoped write naming a field the grant does not cover was accepted")
		}
		if err := <-late; err != nil {
			t.Fatalf("a transaction whose only grant write was refused did not commit: %v", err)
		}
		if owner, ok := fixture.teamOwner(t, team); !ok || owner != "alpha" {
			t.Fatal("the caller-authorised write in the transaction did not commit")
		}
		if exists, _, _ := fixture.inviteExists(t, invite); exists {
			t.Fatal("the refused write committed")
		}
	})
}
