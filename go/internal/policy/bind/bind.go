package bind

import (
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

const (
	publicFrozenVersion = uint16(1)
	maximumBindDepth    = 256
	listCapability      = compilerir.CapabilityID("scalar-list:json-array:v1")
)

type binder struct {
	registry  *schema.Registry
	providers ir.ProviderSet
	rule      uint32
	hasRule   bool
}

// Predicate binds one frozen public predicate against the exact provider set
// declared by registry. It validates construction and shape only. Runtime
// activation remains behind operator.RequireAgreement.
func Predicate(frozen golem.FrozenPredicate, registry *schema.Registry, providers ir.ProviderSet) (ir.Condition, error) {
	value, err := newBinder(registry, providers)
	if err != nil {
		return ir.Condition{}, err
	}
	condition, err := value.predicate(frozen.View(), "predicate")
	if err != nil {
		return ir.Condition{}, err
	}
	return condition, nil
}

// Policy binds an ordered frozen policy without reordering its rules or field
// identities. It validates every rule independently before publishing a Policy.
func Policy(frozen golem.FrozenPolicy, registry *schema.Registry, providers ir.ProviderSet) (ir.Policy, error) {
	value, err := newBinder(registry, providers)
	if err != nil {
		return ir.Policy{}, err
	}
	view := frozen.View()
	if view.Version() != publicFrozenVersion {
		return ir.Policy{}, value.failure(CodeVersion, "policy", "unsupported frozen policy version %d", view.Version())
	}
	if len(view.CanonicalBytes()) == 0 {
		return ir.Policy{}, value.failure(CodeVersion, "policy", "frozen policy has no canonical evidence")
	}
	model := modelID(view.ModelID())
	if _, ok := registry.Model(golem.ModelID(model)); !ok {
		return ir.Policy{}, value.failureFor(CodeModel, "policy.model", model, ir.FieldID{}, ir.RelationID{}, 0, "unknown policy model")
	}

	views := view.Rules()
	rules := make([]ir.Rule, len(views))
	for index, ruleView := range views {
		value.rule, value.hasRule = uint32(index), true
		path := fmt.Sprintf("policy.rules[%d]", index)
		if ruleView.Position() != uint32(index) {
			return ir.Policy{}, value.failureFor(CodeRule, path+".position", model, ir.FieldID{}, ir.RelationID{}, 0, "rule position %d is not contiguous", ruleView.Position())
		}
		if ruleModel := modelID(ruleView.ModelID()); ruleModel != model {
			return ir.Policy{}, value.failureFor(CodeModel, path+".model", ruleModel, ir.FieldID{}, ir.RelationID{}, 0, "rule model does not match policy model")
		}
		action, ok := bindAction(ruleView.Action())
		if !ok {
			return ir.Policy{}, value.failureFor(CodeRule, path+".action", model, ir.FieldID{}, ir.RelationID{}, 0, "unknown action tag %d", ruleView.Action())
		}
		effect, ok := bindEffect(ruleView.Effect())
		if !ok {
			return ir.Policy{}, value.failureFor(CodeRule, path+".effect", model, ir.FieldID{}, ir.RelationID{}, 0, "unknown effect tag %d", ruleView.Effect())
		}

		conditionView, hasCondition := ruleView.Condition()
		unconditional := ruleView.IsUnconditional()
		if unconditional == hasCondition {
			return ir.Policy{}, value.failureFor(CodeRule, path+".condition", model, ir.FieldID{}, ir.RelationID{}, 0, "unconditional and condition tags disagree")
		}
		var condition *ir.Condition
		if hasCondition {
			bound, bindErr := value.predicate(conditionView, path+".condition")
			if bindErr != nil {
				return ir.Policy{}, bindErr
			}
			if bound.ModelID() != model {
				return ir.Policy{}, value.failureFor(CodeModel, path+".condition", bound.ModelID(), ir.FieldID{}, ir.RelationID{}, 0, "condition model does not match rule model")
			}
			condition = &bound
		}

		publicFields, modelWide := ruleView.Fields()
		if modelWide && len(publicFields) != 0 || !modelWide && len(publicFields) == 0 {
			return ir.Policy{}, value.failureFor(CodeRule, path+".fields", model, ir.FieldID{}, ir.RelationID{}, 0, "field-scope tag and field inventory disagree")
		}
		fields := make([]ir.FieldID, len(publicFields))
		seen := make(map[ir.FieldID]struct{}, len(publicFields))
		for fieldIndex, publicField := range publicFields {
			field := fieldID(publicField)
			if _, exists := seen[field]; exists {
				return ir.Policy{}, value.failureFor(CodeField, fmt.Sprintf("%s.fields[%d]", path, fieldIndex), model, field, ir.RelationID{}, 0, "duplicate rule field identity")
			}
			seen[field] = struct{}{}
			if _, exists := registry.Field(golem.ModelID(model), golem.FieldID(field)); !exists {
				return ir.Policy{}, value.failureFor(CodeField, fmt.Sprintf("%s.fields[%d]", path, fieldIndex), model, field, ir.RelationID{}, 0, "unknown or cross-model rule field")
			}
			fields[fieldIndex] = field
		}

		var built ir.Rule
		if modelWide {
			built, err = ir.NewModelRule(action, effect, model, condition, uint32(index))
		} else {
			built, err = ir.NewFieldRule(action, effect, model, condition, fields[0], fields[1:], uint32(index))
		}
		if err != nil {
			return ir.Policy{}, value.wrap(CodeRule, path, model, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		rules[index] = built
	}
	value.hasRule = false
	bound, err := ir.NewPolicy(model, rules)
	if err != nil {
		return ir.Policy{}, value.wrap(CodeInternal, "policy", model, ir.FieldID{}, ir.RelationID{}, 0, err)
	}
	return bound, nil
}

func newBinder(registry *schema.Registry, providers ir.ProviderSet) (*binder, error) {
	value := &binder{registry: registry, providers: providers}
	if registry == nil {
		return nil, value.failure(CodeInternal, "registry", "nil runtime schema registry")
	}
	if !providers.Valid() {
		return nil, value.failure(CodeProvider, "providers", "invalid or empty provider set")
	}
	declared, err := registryProviderSet(registry)
	if err != nil {
		return nil, value.wrap(CodeProvider, "providers", ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
	}
	if declared != providers {
		return nil, value.failure(CodeProvider, "providers", "provider set does not exactly match the fingerprinted schema registry")
	}
	return value, nil
}

func registryProviderSet(registry *schema.Registry) (ir.ProviderSet, error) {
	providers := registry.Providers()
	result := make([]ir.Provider, len(providers))
	for index, provider := range providers {
		switch provider {
		case golem.SQLite:
			result[index] = ir.ProviderSQLite
		case golem.PostgreSQL:
			result[index] = ir.ProviderPostgreSQL
		default:
			return 0, fmt.Errorf("unknown registry provider %q", provider)
		}
	}
	return ir.NewProviderSet(result...)
}

func (b *binder) predicate(view golem.FrozenPredicateView, path string) (ir.Condition, error) {
	if view == nil {
		return ir.Condition{}, b.failure(CodeCondition, path, "nil frozen predicate view")
	}
	if view.Version() != publicFrozenVersion {
		return ir.Condition{}, b.failure(CodeVersion, path, "unsupported frozen predicate version %d", view.Version())
	}
	if len(view.CanonicalBytes()) == 0 {
		return ir.Condition{}, b.failure(CodeVersion, path, "frozen predicate has no canonical evidence")
	}
	model := modelID(view.RootModelID())
	if _, ok := b.registry.Model(golem.ModelID(model)); !ok {
		return ir.Condition{}, b.failureFor(CodeModel, path+".model", model, ir.FieldID{}, ir.RelationID{}, 0, "unknown predicate model")
	}
	condition, err := b.condition(view.Root(), model, path+".root", 0)
	if err != nil {
		return ir.Condition{}, err
	}
	if condition.ModelID() != model {
		return ir.Condition{}, b.failureFor(CodeModel, path+".root", condition.ModelID(), ir.FieldID{}, ir.RelationID{}, 0, "bound root model mismatch")
	}
	if err := operator.ValidateCondition(condition, b.providers); err != nil {
		return ir.Condition{}, b.wrap(CodeOperator, path+".root", model, ir.FieldID{}, ir.RelationID{}, 0, err)
	}
	return condition, nil
}

func (b *binder) condition(view golem.FrozenConditionView, model ir.ModelID, path string, depth int) (ir.Condition, error) {
	if view == nil {
		return ir.Condition{}, b.failureFor(CodeCondition, path, model, ir.FieldID{}, ir.RelationID{}, 0, "nil condition view")
	}
	if depth > maximumBindDepth {
		return ir.Condition{}, b.failureFor(CodeCondition, path, model, ir.FieldID{}, ir.RelationID{}, 0, "condition nesting exceeds %d nodes", maximumBindDepth)
	}
	if _, ok := b.registry.Model(golem.ModelID(model)); !ok {
		return ir.Condition{}, b.failureFor(CodeModel, path, model, ir.FieldID{}, ir.RelationID{}, 0, "unknown current model")
	}

	switch view.Kind() {
	case golem.FrozenConditionConstant:
		return b.constant(view, model, path)
	case golem.FrozenConditionLogical:
		return b.logical(view, model, path, depth)
	case golem.FrozenConditionScalar:
		return b.scalar(view, model, path)
	case golem.FrozenConditionList:
		return b.list(view, model, path)
	case golem.FrozenConditionJSON:
		return b.json(view, model, path)
	case golem.FrozenConditionRelation:
		return b.relation(view, model, path, depth)
	default:
		return ir.Condition{}, b.failureFor(CodeCondition, path+".kind", model, ir.FieldID{}, ir.RelationID{}, 0, "unknown condition kind tag %d", view.Kind())
	}
}

func (b *binder) constant(view golem.FrozenConditionView, model ir.ModelID, path string) (ir.Condition, error) {
	if view.Operator() != golem.FrozenOperatorNone || view.Mode() != 0 || len(view.Children()) != 0 || hasField(view) || hasRelation(view) {
		return ir.Condition{}, b.failureFor(CodeCondition, path, model, ir.FieldID{}, ir.RelationID{}, 0, "malformed constant tags")
	}
	if err := requireNoPath(view.Path()); err != nil {
		return ir.Condition{}, b.wrap(CodeCondition, path+".path", model, ir.FieldID{}, ir.RelationID{}, 0, err)
	}
	operand := view.Operand()
	if operand == nil || operand.Kind() != golem.FrozenOperandFlag {
		return ir.Condition{}, b.failureFor(CodeValue, path+".operand", model, ir.FieldID{}, ir.RelationID{}, 0, "constant requires a flag operand")
	}
	truth, ok := operand.Flag()
	if !ok {
		return ir.Condition{}, b.failureFor(CodeValue, path+".operand", model, ir.FieldID{}, ir.RelationID{}, 0, "constant flag accessor disagrees with its tag")
	}
	condition, err := ir.NewConstant(model, truth)
	if err != nil {
		return ir.Condition{}, b.wrap(CodeInternal, path, model, ir.FieldID{}, ir.RelationID{}, 0, err)
	}
	return condition, nil
}

func (b *binder) logical(view golem.FrozenConditionView, model ir.ModelID, path string, depth int) (ir.Condition, error) {
	if view.Mode() != 0 || hasField(view) || hasRelation(view) {
		return ir.Condition{}, b.failureFor(CodeCondition, path, model, ir.FieldID{}, ir.RelationID{}, 0, "malformed logical tags")
	}
	if err := requireNoPath(view.Path()); err != nil {
		return ir.Condition{}, b.wrap(CodeCondition, path+".path", model, ir.FieldID{}, ir.RelationID{}, 0, err)
	}
	if operand := view.Operand(); operand == nil || operand.Kind() != golem.FrozenOperandNone {
		return ir.Condition{}, b.failureFor(CodeValue, path+".operand", model, ir.FieldID{}, ir.RelationID{}, 0, "logical node requires no operand")
	}
	logical, ok := bindLogicalOperator(view.Operator())
	if !ok {
		return ir.Condition{}, b.failureFor(CodeOperator, path+".operator", model, ir.FieldID{}, ir.RelationID{}, 0, "unknown logical operator tag %d", view.Operator())
	}
	publicChildren := view.Children()
	if logical == ir.LogicalNot && len(publicChildren) != 1 || logical != ir.LogicalNot && len(publicChildren) < 2 {
		return ir.Condition{}, b.failureFor(CodeCondition, path+".children", model, ir.FieldID{}, ir.RelationID{}, 0, "logical arity is not normalized")
	}
	children := make([]ir.Condition, len(publicChildren))
	for index, child := range publicChildren {
		bound, err := b.condition(child, model, fmt.Sprintf("%s.children[%d]", path, index), depth+1)
		if err != nil {
			return ir.Condition{}, err
		}
		children[index] = bound
	}
	condition, err := ir.NewLogical(model, logical, children)
	if err != nil {
		return ir.Condition{}, b.wrap(CodeCondition, path, model, ir.FieldID{}, ir.RelationID{}, 0, err)
	}
	return condition, nil
}

func (b *binder) scalar(view golem.FrozenConditionView, model ir.ModelID, path string) (ir.Condition, error) {
	field, fact, typ, err := b.leaf(view, model, path, compilerir.FieldScalar, compilerir.FieldEnum)
	if err != nil {
		return ir.Condition{}, err
	}
	if typ.Kind() == ir.ValueScalarList || typ.Kind() == ir.ValueJSON {
		return ir.Condition{}, b.failureFor(CodeField, path+".field", model, field, ir.RelationID{}, 0, "scalar node targets a non-scalar logical type")
	}
	if err := requireNoPath(view.Path()); err != nil {
		return ir.Condition{}, b.wrap(CodeCondition, path+".path", model, field, ir.RelationID{}, 0, err)
	}
	operatorID, ok := bindScalarOperator(view.Operator())
	if !ok {
		return ir.Condition{}, b.failureFor(CodeOperator, path+".operator", model, field, ir.RelationID{}, 0, "operator tag %d is not a scalar operator", view.Operator())
	}
	mode, ok := bindMode(view.Mode())
	if !ok {
		return ir.Condition{}, b.failureFor(CodeOperator, path+".mode", model, field, ir.RelationID{}, operatorID, "unknown comparison mode tag %d", view.Mode())
	}
	operand, err := b.operand(view.Operand(), typ, operatorID, path+".operand")
	if err != nil {
		return ir.Condition{}, err
	}
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionScalar, FieldType: typ, Operand: operand, Mode: mode, Providers: b.providers})
	if err != nil {
		return ir.Condition{}, b.wrap(CodeOperator, path, model, field, ir.RelationID{}, operatorID, err)
	}
	if err := b.validatePhysicalField(model, field, fact, path); err != nil {
		return ir.Condition{}, err
	}
	condition, err := ir.NewScalar(model, field, typ, operatorID, mode, operand, requirements)
	if err != nil {
		return ir.Condition{}, b.wrap(CodeInternal, path, model, field, ir.RelationID{}, operatorID, err)
	}
	return condition, nil
}

func (b *binder) list(view golem.FrozenConditionView, model ir.ModelID, path string) (ir.Condition, error) {
	field, fact, typ, err := b.leaf(view, model, path, compilerir.FieldScalarList)
	if err != nil {
		return ir.Condition{}, err
	}
	if view.Mode() != 0 {
		return ir.Condition{}, b.failureFor(CodeOperator, path+".mode", model, field, ir.RelationID{}, 0, "list node carries a comparison mode")
	}
	if err := requireNoPath(view.Path()); err != nil {
		return ir.Condition{}, b.wrap(CodeCondition, path+".path", model, field, ir.RelationID{}, 0, err)
	}
	operatorID, ok := bindListOperator(view.Operator())
	if !ok {
		return ir.Condition{}, b.failureFor(CodeOperator, path+".operator", model, field, ir.RelationID{}, 0, "operator tag %d is not a list operator", view.Operator())
	}
	operand, err := b.listOperand(view.Operand(), typ, operatorID, path+".operand")
	if err != nil {
		return ir.Condition{}, err
	}
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionList, FieldType: typ, Operand: operand, Mode: ir.ComparisonSensitive, Providers: b.providers})
	if err != nil {
		return ir.Condition{}, b.wrap(CodeOperator, path, model, field, ir.RelationID{}, operatorID, err)
	}
	if err := b.validatePhysicalField(model, field, fact, path); err != nil {
		return ir.Condition{}, err
	}
	condition, err := ir.NewList(model, field, typ, operatorID, operand, requirements)
	if err != nil {
		return ir.Condition{}, b.wrap(CodeInternal, path, model, field, ir.RelationID{}, operatorID, err)
	}
	return condition, nil
}

func (b *binder) json(view golem.FrozenConditionView, model ir.ModelID, path string) (ir.Condition, error) {
	field, fact, typ, err := b.leaf(view, model, path, compilerir.FieldScalar)
	if err != nil {
		return ir.Condition{}, err
	}
	if typ.Kind() != ir.ValueJSON {
		return ir.Condition{}, b.failureFor(CodeField, path+".field", model, field, ir.RelationID{}, 0, "JSON node targets a non-JSON logical type")
	}
	operatorID, ok := bindJSONOperator(view.Operator())
	if !ok {
		return ir.Condition{}, b.failureFor(CodeOperator, path+".operator", model, field, ir.RelationID{}, 0, "operator tag %d is not a JSON operator", view.Operator())
	}
	mode, ok := bindMode(view.Mode())
	if !ok {
		return ir.Condition{}, b.failureFor(CodeOperator, path+".mode", model, field, ir.RelationID{}, operatorID, "unknown comparison mode tag %d", view.Mode())
	}
	jsonPath, err := bindPath(view.Path())
	if err != nil {
		return ir.Condition{}, b.wrap(CodeValue, path+".path", model, field, ir.RelationID{}, operatorID, err)
	}
	operand, err := b.operand(view.Operand(), typ, operatorID, path+".operand")
	if err != nil {
		return ir.Condition{}, err
	}
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionJSON, FieldType: typ, Operand: operand, Mode: mode, Path: jsonPath, Providers: b.providers})
	if err != nil {
		return ir.Condition{}, b.wrap(CodeOperator, path, model, field, ir.RelationID{}, operatorID, err)
	}
	if err := b.validatePhysicalField(model, field, fact, path); err != nil {
		return ir.Condition{}, err
	}
	condition, err := ir.NewJSON(model, field, typ, operatorID, mode, jsonPath, operand, requirements)
	if err != nil {
		return ir.Condition{}, b.wrap(CodeInternal, path, model, field, ir.RelationID{}, operatorID, err)
	}
	return condition, nil
}

func (b *binder) relation(view golem.FrozenConditionView, model ir.ModelID, path string, depth int) (ir.Condition, error) {
	if view.Mode() != 0 {
		return ir.Condition{}, b.failureFor(CodeOperator, path+".mode", model, ir.FieldID{}, ir.RelationID{}, 0, "relation node carries a comparison mode")
	}
	if err := requireNoPath(view.Path()); err != nil {
		return ir.Condition{}, b.wrap(CodeCondition, path+".path", model, ir.FieldID{}, ir.RelationID{}, 0, err)
	}
	if operand := view.Operand(); operand == nil || operand.Kind() != golem.FrozenOperandNone {
		return ir.Condition{}, b.failureFor(CodeValue, path+".operand", model, ir.FieldID{}, ir.RelationID{}, 0, "relation node requires no operand")
	}
	publicField, hasPublicField := view.FieldID()
	reference, hasReference := view.Relation()
	field, relation := fieldID(publicField), relationID(reference.RelationID())
	if !hasPublicField || !hasReference || field == (ir.FieldID{}) || relation == (ir.RelationID{}) || reference.FieldID() != publicField {
		return ir.Condition{}, b.failureFor(CodeRelation, path, model, field, relation, 0, "relation field and endpoint tags disagree")
	}
	fact, ok := b.registry.Field(golem.ModelID(model), golem.FieldID(field))
	if !ok || fact.Kind() != compilerir.FieldRelation {
		return ir.Condition{}, b.failureFor(CodeField, path+".field", model, field, relation, 0, "unknown, cross-model, or non-relation field")
	}
	registeredRelation, relationField := fact.RelationID()
	if !relationField || registeredRelation != golem.RelationID(relation) {
		return ir.Condition{}, b.failureFor(CodeRelation, path, model, field, relation, 0, "field relation identity does not match")
	}
	endpoint, ok := b.registry.RelationEndpoint(golem.ModelID(model), golem.FieldID(field), golem.RelationID(relation))
	if !ok {
		return ir.Condition{}, b.failureFor(CodeRelation, path, model, field, relation, 0, "unknown or stale relation endpoint")
	}
	if len(endpoint.Correlation()) == 0 {
		return ir.Condition{}, b.failureFor(CodeRelation, path, model, field, relation, 0, "relation endpoint has no correlation fields")
	}
	target := modelID(endpoint.TargetModelID())
	if _, ok := b.registry.Model(endpoint.TargetModelID()); !ok {
		return ir.Condition{}, b.failureFor(CodeModel, path+".target", target, field, relation, 0, "unknown relation target model")
	}
	cardinality, ok := bindCardinality(endpoint.Cardinality())
	if !ok {
		return ir.Condition{}, b.failureFor(CodeRelation, path+".cardinality", model, field, relation, 0, "unknown relation cardinality %q", endpoint.Cardinality())
	}
	operatorID, ok := bindRelationOperator(view.Operator())
	if !ok {
		return ir.Condition{}, b.failureFor(CodeOperator, path+".operator", model, field, relation, 0, "operator tag %d is not a relation operator", view.Operator())
	}
	publicChildren := view.Children()
	var child *ir.Condition
	if len(publicChildren) > 1 {
		return ir.Condition{}, b.failureFor(CodeCondition, path+".children", model, field, relation, operatorID, "relation node has more than one child")
	}
	if len(publicChildren) == 1 {
		bound, err := b.condition(publicChildren[0], target, path+".child", depth+1)
		if err != nil {
			return ir.Condition{}, err
		}
		child = &bound
	}
	requirements, err := operator.ValidateShape(operatorID, operator.Shape{Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Cardinality: cardinality, HasChild: child != nil, Providers: b.providers})
	if err != nil {
		return ir.Condition{}, b.wrap(CodeOperator, path, model, field, relation, operatorID, err)
	}
	condition, err := ir.NewRelation(model, field, relation, target, cardinality, operatorID, child, requirements)
	if err != nil {
		return ir.Condition{}, b.wrap(CodeInternal, path, model, field, relation, operatorID, err)
	}
	return condition, nil
}

func (b *binder) leaf(view golem.FrozenConditionView, model ir.ModelID, path string, allowed ...compilerir.FieldKind) (ir.FieldID, schema.Field, ir.TypeRef, error) {
	publicField, ok := view.FieldID()
	field := fieldID(publicField)
	if !ok || field == (ir.FieldID{}) || hasRelation(view) || len(view.Children()) != 0 {
		return ir.FieldID{}, schema.Field{}, ir.TypeRef{}, b.failureFor(CodeField, path+".field", model, field, ir.RelationID{}, 0, "malformed leaf field or child tags")
	}
	fact, ok := b.registry.Field(golem.ModelID(model), publicField)
	if !ok {
		return ir.FieldID{}, schema.Field{}, ir.TypeRef{}, b.failureFor(CodeField, path+".field", model, field, ir.RelationID{}, 0, "unknown or cross-model field identity")
	}
	accepted := false
	for _, kind := range allowed {
		accepted = accepted || fact.Kind() == kind
	}
	if !accepted {
		return ir.FieldID{}, schema.Field{}, ir.TypeRef{}, b.failureFor(CodeField, path+".field", model, field, ir.RelationID{}, 0, "field kind %q does not match node family", fact.Kind())
	}
	typ, err := bindType(fact.LogicalType(), fact.Nullable())
	if err != nil {
		return ir.FieldID{}, schema.Field{}, ir.TypeRef{}, b.wrap(CodeField, path+".type", model, field, ir.RelationID{}, 0, err)
	}
	if err := validateFieldKind(fact.Kind(), typ.Kind()); err != nil {
		return ir.FieldID{}, schema.Field{}, ir.TypeRef{}, b.wrap(CodeField, path+".type", model, field, ir.RelationID{}, 0, err)
	}
	return field, fact, typ, nil
}

func (b *binder) validatePhysicalField(model ir.ModelID, field ir.FieldID, fact schema.Field, path string) error {
	for _, provider := range b.providers.Providers() {
		publicProvider := golem.SQLite
		if provider == ir.ProviderPostgreSQL {
			publicProvider = golem.PostgreSQL
		}
		physicalField, ok := b.registry.PhysicalField(publicProvider, golem.ModelID(model), golem.FieldID(field))
		if !ok {
			return b.failureFor(CodeProvider, path+".physical", model, field, ir.RelationID{}, 0, "provider %q has no physical field", publicProvider)
		}
		if physicalField.Nullable() != fact.Nullable() {
			return b.failureFor(CodeProvider, path+".physical", model, field, ir.RelationID{}, 0, "provider %q nullability disagrees with logical field", publicProvider)
		}
		for _, requirement := range physicalField.RequiredCapabilities() {
			if _, present := b.registry.Capability(publicProvider, requirement.Capability); !present {
				return b.failureFor(CodeProvider, path+".physical", model, field, ir.RelationID{}, 0, "provider %q lacks required physical capability %q", publicProvider, requirement.Capability)
			}
		}
	}
	return nil
}

func bindType(logical compilerir.LogicalTypeIR, nullable bool) (ir.TypeRef, error) {
	kind, ok := mapLogicalKind(logical.Kind)
	if !ok {
		return ir.TypeRef{}, fmt.Errorf("unknown logical type %q", logical.Kind)
	}
	var precision, scale uint16
	if logical.Precision != nil {
		precision = *logical.Precision
	}
	if logical.Scale != nil {
		scale = *logical.Scale
	}
	if logical.Kind == compilerir.TypeDecimal && (logical.Precision == nil || logical.Scale == nil) {
		return ir.TypeRef{}, fmt.Errorf("decimal logical type lacks normalized precision or scale")
	}
	if (logical.Kind == compilerir.TypeTime || logical.Kind == compilerir.TypeDateTime) && logical.Precision == nil {
		return ir.TypeRef{}, fmt.Errorf("temporal logical type lacks normalized precision")
	}
	var enum ir.EnumID
	if logical.EnumID != nil {
		parsed, err := fixedID(string(*logical.EnumID))
		if err != nil {
			return ir.TypeRef{}, fmt.Errorf("invalid enum identity: %w", err)
		}
		enum = ir.EnumID(parsed)
	}
	var element *ir.TypeRef
	if logical.Element != nil {
		bound, err := bindType(*logical.Element, false)
		if err != nil {
			return ir.TypeRef{}, fmt.Errorf("invalid scalar-list element: %w", err)
		}
		element = &bound
	}
	var capability ir.Capability
	if logical.Capability != nil {
		switch *logical.Capability {
		case listCapability:
			capability = ir.CapabilityScalarListJSON
		default:
			return ir.TypeRef{}, fmt.Errorf("unknown logical capability %q", *logical.Capability)
		}
	}
	if logical.Kind == compilerir.TypeScalarList && (logical.Capability == nil || *logical.Capability != listCapability) {
		return ir.TypeRef{}, fmt.Errorf("scalar-list logical type lacks capability %q", listCapability)
	}
	return ir.NewTypeRef(kind, nullable, precision, scale, enum, element, capability)
}

func validateFieldKind(fieldKind compilerir.FieldKind, valueKind ir.ValueKind) error {
	switch fieldKind {
	case compilerir.FieldScalar:
		if valueKind == ir.ValueEnum || valueKind == ir.ValueScalarList {
			return fmt.Errorf("scalar field carries logical kind %d", valueKind)
		}
	case compilerir.FieldEnum:
		if valueKind != ir.ValueEnum {
			return fmt.Errorf("enum field carries logical kind %d", valueKind)
		}
	case compilerir.FieldScalarList:
		if valueKind != ir.ValueScalarList {
			return fmt.Errorf("scalar-list field carries logical kind %d", valueKind)
		}
	default:
		return fmt.Errorf("field kind %q is not persisted scalar data", fieldKind)
	}
	return nil
}

func (b *binder) operand(view golem.FrozenOperandView, typ ir.TypeRef, operatorID ir.OperatorID, path string) (ir.Operand, error) {
	if view == nil {
		return ir.Operand{}, b.failureFor(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, "nil operand view")
	}
	switch view.Kind() {
	case golem.FrozenOperandNone:
		return ir.NoOperand(), nil
	case golem.FrozenOperandOne:
		public, ok := view.One()
		if !ok || public == nil {
			return ir.Operand{}, b.failureFor(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, "one operand accessor disagrees with its tag")
		}
		value, err := b.value(public, typ, path+".one")
		if err != nil {
			return ir.Operand{}, err
		}
		bound, err := ir.OneOperand(value)
		if err != nil {
			return ir.Operand{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, err)
		}
		return bound, nil
	case golem.FrozenOperandMany:
		public := view.Many()
		if public == nil {
			return ir.Operand{}, b.failureFor(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, "many operand must distinguish empty from absent")
		}
		values := make([]ir.Value, len(public))
		for index, item := range public {
			bound, err := b.value(item, typ, fmt.Sprintf("%s.many[%d]", path, index))
			if err != nil {
				return ir.Operand{}, err
			}
			values[index] = bound
		}
		bound, err := ir.ManyOperand(values)
		if err != nil {
			return ir.Operand{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, err)
		}
		return bound, nil
	case golem.FrozenOperandFlag:
		flag, ok := view.Flag()
		if !ok {
			return ir.Operand{}, b.failureFor(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, "flag accessor disagrees with its tag")
		}
		return ir.FlagOperand(flag), nil
	default:
		return ir.Operand{}, b.failureFor(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, "unknown operand kind tag %d", view.Kind())
	}
}

func (b *binder) listOperand(view golem.FrozenOperandView, typ ir.TypeRef, operatorID ir.OperatorID, path string) (ir.Operand, error) {
	element, ok := typ.Element()
	if !ok {
		return ir.Operand{}, b.failureFor(CodeField, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, "scalar-list type has no element")
	}
	if operatorID == ir.OperatorListEqual {
		if view == nil || view.Kind() != golem.FrozenOperandMany {
			return ir.Operand{}, b.failureFor(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, "list equality requires an ordered list operand")
		}
		public := view.Many()
		if public == nil {
			return ir.Operand{}, b.failureFor(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, "list equality must distinguish empty from absent")
		}
		values := make([]ir.Value, len(public))
		for index, item := range public {
			bound, err := b.value(item, element, fmt.Sprintf("%s.list[%d]", path, index))
			if err != nil {
				return ir.Operand{}, err
			}
			values[index] = bound
		}
		list, err := ir.NewListValue(values)
		if err != nil {
			return ir.Operand{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, err)
		}
		bound, err := ir.OneOperand(list)
		if err != nil {
			return ir.Operand{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, operatorID, err)
		}
		return bound, nil
	}
	return b.operand(view, element, operatorID, path)
}

func (b *binder) value(view golem.FrozenValueView, typ ir.TypeRef, path string) (ir.Value, error) {
	if view == nil {
		return ir.Value{}, b.failure(CodeValue, path, "nil frozen value view")
	}
	if typ.Kind() == ir.ValueEnum {
		if view.Kind() != golem.FrozenValueString {
			return ir.Value{}, b.failure(CodeValue, path, "enum operand must carry an authored string label, got tag %d", view.Kind())
		}
		label, ok := view.String()
		if !ok {
			return ir.Value{}, b.failure(CodeValue, path, "enum string accessor disagrees with its tag")
		}
		enum, _ := typ.EnumID()
		member, ok := b.registry.EnumValue(compilerir.EnumID(hex.EncodeToString(enum[:])), label)
		if !ok {
			return ir.Value{}, b.failure(CodeValue, path, "unknown enum label %q for enum %s", label, hex.EncodeToString(enum[:]))
		}
		parsed, err := fixedID(string(member))
		if err != nil {
			return ir.Value{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		value, err := ir.NewEnumValue(enum, ir.EnumValueID(parsed))
		if err != nil {
			return ir.Value{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		return value, nil
	}
	want, ok := publicKind(typ.Kind())
	if !ok || view.Kind() != want {
		return ir.Value{}, b.failure(CodeValue, path, "value tag %d does not match logical kind %d", view.Kind(), typ.Kind())
	}
	switch typ.Kind() {
	case ir.ValueBool:
		value, ok := view.Bool()
		if !ok {
			return ir.Value{}, b.failure(CodeValue, path, "boolean accessor disagrees with its tag")
		}
		return ir.BoolValue(value), nil
	case ir.ValueInt16, ir.ValueInt32, ir.ValueInt64:
		value, width, ok := view.Signed()
		if !ok || width != integerWidth(typ.Kind()) {
			return ir.Value{}, b.failure(CodeValue, path, "signed accessor width disagrees with its tag")
		}
		bound, err := ir.SignedValue(typ.Kind(), value)
		if err != nil {
			return ir.Value{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		return bound, nil
	case ir.ValueFloat32:
		bits, width, ok := view.FloatBits()
		if !ok || width != 32 || bits > math.MaxUint32 {
			return ir.Value{}, b.failure(CodeValue, path, "float32 accessor disagrees with its tag")
		}
		bound, err := ir.Float32Value(math.Float32frombits(uint32(bits)))
		if err != nil {
			return ir.Value{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		return bound, nil
	case ir.ValueFloat64:
		bits, width, ok := view.FloatBits()
		if !ok || width != 64 {
			return ir.Value{}, b.failure(CodeValue, path, "float64 accessor disagrees with its tag")
		}
		bound, err := ir.Float64Value(math.Float64frombits(bits))
		if err != nil {
			return ir.Value{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		return bound, nil
	case ir.ValueDecimal:
		value, ok := view.Decimal()
		if !ok {
			return ir.Value{}, b.failure(CodeValue, path, "decimal accessor disagrees with its tag")
		}
		bound, err := ir.NewDecimalValue(value.Coefficient(), value.Scale())
		if err != nil {
			return ir.Value{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		return bound, nil
	case ir.ValueString:
		value, ok := view.String()
		if !ok {
			return ir.Value{}, b.failure(CodeValue, path, "string accessor disagrees with its tag")
		}
		bound, err := ir.StringValue(value)
		if err != nil {
			return ir.Value{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		return bound, nil
	case ir.ValueBytes:
		value, ok := view.Bytes()
		if !ok {
			return ir.Value{}, b.failure(CodeValue, path, "bytes accessor disagrees with its tag")
		}
		return ir.BytesValue(value), nil
	case ir.ValueUUID:
		value, ok := view.UUID()
		if !ok {
			return ir.Value{}, b.failure(CodeValue, path, "UUID accessor disagrees with its tag")
		}
		return ir.UUIDValue(value.Bytes()), nil
	case ir.ValueDate:
		value, ok := view.Date()
		if !ok {
			return ir.Value{}, b.failure(CodeValue, path, "date accessor disagrees with its tag")
		}
		bound, err := ir.NewDateValue(int16(value.Year()), uint8(value.Month()), uint8(value.Day()))
		if err != nil {
			return ir.Value{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		return bound, nil
	case ir.ValueTime:
		value, ok := view.Time()
		if !ok {
			return ir.Value{}, b.failure(CodeValue, path, "time accessor disagrees with its tag")
		}
		bound, err := ir.NewTimeValue(value.Microseconds())
		if err != nil {
			return ir.Value{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		return bound, nil
	case ir.ValueDateTime:
		seconds, nanoseconds, ok := view.DateTime()
		if !ok {
			return ir.Value{}, b.failure(CodeValue, path, "datetime accessor disagrees with its tag")
		}
		instant := time.Unix(seconds, int64(nanoseconds)).UTC()
		if instant.Year() < 1 || instant.Year() > 9999 {
			return ir.Value{}, b.failure(CodeValue, path, "datetime is outside the portable year range 0001-9999")
		}
		bound, err := ir.NewDateTimeValue(seconds, nanoseconds)
		if err != nil {
			return ir.Value{}, b.wrap(CodeValue, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, err)
		}
		return bound, nil
	default:
		return ir.Value{}, b.failure(CodeValue, path, "logical kind %d has no current frozen public value codec", typ.Kind())
	}
}

func bindPath(view golem.FrozenJSONPathView) (ir.JSONPath, error) {
	if view == nil {
		return ir.JSONPath{}, fmt.Errorf("nil JSON path view")
	}
	public := view.Segments()
	if !view.Present() && len(public) != 0 {
		return ir.JSONPath{}, fmt.Errorf("absent JSON path carries segments")
	}
	segments := make([]ir.JSONPathSegment, len(public))
	for index, segment := range public {
		if segment == nil {
			return ir.JSONPath{}, fmt.Errorf("JSON path segment %d is nil", index)
		}
		key, isKey := segment.Key()
		arrayIndex, isIndex := segment.Index()
		if isKey == isIndex {
			return ir.JSONPath{}, fmt.Errorf("JSON path segment %d has an invalid tag", index)
		}
		if isKey {
			bound, err := ir.JSONKeySegment(key)
			if err != nil {
				return ir.JSONPath{}, err
			}
			segments[index] = bound
		} else {
			segments[index] = ir.JSONIndexSegment(uint64(arrayIndex))
		}
	}
	return ir.NewJSONPath(segments...)
}

func requireNoPath(view golem.FrozenJSONPathView) error {
	if view == nil {
		return fmt.Errorf("nil JSON path view")
	}
	if view.Present() || len(view.Segments()) != 0 {
		return fmt.Errorf("non-JSON node carries a JSON path")
	}
	return nil
}

func hasField(view golem.FrozenConditionView) bool {
	_, ok := view.FieldID()
	return ok
}

func hasRelation(view golem.FrozenConditionView) bool {
	_, ok := view.Relation()
	return ok
}

func bindAction(value golem.FrozenAction) (ir.Action, bool) {
	switch value {
	case golem.FrozenActionRead:
		return ir.ActionRead, true
	case golem.FrozenActionCreate:
		return ir.ActionCreate, true
	case golem.FrozenActionUpdate:
		return ir.ActionUpdate, true
	case golem.FrozenActionDelete:
		return ir.ActionDelete, true
	default:
		return 0, false
	}
}

func bindEffect(value golem.FrozenEffect) (ir.Effect, bool) {
	switch value {
	case golem.FrozenEffectGrant:
		return ir.EffectGrant, true
	case golem.FrozenEffectDeny:
		return ir.EffectDeny, true
	default:
		return 0, false
	}
}

func bindLogicalOperator(value golem.FrozenOperator) (ir.LogicalOperator, bool) {
	switch value {
	case golem.FrozenOperatorAnd:
		return ir.LogicalAnd, true
	case golem.FrozenOperatorOr:
		return ir.LogicalOr, true
	case golem.FrozenOperatorNot:
		return ir.LogicalNot, true
	default:
		return 0, false
	}
}

func bindScalarOperator(value golem.FrozenOperator) (ir.OperatorID, bool) {
	values := map[golem.FrozenOperator]ir.OperatorID{
		golem.FrozenOperatorEq: ir.OperatorEqual, golem.FrozenOperatorNe: ir.OperatorNotEqual,
		golem.FrozenOperatorIn: ir.OperatorIn, golem.FrozenOperatorNotIn: ir.OperatorNotIn,
		golem.FrozenOperatorLT: ir.OperatorLessThan, golem.FrozenOperatorLTE: ir.OperatorLessThanOrEqual,
		golem.FrozenOperatorGT: ir.OperatorGreaterThan, golem.FrozenOperatorGTE: ir.OperatorGreaterThanOrEqual,
		golem.FrozenOperatorContains: ir.OperatorContains, golem.FrozenOperatorStartsWith: ir.OperatorStartsWith,
		golem.FrozenOperatorEndsWith: ir.OperatorEndsWith, golem.FrozenOperatorIsNull: ir.OperatorIsNull,
		golem.FrozenOperatorIsNotNull: ir.OperatorIsNotNull,
	}
	result, ok := values[value]
	return result, ok
}

func bindListOperator(value golem.FrozenOperator) (ir.OperatorID, bool) {
	values := map[golem.FrozenOperator]ir.OperatorID{
		golem.FrozenOperatorListEq: ir.OperatorListEqual, golem.FrozenOperatorListHas: ir.OperatorListHas,
		golem.FrozenOperatorListHasEvery: ir.OperatorListHasEvery, golem.FrozenOperatorListHasSome: ir.OperatorListHasSome,
		golem.FrozenOperatorListIsEmpty: ir.OperatorListIsEmpty, golem.FrozenOperatorIsNull: ir.OperatorListIsNull,
		golem.FrozenOperatorIsNotNull: ir.OperatorListIsNotNull,
	}
	result, ok := values[value]
	return result, ok
}

func bindJSONOperator(value golem.FrozenOperator) (ir.OperatorID, bool) {
	switch value {
	case golem.FrozenOperatorIsNull:
		return ir.OperatorJSONIsNull, true
	case golem.FrozenOperatorIsNotNull:
		return ir.OperatorJSONIsNotNull, true
	default:
		return 0, false
	}
}

func bindRelationOperator(value golem.FrozenOperator) (ir.OperatorID, bool) {
	values := map[golem.FrozenOperator]ir.OperatorID{
		golem.FrozenOperatorRelationIs: ir.OperatorRelationIs, golem.FrozenOperatorRelationIsNot: ir.OperatorRelationIsNot,
		golem.FrozenOperatorRelationIsNull: ir.OperatorRelationIsNull, golem.FrozenOperatorRelationIsNotNull: ir.OperatorRelationIsNotNull,
		golem.FrozenOperatorRelationSome: ir.OperatorRelationSome, golem.FrozenOperatorRelationEvery: ir.OperatorRelationEvery,
		golem.FrozenOperatorRelationNone: ir.OperatorRelationNone,
	}
	result, ok := values[value]
	return result, ok
}

func bindMode(value golem.FrozenComparisonMode) (ir.ComparisonMode, bool) {
	if value == golem.FrozenComparisonSensitive {
		return ir.ComparisonSensitive, true
	}
	return 0, false
}

func bindCardinality(value compilerir.RelationCardinality) (ir.RelationCardinality, bool) {
	switch value {
	case compilerir.RelationOne:
		return ir.RelationToOne, true
	case compilerir.RelationMany:
		return ir.RelationToMany, true
	default:
		return 0, false
	}
}

func mapLogicalKind(value compilerir.LogicalTypeKind) (ir.ValueKind, bool) {
	values := map[compilerir.LogicalTypeKind]ir.ValueKind{
		compilerir.TypeBool: ir.ValueBool, compilerir.TypeInt16: ir.ValueInt16, compilerir.TypeInt32: ir.ValueInt32,
		compilerir.TypeInt64: ir.ValueInt64, compilerir.TypeFloat32: ir.ValueFloat32, compilerir.TypeFloat64: ir.ValueFloat64,
		compilerir.TypeDecimal: ir.ValueDecimal, compilerir.TypeString: ir.ValueString, compilerir.TypeBytes: ir.ValueBytes,
		compilerir.TypeUUID: ir.ValueUUID, compilerir.TypeDate: ir.ValueDate, compilerir.TypeTime: ir.ValueTime,
		compilerir.TypeDateTime: ir.ValueDateTime, compilerir.TypeEnum: ir.ValueEnum, compilerir.TypeJSON: ir.ValueJSON,
		compilerir.TypeScalarList: ir.ValueScalarList,
	}
	result, ok := values[value]
	return result, ok
}

func publicKind(value ir.ValueKind) (golem.FrozenValueKind, bool) {
	values := map[ir.ValueKind]golem.FrozenValueKind{
		ir.ValueBool: golem.FrozenValueBool, ir.ValueInt16: golem.FrozenValueInt16, ir.ValueInt32: golem.FrozenValueInt32,
		ir.ValueInt64: golem.FrozenValueInt64, ir.ValueFloat32: golem.FrozenValueFloat32, ir.ValueFloat64: golem.FrozenValueFloat64,
		ir.ValueDecimal: golem.FrozenValueDecimal, ir.ValueString: golem.FrozenValueString, ir.ValueBytes: golem.FrozenValueBytes,
		ir.ValueUUID: golem.FrozenValueUUID, ir.ValueDate: golem.FrozenValueDate, ir.ValueTime: golem.FrozenValueTime,
		ir.ValueDateTime: golem.FrozenValueDateTime,
	}
	result, ok := values[value]
	return result, ok
}

func integerWidth(value ir.ValueKind) uint8 {
	switch value {
	case ir.ValueInt16:
		return 16
	case ir.ValueInt32:
		return 32
	case ir.ValueInt64:
		return 64
	default:
		return 0
	}
}

func fixedID(value string) ([16]byte, error) {
	var result [16]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) || hex.EncodeToString(decoded) != value {
		return result, fmt.Errorf("%q is not a canonical 128-bit lowercase hexadecimal ID", value)
	}
	copy(result[:], decoded)
	return result, nil
}

func modelID(value golem.ModelID) ir.ModelID          { return ir.ModelID(value) }
func fieldID(value golem.FieldID) ir.FieldID          { return ir.FieldID(value) }
func relationID(value golem.RelationID) ir.RelationID { return ir.RelationID(value) }

func (b *binder) failure(code ErrorCode, path, format string, args ...any) error {
	return b.failureFor(code, path, ir.ModelID{}, ir.FieldID{}, ir.RelationID{}, 0, format, args...)
}

func (b *binder) failureFor(code ErrorCode, path string, model ir.ModelID, field ir.FieldID, relation ir.RelationID, operatorID ir.OperatorID, format string, args ...any) error {
	return &Error{Code: code, Path: path, Detail: fmt.Sprintf(format, args...), RulePosition: b.rule, HasRule: b.hasRule, ModelID: model, FieldID: field, RelationID: relation, OperatorID: operatorID}
}

func (b *binder) wrap(code ErrorCode, path string, model ir.ModelID, field ir.FieldID, relation ir.RelationID, operatorID ir.OperatorID, cause error) error {
	return &Error{Code: code, Path: path, Detail: cause.Error(), RulePosition: b.rule, HasRule: b.hasRule, ModelID: model, FieldID: field, RelationID: relation, OperatorID: operatorID, Cause: cause}
}
