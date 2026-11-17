package sql

import (
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestStaticPolicyCaptureReachesSparseScalarNodesWithoutChangingAuthority(t *testing.T) {
	base := policyCaptureBase(t)
	fixture := policyCaptureFixture(t, base)
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		for _, model := range []golem.ModelID{fixture.Post, fixture.User} {
			for _, operation := range []mutationir.Operation{mutationir.Create, mutationir.Update, mutationir.Delete} {
				planned := sparseCapturePlan(t, fixture, model, operation)
				original, err := Render(planned, base.Registry, provider, testProof(t, base, provider))
				if err != nil {
					t.Fatal(err)
				}
				program, err := Render(planned, fixture.Registry, provider, testProof(t, fixture, provider))
				if err != nil {
					t.Fatalf("provider=%v model=%x operation=%d: %v", provider, model, operation, err)
				}
				root, _ := planned.Graph().Root()
				if len(planned.ResultRequirements().Fields()) != 0 || !reflect.DeepEqual(program.FactRequirement(), root.Fact()) {
					t.Fatal("private capture changed result selection or durable fact requirements")
				}
				for index, statement := range program.Statements() {
					if !reflect.DeepEqual(statement.AuthorizationColumns(), original.Statements()[index].AuthorizationColumns()) {
						t.Fatal("private evidence changed the independently denied write-field authorization")
					}
					switch statement.Role() {
					case SelectPreImage, ApplyCreate, ApplyUpdate, ApplyDelete:
						assertPolicyCaptureColumns(t, statement.Columns(), fixture.Registry.PolicyCaptureFields(model))
					case StampVersion:
						if provider == policyir.ProviderPostgreSQL && operation == mutationir.Create {
							assertPolicyCaptureColumns(t, statement.Columns(), fixture.Registry.PolicyCaptureFields(model))
						} else if !reflect.DeepEqual(statement, original.Statements()[index]) {
							t.Fatal("provider clock advance was mistaken for a model-row image")
						}
					case VerifyPostcondition, VerifyReference:
						if !reflect.DeepEqual(statement, original.Statements()[index]) {
							t.Fatal("capture changed the authorization verification statement")
						}
					}
				}
				// The sparse Post node models nested membership writes: neither
				// caller result selection nor mutation input asks for the title.
				// It is present solely because an independent certified fact needs it.
				if model == fixture.Post {
					for _, column := range original.Statements()[0].Columns() {
						if column.FieldID() == policyir.FieldID(fixture.PostTitle) {
							t.Fatal("baseline unexpectedly captured the unauthored fact field")
						}
					}
				}
			}
		}
	}
}

func TestStaticPolicyCaptureAlreadyCoveredByBatchAndCascadeImages(t *testing.T) {
	base := policyCaptureBase(t)
	fixture := policyCaptureFixture(t, base)
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		for _, operation := range []mutationir.Operation{mutationir.UpdateMany, mutationir.DeleteMany} {
			planned := sparseCapturePlan(t, fixture, fixture.Post, operation)
			program, err := mutationbatch.Render(planned, fixture.Registry, provider, testProof(t, fixture, provider))
			if err != nil {
				t.Fatal(err)
			}
			original, err := mutationbatch.Render(planned, base.Registry, provider, testProof(t, base, provider))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(program.CaptureStatement(), original.CaptureStatement()) {
				t.Fatal("static metadata changed an already complete batch capture")
			}
			assertPolicyCaptureColumns(t, program.CaptureStatement().Columns(), fixture.Registry.PolicyCaptureFields(fixture.Post))
			cells := []mutationdecode.Cell{
				mutationdecode.Value(policyir.FieldID(fixture.PostID), uuidValue(1)),
				mutationdecode.Value(policyir.FieldID(fixture.AuthorID), uuidValue(2)),
				mutationdecode.Value(policyir.FieldID(fixture.PostTitle), policyCaptureString(t, "ready")),
			}
			post, _ := fixture.Registry.Model(fixture.Post)
			version, ok := post.RowVersion()
			if !ok {
				t.Fatal("subscribed fixture has no row version")
			}
			one, _ := policyir.SignedValue(policyir.ValueInt64, 1)
			cells = append(cells, mutationdecode.Value(policyir.FieldID(version), one))
			row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), cells)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := program.PrepareCaptured([]mutationdecode.Row{row})
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range prepared.Statements() {
				if statement.Role() != mutationbatch.StampVersion {
					assertPolicyCaptureColumns(t, statement.Columns(), fixture.Registry.PolicyCaptureFields(fixture.Post))
				}
			}
		}
		parent, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.User), []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.UserID), uuidValue(2))})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := mutationbatch.RenderRows(mutationbatch.RowsRequest{Registry: fixture.Registry, Provider: provider, Model: policyir.ModelID(fixture.User), Rows: []mutationdecode.Row{parent}, MaxParameters: 999})
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range rows {
			assertPolicyCaptureColumns(t, statement.Columns(), fixture.Registry.PolicyCaptureFields(fixture.User))
		}
		for _, effect := range fixture.Registry.DeleteEffects(fixture.User) {
			statements, err := mutationbatch.RenderDependents(mutationbatch.DependentsRequest{Registry: fixture.Registry, Provider: provider, Parent: policyir.ModelID(fixture.User), Parents: []mutationdecode.Row{parent}, Effect: effect, MaxRows: 1000, MaxParameters: 999})
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range statements {
				assertPolicyCaptureColumns(t, statement.Columns(), fixture.Registry.PolicyCaptureFields(effect.SourceModelID()))
			}
		}
	}
}

func TestModelOutsideStaticCaptureKeepsIdenticalScalarProgram(t *testing.T) {
	base := policyCaptureBase(t)
	fixture := policyCaptureFixture(t, base)
	compilation, err := schema.DecodeActive(base.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	for _, model := range compilation.Model.Models {
		encoded, err := hex.DecodeString(string(model.ID))
		if err != nil || len(encoded) != len(golem.ModelID{}) {
			t.Fatalf("invalid model identity: %v", err)
		}
		var id golem.ModelID
		copy(id[:], encoded)
		if len(fixture.Registry.PolicyCaptureFields(id)) != 0 {
			continue
		}
		checked = true
		root := policyir.ModelID(id)
		truth, _ := policyir.NewConstant(root, true)
		empty, _ := mutationir.NewImageRequirements(root, nil, nil)
		graph, err := mutationir.NewGraph(mutationir.NodeInput{Model: root, Operation: mutationir.Create, After: empty, RowPostcondition: &truth, Identity: mutationir.IdentityProduced})
		if err != nil {
			t.Fatal(err)
		}
		planned := plan(t, graph, empty)
		for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
			before, err := Render(planned, base.Registry, provider, testProof(t, base, provider))
			if err != nil {
				t.Fatal(err)
			}
			after, err := Render(planned, fixture.Registry, provider, testProof(t, fixture, provider))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("unrelated policy templates changed a model with no capture fields")
			}
		}
	}
	if !checked {
		t.Fatal("fixture has no model outside the static capture inventory")
	}
}

// Manual sparse IR deliberately bypasses the ordinary planner, as internal
// membership nodes do. A false field grant must survive the capture union.
func sparseCapturePlan(t *testing.T, fixture schematest.Fixture, model golem.ModelID, operation mutationir.Operation) mutationir.Plan {
	t.Helper()
	logical, _ := fixture.Registry.Model(model)
	primary := logical.PrimaryKey()[0]
	root := policyir.ModelID(model)
	truth, _ := policyir.NewConstant(root, true)
	denied, _ := policyir.NewConstant(root, false)
	empty, _ := mutationir.NewImageRequirements(root, nil, nil)
	node := mutationir.NodeInput{Model: root, Operation: operation, Before: empty, After: empty, Identity: mutationir.IdentityUnchanged}
	if operation == mutationir.Create || operation == mutationir.Update || operation == mutationir.UpdateMany {
		field := fixture.UserName
		value := policyCaptureString(t, "renamed")
		if model == fixture.Post {
			field, value = fixture.AuthorID, uuidValue(2)
		}
		typ := testType(t, fixture, model, field, policyir.ProviderSQLite)
		set, err := mutationir.NewSet(policyir.FieldID(field), typ, value)
		if err != nil {
			t.Fatal(err)
		}
		node.ScalarOperations = []mutationir.ScalarOperation{set}
		grant, _ := mutationir.NewFieldAuthorization(policyir.FieldID(field), denied)
		node.FieldConditions = []mutationir.FieldAuthorization{grant}
		node.RowPostcondition = &truth
	}
	if operation == mutationir.Create {
		node.Identity = mutationir.IdentityProduced
	} else {
		action := policyir.ActionUpdate
		if operation == mutationir.Delete || operation == mutationir.DeleteMany {
			action = policyir.ActionDelete
		}
		selection, _ := mutationir.NewSelectionRequirement(action, truth)
		node.Selection = &selection
		if operation == mutationir.UpdateMany || operation == mutationir.DeleteMany {
			node.Predicate, node.Identity = &truth, mutationir.IdentityBatchChangeRefused
		} else {
			key := fixture.UserKey
			if model == fixture.Post {
				key = fixture.PostKey
			}
			component, _ := mutationir.NewSelectorValue(policyir.FieldID(primary), uuidValue(1))
			target, err := mutationir.NewTarget(root, key, []mutationir.SelectorValue{component}, nil)
			if err != nil {
				t.Fatal(err)
			}
			node.Target = &target
		}
	}
	graph, err := mutationir.NewGraph(node)
	if err != nil {
		t.Fatal(err)
	}
	return plan(t, graph, empty)
}

func policyCaptureString(t *testing.T, text string) policyir.Value {
	t.Helper()
	value, err := policyir.StringValue(text)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func policyCaptureBase(t *testing.T) schematest.Fixture {
	t.Helper()
	graph := schematest.NewSubscribedGraph(t)
	return schematest.Fixture{
		Bundle: graph.Bundle, Registry: graph.Registry, SQLite: graph.SQLite, PostgreSQL: graph.PostgreSQL,
		User: graph.User, Post: graph.Post, UserID: graph.UserID, UserName: graph.UserName,
		PostID: graph.PostID, AuthorID: graph.AuthorID, PostTitle: graph.PostTitle,
		UserPosts: graph.UserPosts, PostAuthor: graph.PostAuthor, Authorship: graph.Authorship,
		UserKey: graph.UserKey, PostKey: graph.PostKey, PostRowVersion: graph.PostRowVersion,
	}
}

func assertPolicyCaptureColumns[C interface{ FieldID() policyir.FieldID }](t *testing.T, columns []C, want []golem.FieldID) {
	t.Helper()
	if len(want) == 0 {
		t.Fatal("fixture has no static capture fields")
	}
	seen := make(map[policyir.FieldID]bool, len(columns))
	for _, column := range columns {
		seen[column.FieldID()] = true
	}
	for _, field := range want {
		if !seen[policyir.FieldID(field)] {
			t.Fatalf("executable image omitted static policy capture field %x", field)
		}
	}
}

func policyCaptureFixture(t *testing.T, fixture schematest.Fixture) schematest.Fixture {
	t.Helper()
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	modelID := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	fieldID := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	post, user := modelID(fixture.Post), modelID(fixture.User)
	args := compilerir.GoNamedTypeIR{PackagePath: "example.test/capture", Name: "Args"}
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{{ModelID: post, Template: compilerir.ScopeTemplateIR{Name: "authority", Arguments: args, Expression: compilerir.ScopeExpressionIR{ModelID: post, Operator: "true"}}}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	body := compilerir.ScopeExpressionIR{ModelID: post, Operator: "and", Children: []compilerir.ScopeExpressionIR{
		{ModelID: post, Operator: "eq", FieldID: fieldID(fixture.PostTitle), Literals: []compilerir.TypedLiteralIR{{Kind: compilerir.LiteralString, Canonical: "ready"}}},
		{ModelID: post, Operator: "is", FieldID: fieldID(fixture.PostAuthor), RelationID: compilerir.RelationID(hex.EncodeToString(fixture.Authorship[:])), TargetModelID: user, Children: []compilerir.ScopeExpressionIR{
			{ModelID: user, Operator: "eq", FieldID: fieldID(fixture.UserName), Literals: []compilerir.TypedLiteralIR{{Kind: compilerir.LiteralString, Canonical: "enabled"}}},
		}},
	}}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{{ModelID: post, ScopeName: "authority", Template: compilerir.FactTemplateIR{Name: "ready", Arguments: args, Expression: body}}}); len(diagnostics) != 0 {
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
	bytes, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(bytes) != len(golem.SchemaDigest{}) {
		t.Fatalf("invalid contract fingerprint: %v", err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], bytes)
	document := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	old := fixture.Bundle
	fixture.Bundle = golem.GeneratedSchemaBundle(old.GenerationDigest(), old.GeneratorVersion(), old.TemplateABIVersion(), old.Model(), document, old.Providers()...)
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}
