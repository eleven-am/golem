// Package bind lowers already-coerced GraphQL inputs into the existing P2/P3
// request IR. Public GraphQL names are resolved exactly once against ContractIR;
// no name survives into authorization, planning, or SQL rendering.
package bind

import (
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	"github.com/vektah/gqlparser/v2/ast"
)

type Limits struct {
	MaxInputDepth int
	MaxInputNodes int
	MaxListItems  int
	MaxPageSize   int
}

type Binder struct {
	compilation compilerir.CompilationIR
	providers   policyir.ProviderSet
	limits      Limits
	models      map[compilerir.ModelID]compilerir.ModelDeclIR
	contracts   map[compilerir.ModelID]compilerir.ModelContractIR
	relations   map[compilerir.RelationID]compilerir.RelationIR
	enums       map[compilerir.EnumID]compilerir.EnumContractIR
	enumLabels  map[enumMember]string
}

type enumMember struct {
	enum  policyir.EnumID
	value policyir.EnumValueID
}

type QueryInput struct {
	Operation  readir.Operation
	Model      compilerir.ModelID
	Arguments  map[string]any
	Selections []readir.Selection
}

func New(compilation compilerir.CompilationIR, limits Limits) (*Binder, error) {
	if limits.MaxInputDepth <= 0 {
		limits.MaxInputDepth = 32
	}
	if limits.MaxInputNodes <= 0 {
		limits.MaxInputNodes = 16_384
	}
	if limits.MaxListItems <= 0 {
		limits.MaxListItems = 4_096
	}
	providers, err := providerSet(compilation.Model.Providers)
	if err != nil {
		return nil, err
	}
	if limits.MaxPageSize > 0 {
		for _, contract := range compilation.Contract.Models {
			if contract.Exposed && int64(contract.Limits.DefaultPageSize) > int64(limits.MaxPageSize) {
				return nil, fmt.Errorf("GraphQL model %s default page size %d exceeds the MaxPageSize limit %d: raise MaxPageSize or lower the model's default page size", contract.GraphQLName, contract.Limits.DefaultPageSize, limits.MaxPageSize)
			}
		}
	}
	b := &Binder{
		compilation: compilation,
		providers:   providers,
		limits:      limits,
		models:      map[compilerir.ModelID]compilerir.ModelDeclIR{},
		contracts:   map[compilerir.ModelID]compilerir.ModelContractIR{},
		relations:   map[compilerir.RelationID]compilerir.RelationIR{},
		enums:       map[compilerir.EnumID]compilerir.EnumContractIR{},
		enumLabels:  map[enumMember]string{},
	}
	for _, model := range compilation.Model.Models {
		b.models[model.ID] = model
	}
	for _, contract := range compilation.Contract.Models {
		b.contracts[contract.ModelID] = contract
	}
	for _, relation := range compilation.Model.Relations {
		b.relations[relation.ID] = relation
	}
	for _, enum := range compilation.Contract.Enums {
		b.enums[enum.EnumID] = enum
	}
	for _, enum := range compilation.Model.Enums {
		enumID, enumErr := fixedID(string(enum.ID))
		if enumErr != nil {
			return nil, enumErr
		}
		for _, value := range enum.Values {
			valueID, valueErr := fixedID(string(value.ID))
			if valueErr != nil {
				return nil, valueErr
			}
			b.enumLabels[enumMember{enum: policyir.EnumID(enumID), value: policyir.EnumValueID(valueID)}] = value.WireValue
		}
	}
	return b, nil
}

func (b *Binder) Query(input QueryInput) (readir.Request, error) {
	request, err := b.query(input)
	if err != nil {
		return readir.Request{}, compileerr.On(err, b.contracts[input.Model].GraphQLName)
	}
	return request, nil
}

func (b *Binder) query(input QueryInput) (readir.Request, error) {
	model, contract, err := b.model(input.Model)
	if err != nil {
		return readir.Request{}, err
	}
	if input.Operation != readir.FindUnique && input.Operation != readir.FindMany && input.Operation != readir.Count {
		return readir.Request{}, compileerr.Invariantf("unsupported read operation %d", input.Operation)
	}
	args := input.Arguments
	if args == nil {
		args = map[string]any{}
	}
	allowed := map[string]bool{"where": true}
	if input.Operation == readir.FindMany {
		for _, name := range []string{"orderBy", "cursor", "distinct", "skip", "take"} {
			allowed[name] = true
		}
	}
	for _, name := range sortedKeys(args) {
		if !allowed[name] {
			return readir.Request{}, compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableArgument, compileerr.Path(name))
		}
	}
	modelID, err := policyModelID(input.Model)
	if err != nil {
		return readir.Request{}, err
	}
	request := readir.RequestInput{Operation: input.Operation, Model: modelID}
	if input.Operation != readir.Count {
		if len(input.Selections) == 0 {
			return readir.Request{}, compileerr.Invariantf("%s read has an empty projection", contract.GraphQLName)
		}
		request.Projection = readir.ProjectionSelect
		request.Selection = append([]readir.Selection(nil), input.Selections...)
	}
	state := inputState{binder: b}
	if input.Operation == readir.FindUnique {
		raw, present := args["where"]
		if !present {
			return readir.Request{}, compileerr.New(compileerr.KindRequired, compileerr.DetailNone, compileerr.Path("where"))
		}
		if raw == nil {
			return readir.Request{}, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path("where"))
		}
		selector, predicate, selectorErr := state.selector(model, contract, raw, "where", 1)
		if selectorErr != nil {
			return readir.Request{}, selectorErr
		}
		request.Selector, request.Where = &selector, &predicate
	} else if raw, present := args["where"]; present {
		if raw == nil {
			return readir.Request{}, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path("where"))
		}
		predicate, whereErr := state.where(model, contract, raw, "where", 1)
		if whereErr != nil {
			return readir.Request{}, whereErr
		}
		request.Where = &predicate
	}
	if input.Operation == readir.FindMany {
		if raw, present := args["orderBy"]; present {
			orders, orderErr := state.orderBy(model, contract, raw, "orderBy")
			if orderErr != nil {
				return readir.Request{}, orderErr
			}
			request.OrderBy = orders
		}
		if raw, present := args["cursor"]; present {
			if raw == nil {
				return readir.Request{}, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path("cursor"))
			}
			selector, predicate, cursorErr := state.selector(model, contract, raw, "cursor", 1)
			if cursorErr != nil {
				return readir.Request{}, cursorErr
			}
			cursor, cursorErr := readir.NewCursor(selector, predicate)
			if cursorErr != nil {
				return readir.Request{}, compileerr.Invariant(cursorErr)
			}
			request.Cursor = &cursor
		}
		if raw, present := args["distinct"]; present {
			fields, distinctErr := state.distinct(model, contract, raw)
			if distinctErr != nil {
				return readir.Request{}, distinctErr
			}
			request.Distinct = fields
		}
		if raw, present := args["skip"]; present {
			value, valueErr := exactInt(raw)
			if valueErr != nil {
				return readir.Request{}, compileerr.At(valueErr, "skip")
			}
			if value < 0 {
				return readir.Request{}, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerSkip, compileerr.Path("skip"))
			}
			request.Skip = &value
		}
		if raw, present := args["take"]; present {
			value, valueErr := exactInt(raw)
			if valueErr != nil {
				return readir.Request{}, compileerr.At(valueErr, "take")
			}
			if value == 0 {
				return readir.Request{}, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerTakeNonZero, compileerr.Path("take"))
			}
			maximum := readir.NarrowCap(0, int(contract.Limits.MaxPageSize))
			maximum = readir.NarrowCap(maximum, int(contract.Limits.MaxTake))
			maximum = readir.NarrowCap(maximum, b.limits.MaxPageSize)
			if maximum != 0 && absInt(value) > maximum {
				return readir.Request{}, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerTake, compileerr.Path("take"), compileerr.Bounds(-maximum, maximum))
			}
			if value < 0 && len(request.OrderBy) == 0 {
				return readir.Request{}, compileerr.New(compileerr.KindNegativeTakeRequiresOrderBy, compileerr.DetailNone, compileerr.Path("take"))
			}
			request.Take = &value
		} else if contract.Limits.DefaultPageSize != 0 {
			value := int(contract.Limits.DefaultPageSize)
			request.Take = &value
		}
	}
	if state.nodes > b.limits.MaxInputNodes {
		return readir.Request{}, inputNodesExceeded(b.limits.MaxInputNodes)
	}
	bound, err := readir.NewRequest(request)
	if err != nil {
		return readir.Request{}, compileerr.Invariant(err)
	}
	return bound, nil
}

// MutationSelector binds a GraphQL unique-selector map without manufacturing
// a read projection. P5-E uses the same exact scalar and selector machinery as
// P5-C, then freezes the result at P4's mutation boundary.
func (b *Binder) MutationSelector(modelID compilerir.ModelID, raw any) (readir.Selector, policyir.Condition, error) {
	return b.SelectorAt(modelID, raw, "where")
}

func (b *Binder) SelectorAt(modelID compilerir.ModelID, raw any, path string) (readir.Selector, policyir.Condition, error) {
	model, contract, err := b.model(modelID)
	if err != nil {
		return readir.Selector{}, policyir.Condition{}, err
	}
	if raw == nil {
		return readir.Selector{}, policyir.Condition{}, compileerr.On(compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path(path)), contract.GraphQLName)
	}
	state := inputState{binder: b}
	selector, condition, selectorErr := state.selector(model, contract, raw, path, 1)
	if selectorErr != nil {
		return readir.Selector{}, policyir.Condition{}, compileerr.On(selectorErr, contract.GraphQLName)
	}
	return selector, condition, nil
}

// MutationWhere binds a GraphQL batch/nested where map through the same P2
// condition vocabulary, provider checks, exact values, and public limits as a
// P5-C read filter.
func (b *Binder) MutationWhere(modelID compilerir.ModelID, raw any) (policyir.Condition, error) {
	return b.WhereAt(modelID, raw, "where")
}

func (b *Binder) WhereAt(modelID compilerir.ModelID, raw any, path string) (policyir.Condition, error) {
	model, contract, err := b.model(modelID)
	if err != nil {
		return policyir.Condition{}, err
	}
	if raw == nil {
		return policyir.Condition{}, compileerr.On(compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone, compileerr.Path(path)), contract.GraphQLName)
	}
	state := inputState{binder: b}
	condition, whereErr := state.where(model, contract, raw, path, 1)
	if whereErr != nil {
		return policyir.Condition{}, compileerr.On(whereErr, contract.GraphQLName)
	}
	return condition, nil
}

// Child compiles arguments on a selected to-many relation into a P3 request.
// It is suitable for selectset.Request.Child.
func (b *Binder) Child(target compilerir.ModelID, field *ast.Field, selections []readir.Selection, variables map[string]any) (readir.Request, error) {
	args, err := argumentValues(field, variables)
	if err != nil {
		return readir.Request{}, err
	}
	return b.Query(QueryInput{Operation: readir.FindMany, Model: target, Arguments: args, Selections: selections})
}

// Count compiles the optional where argument on a relation count into the same
// P3 Count request used by generated Go callers.
func (b *Binder) Count(target compilerir.ModelID, field *ast.Field, variables map[string]any) (readir.Request, error) {
	args, err := argumentValues(field, variables)
	if err != nil {
		return readir.Request{}, err
	}
	return b.Query(QueryInput{Operation: readir.Count, Model: target, Arguments: args})
}

func argumentValues(field *ast.Field, variables map[string]any) (map[string]any, error) {
	values := make(map[string]any, len(field.Arguments))
	for _, argument := range field.Arguments {
		value, err := argument.Value.Value(variables)
		if err != nil {
			return nil, compileerr.Invariantf("argument %s.%s value: %w", field.Name, argument.Name, err)
		}
		values[argument.Name] = value
	}
	return values, nil
}

func (b *Binder) model(id compilerir.ModelID) (compilerir.ModelDeclIR, compilerir.ModelContractIR, error) {
	model, ok := b.models[id]
	contract, contractOK := b.contracts[id]
	if !ok || !contractOK || !contract.Exposed {
		return compilerir.ModelDeclIR{}, compilerir.ModelContractIR{}, compileerr.Invariantf("model %s is absent or not exposed", id)
	}
	return model, contract, nil
}

func providerSet(values []compilerir.Provider) (policyir.ProviderSet, error) {
	providers := make([]policyir.Provider, len(values))
	for index, value := range values {
		switch value {
		case compilerir.SQLite:
			providers[index] = policyir.ProviderSQLite
		case compilerir.PostgreSQL:
			providers[index] = policyir.ProviderPostgreSQL
		default:
			return 0, compileerr.Invariantf("unsupported provider %q", value)
		}
	}
	return policyir.NewProviderSet(providers...)
}

func fixedID(value string) ([16]byte, error) {
	var result [16]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) || hex.EncodeToString(decoded) != value {
		return result, compileerr.Invariantf("%q is not a canonical 128-bit identity", value)
	}
	copy(result[:], decoded)
	return result, nil
}

func policyModelID(value compilerir.ModelID) (policyir.ModelID, error) {
	parsed, err := fixedID(string(value))
	return policyir.ModelID(parsed), err
}
func policyFieldID(value compilerir.FieldID) (policyir.FieldID, error) {
	parsed, err := fixedID(string(value))
	return policyir.FieldID(parsed), err
}
func golemKeyID(value compilerir.KeyID) (golem.KeyID, error) {
	parsed, err := fixedID(string(value))
	return golem.KeyID(parsed), err
}

func exactInt(value any) (int, error) {
	switch value := value.(type) {
	case nil:
		return 0, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone)
	case int:
		return value, nil
	case int32:
		return int(value), nil
	case int64:
		converted := int(value)
		if int64(converted) != value {
			return 0, compileerr.New(compileerr.KindIntegerRange, compileerr.IntegerPlatform)
		}
		return converted, nil
	default:
		return 0, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongKind, compileerr.Type("Int"))
	}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func object(value any, path string) (map[string]any, error) {
	if typed, ok := value.(map[string]any); ok {
		return typed, nil
	}
	return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape, compileerr.Path(path))
}

func list(value any, path string) ([]any, error) {
	if typed, ok := value.([]any); ok {
		return typed, nil
	}
	return nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape, compileerr.Path(path))
}

func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinPath(parent, child string) string {
	return compileerr.Join(parent, child)
}

func inputNodesExceeded(maximum int) error {
	return compileerr.New(compileerr.KindInputLimit, compileerr.DetailNone, compileerr.Limit("MaxInputNodes", maximum))
}

func validateCondition(condition policyir.Condition, providers policyir.ProviderSet, path string) (policyir.Condition, error) {
	if err := operator.ValidateCondition(condition, providers); err != nil {
		return policyir.Condition{}, unsupportedOperator(path)
	}
	return condition, nil
}
