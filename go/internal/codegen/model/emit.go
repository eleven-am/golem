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
		if _, duplicate := modelByID[model.ID]; duplicate {
			return Result{}, fmt.Errorf("model codegen: duplicate model ID %s", model.ID)
		}
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
		file, symbols, emitErr := emitPackage(packages[path], byPackage[path], modelByID, enumByID, relations, contractMap(request.Compilation.Contract), golemPath, request.FinalStamp)
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

func emitPackage(spec PackageSpec, models []ir.ModelDeclIR, modelByID map[ir.ModelID]ir.ModelDeclIR, enumByID map[ir.EnumID]ir.EnumIR, relations map[ir.RelationID]ir.RelationIR, contracts map[ir.ModelID]ir.ModelContractIR, golemPath string, stamp *FinalStamp) (File, []Symbol, error) {
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
		members := map[string]string{
			"Where": "read method", "OrderBy": "read method", "Take": "read method",
			"Skip": "read method", "Distinct": "read method", "Cursor": "read method", "Select": "read method", "Include": "read method", "Omit": "read method",
		}
		for _, field := range model.Fields {
			if prior := members[field.GoName]; prior != "" {
				return File{}, nil, fmt.Errorf("model codegen: namespace %s member %q collides between %s and field", namespace, field.GoName, prior)
			}
			members[field.GoName] = "field"
		}
		for _, selector := range orderedSelectors(contracts[model.ID], model) {
			name := generatedSelectorName(selector, model)
			if !token.IsIdentifier(name) {
				return File{}, nil, fmt.Errorf("model codegen: namespace %s selector %s has invalid generated Go identifier %q", namespace, selector.KeyID, name)
			}
			if prior := members[name]; prior != "" {
				return File{}, nil, fmt.Errorf("model codegen: namespace %s member %q collides between %s and selector %s", namespace, name, prior, selector.KeyID)
			}
			members[name] = "selector"
		}
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
		contract := contracts[model.ID]
		modelLiteral, err := idLiteral("ModelID", string(model.ID))
		if err != nil {
			return File{}, nil, fmt.Errorf("model codegen: model %s: %w", model.ID, err)
		}
		shape, err := descriptorShapeLiteral(model, contract, modelByID, relations)
		if err != nil {
			return File{}, nil, err
		}
		fmt.Fprintf(&body, "var %s = golem.GeneratedModelDescriptor[%s](%s, %s)\n\n", descriptorName, model.Go.Name, modelLiteral, shape)
		for _, selector := range orderedSelectors(contract, model) {
			selectorType := generatedSelectorType(model, selector)
			fmt.Fprintf(&body, "type %s struct{}\n\n", selectorType)
			method, methodErr := selectorValueMethod(selectorType, selector, model, enumByID, imports)
			if methodErr != nil {
				return File{}, nil, methodErr
			}
			body.WriteString(method)
		}
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
		for _, selector := range orderedSelectors(contract, model) {
			fmt.Fprintf(&body, "\t%s %s\n", generatedSelectorName(selector, model), generatedSelectorType(model, selector))
		}
		body.WriteString("}\n\n")
		fmt.Fprintf(&body, "func (%s) Where(predicate golem.Predicate[%s]) golem.ReadOption[%s] {\n", typeName, model.Go.Name, model.Go.Name)
		fmt.Fprintf(&body, "\treturn golem.Where(predicate)\n}\n\n")
		fmt.Fprintf(&body, "func (%s) OrderBy(terms ...golem.OrderTerm[%s]) golem.ReadOption[%s] {\n", typeName, model.Go.Name, model.Go.Name)
		fmt.Fprintf(&body, "\treturn golem.OrderBy(terms...)\n}\n\n")
		fmt.Fprintf(&body, "func (%s) Take(value int) golem.ReadOption[%s] { return golem.Take[%s](value) }\n\n", typeName, model.Go.Name, model.Go.Name)
		fmt.Fprintf(&body, "func (%s) Skip(value int) golem.ReadOption[%s] { return golem.Skip[%s](value) }\n\n", typeName, model.Go.Name, model.Go.Name)
		fmt.Fprintf(&body, "func (%s) Distinct(fields ...golem.Column[%s]) golem.ReadOption[%s] {\n", typeName, model.Go.Name, model.Go.Name)
		fmt.Fprintf(&body, "\treturn golem.Distinct(fields...)\n}\n\n")
		fmt.Fprintf(&body, "func (%s) Cursor(selector golem.UniqueSelectorValue[%s]) golem.ReadOption[%s] {\n", typeName, model.Go.Name, model.Go.Name)
		fmt.Fprintf(&body, "\treturn golem.Cursor(selector)\n}\n\n")
		fmt.Fprintf(&body, "func (%s) Select(fields ...golem.Selection[%s]) golem.ReadOption[%s] {\n", typeName, model.Go.Name, model.Go.Name)
		fmt.Fprintf(&body, "\treturn golem.Select(fields...)\n}\n\n")
		fmt.Fprintf(&body, "func (%s) Include(relations ...golem.RelationInclusion[%s]) golem.ReadOption[%s] {\n", typeName, model.Go.Name, model.Go.Name)
		fmt.Fprintf(&body, "\treturn golem.Include(relations...)\n}\n\n")
		fmt.Fprintf(&body, "func (%s) Omit(fields ...golem.Column[%s]) golem.ReadOption[%s] {\n", typeName, model.Go.Name, model.Go.Name)
		fmt.Fprintf(&body, "\treturn golem.Omit(fields...)\n}\n\n")
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
		for _, selector := range orderedSelectors(contract, model) {
			name := generatedSelectorName(selector, model)
			fmt.Fprintf(&body, "\t%s: %s{},\n", name, generatedSelectorType(model, selector))
			symbols = append(symbols, Symbol{PackagePath: spec.ImportPath, Namespace: namespace, Name: name, Kind: SymbolSelector, ModelID: model.ID, KeyID: selector.KeyID, Fields: append([]ir.FieldID(nil), selector.Fields...)})
		}
		body.WriteString("}\n\n")
	}
	body.WriteString("func GolemGeneratedDescriptors() golem.PackageDescriptors {\n")
	if stamp == nil {
		body.WriteString("\treturn golem.GeneratedPackageDescriptors(\n")
	} else {
		digest, digestErr := schemaDigestLiteral(stamp.GenerationDigest)
		if digestErr != nil {
			return File{}, nil, digestErr
		}
		fmt.Fprintf(&body, "\treturn golem.GeneratedStampedPackageDescriptors(%s,\n", digest)
	}
	for _, model := range models {
		fmt.Fprintf(&body, "\t\tGolemGenerated%sDescriptor.Metadata(),\n", model.Go.Name)
	}
	body.WriteString("\t)\n}\n")

	var source bytes.Buffer
	source.WriteString("// Code generated by golem. DO NOT EDIT.\n")
	if stamp != nil {
		if stamp.GenerationDigest == "" || stamp.GeneratorVersion == "" || stamp.TemplateABIVersion == "" {
			return File{}, nil, fmt.Errorf("model codegen: incomplete final generation stamp")
		}
		fmt.Fprintf(&source, "// Golem generation digest: %s\n", stamp.GenerationDigest)
		fmt.Fprintf(&source, "// Golem generator version: %s\n", stamp.GeneratorVersion)
		fmt.Fprintf(&source, "// Golem template ABI version: %s\n", stamp.TemplateABIVersion)
	}
	source.WriteByte('\n')
	fmt.Fprintf(&source, "package %s\n\n", spec.PackageName)
	imports.write(&source)
	source.Write(body.Bytes())
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return File{}, nil, fmt.Errorf("model codegen: format package %q: %w\n%s", spec.ImportPath, err, source.String())
	}
	filename := BootstrapFilename
	if stamp != nil {
		filename = FinalFilename
	}
	path := filename
	if spec.Directory != "" {
		path = filepath.Join(spec.Directory, filename)
	}
	return File{ImportPath: spec.ImportPath, PackageName: spec.PackageName, Path: path, Source: formatted}, symbols, nil
}

func schemaDigestLiteral(value string) (string, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != value {
		return "", fmt.Errorf("model codegen: generation digest %q is not canonical SHA-256", value)
	}
	parts := make([]string, len(decoded))
	for index, b := range decoded {
		parts[index] = fmt.Sprintf("0x%02x", b)
	}
	return "golem.SchemaDigest{" + strings.Join(parts, ", ") + "}", nil
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
		handle, valueType, err := scalarHandle(field.Scalar, enums, imports)
		if err != nil {
			return "", err
		}
		if handle == "BytesField" || handle == "NullableBytesField" || handle == "ModeJSONField" || handle == "NullableModeJSONField" {
			return fmt.Sprintf("golem.%s[%s]", handle, ownerModel.Go.Name), nil
		}
		return fmt.Sprintf("golem.%s[%s, %s]", handle, ownerModel.Go.Name, valueType), nil
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
		constructor := handle[:open]
		constructor = strings.TrimPrefix(constructor, "golem.")
		return "golem.Generated" + constructor + args + "(" + literal + ")", nil
	}
	fieldLiteral, err := idLiteral("FieldID", string(field.ID))
	if err != nil {
		return "", err
	}
	relationLiteral, err := idLiteral("RelationID", string(field.Relation.RelationID))
	if err != nil {
		return "", err
	}
	constructor := "golem.GeneratedToOne"
	if field.Relation.Kind == ir.RelationHasMany {
		constructor = "golem.GeneratedToMany"
	}
	target, err := relationTarget(field, owner, models, relations)
	if err != nil {
		return "", err
	}
	targetLiteral, err := idLiteral("ModelID", string(target.ID))
	if err != nil {
		return "", err
	}
	return constructor + args + "(" + fieldLiteral + ", " + relationLiteral + ", " + targetLiteral + ")", nil
}

// scalarHandle is the single bootstrap/final mapping from normalized logical
// field facts to the narrow public policy handle method set. It deliberately
// derives the class from ModelIR instead of serializing a second capability copy.
func scalarHandle(field *ir.ScalarFieldIR, enums map[ir.EnumID]ir.EnumIR, imports *importSet) (name, valueType string, err error) {
	if field == nil {
		return "", "", fmt.Errorf("model codegen: scalar handle has no scalar metadata")
	}
	logical := field.Type
	valueType, err = logicalGoType(logical, enums, imports)
	if err != nil {
		return "", "", err
	}
	if logical.Kind == ir.TypeScalarList {
		if logical.Element == nil {
			return "", "", fmt.Errorf("model codegen: scalar list has no element type")
		}
		valueType, err = logicalGoType(*logical.Element, enums, imports)
		if err != nil {
			return "", "", err
		}
	}
	switch logical.Kind {
	case ir.TypeBool, ir.TypeUUID, ir.TypeEnum:
		name = "EqualField"
	case ir.TypeInt16, ir.TypeInt32, ir.TypeInt64, ir.TypeFloat32, ir.TypeFloat64,
		ir.TypeDecimal, ir.TypeDate, ir.TypeTime, ir.TypeDateTime:
		name = "OrderedField"
	case ir.TypeString:
		name = "ModeTextField"
	case ir.TypeBytes:
		name = "BytesField"
	case ir.TypeScalarList:
		name = "ListField"
	case ir.TypeJSON:
		name = "ModeJSONField"
	default:
		return "", "", fmt.Errorf("model codegen: unsupported logical type %q", logical.Kind)
	}
	if field.Nullable {
		name = "Nullable" + name
	}
	return name, valueType, nil
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

func descriptorShapeLiteral(model ir.ModelDeclIR, contract ir.ModelContractIR, models map[ir.ModelID]ir.ModelDeclIR, relations map[ir.RelationID]ir.RelationIR) (string, error) {
	ordered := orderedFields(model.Fields)
	fieldByID := make(map[ir.FieldID]ir.FieldIR, len(ordered))
	readOnly := map[ir.FieldID]bool{}
	for _, field := range contract.Fields {
		for _, mode := range field.Modes {
			if mode == ir.ModeReadOnly {
				readOnly[field.FieldID] = true
			}
		}
	}
	var scan, write []ir.FieldID
	var relationLiterals []string
	for _, field := range ordered {
		fieldByID[field.ID] = field
		if field.Scalar != nil {
			scan = append(scan, field.ID)
			if field.Scalar.Generation == nil && !field.Scalar.DatabaseReadOnly && !readOnly[field.ID] {
				write = append(write, field.ID)
			}
			continue
		}
		if field.Relation == nil {
			return "", fmt.Errorf("model codegen: field %s has no descriptor payload", field.ID)
		}
		target, err := relationTarget(field, model.ID, models, relations)
		if err != nil {
			return "", err
		}
		modelID, _ := idLiteral("ModelID", string(model.ID))
		targetID, err := idLiteral("ModelID", string(target.ID))
		if err != nil {
			return "", err
		}
		fieldID, err := idLiteral("FieldID", string(field.ID))
		if err != nil {
			return "", err
		}
		relationID, err := idLiteral("RelationID", string(field.Relation.RelationID))
		if err != nil {
			return "", err
		}
		role := "golem.RelationSource"
		if field.Relation.Role == ir.RelationInverse {
			role = "golem.RelationInverse"
		} else if field.Relation.Role != ir.RelationSource {
			return "", fmt.Errorf("model codegen: relation field %s has invalid role %q", field.ID, field.Relation.Role)
		}
		cardinality := "golem.RelationToOne"
		switch field.Relation.Kind {
		case ir.RelationBelongsTo, ir.RelationHasOne:
		case ir.RelationHasMany:
			cardinality = "golem.RelationToMany"
		default:
			return "", fmt.Errorf("model codegen: relation field %s has invalid cardinality %q", field.ID, field.Relation.Kind)
		}
		relationLiterals = append(relationLiterals, fmt.Sprintf("golem.GeneratedRelationMetadata(%s, %s, %s, %s, %s, %s)", modelID, targetID, fieldID, relationID, role, cardinality))
	}
	scanLiteral, err := fieldIDSliceLiteral(scan)
	if err != nil {
		return "", err
	}
	writeLiteral, err := fieldIDSliceLiteral(write)
	if err != nil {
		return "", err
	}
	var identityLiterals []string
	for _, selector := range orderedSelectors(contract, model) {
		if len(selector.Fields) == 0 {
			return "", fmt.Errorf("model codegen: selector %s has no identity fields", selector.KeyID)
		}
		for _, fieldID := range selector.Fields {
			field, exists := fieldByID[fieldID]
			if !exists || field.Scalar == nil {
				return "", fmt.Errorf("model codegen: selector %s references non-scalar or missing field %s", selector.KeyID, fieldID)
			}
		}
		literal, literalErr := identityMetadataLiteral(selector, model)
		if literalErr != nil {
			return "", literalErr
		}
		identityLiterals = append(identityLiterals, literal)
	}
	return "golem.GeneratedDescriptorShape(" + scanLiteral + ", " + writeLiteral + ", []golem.IdentityMetadata{" + strings.Join(identityLiterals, ", ") + "}, []golem.RelationMetadata{" + strings.Join(relationLiterals, ", ") + "})", nil
}

func fieldIDSliceLiteral(fields []ir.FieldID) (string, error) {
	values := make([]string, len(fields))
	for index, field := range fields {
		literal, err := idLiteral("FieldID", string(field))
		if err != nil {
			return "", err
		}
		values[index] = literal
	}
	return "[]golem.FieldID{" + strings.Join(values, ", ") + "}", nil
}

func identityMetadataLiteral(selector ir.SelectorContractIR, model ir.ModelDeclIR) (string, error) {
	modelID, err := idLiteral("ModelID", string(model.ID))
	if err != nil {
		return "", err
	}
	keyID, err := idLiteral("KeyID", string(selector.KeyID))
	if err != nil {
		return "", err
	}
	fields, err := fieldIDSliceLiteral(selector.Fields)
	if err != nil {
		return "", err
	}
	kind := "golem.PrimaryIdentity"
	if selector.Kind == ir.KeyUnique {
		kind = "golem.UniqueIdentity"
	} else if selector.Kind != ir.KeyPrimary {
		return "", fmt.Errorf("model codegen: selector %s has invalid identity kind %q", selector.KeyID, selector.Kind)
	}
	fields = strings.TrimPrefix(strings.TrimSuffix(fields, "}"), "[]golem.FieldID{")
	if fields != "" {
		fields = ", " + fields
	}
	return fmt.Sprintf("golem.GeneratedIdentityMetadata(%s, %s, %s%s)", modelID, keyID, kind, fields), nil
}

func selectorInitializer(selector ir.SelectorContractIR, model ir.ModelDeclIR) (string, error) {
	metadata, err := identityMetadataLiteral(selector, model)
	if err != nil {
		return "", err
	}
	return strings.Replace(metadata, "golem.GeneratedIdentityMetadata", "golem.GeneratedIdentitySelector["+model.Go.Name+"]", 1), nil
}

func orderedSelectors(contract ir.ModelContractIR, model ir.ModelDeclIR) []ir.SelectorContractIR {
	selectors := append([]ir.SelectorContractIR(nil), contract.Selectors...)
	sort.Slice(selectors, func(i, j int) bool {
		left, right := generatedSelectorName(selectors[i], model), generatedSelectorName(selectors[j], model)
		if left != right {
			return left < right
		}
		return selectors[i].KeyID < selectors[j].KeyID
	})
	return selectors
}

// orderedCompoundSelectors returns only selectors that require a distinct
// namespace member. A single-field identity is selected through its existing
// typed ScalarField handle; its identity remains present in ModelMetadata.
func orderedCompoundSelectors(contract ir.ModelContractIR, model ir.ModelDeclIR) []ir.SelectorContractIR {
	selectors := orderedSelectors(contract, model)
	result := make([]ir.SelectorContractIR, 0, len(selectors))
	for _, selector := range selectors {
		if len(selector.Fields) > 1 {
			result = append(result, selector)
		}
	}
	return result
}

func generatedSelectorName(selector ir.SelectorContractIR, model ir.ModelDeclIR) string {
	name := selector.Name
	if name == "" {
		name = selectorName(selector, model)
	}
	if len(selector.Fields) == 1 {
		for _, field := range model.Fields {
			if field.ID == selector.Fields[0] && (name == field.GoName || name == field.LogicalName) {
				return "By" + field.GoName
			}
		}
	}
	if selector.Name != "" {
		return name
	}
	return name
}

func generatedSelectorType(model ir.ModelDeclIR, selector ir.SelectorContractIR) string {
	return "golemGenerated" + model.Go.Name + generatedSelectorName(selector, model) + "Selector"
}

func selectorValueMethod(typeName string, selector ir.SelectorContractIR, model ir.ModelDeclIR, enums map[ir.EnumID]ir.EnumIR, imports *importSet) (string, error) {
	fields := make(map[ir.FieldID]ir.FieldIR, len(model.Fields))
	for _, field := range model.Fields {
		fields[field.ID] = field
	}
	parameters := make([]string, len(selector.Fields))
	components := make([]string, len(selector.Fields))
	for index, fieldID := range selector.Fields {
		field, ok := fields[fieldID]
		if !ok || field.Scalar == nil {
			return "", fmt.Errorf("model codegen: selector %s field %s is absent or non-scalar", selector.KeyID, fieldID)
		}
		valueType, err := logicalGoType(field.Scalar.Type, enums, imports)
		if err != nil {
			return "", err
		}
		parameter := fmt.Sprintf("value%d", index)
		constructor := "golem.GeneratedSelectorComponent"
		if field.Scalar.Nullable {
			valueType = "golem.Null[" + valueType + "]"
			constructor = "golem.GeneratedNullableSelectorComponent"
		}
		parameters[index] = parameter + " " + valueType
		fieldLiteral, err := idLiteral("FieldID", string(fieldID))
		if err != nil {
			return "", err
		}
		components[index] = constructor + "(" + fieldLiteral + ", " + parameter + ")"
	}
	modelLiteral, err := idLiteral("ModelID", string(model.ID))
	if err != nil {
		return "", err
	}
	keyLiteral, err := idLiteral("KeyID", string(selector.KeyID))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("func (%s) Value(%s) golem.UniqueSelectorValue[%s] {\n\treturn golem.GeneratedUniqueSelectorValue[%s](%s, %s, %s)\n}\n\n", typeName, strings.Join(parameters, ", "), model.Go.Name, model.Go.Name, modelLiteral, keyLiteral, strings.Join(components, ", ")), nil
}

func selectorName(selector ir.SelectorContractIR, model ir.ModelDeclIR) string {
	byID := make(map[ir.FieldID]string, len(model.Fields))
	for _, field := range model.Fields {
		name := field.LogicalName
		if name == "" {
			name = field.GoName
		}
		byID[field.ID] = name
	}
	parts := make([]string, 0, len(selector.Fields))
	for _, id := range selector.Fields {
		parts = append(parts, byID[id])
	}
	return strings.Join(parts, "_")
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
