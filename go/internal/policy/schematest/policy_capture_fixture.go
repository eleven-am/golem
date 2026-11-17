package schematest

import (
	"encoding/hex"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

// NewPolicyCaptureGraph adds the named(Name) authority and ready(State)
// User-to-Posts fact templates to the subscribed graph fixture. It preserves
// their exact static identities for runtime and durable evidence codec tests.
func NewPolicyCaptureGraph(t testing.TB) GraphFixture {
	t.Helper()
	fixture := NewSubscribedGraph(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	modelID := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	fieldID := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	user, post := modelID(fixture.User), modelID(fixture.Post)
	name, _ := fixture.Registry.Field(fixture.User, fixture.UserName)
	title, _ := fixture.Registry.Field(fixture.Post, fixture.PostTitle)
	args := compilerir.GoNamedTypeIR{PackagePath: "example.test/history", Name: "HistoryArgs"}
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{{ModelID: user, Template: compilerir.ScopeTemplateIR{
		Name: "named", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "Name", Type: name.LogicalType()}},
		Expression: compilerir.ScopeExpressionIR{ModelID: user, Operator: "eq", FieldID: fieldID(fixture.UserName), Parameter: "Name"},
	}}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{{ModelID: user, ScopeName: "named", Template: compilerir.FactTemplateIR{
		Name: "ready", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: title.LogicalType()}},
		Expression: compilerir.ScopeExpressionIR{ModelID: user, Operator: "some", FieldID: fieldID(fixture.UserPosts), RelationID: compilerir.RelationID(hex.EncodeToString(fixture.Authorship[:])), TargetModelID: post,
			Children: []compilerir.ScopeExpressionIR{{ModelID: post, Operator: "eq", FieldID: fieldID(fixture.PostTitle), Parameter: "State"}}},
	}}}); len(diagnostics) != 0 {
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
	if err != nil || len(encoded) != 32 {
		t.Fatalf("contract fingerprint: %q %v", fingerprint, err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], encoded)
	document := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	fixture.Bundle = golem.GeneratedSchemaBundle(fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), fixture.Bundle.Model(), document, fixture.Bundle.Providers()...)
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}
