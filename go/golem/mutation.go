package golem

import (
	"fmt"
	"reflect"
)

// MutationFieldOperation is the closed portable scalar mutation vocabulary.
// Relation operations use their own generated values and are expanded by the
// P4 binder rather than smuggled through this scalar representation.
type MutationFieldOperation uint8

const (
	MutationFieldCreate MutationFieldOperation = iota + 1
	MutationFieldSet
	MutationFieldNull
	MutationFieldIncrement
	MutationFieldDecrement
)

type mutationValueNode struct {
	model     ModelID
	field     FieldID
	operation MutationFieldOperation
	value     any
	hasValue  bool
	relation  *nestedMutationNode
}

func (node mutationValueNode) clone() mutationValueNode {
	node.value = cloneMutationValue(node.value)
	if node.relation != nil {
		cloned := node.relation.clone()
		node.relation = &cloned
	}
	return node
}

// CreateValue, UpdateValue, and UpdateManyValue are sealed, model-owned input
// capabilities. Generated packages obtain implementations only from the
// Generated* constructors below. UpdateManyValue is deliberately a subset of
// UpdateValue so one generated scalar Set/Increment/Decrement method works in
// both update shapes while nested values remain single-row only.
type CreateValue[M any] interface {
	createMutationValue(M) mutationValueNode
}

type UpdateValue[M any] interface {
	updateMutationValue(M) mutationValueNode
}

type UpdateManyValue[M any] interface {
	UpdateValue[M]
	updateManyMutationValue(M) mutationValueNode
}

// NestedCreateValue and NestedUpdateValue reserve the narrower relation-owned
// capabilities used by generated relation handles. P4's nested binder will
// add their concrete values; scalar values cannot satisfy these interfaces.
type NestedCreateValue[M any] interface {
	CreateValue[M]
	nestedCreateMutationValue(M) mutationValueNode
}

type NestedUpdateValue[M any] interface {
	UpdateValue[M]
	nestedUpdateMutationValue(M) mutationValueNode
}

// CreateFieldCapability is the sealed, generated proof that a scalar field is
// writable during create. It is deliberately narrower than ScalarColumn: a
// write-only field can satisfy this capability without becoming selectable,
// filterable, orderable, or usable in schema expressions.
type CreateFieldCapability[M, V any] interface {
	generatedCreateFieldCapability(M, V) (ModelID, ScalarColumn[M, V])
}

type createFieldCapability[M, V any] struct {
	model  ModelID
	column ScalarColumn[M, V]
}

func (field createFieldCapability[M, V]) generatedCreateFieldCapability(M, V) (ModelID, ScalarColumn[M, V]) {
	return field.model, field.column
}

// GeneratedCreateFieldCapability is used only by generated field wrappers.
// The runtime still validates both identities against the active schema.
func GeneratedCreateFieldCapability[M, V any](model ModelID, column ScalarColumn[M, V]) CreateFieldCapability[M, V] {
	return createFieldCapability[M, V]{model: model, column: column}
}

type createValue[M any] struct {
	node mutationValueNode
	_    func() M
}

func (value createValue[M]) createMutationValue(M) mutationValueNode {
	return value.node.clone()
}

type scalarUpdateValue[M any] struct {
	node mutationValueNode
	_    func() M
}

func (value scalarUpdateValue[M]) updateMutationValue(M) mutationValueNode {
	return value.node.clone()
}

func (value scalarUpdateValue[M]) updateManyMutationValue(M) mutationValueNode {
	return value.node.clone()
}

// GeneratedCreateFieldValue constructs one typed create assignment. Generated
// field methods fix M and V to the declared model and logical Go type.
func GeneratedCreateFieldValue[M, V any](model ModelID, field ScalarColumn[M, V], value V) CreateValue[M] {
	return createValue[M]{node: mutationValueNode{
		model: model, field: scalarFieldIdentity(field), operation: MutationFieldCreate,
		value: cloneMutationValue(value), hasValue: true,
	}}
}

// GeneratedCreateNullFieldValue preserves GraphQL's explicit-null versus
// omitted distinction for nullable create fields, including fields that have a
// database default. Generated packages expose it only on nullable writable
// fields; the P4 binder revalidates that proof against the active schema.
func GeneratedCreateNullFieldValue[M, V any](model ModelID, field ScalarColumn[M, V]) CreateValue[M] {
	return createValue[M]{node: mutationValueNode{
		model: model, field: scalarFieldIdentity(field), operation: MutationFieldNull,
	}}
}

// GeneratedSetFieldValue constructs one typed scalar assignment usable by
// both Update and UpdateMany inputs.
func GeneratedSetFieldValue[M, V any](model ModelID, field ScalarColumn[M, V], value V) UpdateManyValue[M] {
	return scalarUpdateValue[M]{node: mutationValueNode{
		model: model, field: scalarFieldIdentity(field), operation: MutationFieldSet,
		value: cloneMutationValue(value), hasValue: true,
	}}
}

// GeneratedNullFieldValue is emitted only for nullable writable fields.
func GeneratedNullFieldValue[M, V any](model ModelID, field ScalarColumn[M, V]) UpdateManyValue[M] {
	return scalarUpdateValue[M]{node: mutationValueNode{
		model: model, field: scalarFieldIdentity(field), operation: MutationFieldNull,
	}}
}

// GeneratedIncrementFieldValue and GeneratedDecrementFieldValue are emitted
// only for writable numeric fields. Their arithmetic remains a database
// operation; this value merely preserves the exact typed operand.
func GeneratedIncrementFieldValue[M, V any](model ModelID, field ScalarColumn[M, V], value V) UpdateManyValue[M] {
	return scalarUpdateValue[M]{node: mutationValueNode{
		model: model, field: scalarFieldIdentity(field), operation: MutationFieldIncrement,
		value: cloneMutationValue(value), hasValue: true,
	}}
}

func GeneratedDecrementFieldValue[M, V any](model ModelID, field ScalarColumn[M, V], value V) UpdateManyValue[M] {
	return scalarUpdateValue[M]{node: mutationValueNode{
		model: model, field: scalarFieldIdentity(field), operation: MutationFieldDecrement,
		value: cloneMutationValue(value), hasValue: true,
	}}
}

func scalarFieldIdentity[M, V any](field ScalarColumn[M, V]) FieldID {
	if field == nil {
		return FieldID{}
	}
	return field.fieldIdentity()
}

// CreateInput, UpdateInput, and UpdateManyInput are immutable opaque input
// values. Their zero values are intentionally invalid at the runtime freeze
// boundary.
type CreateInput[M any] struct {
	model  ModelID
	values []mutationValueNode
	_      func() M
}

type UpdateInput[M any] struct {
	model  ModelID
	values []mutationValueNode
	_      func() M
}

type UpdateManyInput[M any] struct {
	model  ModelID
	values []mutationValueNode
	_      func() M
}

func cloneCreateInput[M any](input CreateInput[M]) CreateInput[M] {
	return CreateInput[M]{model: input.model, values: cloneMutationNodes(input.values)}
}

func cloneUpdateInput[M any](input UpdateInput[M]) UpdateInput[M] {
	return UpdateInput[M]{model: input.model, values: cloneMutationNodes(input.values)}
}

func cloneUpdateManyInput[M any](input UpdateManyInput[M]) UpdateManyInput[M] {
	return UpdateManyInput[M]{model: input.model, values: cloneMutationNodes(input.values)}
}

func cloneMutationNodes(values []mutationValueNode) []mutationValueNode {
	result := make([]mutationValueNode, len(values))
	for index, value := range values {
		result[index] = value.clone()
	}
	return result
}

func GeneratedCreateInput[M any](model ModelID, values ...CreateValue[M]) CreateInput[M] {
	var witness M
	return CreateInput[M]{model: model, values: collectCreateValues(witness, values)}
}

func GeneratedUpdateInput[M any](model ModelID, values ...UpdateValue[M]) UpdateInput[M] {
	var witness M
	return UpdateInput[M]{model: model, values: collectUpdateValues(witness, values)}
}

func GeneratedUpdateManyInput[M any](model ModelID, values ...UpdateManyValue[M]) UpdateManyInput[M] {
	var witness M
	return UpdateManyInput[M]{model: model, values: collectUpdateManyValues(witness, values)}
}

func collectCreateValues[M any](witness M, values []CreateValue[M]) []mutationValueNode {
	result := make([]mutationValueNode, len(values))
	for index, value := range values {
		if value != nil {
			result[index] = value.createMutationValue(witness)
		}
	}
	return result
}

func collectUpdateValues[M any](witness M, values []UpdateValue[M]) []mutationValueNode {
	result := make([]mutationValueNode, len(values))
	for index, value := range values {
		if value != nil {
			result[index] = value.updateMutationValue(witness)
		}
	}
	return result
}

func collectUpdateManyValues[M any](witness M, values []UpdateManyValue[M]) []mutationValueNode {
	result := make([]mutationValueNode, len(values))
	for index, value := range values {
		if value != nil {
			result[index] = value.updateManyMutationValue(witness)
		}
	}
	return result
}

// FrozenMutationField is a detached runtime view. Value returns a fresh copy
// of mutable bytes, lists, JSON, maps, and pointers on every call.
type FrozenMutationField struct {
	field     FieldID
	operation MutationFieldOperation
	value     any
	hasValue  bool
}

func (field FrozenMutationField) FieldID() FieldID                  { return field.field }
func (field FrozenMutationField) Operation() MutationFieldOperation { return field.operation }
func (field FrozenMutationField) Value() (any, bool) {
	if !field.hasValue {
		return nil, false
	}
	return cloneMutationValue(field.value), true
}

// FrozenMutationInput is the schema-agnostic handoff to P4 binding. The binder
// revalidates model, field, exposure, logical type, requiredness, and operation
// legality against the active fingerprinted registry.
type FrozenMutationInput struct {
	model     ModelID
	fields    []FrozenMutationField
	relations []FrozenNestedMutation
}

// RuntimeMutationFieldValue is the model-erased scalar input used only when
// the nested runtime must expose database-derived membership assignments to a
// generated Update Before hook. The ordinary public API remains generated and
// typed; the binder revalidates every field and value against the schema.
type RuntimeMutationFieldValue struct {
	Field     FieldID
	Operation MutationFieldOperation
	Value     any
	HasValue  bool
}

func RuntimeMutationInputFromFields(model ModelID, fields []RuntimeMutationFieldValue) (FrozenMutationInput, error) {
	if model == (ModelID{}) {
		return FrozenMutationInput{}, invalidMutation("runtime update", model, FieldID{}, "model identity is absent")
	}
	result := FrozenMutationInput{model: model, fields: make([]FrozenMutationField, len(fields))}
	seen := make(map[FieldID]struct{}, len(fields))
	for index, field := range fields {
		if field.Field == (FieldID{}) || field.Operation < MutationFieldSet || field.Operation > MutationFieldDecrement {
			return FrozenMutationInput{}, invalidMutation("runtime update", model, field.Field, "field operation is invalid")
		}
		if _, duplicate := seen[field.Field]; duplicate {
			return FrozenMutationInput{}, invalidMutation("runtime update", model, field.Field, "field operation is duplicated")
		}
		needsValue := field.Operation != MutationFieldNull
		if field.HasValue != needsValue {
			return FrozenMutationInput{}, invalidMutation("runtime update", model, field.Field, "field operand shape is invalid")
		}
		seen[field.Field] = struct{}{}
		result.fields[index] = FrozenMutationField{field: field.Field, operation: field.Operation, value: cloneMutationValue(field.Value), hasValue: field.HasValue}
	}
	return result, nil
}

func (input FrozenMutationInput) ModelID() ModelID { return input.model }
func (input FrozenMutationInput) Fields() []FrozenMutationField {
	result := make([]FrozenMutationField, len(input.fields))
	for index, field := range input.fields {
		result[index] = FrozenMutationField{
			field: field.field, operation: field.operation,
			value: cloneMutationValue(field.value), hasValue: field.hasValue,
		}
	}
	return result
}

func RuntimeFreezeCreateInput[M any](input CreateInput[M]) (FrozenMutationInput, error) {
	return freezeMutationInput("create", input.model, input.values, MutationFieldCreate, MutationFieldCreate)
}

func RuntimeFreezeUpdateInput[M any](input UpdateInput[M]) (FrozenMutationInput, error) {
	return freezeMutationInput("update", input.model, input.values, MutationFieldSet, MutationFieldDecrement)
}

func RuntimeFreezeUpdateManyInput[M any](input UpdateManyInput[M]) (FrozenMutationInput, error) {
	return freezeMutationInput("updateMany", input.model, input.values, MutationFieldSet, MutationFieldDecrement)
}

func freezeMutationInput(operation string, model ModelID, values []mutationValueNode, minimum, maximum MutationFieldOperation) (FrozenMutationInput, error) {
	if model == (ModelID{}) {
		return FrozenMutationInput{}, invalidMutation(operation, model, FieldID{}, "mutation input has no generated model identity")
	}
	result := FrozenMutationInput{
		model:     model,
		fields:    make([]FrozenMutationField, 0, len(values)),
		relations: make([]FrozenNestedMutation, 0, len(values)),
	}
	seen := make(map[FieldID]struct{}, len(values))
	for index, value := range values {
		if value.model == (ModelID{}) || value.field == (FieldID{}) || value.model != model {
			return FrozenMutationInput{}, invalidMutation(operation, model, value.field, fmt.Sprintf("mutation value %d has incomplete or foreign generated identity", index))
		}
		if value.relation != nil {
			frozen, err := freezeNestedMutation(operation, model, value.field, *value.relation)
			if err != nil {
				return FrozenMutationInput{}, err
			}
			result.relations = append(result.relations, frozen)
			continue
		}
		validOperation := value.operation >= minimum && value.operation <= maximum
		if operation == "create" {
			validOperation = value.operation == MutationFieldCreate || value.operation == MutationFieldNull
		}
		if !validOperation {
			return FrozenMutationInput{}, invalidMutation(operation, model, value.field, fmt.Sprintf("mutation value %d has an invalid operation", index))
		}
		if _, duplicate := seen[value.field]; duplicate {
			return FrozenMutationInput{}, invalidMutation(operation, model, value.field, "field has more than one mutation operation")
		}
		seen[value.field] = struct{}{}
		needsValue := value.operation != MutationFieldNull
		if value.hasValue != needsValue {
			return FrozenMutationInput{}, invalidMutation(operation, model, value.field, "mutation operation has an invalid operand shape")
		}
		result.fields = append(result.fields, FrozenMutationField{
			field: value.field, operation: value.operation,
			value: cloneMutationValue(value.value), hasValue: value.hasValue,
		})
	}
	return result, nil
}

// MutationTarget is sealed to generated unique selector values and their
// optional typed guard. A guard can only narrow its selector.
type MutationTarget[M any] interface {
	mutationTarget(M) mutationTargetNode[M]
}

type mutationTargetNode[M any] struct {
	selector UniqueSelectorValue[M]
	guard    *Predicate[M]
}

func (selector UniqueSelectorValue[M]) mutationTarget(M) mutationTargetNode[M] {
	return mutationTargetNode[M]{selector: cloneUniqueSelectorValue(selector)}
}

type guardedMutationTarget[M any] struct {
	node mutationTargetNode[M]
	_    func() M
}

func cloneMutationTargetNode[M any](node mutationTargetNode[M]) mutationTargetNode[M] {
	result := mutationTargetNode[M]{selector: cloneUniqueSelectorValue(node.selector)}
	if node.guard != nil {
		guard := *node.guard
		result.guard = &guard
	}
	return result
}

func mutationTargetFromNode[M any](node mutationTargetNode[M]) MutationTarget[M] {
	cloned := cloneMutationTargetNode(node)
	if cloned.guard == nil {
		return cloned.selector
	}
	return guardedMutationTarget[M]{node: cloned}
}

func (target guardedMutationTarget[M]) mutationTarget(M) mutationTargetNode[M] {
	result := mutationTargetNode[M]{selector: cloneUniqueSelectorValue(target.node.selector)}
	if target.node.guard != nil {
		guard := *target.node.guard
		result.guard = &guard
	}
	return result
}

func (selector UniqueSelectorValue[M]) And(guard Predicate[M]) MutationTarget[M] {
	cloned := guard
	return guardedMutationTarget[M]{node: mutationTargetNode[M]{
		selector: cloneUniqueSelectorValue(selector), guard: &cloned,
	}}
}

// FrozenMutationTarget preserves the selector metadata, exact selector
// predicate, and optional ordinary guard as distinct immutable values.
type FrozenMutationTarget struct {
	selector          FrozenUniqueSelector
	selectorPredicate FrozenPredicate
	guard             *FrozenPredicate
}

type RuntimeSelectorValue struct {
	Field FieldID
	Value any
}

// RuntimeMutationTargetFromIdentity reconstructs a generated-equivalent
// unique selector from an exact decoded database identity. It is used only
// after the nested engine has selected/locked that row.
func RuntimeMutationTargetFromIdentity(model ModelID, key KeyID, values []RuntimeSelectorValue) (FrozenMutationTarget, error) {
	components := make([]selectorComponent, len(values))
	for index, value := range values {
		components[index] = GeneratedSelectorComponent(value.Field, value.Value)
	}
	selector, predicate, err := freezeUniqueSelector[struct{}](ReadFindUnique, model, model, key, components)
	if err != nil {
		return FrozenMutationTarget{}, err
	}
	frozen, err := predicate.freezeForModel(model)
	if err != nil {
		return FrozenMutationTarget{}, err
	}
	return FrozenMutationTarget{selector: selector, selectorPredicate: frozen}, nil
}

func (target FrozenMutationTarget) Selector() FrozenUniqueSelector {
	result := target.selector
	result.fields = target.selector.Fields()
	return result
}
func (target FrozenMutationTarget) SelectorPredicate() FrozenPredicate {
	return cloneFrozenPredicate(target.selectorPredicate)
}
func (target FrozenMutationTarget) Guard() (FrozenPredicate, bool) {
	if target.guard == nil {
		return FrozenPredicate{}, false
	}
	return cloneFrozenPredicate(*target.guard), true
}

func RuntimeFreezeMutationTarget[M any](target MutationTarget[M]) (FrozenMutationTarget, error) {
	if target == nil {
		return FrozenMutationTarget{}, invalidMutation("mutation", ModelID{}, FieldID{}, "mutation target is nil")
	}
	if frozen, ok := target.(interface{ runtimeFrozenTarget() FrozenMutationTarget }); ok {
		value := frozen.runtimeFrozenTarget()
		if value.selector.model == (ModelID{}) || value.selectorPredicate.rootModel != value.selector.model {
			return FrozenMutationTarget{}, invalidMutation("mutation", value.selector.model, FieldID{}, "mutation target is invalid")
		}
		return cloneFrozenMutationTarget(value), nil
	}
	var witness M
	node := target.mutationTarget(witness)
	selector, predicate, err := freezeUniqueSelector[M](ReadFindUnique, node.selector.model, node.selector.model, node.selector.key, node.selector.components)
	if err != nil {
		return FrozenMutationTarget{}, invalidMutationCause("mutation", node.selector.model, FieldID{}, "mutation target selector is invalid", err)
	}
	frozenSelector, err := predicate.freezeForModel(node.selector.model)
	if err != nil {
		return FrozenMutationTarget{}, invalidMutationCause("mutation", node.selector.model, FieldID{}, "mutation target selector is invalid", err)
	}
	result := FrozenMutationTarget{selector: selector, selectorPredicate: frozenSelector}
	if node.guard != nil {
		guard, guardErr := node.guard.freezeForModel(node.selector.model)
		if guardErr != nil {
			return FrozenMutationTarget{}, invalidMutationCause("mutation", node.selector.model, FieldID{}, "mutation target guard is invalid", guardErr)
		}
		result.guard = &guard
	}
	return result, nil
}

func cloneFrozenPredicate(value FrozenPredicate) FrozenPredicate {
	return FrozenPredicate{rootModel: value.rootModel, root: cloneFrozenCondition(value.root), canonical: append([]byte(nil), value.canonical...)}
}

// BatchResult is the opaque result of a bounded batch mutation.
type BatchResult struct{ count int64 }

func RuntimeBatchResult(count int64) BatchResult { return BatchResult{count: count} }
func (result BatchResult) Count() int64          { return result.count }

func invalidMutation(operation string, model ModelID, field FieldID, message string) error {
	return &Error{Code: CodeBadUserInput, Operation: operation, Model: model, Field: field, Message: message}
}

func invalidMutationCause(operation string, model ModelID, field FieldID, message string, cause error) error {
	failure := invalidMutation(operation, model, field, message).(*Error)
	failure.cause = cause
	return failure
}

func cloneMutationValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneMutationReflect(reflect.ValueOf(value)).Interface()
}

type mutationValueCloner interface{ cloneMutationExact() any }

func (value JSON[T]) cloneMutationExact() any {
	value.raw = append([]byte(nil), value.raw...)
	return value
}

func cloneMutationReflect(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	if value.CanInterface() {
		if cloner, ok := value.Interface().(mutationValueCloner); ok {
			return reflect.ValueOf(cloner.cloneMutationExact())
		}
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		inner := cloneMutationReflect(value.Elem())
		result := reflect.New(value.Type()).Elem()
		result.Set(inner)
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(cloneMutationReflect(value.Elem()))
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(cloneMutationReflect(value.Index(index)))
		}
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(cloneMutationReflect(iterator.Key()), cloneMutationReflect(iterator.Value()))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(cloneMutationReflect(value.Index(index)))
		}
		return result
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for index := 0; index < value.NumField(); index++ {
			if result.Field(index).CanSet() && value.Field(index).CanInterface() {
				result.Field(index).Set(cloneMutationReflect(value.Field(index)))
			}
		}
		return result
	default:
		return value
	}
}
