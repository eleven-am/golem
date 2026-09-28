package sqlite

import (
	"context"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

var sqliteFullTextFoldBenchmarkSink driver.Value

func BenchmarkSQLiteFullTextWriteOverhead(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		name := "unindexed"
		if indexed {
			name = "indexed"
		}
		b.Run(name, func(b *testing.B) {
			database := sqliteFullTextWriteBenchmarkDatabase(b, indexed)
			defer database.Close()
			body := strings.Repeat("alpha beta gamma delta ", 90)
			b.SetBytes(int64(len(body)))
			row := 0
			transaction, err := database.Beginx()
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				row++
				key := fmt.Sprintf("%016d", row)
				if _, err := transaction.Exec(`INSERT INTO documents(id,slug,subject,participants,body) VALUES(?,?,?,?,?)`, key, "slug-"+key, "subject alpha", "sender recipient", body); err != nil {
					b.Fatal(err)
				}
				if row%1_000 == 0 {
					if err := transaction.Commit(); err != nil {
						b.Fatal(err)
					}
					transaction, err = database.Beginx()
					if err != nil {
						b.Fatal(err)
					}
				}
			}
			b.StopTimer()
			if err := transaction.Commit(); err != nil {
				b.Fatal(err)
			}
		})
	}
}

func BenchmarkSQLiteFullTextFold(b *testing.B) {
	for _, test := range []struct {
		name, value string
	}{{name: "ascii", value: strings.Repeat("alpha beta gamma delta ", 90)}, {name: "unicode", value: strings.Repeat("Renée Καφές Tiếng Việt ", 90)}} {
		b.Run(test.name, func(b *testing.B) {
			arguments := []driver.Value{test.value}
			b.SetBytes(int64(len(test.value)))
			b.ReportAllocs()
			for b.Loop() {
				folded, err := sqliteFullTextFold(arguments)
				if err != nil {
					b.Fatal(err)
				}
				sqliteFullTextFoldBenchmarkSink = folded
			}
		})
	}
}

func sqliteFullTextWriteBenchmarkDatabase(tb testing.TB, indexed bool) *sqlx.DB {
	tb.Helper()
	database, _, err := New().Open(context.Background(), filepath.Join(tb.TempDir(), "writes.db"))
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE documents(id TEXT NOT NULL,slug TEXT NOT NULL,subject TEXT NOT NULL,participants TEXT NOT NULL,body TEXT NOT NULL,PRIMARY KEY(id),UNIQUE(slug)) STRICT`); err != nil {
		database.Close()
		tb.Fatal(err)
	}
	if !indexed {
		return database
	}
	owner := physical.PhysicalTable{
		ID:   "document",
		Name: "documents",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "slug", Name: "slug", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "subject", Name: "subject", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "participants", Name: "participants", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "body", Name: "body", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{Columns: []ir.FieldID{"id"}},
		Uniques:    []physical.PhysicalKey{{Columns: []ir.FieldID{"slug"}}},
	}
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{
		Name: "content", Folding: fulltextcontract.FoldingDiacritics,
		Fields: []fulltextcontract.Field{{ID: "subject", Weight: 3}, {ID: "participants", Weight: 2}, {ID: "body", Weight: 1}},
	})
	if err != nil {
		database.Close()
		tb.Fatal(err)
	}
	extension, err := fulltextstorage.Lower(ir.ProviderExtensionIR{ID: "index", Provider: ir.SQLite, Kind: fulltextcontract.IndexKind, Version: fulltextcontract.Version, Owner: "document", Payload: payload}, owner)
	if err != nil {
		database.Close()
		tb.Fatal(err)
	}
	statements, err := renderFullTextExtension(extension, owner)
	if err != nil {
		database.Close()
		tb.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			tb.Fatal(err)
		}
	}
	return database
}
