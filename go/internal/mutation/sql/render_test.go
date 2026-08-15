package sql

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

func TestRenderIsDeterministicAcrossShuffledScalarInput(t *testing.T) {
	fixture := schematest.New(t)
	proof := testProof(t, fixture, policyir.ProviderPostgreSQL)
	id := uuidValue(1)
	author := uuidValue(2)
	title, _ := policyir.StringValue("hello")
	idType := testType(t, fixture, fixture.Post, fixture.PostID, policyir.ProviderPostgreSQL)
	authorType := testType(t, fixture, fixture.Post, fixture.AuthorID, policyir.ProviderPostgreSQL)
	titleType := testType(t, fixture, fixture.Post, fixture.PostTitle, policyir.ProviderPostgreSQL)
	setID, _ := mutationir.NewSet(policyir.FieldID(fixture.PostID), idType, id)
	setAuthor, _ := mutationir.NewSet(policyir.FieldID(fixture.AuthorID), authorType, author)
	setTitle, _ := mutationir.NewSet(policyir.FieldID(fixture.PostTitle), titleType, title)

	first := createPlan(t, fixture, []mutationir.ScalarOperation{setTitle, setID, setAuthor})
	second := createPlan(t, fixture, []mutationir.ScalarOperation{setAuthor, setTitle, setID})
	left, err := Render(first, fixture.Registry, policyir.ProviderPostgreSQL, proof)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Render(second, fixture.Registry, policyir.ProviderPostgreSQL, proof)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left.Statements(), right.Statements()) {
		t.Fatalf("shuffled equivalent plans rendered differently:\n%#v\n%#v", left.Statements(), right.Statements())
	}
	statement := left.Statements()[0]
	if statement.Role() != ApplyCreate || !strings.Contains(statement.SQL(), `INSERT INTO "public"."posts"`) || !strings.Contains(statement.SQL(), `$1`) {
		t.Fatalf("unexpected PostgreSQL create SQL: %s", statement.SQL())
	}
	if len(statement.Bindings()) != 3 {
		t.Fatalf("bindings=%d, want 3", len(statement.Bindings()))
	}
}

func TestUpdateRendersActionConstraintLockAtomicOperationsAndPostcondition(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	id := uuidValue(7)
	text, _ := policyir.StringValue("next")
	delta, _ := policyir.SignedValue(policyir.ValueInt64, 3)
	titleType := testType(t, fixture, fixture.Post, fixture.PostTitle, policyir.ProviderPostgreSQL)
	bigType := testType(t, fixture, fixture.Post, fixture.PostBigInt, policyir.ProviderPostgreSQL)
	setTitle, _ := mutationir.NewSet(policyir.FieldID(fixture.PostTitle), titleType, text)
	increment, _ := mutationir.NewIncrement(policyir.FieldID(fixture.PostBigInt), bigType, delta)
	plan := updatePlan(t, fixture, id, []mutationir.ScalarOperation{setTitle, increment}, true)
	program, err := Render(plan, fixture.Registry, policyir.ProviderPostgreSQL, testProof(t, fixture, policyir.ProviderPostgreSQL))
	if err != nil {
		t.Fatal(err)
	}
	statements := program.Statements()
	if program.TransactionRequirement() != PostgreSQLTransaction || len(statements) != 3 {
		t.Fatalf("transaction=%d statements=%d", program.TransactionRequirement(), len(statements))
	}
	identity := program.IdentityVerification()
	beforeIndex, hasBefore := identity.BeforeStatement()
	if identity.Behavior() != mutationir.IdentityUnchanged || !hasBefore || beforeIndex != 0 || identity.AfterStatement() != 1 || !reflect.DeepEqual(identity.Fields(), []policyir.FieldID{policyir.FieldID(fixture.PostID)}) {
		t.Fatalf("identity verification=%#v", identity)
	}
	if statements[0].Role() != SelectPreImage || !strings.HasSuffix(statements[0].SQL(), " FOR UPDATE") {
		t.Fatalf("pre-image SQL=%s", statements[0].SQL())
	}
	if len(statements[0].AuthorizationColumns()) != 2 {
		t.Fatalf("pre-image field grants=%d", len(statements[0].AuthorizationColumns()))
	}
	if statements[1].Role() != ApplyUpdate || !strings.Contains(statements[1].SQL(), `"big_int" = ("big_int" + $2)`) {
		t.Fatalf("update SQL=%s", statements[1].SQL())
	}
	if statements[2].Role() != VerifyPostcondition {
		t.Fatalf("last role=%d", statements[2].Role())
	}
	if index, field, ok := statements[2].Bindings()[0].PriorResult(); !ok || index != 1 || field != policyir.FieldID(fixture.PostID) {
		t.Fatalf("postcondition binding=%#v", statements[2].Bindings()[0])
	}
}

func TestPostconditionDisjunctionRemainsGroupedUnderPrimaryIdentity(t *testing.T) {
	fixture := schematest.New(t)
	model := policyir.ModelID(fixture.Post)
	truth, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	falsehood, err := policyir.NewConstant(model, false)
	if err != nil {
		t.Fatal(err)
	}
	disjunction, err := policyir.NewLogical(model, policyir.LogicalOr, []policyir.Condition{truth, falsehood})
	if err != nil {
		t.Fatal(err)
	}
	target := target(t, fixture, uuidValue(72), nil)
	selection, err := mutationir.NewSelectionRequirement(policyir.ActionUpdate, truth)
	if err != nil {
		t.Fatal(err)
	}
	image, err := mutationir.NewImageRequirements(model, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.PostTitle)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	operation := setString(t, fixture, fixture.PostTitle, "grouped")
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Update, Model: model, Target: &target, ScalarOperations: []mutationir.ScalarOperation{operation}, Before: image, After: image, Selection: &selection, RowPostcondition: &disjunction, Identity: mutationir.IdentityUnchanged})
	if err != nil {
		t.Fatal(err)
	}
	program, err := Render(plan(t, graph, image), fixture.Registry, policyir.ProviderSQLite, testProof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		t.Fatal(err)
	}
	statements := program.Statements()
	if len(statements) != 3 || statements[2].Role() != VerifyPostcondition {
		t.Fatalf("statement roles=%#v", statements)
	}
	sql := statements[2].SQL()
	if !strings.Contains(sql, `WHERE "golem_m0"."id" = ?1 AND ((TRUE) OR (FALSE))`) {
		t.Fatalf("postcondition disjunction escaped primary identity grouping: %s", sql)
	}
}

func TestVersionedOptimisticConcurrencySQLIsExactCAS(t *testing.T) {
	fixture := schematest.NewOptimisticConcurrency(t)
	id := uuidValue(73)
	text, _ := policyir.StringValue("versioned")
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		provider := provider
		t.Run(string(provider), func(t *testing.T) {
			titleType := testType(t, fixture, fixture.Post, fixture.PostTitle, provider)
			setTitle, err := mutationir.NewSet(policyir.FieldID(fixture.PostTitle), titleType, text)
			if err != nil {
				t.Fatal(err)
			}
			plan := updatePlan(t, fixture, id, []mutationir.ScalarOperation{setTitle}, true)
			if _, err := Render(plan, fixture.Registry, provider, testProof(t, fixture, provider)); err == nil {
				t.Fatal("ordinary renderer accepted an existing-row versioned mutation")
			}
			program, err := RenderVersioned(plan, fixture.Registry, provider, testProof(t, fixture, provider))
			if err != nil {
				t.Fatal(err)
			}
			field, enabled := program.OptimisticConcurrency()
			statements := program.Statements()
			if !enabled || field != policyir.FieldID(fixture.PostBigInt) || !program.RequiresConcurrencyPrecheck() || len(statements) < 2 {
				t.Fatalf("versioned program field=%x enabled=%t precheck=%t statements=%d", field, enabled, program.RequiresConcurrencyPrecheck(), len(statements))
			}
			apply := statements[1]
			if !strings.Contains(apply.SQL(), `"big_int" = ("big_int" + 1)`) || !strings.Contains(apply.SQL(), `"big_int" < 9223372036854775807`) {
				t.Fatalf("versioned update SQL=%s", apply.SQL())
			}
			foundObservedBinding := false
			for _, binding := range apply.Bindings() {
				if source, boundField, ok := binding.PriorResult(); ok && source == 0 && boundField == policyir.FieldID(fixture.PostBigInt) {
					foundObservedBinding = true
				}
			}
			if !foundObservedBinding {
				t.Fatalf("versioned update does not bind the locked observed token: %#v", apply.Bindings())
			}
		})
	}
}

func TestIdentityChangingScalarUpdateRendersBeforeAndAfterIdentityDataflow(t *testing.T) {
	fixture := schematest.New(t)
	oldID, newID := uuidValue(70), uuidValue(71)
	typ := testType(t, fixture, fixture.Post, fixture.PostID, policyir.ProviderSQLite)
	operation, _ := mutationir.NewSet(policyir.FieldID(fixture.PostID), typ, newID)
	target := target(t, fixture, oldID, nil)
	truth, _ := policyir.NewConstant(policyir.ModelID(fixture.Post), true)
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, truth)
	image, _ := mutationir.NewImageRequirements(policyir.ModelID(fixture.Post), []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Update, Model: policyir.ModelID(fixture.Post), Target: &target, ScalarOperations: []mutationir.ScalarOperation{operation}, Before: image, After: image, Selection: &selection, RowPostcondition: &truth, Identity: mutationir.IdentityMayChange})
	if err != nil {
		t.Fatal(err)
	}
	program, err := Render(plan(t, graph, image), fixture.Registry, policyir.ProviderSQLite, testProof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		t.Fatal(err)
	}
	verification := program.IdentityVerification()
	before, present := verification.BeforeStatement()
	if verification.Behavior() != mutationir.IdentityMayChange || !present || before != 0 || verification.AfterStatement() != 1 || !reflect.DeepEqual(verification.Fields(), []policyir.FieldID{policyir.FieldID(fixture.PostID)}) {
		t.Fatalf("identity verification=%#v", verification)
	}
	statements := program.Statements()
	if len(statements) != 3 || statements[0].Role() != SelectPreImage || statements[1].Role() != ApplyUpdate || statements[2].Role() != VerifyPostcondition {
		t.Fatalf("identity-changing statement roles=%#v", statements)
	}
	if source, field, ok := statements[2].Bindings()[0].PriorResult(); !ok || source != 1 || field != policyir.FieldID(fixture.PostID) {
		t.Fatalf("postcondition did not rebind to persisted after identity: %#v", statements[2].Bindings())
	}
}

func TestCallerTextCannotBecomeIdentifierOrSQL(t *testing.T) {
	fixture := schematest.New(t)
	injected := `x' OR 1=1 --`
	value, _ := policyir.StringValue(injected)
	typ := testType(t, fixture, fixture.Post, fixture.PostTitle, policyir.ProviderSQLite)
	operand, _ := policyir.OneOperand(value)
	requirements, _ := operator.ValidateShape(policyir.OperatorEqual, operator.Shape{Node: policyir.ConditionScalar, FieldType: typ, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: policyir.PortableProviders()})
	guard, _ := policyir.NewScalar(policyir.ModelID(fixture.Post), policyir.FieldID(fixture.PostTitle), typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
	plan := deletePlan(t, fixture, uuidValue(8), &guard)
	program, err := Render(plan, fixture.Registry, policyir.ProviderSQLite, testProof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
	statement := program.Statements()[0]
	if strings.Contains(statement.SQL(), injected) {
		t.Fatalf("caller text entered SQL: %s", statement.SQL())
	}
	found := false
	for _, binding := range statement.Bindings() {
		if value, ok := binding.StaticValue(); ok && value == injected {
			found = true
		}
	}
	if !found {
		t.Fatalf("caller text was not retained as a positional bind: %#v", statement.Bindings())
	}
	if !strings.Contains(statement.SQL(), `"posts"`) || strings.Contains(statement.SQL(), "x'") {
		t.Fatalf("descriptor-owned identifier rendering failed: %s", statement.SQL())
	}
}

func TestRenderFailsClosedForUnownedWork(t *testing.T) {
	fixture := schematest.New(t)
	plan := updatePlan(t, fixture, uuidValue(1), []mutationir.ScalarOperation{setString(t, fixture, fixture.PostTitle, "value")}, false)
	program, err := Render(plan, fixture.Registry, policyir.ProviderSQLite, testProof(t, fixture, policyir.ProviderSQLite))
	if err != nil || len(program.Statements()) == 0 {
		t.Fatalf("baseline render: program=%#v err=%v", program, err)
	}

	wrongProof := testProof(t, fixture, policyir.ProviderPostgreSQL)
	_, err = Render(plan, fixture.Registry, policyir.ProviderSQLite, wrongProof)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeProvider {
		t.Fatalf("wrong proof error=%v", err)
	}
}

func TestAtomicNullAndDecrementExpressionsAreClosedAndBound(t *testing.T) {
	nullable, err := policyir.NewTypeRef(policyir.ValueString, true, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	nullOperation, err := mutationir.NewNull(policyir.FieldID{15: 1}, nullable)
	if err != nil {
		t.Fatal(err)
	}
	context := renderContext{provider: policyir.ProviderSQLite, dialect: sqliteprovider.NewPolicyDialect()}
	var bindings []Binding
	expression, err := context.scalarExpression(nullOperation, `"optional"`, &bindings)
	if err != nil || expression != "NULL" || len(bindings) != 0 {
		t.Fatalf("null expression=%q bindings=%#v err=%v", expression, bindings, err)
	}

	integer, _ := policyir.NewTypeRef(policyir.ValueInt64, false, 0, 0, policyir.EnumID{}, nil, 0)
	delta, _ := policyir.SignedValue(policyir.ValueInt64, 4)
	decrement, _ := mutationir.NewDecrement(policyir.FieldID{15: 2}, integer, delta)
	expression, err = context.scalarExpression(decrement, `"counter"`, &bindings)
	if err != nil || expression != `("counter" - ?1)` || len(bindings) != 1 {
		t.Fatalf("decrement expression=%q bindings=%#v err=%v", expression, bindings, err)
	}
	if value, ok := bindings[0].StaticValue(); !ok || value != int64(4) {
		t.Fatalf("decrement binding=%#v", bindings[0])
	}
}

func TestLiveSQLiteCreateDefaultNoOpIncrementAndDelete(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	ctx := context.Background()
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "mutation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, `CREATE TABLE "posts" ("id" TEXT PRIMARY KEY, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL DEFAULT 'database-default', "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`); err != nil {
		t.Fatal(err)
	}
	proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint()))
	if err != nil {
		t.Fatal(err)
	}

	id, author := uuidValue(21), uuidValue(22)
	idType := testType(t, fixture, fixture.Post, fixture.PostID, policyir.ProviderSQLite)
	authorType := testType(t, fixture, fixture.Post, fixture.AuthorID, policyir.ProviderSQLite)
	setID, _ := mutationir.NewSet(policyir.FieldID(fixture.PostID), idType, id)
	setAuthor, _ := mutationir.NewSet(policyir.FieldID(fixture.AuthorID), authorType, author)
	create := createPlanWithFields(t, fixture, []mutationir.ScalarOperation{setAuthor, setID}, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.PostTitle), policyir.FieldID(fixture.PostBigInt)})
	created := executeProgram(t, ctx, database, mustRender(t, create, fixture, proof))
	if got := created[0][policyir.FieldID(fixture.PostTitle)]; got != "database-default" {
		t.Fatalf("persisted default=%#v", got)
	}

	// The renderer now precomputes the false grant on the locked pre-image and
	// leaves exact no-op classification to the decoded runtime diff.
	noOp := updatePlan(t, fixture, id, []mutationir.ScalarOperation{setString(t, fixture, fixture.PostTitle, "database-default")}, true)
	executeProgram(t, ctx, database, mustRender(t, noOp, fixture, proof))

	delta, _ := policyir.SignedValue(policyir.ValueInt64, 5)
	bigType := testType(t, fixture, fixture.Post, fixture.PostBigInt, policyir.ProviderSQLite)
	increment, _ := mutationir.NewIncrement(policyir.FieldID(fixture.PostBigInt), bigType, delta)
	incrementPlan := updatePlan(t, fixture, id, []mutationir.ScalarOperation{increment}, false)
	incremented := executeProgram(t, ctx, database, mustRender(t, incrementPlan, fixture, proof))
	if got := incremented[1][policyir.FieldID(fixture.PostBigInt)]; got != int64(5) {
		t.Fatalf("incremented value=%#v", got)
	}

	deleted := executeProgram(t, ctx, database, mustRender(t, deletePlan(t, fixture, id, nil), fixture, proof))
	if deleted[1][policyir.FieldID(fixture.PostID)] == nil {
		t.Fatal("delete did not return exact affected identity")
	}
	var count int
	if err := database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts"`); err != nil || count != 0 {
		t.Fatalf("remaining rows=%d err=%v", count, err)
	}
}

func executeProgram(t *testing.T, ctx context.Context, database *sqlx.DB, program Program) map[uint32]map[policyir.FieldID]any {
	t.Helper()
	tx, err := database.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	results := make(map[uint32]map[policyir.FieldID]any)
	for statementIndex, statement := range program.Statements() {
		args := make([]any, len(statement.Bindings()))
		for index, binding := range statement.Bindings() {
			if value, ok := binding.StaticValue(); ok {
				args[index] = value
				continue
			}
			source, field, ok := binding.PriorResult()
			if !ok {
				t.Fatal("unknown binding kind")
			}
			args[index] = results[source][field]
		}
		rows, err := tx.QueryxContext(ctx, statement.SQL(), args...)
		if err != nil {
			t.Fatalf("statement %d (%d): %s args=%#v: %v", statementIndex, statement.Role(), statement.SQL(), args, err)
		}
		if !rows.Next() {
			rows.Close()
			t.Fatalf("statement %d returned no row", statementIndex)
		}
		columns := statement.Columns()
		authorizations := statement.AuthorizationColumns()
		values := make([]any, len(columns)+len(authorizations))
		destinations := make([]any, len(values))
		for index := range destinations {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if rows.Next() {
			rows.Close()
			t.Fatalf("statement %d returned more than one row", statementIndex)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		row := make(map[policyir.FieldID]any, len(columns))
		for index, column := range columns {
			row[column.FieldID()] = values[index]
		}
		results[uint32(statementIndex)] = row
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return results
}

func mustRender(t *testing.T, plan mutationir.Plan, fixture schematest.Fixture, proof policysql.CapabilityProof) Program {
	t.Helper()
	program, err := Render(plan, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func createPlan(t *testing.T, fixture schematest.Fixture, operations []mutationir.ScalarOperation) mutationir.Plan {
	return createPlanWithFields(t, fixture, operations, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID), policyir.FieldID(fixture.PostTitle)})
}

func createPlanWithFields(t *testing.T, fixture schematest.Fixture, operations []mutationir.ScalarOperation, afterFields []policyir.FieldID) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	post, _ := policyir.NewConstant(model, true)
	after, _ := mutationir.NewImageRequirements(model, afterFields, nil)
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Create, Model: model, ScalarOperations: operations, After: after, RowPostcondition: &post, Identity: mutationir.IdentityProduced})
	if err != nil {
		t.Fatal(err)
	}
	return plan(t, graph, after)
}

func updatePlan(t *testing.T, fixture schematest.Fixture, id policyir.Value, operations []mutationir.ScalarOperation, denyChangedField bool) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	target := target(t, fixture, id, nil)
	truth, _ := policyir.NewConstant(model, true)
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, truth)
	fields := append([]policyir.FieldID{policyir.FieldID(fixture.PostID)}, scalarOperationFields(operations)...)
	before, _ := mutationir.NewImageRequirements(model, fields, nil)
	after, _ := mutationir.NewImageRequirements(model, fields, nil)
	var fieldAuth []mutationir.FieldAuthorization
	if denyChangedField {
		denied, _ := policyir.NewConstant(model, false)
		for _, operation := range operations {
			authorization, _ := mutationir.NewFieldAuthorization(operation.FieldID(), denied)
			fieldAuth = append(fieldAuth, authorization)
		}
	}
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Update, Model: model, Target: &target, ScalarOperations: operations, Before: before, After: after, Selection: &selection, RowPostcondition: &truth, FieldConditions: fieldAuth, Identity: mutationir.IdentityUnchanged})
	if err != nil {
		t.Fatal(err)
	}
	return plan(t, graph, after)
}

func deletePlan(t *testing.T, fixture schematest.Fixture, id policyir.Value, guard *policyir.Condition) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	target := target(t, fixture, id, guard)
	truth, _ := policyir.NewConstant(model, true)
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionDelete, truth)
	before, _ := mutationir.NewImageRequirements(model, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.PostTitle)}, nil)
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Delete, Model: model, Target: &target, Before: before, Selection: &selection, Identity: mutationir.IdentityUnchanged})
	if err != nil {
		t.Fatal(err)
	}
	return plan(t, graph, before)
}

func target(t *testing.T, fixture schematest.Fixture, id policyir.Value, guard *policyir.Condition) mutationir.Target {
	t.Helper()
	selector, _ := mutationir.NewSelectorValue(policyir.FieldID(fixture.PostID), id)
	target, err := mutationir.NewTarget(policyir.ModelID(fixture.Post), fixture.PostKey, []mutationir.SelectorValue{selector}, guard)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func plan(t *testing.T, graph mutationir.Graph, result mutationir.ImageRequirements) mutationir.Plan {
	t.Helper()
	requirement, _ := mutationir.NewProviderRequirement(policyir.PortableProviders(), mutationir.CapabilityTransaction)
	bounds, _ := mutationir.NewStatementBounds(999, 1000)
	value, err := mutationir.NewPlan(mutationir.PlanInput{Stance: mutationir.Caller, Graph: graph, Result: result, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func testType(t *testing.T, fixture schematest.Fixture, model golem.ModelID, field golem.FieldID, provider policyir.Provider) policyir.TypeRef {
	t.Helper()
	resolved, ok := policysql.SchemaResolver(fixture.Registry).Field(provider, policyir.ModelID(model), policyir.FieldID(field))
	if !ok {
		t.Fatalf("field %x has no policy descriptor", field)
	}
	return resolved.Type
}

func testProof(t *testing.T, fixture schematest.Fixture, provider policyir.Provider) policysql.CapabilityProof {
	t.Helper()
	proof, err := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func setString(t *testing.T, fixture schematest.Fixture, field golem.FieldID, value string) mutationir.ScalarOperation {
	t.Helper()
	typ := testType(t, fixture, fixture.Post, field, policyir.ProviderSQLite)
	bound, _ := policyir.StringValue(value)
	operation, err := mutationir.NewSet(policyir.FieldID(field), typ, bound)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func uuidValue(last byte) policyir.Value {
	var value [16]byte
	value[15] = last
	return policyir.UUIDValue(value)
}
