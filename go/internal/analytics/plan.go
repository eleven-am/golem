package analytics

import (
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/imply"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type PolicySet interface {
	Policy(policyir.ModelID) (policyir.Policy, bool)
}

// FieldAuthorizationError identifies an analytical field only by its public
// logical name and stable identity. Provider details and physical names never
// enter this error, so the runtime can safely preserve the name in its public
// FORBIDDEN response while retaining the underlying reason for trusted logs.
type FieldAuthorizationError struct {
	field       golem.FieldID
	logicalName string
	reason      string
}

func (failure *FieldAuthorizationError) Error() string {
	return fmt.Sprintf("P6_ANALYTICS_PLAN_AUTHORIZATION: analytics field %q %s", failure.logicalName, failure.reason)
}
func (failure *FieldAuthorizationError) FieldID() golem.FieldID { return failure.field }
func (failure *FieldAuthorizationError) LogicalName() string    { return failure.logicalName }

type Plan struct {
	request  golem.FrozenAnalyticsRequest
	read     readplan.Plan
	fields   map[golem.FieldID]schema.Field
	relation []RelationHop
}

type RelationHop struct {
	Endpoint   schema.RelationEndpoint
	Authorized readplan.Plan
}

func (plan Plan) Request() golem.FrozenAnalyticsRequest { return plan.request }
func (plan Plan) AuthorizedRead() readplan.Plan         { return plan.read }
func (plan Plan) Field(id golem.FieldID) (schema.Field, bool) {
	value, ok := plan.fields[id]
	return value, ok
}
func (plan Plan) RelationPath() []RelationHop { return append([]RelationHop(nil), plan.relation...) }

func Caller(request golem.FrozenAnalyticsRequest, registry *schema.Registry, providers policyir.ProviderSet, policies PolicySet, limits readplan.Limits) (Plan, error) {
	return build(request, registry, providers, policies, limits, false)
}
func System(request golem.FrozenAnalyticsRequest, registry *schema.Registry, providers policyir.ProviderSet, limits readplan.Limits) (Plan, error) {
	return build(request, registry, providers, nil, limits, true)
}

func build(request golem.FrozenAnalyticsRequest, registry *schema.Registry, providers policyir.ProviderSet, policies PolicySet, limits readplan.Limits, system bool) (Plan, error) {
	if registry == nil || !providers.Valid() {
		return Plan{}, fmt.Errorf("P6_ANALYTICS_PLAN_INPUT: registry and providers are required")
	}
	model, ok := registry.Model(request.ModelID())
	if !ok {
		return Plan{}, fmt.Errorf("P6_ANALYTICS_PLAN_MODEL: model is absent")
	}
	input := readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(request.ModelID()), Projection: readir.ProjectionSelect}
	if frozen, present := request.Where(); present {
		where, err := policybind.Predicate(frozen, registry, providers)
		if err != nil {
			return Plan{}, fmt.Errorf("P6_ANALYTICS_PLAN_WHERE: %w", err)
		}
		input.Where = &where
	}
	terms := append(request.Dimensions(), request.Measures()...)
	if having, present := request.Having(); present {
		collectHavingTerms(having, &terms)
	}
	for _, order := range request.OrderBy() {
		terms = append(terms, order.Term)
	}
	seen := map[golem.FieldID]bool{}
	fields := map[golem.FieldID]schema.Field{}
	for _, term := range terms {
		if term.Model != request.ModelID() {
			return Plan{}, fmt.Errorf("P6_ANALYTICS_PLAN_MODEL: foreign-model term")
		}
		if term.Operator == golem.AggregateCountAll {
			continue
		}
		if term.RelationName != "" {
			if term.Operator != 0 {
				return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: measures over related models are not supported")
			}
			continue
		}
		field, exists := registry.Field(request.ModelID(), term.Field)
		if !exists || !field.Visible() || field.Kind() == compilerir.FieldRelation {
			return Plan{}, fmt.Errorf("P6_ANALYTICS_PLAN_FIELD: field is absent, hidden, or non-scalar")
		}
		if err := validateTerm(field, term); err != nil {
			return Plan{}, err
		}
		fields[term.Field] = field
		if !seen[term.Field] {
			selection, err := readir.NewScalarSelection(policyir.FieldID(term.Field))
			if err != nil {
				return Plan{}, err
			}
			input.Selection = append(input.Selection, selection)
			seen[term.Field] = true
		}
	}
	dimensions := map[string]bool{}
	for _, term := range request.Dimensions() {
		dimensions[analyticsTermKey(term)] = true
	}
	if having, present := request.Having(); present {
		havingTerms := []golem.FrozenAnalyticsTerm{}
		collectHavingTerms(having, &havingTerms)
		for _, term := range havingTerms {
			if term.Operator == 0 && !dimensions[analyticsTermKey(term)] {
				return Plan{}, fmt.Errorf("P6_ANALYTICS_HAVING: dimension is absent from the group key")
			}
		}
	}
	for _, order := range request.OrderBy() {
		if order.Term.Operator == 0 && !dimensions[analyticsTermKey(order.Term)] {
			return Plan{}, fmt.Errorf("P6_ANALYTICS_ORDER: ordered dimension is absent from the group key")
		}
	}
	var relationPath []golem.RelationID
	var relationTerms []golem.FrozenAnalyticsTerm
	var relationEndpoints []schema.RelationEndpoint
	if request.Operation() == golem.AnalyticsRelationGroupBy {
		analytics, present := model.Analytics()
		if !present {
			return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: model has no configured relation dimensions")
		}
		allowed := map[string]compilerir.RelationDimensionContractIR{}
		for _, dimension := range analytics.RelationDimensions {
			allowed[dimension.Name] = dimension
		}
		for _, term := range request.Dimensions() {
			if term.RelationName == "" {
				continue
			}
			declared, exists := allowed[term.RelationName]
			if !exists {
				return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: relation dimension is not configured")
			}
			path, convertErr := relationIDs(declared.Path)
			if convertErr != nil {
				return Plan{}, convertErr
			}
			if !sameRelationPath(path, term.RelationPath) {
				return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: relation dimension path does not match contract")
			}
			if relationPath == nil {
				relationPath = path
			} else if !sameRelationPath(relationPath, path) {
				return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: dimensions use different paths")
			}
			terminal, convertErr := publicFieldID(declared.TerminalField)
			if convertErr != nil || terminal != term.Field {
				return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: terminal identity does not match contract")
			}
			relationTerms = append(relationTerms, term)
		}
		if len(relationTerms) == 0 {
			return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: at least one configured relation dimension is required")
		}
		current := request.ModelID()
		for _, relationID := range relationPath {
			endpoint, exists := registry.ForwardToOneRelation(current, relationID)
			if !exists {
				return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: configured hop is not forward to-one")
			}
			relationEndpoints = append(relationEndpoints, endpoint)
			current = endpoint.TargetModelID()
		}
		if len(relationEndpoints) != 0 {
			for _, pair := range relationEndpoints[0].Correlation() {
				fieldID := pair.ParentFieldID()
				field, exists := registry.Field(request.ModelID(), fieldID)
				if !exists || !field.Visible() || field.Kind() == compilerir.FieldRelation {
					return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: root correlation field is unavailable")
				}
				fields[fieldID] = field
				if !seen[fieldID] {
					selection, selectionErr := readir.NewScalarSelection(policyir.FieldID(fieldID))
					if selectionErr != nil {
						return Plan{}, selectionErr
					}
					input.Selection = append(input.Selection, selection)
					seen[fieldID] = true
				}
			}
		}
	}
	if len(input.Selection) == 0 {
		input.Operation = readir.Count
		input.Projection = readir.ProjectionDefault
	}
	bound, err := readir.NewRequest(input)
	if err != nil {
		return Plan{}, fmt.Errorf("P6_ANALYTICS_PLAN_REQUEST: %w", err)
	}
	var authorized readplan.Plan
	if system {
		authorized, err = readplan.System(bound, registry, limits)
	} else {
		authorized, err = readplan.Caller(bound, registry, policies, limits)
	}
	if err != nil {
		return Plan{}, fmt.Errorf("P6_ANALYTICS_PLAN_AUTHORIZATION: %w", err)
	}
	if err := requireDischargedAnalyticsFields(authorized, terms, fields, false); err != nil {
		return Plan{}, err
	}
	if len(relationEndpoints) != 0 {
		rootJoinTerms := correlationTerms(request.ModelID(), relationEndpoints[0].Correlation(), true)
		if err := requireDischargedAnalyticsFields(authorized, rootJoinTerms, fields, false); err != nil {
			return Plan{}, err
		}
	}
	result := Plan{request: request, read: authorized, fields: fields}
	sourcePlan := authorized
	for index, endpoint := range relationEndpoints {
		targetInput := readir.RequestInput{Operation: readir.Count, Model: policyir.ModelID(endpoint.TargetModelID()), Projection: readir.ProjectionDefault}
		inferred, inferredPresent, inferenceErr := inferCorrelatedEqualities(sourcePlan.Where(), endpoint)
		if inferenceErr != nil {
			return Plan{}, inferenceErr
		}
		if inferredPresent {
			targetInput.Where = &inferred
		}
		seenTarget := map[golem.FieldID]bool{}
		targetJoinTerms := correlationTerms(endpoint.TargetModelID(), endpoint.Correlation(), false)
		if index+1 < len(relationEndpoints) {
			targetJoinTerms = append(targetJoinTerms, correlationTerms(endpoint.TargetModelID(), relationEndpoints[index+1].Correlation(), true)...)
		}
		for _, term := range targetJoinTerms {
			field, fieldExists := registry.Field(endpoint.TargetModelID(), term.Field)
			if !fieldExists || !field.Visible() || field.Kind() == compilerir.FieldRelation {
				return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: correlation field is unavailable")
			}
			fields[term.Field] = field
			if !seenTarget[term.Field] {
				selection, selectionErr := readir.NewScalarSelection(policyir.FieldID(term.Field))
				if selectionErr != nil {
					return Plan{}, selectionErr
				}
				targetInput.Selection = append(targetInput.Selection, selection)
				seenTarget[term.Field] = true
			}
		}
		if len(targetInput.Selection) != 0 {
			targetInput.Operation, targetInput.Projection = readir.FindMany, readir.ProjectionSelect
		}
		if index == len(relationEndpoints)-1 {
			targetInput.Operation, targetInput.Projection = readir.FindMany, readir.ProjectionSelect
			for _, term := range relationTerms {
				if seenTarget[term.Field] {
					continue
				}
				field, fieldExists := registry.Field(endpoint.TargetModelID(), term.Field)
				if !fieldExists || !field.Visible() || field.Kind() == compilerir.FieldRelation {
					return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION: terminal field is absent or unreadable")
				}
				if validationErr := validateTerm(field, term); validationErr != nil {
					return Plan{}, validationErr
				}
				selection, selectionErr := readir.NewScalarSelection(policyir.FieldID(term.Field))
				if selectionErr != nil {
					return Plan{}, selectionErr
				}
				targetInput.Selection = append(targetInput.Selection, selection)
				fields[term.Field] = field
				seenTarget[term.Field] = true
			}
		}
		targetRequest, requestErr := readir.NewRequest(targetInput)
		if requestErr != nil {
			return Plan{}, requestErr
		}
		var targetPlan readplan.Plan
		if system {
			targetPlan, requestErr = readplan.System(targetRequest, registry, limits)
		} else {
			targetPlan, requestErr = readplan.Caller(targetRequest, registry, policies, limits)
		}
		if requestErr != nil {
			return Plan{}, fmt.Errorf("P6_ANALYTICS_RELATION_AUTHORIZATION: %w", requestErr)
		}
		if dischargeErr := requireDischargedAnalyticsFields(targetPlan, targetJoinTerms, fields, false); dischargeErr != nil {
			return Plan{}, dischargeErr
		}
		if index == len(relationEndpoints)-1 {
			if dischargeErr := requireDischargedAnalyticsFields(targetPlan, relationTerms, fields, true); dischargeErr != nil {
				return Plan{}, dischargeErr
			}
		}
		result.relation = append(result.relation, RelationHop{Endpoint: endpoint, Authorized: targetPlan})
		sourcePlan = targetPlan
	}
	if having, present := request.Having(); present {
		if err := validateHavingSemantics(having, fields); err != nil {
			return Plan{}, err
		}
	}
	return result, nil
}

func validateHavingSemantics(value golem.FrozenGroupPredicate, fields map[golem.FieldID]schema.Field) error {
	if value.Kind == golem.RuntimeGroupCompare && (value.Operator == "contains" || value.Operator == "startsWith" || value.Operator == "endsWith") {
		field, present := fields[value.Term.Field]
		if !present || field.LogicalType().Kind != compilerir.TypeString || (value.Term.Operator != golem.AggregateMinimum && value.Term.Operator != golem.AggregateMaximum) {
			return fmt.Errorf("P6_ANALYTICS_HAVING: text comparison requires a string min/max measure")
		}
	}
	for _, child := range value.Children {
		if err := validateHavingSemantics(child, fields); err != nil {
			return err
		}
	}
	return nil
}

// inferCorrelatedEqualities carries only exact equality facts through a
// declared SQL equality correlation. A candidate parent equality is accepted
// only when the implication engine proves it from the complete authorized
// source contribution predicate. Merely appearing in one OR branch is not
// enough, and no inequalities, membership predicates, insensitive text
// comparisons, or relation predicates are projected across the hop.
func inferCorrelatedEqualities(source policyir.Condition, endpoint schema.RelationEndpoint) (policyir.Condition, bool, error) {
	var inferred []policyir.Condition
	for _, pair := range endpoint.Correlation() {
		candidates := equalityCandidates(source, policyir.FieldID(pair.ParentFieldID()))
		for _, candidate := range candidates {
			proved, err := imply.Condition(source, candidate)
			if err != nil {
				return policyir.Condition{}, false, fmt.Errorf("P6_ANALYTICS_RELATION_AUTHORIZATION: prove correlation equality: %w", err)
			}
			if !proved {
				continue
			}
			typ, _ := candidate.FieldType()
			operand, _ := candidate.Operand()
			target, err := policyir.NewScalar(
				policyir.ModelID(endpoint.TargetModelID()),
				policyir.FieldID(pair.ChildFieldID()),
				typ,
				policyir.OperatorEqual,
				policyir.ComparisonSensitive,
				operand,
				candidate.Requirements(),
			)
			if err != nil {
				return policyir.Condition{}, false, fmt.Errorf("P6_ANALYTICS_RELATION_AUTHORIZATION: project correlation equality: %w", err)
			}
			inferred = append(inferred, target)
		}
	}
	if len(inferred) == 0 {
		return policyir.Condition{}, false, nil
	}
	if len(inferred) == 1 {
		return inferred[0], true, nil
	}
	combined, err := policyir.NewLogical(policyir.ModelID(endpoint.TargetModelID()), policyir.LogicalAnd, inferred)
	if err != nil {
		return policyir.Condition{}, false, fmt.Errorf("P6_ANALYTICS_RELATION_AUTHORIZATION: combine correlation equalities: %w", err)
	}
	return combined, true, nil
}

func equalityCandidates(condition policyir.Condition, field policyir.FieldID) []policyir.Condition {
	result := []policyir.Condition{}
	var visit func(policyir.Condition)
	visit = func(candidate policyir.Condition) {
		if _, children, logical := candidate.Logical(); logical {
			for _, child := range children {
				visit(child)
			}
			return
		}
		candidateField, scalar := candidate.Field()
		operator, operatorPresent := candidate.Operator()
		mode, modePresent := candidate.Mode()
		operand, operandPresent := candidate.Operand()
		_, one := operand.One()
		if candidate.Kind() == policyir.ConditionScalar && scalar && candidateField == field && operatorPresent && operator == policyir.OperatorEqual && modePresent && mode == policyir.ComparisonSensitive && operandPresent && one {
			result = append(result, candidate)
		}
	}
	visit(condition)
	return result
}

func requireDischargedAnalyticsFields(authorized readplan.Plan, terms []golem.FrozenAnalyticsTerm, schemaFields map[golem.FieldID]schema.Field, relation bool) error {
	fields := make(map[golem.FieldID]readplan.Field, len(authorized.Fields()))
	for _, field := range authorized.Fields() {
		fields[golem.FieldID(field.FieldID())] = field
	}
	for _, term := range terms {
		if term.Operator == golem.AggregateCountAll || (term.RelationName != "") != relation {
			continue
		}
		field, present := fields[term.Field]
		logicalName := "field"
		if schemaField, exists := schemaFields[term.Field]; exists && schemaField.GraphQLName() != "" {
			logicalName = schemaField.GraphQLName()
		}
		if !present {
			return &FieldAuthorizationError{field: term.Field, logicalName: logicalName, reason: "is absent from the authorized selection"}
		}
		if field.Conditional() && !field.DischargedByConstraint() {
			return &FieldAuthorizationError{field: term.Field, logicalName: logicalName, reason: "is conditional and is not discharged by the contribution predicate"}
		}
	}
	return nil
}

func analyticsTermKey(term golem.FrozenAnalyticsTerm) string {
	return golem.RuntimeAnalyticsTermKey(term)
}

func relationIDs(values []compilerir.RelationID) ([]golem.RelationID, error) {
	result := make([]golem.RelationID, len(values))
	for index, value := range values {
		decoded, err := hex.DecodeString(string(value))
		if err != nil || len(decoded) != 16 {
			return nil, fmt.Errorf("P6_ANALYTICS_RELATION: invalid relation identity")
		}
		copy(result[index][:], decoded)
	}
	return result, nil
}
func publicFieldID(value compilerir.FieldID) (golem.FieldID, error) {
	var result golem.FieldID
	decoded, err := hex.DecodeString(string(value))
	if err != nil || len(decoded) != 16 {
		return result, fmt.Errorf("P6_ANALYTICS_RELATION: invalid field identity")
	}
	copy(result[:], decoded)
	return result, nil
}
func sameRelationPath(left, right []golem.RelationID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func collectHavingTerms(value golem.FrozenGroupPredicate, result *[]golem.FrozenAnalyticsTerm) {
	if value.Term.Model != (golem.ModelID{}) {
		*result = append(*result, value.Term)
	}
	for _, child := range value.Children {
		collectHavingTerms(child, result)
	}
}

func correlationTerms(model golem.ModelID, pairs []schema.Correlation, parent bool) []golem.FrozenAnalyticsTerm {
	result := make([]golem.FrozenAnalyticsTerm, 0, len(pairs))
	seen := map[golem.FieldID]bool{}
	for _, pair := range pairs {
		field := pair.ChildFieldID()
		if parent {
			field = pair.ParentFieldID()
		}
		if seen[field] {
			continue
		}
		seen[field] = true
		result = append(result, golem.FrozenAnalyticsTerm{Model: model, Field: field})
	}
	return result
}

func validateTerm(field schema.Field, term golem.FrozenAnalyticsTerm) error {
	kind := field.LogicalType().Kind
	if term.Operator == 0 {
		if analyticsDimensionKind(kind) {
			return nil
		}
		return fmt.Errorf("P6_ANALYTICS_PLAN_CAPABILITY: field type %s is not a dimension", kind)
	}
	if term.Operator == golem.AggregateCountField {
		if analyticsDimensionKind(kind) {
			return nil
		}
		return fmt.Errorf("P6_ANALYTICS_PLAN_CAPABILITY: field type %s does not support field-count", kind)
	}
	switch term.Operator {
	case golem.AggregateSum, golem.AggregateAverage:
		switch kind {
		case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64, compilerir.TypeFloat32, compilerir.TypeFloat64, compilerir.TypeDecimal:
			return nil
		}
	case golem.AggregateMinimum, golem.AggregateMaximum:
		switch kind {
		case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64, compilerir.TypeFloat32, compilerir.TypeFloat64, compilerir.TypeDecimal, compilerir.TypeString, compilerir.TypeDate, compilerir.TypeTime, compilerir.TypeDateTime:
			return nil
		}
	}
	return fmt.Errorf("P6_ANALYTICS_PLAN_CAPABILITY: operator is not valid for field type %s", kind)
}

func analyticsDimensionKind(kind compilerir.LogicalTypeKind) bool {
	switch kind {
	case compilerir.TypeBool,
		compilerir.TypeInt16,
		compilerir.TypeInt32,
		compilerir.TypeInt64,
		compilerir.TypeFloat32,
		compilerir.TypeFloat64,
		compilerir.TypeDecimal,
		compilerir.TypeString,
		compilerir.TypeUUID,
		compilerir.TypeDate,
		compilerir.TypeTime,
		compilerir.TypeDateTime,
		compilerir.TypeEnum:
		return true
	default:
		return false
	}
}
