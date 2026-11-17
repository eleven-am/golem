package runtime

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestMutationHistoryGraphReparentingCompletesBothRootAudiencesAndBranches(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture {
		return mutationHistoryGraphFixture(t, false)
	}})
	fixture.mutate(t, golem.EventCreated, 81, 1, "moving")
	fixture.mutate(t, golem.EventCreated, 82, 1, "other-branch")
	comments := mutationHistoryGraphComments(fixture.schema)
	if _, err := SystemCreate(fixture.ctx, fixture.app.System(), comments, mutationHistoryGraphCommentInput(fixture.schema, 83, 82, "witness")); err != nil {
		t.Fatal(err)
	}
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if _, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 81), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))); err != nil {
			return err
		}
		snapshot, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		graph, err := newMutationHistoryGraph(fixture.ctx, snapshot)
		if err != nil {
			return err
		}
		if len(graph.affectedRoots()) != 3 { // Both Users and the owning Post template.
			t.Fatalf("affected roots=%d want old/new User and Post", len(graph.affectedRoots()))
		}
		roots := graph.affectedRoots()
		for index := 1; index < len(roots); index++ {
			if roots[index-1].key >= roots[index].key {
				t.Fatal("affected roots are not canonical and deterministic")
			}
		}
		old := mutationHistoryGraphRoot(t, graph, fixture.schema.User, fixture.schema.UserID, 1)
		new := mutationHistoryGraphRoot(t, graph, fixture.schema.User, fixture.schema.UserID, 2)
		mutationHistoryGraphChildren(t, graph, mutationHistoryBefore, old.key, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.PostID, 81, 82)
		mutationHistoryGraphChildren(t, graph, mutationHistoryBefore, new.key, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.PostID)
		mutationHistoryGraphChildren(t, graph, mutationHistoryAfter, old.key, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.PostID, 82)
		mutationHistoryGraphChildren(t, graph, mutationHistoryAfter, new.key, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.PostID, 81)
		// The same Posts extent participates in two independent templates.
		// Capturing it once must retain the other branch's Comments extents,
		// including an explicit empty extent for the unrelated moving row.
		for _, phase := range []mutationHistoryPhase{mutationHistoryBefore, mutationHistoryAfter} {
			for _, root := range []mutationHistoryRoot{old, new} {
				posts, _ := graph.children(phase, root.key, policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
				for _, post := range posts {
					key, err := policyhistory.RowKey(graph.snapshot.registry, graph.snapshot.provider, post)
					if err != nil {
						return err
					}
					id := mutationHistoryGraphRowID(t, post, fixture.schema.PostID)
					if id == 81 {
						mutationHistoryGraphChildren(t, graph, phase, key, fixture.schema.PostComments, fixture.schema.Commenting, fixture.schema.CommentID)
					} else {
						mutationHistoryGraphChildren(t, graph, phase, key, fixture.schema.PostComments, fixture.schema.Commenting, fixture.schema.CommentID, 83)
					}
				}
			}
		}
		// Every accessor returns an owned container or immutable Row value.
		roots[0].key = "mutated"
		row, known := graph.root(mutationHistoryBefore, old.key)
		if !known || row == nil {
			t.Fatal("old source root is unknown")
		}
		*row = mutationdecode.Row{}
		if actual, _ := graph.root(mutationHistoryBefore, old.key); actual.ModelID() != policyir.ModelID(fixture.schema.User) {
			t.Fatal("root accessor exposed mutable storage")
		}
		children, _ := graph.children(mutationHistoryBefore, old.key, policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
		children[0] = mutationdecode.Row{}
		mutationHistoryGraphChildren(t, graph, mutationHistoryBefore, old.key, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.PostID, 81, 82)
		if _, known := graph.children(mutationHistoryBefore, old.key, policyir.FieldID(fixture.schema.PostComments), policyir.RelationID(fixture.schema.Commenting)); known {
			t.Fatal("undeclared/foreign parent extent became complete")
		}
		return graph.check()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMutationHistoryGraphRootCreateDeleteAndChildRewinds(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture {
		return mutationHistoryGraphFixture(t, false)
	}})
	fixture.mutate(t, golem.EventCreated, 91, 1, "deleted")
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if _, err := SystemTxDelete(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 91)); err != nil {
			return err
		}
		userTarget := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		if _, err := SystemTxDelete(fixture.ctx, transaction, fixture.users, userTarget); err != nil {
			return err
		}
		if _, err := SystemTxCreate(fixture.ctx, transaction, fixture.users, golem.GeneratedCreateInput[policyFactRuntimeUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, golem.GeneratedEqualField[policyFactRuntimeUser, golem.UUID](fixture.schema.UserID), golem.UUID{15: 3}),
			golem.GeneratedCreateFieldValue(fixture.schema.User, golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName), "created"))); err != nil {
			return err
		}
		for _, entry := range []struct{ id, author byte }{{92, 2}, {93, 3}} {
			if _, err := SystemTxCreate(fixture.ctx, transaction, fixture.posts, golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: entry.id}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: entry.author}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "created"))); err != nil {
				return err
			}
		}
		snapshot, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		graph, err := newMutationHistoryGraph(fixture.ctx, snapshot)
		if err != nil {
			return err
		}
		old := mutationHistoryGraphRoot(t, graph, fixture.schema.User, fixture.schema.UserID, 1)
		new := mutationHistoryGraphRoot(t, graph, fixture.schema.User, fixture.schema.UserID, 3)
		if row, known := graph.root(mutationHistoryAfter, old.key); !known || row != nil {
			t.Fatal("deleted root was not known absent after")
		}
		if row, known := graph.root(mutationHistoryBefore, new.key); !known || row != nil {
			t.Fatal("created root was not known absent before")
		}
		if _, known := graph.root(mutationHistoryAfter, "unobserved identity"); known {
			t.Fatal("unknown root was converted to absence")
		}
		mutationHistoryGraphChildren(t, graph, mutationHistoryBefore, old.key, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.PostID, 91)
		mutationHistoryGraphChildren(t, graph, mutationHistoryAfter, new.key, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.PostID, 93)
		other := mutationHistoryGraphRoot(t, graph, fixture.schema.User, fixture.schema.UserID, 2)
		mutationHistoryGraphChildren(t, graph, mutationHistoryBefore, other.key, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.PostID)
		mutationHistoryGraphChildren(t, graph, mutationHistoryAfter, other.key, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.PostID, 92)
		if _, known := graph.children(mutationHistoryBefore, new.key, policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship)); known {
			t.Fatal("absent parent acquired a fabricated relation extent")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMutationHistoryGraphNullCorrelationIsCompleteEmpty(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture {
		return mutationHistoryGraphFixture(t, true)
	}})
	fixture.mutate(t, golem.EventCreated, 94, 1, "set-null")
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		target := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		if _, err := SystemTxDelete(fixture.ctx, transaction, fixture.users, target); err != nil {
			return err
		}
		snapshot, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		graph, err := newMutationHistoryGraph(fixture.ctx, snapshot)
		if err != nil {
			return err
		}
		post := mutationHistoryGraphRoot(t, graph, fixture.schema.Post, fixture.schema.PostID, 94)
		mutationHistoryGraphChildren(t, graph, mutationHistoryBefore, post.key, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.UserID, 1)
		mutationHistoryGraphChildren(t, graph, mutationHistoryAfter, post.key, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.UserID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMutationHistoryGraphPoisoningBudgetAndTransactionEpoch(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture {
		return mutationHistoryGraphFixture(t, false)
	}})
	fixture.mutate(t, golem.EventCreated, 95, 1, "before")
	rollback := errors.New("roll back graph owner")
	var retained *mutationHistoryGraph
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if _, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 95), mutationHistoryRuntimeUpdate(fixture, "first")); err != nil {
			return err
		}
		snapshot, err := newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		graph, err := newMutationHistoryGraph(fixture.ctx, snapshot)
		if err != nil {
			return err
		}
		if _, err := SystemTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 95), mutationHistoryRuntimeUpdate(fixture, "second")); err != nil {
			return err
		}
		if err := graph.check(); err == nil {
			t.Fatal("graph accepted a subsequent physical application")
		}
		if partial, err := newMutationHistoryGraph(fixture.ctx, snapshot); err == nil || partial != nil || snapshot.failure == nil {
			t.Fatal("stale graph assembly did not poison its reader")
		}
		snapshot, err = newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		snapshot.nodes = maxMutationHistoryNodes - 1
		partial, err := newMutationHistoryGraph(fixture.ctx, snapshot)
		mutationHistoryLimit(t, err, golem.LimitPolicyHistoryNodes)
		if partial != nil || snapshot.failure == nil {
			t.Fatal("budget overflow published a partial graph")
		}
		if _, err := snapshot.read(fixture.ctx, policyir.ModelID(fixture.schema.User), nil, mutationHistoryAfter, 10); err == nil {
			t.Fatal("failed graph reader was reused as complete evidence")
		}
		snapshot, err = newMutationHistorySnapshot(transaction.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		retained, err = newMutationHistoryGraph(fixture.ctx, snapshot)
		if err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := retained.check(); err == nil {
		t.Fatal("rolled-back graph retained snapshot authority")
	}
	mutationHistoryRuntimeTitle(t, fixture, 95, "before")
}

func mutationHistoryGraphFixture(t *testing.T, nullable bool) schematest.GraphFixture {
	t.Helper()
	fixture := mutationHistoryFixture(t)
	if nullable {
		fixture = mutationHistoryRuntimeDeleteFixture(t, compilerir.ActionSetNull)
	}
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	modelID := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	fieldID := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	relationID := func(id golem.RelationID) compilerir.RelationID {
		return compilerir.RelationID(hex.EncodeToString(id[:]))
	}
	user, post, comment := modelID(fixture.User), modelID(fixture.Post), modelID(fixture.Comment)
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
	}
	args := compilerir.GoNamedTypeIR{PackagePath: "example.test/history", Name: "GraphArgs"}
	name, _ := fixture.Registry.Field(fixture.User, fixture.UserName)
	body, _ := fixture.Registry.Field(fixture.Comment, fixture.CommentBody)
	scopes = append(scopes, scopecontract.Declaration{ModelID: post, Template: compilerir.ScopeTemplateIR{Name: "author", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "Name", Type: name.LogicalType()}}, Expression: compilerir.ScopeExpressionIR{
		ModelID: post, Operator: "is", FieldID: fieldID(fixture.PostAuthor), RelationID: relationID(fixture.Authorship), TargetModelID: user, Children: []compilerir.ScopeExpressionIR{{ModelID: user, Operator: "eq", FieldID: fieldID(fixture.UserName), Parameter: "Name"}},
	}}})
	facts = append(facts, scopecontract.FactDeclaration{ModelID: user, ScopeName: "named", Template: compilerir.FactTemplateIR{Name: "commented", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: body.LogicalType()}}, Expression: compilerir.ScopeExpressionIR{
		ModelID: user, Operator: "some", FieldID: fieldID(fixture.UserPosts), RelationID: relationID(fixture.Authorship), TargetModelID: post, Children: []compilerir.ScopeExpressionIR{{ModelID: post, Operator: "some", FieldID: fieldID(fixture.PostComments), RelationID: relationID(fixture.Commenting), TargetModelID: comment, Children: []compilerir.ScopeExpressionIR{{ModelID: comment, Operator: "eq", FieldID: fieldID(fixture.CommentBody), Parameter: "State"}}}},
	}}})
	if diagnostics := scopecontract.Normalize(&compilation, scopes); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, facts); len(diagnostics) != 0 {
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
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), mutationHistoryRuntimeDigest(t, string(fingerprint)), payload)
	fixture.Bundle = golem.GeneratedSchemaBundle(fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), fixture.Bundle.Model(), contract, fixture.Bundle.Providers()...)
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func mutationHistoryGraphRoot(t *testing.T, graph *mutationHistoryGraph, model golem.ModelID, field golem.FieldID, id byte) mutationHistoryRoot {
	t.Helper()
	for _, root := range graph.affectedRoots() {
		if root.model != policyir.ModelID(model) {
			continue
		}
		for _, phase := range []mutationHistoryPhase{mutationHistoryBefore, mutationHistoryAfter} {
			if row, known := graph.root(phase, root.key); known && row != nil && mutationHistoryGraphRowID(t, *row, field) == id {
				return root
			}
		}
	}
	t.Fatalf("affected model %x root %d missing", model, id)
	return mutationHistoryRoot{}
}

func mutationHistoryGraphRowID(t *testing.T, row mutationdecode.Row, field golem.FieldID) byte {
	t.Helper()
	cell, ok := row.Cell(policyir.FieldID(field))
	if !ok {
		t.Fatal("row identity scalar absent")
	}
	value, _ := cell.PolicyValue()
	id, ok := value.UUID()
	if !ok {
		t.Fatal("row identity is not UUID")
	}
	return id[15]
}

func mutationHistoryGraphChildren(t *testing.T, graph *mutationHistoryGraph, phase mutationHistoryPhase, parent string, field golem.FieldID, relation golem.RelationID, key golem.FieldID, expected ...byte) {
	t.Helper()
	rows, known := graph.children(phase, parent, policyir.FieldID(field), policyir.RelationID(relation))
	if !known || len(rows) != len(expected) {
		t.Fatalf("extent phase=%d field=%x known=%v rows=%d want=%v", phase, field, known, len(rows), expected)
	}
	seen := map[byte]bool{}
	for _, row := range rows {
		seen[mutationHistoryGraphRowID(t, row, key)] = true
	}
	for _, id := range expected {
		if !seen[id] {
			t.Fatalf("extent missing identity %d", id)
		}
	}
}

func mutationHistoryGraphComments(fixture schematest.GraphFixture) golem.ModelDescriptor[policyFactRuntimeComment] {
	return golem.GeneratedModelDescriptor[policyFactRuntimeComment](fixture.Comment, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.CommentID, fixture.CommentPostID, fixture.CommentBody, fixture.CommentRowVersion}, nil,
		[]golem.IdentityMetadata{golem.GeneratedIdentityMetadata(fixture.Comment, fixture.CommentKey, golem.PrimaryIdentity, fixture.CommentID)},
		[]golem.RelationMetadata{golem.GeneratedRelationMetadata(fixture.Comment, fixture.Post, fixture.CommentPost, fixture.Commenting, golem.RelationSource, golem.RelationToOne)},
	))
}

func mutationHistoryGraphCommentInput(fixture schematest.GraphFixture, id, post byte, body string) golem.CreateInput[policyFactRuntimeComment] {
	return golem.GeneratedCreateInput[policyFactRuntimeComment](fixture.Comment,
		golem.GeneratedCreateFieldValue(fixture.Comment, golem.GeneratedEqualField[policyFactRuntimeComment, golem.UUID](fixture.CommentID), golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.Comment, golem.GeneratedEqualField[policyFactRuntimeComment, golem.UUID](fixture.CommentPostID), golem.UUID{15: post}),
		golem.GeneratedCreateFieldValue(fixture.Comment, golem.GeneratedTextField[policyFactRuntimeComment, string](fixture.CommentBody), body),
	)
}
