package postgresql

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextpostgresql "github.com/eleven-am/golem/go/internal/fulltext/postgresql"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/testenv"
)

const normalizationExtensionID = ir.ExtensionID("76000000000000000000000000000001")

func normalizationFullTextSchema(t *testing.T, namespace physical.PhysicalName, normalization string) physical.PhysicalSchema {
	t.Helper()
	schema := livePostgreSQLMigrationSchema(t, namespace, false, false)
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{Name: "content", Folding: fulltextcontract.FoldingNone, Normalization: normalization, Fields: []fulltextcontract.Field{{ID: id(952), Weight: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	extension, err := fulltextstorage.Lower(ir.ProviderExtensionIR{ID: normalizationExtensionID, Provider: ir.PostgreSQL, Version: fulltextcontract.Version, Owner: ir.ObjectID(id(950)), Kind: fulltextcontract.IndexKind, Payload: payload}, schema.Tables[0])
	if err != nil {
		t.Fatal(err)
	}
	schema.Extensions = []physical.Extension{extension}
	return normalizePostgreSQLMigrationSchema(t, schema)
}

func TestLegacyFullTextIndexUpgradesToCtypeIndependentNormalization(t *testing.T) {
	for _, profile := range []struct{ name, env string }{{"postgresql-c", testenv.PostgreSQLDSNVariable}, {"postgresql-linguistic", testenv.LinguisticDSNVariable}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			ctx := context.Background()
			provider := New()
			database, _, err := provider.Open(ctx, testenv.DisposablePostgreSQL(t, profile.env))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			var cType string
			if err := database.Get(&cType, `SELECT datctype FROM pg_catalog.pg_database WHERE datname=current_database()`); err != nil {
				t.Fatal(err)
			}
			const namespace = "golem_fulltext_upgrade"
			legacy := normalizationFullTextSchema(t, namespace, "")
			modern := normalizationFullTextSchema(t, namespace, fulltextcontract.NormalizationNFCLower)
			first, firstFiles := finalizePostgreSQLEntry(t, provider, reviewedPostgreSQLEntry(t, "001_initial", canonicalEmptyPostgreSQLMigrationSchema(t, namespace), legacy, nil))
			if err := provider.ApplyMigration(ctx, database, reviewedPostgreSQLManifest(provider, first), firstFiles); err != nil {
				t.Fatal(err)
			}
			if err := provider.Verify(ctx, database, legacy); err != nil {
				t.Fatalf("legacy index drifted: %v", err)
			}
			for _, row := range []struct {
				id   int64
				name string
			}{{1, "Καφές morning"}, {2, "café noir"}} {
				if _, err := database.Exec(`INSERT INTO "golem_fulltext_upgrade"."items" ("id","name") VALUES ($1,$2)`, row.id, row.name); err != nil {
					t.Fatal(err)
				}
			}
			table := `"golem_fulltext_upgrade"."_golem_fulltext_` + string(normalizationExtensionID) + `_fts"`
			matches := func(normalization, query string) int {
				t.Helper()
				var count int
				if err := database.Get(&count, `SELECT count(*) FROM `+table+` WHERE "document" @@ `+fulltextpostgresql.PhraseQueryFor("$1", fulltextcontract.FoldingNone, normalization, false), query); err != nil {
					t.Fatal(err)
				}
				return count
			}
			legacyLower := 1
			if cType == "C" || cType == "POSIX" {
				legacyLower = 0
			}
			if got := matches("", "Καφές"); got != 1 {
				t.Fatalf("legacy exact-case search matched %d", got)
			}
			if got := matches("", "καφές"); got != legacyLower {
				t.Fatalf("legacy lower-case search on ctype %s matched %d, want %d", cType, got, legacyLower)
			}

			plan, err := migration.Diff(legacy, modern)
			if err != nil {
				t.Fatal(err)
			}
			rewrites := 0
			for _, operation := range plan.Operations {
				switch operation.Kind {
				case migration.DropProviderExtension, migration.CreateProviderExtension:
					if operation.Risk != migration.RiskRewrite || operation.ObjectID != string(normalizationExtensionID) {
						t.Fatalf("normalization upgrade operation=%#v; want a reviewed rewrite", operation)
					}
					rewrites++
				case migration.RecordSchemaVersion:
				default:
					t.Fatalf("unexpected normalization upgrade operation=%#v", operation)
				}
			}
			if rewrites != 2 {
				t.Fatalf("normalization upgrade rewrites=%d operations=%#v", rewrites, plan.Operations)
			}
			second, secondFiles := finalizePostgreSQLEntry(t, provider, reviewedPostgreSQLEntry(t, "002_normalization", legacy, modern, &first))
			if err := provider.ApplyMigration(ctx, database, reviewedPostgreSQLManifest(provider, first, second), mergePostgreSQLMigrationFiles(firstFiles, secondFiles)); err != nil {
				t.Fatal(err)
			}
			if err := provider.Verify(ctx, database, modern); err != nil {
				t.Fatalf("upgraded index drifted: %v", err)
			}
			if err := provider.Verify(ctx, database, legacy); err == nil {
				t.Fatal("upgraded database still verifies as the legacy index")
			}
			for _, test := range []struct {
				query string
				want  int
			}{{"καφές", 1}, {"ΚΑΦΈΣ", 1}, {"Καφές", 1}, {"café", 1}, {"café", 1}, {"cafe", 0}} {
				if got := matches(fulltextcontract.NormalizationNFCLower, test.query); got != test.want {
					t.Fatalf("upgraded search %q on ctype %s matched %d, want %d", test.query, cType, got, test.want)
				}
			}
		})
	}
}
