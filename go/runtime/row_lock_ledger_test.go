package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/mutation/rowlock"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
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
			ids := func(rows []mutationdecode.Row) []byte {
				result := make([]byte, len(rows))
				for index, row := range rows {
					cell, _ := row.Cell(primary)
					value, _ := cell.PolicyValue()
					id, _ := value.UUID()
					result[index] = id[15]
				}
				return result
			}
			selectRows := func(fault string, complete bool) ([]byte, error) {
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
				enumerate := func(ctx context.Context) ([]mutationdecode.Row, []rowlock.Key, error) {
					rows, err := queryIdentityRows(ctx, tx, fixture.app.registry, policyir.ProviderPostgreSQL, model, []policyir.FieldID{primary}, `SELECT "golem_t"."id" FROM `+posts+` AS "golem_t" WHERE "golem_t"."title" = 'x' ORDER BY "golem_t"."id"`, nil)
					if err != nil {
						return nil, nil, err
					}
					keys, err := rowlock.RowKeys(fixture.app.registry, rows)
					return rows, keys, err
				}
				if complete {
					rows, err := rowlock.Select(faulted, session, enumerate)
					return ids(rows), err
				}
				decoder, err := readdecode.NewFields(model, fixture.app.registry, policyir.ProviderPostgreSQL, []policyir.FieldID{primary})
				if err != nil {
					t.Fatal(err)
				}
				reader := rowlock.Reader{Alias: "golem_t", Columns: `"golem_t"."id"`, Match: `"golem_t"."title" = 'x'`}
				locked, err := rowlock.SelectMatching(faulted, session, []rowlock.Reader{reader}, func(ctx context.Context, _ int) ([]mutationdecode.Row, []rowlock.Key, error) {
					return enumerate(ctx)
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
					return nil, err
				}
				return ids(locked[0]), nil
			}
			assertIDs := func(name string, got []byte, err error, want ...byte) {
				t.Helper()
				if err != nil || string(got) != string(want) {
					t.Fatalf("%s ids=%v err=%v; want %v", name, got, err, want)
				}
			}
			setTitle := func(id byte, title string) string {
				return `UPDATE ` + posts + ` SET "title" = '` + title + `' WHERE "id" = '` + mutationResultUUIDText(id) + `'`
			}

			got, err := selectRows(`DELETE FROM `+posts+` WHERE "id" = '`+mutationResultUUIDText(21)+`'`, true)
			assertIDs("a complete selection whose row was deleted before locking", got, err, 20, 22)
			got, err = selectRows(setTitle(22, "z"), true)
			assertIDs("a complete selection whose row stopped matching before locking", got, err, 20)
			got, err = selectRows(setTitle(23, "x"), false)
			assertIDs("a predicate selection excluding a row that newly matched", got, err, 20)
			got, err = selectRows(`DELETE FROM `+posts+` WHERE "id" = '`+mutationResultUUIDText(23)+`'`, false)
			assertIDs("a predicate selection whose row was deleted before locking", got, err, 20)
			_, err = selectRows(setTitle(24, "x"), true)
			var conflict *rowlock.ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("a complete selection that saw a newly matching row it does not hold err=%v; want a row-lock conflict", err)
			}
			got, err = selectRows(setTitle(20, "z"), false)
			assertIDs("a predicate selection whose row stopped matching before locking", got, err, 24)
		})
	}
}

func TestPostgreSQLSelectMatchingReturnsTheImageItLocked(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			fixture, namespace := newMutationResultPostgresFixture(t, ctx, profile)
			posts := `"` + string(namespace) + `"."posts"`
			for _, post := range []byte{40, 41} {
				if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post, golem.UUID{15: 1}, "enumerated")); err != nil {
					t.Fatal(err)
				}
			}
			database := fixture.app.database
			model := policyir.ModelID(fixture.schema.Post)
			fields := []policyir.FieldID{policyir.FieldID(fixture.schema.PostID), policyir.FieldID(fixture.schema.PostTitle)}
			decoder, err := readdecode.NewFields(model, fixture.app.registry, policyir.ProviderPostgreSQL, fields)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := database.BeginTxx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			session := rowlock.Session{Ledger: rowlock.NewLedger(), Queryer: tx, Registry: fixture.app.registry, Provider: policyir.ProviderPostgreSQL, MaxParameters: 1000}
			changed := atEnumeration(ctx, 1, func(ctx context.Context) error {
				_, err := database.ExecContext(ctx, `UPDATE `+posts+` SET "title" = 'committed before the lock' WHERE "id" = $1`, mutationResultUUIDText(41))
				return err
			})
			columns := `"golem_t"."id", "golem_t"."title"`
			match := `"golem_t"."author_id" = '` + mutationResultUUIDText(1) + `'`
			locked, err := rowlock.SelectMatching(changed, session, []rowlock.Reader{{Alias: "golem_t", Columns: columns, Match: match}}, func(ctx context.Context, _ int) ([]mutationdecode.Row, []rowlock.Key, error) {
				rows, err := queryIdentityRows(ctx, tx, fixture.app.registry, policyir.ProviderPostgreSQL, model, fields, `SELECT `+columns+` FROM `+posts+` AS "golem_t" WHERE `+match+` ORDER BY "golem_t"."id"`, nil)
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
				t.Fatal(err)
			}
			var titles []string
			for _, row := range locked[0] {
				cell, _ := row.Cell(policyir.FieldID(fixture.schema.PostTitle))
				value, _ := cell.PolicyValue()
				title, _ := value.Text()
				titles = append(titles, title)
			}
			if len(titles) != 2 || titles[0] != "enumerated" || titles[1] != "committed before the lock" {
				t.Fatalf("titles=%v; each row must be returned as this transaction locked it, in enumeration order", titles)
			}
		})
	}
}

func TestPostgreSQLReferencedParentFoundOnlyAfterLockingIsAConflict(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			fixture := newPostgresSocialMutationFixtureFrom(t, profile, schematest.NewSubscribedSocialMutationPostTagForeignKeysPostgreSQLNamespaces, golem.ModelID{}, nil)
			system := fixture.app.System()
			if _, err := SystemCreate(ctx, system, fixture.userDescriptor, fixture.userCreate(1, "owner")); err != nil {
				t.Fatal(err)
			}
			for _, post := range []byte{10, 11} {
				if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.postRootCreate(post, 1, "post")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := SystemCreate(ctx, system, fixture.tagDescriptor, fixture.tagCreate(30, "a")); err != nil {
				t.Fatal(err)
			}
			database := fixture.app.database
			tags := nestedAcceptanceTable(fixture.app, fixture.schema.Tag)
			posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
			postTags := nestedAcceptanceTable(fixture.app, fixture.schema.PostTag)
			link := func(post byte, tag string) golem.CreateInput[socialMutationPostTag] {
				return golem.GeneratedCreateInput[socialMutationPostTag](fixture.schema.PostTag,
					golem.GeneratedCreateFieldValue(fixture.schema.PostTag, fixture.postTagPostID, golem.UUID{15: post}),
					golem.GeneratedCreateFieldValue(fixture.schema.PostTag, fixture.postTagTagName, tag))
			}

			taken := atEnumeration(ctx, 1, func(ctx context.Context) error {
				if _, err := database.ExecContext(ctx, `UPDATE `+tags+` SET "name" = 'z' WHERE "name" = 'a'`); err != nil {
					return err
				}
				_, err := database.ExecContext(ctx, `INSERT INTO `+tags+` ("id", "name") VALUES ($1, 'a')`, mutationResultUUIDText(39))
				return err
			})
			_, err := SystemCreate(taken, system, fixture.postTagDescriptor, link(10, "a"))
			var failure *golem.Error
			if !errors.As(err, &failure) || failure.Code != golem.CodeConflict {
				t.Fatalf("a create whose unique-key parent was taken by another row between enumeration and locking err=%v; want %s, since that parent is not locked", err, golem.CodeConflict)
			}

			vanished := atEnumeration(ctx, 1, func(ctx context.Context) error {
				_, err := database.ExecContext(ctx, `DELETE FROM `+posts+` WHERE "id" = $1`, mutationResultUUIDText(11))
				return err
			})
			_, err = SystemCreate(vanished, system, fixture.postTagDescriptor, link(11, "a"))
			if !errors.As(err, &failure) || failure.Code == golem.CodeConflict {
				t.Fatalf("a create whose requested parent vanished before locking err=%v; want the foreign key's own failure, not %s", err, golem.CodeConflict)
			}
			var links int
			if err := database.GetContext(ctx, &links, `SELECT COUNT(*) FROM `+postTags); err != nil || links != 0 {
				t.Fatalf("post tags=%d err=%v; neither create may persist a link", links, err)
			}
		})
	}
}
