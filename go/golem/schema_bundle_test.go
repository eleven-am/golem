package golem

import (
	"reflect"
	"testing"
)

func TestGeneratedSchemaBundleIsOpaqueAndCopyIsolated(t *testing.T) {
	digest := SchemaDigest{1}
	modelBytes := []byte("model")
	providerBytes := []byte("physical")
	model := GeneratedSchemaDocument(2, 3, digest, modelBytes)
	contract := GeneratedSchemaDocument(4, 5, SchemaDigest{2}, []byte("contract"))
	provider := GeneratedProviderSchemaDocument(SQLite, SchemaDigest{5}, GeneratedSchemaDocument(6, 7, SchemaDigest{3}, providerBytes))
	bundle := GeneratedSchemaBundle(SchemaDigest{4}, "generator", "template", model, contract, provider)

	modelBytes[0] = 'X'
	providerBytes[0] = 'X'
	modelCopy := bundle.Model().Bytes()
	modelCopy[0] = 'Y'
	providers := bundle.Providers()
	providerCopy := providers[0].Schema().Bytes()
	providerCopy[0] = 'Y'
	providers[0] = ProviderSchemaDocument{}

	if string(bundle.Model().Bytes()) != "model" || string(bundle.Providers()[0].Schema().Bytes()) != "physical" {
		t.Fatal("schema bundle payload escaped defensive copies")
	}
	if bundle.FormatVersion() != SchemaBundleFormatVersion || bundle.GenerationDigest() != (SchemaDigest{4}) || bundle.GeneratorVersion() != "generator" || bundle.TemplateABIVersion() != "template" {
		t.Fatalf("bundle identity was not preserved")
	}
	if bundle.Model().FormatVersion() != 2 || bundle.Model().CanonicalVersion() != 3 || bundle.Model().Fingerprint() != digest || bundle.Providers()[0].Provider() != SQLite || bundle.Providers()[0].SystemFingerprint() != (SchemaDigest{5}) {
		t.Fatalf("document identity was not preserved")
	}
}

func TestGeneratedMigrationManifestDocumentIsBoundAndCopyIsolated(t *testing.T) {
	generation := SchemaDigest{1, 2, 3}
	payload := []byte(`{"formatVersion":1}`)
	migration := GeneratedMigrationManifestDocument(generation, SQLite, payload)
	schema := GeneratedSchemaDocument(1, 1, SchemaDigest{4}, []byte("schema"))
	provider := GeneratedProviderSchemaDocumentWithMigration(SQLite, SchemaDigest{5}, schema, migration)
	bundle := GeneratedSchemaBundle(generation, "generator", "template", schema, schema, provider)
	validated, err := bundle.MigrationManifest(SQLite)
	if err != nil || string(validated) != string(payload) {
		t.Fatal("provider migration manifest was not preserved")
	}
	payload[0] = 'x'
	validated[0] = 'x'
	validated, err = bundle.MigrationManifest(SQLite)
	if err != nil || string(validated) != `{"formatVersion":1}` {
		t.Fatal("migration manifest payload was not copy isolated")
	}
	if _, err := bundle.MigrationManifest(PostgreSQL); err == nil {
		t.Fatal("foreign provider selected the SQLite migration manifest")
	}
	foreign := GeneratedMigrationManifestDocument(SchemaDigest{9}, SQLite, []byte(`{"formatVersion":1}`))
	foreignBundle := GeneratedSchemaBundle(generation, "generator", "template", schema, schema,
		GeneratedProviderSchemaDocumentWithMigration(SQLite, SchemaDigest{5}, schema, foreign))
	if _, err := foreignBundle.MigrationManifest(SQLite); err == nil {
		t.Fatal("foreign generation migration manifest was accepted")
	}
	tampered := bundle
	tampered.providers = append([]ProviderSchemaDocument(nil), bundle.providers...)
	tampered.providers[0].migration.payload = append([]byte(nil), bundle.providers[0].migration.payload...)
	tampered.providers[0].migration.payload[0] = 'x'
	if _, err := tampered.MigrationManifest(SQLite); err == nil {
		t.Fatal("tampered migration manifest bytes were accepted")
	}
}

func TestP8MigrationManifestPublicABIInventory(t *testing.T) {
	if SchemaBundleFormatVersion != 2 {
		t.Fatalf("SchemaBundleFormatVersion = %d, want 2", SchemaBundleFormatVersion)
	}
	value := reflect.TypeOf(MigrationManifestDocument{})
	pointer := reflect.PointerTo(value)
	if value.NumMethod() != 0 || pointer.NumMethod() != 0 {
		t.Fatalf("opaque migration document methods: value=%d pointer=%d", value.NumMethod(), pointer.NumMethod())
	}
	wantDocumentConstructor := reflect.FuncOf(
		[]reflect.Type{reflect.TypeOf(SchemaDigest{}), reflect.TypeOf(Provider("")), reflect.TypeOf([]byte(nil))},
		[]reflect.Type{value}, false,
	)
	if actual := reflect.TypeOf(GeneratedMigrationManifestDocument); actual != wantDocumentConstructor {
		t.Fatalf("GeneratedMigrationManifestDocument signature = %v, want %v", actual, wantDocumentConstructor)
	}
	wantProviderConstructor := reflect.FuncOf(
		[]reflect.Type{reflect.TypeOf(Provider("")), reflect.TypeOf(SchemaDigest{}), reflect.TypeOf(SchemaDocument{}), value},
		[]reflect.Type{reflect.TypeOf(ProviderSchemaDocument{})}, false,
	)
	if actual := reflect.TypeOf(GeneratedProviderSchemaDocumentWithMigration); actual != wantProviderConstructor {
		t.Fatalf("GeneratedProviderSchemaDocumentWithMigration signature = %v, want %v", actual, wantProviderConstructor)
	}
	method, ok := reflect.TypeOf(SchemaBundle{}).MethodByName("MigrationManifest")
	if !ok || method.Type.NumIn() != 2 || method.Type.In(1) != reflect.TypeOf(Provider("")) || method.Type.NumOut() != 2 || method.Type.Out(0) != reflect.TypeOf([]byte(nil)) || method.Type.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		t.Fatalf("SchemaBundle.MigrationManifest public signature = %#v", method)
	}
}
