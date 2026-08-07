package ir

import (
	"bytes"
	"fmt"
	"sort"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type RelationHop struct {
	model    policyir.ModelID
	field    policyir.FieldID
	relation policyir.RelationID
	target   policyir.ModelID
}

func NewRelationHop(model policyir.ModelID, field policyir.FieldID, relation policyir.RelationID, target policyir.ModelID) (RelationHop, error) {
	value := RelationHop{model: model, field: field, relation: relation, target: target}
	if value.model == (policyir.ModelID{}) || value.field == (policyir.FieldID{}) || value.relation == (policyir.RelationID{}) || value.target == (policyir.ModelID{}) {
		return RelationHop{}, fmt.Errorf("P4_MUTATION_IR_DEPENDENCY: relation hop identities must be non-zero")
	}
	return value, nil
}

func (hop RelationHop) ModelID() policyir.ModelID       { return hop.model }
func (hop RelationHop) FieldID() policyir.FieldID       { return hop.field }
func (hop RelationHop) RelationID() policyir.RelationID { return hop.relation }
func (hop RelationHop) TargetModelID() policyir.ModelID { return hop.target }

// Dependency is one exact scalar dependency, optionally reached through a
// relation path. It is intentionally expressed with P1/P2 identities only;
// relation shape validation remains the binder's registry-backed job.
type Dependency struct {
	root  policyir.ModelID
	path  []RelationHop
	field policyir.FieldID
}

func NewDependency(root policyir.ModelID, path []RelationHop, field policyir.FieldID) (Dependency, error) {
	value := Dependency{root: root, path: append([]RelationHop(nil), path...), field: field}
	if err := value.validate(); err != nil {
		return Dependency{}, err
	}
	return value, nil
}

func (dependency Dependency) RootModelID() policyir.ModelID { return dependency.root }
func (dependency Dependency) Path() []RelationHop {
	return append([]RelationHop(nil), dependency.path...)
}
func (dependency Dependency) FieldID() policyir.FieldID { return dependency.field }

func (dependency Dependency) validate() error {
	if dependency.root == (policyir.ModelID{}) || dependency.field == (policyir.FieldID{}) {
		return fmt.Errorf("P4_MUTATION_IR_DEPENDENCY: root model and terminal field are required")
	}
	current := dependency.root
	for index, hop := range dependency.path {
		if hop.model == (policyir.ModelID{}) || hop.field == (policyir.FieldID{}) || hop.relation == (policyir.RelationID{}) || hop.target == (policyir.ModelID{}) {
			return fmt.Errorf("P4_MUTATION_IR_DEPENDENCY: hop %d has zero identities", index)
		}
		if hop.model != current {
			return fmt.Errorf("P4_MUTATION_IR_DEPENDENCY: hop %d does not continue the relation path", index)
		}
		current = hop.target
	}
	return nil
}

type ImageRequirements struct {
	model        policyir.ModelID
	fields       []policyir.FieldID
	dependencies []Dependency
}

func NewImageRequirements(model policyir.ModelID, fields []policyir.FieldID, dependencies []Dependency) (ImageRequirements, error) {
	if model == (policyir.ModelID{}) {
		return ImageRequirements{}, fmt.Errorf("P4_MUTATION_IR_IMAGE: model identity is zero")
	}
	result := ImageRequirements{model: model, fields: append([]policyir.FieldID(nil), fields...), dependencies: cloneDependencies(dependencies)}
	for _, dependency := range result.dependencies {
		if dependency.root != model {
			return ImageRequirements{}, fmt.Errorf("P4_MUTATION_IR_IMAGE: dependency root model mismatch")
		}
	}
	var err error
	result.fields, err = normalizeFields(result.fields)
	if err != nil {
		return ImageRequirements{}, err
	}
	sort.Slice(result.dependencies, func(i, j int) bool { return compareDependency(result.dependencies[i], result.dependencies[j]) < 0 })
	for index := range result.dependencies {
		if err := result.dependencies[index].validate(); err != nil {
			return ImageRequirements{}, err
		}
		if index > 0 && compareDependency(result.dependencies[index-1], result.dependencies[index]) == 0 {
			return ImageRequirements{}, fmt.Errorf("P4_MUTATION_IR_IMAGE: duplicate dependency")
		}
	}
	return result, nil
}

func (requirements ImageRequirements) ModelID() policyir.ModelID { return requirements.model }
func (requirements ImageRequirements) Fields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), requirements.fields...)
}
func (requirements ImageRequirements) Dependencies() []Dependency {
	return cloneDependencies(requirements.dependencies)
}
func (requirements ImageRequirements) clone() ImageRequirements {
	return ImageRequirements{model: requirements.model, fields: requirements.Fields(), dependencies: requirements.Dependencies()}
}

type SelectionRequirement struct {
	action     policyir.Action
	constraint policyir.Condition
}

func NewSelectionRequirement(action policyir.Action, constraint policyir.Condition) (SelectionRequirement, error) {
	if action != policyir.ActionUpdate && action != policyir.ActionDelete {
		return SelectionRequirement{}, fmt.Errorf("P4_MUTATION_IR_SELECTION: selecting action must be update or delete")
	}
	if err := constraint.Validate(); err != nil {
		return SelectionRequirement{}, fmt.Errorf("P4_MUTATION_IR_SELECTION: invalid constraint: %w", err)
	}
	return SelectionRequirement{action: action, constraint: constraint}, nil
}

func (requirement SelectionRequirement) Action() policyir.Action { return requirement.action }
func (requirement SelectionRequirement) Constraint() policyir.Condition {
	return requirement.constraint
}

type FieldAuthorization struct {
	field     policyir.FieldID
	condition policyir.Condition
}

func NewFieldAuthorization(field policyir.FieldID, condition policyir.Condition) (FieldAuthorization, error) {
	if field == (policyir.FieldID{}) {
		return FieldAuthorization{}, fmt.Errorf("P4_MUTATION_IR_FIELD_AUTHORIZATION: field identity is zero")
	}
	if err := condition.Validate(); err != nil {
		return FieldAuthorization{}, fmt.Errorf("P4_MUTATION_IR_FIELD_AUTHORIZATION: invalid condition: %w", err)
	}
	return FieldAuthorization{field: field, condition: condition}, nil
}

func (authorization FieldAuthorization) FieldID() policyir.FieldID { return authorization.field }
func (authorization FieldAuthorization) Condition() policyir.Condition {
	return authorization.condition
}

func normalizeFieldAuthorizations(model policyir.ModelID, input []FieldAuthorization) ([]FieldAuthorization, error) {
	result := append([]FieldAuthorization(nil), input...)
	for index, authorization := range result {
		if authorization.field == (policyir.FieldID{}) {
			return nil, fmt.Errorf("P4_MUTATION_IR_FIELD_AUTHORIZATION: authorization %d has a zero field", index)
		}
		if err := authorization.condition.Validate(); err != nil || authorization.condition.ModelID() != model {
			return nil, fmt.Errorf("P4_MUTATION_IR_FIELD_AUTHORIZATION: authorization %d has an invalid condition or model", index)
		}
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i].field[:], result[j].field[:]) < 0 })
	for index := 1; index < len(result); index++ {
		if result[index-1].field == result[index].field {
			return nil, fmt.Errorf("P4_MUTATION_IR_FIELD_AUTHORIZATION: duplicate field")
		}
	}
	return result, nil
}

type HookRequirement struct {
	phase     HookPhase
	operation HookOperation
}

func NewHookRequirement(phase HookPhase, operation HookOperation) (HookRequirement, error) {
	if phase < BeforeHook || phase > AfterCommitHook || operation < HookCreate || operation > HookDeleteMany {
		return HookRequirement{}, fmt.Errorf("P4_MUTATION_IR_HOOK: invalid hook phase or operation")
	}
	return HookRequirement{phase: phase, operation: operation}, nil
}

func (requirement HookRequirement) Phase() HookPhase         { return requirement.phase }
func (requirement HookRequirement) Operation() HookOperation { return requirement.operation }

type FactRequirement struct {
	enabled               bool
	action                FactAction
	eventSchema           [32]byte
	beforeIdentity        []policyir.FieldID
	afterIdentity         []policyir.FieldID
	deleteSnapshotState   DeleteSnapshotState
	privateDeleteSnapshot []policyir.FieldID
}

func (requirement FactRequirement) WithEventSchema(digest [32]byte) (FactRequirement, error) {
	if !requirement.enabled || digest == ([32]byte{}) {
		return FactRequirement{}, fmt.Errorf("P7_MUTATION_IR_FACT: enabled fact and event-schema fingerprint are required")
	}
	requirement.eventSchema = digest
	return requirement, nil
}

// DeleteSnapshotState distinguishes a sufficient stored-scalar pre-image from
// a delete whose later subscription authorization must fail closed. Relation
// hydration is never implied by a scalar snapshot.
type DeleteSnapshotState uint8

const (
	DeleteSnapshotNotApplicable DeleteSnapshotState = 0
	DeleteSnapshotUnverifiable  DeleteSnapshotState = 1
	DeleteSnapshotStoredScalars DeleteSnapshotState = 2
)

func NewFactRequirement(action FactAction, beforeIdentity, afterIdentity, privateDeleteSnapshot []policyir.FieldID) (FactRequirement, error) {
	value := FactRequirement{enabled: true, action: action}
	var err error
	if value.beforeIdentity, err = validateOrderedFields(beforeIdentity); err != nil {
		return FactRequirement{}, err
	}
	if value.afterIdentity, err = validateOrderedFields(afterIdentity); err != nil {
		return FactRequirement{}, err
	}
	if value.privateDeleteSnapshot, err = normalizeFields(privateDeleteSnapshot); err != nil {
		return FactRequirement{}, err
	}
	if action < FactCreated || action > FactDeleted {
		return FactRequirement{}, fmt.Errorf("P4_MUTATION_IR_FACT: invalid action")
	}
	if action == FactCreated && len(value.afterIdentity) == 0 || action == FactDeleted && len(value.beforeIdentity) == 0 || action == FactUpdated && (len(value.beforeIdentity) == 0 || len(value.afterIdentity) == 0) {
		return FactRequirement{}, fmt.Errorf("P4_MUTATION_IR_FACT: required identity image is absent")
	}
	if action != FactDeleted && len(value.privateDeleteSnapshot) != 0 {
		return FactRequirement{}, fmt.Errorf("P4_MUTATION_IR_FACT: private delete snapshot is valid only for delete")
	}
	if action == FactDeleted {
		value.deleteSnapshotState = DeleteSnapshotUnverifiable
		if privateDeleteSnapshot != nil {
			value.deleteSnapshotState = DeleteSnapshotStoredScalars
		}
	}
	return value, nil
}

// NewDeleteFactRequirement makes the delete verification decision explicit.
// The inventory must contain only compiler-selected stored scalar fields; the
// registry-backed planner validates that invariant before SQL is rendered.
func NewDeleteFactRequirement(beforeIdentity []policyir.FieldID, state DeleteSnapshotState, storedScalars []policyir.FieldID) (FactRequirement, error) {
	if state != DeleteSnapshotUnverifiable && state != DeleteSnapshotStoredScalars {
		return FactRequirement{}, fmt.Errorf("P7_MUTATION_IR_DELETE_SNAPSHOT: invalid verification state")
	}
	if state == DeleteSnapshotUnverifiable && len(storedScalars) != 0 {
		return FactRequirement{}, fmt.Errorf("P7_MUTATION_IR_DELETE_SNAPSHOT: unverifiable snapshot cannot name fields")
	}
	input := storedScalars
	if state == DeleteSnapshotUnverifiable {
		input = nil
	} else if input == nil {
		input = []policyir.FieldID{}
	}
	value, err := NewFactRequirement(FactDeleted, beforeIdentity, nil, input)
	if err != nil {
		return FactRequirement{}, err
	}
	value.deleteSnapshotState = state
	return value, nil
}

func NoFact() FactRequirement                     { return FactRequirement{} }
func (requirement FactRequirement) Enabled() bool { return requirement.enabled }
func (requirement FactRequirement) Action() (FactAction, bool) {
	return requirement.action, requirement.enabled
}
func (requirement FactRequirement) BeforeIdentity() []policyir.FieldID {
	return append([]policyir.FieldID(nil), requirement.beforeIdentity...)
}
func (requirement FactRequirement) AfterIdentity() []policyir.FieldID {
	return append([]policyir.FieldID(nil), requirement.afterIdentity...)
}
func (requirement FactRequirement) PrivateDeleteSnapshot() []policyir.FieldID {
	return append([]policyir.FieldID(nil), requirement.privateDeleteSnapshot...)
}
func (requirement FactRequirement) DeleteSnapshotState() DeleteSnapshotState {
	return requirement.deleteSnapshotState
}
func (requirement FactRequirement) EventSchema() ([32]byte, bool) {
	return requirement.eventSchema, requirement.eventSchema != ([32]byte{})
}

type ProviderRequirement struct {
	providers  policyir.ProviderSet
	capability ProviderCapability
}

func NewProviderRequirement(providers policyir.ProviderSet, capability ProviderCapability) (ProviderRequirement, error) {
	if !providers.Valid() || capability < CapabilityTransaction || capability > CapabilityAtomicNumericUpdate {
		return ProviderRequirement{}, fmt.Errorf("P4_MUTATION_IR_PROVIDER: invalid provider set or capability")
	}
	return ProviderRequirement{providers: providers, capability: capability}, nil
}

func (requirement ProviderRequirement) Providers() policyir.ProviderSet { return requirement.providers }
func (requirement ProviderRequirement) Capability() ProviderCapability  { return requirement.capability }

func normalizeProviderRequirements(input []ProviderRequirement) ([]ProviderRequirement, error) {
	result := append([]ProviderRequirement(nil), input...)
	for _, item := range result {
		if !item.providers.Valid() || item.capability < CapabilityTransaction || item.capability > CapabilityAtomicNumericUpdate {
			return nil, fmt.Errorf("P4_MUTATION_IR_PROVIDER: invalid requirement")
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].providers != result[j].providers {
			return result[i].providers < result[j].providers
		}
		return result[i].capability < result[j].capability
	})
	for index := 1; index < len(result); index++ {
		if result[index-1] == result[index] {
			return nil, fmt.Errorf("P4_MUTATION_IR_PROVIDER: duplicate requirement")
		}
	}
	return result, nil
}

func normalizeFields(input []policyir.FieldID) ([]policyir.FieldID, error) {
	result := append([]policyir.FieldID(nil), input...)
	sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i][:], result[j][:]) < 0 })
	for index, field := range result {
		if field == (policyir.FieldID{}) {
			return nil, fmt.Errorf("P4_MUTATION_IR_FIELD_SET: zero field identity")
		}
		if index > 0 && result[index-1] == field {
			return nil, fmt.Errorf("P4_MUTATION_IR_FIELD_SET: duplicate field identity")
		}
	}
	return result, nil
}

func validateOrderedFields(input []policyir.FieldID) ([]policyir.FieldID, error) {
	result := append([]policyir.FieldID(nil), input...)
	seen := make(map[policyir.FieldID]struct{}, len(result))
	for _, field := range result {
		if field == (policyir.FieldID{}) {
			return nil, fmt.Errorf("P4_MUTATION_IR_FIELD_LIST: zero field identity")
		}
		if _, duplicate := seen[field]; duplicate {
			return nil, fmt.Errorf("P4_MUTATION_IR_FIELD_LIST: duplicate field identity")
		}
		seen[field] = struct{}{}
	}
	return result, nil
}

func cloneDependencies(input []Dependency) []Dependency {
	result := make([]Dependency, len(input))
	for index, dependency := range input {
		result[index] = dependency
		result[index].path = append([]RelationHop(nil), dependency.path...)
	}
	return result
}

func compareDependency(left, right Dependency) int {
	if value := bytes.Compare(left.root[:], right.root[:]); value != 0 {
		return value
	}
	for index := 0; index < len(left.path) && index < len(right.path); index++ {
		if value := bytes.Compare(left.path[index].model[:], right.path[index].model[:]); value != 0 {
			return value
		}
		if value := bytes.Compare(left.path[index].field[:], right.path[index].field[:]); value != 0 {
			return value
		}
		if value := bytes.Compare(left.path[index].relation[:], right.path[index].relation[:]); value != 0 {
			return value
		}
		if value := bytes.Compare(left.path[index].target[:], right.path[index].target[:]); value != 0 {
			return value
		}
	}
	if len(left.path) < len(right.path) {
		return -1
	}
	if len(left.path) > len(right.path) {
		return 1
	}
	return bytes.Compare(left.field[:], right.field[:])
}
