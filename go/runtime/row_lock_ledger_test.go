package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/mutation/rowlock"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func atEnumeration(ctx context.Context, call int32, fault func(context.Context) error) context.Context {
	var calls atomic.Int32
	return rowlock.WithEnumeratedFault(ctx, func(ctx context.Context) error {
		if calls.Add(1) != call {
			return nil
		}
		return fault(ctx)
	})
}

func assertPublicNotFound(t testing.TB, err error) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeNotFound {
		t.Fatalf("error=%v failure=%#v; want %s", err, failure, golem.CodeNotFound)
	}
}

func TestPostgreSQLRowsChangedBetweenEnumerationAndLockLeaveTheWriteWithTheRowsItHolds(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			fixture, namespace := newMutationResultPostgresFixture(t, ctx, profile)
			posts := `"` + string(namespace) + `"."posts"`
			for _, post := range []byte{10, 11, 12, 13, 14, 15} {
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post, golem.UUID{15: 1}, "before")); err != nil {
					t.Fatal(err)
				}
			}
			caller := mustMutationResultCaller(t, fixture)
			database := fixture.app.database
			deleting := func(post byte) context.Context {
				return atEnumeration(ctx, 1, func(ctx context.Context) error {
					_, err := database.ExecContext(ctx, `DELETE FROM `+posts+` WHERE "id" = $1`, mutationResultUUIDText(post))
					return err
				})
			}

			if _, err := CallerUpdate(deleting(10), caller, fixture.postDescriptor, fixture.target(11), fixture.updateTitle("after")); err != nil {
				t.Fatalf("a fault on another row changed this update: %v", err)
			}
			_, err := CallerUpdate(deleting(12), caller, fixture.postDescriptor, fixture.target(12), fixture.updateTitle("after"))
			assertPublicNotFound(t, err)
			_, err = CallerDelete(deleting(13), caller, fixture.postDescriptor, fixture.target(13))
			assertPublicNotFound(t, err)

			count, err := CallerUpdateMany(deleting(14), caller, fixture.postDescriptor, fixture.title.Eq("before"), golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "bulk")))
			if err != nil || count != 1 {
				t.Fatalf("updateMany whose row vanished before locking count=%d err=%v; want the 1 row it held", count, err)
			}

			changed := atEnumeration(ctx, 1, func(ctx context.Context) error {
				_, err := database.ExecContext(ctx, `UPDATE `+posts+` SET "title" = 'moved' WHERE "id" = $1`, mutationResultUUIDText(11))
				return err
			})
			count, err = CallerUpdateMany(changed, caller, fixture.postDescriptor, fixture.title.Eq("after"), golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "bulk")))
			if err != nil || count != 0 {
				t.Fatalf("updateMany whose row stopped matching before locking count=%d err=%v; want 0", count, err)
			}
			var titles []string
			if err := database.SelectContext(ctx, &titles, `SELECT "title" FROM `+posts+` ORDER BY "id"`); err != nil {
				t.Fatal(err)
			}
			if len(titles) != 2 || titles[0] != "moved" || titles[1] != "bulk" {
				t.Fatalf("titles=%v; each write must change exactly the rows that still matched while it held them", titles)
			}
		})
	}
}

func TestPostgreSQLSelectReturnsOnlyRowsItHolds(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			fixture, namespace := newMutationResultPostgresFixture(t, ctx, profile)
			posts := `"` + string(namespace) + `"."posts"`
			for _, post := range []struct {
				id    byte
				title string
			}{{20, "x"}, {21, "x"}, {22, "x"}, {23, "y"}, {24, "y"}} {
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post.id, golem.UUID{15: 1}, post.title)); err != nil {
					t.Fatal(err)
				}
			}
			database := fixture.app.database
			model, primary := policyir.ModelID(fixture.schema.Post), policyir.FieldID(fixture.schema.PostID)
			selectMatching := func(fault string, filtered bool) ([]byte, error) {
				tx, err := database.BeginTxx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				session := rowlock.Session{Ledger: rowlock.NewLedger(), Queryer: tx, Registry: fixture.app.registry, Provider: policyir.ProviderPostgreSQL, MaxParameters: 1000}
				faulted := atEnumeration(ctx, 1, func(ctx context.Context) error {
					_, err := database.ExecContext(ctx, fault)
					return err
				})
				return rowlock.Select(faulted, session, func(ctx context.Context, admit func(rowlock.Key) bool) ([]byte, []rowlock.Key, error) {
					rows, err := queryIdentityRows(ctx, tx, fixture.app.registry, policyir.ProviderPostgreSQL, model, []policyir.FieldID{primary}, `SELECT "id" FROM `+posts+` WHERE "title" = 'x' ORDER BY "id"`, nil)
					if err != nil {
						return nil, nil, err
					}
					keys, err := rowlock.RowKeys(fixture.app.registry, rows)
					if err != nil {
						return nil, nil, err
					}
					if filtered {
						if rows, keys, err = rowlock.AdmitRows(fixture.app.registry, rows, admit); err != nil {
							return nil, nil, err
						}
					}
					ids := make([]byte, len(rows))
					for index, row := range rows {
						cell, _ := row.Cell(primary)
						value, _ := cell.PolicyValue()
						id, _ := value.UUID()
						ids[index] = id[15]
					}
					return ids, keys, nil
				})
			}
			assertIDs := func(name string, got []byte, err error, want ...byte) {
				t.Helper()
				if err != nil || string(got) != string(want) {
					t.Fatalf("%s ids=%v err=%v; want %v", name, got, err, want)
				}
			}

			got, err := selectMatching(`DELETE FROM `+posts+` WHERE "id" = '`+mutationResultUUIDText(21)+`'`, false)
			assertIDs("a row deleted before locking", got, err, 20, 22)
			got, err = selectMatching(`UPDATE `+posts+` SET "title" = 'z' WHERE "id" = '`+mutationResultUUIDText(22)+`'`, false)
			assertIDs("a row that stopped matching before locking", got, err, 20)
			got, err = selectMatching(`UPDATE `+posts+` SET "title" = 'x' WHERE "id" = '`+mutationResultUUIDText(23)+`'`, true)
			assertIDs("a predicate selection excluding a row that newly matched", got, err, 20)
			_, err = selectMatching(`UPDATE `+posts+` SET "title" = 'x' WHERE "id" = '`+mutationResultUUIDText(24)+`'`, false)
			var conflict *rowlock.ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("a complete selection that saw a newly matching row it does not hold err=%v; want a row-lock conflict", err)
			}
		})
	}
}
