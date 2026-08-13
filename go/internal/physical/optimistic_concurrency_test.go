package physical

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestOptimisticConcurrencyPhysicalV3ProjectionIsCanonicalAndClosed(t *testing.T) {
	if SchemaFormatVersion != 3 || CanonicalFormatVersion != 3 {
		t.Fatalf("physical versions = %d/%d; want reviewed 3/3", SchemaFormatVersion, CanonicalFormatVersion)
	}
	for _, fixture := range []struct {
		name    string
		schema  PhysicalSchema
		storage StorageKind
	}{
		{name: "sqlite", schema: sqliteSocialSchema(), storage: StorageSQLiteInteger},
		{name: "postgresql", schema: postgresqlSocialSchema(), storage: StoragePostgreSQLBigInt},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			schema := fixture.schema
			table := &schema.Tables[0]
			field := table.Columns[0].ID
			table.OptimisticConcurrency = &field
			table.Columns[0].Storage = StorageType{Kind: fixture.storage}
			table.Columns[0].Nullable = false
			table.Columns[0].Default = PhysicalDefault{Kind: DefaultNone}
			table.Columns[0].Generated = nil
			table.Columns[0].Collation = nil
			table.PrimaryKey = nil

			encoded, err := CanonicalEncode(schema)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := CanonicalDecode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Tables[0].OptimisticConcurrency == nil || *decoded.Tables[0].OptimisticConcurrency != field {
				t.Fatalf("round trip concurrency = %v; want %s", decoded.Tables[0].OptimisticConcurrency, field)
			}
			without := schema
			without.Tables = append([]PhysicalTable(nil), schema.Tables...)
			without.Tables[0] = schema.Tables[0]
			without.Tables[0].OptimisticConcurrency = nil
			withoutBytes, err := CanonicalEncode(without)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(encoded, withoutBytes) {
				t.Fatal("physical canonical bytes omitted optimistic-concurrency identity")
			}
			withFingerprint, err := PhysicalFingerprint(schema)
			if err != nil {
				t.Fatal(err)
			}
			withoutFingerprint, err := PhysicalFingerprint(without)
			if err != nil {
				t.Fatal(err)
			}
			if withFingerprint == withoutFingerprint {
				t.Fatal("physical fingerprint omitted optimistic-concurrency identity")
			}
		})
	}
}

func TestHistoricalV2CanonicalFragmentRejectsCurrentOnlyConcurrencyField(t *testing.T) {
	field := ir.FieldID("20000000000000000000000000000001")
	table := PhysicalTable{ID: "10000000000000000000000000000001", Name: "items", OptimisticConcurrency: &field}
	if _, err := HistoricalV2CanonicalFragment(table); err == nil || !strings.Contains(err.Error(), "outside the frozen schema") {
		t.Fatalf("v2 fragment accepted v3 table metadata: %v", err)
	}
	if _, err := CanonicalFragmentVersion(table, 2); err == nil || !strings.Contains(err.Error(), "outside the frozen schema") {
		t.Fatalf("generic v2 fragment entrypoint accepted v3 table metadata: %v", err)
	}
}

func TestOptimisticConcurrencyPhysicalValidationRejectsEveryForgedProjection(t *testing.T) {
	base := sqliteSocialSchema()
	table := &base.Tables[0]
	field := table.Columns[0].ID
	table.OptimisticConcurrency = &field
	table.Columns[0].Storage = StorageType{Kind: StorageSQLiteInteger}
	table.PrimaryKey = nil
	if err := Validate(base); err != nil {
		t.Fatal(err)
	}

	foreignField := base.Tables[1].Columns[0].ID
	tests := []struct {
		name string
		edit func(*PhysicalSchema)
		text string
	}{
		{name: "orphan", edit: func(s *PhysicalSchema) { s.Tables[0].OptimisticConcurrency = &foreignField }, text: "same-table stored column"},
		{name: "nullable", edit: func(s *PhysicalSchema) { s.Tables[0].Columns[0].Nullable = true }, text: "non-null"},
		{name: "wrong storage", edit: func(s *PhysicalSchema) { s.Tables[0].Columns[0].Storage = StorageType{Kind: StorageSQLiteText} }, text: "sqlite.integer"},
		{name: "default", edit: func(s *PhysicalSchema) {
			s.Tables[0].Columns[0].Default = PhysicalDefault{Kind: DefaultLiteral, Literal: &ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: "1"}}
		}, text: "no physical default"},
		{name: "generated", edit: func(s *PhysicalSchema) {
			s.Tables[0].Columns[0].Generated = &GeneratedExpression{Kind: GeneratedStored, Expression: columnExpr(field, StorageType{Kind: StorageSQLiteInteger})}
		}, text: "not be generated"},
		{name: "collation", edit: func(s *PhysicalSchema) {
			s.Tables[0].Columns[0].Collation = &SemanticSymbol{Identity: "sqlite.binary", Kind: ir.SchemaSymbolFunction, Version: 1, Provider: ir.ProviderScopeSQLite}
		}, text: "not carry collation"},
		{name: "primary key", edit: func(s *PhysicalSchema) {
			s.Tables[0].PrimaryKey = &PhysicalKey{ID: ir.KeyID(id(999)), Name: "pk_concurrency", Columns: []ir.FieldID{field}}
		}, text: "identity or foreign key"},
		{name: "unique", edit: func(s *PhysicalSchema) {
			s.Tables[0].Uniques = append(s.Tables[0].Uniques, PhysicalKey{ID: ir.KeyID(id(998)), Name: "uq_concurrency", Columns: []ir.FieldID{field}})
		}, text: "identity or foreign key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := cloneSchema(base)
			test.edit(&schema)
			err := Validate(schema)
			if err == nil || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("error = %v; want %q", err, test.text)
			}
		})
	}
}

func TestFrozenPhysicalV2IsIndependentAndReviewedMatrixIsClosed(t *testing.T) {
	v2 := sqliteSocialSchema()
	v2.Version, v2.CanonicalVersion = 2, 2
	encoded, err := CanonicalEncodeHistoricalV2(v2)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := CanonicalDecodeHistoricalV2(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != 2 || decoded.CanonicalVersion != 2 {
		t.Fatalf("historical v2 decoded as %d/%d", decoded.Version, decoded.CanonicalVersion)
	}
	if _, err := CanonicalDecode(encoded); err == nil {
		t.Fatal("active decoder accepted frozen physical v2")
	}
	if _, err := CanonicalDecodeHistorical(encoded); err == nil {
		t.Fatal("v1-only decoder accepted frozen physical v2")
	}
	if _, err := CanonicalDecodeHistoricalV2(bytes.Replace(encoded, []byte("PhysicalTable"), []byte("ForgedTable"), 1)); err == nil {
		t.Fatal("frozen v2 decoder accepted a changed DTO type")
	}
}

func TestFrozenPhysicalV2ValidatorRetainsClosedReleaseMutations(t *testing.T) {
	base := sqliteSocialSchema()
	base.Version, base.CanonicalVersion = 2, 2
	base, err := NormalizeHistoricalV2(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*PhysicalSchema)
		text string
		code historicalV2ValidationCode
		path string
	}{
		{name: "cross-kind stable ID", text: "operation-addressable stable ID", code: historicalV2CodeDuplicate, path: ".primaryKey.id", edit: func(schema *PhysicalSchema) {
			if schema.Tables[0].PrimaryKey == nil {
				t.Fatal("fixture primary key is absent")
			}
			schema.Tables[0].PrimaryKey.ID = ir.KeyID(schema.Tables[0].Columns[0].ID)
		}},
		{name: "generated result type", text: "generated expression result storage", code: historicalV2CodeExpression, path: ".generated.expression.type", edit: func(schema *PhysicalSchema) {
			field := schema.Tables[0].Columns[0].ID
			wrong := StorageType{Kind: StorageSQLiteInteger}
			if schema.Tables[0].Columns[0].Storage == wrong {
				wrong = StorageType{Kind: StorageSQLiteText}
			}
			schema.Tables[0].Columns[0].Generated = &GeneratedExpression{Kind: GeneratedStored, Expression: Expression{Kind: ExpressionColumn, Type: wrong, Column: &field, Operands: []Expression{}}}
		}},
		{name: "registered system object", text: "closed v1 registry entry", code: historicalV2CodeExtension, path: "system.object[", edit: func(schema *PhysicalSchema) {
			schema.System = SystemSchema{Version: 1, Namespace: Namespace{Name: "main"}, Objects: []SystemObject{{ID: OutboxObjectIDV1, Kind: SystemOutbox, Version: 1, Name: "_golem_outbox_forged"}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged, cloneErr := NormalizeHistoricalV2(base)
			if cloneErr != nil {
				t.Fatal(cloneErr)
			}
			test.edit(&forged)
			_, err := NormalizeHistoricalV2(forged)
			if err == nil || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("frozen v2 validator mutation error=%v want %q", err, test.text)
			}
			var diagnostics historicalV2ValidationErrors
			if !errors.As(err, &diagnostics) {
				t.Fatalf("frozen v2 mutation returned untyped diagnostics: %T", err)
			}
			matched := false
			for _, diagnostic := range diagnostics {
				matched = matched || diagnostic.Code == test.code && strings.Contains(diagnostic.Path, test.path)
			}
			if !matched {
				t.Fatalf("frozen v2 mutation diagnostics=%#v want code=%s path~%q", diagnostics, test.code, test.path)
			}
		})
	}
}
