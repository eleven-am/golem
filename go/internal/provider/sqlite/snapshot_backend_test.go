package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

// This compares the real copy engines before choosing a public snapshot
// implementation. Both engines must satisfy the same managed-storage contract;
// a VACUUM failure is evidence to investigate, not an expected/pass condition.
func TestSQLiteSnapshotBackendsPreserveManagedFullTextRowIdentity(t *testing.T) {
	for _, composite := range []bool{false, true} {
		keyName := "text_primary_key"
		if composite {
			keyName = "composite_primary_key"
		}
		t.Run(keyName, func(t *testing.T) {
			for _, backend := range []string{"vacuum_into", "native_backup"} {
				t.Run(backend, func(t *testing.T) {
					ctx := context.Background()
					schema := incrementalFixtureSchema(t, false)
					schema.Tables[0].Columns[0].Storage.Kind = physical.StorageSQLiteText
					if composite {
						schema.Tables[0].Columns = append(schema.Tables[0].Columns, physical.PhysicalColumn{
							ID: fixtureItemNoteField, Name: "tenant", Ordinal: 2,
							Storage: physical.StorageType{Kind: physical.StorageSQLiteText},
							Default: physical.PhysicalDefault{Kind: physical.DefaultNone},
						})
						schema.Tables[0].PrimaryKey.Columns = append(schema.Tables[0].PrimaryKey.Columns, fixtureItemNoteField)
					}
					schema = normalizationFullTextSchema(t, schema, fulltextcontract.NormalizationNFCLower)
					source := openMigrationFixture(t, New(), schema, "snapshot-source.db")
					defer source.Close()
					columns, values := "rowid,id,name", "?,?,?"
					if composite {
						columns, values = columns+",tenant", values+",'tenant'"
					}
					insert := "INSERT INTO items (" + columns + ") VALUES (" + values + ")"
					for _, row := range []struct {
						rowid    int64
						id, text string
					}{{100, "a", "alpha"}, {700, "discarded", "discarded"}, {1300, "c", "omega"}} {
						if _, err := source.ExecContext(ctx, insert, row.rowid, row.id, row.text); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := source.ExecContext(ctx, "DELETE FROM items WHERE id='discarded'"); err != nil {
						t.Fatal(err)
					}
					assertSnapshotFullTextState(t, source, composite, map[string]string{"a": "alpha", "c": "omega"})
					var before []int64
					if err := source.SelectContext(ctx, &before, "SELECT rowid FROM items ORDER BY id"); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, []int64{100, 1300}) {
						t.Fatalf("source does not exercise sparse implicit rowids: %v", before)
					}

					destination := filepath.Join(t.TempDir(), "snapshot.db")
					connection, err := source.Connx(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if backend == "vacuum_into" {
						_, err = connection.ExecContext(ctx, "VACUUM main INTO ?", destination)
					} else {
						err = connection.Raw(func(raw any) error {
							native := raw.(sqlitedriver.Conn).Raw()
							previous := native.SetInterrupt(ctx)
							defer native.SetInterrupt(previous)
							return native.Backup("main", destination)
						})
					}
					closeErr := connection.Close()
					if err != nil || closeErr != nil {
						t.Fatalf("copy error=%v connection close=%v", err, closeErr)
					}
					copied, _, err := New().Open(ctx, destination)
					if err != nil {
						t.Fatal(err)
					}
					defer copied.Close()
					if err := New().Verify(ctx, copied, schema); err != nil {
						t.Fatalf("copied managed schema: %v", err)
					}
					var integrity string
					if err := copied.GetContext(ctx, &integrity, "PRAGMA integrity_check"); err != nil || integrity != "ok" {
						t.Fatalf("copied integrity=%q error=%v", integrity, err)
					}
					var after []int64
					if err := copied.SelectContext(ctx, &after, "SELECT rowid FROM items ORDER BY id"); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(after, before) {
						t.Errorf("copy changed managed owner rowids: source=%v copy=%v", before, after)
					}
					assertSnapshotFullTextState(t, copied, composite, map[string]string{"a": "alpha", "c": "omega"})

					if _, err := copied.ExecContext(ctx, "UPDATE items SET name='updated' WHERE id='a'"); err != nil {
						t.Fatal(err)
					}
					assertSnapshotFullTextState(t, copied, composite, map[string]string{"a": "updated", "c": "omega"})
					if _, err := copied.ExecContext(ctx, "DELETE FROM items WHERE id='a'"); err != nil {
						t.Fatal(err)
					}
					assertSnapshotFullTextState(t, copied, composite, map[string]string{"c": "omega"})
					if _, err := copied.ExecContext(ctx, insert, 100, "a", "reinserted"); err != nil {
						t.Fatal(err)
					}
					assertSnapshotFullTextState(t, copied, composite, map[string]string{"a": "reinserted", "c": "omega"})
					// Exercise the managed rowid-conflict cleanup, not just searches
					// that can continue working with a stale shadow owner rowid.
					if _, err := copied.ExecContext(ctx, "INSERT OR REPLACE INTO items ("+columns+") VALUES ("+values+")", 1300, "d", "replacement"); err != nil {
						t.Fatal(err)
					}
					assertSnapshotFullTextState(t, copied, composite, map[string]string{"a": "reinserted", "d": "replacement"})
					assertSnapshotFullTextState(t, source, composite, map[string]string{"a": "alpha", "c": "omega"})
				})
			}
		})
	}
}

func assertSnapshotFullTextState(t *testing.T, database *sqlx.DB, composite bool, expected map[string]string) {
	t.Helper()
	fts := quote(physical.PhysicalName("_golem_fulltext_" + string(normalizationExtensionID) + "_fts"))
	keys := quote(physical.PhysicalName("_golem_fulltext_" + string(normalizationExtensionID) + "_keys"))
	join := "k.id=o.id"
	if composite {
		join += " AND k.tenant=o.tenant"
	}
	var rows []struct{ ID, Name string }
	if err := database.Select(&rows, "SELECT id,name FROM items ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	actual := make(map[string]string, len(rows))
	for _, row := range rows {
		actual[row.ID] = row.Name
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("owner rows=%v want=%v", actual, expected)
	}
	var bad, keyCount, indexedCount int
	if err := database.Get(&bad, "SELECT count(*) FROM "+keys+" k LEFT JOIN items o ON "+join+" WHERE o.id IS NULL OR k._golem_owner_rowid<>o.rowid"); err != nil {
		t.Fatal(err)
	}
	if err := database.Get(&keyCount, "SELECT count(*) FROM "+keys); err != nil {
		t.Fatal(err)
	}
	if err := database.Get(&indexedCount, "SELECT count(*) FROM "+fts); err != nil {
		t.Fatal(err)
	}
	if bad != 0 || keyCount != len(expected) || indexedCount != len(expected) {
		t.Errorf("managed FTS identity: stale=%d keys=%d index=%d want=%d", bad, keyCount, indexedCount, len(expected))
	}
	for _, term := range []string{"alpha", "discarded", "omega", "updated", "reinserted", "replacement"} {
		var got []string
		if err := database.Select(&got, "SELECT k.id FROM "+fts+" JOIN "+keys+" k ON k.docid="+fts+".rowid WHERE "+fts+" MATCH ? ORDER BY k.id", term); err != nil {
			t.Fatal(err)
		}
		want := []string{}
		for key, value := range expected {
			if value == term {
				want = append(want, key)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("FTS term %q identities=%v want=%v", term, got, want)
		}
	}
}
