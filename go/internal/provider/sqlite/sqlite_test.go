package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func TestLowerRenderApplyIntrospectRoundTrip(t *testing.T) {
	provider := New()
	model := socialModelIR()
	schema, err := provider.Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.System.Objects) != 4 || !hasSystemObject(schema.System, physical.SystemOutbox) || !hasSystemObject(schema.System, physical.SystemUpsertGuard) {
		t.Fatalf("system objects=%#v; want ledger+lock+outbox+upsert-guard", schema.System.Objects)
	}
	script, err := provider.RenderInitial(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`CREATE TABLE "users"`, `CONSTRAINT "pk_users" PRIMARY KEY`, `json_valid(`, `CREATE INDEX "idx_posts_author"`, `CREATE TABLE "_golem_migrations"`, `CREATE TABLE "_golem_outbox"`, `CREATE INDEX "_golem_outbox_pending"`, `CREATE TABLE "_golem_upsert_guard" ("guard_token" BLOB NOT NULL, PRIMARY KEY ("guard_token")) STRICT`} {
		if !strings.Contains(script.SQL(), fragment) {
			t.Errorf("DDL missing %q:\n%s", fragment, script.SQL())
		}
	}
	if strings.Contains(script.SQL(), "UNQUOTED_SENTINEL") {
		t.Fatal("unexpected raw SQL")
	}

	database, report, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "social.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if !report.ForeignKeys || !report.JSON1 || !report.GeneratedColumns || report.Version.Major < 3 {
		t.Fatalf("capabilities=%#v", report)
	}
	assertTwoConnectionsForeignKeys(t, database)
	if err := provider.ApplyInitial(context.Background(), database, schema); err != nil {
		t.Fatal(err)
	}
	actual, err := provider.Introspect(context.Background(), database, schema)
	if err != nil {
		t.Fatal(err)
	}
	wantFingerprint, _ := physical.PhysicalFingerprint(schema)
	gotFingerprint, _ := physical.PhysicalFingerprint(actual)
	if gotFingerprint != wantFingerprint {
		t.Fatalf("fingerprint=%s want %s", gotFingerprint, wantFingerprint)
	}
	wantSystem, _ := physical.SystemFingerprint(schema.Provider, schema.System)
	gotSystem, _ := physical.SystemFingerprint(actual.Provider, actual.System)
	if gotSystem != wantSystem {
		t.Fatal("system fingerprint mismatch")
	}

	if _, err := database.Exec(`INSERT INTO "users" ("id","email","enabled","created_at") VALUES ('00000000-0000-0000-0000-000000000001','a@example.test',2,0)`); err == nil {
		t.Fatal("synthetic boolean check accepted 2")
	}
	if _, err := database.Exec(`INSERT INTO "users" ("id","email","enabled","created_at") VALUES ('00000000-0000-0000-0000-000000000001','a@example.test',1,X'00')`); err == nil {
		t.Fatal("STRICT table accepted BLOB in DateTime INTEGER storage")
	}
	if _, err := database.Exec(`DROP INDEX "idx_posts_author"`); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(context.Background(), database, schema); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("index drift verification error=%v", err)
	}
	for _, statement := range script.statements {
		if strings.HasPrefix(statement, `CREATE INDEX "idx_posts_author"`) {
			if _, err := database.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := database.Exec(`ALTER TABLE "users" ADD COLUMN "drift" TEXT`); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(context.Background(), database, schema); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("drift verification error=%v", err)
	}
}

func hasSystemObject(system physical.SystemSchema, kind physical.SystemObjectKind) bool {
	for _, object := range system.Objects {
		if object.Kind == kind {
			return true
		}
	}
	return false
}

func TestLowerAndRenderAreDeterministicUnderShuffle(t *testing.T) {
	provider := New()
	first := socialModelIR()
	second := socialModelIR()
	reverse(second.Models)
	reverse(second.Enums)
	reverse(second.Relations)
	for index := range second.Models {
		reverse(second.Models[index].Fields)
		reverse(second.Models[index].Uniques)
		reverse(second.Models[index].Indexes)
	}
	a, err := provider.Lower(context.Background(), first, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := provider.Lower(context.Background(), second, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	af, _ := physical.PhysicalFingerprint(a)
	bf, _ := physical.PhysicalFingerprint(b)
	if af != bf {
		t.Fatalf("shuffle fingerprints differ %s %s", af, bf)
	}
	as, _ := provider.RenderInitial(a)
	bs, _ := provider.RenderInitial(b)
	if as.SQL() != bs.SQL() {
		t.Fatalf("shuffle DDL differs")
	}
}

func TestSystemRegistryIDsDoNotDependOnApplicationSchema(t *testing.T) {
	first := socialModelIR()
	second := socialModelIR()
	second.Schema.ID = ir.SchemaID(id(98))
	second.Schema.StableName = "another"
	a, err := New().Lower(context.Background(), first, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := New().Lower(context.Background(), second, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for index := range a.System.Objects {
		if a.System.Objects[index].ID != b.System.Objects[index].ID {
			t.Fatalf("system ID depends on application schema: %s != %s", a.System.Objects[index].ID, b.System.Objects[index].ID)
		}
	}
}

func TestLowerPreservesTemporalAndListSemantics(t *testing.T) {
	schema, err := New().Lower(context.Background(), socialModelIR(), physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	users := findTable(schema, "users")
	if findColumn(users, "event_time").Storage.Kind != physical.StorageSQLiteText {
		t.Fatal("Time must use canonical TEXT")
	}
	identities := map[string]bool{}
	for _, check := range users.Checks {
		if check.Expression.Symbol != nil {
			identities[check.Expression.Symbol.Identity] = true
		}
	}
	for _, identity := range []string{"sqlite.check.time-canonical", "sqlite.check.datetime-precision", "sqlite.check.json-array"} {
		if !identities[identity] {
			t.Errorf("missing %s", identity)
		}
	}
	bad := socialModelIR()
	list := findLogicalField(&bad, "User", "Scores")
	list.Scalar.Type.Element = &ir.LogicalTypeIR{Kind: ir.TypeBytes}
	if _, err := New().Lower(context.Background(), bad, physical.LowerOptions{}); err == nil || !strings.Contains(err.Error(), "scalar-list element") {
		t.Fatalf("invalid list error=%v", err)
	}
}

func TestGeneratedExpressionAndPartialExpressionIndexRender(t *testing.T) {
	model := socialModelIR()
	users := &model.Models[0]
	email := users.Fields[1]
	enabled := users.Fields[2]
	generatedID := ir.FieldID(id(19))
	indexID := ir.IndexID(id(46))
	emailExpr := schemaField(email.ID, email.Scalar.Type, email.Scalar.Nullable)
	lowerExpr := ir.SchemaExprIR{Kind: ir.SchemaExprFunction, ResultType: email.Scalar.Type, Symbol: &ir.SchemaSymbolRef{Identity: "golem.schema.function.lower.v1", Kind: ir.SchemaSymbolFunction, Name: "lower", Version: 1, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true}, Operands: []ir.SchemaExprIR{emailExpr}, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true, ReferencedFields: []ir.FieldID{email.ID}}
	users.Fields = append(users.Fields, ir.FieldIR{ID: generatedID, GoName: "SearchEmail", DeclarationOrder: uint32(len(users.Fields)), Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: "search_email", Type: email.Scalar.Type, DatabaseReadOnly: true, Generation: &ir.GeneratedColumnIR{Expr: lowerExpr, Storage: ir.GeneratedStored, Provider: ir.ProviderScopePortable}}})
	enabledExpr := schemaField(enabled.ID, enabled.Scalar.Type, enabled.Scalar.Nullable)
	literal := ir.TypedLiteralIR{Kind: ir.LiteralBool, Canonical: "true"}
	literalExpr := ir.SchemaExprIR{Kind: ir.SchemaExprLiteral, ResultType: enabled.Scalar.Type, Literal: &literal, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true}
	predicate := ir.SchemaPredicateIR{Kind: ir.SchemaPredicateOperator, ResultType: ir.LogicalTypeIR{Kind: ir.TypeBool}, Symbol: &ir.SchemaSymbolRef{Identity: "golem.schema.predicate.equal.v1", Kind: ir.SchemaSymbolOperator, Name: "eq", Version: 1, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true}, ExpressionOperands: []ir.SchemaExprIR{enabledExpr, literalExpr}, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true, ReferencedFields: []ir.FieldID{enabled.ID}}
	users.Indexes = append(users.Indexes, ir.IndexIR{ID: indexID, ModelID: users.ID, PhysicalName: "idx_users_lower_email", Method: ir.IndexBTree, Keys: []ir.IndexKeyIR{{Expr: &lowerExpr, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, Predicate: &predicate, Provider: ir.ProviderScopePortable})
	schema, err := New().Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	script, err := New().RenderInitial(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"search_email" TEXT NOT NULL GENERATED ALWAYS AS (lower("email")) STORED`, `CREATE INDEX "idx_users_lower_email"`, `WHERE ("enabled" = 1)`} {
		if !strings.Contains(script.SQL(), fragment) {
			t.Errorf("missing %q:\n%s", fragment, script.SQL())
		}
	}
	database, _, err := New().Open(context.Background(), filepath.Join(t.TempDir(), "generated.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := New().ApplyInitial(context.Background(), database, schema); err != nil {
		t.Fatal(err)
	}
}

func TestParserNormalizesFormattingAndRejectsNonGrammar(t *testing.T) {
	a, err := parseDDL(`CREATE TABLE "items" ("id" INTEGER NOT NULL, CONSTRAINT "pk_items" PRIMARY KEY ("id"))`)
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseDDL("create table \"items\"(\"id\" integer not null,constraint \"pk_items\" primary key(\"id\"))")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("formatting changed AST:\n%#v\n%#v", a, b)
	}
	if _, err := parseDDL(`DROP TABLE "items"`); err == nil {
		t.Fatal("DROP accepted by closed parser")
	}
	if _, err := parseDDL(`CREATE TABLE "items ("id" INTEGER)`); err == nil {
		t.Fatal("unterminated identifier accepted")
	}
	if _, err := parseDDL(`CREATE TABLE "items" ("value" TEXT DEFAULT 'oops)`); err == nil {
		t.Fatal("unterminated literal accepted")
	}
}

func TestOpenRejectsCallerPragmaOverrides(t *testing.T) {
	if _, _, err := New().Open(context.Background(), "file:test.db?_pragma=foreign_keys(0)"); err == nil {
		t.Fatal("caller foreign_keys override accepted")
	}
	if _, _, err := New().Open(context.Background(), ":memory:"); err == nil {
		t.Fatal("private :memory: accepted with multi-connection pool")
	}
}

func TestSQLiteForeignKeySetDefaultAndMatchSimple(t *testing.T) {
	model := socialModelIR()
	model.Relations[0].ForeignKey.OnDelete = ir.ActionSetDefault
	defaultAuthor := ir.TypedLiteralIR{Kind: ir.LiteralUUID, Canonical: "00000000-0000-0000-0000-000000000000"}
	for modelIndex := range model.Models {
		for fieldIndex := range model.Models[modelIndex].Fields {
			field := &model.Models[modelIndex].Fields[fieldIndex]
			if field.ID == ir.FieldID(id(23)) {
				field.Scalar.Default = &ir.DefaultIR{Kind: ir.DefaultLiteral, Producer: ir.ProducerDatabase, Literal: &defaultAuthor}
			}
		}
	}
	schema, err := New().Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	script, err := New().RenderInitial(schema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script.SQL(), "ON DELETE SET DEFAULT") {
		t.Fatalf("SET DEFAULT action not rendered:\n%s", script.SQL())
	}
	database, _, err := New().Open(context.Background(), filepath.Join(t.TempDir(), "set-default.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := New().ApplyInitial(context.Background(), database, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := New().Introspect(context.Background(), database, schema); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteLowerRejectsNonSimpleForeignKeyMatch(t *testing.T) {
	model := socialModelIR()
	model.Relations[0].ForeignKey.Match = "full"
	_, err := New().Lower(context.Background(), model, physical.LowerOptions{})
	if err == nil || !strings.Contains(err.Error(), "requires MATCH SIMPLE") {
		t.Fatalf("got error %v, want MATCH SIMPLE refusal", err)
	}
}

func socialModelIR() ir.ModelIR {
	usersID, postsID := ir.ModelID(id(1)), ir.ModelID(id(2))
	statusID := ir.EnumID(id(3))
	scale, precision := uint16(2), uint16(10)
	timePrecision, dateTimePrecision := uint16(3), uint16(6)
	maxEmail, maxBytes := uint32(120), uint32(100)
	schemaWitness := "example.test/Profile"
	listCapability := ir.CapabilityID("scalar-list:json-array:v1")
	userID, emailID, enabledID, scoresID, metaID, createdID, timeID := ir.FieldID(id(11)), ir.FieldID(id(12)), ir.FieldID(id(13)), ir.FieldID(id(14)), ir.FieldID(id(15)), ir.FieldID(id(16)), ir.FieldID(id(17))
	tenantID, postID, authorID, statusFieldID, priceID, dateID, payloadID, smallID := ir.FieldID(id(21)), ir.FieldID(id(22)), ir.FieldID(id(23)), ir.FieldID(id(24)), ir.FieldID(id(25)), ir.FieldID(id(26)), ir.FieldID(id(27)), ir.FieldID(id(28))
	trueLiteral := ir.TypedLiteralIR{Kind: ir.LiteralBool, Canonical: "true"}
	priceLiteral := ir.TypedLiteralIR{Kind: ir.LiteralDecimal, Canonical: "12.34"}
	models := []ir.ModelDeclIR{
		{ID: usersID, LogicalName: "User", Table: ir.TableBindingIR{PhysicalName: "users"}, Fields: []ir.FieldIR{
			scalar(userID, "ID", "id", 0, ir.LogicalTypeIR{Kind: ir.TypeUUID}, false, nil), scalar(emailID, "Email", "email", 1, ir.LogicalTypeIR{Kind: ir.TypeString, MaxLength: &maxEmail}, false, nil), scalar(enabledID, "Enabled", "enabled", 2, ir.LogicalTypeIR{Kind: ir.TypeBool}, false, &ir.DefaultIR{Kind: ir.DefaultLiteral, Producer: ir.ProducerDatabase, Literal: &trueLiteral}),
			scalar(scoresID, "Scores", "scores", 3, ir.LogicalTypeIR{Kind: ir.TypeScalarList, Element: &ir.LogicalTypeIR{Kind: ir.TypeInt64}, Capability: &listCapability}, true, nil), scalar(metaID, "Meta", "meta", 4, ir.LogicalTypeIR{Kind: ir.TypeJSON, JSONSchemaID: &schemaWitness}, true, nil), scalar(createdID, "CreatedAt", "created_at", 5, ir.LogicalTypeIR{Kind: ir.TypeDateTime, Precision: &dateTimePrecision}, false, &ir.DefaultIR{Kind: ir.DefaultNow, Producer: ir.ProducerApplication}), scalar(timeID, "EventTime", "event_time", 6, ir.LogicalTypeIR{Kind: ir.TypeTime, Precision: &timePrecision}, true, nil),
		}, PrimaryKey: &ir.KeyIR{ID: ir.KeyID(id(31)), Kind: ir.KeyPrimary, PhysicalName: "pk_users", Fields: []ir.FieldID{userID}}, Uniques: []ir.KeyIR{{ID: ir.KeyID(id(32)), Kind: ir.KeyUnique, PhysicalName: "uq_users_email", Fields: []ir.FieldID{emailID}}}},
		{ID: postsID, LogicalName: "Post", Table: ir.TableBindingIR{PhysicalName: "posts"}, Fields: []ir.FieldIR{
			scalar(tenantID, "TenantID", "tenant_id", 0, ir.LogicalTypeIR{Kind: ir.TypeString}, false, nil), scalar(postID, "ID", "id", 1, ir.LogicalTypeIR{Kind: ir.TypeString}, false, nil), scalar(authorID, "AuthorID", "author_id", 2, ir.LogicalTypeIR{Kind: ir.TypeUUID}, false, nil), scalar(statusFieldID, "Status", "status", 3, ir.LogicalTypeIR{Kind: ir.TypeEnum, EnumID: &statusID}, false, nil), scalar(priceID, "Price", "price", 4, ir.LogicalTypeIR{Kind: ir.TypeDecimal, Precision: &precision, Scale: &scale}, false, &ir.DefaultIR{Kind: ir.DefaultLiteral, Producer: ir.ProducerDatabase, Literal: &priceLiteral}), scalar(dateID, "EventDate", "event_date", 5, ir.LogicalTypeIR{Kind: ir.TypeDate}, true, nil), scalar(payloadID, "Payload", "payload", 6, ir.LogicalTypeIR{Kind: ir.TypeBytes, MaxLength: &maxBytes}, true, nil), scalar(smallID, "Small", "small", 7, ir.LogicalTypeIR{Kind: ir.TypeInt16}, true, nil),
		}, PrimaryKey: &ir.KeyIR{ID: ir.KeyID(id(33)), Kind: ir.KeyPrimary, PhysicalName: "pk_posts", Fields: []ir.FieldID{tenantID, postID}}, Indexes: []ir.IndexIR{{ID: ir.IndexID(id(41)), ModelID: postsID, PhysicalName: "idx_posts_author", Method: ir.IndexBTree, Keys: []ir.IndexKeyIR{{Column: &authorID, Direction: ir.SortDesc, Nulls: ir.NullsDefault}}, Provider: ir.ProviderScopePortable}}},
	}
	fkID := ir.ForeignKeyID(id(51))
	relation := ir.RelationIR{ID: ir.RelationID(id(50)), SourceModel: postsID, TargetModel: usersID, SourceField: ir.FieldID(id(29)), LocalFields: []ir.FieldID{authorID}, RemoteFields: []ir.FieldID{userID}, ForeignKey: &ir.ForeignKeyIR{ID: fkID, PhysicalName: "fk_posts_author", OnUpdate: ir.ActionNoAction, OnDelete: ir.ActionCascade, Match: ir.MatchSimple, Deferrable: ir.NotDeferrable}}
	return ir.ModelIR{FormatVersion: ir.ModelFormatVersion, Schema: ir.SchemaIdentityIR{ID: ir.SchemaID(id(99)), StableName: "social"}, Providers: []ir.Provider{ir.SQLite, ir.PostgreSQL}, Enums: []ir.EnumIR{{ID: statusID, LogicalName: "Status", Values: []ir.EnumValueIR{{ID: ir.EnumValueID(id(61)), WireValue: "draft"}, {ID: ir.EnumValueID(id(62)), WireValue: "published"}}}}, Models: models, Relations: []ir.RelationIR{relation}}
}

func scalar(fieldID ir.FieldID, goName, column string, order uint32, typ ir.LogicalTypeIR, nullable bool, defaultValue *ir.DefaultIR) ir.FieldIR {
	return ir.FieldIR{ID: fieldID, GoName: goName, DeclarationOrder: order, Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: ir.SQLIdentifier(column), Type: typ, Nullable: nullable, Default: defaultValue}}
}
func schemaField(fieldID ir.FieldID, typ ir.LogicalTypeIR, nullable bool) ir.SchemaExprIR {
	return ir.SchemaExprIR{Kind: ir.SchemaExprField, ResultType: typ, Nullable: nullable, Field: &fieldID, Provider: ir.ProviderScopePortable, Volatility: ir.SchemaVolatilityImmutable, Deterministic: true, ReferencedFields: []ir.FieldID{fieldID}}
}
func id(value int) string { return fmt.Sprintf("%032x", value) }
func reverse[T any](values []T) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
func findTable(schema physical.PhysicalSchema, name physical.PhysicalName) physical.PhysicalTable {
	for _, table := range schema.Tables {
		if table.Name == name {
			return table
		}
	}
	panic("table")
}
func findColumn(table physical.PhysicalTable, name physical.PhysicalName) physical.PhysicalColumn {
	for _, column := range table.Columns {
		if column.Name == name {
			return column
		}
	}
	panic("column")
}
func findLogicalField(model *ir.ModelIR, logicalModel, goName string) *ir.FieldIR {
	for mi := range model.Models {
		if model.Models[mi].LogicalName == logicalModel {
			for fi := range model.Models[mi].Fields {
				if model.Models[mi].Fields[fi].GoName == goName {
					return &model.Models[mi].Fields[fi]
				}
			}
		}
	}
	panic("field")
}

type connectionGetter interface {
	GetContext(context.Context, any, string, ...any) error
	Close() error
}

func assertTwoConnectionsForeignKeys(t *testing.T, database interface {
	Connx(context.Context) (*sqlx.Conn, error)
}) {
	t.Helper()
	ctx := context.Background()
	a, err := database.Connx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := database.Connx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, connection := range []*sqlx.Conn{a, b} {
		var enabled int
		if err := connection.GetContext(ctx, &enabled, "PRAGMA foreign_keys"); err != nil || enabled != 1 {
			t.Fatalf("foreign_keys=%d err=%v", enabled, err)
		}
	}
}
