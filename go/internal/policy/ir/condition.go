package ir

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

type JSONPathSegment struct {
	key        string
	arrayIndex uint64
	isIndex    bool
}

type JSONPath struct{ segments []JSONPathSegment }

func JSONKeySegment(key string) (JSONPathSegment, error) {
	if !utf8.ValidString(key) {
		return JSONPathSegment{}, fmt.Errorf("policy IR: JSON path key is not valid UTF-8")
	}
	return JSONPathSegment{key: key}, nil
}
func JSONIndexSegment(index uint64) JSONPathSegment {
	return JSONPathSegment{arrayIndex: index, isIndex: true}
}
func NewJSONPath(segments ...JSONPathSegment) (JSONPath, error) {
	path := JSONPath{segments: append([]JSONPathSegment(nil), segments...)}
	if err := path.validate(); err != nil {
		return JSONPath{}, err
	}
	return path, nil
}
func (path JSONPath) Segments() []JSONPathSegment {
	return append([]JSONPathSegment(nil), path.segments...)
}
func (segment JSONPathSegment) Key() (string, bool)   { return segment.key, !segment.isIndex }
func (segment JSONPathSegment) Index() (uint64, bool) { return segment.arrayIndex, segment.isIndex }
func (path JSONPath) clone() JSONPath {
	return JSONPath{segments: append([]JSONPathSegment(nil), path.segments...)}
}
func (path JSONPath) validate() error {
	for _, segment := range path.segments {
		if !segment.isIndex && !utf8.ValidString(segment.key) {
			return fmt.Errorf("policy IR: invalid UTF-8 JSON path key")
		}
	}
	return nil
}

type Operand struct {
	kind     OperandKind
	one      Value
	many     []Value
	flag     bool
	jsonNull JSONNullKind
}

func NoOperand() Operand { return Operand{kind: OperandNone} }
func OneOperand(value Value) (Operand, error) {
	if err := value.validate(); err != nil {
		return Operand{}, err
	}
	return Operand{kind: OperandOne, one: value.clone()}, nil
}
func ManyOperand(values []Value) (Operand, error) {
	copy := cloneValues(values)
	for i := range copy {
		if err := copy[i].validate(); err != nil {
			return Operand{}, fmt.Errorf("policy IR: invalid operand %d: %w", i, err)
		}
	}
	if copy == nil {
		copy = make([]Value, 0)
	}
	return Operand{kind: OperandMany, many: copy}, nil
}
func FlagOperand(value bool) Operand { return Operand{kind: OperandFlag, flag: value} }
func JSONNullOperand(kind JSONNullKind) (Operand, error) {
	if kind < JSONDbNull || kind > JSONAnyNull {
		return Operand{}, fmt.Errorf("policy IR: invalid JSON null kind %d", kind)
	}
	return Operand{kind: OperandJSONNull, jsonNull: kind}, nil
}
func (operand Operand) Kind() OperandKind  { return operand.kind }
func (operand Operand) One() (Value, bool) { return operand.one.clone(), operand.kind == OperandOne }
func (operand Operand) Many() ([]Value, bool) {
	return cloneValues(operand.many), operand.kind == OperandMany
}
func (operand Operand) Flag() (bool, bool) { return operand.flag, operand.kind == OperandFlag }
func (operand Operand) JSONNull() (JSONNullKind, bool) {
	return operand.jsonNull, operand.kind == OperandJSONNull
}
func (operand Operand) Validate() error { return operand.validate() }
func (operand Operand) clone() Operand {
	copy := operand
	copy.one = operand.one.clone()
	copy.many = cloneValues(operand.many)
	return copy
}
func (operand Operand) validate() error {
	switch operand.kind {
	case OperandNone:
	case OperandOne:
		if err := operand.one.validate(); err != nil {
			return err
		}
	case OperandMany:
		if operand.many == nil {
			return fmt.Errorf("policy IR: many operand must distinguish empty from absent")
		}
		for _, value := range operand.many {
			if err := value.validate(); err != nil {
				return err
			}
		}
	case OperandFlag:
	case OperandJSONNull:
		if operand.jsonNull < JSONDbNull || operand.jsonNull > JSONAnyNull {
			return fmt.Errorf("policy IR: invalid JSON null operand")
		}
	default:
		return fmt.Errorf("policy IR: invalid operand kind %d", operand.kind)
	}
	if operand.kind != OperandOne && operand.one.kind != 0 || operand.kind != OperandMany && operand.many != nil || operand.kind != OperandFlag && operand.flag || operand.kind != OperandJSONNull && operand.jsonNull != 0 {
		return fmt.Errorf("policy IR: operand kind %d populates an inactive union member", operand.kind)
	}
	return nil
}

type Requirement struct {
	providers  ProviderSet
	capability Capability
}

func NewRequirement(providers ProviderSet, capability Capability) (Requirement, error) {
	value := Requirement{providers: providers, capability: capability}
	if err := value.validate(); err != nil {
		return Requirement{}, err
	}
	return value, nil
}
func (value Requirement) Providers() ProviderSet { return value.providers }
func (value Requirement) Capability() Capability { return value.capability }
func (value Requirement) validate() error {
	if !value.providers.Valid() {
		return fmt.Errorf("policy IR: invalid requirement provider set")
	}
	if !validCapability(value.capability) {
		return fmt.Errorf("policy IR: unknown requirement capability %d", value.capability)
	}
	return nil
}
func normalizeRequirements(input []Requirement) ([]Requirement, error) {
	output := append([]Requirement(nil), input...)
	for _, item := range output {
		if err := item.validate(); err != nil {
			return nil, err
		}
	}
	sort.Slice(output, func(i, j int) bool {
		if output[i].providers != output[j].providers {
			return output[i].providers < output[j].providers
		}
		return output[i].capability < output[j].capability
	})
	unique := output[:0]
	for _, item := range output {
		if len(unique) == 0 || unique[len(unique)-1] != item {
			unique = append(unique, item)
		}
	}
	return unique, nil
}

type conditionNode interface {
	conditionNode()
	kind() ConditionKind
	modelID() ModelID
	requirementsCopy() []Requirement
}
type Condition struct{ node conditionNode }
type constantNode struct {
	model ModelID
	truth bool
}
type logicalNode struct {
	model        ModelID
	operator     LogicalOperator
	children     []Condition
	requirements []Requirement
}
type scalarNode struct {
	model        ModelID
	field        FieldID
	fieldType    TypeRef
	operator     OperatorID
	mode         ComparisonMode
	operand      Operand
	requirements []Requirement
}
type listNode struct {
	model        ModelID
	field        FieldID
	fieldType    TypeRef
	operator     OperatorID
	operand      Operand
	requirements []Requirement
}
type jsonNode struct {
	model        ModelID
	field        FieldID
	fieldType    TypeRef
	operator     OperatorID
	mode         ComparisonMode
	path         JSONPath
	operand      Operand
	requirements []Requirement
}
type relationNode struct {
	model           ModelID
	field           FieldID
	relation        RelationID
	target          ModelID
	cardinality     RelationCardinality
	operator        OperatorID
	child           *Condition
	ownRequirements []Requirement
	requirements    []Requirement
}

func (constantNode) conditionNode()                  {}
func (logicalNode) conditionNode()                   {}
func (scalarNode) conditionNode()                    {}
func (listNode) conditionNode()                      {}
func (jsonNode) conditionNode()                      {}
func (relationNode) conditionNode()                  {}
func (constantNode) kind() ConditionKind             { return ConditionConstant }
func (logicalNode) kind() ConditionKind              { return ConditionLogical }
func (scalarNode) kind() ConditionKind               { return ConditionScalar }
func (listNode) kind() ConditionKind                 { return ConditionList }
func (jsonNode) kind() ConditionKind                 { return ConditionJSON }
func (relationNode) kind() ConditionKind             { return ConditionRelation }
func (n constantNode) modelID() ModelID              { return n.model }
func (n logicalNode) modelID() ModelID               { return n.model }
func (n scalarNode) modelID() ModelID                { return n.model }
func (n listNode) modelID() ModelID                  { return n.model }
func (n jsonNode) modelID() ModelID                  { return n.model }
func (n relationNode) modelID() ModelID              { return n.model }
func (constantNode) requirementsCopy() []Requirement { return nil }
func (n logicalNode) requirementsCopy() []Requirement {
	return append([]Requirement(nil), n.requirements...)
}
func (n scalarNode) requirementsCopy() []Requirement {
	return append([]Requirement(nil), n.requirements...)
}
func (n listNode) requirementsCopy() []Requirement {
	return append([]Requirement(nil), n.requirements...)
}
func (n jsonNode) requirementsCopy() []Requirement {
	return append([]Requirement(nil), n.requirements...)
}
func (n relationNode) requirementsCopy() []Requirement {
	return append([]Requirement(nil), n.requirements...)
}

func NewConstant(model ModelID, truth bool) (Condition, error) {
	if model == (ModelID{}) {
		return Condition{}, fmt.Errorf("policy IR: condition model ID is zero")
	}
	return Condition{node: constantNode{model: model, truth: truth}}, nil
}
func NewLogical(model ModelID, operator LogicalOperator, children []Condition) (Condition, error) {
	if model == (ModelID{}) {
		return Condition{}, fmt.Errorf("policy IR: logical model ID is zero")
	}
	if operator == LogicalNot && len(children) != 1 {
		return Condition{}, fmt.Errorf("policy IR: not requires exactly one child")
	}
	if (operator == LogicalAnd || operator == LogicalOr) && len(children) < 2 {
		return Condition{}, fmt.Errorf("policy IR: normalized and/or requires at least two children")
	}
	if operator < LogicalAnd || operator > LogicalNot {
		return Condition{}, fmt.Errorf("policy IR: invalid logical operator")
	}
	copy := cloneConditions(children)
	requirements := make([]Requirement, 0)
	for i, child := range copy {
		if err := child.Validate(); err != nil {
			return Condition{}, fmt.Errorf("policy IR: invalid logical child %d: %w", i, err)
		}
		if child.ModelID() != model {
			return Condition{}, fmt.Errorf("policy IR: logical child model mismatch")
		}
		requirements = append(requirements, child.Requirements()...)
	}
	requirements, _ = normalizeRequirements(requirements)
	return Condition{node: logicalNode{model: model, operator: operator, children: copy, requirements: requirements}}, nil
}
func NewScalar(model ModelID, field FieldID, typ TypeRef, operator OperatorID, mode ComparisonMode, operand Operand, requirements []Requirement) (Condition, error) {
	if err := validateLeaf(model, field, typ, operator, operand); err != nil {
		return Condition{}, err
	}
	if typ.kind == ValueScalarList || typ.kind == ValueJSON {
		return Condition{}, fmt.Errorf("policy IR: scalar node requires scalar field type")
	}
	if !validMode(mode) {
		return Condition{}, fmt.Errorf("policy IR: invalid comparison mode")
	}
	req, err := normalizeRequirements(requirements)
	if err != nil {
		return Condition{}, err
	}
	return Condition{node: scalarNode{model: model, field: field, fieldType: typ.clone(), operator: operator, mode: mode, operand: operand.clone(), requirements: req}}, nil
}
func NewList(model ModelID, field FieldID, typ TypeRef, operator OperatorID, operand Operand, requirements []Requirement) (Condition, error) {
	if err := validateLeaf(model, field, typ, operator, operand); err != nil {
		return Condition{}, err
	}
	if typ.kind != ValueScalarList {
		return Condition{}, fmt.Errorf("policy IR: list node requires scalar-list field type")
	}
	req, err := normalizeRequirements(requirements)
	if err != nil {
		return Condition{}, err
	}
	return Condition{node: listNode{model: model, field: field, fieldType: typ.clone(), operator: operator, operand: operand.clone(), requirements: req}}, nil
}
func NewJSON(model ModelID, field FieldID, typ TypeRef, operator OperatorID, mode ComparisonMode, path JSONPath, operand Operand, requirements []Requirement) (Condition, error) {
	if err := validateLeaf(model, field, typ, operator, operand); err != nil {
		return Condition{}, err
	}
	if typ.kind != ValueJSON {
		return Condition{}, fmt.Errorf("policy IR: JSON node requires JSON field type")
	}
	if !validMode(mode) {
		return Condition{}, fmt.Errorf("policy IR: invalid comparison mode")
	}
	if err := path.validate(); err != nil {
		return Condition{}, err
	}
	req, err := normalizeRequirements(requirements)
	if err != nil {
		return Condition{}, err
	}
	return Condition{node: jsonNode{model: model, field: field, fieldType: typ.clone(), operator: operator, mode: mode, path: path.clone(), operand: operand.clone(), requirements: req}}, nil
}
func NewRelation(model ModelID, field FieldID, relation RelationID, target ModelID, cardinality RelationCardinality, operator OperatorID, child *Condition, ownRequirements []Requirement) (Condition, error) {
	if model == (ModelID{}) || field == (FieldID{}) || relation == (RelationID{}) || target == (ModelID{}) || !validOperator(operator) {
		return Condition{}, fmt.Errorf("policy IR: relation node has zero identity")
	}
	if cardinality != RelationToOne && cardinality != RelationToMany {
		return Condition{}, fmt.Errorf("policy IR: invalid relation cardinality")
	}
	var childCopy *Condition
	owned, err := normalizeRequirements(ownRequirements)
	if err != nil {
		return Condition{}, err
	}
	requirements := append([]Requirement(nil), owned...)
	if child != nil {
		if err := child.Validate(); err != nil {
			return Condition{}, err
		}
		if child.ModelID() != target {
			return Condition{}, fmt.Errorf("policy IR: relation child model mismatch")
		}
		copy := child.clone()
		childCopy = &copy
		requirements = append(requirements, child.Requirements()...)
	}
	requirements, err = normalizeRequirements(requirements)
	if err != nil {
		return Condition{}, err
	}
	return Condition{node: relationNode{model: model, field: field, relation: relation, target: target, cardinality: cardinality, operator: operator, child: childCopy, ownRequirements: owned, requirements: requirements}}, nil
}
func validateLeaf(model ModelID, field FieldID, typ TypeRef, operator OperatorID, operand Operand) error {
	if model == (ModelID{}) || field == (FieldID{}) || !validOperator(operator) {
		return fmt.Errorf("policy IR: leaf has zero identity")
	}
	if err := typ.validate(); err != nil {
		return err
	}
	return operand.validate()
}

func (condition Condition) Kind() ConditionKind {
	if condition.node == nil {
		return 0
	}
	return condition.node.kind()
}
func (condition Condition) ModelID() ModelID {
	if condition.node == nil {
		return ModelID{}
	}
	return condition.node.modelID()
}
func (condition Condition) Requirements() []Requirement {
	if condition.node == nil {
		return nil
	}
	return condition.node.requirementsCopy()
}
func (condition Condition) Constant() (bool, bool) {
	node, ok := condition.node.(constantNode)
	return node.truth, ok
}
func (condition Condition) Logical() (LogicalOperator, []Condition, bool) {
	node, ok := condition.node.(logicalNode)
	if !ok {
		return 0, nil, false
	}
	return node.operator, cloneConditions(node.children), true
}
func (condition Condition) Field() (FieldID, bool) {
	switch node := condition.node.(type) {
	case scalarNode:
		return node.field, true
	case listNode:
		return node.field, true
	case jsonNode:
		return node.field, true
	case relationNode:
		return node.field, true
	}
	return FieldID{}, false
}
func (condition Condition) FieldType() (TypeRef, bool) {
	switch node := condition.node.(type) {
	case scalarNode:
		return node.fieldType.clone(), true
	case listNode:
		return node.fieldType.clone(), true
	case jsonNode:
		return node.fieldType.clone(), true
	}
	return TypeRef{}, false
}
func (condition Condition) Operator() (OperatorID, bool) {
	switch node := condition.node.(type) {
	case scalarNode:
		return node.operator, true
	case listNode:
		return node.operator, true
	case jsonNode:
		return node.operator, true
	case relationNode:
		return node.operator, true
	}
	return 0, false
}
func (condition Condition) Mode() (ComparisonMode, bool) {
	switch node := condition.node.(type) {
	case scalarNode:
		return node.mode, true
	case jsonNode:
		return node.mode, true
	}
	return 0, false
}
func (condition Condition) Operand() (Operand, bool) {
	switch node := condition.node.(type) {
	case scalarNode:
		return node.operand.clone(), true
	case listNode:
		return node.operand.clone(), true
	case jsonNode:
		return node.operand.clone(), true
	}
	return Operand{}, false
}
func (condition Condition) Path() (JSONPath, bool) {
	node, ok := condition.node.(jsonNode)
	return node.path.clone(), ok
}
func (condition Condition) Relation() (field FieldID, relation RelationID, target ModelID, cardinality RelationCardinality, child *Condition, ok bool) {
	node, ok := condition.node.(relationNode)
	if !ok {
		return
	}
	field = node.field
	relation = node.relation
	target = node.target
	cardinality = node.cardinality
	if node.child != nil {
		copy := node.child.clone()
		child = &copy
	}
	return
}

// RelationOwnRequirements returns only the requirements introduced by the
// relation endpoint itself. This lets normalization rebuild the derived union
// after changing a child without preserving capabilities that belonged only to
// the previous child.
func (condition Condition) RelationOwnRequirements() ([]Requirement, bool) {
	node, ok := condition.node.(relationNode)
	if !ok {
		return nil, false
	}
	return append([]Requirement(nil), node.ownRequirements...), true
}
func (condition Condition) clone() Condition {
	switch node := condition.node.(type) {
	case constantNode:
		return Condition{node: node}
	case logicalNode:
		node.children = cloneConditions(node.children)
		node.requirements = append([]Requirement(nil), node.requirements...)
		return Condition{node: node}
	case scalarNode:
		node.fieldType = node.fieldType.clone()
		node.operand = node.operand.clone()
		node.requirements = append([]Requirement(nil), node.requirements...)
		return Condition{node: node}
	case listNode:
		node.fieldType = node.fieldType.clone()
		node.operand = node.operand.clone()
		node.requirements = append([]Requirement(nil), node.requirements...)
		return Condition{node: node}
	case jsonNode:
		node.fieldType = node.fieldType.clone()
		node.path = node.path.clone()
		node.operand = node.operand.clone()
		node.requirements = append([]Requirement(nil), node.requirements...)
		return Condition{node: node}
	case relationNode:
		if node.child != nil {
			copy := node.child.clone()
			node.child = &copy
		}
		node.requirements = append([]Requirement(nil), node.requirements...)
		node.ownRequirements = append([]Requirement(nil), node.ownRequirements...)
		return Condition{node: node}
	default:
		return Condition{}
	}
}
func cloneConditions(input []Condition) []Condition {
	output := make([]Condition, len(input))
	for i := range input {
		output[i] = input[i].clone()
	}
	return output
}

func (condition Condition) Validate() error {
	if condition.node == nil {
		return fmt.Errorf("policy IR: zero condition")
	}
	switch node := condition.node.(type) {
	case constantNode:
		if node.model == (ModelID{}) {
			return fmt.Errorf("policy IR: zero constant model")
		}
	case logicalNode:
		rebuilt, err := NewLogical(node.model, node.operator, node.children)
		if err != nil {
			return err
		}
		return requireSameRequirements(node.requirements, rebuilt.Requirements())
	case scalarNode:
		rebuilt, err := NewScalar(node.model, node.field, node.fieldType, node.operator, node.mode, node.operand, node.requirements)
		if err != nil {
			return err
		}
		return requireSameRequirements(node.requirements, rebuilt.Requirements())
	case listNode:
		rebuilt, err := NewList(node.model, node.field, node.fieldType, node.operator, node.operand, node.requirements)
		if err != nil {
			return err
		}
		return requireSameRequirements(node.requirements, rebuilt.Requirements())
	case jsonNode:
		rebuilt, err := NewJSON(node.model, node.field, node.fieldType, node.operator, node.mode, node.path, node.operand, node.requirements)
		if err != nil {
			return err
		}
		return requireSameRequirements(node.requirements, rebuilt.Requirements())
	case relationNode:
		rebuilt, err := NewRelation(node.model, node.field, node.relation, node.target, node.cardinality, node.operator, node.child, node.ownRequirements)
		if err != nil {
			return err
		}
		return requireSameRequirements(node.requirements, rebuilt.Requirements())
	default:
		return fmt.Errorf("policy IR: unrecognized condition node")
	}
	return nil
}

func requireSameRequirements(left, right []Requirement) error {
	if len(left) != len(right) {
		return fmt.Errorf("policy IR: requirements are not canonical")
	}
	for index := range left {
		if left[index] != right[index] {
			return fmt.Errorf("policy IR: requirements are not sorted and deduplicated")
		}
	}
	return nil
}

type Rule struct {
	action    Action
	effect    Effect
	model     ModelID
	condition *Condition
	fields    []FieldID
	position  uint32
}
type Policy struct {
	model ModelID
	rules []Rule
}

func NewModelRule(action Action, effect Effect, model ModelID, condition *Condition, position uint32) (Rule, error) {
	return newRule(action, effect, model, condition, nil, true, position)
}
func NewFieldRule(action Action, effect Effect, model ModelID, condition *Condition, first FieldID, rest []FieldID, position uint32) (Rule, error) {
	fields := append([]FieldID{first}, rest...)
	return newRule(action, effect, model, condition, fields, false, position)
}
func newRule(action Action, effect Effect, model ModelID, condition *Condition, fields []FieldID, modelWide bool, position uint32) (Rule, error) {
	if !validAction(action) || !validEffect(effect) || model == (ModelID{}) {
		return Rule{}, fmt.Errorf("policy IR: invalid rule identity")
	}
	if !modelWide && (action == ActionDelete || len(fields) == 0) {
		return Rule{}, fmt.Errorf("policy IR: invalid field-scoped rule")
	}
	seen := map[FieldID]struct{}{}
	copyFields := append([]FieldID(nil), fields...)
	for _, field := range copyFields {
		if field == (FieldID{}) {
			return Rule{}, fmt.Errorf("policy IR: zero field ID")
		}
		if _, ok := seen[field]; ok {
			return Rule{}, fmt.Errorf("policy IR: duplicate field ID")
		}
		seen[field] = struct{}{}
	}
	var copyCondition *Condition
	if condition != nil {
		if err := condition.Validate(); err != nil {
			return Rule{}, err
		}
		if condition.ModelID() != model {
			return Rule{}, fmt.Errorf("policy IR: rule condition model mismatch")
		}
		copy := condition.clone()
		copyCondition = &copy
	}
	return Rule{action: action, effect: effect, model: model, condition: copyCondition, fields: copyFields, position: position}, nil
}
func (rule Rule) Action() Action   { return rule.action }
func (rule Rule) Effect() Effect   { return rule.effect }
func (rule Rule) ModelID() ModelID { return rule.model }
func (rule Rule) Position() uint32 { return rule.position }
func (rule Rule) Condition() (Condition, bool) {
	if rule.condition == nil {
		return Condition{}, false
	}
	return rule.condition.clone(), true
}
func (rule Rule) Fields() (fields []FieldID, modelWide bool) {
	return append([]FieldID(nil), rule.fields...), rule.fields == nil
}
func (rule Rule) clone() Rule {
	copy := rule
	copy.fields = append([]FieldID(nil), rule.fields...)
	if rule.condition != nil {
		condition := rule.condition.clone()
		copy.condition = &condition
	}
	return copy
}
func NewPolicy(model ModelID, rules []Rule) (Policy, error) {
	if model == (ModelID{}) {
		return Policy{}, fmt.Errorf("policy IR: zero policy model")
	}
	copy := make([]Rule, len(rules))
	for i, rule := range rules {
		if err := rule.validate(); err != nil {
			return Policy{}, fmt.Errorf("policy IR: invalid rule %d: %w", i, err)
		}
		if rule.model != model || rule.position != uint32(i) {
			return Policy{}, fmt.Errorf("policy IR: rule %d model or position mismatch", i)
		}
		copy[i] = rule.clone()
	}
	return Policy{model: model, rules: copy}, nil
}

func (rule Rule) validate() error {
	if rule.fields == nil {
		_, err := NewModelRule(rule.action, rule.effect, rule.model, rule.condition, rule.position)
		return err
	}
	if len(rule.fields) == 0 {
		return fmt.Errorf("field-scoped rule has no fields")
	}
	_, err := NewFieldRule(rule.action, rule.effect, rule.model, rule.condition, rule.fields[0], rule.fields[1:], rule.position)
	return err
}
func (policy Policy) ModelID() ModelID { return policy.model }
func (policy Policy) Rules() []Rule {
	output := make([]Rule, len(policy.rules))
	for i := range policy.rules {
		output[i] = policy.rules[i].clone()
	}
	return output
}
