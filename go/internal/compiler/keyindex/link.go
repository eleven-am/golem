package keyindex

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type modelLinker struct {
	model       ir.ModelDeclIR
	raw         *ir.RawModelDecl
	registry    *ir.IDRegistry
	fields      map[ir.FieldID]ir.FieldIR
	byGoName    map[string]ir.FieldIR
	byColumn    map[string]ir.FieldIR
	physical    map[ir.SQLIdentifier]string
	keyShapes   map[string]struct{}
	selectors   map[string]ir.KeyID
	diagnostics []ir.Diagnostic
	fragment    ModelFragment
}

// Link resolves common declarations and optional already-typed advanced
// declarations into deterministic fragments. The supplied registry must be the
// compilation-unit registry so collision checks cover every object kind.
func Link(raw ir.RawDeclIR, base ir.CompilationIR, advanced []AdvancedModelDeclarations, registry *ir.IDRegistry) Result {
	if registry == nil {
		return Result{Diagnostics: []ir.Diagnostic{ir.NewError("P1_ID_REGISTRY_REQUIRED", "key/index linking requires the compilation-unit ID registry", ir.SourceSpan{})}}
	}

	rawModels := make(map[string]*ir.RawModelDecl, len(raw.Models))
	var diagnostics []ir.Diagnostic
	for index := range raw.Models {
		model := &raw.Models[index]
		key := model.PackagePath + "\x00" + model.GoName
		if _, exists := rawModels[key]; exists {
			diagnostics = append(diagnostics, ir.NewError("P1_MODEL_DUPLICATE", fmt.Sprintf("raw model %s.%s is declared more than once", model.PackagePath, model.GoName), model.Span))
			continue
		}
		rawModels[key] = model
	}
	advancedByModel := make(map[ir.ModelID][]AdvancedModelDeclarations, len(advanced))
	for _, declaration := range advanced {
		advancedByModel[declaration.ModelID] = append(advancedByModel[declaration.ModelID], declaration)
	}

	models := append([]ir.ModelDeclIR(nil), base.Model.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	fragments := make([]ModelFragment, 0, len(models))
	seenModels := make(map[ir.ModelID]struct{}, len(models))
	physicalNames := make(map[ir.SQLIdentifier]string)
	contracts := make(map[ir.ModelID]*ir.ModelContractIR, len(base.Contract.Models))
	for index := range base.Contract.Models {
		contracts[base.Contract.Models[index].ModelID] = &base.Contract.Models[index]
	}
	for _, model := range models {
		seenModels[model.ID] = struct{}{}
		key := model.Go.PackagePath + "\x00" + model.Go.Name
		linker := newModelLinker(model, contracts[model.ID], rawModels[key], registry, physicalNames)
		if linker.raw == nil {
			linker.error("P1_RAW_MODEL_MISSING", fmt.Sprintf("base model %s.%s has no RawDeclIR declaration", model.Go.PackagePath, model.Go.Name), ir.SourceSpan{})
		} else {
			linker.linkCommon()
		}
		if declarations := advancedByModel[model.ID]; len(declarations) != 0 {
			linker.linkAdvanced(mergeAdvanced(model.ID, declarations))
		}
		linker.finish()
		fragments = append(fragments, linker.fragment)
		diagnostics = append(diagnostics, linker.diagnostics...)
	}
	unknownModelIDs := make([]ir.ModelID, 0)
	for modelID := range advancedByModel {
		if _, exists := seenModels[modelID]; exists {
			continue
		}
		unknownModelIDs = append(unknownModelIDs, modelID)
	}
	sort.Slice(unknownModelIDs, func(i, j int) bool { return unknownModelIDs[i] < unknownModelIDs[j] })
	for _, modelID := range unknownModelIDs {
		declarations := advancedByModel[modelID]
		for _, declaration := range declarations {
			span := firstAdvancedSpan(declaration)
			diagnostics = append(diagnostics, ir.NewError("P1_ADVANCED_MODEL_MISSING", fmt.Sprintf("advanced declarations reference unknown model ID %s", modelID), span))
		}
	}
	sort.Slice(fragments, func(i, j int) bool { return fragments[i].ModelID < fragments[j].ModelID })
	ir.SortDiagnostics(diagnostics)
	return Result{Fragments: fragments, Diagnostics: diagnostics}
}

func newModelLinker(model ir.ModelDeclIR, contract *ir.ModelContractIR, raw *ir.RawModelDecl, registry *ir.IDRegistry, physicalNames map[ir.SQLIdentifier]string) *modelLinker {
	linker := &modelLinker{
		model: model, raw: raw, registry: registry,
		fields: make(map[ir.FieldID]ir.FieldIR), byGoName: make(map[string]ir.FieldIR), byColumn: make(map[string]ir.FieldIR),
		physical: physicalNames, keyShapes: make(map[string]struct{}), selectors: make(map[string]ir.KeyID),
		fragment: ModelFragment{ModelID: model.ID},
	}
	if contract != nil {
		for _, selector := range contract.Selectors {
			linker.selectors[selector.Name] = selector.KeyID
		}
	}
	for _, field := range model.Fields {
		linker.fields[field.ID] = field
		linker.byGoName[field.GoName] = field
		if field.Scalar != nil {
			linker.byColumn[string(field.Scalar.Column)] = field
		}
	}
	linker.reserveExistingObjects()
	return linker
}

func (linker *modelLinker) reserveExistingObjects() {
	if linker.model.PrimaryKey != nil {
		linker.physical[linker.model.PrimaryKey.PhysicalName] = "primary key"
	}
	for _, key := range linker.model.Uniques {
		linker.physical[key.PhysicalName] = "unique constraint"
	}
	for _, index := range linker.model.Indexes {
		linker.physical[index.PhysicalName] = "index"
	}
	for _, check := range linker.model.Checks {
		linker.physical[check.PhysicalName] = "check"
	}
}

func (linker *modelLinker) linkCommon() {
	rawFields := append([]ir.RawFieldDecl(nil), linker.raw.Fields...)
	sort.Slice(rawFields, func(i, j int) bool { return rawFields[i].GoName < rawFields[j].GoName })
	for _, rawField := range rawFields {
		field, exists := linker.byGoName[rawField.GoName]
		if !exists {
			if attributeSpan, declared := firstAttributeSpan(rawField.GolemAttrs, "pk", "unique"); declared {
				linker.error("P1_KEY_COMPONENT_MISSING", fmt.Sprintf("key field %s is absent from base ModelIR", rawField.GoName), attributeSpan)
			}
			continue
		}
		if attributeSpan, declared := firstAttributeSpan(rawField.GolemAttrs, "pk"); declared {
			linker.addKey(KeyDeclaration{Kind: ir.KeyPrimary, Fields: []ir.FieldID{field.ID}, Span: attributeSpan}, true)
		}
		if attributeSpan, declared := firstAttributeSpan(rawField.GolemAttrs, "unique"); declared {
			linker.addKey(KeyDeclaration{Kind: ir.KeyUnique, Fields: []ir.FieldID{field.ID}, Span: attributeSpan}, true)
		}
	}
	directives := append([]ir.RawDirectiveDecl(nil), linker.raw.Directives...)
	sort.Slice(directives, func(i, j int) bool {
		left, right := directives[i], directives[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return strings.Join(left.Components, "\x00") < strings.Join(right.Components, "\x00")
	})
	for _, directive := range directives {
		fields, valid := linker.resolveColumns(directive.Components, directive.Span, directive.Kind)
		if !valid {
			continue
		}
		switch directive.Kind {
		case "primary":
			linker.addKey(KeyDeclaration{Kind: ir.KeyPrimary, PhysicalName: ir.SQLIdentifier(directive.Name), Fields: fields, Span: directive.Span}, false)
		case "unique":
			linker.addKey(KeyDeclaration{Kind: ir.KeyUnique, PhysicalName: ir.SQLIdentifier(directive.Name), Fields: fields, Span: directive.Span}, false)
		case "index":
			keys := make([]ir.IndexKeyIR, len(fields))
			for index := range fields {
				fieldID := fields[index]
				keys[index] = ir.IndexKeyIR{Column: &fieldID, Direction: ir.SortAsc, Nulls: ir.NullsDefault}
			}
			linker.addIndex(IndexDeclaration{PhysicalName: ir.SQLIdentifier(directive.Name), Method: ir.IndexBTree, Keys: keys, Provider: ir.ProviderScopePortable, Span: directive.Span})
		default:
			linker.error("P1_DIRECTIVE_KIND", fmt.Sprintf("unsupported common directive kind %q", directive.Kind), directive.Span)
		}
	}
}

func (linker *modelLinker) linkAdvanced(declarations AdvancedModelDeclarations) {
	sort.Slice(declarations.Keys, func(i, j int) bool {
		left, right := declarations.Keys[i], declarations.Keys[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.PhysicalName != right.PhysicalName {
			return left.PhysicalName < right.PhysicalName
		}
		return fieldIDsKey(left.Fields) < fieldIDsKey(right.Fields)
	})
	sort.Slice(declarations.Indexes, func(i, j int) bool {
		return declarations.Indexes[i].PhysicalName < declarations.Indexes[j].PhysicalName
	})
	sort.Slice(declarations.Checks, func(i, j int) bool {
		return declarations.Checks[i].PhysicalName < declarations.Checks[j].PhysicalName
	})
	sort.Slice(declarations.Generated, func(i, j int) bool {
		return declarations.Generated[i].FieldID < declarations.Generated[j].FieldID
	})
	for _, key := range declarations.Keys {
		linker.addKey(key, false)
	}
	for _, index := range declarations.Indexes {
		linker.addIndex(index)
	}
	for _, check := range declarations.Checks {
		linker.addCheck(check)
	}
	for _, generated := range declarations.Generated {
		linker.addGenerated(generated)
	}
}

func (linker *modelLinker) resolveColumns(columns []string, span ir.SourceSpan, object string) ([]ir.FieldID, bool) {
	if len(columns) == 0 {
		linker.error(componentCode(object, "EMPTY"), fmt.Sprintf("%s requires at least one component", object), span)
		return nil, false
	}
	fields := make([]ir.FieldID, 0, len(columns))
	seen := make(map[ir.FieldID]struct{}, len(columns))
	valid := true
	for _, column := range columns {
		field, exists := linker.byColumn[column]
		if !exists {
			linker.error(componentCode(object, "MISSING"), fmt.Sprintf("%s component column %q does not resolve to a persisted field", object, column), span)
			valid = false
			continue
		}
		if _, duplicate := seen[field.ID]; duplicate {
			linker.error(componentCode(object, "DUPLICATE"), fmt.Sprintf("%s repeats component column %q", object, column), span)
			valid = false
			continue
		}
		seen[field.ID] = struct{}{}
		fields = append(fields, field.ID)
	}
	return fields, valid
}

func componentCode(object, suffix string) string {
	if object == "index" {
		return "P1_INDEX_COMPONENT_" + suffix
	}
	return "P1_KEY_COMPONENT_" + suffix
}

func (linker *modelLinker) addKey(declaration KeyDeclaration, generatePhysical bool) {
	if declaration.Kind != ir.KeyPrimary && declaration.Kind != ir.KeyUnique {
		linker.error("P1_KEY_KIND", fmt.Sprintf("unsupported key kind %q", declaration.Kind), declaration.Span)
		return
	}
	fields, valid := linker.validateFieldIDs(declaration.Fields, declaration.Span, "key")
	if !valid {
		return
	}
	for _, field := range fields {
		if field.Scalar == nil || field.Kind == ir.FieldRelation {
			linker.error("P1_KEY_COMPONENT_TYPE", fmt.Sprintf("field %s is not a persisted scalar key component", field.GoName), declaration.Span)
			valid = false
		}
		if declaration.Kind == ir.KeyPrimary && field.Scalar != nil && field.Scalar.Nullable {
			linker.error("P1_PRIMARY_KEY_NULLABLE", fmt.Sprintf("primary-key field %s is nullable", field.GoName), declaration.Span)
			valid = false
		}
	}
	if !valid {
		return
	}
	if declaration.Kind == ir.KeyPrimary && (linker.fragment.PrimaryKey != nil || linker.model.PrimaryKey != nil) {
		linker.error("P1_PRIMARY_KEY_DUPLICATE", fmt.Sprintf("model %s declares more than one primary key", linker.model.LogicalName), declaration.Span)
		return
	}

	shape := keyShape(declaration.Kind, declaration.Fields)
	if _, duplicate := linker.keyShapes[shape]; duplicate {
		linker.error("P1_KEY_DUPLICATE", fmt.Sprintf("model %s repeats %s key components", linker.model.LogicalName, declaration.Kind), declaration.Span)
		return
	}
	linker.keyShapes[shape] = struct{}{}

	physical := declaration.PhysicalName
	if physical == "" && generatePhysical {
		physical = linker.generatedPhysicalName(declaration.Kind, fields)
	}
	if !linker.validatePhysicalName(physical, "key", declaration.Span) {
		return
	}
	logicalName := logicalKeyName(declaration.Kind, fields)
	identity, diagnostic := linker.registry.Register(ir.ObjectKey, ir.OwnedIdentity(string(linker.model.ID), shape), declaration.Span)
	if diagnostic != nil {
		linker.diagnostics = append(linker.diagnostics, *diagnostic)
		return
	}
	key := ir.KeyIR{
		ID: ir.KeyIDFrom(identity), Kind: declaration.Kind, LogicalName: logicalName,
		PhysicalName: physical, Fields: append([]ir.FieldID(nil), declaration.Fields...),
	}
	selectorName := selectorName(fields)
	if declaration.Kind == ir.KeyPrimary || allNonNull(fields) {
		if existing, collision := linker.selectors[selectorName]; collision {
			linker.error("P1_SELECTOR_NAME_COLLISION", fmt.Sprintf("selector name %q for key %s collides with key %s", selectorName, key.ID, existing), declaration.Span)
			return
		}
		linker.selectors[selectorName] = key.ID
		linker.fragment.Selectors = append(linker.fragment.Selectors, ir.SelectorContractIR{KeyID: key.ID, Kind: key.Kind, Name: selectorName, Fields: append([]ir.FieldID(nil), declaration.Fields...)})
	}
	linker.physical[physical] = string(declaration.Kind) + " key"
	if declaration.Kind == ir.KeyPrimary {
		linker.fragment.PrimaryKey = &key
	} else {
		linker.fragment.Uniques = append(linker.fragment.Uniques, key)
	}
	leading := declaration.Fields[0]
	keyID := key.ID
	linker.fragment.EqualityIndexes = append(linker.fragment.EqualityIndexes, ir.EqualityIndexIR{FieldID: leading, Kind: ir.EqualityViaKey, KeyID: &keyID})
}

func (linker *modelLinker) addIndex(declaration IndexDeclaration) {
	if len(declaration.Keys) == 0 {
		linker.error("P1_INDEX_EMPTY", "index requires at least one ordered key", declaration.Span)
		return
	}
	if declaration.Method == "" {
		declaration.Method = ir.IndexBTree
	}
	if declaration.Provider == "" {
		declaration.Provider = ir.ProviderScopePortable
	}
	declaration.Keys = append([]ir.IndexKeyIR(nil), declaration.Keys...)
	declaration.Include = append([]ir.FieldID(nil), declaration.Include...)
	if declaration.Predicate != nil {
		predicate := *declaration.Predicate
		declaration.Predicate = &predicate
	}
	valid := true
	seenColumns := make(map[ir.FieldID]struct{})
	for index := range declaration.Keys {
		key := &declaration.Keys[index]
		if (key.Column == nil) == (key.Expr == nil) {
			linker.error("P1_INDEX_KEY_SHAPE", "index key must set exactly one of Column or Expr", declaration.Span)
			valid = false
			continue
		}
		if key.Column != nil {
			field, exists := linker.fields[*key.Column]
			if !exists {
				linker.error("P1_INDEX_COMPONENT_MISSING", fmt.Sprintf("index references unknown field ID %s", *key.Column), declaration.Span)
				valid = false
			} else if field.Scalar == nil || field.Kind == ir.FieldRelation {
				linker.error("P1_INDEX_COMPONENT_TYPE", fmt.Sprintf("field %s is not a persisted scalar index component", field.GoName), declaration.Span)
				valid = false
			}
			if _, duplicate := seenColumns[*key.Column]; duplicate {
				linker.error("P1_INDEX_COMPONENT_DUPLICATE", fmt.Sprintf("index repeats field ID %s", *key.Column), declaration.Span)
				valid = false
			}
			seenColumns[*key.Column] = struct{}{}
		} else if key.Expr.Kind == "" {
			linker.error("P1_INDEX_EXPRESSION_EMPTY", "index expression key requires a typed expression", declaration.Span)
			valid = false
		}
		if key.Direction == "" {
			key.Direction = ir.SortAsc
		} else if key.Direction != ir.SortAsc && key.Direction != ir.SortDesc {
			linker.error("P1_INDEX_DIRECTION", fmt.Sprintf("unknown index direction %q", key.Direction), declaration.Span)
			valid = false
		}
		if key.Nulls == "" {
			key.Nulls = ir.NullsDefault
		} else if key.Nulls != ir.NullsDefault && key.Nulls != ir.NullsFirst && key.Nulls != ir.NullsLast {
			linker.error("P1_INDEX_NULLS_ORDER", fmt.Sprintf("unknown index null ordering %q", key.Nulls), declaration.Span)
			valid = false
		}
	}
	includeSeen := make(map[ir.FieldID]struct{}, len(declaration.Include))
	for _, fieldID := range declaration.Include {
		field, exists := linker.fields[fieldID]
		if !exists {
			linker.error("P1_INDEX_INCLUDE_MISSING", fmt.Sprintf("index includes unknown field ID %s", fieldID), declaration.Span)
			valid = false
		} else if field.Scalar == nil || field.Kind == ir.FieldRelation {
			linker.error("P1_INDEX_INCLUDE_TYPE", fmt.Sprintf("field %s is not a persisted scalar include field", field.GoName), declaration.Span)
			valid = false
		}
		if _, duplicate := includeSeen[fieldID]; duplicate {
			linker.error("P1_INDEX_INCLUDE_DUPLICATE", fmt.Sprintf("index repeats include field ID %s", fieldID), declaration.Span)
			valid = false
		}
		includeSeen[fieldID] = struct{}{}
	}
	if declaration.Predicate != nil && declaration.Predicate.Kind == "" {
		linker.error("P1_INDEX_PREDICATE_EMPTY", "partial index requires a typed predicate", declaration.Span)
		valid = false
	}
	if !validProviderScope(declaration.Provider) {
		linker.error("P1_PROVIDER_SCOPE", fmt.Sprintf("unknown index provider scope %q", declaration.Provider), declaration.Span)
		valid = false
	}
	if !linker.validatePhysicalName(declaration.PhysicalName, "index", declaration.Span) {
		valid = false
	}
	if !valid {
		return
	}
	payload := struct {
		Unique    bool
		Method    ir.IndexMethod
		Keys      []ir.IndexKeyIR
		Include   []ir.FieldID
		Predicate *ir.SchemaPredicateIR
		Provider  ir.ProviderScope
	}{declaration.Unique, declaration.Method, declaration.Keys, declaration.Include, declaration.Predicate, declaration.Provider}
	encoded, _ := json.Marshal(payload)
	identity, diagnostic := linker.registry.Register(ir.ObjectIndex, ir.OwnedIdentity(string(linker.model.ID), "index\x00"+string(encoded)), declaration.Span)
	if diagnostic != nil {
		linker.diagnostics = append(linker.diagnostics, *diagnostic)
		return
	}
	index := ir.IndexIR{
		ID: ir.IndexIDFrom(identity), ModelID: linker.model.ID, PhysicalName: declaration.PhysicalName,
		Unique: declaration.Unique, Method: declaration.Method, Keys: append([]ir.IndexKeyIR(nil), declaration.Keys...),
		Include: append([]ir.FieldID(nil), declaration.Include...), Predicate: declaration.Predicate, Provider: declaration.Provider,
	}
	linker.physical[declaration.PhysicalName] = "index"
	linker.fragment.Indexes = append(linker.fragment.Indexes, index)
	if index.Method == ir.IndexBTree && index.Keys[0].Column != nil && index.Keys[0].Expr == nil {
		indexID := index.ID
		linker.fragment.EqualityIndexes = append(linker.fragment.EqualityIndexes, ir.EqualityIndexIR{FieldID: *index.Keys[0].Column, Kind: ir.EqualityViaIndex, IndexID: &indexID})
	}
}

func (linker *modelLinker) addCheck(declaration CheckDeclaration) {
	if declaration.Predicate.Kind == "" {
		linker.error("P1_CHECK_PREDICATE_EMPTY", "check requires a typed predicate", declaration.Span)
		return
	}
	if declaration.Provider == "" {
		declaration.Provider = ir.ProviderScopePortable
	}
	if !validProviderScope(declaration.Provider) {
		linker.error("P1_PROVIDER_SCOPE", fmt.Sprintf("unknown check provider scope %q", declaration.Provider), declaration.Span)
		return
	}
	if !linker.validatePhysicalName(declaration.PhysicalName, "check", declaration.Span) {
		return
	}
	predicate, _ := json.Marshal(declaration.Predicate)
	canonical := "check\x00" + string(declaration.Provider) + "\x00" + string(predicate)
	identity, diagnostic := linker.registry.Register(ir.ObjectCheck, ir.OwnedIdentity(string(linker.model.ID), canonical), declaration.Span)
	if diagnostic != nil {
		linker.diagnostics = append(linker.diagnostics, *diagnostic)
		return
	}
	check := ir.CheckIR{ID: ir.CheckIDFrom(identity), PhysicalName: declaration.PhysicalName, Predicate: declaration.Predicate, Provider: declaration.Provider}
	linker.physical[declaration.PhysicalName] = "check"
	linker.fragment.Checks = append(linker.fragment.Checks, check)
}

func (linker *modelLinker) addGenerated(declaration GeneratedDeclaration) {
	field, exists := linker.fields[declaration.FieldID]
	if !exists {
		linker.error("P1_GENERATED_FIELD_MISSING", fmt.Sprintf("generated declaration references unknown field ID %s", declaration.FieldID), declaration.Span)
		return
	}
	if field.Scalar == nil || field.Kind == ir.FieldRelation {
		linker.error("P1_GENERATED_FIELD_TYPE", fmt.Sprintf("field %s is not a persisted scalar", field.GoName), declaration.Span)
		return
	}
	if field.Scalar.Generation != nil {
		linker.error("P1_GENERATED_DUPLICATE", fmt.Sprintf("field %s already has a generated expression", field.GoName), declaration.Span)
		return
	}
	for _, generated := range linker.fragment.Generated {
		if generated.FieldID == declaration.FieldID {
			linker.error("P1_GENERATED_DUPLICATE", fmt.Sprintf("field %s has multiple generated expressions", field.GoName), declaration.Span)
			return
		}
	}
	if field.Scalar.Default != nil || field.Scalar.Updated {
		linker.error("P1_GENERATED_WRITE_CONFLICT", fmt.Sprintf("generated field %s cannot have a default or updated behavior", field.GoName), declaration.Span)
		return
	}
	if declaration.Generation.Expr.Kind == "" {
		linker.error("P1_GENERATED_EXPRESSION_EMPTY", "generated column requires a typed expression", declaration.Span)
		return
	}
	if declaration.Generation.Storage == "" {
		declaration.Generation.Storage = ir.GeneratedStored
	} else if declaration.Generation.Storage != ir.GeneratedStored && declaration.Generation.Storage != ir.GeneratedVirtual {
		linker.error("P1_GENERATED_STORAGE", fmt.Sprintf("unknown generated storage %q", declaration.Generation.Storage), declaration.Span)
		return
	}
	if declaration.Generation.Provider == "" {
		declaration.Generation.Provider = ir.ProviderScopePortable
	}
	if !validProviderScope(declaration.Generation.Provider) {
		linker.error("P1_PROVIDER_SCOPE", fmt.Sprintf("unknown generated-column provider scope %q", declaration.Generation.Provider), declaration.Span)
		return
	}
	if !declaration.ExpressionProvenNonNull && !field.Scalar.Nullable {
		linker.error("P1_GENERATED_NULLABILITY", fmt.Sprintf("generated expression for non-null field %s may produce null", field.GoName), declaration.Span)
		return
	}
	linker.fragment.Generated = append(linker.fragment.Generated, GeneratedAssignment{FieldID: declaration.FieldID, Generation: declaration.Generation})
}

func (linker *modelLinker) validateFieldIDs(ids []ir.FieldID, span ir.SourceSpan, object string) ([]ir.FieldIR, bool) {
	if len(ids) == 0 {
		linker.error("P1_KEY_EMPTY", fmt.Sprintf("%s requires at least one ordered field", object), span)
		return nil, false
	}
	fields := make([]ir.FieldIR, 0, len(ids))
	seen := make(map[ir.FieldID]struct{}, len(ids))
	valid := true
	for _, id := range ids {
		field, exists := linker.fields[id]
		if !exists {
			linker.error("P1_KEY_COMPONENT_MISSING", fmt.Sprintf("key references unknown field ID %s", id), span)
			valid = false
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			linker.error("P1_KEY_COMPONENT_DUPLICATE", fmt.Sprintf("key repeats field ID %s", id), span)
			valid = false
			continue
		}
		seen[id] = struct{}{}
		fields = append(fields, field)
	}
	return fields, valid
}

func (linker *modelLinker) generatedPhysicalName(kind ir.KeyKind, fields []ir.FieldIR) ir.SQLIdentifier {
	table := string(linker.model.Table.PhysicalName)
	if kind == ir.KeyPrimary {
		return ir.SQLIdentifier("pk_" + table)
	}
	parts := []string{"uq", table}
	for _, field := range fields {
		parts = append(parts, string(field.Scalar.Column))
	}
	return ir.SQLIdentifier(strings.Join(parts, "_"))
}

func (linker *modelLinker) validatePhysicalName(name ir.SQLIdentifier, kind string, span ir.SourceSpan) bool {
	if !identifierPattern.MatchString(string(name)) {
		linker.error("P1_PHYSICAL_NAME_INVALID", fmt.Sprintf("%s physical name %q is not an unquoted identifier", kind, name), span)
		return false
	}
	if existing, duplicate := linker.physical[name]; duplicate {
		linker.error("P1_PHYSICAL_NAME_DUPLICATE", fmt.Sprintf("%s physical name %q collides with existing %s", kind, name, existing), span)
		return false
	}
	return true
}

func (linker *modelLinker) finish() {
	if linker.fragment.Uniques == nil {
		linker.fragment.Uniques = []ir.KeyIR{}
	}
	if linker.fragment.Indexes == nil {
		linker.fragment.Indexes = []ir.IndexIR{}
	}
	if linker.fragment.Checks == nil {
		linker.fragment.Checks = []ir.CheckIR{}
	}
	if linker.fragment.Generated == nil {
		linker.fragment.Generated = []GeneratedAssignment{}
	}
	if linker.fragment.Selectors == nil {
		linker.fragment.Selectors = []ir.SelectorContractIR{}
	}
	if linker.fragment.EqualityIndexes == nil {
		linker.fragment.EqualityIndexes = []ir.EqualityIndexIR{}
	}
	sort.Slice(linker.fragment.Uniques, func(i, j int) bool { return linker.fragment.Uniques[i].ID < linker.fragment.Uniques[j].ID })
	sort.Slice(linker.fragment.Indexes, func(i, j int) bool { return linker.fragment.Indexes[i].ID < linker.fragment.Indexes[j].ID })
	sort.Slice(linker.fragment.Checks, func(i, j int) bool { return linker.fragment.Checks[i].ID < linker.fragment.Checks[j].ID })
	sort.Slice(linker.fragment.Generated, func(i, j int) bool {
		return linker.fragment.Generated[i].FieldID < linker.fragment.Generated[j].FieldID
	})
	sort.Slice(linker.fragment.Selectors, func(i, j int) bool {
		if linker.fragment.Selectors[i].Name != linker.fragment.Selectors[j].Name {
			return linker.fragment.Selectors[i].Name < linker.fragment.Selectors[j].Name
		}
		return linker.fragment.Selectors[i].KeyID < linker.fragment.Selectors[j].KeyID
	})
	sort.Slice(linker.fragment.EqualityIndexes, func(i, j int) bool {
		left, right := linker.fragment.EqualityIndexes[i], linker.fragment.EqualityIndexes[j]
		if left.FieldID != right.FieldID {
			return left.FieldID < right.FieldID
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return equalityIdentity(left) < equalityIdentity(right)
	})
	ir.SortDiagnostics(linker.diagnostics)
}

func (linker *modelLinker) error(code, message string, span ir.SourceSpan) {
	linker.diagnostics = append(linker.diagnostics, ir.NewError(code, message, span))
}

func firstAttributeSpan(attributes []ir.RawAttribute, names ...string) (ir.SourceSpan, bool) {
	for _, attribute := range attributes {
		for _, name := range names {
			if attribute.Name == name {
				return attribute.Span, true
			}
		}
	}
	return ir.SourceSpan{}, false
}

func keyShape(kind ir.KeyKind, fields []ir.FieldID) string {
	return string(kind) + "\x00" + fieldIDsKey(fields)
}

func fieldIDsKey(fields []ir.FieldID) string {
	parts := make([]string, len(fields))
	for index, field := range fields {
		parts[index] = string(field)
	}
	return strings.Join(parts, "\x00")
}

func logicalKeyName(kind ir.KeyKind, fields []ir.FieldIR) string {
	if kind == ir.KeyPrimary {
		return "primary"
	}
	return selectorName(fields)
}

func selectorName(fields []ir.FieldIR) string {
	parts := make([]string, len(fields))
	for index, field := range fields {
		parts[index] = field.LogicalName
		if parts[index] == "" {
			parts[index] = field.GoName
		}
	}
	return strings.Join(parts, "_")
}

func allNonNull(fields []ir.FieldIR) bool {
	for _, field := range fields {
		if field.Scalar == nil || field.Scalar.Nullable {
			return false
		}
	}
	return true
}

func equalityIdentity(value ir.EqualityIndexIR) string {
	if value.KeyID != nil {
		return string(*value.KeyID)
	}
	if value.IndexID != nil {
		return string(*value.IndexID)
	}
	return ""
}

func firstAdvancedSpan(declaration AdvancedModelDeclarations) ir.SourceSpan {
	if len(declaration.Keys) != 0 {
		return declaration.Keys[0].Span
	}
	if len(declaration.Indexes) != 0 {
		return declaration.Indexes[0].Span
	}
	if len(declaration.Checks) != 0 {
		return declaration.Checks[0].Span
	}
	if len(declaration.Generated) != 0 {
		return declaration.Generated[0].Span
	}
	return ir.SourceSpan{}
}

func validProviderScope(scope ir.ProviderScope) bool {
	return scope == ir.ProviderScopePortable || scope == ir.ProviderScopeSQLite || scope == ir.ProviderScopePostgreSQL
}

func mergeAdvanced(modelID ir.ModelID, declarations []AdvancedModelDeclarations) AdvancedModelDeclarations {
	merged := AdvancedModelDeclarations{ModelID: modelID}
	for _, declaration := range declarations {
		merged.Keys = append(merged.Keys, declaration.Keys...)
		merged.Indexes = append(merged.Indexes, declaration.Indexes...)
		merged.Checks = append(merged.Checks, declaration.Checks...)
		merged.Generated = append(merged.Generated, declaration.Generated...)
	}
	return merged
}
