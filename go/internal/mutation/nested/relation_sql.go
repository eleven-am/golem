package nested

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type RelationSQLRole uint8

const (
	ExpandRelatedRows RelationSQLRole = iota + 1
	ExpandCurrentMembership
	ExpandDesiredTarget
	ApplyMembershipConnect
	ApplyMembershipDisconnect
)

type RelationSQLColumn struct {
	field policyir.FieldID
	alias string
}

func (column RelationSQLColumn) FieldID() policyir.FieldID { return column.field }
func (column RelationSQLColumn) Alias() string             { return column.alias }

type RelationSQLStatement struct {
	role    RelationSQLRole
	model   policyir.ModelID
	text    string
	args    []any
	columns []RelationSQLColumn
	maxRows uint32
}

func (statement RelationSQLStatement) Role() RelationSQLRole { return statement.role }
func (statement RelationSQLStatement) ModelID() policyir.ModelID {
	return statement.model
}
func (statement RelationSQLStatement) SQL() string { return statement.text }
func (statement RelationSQLStatement) Args() []any { return cloneRelationArgs(statement.args) }
func (statement RelationSQLStatement) Columns() []RelationSQLColumn {
	return append([]RelationSQLColumn(nil), statement.columns...)
}
func (statement RelationSQLStatement) MaxRows() uint32 { return statement.maxRows }

type RelationSQLProgram struct{ statements []RelationSQLStatement }

func (program RelationSQLProgram) Statements() []RelationSQLStatement {
	result := make([]RelationSQLStatement, len(program.statements))
	for index, statement := range program.statements {
		result[index] = statement
		result[index].args = statement.Args()
		result[index].columns = statement.Columns()
	}
	return result
}

type RelationExpansionSQLRequest struct {
	Node          mutationir.Node
	Anchor        mutationdecode.Row
	Registry      *schema.Registry
	Provider      policyir.Provider
	Capabilities  policysql.CapabilityProof
	MaxRows       uint32
	MaxParameters uint32
}

// RenderRelationExpansion renders only descriptor-owned identifiers. Runtime
// anchor values, selector values, guards, and predicates are fully bound.
// Correlation pairs are emitted in normalized RelationIR order.
func RenderRelationExpansion(request RelationExpansionSQLRequest) (RelationSQLProgram, error) {
	context, err := newRelationRenderContext(request.Node, request.Anchor, request.Registry, request.Provider, request.Capabilities, request.MaxRows, request.MaxParameters)
	if err != nil {
		return RelationSQLProgram{}, err
	}
	position, ok := request.Node.RelationPosition()
	if !ok {
		return RelationSQLProgram{}, fmt.Errorf("P4_NESTED_SQL_INPUT: node %d has no relation position", request.Node.Ordinal())
	}
	switch position.Kind() {
	case mutationir.PositionEndpoint, mutationir.PositionBranchResult:
		return RelationSQLProgram{}, nil
	case mutationir.PositionSetDifference:
		current, renderErr := context.renderTargetQuery(ExpandCurrentMembership, nil, nil, true, request.MaxRows)
		if renderErr != nil {
			return RelationSQLProgram{}, renderErr
		}
		statements := []RelationSQLStatement{current}
		for _, target := range position.DesiredTargets() {
			value := target
			statement, targetErr := context.renderTargetQuery(ExpandDesiredTarget, &value, nil, false, 1)
			if targetErr != nil {
				return RelationSQLProgram{}, targetErr
			}
			statements = append(statements, statement)
		}
		return RelationSQLProgram{statements: statements}, nil
	case mutationir.PositionCurrentToOne:
		statement, renderErr := context.renderTargetQuery(ExpandRelatedRows, nil, nil, true, 1)
		return oneRelationStatement(statement, renderErr)
	case mutationir.PositionEntireMembership:
		statement, renderErr := context.renderTargetQuery(ExpandCurrentMembership, nil, nil, true, request.MaxRows)
		return oneRelationStatement(statement, renderErr)
	case mutationir.PositionRelatedPredicate:
		predicate, _ := position.Predicate()
		statement, renderErr := context.renderTargetQuery(ExpandRelatedRows, nil, &predicate, true, request.MaxRows)
		return oneRelationStatement(statement, renderErr)
	case mutationir.PositionRelatedTarget:
		target, _ := position.Target()
		// Connect and Disconnect both resolve the selected target independently
		// of current membership. The locked correlation tuple is compared after
		// expansion: already-connected/disconnected targets become no work,
		// while Disconnect never clears a target owned by a different anchor.
		correlated := request.Node.Operation() != mutationir.Connect && request.Node.Operation() != mutationir.Disconnect && request.Node.Operation() != mutationir.ConnectOrCreate && request.Node.Operation() != mutationir.BranchProbe
		statement, renderErr := context.renderTargetQuery(ExpandRelatedRows, &target, nil, correlated, 1)
		return oneRelationStatement(statement, renderErr)
	default:
		return RelationSQLProgram{}, fmt.Errorf("P4_NESTED_SQL_INPUT: unsupported relation position %d", position.Kind())
	}
}

func oneRelationStatement(statement RelationSQLStatement, err error) (RelationSQLProgram, error) {
	if err != nil {
		return RelationSQLProgram{}, err
	}
	return RelationSQLProgram{statements: []RelationSQLStatement{statement}}, nil
}

type MembershipEffect uint8

const (
	MembershipConnect MembershipEffect = iota + 1
	MembershipDisconnect
)

type MembershipSQLRequest struct {
	Node          mutationir.Node
	Anchor        mutationdecode.Row
	Related       mutationdecode.Row
	Effect        MembershipEffect
	Registry      *schema.Registry
	Provider      policyir.Provider
	MaxParameters uint32
}

// RenderMembershipWrite updates the actual FK-owning row. Source endpoints
// write parent correlation fields from the selected/created target row;
// inverse endpoints write target correlation fields from the anchor row.
func RenderMembershipWrite(request MembershipSQLRequest) (RelationSQLStatement, error) {
	if request.Registry == nil || request.MaxParameters == 0 {
		return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_INPUT: registry and parameter bound are required")
	}
	position, ok := request.Node.RelationPosition()
	if !ok || position.ParentModelID() != request.Anchor.ModelID() {
		return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_INPUT: membership node lacks its exact anchor")
	}
	endpoint, ok := request.Registry.RelationEndpoint(golem.ModelID(position.ParentModelID()), golem.FieldID(position.FieldID()), golem.RelationID(position.RelationID()))
	if !ok || policyir.ModelID(endpoint.TargetModelID()) != position.TargetModelID() || request.Related.ModelID() != position.TargetModelID() {
		return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_SCHEMA: relation endpoint or related row is invalid")
	}
	dialect, err := relationDialect(request.Provider)
	if err != nil {
		return RelationSQLStatement{}, err
	}
	resolver := policysql.SchemaResolver(request.Registry)
	ownerModel := request.Node.ModelID()
	owner := request.Anchor
	if endpoint.Role() == compilerir.RelationInverse {
		owner = request.Related
	}
	if owner.ModelID() != ownerModel {
		return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_INPUT: membership owner row belongs to another model")
	}
	model, ok := resolver.Model(request.Provider, ownerModel)
	if !ok {
		return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_SCHEMA: membership owner physical model is absent")
	}
	pairs := endpoint.Correlation()
	if len(pairs) == 0 {
		return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_SCHEMA: relation correlation is empty")
	}
	assignments := make([]string, len(pairs))
	args := make([]any, 0, len(pairs))
	for index, pair := range pairs {
		ownerField, valueField := policyir.FieldID(pair.ParentFieldID()), policyir.FieldID(pair.ChildFieldID())
		valueRow := request.Related
		if endpoint.Role() == compilerir.RelationInverse {
			ownerField, valueField, valueRow = policyir.FieldID(pair.ChildFieldID()), policyir.FieldID(pair.ParentFieldID()), request.Anchor
		}
		physicalField, found := resolver.Field(request.Provider, ownerModel, ownerField)
		if !found {
			return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_SCHEMA: owner correlation field %x is absent", ownerField)
		}
		if request.Effect == MembershipDisconnect {
			if !physicalField.Nullable {
				return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_REQUIRED: non-null membership field %x cannot disconnect", ownerField)
			}
			assignments[index] = dialect.Quote(physicalField.Column) + " = NULL"
			continue
		}
		cell, present := valueRow.Cell(valueField)
		if !present || cell.IsNull() {
			return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_INPUT: correlation value field %x is absent or NULL", valueField)
		}
		value, _ := cell.PolicyValue()
		valuePhysical, found := resolver.Field(request.Provider, valueRow.ModelID(), valueField)
		if !found {
			return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_SCHEMA: correlation value field %x is absent", valueField)
		}
		encoded, encodeErr := encodeRelationValue(dialect, resolver, valuePhysical.Type, value)
		if encodeErr != nil {
			return RelationSQLStatement{}, encodeErr
		}
		args = append(args, encoded)
		assignments[index] = dialect.Quote(physicalField.Column) + " = " + dialect.Placeholder(len(args))
	}
	primary, err := primaryFields(request.Registry, ownerModel)
	if err != nil {
		return RelationSQLStatement{}, err
	}
	where := make([]string, len(primary))
	for index, fieldID := range primary {
		cell, present := owner.Cell(fieldID)
		if !present || cell.IsNull() {
			return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_INPUT: owner primary field %x is absent or NULL", fieldID)
		}
		value, _ := cell.PolicyValue()
		field, found := resolver.Field(request.Provider, ownerModel, fieldID)
		if !found {
			return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_SCHEMA: owner primary field %x is absent", fieldID)
		}
		encoded, encodeErr := encodeRelationValue(dialect, resolver, field.Type, value)
		if encodeErr != nil {
			return RelationSQLStatement{}, encodeErr
		}
		args = append(args, encoded)
		where[index] = dialect.Quote(field.Column) + " = " + dialect.Placeholder(len(args))
	}
	if uint32(len(args)) > request.MaxParameters {
		return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_LIMIT: membership write needs %d parameters; maximum is %d", len(args), request.MaxParameters)
	}
	returning, columns, err := returningAll(request.Registry, resolver, dialect, request.Provider, ownerModel)
	if err != nil {
		return RelationSQLStatement{}, err
	}
	role := ApplyMembershipConnect
	if request.Effect == MembershipDisconnect {
		role = ApplyMembershipDisconnect
	}
	text := "UPDATE " + dialect.Table(model) + " SET " + strings.Join(assignments, ", ") + " WHERE " + strings.Join(where, " AND ") + " RETURNING " + returning
	return RelationSQLStatement{role: role, model: ownerModel, text: text, args: args, columns: columns, maxRows: 1}, nil
}

type relationRenderContext struct {
	node          mutationir.Node
	anchor        mutationdecode.Row
	registry      *schema.Registry
	provider      policyir.Provider
	capabilities  policysql.CapabilityProof
	maxRows       uint32
	maxParameters uint32
	dialect       policysql.Dialect
	resolver      policysql.Resolver
	endpoint      schema.RelationEndpoint
	targetModel   policysql.Model
	alias         physical.PhysicalName
}

func newRelationRenderContext(node mutationir.Node, anchor mutationdecode.Row, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof, maxRows, maxParameters uint32) (relationRenderContext, error) {
	if registry == nil || maxRows == 0 || maxParameters == 0 {
		return relationRenderContext{}, fmt.Errorf("P4_NESTED_SQL_INPUT: registry and positive bounds are required")
	}
	position, ok := node.RelationPosition()
	anchorlessDependency := node.ExecutesBeforeParent() && (node.Operation() == mutationir.BranchProbe || node.Operation() == mutationir.ConnectOrCreate) && anchor.ModelID() == (policyir.ModelID{})
	if !ok || !anchorlessDependency && position.ParentModelID() != anchor.ModelID() {
		return relationRenderContext{}, fmt.Errorf("P4_NESTED_SQL_INPUT: relation position and anchor disagree")
	}
	endpoint, ok := registry.RelationEndpoint(golem.ModelID(position.ParentModelID()), golem.FieldID(position.FieldID()), golem.RelationID(position.RelationID()))
	if !ok || policyir.ModelID(endpoint.TargetModelID()) != position.TargetModelID() {
		return relationRenderContext{}, fmt.Errorf("P4_NESTED_SQL_SCHEMA: exact relation endpoint is absent")
	}
	dialect, err := relationDialect(provider)
	if err != nil {
		return relationRenderContext{}, err
	}
	resolver := policysql.SchemaResolver(registry)
	target, ok := resolver.Model(provider, position.TargetModelID())
	if !ok {
		return relationRenderContext{}, fmt.Errorf("P4_NESTED_SQL_SCHEMA: target physical model is absent")
	}
	if capabilities.Provider() != provider || capabilities.SchemaFingerprint() != resolver.SchemaFingerprint() {
		return relationRenderContext{}, fmt.Errorf("P4_NESTED_SQL_PROVIDER: capability proof does not match provider/schema")
	}
	return relationRenderContext{node: node, anchor: anchor, registry: registry, provider: provider, capabilities: capabilities, maxRows: maxRows, maxParameters: maxParameters, dialect: dialect, resolver: resolver, endpoint: endpoint, targetModel: target, alias: "golem_nr"}, nil
}

func (context relationRenderContext) renderTargetQuery(role RelationSQLRole, target *mutationir.Target, predicate *policyir.Condition, correlated bool, maxRows uint32) (RelationSQLStatement, error) {
	if maxRows == 0 || maxRows > context.maxRows {
		return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_LIMIT: expansion row bound is invalid")
	}
	parts := make([]string, 0)
	args := make([]any, 0)
	if correlated {
		pairs := context.endpoint.Correlation()
		nulls := 0
		for _, pair := range pairs {
			parentField := policyir.FieldID(pair.ParentFieldID())
			cell, ok := context.anchor.Cell(parentField)
			if !ok {
				return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_INPUT: anchor correlation field %x is absent", parentField)
			}
			if cell.IsNull() {
				nulls++
			}
		}
		if nulls != 0 && nulls != len(pairs) {
			return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_INPUT: composite anchor correlation is partially NULL")
		}
		if nulls == len(pairs) {
			// A fully NULL optional source correlation has an empty current
			// membership. Render that empty set explicitly so Disconnect and
			// Set([]) remain true no-ops instead of failing before expansion.
			parts = append(parts, "1 = 0")
		} else {
			for _, pair := range pairs {
				parentField := policyir.FieldID(pair.ParentFieldID())
				childField := policyir.FieldID(pair.ChildFieldID())
				cell, _ := context.anchor.Cell(parentField)
				value, _ := cell.PolicyValue()
				targetPhysical, ok := context.resolver.Field(context.provider, policyir.ModelID(context.endpoint.TargetModelID()), childField)
				if !ok {
					return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_SCHEMA: target correlation field %x is absent", childField)
				}
				encoded, err := encodeRelationValue(context.dialect, context.resolver, targetPhysical.Type, value)
				if err != nil {
					return RelationSQLStatement{}, err
				}
				args = append(args, encoded)
				parts = append(parts, context.qualified(targetPhysical.Column)+" = "+context.dialect.Placeholder(len(args)))
			}
		}
	}
	if target != nil {
		if target.ModelID() != policyir.ModelID(context.endpoint.TargetModelID()) {
			return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_INPUT: target belongs to another model")
		}
		for _, selector := range target.Values() {
			field, ok := context.resolver.Field(context.provider, target.ModelID(), selector.FieldID())
			if !ok {
				return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_SCHEMA: selector field %x is absent", selector.FieldID())
			}
			encoded, err := encodeRelationValue(context.dialect, context.resolver, field.Type, selector.Value())
			if err != nil {
				return RelationSQLStatement{}, err
			}
			args = append(args, encoded)
			parts = append(parts, context.qualified(field.Column)+" = "+context.dialect.Placeholder(len(args)))
		}
		if guard, ok := target.Guard(); ok {
			fragment, err := context.compile(guard, len(args))
			if err != nil {
				return RelationSQLStatement{}, err
			}
			parts, args = append(parts, fragment.text), append(args, fragment.args...)
		}
	}
	if predicate != nil {
		fragment, err := context.compile(*predicate, len(args))
		if err != nil {
			return RelationSQLStatement{}, err
		}
		parts, args = append(parts, fragment.text), append(args, fragment.args...)
	}
	// Exact target-model writes are authorized while their related row is
	// selected and locked. This matters for coordinated source to-one Delete:
	// its delete policy may traverse the relation that the preceding owner
	// disconnect intentionally clears. The later scalar write can therefore use
	// the captured identity without reopening authorization against mutated
	// relation state.
	batchAuthorizesCapturedRows := context.node.Operation() == mutationir.UpdateMany || context.node.Operation() == mutationir.DeleteMany
	if selection, present := context.node.SelectionRequirement(); !batchAuthorizesCapturedRows && present && selection.Constraint().ModelID() == policyir.ModelID(context.endpoint.TargetModelID()) {
		fragment, err := context.compile(selection.Constraint(), len(args))
		if err != nil {
			return RelationSQLStatement{}, err
		}
		parts, args = append(parts, fragment.text), append(args, fragment.args...)
	}
	if len(parts) == 0 {
		parts = append(parts, "1 = 1")
	}
	if uint32(len(args)) > context.maxParameters {
		return RelationSQLStatement{}, fmt.Errorf("P4_NESTED_SQL_LIMIT: expansion needs %d parameters; maximum is %d", len(args), context.maxParameters)
	}
	fields, columns, err := selectAll(context.registry, context.resolver, context.dialect, context.provider, policyir.ModelID(context.endpoint.TargetModelID()), context.alias)
	if err != nil {
		return RelationSQLStatement{}, err
	}
	primary, err := primaryFields(context.registry, policyir.ModelID(context.endpoint.TargetModelID()))
	if err != nil {
		return RelationSQLStatement{}, err
	}
	order := make([]string, len(primary))
	for index, fieldID := range primary {
		field, _ := context.resolver.Field(context.provider, policyir.ModelID(context.endpoint.TargetModelID()), fieldID)
		order[index] = context.qualified(field.Column) + " ASC"
	}
	lock := ""
	if context.provider == policyir.ProviderPostgreSQL {
		lock = " FOR UPDATE"
	}
	limit := uint64(maxRows)
	if maxRows == context.maxRows {
		limit++ // sentinel; executor refuses rather than truncates.
	}
	text := "SELECT " + fields + " FROM " + context.dialect.Table(context.targetModel) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + strings.Join(parts, " AND ") + " ORDER BY " + strings.Join(order, ", ") + fmt.Sprintf(" LIMIT %d", limit) + lock
	return RelationSQLStatement{role: role, model: policyir.ModelID(context.endpoint.TargetModelID()), text: text, args: args, columns: columns, maxRows: maxRows}, nil
}

type relationFragment struct {
	text string
	args []any
}

func (context relationRenderContext) compile(condition policyir.Condition, offset int) (relationFragment, error) {
	fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: context.provider, Resolver: context.resolver, Dialect: context.dialect, Capabilities: context.capabilities, BoundFingerprint: context.resolver.SchemaFingerprint(), RootAlias: context.alias})
	if err != nil {
		return relationFragment{}, fmt.Errorf("P4_NESTED_SQL_RENDER: condition cannot compile: %w", err)
	}
	return relationFragment{text: policysql.RebasePlaceholders(fragment.SQL(), offset, context.provider), args: fragment.Args()}, nil
}

func (context relationRenderContext) qualified(column physical.PhysicalName) string {
	return context.dialect.Quote(context.alias) + "." + context.dialect.Quote(column)
}

func selectAll(registry *schema.Registry, resolver policysql.Resolver, dialect policysql.Dialect, provider policyir.Provider, model policyir.ModelID, alias physical.PhysicalName) (string, []RelationSQLColumn, error) {
	logical, ok := registry.Model(golem.ModelID(model))
	if !ok {
		return "", nil, fmt.Errorf("P4_NESTED_SQL_SCHEMA: logical model is absent")
	}
	var fields []policyir.FieldID
	for _, fieldID := range logical.Fields() {
		if _, ok := resolver.Field(provider, model, policyir.FieldID(fieldID)); ok {
			fields = append(fields, policyir.FieldID(fieldID))
		}
	}
	sort.Slice(fields, func(i, j int) bool { return bytes.Compare(fields[i][:], fields[j][:]) < 0 })
	selects := make([]string, len(fields))
	columns := make([]RelationSQLColumn, len(fields))
	for index, fieldID := range fields {
		field, _ := resolver.Field(provider, model, fieldID)
		name := "golem_f_" + hex.EncodeToString(fieldID[:])
		column := dialect.Quote(field.Column)
		if alias != "" {
			column = dialect.Quote(alias) + "." + column
		}
		selects[index] = column + " AS " + dialect.Quote(physical.PhysicalName(name))
		columns[index] = RelationSQLColumn{field: fieldID, alias: name}
	}
	if len(selects) == 0 {
		return "", nil, fmt.Errorf("P4_NESTED_SQL_SCHEMA: model has no persisted scalar fields")
	}
	return strings.Join(selects, ", "), columns, nil
}

func returningAll(registry *schema.Registry, resolver policysql.Resolver, dialect policysql.Dialect, provider policyir.Provider, model policyir.ModelID) (string, []RelationSQLColumn, error) {
	return selectAll(registry, resolver, dialect, provider, model, "")
}

func primaryFields(registry *schema.Registry, model policyir.ModelID) ([]policyir.FieldID, error) {
	logical, ok := registry.Model(golem.ModelID(model))
	if !ok || len(logical.PrimaryKey()) == 0 {
		return nil, fmt.Errorf("P4_NESTED_SQL_SCHEMA: model has no primary key")
	}
	result := make([]policyir.FieldID, len(logical.PrimaryKey()))
	for index, field := range logical.PrimaryKey() {
		result[index] = policyir.FieldID(field)
	}
	return result, nil
}

func relationDialect(provider policyir.Provider) (policysql.Dialect, error) {
	switch provider {
	case policyir.ProviderSQLite:
		return sqliteprovider.NewPolicyDialect(), nil
	case policyir.ProviderPostgreSQL:
		return postgresprovider.NewPolicyDialect(), nil
	default:
		return nil, fmt.Errorf("P4_NESTED_SQL_PROVIDER: unsupported provider %d", provider)
	}
}

func encodeRelationValue(dialect policysql.Dialect, resolver policysql.Resolver, typ policyir.TypeRef, value policyir.Value) (any, error) {
	bound := policysql.BoundValue{Value: value, Type: typ}
	if typ.Kind() == policyir.ValueEnum {
		enum, member, ok := value.Enum()
		if !ok {
			return nil, fmt.Errorf("P4_NESTED_SQL_INPUT: enum value is invalid")
		}
		wire, ok := resolver.EnumWire(enum, member)
		if !ok {
			return nil, fmt.Errorf("P4_NESTED_SQL_SCHEMA: enum wire value is absent")
		}
		bound.EnumWires = []string{wire}
	}
	return dialect.Encode(bound)
}

func cloneRelationArgs(values []any) []any {
	result := make([]any, len(values))
	for index, value := range values {
		switch typed := value.(type) {
		case []byte:
			result[index] = append([]byte(nil), typed...)
		case []string:
			result[index] = append([]string(nil), typed...)
		case time.Time:
			result[index] = typed
		default:
			result[index] = value
		}
	}
	return result
}
