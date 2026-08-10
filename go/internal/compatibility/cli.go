package compatibility

import (
	"encoding"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strings"
)

var (
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

const CLIInventoryFormatVersion uint16 = 1

type CLIInventory struct {
	FormatVersion uint16        `json:"formatVersion"`
	Documents     []CLIDocument `json:"documents"`
}

type CLIDocument struct {
	Name          string     `json:"name"`
	FormatVersion uint16     `json:"formatVersion"`
	Fields        []CLIField `json:"fields"`
}

type CLIField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type CLISource struct {
	Name          string
	FormatVersion uint16
	Value         any
}

// BuildCLIInventory derives the actual JSON shape from command output structs;
// it does not rely on a manually repeated field list. Nested structs are
// structural and recursive references are closed package/type identities.
func BuildCLIInventory(sources []CLISource) (CLIInventory, error) {
	result := CLIInventory{FormatVersion: CLIInventoryFormatVersion, Documents: make([]CLIDocument, len(sources))}
	for index, source := range sources {
		if source.Name == "" || source.FormatVersion == 0 || source.Value == nil {
			return CLIInventory{}, fail(ReasonInvalidManifest)
		}
		value := reflect.TypeOf(source.Value)
		for value.Kind() == reflect.Pointer {
			value = value.Elem()
		}
		if value.Kind() != reflect.Struct {
			return CLIInventory{}, fail(ReasonInvalidManifest)
		}
		document := CLIDocument{Name: source.Name, FormatVersion: source.FormatVersion, Fields: []CLIField{}}
		for fieldIndex := 0; fieldIndex < value.NumField(); fieldIndex++ {
			field := value.Field(fieldIndex)
			if !field.IsExported() {
				continue
			}
			name, required, present := jsonField(field)
			if !present {
				continue
			}
			typeName, ok := jsonType(field.Type, make(map[reflect.Type]bool))
			if !ok {
				return CLIInventory{}, fail(ReasonInvalidManifest)
			}
			document.Fields = append(document.Fields, CLIField{Name: name, Type: typeName, Required: required})
		}
		sort.Slice(document.Fields, func(i, j int) bool { return document.Fields[i].Name < document.Fields[j].Name })
		result.Documents[index] = document
	}
	sort.Slice(result.Documents, func(i, j int) bool { return result.Documents[i].Name < result.Documents[j].Name })
	if !canonicalCLI(result) {
		return CLIInventory{}, fail(ReasonInvalidManifest)
	}
	return result, nil
}

func jsonField(field reflect.StructField) (string, bool, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, false
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = field.Name
	}
	required := true
	for _, option := range parts[1:] {
		if option == "omitempty" || option == "omitzero" {
			required = false
		}
	}
	return name, required, true
}

func jsonType(value reflect.Type, stack map[reflect.Type]bool) (string, bool) {
	if value == reflect.TypeOf(json.RawMessage{}) {
		return "raw-json", true
	}
	if value.Implements(jsonMarshalerType) || reflect.PointerTo(value).Implements(jsonMarshalerType) {
		return "custom-json(" + value.PkgPath() + "." + value.Name() + ")", true
	}
	if value.Implements(textMarshalerType) || reflect.PointerTo(value).Implements(textMarshalerType) {
		return "text(" + value.PkgPath() + "." + value.Name() + ")", true
	}
	if stack[value] {
		if value.Name() == "" || value.PkgPath() == "" {
			return "", false
		}
		return "ref(" + value.PkgPath() + "." + value.Name() + ")", true
	}
	switch value.Kind() {
	case reflect.Pointer:
		child, ok := jsonType(value.Elem(), stack)
		return "nullable(" + child + ")", ok
	case reflect.Slice, reflect.Array:
		child, ok := jsonType(value.Elem(), stack)
		return "array(" + child + ")", ok
	case reflect.Map:
		key, keyOK := jsonType(value.Key(), stack)
		child, childOK := jsonType(value.Elem(), stack)
		return "map(" + key + "," + child + ")", keyOK && childOK
	case reflect.Struct:
		stack[value] = true
		fields := make([]string, 0, value.NumField())
		for index := 0; index < value.NumField(); index++ {
			field := value.Field(index)
			if !field.IsExported() {
				continue
			}
			name, required, present := jsonField(field)
			if !present {
				continue
			}
			child, ok := jsonType(field.Type, stack)
			if !ok {
				delete(stack, value)
				return "", false
			}
			fields = append(fields, name+":"+child+":"+requiredIdentity(required))
		}
		delete(stack, value)
		sort.Strings(fields)
		return "object{" + strings.Join(fields, ";") + "}", true
	case reflect.Interface:
		return "any", true
	case reflect.Bool:
		return "bool", true
	case reflect.String:
		return "string", true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Kind().String(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Kind().String(), true
	case reflect.Float32, reflect.Float64:
		return value.Kind().String(), true
	default:
		return "", false
	}
}

func requiredIdentity(value bool) string {
	if value {
		return "required"
	}
	return "optional"
}

func EncodeCLIInventory(value CLIInventory) ([]byte, error) {
	if !canonicalCLI(value) {
		return nil, fail(ReasonInvalidManifest)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fail(ReasonInvalidEncoding)
	}
	return append(encoded, '\n'), nil
}

func ParseCLIInventory(encoded []byte) (CLIInventory, error) {
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	var value CLIInventory
	if err := decoder.Decode(&value); err != nil {
		return CLIInventory{}, fail(ReasonInvalidEncoding)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return CLIInventory{}, fail(ReasonInvalidEncoding)
	}
	canonical, err := EncodeCLIInventory(value)
	if err != nil {
		return CLIInventory{}, err
	}
	if string(canonical) != string(encoded) {
		return CLIInventory{}, fail(ReasonNoncanonical)
	}
	return value, nil
}

func CompareCLI(previous, current CLIInventory) LayerChange {
	if !canonicalCLI(previous) || !canonicalCLI(current) {
		return LayerBreaking
	}
	change := LayerUnchanged
	documents := make(map[string]CLIDocument, len(current.Documents))
	for _, document := range current.Documents {
		documents[document.Name] = document
	}
	seen := make(map[string]bool, len(previous.Documents))
	for _, before := range previous.Documents {
		seen[before.Name] = true
		after, exists := documents[before.Name]
		if !exists || before.FormatVersion != after.FormatVersion {
			return LayerBreaking
		}
		fields := make(map[string]CLIField, len(after.Fields))
		for _, field := range after.Fields {
			fields[field.Name] = field
		}
		beforeFields := make(map[string]bool, len(before.Fields))
		for _, field := range before.Fields {
			beforeFields[field.Name] = true
			currentField, exists := fields[field.Name]
			if !exists || currentField.Type != field.Type || currentField.Required != field.Required {
				return LayerBreaking
			}
		}
		for _, field := range after.Fields {
			if !beforeFields[field.Name] {
				change = LayerAdditive
			}
		}
	}
	for _, document := range current.Documents {
		if !seen[document.Name] {
			change = LayerAdditive
		}
	}
	return change
}

func canonicalCLI(value CLIInventory) bool {
	if value.FormatVersion != CLIInventoryFormatVersion || len(value.Documents) == 0 {
		return false
	}
	for index, document := range value.Documents {
		if document.Name == "" || document.FormatVersion == 0 || index > 0 && value.Documents[index-1].Name >= document.Name || len(document.Fields) == 0 {
			return false
		}
		for fieldIndex, field := range document.Fields {
			if field.Name == "" || field.Type == "" || fieldIndex > 0 && document.Fields[fieldIndex-1].Name >= field.Name {
				return false
			}
		}
	}
	return true
}
