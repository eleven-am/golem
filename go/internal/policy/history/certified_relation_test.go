package history_test

import (
	"context"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestCertifiedChildrenDerivesInverseWithoutChangingGraph(t *testing.T) {
	fixture, input := graphInput(t)
	graph := evaluationReadback(t, fixture.Registry, input)
	before := graph.Input()
	for _, entry := range input.Rows {
		if entry.Row.ModelID() != policyir.ModelID(fixture.Post) {
			continue
		}
		key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, entry.Row)
		if err != nil {
			t.Fatal(err)
		}
		if _, known := graph.Children(entry.Phase, key, policyir.FieldID(fixture.PostAuthor), policyir.RelationID(fixture.Authorship)); known {
			t.Fatal("inverse unexpectedly persisted")
		}
		rows, known, err := graph.CertifiedChildren(fixture.Registry, entry.Phase, key, policyir.FieldID(fixture.PostAuthor), policyir.RelationID(fixture.Authorship))
		if err != nil || !known || len(rows) != 1 {
			t.Fatalf("inverse proof: known=%v rows=%d err=%v", known, len(rows), err)
		}
		want, _ := entry.Row.Cell(policyir.FieldID(fixture.AuthorID))
		got, _ := rows[0].Cell(policyir.FieldID(fixture.UserID))
		left, _ := want.PolicyValue()
		right, _ := got.PolicyValue()
		if !mutationdecode.EqualValue(left, right) {
			t.Fatal("inverse crossed phase or parent identity")
		}
	}
	// Full evaluation can use the same certified inverse, then return to the
	// explicitly complete root extent. This is a finite path through a cycle.
	providers := policysql.SchemaResolver(fixture.Registry).Providers()
	name := evaluationScalar(t, fixture.Registry, fixture.User, fixture.UserName, "source", policyir.OperatorEqual)
	requirements, err := operator.ValidateShape(policyir.OperatorRelationIs, operator.Shape{Node: policyir.ConditionRelation, Operand: policyir.NoOperand(), Mode: policyir.ComparisonSensitive, Cardinality: policyir.RelationToOne, HasChild: true, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	author, err := policyir.NewRelation(policyir.ModelID(fixture.Post), policyir.FieldID(fixture.PostAuthor), policyir.RelationID(fixture.Authorship), policyir.ModelID(fixture.User), policyir.RelationToOne, policyir.OperatorRelationIs, &name, requirements)
	if err != nil {
		t.Fatal(err)
	}
	condition := evaluationRelation(t, providers, fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, policyir.OperatorRelationSome, author)
	key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, input.Roots[0].Identity)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := graph.Evaluate(context.Background(), fixture.Registry, providers, history.Before, key, condition); err != nil || !got {
		t.Fatalf("certified evaluator lookup: %v %v", got, err)
	}
	if !reflect.DeepEqual(before, graph.Input()) {
		t.Fatal("derived inverse was persisted in the graph")
	}
	wrong := policyir.RelationID(fixture.Authorship)
	wrong[0] ^= 0xff
	if _, _, err := graph.CertifiedChildren(fixture.Registry, history.Before, key, policyir.FieldID(fixture.UserPosts), wrong); err == nil {
		t.Fatal("foreign relation identity accepted")
	}
	generation := fixture.Bundle.GenerationDigest()
	generation[0] ^= 0xff
	other, err := schema.New(golem.GeneratedSchemaBundle(generation, fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), fixture.Bundle.Model(), fixture.Bundle.Contract(), fixture.Bundle.Providers()...))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := graph.CertifiedChildren(other, history.Before, key, policyir.FieldID(fixture.UserPosts), policyir.RelationID(fixture.Authorship)); err == nil {
		t.Fatal("foreign generation accepted")
	}
}

func TestCertifiedChildrenDistinguishesAbsenceAndPartialInventories(t *testing.T) {
	for _, named := range []bool{false, true} {
		fixture := certifiedScalarFixture(t, named, true)
		for _, test := range []struct {
			name                                                     string
			null, oldTarget, currentTarget, differentCase, duplicate bool
			known                                                    bool
			count                                                    int
			invalid                                                  bool
		}{
			{name: "unknown target"},
			{name: "NULL correlation", null: true, known: true},
			{name: "opposite phase target", oldTarget: true, known: !named},
			{name: "captured same phase target", currentTarget: true, known: true, count: 1},
			{name: "unique value reused by another primary row", oldTarget: true, currentTarget: true, known: true, count: 1},
			{name: "binary text does not fold case", currentTarget: true, differentCase: true, known: !named, count: 1},
			{name: "conflicting named unique values", currentTarget: true, duplicate: true, known: !named, count: 1, invalid: named},
		} {
			t.Run(map[bool]string{false: "primary/", true: "named/"}[named]+test.name, func(t *testing.T) {
				input := history.Input{}
				addRoot := func(row mutationdecode.Row, field golem.FieldID, phase history.Phase) {
					input.Rows = append(input.Rows, history.PhaseRow{Phase: phase, Row: row})
					cell, _ := row.Cell(policyir.FieldID(field))
					identity := graphRow(t, fixture.Registry, golem.ModelID(row.ModelID()), cell)
					root := history.Root{Identity: identity}
					if phase == history.Before {
						root.Before = history.Presence{Present: true, Row: history.RowRef(len(input.Rows))}
					} else {
						root.After = history.Presence{Present: true, Row: history.RowRef(len(input.Rows))}
					}
					input.Roots = append(input.Roots, root)
				}
				if test.oldTarget {
					addRoot(graphUser(t, fixture, 1, "Owner"), fixture.UserID, history.Before)
				}
				if test.currentTarget {
					id := byte(1)
					if test.oldTarget {
						id = 2
					}
					name := "Owner"
					if test.differentCase {
						name = "owner"
					}
					addRoot(graphUser(t, fixture, id, name), fixture.UserID, history.After)
					if test.duplicate && named {
						addRoot(graphUser(t, fixture, 3, "Owner"), fixture.UserID, history.After)
					}
				}
				author := policyir.UUIDValue([16]byte{15: 1})
				if named {
					author = graphText(t, "Owner")
				}
				// In the primary-key control, target row 2 is deliberately a
				// different identity; only named keys may move between rows.
				if !named && test.oldTarget && test.currentTarget {
					author = policyir.UUIDValue([16]byte{15: 2})
				}
				cell := mutationdecode.Value(policyir.FieldID(fixture.AuthorID), author)
				if test.null {
					cell = mutationdecode.Null(policyir.FieldID(fixture.AuthorID))
				}
				post := graphRow(t, fixture.Registry, fixture.Post, mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: 51})), cell, mutationdecode.Value(policyir.FieldID(fixture.PostTitle), graphText(t, "ready")))
				addRoot(post, fixture.PostID, history.After)
				graph := evaluationReadback(t, fixture.Registry, input)
				key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, post)
				if err != nil {
					t.Fatal(err)
				}
				rows, known, err := graph.CertifiedChildren(fixture.Registry, history.After, key, policyir.FieldID(fixture.PostAuthor), policyir.RelationID(fixture.Authorship))
				if test.invalid {
					if err == nil {
						t.Fatal("conflicting uniqueness was certified")
					}
					return
				}
				wantCount := test.count
				if named && test.differentCase {
					wantCount = 0
				}
				if err != nil || known != test.known || len(rows) != wantCount {
					t.Fatalf("known=%v want=%v rows=%d want=%d err=%v", known, test.known, len(rows), wantCount, err)
				}
				if test.currentTarget {
					for _, root := range input.Roots {
						if root.Identity.ModelID() != policyir.ModelID(fixture.User) || !root.After.Present {
							continue
						}
						key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, root.Identity)
						if err != nil {
							t.Fatal(err)
						}
						if _, known, err := graph.CertifiedChildren(fixture.Registry, history.After, key, policyir.FieldID(fixture.UserPosts), policyir.RelationID(fixture.Authorship)); err != nil || known {
							t.Fatalf("partial to-many inventory certified: known=%v err=%v", known, err)
						}
					}
				}
			})
		}
	}
}

func TestCertifiedChildrenMissingParentCorrelationRemainsUnknown(t *testing.T) {
	fixture := certifiedScalarFixture(t, false, false)
	user := graphUser(t, fixture, 1, "Owner")
	post := graphRow(t, fixture.Registry, fixture.Post,
		mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: 51})),
		mutationdecode.Value(policyir.FieldID(fixture.PostTitle), graphText(t, "ready")))
	userID, _ := user.Cell(policyir.FieldID(fixture.UserID))
	postID, _ := post.Cell(policyir.FieldID(fixture.PostID))
	graph := evaluationReadback(t, fixture.Registry, history.Input{
		Rows: []history.PhaseRow{{Phase: history.After, Row: user}, {Phase: history.After, Row: post}},
		Roots: []history.Root{
			{Identity: graphRow(t, fixture.Registry, fixture.User, userID), After: history.Presence{Present: true, Row: 1}},
			{Identity: graphRow(t, fixture.Registry, fixture.Post, postID), After: history.Presence{Present: true, Row: 2}},
		},
	})
	key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, post)
	if err != nil {
		t.Fatal(err)
	}
	if rows, known, err := graph.CertifiedChildren(fixture.Registry, history.After, key, policyir.FieldID(fixture.PostAuthor), policyir.RelationID(fixture.Authorship)); err != nil || known || len(rows) != 0 {
		t.Fatalf("missing correlation certified: known=%v rows=%d err=%v", known, len(rows), err)
	}
}

func TestCertifiedChildrenCompositeStringUniqueKey(t *testing.T) {
	fixture, authorName := graphCompositeStringFixture(t)
	user := graphUserComposite(t, fixture, "tenant", "Owner")
	post := graphRow(t, fixture.Registry, fixture.Post, mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: 51})), mutationdecode.Value(policyir.FieldID(fixture.AuthorID), graphText(t, "tenant")), mutationdecode.Value(policyir.FieldID(authorName), graphText(t, "Owner")), mutationdecode.Value(policyir.FieldID(fixture.PostTitle), graphText(t, "ready")))
	input := history.Input{Rows: []history.PhaseRow{{Phase: history.After, Row: user}, {Phase: history.After, Row: post}}, Roots: []history.Root{{Identity: user, After: history.Presence{Present: true, Row: 1}}}, Extents: []history.Extent{{Phase: history.After, Parent: 1, Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship), Children: []history.RowRef{2}}}}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		graph, err := history.New(fixture.Registry, provider, input)
		if err != nil {
			t.Fatal(err)
		}
		key, err := history.RowKey(fixture.Registry, provider, post)
		if err != nil {
			t.Fatal(err)
		}
		rows, known, err := graph.CertifiedChildren(fixture.Registry, history.After, key, policyir.FieldID(fixture.PostAuthor), policyir.RelationID(fixture.Authorship))
		if err != nil || !known || len(rows) != 1 {
			t.Fatalf("composite proof: known=%v rows=%d err=%v", known, len(rows), err)
		}
	}
}
func graphUserComposite(t *testing.T, fixture schematest.GraphFixture, tenant, name string) mutationdecode.Row {
	return graphRow(t, fixture.Registry, fixture.User, mutationdecode.Value(policyir.FieldID(fixture.UserID), graphText(t, tenant)), mutationdecode.Value(policyir.FieldID(fixture.UserName), graphText(t, name)))
}

// Independent scalar roots deliberately capture no relation extents. This
// tests certification from partial inventories, including named-key reuse.
func certifiedScalarFixture(t *testing.T, named, captureOwner bool) schematest.GraphFixture {
	t.Helper()
	fixture := schematest.NewSubscribedGraph(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	mid := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	fid := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	authorType := compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}
	if named {
		authorType.Kind = compilerir.TypeString
	}
	for index := range compilation.Model.Models {
		model := &compilation.Model.Models[index]
		if model.ID == mid(fixture.Post) {
			for field := range model.Fields {
				if model.Fields[field].ID == fid(fixture.AuthorID) {
					model.Fields[field].Scalar.Nullable = true
					model.Fields[field].Scalar.Type = authorType
				}
			}
		}
		if named && model.ID == mid(fixture.User) {
			model.Uniques = append(model.Uniques, compilerir.KeyIR{ID: compilerir.KeyID("000000000000000000000000000000ef"), Kind: compilerir.KeyUnique, LogicalName: "Name", PhysicalName: "unique_users_name", Fields: []compilerir.FieldID{fid(fixture.UserName)}})
		}
	}
	if named {
		for index := range compilation.Model.Relations {
			if compilation.Model.Relations[index].ID == compilerir.RelationID(hex.EncodeToString(fixture.Authorship[:])) {
				compilation.Model.Relations[index].RemoteFields = []compilerir.FieldID{fid(fixture.UserName)}
			}
		}
	}
	// The modified nullable/type/key declarations must be reflected in the
	// subscribed model's event document before the normal bootstrap agreement.
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
		}
	}
	args := compilerir.GoNamedTypeIR{PackagePath: "example.test/history", Name: "CertifiedArgs"}
	scopes := []scopecontract.Declaration{
		{ModelID: mid(fixture.User), Template: compilerir.ScopeTemplateIR{Name: "named", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "Name", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}}}, Expression: compilerir.ScopeExpressionIR{ModelID: mid(fixture.User), Operator: "eq", FieldID: fid(fixture.UserName), Parameter: "Name"}}},
		{ModelID: mid(fixture.Post), Template: compilerir.ScopeTemplateIR{Name: "owned", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "Owner", Type: authorType}}, Expression: compilerir.ScopeExpressionIR{ModelID: mid(fixture.Post), Operator: "eq", FieldID: fid(fixture.AuthorID), Parameter: "Owner"}}},
	}
	if !captureOwner {
		scopes[1].Template.Parameters = nil
		scopes[1].Template.Expression = compilerir.ScopeExpressionIR{ModelID: mid(fixture.Post), Operator: "true"}
	}
	if diagnostics := scopecontract.Normalize(&compilation, scopes); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{{ModelID: mid(fixture.Post), ScopeName: "owned", Template: compilerir.FactTemplateIR{Name: "ready", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}}}, Expression: compilerir.ScopeExpressionIR{ModelID: mid(fixture.Post), Operator: "eq", FieldID: fid(fixture.PostTitle), Parameter: "State"}}}}); len(diagnostics) != 0 {
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
	return fixture
}
