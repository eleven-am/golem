package schema_test

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/runtime/testdata/v073facts"
)

func requireDocumentFailure(t *testing.T, err error, path, detail string) {
	t.Helper()
	var failure *schema.Error
	if !errors.As(err, &failure) {
		t.Fatalf("error is not a schema error: %v", err)
	}
	if failure.Code != schema.CodeDocument || failure.Path != path || failure.Detail != detail {
		t.Fatalf("schema error = code %s path %q detail %q; want code %s path %q detail %q", failure.Code, failure.Path, failure.Detail, schema.CodeDocument, path, detail)
	}
}

func withContractFormat(bundle golem.SchemaBundle, version uint32) golem.SchemaBundle {
	contract := bundle.Contract()
	document := golem.GeneratedSchemaDocument(version, contract.CanonicalVersion(), contract.Fingerprint(), contract.Bytes())
	return golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), document, bundle.Providers()...)
}

func withModelFormat(bundle golem.SchemaBundle, version uint32) golem.SchemaBundle {
	model := bundle.Model()
	document := golem.GeneratedSchemaDocument(version, model.CanonicalVersion(), model.Fingerprint(), model.Bytes())
	return golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), document, bundle.Contract(), bundle.Providers()...)
}

func TestDecodeActiveRejectsOlderContractFormatWithRegenerate(t *testing.T) {
	const want = "generated contract format 6 predates this golem (format 7): regenerate with golem generate"
	bundle := v073facts.SchemaBundle()
	if bundle.Contract().FormatVersion() != 6 {
		t.Fatalf("frozen v0.7.3 bundle has contract format %d", bundle.Contract().FormatVersion())
	}
	_, decodeErr := schema.DecodeActive(bundle)
	requireDocumentFailure(t, decodeErr, "contract.formatVersion", want)
	_, newErr := schema.New(bundle)
	requireDocumentFailure(t, newErr, "contract.formatVersion", want)
}

func TestDecodeActiveRejectsNewerContractFormatWithUpgrade(t *testing.T) {
	bundle := withContractFormat(schematest.NewSubscribedIndexed(t).Bundle, uint32(compilerir.ContractFormatVersion)+1)
	_, err := schema.DecodeActive(bundle)
	requireDocumentFailure(t, err, "contract.formatVersion", "generated contract format 8 is newer than this golem (format 7): upgrade golem")
}

func TestDecodeActiveRejectsOlderAndNewerModelFormat(t *testing.T) {
	bundle := schematest.NewSubscribedIndexed(t).Bundle
	_, err := schema.DecodeActive(withModelFormat(bundle, uint32(compilerir.ModelFormatVersion)-1))
	requireDocumentFailure(t, err, "model.formatVersion", "generated model format 1 predates this golem (format 2): regenerate with golem generate")
	_, err = schema.DecodeActive(withModelFormat(bundle, uint32(compilerir.ModelFormatVersion)+1))
	requireDocumentFailure(t, err, "model.formatVersion", "generated model format 3 is newer than this golem (format 2): upgrade golem")
}

func TestDecodeActiveAcceptsTheCurrentBundle(t *testing.T) {
	fixture := schematest.NewSubscribedIndexed(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	if compilation.Contract.FormatVersion != compilerir.ContractFormatVersion || compilation.Model.FormatVersion != compilerir.ModelFormatVersion {
		t.Fatalf("decoded formats = %d/%d", compilation.Model.FormatVersion, compilation.Contract.FormatVersion)
	}
}

func TestNewHistoricalRejectsContractV6WithDrainInstruction(t *testing.T) {
	_, err := schema.NewHistorical(v073facts.SchemaBundle())
	requireDocumentFailure(t, err, "contract.formatVersion", "historical contract format 6 is not supported by this golem: deliver or drain undelivered events before upgrading, then remove this bundle from HistoricalEventBundles")
}
