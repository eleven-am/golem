// Package upsert prepares and runs the truthful root-upsert selection kernel.
// It deliberately does not render or execute either mutation branch: the
// selected, already-bound IR node is handed to the higher mutation runtime.
package upsert

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

const guardDomain = "golem:mutation-upsert-selector-guard:v1\x00"

type ErrorCode string

const (
	CodeInput     ErrorCode = "P4_UPSERT_INPUT"
	CodeSchema    ErrorCode = "P4_UPSERT_SCHEMA"
	CodeProvider  ErrorCode = "P4_UPSERT_PROVIDER"
	CodeRender    ErrorCode = "P4_UPSERT_RENDER"
	CodeFreeze    ErrorCode = "P4_UPSERT_FREEZE"
	CodeExecution ErrorCode = "P4_UPSERT_EXECUTION"
	CodeConflict  ErrorCode = "P4_UPSERT_CONFLICT"
	CodeInvariant ErrorCode = "P4_UPSERT_INVARIANT"
)

type Error struct {
	Code   ErrorCode
	Detail string
	Cause  error
}

func (failure *Error) Error() string { return string(failure.Code) + ": " + failure.Detail }
func (failure *Error) Unwrap() error { return failure.Cause }

type StatementRole uint8

const (
	AcquireSelectorGuard StatementRole = iota + 1
	ProbeUpdateReach
	ReleaseSelectorGuard
)

// Statement is an immutable, fully parameterized piece of the selection
// protocol. It contains no authored identifier or value text.
type Statement struct {
	role StatementRole
	sql  string
	args []any
}

func (statement Statement) Role() StatementRole { return statement.role }
func (statement Statement) SQL() string         { return statement.sql }
func (statement Statement) Args() []any         { return cloneArgs(statement.args) }

// Program is the deterministic attempt protocol prepared before any
// transaction starts. Branch nodes remain IR, so this package cannot silently
// omit later hook/fact/runtime work.
type Program struct {
	provider    policyir.Provider
	transaction mutationsql.TransactionRequirement
	retry       mutationir.RetryClass
	token       [32]byte
	guard       Statement
	probe       Statement
	cleanup     *Statement
	create      mutationir.Node
	update      mutationir.Node
}

// SelectorGuard is the provider-closed database coordination primitive shared
// by root upsert and nested upsert/connect-or-create. It deliberately contains
// no branch probe: nested relation expansion owns its correlation and caller
// reach predicate, but must acquire this guard before running that probe.
type SelectorGuard struct {
	token   [32]byte
	acquire Statement
	cleanup *Statement
}

func (guard SelectorGuard) Token() [32]byte             { return guard.token }
func (guard SelectorGuard) AcquireStatement() Statement { return cloneStatement(guard.acquire) }
func (guard SelectorGuard) CleanupStatement() (Statement, bool) {
	if guard.cleanup == nil {
		return Statement{}, false
	}
	return cloneStatement(*guard.cleanup), true
}

// PrepareSelectorGuard hashes only the normalized unique selector. Target
// guards and caller reach remain part of the later locked branch probe so all
// writers for one durable selector coordinate on the same token.
func PrepareSelectorGuard(target mutationir.Target, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof) (SelectorGuard, error) {
	if registry == nil || target.ModelID() == (policyir.ModelID{}) {
		return SelectorGuard{}, fail(CodeInput, "active registry and target are required", nil)
	}
	resolver := policysql.SchemaResolver(registry)
	if capabilities.Provider() != provider || capabilities.SchemaFingerprint() != resolver.SchemaFingerprint() {
		return SelectorGuard{}, fail(CodeProvider, "selector-guard capability proof does not match provider/schema", nil)
	}
	if _, _, err := providerDialect(provider); err != nil {
		return SelectorGuard{}, err
	}
	selector, err := selectorCondition(target, provider, resolver)
	if err != nil {
		return SelectorGuard{}, err
	}
	canonical, err := policyir.CanonicalCondition(selector)
	if err != nil {
		return SelectorGuard{}, fail(CodeInput, "selector canonicalization failed", err)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(guardDomain))
	_, _ = hasher.Write(canonical)
	var token [32]byte
	copy(token[:], hasher.Sum(nil))
	acquire, cleanup := guardStatements(provider, token)
	return SelectorGuard{token: token, acquire: acquire, cleanup: cleanup}, nil
}

func (program Program) Provider() policyir.Provider { return program.provider }
func (program Program) TransactionRequirement() mutationsql.TransactionRequirement {
	return program.transaction
}
func (program Program) RetryClass() mutationir.RetryClass { return program.retry }
func (program Program) GuardToken() [32]byte              { return program.token }
func (program Program) GuardStatement() Statement         { return cloneStatement(program.guard) }
func (program Program) ProbeStatement() Statement         { return cloneStatement(program.probe) }
func (program Program) CleanupStatement() (Statement, bool) {
	if program.cleanup == nil {
		return Statement{}, false
	}
	return cloneStatement(*program.cleanup), true
}
func (program Program) CreateBranch() mutationir.Node { return program.create }
func (program Program) UpdateBranch() mutationir.Node { return program.update }

// Prepare validates the closed upsert graph, proves provider/schema agreement,
// hashes the canonical unique selector, and renders the guard and update-reach
// probe. No transaction, clock, random source, default, or hook is consulted.
func Prepare(plan mutationir.Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof) (Program, error) {
	if registry == nil {
		return Program{}, fail(CodeInput, "active schema registry is required", nil)
	}
	if err := plan.Validate(); err != nil {
		return Program{}, fail(CodeInput, "mutation plan is invalid", err)
	}
	if plan.RetryClass() != mutationir.EngineOwnedUpsertRetry && plan.RetryClass() != mutationir.CallerTransactionNoReplay {
		return Program{}, fail(CodeInput, "upsert retry ownership is invalid", nil)
	}
	root, ok := plan.Graph().Root()
	if !ok || root.Operation() != mutationir.Upsert {
		return Program{}, fail(CodeInput, "plan root is not upsert", nil)
	}
	target, ok := root.Target()
	if !ok {
		return Program{}, fail(CodeInput, "upsert root target is absent", nil)
	}
	create, update, err := exactBranches(plan.Graph().Nodes(), root)
	if err != nil {
		return Program{}, err
	}
	if !hasProviderCapability(plan, provider, mutationir.CapabilityTransaction) || !hasProviderCapability(plan, provider, mutationir.CapabilityTargetLock) || !hasProviderCapability(plan, provider, mutationir.CapabilitySelectorGuard) {
		return Program{}, fail(CodeProvider, "plan does not require the complete upsert capability set for this provider", nil)
	}

	resolver := policysql.SchemaResolver(registry)
	if capabilities.Provider() != provider || capabilities.SchemaFingerprint() != resolver.SchemaFingerprint() {
		return Program{}, fail(CodeProvider, "capability proof does not match provider and schema", nil)
	}
	dialect, transaction, err := providerDialect(provider)
	if err != nil {
		return Program{}, err
	}
	model, ok := resolver.Model(provider, root.ModelID())
	if !ok {
		return Program{}, fail(CodeSchema, "upsert model has no physical descriptor", nil)
	}
	selector, err := selectorCondition(target, provider, resolver)
	if err != nil {
		return Program{}, err
	}
	canonical, err := policyir.CanonicalCondition(selector)
	if err != nil {
		return Program{}, fail(CodeInput, "selector canonicalization failed", err)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(guardDomain))
	_, _ = hasher.Write(canonical)
	var token [32]byte
	copy(token[:], hasher.Sum(nil))

	probeCondition := selector
	if selection, present := root.SelectionRequirement(); present {
		if selection.Action() != policyir.ActionUpdate {
			return Program{}, fail(CodeInput, "upsert reach probe is not an UPDATE selection", nil)
		}
		probeCondition = selection.Constraint()
	} else {
		if plan.Stance() != mutationir.System {
			return Program{}, fail(CodeInput, "caller upsert has no update-reach selection", nil)
		}
		if guard, present := target.Guard(); present {
			probeCondition, err = conjunction(target.ModelID(), selector, guard)
			if err != nil {
				return Program{}, err
			}
		}
	}
	const alias physical.PhysicalName = "golem_upsert_probe"
	fragment, err := policysql.Compile(policysql.Request{
		Condition: probeCondition, Provider: provider, Resolver: resolver,
		Dialect: dialect, Capabilities: capabilities,
		BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: alias,
	})
	if err != nil {
		return Program{}, fail(CodeRender, "update-reach probe could not be compiled", err)
	}
	if uint32(len(fragment.Args())) > plan.Bounds().MaxParameters() {
		return Program{}, fail(CodeRender, "update-reach probe exceeds the plan parameter bound", nil)
	}
	lock := ""
	if provider == policyir.ProviderPostgreSQL {
		lock = " FOR UPDATE"
	}
	probe := Statement{role: ProbeUpdateReach, sql: "SELECT 1 FROM " + dialect.Table(model) + " AS " + dialect.Quote(alias) + " WHERE " + fragment.SQL() + lock, args: fragment.Args()}
	guard, cleanup := guardStatements(provider, token)
	return Program{provider: provider, transaction: transaction, retry: plan.RetryClass(), token: token, guard: guard, probe: probe, cleanup: cleanup, create: create, update: update}, nil
}

func exactBranches(nodes []mutationir.Node, root mutationir.Node) (mutationir.Node, mutationir.Node, error) {
	children := root.ChildOrdinals()
	if len(nodes) != 3 || len(children) != 2 {
		return mutationir.Node{}, mutationir.Node{}, fail(CodeInput, "root upsert must contain exactly two branch nodes", nil)
	}
	var create, update mutationir.Node
	for _, ordinal := range children {
		if int(ordinal) >= len(nodes) {
			return mutationir.Node{}, mutationir.Node{}, fail(CodeInput, "upsert child ordinal is outside the graph", nil)
		}
		node := nodes[ordinal]
		parent, present := node.ParentOrdinal()
		if !present || parent != root.Ordinal() || node.ModelID() != root.ModelID() {
			return mutationir.Node{}, mutationir.Node{}, fail(CodeInput, "upsert branch ownership is invalid", nil)
		}
		switch {
		case node.Operation() == mutationir.Create && node.Branch() == mutationir.UpsertCreateBranch:
			create = node
		case node.Operation() == mutationir.Update && node.Branch() == mutationir.UpsertUpdateBranch:
			update = node
		default:
			return mutationir.Node{}, mutationir.Node{}, fail(CodeInput, "upsert has an unexpected branch", nil)
		}
	}
	if create.Operation() == 0 || update.Operation() == 0 {
		return mutationir.Node{}, mutationir.Node{}, fail(CodeInput, "upsert create/update branch pair is incomplete", nil)
	}
	return create, update, nil
}

func selectorCondition(target mutationir.Target, provider policyir.Provider, resolver policysql.Resolver) (policyir.Condition, error) {
	values := target.Values()
	conditions := make([]policyir.Condition, len(values))
	for index, selected := range values {
		field, ok := resolver.Field(provider, target.ModelID(), selected.FieldID())
		if !ok {
			return policyir.Condition{}, fail(CodeSchema, "selector field has no physical descriptor", nil)
		}
		operand, err := policyir.OneOperand(selected.Value())
		if err != nil {
			return policyir.Condition{}, fail(CodeInput, "selector value is invalid", err)
		}
		requirements, err := operator.ValidateShape(policyir.OperatorEqual, operator.Shape{Node: policyir.ConditionScalar, FieldType: field.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: resolver.Providers()})
		if err != nil {
			return policyir.Condition{}, fail(CodeInput, "selector equality is unsupported", err)
		}
		condition, err := policyir.NewScalar(target.ModelID(), selected.FieldID(), field.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if err != nil {
			return policyir.Condition{}, fail(CodeInput, "selector condition is invalid", err)
		}
		conditions[index] = condition
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	condition, err := policyir.NewLogical(target.ModelID(), policyir.LogicalAnd, conditions)
	if err != nil {
		return policyir.Condition{}, fail(CodeInput, "compound selector condition is invalid", err)
	}
	return condition, nil
}

func conjunction(model policyir.ModelID, conditions ...policyir.Condition) (policyir.Condition, error) {
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	condition, err := policyir.NewLogical(model, policyir.LogicalAnd, conditions)
	if err != nil {
		return policyir.Condition{}, fail(CodeInput, "selector guard condition is invalid", err)
	}
	return condition, nil
}

func guardStatements(provider policyir.Provider, token [32]byte) (Statement, *Statement) {
	switch provider {
	case policyir.ProviderPostgreSQL:
		lockKey := int64(binary.BigEndian.Uint64(token[:8]))
		return Statement{role: AcquireSelectorGuard, sql: "SELECT pg_catalog.pg_advisory_xact_lock($1)", args: []any{lockKey}}, nil
	case policyir.ProviderSQLite:
		value := append([]byte(nil), token[:]...)
		guard := Statement{role: AcquireSelectorGuard, sql: `INSERT INTO "_golem_upsert_guard" ("guard_token") VALUES (?) ON CONFLICT ("guard_token") DO UPDATE SET "guard_token" = excluded."guard_token" RETURNING "guard_token"`, args: []any{value}}
		cleanup := Statement{role: ReleaseSelectorGuard, sql: `DELETE FROM "_golem_upsert_guard" WHERE "guard_token" = ? RETURNING "guard_token"`, args: []any{value}}
		return guard, &cleanup
	default:
		panic("provider validated before guard rendering")
	}
}

func providerDialect(provider policyir.Provider) (policysql.Dialect, mutationsql.TransactionRequirement, error) {
	switch provider {
	case policyir.ProviderPostgreSQL:
		return postgresprovider.NewPolicyDialect(), mutationsql.PostgreSQLTransaction, nil
	case policyir.ProviderSQLite:
		return sqliteprovider.NewPolicyDialect(), mutationsql.SQLiteImmediateTransaction, nil
	default:
		return nil, 0, fail(CodeProvider, fmt.Sprintf("unsupported provider %d", provider), nil)
	}
}

func hasProviderCapability(plan mutationir.Plan, provider policyir.Provider, capability mutationir.ProviderCapability) bool {
	for _, requirement := range plan.ProviderRequirements() {
		if requirement.Capability() == capability && requirement.Providers().Contains(provider) {
			return true
		}
	}
	return false
}

func cloneStatement(statement Statement) Statement {
	statement.args = cloneArgs(statement.args)
	return statement
}

func cloneArgs(values []any) []any {
	result := append([]any(nil), values...)
	for index, value := range result {
		if bytes, ok := value.([]byte); ok {
			result[index] = append([]byte(nil), bytes...)
		}
	}
	return result
}

func fail(code ErrorCode, detail string, cause error) error {
	return &Error{Code: code, Detail: detail, Cause: cause}
}
