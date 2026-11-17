package bind_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/observation"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

type testPost struct{}
type testUser struct{}

func TestGeneratedScopeTemplateBindsExactLocalTruthAndProvenance(t *testing.T) {
	fixture, templates := scopeBindTestFixture(t)
	actor, titles := golem.UUID{15: 7}, []string{"ready", "draft"}
	body := scopeBindTestBody(fixture, actor, titles)
	arguments := scopeBindTestArguments(actor, titles)
	frozen := scopeBindTestFreeze(t, fixture, templates["account"], body, arguments...)
	policy, err := policybind.Policy(frozen, fixture.Registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	scope := policy.FactScope()
	if scope.ModelID() != ir.ModelID(fixture.Post) || scope.UsesRelations() || len(policy.Facts()) != 1 {
		t.Fatal("local binding lost its model domain or fact ownership")
	}
	assertScopeBindTestProvenance(t, scope, frozen, templates["account"])
	want, err := policybind.Predicate(mustScopeBindTestPredicate(t, fixture, body), fixture.Registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	want, err = normalize.Condition(want)
	if err != nil {
		t.Fatal(err)
	}
	// Binding retains authored inventory for runtime admission. Compare truth
	// only after normalizing both sides of this semantic equivalence check.
	actualCondition, err := normalize.Condition(scope.Condition())
	if err != nil {
		t.Fatal(err)
	}
	actualIdentity, err := ir.ConditionFingerprint(actualCondition)
	if err != nil {
		t.Fatal(err)
	}
	wantIdentity, err := ir.ConditionFingerprint(want)
	if err != nil || actualIdentity != wantIdentity {
		t.Fatalf("bound template changed executable authority: %v", err)
	}
	// Scope provenance is independent of harmless logical normal form changes.
	equivalent := scopeBindTestFreeze(t, fixture, templates["account"], body.And(golem.All[testPost]()), arguments...)
	if _, err := policybind.Policy(equivalent, fixture.Registry, ir.PortableProviders()); err != nil {
		t.Fatalf("equivalent generated body was rejected: %v", err)
	}
}

func TestGeneratedScopeTemplateRetainsAuthoredInventoryUntilPolicyNormalization(t *testing.T) {
	fixture, templates := scopeBindTestFixture(t)
	actor, titles := golem.UUID{15: 7}, []string{"ready", "draft"}
	author := golem.GeneratedToOne[testPost, testUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	body := scopeBindTestBody(fixture, actor, titles).And(golem.All[testPost]().Or(author.Is(golem.All[testUser]())))
	frozen := scopeBindTestFreeze(t, fixture, templates["account"], body, scopeBindTestArguments(actor, titles)...)
	policy, err := policybind.Policy(frozen, fixture.Registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	observesAuthor := func(condition ir.Condition) bool {
		plan, err := dependency.Collect(condition)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range plan.Requires() {
			if field == ir.FieldID(fixture.PostAuthor) {
				return true
			}
		}
		return false
	}
	if !observesAuthor(policy.FactScope().Condition()) {
		t.Fatal("binding erased the authored dead physical participant before admission")
	}
	assertScopeBindTestProvenance(t, policy.FactScope(), frozen, templates["account"])
	normalized, err := normalize.Policy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if observesAuthor(normalized.FactScope().Condition()) {
		t.Fatal("policy normalization stopped owning executable Boolean simplification")
	}
	assertScopeBindTestProvenance(t, normalized.FactScope(), frozen, templates["account"])
}

func TestGeneratedScopeTemplateRejectsForgedBodyArgumentsAndIdentity(t *testing.T) {
	fixture, templates := scopeBindTestFixture(t)
	for _, name := range []string{"body", "argument-value", "argument-type", "identity", "fingerprint", "missing-argument", "extra-argument", "renamed-argument", "scalar-for-set", "set-for-scalar"} {
		t.Run(name, func(t *testing.T) {
			actor, titles := golem.UUID{15: 7}, []string{"ready"}
			template := templates["account"]
			body := scopeBindTestBody(fixture, actor, titles)
			arguments := scopeBindTestArguments(actor, titles)
			switch name {
			case "body":
				body = golem.All[testPost]()
			case "argument-value":
				arguments[0] = golem.GeneratedTemplateArgument("AccountID", golem.UUID{15: 8})
			case "argument-type":
				arguments[1] = golem.GeneratedTemplateSetArgument("Titles", []int64{7})
			case "identity":
				template.ID = compilerir.ScopeTemplateID(strings.Repeat("cd", 16))
			case "fingerprint":
				template.Fingerprint = compilerir.Fingerprint(strings.Repeat("ef", 32))
			case "missing-argument":
				arguments = arguments[:1]
			case "extra-argument":
				arguments = append(arguments, golem.GeneratedTemplateArgument("Extra", true))
			case "renamed-argument":
				arguments[1] = golem.GeneratedTemplateSetArgument("Other", titles)
			case "scalar-for-set":
				arguments[1] = golem.GeneratedTemplateArgument("Titles", "ready")
			case "set-for-scalar":
				arguments[0] = golem.GeneratedTemplateSetArgument("AccountID", []golem.UUID{actor})
			}
			// All mutations are valid public values. Rejection must occur at
			// registry-backed binding, not before the forged input reaches it.
			frozen := scopeBindTestFreeze(t, fixture, template, body, arguments...)
			_, err := policybind.Policy(frozen, fixture.Registry, ir.PortableProviders())
			var failure *policybind.Error
			if !errors.As(err, &failure) {
				t.Fatalf("forged generated scope crossed registry binding: %v", err)
			}
		})
	}
}

func TestGeneratedScopeTemplatePreservesEmptyAndDeadScopeProvenance(t *testing.T) {
	fixture, templates := scopeBindTestFixture(t)
	for _, name := range []string{"empty-set", "dead-body"} {
		t.Run(name, func(t *testing.T) {
			var previous, previousResolved ir.Fingerprint
			for index, actor := range []golem.UUID{{15: 7}, {15: 8}} {
				template := templates["account"]
				var titles []string
				body := scopeBindTestBody(fixture, actor, titles)
				if name == "dead-body" {
					template, titles, body = templates["dead"], []string{"ready"}, golem.None[testPost]()
				}
				frozen := scopeBindTestFreeze(t, fixture, template, body, scopeBindTestArguments(actor, titles)...)
				policy, err := policybind.Policy(frozen, fixture.Registry, ir.PortableProviders())
				if err != nil {
					t.Fatal(err)
				}
				owner, err := evaluate.ValueField(ir.FieldID(fixture.AuthorID), ir.UUIDValue(actor.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				title, err := ir.StringValue("ready")
				if err != nil {
					t.Fatal(err)
				}
				label, err := evaluate.ValueField(ir.FieldID(fixture.PostTitle), title)
				if err != nil {
					t.Fatal(err)
				}
				record, err := evaluate.NewRecord(ir.ModelID(fixture.Post), owner, label)
				if err != nil {
					t.Fatal(err)
				}
				for pass := 0; pass < 2; pass++ {
					policy, err = normalize.Policy(policy)
					if err != nil {
						t.Fatal(err)
					}
					assertScopeBindTestProvenance(t, policy.FactScope(), frozen, template)
					if allowed, err := evaluate.Condition(policy.FactScope().Condition(), record, ir.PortableProviders()); err != nil || allowed {
						t.Fatalf("scope with no eligible members allowed a record: allowed=%v err=%v", allowed, err)
					}
				}
				constraint, err := resolve.RowConstraint(policy, ir.ActionRead, ir.ModelID(fixture.Post))
				if err != nil {
					t.Fatal(err)
				}
				if allowed, err := evaluate.Condition(constraint, record, ir.PortableProviders()); err != nil || allowed {
					t.Fatalf("unconditional CanRead escaped the false mandatory scope: allowed=%v err=%v", allowed, err)
				}
				resolvedScope, owned := constraint.FactScope()
				if !constraint.IsFactScope() || !owned {
					t.Fatal("false resolved authority lost its bound scope boundary")
				}
				assertScopeBindTestProvenance(t, resolvedScope, frozen, template)
				resolvedIdentity, err := ir.ConditionFingerprint(constraint)
				if err != nil || index != 0 && previousResolved == resolvedIdentity {
					t.Fatalf("resolved false authority discarded its actor identity: %v", err)
				}
				previousResolved = resolvedIdentity
				identity, err := ir.FactScopeFingerprint(policy.FactScope())
				if err != nil || index != 0 && previous == identity {
					t.Fatalf("false authority discarded its exact actor argument identity: %v", err)
				}
				previous = identity
			}
		})
	}
}

func TestGeneratedRelatedScopeBindsButCannotObserveWithoutEvidence(t *testing.T) {
	fixture, templates := scopeBindTestFixture(t)
	actor := golem.UUID{15: 7}
	author := golem.GeneratedToOne[testPost, testUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	id := golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID)
	frozen := scopeBindTestFreeze(t, fixture, templates["related"], author.Is(id.Eq(actor)), golem.GeneratedTemplateArgument("AccountID", actor))
	policy, err := policybind.Policy(frozen, fixture.Registry, ir.PortableProviders())
	if err != nil {
		t.Fatal(err)
	}
	if !policy.FactScope().UsesRelations() {
		t.Fatal("related template was silently replaced by a root-local authority")
	}
	assertScopeBindTestProvenance(t, policy.FactScope(), frozen, templates["related"])
	condition, err := resolve.RowConstraint(policy, ir.ActionRead, ir.ModelID(fixture.Post))
	if err != nil {
		t.Fatal(err)
	}
	_, err = observation.New(condition, nil)
	var evidence *ir.FactScopeEvidenceError
	if !errors.As(err, &evidence) {
		t.Fatalf("bound related provenance bypassed the historical evidence gate: %v", err)
	}
}

func TestGeneratedScopeDateTimeArgumentsUseOrdinaryPrecisionBinding(t *testing.T) {
	fixture, templates := scopeBindTestFixture(t)
	template := templates["datetime"]
	field := golem.GeneratedOrderedField[testPost, time.Time](fixture.PostUpdatedAt)
	onGrid := time.Date(2026, 11, 13, 12, 0, 0, 123456000, time.UTC)
	var previous ir.ScopeBinding
	for index, offset := range []time.Duration{time.Nanosecond, 2 * time.Nanosecond} {
		when := onGrid.Add(offset)
		body := field.Eq(when)
		frozen := scopeBindTestFreeze(t, fixture, template, body, golem.GeneratedTemplateArgument("When", when))
		policy, err := policybind.Policy(frozen, fixture.Registry, ir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		ordinary, err := policybind.Predicate(mustScopeBindTestPredicate(t, fixture, body), fixture.Registry, ir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		ordinary, err = normalize.Condition(ordinary)
		if err != nil {
			t.Fatal(err)
		}
		want, err := ir.ConditionFingerprint(ordinary)
		if err != nil {
			t.Fatal(err)
		}
		actualCondition, err := normalize.Condition(policy.FactScope().Condition())
		if err != nil {
			t.Fatal(err)
		}
		actual, err := ir.ConditionFingerprint(actualCondition)
		if err != nil || actual != want {
			t.Fatalf("generated scope diverged from ordinary off-grid datetime binding: %v", err)
		}
		assertScopeBindTestProvenance(t, policy.FactScope(), frozen, template)
		binding, present := policy.FactScope().Binding()
		if !present || index != 0 && (previous.Arguments == binding.Arguments || previous.TemplateID != binding.TemplateID || previous.Template != binding.Template) {
			t.Fatal("off-grid instants lost their distinct exact arguments or changed static template identity")
		}
		view, _ := frozen.View().FactScopeBinding()
		value, one := view.Arguments()[0].Operand().One()
		if !one {
			t.Fatal("datetime argument lost scalar cardinality")
		}
		seconds, nanos, typed := value.DateTime()
		if !typed || seconds != when.Unix() || nanos != uint32(when.Nanosecond()) {
			t.Fatal("datetime argument was rounded before its provenance was frozen")
		}
		previous = binding
	}
	for _, mismatch := range []struct {
		name           string
		body, argument time.Time
	}{
		{"off-grid-body-on-grid-argument", onGrid.Add(time.Nanosecond), onGrid},
		{"on-grid-body-off-grid-argument", onGrid, onGrid.Add(time.Nanosecond)},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			frozen := scopeBindTestFreeze(t, fixture, template, field.Eq(mismatch.body), golem.GeneratedTemplateArgument("When", mismatch.argument))
			if _, err := policybind.Policy(frozen, fixture.Registry, ir.PortableProviders()); err == nil {
				t.Fatal("a datetime body was certified against a semantically different argument")
			}
		})
	}
}

func scopeBindTestFixture(t *testing.T) (schematest.GraphFixture, map[string]compilerir.ScopeTemplateIR) {
	t.Helper()
	fixture := schematest.NewSubscribedGraphRuntimeDefaults(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	postID := compilerir.ModelID(hex.EncodeToString(fixture.Post[:]))
	userID := compilerir.ModelID(hex.EncodeToString(fixture.User[:]))
	fieldID := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	title, _ := fixture.Registry.Field(fixture.Post, fixture.PostTitle)
	updatedAt, _ := fixture.Registry.Field(fixture.Post, fixture.PostUpdatedAt)
	arguments := compilerir.GoNamedTypeIR{PackagePath: "example.test/models", Name: "ScopeArgs"}
	parameters := []compilerir.ScopeParameterIR{{Name: "AccountID", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}}, {Name: "Titles", Type: title.LogicalType(), Many: true}}
	local := compilerir.ScopeExpressionIR{ModelID: postID, Operator: "and", Children: []compilerir.ScopeExpressionIR{
		{ModelID: postID, Operator: "eq", FieldID: fieldID(fixture.AuthorID), Parameter: "AccountID"},
		{ModelID: postID, Operator: "in", FieldID: fieldID(fixture.PostTitle), Parameter: "Titles"},
	}}
	declarations := []scopecontract.Declaration{
		{ModelID: postID, Template: compilerir.ScopeTemplateIR{Name: "account", Arguments: arguments, Parameters: parameters, Expression: local}},
		{ModelID: postID, Template: compilerir.ScopeTemplateIR{Name: "dead", Arguments: arguments, Parameters: parameters, Expression: compilerir.ScopeExpressionIR{ModelID: postID, Operator: "and", Children: []compilerir.ScopeExpressionIR{local, {ModelID: postID, Operator: "false"}}}}},
		{ModelID: postID, Template: compilerir.ScopeTemplateIR{Name: "related", Arguments: arguments, Parameters: parameters[:1], Expression: compilerir.ScopeExpressionIR{ModelID: postID, Operator: "is", FieldID: fieldID(fixture.PostAuthor), RelationID: compilerir.RelationID(hex.EncodeToString(fixture.Authorship[:])), TargetModelID: userID, Children: []compilerir.ScopeExpressionIR{{ModelID: userID, Operator: "eq", FieldID: fieldID(fixture.UserID), Parameter: "AccountID"}}}}},
		{ModelID: postID, Template: compilerir.ScopeTemplateIR{Name: "datetime", Arguments: compilerir.GoNamedTypeIR{PackagePath: "example.test/models", Name: "WhenScopeArgs"}, Parameters: []compilerir.ScopeParameterIR{{Name: "When", Type: updatedAt.LogicalType()}}, Expression: compilerir.ScopeExpressionIR{ModelID: postID, Operator: "eq", FieldID: fieldID(fixture.PostUpdatedAt), Parameter: "When"}}},
	}
	if diagnostics := scopecontract.Normalize(&compilation, declarations); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if err := scopecontract.Validate(compilation.Model, compilation.Contract); err != nil {
		t.Fatal(err)
	}
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	document := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), bindDigest(t, string(fingerprint)), payload)
	base := fixture.Bundle
	fixture.Bundle = golem.GeneratedSchemaBundle(base.GenerationDigest(), base.GeneratorVersion(), base.TemplateABIVersion(), base.Model(), document, base.Providers()...)
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := fixture.Registry.Model(fixture.Post)
	templates := map[string]compilerir.ScopeTemplateIR{}
	for _, template := range metadata.ScopeTemplates() {
		templates[template.Name] = template
	}
	return fixture, templates
}

func scopeBindTestBody(fixture schematest.GraphFixture, actor golem.UUID, titles []string) golem.Predicate[testPost] {
	return golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID).Eq(actor).And(golem.GeneratedEqualField[testPost, string](fixture.PostTitle).In(titles...))
}

func scopeBindTestArguments(actor golem.UUID, titles []string) []golem.TemplateArgument {
	return []golem.TemplateArgument{golem.GeneratedTemplateArgument("AccountID", actor), golem.GeneratedTemplateSetArgument("Titles", titles)}
}

func scopeBindTestFreeze(t *testing.T, fixture schematest.GraphFixture, template compilerir.ScopeTemplateIR, body golem.Predicate[testPost], arguments ...golem.TemplateArgument) golem.FrozenPolicy {
	t.Helper()
	rules := golem.NewRules[testPost]()
	rules.BindFactScope(golem.GeneratedBoundFactScope(fixture.Post, string(template.ID), bindDigest(t, string(template.Fingerprint)), body, arguments...))
	rules.Fact("unused", golem.All[testPost]())
	rules.CanRead(golem.All[testPost]())
	frozen, err := rules.Freeze(fixture.Post)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Version() != 4 {
		t.Fatal("fixture did not cross the public v4 boundary")
	}
	return frozen
}

func mustScopeBindTestPredicate(t *testing.T, fixture schematest.GraphFixture, predicate golem.Predicate[testPost]) golem.FrozenPredicate {
	t.Helper()
	frozen, err := predicate.Freeze(modelDescriptor[testPost](fixture.Post))
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func assertScopeBindTestProvenance(t *testing.T, scope ir.FactScope, frozen golem.FrozenPolicy, template compilerir.ScopeTemplateIR) {
	t.Helper()
	view, present := frozen.View().FactScopeBinding()
	if !present {
		t.Fatal("public scope binding is absent")
	}
	digest := sha256.Sum256(view.CanonicalBytes())
	want := ir.ScopeBinding{TemplateID: bindFixedID(t, string(template.ID)), Template: ir.Fingerprint(bindDigest(t, string(template.Fingerprint))), Arguments: ir.Fingerprint(digest)}
	actual, present := scope.Binding()
	if !present || actual != want {
		t.Fatalf("scope provenance=%+v present=%v want=%+v", actual, present, want)
	}
	before, err := ir.FactScopeFingerprint(scope)
	if err != nil {
		t.Fatal(err)
	}
	actual.TemplateID[0] ^= 0xff
	actual.Arguments[0] ^= 0xff
	after, err := ir.FactScopeFingerprint(scope)
	if err != nil || !bytes.Equal(before[:], after[:]) {
		t.Fatalf("binding accessor exposed mutable provenance: %v", err)
	}
}

func modelDescriptor[M any](model golem.ModelID) golem.ModelDescriptor[M] {
	return golem.GeneratedModelDescriptor[M](model, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
}

func bindDigest(t *testing.T, value string) golem.SchemaDigest {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("invalid fixture digest %q: %v", value, err)
	}
	var result golem.SchemaDigest
	copy(result[:], decoded)
	return result
}

func bindFixedID(t *testing.T, value string) [16]byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		t.Fatalf("invalid fixture identity %q: %v", value, err)
	}
	var result [16]byte
	copy(result[:], decoded)
	return result
}
