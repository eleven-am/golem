package analytics

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type Column struct {
	Term     golem.FrozenAnalyticsTerm
	Alias    string
	Type     compilerir.LogicalTypeIR
	Nullable bool
}
type Statement struct {
	text                  string
	args                  []any
	columns               []Column
	grouped               bool
	maxContributionRows   int
	maxIntermediateGroups int
	maxResultRows         int
	guardColumns          bool
	guardRowColumn        bool
}

type RenderOptions struct {
	MaxContributionRows   int
	MaxIntermediateGroups int
	MaxResultRows         int
}

func (s Statement) SQL() string       { return s.text }
func (s Statement) Args() []any       { return append([]any(nil), s.args...) }
func (s Statement) Columns() []Column { return append([]Column(nil), s.columns...) }
func (s Statement) Grouped() bool     { return s.grouped }
func (s Statement) ScanColumnCount() int {
	count := len(s.columns)
	if s.guardColumns {
		count += 2
	}
	if s.guardRowColumn {
		count++
	}
	return count
}
func (s Statement) Guarded() bool { return s.guardColumns }
func (s Statement) LimitOverflow(contributionRows, intermediateGroups int64, resultRows int) string {
	if s.maxContributionRows > 0 && contributionRows > int64(s.maxContributionRows) {
		return "analytics contribution limit exceeded"
	}
	if s.maxIntermediateGroups > 0 && intermediateGroups > int64(s.maxIntermediateGroups) {
		return "analytics intermediate group limit exceeded"
	}
	if s.ResultOverflow(resultRows) {
		return "analytics result limit exceeded"
	}
	return ""
}
func (s Statement) ResultOverflow(rowCount int) bool {
	return s.maxResultRows > 0 && rowCount > s.maxResultRows
}

func Render(plan Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof, options ...RenderOptions) (Statement, error) {
	if len(options) > 1 || len(options) == 1 && (options[0].MaxContributionRows < 0 || options[0].MaxIntermediateGroups < 0 || options[0].MaxResultRows < 0) {
		return Statement{}, fmt.Errorf("P6_ANALYTICS_SQL_LIMIT: invalid render options")
	}
	maxResultRows := 0
	maxContributionRows := 0
	maxIntermediateGroups := 0
	if len(options) == 1 {
		maxContributionRows = options[0].MaxContributionRows
		maxIntermediateGroups = options[0].MaxIntermediateGroups
		maxResultRows = options[0].MaxResultRows
	}
	request := plan.request
	authorized := plan.read
	dialect, external, err := analyticsDialect(provider)
	if err != nil {
		return Statement{}, err
	}
	physicalModel, ok := resolverModel(registry, provider, request.ModelID())
	if !ok {
		return Statement{}, fmt.Errorf("P6_ANALYTICS_SQL_MODEL: physical model is absent")
	}
	rootAlias := physical.PhysicalName("golem_a0")
	resolver := policysql.SchemaResolver(registry)
	root, err := policysql.Compile(policysql.Request{Condition: authorized.Where(), Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: rootAlias})
	if err != nil {
		return Statement{}, fmt.Errorf("P6_ANALYTICS_SQL_POLICY: %w", err)
	}
	classifications := map[golem.FieldID]readplan.Field{}
	for _, field := range authorized.Fields() {
		classifications[golem.FieldID(field.FieldID())] = field
	}
	relationPath := plan.RelationPath()
	if len(relationPath) > 0 {
		if model, present := registry.Model(request.ModelID()); present {
			if contract, configured := model.Analytics(); configured && contract.RelationMaxIntermediateGroups > 0 && (maxIntermediateGroups == 0 || int(contract.RelationMaxIntermediateGroups) < maxIntermediateGroups) {
				maxIntermediateGroups = int(contract.RelationMaxIntermediateGroups)
			}
		}
	}
	relationAliases := make([]physical.PhysicalName, len(relationPath))
	relationClassifications := map[golem.FieldID]readplan.Field{}
	for index, hop := range relationPath {
		relationAliases[index] = physical.PhysicalName(fmt.Sprintf("golem_j%d", index))
		if index == len(relationPath)-1 {
			for _, field := range hop.Authorized.Fields() {
				relationClassifications[golem.FieldID(field.FieldID())] = field
			}
		}
	}
	args := []any{}
	useContributionCTE := maxContributionRows > 0
	contributionAlias := physical.PhysicalName("golem_contributions")
	contributionProjection := []string{}
	contributionProjectionNames := []string{}
	expressionCache := map[string]string{}
	expression := func(term golem.FrozenAnalyticsTerm) (string, error) {
		key := termKey(term)
		if value := expressionCache[key]; value != "" {
			return value, nil
		}
		if term.Operator == golem.AggregateCountAll {
			expressionCache[key] = "COUNT(*)"
			return "COUNT(*)", nil
		}
		field, exists := plan.Field(term.Field)
		if !exists {
			return "", fmt.Errorf("P6_ANALYTICS_SQL_FIELD: field is absent")
		}
		owner, alias, classified := request.ModelID(), rootAlias, classifications[term.Field]
		if term.RelationName != "" {
			if len(relationPath) == 0 {
				return "", fmt.Errorf("P6_ANALYTICS_SQL_RELATION: relation path is absent")
			}
			owner, alias, classified = relationPath[len(relationPath)-1].Endpoint.TargetModelID(), relationAliases[len(relationAliases)-1], relationClassifications[term.Field]
		}
		physicalField, exists := registry.PhysicalField(external, owner, term.Field)
		if !exists {
			return "", fmt.Errorf("P6_ANALYTICS_SQL_FIELD: physical field is absent")
		}
		column := dialect.Quote(alias) + "." + dialect.Quote(physicalField.ColumnName())
		if classified.Conditional() && !classified.DischargedByConstraint() {
			return "", fmt.Errorf("P6_ANALYTICS_SQL_AUTHORIZATION: conditional analytics field is not discharged")
		}
		if _, present := classified.Mask(); present && !classified.Conditional() {
			// Analytical values are all-or-nothing. A mask without a conditional
			// classification is an inconsistent planner result and must never be
			// lowered into a per-row CASE that would partially mask an aggregate.
			return "", fmt.Errorf("P6_ANALYTICS_SQL_AUTHORIZATION: analytical field has an invalid standalone mask")
		}
		if useContributionCTE {
			aliasName := physical.PhysicalName(fmt.Sprintf("golem_c%d", len(contributionProjection)))
			contributionProjection = append(contributionProjection, column+" AS "+dialect.Quote(aliasName))
			contributionProjectionNames = append(contributionProjectionNames, string(aliasName))
			column = dialect.Quote(contributionAlias) + "." + dialect.Quote(aliasName)
		}
		value := aggregateExpression(provider, term.Operator, column, field.LogicalType())
		expressionCache[key] = value
		return value, nil
	}
	dimensions := request.Dimensions()
	measures := request.Measures()
	selects := []string{}
	columns := []Column{}
	groups := []string{}
	publicAliases := map[string]string{}
	for index, term := range dimensions {
		expr, renderErr := expression(term)
		if renderErr != nil {
			return Statement{}, renderErr
		}
		alias := fmt.Sprintf("golem_d%d", index)
		selects = append(selects, expr+" AS "+dialect.Quote(physical.PhysicalName(alias)))
		groups = append(groups, expr)
		publicAliases[termKey(term)] = alias
		field, _ := plan.Field(term.Field)
		columns = append(columns, Column{Term: term, Alias: alias, Type: field.LogicalType(), Nullable: true})
	}
	for index, term := range measures {
		expr, renderErr := expression(term)
		if renderErr != nil {
			return Statement{}, renderErr
		}
		alias := fmt.Sprintf("golem_m%d", index)
		selects = append(selects, expr+" AS "+dialect.Quote(physical.PhysicalName(alias)))
		publicAliases[termKey(term)] = alias
		logical := compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}
		nullable := term.Operator != golem.AggregateCountAll && term.Operator != golem.AggregateCountField
		if field, present := plan.Field(term.Field); present {
			logical = resultType(field.LogicalType(), term.Operator)
		}
		columns = append(columns, Column{Term: term, Alias: alias, Type: logical, Nullable: nullable})
	}
	if len(selects) == 0 {
		return Statement{}, fmt.Errorf("P6_ANALYTICS_SQL_SELECT: request has no output")
	}
	take, takePresent := request.Take()
	reversePage := takePresent && take < 0
	orderSpecs := make([]analyticsOrderSpec, 0, len(request.OrderBy())+len(dimensions))
	ordered := map[string]bool{}
	for index, order := range request.OrderBy() {
		expr, renderErr := expression(order.Term)
		if renderErr != nil {
			return Statement{}, renderErr
		}
		key := termKey(order.Term)
		alias := publicAliases[key]
		if alias == "" {
			alias = fmt.Sprintf("golem_o%d", index)
			selects = append(selects, expr+" AS "+dialect.Quote(physical.PhysicalName(alias)))
			publicAliases[key] = alias
		}
		orderSpecs = append(orderSpecs, analyticsOrderSpec{term: order.Term, expression: expr, alias: alias, descending: order.Descending, typ: analyticsTermType(plan, order.Term), exactNumeric: analyticsExactNumeric(plan, order.Term)})
		ordered[key] = true
	}
	for _, term := range dimensions {
		key := termKey(term)
		if ordered[key] {
			continue
		}
		expr, renderErr := expression(term)
		if renderErr != nil {
			return Statement{}, renderErr
		}
		orderSpecs = append(orderSpecs, analyticsOrderSpec{term: term, expression: expr, alias: publicAliases[key], typ: analyticsTermType(plan, term), exactNumeric: analyticsExactNumeric(plan, term)})
	}
	if having, present := request.Having(); present {
		havingTerms := []golem.FrozenAnalyticsTerm{}
		collectHavingTerms(having, &havingTerms)
		for index, term := range havingTerms {
			key := termKey(term)
			if publicAliases[key] != "" {
				continue
			}
			expr, renderErr := expression(term)
			if renderErr != nil {
				return Statement{}, renderErr
			}
			alias := fmt.Sprintf("golem_h%d", index)
			publicAliases[key] = alias
			selects = append(selects, expr+" AS "+dialect.Quote(physical.PhysicalName(alias)))
		}
	}
	guardColumns := maxContributionRows > 0 || maxIntermediateGroups > 0
	if guardColumns {
		if len(dimensions) > 0 {
			contributionCount := "0"
			if useContributionCTE {
				contributionCount = "MAX(" + dialect.Quote(contributionAlias) + "." + dialect.Quote("golem_contribution_count") + ")"
			}
			selects = append(selects, contributionCount+" AS "+dialect.Quote("golem_contribution_count"))
		} else {
			contributionCount := "COUNT(*)"
			if useContributionCTE {
				contributionCount = "COALESCE(MAX(" + dialect.Quote(contributionAlias) + "." + dialect.Quote("golem_contribution_count") + "), 0)"
			}
			selects = append(selects,
				contributionCount+" AS "+dialect.Quote("golem_contribution_count"),
				"1 AS "+dialect.Quote("golem_intermediate_count"),
			)
		}
	}
	fromText := dialect.Table(physicalModel) + " AS " + dialect.Quote(rootAlias)
	currentModel, currentAlias := request.ModelID(), rootAlias
	for index, hop := range relationPath {
		targetModel, targetOK := resolverModel(registry, provider, hop.Endpoint.TargetModelID())
		if !targetOK {
			return Statement{}, fmt.Errorf("P6_ANALYTICS_SQL_RELATION: target physical model is absent")
		}
		targetAlias := relationAliases[index]
		conditions := make([]string, 0, len(hop.Endpoint.Correlation())+1)
		for _, pair := range hop.Endpoint.Correlation() {
			parent, parentOK := registry.PhysicalField(external, currentModel, pair.ParentFieldID())
			child, childOK := registry.PhysicalField(external, hop.Endpoint.TargetModelID(), pair.ChildFieldID())
			if !parentOK || !childOK {
				return Statement{}, fmt.Errorf("P6_ANALYTICS_SQL_RELATION: correlation field is absent")
			}
			conditions = append(conditions, dialect.Quote(targetAlias)+"."+dialect.Quote(child.ColumnName())+" = "+dialect.Quote(currentAlias)+"."+dialect.Quote(parent.ColumnName()))
		}
		policy, policyErr := policysql.Compile(policysql.Request{Condition: hop.Authorized.Where(), Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: targetAlias})
		if policyErr != nil {
			return Statement{}, policyErr
		}
		conditions = append(conditions, rebase(policy.SQL(), len(args), provider))
		args = append(args, policy.Args()...)
		if index == len(relationPath)-1 {
			for _, field := range hop.Authorized.Fields() {
				if field.Conditional() && !field.DischargedByConstraint() {
					return Statement{}, fmt.Errorf("P6_ANALYTICS_SQL_AUTHORIZATION: conditional relation dimension is not discharged")
				}
				if _, present := field.Mask(); present && !field.Conditional() {
					return Statement{}, fmt.Errorf("P6_ANALYTICS_SQL_AUTHORIZATION: relation analytical field has an invalid standalone mask")
				}
			}
		}
		fromText += " INNER JOIN " + dialect.Table(targetModel) + " AS " + dialect.Quote(targetAlias) + " ON (" + strings.Join(conditions, " AND ") + ")"
		currentModel, currentAlias = hop.Endpoint.TargetModelID(), targetAlias
	}
	rootSQL := rebase(root.SQL(), len(args), provider)
	args = append(args, root.Args()...)
	fromText += " WHERE " + rootSQL
	contributionCTE := ""
	text := "SELECT " + strings.Join(selects, ", ") + " FROM " + fromText
	if useContributionCTE {
		if len(contributionProjection) == 0 {
			contributionProjection = append(contributionProjection, "1 AS "+dialect.Quote("golem_unit"))
			contributionProjectionNames = append(contributionProjectionNames, "golem_unit")
		}
		args = append(args, int64(maxContributionRows+1))
		limitedAlias := physical.PhysicalName("golem_limited_contributions")
		limited := "SELECT " + strings.Join(contributionProjection, ", ") + " FROM " + fromText + " LIMIT " + dialect.Placeholder(len(args))
		outerProjection := make([]string, 0, len(contributionProjectionNames)+1)
		for _, name := range contributionProjectionNames {
			quoted := dialect.Quote(physical.PhysicalName(name))
			outerProjection = append(outerProjection, dialect.Quote(limitedAlias)+"."+quoted+" AS "+quoted)
		}
		outerProjection = append(outerProjection, "COUNT(*) OVER () AS "+dialect.Quote("golem_contribution_count"))
		contributionCTE = "SELECT " + strings.Join(outerProjection, ", ") + " FROM (" + limited + ") AS " + dialect.Quote(limitedAlias)
		text = "SELECT " + strings.Join(selects, ", ") + " FROM " + dialect.Quote(contributionAlias) + " AS " + dialect.Quote(contributionAlias)
	}
	if len(groups) > 0 {
		text += " GROUP BY " + strings.Join(groups, ", ")
	}
	groupedCTE := ""
	projectedAliases := []string{}
	if len(dimensions) > 0 {
		groupedAlias := physical.PhysicalName("golem_grouped")
		outerSelect := make([]string, 0, len(columns)+2)
		selected := map[string]bool{}
		for _, column := range columns {
			selected[column.Alias] = true
			projectedAliases = append(projectedAliases, column.Alias)
			alias := dialect.Quote(physical.PhysicalName(column.Alias))
			outerSelect = append(outerSelect, dialect.Quote(groupedAlias)+"."+alias+" AS "+alias)
		}
		for _, order := range orderSpecs {
			if selected[order.alias] {
				continue
			}
			selected[order.alias] = true
			projectedAliases = append(projectedAliases, order.alias)
			alias := dialect.Quote(physical.PhysicalName(order.alias))
			outerSelect = append(outerSelect, dialect.Quote(groupedAlias)+"."+alias+" AS "+alias)
		}
		if guardColumns {
			for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count"} {
				quoted := dialect.Quote(name)
				outerSelect = append(outerSelect, dialect.Quote(groupedAlias)+"."+quoted+" AS "+quoted)
			}
		}
		if guardColumns {
			groupedCore := text
			if maxIntermediateGroups > 0 {
				args = append(args, int64(maxIntermediateGroups+1))
				groupedCore += " LIMIT " + dialect.Placeholder(len(args))
			}
			limitedGroups := physical.PhysicalName("golem_limited_groups")
			groupedCTE = "SELECT " + dialect.Quote(limitedGroups) + ".*, COUNT(*) OVER () AS " + dialect.Quote("golem_intermediate_count") + " FROM (" + groupedCore + ") AS " + dialect.Quote(limitedGroups)
			text = "SELECT " + strings.Join(outerSelect, ", ") + " FROM " + dialect.Quote("golem_groups") + " AS " + dialect.Quote(groupedAlias)
		} else {
			text = "SELECT " + strings.Join(outerSelect, ", ") + " FROM (" + text + ") AS " + dialect.Quote(groupedAlias)
		}
		aliasExpression := func(term golem.FrozenAnalyticsTerm) (string, error) {
			alias := publicAliases[termKey(term)]
			if alias == "" {
				return "", fmt.Errorf("P6_ANALYTICS_SQL_ALIAS: grouped term has no normalized alias")
			}
			return dialect.Quote(groupedAlias) + "." + dialect.Quote(physical.PhysicalName(alias)), nil
		}
		if having, present := request.Having(); present {
			rendered, values, renderErr := renderHaving(having, aliasExpression, func(term golem.FrozenAnalyticsTerm) bool { return analyticsExactNumeric(plan, term) }, dialect, len(args), provider)
			if renderErr != nil {
				return Statement{}, renderErr
			}
			args = append(args, values...)
			text += " WHERE " + rendered
		}
		for index := range orderSpecs {
			orderSpecs[index].expression = dialect.Quote(groupedAlias) + "." + dialect.Quote(physical.PhysicalName(orderSpecs[index].alias))
		}
	} else if having, present := request.Having(); present {
		rendered, values, renderErr := renderHaving(having, expression, func(term golem.FrozenAnalyticsTerm) bool { return analyticsExactNumeric(plan, term) }, dialect, len(args), provider)
		if renderErr != nil {
			return Statement{}, renderErr
		}
		args = append(args, values...)
		text += " HAVING " + rendered
	}
	if len(orderSpecs) > 0 {
		text += " ORDER BY " + strings.Join(renderAnalyticsOrders(orderSpecs, provider, reversePage, "", dialect), ", ")
	}
	if takePresent {
		if take < 0 {
			take = -take
		}
		args = append(args, int64(take))
		text += " LIMIT " + dialect.Placeholder(len(args))
	} else if len(dimensions) > 0 && maxResultRows > 0 {
		args = append(args, int64(maxResultRows+1))
		text += " LIMIT " + dialect.Placeholder(len(args))
	}
	if skip, present := request.Skip(); present {
		args = append(args, int64(skip))
		text += " OFFSET " + dialect.Placeholder(len(args))
	}
	if reversePage {
		pageAlias := physical.PhysicalName("golem_page")
		public := make([]string, 0, len(projectedAliases)+2)
		for _, name := range projectedAliases {
			alias := dialect.Quote(physical.PhysicalName(name))
			public = append(public, dialect.Quote(pageAlias)+"."+alias+" AS "+alias)
		}
		if guardColumns {
			for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count"} {
				quoted := dialect.Quote(name)
				public = append(public, dialect.Quote(pageAlias)+"."+quoted+" AS "+quoted)
			}
		}
		outerOrders := renderAnalyticsOrders(orderSpecs, provider, false, pageAlias, dialect)
		text = "SELECT " + strings.Join(public, ", ") + " FROM (" + text + ") AS " + dialect.Quote(pageAlias) + " ORDER BY " + strings.Join(outerOrders, ", ")
	}
	guardRowColumn := false
	if len(dimensions) > 0 && guardColumns {
		visibleAlias := physical.PhysicalName("golem_visible")
		pageProjection := make([]string, 0, len(projectedAliases)+4)
		for _, name := range projectedAliases {
			quoted := dialect.Quote(physical.PhysicalName(name))
			pageProjection = append(pageProjection, dialect.Quote(visibleAlias)+"."+quoted+" AS "+quoted)
		}
		for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count"} {
			quoted := dialect.Quote(name)
			pageProjection = append(pageProjection, dialect.Quote(visibleAlias)+"."+quoted+" AS "+quoted)
		}
		pageOrders := renderAnalyticsOrders(orderSpecs, provider, false, visibleAlias, dialect)
		pageProjection = append(pageProjection,
			"ROW_NUMBER() OVER (ORDER BY "+strings.Join(pageOrders, ", ")+") AS "+dialect.Quote("golem_page_ordinal"),
			"0 AS "+dialect.Quote("golem_guard_row"),
		)
		pageSQL := "SELECT " + strings.Join(pageProjection, ", ") + " FROM (" + text + ") AS " + dialect.Quote(visibleAlias)

		sentinel := make([]string, 0, len(projectedAliases)+4)
		for _, name := range projectedAliases {
			sentinel = append(sentinel, "NULL AS "+dialect.Quote(physical.PhysicalName(name)))
		}
		groupsAlias := physical.PhysicalName("golem_guard_groups")
		for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count"} {
			quoted := dialect.Quote(name)
			sentinel = append(sentinel, "COALESCE(MAX("+dialect.Quote(groupsAlias)+"."+quoted+"), 0) AS "+quoted)
		}
		sentinel = append(sentinel, "0 AS "+dialect.Quote("golem_page_ordinal"), "1 AS "+dialect.Quote("golem_guard_row"))
		sentinelSQL := "SELECT " + strings.Join(sentinel, ", ") + " FROM " + dialect.Quote("golem_groups") + " AS " + dialect.Quote(groupsAlias)

		combinedAlias := physical.PhysicalName("golem_guarded")
		finalProjection := make([]string, 0, len(columns)+3)
		for _, column := range columns {
			quoted := dialect.Quote(physical.PhysicalName(column.Alias))
			finalProjection = append(finalProjection, dialect.Quote(combinedAlias)+"."+quoted+" AS "+quoted)
		}
		for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count", "golem_guard_row"} {
			quoted := dialect.Quote(name)
			finalProjection = append(finalProjection, dialect.Quote(combinedAlias)+"."+quoted+" AS "+quoted)
		}
		ctes := []string{}
		if contributionCTE != "" {
			ctes = append(ctes, dialect.Quote(contributionAlias)+" AS MATERIALIZED ("+contributionCTE+")")
		}
		ctes = append(ctes, dialect.Quote("golem_groups")+" AS MATERIALIZED ("+groupedCTE+")")
		text = "WITH " + strings.Join(ctes, ", ") + " SELECT " + strings.Join(finalProjection, ", ") + " FROM (" + pageSQL + " UNION ALL " + sentinelSQL + ") AS " + dialect.Quote(combinedAlias) + " ORDER BY " + dialect.Quote(combinedAlias) + "." + dialect.Quote("golem_guard_row") + " ASC, " + dialect.Quote(combinedAlias) + "." + dialect.Quote("golem_page_ordinal") + " ASC"
		guardRowColumn = true
	}
	if contributionCTE != "" && !(len(dimensions) > 0 && guardColumns) {
		text = "WITH " + dialect.Quote(contributionAlias) + " AS MATERIALIZED (" + contributionCTE + ") " + text
	}
	return Statement{text: text, args: args, columns: columns, grouped: len(dimensions) > 0, maxContributionRows: maxContributionRows, maxIntermediateGroups: maxIntermediateGroups, maxResultRows: maxResultRows, guardColumns: guardColumns, guardRowColumn: guardRowColumn}, nil
}

type analyticsOrderSpec struct {
	term         golem.FrozenAnalyticsTerm
	expression   string
	alias        string
	descending   bool
	typ          compilerir.LogicalTypeIR
	exactNumeric bool
}

func analyticsExactNumeric(plan Plan, term golem.FrozenAnalyticsTerm) bool {
	field, ok := plan.Field(term.Field)
	if !ok {
		return false
	}
	kind := field.LogicalType().Kind
	return term.Operator == golem.AggregateSum && (kind == compilerir.TypeInt16 || kind == compilerir.TypeInt32 || kind == compilerir.TypeInt64 || kind == compilerir.TypeDecimal) ||
		term.Operator == golem.AggregateAverage && kind == compilerir.TypeDecimal
}

func analyticsTermType(plan Plan, term golem.FrozenAnalyticsTerm) compilerir.LogicalTypeIR {
	if term.Operator == golem.AggregateCountAll || term.Operator == golem.AggregateCountField {
		return compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}
	}
	if field, ok := plan.Field(term.Field); ok {
		return resultType(field.LogicalType(), term.Operator)
	}
	return compilerir.LogicalTypeIR{}
}

func renderAnalyticsOrders(specs []analyticsOrderSpec, provider policyir.Provider, reverse bool, aliasOwner physical.PhysicalName, dialect policysql.Dialect) []string {
	parts := make([]string, 0, len(specs)*2)
	for _, spec := range specs {
		expression := spec.expression
		if aliasOwner != "" {
			expression = dialect.Quote(aliasOwner) + "." + dialect.Quote(physical.PhysicalName(spec.alias))
		}
		descending := spec.descending
		if reverse {
			descending = !descending
		}
		operand := analyticalOperand(provider, spec.typ, expression)
		if spec.exactNumeric {
			if provider == policyir.ProviderPostgreSQL {
				operand = "CAST(" + expression + " AS NUMERIC)"
			} else {
				operand = "(" + expression + " COLLATE " + sqliteprovider.AnalyticsNumericCollation + ")"
			}
		}
		if descending {
			parts = append(parts, expression+" IS NULL ASC", operand+" DESC")
		} else {
			parts = append(parts, expression+" IS NOT NULL ASC", operand+" ASC")
		}
	}
	return parts
}

func resolverModel(registry *schema.Registry, provider policyir.Provider, model golem.ModelID) (policysql.Model, bool) {
	resolver := policysql.SchemaResolver(registry)
	return resolver.Model(provider, policyir.ModelID(model))
}

func aggregateExpression(provider policyir.Provider, operator golem.AggregateOperator, column string, typ compilerir.LogicalTypeIR) string {
	analyticalColumn := analyticalOperand(provider, typ, column)
	switch operator {
	case 0:
		return analyticalColumn
	case golem.AggregateCountField:
		return "COUNT(" + column + ")"
	case golem.AggregateSum:
		if typ.Kind == compilerir.TypeInt16 || typ.Kind == compilerir.TypeInt32 || typ.Kind == compilerir.TypeInt64 {
			if provider == policyir.ProviderSQLite {
				return sqliteprovider.AnalyticsIntegerSumFunction + "(" + column + ")"
			}
			return "SUM(" + column + ")::text"
		}
		if typ.Kind == compilerir.TypeDecimal {
			scale := logicalScale(typ)
			if provider == policyir.ProviderSQLite {
				return fmt.Sprintf("%s(%s, %d)", sqliteprovider.AnalyticsDecimalSumFunction, column, scale)
			}
			return "SUM(" + column + ")::text"
		}
		return "SUM(" + column + ")"
	case golem.AggregateAverage:
		if typ.Kind == compilerir.TypeDecimal {
			scale := logicalScale(typ)
			if provider == policyir.ProviderSQLite {
				return fmt.Sprintf("%s(%s, %d)", sqliteprovider.AnalyticsDecimalAvgFunction, column, scale)
			}
			return fmt.Sprintf("ROUND(AVG(%s), %d)::text", column, scale)
		}
		return "AVG(" + column + ")"
	case golem.AggregateMinimum:
		return "MIN(" + analyticalColumn + ")"
	case golem.AggregateMaximum:
		return "MAX(" + analyticalColumn + ")"
	}
	return column
}

func logicalScale(typ compilerir.LogicalTypeIR) uint16 {
	if typ.Scale == nil {
		return 0
	}
	return *typ.Scale
}

func analyticalOperand(provider policyir.Provider, typ compilerir.LogicalTypeIR, expression string) string {
	if typ.Kind != compilerir.TypeString {
		return expression
	}
	if provider == policyir.ProviderPostgreSQL {
		return "(" + expression + " COLLATE \"C\")"
	}
	return "(" + expression + " COLLATE BINARY)"
}
func resultType(input compilerir.LogicalTypeIR, operator golem.AggregateOperator) compilerir.LogicalTypeIR {
	switch operator {
	case golem.AggregateCountField:
		return compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}
	case golem.AggregateAverage:
		if input.Kind == compilerir.TypeDecimal {
			return input
		}
		return compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat64}
	case golem.AggregateSum:
		if input.Kind == compilerir.TypeInt16 || input.Kind == compilerir.TypeInt32 || input.Kind == compilerir.TypeInt64 {
			return compilerir.LogicalTypeIR{Kind: compilerir.TypeString}
		}
		if input.Kind == compilerir.TypeDecimal {
			return input
		}
		return compilerir.LogicalTypeIR{Kind: compilerir.TypeFloat64}
	default:
		return input
	}
}
func renderHaving(value golem.FrozenGroupPredicate, expression func(golem.FrozenAnalyticsTerm) (string, error), exactNumeric func(golem.FrozenAnalyticsTerm) bool, dialect policysql.Dialect, offset int, provider policyir.Provider) (string, []any, error) {
	if value.Kind == 1 {
		expr, err := expression(value.Term)
		if err != nil {
			return "", nil, err
		}
		if value.Operator == "isNull" {
			return expr + " IS NULL", nil, nil
		}
		if value.Operator == "isNotNull" {
			return expr + " IS NOT NULL", nil, nil
		}
		if value.Operator == "contains" || value.Operator == "startsWith" || value.Operator == "endsWith" {
			if value.Mode != golem.FrozenComparisonSensitive && value.Mode != golem.FrozenComparisonASCIIInsensitive {
				return "", nil, fmt.Errorf("P6_ANALYTICS_HAVING: invalid text comparison mode")
			}
			placeholder := dialect.Placeholder(offset + 1)
			left, right := expr, placeholder
			if provider == policyir.ProviderSQLite {
				if value.Mode == golem.FrozenComparisonASCIIInsensitive {
					left, right = "golem_policy_ascii_fold("+left+")", "golem_policy_ascii_fold("+right+")"
				} else {
					left, right = "("+left+" COLLATE BINARY)", "("+right+" COLLATE BINARY)"
				}
				predicate := "instr(" + left + ", " + right + ") > 0"
				if value.Operator == "startsWith" {
					predicate = "substr(" + left + ", 1, length(" + right + ")) = " + right
				}
				if value.Operator == "endsWith" {
					predicate = "length(" + right + ") = 0 OR substr(" + left + ", -length(" + right + ")) = " + right
				}
				return predicate, []any{analyticsArgument(value.Value)}, nil
			}
			left, right = "("+left+" COLLATE \"C\")", "("+right+"::text COLLATE \"C\")"
			if value.Mode == golem.FrozenComparisonASCIIInsensitive {
				const upper, lower = "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz"
				left = "(pg_catalog.translate(" + left + ", '" + upper + "', '" + lower + "') COLLATE \"C\")"
				right = "(pg_catalog.translate(" + right + ", '" + upper + "', '" + lower + "') COLLATE \"C\")"
			}
			predicate := "pg_catalog.strpos(" + left + ", " + right + ") > 0"
			if value.Operator == "startsWith" {
				predicate = "pg_catalog.left(" + left + ", pg_catalog.char_length(" + right + ")) = " + right
			}
			if value.Operator == "endsWith" {
				predicate = "pg_catalog.right(" + left + ", pg_catalog.char_length(" + right + ")) = " + right
			}
			return "COALESCE(" + predicate + ", FALSE)", []any{analyticsArgument(value.Value)}, nil
		}
		operators := map[string]string{"eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}
		operator := operators[value.Operator]
		if operator == "" {
			return "", nil, fmt.Errorf("P6_ANALYTICS_HAVING: invalid operator")
		}
		placeholder := dialect.Placeholder(offset + 1)
		if exactNumeric(value.Term) {
			if provider == policyir.ProviderSQLite {
				return sqliteprovider.AnalyticsNumericCompareFunction + "(" + expr + ", " + placeholder + ") " + operator + " 0", []any{analyticsArgument(value.Value)}, nil
			}
			return "CAST(" + expr + " AS NUMERIC) " + operator + " CAST(" + placeholder + " AS NUMERIC)", []any{analyticsArgument(value.Value)}, nil
		}
		return expr + " " + operator + " " + placeholder, []any{analyticsArgument(value.Value)}, nil
	}
	if value.Kind == 4 && len(value.Children) == 1 {
		child, args, err := renderHaving(value.Children[0], expression, exactNumeric, dialect, offset, provider)
		return "NOT (" + child + ")", args, err
	}
	join := " AND "
	if value.Kind == 3 {
		join = " OR "
	}
	parts := make([]string, len(value.Children))
	args := []any{}
	for i, child := range value.Children {
		part, values, err := renderHaving(child, expression, exactNumeric, dialect, offset+len(args), provider)
		if err != nil {
			return "", nil, err
		}
		parts[i] = "(" + part + ")"
		args = append(args, values...)
	}
	return strings.Join(parts, join), args, nil
}
func analyticsArgument(value any) any {
	switch typed := value.(type) {
	case golem.ExactInteger:
		return typed.String()
	case golem.ExactDecimal:
		return typed.String()
	default:
		return value
	}
}
func termKey(term golem.FrozenAnalyticsTerm) string {
	return golem.RuntimeAnalyticsTermKey(term)
}

func rebase(sql string, offset int, provider policyir.Provider) string {
	if offset == 0 {
		return sql
	}
	marker := byte('$')
	if provider == policyir.ProviderSQLite {
		marker = '?'
	} else if provider != policyir.ProviderPostgreSQL {
		return sql
	}
	var result strings.Builder
	for index := 0; index < len(sql); {
		if sql[index] != marker || index+1 >= len(sql) || sql[index+1] < '0' || sql[index+1] > '9' {
			result.WriteByte(sql[index])
			index++
			continue
		}
		end := index + 1
		for end < len(sql) && sql[end] >= '0' && sql[end] <= '9' {
			end++
		}
		position, _ := strconv.Atoi(sql[index+1 : end])
		result.WriteByte(marker)
		result.WriteString(strconv.Itoa(position + offset))
		index = end
	}
	return result.String()
}
func analyticsDialect(provider policyir.Provider) (policysql.Dialect, golem.Provider, error) {
	switch provider {
	case policyir.ProviderSQLite:
		return sqliteprovider.NewPolicyDialect(), golem.SQLite, nil
	case policyir.ProviderPostgreSQL:
		return postgresprovider.NewPolicyDialect(), golem.PostgreSQL, nil
	default:
		return nil, "", fmt.Errorf("P6_ANALYTICS_SQL_PROVIDER: unsupported provider")
	}
}
