package mutation

import (
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

// MapRootInput is a generated GraphQL root after ordinary input coercion but
// before selector/filter maps cross into P4. Data inputs already carry stable
// field/relation identities; Where is the GraphQL selector or predicate map.
type MapRootInput struct {
	Operation RootOperation
	Model     compilerir.ModelID
	Where     any
	Data      *Input
	Create    *Input
	Update    *Input
	Selection []readir.Selection
}

type MapBinder struct {
	query     *graphqlbind.Binder
	limits    Limits
	models    map[compilerir.ModelID]compilerir.ModelDeclIR
	contracts map[compilerir.ModelID]compilerir.ModelContractIR
	relations map[compilerir.RelationID]compilerir.RelationIR
}

func NewMapBinder(compilation compilerir.CompilationIR, limits Limits) (*MapBinder, error) {
	query, err := graphqlbind.New(compilation, graphqlbind.Limits{MaxInputDepth: limits.MaxInputDepth, MaxInputNodes: limits.MaxInputNodes, MaxListItems: limits.MaxListItems})
	if err != nil {
		return nil, err
	}
	binder := &MapBinder{
		query: query, limits: normalizeLimits(limits),
		models: map[compilerir.ModelID]compilerir.ModelDeclIR{}, contracts: map[compilerir.ModelID]compilerir.ModelContractIR{}, relations: map[compilerir.RelationID]compilerir.RelationIR{},
	}
	for _, model := range compilation.Model.Models {
		binder.models[model.ID] = model
	}
	for _, contract := range compilation.Contract.Models {
		binder.contracts[contract.ModelID] = contract
	}
	for _, relation := range compilation.Model.Relations {
		binder.relations[relation.ID] = relation
	}
	return binder, nil
}

func (binder *MapBinder) Lower(root MapRootInput) (Request, error) {
	if binder == nil || binder.query == nil {
		return Request{}, fmt.Errorf("P5_MUTATION_MAP: binder is absent")
	}
	model, err := golemModelID(root.Model)
	if err != nil {
		return Request{}, err
	}
	lowered := RootInput{Operation: root.Operation, Model: model, Data: root.Data, Create: root.Create, Update: root.Update, Selection: append([]readir.Selection(nil), root.Selection...)}
	switch root.Operation {
	case Create:
		if root.Where != nil {
			return Request{}, fmt.Errorf("P5_MUTATION_MAP: create does not accept where")
		}
	case Update, Upsert, Delete:
		selector, predicate, bindErr := binder.query.MutationSelector(root.Model, root.Where)
		if bindErr != nil {
			return Request{}, bindErr
		}
		frozen, freezeErr := binder.query.FreezePredicate(predicate)
		if freezeErr != nil {
			return Request{}, freezeErr
		}
		fields := selector.Fields()
		publicFields := make([]golem.FieldID, len(fields))
		for index, field := range fields {
			publicFields[index] = golem.FieldID(field)
		}
		target, targetErr := golem.RuntimeMutationTargetFromPredicate(model, selector.KeyID(), publicFields, frozen)
		if targetErr != nil {
			return Request{}, fmt.Errorf("P5_MUTATION_TARGET: %w", targetErr)
		}
		lowered.Target = &target
	case UpdateMany, DeleteMany:
		condition, bindErr := binder.query.MutationWhere(root.Model, root.Where)
		if bindErr != nil {
			return Request{}, bindErr
		}
		frozen, freezeErr := binder.query.FreezePredicate(condition)
		if freezeErr != nil {
			return Request{}, freezeErr
		}
		lowered.Where = &frozen
	default:
		return Request{}, fmt.Errorf("P5_MUTATION_MAP: unsupported root operation %d", root.Operation)
	}
	return Lower(lowered, binder.limits)
}

func fixedID(value string) ([16]byte, error) {
	var result [16]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) || hex.EncodeToString(decoded) != value {
		return result, fmt.Errorf("P5_MUTATION_ID: %q is not a canonical 128-bit identity", value)
	}
	copy(result[:], decoded)
	return result, nil
}

func golemModelID(value compilerir.ModelID) (golem.ModelID, error) {
	parsed, err := fixedID(string(value))
	return golem.ModelID(parsed), err
}
