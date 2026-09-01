package sql

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

// BatchChunkKeys is the maximum number of distinct parent correlation tuples
// accepted by one batch statement. MaxBatchParameters additionally protects
// old SQLite builds whose variable ceiling is 999.
const (
	BatchChunkKeys = 900
)

// BatchStatement is one bounded, row-shaped relation loader statement. It uses
// the ordinary logical decoder; correlation fields are private fields already
// injected into the child plan by the planner.
type BatchStatement struct {
	statement Statement
	keys      []policyir.FieldID
	extraKeys []policyir.FieldID
}

func (statement BatchStatement) SQL() string          { return statement.statement.SQL() }
func (statement BatchStatement) Args() []any          { return statement.statement.Args() }
func (statement BatchStatement) PlanMap() PlanMap     { return statement.statement.PlanMap() }
func (statement BatchStatement) ReverseBuckets() bool { return statement.statement.ReverseResult() }
func (statement BatchStatement) CorrelationFields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), statement.keys...)
}
func (statement BatchStatement) ExtraCorrelationFields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), statement.extraKeys...)
}

type batchContext struct {
	dialect      policysql.Dialect
	resolver     policysql.Resolver
	model        policysql.Model
	alias        physical.PhysicalName
	fields       []readplan.Field
	physical     map[policyir.FieldID]string
	keyFields    []policysql.Field
	keyIDs       []policyir.FieldID
	whereSQL     string
	whereArgs    []any
	counts       []renderedRelationCount
	countArgs    []any
	prefix       string
	from         string
	cursorArgs   []any
	orderSQL     string
	reverse      bool
	provider     policyir.Provider
	registry     *schema.Registry
	capabilities policysql.CapabilityProof
	planMap      PlanMap
}

// BatchKeyCapacity returns the exact per-statement tuple capacity after
// reserving binds used by the target policy, caller predicate, and cursor.
func BatchKeyCapacity(plan readplan.Plan, endpoint schema.RelationEndpoint, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof) (int, error) {
	context, err := prepareBatchContext(plan, endpoint, registry, provider, capabilities)
	if err != nil {
		return 0, err
	}
	width := len(context.keyFields)
	available := plan.Limits().MaxStatementParameters - len(context.cursorArgs) - len(context.countArgs) - len(context.whereArgs)
	if available < width {
		return 0, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "authorized batch predicate leaves no correlation-key parameter capacity", nil)
	}
	capacity := available / width
	if capacity > BatchChunkKeys {
		capacity = BatchChunkKeys
	}
	return capacity, nil
}

// RenderBatch renders one target-policy-scoped relation page for a
// chunk of distinct, non-null canonical parent keys. Pagination is performed
// by ROW_NUMBER partitioned by correlation key, so take/skip are per parent.
func RenderBatch(plan readplan.Plan, endpoint schema.RelationEndpoint, keys [][]policyir.Value, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof) (BatchStatement, error) {
	context, err := prepareBatchContext(plan, endpoint, registry, provider, capabilities)
	if err != nil {
		return BatchStatement{}, err
	}
	capacity, err := BatchKeyCapacity(plan, endpoint, registry, provider, capabilities)
	if err != nil {
		return BatchStatement{}, err
	}
	if len(keys) == 0 || len(keys) > capacity || len(keys) > BatchChunkKeys {
		return BatchStatement{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "batch correlation-key chunk is empty or exceeds its hard bound", nil)
	}

	args := append([]any(nil), context.cursorArgs...)
	keyPredicates := make([]string, len(keys))
	cursorKeyPredicates := make([]string, len(keys))
	for tupleIndex, tuple := range keys {
		if len(tuple) != len(context.keyFields) {
			return BatchStatement{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "batch correlation tuple width does not match the relation", nil)
		}
		parts := make([]string, len(tuple))
		cursorParts := make([]string, len(tuple))
		for fieldIndex, value := range tuple {
			resolved := context.keyFields[fieldIndex]
			encoded, encodeErr := encodeBatchValue(context.dialect, context.resolver, resolved.Type, value)
			if encodeErr != nil {
				return BatchStatement{}, fail(CodeRender, plan.ModelID(), resolved.ID, "batch correlation value did not encode", encodeErr)
			}
			args = append(args, encoded)
			placeholder := context.dialect.Placeholder(len(args))
			parts[fieldIndex] = context.dialect.Quote(context.alias) + "." + context.dialect.Quote(resolved.Column) + " = " + placeholder
			cursorParts[fieldIndex] = context.dialect.Quote("golem_cp0") + "." + context.dialect.Quote(resolved.Column) + " = " + placeholder
		}
		keyPredicates[tupleIndex] = "(" + strings.Join(parts, " AND ") + ")"
		cursorKeyPredicates[tupleIndex] = "(" + strings.Join(cursorParts, " AND ") + ")"
	}
	countExpressions := make([]string, len(context.counts))
	countColumns := make([]CountColumn, len(context.counts))
	for index, count := range context.counts {
		countExpressions[index] = policysql.RebasePlaceholders(count.expression, len(args), provider)
		args = append(args, count.args...)
		countColumns[index] = count.column
	}
	whereSQL := policysql.RebasePlaceholders(context.whereSQL, len(args), provider)
	args = append(args, context.whereArgs...)
	if err := enforceStatementParameterLimitWith(plan.ModelID(), args, plan.Limits().MaxStatementParameters); err != nil {
		return BatchStatement{}, err
	}

	selects := make([]string, len(context.fields), len(context.fields)+len(context.keyIDs))
	fieldAliases := make(map[policyir.FieldID]string, len(context.fields))
	for index, field := range context.fields {
		resolved, ok := context.resolver.Field(provider, plan.ModelID(), field.FieldID())
		if !ok {
			return BatchStatement{}, fail(CodeSchema, plan.ModelID(), field.FieldID(), "batch projection field is absent", nil)
		}
		alias := fmt.Sprintf("golem_c%d", index)
		fieldAliases[field.FieldID()] = alias
		selects[index] = context.dialect.Quote(context.alias) + "." + context.dialect.Quote(resolved.Column) + " AS " + context.dialect.Quote(physical.PhysicalName(alias))
	}
	selects = append(selects, countExpressions...)
	extraKeys := make([]policyir.FieldID, 0, len(context.keyIDs))
	for index, field := range context.keyIDs {
		if fieldAliases[field] != "" {
			continue
		}
		resolved := context.keyFields[index]
		alias := fmt.Sprintf("golem_k%d", index)
		fieldAliases[field] = alias
		extraKeys = append(extraKeys, field)
		selects = append(selects, context.dialect.Quote(context.alias)+"."+context.dialect.Quote(resolved.Column)+" AS "+context.dialect.Quote(physical.PhysicalName(alias)))
	}
	baseWhere := "(" + whereSQL + ") AND (" + strings.Join(keyPredicates, " OR ") + ")"
	prefix := context.prefix
	if prefix != "" {
		const suffix = " LIMIT 1) "
		if !strings.HasSuffix(prefix, suffix) {
			return BatchStatement{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "batch cursor anchor has an invalid statement shape", nil)
		}
		// The anchor is policy/field-guard scoped by renderCursor. Add only
		// these trusted loader keys; caller where remains exclusively in the
		// outer row predicate and cannot make a valid cursor disappear.
		prefix = strings.TrimSuffix(prefix, suffix) + " AND (" + strings.Join(cursorKeyPredicates, " OR ") + ")" + suffix
		// A single chunk can contain many parents but the cursor belongs to one
		// correlation tuple. Project that tuple from the anchor and require the
		// outer child to match it; otherwise the cursor boundary from parent A
		// would incorrectly page parents B..N in the same chunk.
		fromIndex := strings.Index(prefix, " FROM ")
		if fromIndex < 0 {
			return BatchStatement{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "batch cursor anchor has no FROM boundary", nil)
		}
		cursorKeySelects := make([]string, len(context.keyFields))
		cursorOwnership := make([]string, len(context.keyFields))
		for index, field := range context.keyFields {
			alias := physical.PhysicalName(fmt.Sprintf("golem_cursor_key%d", index))
			cursorKeySelects[index] = context.dialect.Quote("golem_cp0") + "." + context.dialect.Quote(field.Column) + " AS " + context.dialect.Quote(alias)
			cursorOwnership[index] = context.dialect.Quote(context.alias) + "." + context.dialect.Quote(field.Column) + " = " + context.dialect.Quote("golem_cursor0") + "." + context.dialect.Quote(alias)
		}
		prefix = prefix[:fromIndex] + ", " + strings.Join(cursorKeySelects, ", ") + prefix[fromIndex:]
		baseWhere += " AND (" + strings.Join(cursorOwnership, " AND ") + ")"
	}
	base := "SELECT " + strings.Join(selects, ", ") + " FROM " + context.from + " WHERE " + baseWhere

	keyAliases := make([]string, len(context.keyIDs))
	keyTypes := make(map[policyir.FieldID]compilerir.LogicalTypeIR, len(context.keyIDs))
	for index, field := range context.keyIDs {
		alias := fieldAliases[field]
		if alias == "" {
			return BatchStatement{}, fail(CodeSchema, plan.ModelID(), field, "batch correlation key is absent from the private projection", nil)
		}
		keyAliases[index] = context.dialect.Quote(physical.PhysicalName(alias))
		fact, ok := registry.Field(golem.ModelID(plan.ModelID()), golem.FieldID(field))
		if !ok {
			return BatchStatement{}, fail(CodeSchema, plan.ModelID(), field, "batch correlation key logical type is absent", nil)
		}
		keyTypes[field] = fact.LogicalType()
	}
	logicalTypes := logicalTypesByField(context.fields)
	orderAliases, err := batchOrderAliases(plan.OrderBy(), fieldAliases, logicalTypes, context.dialect, provider, context.reverse, "golem_b0")
	if err != nil {
		return BatchStatement{}, fail(CodeSchema, plan.ModelID(), policyir.FieldID{}, "batch order did not render", err)
	}
	baseAlias := physical.PhysicalName("golem_b0")
	pageInput := base
	pageAlias := baseAlias
	if distinct := plan.Distinct(); len(distinct) != 0 {
		distinctParts := make([]string, 0, len(keyAliases)+len(distinct))
		for index, key := range keyAliases {
			expression := context.dialect.Quote(baseAlias) + "." + key
			distinctParts = append(distinctParts, portableOrderOperand(provider, keyTypes[context.keyIDs[index]], expression))
		}
		for _, field := range distinct {
			alias := fieldAliases[field]
			if alias == "" {
				return BatchStatement{}, fail(CodeSchema, plan.ModelID(), field, "batch distinct field is absent from projection", nil)
			}
			expression := context.dialect.Quote(baseAlias) + "." + context.dialect.Quote(physical.PhysicalName(alias))
			distinctParts = append(distinctParts, portableOrderOperand(provider, logicalTypes[field], expression))
		}
		distinctOrder, orderErr := batchOrderAliases(plan.OrderBy(), fieldAliases, logicalTypes, context.dialect, provider, context.reverse, string(baseAlias))
		if orderErr != nil {
			return BatchStatement{}, orderErr
		}
		rankedAlias := physical.PhysicalName("golem_bd0")
		pageInput = "SELECT * FROM (SELECT " + context.dialect.Quote(baseAlias) + ".*, ROW_NUMBER() OVER (PARTITION BY " + strings.Join(distinctParts, ", ") + " ORDER BY " + strings.Join(distinctOrder, ", ") + ") AS " + context.dialect.Quote("golem_distinct_rank") + " FROM (" + base + ") AS " + context.dialect.Quote(baseAlias) + ") AS " + context.dialect.Quote(rankedAlias) + " WHERE " + context.dialect.Quote(rankedAlias) + "." + context.dialect.Quote("golem_distinct_rank") + " = 1"
		pageAlias = physical.PhysicalName("golem_b1")
		orderAliases, err = batchOrderAliases(plan.OrderBy(), fieldAliases, logicalTypes, context.dialect, provider, context.reverse, string(pageAlias))
		if err != nil {
			return BatchStatement{}, err
		}
	}

	partition := make([]string, len(keyAliases))
	for index, key := range keyAliases {
		expression := context.dialect.Quote(pageAlias) + "." + key
		partition[index] = portableOrderOperand(provider, keyTypes[context.keyIDs[index]], expression)
	}
	rankAlias := physical.PhysicalName("golem_bp0")
	page := "SELECT " + context.dialect.Quote(pageAlias) + ".*, ROW_NUMBER() OVER (PARTITION BY " + strings.Join(partition, ", ") + " ORDER BY " + strings.Join(orderAliases, ", ") + ") AS " + context.dialect.Quote("golem_page_rank") + " FROM (" + pageInput + ") AS " + context.dialect.Quote(pageAlias)
	outerSelect := make([]string, 0, len(context.fields)+len(countColumns)+len(extraKeys))
	for index := range context.fields {
		alias := context.dialect.Quote(physical.PhysicalName(fmt.Sprintf("golem_c%d", index)))
		outerSelect = append(outerSelect, context.dialect.Quote(rankAlias)+"."+alias)
	}
	for _, count := range countColumns {
		alias := context.dialect.Quote(physical.PhysicalName(count.alias))
		outerSelect = append(outerSelect, context.dialect.Quote(rankAlias)+"."+alias)
	}
	for _, field := range extraKeys {
		alias := context.dialect.Quote(physical.PhysicalName(fieldAliases[field]))
		outerSelect = append(outerSelect, context.dialect.Quote(rankAlias)+"."+alias)
	}
	text := "SELECT " + strings.Join(outerSelect, ", ") + " FROM (" + page + ") AS " + context.dialect.Quote(rankAlias)
	filters := make([]string, 0, 2)
	skip := 0
	if value, ok := plan.Skip(); ok {
		skip = value
	}
	if skip > 0 {
		filters = append(filters, context.dialect.Quote(rankAlias)+"."+context.dialect.Quote("golem_page_rank")+" > "+fmt.Sprintf("%d", skip))
	}
	if take, ok := plan.Take(); ok {
		if take < 0 {
			take = -take
		}
		filters = append(filters, context.dialect.Quote(rankAlias)+"."+context.dialect.Quote("golem_page_rank")+" <= "+fmt.Sprintf("%d", skip+take))
	}
	if len(filters) != 0 {
		text += " WHERE " + strings.Join(filters, " AND ")
	}
	orderFinal := append([]string(nil), partition...)
	orderFinal = append(orderFinal, context.dialect.Quote(rankAlias)+"."+context.dialect.Quote("golem_page_rank")+" ASC")
	// partition was rendered against pageAlias; final ordering must reference
	// the outer ranked alias instead.
	for index, field := range context.keyIDs {
		expression := context.dialect.Quote(rankAlias) + "." + context.dialect.Quote(physical.PhysicalName(fieldAliases[field]))
		orderFinal[index] = portableOrderOperand(provider, keyTypes[field], expression) + " ASC"
	}
	text += " ORDER BY " + strings.Join(orderFinal, ", ")
	text = prefix + text
	if err := ValidateStatementComplexity(plan.ModelID(), text, plan.Limits().MaxStatementBytes, plan.Limits().MaxStatementAliases); err != nil {
		return BatchStatement{}, err
	}
	columns := make([]Column, len(context.fields))
	for index, field := range context.fields {
		columns[index] = Column{field: field.FieldID(), alias: fmt.Sprintf("golem_c%d", index), public: field.Public()}
	}
	return BatchStatement{statement: Statement{text: text, args: args, columns: columns, counts: countColumns, planMap: context.planMap.clone(), reverse: context.reverse}, keys: context.keyIDs, extraKeys: extraKeys}, nil
}

func prepareBatchContext(plan readplan.Plan, endpoint schema.RelationEndpoint, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof) (batchContext, error) {
	if registry == nil || plan.ModelID() == (policyir.ModelID{}) || endpoint.TargetModelID() != golem.ModelID(plan.ModelID()) || (endpoint.Cardinality() != compilerir.RelationOne && endpoint.Cardinality() != compilerir.RelationMany) {
		return batchContext{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "batch plan requires a matching relation endpoint", nil)
	}
	if plan.Operation() != readir.FindMany {
		return batchContext{}, fail(CodeInput, plan.ModelID(), policyir.FieldID{}, "batch relation child must be findMany", nil)
	}
	dialect, err := providerDialect(provider)
	if err != nil {
		return batchContext{}, err
	}
	resolver := policysql.SchemaResolver(registry)
	model, ok := resolver.Model(provider, plan.ModelID())
	if !ok {
		return batchContext{}, fail(CodeSchema, plan.ModelID(), policyir.FieldID{}, "batch target model is absent", nil)
	}
	alias := physical.PhysicalName("golem_br0")
	var planMap planMapBuilder
	policyAliases := policysql.NewPolicyAliasAllocator()
	readAliases := &readPlanAliasAllocator{}
	planMap.add(string(alias), plan.ModelID(), policyir.RelationID(endpoint.RelationID()), endpointChildFieldIDs(endpoint), PlanAliasPhysicalAccess)
	fragment, err := policysql.CompileWithPolicyAliasAllocator(policysql.Request{Condition: plan.Where(), Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: alias}, policyAliases)
	if err != nil {
		return batchContext{}, fail(CodeRender, plan.ModelID(), policyir.FieldID{}, "authorized batch predicate did not render", err)
	}
	planMap.mergePolicy(fragment.PolicyRelationAliases())
	renderedCounts, err := renderRelationCounts(plan, registry, provider, capabilities, dialect, alias, policyAliases, readAliases)
	if err != nil {
		return batchContext{}, err
	}
	for _, count := range renderedCounts {
		planMap.merge(count.planMap)
	}
	countArgs := make([]any, 0)
	for _, count := range renderedCounts {
		countArgs = append(countArgs, count.args...)
	}
	fields := plan.Fields()
	physicalColumns := make(map[policyir.FieldID]string, len(fields))
	for _, field := range fields {
		resolved, found := resolver.Field(provider, plan.ModelID(), field.FieldID())
		if !found {
			return batchContext{}, fail(CodeSchema, plan.ModelID(), field.FieldID(), "batch field is absent", nil)
		}
		physicalColumns[field.FieldID()] = dialect.Quote(alias) + "." + dialect.Quote(resolved.Column)
	}
	reverse := false
	if take, present := plan.Take(); present && take < 0 {
		reverse = true
	}
	rootOrder, _, err := renderOrders(plan.OrderBy(), fields, physicalColumns, dialect, provider, reverse)
	if err != nil || rootOrder == "" {
		return batchContext{}, fail(CodeSchema, plan.ModelID(), policyir.FieldID{}, "batch relation requires deterministic order", err)
	}
	prefix := ""
	from := dialect.Table(model) + " AS " + dialect.Quote(alias)
	whereSQL := fragment.SQL()
	var cursorArgs []any
	if cursor, present := plan.Cursor(); present {
		cursorPrefix, cursorJoin, boundary, values, cursorPlanMap, cursorErr := renderCursor(plan, cursor, registry, provider, capabilities, dialect, model, alias, physicalColumns, plan.OrderBy(), reverse, policyir.RelationID(endpoint.RelationID()), PlanAliasPhysicalAccess, policyAliases)
		if cursorErr != nil {
			return batchContext{}, cursorErr
		}
		prefix, cursorArgs = cursorPrefix, values
		planMap.merge(cursorPlanMap)
		from += cursorJoin
		whereSQL = "(" + whereSQL + ") AND (" + boundary + ")"
	}
	pairs := endpoint.Correlation()
	if len(pairs) == 0 {
		return batchContext{}, fail(CodeSchema, plan.ModelID(), policyir.FieldID{}, "batch relation has no correlation", nil)
	}
	keyFields := make([]policysql.Field, len(pairs))
	keyIDs := make([]policyir.FieldID, len(pairs))
	for index, pair := range pairs {
		id := policyir.FieldID(pair.ChildFieldID())
		resolved, found := resolver.Field(provider, plan.ModelID(), id)
		if !found {
			return batchContext{}, fail(CodeSchema, plan.ModelID(), id, "batch child correlation field is absent", nil)
		}
		keyFields[index], keyIDs[index] = resolved, id
	}
	return batchContext{dialect: dialect, resolver: resolver, model: model, alias: alias, fields: fields, physical: physicalColumns, keyFields: keyFields, keyIDs: keyIDs, whereSQL: whereSQL, whereArgs: fragment.Args(), counts: renderedCounts, countArgs: countArgs, prefix: prefix, from: from, cursorArgs: cursorArgs, orderSQL: rootOrder, reverse: reverse, provider: provider, registry: registry, capabilities: capabilities, planMap: planMap.freeze()}, nil
}

func endpointChildFieldIDs(endpoint schema.RelationEndpoint) []policyir.FieldID {
	pairs := endpoint.Correlation()
	fields := make([]policyir.FieldID, len(pairs))
	for index, pair := range pairs {
		fields[index] = policyir.FieldID(pair.ChildFieldID())
	}
	return fields
}

func encodeBatchValue(dialect policysql.Dialect, resolver policysql.Resolver, typ policyir.TypeRef, value policyir.Value) (any, error) {
	bound := policysql.BoundValue{Value: value, Type: typ}
	if typ.Kind() == policyir.ValueEnum {
		enum, member, ok := value.Enum()
		if !ok {
			return nil, fmt.Errorf("enum correlation value is invalid")
		}
		wire, ok := resolver.EnumWire(enum, member)
		if !ok {
			return nil, fmt.Errorf("enum correlation wire value is absent")
		}
		bound.EnumWires = []string{wire}
	}
	return dialect.Encode(bound)
}

func batchOrderAliases(orders []readir.Order, aliases map[policyir.FieldID]string, logicalTypes map[policyir.FieldID]compilerir.LogicalTypeIR, dialect policysql.Dialect, provider policyir.Provider, reverse bool, owner string) ([]string, error) {
	result := make([]string, 0, len(orders)*2)
	for _, order := range orders {
		alias := aliases[order.FieldID()]
		if alias == "" {
			return nil, fmt.Errorf("order field %x is absent", order.FieldID())
		}
		column := dialect.Quote(physical.PhysicalName(owner)) + "." + dialect.Quote(physical.PhysicalName(alias))
		direction := order.Direction()
		if reverse {
			if direction == readir.Ascending {
				direction = readir.Descending
			} else {
				direction = readir.Ascending
			}
		}
		word, null := "ASC", column+" IS NOT NULL ASC"
		if direction == readir.Descending {
			word, null = "DESC", column+" IS NULL ASC"
		}
		result = append(result, null, portableOrderOperand(provider, logicalTypes[order.FieldID()], column)+" "+word)
	}
	return result, nil
}
