package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

type p10AttemptDepthKey struct{}

type p10InnerWrite string

const (
	p10InnerNone        p10InnerWrite = "none"
	p10InnerGrant       p10InnerWrite = "grant-succeeds"
	p10InnerGrantFailed p10InnerWrite = "grant-fails-handled"
	p10InnerCaller      p10InnerWrite = "caller-succeeds"
)

type p10AttemptRow struct {
	outerNeedsGrant bool
	inner           p10InnerWrite
	depth           int
	transaction     bool
	late            bool
}

func (row p10AttemptRow) name() string {
	outer := "caller-outer"
	if row.outerNeedsGrant {
		outer = "grant-outer"
	}
	mode := "standalone"
	if row.transaction {
		mode = "transaction"
	}
	timing := "live"
	if row.late {
		timing = "late"
	}
	return fmt.Sprintf("%s/%s/depth-%d/%s/%s", outer, row.inner, row.depth, mode, timing)
}

func p10AttemptRows() []p10AttemptRow {
	var rows []p10AttemptRow
	for _, outer := range []bool{false, true} {
		for _, inner := range []p10InnerWrite{p10InnerNone, p10InnerGrant, p10InnerGrantFailed, p10InnerCaller} {
			for _, depth := range []int{1, 2} {
				for _, transaction := range []bool{false, true} {
					for _, late := range []bool{false, true} {
						rows = append(rows, p10AttemptRow{outerNeedsGrant: outer, inner: inner, depth: depth, transaction: transaction, late: late})
					}
				}
			}
		}
	}
	return rows
}

type p10AttemptIDs struct {
	outer, middle, inner golem.UUID
}

func p10AttemptDepth(ctx context.Context) int {
	depth, _ := ctx.Value(p10AttemptDepthKey{}).(int)
	return depth
}

func (fixture *p10OperationFixture) attemptInner(ctx context.Context, row p10AttemptRow, ids p10AttemptIDs, executor golem.HookExecutor) error {
	inner := context.WithValue(ctx, p10AttemptDepthKey{}, row.depth)
	switch row.inner {
	case p10InnerGrant:
		_, err := golem.HookCreateRow(inner, executor, p10operations.GolemGeneratedInviteDescriptor, p10operations.Invites.Create(
			p10operations.Invites.ID.Create(ids.inner), p10operations.Invites.TeamID.Create(fixture.alpha),
			p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("inner@example.test"), p10operations.Invites.Status.Create("pending"),
		))
		return err
	case p10InnerGrantFailed:
		_, err := golem.HookCreateRow(inner, executor, p10operations.GolemGeneratedInviteDescriptor, p10operations.Invites.Create(
			p10operations.Invites.ID.Create(ids.inner), p10operations.Invites.TeamID.Create(fixture.alpha),
			p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("refused@example.test"), p10operations.Invites.Status.Create("pending"),
			p10operations.Invites.Note.Create("outside the grant"),
		))
		if err == nil {
			return errors.New("the inner write outside the grant succeeded")
		}
		return nil
	case p10InnerCaller:
		_, err := golem.HookCreateRow(inner, executor, p10operations.GolemGeneratedTeamDescriptor, p10operations.Teams.Create(p10operations.Teams.ID.Create(ids.inner), p10operations.Teams.Owner.Create("alpha")))
		return err
	}
	return nil
}

func (fixture *p10OperationFixture) installAttemptHooks(row p10AttemptRow, ids p10AttemptIDs, reached func()) {
	outerModel := "team"
	if row.outerNeedsGrant {
		outerModel = "invite"
	}
	hook := func(model string) p10operations.TeamHook {
		return func(ctx context.Context, executor golem.HookExecutor) error {
			depth := p10AttemptDepth(ctx)
			switch {
			case depth == 0 && model == outerModel:
				var err error
				if row.depth == 2 {
					_, err = golem.HookCreateRow(context.WithValue(ctx, p10AttemptDepthKey{}, 1), executor, p10operations.GolemGeneratedTeamDescriptor, p10operations.Teams.Create(p10operations.Teams.ID.Create(ids.middle), p10operations.Teams.Owner.Create("alpha")))
				} else {
					err = fixture.attemptInner(ctx, row, ids, executor)
				}
				if err == nil && reached != nil {
					reached()
				}
				return err
			case depth == 1 && model == "team" && row.depth == 2:
				return fixture.attemptInner(ctx, row, ids, executor)
			}
			return nil
		}
	}
	p10operations.SetTeamHook(hook("team"))
	p10operations.SetInviteExecutorHook(hook("invite"))
}

func (fixture *p10OperationFixture) attemptWrite(ctx context.Context, row p10AttemptRow, ids p10AttemptIDs, caller *p10operations.Caller[p10operations.Principal], beforeCommit func()) error {
	create := func(ctx context.Context, invites func(context.Context, p10operations.InviteCreateInput) error, teams func(context.Context, p10operations.TeamCreateInput) error) error {
		if row.outerNeedsGrant {
			return invites(ctx, p10operations.Invites.Create(
				p10operations.Invites.ID.Create(ids.outer), p10operations.Invites.TeamID.Create(fixture.alpha),
				p10operations.Invites.Owner.Create("alpha"), p10operations.Invites.Email.Create("outer@example.test"), p10operations.Invites.Status.Create("pending"),
			))
		}
		return teams(ctx, p10operations.Teams.Create(p10operations.Teams.ID.Create(ids.outer), p10operations.Teams.Owner.Create("alpha")))
	}
	if !row.transaction {
		return create(ctx, func(ctx context.Context, input p10operations.InviteCreateInput) error {
			_, err := caller.Invites.Create(ctx, input)
			return err
		}, func(ctx context.Context, input p10operations.TeamCreateInput) error {
			_, err := caller.Teams.Create(ctx, input)
			return err
		})
	}
	return caller.Transaction(ctx, func(transaction *p10operations.CallerTx[p10operations.Principal]) error {
		err := create(ctx, func(ctx context.Context, input p10operations.InviteCreateInput) error {
			_, err := transaction.Invites.Create(ctx, input)
			return err
		}, func(ctx context.Context, input p10operations.TeamCreateInput) error {
			_, err := transaction.Teams.Create(ctx, input)
			return err
		})
		if err != nil {
			return err
		}
		if beforeCommit != nil {
			beforeCommit()
		}
		return nil
	})
}

func (row p10AttemptRow) expectedHooks(committed bool) []string {
	var hooks []string
	invites := 0
	if row.outerNeedsGrant {
		hooks = append(hooks, "before_create", "after_create")
		invites++
	} else {
		hooks = append(hooks, "team_after_create")
	}
	if row.depth == 2 {
		hooks = append(hooks, "team_after_create")
	}
	switch row.inner {
	case p10InnerGrant:
		hooks = append(hooks, "before_create", "after_create")
		invites++
	case p10InnerGrantFailed:
		hooks = append(hooks, "before_create")
	case p10InnerCaller:
		hooks = append(hooks, "team_after_create")
	}
	if committed {
		for range invites {
			hooks = append(hooks, "after_commit_create")
		}
	}
	sort.Strings(hooks)
	return hooks
}

func (fixture *p10OperationFixture) exists(t *testing.T, invite bool, id golem.UUID) bool {
	t.Helper()
	if invite {
		exists, _, _ := fixture.inviteExists(t, id)
		return exists
	}
	_, ok := fixture.teamOwner(t, id)
	return ok
}

func TestOperationWriteAttemptsRecordGrantUsageInIsolationAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			p10operations.Reset(nil)
			t.Cleanup(func() { p10operations.Reset(nil) })
			fixture := newP10OperationFixture(t, profile)
			for index, row := range p10AttemptRows() {
				row := row
				ids := p10AttemptIDs{outer: p10OperationID(t, 3000+index*3), middle: p10OperationID(t, 3001+index*3), inner: p10OperationID(t, 3002+index*3)}
				t.Run(row.name(), func(t *testing.T) {
					caller := fixture.caller(t, "alpha")
					reached := make(chan struct{})
					proceed := make(chan struct{})
					var once sync.Once
					signal := func() {
						once.Do(func() {
							close(reached)
							<-proceed
						})
					}
					result := make(chan error, 1)
					p10operations.Reset(func(_ context.Context, operation string, scoped *p10operations.Caller[p10operations.Principal]) error {
						if operation != "inviteMember" {
							return nil
						}
						if !row.late {
							result <- fixture.attemptWrite(context.Background(), row, ids, scoped, nil)
							return errP10StopOperation
						}
						go func() {
							var beforeCommit func()
							if row.transaction {
								beforeCommit = signal
							}
							result <- fixture.attemptWrite(context.Background(), row, ids, scoped, beforeCommit)
						}()
						select {
						case <-reached:
						case err := <-result:
							result <- err
						}
						return errP10StopOperation
					})
					var hookSignal func()
					if row.late && !row.transaction {
						hookSignal = signal
					}
					fixture.installAttemptHooks(row, ids, hookSignal)
					if _, err := p10operations.Mutate(context.Background(), caller, p10operations.InviteMember, fixture.inviteArgs(p10OperationID(t, 9000+index), "stop@example.test")); !errors.Is(err, errP10StopOperation) {
						t.Fatalf("operation = %v", err)
					}
					close(proceed)
					err := <-result
					needsGrant := row.outerNeedsGrant || row.inner == p10InnerGrant
					committed := !row.late || !needsGrant
					outerModel := p10TeamModel()
					if row.outerNeedsGrant {
						outerModel = p10InviteModel()
					}
					if committed {
						if err != nil {
							t.Fatalf("the write did not commit: %v", err)
						}
					} else {
						var failure *golem.Error
						if !errors.As(err, &failure) || failure.Code != golem.CodeConflict || failure.Operation != "create" || failure.Model != outerModel || failure.Message != "mutation conflicted" {
							t.Fatalf("late grant-dependent write = %v, want the ordinary create conflict", err)
						}
					}
					if got := fixture.exists(t, row.outerNeedsGrant, ids.outer); got != committed {
						t.Fatalf("outer row persisted=%t want %t", got, committed)
					}
					if got := row.depth == 2 && fixture.exists(t, false, ids.middle); got != (committed && row.depth == 2) {
						t.Fatalf("middle row persisted=%t", got)
					}
					innerPersisted := committed && (row.inner == p10InnerGrant || row.inner == p10InnerCaller)
					if got := fixture.exists(t, row.inner != p10InnerCaller, ids.inner); got != innerPersisted {
						t.Fatalf("inner row persisted=%t want %t", got, innerPersisted)
					}
					hooks := append([]string(nil), p10operations.Snapshot().Hooks...)
					sort.Strings(hooks)
					if want := row.expectedHooks(committed); fmt.Sprint(hooks) != fmt.Sprint(want) {
						t.Fatalf("hooks=%v want %v", hooks, want)
					}
				})
			}
		})
	}
}
