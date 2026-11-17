package observation

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/history"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

type historicalWatch struct{}
type historicalFixture struct {
	graph                                      schematest.GraphFixture
	watch                                      golem.ModelID
	id, profile, video, relationField, inverse golem.FieldID
	relation                                   golem.RelationID
}

func TestHistoricalCandidatesUseCompleteSiblingCutsAndNeverCurrentOwnerLinks(t *testing.T) {
	fixture := newHistoricalFixture(t)
	graph, copyRow := historicalGraph(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(fixture.graph.Registry, copyRow))
	for _, negative := range []bool{false, true} {
		owner := historicalOwnerCondition(t, fixture, negative)
		video := historicalRelation(t, fixture.graph.Registry, ir.ModelID(fixture.graph.Post), fixture.graph.PostAuthor, fixture.graph.Authorship, ir.ModelID(fixture.graph.User), ir.RelationToOne, owner)
		video = must(logical(video.ModelID(), ir.LogicalAnd, []ir.Condition{historicalScalar(t, fixture.graph.Registry, fixture.graph.Post, fixture.graph.PostTitle, must(ir.StringValue("video"))), video}))
		outer := historicalRelation(t, fixture.graph.Registry, ir.ModelID(fixture.watch), fixture.relationField, fixture.relation, ir.ModelID(fixture.graph.Post), ir.RelationToOne, video)
		outer = must(logical(outer.ModelID(), ir.LogicalAnd, []ir.Condition{historicalScalar(t, fixture.graph.Registry, fixture.watch, fixture.profile, ir.UUIDValue([16]byte{15: 1})), outer}))
		plan := must(New(outer, nil))
		candidates := must(plan.HistoricalCandidates(context.Background(), fixture.graph.Registry, graph, HistoricalChange{Model: ir.ModelID(fixture.graph.Comment), Before: &identity}))
		if len(candidates) != 2 {
			t.Fatalf("negative=%v candidates=%d want both sibling Videos", negative, len(candidates))
		}
		seen := map[byte]bool{}
		for _, candidate := range candidates {
			if candidate.Phase() != history.Before || candidate.CutOffset() != 1 || candidate.ScopeOnly() || candidate.OwnerIdentity().ModelID() != ir.ModelID(fixture.graph.User) || ir.HasFacts(candidate.Prefix()) {
				t.Fatal("cut candidate lost its phase/owner/prefix contract")
			}
			ownerID, _ := candidate.OwnerIdentity().Cell(ir.FieldID(fixture.graph.UserID))
			ownerValue, _ := ownerID.PolicyValue()
			ownerKey, _ := ownerValue.UUID()
			if ownerKey[15] != 1 {
				t.Fatal("A's activity was reparented to owner M2")
			}
			cell, _ := candidate.CutIdentity().Cell(ir.FieldID(fixture.graph.PostID))
			value, _ := cell.PolicyValue()
			cut, _ := value.UUID()
			seen[cut[15]] = true
			// Current W ownership, stable Video identity and Video's ordinary
			// guard must hold. Video.Author/Media is never rejoined.
			for _, test := range []struct {
				profile, video byte
				title          string
				want           bool
			}{{1, cut[15], "video", true}, {2, cut[15], "video", false}, {1, 99, "video", false}, {1, cut[15], "revoked", false}} {
				profile := must(evaluate.ValueField(ir.FieldID(fixture.profile), ir.UUIDValue([16]byte{15: test.profile})))
				video := must(evaluate.ValueField(ir.FieldID(fixture.video), ir.UUIDValue([16]byte{15: test.video})))
				id := must(evaluate.ValueField(ir.FieldID(fixture.graph.PostID), ir.UUIDValue([16]byte{15: test.video})))
				title := must(evaluate.ValueField(ir.FieldID(fixture.graph.PostTitle), must(ir.StringValue(test.title))))
				// B can reparent Video to M2 while its ordinary guard remains
				// true. No Author relation is supplied, so Prefix cannot follow it.
				currentOwner := must(evaluate.ValueField(ir.FieldID(fixture.graph.AuthorID), ir.UUIDValue([16]byte{15: 2})))
				currentVideo := must(evaluate.NewRecord(ir.ModelID(fixture.graph.Post), id, title, currentOwner))
				relation := must(evaluate.ToOneField(ir.FieldID(fixture.relationField), ir.ModelID(fixture.graph.Post), currentVideo))
				record := must(evaluate.NewRecord(ir.ModelID(fixture.watch), profile, video, relation))
				got := must(evaluate.Condition(candidate.Prefix(), record, ir.PortableProviders()))
				if got != test.want {
					t.Fatalf("negative=%v prefix profile=%d video=%d title=%q got=%v", negative, test.profile, test.video, test.title, got)
				}
			}
		}
		if !seen[11] || !seen[12] {
			t.Fatal("changed copy under V1 omitted sibling V2")
		}
		// A different terminal identity cannot borrow the recorded copy.
		other := historicalRow(t, fixture.graph.Registry, fixture.graph.Comment, mutationdecode.Value(ir.FieldID(fixture.graph.CommentID), ir.UUIDValue([16]byte{15: 99})))
		otherID := must(mutationdecode.PrimaryIdentity(fixture.graph.Registry, other))
		if candidates := must(plan.HistoricalCandidates(context.Background(), fixture.graph.Registry, graph, HistoricalChange{Model: ir.ModelID(fixture.graph.Comment), Before: &otherID})); len(candidates) != 0 {
			t.Fatal("unknown changed identity borrowed a captured sibling")
		}
	}
}

func TestHistoricalCandidatesRetainMutableOrdinaryGuardBeyondCut(t *testing.T) {
	fixture := newHistoricalFixture(t)
	graph, copyRow := historicalGraph(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(fixture.graph.Registry, copyRow))
	f := fixture.graph
	for _, guard := range []string{"true", "mutable", "false"} {
		// A finite ordinary cycle places Comment between the recorded Post
		// cut and User owner. Mutable intermediate guards retain a separate
		// identity-bound current fence; constants need no such proof.
		owner := historicalOwnerCondition(t, fixture, false)
		postOwner := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, owner)
		commentPost := historicalRelation(t, f.Registry, ir.ModelID(f.Comment), f.CommentPost, f.Commenting, ir.ModelID(f.Post), ir.RelationToOne, postOwner)
		if guard == "mutable" {
			commentPost = must(logical(commentPost.ModelID(), ir.LogicalAnd, []ir.Condition{historicalScalar(t, f.Registry, f.Comment, f.CommentBody, must(ir.StringValue("ready"))), commentPost}))
		} else if guard == "false" {
			commentPost = must(logical(commentPost.ModelID(), ir.LogicalAnd, []ir.Condition{must(ir.NewConstant(commentPost.ModelID(), false)), commentPost}))
		}
		postComments := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostComments, f.Commenting, ir.ModelID(f.Comment), ir.RelationToMany, commentPost)
		outer := historicalRelation(t, f.Registry, ir.ModelID(fixture.watch), fixture.relationField, fixture.relation, ir.ModelID(f.Post), ir.RelationToOne, postComments)
		plan := must(New(outer, nil))
		candidates, err := plan.HistoricalCandidates(context.Background(), f.Registry, graph, HistoricalChange{Model: ir.ModelID(f.Comment), Before: &identity})
		if guard == "mutable" {
			if err != nil || len(candidates) != 1 || len(candidates[0].Guards()) != 1 {
				t.Fatalf("mutable intermediate guard lost its current fence: candidates=%d err=%v", len(candidates), err)
			}
		} else if guard == "false" {
			if err != nil || len(candidates) != 0 {
				t.Fatalf("immutable false route was treated as unknown: %v", err)
			}
		} else if err != nil || len(candidates) != 1 || candidates[0].CutOffset() != 1 {
			t.Fatalf("all-true ordinary intermediate path failed: candidates=%d err=%v", len(candidates), err)
		}
	}
}

func TestHistoricalCandidatesRetainDirectOwnerAndScopeLossQuestions(t *testing.T) {
	fixture := newHistoricalFixture(t)
	graph, copyRow := historicalGraph(t, fixture, true)
	plan := must(New(historicalOwnerCondition(t, fixture, false), nil))
	copyID := must(mutationdecode.PrimaryIdentity(fixture.graph.Registry, copyRow))
	data := must(plan.HistoricalCandidates(context.Background(), fixture.graph.Registry, graph, HistoricalChange{Model: ir.ModelID(fixture.graph.Comment), Before: &copyID}))
	if len(data) != 1 || data[0].CutOffset() != 0 || data[0].Prefix().ModelID() != ir.ModelID(fixture.graph.User) {
		t.Fatal("direct owner data route was omitted")
	}
	owner := graph.Roots()[0].Identity
	ownerID := must(mutationdecode.PrimaryIdentity(fixture.graph.Registry, owner))
	loss := must(plan.HistoricalCandidates(context.Background(), fixture.graph.Registry, graph, HistoricalChange{Model: ir.ModelID(fixture.graph.User), Before: &ownerID, After: &ownerID}))
	if len(loss) == 0 {
		t.Fatal("direct-owner scope loss disappeared because data paths target only descendants")
	}
	for _, candidate := range loss {
		if !candidate.ScopeOnly() || candidate.Phase() != history.Before || candidate.CutOffset() != 0 {
			t.Fatal("scope loss acquired a fabricated physical path or after authority")
		}
		if must(graph.Evaluate(context.Background(), fixture.graph.Registry, ir.PortableProviders(), history.After, candidate.OwnerKey(), candidate.Scope().Condition())) {
			t.Fatal("revoked authority was inverted by a data fact")
		}
	}
	// Immutable values are safe to hand to a runtime fence; replacing a
	// returned identity row cannot mutate graph evidence or another candidate.
	identity := loss[0].OwnerIdentity()
	identity = mutationdecode.Row{}
	_ = identity
	if loss[0].OwnerIdentity().ModelID() == (ir.ModelID{}) {
		t.Fatal("candidate exposed mutable identity storage")
	}
}

func TestHistoricalCandidatesKeepOwnerWhenFirstFactBodyHasNoPhysicalPaths(t *testing.T) {
	fixture := newHistoricalFixture(t)
	graph, _ := historicalGraph(t, fixture, true)
	root := ir.ModelID(fixture.graph.User)
	// Use the real bound scope established by the public freeze/bind flow.
	bound := must(New(historicalOwnerCondition(t, fixture, false), nil))
	if len(bound.owners) == 0 {
		t.Fatal("bound policy omitted its independent owning-scope question")
	}
	scope := bound.owners[0].owner.scope
	owner := graph.Roots()[0].Identity
	identity := must(mutationdecode.PrimaryIdentity(fixture.graph.Registry, owner))
	local := historicalScalar(t, fixture.graph.Registry, fixture.graph.User, fixture.graph.UserID, ir.UUIDValue([16]byte{15: 1}))
	for _, test := range []struct {
		name string
		body ir.Condition
	}{
		{"constant_true", must(ir.NewConstant(root, true))},
		{"constant_false", must(ir.NewConstant(root, false))},
		{"local_scalar", local},
	} {
		t.Run(test.name, func(t *testing.T) {
			fact := must(ir.NewFact(root, must(ir.FactIdentity(root, test.name)), test.body, scope))
			plan := must(New(fact, nil))
			if len(plan.Paths()) != 0 {
				t.Fatal("owner metadata fabricated a physical data path")
			}
			// Owner questions belong to the historical API. Legacy BindChange
			// must not receive a zero-hop path or invent a data dependency.
			before, known := graph.FindRoot(history.Before, graph.RootKeys()[0])
			if !known || before == nil {
				t.Fatal("fixture lost the exact before owner")
			}
			legacy := must(plan.BindChange(*before, fixture.graph.Registry, ir.ProviderSQLite))
			if truth, constant := legacy.Constant(); !constant || truth {
				t.Fatal("owning-scope metadata changed legacy data-path semantics")
			}
			candidates := must(plan.HistoricalCandidates(context.Background(), fixture.graph.Registry, graph, HistoricalChange{Model: root, Before: &identity, After: &identity}))
			if len(candidates) != 1 {
				t.Fatalf("body=%s owning-scope candidates=%d want 1", test.name, len(candidates))
			}
			candidate := candidates[0]
			if !candidate.ScopeOnly() || candidate.Phase() != history.Before || candidate.CutOffset() != 0 || candidate.OwnerIdentity().ModelID() != root || candidate.Prefix().ModelID() != root {
				t.Fatal("root scope loss acquired a data path, foreign owner, or after authority")
			}
			if must(graph.Evaluate(context.Background(), fixture.graph.Registry, ir.PortableProviders(), history.After, candidate.OwnerKey(), candidate.Scope().Condition())) {
				t.Fatal("constant/local data truth replaced exact revoked scope authority")
			}
		})
	}
}

func TestHistoricalCandidatesRejectMissingScopeEvidenceAndForgedBindings(t *testing.T) {
	fixture := newHistoricalFixture(t)
	graph, copyRow := historicalGraph(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(fixture.graph.Registry, copyRow))
	plan := must(New(historicalOwnerCondition(t, fixture, false), nil))
	for index := range plan.paths {
		binding, _ := plan.paths[index].owner.scope.Binding()
		binding.Template[0] ^= 1
		plan.paths[index].owner.scope = must(ir.NewBoundFactScope(plan.paths[index].owner.scope.Condition(), binding))
	}
	candidates, err := plan.HistoricalCandidates(context.Background(), fixture.graph.Registry, graph, HistoricalChange{Model: ir.ModelID(fixture.graph.Comment), Before: &identity})
	var failure *HistoricalCoverageError
	if !errors.As(err, &failure) || candidates != nil {
		t.Fatalf("forged provenance returned partial/accepted candidates: %v", err)
	}
	plan = must(New(historicalOwnerCondition(t, fixture, false), nil))
	version := historicalScalar(t, fixture.graph.Registry, fixture.graph.User, fixture.graph.UserRowVersion, must(ir.SignedValue(ir.ValueInt64, 1)))
	for index := range plan.paths {
		binding, _ := plan.paths[index].owner.scope.Binding()
		plan.paths[index].owner.scope = must(ir.NewBoundFactScope(version, binding))
	}
	if _, err := plan.HistoricalCandidates(context.Background(), fixture.graph.Registry, graph, HistoricalChange{Model: ir.ModelID(fixture.graph.Comment), Before: &identity}); !errors.As(err, &failure) {
		t.Fatalf("uncaptured authority was widened: %v", err)
	}
}

func historicalOwnerCondition(t *testing.T, fixture historicalFixture, negative bool) ir.Condition {
	t.Helper()
	f := fixture.graph
	model, _ := f.Registry.Model(f.User)
	scope := model.ScopeTemplates()[0]
	fact := model.FactTemplates()[0]
	name := golem.GeneratedTextField[sourceUser, string](f.UserName)
	posts := golem.GeneratedToMany[sourceUser, sourcePost](f.UserPosts, f.Authorship, f.Post)
	title := golem.GeneratedTextField[sourcePost, string](f.PostTitle)
	comments := golem.GeneratedToMany[sourcePost, sourceComment](f.PostComments, f.Commenting, f.Comment)
	body := golem.GeneratedTextField[sourceComment, string](f.CommentBody)
	rules := golem.NewRules[sourceUser]()
	rules.BindFactScope(golem.GeneratedBoundFactScope(f.User, string(scope.ID), historicalDigest(t, string(scope.Fingerprint)), name.Eq("owner"), golem.GeneratedTemplateArgument("Name", "owner")))
	ready := rules.BindFact(golem.GeneratedBoundFact(f.User, string(fact.ID), historicalDigest(t, string(fact.Fingerprint)), fact.Name, posts.Some(title.Eq("video").And(comments.Some(body.Eq("ready")))), golem.GeneratedTemplateArgument("State", "ready")))
	if negative {
		ready = ready.Not()
	}
	rules.CanRead(ready)
	policy := must(policybind.Policy(must(rules.Freeze(f.User)), f.Registry, ir.PortableProviders()))
	return must(resolve.RowConstraint(policy, ir.ActionRead, ir.ModelID(f.User)))
}

func historicalGraph(t *testing.T, fixture historicalFixture, revoked bool) (history.Graph, mutationdecode.Row) {
	t.Helper()
	f := fixture.graph
	user := func(name string) mutationdecode.Row {
		return historicalRow(t, f.Registry, f.User, mutationdecode.Value(ir.FieldID(f.UserID), ir.UUIDValue([16]byte{15: 1})), mutationdecode.Value(ir.FieldID(f.UserName), must(ir.StringValue(name))))
	}
	post := func(id byte) mutationdecode.Row {
		return historicalRow(t, f.Registry, f.Post, mutationdecode.Value(ir.FieldID(f.PostID), ir.UUIDValue([16]byte{15: id})), mutationdecode.Value(ir.FieldID(f.AuthorID), ir.UUIDValue([16]byte{15: 1})), mutationdecode.Value(ir.FieldID(f.PostTitle), must(ir.StringValue("video"))))
	}
	copy := historicalRow(t, f.Registry, f.Comment, mutationdecode.Value(ir.FieldID(f.CommentID), ir.UUIDValue([16]byte{15: 31})), mutationdecode.Value(ir.FieldID(f.CommentPostID), ir.UUIDValue([16]byte{15: 11})), mutationdecode.Value(ir.FieldID(f.CommentBody), must(ir.StringValue("ready"))))
	name := "owner"
	if revoked {
		name = "revoked"
	}
	input := history.Input{Rows: []history.PhaseRow{{Phase: history.Before, Row: user("owner")}, {Phase: history.Before, Row: post(11)}, {Phase: history.Before, Row: post(12)}, {Phase: history.Before, Row: copy}, {Phase: history.After, Row: user(name)}, {Phase: history.After, Row: post(11)}, {Phase: history.After, Row: post(12)}}}
	input.Roots = []history.Root{{Identity: historicalRow(t, f.Registry, f.User, mutationdecode.Value(ir.FieldID(f.UserID), ir.UUIDValue([16]byte{15: 1}))), Before: history.Presence{Present: true, Row: 1}, After: history.Presence{Present: true, Row: 5}}}
	for _, entry := range []struct {
		phase    history.Phase
		parent   history.RowRef
		field    golem.FieldID
		relation golem.RelationID
		children []history.RowRef
	}{{history.Before, 1, f.UserPosts, f.Authorship, []history.RowRef{2, 3}}, {history.Before, 2, f.PostComments, f.Commenting, []history.RowRef{4}}, {history.Before, 3, f.PostComments, f.Commenting, nil}, {history.After, 5, f.UserPosts, f.Authorship, []history.RowRef{6, 7}}, {history.After, 6, f.PostComments, f.Commenting, nil}, {history.After, 7, f.PostComments, f.Commenting, nil}} {
		input.Extents = append(input.Extents, history.Extent{Phase: entry.phase, Parent: entry.parent, Field: ir.FieldID(entry.field), Relation: ir.RelationID(entry.relation), Children: entry.children})
	}
	return must(history.New(f.Registry, ir.ProviderSQLite, input)), copy
}

func newHistoricalFixture(t *testing.T) historicalFixture {
	t.Helper()
	fixture := schematest.NewPolicyCaptureOrdinaryGuardGraph(t)
	return historicalFixture{graph: fixture.Graph, watch: fixture.Watch, id: fixture.ID, profile: fixture.Profile, video: fixture.Video, relationField: fixture.RelationField, inverse: fixture.Inverse, relation: fixture.Relation}
}

func historicalDigest(t *testing.T, text string) golem.SchemaDigest {
	t.Helper()
	data := must(hex.DecodeString(text))
	if len(data) != 32 {
		t.Fatal("invalid static template digest")
	}
	var result golem.SchemaDigest
	copy(result[:], data)
	return result
}
func historicalRow(t *testing.T, registry *schema.Registry, model golem.ModelID, cells ...mutationdecode.Cell) mutationdecode.Row {
	t.Helper()
	return must(mutationdecode.NewRow(registry, ir.ModelID(model), cells))
}
func historicalScalar(t *testing.T, registry *schema.Registry, model golem.ModelID, field golem.FieldID, value ir.Value) ir.Condition {
	t.Helper()
	descriptor, found := policysql.SchemaResolver(registry).Field(ir.ProviderSQLite, ir.ModelID(model), ir.FieldID(field))
	if !found {
		t.Fatal("missing scalar descriptor")
	}
	operand := must(ir.OneOperand(value))
	requirements := must(operator.ValidateShape(ir.OperatorEqual, operator.Shape{Node: ir.ConditionScalar, FieldType: descriptor.Type, Operand: operand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()}))
	return must(ir.NewScalar(ir.ModelID(model), ir.FieldID(field), descriptor.Type, ir.OperatorEqual, ir.ComparisonSensitive, operand, requirements))
}
func historicalRelation(t *testing.T, registry *schema.Registry, model ir.ModelID, field golem.FieldID, relation golem.RelationID, target ir.ModelID, cardinality ir.RelationCardinality, child ir.Condition) ir.Condition {
	t.Helper()
	operation := ir.OperatorRelationIs
	if cardinality == ir.RelationToMany {
		operation = ir.OperatorRelationSome
	}
	requirements := must(operator.ValidateShape(operation, operator.Shape{Node: ir.ConditionRelation, Cardinality: cardinality, HasChild: true, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()}))
	return must(ir.NewRelation(model, ir.FieldID(field), ir.RelationID(relation), target, cardinality, operation, &child, requirements))
}
