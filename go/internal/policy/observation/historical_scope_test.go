package observation

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/history"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func TestHistoricalRelatedScopeObservesLossCreationAndIndependentWitnesses(t *testing.T) {
	f := historicalScopeFixture(t)
	for _, test := range []struct {
		name                      string
		before, after             []string
		beforeChange, afterChange bool
		phase                     history.Phase
	}{
		{"loss-update", []string{"member", "admin"}, []string{"revoked", "admin"}, true, true, history.Before},
		{"loss-delete", []string{"member", "admin"}, []string{"", "admin"}, true, false, history.Before},
		{"gain-create", []string{"", "admin"}, []string{"member", "admin"}, false, true, history.After},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph, identity := historicalScopeGraph(t, f, test.before, test.after)
			for _, unused := range []bool{false, true} {
				row := historicalScopeCondition(t, f, "member", unused)
				if _, err := New(row, nil); err == nil {
					t.Fatal("legacy planner admitted a related authority")
				}
				plan, err := NewHistorical(row, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.Paths()) == 0 {
					t.Fatal("constant/unused data erased scope dependencies")
				}
				change := HistoricalChange{Model: ir.ModelID(f.Post)}
				if test.beforeChange {
					change.Before = &identity
				}
				if test.afterChange {
					change.After = &identity
				}
				candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, change)
				if err != nil || len(candidates) == 0 {
					t.Fatalf("unused=%v scope change disappeared: %v", unused, err)
				}
				for _, candidate := range candidates {
					if !candidate.ScopeOnly() || candidate.Phase() != test.phase || candidate.OwnerIdentity().ModelID() != ir.ModelID(f.User) {
						t.Fatalf("scope route acquired data relevance or another phase: %+v", candidate)
					}
				}
				other := must(NewHistorical(historicalScopeCondition(t, f, "other", unused), nil))
				if got := must(other.HistoricalCandidates(context.Background(), f.Registry, graph, change)); len(got) != 0 {
					t.Fatal("another scope argument borrowed this owner's witnesses")
				}
			}
		})
	}
}

func TestHistoricalScopePlansRefuseLegacyBindingAndUnknownExtents(t *testing.T) {
	f := historicalScopeFixture(t)
	graph, identity := historicalScopeGraph(t, f, []string{"member", "admin"}, []string{"revoked", "admin"})
	plan := must(NewHistorical(historicalScopeCondition(t, f, "member", true), nil))
	row := historicalRow(t, f.Registry, f.Post, mutationdecode.Value(ir.FieldID(f.PostID), ir.UUIDValue([16]byte{15: 11})))
	if _, err := plan.BindChange(row, f.Registry, ir.ProviderSQLite); err == nil {
		t.Fatal("historical plan fell back to a live join")
	}
	input := history.Input{Rows: graph.Rows(), Roots: graph.Roots()}
	// Registered scope extents are required by the immutable graph owner.
	// Such incomplete evidence must never reach the matcher as a valid graph.
	if _, err := history.New(f.Registry, ir.ProviderSQLite, input); err == nil {
		t.Fatal("graph constructor accepted an unknown registered scope extent")
	}
	// A valid complete graph also cannot lend a captured witness to a notice
	// naming another physical identity. There is no recorded route to it.
	missing := historicalRow(t, f.Registry, f.Post, mutationdecode.Value(ir.FieldID(f.PostID), ir.UUIDValue([16]byte{15: 99})))
	missingIdentity := must(mutationdecode.PrimaryIdentity(f.Registry, missing))
	candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Post), Before: &missingIdentity})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("uncaptured changed identity borrowed scope evidence: candidates=%d err=%v", len(candidates), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := plan.HistoricalCandidates(ctx, f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Post), Before: &identity}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	local := must(NewHistorical(must(ir.NewConstant(ir.ModelID(f.User), true)), nil))
	if _, err := local.BindChange(row, f.Registry, ir.ProviderSQLite); err == nil {
		t.Fatal("historical mode with no paths silently entered legacy binder")
	}
}

func TestHistoricalScopePathsDeduplicateSharedOwnerButRetainOccurrenceAndBounds(t *testing.T) {
	f := historicalScopeFixture(t)
	row := historicalScopeCondition(t, f, "member", false)
	base := must(NewHistorical(row, nil))
	duplicate := must(NewHistorical(row, nil, row, row))
	if len(base.Paths()) != len(duplicate.Paths()) || len(base.owners) != len(duplicate.owners) {
		t.Fatal("identical scope occurrence multiplied physical subscriptions or owner questions")
	}
	// Two distinct ordinary source guards are separate occurrences, even when
	// their scope and owning model coincide.
	one := historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("first")))
	two := historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("second")))
	toOwner := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, row)
	left := must(ir.NewLogical(ir.ModelID(f.Post), ir.LogicalAnd, []ir.Condition{one, toOwner}))
	right := must(ir.NewLogical(ir.ModelID(f.Post), ir.LogicalAnd, []ir.Condition{two, toOwner}))
	occurrences := must(NewHistorical(left, nil, right))
	if len(occurrences.Paths()) <= len(base.Paths()) {
		t.Fatal("different ordinary prefixes shared one scope path")
	}
	var questions []ir.Condition
	for index := 0; index <= MaxPaths; index++ {
		questions = append(questions, historicalScopeCondition(t, f, fmt.Sprintf("actor-%d", index), true))
	}
	_, err := NewHistorical(must(ir.NewLogical(ir.ModelID(f.User), ir.LogicalOr, questions)), nil)
	var limit *PathLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("scope dependency paths bypassed MaxPaths: %v", err)
	}
}

func TestHistoricalScopeRequiresBoundRelatedAuthority(t *testing.T) {
	f := historicalScopeFixture(t)
	post := must(ir.NewConstant(ir.ModelID(f.Post), true))
	relation := historicalRelation(t, f.Registry, ir.ModelID(f.User), f.UserPosts, f.Authorship, ir.ModelID(f.Post), ir.RelationToMany, post)
	scope := must(ir.NewFactScope(relation))
	fact := must(ir.NewFact(ir.ModelID(f.User), must(ir.FactIdentity(ir.ModelID(f.User), "unbound")), must(ir.NewConstant(ir.ModelID(f.User), true)), scope))
	if _, err := NewHistorical(fact, nil); err == nil {
		t.Fatal("unbound related authority acquired historical admission")
	}
}

func historicalScopeFixture(t *testing.T) schematest.GraphFixture {
	t.Helper()
	f := schematest.NewSubscribedGraph(t)
	compilation := must(schema.DecodeActive(f.Bundle))
	model := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	field := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	some := func(leaf compilerir.ScopeExpressionIR) compilerir.ScopeExpressionIR {
		return compilerir.ScopeExpressionIR{ModelID: model(f.User), Operator: "some", FieldID: field(f.UserPosts), RelationID: compilerir.RelationID(hex.EncodeToString(f.Authorship[:])), TargetModelID: model(f.Post), Children: []compilerir.ScopeExpressionIR{leaf}}
	}
	title, _ := f.Registry.Field(f.Post, f.PostTitle)
	expression := compilerir.ScopeExpressionIR{ModelID: model(f.User), Operator: "and", Children: []compilerir.ScopeExpressionIR{
		some(compilerir.ScopeExpressionIR{ModelID: model(f.Post), Operator: "eq", FieldID: field(f.PostTitle), Parameter: "Member"}),
		some(compilerir.ScopeExpressionIR{ModelID: model(f.Post), Operator: "eq", FieldID: field(f.PostTitle), Literals: []compilerir.TypedLiteralIR{{Kind: compilerir.LiteralString, Canonical: "admin"}}}),
	}}
	if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{{ModelID: model(f.User), Template: compilerir.ScopeTemplateIR{Name: "membership", Arguments: compilerir.GoNamedTypeIR{PackagePath: "example.test/related", Name: "ScopeArgs"}, Parameters: []compilerir.ScopeParameterIR{{Name: "Member", Type: title.LogicalType()}}, Expression: expression}}}); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	payload := must(compilerir.CanonicalContract(compilation.Contract))
	digest := historicalDigest(t, string(must(compilerir.ContractFingerprint(compilation.Contract))))
	document := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	f.Bundle = golem.GeneratedSchemaBundle(f.Bundle.GenerationDigest(), f.Bundle.GeneratorVersion(), f.Bundle.TemplateABIVersion(), f.Bundle.Model(), document, f.Bundle.Providers()...)
	f.Registry = must(schema.New(f.Bundle))
	return f
}

func historicalScopeCondition(t *testing.T, f schematest.GraphFixture, member string, unused bool) ir.Condition {
	t.Helper()
	metadata, _ := f.Registry.Model(f.User)
	template := metadata.ScopeTemplates()[0]
	posts := golem.GeneratedToMany[sourceUser, sourcePost](f.UserPosts, f.Authorship, f.Post)
	title := golem.GeneratedTextField[sourcePost, string](f.PostTitle)
	rules := golem.NewRules[sourceUser]()
	rules.BindFactScope(golem.GeneratedBoundFactScope(f.User, string(template.ID), historicalDigest(t, string(template.Fingerprint)), posts.Some(title.Eq(member)).And(posts.Some(title.Eq("admin"))), golem.GeneratedTemplateArgument("Member", member)))
	fact := rules.Fact("unused_false", golem.None[sourceUser]())
	if unused {
		rules.CanRead(golem.All[sourceUser]())
	} else {
		rules.CanRead(fact)
	}
	policy := must(policybind.Policy(must(rules.Freeze(f.User)), f.Registry, ir.PortableProviders()))
	return must(resolve.RowConstraint(policy, ir.ActionRead, ir.ModelID(f.User)))
}

func historicalScopeGraph(t *testing.T, f schematest.GraphFixture, before, after []string) (history.Graph, mutationdecode.Identity) {
	t.Helper()
	owner := historicalRow(t, f.Registry, f.User, mutationdecode.Value(ir.FieldID(f.UserID), ir.UUIDValue([16]byte{15: 1})))
	input := history.Input{}
	root := history.Root{Identity: owner}
	for index, titles := range [][]string{before, after} {
		phase := history.Phase(index + 1)
		input.Rows = append(input.Rows, history.PhaseRow{Phase: phase, Row: owner})
		parent := history.RowRef(len(input.Rows))
		presence := history.Presence{Present: true, Row: parent}
		if phase == history.Before {
			root.Before = presence
		} else {
			root.After = presence
		}
		extent := history.Extent{Phase: phase, Parent: parent, Field: ir.FieldID(f.UserPosts), Relation: ir.RelationID(f.Authorship)}
		for offset, title := range titles {
			if title == "" {
				continue
			}
			row := historicalRow(t, f.Registry, f.Post,
				mutationdecode.Value(ir.FieldID(f.PostID), ir.UUIDValue([16]byte{15: byte(11 + offset)})),
				mutationdecode.Value(ir.FieldID(f.AuthorID), ir.UUIDValue([16]byte{15: 1})),
				mutationdecode.Value(ir.FieldID(f.PostTitle), must(ir.StringValue(title))))
			input.Rows = append(input.Rows, history.PhaseRow{Phase: phase, Row: row})
			extent.Children = append(extent.Children, history.RowRef(len(input.Rows)))
		}
		input.Extents = append(input.Extents, extent)
	}
	input.Roots = []history.Root{root}
	changed := historicalRow(t, f.Registry, f.Post, mutationdecode.Value(ir.FieldID(f.PostID), ir.UUIDValue([16]byte{15: 11})))
	return must(history.New(f.Registry, ir.ProviderSQLite, input)), must(mutationdecode.PrimaryIdentity(f.Registry, changed))
}

func TestHistoricalScopeAndDataPathsKeepSeparateRoles(t *testing.T) {
	f := historicalScopeFixture(t)
	base := must(NewHistorical(historicalScopeCondition(t, f, "member", true), nil))
	if len(base.owners) != 1 {
		t.Fatal("scope fixture must have one exact owner")
	}
	scope := base.owners[0].owner.scope
	ready := historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("ready")))
	body := historicalRelation(t, f.Registry, ir.ModelID(f.User), f.UserPosts, f.Authorship, ir.ModelID(f.Post), ir.RelationToMany, ready)
	fact := must(ir.NewFact(ir.ModelID(f.User), must(ir.FactIdentity(ir.ModelID(f.User), "ready")), body, scope))
	plan := must(NewHistorical(fact, nil))
	graph, _ := historicalScopeGraph(t, f, []string{"member", "admin", "ready"}, []string{"member", "admin", "not-ready"})
	changed := historicalRow(t, f.Registry, f.Post, mutationdecode.Value(ir.FieldID(f.PostID), ir.UUIDValue([16]byte{15: 13})))
	identity := must(mutationdecode.PrimaryIdentity(f.Registry, changed))
	candidates := must(plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Post), Before: &identity, After: &identity}))
	if len(candidates) != 1 || candidates[0].ScopeOnly() || candidates[0].Phase() != history.Before {
		t.Fatalf("data-only change borrowed a scope path or lost its body: candidates=%d", len(candidates))
	}
	// The same changed physical identity can be observed independently by a
	// scope and a data fact. Neither role may erase the other during dedup.
	member := historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("member")))
	body = historicalRelation(t, f.Registry, ir.ModelID(f.User), f.UserPosts, f.Authorship, ir.ModelID(f.Post), ir.RelationToMany, member)
	fact = must(ir.NewFact(ir.ModelID(f.User), must(ir.FactIdentity(ir.ModelID(f.User), "member")), body, scope))
	plan = must(NewHistorical(fact, nil))
	graph, identity = historicalScopeGraph(t, f, []string{"member", "admin"}, []string{"revoked", "admin"})
	candidates = must(plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Post), Before: &identity, After: &identity}))
	var scopes, data int
	for _, candidate := range candidates {
		if candidate.ScopeOnly() {
			scopes++
		} else {
			data++
		}
	}
	if scopes != 1 || data != 1 {
		t.Fatalf("scope/data dedup lost an independent role: scope=%d data=%d", scopes, data)
	}
}
