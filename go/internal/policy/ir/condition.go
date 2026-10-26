package ir

import (
	"fmt"
	"sort"
	"sync/atomic"
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
	sort.Slice(output, func(i, j int) bool { return requirementLess(output[i], output[j]) })
	unique := output[:0]
	for _, item := range output {
		if len(unique) == 0 || unique[len(unique)-1] != item {
			unique = append(unique, item)
		}
	}
	return unique, nil
}
func requirementLess(left, right Requirement) bool {
	if left.providers != right.providers {
		return left.providers < right.providers
	}
	return left.capability < right.capability
}
func unionRequirements(left, right []Requirement) []Requirement {
	if len(right) == 0 {
		return left
	}
	if len(left) == 0 {
		return right
	}
	if requirementsCover(left, right) {
		return left
	}
	if requirementsCover(right, left) {
		return right
	}
	merged := make([]Requirement, 0, len(left)+len(right))
	i, j := 0, 0
	for i < len(left) && j < len(right) {
		switch {
		case left[i] == right[j]:
			merged = append(merged, left[i])
			i++
			j++
		case requirementLess(left[i], right[j]):
			merged = append(merged, left[i])
			i++
		default:
			merged = append(merged, right[j])
			j++
		}
	}
	merged = append(merged, left[i:]...)
	return append(merged, right[j:]...)
}
func requirementsCover(set, subset []Requirement) bool {
	i := 0
	for _, item := range subset {
		for i < len(set) && requirementLess(set[i], item) {
			i++
		}
		if i == len(set) || set[i] != item {
			return false
		}
		i++
	}
	return true
}

type nodeState struct {
	canonical atomic.Pointer[[]byte]
	normal    atomic.Bool
}

func (state *nodeState) memo() *nodeState { return state }

type conditionNode interface {
	kind() ConditionKind
	modelID() ModelID
	requirementSet() []Requirement
	memo() *nodeState
}
type Condition struct{ node conditionNode }
type constantNode struct {
	nodeState
	model ModelID
	truth bool
}
type logicalNode struct {
	nodeState
	model        ModelID
	operator     LogicalOperator
	children     []Condition
	requirements []Requirement
}
type scalarNode struct {
	nodeState
	model        ModelID
	field        FieldID
	fieldType    TypeRef
	operator     OperatorID
	mode         ComparisonMode
	operand      Operand
	requirements []Requirement
}
type listNode struct {
	nodeState
	model        ModelID
	field        FieldID
	fieldType    TypeRef
	operator     OperatorID
	operand      Operand
	requirements []Requirement
}
type jsonNode struct {
	nodeState
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
	nodeState
	model           ModelID
	field           FieldID
	relation        RelationID
	target          ModelID
	cardinality     RelationCardinality
	operator        OperatorID
	child           Condition
	ownRequirements []Requirement
	requirements    []Requirement
}

func (*constantNode) kind() ConditionKind             { return ConditionConstant }
func (*logicalNode) kind() ConditionKind              { return ConditionLogical }
func (*scalarNode) kind() ConditionKind               { return ConditionScalar }
func (*listNode) kind() ConditionKind                 { return ConditionList }
func (*jsonNode) kind() ConditionKind                 { return ConditionJSON }
func (*relationNode) kind() ConditionKind             { return ConditionRelation }
func (n *constantNode) modelID() ModelID              { return n.model }
func (n *logicalNode) modelID() ModelID               { return n.model }
func (n *scalarNode) modelID() ModelID                { return n.model }
func (n *listNode) modelID() ModelID                  { return n.model }
func (n *jsonNode) modelID() ModelID                  { return n.model }
func (n *relationNode) modelID() ModelID              { return n.model }
func (*constantNode) requirementSet() []Requirement   { return nil }
func (n *logicalNode) requirementSet() []Requirement  { return n.requirements }
func (n *scalarNode) requirementSet() []Requirement   { return n.requirements }
func (n *listNode) requirementSet() []Requirement     { return n.requirements }
func (n *jsonNode) requirementSet() []Requirement     { return n.requirements }
func (n *relationNode) requirementSet() []Requirement { return n.requirements }

func NewConstant(model ModelID, truth bool) (Condition, error) {
	if model == (ModelID{}) {
		return Condition{}, fmt.Errorf("policy IR: condition model ID is zero")
	}
	return Condition{node: &constantNode{model: model, truth: truth}}, nil
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
	var requirements []Requirement
	for i, child := range children {
		if err := child.Validate(); err != nil {
			return Condition{}, fmt.Errorf("policy IR: invalid logical child %d: %w", i, err)
		}
		if child.node.modelID() != model {
			return Condition{}, fmt.Errorf("policy IR: logical child model mismatch")
		}
		requirements = unionRequirements(requirements, child.node.requirementSet())
	}
	copy := append([]Condition(nil), children...)
	return Condition{node: &logicalNode{model: model, operator: operator, children: copy, requirements: requirements}}, nil
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
	return Condition{node: &scalarNode{model: model, field: field, fieldType: typ, operator: operator, mode: mode, operand: operand, requirements: req}}, nil
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
	return Condition{node: &listNode{model: model, field: field, fieldType: typ, operator: operator, operand: operand, requirements: req}}, nil
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
	return Condition{node: &jsonNode{model: model, field: field, fieldType: typ, operator: operator, mode: mode, path: path, operand: operand, requirements: req}}, nil
}
func NewRelation(model ModelID, field FieldID, relation RelationID, target ModelID, cardinality RelationCardinality, operator OperatorID, child *Condition, ownRequirements []Requirement) (Condition, error) {
	if model == (ModelID{}) || field == (FieldID{}) || relation == (RelationID{}) || target == (ModelID{}) || !validOperator(operator) {
		return Condition{}, fmt.Errorf("policy IR: relation node has zero identity")
	}
	if cardinality != RelationToOne && cardinality != RelationToMany {
		return Condition{}, fmt.Errorf("policy IR: invalid relation cardinality")
	}
	owned, err := normalizeRequirements(ownRequirements)
	if err != nil {
		return Condition{}, err
	}
	requirements := owned
	var shared Condition
	if child != nil {
		if err := child.Validate(); err != nil {
			return Condition{}, err
		}
		if child.node.modelID() != target {
			return Condition{}, fmt.Errorf("policy IR: relation child model mismatch")
		}
		shared = *child
		requirements = unionRequirements(owned, shared.node.requirementSet())
	}
	return Condition{node: &relationNode{model: model, field: field, relation: relation, target: target, cardinality: cardinality, operator: operator, child: shared, ownRequirements: owned, requirements: requirements}}, nil
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

func SealNormal(condition Condition) Condition {
	if condition.node != nil {
		condition.node.memo().normal.Store(true)
	}
	return condition
}
func IsSealedNormal(condition Condition) bool {
	return condition.node != nil && condition.node.memo().normal.Load()
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
	return append([]Requirement(nil), condition.node.requirementSet()...)
}
func (condition Condition) Constant() (bool, bool) {
	node, ok := condition.node.(*constantNode)
	if !ok {
		return false, false
	}
	return node.truth, true
}
func (condition Condition) Logical() (LogicalOperator, []Condition, bool) {
	node, ok := condition.node.(*logicalNode)
	if !ok {
		return 0, nil, false
	}
	return node.operator, append([]Condition(nil), node.children...), true
}
func (condition Condition) LogicalOperator() (LogicalOperator, bool) {
	node, ok := condition.node.(*logicalNode)
	if !ok {
		return 0, false
	}
	return node.operator, true
}
func (condition Condition) ChildCount() int {
	node, ok := condition.node.(*logicalNode)
	if !ok {
		return 0
	}
	return len(node.children)
}
func (condition Condition) Child(index int) Condition {
	return condition.node.(*logicalNode).children[index]
}
func (condition Condition) RelationChild() (Condition, bool) {
	node, ok := condition.node.(*relationNode)
	if !ok || node.child.node == nil {
		return Condition{}, false
	}
	return node.child, true
}
func (condition Condition) Field() (FieldID, bool) {
	switch node := condition.node.(type) {
	case *scalarNode:
		return node.field, true
	case *listNode:
		return node.field, true
	case *jsonNode:
		return node.field, true
	case *relationNode:
		return node.field, true
	}
	return FieldID{}, false
}
func (condition Condition) FieldType() (TypeRef, bool) {
	switch node := condition.node.(type) {
	case *scalarNode:
		return node.fieldType, true
	case *listNode:
		return node.fieldType, true
	case *jsonNode:
		return node.fieldType, true
	}
	return TypeRef{}, false
}
func (condition Condition) Operator() (OperatorID, bool) {
	switch node := condition.node.(type) {
	case *scalarNode:
		return node.operator, true
	case *listNode:
		return node.operator, true
	case *jsonNode:
		return node.operator, true
	case *relationNode:
		return node.operator, true
	}
	return 0, false
}
func (condition Condition) Mode() (ComparisonMode, bool) {
	switch node := condition.node.(type) {
	case *scalarNode:
		return node.mode, true
	case *jsonNode:
		return node.mode, true
	}
	return 0, false
}
func (condition Condition) Operand() (Operand, bool) {
	switch node := condition.node.(type) {
	case *scalarNode:
		return node.operand, true
	case *listNode:
		return node.operand, true
	case *jsonNode:
		return node.operand, true
	}
	return Operand{}, false
}
func (condition Condition) Path() (JSONPath, bool) {
	node, ok := condition.node.(*jsonNode)
	if !ok {
		return JSONPath{}, false
	}
	return node.path, true
}
func (condition Condition) Relation() (field FieldID, relation RelationID, target ModelID, cardinality RelationCardinality, child *Condition, ok bool) {
	node, ok := condition.node.(*relationNode)
	if !ok {
		return
	}
	field = node.field
	relation = node.relation
	target = node.target
	cardinality = node.cardinality
	if node.child.node != nil {
		shared := node.child
		child = &shared
	}
	return
}

// RelationOwnRequirements returns only the requirements introduced by the
// relation endpoint itself. This lets normalization rebuild the derived union
// after changing a child without preserving capabilities that belonged only to
// the previous child.
func (condition Condition) RelationOwnRequirements() ([]Requirement, bool) {
	node, ok := condition.node.(*relationNode)
	if !ok {
		return nil, false
	}
	return append([]Requirement(nil), node.ownRequirements...), true
}

func (condition Condition) Validate() error {
	if condition.node == nil {
		return fmt.Errorf("policy IR: zero condition")
	}
	if condition.node.modelID() == (ModelID{}) {
		return fmt.Errorf("policy IR: zero constant model")
	}
	return nil
}

type Rule struct {
	action    Action
	effect    Effect
	model     ModelID
	condition Condition
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
	fields := make([]FieldID, 0, len(rest)+1)
	fields = append(fields, first)
	fields = append(fields, rest...)
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
	for _, field := range fields {
		if field == (FieldID{}) {
			return Rule{}, fmt.Errorf("policy IR: zero field ID")
		}
		if _, ok := seen[field]; ok {
			return Rule{}, fmt.Errorf("policy IR: duplicate field ID")
		}
		seen[field] = struct{}{}
	}
	var shared Condition
	if condition != nil {
		if err := condition.Validate(); err != nil {
			return Rule{}, err
		}
		if condition.node.modelID() != model {
			return Rule{}, fmt.Errorf("policy IR: rule condition model mismatch")
		}
		shared = *condition
	}
	return Rule{action: action, effect: effect, model: model, condition: shared, fields: fields, position: position}, nil
}
func (rule Rule) Action() Action   { return rule.action }
func (rule Rule) Effect() Effect   { return rule.effect }
func (rule Rule) ModelID() ModelID { return rule.model }
func (rule Rule) Position() uint32 { return rule.position }
func (rule Rule) Condition() (Condition, bool) {
	if rule.condition.node == nil {
		return Condition{}, false
	}
	return rule.condition, true
}
func (rule Rule) Fields() (fields []FieldID, modelWide bool) {
	return append([]FieldID(nil), rule.fields...), rule.fields == nil
}
func NewPolicy(model ModelID, rules []Rule) (Policy, error) {
	if model == (ModelID{}) {
		return Policy{}, fmt.Errorf("policy IR: zero policy model")
	}
	copy := make([]Rule, len(rules))
	for i, rule := range rules {
		if rule.model != model || rule.position != uint32(i) {
			return Policy{}, fmt.Errorf("policy IR: rule %d model or position mismatch", i)
		}
		copy[i] = rule
	}
	return Policy{model: model, rules: copy}, nil
}

func (policy Policy) ModelID() ModelID { return policy.model }
func (policy Policy) Rules() []Rule {
	output := make([]Rule, len(policy.rules))
	copy(output, policy.rules)
	return output
}
func (policy Policy) RuleCount() int        { return len(policy.rules) }
func (policy Policy) RuleAt(index int) Rule { return policy.rules[index] }
