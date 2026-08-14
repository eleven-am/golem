package batch

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

type relationBatchPost struct{}
type relationBatchUser struct{}

func TestBatchMutationExactBoundaryAndOverflowWithoutTruncation(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	plan := updatePlan(t, fixture, 2, false, nil)
	program, err := Render(plan, fixture.Registry, policyir.ProviderPostgreSQL, proof(t, fixture, policyir.ProviderPostgreSQL))
	if err != nil {
		t.Fatal(err)
	}
	if program.SentinelRows() != 3 || !strings.Contains(program.CaptureStatement().SQL(), "LIMIT 3 FOR UPDATE") {
		t.Fatalf("capture does not expose the exact +1 sentinel: %s", program.CaptureStatement().SQL())
	}
	rows := []mutationdecode.Row{postRow(t, fixture, 1, "one"), postRow(t, fixture, 2, "two")}
	prepared, err := program.PrepareCaptured(rows)
	if err != nil || prepared.Count() != 2 || len(prepared.Statements()) == 0 {
		t.Fatalf("boundary prepare=%#v err=%v", prepared, err)
	}
	_, err = program.PrepareCaptured(append(rows, postRow(t, fixture, 3, "three")))
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeLimit {
		t.Fatalf("overflow error=%v", err)
	}
}

func TestBatchMutationCapturesStablePrimaryKeySet(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	program := mustRender(t, updatePlan(t, fixture, 4, false, nil), fixture, policyir.ProviderSQLite)
	if program.TransactionRequirement() != SQLiteImmediateTransaction || strings.Contains(program.CaptureStatement().SQL(), "FOR UPDATE") {
		t.Fatalf("SQLite stabilization contract is wrong: tx=%d sql=%s", program.TransactionRequirement(), program.CaptureStatement().SQL())
	}
	before := []mutationdecode.Row{postRow(t, fixture, 1, "old"), postRow(t, fixture, 2, "old")}
	prepared, err := program.PrepareCaptured(before)
	if err != nil {
		t.Fatal(err)
	}
	after := []mutationdecode.Row{postRow(t, fixture, 1, "next"), postRow(t, fixture, 3, "next")}
	if _, err := prepared.Verify(after, after, 0); err == nil || !strings.Contains(err.Error(), string(CodeSet)) {
		t.Fatalf("changed target set was accepted: %v", err)
	}
	for _, statement := range prepared.Statements() {
		if statement.ExpectedRows() == 0 || statement.Cardinality() != ExactlyCapturedRows {
			t.Fatalf("statement has no exact cardinality: %#v", statement)
		}
	}
}

func TestBatchRejectsPartialImagesAtEveryExactSetBoundary(t *testing.T) {
	fixture := schematest.New(t)
	program := mustRender(t, updatePlan(t, fixture, 2, false, nil), fixture, policyir.ProviderSQLite)
	id := uuidValue(1)
	partial, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.PostID), id)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = program.PrepareCaptured([]mutationdecode.Row{partial})
	assertIncompleteScalarImage(t, "capture", err)
	before := []mutationdecode.Row{postRow(t, fixture, 1, "old")}
	prepared, err := program.PrepareCaptured(before)
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepared.Verify([]mutationdecode.Row{partial}, []mutationdecode.Row{partial}, 0)
	assertIncompleteScalarImage(t, "apply", err)
	_, err = prepared.Verify(before, []mutationdecode.Row{partial}, 0)
	assertIncompleteScalarImage(t, "after", err)
}

func assertIncompleteScalarImage(t *testing.T, phase string, err error) {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeSet {
		t.Fatalf("%s partial image error=%#v", phase, err)
	}
	if !strings.Contains(failure.Detail, "not a complete scalar image") {
		t.Fatalf("%s partial image was refused for another set reason: %s", phase, failure.Detail)
	}
}

func TestBatchMutationEmitsOneOrderedFactPerAffectedRow(t *testing.T) {
	fixture := schematest.New(t)
	program := mustRender(t, updatePlan(t, fixture, 4, true, nil), fixture, policyir.ProviderPostgreSQL)
	before := []mutationdecode.Row{postRow(t, fixture, 1, "old"), postRow(t, fixture, 2, "old")}
	prepared, _ := program.PrepareCaptured(before)
	after := []mutationdecode.Row{postRow(t, fixture, 2, "next"), postRow(t, fixture, 1, "next")}
	verified, err := prepared.VerifyAuthorized(authorizedRows(t, before, policyir.FieldID(fixture.PostTitle), true), after, after, 11)
	if err != nil {
		t.Fatal(err)
	}
	facts := verified.Facts()
	if verified.Count() != 2 || len(facts) != 2 || facts[0].Ordinal() != 11 || facts[1].Ordinal() != 12 || facts[0].Action() != mutationir.FactUpdated {
		t.Fatalf("facts are not one-per-row in capture order: %#v", facts)
	}
	first, _ := mutationdecode.PrimaryIdentity(fixture.Registry, facts[0].Before())
	value, _ := first.Components()[0].PolicyValue()
	id, _ := value.UUID()
	if id[15] != 1 {
		t.Fatalf("facts did not retain capture order: %x", id)
	}
}

func TestBatchIdentityChangeIsRefusedBeforeWrite(t *testing.T) {
	fixture := schematest.New(t)
	typ := fieldType(t, fixture, fixture.PostID, policyir.ProviderPostgreSQL)
	value := uuidValue(9)
	operation, _ := mutationir.NewSet(policyir.FieldID(fixture.PostID), typ, value)
	plan := updatePlan(t, fixture, 2, false, &operation)
	_, err := Render(plan, fixture.Registry, policyir.ProviderPostgreSQL, proof(t, fixture, policyir.ProviderPostgreSQL))
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeIdentity {
		t.Fatalf("identity update error=%v", err)
	}
}

func TestBatchRelationTraversingFieldAuthorizationIsEvaluatedInProviderSQL(t *testing.T) {
	fixture := schematest.NewIndexed(t)
	base := updatePlan(t, fixture, 2, false, nil)
	old := base.Graph().Nodes()[0]
	author := golem.GeneratedToOne[relationBatchPost, relationBatchUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	descriptor := golem.GeneratedModelDescriptor[relationBatchPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	frozen, err := author.Is(golem.All[relationBatchUser]()).Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	condition, err := mutationbind.BatchPredicate(frozen, fixture.Post, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	authorization, _ := mutationir.NewFieldAuthorization(policyir.FieldID(fixture.PostTitle), condition)
	predicate, _ := old.Predicate()
	selection, _ := old.SelectionRequirement()
	postcondition, _ := old.RowPostcondition()
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: old.Operation(), Model: old.ModelID(), Predicate: &predicate, ScalarOperations: old.ScalarOperations(), Selection: &selection, RowPostcondition: &postcondition, FieldConditions: []mutationir.FieldAuthorization{authorization}, Before: old.BeforeRequirements(), After: old.AfterRequirements(), Fact: old.Fact(), Identity: old.IdentityBehavior()})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := mutationir.NewPlan(mutationir.PlanInput{Stance: base.Stance(), Graph: graph, Result: base.ResultRequirements(), Providers: base.ProviderRequirements(), Retry: base.RetryClass(), Bounds: base.Bounds()})
	if err != nil {
		t.Fatal(err)
	}
	program, err := Render(plan, fixture.Registry, policyir.ProviderSQLite, proof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("relation batch render failure: %T: %v", cause, cause)
		}
		t.Fatal(err)
	}
	prepared, err := program.PrepareCaptured([]mutationdecode.Row{postRow(t, fixture, 1, "one")})
	if err != nil {
		t.Fatal(err)
	}
	statements := prepared.Statements()
	if len(statements) == 0 || !strings.Contains(statements[0].SQL(), "EXISTS") {
		t.Fatalf("relation field grant was not compiled into provider SQL: %#v", statements)
	}
}

func TestDeleteManyUsesCapturedSetAndProducesNoAfterImage(t *testing.T) {
	fixture := schematest.New(t)
	program := mustRender(t, deletePlan(t, fixture, 3, true), fixture, policyir.ProviderPostgreSQL)
	before := []mutationdecode.Row{postRow(t, fixture, 1, "one"), postRow(t, fixture, 2, "two")}
	prepared, err := program.PrepareCaptured(before)
	if err != nil {
		t.Fatal(err)
	}
	statements := prepared.Statements()
	if len(statements) != 2 || statements[0].Role() != AuthorizePreImage || statements[1].Role() != ApplyDelete || strings.Contains(statements[1].SQL(), "title") && strings.Contains(strings.Split(statements[1].SQL(), " RETURNING ")[0], "title") {
		t.Fatalf("delete-many program did not authorize first then delete exact PKs: %#v", statements)
	}
	verified, err := prepared.Verify(before, nil, 7)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Count() != 2 || len(verified.Facts()) != 2 || verified.Facts()[0].Action() != mutationir.FactDeleted {
		t.Fatalf("delete facts=%#v", verified.Facts())
	}
}

func TestBatchCompositePrimaryKeyChunksUseEveryComponent(t *testing.T) {
	fixture := schematest.New(t)
	program := mustRender(t, updatePlan(t, fixture, 2, false, nil), fixture, policyir.ProviderPostgreSQL)
	// The fixture's physical fields are real descriptor-owned UUID columns. This
	// white-box substitution isolates the generic composite tuple machinery from
	// schema bootstrap tests, which already cover composite primary descriptors.
	program.primary = []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID)}
	program.context.primary = append([]policyir.FieldID(nil), program.primary...)
	prepared, err := program.PrepareCaptured([]mutationdecode.Row{postRow(t, fixture, 1, "one"), postRow(t, fixture, 2, "two")})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range prepared.Statements() {
		if statement.Role() == ApplyUpdate && (!strings.Contains(statement.SQL(), `"id"`) || !strings.Contains(statement.SQL(), `"author_id"`)) {
			t.Fatalf("composite identity component disappeared: %s", statement.SQL())
		}
		if uint32(len(statement.Bindings())) > program.context.plan.Bounds().MaxParameters() {
			t.Fatalf("composite chunk exceeded parameter bound")
		}
	}
}

func TestSystemBatchUsesSameExactSetKernelWithoutCallerAuthorization(t *testing.T) {
	fixture := schematest.New(t)
	program := mustRender(t, systemUpdatePlan(t, fixture, 2), fixture, policyir.ProviderSQLite)
	prepared, err := program.PrepareCaptured([]mutationdecode.Row{postRow(t, fixture, 1, "old")})
	if err != nil {
		t.Fatal(err)
	}
	statements := prepared.Statements()
	if len(statements) != 2 || statements[0].Role() != ApplyUpdate || statements[1].Role() != RehydrateAfterImage {
		t.Fatalf("system kernel ran caller authorization or omitted verification: %#v", statements)
	}
}

func TestBatchFieldAuthorizationUsesOnlyActualPersistedDiff(t *testing.T) {
	fixture := schematest.New(t)
	program := mustRender(t, updatePlan(t, fixture, 4, false, nil), fixture, policyir.ProviderPostgreSQL)
	before := []mutationdecode.Row{postRow(t, fixture, 1, "next"), postRow(t, fixture, 2, "old")}
	prepared, _ := program.PrepareCaptured(before)
	after := []mutationdecode.Row{postRow(t, fixture, 1, "next"), postRow(t, fixture, 2, "next")}
	authorized := append(
		authorizedRows(t, before[:1], policyir.FieldID(fixture.PostTitle), false),
		authorizedRows(t, before[1:], policyir.FieldID(fixture.PostTitle), true)...,
	)
	verified, err := prepared.VerifyAuthorized(authorized, after, after, 0)
	if err != nil {
		t.Fatal(err)
	}
	rows := verified.Rows()
	if len(rows[0].AuthoredChangedFields()) != 0 || len(rows[0].RequiredFieldAuthorizations()) != 0 {
		t.Fatalf("no-op row required field authorization: %#v", rows[0])
	}
	if !reflect.DeepEqual(rows[1].AuthoredChangedFields(), []policyir.FieldID{policyir.FieldID(fixture.PostTitle)}) || len(rows[1].RequiredFieldAuthorizations()) != 1 {
		t.Fatalf("changed row did not require its exact field authorization: %#v", rows[1])
	}
	if strings.Contains(prepared.Statements()[0].SQL(), "IS NOT DISTINCT FROM") || len(prepared.Statements()[0].AuthorizationColumns()) != 1 {
		t.Fatalf("authorization did not precompute a grant independently of provider equality: %s", prepared.Statements()[0].SQL())
	}

	_, err = prepared.VerifyAuthorized(authorizedRows(t, before, policyir.FieldID(fixture.PostTitle), false), after, after, 0)
	var denied *Error
	if !errors.As(err, &denied) || denied.Code != CodeForbidden || denied.Field != policyir.FieldID(fixture.PostTitle) {
		t.Fatalf("changed denied field was accepted: %#v err=%v", denied, err)
	}
}

func TestBatchDeniedFloatUsesExactLogicalBitsForChangeAndNoOp(t *testing.T) {
	fixture := schematest.NewLogicalDiff(t)
	model := policyir.ModelID(fixture.Record)
	typ, ok := policysql.SchemaResolver(fixture.Registry).Field(policyir.ProviderSQLite, model, policyir.FieldID(fixture.Score))
	if !ok {
		t.Fatal("score field is absent")
	}
	one, _ := policyir.Float64Value(1)
	next, _ := policyir.Float64Value(math.Nextafter(1, 2))
	operation, _ := mutationir.NewSet(policyir.FieldID(fixture.Score), typ.Type, next)
	truth, _ := policyir.NewConstant(model, true)
	denied, _ := policyir.NewConstant(model, false)
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, truth)
	authorization, _ := mutationir.NewFieldAuthorization(policyir.FieldID(fixture.Score), denied)
	image, _ := mutationir.NewImageRequirements(model, nil, nil)
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.UpdateMany, Model: model, Predicate: &truth, ScalarOperations: []mutationir.ScalarOperation{operation}, Selection: &selection, RowPostcondition: &truth, FieldConditions: []mutationir.FieldAuthorization{authorization}, Before: image, After: image, Fact: mutationir.NoFact(), Identity: mutationir.IdentityBatchChangeRefused})
	if err != nil {
		t.Fatal(err)
	}
	requirement, _ := mutationir.NewProviderRequirement(policyir.PortableProviders(), mutationir.CapabilityTransaction)
	bounds, _ := mutationir.NewStatementBounds(16, 2)
	plan, err := mutationir.NewPlan(mutationir.PlanInput{Stance: mutationir.Caller, Graph: graph, Result: image, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds})
	if err != nil {
		t.Fatal(err)
	}
	program, err := Render(plan, fixture.Registry, policyir.ProviderSQLite, logicalDiffProof(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	before := logicalDiffRow(t, fixture, one)
	prepared, err := program.PrepareCaptured([]mutationdecode.Row{before})
	if err != nil {
		t.Fatal(err)
	}
	grant, _ := NewFieldGrant(policyir.FieldID(fixture.Score), false)
	locked, _ := NewAuthorizedRow(before, grant)
	if _, err := prepared.VerifyAuthorized([]AuthorizedRow{locked}, []mutationdecode.Row{before}, []mutationdecode.Row{before}, 0); err != nil {
		t.Fatalf("bit-exact float no-op required a denied grant: %v", err)
	}
	changed := logicalDiffRow(t, fixture, next)
	_, err = prepared.VerifyAuthorized([]AuthorizedRow{locked}, []mutationdecode.Row{changed}, []mutationdecode.Row{changed}, 0)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeForbidden || failure.Field != policyir.FieldID(fixture.Score) {
		t.Fatalf("bit-distinct denied float change succeeded: %#v err=%v", failure, err)
	}
}

func TestBatchSQLIsParameterizedDeterministicAndProviderBounded(t *testing.T) {
	fixture := schematest.New(t)
	injected := `x' OR 1=1 --`
	condition := titleEquals(t, fixture, injected)
	plan := updatePlan(t, fixture, 3, false, nil)
	plan = replaceConstraint(t, plan, condition)
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		first := mustRender(t, plan, fixture, provider)
		second := mustRender(t, plan, fixture, provider)
		if first.CaptureStatement().SQL() != second.CaptureStatement().SQL() || !reflect.DeepEqual(first.CaptureStatement().Bindings(), second.CaptureStatement().Bindings()) {
			t.Fatalf("provider %d rendering is nondeterministic", provider)
		}
		if strings.Contains(first.CaptureStatement().SQL(), injected) {
			t.Fatalf("caller text entered SQL: %s", first.CaptureStatement().SQL())
		}
		found := false
		for _, binding := range first.CaptureStatement().Bindings() {
			if binding.Value() == injected {
				found = true
			}
		}
		if !found {
			t.Fatalf("caller value was not retained as a positional bind: %#v", first.CaptureStatement().Bindings())
		}
		prepared, err := first.PrepareCaptured([]mutationdecode.Row{postRow(t, fixture, 1, "old"), postRow(t, fixture, 2, "old"), postRow(t, fixture, 3, "old")})
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range prepared.Statements() {
			if uint32(len(statement.Bindings())) > plan.Bounds().MaxParameters() {
				t.Fatalf("parameter bound exceeded: %#v", statement)
			}
		}
	}
}

func mustRender(t *testing.T, plan mutationir.Plan, fixture schematest.Fixture, provider policyir.Provider) Program {
	t.Helper()
	result, err := Render(plan, fixture.Registry, provider, proof(t, fixture, provider))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func updatePlan(t *testing.T, fixture schematest.Fixture, maxRows uint32, facts bool, replacement *mutationir.ScalarOperation) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	truth, _ := policyir.NewConstant(model, true)
	typ := fieldType(t, fixture, fixture.PostTitle, policyir.ProviderPostgreSQL)
	value, _ := policyir.StringValue("next")
	operation, _ := mutationir.NewSet(policyir.FieldID(fixture.PostTitle), typ, value)
	if replacement != nil {
		operation = *replacement
	}
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, truth)
	authorization, _ := mutationir.NewFieldAuthorization(operation.FieldID(), truth)
	image, _ := mutationir.NewImageRequirements(model, nil, nil)
	fact := mutationir.NoFact()
	if facts {
		fact, _ = mutationir.NewFactRequirement(mutationir.FactUpdated, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	}
	node := mutationir.NodeInput{Operation: mutationir.UpdateMany, Model: model, Predicate: &truth, ScalarOperations: []mutationir.ScalarOperation{operation}, Selection: &selection, RowPostcondition: &truth, FieldConditions: []mutationir.FieldAuthorization{authorization}, Before: image, After: image, Fact: fact, Identity: mutationir.IdentityBatchChangeRefused}
	graph, err := mutationir.NewGraph(node)
	if err != nil {
		t.Fatal(err)
	}
	requirement, _ := mutationir.NewProviderRequirement(policyir.PortableProviders(), mutationir.CapabilityTransaction)
	bounds, _ := mutationir.NewStatementBounds(8, maxRows)
	input := mutationir.PlanInput{Stance: mutationir.Caller, Graph: graph, Result: image, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds}
	if facts {
		codec, _ := mutationir.NewFactCodecRequirement(1, "golem.fact.v1", [32]byte(fixture.Registry.GenerationDigest()))
		input.FactCodec = &codec
	}
	result, err := mutationir.NewPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func deletePlan(t *testing.T, fixture schematest.Fixture, maxRows uint32, facts bool) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	truth, _ := policyir.NewConstant(model, true)
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionDelete, truth)
	image, _ := mutationir.NewImageRequirements(model, nil, nil)
	fact := mutationir.NoFact()
	if facts {
		fact, _ = mutationir.NewFactRequirement(mutationir.FactDeleted, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil, nil)
	}
	node := mutationir.NodeInput{Operation: mutationir.DeleteMany, Model: model, Predicate: &truth, Selection: &selection, Before: image, After: image, Fact: fact, Identity: mutationir.IdentityBatchChangeRefused}
	graph, err := mutationir.NewGraph(node)
	if err != nil {
		t.Fatal(err)
	}
	requirement, _ := mutationir.NewProviderRequirement(policyir.PortableProviders(), mutationir.CapabilityTransaction)
	bounds, _ := mutationir.NewStatementBounds(8, maxRows)
	input := mutationir.PlanInput{Stance: mutationir.Caller, Graph: graph, Result: image, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds}
	if facts {
		codec, _ := mutationir.NewFactCodecRequirement(1, "golem.fact.v1", [32]byte(fixture.Registry.GenerationDigest()))
		input.FactCodec = &codec
	}
	result, err := mutationir.NewPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func systemUpdatePlan(t *testing.T, fixture schematest.Fixture, maxRows uint32) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	truth, _ := policyir.NewConstant(model, true)
	typ := fieldType(t, fixture, fixture.PostTitle, policyir.ProviderSQLite)
	value, _ := policyir.StringValue("next")
	operation, _ := mutationir.NewSet(policyir.FieldID(fixture.PostTitle), typ, value)
	image, _ := mutationir.NewImageRequirements(model, nil, nil)
	node := mutationir.NodeInput{Operation: mutationir.UpdateMany, Model: model, Predicate: &truth, ScalarOperations: []mutationir.ScalarOperation{operation}, Before: image, After: image, Fact: mutationir.NoFact(), Identity: mutationir.IdentityBatchChangeRefused}
	graph, err := mutationir.NewGraph(node)
	if err != nil {
		t.Fatal(err)
	}
	requirement, _ := mutationir.NewProviderRequirement(policyir.PortableProviders(), mutationir.CapabilityTransaction)
	bounds, _ := mutationir.NewStatementBounds(8, maxRows)
	result, err := mutationir.NewPlan(mutationir.PlanInput{Stance: mutationir.System, Graph: graph, Result: image, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func replaceConstraint(t *testing.T, plan mutationir.Plan, condition policyir.Condition) mutationir.Plan {
	t.Helper()
	old := plan.Graph().Nodes()[0]
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, condition)
	input := mutationir.NodeInput{Operation: old.Operation(), Model: old.ModelID(), Predicate: &condition, ScalarOperations: old.ScalarOperations(), Selection: &selection, RowPostcondition: &condition, FieldConditions: old.FieldAuthorizations(), Before: old.BeforeRequirements(), After: old.AfterRequirements(), Fact: old.Fact(), Identity: old.IdentityBehavior()}
	graph, err := mutationir.NewGraph(input)
	if err != nil {
		t.Fatal(err)
	}
	codec, hasCodec := plan.FactCodecRequirement()
	next := mutationir.PlanInput{Stance: plan.Stance(), Graph: graph, Result: plan.ResultRequirements(), Providers: plan.ProviderRequirements(), Retry: plan.RetryClass(), Bounds: plan.Bounds()}
	if hasCodec {
		next.FactCodec = &codec
	}
	result, err := mutationir.NewPlan(next)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func proof(t *testing.T, fixture schematest.Fixture, provider policyir.Provider) policysql.CapabilityProof {
	t.Helper()
	result, err := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func fieldType(t *testing.T, fixture schematest.Fixture, field golem.FieldID, provider policyir.Provider) policyir.TypeRef {
	t.Helper()
	value, ok := policysql.SchemaResolver(fixture.Registry).Field(provider, policyir.ModelID(fixture.Post), policyir.FieldID(field))
	if !ok {
		t.Fatal("field missing")
	}
	return value.Type
}

func postRow(t *testing.T, fixture schematest.Fixture, last byte, title string) mutationdecode.Row {
	t.Helper()
	id := uuidValue(last)
	author := uuidValue(99)
	text, _ := policyir.StringValue(title)
	row, err := mutationdecode.NewCompleteRow(fixture.Registry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.PostID), id), mutationdecode.Value(policyir.FieldID(fixture.AuthorID), author), mutationdecode.Value(policyir.FieldID(fixture.PostTitle), text)})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func authorizedRows(t *testing.T, rows []mutationdecode.Row, field policyir.FieldID, granted bool) []AuthorizedRow {
	t.Helper()
	grant, err := NewFieldGrant(field, granted)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]AuthorizedRow, len(rows))
	for index, row := range rows {
		result[index], err = NewAuthorizedRow(row, grant)
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func logicalDiffRow(t *testing.T, fixture schematest.LogicalDiffFixture, score policyir.Value) mutationdecode.Row {
	t.Helper()
	documentScalar, _ := policyir.JSONStringValue("same")
	document, _ := policyir.NewJSONValue(documentScalar)
	tag, _ := policyir.StringValue("tag")
	tags, _ := policyir.NewListValue([]policyir.Value{tag})
	row, err := mutationdecode.NewCompleteRow(fixture.Registry, policyir.ModelID(fixture.Record), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.ID), policyir.UUIDValue([16]byte{15: 1})),
		mutationdecode.Value(policyir.FieldID(fixture.Document), document),
		mutationdecode.Value(policyir.FieldID(fixture.Tags), tags),
		mutationdecode.Value(policyir.FieldID(fixture.Score), score),
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func logicalDiffProof(t *testing.T, fixture schematest.LogicalDiffFixture) policysql.CapabilityProof {
	t.Helper()
	proof, err := policysql.NewCapabilityProof(policyir.ProviderSQLite, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func uuidValue(last byte) policyir.Value {
	var value [16]byte
	value[15] = last
	return policyir.UUIDValue(value)
}

func titleEquals(t *testing.T, fixture schematest.Fixture, value string) policyir.Condition {
	t.Helper()
	typ := fieldType(t, fixture, fixture.PostTitle, policyir.ProviderPostgreSQL)
	operandValue, _ := policyir.StringValue(value)
	operand, _ := policyir.OneOperand(operandValue)
	requirements, err := operator.ValidateShape(policyir.OperatorEqual, operator.Shape{Node: policyir.ConditionScalar, FieldType: typ, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: policyir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := policyir.NewScalar(policyir.ModelID(fixture.Post), policyir.FieldID(fixture.PostTitle), typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
