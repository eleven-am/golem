package schematest

import (
	"encoding/hex"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/dataincarnation"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// WithDataIncarnation opts a graph into the real provider-owned identity schema.
// Both physical documents are lowered from the same complete extension inventory.
func WithDataIncarnation(t testing.TB, fixture GraphFixture) GraphFixture {
	t.Helper()
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	if compilation.Contract.DataIncarnation != nil {
		t.Fatal("fixture already declares data incarnation")
	}
	for _, provider := range compilation.Model.Providers {
		identity, diagnostic := compilerir.NewIDRegistry().Register(compilerir.ObjectExtension,
			compilerir.OwnedIdentity(string(compilation.Model.Schema.ID), dataincarnation.Kind+"\x00"+string(provider)), compilerir.SourceSpan{})
		if diagnostic != nil {
			t.Fatal(diagnostic)
		}
		compilation.Model.Extensions = append(compilation.Model.Extensions, compilerir.ProviderExtensionIR{
			ID: compilerir.ExtensionIDFrom(identity), Provider: provider, Version: dataincarnation.Version,
			Owner: compilerir.ObjectID(compilation.Model.Schema.ID), Kind: dataincarnation.Kind, Payload: "{}",
		})
	}
	compilation.Contract.DataIncarnation = &compilerir.DataIncarnationContractIR{Kind: "provider", Version: dataincarnation.Version}
	if err := compilerir.ValidateDataIncarnation(compilation); err != nil {
		t.Fatal(err)
	}
	fixture.SQLite = lowerSemantic(t, sqliteprovider.New(), compilation.Model, fixture.SQLite)
	fixture.PostgreSQL = lowerSemantic(t, postgresprovider.New(), compilation.Model, fixture.PostgreSQL)
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(encoded) != 32 {
		t.Fatal("invalid incarnation contract fingerprint")
	}
	var digest golem.SchemaDigest
	copy(digest[:], encoded)
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	fixture.Bundle = golem.GeneratedSchemaBundle(fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		canonicalModelDocument(t, fixture.Bundle.Model(), compilation.Model), contract,
		ProviderDocument(t, golem.SQLite, fixture.SQLite), ProviderDocument(t, golem.PostgreSQL, fixture.PostgreSQL))
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}
