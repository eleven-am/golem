package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
)

const normalizationExtensionID = ir.ExtensionID("76000000000000000000000000000001")

func normalizationFullTextSchema(t *testing.T, base physical.PhysicalSchema, normalization string) physical.PhysicalSchema {
	t.Helper()
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Normalization: normalization, Prefix: []uint8{}, Fields: []fulltextcontract.Field{{ID: string(fixtureItemNameField), Weight: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	extension, err := fulltextstorage.Lower(ir.ProviderExtensionIR{ID: normalizationExtensionID, Provider: ir.SQLite, Version: fulltextcontract.Version, Owner: ir.ObjectID(fixtureItemTable), Kind: fulltextcontract.IndexKind, Payload: payload}, migrationFixtureTable(t, base, fixtureItemTable))
	if err != nil {
		t.Fatal(err)
	}
	schema := normalizeMigrationFixture(t, base)
	schema.Extensions = []physical.Extension{extension}
	return normalizeMigrationFixture(t, schema)
}

func TestSQLiteLegacyFullTextIndexUpgradesToNFCNormalization(t *testing.T) {
	ctx := context.Background()
	provider := New()
	base := incrementalFixtureSchema(t, false)
	legacy := normalizationFullTextSchema(t, base, "")
	modern := normalizationFullTextSchema(t, base, fulltextcontract.NormalizationNFCLower)
	database := openMigrationFixture(t, provider, legacy, "fulltext-normalization.db")
	if err := provider.Verify(ctx, database, legacy); err != nil {
		t.Fatalf("legacy index drifted: %v", err)
	}
	var triggers string
	if err := database.GetContext(ctx, &triggers, `SELECT group_concat(sql,' ') FROM sqlite_master WHERE type='trigger'`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(triggers, fullTextNFCFunction) || strings.Contains(triggers, fullTextFoldFunction) {
		t.Fatalf("legacy FoldNone index requires a Golem function:\n%s", triggers)
	}
	table := migrationFixtureTable(t, legacy, fixtureItemTable)
	for position, name := range []string{"Καφές morning", "café noir"} {
		if _, err := database.ExecContext(ctx, "INSERT INTO "+quote(table.Name)+" (\"id\",\"name\") VALUES (?,?)", position+1, name); err != nil {
			t.Fatal(err)
		}
	}
	fts := quote(physical.PhysicalName("_golem_fulltext_" + string(normalizationExtensionID) + "_fts"))
	matches := func(query string) int {
		t.Helper()
		var count int
		if err := database.GetContext(ctx, &count, `SELECT count(*) FROM `+fts+` WHERE `+fts+` MATCH ?`, `"`+query+`"`); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := matches("καφές"); got != 1 {
		t.Fatalf("legacy case-insensitive search matched %d", got)
	}
	if got := matches("café"); got != 0 {
		t.Fatalf("legacy NFC query matched NFD text %d times before the upgrade", got)
	}

	plan, err := migration.Diff(legacy, modern)
	if err != nil {
		t.Fatal(err)
	}
	rewrites := 0
	for _, operation := range plan.Operations {
		switch operation.Kind {
		case migration.DropProviderExtension, migration.CreateProviderExtension:
			if operation.Risk != migration.RiskRewrite {
				t.Fatalf("normalization upgrade operation=%#v; want a reviewed rewrite", operation)
			}
			rewrites++
		case migration.RecordSchemaVersion:
		default:
			t.Fatalf("unexpected normalization upgrade operation=%#v", operation)
		}
	}
	if rewrites != 2 {
		t.Fatalf("normalization upgrade rewrites=%d", rewrites)
	}
	manifest, files := migrationFixtureManifest(t, legacy, modern, "002_fulltext_normalization.sql", nil)
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, modern); err != nil {
		t.Fatalf("upgraded index drifted: %v", err)
	}
	if err := database.GetContext(ctx, &triggers, `SELECT group_concat(sql,' ') FROM sqlite_master WHERE type='trigger'`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(triggers, fullTextNFCFunction) {
		t.Fatalf("upgraded FoldNone index does not normalize writes:\n%s", triggers)
	}
	for _, test := range []struct {
		query string
		want  int
	}{{"καφές", 1}, {"café", 1}, {"cafe", 0}} {
		if got := matches(test.query); got != test.want {
			t.Fatalf("upgraded search %q matched %d, want %d", test.query, got, test.want)
		}
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO "+quote(table.Name)+" (\"id\",\"name\") VALUES (?,?)", 3, "résumé"); err != nil {
		t.Fatal(err)
	}
	if got := matches("résumé"); got != 1 {
		t.Fatalf("post-upgrade NFD write matched %d", got)
	}
}
