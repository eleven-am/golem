package graphql

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
)

type executableData []byte

func (data executableData) MarshalJSON() ([]byte, error) { return data, nil }

func (data executableData) decode() (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func canonicalExecutableData(raw []byte) (executableData, error) {
	if !json.Valid(raw) {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		return nil, errors.New("invalid JSON")
	}
	output := make([]byte, 0, len(raw)+len(raw)/32)
	for index := 0; index < len(raw); {
		switch character := raw[index]; character {
		case ' ', '\t', '\n', '\r':
			index++
		case '"':
			end, verbatim := jsonStringEnd(raw, index)
			if verbatim {
				output = append(output, raw[index:end]...)
			} else {
				var value string
				if err := json.Unmarshal(raw[index:end], &value); err != nil {
					return nil, err
				}
				var err error
				if output, err = appendJSONString(output, value); err != nil {
					return nil, err
				}
			}
			index = end
		default:
			output = append(output, character)
			index++
		}
	}
	return output, nil
}

func jsonStringEnd(raw []byte, start int) (int, bool) {
	verbatim := true
	for index := start + 1; index < len(raw); index++ {
		switch character := raw[index]; {
		case character == '"':
			return index + 1, verbatim
		case character == '\\':
			verbatim = false
			index++
		case character < 0x20, character >= 0x80, character == '<', character == '>', character == '&':
			verbatim = false
		}
	}
	return len(raw), false
}

func appendJSONString(output []byte, value string) ([]byte, error) {
	for index := 0; index < len(value); index++ {
		switch character := value[index]; {
		case character < 0x20, character >= 0x80, character == '"', character == '\\', character == '<', character == '>', character == '&':
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			return append(output, encoded...), nil
		}
	}
	output = append(output, '"')
	output = append(output, value...)
	return append(output, '"'), nil
}

var responseBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

const maxPooledResponseBuffer = 4 << 20

func encodeResponse(response Response) ([]byte, error) {
	var buffer bytes.Buffer
	if err := encodeResponseInto(&buffer, response); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func encodeResponseInto(buffer *bytes.Buffer, response Response) error {
	switch value := response.Data.(type) {
	case executableData:
		buffer.Grow(len(value) + 64)
		buffer.WriteString(`{"data":`)
		buffer.Write(value)
	case selectionOrderedData:
		buffer.WriteString(`{"data":`)
		if err := value.encodeInto(buffer); err != nil {
			return err
		}
	default:
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		buffer.Write(encoded)
		return nil
	}
	if len(response.Errors) != 0 {
		failures, err := json.Marshal(response.Errors)
		if err != nil {
			return err
		}
		buffer.WriteString(`,"errors":`)
		buffer.Write(failures)
	}
	buffer.WriteByte('}')
	return nil
}

type selectionOrderedData struct {
	value     any
	schema    *ast.Schema
	operation Operation
}

func (server *Server[P]) selectionOrdered(response Response, operation Operation) Response {
	if response.Data == nil || operation.Document == nil || operation.Definition == nil {
		return response
	}
	response.Data = selectionOrderedData{value: response.Data, schema: server.schema, operation: operation}
	return response
}

func decodedResponse(response Response) Response {
	switch data := response.Data.(type) {
	case executableData:
		value, err := data.decode()
		if err != nil {
			return Response{Errors: []Error{publicError("INTERNAL_SERVER_ERROR", "internal server error")}}
		}
		response.Data = value
	case selectionOrderedData:
		response.Data = data.value
	}
	return response
}

func (data selectionOrderedData) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	if err := data.encodeInto(&buffer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (data selectionOrderedData) encodeInto(output *bytes.Buffer) error {
	writer := selectionWriter{schema: data.schema, fragments: data.operation.Document.Fragments, variables: data.operation.Variables, output: output, encoder: json.NewEncoder(output), plans: map[selectionPlanKey][]collectedField{}}
	var root *ast.Definition
	if data.schema != nil {
		switch data.operation.Definition.Operation {
		case ast.Mutation:
			root = data.schema.Mutation
		case ast.Subscription:
			root = data.schema.Subscription
		default:
			root = data.schema.Query
		}
	}
	return writer.value(data.value, []ast.SelectionSet{data.operation.Definition.SelectionSet}, root)
}

type selectionWriter struct {
	schema    *ast.Schema
	fragments ast.FragmentDefinitionList
	variables map[string]any
	output    *bytes.Buffer
	encoder   *json.Encoder
	plans     map[selectionPlanKey][]collectedField
}

type selectionPlanKey struct {
	sets     *ast.SelectionSet
	count    int
	parent   *ast.Definition
	typename string
}

type collectedField struct {
	name       string
	definition *ast.Definition
	sets       []ast.SelectionSet
	typename   bool
}

func (writer *selectionWriter) value(value any, sets []ast.SelectionSet, parent *ast.Definition) error {
	if !hasSelections(sets) {
		return writer.scalar(value)
	}
	switch value := value.(type) {
	case map[string]any:
		return writer.object(value, sets, parent)
	case PreparedObject:
		return writer.object(value, sets, parent)
	case []any:
		return writer.list(len(value), func(index int) any { return value[index] }, sets, parent)
	case []map[string]any:
		return writer.list(len(value), func(index int) any { return value[index] }, sets, parent)
	case []PreparedObject:
		return writer.list(len(value), func(index int) any { return value[index] }, sets, parent)
	default:
		return writer.scalar(value)
	}
}

func (writer *selectionWriter) list(length int, item func(int) any, sets []ast.SelectionSet, parent *ast.Definition) error {
	writer.output.WriteByte('[')
	for index := range length {
		if index != 0 {
			writer.output.WriteByte(',')
		}
		if err := writer.value(item(index), sets, parent); err != nil {
			return err
		}
	}
	writer.output.WriteByte(']')
	return nil
}

func (writer *selectionWriter) object(value map[string]any, sets []ast.SelectionSet, parent *ast.Definition) error {
	if value == nil {
		writer.output.WriteString("null")
		return nil
	}
	typename := ""
	if parent != nil && parent.IsAbstractType() {
		typename = writer.typename(value, sets, parent)
	}
	fields := writer.collect(sets, parent, typename)
	writer.output.WriteByte('{')
	written := 0
	for _, field := range fields {
		child, present := value[field.name]
		if !present {
			continue
		}
		if written != 0 {
			writer.output.WriteByte(',')
		}
		written++
		if err := writer.key(field.name); err != nil {
			return err
		}
		if err := writer.value(child, field.sets, field.definition); err != nil {
			return err
		}
	}
	if written != len(value) {
		selected := make(map[string]struct{}, len(fields))
		for _, field := range fields {
			selected[field.name] = struct{}{}
		}
		remaining := make([]string, 0, len(value)-written)
		for name := range value {
			if _, ok := selected[name]; !ok {
				remaining = append(remaining, name)
			}
		}
		sort.Strings(remaining)
		for _, name := range remaining {
			if written != 0 {
				writer.output.WriteByte(',')
			}
			written++
			if err := writer.key(name); err != nil {
				return err
			}
			if err := writer.scalar(value[name]); err != nil {
				return err
			}
		}
	}
	writer.output.WriteByte('}')
	return nil
}

func (writer *selectionWriter) key(name string) error {
	encoded, err := appendJSONString(writer.output.AvailableBuffer(), name)
	if err != nil {
		return err
	}
	writer.output.Write(encoded)
	writer.output.WriteByte(':')
	return nil
}

func (writer *selectionWriter) scalar(value any) error {
	switch value := value.(type) {
	case nil:
		writer.output.WriteString("null")
		return nil
	case string:
		encoded, err := appendJSONString(writer.output.AvailableBuffer(), value)
		if err != nil {
			return err
		}
		writer.output.Write(encoded)
		return nil
	case bool:
		writer.output.Write(strconv.AppendBool(writer.output.AvailableBuffer(), value))
		return nil
	case int:
		writer.output.Write(strconv.AppendInt(writer.output.AvailableBuffer(), int64(value), 10))
		return nil
	case int32:
		writer.output.Write(strconv.AppendInt(writer.output.AvailableBuffer(), int64(value), 10))
		return nil
	case int64:
		writer.output.Write(strconv.AppendInt(writer.output.AvailableBuffer(), value, 10))
		return nil
	}
	if err := writer.encoder.Encode(value); err != nil {
		return err
	}
	writer.output.Truncate(writer.output.Len() - 1)
	return nil
}

func (writer *selectionWriter) typename(value map[string]any, sets []ast.SelectionSet, parent *ast.Definition) string {
	if writer.schema == nil {
		return ""
	}
	best, bestRank := "", runtimeTypeRank{}
	for index, candidate := range writer.schema.GetPossibleTypes(parent) {
		rank := runtimeTypeRankOf(value, writer.collect(sets, parent, candidate.Name), candidate.Name, index)
		if best == "" || rank.before(bestRank) {
			best, bestRank = candidate.Name, rank
		}
	}
	return best
}

type runtimeTypeRank struct {
	contradicted bool
	incomplete   bool
	present      int
	confirmed    bool
	index        int
}

func runtimeTypeRankOf(value map[string]any, fields []collectedField, typename string, index int) runtimeTypeRank {
	rank := runtimeTypeRank{index: index}
	for _, field := range fields {
		child, present := value[field.name]
		if !present {
			rank.incomplete = true
			continue
		}
		rank.present++
		if !field.typename {
			continue
		}
		if child != typename {
			rank.contradicted = true
			continue
		}
		rank.confirmed = true
	}
	return rank
}

func (rank runtimeTypeRank) before(other runtimeTypeRank) bool {
	switch {
	case rank.contradicted != other.contradicted:
		return !rank.contradicted
	case rank.incomplete != other.incomplete:
		return !rank.incomplete
	case rank.present != other.present:
		return rank.present > other.present
	case rank.confirmed != other.confirmed:
		return rank.confirmed
	default:
		return rank.index < other.index
	}
}

func (writer *selectionWriter) collect(sets []ast.SelectionSet, parent *ast.Definition, typename string) []collectedField {
	key := selectionPlanKey{count: len(sets), parent: parent, typename: typename}
	if len(sets) != 0 {
		key.sets = &sets[0]
	}
	if fields, ok := writer.plans[key]; ok {
		return fields
	}
	fields := writer.collectFields(sets, parent, typename)
	writer.plans[key] = fields
	return fields
}

func (writer *selectionWriter) collectFields(sets []ast.SelectionSet, parent *ast.Definition, typename string) []collectedField {
	fields := make([]collectedField, 0, 8)
	positions := map[string]int{}
	visited := map[string]bool{}
	var visit func(ast.SelectionSet)
	visit = func(set ast.SelectionSet) {
		for _, selection := range set {
			switch selection := selection.(type) {
			case *ast.Field:
				if !graphqlIncluded(selection.Directives, writer.variables) {
					continue
				}
				name := selection.Alias
				if name == "" {
					name = selection.Name
				}
				if position, ok := positions[name]; ok {
					fields[position].sets = append(fields[position].sets, selection.SelectionSet)
					continue
				}
				positions[name] = len(fields)
				fields = append(fields, collectedField{name: name, definition: writer.fieldType(selection), sets: []ast.SelectionSet{selection.SelectionSet}, typename: selection.Name == "__typename"})
			case *ast.InlineFragment:
				if graphqlIncluded(selection.Directives, writer.variables) && writer.applies(selection.TypeCondition, parent, typename) {
					visit(selection.SelectionSet)
				}
			case *ast.FragmentSpread:
				if visited[selection.Name] || !graphqlIncluded(selection.Directives, writer.variables) {
					continue
				}
				visited[selection.Name] = true
				fragment := writer.fragments.ForName(selection.Name)
				if fragment != nil && writer.applies(fragment.TypeCondition, parent, typename) {
					visit(fragment.SelectionSet)
				}
			}
		}
	}
	for _, set := range sets {
		visit(set)
	}
	return fields
}

func (writer *selectionWriter) fieldType(field *ast.Field) *ast.Definition {
	if writer.schema == nil || field.Definition == nil || field.Definition.Type == nil {
		return nil
	}
	return writer.schema.Types[field.Definition.Type.Name()]
}

func (writer *selectionWriter) applies(condition string, parent *ast.Definition, typename string) bool {
	if condition == "" || writer.schema == nil || parent == nil {
		return true
	}
	concrete := parent
	if parent.IsAbstractType() {
		concrete = writer.schema.Types[typename]
		if concrete == nil {
			return false
		}
	}
	if condition == concrete.Name {
		return true
	}
	target := writer.schema.Types[condition]
	if target == nil || !target.IsAbstractType() {
		return false
	}
	for _, possible := range writer.schema.GetPossibleTypes(target) {
		if possible.Name == concrete.Name {
			return true
		}
	}
	return false
}

func hasSelections(sets []ast.SelectionSet) bool {
	for _, set := range sets {
		if len(set) != 0 {
			return true
		}
	}
	return false
}
