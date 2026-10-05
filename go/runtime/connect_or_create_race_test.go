package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jackc/pgx/v5/pgconn"
)

func forEachPostgreSQLSocialFixture(t *testing.T, run func(*testing.T, socialMutationFixture)) {
	t.Helper()
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			run(t, newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil))
		})
	}
}

func TestPostgreSQLConnectOrCreateRedecidesAfterAConcurrentInsertOfItsTarget(t *testing.T) {
	for name, racer := range map[string]string{"readable racer": "visible-racer", "unreadable racer": "hidden-racer"} {
		racer := racer
		t.Run(name, func(t *testing.T) {
			forEachPostgreSQLSocialFixture(t, func(t *testing.T, fixture socialMutationFixture) {
				ctx := context.Background()
				fixture.seedUsers(t, 2)
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postRootCreate(54, 2, "visible")); err != nil {
					t.Fatal(err)
				}
				var creates atomic.Int64
				users := nestedAcceptanceTable(fixture.app, fixture.schema.User)
				database := fixture.app.database
				hooks := []golem.HookBinding[graphMutationActor]{
					golem.GeneratedBeforeHookBinding[graphMutationActor, socialMutationUser, golem.CreateHookRequest[socialMutationUser]](fixture.schema.User, golem.HookCreate, func(ctx context.Context, _ *golem.CreateHookRequest[socialMutationUser]) error {
						if creates.Add(1) == 1 {
							_, err := database.ExecContext(ctx, `INSERT INTO `+users+` ("id", "name") VALUES ($1, $2)`, mutationResultUUIDText(9), racer)
							return err
						}
						return nil
					}),
				}
				fixture = reopenSocialMutation(t, fixture, socialPolicies(fixture,
					func(rules *golem.Rules[socialMutationUser]) {
						rules.CanRead(fixture.userName.StartsWith("visible"))
						rules.CanCreate(golem.All[socialMutationUser]())
						rules.CanUpdate(fixture.userName.StartsWith("visible"))
					}, nil), hooks)
				caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
				if err != nil {
					t.Fatal(err)
				}
				coc := golem.GeneratedNestedConnectOrCreate[socialMutationPost, socialMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.PostAuthorship, fixture.schema.User, fixture.userTarget(9), fixture.userCreate(9, "visible-mine"))
				_, err = CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(54), golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post, coc))
				if creates.Load() != 1 {
					t.Fatalf("create branch ran %d times, want exactly the one that met the racer", creates.Load())
				}
				var stored string
				if err := database.GetContext(ctx, &stored, `SELECT "name" FROM `+users+` WHERE "id" = $1`, mutationResultUUIDText(9)); err != nil || stored != racer {
					t.Fatalf("selector row name=%q err=%v; want the racer's row untouched", stored, err)
				}
				if racer == "hidden-racer" {
					assertSocialNotFound(t, err)
					if author := socialPostAuthor(t, fixture, 54); author != mutationResultUUIDText(2) {
						t.Fatalf("unreadable racer was linked: author=%s", author)
					}
					return
				}
				if err != nil {
					t.Fatalf("connectOrCreate after a readable racer: %v", err)
				}
				if author := socialPostAuthor(t, fixture, 54); author != mutationResultUUIDText(9) {
					t.Fatalf("readable racer was not linked: author=%s", author)
				}
			})
		})
	}
}

func TestPostgreSQLConnectOrCreateHoldsItsSelectedTargetUntilTheWrite(t *testing.T) {
	forEachPostgreSQLSocialFixture(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		fixture.seedUsers(t, 1, 2)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postRootCreate(57, 1, "selected")); err != nil {
			t.Fatal(err)
		}
		posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
		database := fixture.app.database
		var deleteErr error
		var updates atomic.Int64
		hooks := []golem.HookBinding[graphMutationActor]{
			golem.GeneratedBeforeHookBinding[graphMutationActor, socialMutationPost, golem.UpdateHookRequest[socialMutationPost]](fixture.schema.Post, golem.HookUpdate, func(ctx context.Context, _ *golem.UpdateHookRequest[socialMutationPost]) error {
				updates.Add(1)
				conn, err := database.Conn(ctx)
				if err != nil {
					return err
				}
				defer conn.Close()
				if _, err := conn.ExecContext(ctx, `SET lock_timeout = '200ms'`); err != nil {
					return err
				}
				_, deleteErr = conn.ExecContext(ctx, `DELETE FROM `+posts+` WHERE "id" = $1`, mutationResultUUIDText(57))
				_, err = conn.ExecContext(ctx, `RESET lock_timeout`)
				return err
			}),
		}
		fixture = reopenSocialMutation(t, fixture, socialPolicies(fixture, nil, nil), hooks)
		caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		coc := golem.GeneratedNestedConnectOrCreate[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post, fixture.postTarget(57), fixture.postCreate(57, "unused"))
		if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, fixture.userTarget(2), golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User, coc)); err != nil {
			t.Fatal(err)
		}
		if updates.Load() == 0 {
			t.Fatal("the connect branch ran no update hook, so the race window was not exercised")
		}
		var lockTimeout *pgconn.PgError
		if !errors.As(deleteErr, &lockTimeout) || lockTimeout.Code != "55P03" {
			t.Fatalf("concurrent delete of the selected target err=%v; want lock_not_available while the branch holds it", deleteErr)
		}
		if author := socialPostAuthor(t, fixture, 57); author != mutationResultUUIDText(2) {
			t.Fatalf("selected target author=%s; want it linked to user 2", author)
		}
	})
}
