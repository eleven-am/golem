package bind

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type InputKind uint8

const (
	InputCreate InputKind = iota + 1
	InputUpdate
	InputUpdateMany
)

// ScalarInput is the immutable root-scalar result of binding. Operations are
// sorted by stable field identity and copied at every collection boundary.
type ScalarInput struct {
	model      policyir.ModelID
	kind       InputKind
	operations []mutationir.ScalarOperation
}

type RuntimeOwnedValue struct {
	Field policyir.FieldID
	Value policyir.Value
}

func (input ScalarInput) ModelID() policyir.ModelID { return input.model }
func (input ScalarInput) Kind() InputKind           { return input.kind }
func (input ScalarInput) Operations() []mutationir.ScalarOperation {
	return append([]mutationir.ScalarOperation(nil), input.operations...)
}

// WithRuntimeOwnedValues appends schema-authorized application defaults and
// updated-field values without turning them into caller-authored fields.
func WithRuntimeOwnedValues(input ScalarInput, registry *schema.Registry, values []RuntimeOwnedValue) (ScalarInput, error) {
	if registry == nil || input.model == (policyir.ModelID{}) || input.kind < InputCreate || input.kind > InputUpdateMany {
		return ScalarInput{}, fail(CodeInput, golem.ModelID(input.model), golem.FieldID{}, "runtime-owned value input is invalid", nil)
	}
	operations := input.Operations()
	seen := make(map[policyir.FieldID]struct{}, len(operations)+len(values))
	for _, operation := range operations {
		seen[operation.FieldID()] = struct{}{}
	}
	for _, runtimeValue := range values {
		fieldID := golem.FieldID(runtimeValue.Field)
		if _, duplicate := seen[runtimeValue.Field]; duplicate {
			return ScalarInput{}, fail(CodeOperation, golem.ModelID(input.model), fieldID, "runtime-owned value duplicates an authored or resolved field", nil)
		}
		field, ok := registry.Field(golem.ModelID(input.model), fieldID)
		if !ok || field.Kind() == compilerir.FieldRelation {
			return ScalarInput{}, fail(CodeField, golem.ModelID(input.model), fieldID, "runtime-owned value field is absent or relational", nil)
		}
		allowed := field.Updated()
		if input.kind == InputCreate {
			if defaultValue, present := field.Default(); present && defaultValue.Producer == compilerir.ProducerApplication {
				allowed = true
			}
		}
		if !allowed {
			return ScalarInput{}, fail(CodeExposure, golem.ModelID(input.model), fieldID, "field is not application-runtime owned for this mutation", nil)
		}
		typ, err := bindType(field.LogicalType(), field.Nullable())
		if err != nil {
			return ScalarInput{}, fail(CodeInternal, golem.ModelID(input.model), fieldID, "runtime-owned field has an invalid type", err)
		}
		operation, err := mutationir.NewRuntimeSet(runtimeValue.Field, typ, runtimeValue.Value)
		if err != nil {
			return ScalarInput{}, fail(CodeValue, golem.ModelID(input.model), fieldID, "runtime-owned value does not fit the field type", err)
		}
		seen[runtimeValue.Field] = struct{}{}
		operations = append(operations, operation)
	}
	sort.Slice(operations, func(i, j int) bool {
		left, right := operations[i].FieldID(), operations[j].FieldID()
		return string(left[:]) < string(right[:])
	})
	return ScalarInput{model: input.model, kind: input.kind, operations: operations}, nil
}

// WithOptimisticConcurrencyCreate installs the sole runtime-owned initial
// token for a concurrency-enabled model. Ordinary runtime defaults are not an
// authority for this field: its schema explicitly forbids a default/updated
// producer, and the fixed value 1 belongs only to this closed boundary.
func WithOptimisticConcurrencyCreate(input ScalarInput, registry *schema.Registry) (ScalarInput, error) {
	if registry == nil || input.model == (policyir.ModelID{}) || input.kind != InputCreate {
		return ScalarInput{}, fail(CodeInput, golem.ModelID(input.model), golem.FieldID{}, "optimistic-concurrency create input is invalid", nil)
	}
	model, ok := registry.Model(golem.ModelID(input.model))
	if !ok {
		return ScalarInput{}, fail(CodeModel, golem.ModelID(input.model), golem.FieldID{}, "optimistic-concurrency model is absent", nil)
	}
	fieldID, enabled := model.OptimisticConcurrency()
	if !enabled {
		return input, nil
	}
	field, ok := registry.Field(model.ID(), fieldID)
	if !ok || field.Kind() == compilerir.FieldRelation || field.LogicalType().Kind != compilerir.TypeInt64 || field.Nullable() || field.Updated() || field.DatabaseReadOnly() {
		return ScalarInput{}, fail(CodeInternal, model.ID(), fieldID, "optimistic-concurrency field is not the exact stored int64 owner", nil)
	}
	if _, present := field.Default(); present {
		return ScalarInput{}, fail(CodeInternal, model.ID(), fieldID, "optimistic-concurrency field has a competing default owner", nil)
	}
	if _, present := field.Generation(); present {
		return ScalarInput{}, fail(CodeInternal, model.ID(), fieldID, "optimistic-concurrency field has a competing generated owner", nil)
	}
	operations := input.Operations()
	for _, operation := range operations {
		if operation.FieldID() == policyir.FieldID(fieldID) {
			return ScalarInput{}, fail(CodeExposure, model.ID(), fieldID, "optimistic-concurrency field cannot be application authored", nil)
		}
	}
	typ, err := bindType(field.LogicalType(), false)
	if err != nil || typ.Kind() != policyir.ValueInt64 {
		return ScalarInput{}, fail(CodeInternal, model.ID(), fieldID, "optimistic-concurrency field has an invalid runtime type", err)
	}
	one, err := policyir.SignedValue(policyir.ValueInt64, 1)
	if err != nil {
		return ScalarInput{}, fail(CodeInternal, model.ID(), fieldID, "optimistic-concurrency initial value is invalid", err)
	}
	operation, err := mutationir.NewRuntimeSet(policyir.FieldID(fieldID), typ, one)
	if err != nil {
		return ScalarInput{}, fail(CodeInternal, model.ID(), fieldID, "optimistic-concurrency initial operation is invalid", err)
	}
	operations = append(operations, operation)
	sort.Slice(operations, func(i, j int) bool {
		left, right := operations[i].FieldID(), operations[j].FieldID()
		return string(left[:]) < string(right[:])
	})
	return ScalarInput{model: input.model, kind: input.kind, operations: operations}, nil
}

// WithRuntimeOwnedOperations is the model-erased nested-planner boundary. The
// supplied operations must already have passed ordinary public binding; this
// function only reconstructs that immutable input long enough to apply the
// same runtime-ownership validation used by root mutations.
func WithRuntimeOwnedOperations(model policyir.ModelID, kind InputKind, operations []mutationir.ScalarOperation, registry *schema.Registry, values []RuntimeOwnedValue) ([]mutationir.ScalarOperation, error) {
	if registry == nil {
		return nil, fail(CodeInput, golem.ModelID(model), golem.FieldID{}, "active schema registry is required", nil)
	}
	modelFact, ok := registry.Model(golem.ModelID(model))
	if !ok {
		return nil, fail(CodeModel, golem.ModelID(model), golem.FieldID{}, "model-erased mutation model is absent", nil)
	}
	concurrencyField, concurrencyEnabled := modelFact.OptimisticConcurrency()
	for _, operation := range operations {
		if err := operation.Validate(); err != nil {
			return nil, fail(CodeOperation, golem.ModelID(model), golem.FieldID(operation.FieldID()), "nested scalar operation is invalid", err)
		}
		if operation.RuntimeOwned() {
			return nil, fail(CodeOperation, golem.ModelID(model), golem.FieldID(operation.FieldID()), "nested scalar operation was resolved more than once", nil)
		}
		if concurrencyEnabled && operation.FieldID() == policyir.FieldID(concurrencyField) {
			return nil, fail(CodeExposure, golem.ModelID(model), concurrencyField, "optimistic-concurrency field cannot cross a model-erased authored boundary", nil)
		}
	}
	resolved, err := WithRuntimeOwnedValues(ScalarInput{model: model, kind: kind, operations: append([]mutationir.ScalarOperation(nil), operations...)}, registry, values)
	if err != nil {
		return nil, err
	}
	if kind == InputCreate {
		resolved, err = WithOptimisticConcurrencyCreate(resolved, registry)
		if err != nil {
			return nil, err
		}
	}
	return resolved.Operations(), nil
}

func CreateInput(frozen golem.FrozenMutationInput, registry *schema.Registry) (ScalarInput, error) {
	input, _, err := bindInput(InputCreate, frozen, registry, nil, nil)
	return input, err
}

func CreateInputFromHook(frozen golem.FrozenMutationInput, registry *schema.Registry, runtimeFields []golem.FieldID, hookAuthored []golem.FieldID) (ScalarInput, []policyir.FieldID, error) {
	return bindInput(InputCreate, frozen, registry, runtimeFields, hookAuthored)
}

func UpdateInputFromHook(frozen golem.FrozenMutationInput, registry *schema.Registry, hookAuthored []golem.FieldID) (ScalarInput, error) {
	input, _, err := bindInput(InputUpdate, frozen, registry, nil, hookAuthored)
	return input, err
}

// CreateInputWithRuntimeOwnedFields is the narrow nested-create boundary for
// correlation columns whose values are derived from a persisted parent row.
// Root creates must use CreateInput. Every exemption is revalidated against the
// active schema and returned as a closed, sorted inventory for the planner.
func CreateInputWithRuntimeOwnedFields(frozen golem.FrozenMutationInput, registry *schema.Registry, fields []golem.FieldID) (ScalarInput, []policyir.FieldID, error) {
	return bindInput(InputCreate, frozen, registry, fields, nil)
}

func UpdateInput(frozen golem.FrozenMutationInput, registry *schema.Registry) (ScalarInput, error) {
	input, _, err := bindInput(InputUpdate, frozen, registry, nil, nil)
	return input, err
}

func UpdateManyInput(frozen golem.FrozenMutationInput, registry *schema.Registry) (ScalarInput, error) {
	input, _, err := bindInput(InputUpdateMany, frozen, registry, nil, nil)
	return input, err
}

func hookAuthoredSet(modelID golem.ModelID, registry *schema.Registry, fields []golem.FieldID) (map[golem.FieldID]struct{}, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	result := make(map[golem.FieldID]struct{}, len(fields))
	for _, fieldID := range fields {
		if fieldID == (golem.FieldID{}) {
			return nil, fail(CodeField, modelID, fieldID, "hook-authored field is zero", nil)
		}
		if _, duplicate := result[fieldID]; duplicate {
			return nil, fail(CodeField, modelID, fieldID, "hook-authored field is duplicate", nil)
		}
		field, present := registry.Field(modelID, fieldID)
		if !present || field.Kind() == compilerir.FieldRelation {
			return nil, fail(CodeField, modelID, fieldID, "hook-authored field is absent, foreign, or relational", nil)
		}
		if !compilerir.HasMode(field.Modes(), compilerir.ModeSystem) {
			return nil, fail(CodeExposure, modelID, fieldID, "hook authorship is reserved for a system field", nil)
		}
		result[fieldID] = struct{}{}
	}
	return result, nil
}

func bindInput(kind InputKind, frozen golem.FrozenMutationInput, registry *schema.Registry, runtimeFields []golem.FieldID, hookAuthoredFields []golem.FieldID) (ScalarInput, []policyir.FieldID, error) {
	modelID := frozen.ModelID()
	if registry == nil {
		return ScalarInput{}, nil, fail(CodeInput, modelID, golem.FieldID{}, "active schema registry is required", nil)
	}
	if kind < InputCreate || kind > InputUpdateMany {
		return ScalarInput{}, nil, fail(CodeInput, modelID, golem.FieldID{}, "unknown input kind", nil)
	}
	model, ok := registry.Model(modelID)
	if modelID == (golem.ModelID{}) || !ok {
		return ScalarInput{}, nil, fail(CodeModel, modelID, golem.FieldID{}, "model is absent from the active schema", nil)
	}
	if kind != InputCreate && len(runtimeFields) != 0 {
		return ScalarInput{}, nil, fail(CodeInput, modelID, golem.FieldID{}, "runtime-owned fields are valid only for create", nil)
	}
	hookAuthored, hookErr := hookAuthoredSet(modelID, registry, hookAuthoredFields)
	if hookErr != nil {
		return ScalarInput{}, nil, hookErr
	}
	concurrencyField, concurrencyEnabled := model.OptimisticConcurrency()
	runtimeOwned := make(map[golem.FieldID]struct{}, len(runtimeFields))
	runtimeInventory := make([]policyir.FieldID, 0, len(runtimeFields))
	for _, fieldID := range runtimeFields {
		if fieldID == (golem.FieldID{}) {
			return ScalarInput{}, nil, fail(CodeField, modelID, fieldID, "runtime-owned field is zero", nil)
		}
		if _, duplicate := runtimeOwned[fieldID]; duplicate {
			return ScalarInput{}, nil, fail(CodeField, modelID, fieldID, "runtime-owned field is duplicate", nil)
		}
		field, present := registry.Field(modelID, fieldID)
		if !present || field.Kind() == compilerir.FieldRelation {
			return ScalarInput{}, nil, fail(CodeField, modelID, fieldID, "runtime-owned field is absent, foreign, or relational", nil)
		}
		if !runtimeOwnableCreateField(field) {
			return ScalarInput{}, nil, fail(CodeRequired, modelID, fieldID, "runtime-owned field must be a writable scalar without default, generation, or database ownership", nil)
		}
		runtimeOwned[fieldID] = struct{}{}
		runtimeInventory = append(runtimeInventory, policyir.FieldID(fieldID))
	}
	sort.Slice(runtimeInventory, func(i, j int) bool { return string(runtimeInventory[i][:]) < string(runtimeInventory[j][:]) })

	publicFields := frozen.Fields()
	relations := frozen.Relations()
	if kind == InputUpdate && len(publicFields) == 0 && len(relations) == 0 {
		return ScalarInput{}, nil, fail(CodeInput, modelID, golem.FieldID{}, "update input has no operations", nil)
	}
	if kind == InputUpdateMany && (len(publicFields) == 0 || len(relations) != 0) {
		return ScalarInput{}, nil, fail(CodeInput, modelID, golem.FieldID{}, "update-many input requires scalar operations and cannot contain relations", nil)
	}
	seen := make(map[golem.FieldID]struct{}, len(publicFields))
	authored := make(map[golem.FieldID]struct{}, len(publicFields))
	operations := make([]mutationir.ScalarOperation, 0, len(publicFields))
	for index, public := range publicFields {
		fieldID := public.FieldID()
		if fieldID == (golem.FieldID{}) {
			return ScalarInput{}, nil, fail(CodeField, modelID, fieldID, fmt.Sprintf("field %d has a zero identity", index), nil)
		}
		if _, duplicate := seen[fieldID]; duplicate {
			return ScalarInput{}, nil, fail(CodeField, modelID, fieldID, "field has more than one mutation operation", nil)
		}
		seen[fieldID] = struct{}{}
		field, ok := registry.Field(modelID, fieldID)
		if !ok || field.Kind() == compilerir.FieldRelation {
			return ScalarInput{}, nil, fail(CodeField, modelID, fieldID, "field is absent, foreign, or not a persisted scalar", nil)
		}
		if _, owned := runtimeOwned[fieldID]; owned {
			return ScalarInput{}, nil, fail(CodeOperation, modelID, fieldID, "runtime-owned correlation field cannot also be explicitly authored", nil)
		}
		if concurrencyEnabled && fieldID == concurrencyField {
			return ScalarInput{}, nil, fail(CodeExposure, modelID, fieldID, "optimistic-concurrency field cannot be application authored", nil)
		}
		if reason := mutationExposure(field, kind); reason != "" {
			return ScalarInput{}, nil, fail(CodeExposure, modelID, fieldID, reason, nil)
		}
		if kind == InputUpdateMany && identityField(model, fieldID) {
			return ScalarInput{}, nil, fail(CodeOperation, modelID, fieldID, "batch mutation cannot change an identity component", nil)
		}
		typ, err := bindType(field.LogicalType(), field.Nullable())
		if err != nil {
			return ScalarInput{}, nil, fail(CodeInternal, modelID, fieldID, "active field has an invalid logical type", err)
		}
		operation, err := bindScalarOperation(kind, public, field, typ, registry)
		if err != nil {
			return ScalarInput{}, nil, err
		}
		if _, byHook := hookAuthored[fieldID]; byHook {
			delete(hookAuthored, fieldID)
			operation, err = mutationir.NewHookAuthored(operation)
			if err != nil {
				return ScalarInput{}, nil, fail(CodeInternal, modelID, fieldID, "hook-authored operation is invalid", err)
			}
		}
		operations = append(operations, operation)
		authored[fieldID] = struct{}{}
	}

	if kind == InputCreate {
		for _, fieldID := range model.Fields() {
			if concurrencyEnabled && fieldID == concurrencyField {
				continue
			}
			field, ok := registry.Field(modelID, fieldID)
			if !ok || field.Kind() == compilerir.FieldRelation || !createRequired(field) {
				continue
			}
			if reason := mutationExposure(field, InputCreate); reason != "" {
				return ScalarInput{}, nil, fail(CodeInternal, modelID, fieldID, "active schema has a non-authorable required create field: "+reason, nil)
			}
			if _, owned := runtimeOwned[fieldID]; owned {
				continue
			}
			if _, present := authored[fieldID]; !present {
				return ScalarInput{}, nil, fail(CodeRequired, modelID, fieldID, "required create field is absent", nil)
			}
		}
	}

	for fieldID := range hookAuthored {
		return ScalarInput{}, nil, fail(CodeField, modelID, fieldID, "hook-authored field has no operation in the transformed input", nil)
	}
	sort.Slice(operations, func(i, j int) bool {
		left, right := operations[i].FieldID(), operations[j].FieldID()
		return string(left[:]) < string(right[:])
	})
	return ScalarInput{model: policyir.ModelID(modelID), kind: kind, operations: operations}, runtimeInventory, nil
}

func bindScalarOperation(kind InputKind, public golem.FrozenMutationField, field schema.Field, typ policyir.TypeRef, registry *schema.Registry) (mutationir.ScalarOperation, error) {
	modelID, fieldID := field.ModelID(), public.FieldID()
	operation := public.Operation()
	raw, hasValue := public.Value()
	if kind == InputCreate {
		if operation == golem.MutationFieldNull {
			if hasValue || !field.Nullable() {
				return mutationir.ScalarOperation{}, fail(CodeOperation, modelID, fieldID, "create null requires a nullable field and no operand", nil)
			}
			bound, err := mutationir.NewNull(policyir.FieldID(fieldID), typ)
			if err != nil {
				return mutationir.ScalarOperation{}, fail(CodeInternal, modelID, fieldID, "create scalar IR rejected validated null", err)
			}
			return bound, nil
		}
		if operation != golem.MutationFieldCreate || !hasValue {
			return mutationir.ScalarOperation{}, fail(CodeOperation, modelID, fieldID, "create field has an invalid operation or operand shape", nil)
		}
		value, err := bindValue(raw, field.LogicalType(), typ, registry)
		if err != nil {
			return mutationir.ScalarOperation{}, fail(CodeValue, modelID, fieldID, "create value does not match the exact logical type", err)
		}
		bound, err := mutationir.NewSet(policyir.FieldID(fieldID), typ, value)
		if err != nil {
			return mutationir.ScalarOperation{}, fail(CodeInternal, modelID, fieldID, "create scalar IR rejected a validated value", err)
		}
		return bound, nil
	}

	var bound mutationir.ScalarOperation
	var err error
	switch operation {
	case golem.MutationFieldSet:
		if !hasValue {
			return bound, fail(CodeOperation, modelID, fieldID, "set requires one value", nil)
		}
		value, valueErr := bindValue(raw, field.LogicalType(), typ, registry)
		if valueErr != nil {
			return bound, fail(CodeValue, modelID, fieldID, "set value does not match the exact logical type", valueErr)
		}
		bound, err = mutationir.NewSet(policyir.FieldID(fieldID), typ, value)
	case golem.MutationFieldNull:
		if hasValue || !field.Nullable() {
			return bound, fail(CodeOperation, modelID, fieldID, "null requires a nullable field and no operand", nil)
		}
		bound, err = mutationir.NewNull(policyir.FieldID(fieldID), typ)
	case golem.MutationFieldIncrement, golem.MutationFieldDecrement:
		if !hasValue || !numeric(field.LogicalType().Kind) {
			return bound, fail(CodeOperation, modelID, fieldID, "arithmetic requires a numeric field and one operand", nil)
		}
		value, valueErr := bindValue(raw, field.LogicalType(), typ, registry)
		if valueErr != nil {
			return bound, fail(CodeValue, modelID, fieldID, "arithmetic operand does not match the exact logical type", valueErr)
		}
		if operation == golem.MutationFieldIncrement {
			bound, err = mutationir.NewIncrement(policyir.FieldID(fieldID), typ, value)
		} else {
			bound, err = mutationir.NewDecrement(policyir.FieldID(fieldID), typ, value)
		}
	default:
		return bound, fail(CodeOperation, modelID, fieldID, "operation is not valid for an update input", nil)
	}
	if err != nil {
		return mutationir.ScalarOperation{}, fail(CodeInternal, modelID, fieldID, "scalar IR rejected a validated operation", err)
	}
	return bound, nil
}

func mutationExposure(field schema.Field, kind InputKind) string {
	for _, mode := range field.Modes() {
		switch mode {
		case compilerir.ModeHidden:
			return "hidden field is not writable"
		case compilerir.ModeReadOnly:
			return "read-only field is not writable"
		case compilerir.ModeImmutable:
			if kind != InputCreate {
				return "immutable field is writable only during create"
			}
		}
	}
	if field.DatabaseReadOnly() {
		return "database-read-only field is not caller writable"
	}
	if _, generated := field.Generation(); generated {
		return "generated field is not caller writable"
	}
	if field.Updated() {
		return "updated field is runtime owned"
	}
	return ""
}

func createRequired(field schema.Field) bool {
	if field.Nullable() || field.Updated() || field.DatabaseReadOnly() {
		return false
	}
	if _, present := field.Default(); present {
		return false
	}
	if _, present := field.Generation(); present {
		return false
	}
	return true
}

// Runtime-owned nested correlation values are always supplied by the engine
// from the persisted relation anchor. Nullable is therefore legal here: an
// optional self relation still receives a non-null value for a nested child,
// while an omitted root value remains null. Defaults, generated values, and
// database-owned values remain forbidden because they would create competing
// authorities for the same correlation column.
func runtimeOwnableCreateField(field schema.Field) bool {
	if field.Updated() || field.DatabaseReadOnly() {
		return false
	}
	if _, present := field.Default(); present {
		return false
	}
	if _, present := field.Generation(); present {
		return false
	}
	return true
}

func identityField(model schema.Model, field golem.FieldID) bool {
	for _, identity := range model.Identities() {
		for _, component := range identity.Fields() {
			if component == field {
				return true
			}
		}
	}
	return false
}

func numeric(kind compilerir.LogicalTypeKind) bool {
	switch kind {
	case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64,
		compilerir.TypeFloat32, compilerir.TypeFloat64, compilerir.TypeDecimal:
		return true
	default:
		return false
	}
}
