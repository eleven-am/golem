package postgresql

import (
	"context"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

func TestSemanticExactVectorVersionKeepsPostgreSQLStorageShapeAndInvalidatesRows(t *testing.T) {
	before := semanticUpgradeExtension(t, semanticstorage.StateVersionStrikes)
	after := semanticUpgradeExtension(t, semanticstorage.StateVersionExactVectors)
	upgrade, err := renderSemanticStateUpgrade("semantic", before, after)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`UPDATE "semantic"."_golem_semantic_semanticid_state" SET "status"='pending', "attempt_count"=0, "error_code"=NULL, "ambiguous_strikes"=0`}
	if !reflect.DeepEqual(upgrade, want) {
		t.Fatalf("PostgreSQL exact-vector upgrade=%v want=%v", upgrade, want)
	}
	previous, err := renderPostgreSQLSemanticExtension("semantic", before, false)
	if err != nil {
		t.Fatal(err)
	}
	current, err := renderPostgreSQLSemanticExtension("semantic", after, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(previous, current) {
		t.Fatalf("PostgreSQL storage changed across a metadata-only semantic version\nprevious=%v\ncurrent=%v", previous, current)
	}
}

const semanticUpgradeNamespace = "semantic_state_upgrade"

func semanticUpgradeExtension(t *testing.T, stateVersion uint16) physical.Extension {
	t.Helper()
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 4, Fields: []string{"body"}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	owner := physical.PhysicalTable{
		ID: "model-id", Name: "notes",
		Columns: []physical.PhysicalColumn{
			{ID: "tenant", Name: "tenant", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}},
			{ID: "serial", Name: "serial", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "model-primary", Name: "pk_notes", Columns: []ir.FieldID{"tenant", "serial"}},
	}
	extension, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: "semanticid", Provider: ir.PostgreSQL, Kind: semanticcontract.IndexKind, Version: semanticcontract.Version, Owner: "model-id", Payload: payload}, owner)
	if err != nil {
		t.Fatal(err)
	}
	attributes := make([]physical.Attribute, 0, len(extension.Attributes))
	for _, attribute := range extension.Attributes {
		if attribute.Name == "state_version" {
			if stateVersion == semanticstorage.StateVersionIdentity {
				continue
			}
			attribute.Value = physical.SemanticValue{Kind: physical.ValueInteger, Integer: int64(stateVersion)}
		}
		attributes = append(attributes, attribute)
	}
	extension.Attributes = attributes
	return extension
}

func semanticUpgradeSchema(namespace string, extension physical.Extension) physical.PhysicalSchema {
	return physical.PhysicalSchema{
		Version:          physical.SchemaFormatVersion,
		CanonicalVersion: physical.CanonicalFormatVersion,
		Namespace:        physical.Namespace{Name: physical.PhysicalName(namespace)},
		Extensions:       []physical.Extension{extension},
	}
}

func openSemanticUpgradeFixture(t *testing.T, namespace string) *sqlx.DB {
	t.Helper()
	dsn := testenv.DisposablePGVector(t)
	database, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+namespace+`" CASCADE`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `CREATE SCHEMA "`+namespace+`"`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+namespace+`" CASCADE`)
		database.Close()
	})
	return database
}

func TestSemanticStateUpgradeIsAcceptedByIntrospectionOnPostgreSQL(t *testing.T) {
	database := openSemanticUpgradeFixture(t, semanticUpgradeNamespace)
	ctx := context.Background()
	before := semanticUpgradeExtension(t, semanticstorage.StateVersionIdentity)
	after := semanticUpgradeExtension(t, semanticstorage.StateVersionCurrent)

	statements, err := renderPostgreSQLSemanticExtension(physical.PhysicalName(semanticUpgradeNamespace), before, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create original semantic storage %q: %v", statement, err)
		}
	}

	if err := introspectSemanticExtensions(ctx, database, semanticUpgradeSchema(semanticUpgradeNamespace, before), false); err != nil {
		t.Fatalf("original shadow state was refused at its own version: %v", err)
	}
	if err := introspectSemanticExtensions(ctx, database, semanticUpgradeSchema(semanticUpgradeNamespace, after), false); err == nil {
		t.Fatal("the current contract accepted storage that has not been upgraded")
	}

	upgrade, err := renderSemanticStateUpgrade(physical.PhysicalName(semanticUpgradeNamespace), before, after)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range upgrade {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("apply upgrade %q: %v", statement, err)
		}
	}

	if err := introspectSemanticExtensions(ctx, database, semanticUpgradeSchema(semanticUpgradeNamespace, after), false); err != nil {
		t.Fatalf("upgraded shadow state was refused: %v", err)
	}
	if err := introspectSemanticExtensions(ctx, database, semanticUpgradeSchema(semanticUpgradeNamespace, before), false); err == nil {
		t.Fatal("the original contract accepted a table that now carries the strike column")
	}
}

func TestSemanticStateUpgradeConvergesWithAFreshlyCreatedTableOnPostgreSQL(t *testing.T) {
	const fresh = semanticUpgradeNamespace + "_fresh"
	upgraded := openSemanticUpgradeFixture(t, semanticUpgradeNamespace+"_alter")
	created := openSemanticUpgradeFixture(t, fresh)
	ctx := context.Background()
	before := semanticUpgradeExtension(t, semanticstorage.StateVersionIdentity)
	after := semanticUpgradeExtension(t, semanticstorage.StateVersionCurrent)

	original, err := renderPostgreSQLSemanticExtension(physical.PhysicalName(semanticUpgradeNamespace+"_alter"), before, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range original {
		if _, err := upgraded.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	upgrade, err := renderSemanticStateUpgrade(physical.PhysicalName(semanticUpgradeNamespace+"_alter"), before, after)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range upgrade {
		if _, err := upgraded.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	direct, err := renderPostgreSQLSemanticExtension(physical.PhysicalName(fresh), after, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range direct {
		if _, err := created.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	const columnsSQL = `SELECT COALESCE(string_agg(a.attname||':'||pg_catalog.format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull::text,',' ORDER BY a.attnum),'') FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped`
	var alteredColumns, createdColumns string
	if err := upgraded.QueryRowxContext(ctx, columnsSQL, semanticUpgradeNamespace+"_alter", "_golem_semantic_semanticid_state").Scan(&alteredColumns); err != nil {
		t.Fatal(err)
	}
	if err := created.QueryRowxContext(ctx, columnsSQL, fresh, "_golem_semantic_semanticid_state").Scan(&createdColumns); err != nil {
		t.Fatal(err)
	}
	if alteredColumns != createdColumns {
		t.Fatalf("catalog columns differ between upgrade and creation\naltered=%s\ncreated=%s", alteredColumns, createdColumns)
	}
}
