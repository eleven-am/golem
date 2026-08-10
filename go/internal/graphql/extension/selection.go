package extension

import (
	"encoding/json"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

// DependencyAccessKind is the closed set of public-row cells a generated
// computed resolver may receive. Both forms are populated only after the
// ordinary P3 read path has applied field policy and conditional masks.
type DependencyAccessKind uint8

const (
	DependencyMaskedScalar DependencyAccessKind = iota + 1
	DependencyMaskedRelation
)

// DependencyAccess addresses one dependency in the masked public row. A
// relation dependency uses a non-response occurrence so caller-authored
// aliases, filters, and pagination cannot shape the resolver's input.
type DependencyAccess struct {
	Kind       DependencyAccessKind
	FieldID    compilerir.FieldID
	RelationID compilerir.RelationID
	Occurrence readir.OccurrenceID
}

// BoundArgument retains both the declared closed GraphQL type and its exact
// coerced value. Canonical is the deterministic wire representation used in a
// batched-computed loader key; it is never derived by reflecting application
// values at dispatch time.
type BoundArgument struct {
	Name      string
	Type      compilerir.GraphQLTypeIR
	Value     any
	Canonical json.RawMessage
}

// ComputedSelection is the execution-free binding produced for one selected
// computed-field occurrence. Projection/Children describe model-valued
// results; Dependencies describe only masked public parent-row access.
type ComputedSelection struct {
	ExtensionID        compilerir.ExtensionID
	Result             compilerir.GraphQLTypeIR
	Arguments          []BoundArgument
	CanonicalArguments string
	Dependencies       []DependencyAccess
	Batch              *compilerir.ComputedBatchContractIR
	Projection         []readir.Selection
}

func CloneComputedSelection(value *ComputedSelection) *ComputedSelection {
	if value == nil {
		return nil
	}
	result := *value
	result.Result = cloneGraphQLType(value.Result)
	result.Arguments = make([]BoundArgument, len(value.Arguments))
	for index, argument := range value.Arguments {
		result.Arguments[index] = argument
		result.Arguments[index].Type = cloneGraphQLType(argument.Type)
		result.Arguments[index].Canonical = append(json.RawMessage(nil), argument.Canonical...)
		result.Arguments[index].Value = cloneBoundValue(argument.Value)
	}
	result.Dependencies = append([]DependencyAccess(nil), value.Dependencies...)
	result.Projection = append([]readir.Selection(nil), value.Projection...)
	if value.Batch != nil {
		batch := *value.Batch
		if value.Batch.CacheKey != nil {
			codec := *value.Batch.CacheKey
			batch.CacheKey = &codec
		}
		result.Batch = &batch
	}
	return &result
}

func cloneGraphQLType(value compilerir.GraphQLTypeIR) compilerir.GraphQLTypeIR {
	if value.Element != nil {
		element := cloneGraphQLType(*value.Element)
		value.Element = &element
	}
	return value
}

func cloneBoundValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return append([]byte(nil), typed...)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneBoundValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneBoundValue(item)
		}
		return result
	default:
		return value
	}
}
