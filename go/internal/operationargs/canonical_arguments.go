package operationargs

import (
	"encoding/binary"
	"errors"
	"math"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	MaxCanonicalArgumentBytes = 128 * 1024
	MaxCanonicalArgumentNodes = 16 * 1024
	MaxCanonicalArgumentDepth = 32
)

var (
	ErrCanonicalArgumentsInvalid     = errors.New("custom canonical arguments require an owned, exact typed request")
	ErrCanonicalArgumentsUnsupported = errors.New("custom argument type has no canonical continuation representation")
	ErrCanonicalArgumentsLimit       = errors.New("custom canonical argument work exceeds its bound")
)

// PrepareCanonical bounds the complete declared input and supplied value shape
// before Prepare validates or clones it. Callers must supply the same exact
// coerced scalar representations accepted by Prepare. No value hooks, JSON
// marshaling, or arbitrary Go reflection run during the bounded preflight.
// Unlike CanonicalArguments, this preflight includes every argument, including
// a continuation that the caller will subsequently exclude from its identity.
func (registry *Registry) PrepareCanonical(kind compilerir.CustomOperationKind, name string, supplied map[string]any) (Request, error) {
	if registry == nil {
		return Request{}, ErrCanonicalArgumentsInvalid
	}
	if len(name) > MaxCanonicalArgumentBytes || len(kind) > MaxCanonicalArgumentBytes || len(supplied) > MaxCanonicalArgumentNodes {
		return Request{}, ErrCanonicalArgumentsLimit
	}
	operation, found := registry.byRoot[rootKey{kind: kind, name: name}]
	if !found {
		return Request{}, ErrCanonicalArgumentsInvalid
	}
	if len(operation.Arguments) > MaxCanonicalArgumentNodes {
		return Request{}, ErrCanonicalArgumentsLimit
	}
	if len(supplied) > len(operation.Arguments) {
		return Request{}, ErrCanonicalArgumentsInvalid
	}
	// This is a borrowed view, never returned. CanonicalArguments bounds and
	// checks the closed schema before visiting any of these unowned values.
	request := Request{owner: registry, operation: operation, arguments: make([]Argument, len(operation.Arguments))}
	seen := 0
	for index, declaration := range operation.Arguments {
		if len(declaration.Name) > MaxCanonicalArgumentBytes {
			return Request{}, ErrCanonicalArgumentsLimit
		}
		value, present := supplied[declaration.Name]
		if present {
			seen++
		}
		request.arguments[index] = Argument{name: declaration.Name, value: value, present: present}
	}
	if seen != len(supplied) {
		return Request{}, ErrCanonicalArgumentsInvalid
	}
	if _, err := registry.CanonicalArguments(request); err != nil {
		return Request{}, err
	}
	return registry.prepareOperation(operation, supplied)
}

// CanonicalArguments encodes the closed, prepared input contract, with explicit
// omitted/null/value states. Excluded names must be declared arguments; the page
// owner may exclude its continuation while including effective take. These
// bytes carry no actor identity and do not activate pagination or authorize data.
// JSON and executable model-derived input wrappers deliberately have no encoding.
func (registry *Registry) CanonicalArguments(request Request, excluded ...string) ([]byte, error) {
	if registry == nil || request.owner != registry {
		return nil, ErrCanonicalArgumentsInvalid
	}
	contract, found := registry.byID[request.operation.ExtensionID]
	if !found || contract.Name != request.operation.Name || contract.Operation != request.operation.Operation || contract.Resolver != request.operation.Resolver || len(contract.Arguments) != len(request.arguments) {
		return nil, ErrCanonicalArgumentsInvalid
	}
	writer := canonicalArgumentWriter{registry: registry}
	if err := writer.step(len(contract.Arguments)+len(excluded), 0); err != nil {
		return nil, err
	}
	if len(excluded) > len(contract.Arguments) {
		return nil, ErrCanonicalArgumentsInvalid
	}
	declared := make(map[string]bool, len(contract.Arguments))
	for _, argument := range contract.Arguments {
		if len(argument.Name) > MaxCanonicalArgumentBytes {
			return nil, ErrCanonicalArgumentsLimit
		}
		declared[argument.Name] = true
	}
	skip := make(map[string]bool, len(excluded))
	for _, name := range excluded {
		if len(name) > MaxCanonicalArgumentBytes {
			return nil, ErrCanonicalArgumentsLimit
		}
		if !declared[name] || skip[name] {
			return nil, ErrCanonicalArgumentsInvalid
		}
		skip[name] = true
	}
	if err := writer.text("golem/custom-arguments/1"); err != nil {
		return nil, err
	}
	if err := writer.number(uint64(len(contract.Arguments) - len(skip))); err != nil {
		return nil, err
	}
	for index, declaration := range contract.Arguments {
		argument := request.arguments[index]
		if argument.name != declaration.Name {
			return nil, ErrCanonicalArgumentsInvalid
		}
		if skip[declaration.Name] {
			continue
		}
		if err := writer.text(declaration.Name); err != nil {
			return nil, err
		}
		if err := writer.schemaType(declaration.Type, 1); err != nil {
			return nil, err
		}
	}
	// Validate every included declaration before visiting any input value: an
	// omitted unsupported later argument cannot hide behind earlier input work.
	for index, declaration := range contract.Arguments {
		if skip[declaration.Name] {
			continue
		}
		argument := request.arguments[index]
		if err := writer.value(declaration.Type, argument.value, argument.present, 1); err != nil {
			return nil, err
		}
	}
	return writer.bytes, nil
}

type canonicalArgumentWriter struct {
	registry *Registry
	bytes    []byte
	nodes    int
}

func (writer *canonicalArgumentWriter) step(count, depth int) error {
	if depth > MaxCanonicalArgumentDepth || count < 0 || count > MaxCanonicalArgumentNodes-writer.nodes {
		return ErrCanonicalArgumentsLimit
	}
	writer.nodes += count
	return nil
}
func (writer *canonicalArgumentWriter) reserve(size int) error {
	if size < 0 || size > MaxCanonicalArgumentBytes-len(writer.bytes) {
		return ErrCanonicalArgumentsLimit
	}
	return nil
}
func (writer *canonicalArgumentWriter) number(value uint64) error {
	if err := writer.reserve(8); err != nil {
		return err
	}
	writer.bytes = binary.BigEndian.AppendUint64(writer.bytes, value)
	return nil
}
func (writer *canonicalArgumentWriter) text(value string) error {
	if len(value) > MaxCanonicalArgumentBytes-8 {
		return ErrCanonicalArgumentsLimit
	}
	if err := writer.reserve(8 + len(value)); err != nil {
		return err
	}
	writer.bytes = binary.BigEndian.AppendUint64(writer.bytes, uint64(len(value)))
	writer.bytes = append(writer.bytes, value...)
	return nil
}
func (writer *canonicalArgumentWriter) data(value []byte) error {
	if len(value) > MaxCanonicalArgumentBytes-8 {
		return ErrCanonicalArgumentsLimit
	}
	if err := writer.reserve(8 + len(value)); err != nil {
		return err
	}
	writer.bytes = binary.BigEndian.AppendUint64(writer.bytes, uint64(len(value)))
	writer.bytes = append(writer.bytes, value...)
	return nil
}

// Schema traversal precedes value traversal, including omitted/null inputs and
// empty lists: an unsupported executable wrapper cannot hide in an empty value.
func (writer *canonicalArgumentWriter) schemaType(typ compilerir.GraphQLTypeIR, depth int) error {
	if err := writer.step(1, depth); err != nil {
		return err
	}
	if err := writer.text(string(typ.Kind)); err != nil {
		return err
	}
	if err := writer.text(typ.Name); err != nil {
		return err
	}
	nullable := uint64(0)
	if typ.Nullable {
		nullable = 1
	}
	if err := writer.number(nullable); err != nil {
		return err
	}
	switch typ.Kind {
	case compilerir.GraphQLTypeScalar:
		switch typ.Name {
		case "Boolean", "Int", "BigInt", "Float", "String", "Decimal", "UUID", "Date", "Time", "DateTime", "Bytes":
			return nil
		default:
			return ErrCanonicalArgumentsUnsupported
		}
	case compilerir.GraphQLTypeEnum:
		if _, exists := writer.registry.enums[typ.Name]; !exists {
			return ErrCanonicalArgumentsInvalid
		}
		return nil
	case compilerir.GraphQLTypeList:
		if typ.Element == nil {
			return ErrCanonicalArgumentsInvalid
		}
		return writer.schemaType(*typ.Element, depth+1)
	case compilerir.GraphQLTypeInputObject:
		definition, exists := writer.registry.definitions[typ.Name]
		if !exists || definition.Kind != typ.Kind {
			return ErrCanonicalArgumentsInvalid
		}
		if err := writer.step(len(definition.Fields), depth); err != nil {
			return err
		}
		if err := writer.number(uint64(len(definition.Fields))); err != nil {
			return err
		}
		for _, field := range definition.Fields {
			if err := writer.text(field.Name); err != nil {
				return err
			}
			if err := writer.schemaType(field.Type, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return ErrCanonicalArgumentsUnsupported
	}
}

func (writer *canonicalArgumentWriter) value(typ compilerir.GraphQLTypeIR, value any, present bool, depth int) error {
	if err := writer.step(1, depth); err != nil {
		return err
	}
	if !present {
		if !typ.Nullable {
			return ErrCanonicalArgumentsInvalid
		}
		return writer.number(0)
	}
	if value == nil {
		if !typ.Nullable {
			return ErrCanonicalArgumentsInvalid
		}
		return writer.number(1)
	}
	if err := writer.number(2); err != nil {
		return err
	}
	switch typ.Kind {
	case compilerir.GraphQLTypeList:
		items, ok := value.([]any)
		if !ok || typ.Element == nil {
			return ErrCanonicalArgumentsInvalid
		}
		if err := writer.step(len(items), depth); err != nil {
			return err
		}
		if err := writer.number(uint64(len(items))); err != nil {
			return err
		}
		for _, item := range items {
			if err := writer.value(*typ.Element, item, true, depth+1); err != nil {
				return err
			}
		}
		return nil
	case compilerir.GraphQLTypeInputObject:
		fields, ok := value.(map[string]any)
		definition, exists := writer.registry.definitions[typ.Name]
		if !ok || !exists || len(fields) > len(definition.Fields) {
			return ErrCanonicalArgumentsInvalid
		}
		if err := writer.step(len(definition.Fields), depth); err != nil {
			return err
		}
		seen := 0
		for _, field := range definition.Fields {
			item, present := fields[field.Name]
			if present {
				seen++
			}
			if err := writer.value(field.Type, item, present, depth+1); err != nil {
				return err
			}
		}
		if seen != len(fields) {
			return ErrCanonicalArgumentsInvalid
		}
		return nil
	case compilerir.GraphQLTypeEnum:
		wire, ok := value.(string)
		if len(wire) > MaxCanonicalArgumentBytes {
			return ErrCanonicalArgumentsLimit
		}
		if !ok || !writer.registry.enums[typ.Name].wire[wire] {
			return ErrCanonicalArgumentsInvalid
		}
		return writer.text(wire)
	case compilerir.GraphQLTypeScalar:
		return writer.scalar(typ.Name, value)
	default:
		return ErrCanonicalArgumentsUnsupported
	}
}

func (writer *canonicalArgumentWriter) scalar(name string, value any) error {
	switch name {
	case "Boolean":
		if value, ok := value.(bool); ok {
			if value {
				return writer.number(1)
			}
			return writer.number(0)
		}
	case "Int":
		if value, ok := value.(int32); ok {
			return writer.number(uint64(int64(value)))
		}
	case "BigInt":
		if value, ok := value.(int64); ok {
			return writer.number(uint64(value))
		}
	case "Float":
		if value, ok := value.(float64); ok && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return writer.number(math.Float64bits(value))
		}
	case "String":
		if value, ok := value.(string); ok {
			if len(value) > MaxCanonicalArgumentBytes {
				return ErrCanonicalArgumentsLimit
			}
			if utf8.ValidString(value) {
				return writer.text(value)
			}
		}
	case "Bytes":
		if value, ok := value.([]byte); ok {
			return writer.data(value)
		}
	case "Decimal":
		if value, ok := value.(golem.Decimal); ok {
			normalized, err := golem.NewDecimal(value.Coefficient(), value.Scale())
			if err != nil || normalized != value {
				return ErrCanonicalArgumentsInvalid
			}
			if err := writer.number(uint64(value.Coefficient())); err != nil {
				return err
			}
			return writer.number(uint64(value.Scale()))
		}
	case "UUID":
		if value, ok := value.(golem.UUID); ok {
			return writer.data(value[:])
		}
	case "Date":
		if value, ok := value.(golem.Date); ok {
			if _, err := golem.NewDate(value.Year(), value.Month(), value.Day()); err != nil {
				return ErrCanonicalArgumentsInvalid
			}
			return writer.text(value.String())
		}
	case "Time":
		if value, ok := value.(golem.Time); ok && value.Microseconds() >= 0 && value.Microseconds() < 86400*1000000 {
			return writer.number(uint64(value.Microseconds()))
		}
	case "DateTime":
		if value, ok := value.(time.Time); ok {
			value = value.UTC()
			if value.Year() < 1 || value.Year() > 9999 || value.Nanosecond()%1000 != 0 {
				return ErrCanonicalArgumentsInvalid
			}
			if err := writer.number(uint64(value.Unix())); err != nil {
				return err
			}
			return writer.number(uint64(value.Nanosecond() / 1000))
		}
	default:
		return ErrCanonicalArgumentsUnsupported
	}
	return ErrCanonicalArgumentsInvalid
}
