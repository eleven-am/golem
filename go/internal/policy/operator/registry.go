// Package operator owns the single closed metadata registry for policy
// operators. It declares portable meaning and shape; evaluators and SQL
// renderers attach implementations to these identities rather than inventing
// provider-local semantics.
package operator

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/internal/policy/ir"
)

type KindMask uint32
type OperandMask uint8
type ModeMask uint8
type PathPolicy uint8
type NullabilityPolicy uint8
type ChildPolicy uint8
type EmptyMeaning uint8
type NullSubjectBehavior uint8
type AgreementStatus uint8

const (
	PathForbidden PathPolicy = 1
	PathAllowed   PathPolicy = 2
)
const (
	NullabilityAny      NullabilityPolicy = 1
	NullabilityRequired NullabilityPolicy = 2
	NullabilityNullable NullabilityPolicy = 3
)
const (
	ChildForbidden ChildPolicy = 1
	ChildRequired  ChildPolicy = 2
)
const (
	EmptyNotApplicable   EmptyMeaning = 1
	EmptyFalse           EmptyMeaning = 2
	EmptyTrue            EmptyMeaning = 3
	EmptyTrueWhenPresent EmptyMeaning = 4
)
const (
	NullSubjectNeverMatches     NullSubjectBehavior = 1
	NullSubjectAlwaysMatches    NullSubjectBehavior = 2
	NullSubjectMatchesNull      NullSubjectBehavior = 3
	NullSubjectMatchesPresent   NullSubjectBehavior = 4
	NullSubjectOperandDependent NullSubjectBehavior = 5
)
const (
	AgreementPending AgreementStatus = 1
	AgreementProved  AgreementStatus = 2
)

func kindMask(kinds ...ir.ValueKind) KindMask {
	var mask KindMask
	for _, kind := range kinds {
		if kind > 0 && kind < 32 {
			mask |= 1 << kind
		}
	}
	return mask
}
func operandMask(kinds ...ir.OperandKind) OperandMask {
	var mask OperandMask
	for _, kind := range kinds {
		if kind > 0 && kind < 8 {
			mask |= 1 << kind
		}
	}
	return mask
}
func modeMask(modes ...ir.ComparisonMode) ModeMask {
	var mask ModeMask
	for _, mode := range modes {
		if mode > 0 && mode < 8 {
			mask |= 1 << mode
		}
	}
	return mask
}
func (mask KindMask) Contains(kind ir.ValueKind) bool {
	return kind > 0 && kind < 32 && mask&(1<<kind) != 0
}
func (mask OperandMask) Contains(kind ir.OperandKind) bool {
	return kind > 0 && kind < 8 && mask&(1<<kind) != 0
}
func (mask ModeMask) Contains(mode ir.ComparisonMode) bool {
	return mode > 0 && mode < 8 && mask&(1<<mode) != 0
}

type Entry struct {
	id                 ir.OperatorID
	name               string
	node               ir.ConditionKind
	fieldKinds         KindMask
	elementKinds       KindMask
	operands           OperandMask
	modes              ModeMask
	nullability        NullabilityPolicy
	path               PathPolicy
	cardinality        ir.RelationCardinality
	child              ChildPolicy
	empty              EmptyMeaning
	nullSubject        NullSubjectBehavior
	twoValued          bool
	declaredProviders  ir.ProviderSet
	agreementProviders ir.ProviderSet
	capability         ir.Capability
}

func (entry Entry) ID() ir.OperatorID                       { return entry.id }
func (entry Entry) Name() string                            { return entry.name }
func (entry Entry) NodeKind() ir.ConditionKind              { return entry.node }
func (entry Entry) AcceptsFieldKind(kind ir.ValueKind) bool { return entry.fieldKinds.Contains(kind) }
func (entry Entry) AcceptsElementKind(kind ir.ValueKind) bool {
	return entry.elementKinds.Contains(kind)
}
func (entry Entry) AcceptsOperand(kind ir.OperandKind) bool { return entry.operands.Contains(kind) }
func (entry Entry) AcceptsMode(mode ir.ComparisonMode) bool { return entry.modes.Contains(mode) }
func (entry Entry) Nullability() NullabilityPolicy          { return entry.nullability }
func (entry Entry) Path() PathPolicy                        { return entry.path }
func (entry Entry) Cardinality() ir.RelationCardinality     { return entry.cardinality }
func (entry Entry) Child() ChildPolicy                      { return entry.child }
func (entry Entry) EmptyMeaning() EmptyMeaning              { return entry.empty }
func (entry Entry) NullSubject() NullSubjectBehavior        { return entry.nullSubject }
func (entry Entry) SQLIsTwoValued() bool                    { return entry.twoValued }
func (entry Entry) DeclaredProviders() ir.ProviderSet       { return entry.declaredProviders }
func (entry Entry) AgreementProviders() ir.ProviderSet      { return entry.agreementProviders }
func (entry Entry) AgreementStatus() AgreementStatus {
	if entry.agreementProviders.Valid() && entry.declaredProviders.IsSubsetOf(entry.agreementProviders) {
		return AgreementProved
	}
	return AgreementPending
}
func (entry Entry) Capability() ir.Capability { return entry.capability }

var (
	allScalar     = kindMask(ir.ValueBool, ir.ValueInt16, ir.ValueInt32, ir.ValueInt64, ir.ValueFloat32, ir.ValueFloat64, ir.ValueDecimal, ir.ValueString, ir.ValueBytes, ir.ValueUUID, ir.ValueDate, ir.ValueTime, ir.ValueDateTime, ir.ValueEnum)
	orderedScalar = kindMask(ir.ValueInt16, ir.ValueInt32, ir.ValueInt64, ir.ValueFloat32, ir.ValueFloat64, ir.ValueDecimal, ir.ValueString, ir.ValueDate, ir.ValueTime, ir.ValueDateTime)
	listElements  = kindMask(ir.ValueBool, ir.ValueInt16, ir.ValueInt32, ir.ValueInt64, ir.ValueFloat32, ir.ValueFloat64, ir.ValueDecimal, ir.ValueString, ir.ValueUUID, ir.ValueDate, ir.ValueTime, ir.ValueDateTime, ir.ValueEnum)
	sensitive     = modeMask(ir.ComparisonSensitive)
	textModes     = modeMask(ir.ComparisonSensitive, ir.ComparisonASCIIInsensitive)
	portable      = ir.PortableProviders()
)

// Every public entry is activated only for the provider set exercised by the
// checked-in live agreement oracle. Adding a provider or operator still starts
// closed until that exact cell is promoted here after the oracle passes.
var entries = []Entry{
	scalar(ir.OperatorEqual, "scalar.equal", allScalar, operandMask(ir.OperandOne), textModes, NullSubjectNeverMatches, EmptyNotApplicable),
	scalar(ir.OperatorNotEqual, "scalar.not_equal", allScalar, operandMask(ir.OperandOne), textModes, NullSubjectAlwaysMatches, EmptyNotApplicable),
	scalar(ir.OperatorIn, "scalar.in", allScalar, operandMask(ir.OperandMany), textModes, NullSubjectNeverMatches, EmptyFalse),
	scalar(ir.OperatorNotIn, "scalar.not_in", allScalar, operandMask(ir.OperandMany), textModes, NullSubjectAlwaysMatches, EmptyTrue),
	scalar(ir.OperatorLessThan, "scalar.less_than", orderedScalar, operandMask(ir.OperandOne), textModes, NullSubjectNeverMatches, EmptyNotApplicable),
	scalar(ir.OperatorLessThanOrEqual, "scalar.less_than_or_equal", orderedScalar, operandMask(ir.OperandOne), textModes, NullSubjectNeverMatches, EmptyNotApplicable),
	scalar(ir.OperatorGreaterThan, "scalar.greater_than", orderedScalar, operandMask(ir.OperandOne), textModes, NullSubjectNeverMatches, EmptyNotApplicable),
	scalar(ir.OperatorGreaterThanOrEqual, "scalar.greater_than_or_equal", orderedScalar, operandMask(ir.OperandOne), textModes, NullSubjectNeverMatches, EmptyNotApplicable),
	scalar(ir.OperatorContains, "scalar.contains", kindMask(ir.ValueString), operandMask(ir.OperandOne), textModes, NullSubjectNeverMatches, EmptyNotApplicable),
	scalar(ir.OperatorStartsWith, "scalar.starts_with", kindMask(ir.ValueString), operandMask(ir.OperandOne), textModes, NullSubjectNeverMatches, EmptyNotApplicable),
	scalar(ir.OperatorEndsWith, "scalar.ends_with", kindMask(ir.ValueString), operandMask(ir.OperandOne), textModes, NullSubjectNeverMatches, EmptyNotApplicable),
	presence(ir.OperatorIsNull, "scalar.is_null", ir.ConditionScalar, allScalar, NullSubjectMatchesNull),
	presence(ir.OperatorIsNotNull, "scalar.is_not_null", ir.ConditionScalar, allScalar, NullSubjectMatchesPresent),

	list(ir.OperatorListEqual, "list.equal", operandMask(ir.OperandOne), EmptyNotApplicable),
	list(ir.OperatorListHas, "list.has", operandMask(ir.OperandOne), EmptyNotApplicable),
	list(ir.OperatorListHasEvery, "list.has_every", operandMask(ir.OperandMany), EmptyTrueWhenPresent),
	list(ir.OperatorListHasSome, "list.has_some", operandMask(ir.OperandMany), EmptyFalse),
	list(ir.OperatorListIsEmpty, "list.is_empty", operandMask(ir.OperandFlag), EmptyNotApplicable),
	presenceWithCapability(ir.OperatorListIsNull, "list.is_null", ir.ConditionList, kindMask(ir.ValueScalarList), NullSubjectMatchesNull, ir.CapabilityScalarListJSON),
	presenceWithCapability(ir.OperatorListIsNotNull, "list.is_not_null", ir.ConditionList, kindMask(ir.ValueScalarList), NullSubjectMatchesPresent, ir.CapabilityScalarListJSON),

	jsonPresence(ir.OperatorJSONIsNull, "json.is_null", NullSubjectMatchesNull),
	jsonPresence(ir.OperatorJSONIsNotNull, "json.is_not_null", NullSubjectMatchesPresent),
	json(ir.OperatorJSONEqual, "json.equal", operandMask(ir.OperandOne, ir.OperandJSONNull), sensitive),
	json(ir.OperatorJSONNotEqual, "json.not_equal", operandMask(ir.OperandOne, ir.OperandJSONNull), sensitive),
	json(ir.OperatorJSONLessThan, "json.less_than", operandMask(ir.OperandOne), sensitive),
	json(ir.OperatorJSONLessThanOrEqual, "json.less_than_or_equal", operandMask(ir.OperandOne), sensitive),
	json(ir.OperatorJSONGreaterThan, "json.greater_than", operandMask(ir.OperandOne), sensitive),
	json(ir.OperatorJSONGreaterThanOrEqual, "json.greater_than_or_equal", operandMask(ir.OperandOne), sensitive),
	json(ir.OperatorJSONStringContains, "json.string_contains", operandMask(ir.OperandOne), textModes),
	json(ir.OperatorJSONStringStartsWith, "json.string_starts_with", operandMask(ir.OperandOne), textModes),
	json(ir.OperatorJSONStringEndsWith, "json.string_ends_with", operandMask(ir.OperandOne), textModes),
	json(ir.OperatorJSONArrayContains, "json.array_contains", operandMask(ir.OperandOne), sensitive),
	json(ir.OperatorJSONArrayStartsWith, "json.array_starts_with", operandMask(ir.OperandOne), sensitive),
	json(ir.OperatorJSONArrayEndsWith, "json.array_ends_with", operandMask(ir.OperandOne), sensitive),

	relation(ir.OperatorRelationIs, "relation.is", ir.RelationToOne, ChildRequired, NullSubjectNeverMatches),
	relation(ir.OperatorRelationIsNot, "relation.is_not", ir.RelationToOne, ChildRequired, NullSubjectAlwaysMatches),
	relation(ir.OperatorRelationIsNull, "relation.is_null", ir.RelationToOne, ChildForbidden, NullSubjectMatchesNull),
	relation(ir.OperatorRelationIsNotNull, "relation.is_not_null", ir.RelationToOne, ChildForbidden, NullSubjectMatchesPresent),
	relation(ir.OperatorRelationSome, "relation.some", ir.RelationToMany, ChildRequired, NullSubjectNeverMatches),
	relation(ir.OperatorRelationEvery, "relation.every", ir.RelationToMany, ChildRequired, NullSubjectNeverMatches),
	relation(ir.OperatorRelationNone, "relation.none", ir.RelationToMany, ChildRequired, NullSubjectNeverMatches),
}

func scalar(id ir.OperatorID, name string, kinds KindMask, operands OperandMask, modes ModeMask, nullSubject NullSubjectBehavior, empty EmptyMeaning) Entry {
	return Entry{id: id, name: name, node: ir.ConditionScalar, fieldKinds: kinds, operands: operands, modes: modes, nullability: NullabilityAny, path: PathForbidden, child: ChildForbidden, empty: empty, nullSubject: nullSubject, twoValued: true, declaredProviders: portable, agreementProviders: portable}
}
func presence(id ir.OperatorID, name string, node ir.ConditionKind, kinds KindMask, nullSubject NullSubjectBehavior) Entry {
	return presenceWithCapability(id, name, node, kinds, nullSubject, 0)
}
func presenceWithCapability(id ir.OperatorID, name string, node ir.ConditionKind, kinds KindMask, nullSubject NullSubjectBehavior, capability ir.Capability) Entry {
	return Entry{id: id, name: name, node: node, fieldKinds: kinds, operands: operandMask(ir.OperandNone), modes: sensitive, nullability: NullabilityNullable, path: PathForbidden, child: ChildForbidden, empty: EmptyNotApplicable, nullSubject: nullSubject, twoValued: true, declaredProviders: portable, agreementProviders: portable, capability: capability}
}
func list(id ir.OperatorID, name string, operands OperandMask, empty EmptyMeaning) Entry {
	return Entry{id: id, name: name, node: ir.ConditionList, fieldKinds: kindMask(ir.ValueScalarList), elementKinds: listElements, operands: operands, modes: sensitive, nullability: NullabilityAny, path: PathForbidden, child: ChildForbidden, empty: empty, nullSubject: NullSubjectNeverMatches, twoValued: true, declaredProviders: portable, agreementProviders: portable, capability: ir.CapabilityScalarListJSON}
}
func jsonPresence(id ir.OperatorID, name string, nullSubject NullSubjectBehavior) Entry {
	return presenceWithCapability(id, name, ir.ConditionJSON, kindMask(ir.ValueJSON), nullSubject, 0)
}
func json(id ir.OperatorID, name string, operands OperandMask, modes ModeMask) Entry {
	return Entry{id: id, name: name, node: ir.ConditionJSON, fieldKinds: kindMask(ir.ValueJSON), operands: operands, modes: modes, nullability: NullabilityAny, path: PathAllowed, child: ChildForbidden, empty: EmptyNotApplicable, nullSubject: NullSubjectOperandDependent, twoValued: true, declaredProviders: portable, agreementProviders: portable, capability: ir.CapabilityExactJSON}
}
func relation(id ir.OperatorID, name string, cardinality ir.RelationCardinality, child ChildPolicy, nullSubject NullSubjectBehavior) Entry {
	empty := EmptyNotApplicable
	if id == ir.OperatorRelationSome {
		empty = EmptyFalse
	} else if id == ir.OperatorRelationEvery || id == ir.OperatorRelationNone {
		empty = EmptyTrue
	}
	return Entry{id: id, name: name, node: ir.ConditionRelation, operands: operandMask(ir.OperandNone), modes: sensitive, nullability: NullabilityAny, path: PathForbidden, cardinality: cardinality, child: child, empty: empty, nullSubject: nullSubject, twoValued: true, declaredProviders: portable, agreementProviders: portable, capability: ir.CapabilityRelationCorrelation}
}

var byID = func() map[ir.OperatorID]Entry {
	result := make(map[ir.OperatorID]Entry, len(entries))
	for _, entry := range entries {
		if _, exists := result[entry.id]; exists {
			panic("duplicate policy operator ID")
		}
		result[entry.id] = entry
	}
	return result
}()

func Lookup(id ir.OperatorID) (Entry, bool) { entry, ok := byID[id]; return entry, ok }
func Entries() []Entry {
	output := append([]Entry(nil), entries...)
	sort.Slice(output, func(i, j int) bool { return output[i].id < output[j].id })
	return output
}

type Shape struct {
	Node        ir.ConditionKind
	FieldType   ir.TypeRef
	Operand     ir.Operand
	Mode        ir.ComparisonMode
	Path        ir.JSONPath
	Cardinality ir.RelationCardinality
	HasChild    bool
	Providers   ir.ProviderSet
}

// ValidateShape validates the closed operator/type/operand cell and derives its
// immutable proof obligations. It intentionally does not claim provider
// agreement; RequireAgreement performs that separate fail-closed gate.
func ValidateShape(id ir.OperatorID, shape Shape) ([]ir.Requirement, error) {
	entry, ok := Lookup(id)
	if !ok {
		return nil, fmt.Errorf("policy operator: unknown operator ID %d", id)
	}
	if shape.Node != entry.node {
		return nil, fmt.Errorf("policy operator %s: node kind mismatch", entry.name)
	}
	if !shape.Providers.Valid() {
		return nil, fmt.Errorf("policy operator %s: invalid provider set", entry.name)
	}
	if entry.node != ir.ConditionRelation {
		if err := shape.FieldType.Validate(); err != nil {
			return nil, fmt.Errorf("policy operator %s: %w", entry.name, err)
		}
		if !entry.fieldKinds.Contains(shape.FieldType.Kind()) {
			return nil, fmt.Errorf("policy operator %s: field kind %d is unsupported", entry.name, shape.FieldType.Kind())
		}
		if entry.nullability == NullabilityNullable && !shape.FieldType.Nullable() {
			return nil, fmt.Errorf("policy operator %s: requires nullable field", entry.name)
		}
		if entry.nullability == NullabilityRequired && shape.FieldType.Nullable() {
			return nil, fmt.Errorf("policy operator %s: rejects nullable field", entry.name)
		}
		if shape.FieldType.Kind() == ir.ValueScalarList {
			element, _ := shape.FieldType.Element()
			if !entry.elementKinds.Contains(element.Kind()) && entry.elementKinds != 0 {
				return nil, fmt.Errorf("policy operator %s: list element kind %d is unsupported", entry.name, element.Kind())
			}
		}
	}
	if err := shape.Operand.Validate(); err != nil {
		return nil, fmt.Errorf("policy operator %s: %w", entry.name, err)
	}
	if !entry.operands.Contains(shape.Operand.Kind()) {
		return nil, fmt.Errorf("policy operator %s: operand kind %d is unsupported", entry.name, shape.Operand.Kind())
	}
	if !entry.modes.Contains(shape.Mode) {
		return nil, fmt.Errorf("policy operator %s: comparison mode %d is unsupported", entry.name, shape.Mode)
	}
	if shape.Mode == ir.ComparisonASCIIInsensitive && shape.FieldType.Kind() != ir.ValueString && entry.node != ir.ConditionJSON {
		return nil, fmt.Errorf("policy operator %s: ASCII-insensitive mode requires a string field", entry.name)
	}
	if entry.path == PathForbidden && len(shape.Path.Segments()) != 0 {
		return nil, fmt.Errorf("policy operator %s: JSON path is forbidden", entry.name)
	}
	if entry.node == ir.ConditionRelation {
		if shape.Cardinality != entry.cardinality {
			return nil, fmt.Errorf("policy operator %s: relation cardinality mismatch", entry.name)
		}
		if shape.HasChild != (entry.child == ChildRequired) {
			return nil, fmt.Errorf("policy operator %s: nested condition shape mismatch", entry.name)
		}
	}
	if err := validateOperand(entry, shape); err != nil {
		return nil, err
	}
	requirements := make([]ir.Requirement, 0, 3)
	add := func(capability ir.Capability) {
		if capability == 0 {
			return
		}
		requirement, err := ir.NewRequirement(shape.Providers, capability)
		if err != nil {
			return
		}
		requirements = append(requirements, requirement)
	}
	add(entry.capability)
	if shape.FieldType.Capability() != 0 {
		add(shape.FieldType.Capability())
	}
	if shape.FieldType.Kind() == ir.ValueString && entry.id != ir.OperatorIsNull && entry.id != ir.OperatorIsNotNull {
		add(ir.CapabilityBinaryText)
	}
	if shape.Mode == ir.ComparisonASCIIInsensitive {
		add(ir.CapabilityASCIIInsensitiveText)
	}
	sort.Slice(requirements, func(i, j int) bool {
		if requirements[i].Providers() != requirements[j].Providers() {
			return requirements[i].Providers() < requirements[j].Providers()
		}
		return requirements[i].Capability() < requirements[j].Capability()
	})
	unique := requirements[:0]
	for _, item := range requirements {
		if len(unique) == 0 || unique[len(unique)-1].Capability() != item.Capability() || unique[len(unique)-1].Providers() != item.Providers() {
			unique = append(unique, item)
		}
	}
	return unique, nil
}

// ValidateCondition proves that a structurally valid IR tree uses registry
// operators in their declared node/type cells and carries exactly the derived
// requirements for the supplied schema provider set.
func ValidateCondition(condition ir.Condition, providers ir.ProviderSet) error {
	if !providers.Valid() {
		return fmt.Errorf("policy operator: invalid provider set")
	}
	if err := condition.Validate(); err != nil {
		return err
	}
	switch condition.Kind() {
	case ir.ConditionConstant:
		if len(condition.Requirements()) != 0 {
			return fmt.Errorf("policy operator: constant carries requirements")
		}
		return nil
	case ir.ConditionLogical:
		_, children, _ := condition.Logical()
		for _, child := range children {
			if err := ValidateCondition(child, providers); err != nil {
				return err
			}
		}
		return nil
	case ir.ConditionScalar, ir.ConditionList, ir.ConditionJSON:
		operatorID, _ := condition.Operator()
		typ, _ := condition.FieldType()
		operand, _ := condition.Operand()
		mode := ir.ComparisonSensitive
		if authoredMode, ok := condition.Mode(); ok {
			mode = authoredMode
		}
		path, _ := condition.Path()
		expected, err := ValidateShape(operatorID, Shape{Node: condition.Kind(), FieldType: typ, Operand: operand, Mode: mode, Path: path, Providers: providers})
		if err != nil {
			return err
		}
		return sameRequirements(condition.Requirements(), expected)
	case ir.ConditionRelation:
		operatorID, _ := condition.Operator()
		_, _, _, cardinality, child, _ := condition.Relation()
		expected, err := ValidateShape(operatorID, Shape{Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Cardinality: cardinality, HasChild: child != nil, Providers: providers})
		if err != nil {
			return err
		}
		if child != nil {
			if err := ValidateCondition(*child, providers); err != nil {
				return err
			}
			expected = append(expected, child.Requirements()...)
		}
		return sameRequirements(condition.Requirements(), expected)
	default:
		return fmt.Errorf("policy operator: unknown condition kind %d", condition.Kind())
	}
}

func sameRequirements(actual, expected []ir.Requirement) error {
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].Providers() != expected[j].Providers() {
			return expected[i].Providers() < expected[j].Providers()
		}
		return expected[i].Capability() < expected[j].Capability()
	})
	unique := expected[:0]
	for _, item := range expected {
		if len(unique) == 0 || unique[len(unique)-1].Providers() != item.Providers() || unique[len(unique)-1].Capability() != item.Capability() {
			unique = append(unique, item)
		}
	}
	if len(actual) != len(unique) {
		return fmt.Errorf("policy operator: condition requirements do not match registry")
	}
	for index := range actual {
		if actual[index].Providers() != unique[index].Providers() || actual[index].Capability() != unique[index].Capability() {
			return fmt.Errorf("policy operator: condition requirements do not match registry")
		}
	}
	return nil
}

func validateOperand(entry Entry, shape Shape) error {
	switch shape.Operand.Kind() {
	case ir.OperandOne:
		value, _ := shape.Operand.One()
		switch entry.node {
		case ir.ConditionScalar:
			if !valueMatchesType(value, shape.FieldType) {
				return fmt.Errorf("policy operator %s: operand kind does not match field", entry.name)
			}
		case ir.ConditionList:
			element, _ := shape.FieldType.Element()
			if entry.id == ir.OperatorListEqual {
				if value.Kind() != ir.ValueScalarList {
					return fmt.Errorf("policy operator %s: whole-list equality requires a list operand", entry.name)
				}
				if !valueMatchesType(value, shape.FieldType) {
					return fmt.Errorf("policy operator %s: list element kind mismatch", entry.name)
				}
			} else if !valueMatchesType(value, element) {
				return fmt.Errorf("policy operator %s: element operand kind mismatch", entry.name)
			}
		case ir.ConditionJSON:
			if value.Kind() != ir.ValueJSON {
				return fmt.Errorf("policy operator %s: requires JSON operand", entry.name)
			}
			jsonValue, _ := value.JSON()
			if entry.id >= ir.OperatorJSONLessThan && entry.id <= ir.OperatorJSONGreaterThanOrEqual && (jsonValue.Kind() != ir.JSONNumber && jsonValue.Kind() != ir.JSONString) {
				return fmt.Errorf("policy operator %s: ordering requires JSON number or string", entry.name)
			}
			if entry.id >= ir.OperatorJSONStringContains && entry.id <= ir.OperatorJSONStringEndsWith && jsonValue.Kind() != ir.JSONString {
				return fmt.Errorf("policy operator %s: string operation requires JSON string", entry.name)
			}
		}
	case ir.OperandMany:
		values, _ := shape.Operand.Many()
		if entry.node == ir.ConditionScalar {
			for _, value := range values {
				if !valueMatchesType(value, shape.FieldType) {
					return fmt.Errorf("policy operator %s: membership operand kind mismatch", entry.name)
				}
			}
		} else if entry.node == ir.ConditionList {
			element, _ := shape.FieldType.Element()
			for _, value := range values {
				if !valueMatchesType(value, element) {
					return fmt.Errorf("policy operator %s: list operand kind mismatch", entry.name)
				}
			}
		}
	}
	return nil
}

func valueMatchesType(value ir.Value, typ ir.TypeRef) bool {
	if value.Kind() != typ.Kind() {
		return false
	}
	switch typ.Kind() {
	case ir.ValueEnum:
		enum, _, ok := value.Enum()
		expected, typed := typ.EnumID()
		return ok && typed && enum == expected
	case ir.ValueDecimal:
		coefficient, scale, ok := value.Decimal()
		if !ok || uint16(scale) > typ.Scale() {
			return false
		}
		return decimalDigits(coefficient)+int(typ.Scale())-int(scale) <= int(typ.Precision())
	case ir.ValueTime:
		microseconds, ok := value.Time()
		return ok && exactlyQuantized(microseconds, typ.Precision(), 6)
	case ir.ValueDateTime:
		_, nanoseconds, ok := value.DateTime()
		return ok && exactlyQuantized(int64(nanoseconds), typ.Precision(), 9)
	case ir.ValueScalarList:
		element, ok := typ.Element()
		if !ok {
			return false
		}
		values, ok := value.List()
		if !ok {
			return false
		}
		for _, item := range values {
			if !valueMatchesType(item, element) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func decimalDigits(value int64) int {
	if value == 0 {
		return 1
	}
	count := 0
	for value != 0 {
		count++
		value /= 10
	}
	return count
}

func exactlyQuantized(value int64, precision, base uint16) bool {
	if precision > base {
		return false
	}
	factor := int64(1)
	for index := precision; index < base; index++ {
		factor *= 10
	}
	return value%factor == 0
}

// RequireAgreement is the runtime activation gate. It accepts only provider
// subsets explicitly promoted by the live Go/SQLite/PostgreSQL matrix.
func RequireAgreement(id ir.OperatorID, providers ir.ProviderSet) error {
	entry, ok := Lookup(id)
	if !ok {
		return fmt.Errorf("policy operator: unknown operator ID %d", id)
	}
	if !providers.Valid() {
		return fmt.Errorf("policy operator %s: invalid provider set", entry.name)
	}
	if !providers.IsSubsetOf(entry.agreementProviders) {
		return fmt.Errorf("policy operator %s: provider agreement is not proved", entry.name)
	}
	return nil
}
