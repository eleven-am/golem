// Package resolve converts source-located declarations into the base logical
// model and contract registries. Keys, indexes, relations, advanced schema, and
// provider lowering intentionally belong to later passes.
package resolve

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/scalar"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

type Result struct {
	Compilation ir.CompilationIR
	Diagnostics []ir.Diagnostic
	// IDs is the compilation-unit registry that later key/index/relation passes
	// must reuse so collision checks span every object kind.
	IDs *ir.IDRegistry
}

// Base resolves the persistence and contract facts that do not require global
// key, index, or relation linking. The returned IR may be partial when
// Diagnostics contains errors, which lets callers report independent failures
// in one deterministic pass.
func Base(raw ir.RawDeclIR) Result {
	return BaseWithRegistry(raw, ir.NewIDRegistry())
}

// BaseWithRegistry supports pipeline composition while preserving one identity
// collision domain across base, key/index, and relation linking.
func BaseWithRegistry(raw ir.RawDeclIR, registry *ir.IDRegistry) Result {
	if registry == nil {
		diagnostic := ir.NewError("P1_ID_REGISTRY_REQUIRED", "base resolution requires a compilation-unit ID registry", raw.Root.Span)
		return Result{Diagnostics: []ir.Diagnostic{diagnostic}}
	}
	resolver := baseResolver{raw: raw, ids: registry, enums: map[string]resolvedEnum{}}
	resolver.resolveSchema()
	resolver.resolveEnums()
	resolver.resolveModels()
	ir.SortDiagnostics(resolver.diagnostics)
	return Result{Compilation: resolver.compilation, Diagnostics: resolver.diagnostics, IDs: registry}
}

type baseResolver struct {
	raw         ir.RawDeclIR
	ids         *ir.IDRegistry
	compilation ir.CompilationIR
	diagnostics []ir.Diagnostic
	enums       map[string]resolvedEnum
}

type resolvedEnum struct {
	id     ir.EnumID
	values []string
}

func (r *baseResolver) resolveSchema() {
	root := r.raw.Root
	identity, diagnostic := r.ids.Register(ir.ObjectSchema, root.SchemaName, root.SchemaNameSpan)
	if diagnostic != nil {
		r.diagnostics = append(r.diagnostics, *diagnostic)
	}
	actor := ir.GoNamedTypeIR{}
	if root.Actor == nil {
		r.diagnostics = append(r.diagnostics, ir.NewError("P1_RESOLVE_ACTOR_MISSING", "schema actor declaration is missing", root.Span))
	} else {
		actor = ir.GoNamedTypeIR{PackagePath: root.Actor.PackagePath, Name: root.Actor.GoName}
	}
	providers := []ir.Provider{ir.SQLite, ir.PostgreSQL}
	if len(root.Providers) != 0 {
		providers = make([]ir.Provider, 0, len(root.Providers))
		for _, provider := range root.Providers {
			providers = append(providers, provider.Provider)
		}
		sort.Slice(providers, func(i, j int) bool { return providerRank(providers[i]) < providerRank(providers[j]) })
	}
	r.compilation.Model = ir.ModelIR{
		FormatVersion: ir.ModelFormatVersion,
		Schema: ir.SchemaIdentityIR{
			ID: ir.SchemaIDFrom(identity), StableName: root.SchemaName,
			PackagePath: root.PackagePath, RootFunction: root.FunctionName, Actor: actor,
		},
		Providers: providers,
	}
	for _, space := range root.EmbeddingSpaces {
		payload, err := semanticcontract.Encode(semanticcontract.Space{Name: space.Name, Dimensions: space.Dimensions})
		if err != nil {
			r.diagnostics = append(r.diagnostics, ir.NewError("P9_EMBEDDING_SPACE_ENCODE", err.Error(), space.Span))
			continue
		}
		for _, provider := range providers {
			canonical := ir.OwnedIdentity(identity.ID, semanticcontract.SpaceKind+"\x00"+string(provider)+"\x00"+space.Name)
			extensionIdentity, extensionDiagnostic := r.ids.Register(ir.ObjectExtension, canonical, space.Span)
			if extensionDiagnostic != nil {
				r.diagnostics = append(r.diagnostics, *extensionDiagnostic)
				continue
			}
			r.compilation.Model.Extensions = append(r.compilation.Model.Extensions, ir.ProviderExtensionIR{
				ID:       ir.ExtensionIDFrom(extensionIdentity),
				Provider: provider,
				Version:  semanticcontract.Version,
				Owner:    ir.ObjectID(identity.ID),
				Kind:     semanticcontract.SpaceKind,
				Payload:  payload,
			})
		}
	}
	r.compilation.Contract = ir.ContractIR{FormatVersion: ir.ContractFormatVersion}
}

func (r *baseResolver) resolveEnums() {
	items := append([]ir.RawEnumDecl(nil), r.raw.Enums...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].PackagePath != items[j].PackagePath {
			return items[i].PackagePath < items[j].PackagePath
		}
		return items[i].GoName < items[j].GoName
	})
	for _, rawEnum := range items {
		canonical := ir.ModelIdentity(r.raw.Root.SchemaName, rawEnum.PackagePath+"."+rawEnum.GoName)
		identity, diagnostic := r.ids.Register(ir.ObjectEnum, canonical, rawEnum.Span)
		if diagnostic != nil {
			r.diagnostics = append(r.diagnostics, *diagnostic)
			continue
		}
		enumID := ir.EnumIDFrom(identity)
		enum := ir.EnumIR{ID: enumID, Go: ir.GoNamedTypeIR{PackagePath: rawEnum.PackagePath, Name: rawEnum.GoName}, LogicalName: rawEnum.GoName}
		values := append([]ir.RawEnumValue(nil), rawEnum.Values...)
		sort.SliceStable(values, func(i, j int) bool { return values[i].Ordinal < values[j].Ordinal })
		for _, rawValue := range values {
			valueCanonical := ir.OwnedIdentity(string(enumID), rawValue.WireValue)
			if rawValue.StableID != nil {
				valueCanonical = *rawValue.StableID
			}
			valueIdentity, diagnostic := r.ids.Register(ir.ObjectEnumValue, valueCanonical, rawValue.Span)
			if diagnostic != nil {
				r.diagnostics = append(r.diagnostics, *diagnostic)
				continue
			}
			enum.Values = append(enum.Values, ir.EnumValueIR{ID: ir.EnumValueIDFrom(valueIdentity), GoName: rawValue.GoName, WireValue: rawValue.WireValue})
		}
		r.diagnostics = append(r.diagnostics, scalar.ValidateEnum(enum, rawEnum.Span)...)
		r.compilation.Model.Enums = append(r.compilation.Model.Enums, enum)
		enumContract := ir.EnumContractIR{EnumID: enumID, GraphQLName: rawEnum.GoName}
		for index, value := range enum.Values {
			graphqlName := value.GoName
			if index < len(values) && values[index].GraphQLName != nil {
				graphqlName = *values[index].GraphQLName
			}
			enumContract.Values = append(enumContract.Values, ir.EnumValueContractIR{ValueID: value.ID, GraphQLName: graphqlName})
		}
		r.compilation.Contract.Enums = append(r.compilation.Contract.Enums, enumContract)
		wireValues := make([]string, len(enum.Values))
		for index := range enum.Values {
			wireValues[index] = enum.Values[index].WireValue
		}
		r.enums[typeKey(rawEnum.PackagePath, rawEnum.GoName)] = resolvedEnum{id: enumID, values: wireValues}
	}
}

func (r *baseResolver) resolveModels() {
	items := append([]ir.RawModelDecl(nil), r.raw.Models...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].PackagePath != items[j].PackagePath {
			return items[i].PackagePath < items[j].PackagePath
		}
		return items[i].GoName < items[j].GoName
	})
	for _, rawModel := range items {
		r.resolveModel(rawModel)
	}
}

func (r *baseResolver) resolveModel(rawModel ir.RawModelDecl) {
	explicitID, idSpan := attributeValue(rawModel.Marker, "id")
	canonical := explicitID
	if canonical == "" {
		canonical = ir.ModelIdentity(r.raw.Root.SchemaName, rawModel.PackagePath+"."+rawModel.GoName)
		idSpan = rawModel.Span
	}
	identity, diagnostic := r.ids.Register(ir.ObjectModel, canonical, idSpan)
	if diagnostic != nil {
		r.diagnostics = append(r.diagnostics, *diagnostic)
		return
	}
	modelID := ir.ModelIDFrom(identity)
	table, _ := attributeValue(rawModel.Marker, "table")
	model := ir.ModelDeclIR{
		ID: modelID, CanonicalIdentity: identity.Canonical, Go: ir.GoNamedTypeIR{PackagePath: rawModel.PackagePath, Name: rawModel.GoName},
		LogicalName: rawModel.GoName, Table: ir.TableBindingIR{PhysicalName: ir.SQLIdentifier(table)},
	}
	graphqlName, _ := attributeValue(rawModel.Marker, "graphql")
	if graphqlName == "" {
		graphqlName = rawModel.GoName
	}
	contract := ir.ModelContractIR{ModelID: modelID, GraphQLName: graphqlName, Exposed: true}

	for ordinal, rawField := range rawModel.Fields {
		if hasAttribute(rawField.GolemAttrs, "relation") || rawField.DBTag == nil || *rawField.DBTag == "-" {
			continue
		}
		field, mode, diagnostics := r.resolveField(modelID, rawField, uint32(ordinal))
		r.diagnostics = append(r.diagnostics, diagnostics...)
		if field == nil {
			continue
		}
		model.Fields = append(model.Fields, *field)
		contract.Fields = append(contract.Fields, mode)
	}
	r.compilation.Model.Models = append(r.compilation.Model.Models, model)
	r.compilation.Contract.Models = append(r.compilation.Contract.Models, contract)
}

func (r *baseResolver) resolveField(modelID ir.ModelID, rawField ir.RawFieldDecl, ordinal uint32) (*ir.FieldIR, ir.FieldContractIR, []ir.Diagnostic) {
	var diagnostics []ir.Diagnostic
	explicitID, idSpan := attributeValue(rawField.GolemAttrs, "id")
	canonical := explicitID
	if canonical == "" {
		canonical = ir.OwnedIdentity(string(modelID), rawField.GoName)
		idSpan = rawField.Span
	}
	identity, diagnostic := r.ids.Register(ir.ObjectField, canonical, idSpan)
	if diagnostic != nil {
		return nil, ir.FieldContractIR{}, []ir.Diagnostic{*diagnostic}
	}
	fieldID := ir.FieldIDFrom(identity)
	logicalType, nullable, kind, enumValues, typeDiagnostics := r.resolveGoType(rawField.GoType)
	diagnostics = append(diagnostics, typeDiagnostics...)
	if hasErrors(typeDiagnostics) {
		return nil, ir.FieldContractIR{}, diagnostics
	}
	if override, span := attributeValue(rawField.GolemAttrs, "type"); override != "" {
		var overrideDiagnostics []ir.Diagnostic
		logicalType, overrideDiagnostics = applyTypeOverride(logicalType, override, span)
		diagnostics = append(diagnostics, overrideDiagnostics...)
	}
	normalizedType, typeDiagnostics := scalar.NormalizeType(logicalType, rawField.GoType.Span)
	diagnostics = append(diagnostics, typeDiagnostics...)
	logicalType = normalizedType
	var defaultValue *ir.DefaultIR
	if token, span := attributeValue(rawField.GolemAttrs, "default"); token != "" {
		resolved, defaultDiagnostics := scalar.ResolveDefault(logicalType, token, scalar.DefaultContext{EnumValues: enumValues, Span: span})
		defaultValue = resolved
		diagnostics = append(diagnostics, defaultDiagnostics...)
	}
	updated := hasAttribute(rawField.GolemAttrs, "updated")
	if updated {
		diagnostics = append(diagnostics, scalar.ValidateUpdated(logicalType, nullable, defaultValue, rawField.Span)...)
	}
	modes, modeDiagnostics := resolveModes(rawField)
	diagnostics = append(diagnostics, modeDiagnostics...)
	field := &ir.FieldIR{
		ID: fieldID, CanonicalIdentity: identity.Canonical, GoName: rawField.GoName, LogicalName: rawField.GoName,
		DeclarationOrder: ordinal, Kind: kind,
		Scalar: &ir.ScalarFieldIR{Column: ir.SQLIdentifier(*rawField.DBTag), Type: logicalType, Nullable: nullable, Default: defaultValue, Updated: updated},
	}
	graphqlName, _ := attributeValue(rawField.GolemAttrs, "graphql")
	if graphqlName == "" {
		graphqlName = graphqlcontract.LowerCamel(rawField.GoName)
	}
	return field, ir.FieldContractIR{FieldID: fieldID, GraphQLName: graphqlName, Modes: modes}, diagnostics
}

func attributeValue(attributes []ir.RawAttribute, name string) (string, ir.SourceSpan) {
	for _, attribute := range attributes {
		if attribute.Name == name && attribute.RawValue != nil {
			return *attribute.RawValue, attribute.Span
		}
	}
	return "", ir.SourceSpan{}
}

func hasAttribute(attributes []ir.RawAttribute, name string) bool {
	for _, attribute := range attributes {
		if attribute.Name == name {
			return true
		}
	}
	return false
}

func providerRank(provider ir.Provider) int {
	switch provider {
	case ir.SQLite:
		return 0
	case ir.PostgreSQL:
		return 1
	default:
		return 2
	}
}

func typeKey(packagePath, name string) string { return packagePath + "\x00" + name }

func hasErrors(diagnostics []ir.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == ir.SeverityError {
			return true
		}
	}
	return false
}

func errorf(code string, span ir.SourceSpan, format string, values ...any) ir.Diagnostic {
	return ir.NewError(code, fmt.Sprintf(format, values...), span)
}
