package postgresql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

func TestSemanticIndexRendersPGVectorStorage(t *testing.T) {
	provider := New()
	model := fixtureModel()
	payload, err := semanticcontract.Encode(semanticcontract.Index{
		Name: "related", Space: "content", Dimensions: 384,
		Fields: []string{id(29)}, Metric: "cosine",
	})
	if err != nil {
		t.Fatal(err)
	}
	model.Extensions = append(model.Extensions, ir.ProviderExtensionIR{
		ID: ir.ExtensionID(id(73)), Provider: ir.PostgreSQL, Version: semanticcontract.Version,
		Owner: ir.ObjectID(id(2)), Kind: semanticcontract.IndexKind, Payload: payload,
	})
	schema, err := provider.Lower(context.Background(), model, physical.LowerOptions{Namespace: "social"})
	if err != nil {
		t.Fatal(err)
	}
	script, err := provider.RenderInitial(schema)
	if err != nil {
		t.Fatal(err)
	}
	base := "_golem_semantic_" + id(73)
	for _, fragment := range []string{
		"CREATE EXTENSION IF NOT EXISTS vector",
		`CREATE TABLE "social"."` + base + `_state"`,
		`CREATE TABLE "social"."` + base + `_vec"`,
		`"embedding" vector(384) NOT NULL`,
		`"updated_at" bigint NOT NULL CHECK ("updated_at" >= 0), "id" uuid NOT NULL)`,
		`CREATE INDEX "` + base + `_state_identity" ON "social"."` + base + `_state" ("id")`,
		`CREATE INDEX "` + base + `_state_stale" ON "social"."` + base + `_state" ("record_key") WHERE "status" <> 'ready'`,
	} {
		if !strings.Contains(script.SQL(), fragment) {
			t.Fatalf("PostgreSQL semantic DDL missing %q:\n%s", fragment, script.SQL())
		}
	}
	if strings.Contains(script.SQL(), "USING hnsw") {
		t.Fatalf("exact semantic storage created an unused HNSW index:\n%s", script.SQL())
	}
}

func TestReviewedSemanticSnapshotReplaysLegacyShadowShape(t *testing.T) {
	provider := New()
	model := fixtureModel()
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{id(29)}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	model.Extensions = append(model.Extensions, ir.ProviderExtensionIR{ID: ir.ExtensionID(id(73)), Provider: ir.PostgreSQL, Version: semanticcontract.Version, Owner: ir.ObjectID(id(2)), Kind: semanticcontract.IndexKind, Payload: payload})
	schema, err := provider.Lower(context.Background(), model, physical.LowerOptions{Namespace: "social"})
	if err != nil {
		t.Fatal(err)
	}
	legacy := schema
	legacy.Extensions = append([]physical.Extension(nil), schema.Extensions...)
	legacy.Extensions[0].Attributes = nil
	for _, attribute := range schema.Extensions[0].Attributes {
		if attribute.Name != "identity" {
			legacy.Extensions[0].Attributes = append(legacy.Extensions[0].Attributes, attribute)
		}
	}
	if _, err := provider.renderInitial(legacy); err == nil {
		t.Fatal("ordinary rendering accepted a legacy semantic extension")
	}
	script, err := provider.renderReviewedInitialSnapshot(reviewedInitialSnapshot{schema: legacy})
	if err != nil {
		t.Fatal(err)
	}
	base := "_golem_semantic_" + id(73)
	if !strings.Contains(script.SQL(), `CREATE TABLE "social"."`+base+`_state"`) || !strings.Contains(script.SQL(), `CREATE TABLE "social"."`+base+`_vec"`) {
		t.Fatalf("legacy semantic storage is absent:\n%s", script.SQL())
	}
	if strings.Contains(script.SQL(), base+"_state_identity") || strings.Contains(script.SQL(), base+"_state_stale") {
		t.Fatalf("legacy replay invented current state indexes:\n%s", script.SQL())
	}
}

func TestLowerPreservesPortablePostgreSQLSemantics(t *testing.T) {
	provider := New()
	schema, err := provider.Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "social"})
	if err != nil {
		t.Fatal(err)
	}
	if schema.Provider.Driver.Module != "github.com/jackc/pgx/v5/stdlib" || schema.Provider.MinimumVersion.Major != 15 {
		t.Fatalf("manifest = %#v", schema.Provider)
	}
	posts := physicalTable(t, schema, "posts")
	if len(posts.Columns) != 10 {
		t.Fatalf("post columns = %d", len(posts.Columns))
	}
	for index, column := range posts.Columns {
		if column.Ordinal != uint32(index) {
			t.Fatalf("column %s ordinal=%d want %d", column.Name, column.Ordinal, index)
		}
	}
	assertStorage(t, posts, "amount", physical.StoragePostgreSQLNumeric, 18, 4)
	assertStorage(t, posts, "id", physical.StoragePostgreSQLUUID, 0, 0)
	assertStorage(t, posts, "day", physical.StoragePostgreSQLDate, 0, 0)
	assertStorage(t, posts, "clock", physical.StoragePostgreSQLTime, 0, 0)
	assertStorage(t, posts, "created_at", physical.StoragePostgreSQLTimestampTZ, 0, 0)
	assertStorage(t, posts, "metadata", physical.StoragePostgreSQLJSONB, 0, 0)
	assertStorage(t, posts, "tags", physical.StoragePostgreSQLJSONB, 0, 0)
	if posts.PrimaryKey == nil || len(posts.PrimaryKey.Columns) != 1 || len(posts.ForeignKeys) != 1 || posts.ForeignKeys[0].OnDelete != ir.ActionCascade {
		t.Fatalf("keys/fk = %#v %#v", posts.PrimaryKey, posts.ForeignKeys)
	}
	if len(posts.Checks) < 2 {
		t.Fatalf("synthetic enum/list checks missing: %#v", posts.Checks)
	}
	if len(schema.System.Objects) != 5 || !hasPostgreSQLSystemObject(schema.System, physical.SystemOutbox) || !hasPostgreSQLSystemObject(schema.System, physical.SystemOutboxDelivery) || !hasPostgreSQLSystemObject(schema.System, physical.SystemUpsertGuard) {
		t.Fatalf("system schema = %#v", schema.System)
	}
	first, _ := physical.PhysicalFingerprint(schema)
	model := fixtureModel()
	for left, right := 0, len(model.Models[1].Fields)-1; left < right; left, right = left+1, right-1 {
		model.Models[1].Fields[left], model.Models[1].Fields[right] = model.Models[1].Fields[right], model.Models[1].Fields[left]
	}
	model.Models[0], model.Models[1] = model.Models[1], model.Models[0]
	secondSchema, err := provider.Lower(context.Background(), model, physical.LowerOptions{Namespace: "social"})
	if err != nil {
		t.Fatal(err)
	}
	second, _ := physical.PhysicalFingerprint(secondSchema)
	if first != second {
		t.Fatalf("shuffle changed physical fingerprint: %s != %s", first, second)
	}
}

func TestRenderInitialFullyQualifiesAndQuotesEveryObject(t *testing.T) {
	provider := New()
	schema, err := provider.Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "social"})
	if err != nil {
		t.Fatal(err)
	}
	script, err := provider.RenderInitial(schema)
	if err != nil {
		t.Fatal(err)
	}
	sql := script.SQL()
	if strings.Contains(sql, `_golem_upsert_guard`) {
		t.Fatalf("PostgreSQL selector guard must not render a relation:\n%s", sql)
	}
	for _, fragment := range []string{`CREATE SCHEMA IF NOT EXISTS "social"`, `CREATE SCHEMA IF NOT EXISTS "_golem"`, `CREATE TABLE "social"."posts"`, `CREATE TABLE "_golem"."_golem_migrations"`, `CREATE TABLE "_golem"."_golem_outbox"`, `CREATE INDEX "_golem_outbox_pending" ON "_golem"."_golem_outbox"`, `CREATE TABLE "_golem"."_golem_outbox_delivery"`, `CREATE INDEX "_golem_outbox_delivery_pending" ON "_golem"."_golem_outbox_delivery"`, `ALTER TABLE "social"."posts" ADD CONSTRAINT`, `REFERENCES "social"."users"`, `CREATE INDEX "idx_posts_created" ON "social"."posts"`} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("DDL missing %q:\n%s", fragment, sql)
		}
	}
	if strings.Contains(strings.ToLower(sql), "search_path") {
		t.Fatal("DDL depends on search_path")
	}
	for _, field := range []string{`"migration_id"`, `"parent_chain_hash"`, `"chain_hash"`, `"file_checksums"`, `"before_physical_fingerprint"`, `"after_physical_fingerprint"`, `"phases"`, `"applied_at"`} {
		if !strings.Contains(sql, field) {
			t.Errorf("ledger DDL missing %s", field)
		}
	}
	if strings.Contains(sql, `"phase_status"`) {
		t.Fatal("ledger collapsed the canonical phase inventory to one status")
	}
	first := sql
	schema.Tables[0], schema.Tables[1] = schema.Tables[1], schema.Tables[0]
	second, err := provider.RenderInitial(schema)
	if err != nil {
		t.Fatal(err)
	}
	if first != second.SQL() {
		t.Fatal("shuffled physical schema changed DDL")
	}
}

func TestPublicPostgreSQLRenderInitialRefusesHistoricalV1(t *testing.T) {
	schema, err := New().Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "social"})
	if err != nil {
		t.Fatal(err)
	}
	schema.Version, schema.CanonicalVersion = 1, 1
	if _, err := New().RenderInitial(schema); err == nil || !strings.Contains(err.Error(), "got 1, want") {
		t.Fatalf("public RenderInitial accepted historical v1: %v", err)
	}
	if _, err := New().Introspect(context.Background(), nil, schema); err == nil || !strings.Contains(err.Error(), "got 1, want") {
		t.Fatalf("public Introspect did not refuse historical v1 before database work: %v", err)
	}
	if err := New().ApplyInitial(context.Background(), nil, schema); err == nil || !strings.Contains(err.Error(), "got 1, want") {
		t.Fatalf("public ApplyInitial did not refuse historical v1 before database work: %v", err)
	}
}

func hasPostgreSQLSystemObject(system physical.SystemSchema, kind physical.SystemObjectKind) bool {
	for _, object := range system.Objects {
		if object.Kind == kind {
			return true
		}
	}
	return false
}

func TestCatalogExpressionParserNormalizesPostgreSQLOutput(t *testing.T) {
	statusFieldID := ir.FieldID(id(21))
	table := physical.PhysicalTable{ID: ir.ModelID(id(2)), Name: "posts", Columns: []physical.PhysicalColumn{{ID: statusFieldID, Name: "status", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}}}}
	expression, err := parseCatalogExpression(`(("status"=ANY(ARRAY['draft])value'::text,'live'::text])))`, table)
	if err != nil {
		t.Fatal(err)
	}
	if expression.Symbol == nil || expression.Symbol.Identity != "golem.schema.predicate.in.v1" || len(expression.Operands) != 3 {
		t.Fatalf("parsed expression = %#v", expression)
	}
	jsonID := ir.FieldID(id(22))
	table.Columns = append(table.Columns, physical.PhysicalColumn{ID: jsonID, Name: "tags", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLJSONB}})
	expression, err = parseCatalogExpression(`(jsonb_typeof("tags") = 'array'::text)`, table)
	if err != nil {
		t.Fatal(err)
	}
	if expression.Operands[0].Symbol == nil || expression.Operands[0].Symbol.Identity != "golem.postgresql.function.jsonb-typeof.v1" {
		t.Fatalf("parsed json check = %#v", expression)
	}
}

func TestCatalogGeneratedExpressionNormalizesOnlyRegisteredVarcharTextCoercion(t *testing.T) {
	field := ir.FieldID("10000000000000000000000000000001")
	storage := physical.StorageType{Kind: physical.StoragePostgreSQLVarchar, Length: 160}
	table := physical.PhysicalTable{ID: ir.ModelID("00000000000000000000000000000001"), Name: "posts", Columns: []physical.PhysicalColumn{{ID: field, Name: "title", Storage: storage, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}}}

	raw, err := parseCatalogExpression(`lower((title)::text)`, table)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Operands) != 1 || raw.Operands[0].Kind != physical.ExpressionCast || raw.Operands[0].Symbol == nil || raw.Operands[0].Symbol.Identity != catalogCastVarcharToTextV1 {
		t.Fatalf("raw registered cast was not preserved: %#v", raw)
	}
	reviewed := physical.Expression{Kind: physical.ExpressionFunction, Type: storage, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.function.lower.v1", Kind: ir.SchemaSymbolFunction, Version: 1, Provider: ir.ProviderScopePortable}, Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: storage, Column: &field, Operands: []physical.Expression{}}}}
	normalized, err := parseCatalogGeneratedExpression(`lower((title)::text)`, table, reviewed)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Type != storage || len(normalized.Operands) != 1 || normalized.Operands[0].Kind != physical.ExpressionColumn || normalized.Operands[0].Type != storage {
		t.Fatalf("generated coercion did not reconstruct reviewed expression: %#v", normalized)
	}
	if _, err := parseCatalogGeneratedExpression(`lower((title)::uuid)`, table, reviewed); err == nil || !strings.Contains(err.Error(), "unsupported catalog cast uuid") {
		t.Fatalf("unregistered catalog cast error = %v", err)
	}
	otherReviewed := reviewed
	otherReviewed.Type = physical.StorageType{Kind: physical.StoragePostgreSQLVarchar, Length: 159}
	if _, err := parseCatalogGeneratedExpression(`lower((title)::text)`, table, otherReviewed); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched target coercion error = %v", err)
	}
}

func TestSyntheticChecksUseStableIdentityNotPhysicalSpellingOrOrdinal(t *testing.T) {
	model := fixtureModel()
	first, err := New().Lower(context.Background(), model, physical.LowerOptions{Namespace: "social"})
	if err != nil {
		t.Fatal(err)
	}
	post := &model.Models[1]
	for index := range post.Fields {
		if post.Fields[index].ID == ir.FieldID(id(30)) {
			post.Fields[index].Scalar.Column = "state"
			post.Fields[index].DeclarationOrder = 100
		}
	}
	second, err := New().Lower(context.Background(), model, physical.LowerOptions{Namespace: "social"})
	if err != nil {
		t.Fatal(err)
	}
	checkIdentity := func(schema physical.PhysicalSchema) (ir.CheckID, physical.PhysicalName) {
		for _, check := range physicalTable(t, schema, "posts").Checks {
			if strings.Contains(string(check.Name), "enum") {
				return check.ID, check.Name
			}
		}
		t.Fatal("enum check absent")
		return "", ""
	}
	firstID, firstName := checkIdentity(first)
	secondID, secondName := checkIdentity(second)
	if firstID != secondID || firstName != secondName {
		t.Fatalf("synthetic check identity changed under physical rename/order: %s/%s != %s/%s", firstID, firstName, secondID, secondName)
	}
}

func TestLowerRejectsWrongProviderScopeAndInvalidScalarList(t *testing.T) {
	for _, mutate := range []func(*ir.ModelIR){
		func(model *ir.ModelIR) { model.Providers = []ir.Provider{ir.SQLite} },
		func(model *ir.ModelIR) { model.Models[1].Indexes[0].Provider = ir.ProviderScopeSQLite },
		func(model *ir.ModelIR) {
			model.Models[1].Indexes[0].Keys[0].Collation = stringPointer("provider_collation")
		},
		func(model *ir.ModelIR) {
			for index := range model.Models[1].Fields {
				field := &model.Models[1].Fields[index]
				if field.Scalar != nil && field.Scalar.Type.Kind == ir.TypeScalarList {
					bad := ir.CapabilityID("lookalike")
					field.Scalar.Type.Capability = &bad
				}
			}
		},
		func(model *ir.ModelIR) {
			for index := range model.Models[1].Fields {
				field := &model.Models[1].Fields[index]
				if field.Scalar != nil && field.Scalar.Type.Kind == ir.TypeScalarList {
					field.Scalar.Type.Element = &ir.LogicalTypeIR{Kind: ir.TypeBytes}
				}
			}
		},
	} {
		model := fixtureModel()
		mutate(&model)
		if _, err := New().Lower(context.Background(), model, physical.LowerOptions{Namespace: "social"}); err == nil {
			t.Fatal("unsupported PostgreSQL declaration lowered")
		}
	}
}

func TestEveryAcceptedScalarListElementValidates(t *testing.T) {
	enumID := ir.EnumID(id(70))
	enums := map[ir.EnumID]ir.EnumIR{enumID: {ID: enumID}}
	p, s, tp := uint16(18), uint16(4), uint16(6)
	values := []ir.LogicalTypeIR{{Kind: ir.TypeString}, {Kind: ir.TypeBool}, {Kind: ir.TypeInt16}, {Kind: ir.TypeInt32}, {Kind: ir.TypeInt64}, {Kind: ir.TypeFloat32}, {Kind: ir.TypeFloat64}, {Kind: ir.TypeDecimal, Precision: &p, Scale: &s}, {Kind: ir.TypeUUID}, {Kind: ir.TypeDate}, {Kind: ir.TypeTime, Precision: &tp}, {Kind: ir.TypeDateTime, Precision: &tp}, {Kind: ir.TypeEnum, EnumID: &enumID}}
	for _, element := range values {
		element := element
		t.Run(string(element.Kind), func(t *testing.T) {
			capability := scalarListJSONCapability
			logical := ir.LogicalTypeIR{Kind: ir.TypeScalarList, Element: &element, Capability: &capability}
			if err := validateLogicalStorageType(logical, enums); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRenderRejectsUnknownSystemObjectVersion(t *testing.T) {
	schema, err := New().Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "social"})
	if err != nil {
		t.Fatal(err)
	}
	schema.System.Objects[1].Version = 2
	if _, err = New().RenderInitial(schema); err == nil {
		t.Fatal("unknown migration-lock registry version rendered")
	}
}

func TestByteaLiteralRoundTripsClosedPhysicalForm(t *testing.T) {
	literal := ir.TypedLiteralIR{Kind: ir.LiteralBytes, Canonical: "AP8"}
	rendered, err := renderLiteral(literal)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != `'\x00ff'::bytea` {
		t.Fatalf("rendered bytea = %s", rendered)
	}
	parsed, err := parseCatalogLiteral(rendered, physical.StorageType{Kind: physical.StoragePostgreSQLBytea})
	if err != nil {
		t.Fatal(err)
	}
	if parsed != literal {
		t.Fatalf("bytea roundtrip = %#v", parsed)
	}
}

func TestCatalogTypeLiteralAndConstraintNormalization(t *testing.T) {
	storage, err := parseCatalogStorage("numeric(18,4)")
	if err != nil || storage.Precision != 18 || storage.Scale != 4 {
		t.Fatalf("numeric = %#v, %v", storage, err)
	}
	storage, err = parseCatalogStorage("timestamp(6) with time zone")
	if err != nil || storage.Kind != physical.StoragePostgreSQLTimestampTZ || storage.Length != 6 {
		t.Fatalf("timestamp = %#v, %v", storage, err)
	}
	literal, err := parseCatalogLiteral(`'{"z":1.00,"a":true}'::jsonb`, physical.StorageType{Kind: physical.StoragePostgreSQLJSONB})
	if err != nil || literal.Canonical != `{"a":true,"z":1}` {
		t.Fatalf("json literal = %#v, %v", literal, err)
	}
	if parseAction("c") != ir.ActionCascade || parseAction("d") != ir.ActionSetDefault || renderAction(ir.ActionSetDefault) != "SET DEFAULT" || parseDeferrable(true, true) != ir.InitiallyDeferred {
		t.Fatal("catalog FK semantics were not normalized")
	}
}

func TestCatalogQuotedLiteralsMayContainCastTokens(t *testing.T) {
	textLiteral, err := parseCatalogLiteral(`'legal::value'::text`, physical.StorageType{Kind: physical.StoragePostgreSQLText})
	if err != nil || textLiteral.Canonical != "legal::value" {
		t.Fatalf("text literal = %#v, %v", textLiteral, err)
	}
	bareText, err := parseCatalogLiteral(`'uncast text'`, physical.StorageType{Kind: physical.StoragePostgreSQLText})
	if err != nil || bareText.Canonical != "uncast text" {
		t.Fatalf("bare text literal = %#v, %v", bareText, err)
	}
	jsonLiteral, err := parseCatalogLiteral(`'{"nested":"x::y"}'::jsonb`, physical.StorageType{Kind: physical.StoragePostgreSQLJSONB})
	if err != nil || jsonLiteral.Canonical != `{"nested":"x::y"}` {
		t.Fatalf("JSON literal = %#v, %v", jsonLiteral, err)
	}
	if _, err = parseCatalogLiteral(`'legal::value'::uuid`, physical.StorageType{Kind: physical.StoragePostgreSQLText}); err == nil {
		t.Fatal("mismatched catalog cast was accepted")
	}
	if _, err = parseCatalogLiteral(`'00000000-0000-0000-0000-000000000000'`, physical.StorageType{Kind: physical.StoragePostgreSQLUUID}); err == nil {
		t.Fatal("missing non-text catalog cast was accepted")
	}
}

func TestCatalogTemporalDefaultsNormalizeToGolemCanonicalUTC(t *testing.T) {
	timeLiteral, err := parseCatalogLiteral(`'12:34:56.12'::time without time zone`, physical.StorageType{Kind: physical.StoragePostgreSQLTime, Length: 3})
	if err != nil || timeLiteral.Canonical != "12:34:56.120" {
		t.Fatalf("time=%#v %v", timeLiteral, err)
	}
	dateTime, err := parseCatalogLiteral(`'2026-08-05 12:00:00.1+02'::timestamp with time zone`, physical.StorageType{Kind: physical.StoragePostgreSQLTimestampTZ, Length: 6})
	if err != nil || dateTime.Canonical != "2026-08-05T10:00:00.100000Z" {
		t.Fatalf("datetime=%#v %v", dateTime, err)
	}
	date, err := parseCatalogLiteral(`'2026-08-05'::date`, physical.StorageType{Kind: physical.StoragePostgreSQLDate})
	if err != nil || date.Canonical != "2026-08-05" {
		t.Fatalf("date=%#v %v", date, err)
	}
}

func TestDriverConfigOwnsDeterministicSessionFormatting(t *testing.T) {
	config, err := driverConfig("postgres://user:password@localhost/database?timezone=Europe%2FParis&datestyle=SQL%2CMDY")
	if err != nil {
		t.Fatal(err)
	}
	if config.RuntimeParams["timezone"] != "UTC" || config.RuntimeParams["datestyle"] != "ISO, YMD" || config.RuntimeParams["intervalstyle"] != "iso_8601" || config.RuntimeParams["standard_conforming_strings"] != "on" {
		t.Fatalf("runtime params=%#v", config.RuntimeParams)
	}
	if _, err = driverConfig("  "); err == nil {
		t.Fatal("blank DSN was accepted")
	}
}

func TestCatalogFixedBaselineFactsFailClosed(t *testing.T) {
	for _, facts := range [][2]string{{"p", "p"}, {"r", "u"}, {"r", "t"}} {
		if err := validateCatalogTableFacts(facts[0], facts[1]); err == nil {
			t.Fatalf("table facts %v accepted", facts)
		}
	}
	for _, facts := range [][5]bool{{false, true, false, false, false}, {true, false, false, false, false}, {true, true, true, false, false}, {true, true, false, true, false}, {true, true, false, false, true}} {
		if err := validateCatalogIndexFacts(facts[0], facts[1], facts[2], facts[3], facts[4]); err == nil {
			t.Fatalf("index facts %v accepted", facts)
		}
	}
	if validateCatalogBehaviorFlags(false, false) != nil || validateCatalogBehaviorFlags(true, false) == nil || validateCatalogBehaviorFlags(false, true) == nil {
		t.Fatal("row-level security catalog flags were not closed")
	}
	allowedBehavior := map[string]bool{"trigger\x00reviewed_trigger": true, "policy\x00reviewed_policy": true, "rule\x00reviewed_rule": true}
	if !catalogBehaviorObjectAllowed("trigger", "reviewed_trigger", allowedBehavior) || catalogBehaviorObjectAllowed("trigger", "reviewed", allowedBehavior) || catalogBehaviorObjectAllowed("rule", "reviewed_policy", allowedBehavior) {
		t.Fatal("behavior-object allowlist was not exact by kind and name")
	}
	if validateIdentityMode("d") != nil || validateIdentityMode("a") == nil || validateGeneratedMode("s") != nil || validateGeneratedMode("v") == nil {
		t.Fatal("identity/generated catalog modes were not closed")
	}
	// PostgreSQL reports connoinherit=true for non-CHECK constraints, including
	// ordinary primary and foreign keys. It denotes unsupported NO INHERIT only
	// for CHECK constraints.
	if validateCatalogConstraintFacts("p", "", false, false, true, true, false) != nil || validateCatalogConstraintFacts("f", "s", false, false, true, true, false) != nil {
		t.Fatal("ordinary PostgreSQL key constraints were mistaken for CHECK NO INHERIT")
	}
	for _, test := range []struct {
		kind, match                                                  string
		deferrable, deferred, validated, noInherit, nullsNotDistinct bool
	}{{"f", "f", false, false, true, false, false}, {"u", "", true, false, true, false, false}, {"u", "", false, false, true, false, true}, {"c", "", false, false, false, false, false}, {"c", "", false, false, true, true, false}, {"x", "", false, false, true, false, false}} {
		if err := validateCatalogConstraintFacts(test.kind, test.match, test.deferrable, test.deferred, test.validated, test.noInherit, test.nullsNotDistinct); err == nil {
			t.Fatalf("constraint facts %#v accepted", test)
		}
	}
}

func TestLiveBlankSchemaRoundTrip(t *testing.T) {
	dsn := os.Getenv("GOLEM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOLEM_TEST_POSTGRES_DSN is not set")
	}
	provider := New()
	db, report, err := provider.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if report.Version.Major < 15 {
		t.Fatalf("server=%#v", report.Version)
	}
	// The opt-in DSN is required to name a disposable test database. The exact
	// managed namespaces are checked blank before mutation and removed afterward.
	_, _ = db.Exec(`DROP SCHEMA IF EXISTS "golem_p1_live" CASCADE`)
	_, _ = db.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	defer db.Exec(`DROP SCHEMA IF EXISTS "golem_p1_live" CASCADE`)
	defer db.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	schema, err := provider.Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "golem_p1_live"})
	if err != nil {
		t.Fatal(err)
	}
	if err = provider.ApplyInitial(context.Background(), db, schema); err != nil {
		t.Fatal(err)
	}
	if err = provider.Verify(context.Background(), db, schema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ALTER TABLE "golem_p1_live"."posts" ALTER COLUMN "metadata" TYPE text USING "metadata"::text`); err != nil {
		t.Fatal(err)
	}
	if err = provider.Verify(context.Background(), db, schema); err == nil {
		t.Fatal("storage-type drift verified successfully")
	}
}

func TestLiveOptimisticConcurrencyIntrospectionRequiresExactCatalogProof(t *testing.T) {
	dsn := os.Getenv("GOLEM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOLEM_TEST_POSTGRES_DSN is not set")
	}
	provider := New()
	database, _, err := provider.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	const namespace = "golem_oc_introspection_live"
	_, _ = database.Exec(`DROP SCHEMA IF EXISTS "golem_oc_introspection_live" CASCADE`)
	_, _ = database.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	defer database.Exec(`DROP SCHEMA IF EXISTS "golem_oc_introspection_live" CASCADE`)
	defer database.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`)

	model := fixtureModel()
	field := ir.FieldID(id(60))
	model.Models[1].Fields = append(model.Models[1].Fields, scalarField(field, "Version", 11, "version", ir.LogicalTypeIR{Kind: ir.TypeInt64}, false))
	model.Models[1].OptimisticConcurrency = &field
	expected, err := provider.Lower(context.Background(), model, physical.LowerOptions{Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ApplyInitial(context.Background(), database, expected); err != nil {
		t.Fatalf("post-apply optimistic-concurrency introspection: %v", err)
	}
	actual, err := provider.Introspect(context.Background(), database, expected)
	if err != nil {
		t.Fatal(err)
	}
	expectedFingerprint, _ := physical.PhysicalFingerprint(expected)
	actualFingerprint, _ := physical.PhysicalFingerprint(actual)
	if actualFingerprint != expectedFingerprint {
		t.Fatalf("live reconciled fingerprint=%s want=%s", actualFingerprint, expectedFingerprint)
	}

	if _, err := database.Exec(`ALTER TABLE "golem_oc_introspection_live"."posts" ALTER COLUMN "version" TYPE integer USING "version"::integer`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Introspect(context.Background(), database, expected); err == nil || !strings.Contains(err.Error(), "optimistic concurrency column") {
		t.Fatalf("drifted live concurrency column error=%v", err)
	}
}

func fixtureModel() ir.ModelIR {
	users, posts := ir.ModelID(id(1)), ir.ModelID(id(2))
	userID, postID, authorID := ir.FieldID(id(11)), ir.FieldID(id(21)), ir.FieldID(id(22))
	statusID := ir.EnumID(id(70))
	precision, scale, timePrecision := uint16(18), uint16(4), uint16(6)
	max := uint32(120)
	listCapability := scalarListJSONCapability
	return ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Schema: ir.SchemaIdentityIR{ID: ir.SchemaID(id(90)), StableName: "social"}, Providers: []ir.Provider{ir.SQLite, ir.PostgreSQL}, Enums: []ir.EnumIR{{ID: statusID, LogicalName: "Status", Values: []ir.EnumValueIR{{ID: ir.EnumValueID(id(71)), WireValue: "draft"}, {ID: ir.EnumValueID(id(72)), WireValue: "live"}}}}, Models: []ir.ModelDeclIR{
		{ID: users, LogicalName: "User", Table: ir.TableBindingIR{PhysicalName: "users"}, Fields: []ir.FieldIR{scalarField(userID, "ID", 0, "id", ir.LogicalTypeIR{Kind: ir.TypeUUID}, false)}, PrimaryKey: &ir.KeyIR{ID: ir.KeyID(id(31)), Kind: ir.KeyPrimary, PhysicalName: "pk_users", Fields: []ir.FieldID{userID}}},
		{ID: posts, LogicalName: "Post", Table: ir.TableBindingIR{PhysicalName: "posts"}, Fields: []ir.FieldIR{
			scalarField(postID, "ID", 0, "id", ir.LogicalTypeIR{Kind: ir.TypeUUID}, false),
			{ID: ir.FieldID(id(99)), Kind: ir.FieldRelation, DeclarationOrder: 1, Relation: &ir.RelationFieldIR{RelationID: ir.RelationID(id(80)), Role: ir.RelationSource, Kind: ir.RelationBelongsTo}},
			scalarField(authorID, "AuthorID", 2, "author_id", ir.LogicalTypeIR{Kind: ir.TypeUUID}, false), scalarField(ir.FieldID(id(23)), "Amount", 3, "amount", ir.LogicalTypeIR{Kind: ir.TypeDecimal, Precision: &precision, Scale: &scale}, false), scalarField(ir.FieldID(id(24)), "Day", 4, "day", ir.LogicalTypeIR{Kind: ir.TypeDate}, false), scalarField(ir.FieldID(id(25)), "Clock", 5, "clock", ir.LogicalTypeIR{Kind: ir.TypeTime, Precision: &timePrecision}, false), scalarField(ir.FieldID(id(26)), "CreatedAt", 6, "created_at", ir.LogicalTypeIR{Kind: ir.TypeDateTime, Precision: &timePrecision}, false), scalarField(ir.FieldID(id(27)), "Metadata", 7, "metadata", ir.LogicalTypeIR{Kind: ir.TypeJSON}, true), scalarField(ir.FieldID(id(28)), "Tags", 8, "tags", ir.LogicalTypeIR{Kind: ir.TypeScalarList, Element: &ir.LogicalTypeIR{Kind: ir.TypeString}, Capability: &listCapability}, false), scalarField(ir.FieldID(id(29)), "Title", 9, "title", ir.LogicalTypeIR{Kind: ir.TypeString, MaxLength: &max}, false), scalarField(ir.FieldID(id(30)), "Status", 10, "status", ir.LogicalTypeIR{Kind: ir.TypeEnum, EnumID: &statusID}, false)}, PrimaryKey: &ir.KeyIR{ID: ir.KeyID(id(32)), Kind: ir.KeyPrimary, PhysicalName: "pk_posts", Fields: []ir.FieldID{postID}}, Indexes: []ir.IndexIR{{ID: ir.IndexID(id(50)), ModelID: posts, PhysicalName: "idx_posts_created", Method: ir.IndexBTree, Provider: ir.ProviderScopePortable, Keys: []ir.IndexKeyIR{{Column: fieldPointer(ir.FieldID(id(26))), Direction: ir.SortAsc, Nulls: ir.NullsDefault}}}}},
	}, Relations: []ir.RelationIR{{ID: ir.RelationID(id(80)), SourceModel: posts, TargetModel: users, SourceField: ir.FieldID(id(99)), Cardinality: ir.RelationOne, LocalFields: []ir.FieldID{authorID}, RemoteFields: []ir.FieldID{userID}, ForeignKey: &ir.ForeignKeyIR{ID: ir.ForeignKeyID(id(40)), PhysicalName: "fk_posts_author", OnUpdate: ir.ActionNoAction, OnDelete: ir.ActionCascade, Match: ir.MatchSimple, Deferrable: ir.NotDeferrable}}}}
}

func TestLowerProjectsOnlyDeclaredOptimisticConcurrencyField(t *testing.T) {
	model := fixtureModel()
	field := ir.FieldID(id(60))
	model.Models[1].Fields = append(model.Models[1].Fields, scalarField(field, "Version", 11, "version", ir.LogicalTypeIR{Kind: ir.TypeInt64}, false))
	model.Models[1].OptimisticConcurrency = &field
	schema, err := New().Lower(context.Background(), model, physical.LowerOptions{Namespace: "public"})
	if err != nil {
		t.Fatal(err)
	}
	table := physicalTable(t, schema, "posts")
	if table.OptimisticConcurrency == nil || *table.OptimisticConcurrency != field {
		t.Fatalf("physical concurrency = %v; want %s", table.OptimisticConcurrency, field)
	}
	column, ok := postgresqlColumn(table, field)
	if !ok || column.Storage != (physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}) || column.Nullable || column.Default.Kind != physical.DefaultNone {
		t.Fatalf("concurrency column = %#v, %v", column, ok)
	}
	model.Models[1].OptimisticConcurrency = nil
	schema, err = New().Lower(context.Background(), model, physical.LowerOptions{Namespace: "public"})
	if err != nil {
		t.Fatal(err)
	}
	if table := physicalTable(t, schema, "posts"); table.OptimisticConcurrency != nil {
		t.Fatalf("undeclared version-like field was inferred: %v", table.OptimisticConcurrency)
	}
}

func TestLowerRejectsIneligibleLogicalOptimisticConcurrencyField(t *testing.T) {
	model := fixtureModel()
	field := ir.FieldID(id(60))
	model.Models[1].Fields = append(model.Models[1].Fields, scalarField(field, "Version", 11, "version", ir.LogicalTypeIR{Kind: ir.TypeInt32}, false))
	model.Models[1].OptimisticConcurrency = &field
	if _, err := New().Lower(context.Background(), model, physical.LowerOptions{Namespace: "public"}); err == nil || !strings.Contains(err.Error(), "logical type int64") {
		t.Fatalf("PostgreSQL lower accepted logical int32 concurrency field: %v", err)
	}
}

func TestPostgreSQLIntrospectionReconcilesConcurrencyOnlyAfterExactCatalogTableProof(t *testing.T) {
	model := fixtureModel()
	field := ir.FieldID(id(60))
	model.Models[1].Fields = append(model.Models[1].Fields, scalarField(field, "Version", 11, "version", ir.LogicalTypeIR{Kind: ir.TypeInt64}, false))
	model.Models[1].OptimisticConcurrency = &field
	expected, err := New().Lower(context.Background(), model, physical.LowerOptions{Namespace: "public"})
	if err != nil {
		t.Fatal(err)
	}

	withoutDeclaration := func(t *testing.T) physical.PhysicalSchema {
		t.Helper()
		actual, err := physical.Normalize(expected)
		if err != nil {
			t.Fatal(err)
		}
		for index := range actual.Tables {
			actual.Tables[index].OptimisticConcurrency = nil
		}
		return actual
	}

	actual := withoutDeclaration(t)
	reconciled, err := reconcileOptimisticConcurrency(expected, actual)
	if err != nil {
		t.Fatal(err)
	}
	if table := physicalTable(t, reconciled, "posts"); table.OptimisticConcurrency == nil || *table.OptimisticConcurrency != field {
		t.Fatalf("reconciled concurrency = %v; want %s", table.OptimisticConcurrency, field)
	}
	expectedFingerprint, _ := physical.PhysicalFingerprint(expected)
	actualFingerprint, _ := physical.PhysicalFingerprint(reconciled)
	if actualFingerprint != expectedFingerprint {
		t.Fatalf("reconciled fingerprint=%s want=%s", actualFingerprint, expectedFingerprint)
	}

	for _, test := range []struct {
		name string
		edit func(*physical.PhysicalTable)
		want string
	}{
		{name: "column storage", edit: func(table *physical.PhysicalTable) {
			column, _ := postgresqlColumn(*table, field)
			for index := range table.Columns {
				if table.Columns[index].ID == column.ID {
					table.Columns[index].Storage = physical.StorageType{Kind: physical.StoragePostgreSQLInteger}
				}
			}
		}, want: "column"},
		{name: "table constraint", edit: func(table *physical.PhysicalTable) {
			table.Indexes = nil
		}, want: "table"},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := withoutDeclaration(t)
			for index := range forged.Tables {
				if forged.Tables[index].Name == "posts" {
					test.edit(&forged.Tables[index])
				}
			}
			if _, err := reconcileOptimisticConcurrency(expected, forged); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("forged catalog proof error=%v", err)
			}
		})
	}
}

func scalarField(identifier ir.FieldID, name string, ordinal uint32, column ir.SQLIdentifier, kind ir.LogicalTypeIR, nullable bool) ir.FieldIR {
	return ir.FieldIR{ID: identifier, GoName: name, LogicalName: name, DeclarationOrder: ordinal, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: column, Type: kind, Nullable: nullable}}
}
func fieldPointer(value ir.FieldID) *ir.FieldID { return &value }
func stringPointer(value string) *string        { return &value }
func physicalTable(t *testing.T, schema physical.PhysicalSchema, name physical.PhysicalName) physical.PhysicalTable {
	t.Helper()
	for _, table := range schema.Tables {
		if table.Name == name {
			return table
		}
	}
	t.Fatalf("table %s absent", name)
	return physical.PhysicalTable{}
}
func assertStorage(t *testing.T, table physical.PhysicalTable, name physical.PhysicalName, kind physical.StorageKind, precision, scale uint16) {
	t.Helper()
	for _, column := range table.Columns {
		if column.Name == name {
			if column.Storage.Kind != kind || column.Storage.Precision != precision || column.Storage.Scale != scale {
				t.Fatalf("column %s storage=%#v", name, column.Storage)
			}
			return
		}
	}
	t.Fatalf("column %s absent", name)
}
func id(value int) string { return fmt.Sprintf("%032x", value) }
