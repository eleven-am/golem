package mutation

import (
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

// LowerValues binds the already-coerced argument map of one validated
// generated GraphQL mutation root. Presence is retained by map membership:
// omitted create fields are absent while an explicit nil becomes create-null.
func (binder *MapBinder) LowerValues(operation RootOperation, model compilerir.ModelID, arguments map[string]any, selection []readir.Selection) (Request, error) {
	if binder == nil || binder.query == nil {
		return Request{}, compileerr.Invariantf("mutation binder is absent")
	}
	request, err := binder.lowerValues(operation, model, arguments, selection)
	if err != nil {
		return Request{}, compileerr.On(err, binder.contracts[model].GraphQLName)
	}
	return request, nil
}

func (binder *MapBinder) lowerValues(operation RootOperation, model compilerir.ModelID, arguments map[string]any, selection []readir.Selection) (Request, error) {
	state := mapInputState{binder: binder}
	root := MapRootInput{Operation: operation, Model: model, Selection: append([]readir.Selection(nil), selection...)}
	required := map[string]bool{}
	_, versioned := binder.versioned[model]
	switch operation {
	case Create:
		required["data"] = true
	case Update:
		required["where"], required["data"] = true, true
		if versioned {
			required["expectedVersion"] = true
		}
	case Upsert:
		required["where"], required["create"], required["update"] = true, true, true
		if versioned {
			required["expectation"] = true
		}
	case Delete:
		required["where"] = true
		if versioned {
			required["expectedVersion"] = true
		}
	case UpdateMany:
		if versioned {
			return Request{}, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedVersionedBatch)
		}
		required["where"], required["data"] = true, true
	case DeleteMany:
		if versioned {
			return Request{}, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedVersionedBatch)
		}
		required["where"] = true
	default:
		return Request{}, compileerr.Invariantf("unsupported mutation root operation %d", operation)
	}
	for _, name := range sortedMapKeys(arguments) {
		if !required[name] {
			return Request{}, compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableArgument, compileerr.Path(name))
		}
	}
	for _, name := range sortedRequired(required) {
		value, present := arguments[name]
		if !present {
			return Request{}, compileerr.New(compileerr.KindRequired, compileerr.DetailNone, compileerr.Path(name))
		}
		if value == nil {
			return Request{}, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path(name))
		}
	}
	root.Where = arguments["where"]
	if versioned {
		switch operation {
		case Update, Delete:
			value, ok := arguments["expectedVersion"].(int64)
			if !ok {
				return Request{}, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongKind, compileerr.Path("expectedVersion"), compileerr.Type("BigInt"))
			}
			if value <= 0 {
				return Request{}, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerVersion, compileerr.Path("expectedVersion"))
			}
			claim := golem.ExpectVersion(value)
			root.ExistingVersion = &claim
		case Upsert:
			claim, expectationErr := concurrencyExpectation(arguments["expectation"])
			if expectationErr != nil {
				return Request{}, compileerr.At(expectationErr, "expectation")
			}
			root.ConcurrencyExpectation = &claim
		}
	}
	var err error
	switch operation {
	case Create:
		root.Data, err = state.input(model, CreateInput, arguments["data"], nil, 1)
		err = compileerr.At(err, "data")
	case Update:
		root.Data, err = state.input(model, UpdateInput, arguments["data"], nil, 1)
		err = compileerr.At(err, "data")
	case Upsert:
		root.Create, err = state.input(model, CreateInput, arguments["create"], nil, 1)
		err = compileerr.At(err, "create")
		if err == nil {
			root.Update, err = state.input(model, UpdateInput, arguments["update"], nil, 1)
			err = compileerr.At(err, "update")
		}
	case UpdateMany:
		root.Data, err = state.input(model, UpdateManyInput, arguments["data"], nil, 1)
		err = compileerr.At(err, "data")
	}
	if err != nil {
		return Request{}, err
	}
	return binder.Lower(root)
}

func concurrencyExpectation(raw any) (golem.ConcurrencyExpectation, error) {
	object, ok := raw.(map[string]any)
	if !ok || len(object) != 1 {
		return golem.ConcurrencyExpectation{}, compileerr.New(compileerr.KindExactlyOne, compileerr.DetailNone)
	}
	if version, present := object["version"]; present {
		value, valid := version.(int64)
		if !valid {
			return golem.ConcurrencyExpectation{}, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongKind, compileerr.Path("version"), compileerr.Type("BigInt"))
		}
		if value <= 0 {
			return golem.ConcurrencyExpectation{}, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerVersion, compileerr.Path("version"))
		}
		return golem.ExpectExisting(value), nil
	}
	if absent, present := object["absent"]; present {
		value, valid := absent.(bool)
		if !valid || !value {
			return golem.ConcurrencyExpectation{}, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedMustBeTrue, compileerr.Path("absent"))
		}
		return golem.ExpectAbsent(), nil
	}
	return golem.ConcurrencyExpectation{}, compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableInput)
}

func (binder *MapBinder) CustomInput(kind InputKind, model compilerir.ModelID, raw any) (golem.FrozenMutationInput, error) {
	if binder == nil || binder.query == nil || kind < CreateInput || kind > UpdateManyInput {
		return golem.FrozenMutationInput{}, compileerr.Invariantf("custom mutation input binder or input kind is invalid")
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
		return golem.FrozenMutationInput{}, compileerr.Invariantf("custom mutation frozen input is absent")
	}
	return *frozen, nil
}

type mapInputState struct {
	binder *MapBinder
	nodes  int
}

func (state *mapInputState) input(modelID compilerir.ModelID, kind InputKind, raw any, excluded map[compilerir.FieldID]bool, depth int) (*Input, error) {
	if depth > state.binder.limits.MaxInputDepth {
		return nil, limitError("MaxInputDepth", state.binder.limits.MaxInputDepth)
	}
	model, contract, ok := state.model(modelID)
	if !ok {
		return nil, compileerr.Invariantf("mutation model %s is absent or unexposed", modelID)
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape, compileerr.Model(contract.GraphQLName))
	}
	if err := state.consume(1 + len(object)); err != nil {
		return nil, err
	}
	result := &Input{Kind: kind, Model: golem.ModelID(mustIdentity(modelID))}
	for _, name := range sortedMapKeys(object) {
		field, fieldContract, found := mutationField(model, contract, name)
		if !found || excluded[field.ID] || mutationFieldRefusal(field, fieldContract, kind) || state.runtimeOwned(modelID, field) {
			return nil, compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableInput, compileerr.Path(name), compileerr.Model(contract.GraphQLName))
		}
		if field.Scalar != nil {
			scalar, err := state.scalar(modelID, field, kind, object[name])
			if err != nil {
				return nil, compileerr.On(compileerr.At(err, name), contract.GraphQLName)
			}
			result.Scalars = append(result.Scalars, scalar)
			continue
		}
		if kind == UpdateManyInput {
			return nil, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedBatchRelation, compileerr.Path(name), compileerr.Model(contract.GraphQLName))
		}
		relations, err := state.relation(model, field, kind, object[name], depth+1)
		if err != nil {
			return nil, compileerr.On(compileerr.At(err, name), contract.GraphQLName)
		}
		result.Relations = append(result.Relations, relations...)
	}
	if kind != CreateInput {
		if _, versioned := state.binder.versioned[modelID]; versioned && len(result.Relations) != 0 {
			return nil, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedVersionedRelation, compileerr.Model(contract.GraphQLName))
		}
	}
	if kind != CreateInput && len(result.Scalars) == 0 && len(result.Relations) == 0 {
		return nil, compileerr.New(compileerr.KindEmpty, compileerr.DetailNone, compileerr.Model(contract.GraphQLName))
	}
	return result, nil
}

func (state *mapInputState) scalar(model compilerir.ModelID, field compilerir.FieldIR, kind InputKind, raw any) (Scalar, error) {
	publicField := golem.FieldID(mustIdentity(field.ID))
	if kind == CreateInput {
		if raw == nil {
			if field.Scalar == nil || !field.Scalar.Nullable {
				return Scalar{}, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone)
			}
			return Scalar{Field: publicField, Operation: golem.MutationFieldNull}, nil
		}
		value, err := state.binder.query.MutationValue(model, field.ID, raw)
		if err != nil {
			return Scalar{}, err
		}
		return Scalar{Field: publicField, Operation: golem.MutationFieldCreate, Value: value}, nil
	}
	envelope, ok := raw.(map[string]any)
	if !ok || len(envelope) != 1 {
		return Scalar{}, compileerr.New(compileerr.KindExactlyOne, compileerr.DetailNone)
	}
	name := sortedMapKeys(envelope)[0]
	value := envelope[name]
	operation := map[string]golem.MutationFieldOperation{
		"set": golem.MutationFieldSet, "setNull": golem.MutationFieldNull,
		"increment": golem.MutationFieldIncrement, "decrement": golem.MutationFieldDecrement,
	}[name]
	if operation == 0 {
		return Scalar{}, compileerr.New(compileerr.KindOperatorUnsupported, compileerr.OperatorNotSupported, compileerr.Path(name))
	}
	if operation == golem.MutationFieldNull {
		if field.Scalar == nil || !field.Scalar.Nullable {
			return Scalar{}, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone)
		}
		flag, valid := value.(bool)
		if !valid || !flag {
			return Scalar{}, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedMustBeTrue, compileerr.Path(name))
		}
		return Scalar{Field: publicField, Operation: operation}, nil
	}
	if value == nil {
		return Scalar{}, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path(name))
	}
	if (operation == golem.MutationFieldIncrement || operation == golem.MutationFieldDecrement) && !numericLogical(field.Scalar.Type.Kind) {
		return Scalar{}, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedNumericOnly, compileerr.Path(name))
	}
	exact, err := state.binder.query.MutationValue(model, field.ID, value)
	if err != nil {
		return Scalar{}, compileerr.At(err, name)
	}
	return Scalar{Field: publicField, Operation: operation, Value: exact}, nil
}

func (state *mapInputState) relation(parent compilerir.ModelDeclIR, field compilerir.FieldIR, kind InputKind, raw any, depth int) ([]Relation, error) {
	if field.Relation == nil {
		return nil, compileerr.Invariantf("mutation field %s is not relational", field.ID)
	}
	edge, ok := state.binder.relations[field.Relation.RelationID]
	if !ok {
		return nil, compileerr.Invariantf("mutation relation %s is absent", field.Relation.RelationID)
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
	if !ok {
		return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape)
	}
	if len(envelope) == 0 {
		return nil, compileerr.New(compileerr.KindEmpty, compileerr.DetailNone)
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
	for _, name := range sortedMapKeys(envelope) {
		if !known[name] {
			return nil, compileerr.New(compileerr.KindOperatorUnsupported, compileerr.OperatorNotSupported, compileerr.Path(name))
		}
	}
	var result []Relation
	for _, item := range order {
		value, present := envelope[item.name]
		if !present {
			continue
		}
		if kind == CreateInput && item.action > golem.MutationRelationConnectOrCreate {
			return nil, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedNestedInCreate, compileerr.Path(item.name))
		}
		_, targetVersioned := state.binder.versioned[targetID]
		writesExistingTarget := item.action == golem.MutationRelationUpdate || item.action == golem.MutationRelationUpdateMany || item.action == golem.MutationRelationUpsert || item.action == golem.MutationRelationDelete || item.action == golem.MutationRelationDeleteMany
		writesVersionedInverseOwner := field.Relation.Role == compilerir.RelationInverse && (item.action == golem.MutationRelationConnect || item.action == golem.MutationRelationConnectOrCreate || item.action == golem.MutationRelationDisconnect || item.action == golem.MutationRelationSet)
		if targetVersioned && (writesExistingTarget || writesVersionedInverseOwner) {
			return nil, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedNestedNeedsExpectation, compileerr.Path(item.name))
		}
		if !many && (item.action == golem.MutationRelationCreateMany || item.action == golem.MutationRelationSet || item.action == golem.MutationRelationUpdateMany || item.action == golem.MutationRelationDeleteMany) {
			return nil, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedNestedToOne, compileerr.Path(item.name))
		}
		entries, err := state.relationEntries(targetID, item.action, many, value, excluded, depth)
		if err != nil {
			return nil, compileerr.At(err, item.name)
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
		return nil, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone)
	}
	if !many && (action == golem.MutationRelationDisconnect || action == golem.MutationRelationDelete) {
		flag, ok := raw.(bool)
		if !ok || !flag {
			return nil, compileerr.New(compileerr.KindNotAllowedHere, compileerr.NotAllowedMustBeTrue)
		}
		return []RelationEntry{{}}, nil
	}
	items := []any{raw}
	if many {
		var ok bool
		items, ok = raw.([]any)
		if !ok {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape)
		}
		if len(items) > state.binder.limits.MaxListItems {
			return nil, limitError("MaxListItems", state.binder.limits.MaxListItems)
		}
		if action == golem.MutationRelationSet && len(items) == 0 {
			return []RelationEntry{}, nil
		}
		if len(items) == 0 {
			return nil, compileerr.New(compileerr.KindEmpty, compileerr.DetailNone)
		}
	}
	if err := state.consume(len(items)); err != nil {
		return nil, err
	}
	entries := make([]RelationEntry, len(items))
	for index, item := range items {
		entry, err := state.relationEntry(target, action, many, item, excluded, depth)
		if err != nil {
			if !many {
				return nil, err
			}
			return nil, compileerr.At(err, compileerr.Index("", index))
		}
		entries[index] = entry
	}
	return entries, nil
}

func (state *mapInputState) relationEntry(target compilerir.ModelID, action golem.MutationRelationAction, many bool, raw any, excluded map[compilerir.FieldID]bool, depth int) (RelationEntry, error) {
	object := func(required ...string) (map[string]any, error) {
		value, ok := raw.(map[string]any)
		if !ok {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape)
		}
		for _, name := range required {
			if item, present := value[name]; !present || item == nil {
				return nil, compileerr.New(compileerr.KindRequired, compileerr.DetailNone, compileerr.Path(name))
			}
		}
		if len(value) != len(required) {
			return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape)
		}
		return value, nil
	}
	switch action {
	case golem.MutationRelationCreate, golem.MutationRelationCreateMany:
		input, err := state.input(target, CreateInput, raw, excluded, depth)
		if err != nil {
			return RelationEntry{}, err
		}
		return RelationEntry{Create: input}, nil
	case golem.MutationRelationConnect, golem.MutationRelationDisconnect, golem.MutationRelationSet, golem.MutationRelationDelete:
		targetValue, err := state.target(target, raw)
		if err != nil {
			return RelationEntry{}, err
		}
		return RelationEntry{Target: targetValue}, nil
	case golem.MutationRelationConnectOrCreate:
		value, err := object("where", "create")
		if err != nil {
			return RelationEntry{}, err
		}
		targetValue, err := state.target(target, value["where"])
		if err != nil {
			return RelationEntry{}, compileerr.At(err, "where")
		}
		create, err := state.input(target, CreateInput, value["create"], excluded, depth)
		if err != nil {
			return RelationEntry{}, compileerr.At(err, "create")
		}
		return RelationEntry{Target: targetValue, Create: create}, nil
	case golem.MutationRelationUpdate:
		if many {
			value, err := object("where", "data")
			if err != nil {
				return RelationEntry{}, err
			}
			targetValue, err := state.target(target, value["where"])
			if err != nil {
				return RelationEntry{}, compileerr.At(err, "where")
			}
			update, err := state.input(target, UpdateInput, value["data"], excluded, depth)
			if err != nil {
				return RelationEntry{}, compileerr.At(err, "data")
			}
			return RelationEntry{Target: targetValue, Update: update}, nil
		}
		update, err := state.input(target, UpdateInput, raw, excluded, depth)
		if err != nil {
			return RelationEntry{}, err
		}
		return RelationEntry{Update: update}, nil
	case golem.MutationRelationUpdateMany:
		value, err := object("where", "data")
		if err != nil {
			return RelationEntry{}, err
		}
		predicate, err := state.predicate(target, value["where"])
		if err != nil {
			return RelationEntry{}, compileerr.At(err, "where")
		}
		update, err := state.input(target, UpdateManyInput, value["data"], nil, depth)
		if err != nil {
			return RelationEntry{}, compileerr.At(err, "data")
		}
		return RelationEntry{Predicate: predicate, Update: update}, nil
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
				return RelationEntry{}, compileerr.At(err, "where")
			}
		}
		create, err := state.input(target, CreateInput, value["create"], excluded, depth)
		if err != nil {
			return RelationEntry{}, compileerr.At(err, "create")
		}
		update, err := state.input(target, UpdateInput, value["update"], excluded, depth)
		if err != nil {
			return RelationEntry{}, compileerr.At(err, "update")
		}
		return RelationEntry{Target: targetValue, Create: create, Update: update}, nil
	case golem.MutationRelationDeleteMany:
		predicate, err := state.predicate(target, raw)
		if err != nil {
			return RelationEntry{}, err
		}
		return RelationEntry{Predicate: predicate}, nil
	default:
		return RelationEntry{}, compileerr.Invariantf("unsupported nested mutation action %d", action)
	}
}

func (state *mapInputState) target(model compilerir.ModelID, raw any) (*golem.FrozenMutationTarget, error) {
	value, err := state.binder.Target(model, raw, "")
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (state *mapInputState) predicate(model compilerir.ModelID, raw any) (*golem.FrozenPredicate, error) {
	condition, err := state.binder.query.WhereAt(model, raw, "")
	if err != nil {
		return nil, err
	}
	value, err := state.binder.query.FreezePredicate(condition)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (state *mapInputState) model(id compilerir.ModelID) (compilerir.ModelDeclIR, compilerir.ModelContractIR, bool) {
	model, modelOK := state.binder.models[id]
	contract, contractOK := state.binder.contracts[id]
	return model, contract, modelOK && contractOK && contract.Exposed
}

func (state *mapInputState) consume(nodes int) error {
	state.nodes += nodes
	if state.nodes > state.binder.limits.MaxInputNodes {
		return limitError("MaxInputNodes", state.binder.limits.MaxInputNodes)
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

func mutationFieldRefusal(field compilerir.FieldIR, contract compilerir.FieldContractIR, kind InputKind) bool {
	for _, mode := range contract.Modes {
		if mode == compilerir.ModeHidden || mode == compilerir.ModeInternal || mode == compilerir.ModeReadOnly || mode == compilerir.ModeSystem {
			return true
		}
		if mode == compilerir.ModeImmutable && kind != CreateInput {
			return true
		}
	}
	return field.Scalar != nil && (field.Scalar.DatabaseReadOnly || field.Scalar.Generation != nil || field.Scalar.Updated)
}

func (state *mapInputState) runtimeOwned(model compilerir.ModelID, field compilerir.FieldIR) bool {
	concurrency, enabled := state.binder.versioned[model]
	return enabled && field.ID == concurrency
}

func limitError(name string, maximum int) error {
	return compileerr.New(compileerr.KindInputLimit, compileerr.DetailNone, compileerr.Limit(name, maximum))
}

func sortedRequired(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
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
