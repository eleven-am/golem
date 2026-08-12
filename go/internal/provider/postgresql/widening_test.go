package postgresql

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

const (
	wideningModel       = 960
	wideningID          = 961
	wideningViews       = 962
	wideningRatio       = 963
	wideningAmount      = 964
	wideningTick        = 965
	wideningSeen        = 966
	wideningRank        = 967
	wideningLabel       = 968
	wideningPrimary     = 969
	wideningUnique      = 970
	wideningIndex       = 971
	wideningLinkModel   = 972
	wideningLinkID      = 973
	wideningLinkViews   = 974
	wideningLinkKey     = 975
	wideningLinkForeig  = 976
	wideningDescription = 977
)

func postgresqlWideningStorage(widened bool) map[ir.FieldID]physical.StorageType {
	narrow := map[ir.FieldID]physical.StorageType{
		ir.FieldID(id(wideningViews)):       {Kind: physical.StoragePostgreSQLInteger},
		ir.FieldID(id(wideningRatio)):       {Kind: physical.StoragePostgreSQLReal},
		ir.FieldID(id(wideningAmount)):      {Kind: physical.StoragePostgreSQLNumeric, Precision: 10, Scale: 2},
		ir.FieldID(id(wideningTick)):        {Kind: physical.StoragePostgreSQLTime, Length: 0},
		ir.FieldID(id(wideningSeen)):        {Kind: physical.StoragePostgreSQLTimestampTZ, Length: 0},
		ir.FieldID(id(wideningRank)):        {Kind: physical.StoragePostgreSQLSmallInt},
		ir.FieldID(id(wideningLabel)):       {Kind: physical.StoragePostgreSQLVarchar, Length: 16},
		ir.FieldID(id(wideningDescription)): {Kind: physical.StoragePostgreSQLVarchar, Length: 16},
		ir.FieldID(id(wideningLinkViews)):   {Kind: physical.StoragePostgreSQLInteger},
	}
	if !widened {
		return narrow
	}
	return map[ir.FieldID]physical.StorageType{
		ir.FieldID(id(wideningViews)):       {Kind: physical.StoragePostgreSQLBigInt},
		ir.FieldID(id(wideningRatio)):       {Kind: physical.StoragePostgreSQLDouble},
		ir.FieldID(id(wideningAmount)):      {Kind: physical.StoragePostgreSQLNumeric, Precision: 14, Scale: 2},
		ir.FieldID(id(wideningTick)):        {Kind: physical.StoragePostgreSQLTime, Length: 6},
		ir.FieldID(id(wideningSeen)):        {Kind: physical.StoragePostgreSQLTimestampTZ, Length: 6},
		ir.FieldID(id(wideningRank)):        {Kind: physical.StoragePostgreSQLInteger},
		ir.FieldID(id(wideningLabel)):       {Kind: physical.StoragePostgreSQLVarchar, Length: 32},
		ir.FieldID(id(wideningDescription)): {Kind: physical.StoragePostgreSQLText},
		ir.FieldID(id(wideningLinkViews)):   {Kind: physical.StoragePostgreSQLBigInt},
	}
}

func livePostgreSQLWideningSchema(t *testing.T, namespace physical.PhysicalName, widened, link bool) physical.PhysicalSchema {
	t.Helper()
	storage := postgresqlWideningStorage(widened)
	bigint := physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
	none := physical.PhysicalDefault{Kind: physical.DefaultNone}
	viewsField := ir.FieldID(id(wideningViews))
	metrics := physical.PhysicalTable{
		ID: ir.ModelID(id(wideningModel)), Name: "metrics",
		Columns: []physical.PhysicalColumn{
			{ID: ir.FieldID(id(wideningID)), Name: "id", Ordinal: 0, Storage: bigint, Default: none},
			{ID: viewsField, Name: "views", Ordinal: 1, Storage: storage[viewsField], Default: none},
			{ID: ir.FieldID(id(wideningRatio)), Name: "ratio", Ordinal: 2, Storage: storage[ir.FieldID(id(wideningRatio))], Nullable: true, Default: none},
			{ID: ir.FieldID(id(wideningAmount)), Name: "amount", Ordinal: 3, Storage: storage[ir.FieldID(id(wideningAmount))], Nullable: true, Default: none},
			{ID: ir.FieldID(id(wideningTick)), Name: "tick", Ordinal: 4, Storage: storage[ir.FieldID(id(wideningTick))], Nullable: true, Default: none},
			{ID: ir.FieldID(id(wideningSeen)), Name: "seen", Ordinal: 5, Storage: storage[ir.FieldID(id(wideningSeen))], Nullable: true, Default: none},
			{ID: ir.FieldID(id(wideningRank)), Name: "position", Ordinal: 6, Storage: storage[ir.FieldID(id(wideningRank))], Nullable: true, Default: none},
			{ID: ir.FieldID(id(wideningLabel)), Name: "label", Ordinal: 7, Storage: storage[ir.FieldID(id(wideningLabel))], Nullable: true, Default: none},
			{ID: ir.FieldID(id(wideningDescription)), Name: "description", Ordinal: 8, Storage: storage[ir.FieldID(id(wideningDescription))], Nullable: true, Default: none},
		},
		PrimaryKey: &physical.PhysicalKey{ID: ir.KeyID(id(wideningPrimary)), Name: "pk_metrics", Columns: []ir.FieldID{ir.FieldID(id(wideningID))}},
		Uniques:    []physical.PhysicalKey{{ID: ir.KeyID(id(wideningUnique)), Name: "uq_metrics_views", Columns: []ir.FieldID{viewsField}}},
		Indexes:    []physical.PhysicalIndex{{ID: ir.IndexID(id(wideningIndex)), Name: "idx_metrics_views", Method: physical.IndexBTree, CreationMode: physical.IndexTransactional, Keys: []physical.IndexKey{{Column: &viewsField, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}}},
	}
	linkViews := ir.FieldID(id(wideningLinkViews))
	links := physical.PhysicalTable{
		ID: ir.ModelID(id(wideningLinkModel)), Name: "metric_links",
		Columns: []physical.PhysicalColumn{
			{ID: ir.FieldID(id(wideningLinkID)), Name: "id", Ordinal: 0, Storage: bigint, Default: none},
			{ID: linkViews, Name: "views_ref", Ordinal: 1, Storage: storage[linkViews], Nullable: true, Default: none},
		},
		PrimaryKey: &physical.PhysicalKey{ID: ir.KeyID(id(wideningLinkKey)), Name: "pk_metric_links", Columns: []ir.FieldID{ir.FieldID(id(wideningLinkID))}},
	}
	if link {
		links.ForeignKeys = []physical.PhysicalForeignKey{{
			ID: ir.ForeignKeyID(id(wideningLinkForeig)), Name: "fk_metric_links_views",
			Columns: []ir.FieldID{linkViews}, ReferencedTable: metrics.ID, ReferencedColumns: []ir.FieldID{viewsField},
			OnUpdate: ir.ActionNoAction, OnDelete: ir.ActionNoAction, Deferrable: ir.NotDeferrable,
		}}
	}
	return normalizePostgreSQLMigrationSchema(t, physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: New().Manifest(), Namespace: physical.Namespace{Name: namespace}, System: systemSchema(),
		Tables: []physical.PhysicalTable{metrics, links},
	})
}

func TestPostgreSQLSafeTypeWideningAllowlistAndRiskClassification(t *testing.T) {
	storage := func(kind physical.StorageKind, precision, scale uint16, length uint32) physical.StorageType {
		return physical.StorageType{Kind: kind, Precision: precision, Scale: scale, Length: length}
	}
	smallint := storage(physical.StoragePostgreSQLSmallInt, 0, 0, 0)
	integer := storage(physical.StoragePostgreSQLInteger, 0, 0, 0)
	bigint := storage(physical.StoragePostgreSQLBigInt, 0, 0, 0)
	real := storage(physical.StoragePostgreSQLReal, 0, 0, 0)
	double := storage(physical.StoragePostgreSQLDouble, 0, 0, 0)
	text := storage(physical.StoragePostgreSQLText, 0, 0, 0)
	varchar := func(length uint32) physical.StorageType {
		return storage(physical.StoragePostgreSQLVarchar, 0, 0, length)
	}
	uuid := storage(physical.StoragePostgreSQLUUID, 0, 0, 0)
	date := storage(physical.StoragePostgreSQLDate, 0, 0, 0)
	jsonb := storage(physical.StoragePostgreSQLJSONB, 0, 0, 0)
	bytea := storage(physical.StoragePostgreSQLBytea, 0, 0, 0)
	boolean := storage(physical.StoragePostgreSQLBoolean, 0, 0, 0)
	numeric := func(precision, scale uint16) physical.StorageType {
		return storage(physical.StoragePostgreSQLNumeric, precision, scale, 0)
	}
	clock := func(length uint32) physical.StorageType { return storage(physical.StoragePostgreSQLTime, 0, 0, length) }
	stamp := func(length uint32) physical.StorageType {
		return storage(physical.StoragePostgreSQLTimestampTZ, 0, 0, length)
	}

	accepted := []struct {
		name          string
		before, after physical.StorageType
	}{
		{"smallint to integer", smallint, integer},
		{"smallint to bigint", smallint, bigint},
		{"integer to bigint", integer, bigint},
		{"real to double precision", real, double},
		{"varchar length growth", varchar(16), varchar(32)},
		{"varchar to text", varchar(16), text},
		{"numeric precision growth", numeric(10, 2), numeric(14, 2)},
		{"numeric precision growth to maximum", numeric(1, 0), numeric(1000, 0)},
		{"time precision growth", clock(0), clock(6)},
		{"time precision single step", clock(2), clock(3)},
		{"timestamptz precision growth", stamp(0), stamp(6)},
	}
	for _, item := range accepted {
		t.Run("accept/"+item.name, func(t *testing.T) {
			if !migration.SafeWidening(ir.PostgreSQL, item.before, item.after) {
				t.Fatalf("value-preserving widening %#v -> %#v was refused", item.before, item.after)
			}
		})
	}

	refused := []struct {
		name          string
		before, after physical.StorageType
	}{
		{"bigint to integer", bigint, integer},
		{"bigint to smallint", bigint, smallint},
		{"integer to smallint", integer, smallint},
		{"double to real", double, real},
		{"integer to real", integer, real},
		{"integer to double", integer, double},
		{"integer to numeric", integer, numeric(18, 0)},
		{"integer to text", integer, text},
		{"integer to uuid", integer, uuid},
		{"real to numeric", real, numeric(18, 6)},
		{"real to text", real, text},
		{"text to uuid", text, uuid},
		{"varchar narrowing", varchar(32), varchar(16)},
		{"text to varchar", text, varchar(32)},
		{"text to date", text, date},
		{"text to jsonb", text, jsonb},
		{"text to bytea", text, bytea},
		{"uuid to text", uuid, text},
		{"jsonb to text", jsonb, text},
		{"date to timestamptz", date, stamp(6)},
		{"timestamptz to date", stamp(6), date},
		{"time to timestamptz", clock(6), stamp(6)},
		{"boolean to smallint", boolean, smallint},
		{"smallint to boolean", smallint, boolean},
		{"numeric precision shrink", numeric(10, 2), numeric(8, 2)},
		{"numeric scale growth", numeric(10, 2), numeric(12, 4)},
		{"numeric scale shrink", numeric(10, 4), numeric(10, 2)},
		{"numeric to double", numeric(10, 2), double},
		{"numeric to text", numeric(10, 2), text},
		{"time precision shrink", clock(6), clock(0)},
		{"timestamptz precision shrink", stamp(6), stamp(3)},
		{"timestamptz precision above microseconds", stamp(6), stamp(7)},
		{"identical integer", integer, integer},
		{"identical numeric", numeric(10, 2), numeric(10, 2)},
	}
	for _, item := range refused {
		t.Run("refuse/"+item.name, func(t *testing.T) {
			if migration.SafeWidening(ir.PostgreSQL, item.before, item.after) {
				t.Fatalf("transition %#v -> %#v was admitted as a safe widening", item.before, item.after)
			}
		})
	}

	t.Run("refuse/other providers", func(t *testing.T) {
		for _, provider := range []ir.Provider{ir.SQLite, ir.Provider("mysql"), ir.Provider("")} {
			if migration.SafeWidening(provider, integer, bigint) {
				t.Fatalf("provider %s admitted a PostgreSQL widening", provider)
			}
		}
	})

	t.Run("refuse/extension symbol", func(t *testing.T) {
		symbol := &physical.SemanticSymbol{Identity: "golem.postgresql.cast.v1", Kind: ir.SchemaSymbolCast, Version: 1, Provider: ir.ProviderScopePostgreSQL}
		tainted := bigint
		tainted.Symbol = symbol
		if migration.SafeWidening(ir.PostgreSQL, integer, tainted) || migration.SafeWidening(ir.PostgreSQL, tainted, bigint) {
			t.Fatal("extension-symbol storage was admitted as a safe widening")
		}
	})

	t.Run("risk classification and mandatory approval", func(t *testing.T) {
		before := livePostgreSQLWideningSchema(t, "reviewed", false, true)
		after := livePostgreSQLWideningSchema(t, "reviewed", true, true)
		plan, err := migration.Diff(before, after)
		if err != nil {
			t.Fatal(err)
		}
		widenings := 0
		for _, operation := range plan.Operations {
			if operation.Kind != migration.AlterColumnType {
				continue
			}
			widenings++
			if operation.Risk != migration.RiskRewrite {
				t.Fatalf("widening %s risk=%s want=%s", operation.ObjectID, operation.Risk, migration.RiskRewrite)
			}
			if !migration.RequiresApproval(operation) {
				t.Fatalf("widening %s does not require an approval", operation.ObjectID)
			}
			if operation.Before == "" || operation.After == "" || operation.Before == operation.After {
				t.Fatalf("widening %s lacks distinct typed before/after metadata", operation.ObjectID)
			}
		}
		if widenings != 9 {
			t.Fatalf("widening operations=%d want=9", widenings)
		}
		var approvals []migration.Approval
		for _, operation := range plan.Operations {
			if operation.Kind != migration.AlterColumnType && operation.Risk != migration.RiskDataLoss && operation.Risk != migration.RiskManual {
				continue
			}
			approvals = append(approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
		if err := migration.ValidatePlan(plan, approvals); err != nil {
			t.Fatal(err)
		}
		for index := range approvals {
			short := approvals[:index]
			short = append(short, approvals[index+1:]...)
			if migration.ValidatePlan(plan, short) == nil {
				t.Fatalf("plan validated without the approval for %s", approvals[index].OperationID)
			}
		}
		for index := range approvals {
			tampered := append([]migration.Approval(nil), approvals...)
			tampered[index].After = tampered[index].Before
			if migration.ValidatePlan(plan, tampered) == nil {
				t.Fatalf("plan validated with a non-exact approval for %s", approvals[index].OperationID)
			}
		}
	})

	t.Run("narrowing keeps data-loss risk", func(t *testing.T) {
		before := livePostgreSQLWideningSchema(t, "reviewed", true, true)
		after := livePostgreSQLWideningSchema(t, "reviewed", false, true)
		plan, err := migration.Diff(before, after)
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range plan.Operations {
			if operation.Kind == migration.AlterColumnType && operation.Risk != migration.RiskDataLoss {
				t.Fatalf("narrowing %s risk=%s want=%s", operation.ObjectID, operation.Risk, migration.RiskDataLoss)
			}
		}
	})
}

func TestPostgreSQLTypeNarrowingAndUnregisteredCastFailBeforeDatabaseWork(t *testing.T) {
	provider := New()
	widened := livePostgreSQLWideningSchema(t, "reviewed", true, true)
	narrow := livePostgreSQLWideningSchema(t, "reviewed", false, true)

	t.Run("narrowing", func(t *testing.T) {
		entry := reviewedPostgreSQLEntry(t, "002_narrow", widened, narrow, nil)
		if _, err := provider.PlanIncremental(entry); err == nil || !strings.Contains(err.Error(), "alterColumnType") {
			t.Fatalf("error=%v; want a typed narrowing refusal", err)
		}
	})

	unregistered := []struct {
		name    string
		storage physical.StorageType
	}{
		{"integer to text", physical.StorageType{Kind: physical.StoragePostgreSQLText}},
		{"integer to double", physical.StorageType{Kind: physical.StoragePostgreSQLDouble}},
		{"integer to uuid", physical.StorageType{Kind: physical.StoragePostgreSQLUUID}},
		{"integer to numeric", physical.StorageType{Kind: physical.StoragePostgreSQLNumeric, Precision: 18, Scale: 0}},
		{"integer to jsonb", physical.StorageType{Kind: physical.StoragePostgreSQLJSONB}},
	}
	for _, item := range unregistered {
		t.Run(item.name, func(t *testing.T) {
			after, err := physical.Normalize(narrow)
			if err != nil {
				t.Fatal(err)
			}
			metrics := postgresqlTablePointer(&after, ir.ModelID(id(wideningModel)))
			links := postgresqlTablePointer(&after, ir.ModelID(id(wideningLinkModel)))
			for index := range metrics.Columns {
				if metrics.Columns[index].ID == ir.FieldID(id(wideningViews)) {
					metrics.Columns[index].Storage = item.storage
				}
			}
			for index := range links.Columns {
				if links.Columns[index].ID == ir.FieldID(id(wideningLinkViews)) {
					links.Columns[index].Storage = item.storage
				}
			}
			after = normalizePostgreSQLMigrationSchema(t, after)
			entry := reviewedPostgreSQLEntry(t, "002_unregistered", narrow, after, nil)
			if _, err := provider.PlanIncremental(entry); err == nil || !strings.Contains(err.Error(), "alterColumnType") {
				t.Fatalf("error=%v; want an unregistered cast refusal", err)
			}
		})
	}

	t.Run("numeric scale change", func(t *testing.T) {
		after, err := physical.Normalize(narrow)
		if err != nil {
			t.Fatal(err)
		}
		metrics := postgresqlTablePointer(&after, ir.ModelID(id(wideningModel)))
		for index := range metrics.Columns {
			if metrics.Columns[index].ID == ir.FieldID(id(wideningAmount)) {
				metrics.Columns[index].Storage = physical.StorageType{Kind: physical.StoragePostgreSQLNumeric, Precision: 14, Scale: 4}
			}
		}
		after = normalizePostgreSQLMigrationSchema(t, after)
		entry := reviewedPostgreSQLEntry(t, "002_scale", narrow, after, nil)
		if _, err := provider.PlanIncremental(entry); err == nil || !strings.Contains(err.Error(), "alterColumnType") {
			t.Fatalf("error=%v; want a numeric scale refusal", err)
		}
	})

	t.Run("live refusal touches no database state", func(t *testing.T) {
		forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
			namespace := physical.PhysicalName("golem_widening_refuse_" + profile)
			dropPostgreSQLWideningNamespace(t, database, namespace)
			defer dropPostgreSQLWideningNamespace(t, database, namespace)
			empty := canonicalEmptyPostgreSQLMigrationSchema(t, namespace)
			initial := livePostgreSQLWideningSchema(t, namespace, true, true)
			first := reviewedPostgreSQLEntry(t, "001_initial", empty, initial, nil)
			first, firstFiles := finalizePostgreSQLEntry(t, New(), first)
			manifest := reviewedPostgreSQLManifest(New(), first)
			if err := New().ApplyMigration(context.Background(), database, manifest, firstFiles); err != nil {
				t.Fatalf("bootstrap: %v", err)
			}
			narrowed := livePostgreSQLWideningSchema(t, namespace, false, true)
			second := reviewedPostgreSQLEntry(t, "002_narrow", initial, narrowed, &first)
			second, secondFiles := sealPostgreSQLEntryWithOpaqueSQL(t, second)
			files := mergePostgreSQLMigrationFiles(firstFiles, secondFiles)
			failed := reviewedPostgreSQLManifest(New(), first, second)
			if err := New().ApplyMigration(context.Background(), database, failed, files); err == nil || !strings.Contains(err.Error(), "alterColumnType") {
				t.Fatalf("error=%v; want a typed narrowing refusal", err)
			}
			ledger, err := New().ReadLedger(context.Background(), database)
			if err != nil || len(ledger) != 1 {
				t.Fatalf("ledger=%d error=%v", len(ledger), err)
			}
			if err := New().Verify(context.Background(), database, initial); err != nil {
				t.Fatalf("refused narrowing changed the live schema: %v", err)
			}
		})
	})
}

func TestPostgreSQLRendererRefusesGeneratedAlterTypeWithoutTypedDetach(t *testing.T) {
	field := ir.FieldID(id(wideningLabel))
	input := field
	generated := func(length uint32) physical.PhysicalColumn {
		storage := physical.StorageType{Kind: physical.StoragePostgreSQLVarchar, Length: length}
		return physical.PhysicalColumn{
			ID: field, Name: "label", Storage: storage, Default: physical.PhysicalDefault{Kind: physical.DefaultNone},
			Generated: &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: physical.Expression{
				Kind: physical.ExpressionFunction, Type: storage,
				Symbol:   &physical.SemanticSymbol{Identity: "golem.schema.function.lower.v1", Kind: ir.SchemaSymbolFunction, Version: 1, Provider: ir.ProviderScopePortable},
				Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: storage, Column: &input, Operands: []physical.Expression{}}},
			}},
		}
	}
	tableID := ir.ModelID(id(wideningModel))
	before := physical.PhysicalTable{ID: tableID, Name: "metrics", Columns: []physical.PhysicalColumn{generated(16)}}
	after := physical.PhysicalTable{ID: tableID, Name: "metrics", Columns: []physical.PhysicalColumn{generated(32)}}
	renderer := ddlRenderer{schema: physical.PhysicalSchema{Namespace: physical.Namespace{Name: "public"}}}
	operation := migration.Operation{ID: "forged-generated-alter", Kind: migration.AlterColumnType, ObjectID: string(field), Risk: migration.RiskRewrite}
	if _, err := renderer.incrementalOperation(operation, map[migration.OperationID]ir.ModelID{operation.ID: tableID}, map[ir.ModelID]physical.PhysicalTable{tableID: before}, map[ir.ModelID]physical.PhysicalTable{tableID: after}); err == nil || !strings.Contains(err.Error(), "reviewed detach/recreate DAG") {
		t.Fatalf("generated direct AlterColumnType error=%v", err)
	}
}

func TestPostgreSQLGeneratedCatalogCastRoundTripAndUnknownCastRefusalLiveProfiles(t *testing.T) {
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		namespace := physical.PhysicalName("golem_generated_cast_" + profile)
		dropPostgreSQLWideningNamespace(t, database, namespace)
		defer dropPostgreSQLWideningNamespace(t, database, namespace)
		if _, err := database.Exec(fmt.Sprintf(`CREATE SCHEMA %q; CREATE TABLE %q.%q (
"title" character varying(160) NOT NULL,
"safe" character varying(160) GENERATED ALWAYS AS (lower("title")) STORED,
"forged" text GENERATED ALWAYS AS ((("title")::uuid)::text) STORED
)`, namespace, namespace, "posts")); err != nil {
			t.Fatal(err)
		}
		rows, err := database.Queryx(`SELECT a.attname,pg_catalog.pg_get_expr(d.adbin,d.adrelid)
FROM pg_catalog.pg_attrdef d
JOIN pg_catalog.pg_class c ON c.oid=d.adrelid
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=d.adnum
WHERE n.nspname=$1 AND c.relname='posts' ORDER BY a.attname`, namespace)
		if err != nil {
			t.Fatal(err)
		}
		deparsed := map[string]string{}
		for rows.Next() {
			var name, expression string
			if err := rows.Scan(&name, &expression); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			deparsed[name] = expression
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		field := ir.FieldID("10000000000000000000000000000001")
		varchar := physical.StorageType{Kind: physical.StoragePostgreSQLVarchar, Length: 160}
		table := physical.PhysicalTable{ID: ir.ModelID("00000000000000000000000000000001"), Name: "posts", Columns: []physical.PhysicalColumn{{ID: field, Name: "title", Storage: varchar, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}}}
		if deparsed["safe"] != `lower((title)::text)` {
			t.Fatalf("profile %s generated catalog expression=%q", profile, deparsed["safe"])
		}
		reviewed := physical.Expression{Kind: physical.ExpressionFunction, Type: varchar, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.function.lower.v1", Kind: ir.SchemaSymbolFunction, Version: 1, Provider: ir.ProviderScopePortable}, Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: varchar, Column: &field, Operands: []physical.Expression{}}}}
		if _, err := parseCatalogGeneratedExpression(deparsed["safe"], table, reviewed); err != nil {
			t.Fatalf("profile %s registered generated coercion: %v", profile, err)
		}
		if _, err := parseCatalogGeneratedExpression(deparsed["forged"], table, reviewed); err == nil || !strings.Contains(err.Error(), "unsupported catalog cast uuid") {
			t.Fatalf("profile %s forged catalog cast error=%v expression=%q", profile, err, deparsed["forged"])
		}
	})
}

func TestPostgreSQLGeneratedCatalogRoundTripPreservesTypedFingerprintLiveProfiles(t *testing.T) {
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		namespace := physical.PhysicalName("golem_generated_roundtrip_" + profile)
		dropPostgreSQLWideningNamespace(t, database, namespace)
		defer dropPostgreSQLWideningNamespace(t, database, namespace)
		expected := livePostgreSQLWideningSchema(t, namespace, false, false)
		table := postgresqlTablePointer(&expected, ir.ModelID(id(wideningModel)))
		labelID := ir.FieldID(id(wideningLabel))
		generatedID := ir.FieldID("10000000000000000000000000009991")
		var label physical.PhysicalColumn
		for _, column := range table.Columns {
			if column.ID == labelID {
				label = column
				break
			}
		}
		if label.ID == "" {
			t.Fatal("label field missing from focused generated schema")
		}
		generatedStorage := label.Storage
		table.Columns = append(table.Columns, physical.PhysicalColumn{
			ID: generatedID, Name: "label_search", Ordinal: uint32(len(table.Columns)), Storage: generatedStorage, Nullable: label.Nullable,
			Default: physical.PhysicalDefault{Kind: physical.DefaultNone},
			Generated: &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: physical.Expression{
				Kind: physical.ExpressionFunction, Type: generatedStorage, Nullable: label.Nullable,
				Symbol:   &physical.SemanticSymbol{Identity: "golem.schema.function.lower.v1", Kind: ir.SchemaSymbolFunction, Version: 1, Provider: ir.ProviderScopePortable},
				Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: label.Storage, Nullable: label.Nullable, Column: &labelID, Operands: []physical.Expression{}}},
			}},
			RequiredCapabilities: []physical.CapabilityRequirement{{Capability: CapabilityGeneratedColumns, Owner: physical.ObjectRef{Kind: ir.ObjectField, ModelID: table.ID, FieldID: generatedID}}},
		})
		labelColumn := physical.Expression{Kind: physical.ExpressionColumn, Type: label.Storage, Nullable: label.Nullable, Column: &labelID, Operands: []physical.Expression{}}
		lowerLabel := physical.Expression{Kind: physical.ExpressionFunction, Type: label.Storage, Nullable: label.Nullable, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.function.lower.v1", Kind: ir.SchemaSymbolFunction, Version: 1, Provider: ir.ProviderScopePortable}, Operands: []physical.Expression{labelColumn}}
		isNotNull := physical.Expression{Kind: physical.ExpressionOperator, Type: physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}, Nullable: label.Nullable, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.predicate.is-not-null.v1", Kind: ir.SchemaSymbolOperator, Version: 1, Provider: ir.ProviderScopePortable}, Operands: []physical.Expression{lowerLabel}}
		equalLower := physical.Expression{Kind: physical.ExpressionOperator, Type: physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}, Nullable: label.Nullable, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.predicate.equal.v1", Kind: ir.SchemaSymbolOperator, Version: 1, Provider: ir.ProviderScopePortable}, Operands: []physical.Expression{lowerLabel, labelColumn}}
		checkID := ir.CheckID("20000000000000000000000000009991")
		indexID := ir.IndexID("30000000000000000000000000009991")
		table.Checks = append(table.Checks, physical.PhysicalCheck{ID: checkID, Name: "ck_label_lower", Expression: equalLower})
		table.Indexes = append(table.Indexes, physical.PhysicalIndex{ID: indexID, Name: "idx_label_lower", Method: physical.IndexBTree, Keys: []physical.IndexKey{{Expression: &lowerLabel, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, Predicate: &isNotNull, CreationMode: physical.IndexTransactional, RequiredCapabilities: []physical.CapabilityRequirement{{Capability: capabilityAdvancedIndexes, Owner: physical.ObjectRef{Kind: ir.ObjectIndex, ModelID: table.ID, ObjectID: ir.ObjectID(indexID)}}}})
		expected = normalizePostgreSQLMigrationSchema(t, expected)
		script, err := New().RenderInitial(expected)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(context.Background(), script.SQL()); err != nil {
			t.Fatal(err)
		}
		actual, err := New().introspect(context.Background(), database, expected)
		if err != nil {
			t.Fatal(err)
		}
		expectedFingerprint, err := physical.PhysicalFingerprint(expected)
		if err != nil {
			t.Fatal(err)
		}
		actualFingerprint, err := physical.PhysicalFingerprint(actual)
		if err != nil {
			t.Fatal(err)
		}
		if expectedFingerprint != actualFingerprint {
			t.Fatalf("profile %s typed schema differences: %s", profile, typedSchemaDifferences(expected, actual))
		}
	})
}

func typedSchemaDifferences(expected, actual physical.PhysicalSchema) string {
	var differences []string
	if expected.Version != actual.Version || expected.CanonicalVersion != actual.CanonicalVersion {
		differences = append(differences, fmt.Sprintf("schema.version expected=%d/%d actual=%d/%d", expected.Version, expected.CanonicalVersion, actual.Version, actual.CanonicalVersion))
	}
	if !reflect.DeepEqual(expected.Provider, actual.Provider) {
		differences = append(differences, fmt.Sprintf("provider expected=%#v actual=%#v", expected.Provider, actual.Provider))
	}
	if !reflect.DeepEqual(expected.System, actual.System) {
		differences = append(differences, fmt.Sprintf("system expected=%#v actual=%#v", expected.System, actual.System))
	}
	if !reflect.DeepEqual(expected.Extensions, actual.Extensions) {
		differences = append(differences, fmt.Sprintf("extensions expected=%#v actual=%#v", expected.Extensions, actual.Extensions))
	}
	actualTables := map[ir.ModelID]physical.PhysicalTable{}
	for _, table := range actual.Tables {
		actualTables[table.ID] = table
	}
	for _, expectedTable := range expected.Tables {
		actualTable, exists := actualTables[expectedTable.ID]
		if !exists {
			differences = append(differences, "table["+string(expectedTable.ID)+"].missing")
			continue
		}
		actualColumns := map[ir.FieldID]physical.PhysicalColumn{}
		for _, column := range actualTable.Columns {
			actualColumns[column.ID] = column
		}
		for _, expectedColumn := range expectedTable.Columns {
			actualColumn, exists := actualColumns[expectedColumn.ID]
			if !exists {
				differences = append(differences, "field["+string(expectedColumn.ID)+"].missing")
				continue
			}
			left, right := expectedColumn, actualColumn
			left.Name, right.Name = "", ""
			if !reflect.DeepEqual(left, right) {
				differences = append(differences, fmt.Sprintf("field[%s] expected=%#v actual=%#v", expectedColumn.ID, left, right))
			}
		}
		leftTable, rightTable := expectedTable, actualTable
		leftTable.Name, rightTable.Name = "", ""
		leftTable.Columns, rightTable.Columns = nil, nil
		if !reflect.DeepEqual(leftTable, rightTable) {
			if !reflect.DeepEqual(expectedTable.PrimaryKey, actualTable.PrimaryKey) {
				differences = append(differences, fmt.Sprintf("table[%s].primary expected=%#v actual=%#v", expectedTable.ID, expectedTable.PrimaryKey, actualTable.PrimaryKey))
			}
			appendObjectDifference := func(kind string, expectedValues, actualValues any) {
				if !reflect.DeepEqual(expectedValues, actualValues) {
					left, _ := json.Marshal(expectedValues)
					right, _ := json.Marshal(actualValues)
					differences = append(differences, fmt.Sprintf("table[%s].%s expected=%s actual=%s", expectedTable.ID, kind, left, right))
				}
			}
			appendObjectDifference("uniques", expectedTable.Uniques, actualTable.Uniques)
			appendObjectDifference("foreignKeys", expectedTable.ForeignKeys, actualTable.ForeignKeys)
			appendObjectDifference("checks", expectedTable.Checks, actualTable.Checks)
			appendObjectDifference("indexes", expectedTable.Indexes, actualTable.Indexes)
		}
	}
	if len(differences) == 0 {
		differences = append(differences, "non-column schema facts differ")
	}
	return strings.Join(differences, "; ")
}

func TestPostgreSQLSafeTypeWideningPreservesEveryValueAndDependentObject(t *testing.T) {
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		namespace := physical.PhysicalName("golem_widening_values_" + profile)
		dropPostgreSQLWideningNamespace(t, database, namespace)
		defer dropPostgreSQLWideningNamespace(t, database, namespace)
		before, after, manifest, files := postgresqlWideningHistory(t, namespace)
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		insertPostgreSQLWideningCorpus(t, database, namespace)
		captured := readPostgreSQLWideningCorpus(t, database, namespace)
		if len(captured) == 0 {
			t.Fatal("value corpus is empty")
		}
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("widening: %v", err)
		}
		if err := New().Verify(context.Background(), database, after); err != nil {
			t.Fatalf("widened schema does not match the reviewed after snapshot: %v", err)
		}
		if err := New().Verify(context.Background(), database, before); err == nil {
			t.Fatal("widened database still matches the pre-widening snapshot")
		}
		widened := readPostgreSQLWideningCorpus(t, database, namespace)
		if len(widened) != len(captured) {
			t.Fatalf("row count changed from %d to %d", len(captured), len(widened))
		}
		for index := range captured {
			if captured[index] != widened[index] {
				t.Fatalf("row %d changed:\n before=%s\n  after=%s", index, captured[index], widened[index])
			}
		}
		for column, expected := range map[string]string{
			"views": "bigint", "ratio": "double precision", "amount": "numeric(14,2)",
			"tick": "time(6) without time zone", "seen": "timestamp(6) with time zone", "position": "integer",
			"label": "character varying(32)", "description": "text",
		} {
			if actual := postgresqlColumnCatalogType(t, database, namespace, "metrics", column); actual != expected {
				t.Fatalf("column %s type=%q want=%q", column, actual, expected)
			}
		}
		if actual := postgresqlColumnCatalogType(t, database, namespace, "metric_links", "views_ref"); actual != "bigint" {
			t.Fatalf("dependent child column type=%q want=bigint", actual)
		}
		for name, kind := range map[string]string{"uq_metrics_views": "u", "pk_metrics": "p", "fk_metric_links_views": "f"} {
			if !postgresqlConstraintExists(t, database, namespace, name, kind) {
				t.Fatalf("dependent constraint %s (%s) was not restored", name, kind)
			}
		}
		if !postgresqlIndexExists(t, database, namespace, "idx_metrics_views") {
			t.Fatal("dependent index idx_metrics_views was not restored")
		}
		if _, err := database.Exec(fmt.Sprintf(`INSERT INTO %q.%q ("id","views") VALUES (900, 2147483647)`, namespace, "metrics")); err == nil {
			t.Fatal("restored unique constraint did not reject a duplicate")
		}
		if _, err := database.Exec(fmt.Sprintf(`INSERT INTO %q.%q ("id","views_ref") VALUES (900, 8589934592)`, namespace, "metric_links")); err == nil {
			t.Fatal("restored foreign key did not reject an orphan")
		}
		if _, err := database.Exec(fmt.Sprintf(`INSERT INTO %q.%q ("id","views") VALUES (901, 8589934592)`, namespace, "metrics")); err != nil {
			t.Fatalf("widened column rejected a value outside the old domain: %v", err)
		}
	})
}

func TestPostgreSQLWideningRollbackLeavesSchemaDataAndLedgerUnchanged(t *testing.T) {
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		namespace := physical.PhysicalName("golem_widening_rollback_" + profile)
		dropPostgreSQLWideningNamespace(t, database, namespace)
		defer dropPostgreSQLWideningNamespace(t, database, namespace)
		empty := canonicalEmptyPostgreSQLMigrationSchema(t, namespace)
		initial := livePostgreSQLWideningSchema(t, namespace, false, false)
		first := reviewedPostgreSQLEntry(t, "001_initial", empty, initial, nil)
		first, firstFiles := finalizePostgreSQLEntry(t, New(), first)
		if err := New().ApplyMigration(context.Background(), database, reviewedPostgreSQLManifest(New(), first), firstFiles); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		insertPostgreSQLWideningCorpus(t, database, namespace)
		if _, err := database.Exec(fmt.Sprintf(`INSERT INTO %q.%q ("id","views_ref") VALUES (500, 424242)`, namespace, "metric_links")); err != nil {
			t.Fatal(err)
		}
		captured := readPostgreSQLWideningCorpus(t, database, namespace)
		widened := livePostgreSQLWideningSchema(t, namespace, true, true)
		second := reviewedPostgreSQLEntry(t, "002_widen_and_link", initial, widened, &first)
		second, secondFiles := finalizePostgreSQLEntry(t, New(), second)
		if !strings.Contains(string(secondFiles["migrations/postgresql/002_widen_and_link.sql"]), "ALTER COLUMN \"views\" TYPE bigint") {
			t.Fatalf("entry does not contain the widening:\n%s", secondFiles["migrations/postgresql/002_widen_and_link.sql"])
		}
		manifest := reviewedPostgreSQLManifest(New(), first, second)
		files := mergePostgreSQLMigrationFiles(firstFiles, secondFiles)
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err == nil {
			t.Fatal("migration with an orphan foreign-key row unexpectedly committed")
		}
		ledger, err := New().ReadLedger(context.Background(), database)
		if err != nil || len(ledger) != 1 {
			t.Fatalf("ledger=%d error=%v", len(ledger), err)
		}
		if err := New().Verify(context.Background(), database, initial); err != nil {
			t.Fatalf("rolled-back migration left schema drift: %v", err)
		}
		if actual := postgresqlColumnCatalogType(t, database, namespace, "metrics", "views"); actual != "integer" {
			t.Fatalf("rolled-back column type=%q want=integer", actual)
		}
		rolled := readPostgreSQLWideningCorpus(t, database, namespace)
		if len(rolled) != len(captured) {
			t.Fatalf("row count changed from %d to %d", len(captured), len(rolled))
		}
		for index := range captured {
			if captured[index] != rolled[index] {
				t.Fatalf("row %d changed across rollback:\n before=%s\n  after=%s", index, captured[index], rolled[index])
			}
		}
	})
}

func TestPostgreSQLWideningCAndLinguisticProfilesProduceIdenticalTruth(t *testing.T) {
	c := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))
	linguistic := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"))
	if c == "" || linguistic == "" {
		t.Skip("both GOLEM_TEST_POSTGRES_DSN and GOLEM_TEST_POSTGRES_LINGUISTIC_DSN are required")
	}
	var truth []string
	var corpus []string
	for _, profile := range []struct{ name, dsn string }{{"c", c}, {"linguistic", linguistic}} {
		database, _, err := New().Open(context.Background(), profile.dsn)
		if err != nil {
			t.Fatal(err)
		}
		namespace := physical.PhysicalName("golem_widening_profile")
		dropPostgreSQLWideningNamespace(t, database, namespace)
		_, after, manifest, files := postgresqlWideningHistory(t, namespace)
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("%s bootstrap: %v", profile.name, err)
		}
		insertPostgreSQLWideningCorpus(t, database, namespace)
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("%s widening: %v", profile.name, err)
		}
		if err := New().Verify(context.Background(), database, after); err != nil {
			t.Fatalf("%s widened schema mismatch: %v", profile.name, err)
		}
		observed := postgresqlWideningTruth(t, manifest.Entries[1], database, namespace)
		if truth == nil {
			truth, corpus = observed, readPostgreSQLWideningCorpus(t, database, namespace)
		} else {
			if len(observed) != len(truth) {
				t.Fatalf("%s produced %d truth lines, want %d", profile.name, len(observed), len(truth))
			}
			for index := range truth {
				if truth[index] != observed[index] {
					t.Fatalf("%s truth line %d differs:\n c=%s\n %s=%s", profile.name, index, truth[index], profile.name, observed[index])
				}
			}
			rows := readPostgreSQLWideningCorpus(t, database, namespace)
			if len(rows) != len(corpus) {
				t.Fatalf("%s produced %d rows, want %d", profile.name, len(rows), len(corpus))
			}
			for index := range corpus {
				if corpus[index] != rows[index] {
					t.Fatalf("%s row %d differs:\n c=%s\n %s=%s", profile.name, index, corpus[index], profile.name, rows[index])
				}
			}
		}
		dropPostgreSQLWideningNamespace(t, database, namespace)
		database.Close()
	}
}

func postgresqlWideningTruth(t *testing.T, entry migration.ManifestEntry, database *sqlx.DB, namespace physical.PhysicalName) []string {
	t.Helper()
	plan, err := New().PlanIncremental(entry)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{"sql:" + plan.SQL(), "chain:" + string(entry.ChainHash)}
	for _, operation := range entry.Operations {
		lines = append(lines, fmt.Sprintf("op:%s:%s:%s:%s:%s:%s", operation.ID, operation.Kind, operation.ObjectID, operation.Risk, operation.Before, operation.After))
	}
	for _, approval := range entry.Approvals {
		lines = append(lines, fmt.Sprintf("approval:%s:%s:%s:%s", approval.OperationID, approval.Risk, approval.Before, approval.After))
	}
	for _, column := range []string{"views", "ratio", "amount", "tick", "seen", "position", "label", "description"} {
		lines = append(lines, "type:"+column+":"+postgresqlColumnCatalogType(t, database, namespace, "metrics", column))
	}
	var collations string
	if err := database.Get(&collations, `SELECT COALESCE(string_agg(a.attname||'='||COALESCE(co.collname,'-'),',' ORDER BY a.attname),'') FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_collation co ON co.oid=a.attcollation WHERE n.nspname=$1 AND c.relname='metrics' AND a.attnum>0 AND NOT a.attisdropped`, string(namespace)); err != nil {
		t.Fatal(err)
	}
	return append(lines, "collation:"+collations)
}

func postgresqlWideningHistory(t *testing.T, namespace physical.PhysicalName) (physical.PhysicalSchema, physical.PhysicalSchema, migration.Manifest, map[string][]byte) {
	t.Helper()
	empty := canonicalEmptyPostgreSQLMigrationSchema(t, namespace)
	before := livePostgreSQLWideningSchema(t, namespace, false, true)
	after := livePostgreSQLWideningSchema(t, namespace, true, true)
	first := reviewedPostgreSQLEntry(t, "001_initial", empty, before, nil)
	first, firstFiles := finalizePostgreSQLEntry(t, New(), first)
	second := reviewedPostgreSQLEntry(t, "002_widen", before, after, &first)
	second, secondFiles := finalizePostgreSQLEntry(t, New(), second)
	return before, after, reviewedPostgreSQLManifest(New(), first, second), mergePostgreSQLMigrationFiles(firstFiles, secondFiles)
}

var postgresqlWideningCorpusRows = []struct {
	id                               int
	views, position                  string
	ratio, amount, tick, seen, label string
}{
	{1, "-2147483648", "-32768", "'-Infinity'", "'-99999999.99'", "'00:00:00'", "'0001-01-01 00:00:00+00'", "'1234567890abcdef'"},
	{2, "2147483647", "32767", "'Infinity'", "'99999999.99'", "'23:59:59'", "'9999-12-31 23:59:59+00'", "'max'"},
	{3, "0", "0", "'NaN'", "'0.00'", "'12:00:00'", "'2026-08-11 10:00:00+00'", "'zero'"},
	{4, "-1", "-1", "'-0'", "'-0.01'", "'00:00:01'", "'1970-01-01 00:00:00+00'", "NULL"},
	{5, "1", "1", "'3.4028235e+38'", "'0.01'", "'23:59:58'", "'2000-02-29 12:34:56+00'", "'float4 max'"},
	{6, "42", "7", "'-3.4028235e+38'", "'12345.67'", "'06:30:00'", "'1999-12-31 23:59:59+00'", "'float4 min'"},
	{7, "1000000", "255", "'1.1754944e-38'", "'-12345.67'", "'18:45:30'", "'2038-01-19 03:14:07+00'", "'float4 tiny'"},
	{8, "-1000000", "-255", "'0.1'", "'1.10'", "'01:02:03'", "'2026-01-01 00:00:00+00'", "'inexact decimal'"},
}

func insertPostgreSQLWideningCorpus(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName) {
	t.Helper()
	for _, row := range postgresqlWideningCorpusRows {
		statement := fmt.Sprintf(`INSERT INTO %q.%q ("id","views","ratio","amount","tick","seen","position","label","description") VALUES (%d,%s,%s,%s,%s,%s,%s,%s,%s)`,
			namespace, "metrics", row.id, row.views, row.ratio, row.amount, row.tick, row.seen, row.position, row.label, row.label)
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("insert row %d: %v", row.id, err)
		}
	}
}

func readPostgreSQLWideningCorpus(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName) []string {
	t.Helper()
	statement := fmt.Sprintf(`SELECT "id"::text||'|'||"views"::text||'|'||COALESCE("ratio"::real::text,'~')||'|'||COALESCE("amount"::numeric(10,2)::text,'~')||'|'||COALESCE("tick"::time(0)::text,'~')||'|'||COALESCE((("seen"::timestamptz(0)) AT TIME ZONE 'UTC')::text,'~')||'|'||COALESCE("position"::smallint::text,'~')||'|'||COALESCE("label",'~')||'|'||COALESCE("description",'~') FROM %q.%q ORDER BY "id"`, namespace, "metrics")
	var rows []string
	if err := database.Select(&rows, statement); err != nil {
		t.Fatal(err)
	}
	return rows
}

func postgresqlColumnCatalogType(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName, table, column string) string {
	t.Helper()
	var value string
	if err := database.Get(&value, `SELECT pg_catalog.format_type(a.atttypid,a.atttypmod) FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND a.attname=$3`, string(namespace), table, column); err != nil {
		t.Fatal(err)
	}
	return value
}

func postgresqlConstraintExists(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName, name, kind string) bool {
	t.Helper()
	var count int
	if err := database.Get(&count, `SELECT count(*) FROM pg_catalog.pg_constraint k JOIN pg_catalog.pg_namespace n ON n.oid=k.connamespace WHERE n.nspname=$1 AND k.conname=$2 AND k.contype=$3 AND k.convalidated`, string(namespace), name, kind); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func postgresqlIndexExists(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName, name string) bool {
	t.Helper()
	var count int
	if err := database.Get(&count, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind='i'`, string(namespace), name); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func dropPostgreSQLWideningNamespace(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName) {
	t.Helper()
	if _, err := database.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, namespace)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func sealPostgreSQLEntryWithOpaqueSQL(t *testing.T, entry migration.ManifestEntry) (migration.ManifestEntry, map[string][]byte) {
	t.Helper()
	base := "migrations/postgresql/" + string(entry.ID)
	beforeBytes, err := json.Marshal(entry.BeforeSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := json.Marshal(entry.AfterSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		base + ".sql":                  []byte("-- refused\n"),
		base + ".before.snapshot.json": beforeBytes,
		base + ".after.snapshot.json":  afterBytes,
	}
	entry.Files = []migration.FileChecksum{
		{Path: base + ".sql", SHA256: migration.Checksum(files[base+".sql"])},
		{Path: base + ".before.snapshot.json", SHA256: migration.Checksum(beforeBytes)},
		{Path: base + ".after.snapshot.json", SHA256: migration.Checksum(afterBytes)},
	}
	entry.ChainHash = migration.ChainHash(entry)
	return entry, files
}

func forEachPostgreSQLProfile(t *testing.T, run func(*testing.T, string, *sqlx.DB)) {
	t.Helper()
	profiles := []struct{ name, environment string }{
		{"c", "GOLEM_TEST_POSTGRES_DSN"},
		{"linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.environment))
			if dsn == "" {
				t.Skipf("%s is not set", profile.environment)
			}
			database, _, err := New().Open(context.Background(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			run(t, profile.name, database)
		})
	}
}
