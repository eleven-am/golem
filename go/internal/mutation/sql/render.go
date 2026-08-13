package sql

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// Render accepts only a validated, root-only scalar Create, Update, or Delete
// plan. It returns SQL plus closed positional dataflow; it never executes SQL.
func Render(plan mutationir.Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof) (Program, error) {
	return render(plan, registry, provider, capabilities, false)
}

// RenderVersioned is the sole SQL construction boundary for an update/delete
// whose exact expectation will be validated by the runtime CAS kernel. It
// carries no claim value; the returned program is marked as requiring the
// kernel's unforgeable precheck capability before execution.
func RenderVersioned(plan mutationir.Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof) (Program, error) {
	return render(plan, registry, provider, capabilities, true)
}

func render(plan mutationir.Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof, versioned bool) (Program, error) {
	if registry == nil {
		return Program{}, fail(CodeInput, policyir.ModelID{}, policyir.FieldID{}, "active schema registry is required", nil)
	}
	if err := plan.Validate(); err != nil {
		return Program{}, fail(CodeInput, policyir.ModelID{}, policyir.FieldID{}, "mutation plan is invalid", err)
	}
	nodes := plan.Graph().Nodes()
	if len(nodes) != 1 {
		return Program{}, fail(CodeUnsupported, nodes[0].ModelID(), policyir.FieldID{}, "P4-D renders one root scalar node; nested nodes are handled by P4-E", nil)
	}
	node := nodes[0]
	if node.Operation() != mutationir.Create && node.Operation() != mutationir.Update && node.Operation() != mutationir.Delete {
		return Program{}, fail(CodeUnsupported, node.ModelID(), policyir.FieldID{}, "operation is outside root scalar create/update/delete", nil)
	}
	if !validRootIdentityBehavior(node.Operation(), node.IdentityBehavior()) {
		return Program{}, fail(CodeInput, node.ModelID(), policyir.FieldID{}, "identity behavior does not match the root scalar operation", nil)
	}
	// Relation-traversing image dependencies are discharged by the exact policy
	// conditions below, not decoded into a root-model Go row. actionWhere,
	// authorizationSelect, and postconditionStatement compile those conditions
	// as provider-side correlated EXISTS/NOT EXISTS expressions against the
	// locked or persisted row. Keeping the dependencies in the plan preserves
	// the audit contract while avoiding an in-memory authorization fallback.
	dialect, transaction, err := providerDialect(provider)
	if err != nil {
		return Program{}, fail(CodeProvider, node.ModelID(), policyir.FieldID{}, "provider is unsupported", err)
	}
	resolver := policysql.SchemaResolver(registry)
	if capabilities.Provider() != provider || capabilities.SchemaFingerprint() != resolver.SchemaFingerprint() {
		return Program{}, fail(CodeProvider, node.ModelID(), policyir.FieldID{}, "policy capability proof does not match the provider and schema", nil)
	}
	model, ok := resolver.Model(provider, node.ModelID())
	if !ok {
		return Program{}, fail(CodeSchema, node.ModelID(), policyir.FieldID{}, "root physical model is absent", nil)
	}
	context := renderContext{plan: plan, node: node, registry: registry, provider: provider, capabilities: capabilities, dialect: dialect, resolver: resolver, model: model, alias: "golem_m0", precheckAllAuthorizations: versioned && plan.Stance() == mutationir.Caller && node.Operation() == mutationir.Update}
	logicalModel, ok := registry.Model(golem.ModelID(node.ModelID()))
	if !ok {
		return Program{}, fail(CodeSchema, node.ModelID(), policyir.FieldID{}, "root logical model is absent", nil)
	}
	if field, enabled := logicalModel.OptimisticConcurrency(); enabled {
		value := policyir.FieldID(field)
		context.concurrency = &value
		for _, operation := range node.ScalarOperations() {
			if operation.FieldID() == value && !operation.RuntimeOwned() {
				return Program{}, fail(CodeInput, node.ModelID(), value, "optimistic-concurrency field is application authored", nil)
			}
		}
		if node.Operation() == mutationir.Update || node.Operation() == mutationir.Delete {
			if !versioned {
				return Program{}, fail(CodeUnsupported, node.ModelID(), value, "optimistic-concurrency mutation requires an explicit expectation", nil)
			}
		}
	} else if versioned {
		return Program{}, fail(CodeInput, node.ModelID(), policyir.FieldID{}, "explicit concurrency rendering requires a concurrency-enabled model", nil)
	}
	if node.IdentityBehavior() == mutationir.IdentityMayChange && registry.IdentityChangeRequiresReferentialEnumeration(golem.ModelID(node.ModelID()), golemFields(scalarOperationFields(node.ScalarOperations()))) {
		return Program{}, fail(CodeUnsupported, node.ModelID(), policyir.FieldID{}, "identity-changing scalar update has relation effects that are not enumerated by this mutation graph", nil)
	}
	if len(context.primaryKey()) == 0 {
		return Program{}, fail(CodeSchema, node.ModelID(), policyir.FieldID{}, "single-row mutation requires a declared primary key for exact identity verification", nil)
	}
	var statements []Statement
	switch node.Operation() {
	case mutationir.Create:
		statements, err = context.renderCreate()
	case mutationir.Update:
		statements, err = context.renderUpdate()
	case mutationir.Delete:
		statements, err = context.renderDelete()
	}
	if err != nil {
		return Program{}, err
	}
	for _, statement := range statements {
		if uint32(len(statement.bindings)) > plan.Bounds().MaxParameters() {
			return Program{}, fail(CodeLimit, node.ModelID(), policyir.FieldID{}, "statement exceeds the plan parameter bound", nil)
		}
	}
	identity := IdentityVerification{behavior: node.IdentityBehavior(), afterStatement: mutationStatementIndex(node.Operation()), fields: context.identityVerificationFields()}
	if node.Operation() == mutationir.Update || node.Operation() == mutationir.Delete {
		identity.hasBefore = true
		identity.beforeStatement = 0
	}
	program := Program{provider: provider, operation: node.Operation(), model: node.ModelID(), stance: plan.Stance(), transaction: transaction, statements: statements, identity: identity, authored: authoredScalarOperationFields(node.ScalarOperations()), fact: node.Fact()}
	if context.concurrency != nil {
		field := *context.concurrency
		program.concurrency = &field
		program.requiresConcurrencyPrecheck = versioned && (node.Operation() == mutationir.Update || node.Operation() == mutationir.Delete)
	}
	return program, nil
}

type renderContext struct {
	plan                      mutationir.Plan
	node                      mutationir.Node
	registry                  *schema.Registry
	provider                  policyir.Provider
	capabilities              policysql.CapabilityProof
	dialect                   policysql.Dialect
	resolver                  policysql.Resolver
	model                     policysql.Model
	alias                     physical.PhysicalName
	concurrency               *policyir.FieldID
	precheckAllAuthorizations bool
}

func (context renderContext) renderCreate() ([]Statement, error) {
	operations := context.node.ScalarOperations()
	columns := make([]string, len(operations))
	values := make([]string, len(operations))
	bindings := make([]Binding, 0, len(operations))
	for index, operation := range operations {
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), operation.FieldID())
		if !ok {
			return nil, fail(CodeSchema, context.node.ModelID(), operation.FieldID(), "create field has no physical descriptor", nil)
		}
		if !sameType(operation.Type(), field.Type) {
			return nil, fail(CodeSchema, context.node.ModelID(), operation.FieldID(), "create operation type does not match the active field descriptor", nil)
		}
		columns[index] = context.dialect.Quote(field.Column)
		switch operation.Kind() {
		case mutationir.ScalarNull:
			values[index] = "NULL"
		case mutationir.ScalarSet:
			value, present := operation.Value()
			if !present {
				return nil, fail(CodeInput, context.node.ModelID(), operation.FieldID(), "create set operand is absent", nil)
			}
			encoded, err := context.encode(value, operation.Type())
			if err != nil {
				return nil, fail(CodeRender, context.node.ModelID(), operation.FieldID(), "create value did not encode exactly", err)
			}
			bindings = append(bindings, Binding{kind: StaticBinding, value: encoded})
			values[index] = context.dialect.Placeholder(len(bindings))
		default:
			return nil, fail(CodeInput, context.node.ModelID(), operation.FieldID(), "create carries an unsupported scalar operation", nil)
		}
	}
	returned, err := context.returnFields(context.node.AfterRequirements().Fields(), context.plan.ResultRequirements().Fields(), context.primaryKey(), scalarOperationFields(operations))
	if err != nil {
		return nil, err
	}
	returning, resultColumns, err := context.returning(returned)
	if err != nil {
		return nil, err
	}
	prefix := "INSERT INTO " + context.dialect.Table(context.model)
	if len(columns) == 0 {
		prefix += " DEFAULT VALUES"
	} else {
		prefix += " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(values, ", ") + ")"
	}
	statements := []Statement{{role: ApplyCreate, text: prefix + returning, bindings: bindings, columns: resultColumns, cardinality: ExactlyOneRow}}
	verify, present, err := context.postconditionStatement(uint32(len(statements)-1), context.createVerificationConditions())
	if err != nil {
		return nil, err
	}
	if present {
		statements = append(statements, verify)
	}
	return statements, nil
}

func (context renderContext) renderUpdate() ([]Statement, error) {
	target, _ := context.node.Target()
	operations := context.node.ScalarOperations()
	beforeFields, err := context.returnFields(context.node.BeforeRequirements().Fields(), context.identityVerificationFields(), scalarOperationFields(operations), context.concurrencyFields())
	if err != nil {
		return nil, err
	}
	where, whereBindings, err := context.actionWhere(target, true, 0)
	if err != nil {
		return nil, err
	}
	preselect, preColumns, err := context.selectFields(beforeFields)
	if err != nil {
		return nil, err
	}
	authorizationSelect, authorizationBindings, authorizationColumns, err := context.authorizationSelect(len(whereBindings))
	if err != nil {
		return nil, err
	}
	if authorizationSelect != "" {
		preselect += ", " + authorizationSelect
		whereBindings = append(whereBindings, authorizationBindings...)
	}
	lock := ""
	if context.provider == policyir.ProviderPostgreSQL {
		lock = " FOR UPDATE"
	}
	statements := []Statement{{role: SelectPreImage, text: "SELECT " + preselect + " FROM " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + where + lock, bindings: whereBindings, columns: preColumns, authorizations: authorizationColumns, cardinality: ExactlyOneRow}}

	assignments := make([]string, len(operations), len(operations)+1)
	updateBindings := make([]Binding, 0, len(operations))
	for index, operation := range operations {
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), operation.FieldID())
		if !ok {
			return nil, fail(CodeSchema, context.node.ModelID(), operation.FieldID(), "update field has no physical descriptor", nil)
		}
		if !sameType(operation.Type(), field.Type) {
			return nil, fail(CodeSchema, context.node.ModelID(), operation.FieldID(), "update operation type does not match the active field descriptor", nil)
		}
		column := context.dialect.Quote(field.Column)
		expression, err := context.scalarExpression(operation, column, &updateBindings)
		if err != nil {
			return nil, err
		}
		assignments[index] = column + " = " + expression
	}
	if context.concurrency != nil {
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), *context.concurrency)
		if !ok || field.Type.Kind() != policyir.ValueInt64 || field.Type.Nullable() {
			return nil, fail(CodeSchema, context.node.ModelID(), *context.concurrency, "optimistic-concurrency field has no exact non-null int64 descriptor", nil)
		}
		column := context.dialect.Quote(field.Column)
		assignments = append(assignments, column+" = ("+column+" + 1)")
	}
	updateWhere, updateWhereBindings, err := context.actionWhere(target, true, len(updateBindings))
	if err != nil {
		return nil, err
	}
	updateBindings = append(updateBindings, updateWhereBindings...)
	if context.concurrency != nil {
		field, _ := context.resolver.Field(context.provider, context.node.ModelID(), *context.concurrency)
		updateBindings = append(updateBindings, Binding{kind: PriorResultBinding, statementIndex: 0, field: *context.concurrency})
		updateWhere += " AND " + context.qualified(field.Column) + " = " + context.dialect.Placeholder(len(updateBindings))
		updateWhere += " AND " + context.qualified(field.Column) + " < 9223372036854775807"
	}
	returned, err := context.returnFields(context.node.AfterRequirements().Fields(), context.plan.ResultRequirements().Fields(), context.identityVerificationFields(), scalarOperationFields(operations), context.concurrencyFields())
	if err != nil {
		return nil, err
	}
	if len(assignments) == 0 {
		// Relation-only single updates still need the root action constraint,
		// locked image, postcondition, projection, and fact contract, but must not
		// manufacture a scalar write (or fire database update triggers). Treat the
		// already locked root as a verified no-op apply image. Public binding is
		// responsible for rejecting a truly empty update input.
		selectList, resultColumns, err := context.selectFields(returned)
		if err != nil {
			return nil, err
		}
		statements = append(statements, Statement{role: ApplyUpdate, text: "SELECT " + selectList + " FROM " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + updateWhere, bindings: updateBindings, columns: resultColumns, cardinality: ExactlyOneRow})
		verify, present, err := context.postconditionStatement(1, context.updateVerificationConditions())
		if err != nil {
			return nil, err
		}
		if present {
			statements = append(statements, verify)
		}
		return statements, nil
	}
	returning, resultColumns, err := context.returning(returned)
	if err != nil {
		return nil, err
	}
	statements = append(statements, Statement{role: ApplyUpdate, text: "UPDATE " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " SET " + strings.Join(assignments, ", ") + " WHERE " + updateWhere + returning, bindings: updateBindings, columns: resultColumns, cardinality: ExactlyOneRow})
	verify, present, err := context.postconditionStatement(1, context.updateVerificationConditions())
	if err != nil {
		return nil, err
	}
	if present {
		statements = append(statements, verify)
	}
	return statements, nil
}

func (context renderContext) renderDelete() ([]Statement, error) {
	target, _ := context.node.Target()
	beforeFields, err := context.returnFields(context.node.BeforeRequirements().Fields(), context.plan.ResultRequirements().Fields(), context.primaryKey(), context.concurrencyFields())
	if err != nil {
		return nil, err
	}
	where, bindings, err := context.actionWhere(target, true, 0)
	if err != nil {
		return nil, err
	}
	preselect, preColumns, err := context.selectFields(beforeFields)
	if err != nil {
		return nil, err
	}
	lock := ""
	if context.provider == policyir.ProviderPostgreSQL {
		lock = " FOR UPDATE"
	}
	statements := []Statement{{role: SelectPreImage, text: "SELECT " + preselect + " FROM " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + where + lock, bindings: bindings, columns: preColumns, cardinality: ExactlyOneRow}}
	pkWhere, pkBindings, err := context.priorPrimaryKeyWhere(0)
	if err != nil {
		return nil, err
	}
	if context.concurrency != nil {
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), *context.concurrency)
		if !ok || field.Type.Kind() != policyir.ValueInt64 || field.Type.Nullable() {
			return nil, fail(CodeSchema, context.node.ModelID(), *context.concurrency, "optimistic-concurrency field has no exact non-null int64 descriptor", nil)
		}
		// The locked primary identity prevents hook retargeting, but it is not a
		// substitute for the authorized target contract. Re-evaluate the exact
		// selector, caller selection policy, and target guard in the atomic DELETE
		// so a policy dependency revoked while Before runs cannot be bypassed.
		authorizedWhere, authorizedBindings, renderErr := context.actionWhere(target, true, len(pkBindings))
		if renderErr != nil {
			return nil, renderErr
		}
		pkWhere += " AND (" + authorizedWhere + ")"
		pkBindings = append(pkBindings, authorizedBindings...)
		pkBindings = append(pkBindings, Binding{kind: PriorResultBinding, statementIndex: 0, field: *context.concurrency})
		pkWhere += " AND " + context.qualified(field.Column) + " = " + context.dialect.Placeholder(len(pkBindings))
	}
	returning, resultColumns, err := context.returning(beforeFields)
	if err != nil {
		return nil, err
	}
	statements = append(statements, Statement{role: ApplyDelete, text: "DELETE FROM " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + pkWhere + returning, bindings: pkBindings, columns: resultColumns, cardinality: ExactlyOneRow})
	return statements, nil
}

func (context renderContext) scalarExpression(operation mutationir.ScalarOperation, column string, bindings *[]Binding) (string, error) {
	if operation.Kind() == mutationir.ScalarNull {
		return "NULL", nil
	}
	value, ok := operation.Value()
	if !ok {
		return "", fail(CodeInput, context.node.ModelID(), operation.FieldID(), "scalar operation operand is absent", nil)
	}
	encoded, err := context.encode(value, operation.Type())
	if err != nil {
		return "", fail(CodeRender, context.node.ModelID(), operation.FieldID(), "scalar operation did not encode exactly", err)
	}
	*bindings = append(*bindings, Binding{kind: StaticBinding, value: encoded})
	placeholder := context.dialect.Placeholder(len(*bindings))
	switch operation.Kind() {
	case mutationir.ScalarSet:
		return placeholder, nil
	case mutationir.ScalarIncrement:
		return "(" + column + " + " + placeholder + ")", nil
	case mutationir.ScalarDecrement:
		return "(" + column + " - " + placeholder + ")", nil
	default:
		return "", fail(CodeInput, context.node.ModelID(), operation.FieldID(), "unknown scalar operation", nil)
	}
}

func (context renderContext) actionWhere(target mutationir.Target, includeSelection bool, offset int) (string, []Binding, error) {
	parts := make([]string, 0, 4)
	bindings := make([]Binding, 0)
	identity, err := context.identity(target)
	if err != nil {
		return "", nil, err
	}
	values := target.Values()
	for index, fieldID := range identity.Fields() {
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), policyir.FieldID(fieldID))
		if !ok {
			return "", nil, fail(CodeSchema, context.node.ModelID(), policyir.FieldID(fieldID), "target field has no physical descriptor", nil)
		}
		value := values[index].Value()
		encoded, encodeErr := context.encode(value, field.Type)
		if encodeErr != nil {
			return "", nil, fail(CodeRender, context.node.ModelID(), policyir.FieldID(fieldID), "target value did not encode exactly", encodeErr)
		}
		bindings = append(bindings, Binding{kind: StaticBinding, value: encoded})
		parts = append(parts, context.qualified(field.Column)+" = "+context.dialect.Placeholder(offset+len(bindings)))
	}
	if guard, present := target.Guard(); present {
		fragment, compileErr := context.compileCondition(guard, offset+len(bindings))
		if compileErr != nil {
			return "", nil, compileErr
		}
		// A compiled target guard may be a top-level disjunction. Keep it grouped
		// under the selector predicate so SQL precedence cannot widen the target.
		parts = append(parts, "("+fragment.text+")")
		bindings = append(bindings, fragment.bindings...)
	}
	if includeSelection {
		selection, present := context.node.SelectionRequirement()
		if context.plan.Stance() == mutationir.Caller && !present {
			return "", nil, fail(CodeInput, context.node.ModelID(), policyir.FieldID{}, "caller target lacks an action-constrained selecting condition", nil)
		}
		if present {
			fragment, compileErr := context.compileCondition(selection.Constraint(), offset+len(bindings))
			if compileErr != nil {
				return "", nil, compileErr
			}
			parts = append(parts, fragment.text)
			bindings = append(bindings, fragment.bindings...)
		}
	}
	if len(parts) == 0 {
		return "", nil, fail(CodeInput, context.node.ModelID(), policyir.FieldID{}, "mutation target rendered no predicate", nil)
	}
	return strings.Join(parts, " AND "), bindings, nil
}

func (context renderContext) authorizationSelect(offset int) (string, []Binding, []AuthorizationColumn, error) {
	authored := make(map[policyir.FieldID]struct{}, len(context.node.ScalarOperations()))
	for _, operation := range context.node.ScalarOperations() {
		if !operation.RuntimeOwned() {
			authored[operation.FieldID()] = struct{}{}
		}
	}
	selects := make([]string, 0, len(context.node.FieldAuthorizations()))
	bindings := make([]Binding, 0)
	columns := make([]AuthorizationColumn, 0, len(context.node.FieldAuthorizations()))
	for _, authorization := range context.node.FieldAuthorizations() {
		if _, applies := authored[authorization.FieldID()]; !applies && !context.precheckAllAuthorizations {
			continue
		}
		if _, ok := context.resolver.Field(context.provider, context.node.ModelID(), authorization.FieldID()); !ok {
			return "", nil, nil, fail(CodeSchema, context.node.ModelID(), authorization.FieldID(), "authorized update field has no physical descriptor", nil)
		}
		fragment, err := context.compileCondition(authorization.Condition(), offset+len(bindings))
		if err != nil {
			return "", nil, nil, err
		}
		alias := fmt.Sprintf("golem_auth_%x", authorization.FieldID())
		selects = append(selects, "CASE WHEN ("+fragment.text+") THEN 1 ELSE 0 END AS "+context.dialect.Quote(physical.PhysicalName(alias)))
		bindings = append(bindings, fragment.bindings...)
		columns = append(columns, AuthorizationColumn{field: authorization.FieldID(), alias: alias})
	}
	return strings.Join(selects, ", "), bindings, columns, nil
}

type renderedFragment struct {
	text     string
	bindings []Binding
}

func (context renderContext) compileCondition(condition policyir.Condition, offset int) (renderedFragment, error) {
	fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: context.provider, Resolver: context.resolver, Dialect: context.dialect, Capabilities: context.capabilities, BoundFingerprint: context.resolver.SchemaFingerprint(), RootAlias: context.alias})
	if err != nil {
		return renderedFragment{}, fail(CodeUnsupported, context.node.ModelID(), policyir.FieldID{}, "policy fragment cannot be rendered safely", err)
	}
	args := fragment.Args()
	bindings := make([]Binding, len(args))
	for index, value := range args {
		bindings[index] = Binding{kind: StaticBinding, value: value}
	}
	return renderedFragment{text: rebasePlaceholders(fragment.SQL(), offset, context.provider), bindings: bindings}, nil
}

func (context renderContext) postconditionStatement(source uint32, conditions []policyir.Condition) (Statement, bool, error) {
	if len(conditions) == 0 {
		return Statement{}, false, nil
	}
	where, bindings, err := context.priorPrimaryKeyWhere(source)
	if err != nil {
		return Statement{}, false, err
	}
	parts := []string{where}
	for _, condition := range conditions {
		fragment, compileErr := context.compileCondition(condition, len(bindings))
		if compileErr != nil {
			return Statement{}, false, compileErr
		}
		// A compiled postcondition may be a top-level disjunction. Group it
		// beneath the locked primary identity so SQL operator precedence cannot
		// widen verification to unrelated rows.
		parts = append(parts, "("+fragment.text+")")
		bindings = append(bindings, fragment.bindings...)
	}
	pk := context.primaryKey()
	selectList, columns, err := context.selectFields(pk)
	if err != nil {
		return Statement{}, false, err
	}
	return Statement{role: VerifyPostcondition, text: "SELECT " + selectList + " FROM " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + strings.Join(parts, " AND "), bindings: bindings, columns: columns, cardinality: ExactlyOneRow}, true, nil
}

func (context renderContext) createVerificationConditions() []policyir.Condition {
	result := make([]policyir.Condition, 0, len(context.node.FieldAuthorizations())+1)
	if condition, ok := context.node.RowPostcondition(); ok {
		result = append(result, condition)
	}
	authored := make(map[policyir.FieldID]struct{}, len(context.node.ScalarOperations()))
	for _, operation := range context.node.ScalarOperations() {
		if !operation.RuntimeOwned() {
			authored[operation.FieldID()] = struct{}{}
		}
	}
	for _, authorization := range context.node.FieldAuthorizations() {
		if _, present := authored[authorization.FieldID()]; present {
			result = append(result, authorization.Condition())
		}
	}
	return result
}

func (context renderContext) updateVerificationConditions() []policyir.Condition {
	if condition, ok := context.node.RowPostcondition(); ok {
		return []policyir.Condition{condition}
	}
	return nil
}

func (context renderContext) priorPrimaryKeyWhere(source uint32) (string, []Binding, error) {
	primary := context.primaryKey()
	parts := make([]string, len(primary))
	bindings := make([]Binding, len(primary))
	for index, fieldID := range primary {
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), fieldID)
		if !ok {
			return "", nil, fail(CodeSchema, context.node.ModelID(), fieldID, "primary-key field has no physical descriptor", nil)
		}
		parts[index] = context.qualified(field.Column) + " = " + context.dialect.Placeholder(index+1)
		bindings[index] = Binding{kind: PriorResultBinding, statementIndex: source, field: fieldID}
	}
	return strings.Join(parts, " AND "), bindings, nil
}

func (context renderContext) identity(target mutationir.Target) (schema.Identity, error) {
	model, ok := context.registry.Model(golem.ModelID(context.node.ModelID()))
	if !ok {
		return schema.Identity{}, fail(CodeSchema, context.node.ModelID(), policyir.FieldID{}, "logical model is absent", nil)
	}
	identity, ok := model.Identity(target.KeyID())
	if !ok {
		return schema.Identity{}, fail(CodeSchema, context.node.ModelID(), policyir.FieldID{}, "target key is not an active unique identity", nil)
	}
	values := target.Values()
	fields := identity.Fields()
	if len(values) != len(fields) {
		return schema.Identity{}, fail(CodeInput, context.node.ModelID(), policyir.FieldID{}, "target value count does not match identity", nil)
	}
	for index := range values {
		if values[index].FieldID() != policyir.FieldID(fields[index]) {
			return schema.Identity{}, fail(CodeInput, context.node.ModelID(), values[index].FieldID(), "target value order does not match the declared identity", nil)
		}
	}
	return identity, nil
}

func (context renderContext) primaryKey() []policyir.FieldID {
	model, ok := context.registry.Model(golem.ModelID(context.node.ModelID()))
	if !ok {
		return nil
	}
	public := model.PrimaryKey()
	result := make([]policyir.FieldID, len(public))
	for index := range public {
		result[index] = policyir.FieldID(public[index])
	}
	return result
}

func (context renderContext) concurrencyFields() []policyir.FieldID {
	if context.concurrency == nil {
		return nil
	}
	return []policyir.FieldID{*context.concurrency}
}

func (context renderContext) identityVerificationFields() []policyir.FieldID {
	primary := context.primaryKey()
	if context.node.IdentityBehavior() != mutationir.IdentityMayChange {
		return primary
	}
	model, ok := context.registry.Model(golem.ModelID(context.node.ModelID()))
	if !ok {
		return primary
	}
	result := append([]policyir.FieldID(nil), primary...)
	seen := make(map[policyir.FieldID]struct{}, len(result))
	for _, field := range result {
		seen[field] = struct{}{}
	}
	for _, identity := range model.Identities() {
		for _, field := range identity.Fields() {
			id := policyir.FieldID(field)
			if _, present := seen[id]; present {
				continue
			}
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result
}

func golemFields(fields []policyir.FieldID) []golem.FieldID {
	result := make([]golem.FieldID, len(fields))
	for index, field := range fields {
		result[index] = golem.FieldID(field)
	}
	return result
}

func (context renderContext) returnFields(groups ...[]policyir.FieldID) ([]policyir.FieldID, error) {
	set := make(map[policyir.FieldID]struct{})
	for _, group := range groups {
		for _, field := range group {
			set[field] = struct{}{}
		}
	}
	result := make([]policyir.FieldID, 0, len(set))
	for field := range set {
		if _, ok := context.resolver.Field(context.provider, context.node.ModelID(), field); !ok {
			return nil, fail(CodeSchema, context.node.ModelID(), field, "persisted image field has no scalar physical descriptor", nil)
		}
		result = append(result, field)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i][:], result[j][:]) < 0 })
	if len(result) == 0 {
		return nil, fail(CodeSchema, context.node.ModelID(), policyir.FieldID{}, "mutation cannot verify identity because no primary key is available", nil)
	}
	return result, nil
}

func (context renderContext) returning(fields []policyir.FieldID) (string, []ResultColumn, error) {
	items := make([]string, len(fields))
	columns := make([]ResultColumn, len(fields))
	for index, fieldID := range fields {
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), fieldID)
		if !ok {
			return "", nil, fail(CodeSchema, context.node.ModelID(), fieldID, "returning field has no physical descriptor", nil)
		}
		alias := fmt.Sprintf("golem_c%d", index)
		items[index] = context.dialect.Quote(field.Column) + " AS " + context.dialect.Quote(physical.PhysicalName(alias))
		columns[index] = ResultColumn{field: fieldID, alias: alias}
	}
	return " RETURNING " + strings.Join(items, ", "), columns, nil
}

func (context renderContext) selectFields(fields []policyir.FieldID) (string, []ResultColumn, error) {
	items := make([]string, len(fields))
	columns := make([]ResultColumn, len(fields))
	for index, fieldID := range fields {
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), fieldID)
		if !ok {
			return "", nil, fail(CodeSchema, context.node.ModelID(), fieldID, "selected image field has no physical descriptor", nil)
		}
		alias := fmt.Sprintf("golem_c%d", index)
		items[index] = context.qualified(field.Column) + " AS " + context.dialect.Quote(physical.PhysicalName(alias))
		columns[index] = ResultColumn{field: fieldID, alias: alias}
	}
	return strings.Join(items, ", "), columns, nil
}

func (context renderContext) encode(value policyir.Value, typ policyir.TypeRef) (any, error) {
	bound := policysql.BoundValue{Value: value, Type: typ}
	if typ.Kind() == policyir.ValueEnum {
		enum, member, ok := value.Enum()
		if !ok {
			return nil, fmt.Errorf("enum type received non-enum value")
		}
		wire, ok := context.resolver.EnumWire(enum, member)
		if !ok {
			return nil, fmt.Errorf("enum member has no physical wire label")
		}
		bound.EnumWires = []string{wire}
	} else if typ.Kind() == policyir.ValueScalarList {
		element, _ := typ.Element()
		if element.Kind() == policyir.ValueEnum {
			values, _ := value.List()
			bound.EnumWires = make([]string, len(values))
			for index, item := range values {
				enum, member, ok := item.Enum()
				if !ok {
					return nil, fmt.Errorf("enum list contains non-enum value")
				}
				wire, ok := context.resolver.EnumWire(enum, member)
				if !ok {
					return nil, fmt.Errorf("enum list member has no physical wire label")
				}
				bound.EnumWires[index] = wire
			}
		}
	}
	return context.dialect.Encode(bound)
}

func (context renderContext) qualified(column physical.PhysicalName) string {
	return context.dialect.Quote(context.alias) + "." + context.dialect.Quote(column)
}

func hasDependencies(requirements mutationir.ImageRequirements) bool {
	for _, dependency := range requirements.Dependencies() {
		if len(dependency.Path()) != 0 {
			return true
		}
	}
	return false
}

func scalarOperationFields(operations []mutationir.ScalarOperation) []policyir.FieldID {
	result := make([]policyir.FieldID, len(operations))
	for index, operation := range operations {
		result[index] = operation.FieldID()
	}
	return result
}

func authoredScalarOperationFields(operations []mutationir.ScalarOperation) []policyir.FieldID {
	result := make([]policyir.FieldID, 0, len(operations))
	for _, operation := range operations {
		if !operation.RuntimeOwned() {
			result = append(result, operation.FieldID())
		}
	}
	return result
}

func providerDialect(provider policyir.Provider) (policysql.Dialect, TransactionRequirement, error) {
	switch provider {
	case policyir.ProviderSQLite:
		return sqliteprovider.NewPolicyDialect(), SQLiteImmediateTransaction, nil
	case policyir.ProviderPostgreSQL:
		return postgresprovider.NewPolicyDialect(), PostgreSQLTransaction, nil
	default:
		return nil, 0, fmt.Errorf("unknown provider %d", provider)
	}
}

func mutationStatementIndex(operation mutationir.Operation) uint32 {
	if operation == mutationir.Create {
		return 0
	}
	return 1
}

func validRootIdentityBehavior(operation mutationir.Operation, behavior mutationir.IdentityBehavior) bool {
	switch operation {
	case mutationir.Create:
		return behavior == mutationir.IdentityProduced
	case mutationir.Update:
		return behavior == mutationir.IdentityUnchanged || behavior == mutationir.IdentityMayChange
	case mutationir.Delete:
		return behavior == mutationir.IdentityUnchanged
	default:
		return false
	}
}

func rebasePlaceholders(value string, offset int, provider policyir.Provider) string {
	if offset == 0 {
		return value
	}
	marker := byte('$')
	if provider == policyir.ProviderSQLite {
		marker = '?'
	}
	var result strings.Builder
	for index := 0; index < len(value); {
		if value[index] != marker || index+1 >= len(value) || value[index+1] < '0' || value[index+1] > '9' {
			result.WriteByte(value[index])
			index++
			continue
		}
		end := index + 1
		for end < len(value) && value[end] >= '0' && value[end] <= '9' {
			end++
		}
		position, _ := strconv.Atoi(value[index+1 : end])
		result.WriteByte(marker)
		result.WriteString(strconv.Itoa(position + offset))
		index = end
	}
	return result.String()
}

func sameType(left, right policyir.TypeRef) bool {
	if left.Kind() != right.Kind() || left.Nullable() != right.Nullable() || left.Precision() != right.Precision() || left.Scale() != right.Scale() || left.Capability() != right.Capability() {
		return false
	}
	leftEnum, leftHasEnum := left.EnumID()
	rightEnum, rightHasEnum := right.EnumID()
	if leftHasEnum != rightHasEnum || leftHasEnum && leftEnum != rightEnum {
		return false
	}
	leftElement, leftHasElement := left.Element()
	rightElement, rightHasElement := right.Element()
	return leftHasElement == rightHasElement && (!leftHasElement || sameType(leftElement, rightElement))
}
