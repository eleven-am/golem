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
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type RelationStrategy uint8

const (
	RelationBatch RelationStrategy = iota + 1
	RelationCorrelated
)

// ChooseRelationStrategy is the single deterministic production chooser. A
// true correlated JSON statement is selected for a single-column to-one, or
// for an equality-indexed, single-column, plain-scalar to-many correlation.
// Every other shape uses the bounded row batch.
func ChooseRelationStrategy(parent readplan.Plan, relation readplan.Relation, registry *schema.Registry, provider policyir.Provider) RelationStrategy {
	if registry == nil || (provider != policyir.ProviderSQLite && provider != policyir.ProviderPostgreSQL) {
		return RelationBatch
	}
	endpoint, ok := registry.RelationEndpoint(golem.ModelID(parent.ModelID()), golem.FieldID(relation.FieldID()), golem.RelationID(relation.RelationID()))
	if !ok || len(endpoint.Correlation()) != 1 {
		return RelationBatch
	}
	if !relation.ToMany() {
		return RelationCorrelated
	}
	pair := endpoint.Correlation()[0]
	target, ok := registry.Model(endpoint.TargetModelID())
	if !ok || !target.EqualityIndexed(pair.ChildFieldID()) {
		return RelationBatch
	}
	field, ok := registry.Field(endpoint.TargetModelID(), pair.ChildFieldID())
	if !ok {
		return RelationBatch
	}
	if correlatedKeyKindAllowed(field.LogicalType().Kind) {
		return RelationCorrelated
	}
	return RelationBatch
}

func correlatedKeyKindAllowed(kind compilerir.LogicalTypeKind) bool {
	switch kind {
	case compilerir.TypeBool, compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64,
		compilerir.TypeFloat32, compilerir.TypeFloat64, compilerir.TypeDecimal, compilerir.TypeString,
		compilerir.TypeBytes, compilerir.TypeUUID, compilerir.TypeDate, compilerir.TypeTime,
		compilerir.TypeDateTime, compilerir.TypeEnum:
		return true
	default:
		return false
	}
}

type CorrelatedColumn struct {
	field    policyir.FieldID
	relation policyir.RelationID
	alias    string
}

func (column CorrelatedColumn) FieldID() policyir.FieldID       { return column.field }
func (column CorrelatedColumn) RelationID() policyir.RelationID { return column.relation }
func (column CorrelatedColumn) Alias() string                   { return column.alias }

type renderedCorrelated struct {
	expression string
	column     CorrelatedColumn
	args       []any
}

func renderCorrelatedRelations(parent readplan.Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof, dialect policysql.Dialect, parentAlias physical.PhysicalName) ([]renderedCorrelated, error) {
	relations := parent.Relations()
	result := make([]renderedCorrelated, 0, len(relations))
	for index, relation := range relations {
		if ChooseRelationStrategy(parent, relation, registry, provider) != RelationCorrelated {
			continue
		}
		rendered, err := renderCorrelatedRelation(parent, relation, index, registry, provider, capabilities, dialect, parentAlias)
		if err != nil {
			return nil, err
		}
		result = append(result, rendered)
	}
	return result, nil
}

func renderCorrelatedRelation(parent readplan.Plan, relation readplan.Relation, relationIndex int, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof, dialect policysql.Dialect, parentAlias physical.PhysicalName) (renderedCorrelated, error) {
	child := relation.Child()
	if child.Operation() != readir.FindMany || len(child.Fields()) == 0 {
		return renderedCorrelated{}, fail(CodeInput, parent.ModelID(), relation.FieldID(), "correlated to-many child is not a row-shaped findMany plan", nil)
	}
	endpoint, ok := registry.RelationEndpoint(golem.ModelID(parent.ModelID()), golem.FieldID(relation.FieldID()), golem.RelationID(relation.RelationID()))
	if !ok || len(endpoint.Correlation()) != 1 {
		return renderedCorrelated{}, fail(CodeSchema, parent.ModelID(), relation.FieldID(), "correlated relation endpoint is absent or unsupported", nil)
	}
	resolver := policysql.SchemaResolver(registry)
	model, ok := resolver.Model(provider, child.ModelID())
	if !ok {
		return renderedCorrelated{}, fail(CodeSchema, child.ModelID(), relation.FieldID(), "correlated child model is absent", nil)
	}
	baseAlias := physical.PhysicalName(fmt.Sprintf("golem_cr%d", relationIndex))
	whereFragment, err := policysql.Compile(policysql.Request{Condition: child.Where(), Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: baseAlias})
	if err != nil {
		return renderedCorrelated{}, fail(CodeRender, child.ModelID(), relation.FieldID(), "authorized correlated child predicate did not render", err)
	}

	fields := child.Fields()
	physicalColumns := make(map[policyir.FieldID]string, len(fields))
	innerSelects := make([]string, len(fields))
	fieldAliases := make(map[policyir.FieldID]string, len(fields))
	logicalTypes := logicalTypesByField(fields)
	for index, field := range fields {
		resolved, found := resolver.Field(provider, child.ModelID(), field.FieldID())
		if !found {
			return renderedCorrelated{}, fail(CodeSchema, child.ModelID(), field.FieldID(), "correlated child field is absent", nil)
		}
		alias := fmt.Sprintf("golem_c%d", index)
		fieldAliases[field.FieldID()] = alias
		column := dialect.Quote(baseAlias) + "." + dialect.Quote(resolved.Column)
		physicalColumns[field.FieldID()] = column
		innerSelects[index] = column + " AS " + dialect.Quote(physical.PhysicalName(alias))
	}

	counts, err := renderRelationCounts(child, registry, provider, capabilities, dialect, baseAlias)
	if err != nil {
		return renderedCorrelated{}, err
	}
	args := make([]any, 0)
	for _, count := range counts {
		innerSelects = append(innerSelects, rebasePlaceholders(count.expression, len(args), provider))
		args = append(args, count.args...)
	}
	whereSQL := rebasePlaceholders(whereFragment.SQL(), len(args), provider)
	args = append(args, whereFragment.Args()...)

	pair := endpoint.Correlation()[0]
	parentField, parentOK := resolver.Field(provider, parent.ModelID(), policyir.FieldID(pair.ParentFieldID()))
	childField, childOK := resolver.Field(provider, child.ModelID(), policyir.FieldID(pair.ChildFieldID()))
	if !parentOK || !childOK {
		return renderedCorrelated{}, fail(CodeSchema, parent.ModelID(), relation.FieldID(), "correlated relation key field is absent", nil)
	}
	correlation := dialect.Quote(baseAlias) + "." + dialect.Quote(childField.Column) + " = " + dialect.Quote(parentAlias) + "." + dialect.Quote(parentField.Column)
	whereSQL = "(" + whereSQL + ") AND (" + correlation + ")"

	reverse := false
	if take, present := child.Take(); present && take < 0 {
		reverse = true
	}
	rootOrder, _, err := renderOrders(child.OrderBy(), fields, physicalColumns, dialect, provider, reverse)
	if err != nil || rootOrder == "" {
		return renderedCorrelated{}, fail(CodeRender, child.ModelID(), relation.FieldID(), "correlated child order did not render", err)
	}
	if cursor, present := child.Cursor(); present {
		cursorWhere, cursorArgs, boundary, cursorErr := renderCorrelatedCursor(child, cursor, registry, provider, capabilities, dialect, model, baseAlias, parentAlias, parentField, childField, physicalColumns, reverse, len(args))
		if cursorErr != nil {
			return renderedCorrelated{}, cursorErr
		}
		args = append(args, cursorArgs...)
		whereSQL += " AND (" + cursorWhere + ") AND (" + boundary + ")"
	}

	inner := "SELECT " + strings.Join(innerSelects, ", ")
	if distinct := child.Distinct(); len(distinct) != 0 {
		partitions := make([]string, len(distinct))
		for index, field := range distinct {
			column := physicalColumns[field]
			if column == "" {
				return renderedCorrelated{}, fail(CodeSchema, child.ModelID(), field, "correlated distinct field is absent", nil)
			}
			partitions[index] = portableOrderOperand(provider, logicalTypes[field], column)
		}
		inner += ", ROW_NUMBER() OVER (PARTITION BY " + strings.Join(partitions, ", ") + " ORDER BY " + rootOrder + ") AS " + dialect.Quote("golem_distinct_rank")
	}
	inner += " FROM " + dialect.Table(model) + " AS " + dialect.Quote(baseAlias) + " WHERE " + whereSQL

	rowAlias := physical.PhysicalName(fmt.Sprintf("golem_cd%d", relationIndex))
	source := "(" + inner + ") AS " + dialect.Quote(rowAlias)
	if len(child.Distinct()) != 0 {
		filteredAlias := physical.PhysicalName(fmt.Sprintf("golem_cf%d", relationIndex))
		source = "(SELECT * FROM " + source + " WHERE " + dialect.Quote(rowAlias) + "." + dialect.Quote("golem_distinct_rank") + " = 1) AS " + dialect.Quote(filteredAlias)
		rowAlias = filteredAlias
	}

	payloadEntries := make([]string, 0, len(fields)*2+len(counts)*2)
	for index, field := range fields {
		column := dialect.Quote(rowAlias) + "." + dialect.Quote(physical.PhysicalName(fmt.Sprintf("golem_c%d", index)))
		payloadEntries = append(payloadEntries, quoteJSONKey(dialect, fmt.Sprintf("golem_c%d", index)), correlatedJSONValue(provider, field.LogicalType(), column))
	}
	for index, count := range counts {
		column := dialect.Quote(rowAlias) + "." + dialect.Quote(physical.PhysicalName(count.column.alias))
		payloadEntries = append(payloadEntries, quoteJSONKey(dialect, count.column.alias), correlatedJSONValue(provider, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, column))
		_ = index
	}
	outerOrders, err := batchOrderAliases(child.OrderBy(), fieldAliases, logicalTypes, dialect, provider, reverse, string(rowAlias))
	if err != nil {
		return renderedCorrelated{}, err
	}
	payload := ""
	if provider == policyir.ProviderPostgreSQL {
		payload = "json_build_object(" + strings.Join(payloadEntries, ", ") + ")"
	} else {
		payload = "json_object(" + strings.Join(payloadEntries, ", ") + ")"
	}
	ordinal := "ROW_NUMBER() OVER (ORDER BY " + strings.Join(outerOrders, ", ") + ")"
	page := "SELECT " + payload + " AS " + dialect.Quote("golem_payload") + ", " + ordinal + " AS " + dialect.Quote("golem_ordinal") + " FROM " + source + " ORDER BY " + strings.Join(outerOrders, ", ")
	pageTake, pageTakePresent := child.Take()
	if !relation.ToMany() && (!pageTakePresent || pageTake > 2 || pageTake < -2) {
		pageTake, pageTakePresent = 2, true
	}
	if pageTakePresent {
		take := pageTake
		if take < 0 {
			take = -take
		}
		args = append(args, int64(take))
		page += " LIMIT " + dialect.Placeholder(len(args))
	}
	if skip, present := child.Skip(); present {
		if !pageTakePresent && provider == policyir.ProviderSQLite {
			page += " LIMIT -1"
		}
		args = append(args, int64(skip))
		page += " OFFSET " + dialect.Placeholder(len(args))
	}
	aggregateAlias := physical.PhysicalName(fmt.Sprintf("golem_cg%d", relationIndex))
	var aggregate string
	if provider == policyir.ProviderPostgreSQL {
		aggregate = "(SELECT COALESCE(json_agg(" + dialect.Quote(aggregateAlias) + "." + dialect.Quote("golem_payload") + " ORDER BY " + dialect.Quote(aggregateAlias) + "." + dialect.Quote("golem_ordinal") + ")::text, '[]') FROM (" + page + ") AS " + dialect.Quote(aggregateAlias) + ")"
	} else {
		aggregate = "(SELECT COALESCE(json_group_array(json(" + dialect.Quote(aggregateAlias) + "." + dialect.Quote("golem_payload") + ")), '[]') FROM (" + page + ") AS " + dialect.Quote(aggregateAlias) + ")"
	}
	alias := fmt.Sprintf("golem_rel%d", relationIndex)
	return renderedCorrelated{expression: aggregate + " AS " + dialect.Quote(physical.PhysicalName(alias)), column: CorrelatedColumn{field: relation.FieldID(), relation: relation.RelationID(), alias: alias}, args: args}, nil
}

func renderCorrelatedCursor(plan readplan.Plan, cursor readir.Cursor, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof, dialect policysql.Dialect, model policysql.Model, childAlias, parentAlias physical.PhysicalName, parentField, childField policysql.Field, physicalColumns map[policyir.FieldID]string, reverse bool, offset int) (string, []any, string, error) {
	condition, err := policyir.NewLogical(plan.ModelID(), policyir.LogicalAnd, []policyir.Condition{plan.CursorAnchor(), cursor.Predicate()})
	if err != nil {
		return "", nil, "", err
	}
	condition, err = normalize.Condition(condition)
	if err != nil {
		return "", nil, "", err
	}
	resolver := policysql.SchemaResolver(registry)
	anchorAlias := physical.PhysicalName(string(childAlias) + "_cursor")
	fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: anchorAlias})
	if err != nil {
		return "", nil, "", err
	}
	fragmentSQL := rebasePlaceholders(fragment.SQL(), offset, provider)
	anchorCorrelation := dialect.Quote(anchorAlias) + "." + dialect.Quote(childField.Column) + " = " + dialect.Quote(parentAlias) + "." + dialect.Quote(parentField.Column)
	anchorWhere := "(" + fragmentSQL + ") AND (" + anchorCorrelation + ")"
	orders := plan.OrderBy()
	cursorColumns := make([]string, len(orders))
	for index, order := range orders {
		resolved, ok := resolver.Field(provider, plan.ModelID(), order.FieldID())
		if !ok {
			return "", nil, "", fmt.Errorf("correlated cursor order field is absent")
		}
		cursorColumns[index] = "(SELECT " + dialect.Quote(anchorAlias) + "." + dialect.Quote(resolved.Column) + " FROM " + dialect.Table(model) + " AS " + dialect.Quote(anchorAlias) + " WHERE " + anchorWhere + " LIMIT 1)"
	}
	boundary := cursorBoundary(orders, physicalColumns, cursorColumns, provider, logicalTypesByField(plan.Fields()), reverse)
	exists := "EXISTS (SELECT 1 FROM " + dialect.Table(model) + " AS " + dialect.Quote(anchorAlias) + " WHERE " + anchorWhere + ")"
	return exists, fragment.Args(), boundary, nil
}

func quoteJSONKey(dialect policysql.Dialect, value string) string {
	// Keys are compiler-owned ASCII aliases, so a SQL literal needs only the
	// standard single-quote escape even though no generated alias contains it.
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func correlatedJSONValue(provider policyir.Provider, typ compilerir.LogicalTypeIR, column string) string {
	if provider == policyir.ProviderPostgreSQL {
		switch typ.Kind {
		case compilerir.TypeBytes:
			return "CASE WHEN " + column + " IS NULL THEN NULL ELSE encode(" + column + ", 'hex') END"
		case compilerir.TypeDate:
			return "CASE WHEN " + column + " IS NULL THEN NULL ELSE to_char(" + column + ", 'YYYY-MM-DD') END"
		case compilerir.TypeTime:
			return "CASE WHEN " + column + " IS NULL THEN NULL ELSE to_char(" + column + ", 'HH24:MI:SS.US') END"
		case compilerir.TypeDateTime:
			return "CASE WHEN " + column + " IS NULL THEN NULL ELSE to_char(" + column + " AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') END"
		default:
			return "CASE WHEN " + column + " IS NULL THEN NULL ELSE CAST(" + column + " AS TEXT) END"
		}
	}
	switch typ.Kind {
	case compilerir.TypeBytes:
		return "CASE WHEN " + column + " IS NULL THEN NULL ELSE hex(" + column + ") END"
	case compilerir.TypeFloat32:
		return "CASE WHEN " + column + " IS NULL THEN NULL ELSE printf('%.9g', " + column + ") END"
	case compilerir.TypeFloat64:
		return "CASE WHEN " + column + " IS NULL THEN NULL ELSE printf('%.17g', " + column + ") END"
	default:
		return "CASE WHEN " + column + " IS NULL THEN NULL ELSE CAST(" + column + " AS TEXT) END"
	}
}
