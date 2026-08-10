// Package scoped owns the closed, provider-neutral planning and SQL lowering
// for P6 typed scoped reads.
package scoped

import (
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type PolicySet interface {
	Policy(policyir.ModelID) (policyir.Policy, bool)
}

type occurrence struct {
	model      golem.ModelID
	join       *golem.FrozenScopedJoin
	endpoint   *schema.RelationEndpoint
	authorized readplan.Plan
	fields     map[golem.FieldID]schema.Field
	classified map[golem.FieldID]readplan.Field
}

type Plan struct {
	request     golem.FrozenScopedQuery
	occurrences map[uint32]occurrence
}

func (plan Plan) Request() golem.FrozenScopedQuery { return plan.request }

func Caller(request golem.FrozenScopedQuery, registry *schema.Registry, providers policyir.ProviderSet, policies PolicySet, limits readplan.Limits) (Plan, error) {
	return build(request, registry, providers, policies, limits, false)
}
func System(request golem.FrozenScopedQuery, registry *schema.Registry, providers policyir.ProviderSet, limits readplan.Limits) (Plan, error) {
	return build(request, registry, providers, nil, limits, true)
}

func build(request golem.FrozenScopedQuery, registry *schema.Registry, providers policyir.ProviderSet, policies PolicySet, limits readplan.Limits, system bool) (Plan, error) {
	if registry == nil || !providers.Valid() {
		return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_INPUT: registry and provider set are required")
	}
	root, ok := registry.Model(request.RootModelID())
	if !ok || !root.ScopedReadsEnabled() {
		return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_ROOT: scoped reads are not enabled for this root")
	}
	occurrences := map[uint32]occurrence{0: {model: request.RootModelID(), fields: map[golem.FieldID]schema.Field{}}}
	joins := request.Joins()
	for index := range joins {
		join := joins[index]
		parent, present := occurrences[join.ParentOccurrence]
		if !present || parent.model != join.ParentModel {
			return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_JOIN: parent occurrence is absent")
		}
		endpoint, present := registry.RelationEndpoint(join.ParentModel, join.Field, join.Relation)
		if !present || endpoint.TargetModelID() != join.Model {
			return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_JOIN: relation edge does not match schema")
		}
		cardinality := golem.ScopedRelationToOne
		if endpoint.Cardinality() == compilerir.RelationMany {
			cardinality = golem.ScopedRelationToMany
		}
		if cardinality != join.Cardinality {
			return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_JOIN: relation cardinality does not match schema")
		}
		copyEndpoint := endpoint
		copyJoin := join
		occurrences[join.Occurrence] = occurrence{model: join.Model, join: &copyJoin, endpoint: &copyEndpoint, fields: map[golem.FieldID]schema.Field{}}
	}
	collect := func(expression golem.FrozenScopedExpression) error {
		item, present := occurrences[expression.Occurrence]
		if !present || item.model != expression.Model {
			return fmt.Errorf("P6_SCOPED_PLAN_EXPRESSION: occurrence/model mismatch")
		}
		if expression.Kind == golem.ScopedExpressionCountAll {
			return nil
		}
		field, present := registry.Field(expression.Model, expression.Field)
		if !present || !field.Visible() || (field.Kind() != compilerir.FieldScalar && field.Kind() != compilerir.FieldEnum) {
			return fmt.Errorf("P6_SCOPED_PLAN_FIELD: field is absent, hidden, or non-scalar")
		}
		if err := validateExpression(field, expression.Kind); err != nil {
			return err
		}
		item.fields[expression.Field] = field
		occurrences[expression.Occurrence] = item
		return nil
	}
	for _, expression := range request.Selections() {
		if err := collect(expression); err != nil {
			return Plan{}, err
		}
	}
	for _, expression := range request.GroupBy() {
		if expression.Kind != golem.ScopedExpressionField {
			return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_GROUP: group keys must be fields")
		}
		if err := collect(expression); err != nil {
			return Plan{}, err
		}
	}
	for _, order := range request.Orders() {
		if err := collect(order.Expression); err != nil {
			return Plan{}, err
		}
	}
	if where, present := request.Where(); present {
		if err := walkPredicate(where, collect, false); err != nil {
			return Plan{}, err
		}
	}
	if having, present := request.Having(); present {
		if err := walkPredicate(having, collect, true); err != nil {
			return Plan{}, err
		}
	}
	constraints := map[uint32]policyir.Condition{}
	if where, present := request.Where(); present {
		var err error
		constraints, err = scopedConstraints(where, registry)
		if err != nil {
			return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_CONSTRAINT: %w", err)
		}
	}
	required := map[uint32]map[golem.FieldID]bool{}
	markRequired := func(expression golem.FrozenScopedExpression) {
		if expression.Field == (golem.FieldID{}) {
			return
		}
		if required[expression.Occurrence] == nil {
			required[expression.Occurrence] = map[golem.FieldID]bool{}
		}
		required[expression.Occurrence][expression.Field] = true
	}
	for _, join := range request.Joins() {
		item := occurrences[join.Occurrence]
		parent := occurrences[join.ParentOccurrence]
		for _, pair := range item.endpoint.Correlation() {
			for _, position := range []struct {
				occurrence uint32
				field      golem.FieldID
				item       *occurrence
			}{
				{join.ParentOccurrence, pair.ParentFieldID(), &parent},
				{join.Occurrence, pair.ChildFieldID(), &item},
			} {
				descriptor, present := registry.Field(position.item.model, position.field)
				if !present || !descriptor.Visible() {
					return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_JOIN_CLASSIFICATION: correlation field is unavailable")
				}
				position.item.fields[position.field] = descriptor
				markRequired(golem.FrozenScopedExpression{
					Occurrence: position.occurrence,
					Model:      position.item.model,
					Field:      position.field,
					Kind:       golem.ScopedExpressionField,
				})
			}
		}
		occurrences[join.ParentOccurrence] = parent
		occurrences[join.Occurrence] = item
	}
	if where, present := request.Where(); present {
		walkPredicateExpressions(where, markRequired)
	}
	if having, present := request.Having(); present {
		walkPredicateExpressions(having, markRequired)
	}
	for _, expression := range request.GroupBy() {
		markRequired(expression)
	}
	for _, order := range request.Orders() {
		markRequired(order.Expression)
	}
	for _, expression := range request.Selections() {
		if expression.Kind != golem.ScopedExpressionField && expression.Kind != golem.ScopedExpressionCountAll {
			markRequired(expression)
		}
	}
	grouped := len(request.GroupBy()) != 0
	groupSet := map[string]bool{}
	for _, expression := range request.GroupBy() {
		groupSet[expressionKey(expression)] = true
	}
	for _, expression := range request.Selections() {
		if expression.Kind == golem.ScopedExpressionField && grouped && !groupSet[expressionKey(expression)] {
			return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_GROUP: selected field is absent from group key")
		}
		if expression.Kind != golem.ScopedExpressionField && !grouped && hasSelectedField(request.Selections()) {
			return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_GROUP: fields and aggregates require group-by")
		}
	}
	for id, item := range occurrences {
		selection := make([]readir.Selection, 0, len(item.fields))
		for _, fieldID := range sortedFields(item.fields) {
			value, err := readir.NewScalarSelection(policyir.FieldID(fieldID))
			if err != nil {
				return Plan{}, err
			}
			selection = append(selection, value)
		}
		input := readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(item.model), Projection: readir.ProjectionSelect, Selection: selection}
		if constraint, present := constraints[id]; present {
			input.Where = &constraint
		}
		if len(selection) == 0 {
			input.Operation, input.Projection = readir.Count, readir.ProjectionDefault
		}
		bound, err := readir.NewRequest(input)
		if err != nil {
			return Plan{}, err
		}
		var authorized readplan.Plan
		if system {
			authorized, err = readplan.System(bound, registry, limits)
		} else {
			authorized, err = readplan.Caller(bound, registry, policies, limits)
		}
		if err != nil {
			return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_AUTHORIZATION: occurrence=%d: %w", id, err)
		}
		item.authorized = authorized
		item.classified = map[golem.FieldID]readplan.Field{}
		for _, field := range authorized.Fields() {
			item.classified[golem.FieldID(field.FieldID())] = field
		}
		for fieldID := range required[id] {
			classified, present := item.classified[fieldID]
			if !present {
				return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_CLASSIFICATION: required field was not classified")
			}
			if classified.Conditional() && !classified.DischargedByConstraint() {
				return Plan{}, fmt.Errorf("P6_SCOPED_PLAN_CLASSIFICATION: filter/group/order/aggregate field access is not discharged")
			}
		}
		occurrences[id] = item
	}
	return Plan{request: request, occurrences: occurrences}, nil
}

func validateExpression(field schema.Field, kind golem.ScopedExpressionKind) error {
	switch kind {
	case golem.ScopedExpressionField, golem.ScopedExpressionCountField:
		return nil
	case golem.ScopedExpressionSum, golem.ScopedExpressionAverage:
		switch field.LogicalType().Kind {
		case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64, compilerir.TypeFloat32, compilerir.TypeFloat64, compilerir.TypeDecimal:
			return nil
		}
	case golem.ScopedExpressionMinimum, golem.ScopedExpressionMaximum:
		switch field.LogicalType().Kind {
		case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64, compilerir.TypeFloat32, compilerir.TypeFloat64, compilerir.TypeDecimal, compilerir.TypeString, compilerir.TypeDate, compilerir.TypeTime, compilerir.TypeDateTime:
			return nil
		}
	}
	return fmt.Errorf("P6_SCOPED_PLAN_CAPABILITY: expression %d is not valid for %s", kind, field.LogicalType().Kind)
}
func walkPredicate(value golem.FrozenScopedPredicate, collect func(golem.FrozenScopedExpression) error, grouped bool) error {
	switch value.Operator {
	case golem.ScopedPredicateAnd, golem.ScopedPredicateOr, golem.ScopedPredicateNot:
		for _, child := range value.Children {
			if err := walkPredicate(child, collect, grouped); err != nil {
				return err
			}
		}
		return nil
	default:
		if grouped && value.Expression.Kind == golem.ScopedExpressionField { /* grouped dimensions are legal */
		}
		return collect(value.Expression)
	}
}
func walkPredicateExpressions(value golem.FrozenScopedPredicate, visit func(golem.FrozenScopedExpression)) {
	if value.Expression.Kind != 0 {
		visit(value.Expression)
	}
	for _, child := range value.Children {
		walkPredicateExpressions(child, visit)
	}
}

func scopedConstraints(value golem.FrozenScopedPredicate, registry *schema.Registry) (map[uint32]policyir.Condition, error) {
	if value.Operator == golem.ScopedPredicateAnd {
		result := map[uint32][]policyir.Condition{}
		for _, child := range value.Children {
			constraints, err := scopedConstraints(child, registry)
			if err != nil {
				return nil, err
			}
			for occurrence, condition := range constraints {
				result[occurrence] = append(result[occurrence], condition)
			}
		}
		merged := map[uint32]policyir.Condition{}
		for occurrence, conditions := range result {
			if len(conditions) == 1 {
				merged[occurrence] = conditions[0]
				continue
			}
			condition, err := policyir.NewLogical(conditions[0].ModelID(), policyir.LogicalAnd, conditions)
			if err != nil {
				return nil, err
			}
			merged[occurrence] = condition
		}
		return merged, nil
	}
	if value.Operator == golem.ScopedPredicateOr || value.Operator == golem.ScopedPredicateNot {
		var occurrence uint32
		conditions := []policyir.Condition{}
		for _, child := range value.Children {
			scoped, err := scopedConstraints(child, registry)
			if err != nil {
				return nil, err
			}
			if len(scoped) != 1 {
				return nil, fmt.Errorf("cross-occurrence OR/NOT cannot be proven")
			}
			for current, condition := range scoped {
				if len(conditions) > 0 && current != occurrence {
					return nil, fmt.Errorf("cross-occurrence OR/NOT cannot be proven")
				}
				occurrence = current
				conditions = append(conditions, condition)
			}
		}
		operator := policyir.LogicalOr
		if value.Operator == golem.ScopedPredicateNot {
			operator = policyir.LogicalNot
		}
		condition, err := policyir.NewLogical(conditions[0].ModelID(), operator, conditions)
		if err != nil {
			return nil, err
		}
		return map[uint32]policyir.Condition{occurrence: condition}, nil
	}
	condition, err := scopedLeafCondition(value, registry)
	if err != nil {
		return nil, err
	}
	return map[uint32]policyir.Condition{value.Expression.Occurrence: condition}, nil
}

func scopedLeafCondition(value golem.FrozenScopedPredicate, registry *schema.Registry) (policyir.Condition, error) {
	resolver := policysql.SchemaResolver(registry)
	descriptor, ok := resolver.Field(policyir.ProviderSQLite, policyir.ModelID(value.Expression.Model), policyir.FieldID(value.Expression.Field))
	if !ok {
		return policyir.Condition{}, fmt.Errorf("field type is unavailable")
	}
	operator, ok := scopedPolicyOperator(value.Operator)
	if !ok {
		return policyir.Condition{}, fmt.Errorf("operator has no policy equivalent")
	}
	operand := policyir.NoOperand()
	if operator != policyir.OperatorIsNull && operator != policyir.OperatorIsNotNull {
		values := make([]policyir.Value, len(value.Values))
		for index, raw := range value.Values {
			converted, err := scopedPolicyValue(raw, descriptor.Type, registry, value.Expression)
			if err != nil {
				return policyir.Condition{}, err
			}
			values[index] = converted
		}
		var err error
		if operator == policyir.OperatorIn || operator == policyir.OperatorNotIn {
			operand, err = policyir.ManyOperand(values)
		} else if len(values) == 1 {
			operand, err = policyir.OneOperand(values[0])
		} else {
			err = fmt.Errorf("comparison arity is invalid")
		}
		if err != nil {
			return policyir.Condition{}, err
		}
	}
	requirements, err := policyoperator.ValidateShape(operator, policyoperator.Shape{
		Node:      policyir.ConditionScalar,
		FieldType: descriptor.Type,
		Operand:   operand,
		Mode:      policyir.ComparisonSensitive,
		Providers: policyir.PortableProviders(),
	})
	if err != nil {
		return policyir.Condition{}, err
	}
	return policyir.NewScalar(policyir.ModelID(value.Expression.Model), policyir.FieldID(value.Expression.Field), descriptor.Type, operator, policyir.ComparisonSensitive, operand, requirements)
}
func scopedPolicyOperator(value golem.ScopedPredicateOperator) (policyir.OperatorID, bool) {
	switch value {
	case golem.ScopedPredicateEqual:
		return policyir.OperatorEqual, true
	case golem.ScopedPredicateNotEqual:
		return policyir.OperatorNotEqual, true
	case golem.ScopedPredicateIn:
		return policyir.OperatorIn, true
	case golem.ScopedPredicateNotIn:
		return policyir.OperatorNotIn, true
	case golem.ScopedPredicateLessThan:
		return policyir.OperatorLessThan, true
	case golem.ScopedPredicateLessThanOrEqual:
		return policyir.OperatorLessThanOrEqual, true
	case golem.ScopedPredicateGreaterThan:
		return policyir.OperatorGreaterThan, true
	case golem.ScopedPredicateGreaterThanOrEqual:
		return policyir.OperatorGreaterThanOrEqual, true
	case golem.ScopedPredicateContains:
		return policyir.OperatorContains, true
	case golem.ScopedPredicateStartsWith:
		return policyir.OperatorStartsWith, true
	case golem.ScopedPredicateEndsWith:
		return policyir.OperatorEndsWith, true
	case golem.ScopedPredicateIsNull:
		return policyir.OperatorIsNull, true
	case golem.ScopedPredicateIsNotNull:
		return policyir.OperatorIsNotNull, true
	}
	return 0, false
}
func scopedPolicyValue(raw any, typ policyir.TypeRef, registry *schema.Registry, expression golem.FrozenScopedExpression) (policyir.Value, error) {
	v := reflect.ValueOf(raw)
	switch typ.Kind() {
	case policyir.ValueBool:
		if v.Kind() == reflect.Bool {
			return policyir.BoolValue(v.Bool()), nil
		}
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64:
		if v.Kind() >= reflect.Int && v.Kind() <= reflect.Int64 {
			return policyir.SignedValue(typ.Kind(), v.Int())
		}
	case policyir.ValueFloat32:
		if v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64 {
			return policyir.Float32Value(float32(v.Float()))
		}
	case policyir.ValueFloat64:
		if v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64 {
			return policyir.Float64Value(v.Float())
		}
	case policyir.ValueString:
		if v.Kind() == reflect.String {
			return policyir.StringValue(v.String())
		}
	case policyir.ValueUUID:
		if value, ok := raw.(golem.UUID); ok {
			return policyir.UUIDValue(value.Bytes()), nil
		}
	case policyir.ValueDecimal:
		if value, ok := raw.(golem.Decimal); ok {
			return policyir.NewDecimalValue(value.Coefficient(), value.Scale())
		}
	case policyir.ValueDate:
		if value, ok := raw.(golem.Date); ok {
			return policyir.NewDateValue(int16(value.Year()), uint8(value.Month()), uint8(value.Day()))
		}
	case policyir.ValueTime:
		if value, ok := raw.(golem.Time); ok {
			return policyir.NewTimeValue(value.Microseconds())
		}
	case policyir.ValueDateTime:
		if value, ok := raw.(time.Time); ok {
			return policyir.NewDateTimeValue(value.Unix(), uint32(value.Nanosecond()))
		}
	case policyir.ValueEnum:
		if v.Kind() == reflect.String {
			logical, ok := registry.Field(expression.Model, expression.Field)
			if !ok || logical.LogicalType().EnumID == nil {
				break
			}
			member, ok := registry.EnumValue(*logical.LogicalType().EnumID, v.String())
			if !ok {
				break
			}
			enumBytes, err := hex.DecodeString(string(*logical.LogicalType().EnumID))
			if err != nil || len(enumBytes) != 16 {
				return policyir.Value{}, fmt.Errorf("invalid enum identity")
			}
			memberBytes, err := hex.DecodeString(string(member))
			if err != nil || len(memberBytes) != 16 {
				return policyir.Value{}, fmt.Errorf("invalid enum member identity")
			}
			var enum policyir.EnumID
			var item policyir.EnumValueID
			copy(enum[:], enumBytes)
			copy(item[:], memberBytes)
			return policyir.NewEnumValue(enum, item)
		}
	}
	return policyir.Value{}, fmt.Errorf("operand %T does not match field type %d", raw, typ.Kind())
}
func hasSelectedField(values []golem.FrozenScopedExpression) bool {
	for _, value := range values {
		if value.Kind == golem.ScopedExpressionField {
			return true
		}
	}
	return false
}
func expressionKey(value golem.FrozenScopedExpression) string {
	return fmt.Sprintf("%d:%x:%x:%d", value.Occurrence, value.Model, value.Field, value.Kind)
}
func sortedFields(values map[golem.FieldID]schema.Field) []golem.FieldID {
	result := make([]golem.FieldID, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && string(result[j][:]) < string(result[j-1][:]); j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

type Column struct {
	Expression golem.FrozenScopedExpression
	Logical    compilerir.LogicalTypeIR
}
type Statement struct {
	sql                   string
	args                  []any
	columns               []Column
	guardColumns          bool
	guardRowColumn        bool
	maxContributionRows   int
	maxIntermediateGroups int
	maxResultRows         int
}

type RenderOptions struct {
	MaxContributionRows   int
	MaxIntermediateGroups int
	MaxResultRows         int
}

func (statement Statement) SQL() string       { return statement.sql }
func (statement Statement) Args() []any       { return append([]any(nil), statement.args...) }
func (statement Statement) Columns() []Column { return append([]Column(nil), statement.columns...) }
func (statement Statement) ScanColumnCount() int {
	if statement.guardColumns {
		count := len(statement.columns) + 2
		if statement.guardRowColumn {
			count++
		}
		return count
	}
	return len(statement.columns)
}
func (statement Statement) Guarded() bool     { return statement.guardColumns }
func (statement Statement) HasGuardRow() bool { return statement.guardRowColumn }
func (statement Statement) LimitOverflow(contributions, intermediates int64, results int) string {
	if statement.maxContributionRows > 0 && contributions > int64(statement.maxContributionRows) {
		return "scoped contribution limit exceeded"
	}
	if statement.maxIntermediateGroups > 0 && intermediates > int64(statement.maxIntermediateGroups) {
		return "scoped intermediate group limit exceeded"
	}
	if statement.maxResultRows > 0 && results > statement.maxResultRows {
		return "scoped result limit exceeded"
	}
	return ""
}

func Render(plan Plan, registry *schema.Registry, provider policyir.Provider, capabilities policysql.CapabilityProof, options ...RenderOptions) (Statement, error) {
	if len(options) > 1 || len(options) == 1 && (options[0].MaxContributionRows < 0 || options[0].MaxIntermediateGroups < 0 || options[0].MaxResultRows < 0) {
		return Statement{}, fmt.Errorf("P6_SCOPED_SQL_LIMIT: invalid render options")
	}
	limits := RenderOptions{}
	if len(options) == 1 {
		limits = options[0]
	}
	dialect, external, err := scopedDialect(provider)
	if err != nil {
		return Statement{}, err
	}
	resolver := policysql.SchemaResolver(registry)
	aliases := map[uint32]physical.PhysicalName{0: "golem_s0"}
	for _, join := range plan.request.Joins() {
		aliases[join.Occurrence] = physical.PhysicalName(fmt.Sprintf("golem_s%d", join.Occurrence))
	}
	args := []any{}
	compilePolicy := func(item occurrence, alias physical.PhysicalName) (string, error) {
		fragment, compileErr := policysql.Compile(policysql.Request{Condition: item.authorized.Where(), Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: alias})
		if compileErr != nil {
			return "", compileErr
		}
		value := rebase(fragment.SQL(), len(args), provider)
		args = append(args, fragment.Args()...)
		return value, nil
	}
	useContributionCTE := limits.MaxContributionRows > 0
	contributionAlias := physical.PhysicalName("golem_scoped_contributions")
	contributionProjection := []string{}
	contributionProjectionNames := []string{}
	contributionColumns := map[string]physical.PhysicalName{}
	sourceExpression := func(value golem.FrozenScopedExpression) (string, error) {
		return renderExpression(value, plan, registry, provider, external, dialect, capabilities, aliases, &args)
	}
	expression := func(value golem.FrozenScopedExpression) (string, error) {
		if useContributionCTE && value.Kind != golem.ScopedExpressionCountAll {
			baseKey := fmt.Sprintf("%d:%x:%x", value.Occurrence, value.Model, value.Field)
			aliasName, present := contributionColumns[baseKey]
			field := plan.occurrences[value.Occurrence].fields[value.Field]
			if !present {
				column, descriptor, renderErr := renderScopedRawColumn(value, plan, registry, provider, external, dialect, capabilities, aliases, &args)
				if renderErr != nil {
					return "", renderErr
				}
				field = descriptor
				aliasName = physical.PhysicalName(fmt.Sprintf("golem_c%d", len(contributionProjection)))
				contributionColumns[baseKey] = aliasName
				contributionProjection = append(contributionProjection, column+" AS "+dialect.Quote(aliasName))
				contributionProjectionNames = append(contributionProjectionNames, string(aliasName))
			}
			column := dialect.Quote(contributionAlias) + "." + dialect.Quote(aliasName)
			return renderScopedExpressionFromColumn(value, field, column, provider)
		}
		return sourceExpression(value)
	}
	selects := make([]string, len(plan.request.Selections()))
	columns := make([]Column, len(selects))
	selectedAliases := map[string]string{}
	for index, selected := range plan.request.Selections() {
		value, renderErr := expression(selected)
		if renderErr != nil {
			return Statement{}, renderErr
		}
		alias := fmt.Sprintf("golem_o%d", index)
		selects[index] = value + " AS " + dialect.Quote(physical.PhysicalName(alias))
		selectedAliases[expressionKey(selected)] = alias
		logical := compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}
		if selected.Kind != golem.ScopedExpressionCountAll {
			logical = plan.occurrences[selected.Occurrence].fields[selected.Field].LogicalType()
		}
		columns[index] = Column{Expression: selected, Logical: logical}
	}
	take, takePresent := plan.request.Take()
	reversePage := takePresent && take < 0
	type orderSpec struct {
		expression golem.FrozenScopedExpression
		sql, alias string
		descending bool
	}
	orderSpecs := []orderSpec{}
	allAliases := make([]string, len(columns))
	for index := range columns {
		allAliases[index] = fmt.Sprintf("golem_o%d", index)
	}
	ordered := map[string]bool{}
	for index, order := range plan.request.Orders() {
		rendered, renderErr := expression(order.Expression)
		if renderErr != nil {
			return Statement{}, renderErr
		}
		key := expressionKey(order.Expression)
		alias := selectedAliases[key]
		if alias == "" {
			alias = fmt.Sprintf("golem_private_order_%d", index)
			selects = append(selects, rendered+" AS "+dialect.Quote(physical.PhysicalName(alias)))
			allAliases = append(allAliases, alias)
			selectedAliases[key] = alias
		}
		orderSpecs = append(orderSpecs, orderSpec{expression: order.Expression, sql: rendered, alias: alias, descending: order.Descending})
		ordered[key] = true
	}
	for _, group := range plan.request.GroupBy() {
		key := expressionKey(group)
		if ordered[key] {
			continue
		}
		rendered, renderErr := expression(group)
		if renderErr != nil {
			return Statement{}, renderErr
		}
		alias := selectedAliases[key]
		if alias == "" {
			alias = fmt.Sprintf("golem_private_order_%d", len(orderSpecs))
			selects = append(selects, rendered+" AS "+dialect.Quote(physical.PhysicalName(alias)))
			allAliases = append(allAliases, alias)
			selectedAliases[key] = alias
		}
		orderSpecs = append(orderSpecs, orderSpec{expression: group, sql: rendered, alias: alias})
	}
	if having, present := plan.request.Having(); present {
		havingExpressions := []golem.FrozenScopedExpression{}
		walkPredicateExpressions(having, func(value golem.FrozenScopedExpression) { havingExpressions = append(havingExpressions, value) })
		for _, value := range havingExpressions {
			key := expressionKey(value)
			if selectedAliases[key] != "" {
				continue
			}
			rendered, renderErr := expression(value)
			if renderErr != nil {
				return Statement{}, renderErr
			}
			alias := fmt.Sprintf("golem_private_having_%d", len(allAliases))
			selects = append(selects, rendered+" AS "+dialect.Quote(physical.PhysicalName(alias)))
			selectedAliases[key] = alias
			allAliases = append(allAliases, alias)
		}
	}
	guardColumns := limits.MaxContributionRows > 0 || limits.MaxIntermediateGroups > 0
	if guardColumns {
		if len(plan.request.GroupBy()) != 0 {
			contributionCount := "0"
			if useContributionCTE {
				contributionCount = "MAX(" + dialect.Quote(contributionAlias) + "." + dialect.Quote("golem_contribution_count") + ")"
			}
			selects = append(selects, contributionCount+" AS "+dialect.Quote("golem_contribution_count"))
		} else if scopedSelectionsAggregate(plan.request.Selections()) {
			contributionCount := "COUNT(*)"
			if useContributionCTE {
				contributionCount = "COALESCE(MAX(" + dialect.Quote(contributionAlias) + "." + dialect.Quote("golem_contribution_count") + "), 0)"
			}
			selects = append(selects, contributionCount+" AS "+dialect.Quote("golem_contribution_count"), "1 AS "+dialect.Quote("golem_intermediate_count"))
		} else {
			contributionCount := "COUNT(*) OVER ()"
			if useContributionCTE {
				contributionCount = "MAX(" + dialect.Quote(contributionAlias) + "." + dialect.Quote("golem_contribution_count") + ") OVER ()"
			}
			// An ungrouped row query has no intermediate groups. Its source
			// cardinality is guarded independently by the contribution count.
			selects = append(selects, contributionCount+" AS "+dialect.Quote("golem_contribution_count"), "0 AS "+dialect.Quote("golem_intermediate_count"))
		}
	}
	rootPhysical, ok := resolver.Model(provider, policyir.ModelID(plan.request.RootModelID()))
	if !ok {
		return Statement{}, fmt.Errorf("P6_SCOPED_SQL_MODEL: root physical model is absent")
	}
	fromText := dialect.Table(rootPhysical) + " AS " + dialect.Quote(aliases[0])
	for _, join := range plan.request.Joins() {
		item := plan.occurrences[join.Occurrence]
		parent := plan.occurrences[join.ParentOccurrence]
		target, present := resolver.Model(provider, policyir.ModelID(join.Model))
		if !present {
			return Statement{}, fmt.Errorf("P6_SCOPED_SQL_JOIN: target model absent")
		}
		conditions := []string{}
		for _, pair := range item.endpoint.Correlation() {
			left, lok := registry.PhysicalField(external, parent.model, pair.ParentFieldID())
			right, rok := registry.PhysicalField(external, item.model, pair.ChildFieldID())
			if !lok || !rok {
				return Statement{}, fmt.Errorf("P6_SCOPED_SQL_JOIN: correlation field absent")
			}
			conditions = append(conditions, dialect.Quote(aliases[join.Occurrence])+"."+dialect.Quote(right.ColumnName())+" = "+dialect.Quote(aliases[join.ParentOccurrence])+"."+dialect.Quote(left.ColumnName()))
		}
		policy, policyErr := compilePolicy(item, aliases[join.Occurrence])
		if policyErr != nil {
			return Statement{}, policyErr
		}
		// The policy compiler returns a complete Boolean expression. Preserve it
		// as one JOIN operand: an unwrapped OR would otherwise escape the
		// correlation equality through SQL's AND-before-OR precedence.
		conditions = append(conditions, "("+policy+")")
		keyword := " INNER JOIN "
		if join.Kind == golem.ScopedLeftJoin {
			keyword = " LEFT JOIN "
		}
		fromText += keyword + dialect.Table(target) + " AS " + dialect.Quote(aliases[join.Occurrence]) + " ON (" + strings.Join(conditions, " AND ") + ")"
	}
	rootPolicy, err := compilePolicy(plan.occurrences[0], aliases[0])
	if err != nil {
		return Statement{}, err
	}
	where := []string{rootPolicy}
	if predicate, present := plan.request.Where(); present {
		rendered, renderErr := renderPredicate(predicate, sourceExpression, plan, registry, provider, external, dialect, capabilities, aliases, &args)
		if renderErr != nil {
			return Statement{}, renderErr
		}
		where = append(where, rendered)
	}
	fromText += " WHERE " + strings.Join(where, " AND ")
	contributionCTE := ""
	text := "SELECT " + strings.Join(selects, ", ") + " FROM " + fromText
	if useContributionCTE {
		if len(contributionProjection) == 0 {
			contributionProjection = append(contributionProjection, "1 AS "+dialect.Quote("golem_unit"))
			contributionProjectionNames = append(contributionProjectionNames, "golem_unit")
		}
		args = append(args, int64(limits.MaxContributionRows+1))
		limitedAlias := physical.PhysicalName("golem_limited_scoped_contributions")
		limited := "SELECT " + strings.Join(contributionProjection, ", ") + " FROM " + fromText + " LIMIT " + dialect.Placeholder(len(args))
		projection := make([]string, 0, len(contributionProjectionNames)+1)
		for _, name := range contributionProjectionNames {
			quoted := dialect.Quote(physical.PhysicalName(name))
			projection = append(projection, dialect.Quote(limitedAlias)+"."+quoted+" AS "+quoted)
		}
		projection = append(projection, "COUNT(*) OVER () AS "+dialect.Quote("golem_contribution_count"))
		contributionCTE = "SELECT " + strings.Join(projection, ", ") + " FROM (" + limited + ") AS " + dialect.Quote(limitedAlias)
		text = "SELECT " + strings.Join(selects, ", ") + " FROM " + dialect.Quote(contributionAlias) + " AS " + dialect.Quote(contributionAlias)
	}
	groups := plan.request.GroupBy()
	if len(groups) != 0 {
		values := make([]string, len(groups))
		for i, group := range groups {
			values[i], err = expression(group)
			if err != nil {
				return Statement{}, err
			}
		}
		text += " GROUP BY " + strings.Join(values, ", ")
	}
	groupedCTE := ""
	if len(groups) != 0 && guardColumns {
		groupedCore := text
		if limits.MaxIntermediateGroups > 0 {
			args = append(args, int64(limits.MaxIntermediateGroups+1))
			groupedCore += " LIMIT " + dialect.Placeholder(len(args))
		}
		limitedGroups := physical.PhysicalName("golem_limited_scoped_groups")
		groupedCTE = "SELECT " + dialect.Quote(limitedGroups) + ".*, COUNT(*) OVER () AS " + dialect.Quote("golem_intermediate_count") + " FROM (" + groupedCore + ") AS " + dialect.Quote(limitedGroups)
		groupAlias := physical.PhysicalName("golem_grouped")
		projection := make([]string, 0, len(allAliases)+2)
		for _, name := range allAliases {
			quoted := dialect.Quote(physical.PhysicalName(name))
			projection = append(projection, dialect.Quote(groupAlias)+"."+quoted+" AS "+quoted)
		}
		for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count"} {
			quoted := dialect.Quote(name)
			projection = append(projection, dialect.Quote(groupAlias)+"."+quoted+" AS "+quoted)
		}
		text = "SELECT " + strings.Join(projection, ", ") + " FROM " + dialect.Quote("golem_groups") + " AS " + dialect.Quote(groupAlias)
		if having, present := plan.request.Having(); present {
			aliasExpression := func(value golem.FrozenScopedExpression) (string, error) {
				name := selectedAliases[expressionKey(value)]
				if name == "" {
					return "", fmt.Errorf("P6_SCOPED_SQL_ALIAS: having expression has no alias")
				}
				return dialect.Quote(groupAlias) + "." + dialect.Quote(physical.PhysicalName(name)), nil
			}
			rendered, renderErr := renderPredicate(having, aliasExpression, plan, registry, provider, external, dialect, capabilities, aliases, &args)
			if renderErr != nil {
				return Statement{}, renderErr
			}
			text += " WHERE " + rendered
		}
		for index := range orderSpecs {
			orderSpecs[index].sql = dialect.Quote(groupAlias) + "." + dialect.Quote(physical.PhysicalName(orderSpecs[index].alias))
		}
	} else if having, present := plan.request.Having(); present {
		rendered, renderErr := renderPredicate(having, expression, plan, registry, provider, external, dialect, capabilities, aliases, &args)
		if renderErr != nil {
			return Statement{}, renderErr
		}
		text += " HAVING " + rendered
	}
	if len(orderSpecs) != 0 {
		values := make([]string, len(orderSpecs))
		for i, order := range orderSpecs {
			descending := order.descending
			if reversePage {
				descending = !descending
			}
			values[i] = scopedOrderOperand(order.sql, order.expression, plan, provider)
			if descending {
				values[i] += " DESC"
			} else {
				values[i] += " ASC"
			}
		}
		text += " ORDER BY " + strings.Join(values, ", ")
	}
	if takePresent {
		if take < 0 {
			take = -take
		}
		args = append(args, int64(take))
		text += " LIMIT " + dialect.Placeholder(len(args))
	} else if limits.MaxResultRows > 0 {
		args = append(args, int64(limits.MaxResultRows+1))
		text += " LIMIT " + dialect.Placeholder(len(args))
	}
	if skip, present := plan.request.Skip(); present {
		args = append(args, int64(skip))
		text += " OFFSET " + dialect.Placeholder(len(args))
	}
	if reversePage {
		if len(orderSpecs) == 0 {
			return Statement{}, fmt.Errorf("P6_SCOPED_SQL_ORDER: negative take requires deterministic order")
		}
		pageAlias := physical.PhysicalName("golem_page")
		public := make([]string, len(allAliases))
		for index, name := range allAliases {
			alias := dialect.Quote(physical.PhysicalName(name))
			public[index] = dialect.Quote(pageAlias) + "." + alias + " AS " + alias
		}
		if guardColumns {
			for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count"} {
				alias := dialect.Quote(name)
				public = append(public, dialect.Quote(pageAlias)+"."+alias+" AS "+alias)
			}
		}
		outerOrders := make([]string, len(orderSpecs))
		for index, order := range orderSpecs {
			if order.alias == "" {
				return Statement{}, fmt.Errorf("P6_SCOPED_SQL_ORDER: reverse order expression has no alias")
			}
			value := dialect.Quote(pageAlias) + "." + dialect.Quote(physical.PhysicalName(order.alias))
			value = scopedOrderOperand(value, order.expression, plan, provider)
			if order.descending {
				value += " DESC"
			} else {
				value += " ASC"
			}
			outerOrders[index] = value
		}
		text = "SELECT " + strings.Join(public, ", ") + " FROM (" + text + ") AS " + dialect.Quote(pageAlias) + " ORDER BY " + strings.Join(outerOrders, ", ")
	}
	guardRowColumn := false
	if groupedCTE != "" {
		visibleAlias := physical.PhysicalName("golem_visible")
		page := make([]string, 0, len(allAliases)+4)
		for _, aliasName := range allAliases {
			name := physical.PhysicalName(aliasName)
			quoted := dialect.Quote(name)
			page = append(page, dialect.Quote(visibleAlias)+"."+quoted+" AS "+quoted)
		}
		for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count"} {
			quoted := dialect.Quote(name)
			page = append(page, dialect.Quote(visibleAlias)+"."+quoted+" AS "+quoted)
		}
		pageOrders := make([]string, len(orderSpecs))
		for index, order := range orderSpecs {
			value := dialect.Quote(visibleAlias) + "." + dialect.Quote(physical.PhysicalName(order.alias))
			value = scopedOrderOperand(value, order.expression, plan, provider)
			if order.descending {
				value += " DESC"
			} else {
				value += " ASC"
			}
			pageOrders[index] = value
		}
		if len(pageOrders) == 0 {
			pageOrders = []string{"1"}
		}
		page = append(page, "ROW_NUMBER() OVER (ORDER BY "+strings.Join(pageOrders, ", ")+") AS "+dialect.Quote("golem_page_ordinal"), "0 AS "+dialect.Quote("golem_guard_row"))
		pageSQL := "SELECT " + strings.Join(page, ", ") + " FROM (" + text + ") AS " + dialect.Quote(visibleAlias)
		sentinel := make([]string, 0, len(allAliases)+4)
		for _, name := range allAliases {
			sentinel = append(sentinel, "NULL AS "+dialect.Quote(physical.PhysicalName(name)))
		}
		groupsAlias := physical.PhysicalName("golem_guard_groups")
		for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count"} {
			quoted := dialect.Quote(name)
			sentinel = append(sentinel, "COALESCE(MAX("+dialect.Quote(groupsAlias)+"."+quoted+"), 0) AS "+quoted)
		}
		sentinel = append(sentinel, "0 AS "+dialect.Quote("golem_page_ordinal"), "1 AS "+dialect.Quote("golem_guard_row"))
		sentinelSQL := "SELECT " + strings.Join(sentinel, ", ") + " FROM " + dialect.Quote("golem_groups") + " AS " + dialect.Quote(groupsAlias)
		combined := physical.PhysicalName("golem_guarded")
		final := make([]string, 0, len(columns)+3)
		for index := range columns {
			name := physical.PhysicalName(fmt.Sprintf("golem_o%d", index))
			quoted := dialect.Quote(name)
			final = append(final, dialect.Quote(combined)+"."+quoted+" AS "+quoted)
		}
		for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count", "golem_guard_row"} {
			quoted := dialect.Quote(name)
			final = append(final, dialect.Quote(combined)+"."+quoted+" AS "+quoted)
		}
		ctes := []string{}
		if contributionCTE != "" {
			ctes = append(ctes, dialect.Quote(contributionAlias)+" AS MATERIALIZED ("+contributionCTE+")")
		}
		ctes = append(ctes, dialect.Quote("golem_groups")+" AS MATERIALIZED ("+groupedCTE+")")
		text = "WITH " + strings.Join(ctes, ", ") + " SELECT " + strings.Join(final, ", ") + " FROM (" + pageSQL + " UNION ALL " + sentinelSQL + ") AS " + dialect.Quote(combined) + " ORDER BY " + dialect.Quote(combined) + "." + dialect.Quote("golem_guard_row") + " ASC, " + dialect.Quote(combined) + "." + dialect.Quote("golem_page_ordinal") + " ASC"
		guardRowColumn = true
	} else if guardColumns {
		// Paging, including a large skip, and an aggregate HAVING clause may
		// remove every public row. Always union a private guard row so source
		// overflow cannot be hidden by the final result shape.
		visibleAlias := physical.PhysicalName("golem_visible")
		page := make([]string, 0, len(allAliases)+4)
		for _, aliasName := range allAliases {
			name := physical.PhysicalName(aliasName)
			quoted := dialect.Quote(name)
			page = append(page, dialect.Quote(visibleAlias)+"."+quoted+" AS "+quoted)
		}
		for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count"} {
			quoted := dialect.Quote(name)
			page = append(page, dialect.Quote(visibleAlias)+"."+quoted+" AS "+quoted)
		}
		pageOrders := make([]string, len(orderSpecs))
		for index, order := range orderSpecs {
			value := dialect.Quote(visibleAlias) + "." + dialect.Quote(physical.PhysicalName(order.alias))
			value = scopedOrderOperand(value, order.expression, plan, provider)
			if order.descending {
				value += " DESC"
			} else {
				value += " ASC"
			}
			pageOrders[index] = value
		}
		if len(pageOrders) == 0 {
			pageOrders = []string{"1"}
		}
		page = append(page, "ROW_NUMBER() OVER (ORDER BY "+strings.Join(pageOrders, ", ")+") AS "+dialect.Quote("golem_page_ordinal"), "0 AS "+dialect.Quote("golem_guard_row"))
		pageSQL := "SELECT " + strings.Join(page, ", ") + " FROM (" + text + ") AS " + dialect.Quote(visibleAlias)

		sentinel := make([]string, 0, len(allAliases)+4)
		for _, name := range allAliases {
			sentinel = append(sentinel, "NULL AS "+dialect.Quote(physical.PhysicalName(name)))
		}
		contributionCount := "0"
		sentinelFrom := ""
		if contributionCTE != "" {
			guardSource := physical.PhysicalName("golem_guard_contributions")
			contributionCount = "COALESCE(MAX(" + dialect.Quote(guardSource) + "." + dialect.Quote("golem_contribution_count") + "), 0)"
			sentinelFrom = " FROM " + dialect.Quote(contributionAlias) + " AS " + dialect.Quote(guardSource)
		}
		intermediateCount := "0"
		if scopedSelectionsAggregate(plan.request.Selections()) {
			intermediateCount = "1"
		}
		sentinel = append(sentinel,
			contributionCount+" AS "+dialect.Quote("golem_contribution_count"),
			intermediateCount+" AS "+dialect.Quote("golem_intermediate_count"),
			"0 AS "+dialect.Quote("golem_page_ordinal"),
			"1 AS "+dialect.Quote("golem_guard_row"),
		)
		sentinelSQL := "SELECT " + strings.Join(sentinel, ", ") + sentinelFrom

		combined := physical.PhysicalName("golem_guarded")
		final := make([]string, 0, len(columns)+3)
		for index := range columns {
			name := physical.PhysicalName(fmt.Sprintf("golem_o%d", index))
			quoted := dialect.Quote(name)
			final = append(final, dialect.Quote(combined)+"."+quoted+" AS "+quoted)
		}
		for _, name := range []physical.PhysicalName{"golem_contribution_count", "golem_intermediate_count", "golem_guard_row"} {
			quoted := dialect.Quote(name)
			final = append(final, dialect.Quote(combined)+"."+quoted+" AS "+quoted)
		}
		prefix := ""
		if contributionCTE != "" {
			prefix = "WITH " + dialect.Quote(contributionAlias) + " AS MATERIALIZED (" + contributionCTE + ") "
		}
		text = prefix + "SELECT " + strings.Join(final, ", ") + " FROM (" + pageSQL + " UNION ALL " + sentinelSQL + ") AS " + dialect.Quote(combined) + " ORDER BY " + dialect.Quote(combined) + "." + dialect.Quote("golem_guard_row") + " ASC, " + dialect.Quote(combined) + "." + dialect.Quote("golem_page_ordinal") + " ASC"
		guardRowColumn = true
	}
	if contributionCTE != "" && groupedCTE == "" && !guardRowColumn {
		text = "WITH " + dialect.Quote(contributionAlias) + " AS MATERIALIZED (" + contributionCTE + ") " + text
	}
	return Statement{sql: text, args: args, columns: columns, guardColumns: guardColumns, guardRowColumn: guardRowColumn, maxContributionRows: limits.MaxContributionRows, maxIntermediateGroups: limits.MaxIntermediateGroups, maxResultRows: limits.MaxResultRows}, nil
}

func scopedSelectionsAggregate(values []golem.FrozenScopedExpression) bool {
	for _, value := range values {
		if value.Kind != golem.ScopedExpressionField {
			return true
		}
	}
	return false
}

func scopedOrderOperand(value string, expression golem.FrozenScopedExpression, plan Plan, provider policyir.Provider) string {
	if expression.Field == (golem.FieldID{}) {
		return value
	}
	field, present := plan.occurrences[expression.Occurrence].fields[expression.Field]
	if !present || !exactScopedNumeric(expression, field) {
		return value
	}
	if provider == policyir.ProviderPostgreSQL {
		return "CAST(" + value + " AS NUMERIC)"
	}
	return "(" + value + " COLLATE " + sqliteprovider.AnalyticsNumericCollation + ")"
}

func renderExpression(value golem.FrozenScopedExpression, plan Plan, registry *schema.Registry, provider policyir.Provider, external golem.Provider, dialect policysql.Dialect, capabilities policysql.CapabilityProof, aliases map[uint32]physical.PhysicalName, args *[]any) (string, error) {
	if value.Kind == golem.ScopedExpressionCountAll {
		return "COUNT(*)", nil
	}
	column, field, err := renderScopedRawColumn(value, plan, registry, provider, external, dialect, capabilities, aliases, args)
	if err != nil {
		return "", err
	}
	return renderScopedExpressionFromColumn(value, field, column, provider)
}

func renderScopedRawColumn(value golem.FrozenScopedExpression, plan Plan, registry *schema.Registry, provider policyir.Provider, external golem.Provider, dialect policysql.Dialect, capabilities policysql.CapabilityProof, aliases map[uint32]physical.PhysicalName, args *[]any) (string, schema.Field, error) {
	item, present := plan.occurrences[value.Occurrence]
	if !present {
		return "", schema.Field{}, fmt.Errorf("P6_SCOPED_SQL_EXPRESSION: occurrence absent")
	}
	field, present := item.fields[value.Field]
	if !present {
		return "", schema.Field{}, fmt.Errorf("P6_SCOPED_SQL_EXPRESSION: field absent")
	}
	physicalField, present := registry.PhysicalField(external, value.Model, value.Field)
	if !present {
		return "", schema.Field{}, fmt.Errorf("P6_SCOPED_SQL_EXPRESSION: physical field absent")
	}
	column := dialect.Quote(aliases[value.Occurrence]) + "." + dialect.Quote(physicalField.ColumnName())
	if classified, ok := item.classified[value.Field]; ok {
		if mask, masked := classified.Mask(); masked && !classified.DischargedByConstraint() {
			resolver := policysql.SchemaResolver(registry)
			fragment, err := policysql.Compile(policysql.Request{Condition: mask, Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: aliases[value.Occurrence]})
			if err != nil {
				return "", schema.Field{}, err
			}
			maskSQL := rebase(fragment.SQL(), len(*args), provider)
			*args = append(*args, fragment.Args()...)
			column = "CASE WHEN " + maskSQL + " THEN " + column + " ELSE NULL END"
		}
	}
	return column, field, nil
}

func renderScopedExpressionFromColumn(value golem.FrozenScopedExpression, field schema.Field, column string, provider policyir.Provider) (string, error) {
	switch value.Kind {
	case golem.ScopedExpressionField:
		if field.LogicalType().Kind == compilerir.TypeString {
			if provider == policyir.ProviderPostgreSQL {
				return "(" + column + " COLLATE \"C\")", nil
			}
			return "(" + column + " COLLATE BINARY)", nil
		}
		return column, nil
	case golem.ScopedExpressionCountField:
		return "COUNT(" + column + ")", nil
	case golem.ScopedExpressionSum:
		kind := field.LogicalType().Kind
		if kind == compilerir.TypeInt16 || kind == compilerir.TypeInt32 || kind == compilerir.TypeInt64 {
			if provider == policyir.ProviderSQLite {
				return sqliteprovider.AnalyticsIntegerSumFunction + "(" + column + ")", nil
			}
			return "SUM(" + column + ")::text", nil
		}
		if kind == compilerir.TypeDecimal {
			scale := uint16(0)
			if field.LogicalType().Scale != nil {
				scale = *field.LogicalType().Scale
			}
			if provider == policyir.ProviderSQLite {
				return fmt.Sprintf("%s(%s, %d)", sqliteprovider.AnalyticsDecimalSumFunction, column, scale), nil
			}
			return "SUM(" + column + ")::text", nil
		}
		return "SUM(" + column + ")", nil
	case golem.ScopedExpressionAverage:
		if field.LogicalType().Kind == compilerir.TypeDecimal {
			scale := uint16(0)
			if field.LogicalType().Scale != nil {
				scale = *field.LogicalType().Scale
			}
			if provider == policyir.ProviderSQLite {
				return fmt.Sprintf("%s(%s, %d)", sqliteprovider.AnalyticsDecimalAvgFunction, column, scale), nil
			}
			return fmt.Sprintf("ROUND(AVG(%s), %d)::text", column, scale), nil
		}
		return "AVG(" + column + ")", nil
	case golem.ScopedExpressionMinimum:
		if field.LogicalType().Kind == compilerir.TypeString {
			if provider == policyir.ProviderPostgreSQL {
				column = "(" + column + " COLLATE \"C\")"
			} else {
				column = "(" + column + " COLLATE BINARY)"
			}
		}
		return "MIN(" + column + ")", nil
	case golem.ScopedExpressionMaximum:
		if field.LogicalType().Kind == compilerir.TypeString {
			if provider == policyir.ProviderPostgreSQL {
				column = "(" + column + " COLLATE \"C\")"
			} else {
				column = "(" + column + " COLLATE BINARY)"
			}
		}
		return "MAX(" + column + ")", nil
	default:
		return "", fmt.Errorf("P6_SCOPED_SQL_EXPRESSION: unsupported kind")
	}
}

func renderPredicate(value golem.FrozenScopedPredicate, expression func(golem.FrozenScopedExpression) (string, error), plan Plan, registry *schema.Registry, provider policyir.Provider, external golem.Provider, dialect policysql.Dialect, capabilities policysql.CapabilityProof, aliases map[uint32]physical.PhysicalName, args *[]any) (string, error) {
	if value.Operator == golem.ScopedPredicateAnd || value.Operator == golem.ScopedPredicateOr {
		parts := make([]string, len(value.Children))
		for i, child := range value.Children {
			rendered, err := renderPredicate(child, expression, plan, registry, provider, external, dialect, capabilities, aliases, args)
			if err != nil {
				return "", err
			}
			parts[i] = "(" + rendered + ")"
		}
		separator := " AND "
		if value.Operator == golem.ScopedPredicateOr {
			separator = " OR "
		}
		return strings.Join(parts, separator), nil
	}
	if value.Operator == golem.ScopedPredicateNot {
		rendered, err := renderPredicate(value.Children[0], expression, plan, registry, provider, external, dialect, capabilities, aliases, args)
		return "NOT (" + rendered + ")", err
	}
	left, err := expression(value.Expression)
	if err != nil {
		return "", err
	}
	if value.Operator == golem.ScopedPredicateIsNull {
		return left + " IS NULL", nil
	}
	if value.Operator == golem.ScopedPredicateIsNotNull {
		return left + " IS NOT NULL", nil
	}
	bind := func(raw any) (string, error) {
		if value.Expression.Kind == golem.ScopedExpressionCountAll {
			integer := reflect.ValueOf(raw)
			if !integer.IsValid() || integer.Kind() < reflect.Int || integer.Kind() > reflect.Int64 {
				return "", fmt.Errorf("P6_SCOPED_BIND: count operand %T is not an integer", raw)
			}
			*args = append(*args, integer.Int())
			return dialect.Placeholder(len(*args)), nil
		}
		field := plan.occurrences[value.Expression.Occurrence].fields[value.Expression.Field]
		if exactScopedNumeric(value.Expression, field) {
			text, ok := raw.(fmt.Stringer)
			if !ok {
				return "", fmt.Errorf("P6_SCOPED_BIND: exact aggregate operand %T is not canonical numeric", raw)
			}
			*args = append(*args, text.String())
			return dialect.Placeholder(len(*args)), nil
		}
		encoded, encodeErr := encodeScopedValue(raw, field, registry, provider)
		if encodeErr != nil {
			return "", encodeErr
		}
		*args = append(*args, encoded)
		return dialect.Placeholder(len(*args)), nil
	}
	if value.Operator == golem.ScopedPredicateIn || value.Operator == golem.ScopedPredicateNotIn {
		if len(value.Values) == 0 {
			if value.Operator == golem.ScopedPredicateNotIn {
				return "TRUE", nil
			}
			return "FALSE", nil
		}
		values := make([]string, len(value.Values))
		for i, raw := range value.Values {
			values[i], err = bind(raw)
			if err != nil {
				return "", err
			}
		}
		keyword := " IN "
		if value.Operator == golem.ScopedPredicateNotIn {
			keyword = " NOT IN "
		}
		logical := plan.occurrences[value.Expression.Occurrence].fields[value.Expression.Field].LogicalType().Kind
		if logical == compilerir.TypeString || logical == compilerir.TypeEnum {
			if provider == policyir.ProviderPostgreSQL {
				left = "(" + left + " COLLATE \"C\")"
			} else {
				left = "(" + left + " COLLATE BINARY)"
			}
		}
		return left + keyword + "(" + strings.Join(values, ",") + ")", nil
	}
	if len(value.Values) != 1 {
		return "", fmt.Errorf("P6_SCOPED_SQL_PREDICATE: comparison arity")
	}
	right, err := bind(value.Values[0])
	if err != nil {
		return "", err
	}
	logical := compilerir.TypeInt64
	if value.Expression.Kind != golem.ScopedExpressionCountAll {
		logical = plan.occurrences[value.Expression.Occurrence].fields[value.Expression.Field].LogicalType().Kind
	}
	if exactScopedNumeric(value.Expression, plan.occurrences[value.Expression.Occurrence].fields[value.Expression.Field]) {
		if provider == policyir.ProviderPostgreSQL {
			left, right = "CAST("+left+" AS NUMERIC)", "CAST("+right+" AS NUMERIC)"
		} else {
			left, right = "("+left+" COLLATE "+sqliteprovider.AnalyticsNumericCollation+")", "("+right+" COLLATE "+sqliteprovider.AnalyticsNumericCollation+")"
		}
	}
	if logical == compilerir.TypeString || logical == compilerir.TypeEnum {
		if provider == policyir.ProviderPostgreSQL {
			left = "(" + left + " COLLATE \"C\")"
			right = "(" + right + " COLLATE \"C\")"
		} else {
			left = "(" + left + " COLLATE BINARY)"
			right = "(" + right + " COLLATE BINARY)"
		}
	}
	switch value.Operator {
	case golem.ScopedPredicateEqual:
		return left + " = " + right, nil
	case golem.ScopedPredicateNotEqual:
		return left + " <> " + right, nil
	case golem.ScopedPredicateLessThan:
		return left + " < " + right, nil
	case golem.ScopedPredicateLessThanOrEqual:
		return left + " <= " + right, nil
	case golem.ScopedPredicateGreaterThan:
		return left + " > " + right, nil
	case golem.ScopedPredicateGreaterThanOrEqual:
		return left + " >= " + right, nil
	case golem.ScopedPredicateContains:
		if provider == policyir.ProviderPostgreSQL {
			return "STRPOS(" + left + ", " + right + ") > 0", nil
		}
		return "instr(" + left + ", " + right + ") > 0", nil
	case golem.ScopedPredicateStartsWith:
		if provider == policyir.ProviderPostgreSQL {
			return "LEFT(" + left + ", CHAR_LENGTH(" + right + ")) = " + right, nil
		}
		return "substr(" + left + ", 1, length(" + right + ")) = " + right, nil
	case golem.ScopedPredicateEndsWith:
		if provider == policyir.ProviderPostgreSQL {
			return "RIGHT(" + left + ", CHAR_LENGTH(" + right + ")) = " + right, nil
		}
		return "substr(" + left + ", -length(" + right + ")) = " + right, nil
	}
	return "", fmt.Errorf("P6_SCOPED_SQL_PREDICATE: unsupported operator")
}

func exactScopedNumeric(expression golem.FrozenScopedExpression, field schema.Field) bool {
	kind := field.LogicalType().Kind
	return expression.Kind == golem.ScopedExpressionSum && (kind == compilerir.TypeInt16 || kind == compilerir.TypeInt32 || kind == compilerir.TypeInt64 || kind == compilerir.TypeDecimal) ||
		expression.Kind == golem.ScopedExpressionAverage && kind == compilerir.TypeDecimal
}

func encodeScopedValue(raw any, field schema.Field, registry *schema.Registry, provider policyir.Provider) (any, error) {
	v := reflect.ValueOf(raw)
	if !v.IsValid() {
		return nil, fmt.Errorf("P6_SCOPED_BIND: nil operand")
	}
	kind := field.LogicalType().Kind
	switch kind {
	case compilerir.TypeBool:
		if v.Kind() == reflect.Bool {
			value := v.Bool()
			if provider == policyir.ProviderSQLite {
				if value {
					return int64(1), nil
				}
				return int64(0), nil
			}
			return value, nil
		}
	case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64:
		if v.Kind() >= reflect.Int && v.Kind() <= reflect.Int64 {
			return v.Int(), nil
		}
	case compilerir.TypeFloat32, compilerir.TypeFloat64:
		if v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64 {
			return v.Float(), nil
		}
	case compilerir.TypeString:
		if v.Kind() == reflect.String {
			return v.String(), nil
		}
	case compilerir.TypeEnum:
		if v.Kind() == reflect.String && field.LogicalType().EnumID != nil {
			member, ok := registry.EnumValue(*field.LogicalType().EnumID, v.String())
			if !ok {
				return nil, fmt.Errorf("P6_SCOPED_BIND: unknown enum value")
			}
			wire, ok := registry.EnumLabel(*field.LogicalType().EnumID, member)
			if ok {
				return wire, nil
			}
		}
	case compilerir.TypeDecimal:
		if decimal, ok := raw.(golem.Decimal); ok && provider == policyir.ProviderSQLite {
			target := uint16(0)
			if field.LogicalType().Scale != nil {
				target = *field.LogicalType().Scale
			}
			coefficient, source := decimal.Coefficient(), uint16(decimal.Scale())
			for source < target {
				if coefficient > math.MaxInt64/10 || coefficient < math.MinInt64/10 {
					return nil, fmt.Errorf("P6_SCOPED_BIND: decimal overflow")
				}
				coefficient *= 10
				source++
			}
			if source > target {
				return nil, fmt.Errorf("P6_SCOPED_BIND: decimal scale exceeds field scale")
			}
			return coefficient, nil
		}
		if value, ok := raw.(fmt.Stringer); ok {
			return value.String(), nil
		}
	case compilerir.TypeUUID, compilerir.TypeDate, compilerir.TypeTime:
		if value, ok := raw.(fmt.Stringer); ok {
			return value.String(), nil
		}
	case compilerir.TypeDateTime:
		if value, ok := raw.(time.Time); ok {
			if provider == policyir.ProviderSQLite {
				return value.UnixMicro(), nil
			}
			return value.UTC(), nil
		}
	}
	return nil, fmt.Errorf("P6_SCOPED_BIND: operand %T does not match %s", raw, kind)
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
func scopedDialect(provider policyir.Provider) (policysql.Dialect, golem.Provider, error) {
	switch provider {
	case policyir.ProviderSQLite:
		return sqliteprovider.NewPolicyDialect(), golem.SQLite, nil
	case policyir.ProviderPostgreSQL:
		return postgresprovider.NewPolicyDialect(), golem.PostgreSQL, nil
	default:
		return nil, "", fmt.Errorf("P6_SCOPED_SQL_PROVIDER: unsupported provider")
	}
}
