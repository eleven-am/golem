package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/mutation/rowlock"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

func authoredByAlicePolicies(observer observe.Observer) func(schematest.Fixture, *Config[mutationResultPrincipal, mutationResultActor]) {
	return func(schemaFixture schematest.Fixture, config *Config[mutationResultPrincipal, mutationResultActor]) {
		title := golem.GeneratedTextField[mutationResultPost, string](schemaFixture.PostTitle)
		userName := golem.GeneratedTextField[mutationResultUser, string](schemaFixture.UserName)
		author := golem.GeneratedToOne[mutationResultPost, mutationResultUser](schemaFixture.PostAuthor, schemaFixture.Authorship, schemaFixture.User)
		ownerAlice := author.Is(userName.Eq("alice"))
		users := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schemaFixture.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[mutationResultUser]()
			rules.CanRead(golem.All[mutationResultUser]())
			rules.CanCreate(golem.All[mutationResultUser]())
			rules.CanUpdate(golem.All[mutationResultUser]())
			rules.CanDelete(golem.All[mutationResultUser]())
			return rules.Freeze(schemaFixture.User)
		})
		posts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schemaFixture.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[mutationResultPost]()
			rules.CanRead(golem.All[mutationResultPost]())
			rules.CanCreate(golem.All[mutationResultPost]())
			rules.CanUpdate(golem.All[mutationResultPost]())
			rules.CannotUpdateFields(golem.All[mutationResultPost](), title)
			rules.CanUpdateFields(ownerAlice, title)
			rules.CanDelete(ownerAlice)
			return rules.Freeze(schemaFixture.Post)
		})
		bindings, err := golem.GeneratedApplicationBindings(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schemaFixture.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{users, posts}, nil))
		if err != nil {
			panic(err)
		}
		config.Bindings = bindings
		if observer != nil {
			config.Observer = observer
		}
	}
}

func TestPostgreSQLWriteThatWaitedForItsRowSeesAGrantRevokedWhileItWaited(t *testing.T) {
	runConfiguredRelationDeleteProviderProfiles(t, "fresh_grant", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, compilerir.ActionCascade)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(compilerir.ActionCascade), authoredByAlicePolicies(nil), func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		if profile.provider != golem.PostgreSQL {
			t.Skip("a write waits for a row lock only on PostgreSQL")
		}
		ctx, fixture := context.Background(), profile.fixture
		seedCascadeUser(t, fixture, 7, "alice")
		for _, post := range []byte{40, 41} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post, golem.UUID{15: 7}, "kept")); err != nil {
				t.Fatal(err)
			}
		}
		caller := mustMutationResultCaller(t, fixture)
		users := nestedAcceptanceTable(fixture.app, fixture.schema.User)
		database := fixture.app.database
		revokeWhileWaiting := func(post byte, write func(context.Context) error) error {
			if _, err := database.ExecContext(ctx, `UPDATE `+users+` SET "name" = 'alice' WHERE "id" = $1`, mutationResultUUIDText(7)); err != nil {
				t.Fatal(err)
			}
			gate, err := database.BeginTxx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := gate.ExecContext(ctx, `SELECT 1 FROM `+profile.posts+` WHERE "id" = $1 FOR UPDATE`, mutationResultUUIDText(post)); err != nil {
				_ = gate.Rollback()
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- write(ctx) }()
			deadline := time.Now().Add(15 * time.Second)
			for lockWaiters(t, database, lockOrderMarker(profile.posts)) == 0 {
				if time.Now().After(deadline) {
					_ = gate.Rollback()
					t.Fatal("the write never waited for the held row")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if _, err := database.ExecContext(ctx, `UPDATE `+users+` SET "name" = 'revoked' WHERE "id" = $1`, mutationResultUUIDText(7)); err != nil {
				_ = gate.Rollback()
				t.Fatal(err)
			}
			if err := gate.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				return err
			case <-time.After(30 * time.Second):
				t.Fatal("the write did not finish after the row was released")
				return nil
			}
		}
		var failure *golem.Error

		err := revokeWhileWaiting(40, func(ctx context.Context) error {
			_, err := CallerDelete(ctx, caller, fixture.postDescriptor, fixture.target(40))
			return err
		})
		if !errors.As(err, &failure) {
			t.Errorf("a delete whose grant was revoked while it waited err=%v; want it denied", err)
		}
		err = revokeWhileWaiting(41, func(ctx context.Context) error {
			_, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(41), fixture.updateTitle("stale grant"))
			return err
		})
		if !errors.As(err, &failure) {
			t.Errorf("an update whose field grant was revoked while it waited err=%v; want it denied", err)
		}
		var titles []string
		if err := database.SelectContext(ctx, &titles, `SELECT "title" FROM `+profile.posts+` WHERE "id" IN ($1, $2) ORDER BY "id"`, mutationResultUUIDText(40), mutationResultUUIDText(41)); err != nil {
			t.Fatal(err)
		}
		if len(titles) != 2 || titles[0] != "kept" || titles[1] != "kept" {
			t.Fatalf("titles=%v; neither write may apply on a grant revoked before it held the row", titles)
		}
	})
}

func TestPostgreSQLLockReadStatementsFollowThePolicyShape(t *testing.T) {
	collector := &p8ObservationCollector{}
	runConfiguredRelationDeleteProviderProfiles(t, "lock_read_shape", schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, authoredByAlicePolicies(collector), func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		seedCascadeUser(t, fixture, 7, "alice")
		for _, post := range []byte{50, 51, 52} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post, golem.UUID{15: 7}, "shape")); err != nil {
				t.Fatal(err)
			}
		}
		caller := mustMutationResultCaller(t, fixture)
		freshRead := 0
		if profile.provider == golem.PostgreSQL {
			freshRead = 2
		}
		before := len(collector.matching(observe.KindMutation, observe.OperationMutationDelete))
		if _, err := CallerDelete(ctx, caller, fixture.postDescriptor, fixture.target(50)); err != nil {
			t.Fatal(err)
		}
		assertDeleteStatementCount(t, collector.matching(observe.KindMutation, observe.OperationMutationDelete)[before:], observe.OperationMutationDelete, 4+freshRead)
		before = len(collector.matching(observe.KindMutation, observe.OperationMutationDeleteMany))
		if count, err := CallerDeleteMany(ctx, caller, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 51}, golem.UUID{15: 52})); err != nil || count != 2 {
			t.Fatalf("delete-many count=%d err=%v", count, err)
		}
		assertDeleteStatementCount(t, collector.matching(observe.KindMutation, observe.OperationMutationDeleteMany)[before:], observe.OperationMutationDeleteMany, 5+freshRead)
	})
}

func TestPostgreSQLLockReadWhoseArgumentsFillTheParameterBoundStillLocksItsRows(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			fixture, namespace := newMutationResultPostgresFixture(t, ctx, profile)
			posts := `"` + string(namespace) + `"."posts"`
			for _, post := range []byte{60, 61, 62} {
				title := "bounded"
				if post == 62 {
					title = "other"
				}
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post, golem.UUID{15: 1}, title)); err != nil {
					t.Fatal(err)
				}
			}
			newlyMatching := atEnumeration(ctx, 1, func(ctx context.Context) error {
				_, err := fixture.app.database.ExecContext(ctx, `UPDATE `+posts+` SET "title" = 'bounded' WHERE "id" = $1`, mutationResultUUIDText(62))
				return err
			})
			model, primary := policyir.ModelID(fixture.schema.Post), policyir.FieldID(fixture.schema.PostID)
			tx, err := fixture.app.database.BeginTxx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			ledger := rowlock.NewLedger()
			session := rowlock.Session{Ledger: ledger, Queryer: tx, Registry: fixture.app.registry, Provider: policyir.ProviderPostgreSQL, MaxParameters: 1}
			reader := rowlock.Reader{Alias: "golem_t", Columns: `"golem_t"."id"`, Match: `"golem_t"."title" = $1`, Args: []any{"bounded"}}
			decoder, err := readdecode.NewFields(model, fixture.app.registry, policyir.ProviderPostgreSQL, []policyir.FieldID{primary})
			if err != nil {
				t.Fatal(err)
			}
			locked, err := rowlock.SelectMatching(newlyMatching, session, []rowlock.Reader{reader}, func(ctx context.Context, _ int) ([]mutationdecode.Row, []rowlock.Key, error) {
				rows, err := queryIdentityRows(ctx, tx, fixture.app.registry, policyir.ProviderPostgreSQL, model, []policyir.FieldID{primary}, `SELECT "golem_t"."id" FROM `+posts+` AS "golem_t" WHERE "golem_t"."title" = $1 ORDER BY "golem_t"."id"`, []any{"bounded"})
				if err != nil {
					return nil, nil, err
				}
				keys, err := rowlock.RowKeys(fixture.app.registry, rows)
				return rows, keys, err
			}, func(_ int, rows *sqlx.Rows, trailing ...any) (mutationdecode.Row, error) {
				scan := decoder.NewScan()
				if err := rows.Scan(append(scan.Destinations(), trailing...)...); err != nil {
					return mutationdecode.Row{}, err
				}
				cells, err := scan.Decode()
				if err != nil {
					return mutationdecode.Row{}, err
				}
				return mutationdecode.FromReadCells(fixture.app.registry, model, cells)
			})
			if err != nil {
				t.Fatalf("a selection whose own argument fills the parameter bound: %v", err)
			}
			if len(locked[0]) != 2 {
				t.Fatalf("selected %d rows; want the two rows it locked, not the row that matched after enumeration", len(locked[0]))
			}
			keys, err := rowlock.RowKeys(fixture.app.registry, locked[0])
			if err != nil {
				t.Fatal(err)
			}
			if acquired, err := session.Lock(ctx, keys); err != nil || acquired {
				t.Fatalf("acquired=%t err=%v; every selected row must already be held", acquired, err)
			}
		})
	}
}
