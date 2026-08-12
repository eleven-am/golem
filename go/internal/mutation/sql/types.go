// Package sql renders validated P4 scalar mutation plans into deterministic,
// provider-specific statement programs. Programs are descriptions only: the
// caller must execute every statement, in order, on one transaction satisfying
// TransactionRequirement.
package sql

import (
	"fmt"
	"time"

	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type ErrorCode string

const (
	CodeInput       ErrorCode = "P4_SQL_INPUT"
	CodeSchema      ErrorCode = "P4_SQL_SCHEMA"
	CodeProvider    ErrorCode = "P4_SQL_PROVIDER"
	CodeUnsupported ErrorCode = "P4_SQL_UNSUPPORTED"
	CodeRender      ErrorCode = "P4_SQL_RENDER"
	CodeLimit       ErrorCode = "P4_SQL_LIMIT"
)

type Error struct {
	Code   ErrorCode
	Model  policyir.ModelID
	Field  policyir.FieldID
	Detail string
	Cause  error
}

func (failure *Error) Error() string {
	return fmt.Sprintf("%s: model=%x field=%x: %s", failure.Code, failure.Model, failure.Field, failure.Detail)
}

func (failure *Error) Unwrap() error { return failure.Cause }

type TransactionRequirement uint8

const (
	PostgreSQLTransaction TransactionRequirement = iota + 1
	// SQLiteImmediateTransaction means the connection must have acquired write
	// intent (BEGIN IMMEDIATE or an equivalent provider-owned primitive) before
	// the first statement. A deferred read transaction is not sufficient.
	SQLiteImmediateTransaction
)

type Role uint8

const (
	SelectPreImage Role = iota + 1
	ApplyCreate
	ApplyUpdate
	ApplyDelete
	VerifyPostcondition
)

type Cardinality uint8

const (
	ExactlyOneRow Cardinality = iota + 1
)

type BindingKind uint8

const (
	StaticBinding BindingKind = iota + 1
	PriorResultBinding
)

// Binding is one positional SQL argument. Prior-result bindings make dataflow
// explicit: execution reads FieldID from the exactly-one row returned by the
// statement at StatementIndex. It must not accept a caller-supplied substitute.
type Binding struct {
	kind           BindingKind
	value          any
	statementIndex uint32
	field          policyir.FieldID
}

func (binding Binding) Kind() BindingKind { return binding.kind }
func (binding Binding) StaticValue() (any, bool) {
	if binding.kind != StaticBinding {
		return nil, false
	}
	return cloneArgument(binding.value), true
}
func (binding Binding) PriorResult() (uint32, policyir.FieldID, bool) {
	return binding.statementIndex, binding.field, binding.kind == PriorResultBinding
}

type ResultColumn struct {
	field policyir.FieldID
	alias string
}

func (column ResultColumn) FieldID() policyir.FieldID { return column.field }
func (column ResultColumn) Alias() string             { return column.alias }

// AuthorizationColumn is one precomputed field grant evaluated by SQL against
// the locked pre-image. It is deliberately separate from ResultColumn: grant
// booleans are execution evidence, never model data and never enter the exact
// persisted-row decoder.
type AuthorizationColumn struct {
	field policyir.FieldID
	alias string
}

func (column AuthorizationColumn) FieldID() policyir.FieldID { return column.field }
func (column AuthorizationColumn) Alias() string             { return column.alias }

type Statement struct {
	role           Role
	text           string
	bindings       []Binding
	columns        []ResultColumn
	authorizations []AuthorizationColumn
	cardinality    Cardinality
}

// Every statement is a row-producing query, including INSERT/UPDATE/DELETE
// through RETURNING. The executor must retain each returned driver's raw
// physical value by FieldID for PriorResultBinding before separately decoding
// any requested logical result. Zero or multiple rows violates ExactlyOneRow
// and requires rollback; it must never be converted to success or truncation.

func (statement Statement) Role() Role               { return statement.role }
func (statement Statement) SQL() string              { return statement.text }
func (statement Statement) Cardinality() Cardinality { return statement.cardinality }
func (statement Statement) Bindings() []Binding      { return cloneBindings(statement.bindings) }
func (statement Statement) Columns() []ResultColumn {
	return append([]ResultColumn(nil), statement.columns...)
}
func (statement Statement) AuthorizationColumns() []AuthorizationColumn {
	return append([]AuthorizationColumn(nil), statement.authorizations...)
}

type IdentityVerification struct {
	behavior        mutationir.IdentityBehavior
	beforeStatement uint32
	hasBefore       bool
	afterStatement  uint32
	fields          []policyir.FieldID
}

// IdentityVerification is mandatory execution work, not metadata for logging.
// Produced identities require a complete after tuple. Unchanged identities
// require exact physical-value equality between the before and after tuples.
// MayChange requires both complete tuples and leaves cascade-effect proof to
// the higher mutation kernel. Any mismatch or missing component rolls back.

func (verification IdentityVerification) Behavior() mutationir.IdentityBehavior {
	return verification.behavior
}
func (verification IdentityVerification) BeforeStatement() (uint32, bool) {
	return verification.beforeStatement, verification.hasBefore
}
func (verification IdentityVerification) AfterStatement() uint32 { return verification.afterStatement }
func (verification IdentityVerification) Fields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), verification.fields...)
}

type Program struct {
	provider                    policyir.Provider
	operation                   mutationir.Operation
	model                       policyir.ModelID
	stance                      mutationir.Stance
	transaction                 TransactionRequirement
	statements                  []Statement
	identity                    IdentityVerification
	authored                    []policyir.FieldID
	fact                        mutationir.FactRequirement
	concurrency                 *policyir.FieldID
	requiresConcurrencyPrecheck bool
}

func (program Program) Provider() policyir.Provider                    { return program.provider }
func (program Program) Operation() mutationir.Operation                { return program.operation }
func (program Program) ModelID() policyir.ModelID                      { return program.model }
func (program Program) Stance() mutationir.Stance                      { return program.stance }
func (program Program) TransactionRequirement() TransactionRequirement { return program.transaction }
func (program Program) AuthoredFields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), program.authored...)
}
func (program Program) FactRequirement() mutationir.FactRequirement { return program.fact }
func (program Program) OptimisticConcurrency() (policyir.FieldID, bool) {
	if program.concurrency == nil {
		return policyir.FieldID{}, false
	}
	return *program.concurrency, true
}
func (program Program) RequiresConcurrencyPrecheck() bool { return program.requiresConcurrencyPrecheck }
func (program Program) IdentityVerification() IdentityVerification {
	copy := program.identity
	copy.fields = program.identity.Fields()
	return copy
}
func (program Program) Statements() []Statement {
	result := make([]Statement, len(program.statements))
	for index, statement := range program.statements {
		result[index] = statement
		result[index].bindings = cloneBindings(statement.bindings)
		result[index].columns = statement.Columns()
		result[index].authorizations = statement.AuthorizationColumns()
	}
	return result
}

func cloneBindings(values []Binding) []Binding {
	result := append([]Binding(nil), values...)
	for index := range result {
		result[index].value = cloneArgument(result[index].value)
	}
	return result
}

func cloneArgument(value any) any {
	switch typed := value.(type) {
	case []byte:
		return append([]byte(nil), typed...)
	case []string:
		return append([]string(nil), typed...)
	case time.Time:
		return typed
	default:
		return value
	}
}

func fail(code ErrorCode, model policyir.ModelID, field policyir.FieldID, detail string, cause error) error {
	return &Error{Code: code, Model: model, Field: field, Detail: detail, Cause: cause}
}
