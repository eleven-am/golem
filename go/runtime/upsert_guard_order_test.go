package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

func deadlocksAfterBackendsExit(t testing.TB, dsn string) int64 {
	t.Helper()
	admin, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var deadlocks int64
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := admin.Exec(`SELECT pg_catalog.pg_stat_clear_snapshot()`); err != nil {
			t.Fatal(err)
		}
		if err := admin.Get(&deadlocks, `SELECT deadlocks FROM pg_catalog.pg_stat_database WHERE datname = pg_catalog.current_database()`); err != nil {
			t.Fatal(err)
		}
		if deadlocks != 0 || time.Now().After(deadline) {
			return deadlocks
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPostgreSQLNestedUpsertGuardTakenUnderARowLockNeverDeadlocksARootUpsert(t *testing.T) {
	for _, shared := range postgresAcceptanceProfiles() {
		shared := shared
		t.Run("postgresql-"+shared.name, func(t *testing.T) {
			if shared.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, shared.env+" is not configured")
			}
			profile := postgresAcceptanceProfile{name: shared.name, env: shared.env, dsn: testenv.DisposablePostgreSQL(t, shared.env)}
			assertNestedUpsertGuardOrder(t, newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil), profile.dsn)
		})
	}
}

func assertNestedUpsertGuardOrder(t *testing.T, fixture socialMutationFixture, dsn string) {
	ctx := context.Background()
	fixture.seedUsers(t, 1, 2)
	database := fixture.app.database
	marker := lockOrderMarker(nestedAcceptanceTable(fixture.app, fixture.schema.User))
	system := fixture.app.System()
	nestedUpsert := golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User,
		golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "nested-upsert"),
		golem.GeneratedNestedUpsert[socialMutationUser, socialMutationFriendship](fixture.schema.User, fixture.schema.UserFriendshipsFrom, fixture.schema.FriendshipOrigin, fixture.schema.Friendship,
			fixture.friendshipTarget(1, 2), golem.GeneratedCreateInput[socialMutationFriendship](fixture.schema.Friendship, fixture.connectFriendshipFriend(2)), fixture.friendshipUpdate(2)))
	var once sync.Once
	results := make(chan lockOrderOutcome, 2)
	waitErr := make(chan error, 1)
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, socialMutationFriendship, golem.CreateHookRequest[socialMutationFriendship]](fixture.schema.Friendship, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[socialMutationFriendship]) error {
			once.Do(func() {
				go func() {
					_, err := SystemUpdate(ctx, system, fixture.userDescriptor, fixture.userTarget(1), nestedUpsert)
					results <- lockOrderOutcome{name: "nested upsert under the user row", err: err}
				}()
				deadline := time.Now().Add(15 * time.Second)
				for {
					var waiting int
					if err := database.Get(&waiting, `SELECT COUNT(*) FROM pg_catalog.pg_stat_activity WHERE datname = pg_catalog.current_database() AND wait_event_type = 'Lock' AND (strpos(query, $1) > 0 OR strpos(query, 'advisory') > 0)`, marker); err != nil {
						waitErr <- err
						return
					}
					if waiting > 0 || len(results) > 0 {
						waitErr <- nil
						return
					}
					if time.Now().After(deadline) {
						waitErr <- context.DeadlineExceeded
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
			})
			return nil
		}),
	}
	fixture = reopenSocialMutation(t, fixture, socialPolicies(fixture, nil, nil), hooks)
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	create := golem.GeneratedCreateInput[socialMutationFriendship](fixture.schema.Friendship, fixture.connectFriendshipUser(1), fixture.connectFriendshipFriend(2))
	_, rootErr := CallerUpsert(ctx, caller, fixture.friendshipDescriptor, fixture.friendshipTarget(1, 2), create, fixture.friendshipUpdate(2))
	results <- lockOrderOutcome{name: "root upsert holding the guard", err: rootErr}
	if err := <-waitErr; err != nil {
		t.Fatalf("the nested upsert neither waited nor finished: %v", err)
	}
	outcomes := make([]lockOrderOutcome, 0, 2)
	for range 2 {
		select {
		case outcome := <-results:
			outcomes = append(outcomes, outcome)
		case <-time.After(30 * time.Second):
			t.Fatal("the concurrent upserts did not finish")
		}
	}
	assertSerializedOrOneConflict(t, outcomes, nil)
	if got := socialFriendships(t, fixture); len(got) != 1 || got[0] != socialFriendshipKey(1, 2) {
		t.Fatalf("friendships=%v; want exactly the one both upserts target", got)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if deadlocks := deadlocksAfterBackendsExit(t, dsn); deadlocks != 0 {
		t.Fatalf("PostgreSQL resolved %d deadlocks between the upserts", deadlocks)
	}
}
