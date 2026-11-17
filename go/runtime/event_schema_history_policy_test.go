package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestPolicyHistorySchemaRequiresExactGenerationAndProjection(t *testing.T) {
	fixture := schematest.NewSubscribedGraph(t)
	activeBundle := policyHistorySchemaBundle(t, fixture, "active", true)
	oldBundle := policyHistorySchemaBundle(t, fixture, "old", false)
	sameProjectionBundle := policyHistorySchemaBundle(t, fixture, "renamed", true)
	active, err := schema.New(activeBundle)
	if err != nil {
		t.Fatal(err)
	}
	history, err := newEventSchemaHistory(active, []golem.SchemaBundle{oldBundle, sameProjectionBundle, fixture.Bundle})
	if err != nil {
		t.Fatal(err)
	}
	old, _, oldFound := history.ResolveFactSchema(mutationfact.SchemaReference{FormatVersion: mutationfact.HistoryFactVersion, Generation: oldBundle.GenerationDigest()})
	sameProjection, _, sameFound := history.ResolveFactSchema(mutationfact.SchemaReference{FormatVersion: mutationfact.HistoryFactVersion, Generation: sameProjectionBundle.GenerationDigest()})
	if !oldFound || !sameFound || old == nil || sameProjection == nil {
		t.Fatal("historical generations were not registered")
	}
	if old.PolicyCaptureFingerprint() == active.PolicyCaptureFingerprint() || sameProjection.PolicyCaptureFingerprint() != active.PolicyCaptureFingerprint() {
		t.Fatal("fixture did not distinguish capture shape from declaration metadata")
	}
	for _, registry := range []*schema.Registry{active, old, sameProjection} {
		got, ok := history.ResolvePolicyHistorySchema(registry.GenerationDigest(), registry.PolicyCaptureFingerprint())
		if !ok || got != registry {
			t.Fatal("exact historical generation and projection did not resolve")
		}
	}
	for _, test := range []struct {
		name                   string
		generation, projection golem.SchemaDigest
	}{
		{"zero-generation", golem.SchemaDigest{}, active.PolicyCaptureFingerprint()},
		{"unknown-generation", golem.SchemaDigest{255}, active.PolicyCaptureFingerprint()},
		{"zero-projection", active.GenerationDigest(), golem.SchemaDigest{}},
		{"wrong-projection", active.GenerationDigest(), old.PolicyCaptureFingerprint()},
		{"wrong-historical-projection", old.GenerationDigest(), active.PolicyCaptureFingerprint()},
		{"no-template-generation", fixture.Bundle.GenerationDigest(), active.PolicyCaptureFingerprint()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, ok := history.ResolvePolicyHistorySchema(test.generation, test.projection); ok || got != nil {
				t.Fatal("mismatched evidence resolved")
			}
		})
	}
	var absent *eventSchemaHistory
	if got, ok := absent.ResolvePolicyHistorySchema(active.GenerationDigest(), active.PolicyCaptureFingerprint()); ok || got != nil {
		t.Fatal("nil history resolved graph evidence")
	}

	activeModel, _ := active.Model(fixture.User)
	oldModel, _ := old.Model(fixture.User)
	activeEvent, _, enabled := activeModel.EventSchema()
	oldEvent, _, _ := oldModel.EventSchema()
	if !enabled || activeEvent != oldEvent {
		t.Fatal("fixture must retain identical V3 event-row schemas across capture changes")
	}
	eventDigest, err := mutationfact.ParseEventSchemaFingerprint(activeEvent)
	if err != nil {
		t.Fatal(err)
	}
	// V3 intentionally remains event-schema compatible, including when its
	// supplied generation is not registered. V4 cannot inherit this shortcut.
	reference := mutationfact.SchemaReference{FormatVersion: mutationfact.FormatVersion, Generation: golem.SchemaDigest{255}, EventSchema: eventDigest}
	if got, digest, ok := history.ResolveFactSchema(reference); !ok || got != active || digest != eventDigest {
		t.Fatal("legacy event-schema resolver behavior changed")
	}
	reference.FormatVersion = mutationfact.HistoryFactVersion
	if got, _, ok := history.ResolveFactSchema(reference); ok || got != nil {
		t.Fatal("V4 accepted unknown generation through identical event shape")
	}
	reference.Generation = old.GenerationDigest()
	if got, digest, ok := history.ResolveFactSchema(reference); !ok || got != old || digest != eventDigest {
		t.Fatal("V4 did not resolve the exact historical generation")
	}
}

func policyHistorySchemaBundle(t *testing.T, fixture schematest.GraphFixture, name string, includeName bool) golem.SchemaBundle {
	t.Helper()
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	root := compilerir.ModelID(hex.EncodeToString(fixture.User[:]))
	expression := compilerir.ScopeExpressionIR{ModelID: root, Operator: "true"}
	if includeName {
		expression = compilerir.ScopeExpressionIR{ModelID: root, Operator: "eq", FieldID: compilerir.FieldID(hex.EncodeToString(fixture.UserName[:])), Literals: []compilerir.TypedLiteralIR{{Kind: compilerir.LiteralString, Canonical: "ready"}}}
	}
	declaration := scopecontract.Declaration{ModelID: root, Template: compilerir.ScopeTemplateIR{
		Name: name, Arguments: compilerir.GoNamedTypeIR{PackagePath: "example.test/history", Name: "Args"}, Expression: expression,
	}}
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{declaration}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hex.DecodeString(string(fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], encoded)
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	// schematest uses a constant generation. Derive distinct fixture identities
	// from their documents instead; this tests exact lookup, not a cryptographic
	// proof that production generation manifests have been validated.
	hash := sha256.New()
	_, _ = hash.Write([]byte("test:policy-history-generation:v1\x00"))
	modelFingerprint := fixture.Bundle.Model().Fingerprint()
	_, _ = hash.Write(modelFingerprint[:])
	_, _ = hash.Write(digest[:])
	var generation golem.SchemaDigest
	copy(generation[:], hash.Sum(nil))
	return golem.GeneratedSchemaBundle(generation, fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), fixture.Bundle.Model(), contract, fixture.Bundle.Providers()...)
}
