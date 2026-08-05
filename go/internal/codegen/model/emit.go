package model

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"go/format"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// Emit produces one deterministic same-package bootstrap file per model
// package. It never writes source to disk.
func Emit(request Request) (Result, error) {
	packages, err := packageMap(request.Packages)
	if err != nil {
		return Result{}, err
	}
	models := append([]ir.ModelDeclIR(nil), request.Compilation.Model.Models...)
	sort.Slice(models, func(i, j int) bool {
		if models[i].Go.PackagePath != models[j].Go.PackagePath {
			return models[i].Go.PackagePath < models[j].Go.PackagePath
		}
		return models[i].ID < models[j].ID
	})
	modelByID := make(map[ir.ModelID]ir.ModelDeclIR, len(models))
	for _, model := range models {
		modelByID[model.ID] = model
		if _, ok := packages[model.Go.PackagePath]; !ok {
			return Result{}, fmt.Errorf("model codegen: missing package specification for %q", model.Go.PackagePath)
		}
	}
	enumByID := make(map[ir.EnumID]ir.EnumIR, len(request.Compilation.Model.Enums))
	for _, enum := range request.Compilation.Model.Enums {
		enumByID[enum.ID] = enum
	}
	relations := make(map[ir.RelationID]ir.RelationIR, len(request.Compilation.Model.Relations))
	for _, relation := range request.Compilation.Model.Relations {
		relations[relation.ID] = relation
	}

	byPackage := make(map[string][]ir.ModelDeclIR)
	for _, model := range models {
		byPackage[model.Go.PackagePath] = append(byPackage[model.Go.PackagePath], model)
	}
	paths := make([]string, 0, len(byPackage))
	for path := range byPackage {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	golemPath := request.GolemImportPath
	if golemPath == "" {
		golemPath = DefaultGolemImportPath
	}
	result := Result{}
	for _, path := range paths {
		file, symbols, emitErr := emitPackage(packages[path], byPackage[path], modelByID, enumByID, relations, contractMap(request.Compilation.Contract), golemPath)
		if emitErr != nil {
			return Result{}, emitErr
		}
		result.Files = append(result.Files, file)
		result.Manifest.Symbols = append(result.Manifest.Symbols, symbols...)
	}
	sort.Slice(result.Manifest.Symbols, func(i, j int) bool {
		a, b := result.Manifest.Symbols[i], result.Manifest.Symbols[j]
		if a.PackagePath != b.PackagePath {
			return a.PackagePath < b.PackagePath
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Kind < b.Kind
	})
	return result, nil
}

func packageMap(specs []PackageSpec) (map[string]PackageSpec, error) {
	result := make(map[string]PackageSpec, len(specs))
	for _, spec := range specs {
		if spec.ImportPath == "" || !token.IsIdentifier(spec.PackageName) {
			return nil, fmt.Errorf("model codegen: invalid package specification %#v", spec)
		}
		if _, exists := result[spec.ImportPath]; exists {
			return nil, fmt.Errorf("model codegen: duplicate package specification for %q", spec.ImportPath)
		}
		result[spec.ImportPath] = spec
	}
	return result, nil
}

func contractMap(contract ir.ContractIR) map[ir.ModelID]ir.ModelContractIR {
	result := make(map[ir.ModelID]ir.ModelContractIR, len(contract.Models))
	for _, model := range contract.Models {
		result[model.ModelID] = model
	}
	return result
}

func emitPackage(spec PackageSpec, models []ir.ModelDeclIR, modelByID map[ir.ModelID]ir.ModelDeclIR, enumByID map[ir.EnumID]ir.EnumIR, relations map[ir.RelationID]ir.RelationIR, contracts map[ir.ModelID]ir.ModelContractIR, golemPath string) (File, []Symbol, error) {
	namespaces := make(map[string]ir.ModelID, len(models))
	for _, model := range models {
		namespace := plural(model.LogicalName)
		if !token.IsIdentifier(namespace) || !token.IsIdentifier(model.Go.Name) {
			return File{}, nil, fmt.Errorf("model codegen: model %s has invalid generated Go identifiers", model.ID)
		}
		if prior, exists := namespaces[namespace]; exists {
			return File{}, nil, fmt.Errorf("model codegen: namespace %q collides for models %s and %s", namespace, prior, model.ID)
		}
		namespaces[namespace] = model.ID
	}

	imports := newImports(spec.ImportPath, golemPath)
	for _, model := range models {
		for _, field := range model.Fields {
			if field.Scalar != nil {
				if _, err := logicalGoType(field.Scalar.Type, enumByID, imports); err != nil {
					return File{}, nil, err
				}
			}
			if field.Relation != nil {
				target, err := relationTarget(field, model.ID, modelByID, relations)
				if err != nil {
					return File{}, nil, err
				}
				imports.qualify(target.Go.PackagePath, target.Go.Name)
			}
		}
	}

	var body bytes.Buffer
	symbols := make([]Symbol, 0)
	for _, model := range models {
		namespace := plural(model.LogicalName)
		typeName := "golemGenerated" + model.Go.Name + "Fields"
		descriptorName := "GolemGenerated" + model.Go.Name + "Descriptor"
		modelLiteral, err := idLiteral("ModelID", string(model.ID))
		if err != nil {
			return File{}, nil, fmt.Errorf("model codegen: model %s: %w", model.ID, err)
		}
		fmt.Fprintf(&body, "var %s = golem.GeneratedModelDescriptor[%s](%s)\n\n", descriptorName, model.Go.Name, modelLiteral)
		symbols = append(symbols,
			Symbol{PackagePath: spec.ImportPath, Name: descriptorName, Kind: SymbolModelDescriptor, ModelID: model.ID},
			Symbol{PackagePath: spec.ImportPath, Name: namespace, Kind: SymbolNamespace, ModelID: model.ID},
		)
		fmt.Fprintf(&body, "type %s struct {\n", typeName)
		for _, field := range orderedFields(model.Fields) {
			handle, err := fieldHandle(field, model.ID, modelByID, enumByID, relations, imports)
			if err != nil {
				return File{}, nil, err
			}
			fmt.Fprintf(&body, "\t%s %s\n", field.GoName, handle)
		}
		body.WriteString("}\n\n")
		fmt.Fprintf(&body, "var %s = %s{\n", namespace, typeName)
		for _, field := range orderedFields(model.Fields) {
			initializer, err := fieldInitializer(field, model.ID, modelByID, enumByID, relations, imports)
			if err != nil {
				return File{}, nil, err
			}
			fmt.Fprintf(&body, "\t%s: %s,\n", field.GoName, initializer)
			kind := SymbolField
			symbol := Symbol{PackagePath: spec.ImportPath, Namespace: namespace, Name: field.GoName, Kind: kind, ModelID: model.ID, FieldID: field.ID}
			if field.Relation != nil {
				symbol.Kind, symbol.RelationID = SymbolRelation, field.Relation.RelationID
			}
			symbols = append(symbols, symbol)
		}
		body.WriteString("}\n\n")
		contract := contracts[model.ID]
		for _, selector := range contract.Selectors {
			name := selector.Name
			if name == "" {
				name = selectorName(selector, model)
			}
			symbols = append(symbols, Symbol{PackagePath: spec.ImportPath, Namespace: namespace, Name: name, Kind: SymbolSelector, ModelID: model.ID, KeyID: selector.KeyID})
		}
	}

	var source bytes.Buffer
	source.WriteString("// Code generated by golem. DO NOT EDIT.\n\n")
	fmt.Fprintf(&source, "package %s\n\n", spec.PackageName)
	imports.write(&source)
	source.Write(body.Bytes())
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return File{}, nil, fmt.Errorf("model codegen: format package %q: %w\n%s", spec.ImportPath, err, source.String())
	}
	path := BootstrapFilename
	if spec.Directory != "" {
		path = filepath.Join(spec.Directory, BootstrapFilename)
	}
	return File{ImportPath: spec.ImportPath, PackageName: spec.PackageName, Path: path, Source: formatted}, symbols, nil
}

func orderedFields(fields []ir.FieldIR) []ir.FieldIR {
	result := append([]ir.FieldIR(nil), fields...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].DeclarationOrder != result[j].DeclarationOrder {
			return result[i].DeclarationOrder < result[j].DeclarationOrder
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func fieldHandle(field ir.FieldIR, owner ir.ModelID, models map[ir.ModelID]ir.ModelDeclIR, enums map[ir.EnumID]ir.EnumIR, relations map[ir.RelationID]ir.RelationIR, imports *importSet) (string, error) {
	ownerModel := models[owner]
	if field.Scalar != nil {
		valueType, err := logicalGoType(field.Scalar.Type, enums, imports)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("golem.ScalarField[%s, %s]", ownerModel.Go.Name, valueType), nil
	}
	if field.Relation == nil {
		return "", fmt.Errorf("model codegen: field %s has no scalar or relation payload", field.ID)
	}
	target, err := relationTarget(field, owner, models, relations)
	if err != nil {
		return "", err
	}
	targetType := imports.qualify(target.Go.PackagePath, target.Go.Name)
	kind := "ToOne"
	if field.Relation.Kind == ir.RelationHasMany {
		kind = "ToMany"
	}
	return fmt.Sprintf("golem.%s[%s, %s]", kind, ownerModel.Go.Name, targetType), nil
}

func fieldInitializer(field ir.FieldIR, owner ir.ModelID, models map[ir.ModelID]ir.ModelDeclIR, enums map[ir.EnumID]ir.EnumIR, relations map[ir.RelationID]ir.RelationIR, imports *importSet) (string, error) {
	handle, err := fieldHandle(field, owner, models, enums, relations, imports)
	if err != nil {
		return "", err
	}
	open := strings.IndexByte(handle, '[')
	args := handle[open:]
	if field.Scalar != nil {
		literal, err := idLiteral("FieldID", string(field.ID))
		if err != nil {
			return "", err
		}
		return "golem.GeneratedScalarField" + args + "(" + literal + ")", nil
	}
	literal, err := idLiteral("RelationID", string(field.Relation.RelationID))
	if err != nil {
		return "", err
	}
	constructor := "golem.GeneratedToOne"
	if field.Relation.Kind == ir.RelationHasMany {
		constructor = "golem.GeneratedToMany"
	}
	return constructor + args + "(" + literal + ")", nil
}

func relationTarget(field ir.FieldIR, owner ir.ModelID, models map[ir.ModelID]ir.ModelDeclIR, relations map[ir.RelationID]ir.RelationIR) (ir.ModelDeclIR, error) {
	relation, ok := relations[field.Relation.RelationID]
	if !ok {
		return ir.ModelDeclIR{}, fmt.Errorf("model codegen: missing relation %s", field.Relation.RelationID)
	}
	targetID := relation.TargetModel
	if field.Relation.Role == ir.RelationInverse {
		targetID = relation.SourceModel
	}
	if field.Relation.Role != ir.RelationSource && field.Relation.Role != ir.RelationInverse {
		return ir.ModelDeclIR{}, fmt.Errorf("model codegen: invalid relation role %q", field.Relation.Role)
	}
	target, ok := models[targetID]
	if !ok {
		return ir.ModelDeclIR{}, fmt.Errorf("model codegen: relation %s target %s not found", relation.ID, targetID)
	}
	return target, nil
}

func logicalGoType(logical ir.LogicalTypeIR, enums map[ir.EnumID]ir.EnumIR, imports *importSet) (string, error) {
	switch logical.Kind {
	case ir.TypeBool:
		return "bool", nil
	case ir.TypeInt16:
		return "int16", nil
	case ir.TypeInt32:
		return "int32", nil
	case ir.TypeInt64:
		return "int64", nil
	case ir.TypeFloat32:
		return "float32", nil
	case ir.TypeFloat64:
		return "float64", nil
	case ir.TypeDecimal:
		return "golem.Decimal", nil
	case ir.TypeString:
		return "string", nil
	case ir.TypeBytes:
		return "[]byte", nil
	case ir.TypeUUID:
		return "golem.UUID", nil
	case ir.TypeDate:
		return "golem.Date", nil
	case ir.TypeTime:
		return "golem.Time", nil
	case ir.TypeDateTime:
		imports.add("time")
		return "time.Time", nil
	case ir.TypeJSON:
		return "golem.JSON[any]", nil
	case ir.TypeEnum:
		if logical.EnumID == nil {
			return "", fmt.Errorf("model codegen: enum logical type has no enum ID")
		}
		enum, ok := enums[*logical.EnumID]
		if !ok {
			return "", fmt.Errorf("model codegen: enum %s not found", *logical.EnumID)
		}
		return imports.qualify(enum.Go.PackagePath, enum.Go.Name), nil
	case ir.TypeScalarList:
		if logical.Element == nil {
			return "", fmt.Errorf("model codegen: scalar-list logical type has no element")
		}
		element, err := logicalGoType(*logical.Element, enums, imports)
		if err != nil {
			return "", err
		}
		return "golem.List[" + element + "]", nil
	default:
		return "", fmt.Errorf("model codegen: unsupported logical type %q", logical.Kind)
	}
}

func idLiteral(kind, value string) (string, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		return "", fmt.Errorf("%s %q is not a canonical 128-bit hexadecimal ID", kind, value)
	}
	parts := make([]string, len(decoded))
	for i, value := range decoded {
		parts[i] = fmt.Sprintf("0x%02x", value)
	}
	return "golem." + kind + "{" + strings.Join(parts, ", ") + "}", nil
}

func selectorName(selector ir.SelectorContractIR, model ir.ModelDeclIR) string {
	byID := make(map[ir.FieldID]string, len(model.Fields))
	for _, field := range model.Fields {
		byID[field.ID] = field.GoName
	}
	var b strings.Builder
	b.WriteString("By")
	for _, id := range selector.Fields {
		b.WriteString(byID[id])
	}
	return b.String()
}

func plural(name string) string {
	if strings.HasSuffix(name, "y") && len(name) > 1 && !strings.ContainsRune("aeiouAEIOU", rune(name[len(name)-2])) {
		return name[:len(name)-1] + "ies"
	}
	for _, suffix := range []string{"s", "x", "z", "ch", "sh"} {
		if strings.HasSuffix(strings.ToLower(name), suffix) {
			return name + "es"
		}
	}
	return name + "s"
}

type importSet struct {
	current, golem string
	paths          map[string]string
	used           map[string]bool
}

func newImports(current, golem string) *importSet {
	return &importSet{current: current, golem: golem, paths: map[string]string{golem: "golem"}, used: map[string]bool{"golem": true}}
}
func (s *importSet) add(path string) string {
	if path == s.current {
		return ""
	}
	if alias, ok := s.paths[path]; ok {
		return alias
	}
	base := path[strings.LastIndex(path, "/")+1:]
	base = sanitize(base)
	alias := base
	for n := 2; s.used[alias]; n++ {
		alias = fmt.Sprintf("%s%d", base, n)
	}
	s.paths[path], s.used[alias] = alias, true
	return alias
}
func (s *importSet) qualify(path, name string) string {
	alias := s.add(path)
	if alias == "" {
		return name
	}
	return alias + "." + name
}
func (s *importSet) write(buffer *bytes.Buffer) {
	paths := make([]string, 0, len(s.paths))
	for path := range s.paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	buffer.WriteString("import (\n")
	for _, path := range paths {
		fmt.Fprintf(buffer, "\t%s %q\n", s.paths[path], path)
	}
	buffer.WriteString(")\n\n")
}
func sanitize(value string) string {
	var b strings.Builder
	for i, r := range value {
		if unicode.IsLetter(r) || r == '_' || (i > 0 && unicode.IsDigit(r)) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "pkg"
	}
	return b.String()
}
