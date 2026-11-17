package observation

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/history"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestHistoricalAdditionalMainChainScopeUsesCapturedSiblingIdentity(t *testing.T) {
	fixture := newHistoricalFixture(t)
	f := fixture.graph
	graph, changed := historicalGraph(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	owner := historicalOwnerCondition(t, fixture, false)
	selected := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, owner)
	for _, allowed := range []bool{true, false} {
		local := historicalScalar(t, f.Registry, f.Post, f.AuthorID, ir.UUIDValue([16]byte{15: 1}))
		if !allowed {
			local = historicalScalar(t, f.Registry, f.Post, f.AuthorID, ir.UUIDValue([16]byte{15: 2}))
		}
		scope := must(ir.NewFactScope(local)) // deliberately local and unbound
		fact := must(ir.NewFact(ir.ModelID(f.Post), must(ir.FactIdentity(ir.ModelID(f.Post), "post_domain")), must(ir.NewConstant(ir.ModelID(f.Post), true)), scope))
		question := must(ir.NewLogical(ir.ModelID(f.Post), ir.LogicalAnd, []ir.Condition{fact, selected}))
		plan := must(NewHistorical(question, nil))
		candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity})
		if err != nil {
			t.Fatal(err)
		}
		if !allowed {
			if len(candidates) != 0 {
				t.Fatal("another actor's main-chain scope borrowed the primary owner's authority")
			}
			continue
		}
		if len(candidates) != 2 {
			t.Fatalf("captured sibling candidates=%d want2", len(candidates))
		}
		seen := map[string]bool{}
		for _, candidate := range candidates {
			scopes := candidate.AdditionalScopes()
			if len(scopes) != 1 || scopes[0].Offset() != 0 || scopes[0].Identity().ModelID() != ir.ModelID(f.Post) {
				t.Fatal("main-chain scope lost its exact occurrence")
			}
			if _, known := graph.FindRoot(history.Before, scopes[0].Key()); known {
				t.Fatal("acceptance accidentally promoted captured Post to a root")
			}
			if _, known := graph.FindRow(history.Before, scopes[0].Key()); !known {
				t.Fatal("scope did not name a captured row")
			}
			if scopes[0].Key() != must(history.RowKey(f.Registry, ir.ProviderSQLite, candidate.CutIdentity())) {
				t.Fatal("scope borrowed a different sibling cut")
			}
			seen[scopes[0].Key()] = true
			scopes[0] = HistoricalScope{}
			if candidate.AdditionalScopes()[0].Key() == "" {
				t.Fatal("additional scope accessor exposed slice storage")
			}
		}
		if len(seen) != 2 {
			t.Fatal("independent sibling candidates shared one scope identity")
		}
		if _, err := plan.BindChange(changed, f.Registry, ir.ProviderSQLite); err == nil {
			t.Fatal("multi-scope historical plan entered legacy live binding")
		}
		legacy := must(New(question, nil))
		if _, err := legacy.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity}); err == nil {
			t.Fatal("legacy planner silently acquired multi-owner capability")
		}
	}
}

func TestHistoricalAdditionalSideScopeUsesSameRecordedParentAndLocalGuard(t *testing.T) {
	fixture := historicalMultiScopeFixture(t)
	f := fixture.graph
	graph, changed := historicalMultiScopeGraph(t, fixture)
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	post := historicalMultiScopePost(t, fixture)
	user := historicalOwnerCondition(t, fixture, false)
	local := historicalScalar(t, f.Registry, f.User, f.UserName, must(ir.StringValue("owner")))
	child := must(ir.NewLogical(ir.ModelID(f.User), ir.LogicalAnd, []ir.Condition{user, local}))
	side := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, child)
	question := must(ir.NewLogical(ir.ModelID(f.Post), ir.LogicalAnd, []ir.Condition{post, side}))
	plan := must(NewHistorical(question, nil))
	candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity})
	if err != nil {
		t.Fatal(err)
	}
	var sideCount int
	for _, candidate := range candidates {
		if candidate.OwnerIdentity().ModelID() != ir.ModelID(f.Post) {
			if len(candidate.Guards()) != 1 || candidate.Guards()[0].ModelID() != ir.ModelID(f.User) {
				t.Fatal("equivalent main-chain candidate bypassed the owner's ordinary local guard")
			}
			continue
		}
		sideCount++
		scopes := candidate.AdditionalScopes()
		if len(scopes) != 1 || scopes[0].Identity().ModelID() != ir.ModelID(f.User) || scopes[0].Offset() != 0 || scopes[0].occurrence == "" {
			t.Fatal("side scope is not tied to its recorded Post occurrence")
		}
		if len(candidate.Guards()) != 1 || candidate.Guards()[0].ModelID() != ir.ModelID(f.User) {
			t.Fatal("side target's ordinary local guard disappeared")
		}
		if got, want := must(ir.ConditionFingerprint(candidate.Prefix())), must(ir.ConditionFingerprint(historicalScalar(t, f.Registry, f.Post, f.PostID, ir.UUIDValue([16]byte{15: 11})))); got != want {
			t.Fatal("current prefix rejoins the old side owner")
		}
	}
	if sideCount == 0 {
		t.Fatal("positive conjunctive side scope did not produce a candidate")
	}
	// A scope and repeated reference to it at the same side occurrence form
	// one requirement; no duplicate observer or actor truth cache is needed.
	duplicate := must(NewHistorical(question, nil, question))
	dups := must(duplicate.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity}))
	if len(dups) != len(candidates) {
		t.Fatal("same occurrence duplicated candidate scope conjunctions")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := plan.HistoricalCandidates(ctx, f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity}); !errors.Is(err, context.Canceled) {
		t.Fatalf("multi-scope matching ignored cancellation: %v", err)
	}
}

func TestHistoricalAdditionalSideScopeRefusesConditionalAndMissingWitnesses(t *testing.T) {
	fixture := historicalMultiScopeFixture(t)
	f := fixture.graph
	graph, changed := historicalMultiScopeGraph(t, fixture)
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	post := historicalMultiScopePost(t, fixture)
	user := historicalOwnerCondition(t, fixture, false)
	side := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, user)
	for _, operation := range []ir.LogicalOperator{ir.LogicalOr, ir.LogicalNot} {
		children := []ir.Condition{side}
		if operation == ir.LogicalOr {
			children = append(children, historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("video"))))
		}
		conditional := must(ir.NewLogical(ir.ModelID(f.Post), operation, children))
		plan := must(NewHistorical(must(ir.NewLogical(ir.ModelID(f.Post), ir.LogicalAnd, []ir.Condition{post, conditional})), nil))
		candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity})
		var failure *HistoricalCoverageError
		if !errors.As(err, &failure) || candidates != nil {
			t.Fatalf("conditional side scope was conjoined globally: %v", err)
		}
	}
	// A separately captured Post points to an unrecorded User. It is a valid
	// registered root with complete Comments, not an invalid partial graph.
	input := graph.Input()
	missing := historicalRow(t, f.Registry, f.Post, mutationdecode.Value(ir.FieldID(f.PostID), ir.UUIDValue([16]byte{15: 99})), mutationdecode.Value(ir.FieldID(f.AuthorID), ir.UUIDValue([16]byte{15: 98})), mutationdecode.Value(ir.FieldID(f.PostTitle), must(ir.StringValue("video"))))
	root := history.Root{Identity: must(historicalPrimaryRow(f.Registry, missing))}
	for _, phase := range []history.Phase{history.Before, history.After} {
		input.Rows = append(input.Rows, history.PhaseRow{Phase: phase, Row: missing})
		ref := history.RowRef(len(input.Rows))
		presence := history.Presence{Present: true, Row: ref}
		if phase == history.Before {
			root.Before = presence
		} else {
			root.After = presence
		}
		input.Extents = append(input.Extents, history.Extent{Phase: phase, Parent: ref, Field: ir.FieldID(f.PostComments), Relation: ir.RelationID(f.Commenting)})
	}
	input.Roots = append(input.Roots, root)
	graph = must(history.New(f.Registry, ir.ProviderSQLite, input))
	identity = must(mutationdecode.PrimaryIdentity(f.Registry, missing))
	plan := must(NewHistorical(must(ir.NewLogical(ir.ModelID(f.Post), ir.LogicalAnd, []ir.Condition{post, side})), nil))
	candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Post), Before: &identity})
	var failure *HistoricalCoverageError
	if !errors.As(err, &failure) || candidates != nil {
		t.Fatalf("unknown side witness was treated as absent or another owner: %v", err)
	}
}

func historicalMultiScopeFixture(t *testing.T) historicalFixture {
	t.Helper()
	fixture := newHistoricalFixture(t)
	f := &fixture.graph
	compilation := must(schema.DecodeActive(f.Bundle))
	var scopes []scopecontract.Declaration
	var facts []scopecontract.FactDeclaration
	for _, owner := range compilation.Contract.Models {
		names := map[compilerir.ScopeTemplateID]string{}
		for _, scope := range owner.ScopeTemplates {
			scopes = append(scopes, scopecontract.Declaration{ModelID: owner.ModelID, Template: scope})
			names[scope.ID] = scope.Name
		}
		for _, fact := range owner.FactTemplates {
			facts = append(facts, scopecontract.FactDeclaration{ModelID: owner.ModelID, ScopeName: names[fact.ScopeTemplateID], Template: fact})
		}
	}
	model := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	field := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	args := compilerir.GoNamedTypeIR{PackagePath: "example.test/multiowner", Name: "Arguments"}
	title, _ := f.Registry.Field(f.Post, f.PostTitle)
	body, _ := f.Registry.Field(f.Comment, f.CommentBody)
	scopes = append(scopes, scopecontract.Declaration{ModelID: model(f.Post), Template: compilerir.ScopeTemplateIR{Name: "named", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "Name", Type: title.LogicalType()}}, Expression: compilerir.ScopeExpressionIR{ModelID: model(f.Post), Operator: "eq", FieldID: field(f.PostTitle), Parameter: "Name"}}})
	if diagnostics := scopecontract.Normalize(&compilation, scopes); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	facts = append(facts, scopecontract.FactDeclaration{ModelID: model(f.Post), ScopeName: "named", Template: compilerir.FactTemplateIR{Name: "ready", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: body.LogicalType()}}, Expression: compilerir.ScopeExpressionIR{ModelID: model(f.Post), Operator: "some", FieldID: field(f.PostComments), RelationID: compilerir.RelationID(hex.EncodeToString(f.Commenting[:])), TargetModelID: model(f.Comment), Children: []compilerir.ScopeExpressionIR{{ModelID: model(f.Comment), Operator: "eq", FieldID: field(f.CommentBody), Parameter: "State"}}}}})
	if diagnostics := scopecontract.NormalizeFacts(&compilation, facts); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	payload := must(compilerir.CanonicalContract(compilation.Contract))
	document := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), historicalDigest(t, string(must(compilerir.ContractFingerprint(compilation.Contract)))), payload)
	f.Bundle = golem.GeneratedSchemaBundle(f.Bundle.GenerationDigest(), f.Bundle.GeneratorVersion(), f.Bundle.TemplateABIVersion(), f.Bundle.Model(), document, f.Bundle.Providers()...)
	f.Registry = must(schema.New(f.Bundle))
	return fixture
}

func historicalMultiScopePost(t *testing.T, fixture historicalFixture) ir.Condition {
	t.Helper()
	f := fixture.graph
	model, _ := f.Registry.Model(f.Post)
	scope, fact := model.ScopeTemplates()[0], model.FactTemplates()[0]
	title := golem.GeneratedTextField[sourcePost, string](f.PostTitle)
	comments := golem.GeneratedToMany[sourcePost, sourceComment](f.PostComments, f.Commenting, f.Comment)
	body := golem.GeneratedTextField[sourceComment, string](f.CommentBody)
	rules := golem.NewRules[sourcePost]()
	rules.BindFactScope(golem.GeneratedBoundFactScope(f.Post, string(scope.ID), historicalDigest(t, string(scope.Fingerprint)), title.Eq("video"), golem.GeneratedTemplateArgument("Name", "video")))
	ready := rules.BindFact(golem.GeneratedBoundFact(f.Post, string(fact.ID), historicalDigest(t, string(fact.Fingerprint)), fact.Name, comments.Some(body.Eq("ready")), golem.GeneratedTemplateArgument("State", "ready")))
	rules.CanRead(ready)
	policy := must(policybind.Policy(must(rules.Freeze(f.Post)), f.Registry, ir.PortableProviders()))
	return must(resolve.RowConstraint(policy, ir.ActionRead, ir.ModelID(f.Post)))
}

func historicalMultiScopeGraph(t *testing.T, fixture historicalFixture) (history.Graph, mutationdecode.Row) {
	t.Helper()
	graph, changed := historicalGraph(t, fixture, false)
	input := graph.Input()
	f := fixture.graph
	var root history.Root
	for index, phaseRow := range input.Rows {
		if phaseRow.Row.ModelID() != ir.ModelID(f.Post) {
			continue
		}
		cell, _ := phaseRow.Row.Cell(ir.FieldID(f.PostID))
		value, _ := cell.PolicyValue()
		id, _ := value.UUID()
		if id != ([16]byte{15: 11}) {
			continue
		}
		root.Identity = must(historicalPrimaryRow(f.Registry, phaseRow.Row))
		presence := history.Presence{Present: true, Row: history.RowRef(index + 1)}
		if phaseRow.Phase == history.Before {
			root.Before = presence
		} else {
			root.After = presence
		}
	}
	input.Roots = append(input.Roots, root)
	return must(history.New(f.Registry, ir.ProviderSQLite, input)), changed
}
