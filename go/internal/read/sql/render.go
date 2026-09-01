// Package sql renders complete deterministic P3 root read statements from
// authorized provider-neutral plans. Caller values remain positional binds.
package sql

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type ErrorCode string

const (
	CodeInput  ErrorCode = "P3_SQL_INPUT"
	CodeSchema ErrorCode = "P3_SQL_SCHEMA"
	CodeRender ErrorCode = "P3_SQL_RENDER"
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

type Column struct {
	field  policyir.FieldID
	alias  string
	public bool
}

type CountColumn struct {
	relation   policyir.RelationID
	alias      string
	occurrence readir.OccurrenceID
}

func (column CountColumn) RelationID() policyir.RelationID   { return column.relation }
func (column CountColumn) Alias() string                     { return column.alias }
func (column CountColumn) OccurrenceID() readir.OccurrenceID { return column.occurrence }

func (column Column) FieldID() policyir.FieldID { return column.field }
func (column Column) Alias() string             { return column.alias }
func (column Column) Public() bool              { return column.public }

type Statement struct {
	text       string
	args       []any
	columns    []Column
	counts     []CountColumn
	correlated []CorrelatedColumn
	planMap    PlanMap
	count      bool
	reverse    bool
}

func (statement Statement) SQL() string       { return statement.text }
func (statement Statement) Args() []any       { return cloneArgs(statement.args) }
func (statement Statement) Columns() []Column { return append([]Column(nil), statement.columns...) }
func (statement Statement) CountColumns() []CountColumn {
	return append([]CountColumn(nil), statement.counts...)
}
func (statement Statement) CorrelatedColumns() []CorrelatedColumn {
	return append([]CorrelatedColumn(nil), statement.correlated...)
}
func (statement Statement) PlanMap() PlanMap    { return statement.planMap.clone() }
func (statement Statement) IsCount() bool       { return statement.count }
func (statement Statement) ReverseResult() bool { return statement.reverse }

func Render(plan readplan.Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof) (Statement, error) {
	if registry == nil || plan.ModelID() == (policyir.ModelID{}) {
		return Statement{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "plan and registry are required", nil)
	}
	dialect, err := providerDialect(provider)
	if err != nil {
		return Statement{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "provider is unsupported", err)
	}
	resolver := policysql.SchemaResolver(registry)
	model, ok := resolver.Model(provider, plan.ModelID())
	if !ok {
		return Statement{}, fail(CodeSchema, plan.ModelID(), policyir.FieldID{}, "physical model is absent", nil)
	}
	rootAlias := physical.PhysicalName("golem_r0")
	var planMap planMapBuilder
	policyAliases := policysql.NewPolicyAliasAllocator()
	readAliases := &readPlanAliasAllocator{}
	planMap.add(string(rootAlias), plan.ModelID(), policyir.RelationID{}, nil, PlanAliasPhysicalAccess)
	fragment, err := policysql.CompileWithPolicyAliasAllocator(policysql.Request{Condition: plan.Where(), Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: rootAlias}, policyAliases)
	if err != nil {
		return Statement{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "authorized predicate did not render", err)
	}
	planMap.mergePolicy(fragment.PolicyRelationAliases())
	rootArgs := fragment.Args()
	if plan.Operation() == readir.Count {
		if err := enforceStatementParameterLimitWith(plan.ModelID(), rootArgs, plan.Limits().MaxStatementParameters); err != nil {
			return Statement{}, err
		}
		text := "SELECT COUNT(*) AS " + dialect.Quote("golem_count") + " FROM " + dialect.Table(model) + " AS " + dialect.Quote(rootAlias) + " WHERE " + fragment.SQL()
		if err := ValidateStatementComplexity(plan.ModelID(), text, plan.Limits().MaxStatementBytes, plan.Limits().MaxStatementAliases); err != nil {
			return Statement{}, err
		}
		return Statement{text: text, args: rootArgs, planMap: planMap.freeze(), count: true}, nil
	}

	fields := plan.Fields()
	if len(fields) == 0 {
		return Statement{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "row statement has no scalar decode columns", nil)
	}
	columns := make([]Column, len(fields))
	physicalColumns := make(map[policyir.FieldID]string, len(fields))
	selectRoot := make([]string, len(fields))
	for index, field := range fields {
		resolved, found := resolver.Field(provider, plan.ModelID(), field.FieldID())
		if !found {
			return Statement{}, fail(CodeSchema, plan.ModelID(), field.FieldID(), "physical field is absent", nil)
		}
		alias := fmt.Sprintf("golem_c%d", index)
		column := dialect.Quote(rootAlias) + "." + dialect.Quote(resolved.Column)
		physicalColumns[field.FieldID()] = column
		selectRoot[index] = column + " AS " + dialect.Quote(physical.PhysicalName(alias))
		columns[index] = Column{field: field.FieldID(), alias: alias, public: field.Public()}
	}
	renderedCounts, err := renderRelationCounts(plan, registry, provider, capabilities, dialect, rootAlias, policyAliases, readAliases)
	if err != nil {
		return Statement{}, err
	}
	for _, count := range renderedCounts {
		planMap.merge(count.planMap)
	}
	renderedRelations, err := renderCorrelatedRelations(plan, registry, provider, capabilities, dialect, rootAlias, policyAliases, readAliases)
	if err != nil {
		return Statement{}, err
	}
	for _, relation := range renderedRelations {
		planMap.merge(relation.planMap)
	}

	orders := plan.OrderBy()
	reverse := false
	if take, present := plan.Take(); present && take < 0 {
		reverse = true
	}
	rootOrder, outerOrder, err := renderOrders(orders, fields, physicalColumns, dialect, provider, reverse)
	if err != nil {
		return Statement{}, fail(CodeSchema, plan.ModelID(), policyir.FieldID{}, "order did not render", err)
	}
	prefix := ""
	from := dialect.Table(model) + " AS " + dialect.Quote(rootAlias)
	rootWhere := fragment.SQL()
	var cursorArgs []any
	if cursor, present := plan.Cursor(); present {
		cursorPrefix, cursorJoin, boundary, values, cursorPlanMap, cursorErr := renderCursor(plan, cursor, registry, provider, capabilities, dialect, model, rootAlias, physicalColumns, orders, reverse, policyir.RelationID{}, PlanAliasPhysicalAccess, policyAliases)
		if cursorErr != nil {
			return Statement{}, cursorErr
		}
		prefix = cursorPrefix
		cursorArgs = values
		planMap.merge(cursorPlanMap)
		from += cursorJoin
		rootWhere = "(" + rootWhere + ") AND (" + boundary + ")"
	}
	countExpressions := make([]string, len(renderedCounts))
	countColumns := make([]CountColumn, len(renderedCounts))
	countArgs := make([]any, 0)
	for index, count := range renderedCounts {
		countExpressions[index] = policysql.RebasePlaceholders(count.expression, len(cursorArgs)+len(countArgs), provider)
		countColumns[index] = count.column
		countArgs = append(countArgs, count.args...)
	}
	relationExpressions := make([]string, len(renderedRelations))
	correlatedColumns := make([]CorrelatedColumn, len(renderedRelations))
	relationArgs := make([]any, 0)
	for index, relation := range renderedRelations {
		relationExpressions[index] = policysql.RebasePlaceholders(relation.expression, len(cursorArgs)+len(countArgs)+len(relationArgs), provider)
		correlatedColumns[index] = relation.column
		relationArgs = append(relationArgs, relation.args...)
	}
	rootWhere = policysql.RebasePlaceholders(rootWhere, len(cursorArgs)+len(countArgs)+len(relationArgs), provider)
	args := make([]any, 0, len(cursorArgs)+len(countArgs)+len(relationArgs)+len(rootArgs)+2)
	args = append(args, cursorArgs...)
	args = append(args, countArgs...)
	args = append(args, relationArgs...)
	args = append(args, rootArgs...)
	selectRoot = append(selectRoot, countExpressions...)
	selectRoot = append(selectRoot, relationExpressions...)

	var text string
	if distinct := plan.Distinct(); len(distinct) != 0 {
		logicalTypes := logicalTypesByField(fields)
		partitions := make([]string, len(distinct))
		for index, field := range distinct {
			column := physicalColumns[field]
			if column == "" {
				return Statement{}, fail(CodeSchema, plan.ModelID(), field, "distinct field is absent from physical projection", nil)
			}
			partitions[index] = portableOrderOperand(provider, logicalTypes[field], column)
		}
		windowOrder := rootOrder
		if windowOrder == "" {
			windowOrder = strings.Join(partitions, ", ")
		}
		inner := append([]string(nil), selectRoot...)
		inner = append(inner, "ROW_NUMBER() OVER (PARTITION BY "+strings.Join(partitions, ", ")+" ORDER BY "+windowOrder+") AS "+dialect.Quote("golem_rank"))
		outerAlias := physical.PhysicalName("golem_d0")
		outerSelect := make([]string, 0, len(columns)+len(countColumns)+len(correlatedColumns))
		for _, column := range columns {
			quoted := dialect.Quote(physical.PhysicalName(column.alias))
			outerSelect = append(outerSelect, dialect.Quote(outerAlias)+"."+quoted+" AS "+quoted)
		}
		for _, column := range countColumns {
			quoted := dialect.Quote(physical.PhysicalName(column.alias))
			outerSelect = append(outerSelect, dialect.Quote(outerAlias)+"."+quoted+" AS "+quoted)
		}
		for _, column := range correlatedColumns {
			quoted := dialect.Quote(physical.PhysicalName(column.alias))
			outerSelect = append(outerSelect, dialect.Quote(outerAlias)+"."+quoted+" AS "+quoted)
		}
		text = "SELECT " + strings.Join(outerSelect, ", ") + " FROM (SELECT " + strings.Join(inner, ", ") + " FROM " + from + " WHERE " + rootWhere + ") AS " + dialect.Quote(outerAlias) + " WHERE " + dialect.Quote(outerAlias) + "." + dialect.Quote("golem_rank") + " = 1"
		if outerOrder != "" {
			text += " ORDER BY " + outerOrder
		}
	} else {
		text = "SELECT " + strings.Join(selectRoot, ", ") + " FROM " + from + " WHERE " + rootWhere
		if rootOrder != "" {
			text += " ORDER BY " + rootOrder
		}
	}
	limit := 0
	limitPresent := false
	if take, present := plan.Take(); present {
		limit, limitPresent = take, true
		if limit < 0 {
			limit = -limit
		}
	}
	if plan.Operation() == readir.FindFirst {
		limit, limitPresent = 1, true
	}
	if plan.Operation() == readir.FindUnique {
		limit, limitPresent = 2, true
	}
	if limitPresent {
		args = append(args, int64(limit))
		text += " LIMIT " + dialect.Placeholder(len(args))
	}
	if skip, present := plan.Skip(); present {
		if !limitPresent && provider == policyir.ProviderSQLite {
			text += " LIMIT -1"
		}
		args = append(args, int64(skip))
		text += " OFFSET " + dialect.Placeholder(len(args))
	}
	text = prefix + text
	if err := enforceStatementParameterLimitWith(plan.ModelID(), args, plan.Limits().MaxStatementParameters); err != nil {
		return Statement{}, err
	}
	if err := ValidateStatementComplexity(plan.ModelID(), text, plan.Limits().MaxStatementBytes, plan.Limits().MaxStatementAliases); err != nil {
		return Statement{}, err
	}
	return Statement{text: text, args: cloneArgs(args), columns: columns, counts: countColumns, correlated: correlatedColumns, planMap: planMap.freeze(), reverse: reverse}, nil
}

type renderedRelationCount struct {
	expression string
	column     CountColumn
	args       []any
	planMap    PlanMap
}

func renderRelationCounts(plan readplan.Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof, dialect policysql.Dialect, rootAlias physical.PhysicalName, policyAliases *policysql.PolicyAliasAllocator, readAliases *readPlanAliasAllocator) ([]renderedRelationCount, error) {
	counts := plan.RelationCounts()
	result := make([]renderedRelationCount, len(counts))
	resolver := policysql.SchemaResolver(registry)
	for index, count := range counts {
		child := count.Child()
		if child.Operation() != readir.Count || child.ModelID() != count.TargetModelID() {
			return nil, fail(CodeInput, plan.ModelID(), count.FieldID(), "relation count has an invalid child plan", nil)
		}
		endpoint, ok := registry.RelationEndpoint(golem.ModelID(plan.ModelID()), golem.FieldID(count.FieldID()), golem.RelationID(count.RelationID()))
		if !ok || policyir.ModelID(endpoint.TargetModelID()) != count.TargetModelID() || endpoint.Cardinality() != compilerir.RelationMany {
			return nil, fail(CodeSchema, plan.ModelID(), count.FieldID(), "relation count endpoint is absent or not to-many", nil)
		}
		childModel, ok := resolver.Model(provider, child.ModelID())
		if !ok {
			return nil, fail(CodeSchema, plan.ModelID(), count.FieldID(), "relation count target model is absent", nil)
		}
		childAlias := physical.PhysicalName(readAliases.relationCount())
		var planMap planMapBuilder
		planMap.add(string(childAlias), child.ModelID(), count.RelationID(), nil, PlanAliasCorrelatedRelation)
		fragment, err := policysql.CompileWithPolicyAliasAllocator(policysql.Request{Condition: child.Where(), Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: childAlias}, policyAliases)
		if err != nil {
			return nil, fail(CodeRender, plan.ModelID(), count.FieldID(), "authorized relation-count predicate did not render", err)
		}
		planMap.mergePolicy(fragment.PolicyRelationAliases())
		correlations := make([]string, len(endpoint.Correlation()))
		for pairIndex, pair := range endpoint.Correlation() {
			parentField, parentOK := resolver.Field(provider, plan.ModelID(), policyir.FieldID(pair.ParentFieldID()))
			childField, childOK := resolver.Field(provider, child.ModelID(), policyir.FieldID(pair.ChildFieldID()))
			if !parentOK || !childOK {
				return nil, fail(CodeSchema, plan.ModelID(), count.FieldID(), "relation-count correlation field is absent", nil)
			}
			correlations[pairIndex] = dialect.Quote(childAlias) + "." + dialect.Quote(childField.Column) + " = " + dialect.Quote(rootAlias) + "." + dialect.Quote(parentField.Column)
		}
		if len(correlations) == 0 {
			return nil, fail(CodeSchema, plan.ModelID(), count.FieldID(), "relation count has no correlation", nil)
		}
		alias := fmt.Sprintf("golem_count%d", index)
		result[index] = renderedRelationCount{
			expression: "(SELECT COUNT(*) FROM " + dialect.Table(childModel) + " AS " + dialect.Quote(childAlias) + " WHERE (" + fragment.SQL() + ") AND (" + strings.Join(correlations, " AND ") + ")) AS " + dialect.Quote(physical.PhysicalName(alias)),
			column:     CountColumn{relation: count.RelationID(), alias: alias, occurrence: count.OccurrenceID()},
			args:       fragment.Args(),
			planMap:    planMap.freeze(),
		}
	}
	return result, nil
}

func renderCursor(plan readplan.Plan, cursor readir.Cursor, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof, dialect policysql.Dialect, model policysql.Model, rootAlias physical.PhysicalName, physicalColumns map[policyir.FieldID]string, orders []readir.Order, reverse bool, relation policyir.RelationID, role PlanAliasRole, policyAliases *policysql.PolicyAliasAllocator) (string, string, string, []any, PlanMap, error) {
	if len(orders) == 0 {
		return "", "", "", nil, PlanMap{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "cursor requires a deterministic order", nil)
	}
	condition, err := policyir.NewLogical(plan.ModelID(), policyir.LogicalAnd, []policyir.Condition{plan.CursorAnchor(), cursor.Predicate()})
	if err != nil {
		return "", "", "", nil, PlanMap{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "cursor lookup predicate is invalid", err)
	}
	condition, err = normalize.Condition(condition)
	if err != nil {
		return "", "", "", nil, PlanMap{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "cursor lookup predicate did not normalize", err)
	}
	resolver := policysql.SchemaResolver(registry)
	cursorTableAlias := physical.PhysicalName("golem_cp0")
	var planMap planMapBuilder
	planMap.add(string(cursorTableAlias), plan.ModelID(), relation, orderFieldIDs(orders), role)
	fragment, err := policysql.CompileWithPolicyAliasAllocator(policysql.Request{Condition: condition, Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: cursorTableAlias}, policyAliases)
	if err != nil {
		return "", "", "", nil, PlanMap{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "authorized cursor lookup did not render", err)
	}
	planMap.mergePolicy(fragment.PolicyRelationAliases())
	cteName := physical.PhysicalName("golem_cursor0")
	selects := make([]string, len(orders))
	cursorColumns := make([]string, len(orders))
	for index, order := range orders {
		resolved, ok := resolver.Field(provider, plan.ModelID(), order.FieldID())
		if !ok {
			return "", "", "", nil, PlanMap{}, fail(CodeSchema, plan.ModelID(), order.FieldID(), "cursor order field is absent", nil)
		}
		alias := physical.PhysicalName(fmt.Sprintf("golem_o%d", index))
		selects[index] = dialect.Quote(cursorTableAlias) + "." + dialect.Quote(resolved.Column) + " AS " + dialect.Quote(alias)
		cursorColumns[index] = dialect.Quote(cteName) + "." + dialect.Quote(alias)
	}
	prefix := "WITH " + dialect.Quote(cteName) + " AS (SELECT " + strings.Join(selects, ", ") + " FROM " + dialect.Table(model) + " AS " + dialect.Quote(cursorTableAlias) + " WHERE " + fragment.SQL() + " LIMIT 1) "
	join := " CROSS JOIN " + dialect.Quote(cteName)
	boundary := cursorBoundary(orders, physicalColumns, cursorColumns, provider, logicalTypesByField(plan.Fields()), reverse)
	if boundary == "" {
		return "", "", "", nil, PlanMap{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "cursor boundary is empty", nil)
	}
	_ = rootAlias
	return prefix, join, boundary, fragment.Args(), planMap.freeze(), nil
}

func orderFieldIDs(orders []readir.Order) []policyir.FieldID {
	fields := make([]policyir.FieldID, len(orders))
	for index, order := range orders {
		fields[index] = order.FieldID()
	}
	return fields
}

func cursorBoundary(orders []readir.Order, roots map[policyir.FieldID]string, cursors []string, provider policyir.Provider, logicalTypes map[policyir.FieldID]compilerir.LogicalTypeIR, reverse bool) string {
	terms := make([]string, 0, len(orders)+1)
	equalPrefix := make([]string, 0, len(orders))
	for index, order := range orders {
		rawRoot, rawCursor := roots[order.FieldID()], cursors[index]
		root := portableOrderOperand(provider, logicalTypes[order.FieldID()], rawRoot)
		cursor := portableOrderOperand(provider, logicalTypes[order.FieldID()], rawCursor)
		if rawRoot == "" || rawCursor == "" {
			return ""
		}
		direction := order.Direction()
		if reverse {
			if direction == readir.Ascending {
				direction = readir.Descending
			} else {
				direction = readir.Ascending
			}
		}
		equal := "((" + root + " IS NULL AND " + cursor + " IS NULL) OR " + root + " = " + cursor + ")"
		greater := ""
		if direction == readir.Ascending {
			// Ascending uses NULLS FIRST, matching renderOrders.
			greater = "((" + cursor + " IS NULL AND " + root + " IS NOT NULL) OR (" + cursor + " IS NOT NULL AND " + root + " IS NOT NULL AND " + root + " > " + cursor + "))"
		} else {
			// Descending uses NULLS LAST, matching renderOrders.
			greater = "(" + cursor + " IS NOT NULL AND (" + root + " IS NULL OR (" + root + " IS NOT NULL AND " + root + " < " + cursor + ")))"
		}
		parts := append(append([]string(nil), equalPrefix...), greater)
		terms = append(terms, "("+strings.Join(parts, " AND ")+")")
		equalPrefix = append(equalPrefix, equal)
	}
	// Cursor semantics are inclusive. Callers use Skip(1) to exclude the
	// cursor row, matching the TypeScript/Prisma contract.
	terms = append(terms, "("+strings.Join(equalPrefix, " AND ")+")")
	return strings.Join(terms, " OR ")
}

func renderOrders(orders []readir.Order, fields []readplan.Field, columns map[policyir.FieldID]string, dialect policysql.Dialect, provider policyir.Provider, reverse bool) (string, string, error) {
	aliases := make(map[policyir.FieldID]string, len(fields))
	logicalTypes := make(map[policyir.FieldID]compilerir.LogicalTypeIR, len(fields))
	for index, field := range fields {
		aliases[field.FieldID()] = fmt.Sprintf("golem_c%d", index)
		logicalTypes[field.FieldID()] = field.LogicalType()
	}
	rootParts, outerParts := make([]string, 0, len(orders)*2), make([]string, 0, len(orders)*2)
	for _, order := range orders {
		root := columns[order.FieldID()]
		alias := aliases[order.FieldID()]
		if root == "" || alias == "" {
			return "", "", fmt.Errorf("order field %x has no selected physical column", order.FieldID())
		}
		direction := order.Direction()
		if reverse {
			if direction == readir.Ascending {
				direction = readir.Descending
			} else {
				direction = readir.Ascending
			}
		}
		word := "ASC"
		nullRoot, nullOuter := root+" IS NOT NULL", dialect.Quote("golem_d0")+"."+dialect.Quote(physical.PhysicalName(alias))+" IS NOT NULL"
		if direction == readir.Descending {
			word = "DESC"
			nullRoot, nullOuter = root+" IS NULL", dialect.Quote("golem_d0")+"."+dialect.Quote(physical.PhysicalName(alias))+" IS NULL"
		}
		outer := dialect.Quote("golem_d0") + "." + dialect.Quote(physical.PhysicalName(alias))
		orderedRoot := portableOrderOperand(provider, logicalTypes[order.FieldID()], root)
		orderedOuter := portableOrderOperand(provider, logicalTypes[order.FieldID()], outer)
		rootParts = append(rootParts, nullRoot+" ASC", orderedRoot+" "+word)
		outerParts = append(outerParts, nullOuter+" ASC", orderedOuter+" "+word)
	}
	return strings.Join(rootParts, ", "), strings.Join(outerParts, ", "), nil
}

func logicalTypesByField(fields []readplan.Field) map[policyir.FieldID]compilerir.LogicalTypeIR {
	result := make(map[policyir.FieldID]compilerir.LogicalTypeIR, len(fields))
	for _, field := range fields {
		result[field.FieldID()] = field.LogicalType()
	}
	return result
}

// portableOrderOperand fences provider/session defaults so identical logical
// values have one ordering on SQLite and every supported PostgreSQL locale.
// PostgreSQL enum ordering is declaration order, so enums are first lowered to
// their stable wire text before the binary collation is applied.
func portableOrderOperand(provider policyir.Provider, typ compilerir.LogicalTypeIR, expression string) string {
	if expression == "" {
		return ""
	}
	switch provider {
	case policyir.ProviderPostgreSQL:
		switch typ.Kind {
		case compilerir.TypeString:
			return "(" + expression + " COLLATE \"C\")"
		case compilerir.TypeEnum:
			return "(CAST(" + expression + " AS TEXT) COLLATE \"C\")"
		}
	case policyir.ProviderSQLite:
		if typ.Kind == compilerir.TypeString || typ.Kind == compilerir.TypeEnum {
			return "(" + expression + " COLLATE BINARY)"
		}
	}
	return expression
}

func providerDialect(provider policyir.Provider) (policysql.Dialect, error) {
	switch provider {
	case policyir.ProviderSQLite:
		return sqliteprovider.NewPolicyDialect(), nil
	case policyir.ProviderPostgreSQL:
		return postgresprovider.NewPolicyDialect(), nil
	default:
		return nil, fmt.Errorf("unknown provider %d", provider)
	}
}
func cloneArgs(values []any) []any {
	result := make([]any, len(values))
	for index, value := range values {
		if bytes, ok := value.([]byte); ok {
			result[index] = append([]byte(nil), bytes...)
		} else {
			result[index] = value
		}
	}
	return result
}
func fail(code ErrorCode, model policyir.ModelID, field policyir.FieldID, detail string, cause error) error {
	return &Error{Code: code, Model: model, Field: field, Detail: detail, Cause: cause}
}
