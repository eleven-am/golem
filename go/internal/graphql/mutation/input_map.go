package mutation

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

// LowerValues binds the already-coerced argument map of one validated
// generated GraphQL mutation root. Presence is retained by map membership:
// omitted create fields are absent while an explicit nil becomes create-null.
func (binder *MapBinder) LowerValues(operation RootOperation, model compilerir.ModelID, arguments map[string]any, selection []readir.Selection) (Request, error) {
	if binder == nil || binder.query == nil {
		return Request{}, fmt.Errorf("P5_MUTATION_MAP: binder is absent")
	}
	state := mapInputState{binder: binder}
	root := MapRootInput{Operation: operation, Model: model, Selection: append([]readir.Selection(nil), selection...)}
	required := map[string]bool{}
	switch operation {
	case Create:
		required["data"] = true
	case Update:
		required["where"], required["data"] = true, true
	case Upsert:
		required["where"], required["create"], required["update"] = true, true, true
	case Delete:
		required["where"] = true
	case UpdateMany:
		required["where"], required["data"] = true, true
	case DeleteMany:
		required["where"] = true
	default:
		return Request{}, fmt.Errorf("P5_MUTATION_MAP: unsupported root operation %d", operation)
	}
	for name := range arguments {
		if !required[name] {
			return Request{}, fmt.Errorf("P5_MUTATION_ARGUMENT: %s is not accepted for this operation", name)
		}
	}
	for name := range required {
		if value, present := arguments[name]; !present || value == nil {
			return Request{}, fmt.Errorf("P5_MUTATION_ARGUMENT: %s is required and cannot be null", name)
		}
	}
	root.Where = arguments["where"]
	var err error
	switch operation {
	case Create:
		root.Data, err = state.input(model, CreateInput, arguments["data"], nil, 1)
	case Update:
		root.Data, err = state.input(model, UpdateInput, arguments["data"], nil, 1)
	case Upsert:
		root.Create, err = state.input(model, CreateInput, arguments["create"], nil, 1)
		if err == nil {
			root.Update, err = state.input(model, UpdateInput, arguments["update"], nil, 1)
		}
	case UpdateMany:
		root.Data, err = state.input(model, UpdateManyInput, arguments["data"], nil, 1)
	}
	if err != nil {
		return Request{}, err
	}
	return binder.Lower(root)
}

func (binder *MapBinder) CustomInput(kind InputKind, model compilerir.ModelID, raw any) (golem.FrozenMutationInput, error) {
	if binder == nil || binder.query == nil || kind < CreateInput || kind > UpdateManyInput {
		return golem.FrozenMutationInput{}, fmt.Errorf("P5_MUTATION_CUSTOM: binder or input kind is invalid")
	}
	mapped, err := (&mapInputState{binder: binder}).input(model, kind, raw, nil, 1)
	if err != nil {
		return golem.FrozenMutationInput{}, err
	}
	frozen, err := (&lowerer{limits: normalizeLimits(binder.limits)}).input(mapped, kind, mapped.Model, 1)
	if err != nil {
		return golem.FrozenMutationInput{}, err
	}
	if frozen == nil {
		return golem.FrozenMutationInput{}, fmt.Errorf("P5_MUTATION_CUSTOM: frozen input is absent")
	}
	return *frozen, nil
}

type mapInputState struct {
	binder *MapBinder
	nodes  int
}

func (state *mapInputState) input(modelID compilerir.ModelID, kind InputKind, raw any, excluded map[compilerir.FieldID]bool, depth int) (*Input, error) {
	if depth > state.binder.limits.MaxInputDepth {
		return nil, fmt.Errorf("P5_MUTATION_LIMIT: input depth exceeds %d", state.binder.limits.MaxInputDepth)
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("P5_MUTATION_INPUT: expected input object, got %T", raw)
	}
	if err := state.consume(1 + len(object)); err != nil {
		return nil, err
	}
	model, contract, ok := state.model(modelID)
	if !ok {
		return nil, fmt.Errorf("P5_MUTATION_MODEL: model is absent or unexposed")
	}
	result := &Input{Kind: kind, Model: golem.ModelID(mustIdentity(modelID))}
	for _, name := range sortedMapKeys(object) {
		field, fieldContract, found := mutationField(model, contract, name)
		if !found || excluded[field.ID] {
			return nil, fmt.Errorf("P5_MUTATION_FIELD: unknown or excluded field %s.%s", contract.GraphQLName, name)
		}
		if reason := mutationFieldRefusal(field, fieldContract, kind); reason != "" {
			return nil, fmt.Errorf("P5_MUTATION_FIELD: %s.%s %s", contract.GraphQLName, name, reason)
		}
		if field.Scalar != nil {
			scalar, err := state.scalar(modelID, field, kind, object[name])
			if err != nil {
				return nil, fmt.Errorf("P5_MUTATION_VALUE: %s.%s: %w", contract.GraphQLName, name, err)
			}
			result.Scalars = append(result.Scalars, scalar)
			continue
		}
		if kind == UpdateManyInput {
			return nil, fmt.Errorf("P5_MUTATION_FIELD: update-many cannot contain relation %s.%s", contract.GraphQLName, name)
		}
		relations, err := state.relation(model, field, kind, object[name], depth+1)
		if err != nil {
			return nil, fmt.Errorf("P5_MUTATION_RELATION: %s.%s: %w", contract.GraphQLName, name, err)
		}
		result.Relations = append(result.Relations, relations...)
	}
	if kind != CreateInput && len(result.Scalars) == 0 && len(result.Relations) == 0 {
		return nil, fmt.Errorf("P5_MUTATION_INPUT: update input has no operations")
	}
	return result, nil
}

func (state *mapInputState) scalar(model compilerir.ModelID, field compilerir.FieldIR, kind InputKind, raw any) (Scalar, error) {
	publicField := golem.FieldID(mustIdentity(field.ID))
	if kind == CreateInput {
		if raw == nil {
			if field.Scalar == nil || !field.Scalar.Nullable {
				return Scalar{}, fmt.Errorf("explicit null requires a nullable field")
			}
			return Scalar{Field: publicField, Operation: golem.MutationFieldNull}, nil
		}
		value, err := state.binder.query.MutationValue(model, field.ID, raw)
		return Scalar{Field: publicField, Operation: golem.MutationFieldCreate, Value: value}, err
	}
	envelope, ok := raw.(map[string]any)
	if !ok || len(envelope) != 1 {
		return Scalar{}, fmt.Errorf("update operation must contain exactly one member")
	}
	name := sortedMapKeys(envelope)[0]
	value := envelope[name]
	operation := map[string]golem.MutationFieldOperation{
		"set": golem.MutationFieldSet, "setNull": golem.MutationFieldNull,
		"increment": golem.MutationFieldIncrement, "decrement": golem.MutationFieldDecrement,
	}[name]
	if operation == 0 {
		return Scalar{}, fmt.Errorf("unknown update operation %q", name)
	}
	if operation == golem.MutationFieldNull {
		flag, valid := value.(bool)
		if !valid || !flag || field.Scalar == nil || !field.Scalar.Nullable {
			return Scalar{}, fmt.Errorf("setNull must be true on a nullable field")
		}
		return Scalar{Field: publicField, Operation: operation}, nil
	}
	if value == nil {
		return Scalar{}, fmt.Errorf("%s requires a non-null exact value", name)
	}
	if (operation == golem.MutationFieldIncrement || operation == golem.MutationFieldDecrement) && !numericLogical(field.Scalar.Type.Kind) {
		return Scalar{}, fmt.Errorf("%s requires a numeric field", name)
	}
	exact, err := state.binder.query.MutationValue(model, field.ID, value)
	return Scalar{Field: publicField, Operation: operation, Value: exact}, err
}

func (state *mapInputState) relation(parent compilerir.ModelDeclIR, field compilerir.FieldIR, kind InputKind, raw any, depth int) ([]Relation, error) {
	if field.Relation == nil {
		return nil, fmt.Errorf("field is not relational")
	}
	edge, ok := state.binder.relations[field.Relation.RelationID]
	if !ok {
		return nil, fmt.Errorf("relation identity is absent")
	}
	targetID := edge.TargetModel
	back := edge.InverseField
	if field.Relation.Role == compilerir.RelationInverse {
		targetID = edge.SourceModel
		value := edge.SourceField
		back = &value
	}
	excluded := map[compilerir.FieldID]bool{}
	if back != nil {
		excluded[*back] = true
	}
	if field.Relation.Role == compilerir.RelationInverse {
		for _, local := range edge.LocalFields {
			excluded[local] = true
		}
	}
	envelope, ok := raw.(map[string]any)
	if !ok || len(envelope) == 0 {
		return nil, fmt.Errorf("relation operation envelope is empty or invalid")
	}
	many := field.Relation.Kind == compilerir.RelationHasMany
	order := []struct {
		name   string
		action golem.MutationRelationAction
	}{
		{"create", golem.MutationRelationCreate}, {"createMany", golem.MutationRelationCreateMany}, {"connect", golem.MutationRelationConnect},
		{"connectOrCreate", golem.MutationRelationConnectOrCreate}, {"disconnect", golem.MutationRelationDisconnect}, {"set", golem.MutationRelationSet},
		{"update", golem.MutationRelationUpdate}, {"updateMany", golem.MutationRelationUpdateMany}, {"upsert", golem.MutationRelationUpsert},
		{"delete", golem.MutationRelationDelete}, {"deleteMany", golem.MutationRelationDeleteMany},
	}
	known := map[string]bool{}
	for _, item := range order {
		known[item.name] = true
	}
	for name := range envelope {
		if !known[name] {
			return nil, fmt.Errorf("unknown nested operation %q", name)
		}
	}
	var result []Relation
	for _, item := range order {
		value, present := envelope[item.name]
		if !present {
			continue
		}
		if kind == CreateInput && item.action > golem.MutationRelationConnectOrCreate {
			return nil, fmt.Errorf("nested operation %s is not legal during create", item.name)
		}
		if !many && (item.action == golem.MutationRelationCreateMany || item.action == golem.MutationRelationSet || item.action == golem.MutationRelationUpdateMany || item.action == golem.MutationRelationDeleteMany) {
			return nil, fmt.Errorf("nested operation %s is not legal on a to-one relation", item.name)
		}
		entries, err := state.relationEntries(targetID, item.action, many, value, excluded, depth)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", item.name, err)
		}
		result = append(result, Relation{
			Field: golem.FieldID(mustIdentity(field.ID)), Relation: golem.RelationID(mustIdentity(edge.ID)),
			TargetModel: golem.ModelID(mustIdentity(targetID)), Action: item.action, Entries: entries,
		})
	}
	return result, nil
}

func (state *mapInputState) relationEntries(target compilerir.ModelID, action golem.MutationRelationAction, many bool, raw any, excluded map[compilerir.FieldID]bool, depth int) ([]RelationEntry, error) {
	if raw == nil {
		return nil, fmt.Errorf("nested operation cannot be null")
	}
	if !many && (action == golem.MutationRelationDisconnect || action == golem.MutationRelationDelete) {
		flag, ok := raw.(bool)
		if !ok || !flag {
			return nil, fmt.Errorf("to-one boolean operation must be true")
		}
		return []RelationEntry{{}}, nil
	}
	items := []any{raw}
	if many {
		var ok bool
		items, ok = raw.([]any)
		if !ok {
			return nil, fmt.Errorf("to-many operation requires a list")
		}
		if len(items) > state.binder.limits.MaxListItems {
			return nil, fmt.Errorf("P5_MUTATION_LIMIT: list items exceed %d", state.binder.limits.MaxListItems)
		}
		if action == golem.MutationRelationSet && len(items) == 0 {
			return []RelationEntry{}, nil
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("nested operation list cannot be empty")
		}
	}
	if err := state.consume(len(items)); err != nil {
		return nil, err
	}
	entries := make([]RelationEntry, len(items))
	for index, item := range items {
		entry, err := state.relationEntry(target, action, many, item, excluded, depth)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", index, err)
		}
		entries[index] = entry
	}
	return entries, nil
}

func (state *mapInputState) relationEntry(target compilerir.ModelID, action golem.MutationRelationAction, many bool, raw any, excluded map[compilerir.FieldID]bool, depth int) (RelationEntry, error) {
	object := func(required ...string) (map[string]any, error) {
		value, ok := raw.(map[string]any)
		if !ok || len(value) != len(required) {
			return nil, fmt.Errorf("nested object requires exactly %v", required)
		}
		for _, name := range required {
			if item, present := value[name]; !present || item == nil {
				return nil, fmt.Errorf("nested object member %s is required", name)
			}
		}
		return value, nil
	}
	switch action {
	case golem.MutationRelationCreate, golem.MutationRelationCreateMany:
		input, err := state.input(target, CreateInput, raw, excluded, depth)
		return RelationEntry{Create: input}, err
	case golem.MutationRelationConnect, golem.MutationRelationDisconnect, golem.MutationRelationSet, golem.MutationRelationDelete:
		targetValue, err := state.target(target, raw)
		return RelationEntry{Target: targetValue}, err
	case golem.MutationRelationConnectOrCreate:
		value, err := object("where", "create")
		if err != nil {
			return RelationEntry{}, err
		}
		targetValue, err := state.target(target, value["where"])
		if err != nil {
			return RelationEntry{}, err
		}
		create, err := state.input(target, CreateInput, value["create"], excluded, depth)
		return RelationEntry{Target: targetValue, Create: create}, err
	case golem.MutationRelationUpdate:
		if many {
			value, err := object("where", "data")
			if err != nil {
				return RelationEntry{}, err
			}
			targetValue, err := state.target(target, value["where"])
			if err != nil {
				return RelationEntry{}, err
			}
			update, err := state.input(target, UpdateInput, value["data"], excluded, depth)
			return RelationEntry{Target: targetValue, Update: update}, err
		}
		update, err := state.input(target, UpdateInput, raw, excluded, depth)
		return RelationEntry{Update: update}, err
	case golem.MutationRelationUpdateMany:
		value, err := object("where", "data")
		if err != nil {
			return RelationEntry{}, err
		}
		predicate, err := state.predicate(target, value["where"])
		if err != nil {
			return RelationEntry{}, err
		}
		update, err := state.input(target, UpdateManyInput, value["data"], nil, depth)
		return RelationEntry{Predicate: predicate, Update: update}, err
	case golem.MutationRelationUpsert:
		required := []string{"create", "update"}
		if many {
			required = []string{"where", "create", "update"}
		}
		value, err := object(required...)
		if err != nil {
			return RelationEntry{}, err
		}
		var targetValue *golem.FrozenMutationTarget
		if where, present := value["where"]; present {
			targetValue, err = state.target(target, where)
			if err != nil {
				return RelationEntry{}, err
			}
		}
		create, err := state.input(target, CreateInput, value["create"], excluded, depth)
		if err != nil {
			return RelationEntry{}, err
		}
		update, err := state.input(target, UpdateInput, value["update"], excluded, depth)
		return RelationEntry{Target: targetValue, Create: create, Update: update}, err
	case golem.MutationRelationDeleteMany:
		predicate, err := state.predicate(target, raw)
		return RelationEntry{Predicate: predicate}, err
	default:
		return RelationEntry{}, fmt.Errorf("unsupported nested operation")
	}
}

func (state *mapInputState) target(model compilerir.ModelID, raw any) (*golem.FrozenMutationTarget, error) {
	selector, condition, err := state.binder.query.MutationSelector(model, raw)
	if err != nil {
		return nil, err
	}
	predicate, err := state.binder.query.FreezePredicate(condition)
	if err != nil {
		return nil, err
	}
	fields := selector.Fields()
	public := make([]golem.FieldID, len(fields))
	for index, field := range fields {
		public[index] = golem.FieldID(field)
	}
	value, err := golem.RuntimeMutationTargetFromPredicate(golem.ModelID(mustIdentity(model)), selector.KeyID(), public, predicate)
	return &value, err
}

func (state *mapInputState) predicate(model compilerir.ModelID, raw any) (*golem.FrozenPredicate, error) {
	condition, err := state.binder.query.MutationWhere(model, raw)
	if err != nil {
		return nil, err
	}
	value, err := state.binder.query.FreezePredicate(condition)
	return &value, err
}

func (state *mapInputState) model(id compilerir.ModelID) (compilerir.ModelDeclIR, compilerir.ModelContractIR, bool) {
	model, modelOK := state.binder.models[id]
	contract, contractOK := state.binder.contracts[id]
	return model, contract, modelOK && contractOK && contract.Exposed
}

func (state *mapInputState) consume(nodes int) error {
	state.nodes += nodes
	if state.nodes > state.binder.limits.MaxInputNodes {
		return fmt.Errorf("P5_MUTATION_LIMIT: input nodes exceed %d", state.binder.limits.MaxInputNodes)
	}
	return nil
}

func mutationField(model compilerir.ModelDeclIR, contract compilerir.ModelContractIR, name string) (compilerir.FieldIR, compilerir.FieldContractIR, bool) {
	var fieldContract compilerir.FieldContractIR
	found := false
	for _, candidate := range contract.Fields {
		if candidate.GraphQLName == name {
			fieldContract, found = candidate, true
			break
		}
	}
	if !found {
		return compilerir.FieldIR{}, compilerir.FieldContractIR{}, false
	}
	for _, field := range model.Fields {
		if field.ID == fieldContract.FieldID {
			return field, fieldContract, true
		}
	}
	return compilerir.FieldIR{}, compilerir.FieldContractIR{}, false
}

func mutationFieldRefusal(field compilerir.FieldIR, contract compilerir.FieldContractIR, kind InputKind) string {
	for _, mode := range contract.Modes {
		if mode == compilerir.ModeHidden || mode == compilerir.ModeReadOnly {
			return "is not writable"
		}
		if mode == compilerir.ModeImmutable && kind != CreateInput {
			return "is immutable"
		}
	}
	if field.Scalar != nil && (field.Scalar.DatabaseReadOnly || field.Scalar.Generation != nil || field.Scalar.Updated) {
		return "is runtime or database owned"
	}
	return ""
}

func numericLogical(kind compilerir.LogicalTypeKind) bool {
	return kind == compilerir.TypeInt16 || kind == compilerir.TypeInt32 || kind == compilerir.TypeInt64 || kind == compilerir.TypeFloat32 || kind == compilerir.TypeFloat64 || kind == compilerir.TypeDecimal
}

func sortedMapKeys(value map[string]any) []string {
	result := make([]string, 0, len(value))
	for key := range value {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func mustIdentity[T ~string](value T) [16]byte {
	parsed, _ := fixedID(string(value))
	return parsed
}
