package schematest

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

// SemanticIndexName is the single index declared by the semantic fixtures.
const SemanticIndexName = "related"

// NewSemanticIndexed is the subscribed indexed fixture with one semantic index
// declared over Post.Title. Post is therefore both subscription-enabled and
// semantic-indexed, and User is neither, so one fixture carries both sides of
// every registry-derived decision.
func NewSemanticIndexed(t testing.TB) Fixture {
	return WithSemanticIndex(t, NewSubscribedIndexed(t))
}

// NewSemanticIndexedUnsubscribed declares the same semantic index on a Post
// that emits no durable facts. It separates the semantic mark from the outbox
// row so neither assertion can stand in for the other.
func NewSemanticIndexedUnsubscribed(t testing.TB) Fixture {
	return WithSemanticIndex(t, NewIndexed(t))
}

// NewSemanticIndexedUniqueTitle declares the semantic index over a Post.Title
// that is also a unique key. Writing that field then makes the mutation's
// identity behavior IdentityMayChange, which widens the program's identity
// verification beyond the primary key — the one shape where a mark built from
// the verified field list rather than the primary key would be wrong.
func NewSemanticIndexedUniqueTitle(t testing.TB) Fixture {
	return WithSemanticIndex(t, withUniqueTitle(t, NewSubscribedIndexed(t)))
}

// NewSemanticIndexedUniqueAuthorIDWithContractModes declares a semantic Post
// index with a unique visible author selector and caller-selected field modes.
func NewSemanticIndexedUniqueAuthorIDWithContractModes(t testing.TB, modes ContractModes) Fixture {
	fixture := NewWithContractModes(t, modes)
	authorID := compilerir.FieldID(hex.EncodeToString(fixture.AuthorID[:]))
	return WithSemanticIndex(t, withUniquePostField(t, fixture, authorID, "uq_posts_author_id"))
}

func withUniqueTitle(t testing.TB, fixture Fixture) Fixture {
	return withUniquePostField(t, fixture, compilerir.FieldID(hex.EncodeToString(fixture.PostTitle[:])), "uq_posts_title")
}

func withUniquePostField(t testing.TB, fixture Fixture, field compilerir.FieldID, physicalName compilerir.SQLIdentifier) Fixture {
	t.Helper()
	modelDocument := fixture.Bundle.Model()
	var model compilerir.ModelIR
	if err := json.Unmarshal(modelDocument.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	for index := range model.Models {
		if model.Models[index].ID != compilerir.ModelID(hex.EncodeToString(fixture.Post[:])) {
			continue
		}
		model.Models[index].Uniques = []compilerir.KeyIR{{
			ID: compilerir.KeyID(id(62)), Kind: compilerir.KeyUnique,
			PhysicalName: physicalName, Fields: []compilerir.FieldID{field},
		}}
	}
	fixture.Bundle = golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		canonicalModelDocument(t, modelDocument, model), fixture.Bundle.Contract(), fixture.Bundle.Providers()...,
	)
	return fixture
}

// WithSemanticIndex declares the fixture's semantic index on any Post-shaped
// fixture, so optimistic-concurrency and mutation-vocabulary variants can carry
// one without a second copy of the builder.
func WithSemanticIndex(t testing.TB, fixture Fixture) Fixture {
	t.Helper()
	modelDocument := fixture.Bundle.Model()
	var model compilerir.ModelIR
	if err := json.Unmarshal(modelDocument.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	payload, err := semanticcontract.Encode(semanticcontract.Index{
		Name: SemanticIndexName, Space: "content", Dimensions: 3,
		Fields: []string{hex.EncodeToString(fixture.PostTitle[:])}, Metric: "cosine",
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := compilerir.ObjectID(hex.EncodeToString(fixture.Post[:]))
	for _, provider := range []compilerir.Provider{compilerir.PostgreSQL, compilerir.SQLite} {
		model.Extensions = append(model.Extensions, compilerir.ProviderExtensionIR{
			ID: compilerir.ExtensionID(id(61)), Provider: provider, Version: semanticcontract.Version,
			Owner: owner, Kind: semanticcontract.IndexKind, Payload: payload,
		})
	}
	modelDocument = canonicalModelDocument(t, modelDocument, model)
	// The physical schema has to be lowered again, not carried through. An index
	// declared only in the model IR leaves the provider documents without the
	// shadow storage the runtime derives its index inventory from, which would
	// make the whole semantic surface silently inert.
	fixture.SQLite = lowerSemantic(t, sqliteprovider.New(), model, fixture.SQLite)
	fixture.PostgreSQL = lowerSemantic(t, postgresprovider.New(), model, fixture.PostgreSQL)
	fixture.Bundle = golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		modelDocument, fixture.Bundle.Contract(),
		providerDocument(t, golem.SQLite, fixture.SQLite), providerDocument(t, golem.PostgreSQL, fixture.PostgreSQL),
	)
	registry, err := schema.New(fixture.Bundle)
	if err != nil {
		t.Fatalf("bootstrap semantic schema fixture: %v", err)
	}
	fixture.Registry = registry
	return fixture
}

// lowerSemantic re-lowers one provider schema and keeps the namespaces the
// original fixture chose, so a PostgreSQL fixture built for a private namespace
// keeps it after the semantic index is added.
func lowerSemantic(t testing.TB, provider physical.Lowerer, model compilerir.ModelIR, previous physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	lowered, err := provider.Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lowered.Namespace.Name = previous.Namespace.Name
	lowered.System.Namespace.Name = previous.System.Namespace.Name
	return lowered
}

// ProviderDocument packages one physical schema exactly as the fixture builder
// does, so a test can rebuild a bundle around a modified provider schema.
func ProviderDocument(t testing.TB, provider golem.Provider, value physical.PhysicalSchema) golem.ProviderSchemaDocument {
	t.Helper()
	return providerDocument(t, provider, value)
}

func canonicalModelDocument(t testing.TB, previous golem.SchemaDocument, model compilerir.ModelIR) golem.SchemaDocument {
	t.Helper()
	canonical, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(raw) != len(golem.SchemaDigest{}) {
		t.Fatalf("model fingerprint=%q error=%v", fingerprint, err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], raw)
	return golem.GeneratedSchemaDocument(previous.FormatVersion(), previous.CanonicalVersion(), digest, canonical)
}
