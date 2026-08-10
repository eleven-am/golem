package physical

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestNormalizeCanonicalizesSetsAndPreservesOrderedComponents(t *testing.T) {
	first := sqliteSocialSchema()
	second := sqliteSocialSchema()
	reverse(second.Provider.Capabilities)
	reverse(second.Tables)
	for tableIndex := range second.Tables {
		reverse(second.Tables[tableIndex].Columns)
		reverse(second.Tables[tableIndex].Uniques)
		reverse(second.Tables[tableIndex].ForeignKeys)
		reverse(second.Tables[tableIndex].Checks)
		reverse(second.Tables[tableIndex].Indexes)
		reverse(second.Tables[tableIndex].RequiredCapabilities)
		for columnIndex := range second.Tables[tableIndex].Columns {
			reverse(second.Tables[tableIndex].Columns[columnIndex].RequiredCapabilities)
		}
	}
	reverse(second.System.Objects)
	reverse(second.Unmanaged)

	firstBytes, err := CanonicalEncode(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := CanonicalEncode(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("canonical encoding changed under set shuffle")
	}
	firstFingerprint, _ := PhysicalFingerprint(first)
	secondFingerprint, _ := PhysicalFingerprint(second)
	if firstFingerprint != secondFingerprint {
		t.Fatalf("fingerprint changed under set shuffle: %s != %s", firstFingerprint, secondFingerprint)
	}

	changed := sqliteSocialSchema()
	posts := tableByName(&changed, "posts")
	posts.PrimaryKey.Columns[0], posts.PrimaryKey.Columns[1] = posts.PrimaryKey.Columns[1], posts.PrimaryKey.Columns[0]
	changedFingerprint, err := PhysicalFingerprint(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedFingerprint == firstFingerprint {
		t.Fatalf("ordered composite key change did not affect fingerprint")
	}
}

func TestValidateRejectsIdentifiersStorageAndConstraintViolations(t *testing.T) {
	tests := []struct {
		name     string
		edit     func(*PhysicalSchema)
		code     ValidationCode
		contains string
	}{
		{"uppercase identifier", func(schema *PhysicalSchema) { schema.Tables[0].Name = "Users" }, CodeIdentifier, "Users"},
		{"reserved identifier", func(schema *PhysicalSchema) { schema.Tables[0].Name = "_golem_users" }, CodeIdentifier, "_golem_users"},
		{"overlong identifier", func(schema *PhysicalSchema) { schema.Tables[0].Name = PhysicalName("a" + strings.Repeat("b", 63)) }, CodeIdentifier, "invalid physical identifier"},
		{"noncanonical ID", func(schema *PhysicalSchema) { schema.Tables[0].Columns[0].ID = "field-id" }, CodeStableID, "128-bit"},
		{"wrong storage provider", func(schema *PhysicalSchema) { schema.Tables[0].Columns[0].Storage.Kind = StoragePostgreSQLUUID }, CodeStorage, "invalid for provider sqlite"},
		{"duplicate ordinal", func(schema *PhysicalSchema) {
			schema.Tables[0].Columns[1].Ordinal = schema.Tables[0].Columns[0].Ordinal
		}, CodeDuplicate, "ordinal"},
		{"nullable primary key", func(schema *PhysicalSchema) { schema.Tables[0].Columns[0].Nullable = true }, CodeConstraint, "primary-key"},
		{"foreign key arity", func(schema *PhysicalSchema) { tableByName(schema, "posts").ForeignKeys[0].ReferencedColumns = nil }, CodeConstraint, "equal non-zero arity"},
		{"foreign key type mismatch", func(schema *PhysicalSchema) {
			tableByName(schema, "posts").Columns[1].Storage.Kind = StorageSQLiteInteger
		}, CodeConstraint, "storage mismatch"},
		{"set null required", func(schema *PhysicalSchema) { tableByName(schema, "posts").ForeignKeys[0].OnDelete = ir.ActionSetNull }, CodeConstraint, "requires nullable"},
		{"set default missing default", func(schema *PhysicalSchema) {
			tableByName(schema, "posts").ForeignKeys[0].OnDelete = ir.ActionSetDefault
		}, CodeConstraint, "requires a database/provider default"},
		{"deferred restrict", func(schema *PhysicalSchema) {
			foreign := &tableByName(schema, "posts").ForeignKeys[0]
			foreign.Deferrable, foreign.OnDelete = ir.InitiallyDeferred, ir.ActionRestrict
		}, CodeConstraint, "deferred foreign key"},
		{"generated and default", func(schema *PhysicalSchema) {
			column := &tableByName(schema, "posts").Columns[2]
			column.Generated = &GeneratedExpression{Kind: GeneratedStored, Expression: columnExpr(column.ID, column.Storage)}
		}, CodeConstraint, "generated column cannot"},
		{"invalid index shape", func(schema *PhysicalSchema) {
			index := &tableByName(schema, "posts").Indexes[0]
			expression := columnExpr(*index.Keys[0].Column, StorageType{Kind: StorageSQLiteText})
			index.Keys[0].Expression = &expression
		}, CodeConstraint, "exactly one"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := sqliteSocialSchema()
			test.edit(&schema)
			err := Validate(schema)
			if err == nil || !IsValidationCode(err, test.code) || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v; want code %s containing %q", err, test.code, test.contains)
			}
		})
	}
}

func TestValidateAcceptsSetDefaultWithLocalDefault(t *testing.T) {
	schema := sqliteSocialSchema()
	posts := tableByName(&schema, "posts")
	posts.Columns[1].Default = PhysicalDefault{Kind: DefaultLiteral, Literal: &ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: "00000000000000000000000000000000"}}
	posts.ForeignKeys[0].OnDelete = ir.ActionSetDefault
	if err := Validate(schema); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityRejectionNamesProviderAndStableOwner(t *testing.T) {
	schema := sqliteSocialSchema()
	schema.Provider.Capabilities = schema.Provider.Capabilities[:1]
	err := Validate(schema)
	if err == nil {
		t.Fatal("expected missing JSON1 capability")
	}
	var diagnostics ValidationErrors
	if !errors.As(err, &diagnostics) {
		t.Fatalf("error type = %T; want ValidationErrors", err)
	}
	postsID := ir.ModelID(id(2))
	bodyID := ir.FieldID(id(23))
	found := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == CodeCapabilityMissing && diagnostic.Capability == CapabilitySQLiteJSON1 && diagnostic.Provider == ir.SQLite && diagnostic.Owner.ModelID == postsID && diagnostic.Owner.FieldID == bodyID {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing-capability diagnostic lacks stable model/field owner: %#v", diagnostics)
	}
}

func TestExpressionValidationIsClosedAndProviderScoped(t *testing.T) {
	tests := []struct {
		name     string
		edit     func(*Expression)
		contains string
	}{
		{"missing symbol", func(expression *Expression) { expression.Symbol = nil }, "semantic symbol"},
		{"wrong symbol kind", func(expression *Expression) { expression.Symbol.Kind = ir.SchemaSymbolFunction }, "want \"operator\""},
		{"postgresql symbol", func(expression *Expression) { expression.Symbol.Provider = ir.ProviderScopePostgreSQL }, "non-PostgreSQL"},
		{"unknown expression kind", func(expression *Expression) { expression.Kind = "raw_sql" }, "invalid expression kind"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := sqliteSocialSchema()
			expression := &tableByName(&schema, "posts").Checks[0].Expression
			test.edit(expression)
			err := Validate(schema)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v; want %q", err, test.contains)
			}
		})
	}
}

func TestPhysicalAndSystemFingerprintsHaveSeparateDomains(t *testing.T) {
	schema := sqliteSocialSchema()
	physicalBefore, err := PhysicalFingerprint(schema)
	if err != nil {
		t.Fatal(err)
	}
	systemBefore, err := SystemFingerprint(schema.Provider, schema.System)
	if err != nil {
		t.Fatal(err)
	}

	schema.System.Objects[0].Version++
	physicalAfter, err := PhysicalFingerprint(schema)
	if err != nil {
		t.Fatal(err)
	}
	systemAfter, err := SystemFingerprint(schema.Provider, schema.System)
	if err != nil {
		t.Fatal(err)
	}
	if physicalBefore != physicalAfter {
		t.Fatal("system-only change altered application physical fingerprint")
	}
	if systemBefore == systemAfter {
		t.Fatal("system-only change did not alter system fingerprint")
	}
	if physicalBefore == systemBefore {
		t.Fatal("application and system domains collided")
	}

	changed := sqliteSocialSchema()
	tableByName(&changed, "posts").Columns[2].Nullable = true
	physicalChanged, err := PhysicalFingerprint(changed)
	if err != nil {
		t.Fatal(err)
	}
	if physicalChanged == physicalBefore {
		t.Fatal("physical column change did not alter fingerprint")
	}
	parsed, err := ParseDigest(physicalBefore.String())
	if err != nil || parsed != physicalBefore {
		t.Fatalf("digest round trip = %s, %v", parsed, err)
	}
	if _, err := ParseDigest(strings.ToUpper(physicalBefore.String())); err == nil {
		t.Fatal("uppercase digest was not rejected")
	}
}

func TestOutboxDeliveryV1RegistryValidationCanonicalizationAndFingerprint(t *testing.T) {
	base := sqliteSocialSchema()
	base.System.Objects = append(base.System.Objects, OutboxSystemObjectV1(), OutboxDeliverySystemObjectV1())
	normalized, err := Normalize(base)
	if err != nil {
		t.Fatal(err)
	}
	first, err := SystemFingerprint(normalized.Provider, normalized.System)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := base
	shuffled.System.Objects = append([]SystemObject(nil), base.System.Objects...)
	reverse(shuffled.System.Objects)
	second, err := SystemFingerprint(shuffled.Provider, shuffled.System)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("system fingerprint changed under registry shuffle: %s != %s", first, second)
	}
	withoutOutbox := sqliteSocialSchema()
	withoutOutbox.System.Objects = append(withoutOutbox.System.Objects, OutboxDeliverySystemObjectV1())
	if _, err := Normalize(withoutOutbox); err == nil {
		t.Fatal("outbox delivery without immutable outbox was accepted")
	}
	forged := base
	forged.System.Objects = append([]SystemObject(nil), base.System.Objects...)
	forged.System.Objects[len(forged.System.Objects)-1].Name = "_golem_delivery_forged"
	if _, err := Normalize(forged); err == nil {
		t.Fatal("forged outbox delivery registry entry was accepted")
	}
}

func TestCanonicalFragmentIsStableAndRejectsUnsupportedKinds(t *testing.T) {
	schema, err := Normalize(sqliteSocialSchema())
	if err != nil {
		t.Fatal(err)
	}
	first, err := CanonicalFragment(schema.Tables[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalFragment(schema.Tables[0])
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("fragment encoding is not stable: %v", err)
	}
	if _, err := CanonicalFragment(map[string]string{"raw": "ddl"}); err == nil {
		t.Fatal("map fragment must be rejected")
	}
	if _, err := CanonicalFragment(nil); err == nil {
		t.Fatal("nil fragment must be rejected")
	}
}

func TestProviderManifestFloorsAndFactsAreValidated(t *testing.T) {
	sqlite := SQLiteManifest()
	if sqlite.Driver != (DriverIdentity{Module: "github.com/ncruces/go-sqlite3", Adapter: "sqlx"}) || sqlite.MinimumVersion != (Version{Major: 3, Minor: 38}) {
		t.Fatalf("SQLite manifest = %#v", sqlite)
	}
	if len(sqlite.Capabilities) != 1 || sqlite.Capabilities[0].ID != CapabilitySQLiteForeignKeys || sqlite.Capabilities[0].Verification != VerificationRuntimeProbe {
		t.Fatalf("SQLite foreign-key probe fact missing: %#v", sqlite.Capabilities)
	}
	postgresql := PostgreSQLManifest()
	if postgresql.Driver.Module != "github.com/jackc/pgx/v5/stdlib" || postgresql.MinimumVersion != (Version{Major: 15}) {
		t.Fatalf("PostgreSQL manifest = %#v", postgresql)
	}

	schema := sqliteSocialSchema()
	schema.Provider.Driver = DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
	if err := Validate(schema); err != nil {
		t.Fatalf("historical reviewed SQLite driver identity was rejected: %v", err)
	}
	schema = sqliteSocialSchema()
	schema.Provider.Driver = DriverIdentity{Module: "example.com/unknown", Adapter: "sqlx"}
	if err := Validate(schema); err == nil || !IsValidationCode(err, CodeProvider) {
		t.Fatalf("unknown SQLite driver error = %v", err)
	}
	schema = sqliteSocialSchema()
	schema.Provider.MinimumVersion = Version{Major: 3, Minor: 37}
	if err := Validate(schema); err == nil || !IsValidationCode(err, CodeProvider) {
		t.Fatalf("old SQLite floor error = %v", err)
	}
	schema = sqliteSocialSchema()
	schema.Provider.Capabilities[0].Verification = "assumed"
	if err := Validate(schema); err == nil || !IsValidationCode(err, CodeCapabilityManifest) {
		t.Fatalf("optimistic capability error = %v", err)
	}
}

func sqliteSocialSchema() PhysicalSchema {
	usersID, postsID := ir.ModelID(id(1)), ir.ModelID(id(2))
	userID, emailID, activeID := ir.FieldID(id(11)), ir.FieldID(id(12)), ir.FieldID(id(13))
	postID, authorID, bodyID, tenantID := ir.FieldID(id(21)), ir.FieldID(id(22)), ir.FieldID(id(23)), ir.FieldID(id(24))
	jsonCapability := CapabilityFact{ID: CapabilitySQLiteJSON1, Version: 1, Verification: VerificationRuntimeProbe}
	bodyOwner := ObjectRef{Kind: ir.ObjectField, ModelID: postsID, FieldID: bodyID}
	foreignOwner := ObjectRef{Kind: ir.ObjectForeignKey, ModelID: postsID, ObjectID: ir.ObjectID(id(42))}
	checkOwner := ObjectRef{Kind: ir.ObjectCheck, ModelID: postsID, ObjectID: ir.ObjectID(id(43))}
	boolLiteral := ir.TypedLiteralIR{Kind: ir.LiteralBool, Canonical: "true"}
	jsonCheck := Expression{
		Kind:     ExpressionOperator,
		Type:     StorageType{Kind: StorageSQLiteInteger},
		Symbol:   &SemanticSymbol{Identity: "sqlite.json_valid", Kind: ir.SchemaSymbolOperator, Version: 1, Provider: ir.ProviderScopeSQLite},
		Operands: []Expression{columnExpr(bodyID, StorageType{Kind: StorageSQLiteText})},
	}
	return PhysicalSchema{
		Version:          SchemaFormatVersion,
		CanonicalVersion: CanonicalFormatVersion,
		Provider:         SQLiteManifest(jsonCapability),
		Namespace:        Namespace{Name: "main"},
		Tables: []PhysicalTable{
			{
				ID: postsID, Name: "posts",
				Columns: []PhysicalColumn{
					{ID: postID, Name: "id", Ordinal: 0, Storage: StorageType{Kind: StorageSQLiteText}, Default: PhysicalDefault{Kind: DefaultNone}},
					{ID: authorID, Name: "author_id", Ordinal: 1, Storage: StorageType{Kind: StorageSQLiteText}, Default: PhysicalDefault{Kind: DefaultNone}},
					{ID: bodyID, Name: "body", Ordinal: 2, Storage: StorageType{Kind: StorageSQLiteText}, Default: PhysicalDefault{Kind: DefaultLiteral, Literal: &ir.TypedLiteralIR{Kind: ir.LiteralJSON, Canonical: "{}"}}, RequiredCapabilities: []CapabilityRequirement{{Capability: CapabilitySQLiteJSON1, Owner: bodyOwner}}},
					{ID: tenantID, Name: "tenant_id", Ordinal: 3, Storage: StorageType{Kind: StorageSQLiteText}, Default: PhysicalDefault{Kind: DefaultNone}},
				},
				PrimaryKey: &PhysicalKey{ID: ir.KeyID(id(31)), Name: "pk_posts", Columns: []ir.FieldID{tenantID, postID}},
				ForeignKeys: []PhysicalForeignKey{{
					ID: ir.ForeignKeyID(id(42)), Name: "fk_posts_author", Columns: []ir.FieldID{authorID}, ReferencedTable: usersID, ReferencedColumns: []ir.FieldID{userID},
					OnUpdate: ir.ActionNoAction, OnDelete: ir.ActionCascade, Deferrable: ir.NotDeferrable,
					RequiredCapabilities: []CapabilityRequirement{{Capability: CapabilitySQLiteForeignKeys, Owner: foreignOwner}},
				}},
				Checks:  []PhysicalCheck{{ID: ir.CheckID(id(43)), Name: "ck_posts_body_json", Expression: jsonCheck, RequiredCapabilities: []CapabilityRequirement{{Capability: CapabilitySQLiteJSON1, Owner: checkOwner}}}},
				Indexes: []PhysicalIndex{{ID: ir.IndexID(id(44)), Name: "idx_posts_author", Method: IndexBTree, Keys: []IndexKey{{Column: fieldIDPointer(authorID), Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: IndexTransactional}},
			},
			{
				ID: usersID, Name: "users",
				Columns: []PhysicalColumn{
					{ID: userID, Name: "id", Ordinal: 0, Storage: StorageType{Kind: StorageSQLiteText}, Default: PhysicalDefault{Kind: DefaultNone}},
					{ID: emailID, Name: "email", Ordinal: 1, Storage: StorageType{Kind: StorageSQLiteText}, Default: PhysicalDefault{Kind: DefaultNone}},
					{ID: activeID, Name: "active", Ordinal: 2, Storage: StorageType{Kind: StorageSQLiteInteger}, Default: PhysicalDefault{Kind: DefaultLiteral, Literal: &boolLiteral}},
				},
				PrimaryKey: &PhysicalKey{ID: ir.KeyID(id(32)), Name: "pk_users", Columns: []ir.FieldID{userID}},
				Uniques:    []PhysicalKey{{ID: ir.KeyID(id(33)), Name: "uq_users_email", Columns: []ir.FieldID{emailID}}},
			},
		},
		System:    SystemSchema{Version: 1, Namespace: Namespace{Name: "main"}, Objects: []SystemObject{{ID: ir.ObjectID(id(90)), Kind: SystemMigrationLedger, Version: 1, Name: "_golem_migrations"}}},
		Unmanaged: []UnmanagedObject{{Kind: "view", Name: "reporting_users"}},
	}
}

func columnExpr(fieldID ir.FieldID, storage StorageType) Expression {
	return Expression{Kind: ExpressionColumn, Type: storage, Column: fieldIDPointer(fieldID)}
}

func fieldIDPointer(value ir.FieldID) *ir.FieldID { return &value }

func tableByName(schema *PhysicalSchema, name PhysicalName) *PhysicalTable {
	for index := range schema.Tables {
		if schema.Tables[index].Name == name {
			return &schema.Tables[index]
		}
	}
	panic("table not found: " + string(name))
}

func id(value int) string { return fmt.Sprintf("%032x", value) }

func reverse[T any](values []T) {
	sort.SliceStable(values, func(i, j int) bool { return i > j })
}

func TestNormalizeDoesNotMutateInput(t *testing.T) {
	schema := sqliteSocialSchema()
	original := fmt.Sprintf("%#v", schema)
	_, err := Normalize(schema)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%#v", schema) != original {
		t.Fatal("Normalize mutated its input")
	}
}
