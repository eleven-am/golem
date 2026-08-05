package golem

import "testing"

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
