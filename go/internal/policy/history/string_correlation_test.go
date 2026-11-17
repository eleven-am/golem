package history_test

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestGraphCompositeStringCorrelationUsesPhysicalOperatorRequirements(t *testing.T) {
	fixture, authorName := graphCompositeStringFixture(t)
	user := graphRow(t, fixture.Registry, fixture.User,
		mutationdecode.Value(policyir.FieldID(fixture.UserID), graphText(t, "tenant")),
		mutationdecode.Value(policyir.FieldID(fixture.UserName), graphText(t, "Owner")))
	post := func(owner string) mutationdecode.Row {
		return graphRow(t, fixture.Registry, fixture.Post,
			mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: 51})),
			mutationdecode.Value(policyir.FieldID(fixture.AuthorID), graphText(t, "tenant")),
			mutationdecode.Value(policyir.FieldID(authorName), graphText(t, owner)),
			mutationdecode.Value(policyir.FieldID(fixture.PostTitle), graphText(t, "ready")))
	}
	input := history.Input{
		Rows:  []history.PhaseRow{{Phase: history.Before, Row: user}, {Phase: history.Before, Row: post("Owner")}, {Phase: history.After, Row: user}, {Phase: history.After, Row: post("Owner")}},
		Roots: []history.Root{{Identity: user, Before: history.Presence{Present: true, Row: 1}, After: history.Presence{Present: true, Row: 3}}},
		Extents: []history.Extent{
			{Phase: history.Before, Parent: 1, Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship), Children: []history.RowRef{2}},
			{Phase: history.After, Parent: 3, Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship), Children: []history.RowRef{4}},
		},
	}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		graph, err := history.New(fixture.Registry, provider, input)
		if err != nil {
			t.Fatalf("provider=%d composite string equality rejected: %v", provider, err)
		}
		key, err := history.RowKey(fixture.Registry, provider, user)
		if err != nil {
			t.Fatal(err)
		}
		if rows, known := graph.Children(history.After, key, policyir.FieldID(fixture.UserPosts), policyir.RelationID(fixture.Authorship)); !known || len(rows) != 1 {
			t.Fatal("composite string extent was not retained")
		}
		wrong := cloneGraphInput(input)
		wrong.Rows[3].Row = post("owner")
		if _, err := history.New(fixture.Registry, provider, wrong); err == nil {
			t.Fatal("binary string correlation ignored the second component or its case")
		}
	}
	for _, projection := range fixture.Registry.PolicyCaptureProjections(fixture.User) {
		if len(projection.Path) == 0 {
			continue
		}
		hop := projection.Path[0]
		endpoint, err := history.CaptureEndpoint(fixture.Registry, fixture.User, hop)
		if err != nil || len(endpoint.Correlation()) != 2 {
			t.Fatalf("shared composite endpoint: %v", err)
		}
		hop.ChildFields[1] = hop.ChildFields[0]
		if _, err := history.CaptureEndpoint(fixture.Registry, fixture.User, hop); err == nil {
			t.Fatal("shared endpoint owner accepted a forged correlation component")
		}
		return
	}
	t.Fatal("fixture omitted its actual registered composite path")
}

// Adapt the real subscribed graph's author relation to the composite string
// primary key (User.ID, User.Name). Rebuild logical/event/physical documents
// normally, then normalize the exact same authority and fact declarations.
func graphCompositeStringFixture(t *testing.T) (schematest.GraphFixture, golem.FieldID) {
	t.Helper()
	fixture := schematest.NewPolicyCaptureGraph(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	modelID := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	fieldID := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	authorName := golem.FieldID{14: 2, 15: 201}
	var scopes []scopecontract.Declaration
	var facts []scopecontract.FactDeclaration
	for index := range compilation.Contract.Models {
		owner := &compilation.Contract.Models[index]
		for _, template := range owner.ScopeTemplates {
			scopes = append(scopes, scopecontract.Declaration{ModelID: owner.ModelID, Template: template})
		}
		for _, template := range owner.FactTemplates {
			facts = append(facts, scopecontract.FactDeclaration{ModelID: owner.ModelID, ScopeName: "named", Template: template})
		}
		owner.ScopeTemplates, owner.FactTemplates = nil, nil
		if owner.ModelID == modelID(fixture.Post) {
			owner.Fields = append(owner.Fields, compilerir.FieldContractIR{FieldID: fieldID(authorName)})
		}
	}
	for index := range compilation.Model.Models {
		model := &compilation.Model.Models[index]
		for field := range model.Fields {
			if model.Fields[field].ID == fieldID(fixture.UserID) || model.Fields[field].ID == fieldID(fixture.AuthorID) {
				model.Fields[field].Scalar.Type = compilerir.LogicalTypeIR{Kind: compilerir.TypeString}
			}
		}
		if model.ID == modelID(fixture.User) {
			model.PrimaryKey.Fields = []compilerir.FieldID{fieldID(fixture.UserID), fieldID(fixture.UserName)}
		}
		if model.ID == modelID(fixture.Post) {
			model.Fields = append(model.Fields, compilerir.FieldIR{ID: fieldID(authorName), GoName: "AuthorName", LogicalName: "AuthorName", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "author_name", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}}})
		}
	}
	for index := range compilation.Model.Relations {
		relation := &compilation.Model.Relations[index]
		if relation.ID == compilerir.RelationID(hex.EncodeToString(fixture.Authorship[:])) {
			relation.LocalFields = []compilerir.FieldID{fieldID(fixture.AuthorID), fieldID(authorName)}
			relation.RemoteFields = []compilerir.FieldID{fieldID(fixture.UserID), fieldID(fixture.UserName)}
		}
	}
	for index := range compilation.Contract.Models {
		owner := &compilation.Contract.Models[index]
		for _, model := range compilation.Model.Models {
			if owner.ModelID != model.ID {
				continue
			}
			fields, err := compilerir.DeleteSnapshotFields(model, compilation.Model.Relations, nil)
			if err != nil {
				t.Fatal(err)
			}
			shape, err := compilerir.BuildEventSchemaShape(model, compilation.Model.Enums, fields)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint, err := compilerir.EventSchemaFingerprint(shape)
			if err != nil {
				t.Fatal(err)
			}
			owner.Event.Schema, owner.Event.SchemaFingerprint = shape, fingerprint
			owner.Event.DeleteSnapshotFull = compilerir.DeleteSnapshotIsFull(model, fields)
			if len(shape.IdentityFields) > 1 {
				owner.Event.IdentityTypeName = model.LogicalName + "EventIdentity"
			}
		}
	}
	if diagnostics := scopecontract.Normalize(&compilation, scopes); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, facts); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	fixture.SQLite, err = sqliteprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fixture.PostgreSQL, err = postgresprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	modelBytes, err := compilerir.CanonicalModel(compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := compilerir.ModelFingerprint(compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	contractBytes, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	model := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), graphDigest(t, string(modelFingerprint)), modelBytes)
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), graphDigest(t, string(contractFingerprint)), contractBytes)
	fixture.Bundle = golem.GeneratedSchemaBundle(fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), model, contract, schematest.ProviderDocument(t, golem.SQLite, fixture.SQLite), schematest.ProviderDocument(t, golem.PostgreSQL, fixture.PostgreSQL))
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, authorName
}

func graphDigest(t *testing.T, text string) golem.SchemaDigest {
	t.Helper()
	data, err := hex.DecodeString(text)
	if err != nil || len(data) != 32 {
		t.Fatalf("invalid graph fixture digest: %v", err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], data)
	return digest
}
