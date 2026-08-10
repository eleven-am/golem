package upsert

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jackc/pgx/v5/pgconn"
)

type testPost struct{}

func TestPrepareRendersClosedGuardAndLockedProbePrograms(t *testing.T) {
	fixture := schematest.New(t)
	plan := systemPlan(t, fixture, mutationir.EngineOwnedUpsertRetry)

	postgres, err := Prepare(plan, fixture.Registry, policyir.ProviderPostgreSQL, proof(t, fixture, policyir.ProviderPostgreSQL))
	if err != nil {
		t.Fatal(err)
	}
	if postgres.TransactionRequirement() != mutationsql.PostgreSQLTransaction || postgres.GuardStatement().SQL() != "SELECT pg_catalog.pg_advisory_xact_lock($1)" || !strings.HasSuffix(postgres.ProbeStatement().SQL(), " FOR UPDATE") {
		t.Fatalf("unexpected PostgreSQL program: guard=%q probe=%q", postgres.GuardStatement().SQL(), postgres.ProbeStatement().SQL())
	}
	if _, present := postgres.CleanupStatement(); present {
		t.Fatal("PostgreSQL advisory guard unexpectedly has cleanup SQL")
	}

	sqlite, err := Prepare(plan, fixture.Registry, policyir.ProviderSQLite, proof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		t.Fatal(err)
	}
	if sqlite.TransactionRequirement() != mutationsql.SQLiteImmediateTransaction || !strings.HasPrefix(sqlite.GuardStatement().SQL(), `INSERT INTO "_golem_upsert_guard"`) || strings.HasSuffix(sqlite.ProbeStatement().SQL(), " FOR UPDATE") {
		t.Fatalf("unexpected SQLite program: guard=%q probe=%q", sqlite.GuardStatement().SQL(), sqlite.ProbeStatement().SQL())
	}
	cleanup, present := sqlite.CleanupStatement()
	if !present || !strings.HasPrefix(cleanup.SQL(), `DELETE FROM "_golem_upsert_guard"`) {
		t.Fatalf("SQLite cleanup=%q present=%t", cleanup.SQL(), present)
	}
	if postgres.GuardToken() != sqlite.GuardToken() {
		t.Fatal("the same canonical selector produced provider-dependent guard tokens")
	}
	root, _ := plan.Graph().Root()
	target, _ := root.Target()
	sharedPostgres, err := PrepareSelectorGuard(target, fixture.Registry, policyir.ProviderPostgreSQL, proof(t, fixture, policyir.ProviderPostgreSQL))
	if err != nil {
		t.Fatal(err)
	}
	sharedSQLite, err := PrepareSelectorGuard(target, fixture.Registry, policyir.ProviderSQLite, proof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		t.Fatal(err)
	}
	if sharedPostgres.Token() != postgres.GuardToken() || sharedSQLite.Token() != sqlite.GuardToken() || sharedPostgres.AcquireStatement().SQL() != postgres.GuardStatement().SQL() || sharedSQLite.AcquireStatement().SQL() != sqlite.GuardStatement().SQL() {
		t.Fatal("shared nested selector guard diverged from root upsert coordination")
	}
	left := sqlite.GuardStatement().Args()[0].([]byte)
	left[0] ^= 0xff
	right := sqlite.GuardStatement().Args()[0].([]byte)
	if reflect.DeepEqual(left, right) {
		t.Fatal("statement arguments alias program storage")
	}
}

func TestUpsertProbeUsesUpdateConstraintNotReadConstraint(t *testing.T) {
	fixture := schematest.New(t)
	injected := `x' OR 1=1 --`
	condition := stringEqual(t, fixture, injected)
	policy, err := policyir.NewPolicy(policyir.ModelID(fixture.Post), []policyir.Rule{
		modelRule(t, fixture, policyir.ActionRead, nil, 0),
		modelRule(t, fixture, policyir.ActionCreate, nil, 1),
		modelRule(t, fixture, policyir.ActionUpdate, &condition, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := callerPlan(t, fixture, policy, mutationir.EngineOwnedUpsertRetry)
	program, err := Prepare(plan, fixture.Registry, policyir.ProviderSQLite, proof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(program.ProbeStatement().SQL(), injected) || !strings.Contains(program.ProbeStatement().SQL(), `"title"`) {
		t.Fatalf("caller value entered SQL or update reach was omitted: %s", program.ProbeStatement().SQL())
	}
	found := false
	for _, argument := range program.ProbeStatement().Args() {
		found = found || argument == injected
	}
	if !found {
		t.Fatalf("caller value is absent from positional args: %#v", program.ProbeStatement().Args())
	}
}

func TestPrepareKeepsSystemTargetGuardOutOfSelectorTokenButInProbe(t *testing.T) {
	fixture := schematest.New(t)
	unguarded := systemPlan(t, fixture, mutationir.EngineOwnedUpsertRetry)
	guarded := systemGuardPlan(t, fixture, "must-match", mutationir.EngineOwnedUpsertRetry)
	left, err := Prepare(unguarded, fixture.Registry, policyir.ProviderSQLite, proof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		t.Fatal(err)
	}
	right, err := Prepare(guarded, fixture.Registry, policyir.ProviderSQLite, proof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		t.Fatal(err)
	}
	if left.GuardToken() != right.GuardToken() {
		t.Fatal("a non-identity target guard changed selector serialization")
	}
	if !strings.Contains(right.ProbeStatement().SQL(), `"title"`) {
		t.Fatalf("system target guard missing from update-reach probe: %s", right.ProbeStatement().SQL())
	}
	found := false
	for _, argument := range right.ProbeStatement().Args() {
		found = found || argument == "must-match"
	}
	if !found {
		t.Fatalf("system target guard is absent from probe args: %#v", right.ProbeStatement().Args())
	}
}

func TestRunFreezesOnceAndExecutesTruthfulOrder(t *testing.T) {
	program := preparedSQLite(t, mutationir.EngineOwnedUpsertRetry)
	attempt := &fakeAttempt{probeRows: 1}
	backend := &fakeBackend{attempts: []*fakeAttempt{attempt}}
	freezes := 0
	executor := branchExecutorFunc(func(_ context.Context, _ Attempt, node mutationir.Node, frozen FrozenValues) (any, error) {
		if node.Branch() != mutationir.UpsertUpdateBranch || string(frozen.Bytes()) != "frozen" {
			t.Fatalf("branch=%d frozen=%q", node.Branch(), frozen.Bytes())
		}
		return "updated", nil
	})
	result, err := Run(context.Background(), program, backend, func(context.Context) (FrozenValues, error) {
		freezes++
		return NewFrozenValues([]byte("frozen")), nil
	}, executor, Options{MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	if freezes != 1 || result.Branch() != UpdatedBranch || result.Attempt() != 1 || result.Value() != "updated" {
		t.Fatalf("freezes=%d result=%#v", freezes, result)
	}
	if got, want := attempt.roles, []StatementRole{AcquireSelectorGuard, ProbeUpdateReach, ReleaseSelectorGuard}; !reflect.DeepEqual(got, want) || attempt.finished != 1 || attempt.aborted != 0 {
		t.Fatalf("attempt roles=%v finish=%d abort=%d", got, attempt.finished, attempt.aborted)
	}
}

func TestUpsertRetryFreezesRuntimeOwnedValuesOnce(t *testing.T) {
	postgresUnique := &pgconn.PgError{Code: "23505", Message: "secret provider text"}

	t.Run("engine owned", func(t *testing.T) {
		program := preparedPostgreSQL(t, mutationir.EngineOwnedUpsertRetry)
		first, second := &fakeAttempt{}, &fakeAttempt{}
		backend := &fakeBackend{attempts: []*fakeAttempt{first, second}}
		calls, freezes := 0, 0
		executor := branchExecutorFunc(func(_ context.Context, _ Attempt, node mutationir.Node, frozen FrozenValues) (any, error) {
			calls++
			if node.Branch() != mutationir.UpsertCreateBranch || string(frozen.Bytes()) != "once" {
				t.Fatalf("branch=%d frozen=%q", node.Branch(), frozen.Bytes())
			}
			if calls == 1 {
				return nil, postgresUnique
			}
			return "created", nil
		})
		result, err := Run(context.Background(), program, backend, func(context.Context) (FrozenValues, error) {
			freezes++
			return NewFrozenValues([]byte("once")), nil
		}, executor, Options{MaxAttempts: 2})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 2 || freezes != 1 || result.Attempt() != 2 || first.aborted != 1 || second.finished != 1 || !reflect.DeepEqual(backend.ordinals, []uint32{1, 2}) {
			t.Fatalf("calls=%d freezes=%d result=%#v first=%#v second=%#v ordinals=%v", calls, freezes, result, first, second, backend.ordinals)
		}
	})

	t.Run("caller transaction", func(t *testing.T) {
		program := preparedPostgreSQL(t, mutationir.CallerTransactionNoReplay)
		attempt := &fakeAttempt{}
		backend := &fakeBackend{attempts: []*fakeAttempt{attempt, {}}}
		executor := branchExecutorFunc(func(context.Context, Attempt, mutationir.Node, FrozenValues) (any, error) { return nil, postgresUnique })
		_, err := Run(context.Background(), program, backend, func(context.Context) (FrozenValues, error) { return NewFrozenValues(nil), nil }, executor, Options{MaxAttempts: 50})
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != CodeConflict || strings.Contains(err.Error(), "secret provider text") {
			t.Fatalf("error=%v", err)
		}
		if len(backend.ordinals) != 1 || attempt.aborted != 1 {
			t.Fatalf("caller transaction replayed: ordinals=%v aborts=%d", backend.ordinals, attempt.aborted)
		}
	})
}

func TestUpsertRetriesWholeEngineAttemptAndExhaustsAsConflict(t *testing.T) {
	program := preparedPostgreSQL(t, mutationir.EngineOwnedUpsertRetry)
	attempts := []*fakeAttempt{{}, {}, {}}
	backend := &fakeBackend{attempts: append([]*fakeAttempt(nil), attempts...)}
	providerConflict := &pgconn.PgError{Code: "23505", Message: "provider constraint and values must stay private"}
	freezes, branches := 0, 0

	_, err := Run(context.Background(), program, backend, func(context.Context) (FrozenValues, error) {
		freezes++
		return NewFrozenValues([]byte("stable-runtime-owned-values")), nil
	}, branchExecutorFunc(func(_ context.Context, _ Attempt, node mutationir.Node, frozen FrozenValues) (any, error) {
		branches++
		if node.Branch() != mutationir.UpsertCreateBranch || string(frozen.Bytes()) != "stable-runtime-owned-values" {
			t.Fatalf("attempt %d branch=%d frozen=%q", branches, node.Branch(), frozen.Bytes())
		}
		return nil, providerConflict
	}), Options{MaxAttempts: 3})

	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeConflict {
		t.Fatalf("exhausted retry error=%v", err)
	}
	if freezes != 1 || branches != 3 || !reflect.DeepEqual(backend.ordinals, []uint32{1, 2, 3}) {
		t.Fatalf("freezes=%d branches=%d ordinals=%v", freezes, branches, backend.ordinals)
	}
	for ordinal, attempt := range attempts {
		if got, want := attempt.roles, []StatementRole{AcquireSelectorGuard, ProbeUpdateReach}; !reflect.DeepEqual(got, want) {
			t.Fatalf("attempt %d statement roles=%v want=%v", ordinal+1, got, want)
		}
		if attempt.aborted != 1 || attempt.finished != 0 {
			t.Fatalf("attempt %d aborts=%d finishes=%d", ordinal+1, attempt.aborted, attempt.finished)
		}
	}
}

func TestSQLiteUniqueAndBusyErrorsAreRetryableInterference(t *testing.T) {
	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), filepath.Join(t.TempDir(), "classifier.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE "unique_items" ("id" INTEGER PRIMARY KEY) STRICT`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO "unique_items" ("id") VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	_, uniqueErr := database.Exec(`INSERT INTO "unique_items" ("id") VALUES (1)`)
	if uniqueErr == nil || !retryableInterference(uniqueErr) {
		t.Fatalf("SQLite unique error was not classified as interference: %v", uniqueErr)
	}
}

func TestRunRejectsProbeCardinalityBeforeBranch(t *testing.T) {
	program := preparedSQLite(t, mutationir.EngineOwnedUpsertRetry)
	attempt := &fakeAttempt{probeRows: 2}
	executed := false
	_, err := Run(context.Background(), program, &fakeBackend{attempts: []*fakeAttempt{attempt}}, func(context.Context) (FrozenValues, error) {
		return NewFrozenValues(nil), nil
	}, branchExecutorFunc(func(context.Context, Attempt, mutationir.Node, FrozenValues) (any, error) {
		executed = true
		return nil, nil
	}), Options{MaxAttempts: 1})
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeInvariant || executed || attempt.aborted != 1 {
		t.Fatalf("error=%v executed=%t abort=%d", err, executed, attempt.aborted)
	}
}

func TestRunAbortsBeforeRepanickingFromBranchExecutor(t *testing.T) {
	program := preparedSQLite(t, mutationir.EngineOwnedUpsertRetry)
	attempt := &fakeAttempt{}
	defer func() {
		if recovered := recover(); recovered != "boom" || attempt.aborted != 1 {
			t.Fatalf("recovered=%v abort=%d", recovered, attempt.aborted)
		}
	}()
	_, _ = Run(context.Background(), program, &fakeBackend{attempts: []*fakeAttempt{attempt}}, func(context.Context) (FrozenValues, error) {
		return NewFrozenValues(nil), nil
	}, branchExecutorFunc(func(context.Context, Attempt, mutationir.Node, FrozenValues) (any, error) {
		panic("boom")
	}), Options{MaxAttempts: 1})
}

type fakeAttempt struct {
	probeRows uint32
	roles     []StatementRole
	finished  int
	aborted   int
}

func (attempt *fakeAttempt) Query(_ context.Context, statement Statement) (uint32, error) {
	attempt.roles = append(attempt.roles, statement.Role())
	if statement.Role() == ProbeUpdateReach {
		return attempt.probeRows, nil
	}
	return 1, nil
}
func (attempt *fakeAttempt) Finish(context.Context) error { attempt.finished++; return nil }
func (attempt *fakeAttempt) Abort() error                 { attempt.aborted++; return nil }

type fakeBackend struct {
	mu       sync.Mutex
	attempts []*fakeAttempt
	ordinals []uint32
}

func (backend *fakeBackend) Begin(_ context.Context, _ mutationsql.TransactionRequirement, ordinal uint32) (Attempt, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.ordinals = append(backend.ordinals, ordinal)
	if len(backend.attempts) == 0 {
		return nil, sql.ErrTxDone
	}
	attempt := backend.attempts[0]
	backend.attempts = backend.attempts[1:]
	return attempt, nil
}

type branchExecutorFunc func(context.Context, Attempt, mutationir.Node, FrozenValues) (any, error)

func (function branchExecutorFunc) ExecuteBranch(ctx context.Context, attempt Attempt, node mutationir.Node, frozen FrozenValues) (any, error) {
	return function(ctx, attempt, node, frozen)
}

type testPolicySet struct {
	generation golem.SchemaDigest
	policy     policyir.Policy
}

func (set testPolicySet) GenerationDigest() golem.SchemaDigest { return set.generation }
func (set testPolicySet) Provider() policyir.Provider          { return policyir.ProviderSQLite }
func (set testPolicySet) Policy(model policyir.ModelID) (policyir.Policy, bool) {
	return set.policy, set.policy.ModelID() == model
}

func systemPlan(t testing.TB, fixture schematest.Fixture, retry mutationir.RetryClass) mutationir.Plan {
	return buildPlan(t, fixture, mutationir.System, nil, retry)
}

func systemGuardPlan(t testing.TB, fixture schematest.Fixture, value string, retry mutationir.RetryClass) mutationir.Plan {
	t.Helper()
	create, update, _ := boundInputs(t, fixture)
	target := boundTarget(t, fixture, value)
	bounds, err := mutationir.NewStatementBounds(999, 1000)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := mutationplan.BuildRoot(mutationplan.RootRequest{
		Stance: mutationir.System, Operation: mutationir.Upsert, Model: policyir.ModelID(fixture.Post),
		Registry: fixture.Registry, Target: &target, Create: &create, Update: &update,
		Retry: retry, Bounds: bounds,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func callerPlan(t testing.TB, fixture schematest.Fixture, policy policyir.Policy, retry mutationir.RetryClass) mutationir.Plan {
	set := testPolicySet{generation: fixture.Registry.GenerationDigest(), policy: policy}
	return buildPlan(t, fixture, mutationir.Caller, set, retry)
}

func buildPlan(t testing.TB, fixture schematest.Fixture, stance mutationir.Stance, policies mutationplan.PolicySet, retry mutationir.RetryClass) mutationir.Plan {
	t.Helper()
	create, update, target := boundInputs(t, fixture)
	bounds, err := mutationir.NewStatementBounds(999, 1000)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := mutationplan.BuildRoot(mutationplan.RootRequest{
		Stance: stance, Operation: mutationir.Upsert, Model: policyir.ModelID(fixture.Post),
		Registry: fixture.Registry, Policies: policies, Target: &target, Create: &create, Update: &update,
		Retry: retry, Bounds: bounds,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func boundInputs(t testing.TB, fixture schematest.Fixture) (mutationbind.ScalarInput, mutationbind.ScalarInput, mutationbind.BoundTarget) {
	t.Helper()
	id := golem.GeneratedEqualField[testPost, golem.UUID](fixture.PostID)
	author := golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID)
	title := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	uuid := golem.NewUUID([16]byte{4})
	createFrozen, err := golem.RuntimeFreezeCreateInput(golem.GeneratedCreateInput(fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, id, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, author, uuid),
		golem.GeneratedCreateFieldValue(fixture.Post, title, "created"),
	))
	if err != nil {
		t.Fatal(err)
	}
	create, err := mutationbind.CreateInput(createFrozen, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	updateFrozen, err := golem.RuntimeFreezeUpdateInput(golem.GeneratedUpdateInput(fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "updated")))
	if err != nil {
		t.Fatal(err)
	}
	update, err := mutationbind.UpdateInput(updateFrozen, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	selector := golem.GeneratedUniqueSelectorValue[testPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, uuid))
	targetFrozen, err := golem.RuntimeFreezeMutationTarget[testPost](selector)
	if err != nil {
		t.Fatal(err)
	}
	target, err := mutationbind.Target(targetFrozen, fixture.Post, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	return create, update, target
}

func boundTarget(t testing.TB, fixture schematest.Fixture, guardValue string) mutationbind.BoundTarget {
	t.Helper()
	uuid := golem.NewUUID([16]byte{4})
	selector := golem.GeneratedUniqueSelectorValue[testPost](fixture.Post, fixture.PostKey, golem.GeneratedSelectorComponent(fixture.PostID, uuid))
	title := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	targetFrozen, err := golem.RuntimeFreezeMutationTarget[testPost](selector.And(title.Eq(guardValue)))
	if err != nil {
		t.Fatal(err)
	}
	target, err := mutationbind.Target(targetFrozen, fixture.Post, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func proof(t testing.TB, fixture schematest.Fixture, provider policyir.Provider) policysql.CapabilityProof {
	t.Helper()
	result, err := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()),
		policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON,
		policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation,
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func stringEqual(t testing.TB, fixture schematest.Fixture, value string) policyir.Condition {
	t.Helper()
	typ, err := policyir.NewTypeRef(policyir.ValueString, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	operandValue, err := policyir.StringValue(value)
	if err != nil {
		t.Fatal(err)
	}
	operand, err := policyir.OneOperand(operandValue)
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := operator.ValidateShape(policyir.OperatorEqual, operator.Shape{Node: policyir.ConditionScalar, FieldType: typ, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: policyir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewScalar(policyir.ModelID(fixture.Post), policyir.FieldID(fixture.PostTitle), typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func modelRule(t testing.TB, fixture schematest.Fixture, action policyir.Action, condition *policyir.Condition, position uint32) policyir.Rule {
	t.Helper()
	rule, err := policyir.NewModelRule(action, policyir.EffectGrant, policyir.ModelID(fixture.Post), condition, position)
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func preparedSQLite(t testing.TB, retry mutationir.RetryClass) Program {
	t.Helper()
	fixture := schematest.New(t)
	program, err := Prepare(systemPlan(t, fixture, retry), fixture.Registry, policyir.ProviderSQLite, proof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func preparedPostgreSQL(t testing.TB, retry mutationir.RetryClass) Program {
	t.Helper()
	fixture := schematest.New(t)
	program, err := Prepare(systemPlan(t, fixture, retry), fixture.Registry, policyir.ProviderPostgreSQL, proof(t, fixture, policyir.ProviderPostgreSQL))
	if err != nil {
		t.Fatal(err)
	}
	return program
}
