package event

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"go/format"
	"go/token"
	"path/filepath"
	"sort"
	"strings"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func Emit(request Request) ([]File, error) {
	if request.GolemImportPath == "" {
		request.GolemImportPath = modelcodegen.DefaultGolemImportPath
	}
	if request.FinalStamp.GenerationDigest == "" || request.FinalStamp.GeneratorVersion == "" || request.FinalStamp.TemplateABIVersion == "" {
		return nil, fmt.Errorf("event codegen: incomplete final generation stamp")
	}
	packages := make(map[string]modelcodegen.PackageSpec, len(request.Packages))
	for _, spec := range request.Packages {
		if spec.ImportPath == "" || !token.IsIdentifier(spec.PackageName) {
			return nil, fmt.Errorf("event codegen: invalid package specification %#v", spec)
		}
		if _, duplicate := packages[spec.ImportPath]; duplicate {
			return nil, fmt.Errorf("event codegen: duplicate package %q", spec.ImportPath)
		}
		packages[spec.ImportPath] = spec
	}
	contracts := make(map[ir.ModelID]ir.ModelContractIR, len(request.Compilation.Contract.Models))
	for _, contract := range request.Compilation.Contract.Models {
		contracts[contract.ModelID] = contract
	}
	enums := make(map[ir.EnumID]ir.EnumIR, len(request.Compilation.Model.Enums))
	for _, enum := range request.Compilation.Model.Enums {
		enums[enum.ID] = enum
	}
	byPackage := make(map[string][]ir.ModelDeclIR)
	for _, model := range request.Compilation.Model.Models {
		if _, exists := packages[model.Go.PackagePath]; !exists {
			return nil, fmt.Errorf("event codegen: model %s has no package specification", model.ID)
		}
		byPackage[model.Go.PackagePath] = append(byPackage[model.Go.PackagePath], model)
	}
	paths := make([]string, 0, len(byPackage))
	for path, models := range byPackage {
		enabled := false
		for _, model := range models {
			if contracts[model.ID].Subscriptions {
				enabled = true
				break
			}
		}
		if !enabled {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]File, 0, len(paths))
	for _, path := range paths {
		models := byPackage[path]
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
		file, err := emitPackage(packages[path], models, contracts, enums, request)
		if err != nil {
			return nil, err
		}
		result = append(result, file)
	}
	return result, nil
}

func emitPackage(spec modelcodegen.PackageSpec, models []ir.ModelDeclIR, contracts map[ir.ModelID]ir.ModelContractIR, enums map[ir.EnumID]ir.EnumIR, request Request) (File, error) {
	imports := newImports(spec.ImportPath)
	golemAlias := imports.qualify(request.GolemImportPath, "golem")
	runtimePath := request.RuntimeImportPath
	if runtimePath == "" {
		runtimePath = strings.TrimSuffix(request.GolemImportPath, "/golem") + "/runtime"
	}
	runtimeAlias := imports.qualify(runtimePath, "golemruntime")
	imports.qualify("fmt", "fmt")
	generation, err := digestLiteral(golemAlias+".SchemaDigest", request.FinalStamp.GenerationDigest)
	if err != nil {
		return File{}, err
	}
	var body bytes.Buffer
	var registrations []string
	var factories []string
	for _, model := range models {
		contract := contracts[model.ID]
		if !contract.Subscriptions {
			continue
		}
		if contract.Event == nil || model.PrimaryKey == nil || len(model.PrimaryKey.Fields) == 0 {
			return File{}, fmt.Errorf("event codegen: subscribed model %s has incomplete event metadata", model.ID)
		}
		fields := make(map[ir.FieldID]ir.FieldIR, len(model.Fields))
		for _, field := range model.Fields {
			fields[field.ID] = field
		}
		identityFields := make([]ir.FieldIR, len(contract.Event.Schema.IdentityFields))
		for index, eventField := range contract.Event.Schema.IdentityFields {
			field, exists := fields[eventField.FieldID]
			if !exists || field.Scalar == nil || eventField.FieldID != model.PrimaryKey.Fields[index] {
				return File{}, fmt.Errorf("event codegen: model %s identity metadata disagrees with primary key", model.ID)
			}
			identityFields[index] = field
		}
		payloadName := model.Go.Name + "Event"
		identityName := model.Go.Name + "EventIdentity"
		if len(identityFields) == 1 {
			valueType, err := logicalGoType(identityFields[0].Scalar.Type, enums, imports, golemAlias)
			if err != nil {
				return File{}, err
			}
			fmt.Fprintf(&body, "type %s = %s\n\n", identityName, valueType)
		} else {
			fmt.Fprintf(&body, "type %s struct {\n", identityName)
			for _, field := range identityFields {
				valueType, err := logicalGoType(field.Scalar.Type, enums, imports, golemAlias)
				if err != nil {
					return File{}, err
				}
				fmt.Fprintf(&body, "\t%s %s\n", privateName(field.GoName), valueType)
			}
			body.WriteString("}\n\n")
			for _, field := range identityFields {
				valueType, _ := logicalGoType(field.Scalar.Type, enums, imports, golemAlias)
				fmt.Fprintf(&body, "func (identity %s) %s() %s { return %s }\n", identityName, field.GoName, valueType, cloneExpression("identity."+privateName(field.GoName), field.Scalar.Type))
			}
			body.WriteByte('\n')
		}
		fmt.Fprintf(&body, "type %s struct {\n\tmetadata %s.EventMetadata\n\tidentity %s\n\tentity %s.Row[%s]\n\tentityPresent bool\n}\n\n", payloadName, golemAlias, identityName, golemAlias, model.Go.Name)
		fmt.Fprintf(&body, "func (event %s) Metadata() %s.EventMetadata { return event.metadata }\n", payloadName, golemAlias)
		identityReturn := "event.identity"
		if len(identityFields) == 1 {
			identityReturn = cloneExpression(identityReturn, identityFields[0].Scalar.Type)
		}
		fmt.Fprintf(&body, "func (event %s) ID() %s { return %s }\n", payloadName, identityName, identityReturn)
		fmt.Fprintf(&body, "func (event %s) Entity() (%s.Row[%s], bool) {\n\tif !event.entityPresent { return %s.Row[%s]{}, false }\n\treturn event.entity, true\n}\n\n", payloadName, golemAlias, model.Go.Name, golemAlias, model.Go.Name)
		modelID, err := idLiteral(golemAlias+".ModelID", string(model.ID))
		if err != nil {
			return File{}, err
		}
		schemaDigest, err := digestLiteral(golemAlias+".EventSchemaDigest", string(contract.Event.SchemaFingerprint))
		if err != nil {
			return File{}, err
		}
		fieldLiterals := make([]string, len(identityFields))
		for index, field := range identityFields {
			fieldLiterals[index], err = idLiteral(golemAlias+".FieldID", string(field.ID))
			if err != nil {
				return File{}, err
			}
		}
		identityContractName := contract.Event.IdentityTypeName
		if identityContractName == "" {
			identityContractName = identityName
		}
		registrations = append(registrations, fmt.Sprintf("%s.GeneratedEventModelMetadata(%s, %s, []%s.FieldID{%s}, %q, %q)", golemAlias, modelID, schemaDigest, golemAlias, strings.Join(fieldLiterals, ", "), payloadName, identityContractName))
		factoryName := "golemGenerated" + model.Go.Name + "EventFactory"
		fmt.Fprintf(&body, "type %s struct{}\n\n", factoryName)
		fmt.Fprintf(&body, "func (%s) ModelID() %s.ModelID { return %s }\n", factoryName, golemAlias, modelID)
		fmt.Fprintf(&body, "func (%s) EventSchemaDigest() %s.EventSchemaDigest { return %s }\n", factoryName, golemAlias, schemaDigest)
		fmt.Fprintf(&body, "func (%s) Build(input %s.ValidatedEvent) (any, error) {\n", factoryName, runtimeAlias)
		body.WriteString("\tmetadata := input.Metadata()\n")
		fmt.Fprintf(&body, "\tif metadata.ModelID() != (%s) || input.ResolvedEventSchemaDigest() != (%s) { return nil, fmt.Errorf(\"GOLEM_EVENT_CODEC: validated event metadata disagrees with generated schema\") }\n", modelID, schemaDigest)
		body.WriteString("\tvalues := input.IdentityValues()\n")
		fmt.Fprintf(&body, "\tif len(values) != %d { return nil, fmt.Errorf(\"GOLEM_EVENT_CODEC: validated event identity has %%d components; want %d\", len(values)) }\n", len(identityFields), len(identityFields))
		componentNames := make([]string, len(identityFields))
		for index, field := range identityFields {
			valueType, typeErr := logicalGoType(field.Scalar.Type, enums, imports, golemAlias)
			if typeErr != nil {
				return File{}, typeErr
			}
			componentNames[index] = fmt.Sprintf("component%d", index)
			fmt.Fprintf(&body, "\t%s, ok := values[%d].(%s)\n\tif !ok { return nil, fmt.Errorf(\"GOLEM_EVENT_CODEC: validated event identity component %d has the wrong Go type\") }\n", componentNames[index], index, valueType, index)
			if field.Scalar.Type.Kind == ir.TypeBytes || field.Scalar.Type.Kind == ir.TypeScalarList {
				fmt.Fprintf(&body, "\t%s = %s\n", componentNames[index], cloneExpression(componentNames[index], field.Scalar.Type))
			}
		}
		if len(identityFields) == 1 {
			fmt.Fprintf(&body, "\tidentity := %s\n", componentNames[0])
		} else {
			fmt.Fprintf(&body, "\tidentity := %s{\n", identityName)
			for index, field := range identityFields {
				fmt.Fprintf(&body, "\t\t%s: %s,\n", privateName(field.GoName), componentNames[index])
			}
			body.WriteString("\t}\n")
		}
		fmt.Fprintf(&body, "\tvar entity %s.Row[%s]\n\tentityPresent := false\n", golemAlias, model.Go.Name)
		fmt.Fprintf(&body, "\tif runtimeEntity, present := input.Entity(); present {\n\t\ttyped, err := %s.RuntimeTypedReadRow(%s, runtimeEntity)\n\t\tif err != nil { return nil, fmt.Errorf(\"GOLEM_EVENT_CODEC: validated event entity: %%w\", err) }\n\t\tentity, entityPresent = typed, true\n\t}\n", golemAlias, "GolemGenerated"+model.Go.Name+"Descriptor")
		fmt.Fprintf(&body, "\treturn %s{metadata: metadata, identity: identity, entity: entity, entityPresent: entityPresent}, nil\n}\n\n", payloadName)
		factories = append(factories, factoryName+"{}")
	}
	fmt.Fprintf(&body, "func GolemGeneratedEventModels() %s.PackageEventRegistry {\n", golemAlias)
	fmt.Fprintf(&body, "\treturn %s.GeneratedPackageEventRegistry(%s", golemAlias, generation)
	for _, registration := range registrations {
		fmt.Fprintf(&body, ",\n\t\t%s", registration)
	}
	body.WriteString(")\n}\n")
	fmt.Fprintf(&body, "\nfunc GolemGeneratedEventFactories() %s.PackageEventFactories {\n", runtimeAlias)
	fmt.Fprintf(&body, "\treturn %s.GeneratedPackageEventFactories(%s", runtimeAlias, generation)
	for _, factory := range factories {
		fmt.Fprintf(&body, ",\n\t\t%s", factory)
	}
	body.WriteString(")\n}\n")

	var source bytes.Buffer
	source.WriteString("// Code generated by golem. DO NOT EDIT.\n")
	fmt.Fprintf(&source, "// Golem generation digest: %s\n", request.FinalStamp.GenerationDigest)
	fmt.Fprintf(&source, "// Golem generator version: %s\n", request.FinalStamp.GeneratorVersion)
	fmt.Fprintf(&source, "// Golem template ABI version: %s\n\n", request.FinalStamp.TemplateABIVersion)
	fmt.Fprintf(&source, "package %s\n\n", spec.PackageName)
	imports.write(&source)
	source.Write(body.Bytes())
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return File{}, fmt.Errorf("event codegen: format package %q: %w\n%s", spec.ImportPath, err, source.String())
	}
	path := Filename
	if spec.Directory != "" {
		path = filepath.Join(spec.Directory, Filename)
	}
	return File{ImportPath: spec.ImportPath, PackageName: spec.PackageName, Path: path, Source: formatted}, nil
}

func logicalGoType(logical ir.LogicalTypeIR, enums map[ir.EnumID]ir.EnumIR, imports *importSet, golemAlias string) (string, error) {
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
		return golemAlias + ".Decimal", nil
	case ir.TypeString:
		return "string", nil
	case ir.TypeBytes:
		return "[]byte", nil
	case ir.TypeUUID:
		return golemAlias + ".UUID", nil
	case ir.TypeDate:
		return golemAlias + ".Date", nil
	case ir.TypeTime:
		return golemAlias + ".Time", nil
	case ir.TypeDateTime:
		return imports.qualify("time", "time") + ".Time", nil
	case ir.TypeJSON:
		return golemAlias + ".JSON[any]", nil
	case ir.TypeEnum:
		if logical.EnumID == nil {
			return "", fmt.Errorf("event codegen: enum type has no stable identity")
		}
		enum, exists := enums[*logical.EnumID]
		if !exists {
			return "", fmt.Errorf("event codegen: enum %s is absent", *logical.EnumID)
		}
		alias := imports.qualify(enum.Go.PackagePath, "eventenum")
		if alias == "" {
			return enum.Go.Name, nil
		}
		return alias + "." + enum.Go.Name, nil
	case ir.TypeScalarList:
		if logical.Element == nil {
			return "", fmt.Errorf("event codegen: scalar list has no element type")
		}
		element, err := logicalGoType(*logical.Element, enums, imports, golemAlias)
		if err != nil {
			return "", err
		}
		return golemAlias + ".List[" + element + "]", nil
	default:
		return "", fmt.Errorf("event codegen: unsupported logical type %q", logical.Kind)
	}
}

func cloneExpression(value string, logical ir.LogicalTypeIR) string {
	if logical.Kind == ir.TypeBytes {
		return "append([]byte(nil), " + value + "...)"
	}
	if logical.Kind == ir.TypeScalarList {
		return "append(" + value + "[:0:0], " + value + "...)"
	}
	return value
}

func privateName(exported string) string {
	if exported == "" {
		return ""
	}
	return strings.ToLower(exported[:1]) + exported[1:]
}

func idLiteral(typeName, value string) (string, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != value {
		return "", fmt.Errorf("event codegen: identity %q is not canonical 128-bit hex", value)
	}
	parts := make([]string, len(decoded))
	for index, value := range decoded {
		parts[index] = fmt.Sprintf("0x%02x", value)
	}
	return typeName + "{" + strings.Join(parts, ", ") + "}", nil
}

func digestLiteral(typeName, value string) (string, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != value {
		return "", fmt.Errorf("event codegen: digest %q is not canonical SHA-256", value)
	}
	parts := make([]string, len(decoded))
	for index, value := range decoded {
		parts[index] = fmt.Sprintf("0x%02x", value)
	}
	return typeName + "{" + strings.Join(parts, ", ") + "}", nil
}

type importSet struct {
	current string
	paths   map[string]string
	used    map[string]bool
}

func newImports(current string) *importSet {
	return &importSet{current: current, paths: make(map[string]string), used: make(map[string]bool)}
}

func (imports *importSet) qualify(path, preferred string) string {
	if path == imports.current {
		return ""
	}
	if alias, exists := imports.paths[path]; exists {
		return alias
	}
	alias := preferred
	for suffix := 2; imports.used[alias]; suffix++ {
		alias = fmt.Sprintf("%s%d", preferred, suffix)
	}
	imports.paths[path], imports.used[alias] = alias, true
	return alias
}

func (imports *importSet) write(destination *bytes.Buffer) {
	paths := make([]string, 0, len(imports.paths))
	for path := range imports.paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return
	}
	destination.WriteString("import (\n")
	for _, path := range paths {
		fmt.Fprintf(destination, "\t%s %q\n", imports.paths[path], path)
	}
	destination.WriteString(")\n\n")
}
