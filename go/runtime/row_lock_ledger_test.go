package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/mutation/rowlock"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func assertPublicConflict(t testing.TB, err error) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeConflict {
		t.Fatalf("error=%v failure=%#v; want %s", err, failure, golem.CodeConflict)
	}
	var conflict *rowlock.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("CONFLICT %v does not come from the row-lock ledger", err)
	}
}

func onceAfterEnumeration(ctx context.Context, fault func(context.Context) error) context.Context {
	var once sync.Once
	return rowlock.WithEnumeratedFault(ctx, func(ctx context.Context) error {
		var err error
		once.Do(func() { err = fault(ctx) })
		return err
	})
}

func TestPostgreSQLRowsChangedBetweenEnumerationAndLockFailWithConflict(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			fixture, namespace := newMutationResultPostgresFixture(t, ctx, profile)
			posts := `"` + string(namespace) + `"."posts"`
			for _, post := range []byte{10, 11, 12} {
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post, golem.UUID{15: 1}, "before")); err != nil {
					t.Fatal(err)
				}
			}
			caller := mustMutationResultCaller(t, fixture)
			database := fixture.app.database

			vanished := onceAfterEnumeration(ctx, func(ctx context.Context) error {
				_, err := database.ExecContext(ctx, `DELETE FROM `+posts+` WHERE "id" = $1`, mutationResultUUIDText(10))
				return err
			})
			_, err := CallerUpdate(vanished, caller, fixture.postDescriptor, fixture.target(11), fixture.updateTitle("after"))
			if err != nil {
				t.Fatalf("a fault on another row changed this update: %v", err)
			}
			vanishedTarget := onceAfterEnumeration(ctx, func(ctx context.Context) error {
				_, err := database.ExecContext(ctx, `DELETE FROM `+posts+` WHERE "id" = $1`, mutationResultUUIDText(12))
				return err
			})
			_, err = CallerUpdate(vanishedTarget, caller, fixture.postDescriptor, fixture.target(12), fixture.updateTitle("after"))
			assertPublicConflict(t, err)

			changed := onceAfterEnumeration(ctx, func(ctx context.Context) error {
				_, err := database.ExecContext(ctx, `UPDATE `+posts+` SET "title" = 'moved' WHERE "id" = $1`, mutationResultUUIDText(11))
				return err
			})
			_, err = CallerUpdateMany(changed, caller, fixture.postDescriptor, fixture.title.Eq("after"), golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "bulk")))
			assertPublicConflict(t, err)
			var titles []string
			if err := database.SelectContext(ctx, &titles, `SELECT "title" FROM `+posts+` ORDER BY "id"`); err != nil {
				t.Fatal(err)
			}
			if len(titles) != 1 || titles[0] != "moved" {
				t.Fatalf("titles=%v; the conflicting writes must leave only the concurrent change", titles)
			}
		})
	}
}
