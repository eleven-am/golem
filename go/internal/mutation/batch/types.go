// Package batch owns the execution-neutral, bounded exact-set contract for
// update-many and delete-many. It deliberately separates the sentinel capture
// query from every write: an executor cannot obtain write statements until it
// has proved that the complete authorized set fits the configured limit.
package batch

import (
	"fmt"
	"sort"
	"time"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type ErrorCode string

const (
	CodeInput     ErrorCode = "P4_BATCH_INPUT"
	CodeSchema    ErrorCode = "P4_BATCH_SCHEMA"
	CodeProvider  ErrorCode = "P4_BATCH_PROVIDER"
	CodeLimit     ErrorCode = "P4_BATCH_LIMIT_EXCEEDED"
	CodeSet       ErrorCode = "P4_BATCH_SET_MISMATCH"
	CodeIdentity  ErrorCode = "P4_BATCH_IDENTITY_CHANGE"
	CodeForbidden ErrorCode = "P4_BATCH_FORBIDDEN"
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
	// SQLiteImmediateTransaction requires write intent before Capture executes.
	// That is what stabilizes the captured set on SQLite; a deferred transaction
	// is not an implementation of this contract.
	SQLiteImmediateTransaction
)

type Role uint8

const (
	CaptureExactSet Role = iota + 1
	AuthorizePreImage
	ApplyUpdate
	ApplyDelete
	RehydrateAfterImage
)

type Cardinality uint8

const (
	AtMostSentinelRows Cardinality = iota + 1
	ExactlyCapturedRows
)

// Binding is one immutable positional argument. Batch programs intentionally
// contain static values only; captured identity values are encoded into the
// prepared program and never accepted from a second caller-controlled source.
type Binding struct{ value any }

func (binding Binding) Value() any { return cloneArgument(binding.value) }

type ResultColumn struct {
	field policyir.FieldID
	alias string
}

func (column ResultColumn) FieldID() policyir.FieldID { return column.field }
func (column ResultColumn) Alias() string             { return column.alias }

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
	expected       uint32
}

func (statement Statement) Role() Role               { return statement.role }
func (statement Statement) SQL() string              { return statement.text }
func (statement Statement) Cardinality() Cardinality { return statement.cardinality }
func (statement Statement) ExpectedRows() uint32     { return statement.expected }
func (statement Statement) Bindings() []Binding {
	result := make([]Binding, len(statement.bindings))
	for index := range statement.bindings {
		result[index] = Binding{value: cloneArgument(statement.bindings[index].value)}
	}
	return result
}
func (statement Statement) Columns() []ResultColumn {
	return append([]ResultColumn(nil), statement.columns...)
}
func (statement Statement) AuthorizationColumns() []AuthorizationColumn {
	return append([]AuthorizationColumn(nil), statement.authorizations...)
}

// Program contains only the capture phase. PrepareCaptured is the sole bridge
// to write SQL and enforces the +1 sentinel before producing any statement.
type Program struct {
	context     renderContext
	transaction TransactionRequirement
	capture     Statement
	maxRows     uint32
	primary     []policyir.FieldID
}

func (program Program) Provider() policyir.Provider                    { return program.context.provider }
func (program Program) Operation() mutationir.Operation                { return program.context.node.Operation() }
func (program Program) ModelID() policyir.ModelID                      { return program.context.node.ModelID() }
func (program Program) TransactionRequirement() TransactionRequirement { return program.transaction }
func (program Program) CaptureStatement() Statement {
	copy := program.capture
	copy.bindings = program.capture.Bindings()
	copy.columns = program.capture.Columns()
	return copy
}
func (program Program) MaxRows() uint32      { return program.maxRows }
func (program Program) SentinelRows() uint32 { return program.maxRows + 1 }
func (program Program) PrimaryKey() []policyir.FieldID {
	return append([]policyir.FieldID(nil), program.primary...)
}

type Prepared struct {
	operation  mutationir.Operation
	statements []Statement
	before     []mutationdecode.Row
	context    renderContext
}

func (prepared Prepared) Operation() mutationir.Operation { return prepared.operation }
func (prepared Prepared) Count() int64                    { return int64(len(prepared.before)) }
func (prepared Prepared) FactRequirement() mutationir.FactRequirement {
	return prepared.context.node.Fact()
}
func (prepared Prepared) Statements() []Statement {
	result := make([]Statement, len(prepared.statements))
	for index, statement := range prepared.statements {
		result[index] = statement
		result[index].bindings = statement.Bindings()
		result[index].columns = statement.Columns()
		result[index].authorizations = statement.AuthorizationColumns()
	}
	return result
}

// FieldGrant is one SQL-evaluated authorization decision from the locked
// pre-image. Callers cannot substitute a condition or a post-write value here;
// an executor must decode the AuthorizationColumns emitted by the prepared
// AuthorizePreImage statement.
type FieldGrant struct {
	field   policyir.FieldID
	granted bool
}

func NewFieldGrant(field policyir.FieldID, granted bool) (FieldGrant, error) {
	if field == (policyir.FieldID{}) {
		return FieldGrant{}, fail(CodeInput, policyir.ModelID{}, field, "authorization grant field is zero", nil)
	}
	return FieldGrant{field: field, granted: granted}, nil
}

func (grant FieldGrant) FieldID() policyir.FieldID { return grant.field }
func (grant FieldGrant) Granted() bool             { return grant.granted }

type AuthorizedRow struct {
	before mutationdecode.Row
	grants map[policyir.FieldID]bool
}

func NewAuthorizedRow(before mutationdecode.Row, grants ...FieldGrant) (AuthorizedRow, error) {
	result := AuthorizedRow{before: before, grants: make(map[policyir.FieldID]bool, len(grants))}
	for _, grant := range grants {
		if grant.field == (policyir.FieldID{}) {
			return AuthorizedRow{}, fail(CodeInput, before.ModelID(), grant.field, "authorization grant field is zero", nil)
		}
		if _, duplicate := result.grants[grant.field]; duplicate {
			return AuthorizedRow{}, fail(CodeInput, before.ModelID(), grant.field, "authorization grant field appears more than once", nil)
		}
		result.grants[grant.field] = grant.granted
	}
	return result, nil
}

func (row AuthorizedRow) Before() mutationdecode.Row { return row.before }
func (row AuthorizedRow) Grants() []FieldGrant {
	fields := make([]policyir.FieldID, 0, len(row.grants))
	for field := range row.grants {
		fields = append(fields, field)
	}
	sort.Slice(fields, func(i, j int) bool { return string(fields[i][:]) < string(fields[j][:]) })
	result := make([]FieldGrant, len(fields))
	for index, field := range fields {
		result[index] = FieldGrant{field: field, granted: row.grants[field]}
	}
	return result
}

type RowVerification struct {
	ordinal    uint32
	before     mutationdecode.Row
	after      *mutationdecode.Row
	authored   []policyir.FieldID
	conditions []mutationir.FieldAuthorization
}

func (row RowVerification) Ordinal() uint32            { return row.ordinal }
func (row RowVerification) Before() mutationdecode.Row { return row.before }
func (row RowVerification) After() (mutationdecode.Row, bool) {
	if row.after == nil {
		return mutationdecode.Row{}, false
	}
	return *row.after, true
}
func (row RowVerification) AuthoredChangedFields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), row.authored...)
}
func (row RowVerification) RequiredFieldAuthorizations() []mutationir.FieldAuthorization {
	return append([]mutationir.FieldAuthorization(nil), row.conditions...)
}

// FactSpec is the exact runtime seam for one ordered fact. Event and causation
// IDs remain runtime-owned; the batch kernel fixes action, images, and ordinal.
type FactSpec struct {
	action  mutationir.FactAction
	ordinal uint32
	before  mutationdecode.Row
	after   *mutationdecode.Row
}

func (fact FactSpec) Action() mutationir.FactAction { return fact.action }
func (fact FactSpec) Ordinal() uint32               { return fact.ordinal }
func (fact FactSpec) Before() mutationdecode.Row    { return fact.before }
func (fact FactSpec) After() (mutationdecode.Row, bool) {
	if fact.after == nil {
		return mutationdecode.Row{}, false
	}
	return *fact.after, true
}

type Verification struct {
	rows  []RowVerification
	facts []FactSpec
}

func (verification Verification) Count() int64 { return int64(len(verification.rows)) }
func (verification Verification) Rows() []RowVerification {
	return append([]RowVerification(nil), verification.rows...)
}
func (verification Verification) Facts() []FactSpec {
	return append([]FactSpec(nil), verification.facts...)
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
