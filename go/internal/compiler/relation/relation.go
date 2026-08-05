// Package relation links authored relation fields into canonical logical edges.
// It is deliberately provider-neutral: physical SQL lowering happens after the
// ModelIR has been assembled.
package relation

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	codeDBTag            = "P1_RELATION_DB_TAG"
	codeImplicitM2M      = "P1_RELATION_IMPLICIT_M2M_UNAVAILABLE"
	codeKind             = "P1_RELATION_KIND"
	codeType             = "P1_RELATION_TYPE"
	codeTargetMissing    = "P1_RELATION_TARGET_MISSING"
	codeMapping          = "P1_RELATION_MAPPING"
	codeMappingField     = "P1_RELATION_MAPPING_FIELD"
	codeArity            = "P1_RELATION_ARITY"
	codeTypeMismatch     = "P1_RELATION_TYPE_MISMATCH"
	codeLocalNullability = "P1_RELATION_LOCAL_NULLABILITY"
	codeRemoteKey        = "P1_RELATION_REMOTE_KEY"
	codeNameRequired     = "P1_RELATION_NAME_REQUIRED"
	codeDuplicateMapping = "P1_RELATION_DUPLICATE_MAPPING"
	codeInverseAmbiguous = "P1_RELATION_INVERSE_AMBIGUOUS"
	codeInverseMissing   = "P1_RELATION_INVERSE_OWNER_MISSING"
	codeOneToOneUnique   = "P1_RELATION_ONE_TO_ONE_UNIQUE"
	codeWriteOnly        = "P1_RELATION_WRITE_ONLY"
	codeRegistryMissing  = "P1_ID_REGISTRY_REQUIRED"
	codeFieldConflict    = "P1_RELATION_FIELD_CONFLICT"
)

// ModelFragment contains relation fields to merge into one base ModelDeclIR.
// It never repeats or mutates scalar fields from the base resolver.
type ModelFragment struct {
	ModelID ir.ModelID   `json:"modelId"`
	Fields  []ir.FieldIR `json:"fields"`
}

// Result is the provider-neutral output of Link. All slices are canonicalized.
type Result struct {
	Relations   []ir.RelationIR `json:"relations"`
	Fragments   []ModelFragment `json:"fragments"`
	Diagnostics []ir.Diagnostic `json:"diagnostics"`
}

type modelKey struct {
	pkg  string
	name string
}

type declaration struct {
	ownerRaw       *ir.RawModelDecl
	owner          *ir.ModelDeclIR
	target         *ir.ModelDeclIR
	raw            *ir.RawFieldDecl
	order          uint32
	kind           ir.RelationKind
	explicitName   string
	local          []ir.FieldIR
	remote         []ir.FieldIR
	fieldID        ir.FieldID
	fieldCanonical string
	valid          bool
	used           bool
	duplicate      bool
}

// Prelink resolves relation identities, shapes, mappings, and inverse pairing.
// Key-dependent eligibility checks are deliberately deferred to Validate.
func Prelink(raw ir.RawDeclIR, base ir.ModelIR, registry *ir.IDRegistry) Result {
	var result Result
	if registry == nil {
		result.Diagnostics = []ir.Diagnostic{ir.NewError(codeRegistryMissing, "relation linking requires the generation unit ID registry", raw.Root.Span)}
		return canonicalResult(result)
	}

	baseModels := make(map[modelKey]*ir.ModelDeclIR, len(base.Models))
	for i := range base.Models {
		model := &base.Models[i]
		baseModels[modelKey{pkg: model.Go.PackagePath, name: model.Go.Name}] = model
	}

	rawModels := append([]ir.RawModelDecl(nil), raw.Models...)
	sort.Slice(rawModels, func(i, j int) bool {
		if rawModels[i].PackagePath != rawModels[j].PackagePath {
			return rawModels[i].PackagePath < rawModels[j].PackagePath
		}
		return rawModels[i].GoName < rawModels[j].GoName
	})

	declarations := make([]*declaration, 0)
	for rawModelIndex := range rawModels {
		rawModel := &rawModels[rawModelIndex]
		owner := baseModels[modelKey{pkg: rawModel.PackagePath, name: rawModel.GoName}]
		if owner == nil {
			continue // The base model resolver owns missing source-model diagnostics.
		}
		for fieldIndex := range rawModel.Fields {
			rawField := &rawModel.Fields[fieldIndex]
			if _, ok := attribute(rawField, "relation"); !ok {
				continue
			}
			declaration := parseDeclaration(rawModel, owner, rawField, uint32(fieldIndex), baseModels, &result.Diagnostics)
			if declaration != nil {
				declarations = append(declarations, declaration)
			}
		}
	}

	sort.Slice(declarations, func(i, j int) bool { return declarationLess(declarations[i], declarations[j]) })
	requireNames(declarations, &result.Diagnostics)
	markDuplicateMappings(declarations, &result.Diagnostics)

	fragments := make(map[ir.ModelID][]ir.FieldIR)
	fieldIDs := make(map[*declaration]ir.FieldID, len(declarations))
	for _, declaration := range declarations {
		if !declaration.valid || declaration.duplicate {
			continue
		}
		fieldID, canonical, ok := relationFieldID(declaration, registry, &result.Diagnostics)
		if !ok {
			declaration.valid = false
			continue
		}
		declaration.fieldID = fieldID
		declaration.fieldCanonical = canonical
		fieldIDs[declaration] = fieldID
	}

	owners := make([]*declaration, 0)
	for _, declaration := range declarations {
		if declaration.valid && !declaration.duplicate && declaration.kind == ir.RelationBelongsTo {
			owners = append(owners, declaration)
		}
	}

	for _, source := range owners {
		inverses := compatibleInverses(source, declarations)
		if len(inverses) > 1 {
			diagnostic := ir.NewError(codeInverseAmbiguous, fmt.Sprintf("relation %s.%s has %d compatible inverse declarations; add matching name attributes", source.owner.Go.Name, source.raw.GoName, len(inverses)), source.raw.Span)
			for _, inverse := range inverses {
				diagnostic.Related = append(diagnostic.Related, ir.DiagnosticLabel{Message: "compatible inverse", Span: inverse.raw.Span, StableID: string(inverse.fieldID)})
			}
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			continue
		}

		var inverse *declaration
		if len(inverses) == 1 {
			inverse = inverses[0]
			inverse.used = true
		}

		relationIdentity, diagnostic := registry.Register(ir.ObjectRelation, stableCanonical(source), source.raw.Span)
		if diagnostic != nil {
			result.Diagnostics = append(result.Diagnostics, *diagnostic)
			continue
		}
		relationID := ir.RelationIDFrom(relationIdentity)
		foreignKeyIdentity, diagnostic := registry.Register(ir.ObjectForeignKey, foreignKeyCanonical(relationID, source), source.raw.Span)
		if diagnostic != nil {
			result.Diagnostics = append(result.Diagnostics, *diagnostic)
			continue
		}

		cardinality := ir.RelationMany
		if inverse != nil && inverse.kind == ir.RelationHasOne {
			cardinality = ir.RelationOne
		}
		edge := ir.RelationIR{
			ID:           relationID,
			Name:         relationName(source),
			SourceModel:  source.owner.ID,
			TargetModel:  source.target.ID,
			SourceField:  source.fieldID,
			Cardinality:  cardinality,
			LocalFields:  fieldIDList(source.local),
			RemoteFields: fieldIDList(source.remote),
			ForeignKey: &ir.ForeignKeyIR{
				ID:           ir.ForeignKeyIDFrom(foreignKeyIdentity),
				PhysicalName: defaultForeignKeyName(source),
				OnUpdate:     ir.ActionNoAction,
				OnDelete:     ir.ActionNoAction,
				Match:        ir.MatchSimple,
				Deferrable:   ir.NotDeferrable,
			},
		}
		if inverse != nil {
			inverseID := inverse.fieldID
			edge.InverseField = &inverseID
		}
		result.Relations = append(result.Relations, edge)

		fragments[source.owner.ID] = append(fragments[source.owner.ID], relationFieldFragment(source, relationID, ir.RelationSource))
		if inverse != nil {
			fragments[inverse.owner.ID] = append(fragments[inverse.owner.ID], relationFieldFragment(inverse, relationID, ir.RelationInverse))
		}
	}

	for _, declaration := range declarations {
		if !declaration.valid || declaration.duplicate || declaration.kind == ir.RelationBelongsTo || declaration.used {
			continue
		}
		result.Diagnostics = append(result.Diagnostics, ir.NewError(codeInverseMissing, fmt.Sprintf("inverse relation %s.%s has no matching belongs_to owner", declaration.owner.Go.Name, declaration.raw.GoName), declaration.raw.Span))
	}

	for modelID, fields := range fragments {
		result.Fragments = append(result.Fragments, ModelFragment{ModelID: modelID, Fields: fields})
	}
	return canonicalResult(result)
}

// Link is the compatibility one-shot linker for callers whose final key
// inventory is already present.
func Link(raw ir.RawDeclIR, base ir.ModelIR, registry *ir.IDRegistry) Result {
	result := Prelink(raw, base, registry)
	if len(result.Diagnostics) == 0 {
		result.Diagnostics = append(result.Diagnostics, Validate(base, result.Relations)...)
	}
	return canonicalResult(result)
}

// Validate checks invariants that depend on the final primary/unique inventory.
// It performs no registration and does not mutate either input.
func Validate(base ir.ModelIR, relations []ir.RelationIR) []ir.Diagnostic {
	models := make(map[ir.ModelID]*ir.ModelDeclIR, len(base.Models))
	for index := range base.Models {
		models[base.Models[index].ID] = &base.Models[index]
	}
	var diagnostics []ir.Diagnostic
	for _, edge := range relations {
		source, target := models[edge.SourceModel], models[edge.TargetModel]
		if source == nil || target == nil {
			continue
		}
		if !formsUniqueKey(target, edge.RemoteFields, true) {
			diagnostics = append(diagnostics, ir.NewError(codeRemoteKey, fmt.Sprintf("relation %s references fields that are not a primary key or non-null unique key", edge.Name), ir.SourceSpan{}))
		}
		if edge.Cardinality == ir.RelationOne && !formsUniqueKey(source, edge.LocalFields, false) {
			diagnostics = append(diagnostics, ir.NewError(codeOneToOneUnique, fmt.Sprintf("one-to-one relation %s requires local fields to form a unique key", edge.Name), ir.SourceSpan{}))
		}
	}
	ir.SortDiagnostics(diagnostics)
	return diagnostics
}

func parseDeclaration(rawModel *ir.RawModelDecl, owner *ir.ModelDeclIR, rawField *ir.RawFieldDecl, order uint32, models map[modelKey]*ir.ModelDeclIR, diagnostics *[]ir.Diagnostic) *declaration {
	relationValue, _ := attribute(rawField, "relation")
	if relationValue == nil {
		*diagnostics = append(*diagnostics, ir.NewError(codeKind, "relation attribute requires a value", rawField.Span))
		return nil
	}
	if *relationValue == "many_to_many" {
		*diagnostics = append(*diagnostics, ir.NewError(codeImplicitM2M, fmt.Sprintf("implicit many-to-many relation %s.%s is unavailable; declare an explicit join model", owner.Go.Name, rawField.GoName), rawField.Span))
		return nil
	}

	kind, ok := relationKind(*relationValue)
	if !ok {
		*diagnostics = append(*diagnostics, ir.NewError(codeKind, fmt.Sprintf("unknown relation kind %q", *relationValue), rawField.Span))
		return nil
	}
	decl := &declaration{ownerRaw: rawModel, owner: owner, raw: rawField, order: order, kind: kind, valid: true}
	if name, exists := attribute(rawField, "name"); exists && name != nil {
		decl.explicitName = *name
	}
	if rawField.DBTag == nil || *rawField.DBTag != "-" {
		*diagnostics = append(*diagnostics, ir.NewError(codeDBTag, fmt.Sprintf("relation field %s.%s must use db:\"-\"", owner.Go.Name, rawField.GoName), rawField.Span))
		decl.valid = false
	}
	if _, exists := attribute(rawField, "writeonly"); exists {
		*diagnostics = append(*diagnostics, ir.NewError(codeWriteOnly, fmt.Sprintf("relation field %s.%s cannot be write-only", owner.Go.Name, rawField.GoName), rawField.Span))
		decl.valid = false
	}

	targetType, shapeOK := relationTarget(rawField.GoType, kind)
	if !shapeOK {
		*diagnostics = append(*diagnostics, ir.NewError(codeType, fmt.Sprintf("relation field %s.%s has incompatible Go type %q", owner.Go.Name, rawField.GoName, rawField.TypeSyntax), rawField.Span))
		decl.valid = false
		return decl
	}
	decl.target = models[modelKey{pkg: targetType.PackagePath, name: targetType.GoName}]
	if decl.target == nil {
		*diagnostics = append(*diagnostics, ir.NewError(codeTargetMissing, fmt.Sprintf("relation target %s.%s is not registered in the schema root", targetType.PackagePath, targetType.GoName), targetType.Span))
		decl.valid = false
		return decl
	}

	localNames, localOK := mappingAttribute(rawField, "fields")
	remoteNames, remoteOK := mappingAttribute(rawField, "references")
	if !localOK || !remoteOK || len(localNames) == 0 || len(remoteNames) == 0 {
		*diagnostics = append(*diagnostics, ir.NewError(codeMapping, fmt.Sprintf("relation %s.%s requires non-empty ordered fields and references mappings", owner.Go.Name, rawField.GoName), rawField.Span))
		decl.valid = false
		return decl
	}
	if len(localNames) != len(remoteNames) {
		*diagnostics = append(*diagnostics, ir.NewError(codeArity, fmt.Sprintf("relation %s.%s maps %d local fields to %d referenced fields", owner.Go.Name, rawField.GoName, len(localNames), len(remoteNames)), rawField.Span))
		decl.valid = false
		return decl
	}
	if duplicate := firstDuplicate(localNames); duplicate != "" {
		*diagnostics = append(*diagnostics, ir.NewError(codeDuplicateMapping, fmt.Sprintf("relation %s.%s repeats local mapping column %q", owner.Go.Name, rawField.GoName, duplicate), rawField.Span))
		decl.valid = false
		return decl
	}
	if duplicate := firstDuplicate(remoteNames); duplicate != "" {
		*diagnostics = append(*diagnostics, ir.NewError(codeDuplicateMapping, fmt.Sprintf("relation %s.%s repeats referenced mapping column %q", owner.Go.Name, rawField.GoName, duplicate), rawField.Span))
		decl.valid = false
		return decl
	}
	decl.local = resolveColumns(owner, localNames, rawField.Span, diagnostics)
	decl.remote = resolveColumns(decl.target, remoteNames, rawField.Span, diagnostics)
	if len(decl.local) != len(localNames) || len(decl.remote) != len(remoteNames) {
		decl.valid = false
		return decl
	}
	for index := range decl.local {
		if !logicalTypesEqual(decl.local[index].Scalar.Type, decl.remote[index].Scalar.Type) {
			*diagnostics = append(*diagnostics, ir.NewError(codeTypeMismatch, fmt.Sprintf("relation %s.%s maps incompatible columns %s and %s at component %d", owner.Go.Name, rawField.GoName, decl.local[index].Scalar.Column, decl.remote[index].Scalar.Column, index+1), rawField.Span))
			decl.valid = false
		}
	}
	if len(decl.local) > 1 && mixedNullability(decl.local) {
		*diagnostics = append(*diagnostics, ir.NewError(codeLocalNullability, fmt.Sprintf("composite relation %s.%s must use either all-nullable or all-non-null local fields", owner.Go.Name, rawField.GoName), rawField.Span))
		decl.valid = false
	}
	return decl
}

func relationKind(value string) (ir.RelationKind, bool) {
	switch value {
	case "belongs_to":
		return ir.RelationBelongsTo, true
	case "has_one":
		return ir.RelationHasOne, true
	case "has_many":
		return ir.RelationHasMany, true
	default:
		return "", false
	}
}

func relationTarget(raw ir.RawGoTypeRef, kind ir.RelationKind) (ir.RawGoTypeRef, bool) {
	want := ir.RawGoTypePointer
	if kind == ir.RelationHasMany {
		want = ir.RawGoTypeSlice
	}
	if raw.Kind != want || len(raw.Args) != 1 {
		return ir.RawGoTypeRef{}, false
	}
	target := raw.Args[0]
	if target.Kind != ir.RawGoTypeNamed || target.PackagePath == "" || target.GoName == "" || len(target.Args) != 0 {
		return ir.RawGoTypeRef{}, false
	}
	return target, true
}

func resolveColumns(model *ir.ModelDeclIR, names []string, span ir.SourceSpan, diagnostics *[]ir.Diagnostic) []ir.FieldIR {
	result := make([]ir.FieldIR, 0, len(names))
	for _, name := range names {
		var matches []ir.FieldIR
		for _, field := range model.Fields {
			if field.Scalar != nil && field.Scalar.Column == ir.SQLIdentifier(name) && (field.Kind == ir.FieldScalar || field.Kind == ir.FieldEnum) {
				matches = append(matches, field)
			}
		}
		if len(matches) != 1 {
			*diagnostics = append(*diagnostics, ir.NewError(codeMappingField, fmt.Sprintf("model %s has no unique persisted scalar column %q", model.Go.Name, name), span))
			continue
		}
		result = append(result, matches[0])
	}
	return result
}

func formsUniqueKey(model *ir.ModelDeclIR, fields []ir.FieldID, requireNonNull bool) bool {
	if requireNonNull {
		for _, fieldID := range fields {
			field := fieldByID(model, fieldID)
			if field == nil || field.Scalar == nil || field.Scalar.Nullable {
				return false
			}
		}
	}
	if model.PrimaryKey != nil && equalFieldIDs(model.PrimaryKey.Fields, fields) {
		return true
	}
	for _, key := range model.Uniques {
		if equalFieldIDs(key.Fields, fields) {
			return true
		}
	}
	return false
}

func fieldByID(model *ir.ModelDeclIR, id ir.FieldID) *ir.FieldIR {
	for index := range model.Fields {
		if model.Fields[index].ID == id {
			return &model.Fields[index]
		}
	}
	return nil
}

func logicalTypesEqual(left, right ir.LogicalTypeIR) bool {
	return reflect.DeepEqual(left, right)
}

func mixedNullability(fields []ir.FieldIR) bool {
	nullable := fields[0].Scalar.Nullable
	for _, field := range fields[1:] {
		if field.Scalar.Nullable != nullable {
			return true
		}
	}
	return false
}

func requireNames(declarations []*declaration, diagnostics *[]ir.Diagnostic) {
	counts := make(map[string]int)
	for _, declaration := range declarations {
		if declaration.valid && declaration.kind == ir.RelationBelongsTo {
			counts[modelPair(declaration.owner.ID, declaration.target.ID)]++
		}
	}
	for _, declaration := range declarations {
		if !declaration.valid {
			continue
		}
		self := declaration.owner.ID == declaration.target.ID
		multiple := counts[modelPair(declaration.owner.ID, declaration.target.ID)] > 1
		if (self || multiple) && declaration.explicitName == "" {
			reason := "self-relations"
			if multiple && !self {
				reason = "multiple relations between the same models"
			}
			*diagnostics = append(*diagnostics, ir.NewError(codeNameRequired, fmt.Sprintf("relation %s.%s requires name because %s must be disambiguated", declaration.owner.Go.Name, declaration.raw.GoName, reason), declaration.raw.Span))
			declaration.valid = false
		}
	}
}

func markDuplicateMappings(declarations []*declaration, diagnostics *[]ir.Diagnostic) {
	seen := make(map[string]*declaration)
	for _, declaration := range declarations {
		if !declaration.valid || declaration.kind != ir.RelationBelongsTo {
			continue
		}
		key := mappingKey(declaration)
		if previous := seen[key]; previous != nil {
			diagnostic := ir.NewError(codeDuplicateMapping, fmt.Sprintf("relation %s.%s duplicates the foreign-key mapping owned by %s.%s", declaration.owner.Go.Name, declaration.raw.GoName, previous.owner.Go.Name, previous.raw.GoName), declaration.raw.Span)
			diagnostic.Related = []ir.DiagnosticLabel{{Message: "first mapping", Span: previous.raw.Span}}
			*diagnostics = append(*diagnostics, diagnostic)
			declaration.duplicate = true
			continue
		}
		seen[key] = declaration
	}
}

func compatibleInverses(source *declaration, declarations []*declaration) []*declaration {
	var result []*declaration
	for _, inverse := range declarations {
		if !inverse.valid || inverse.duplicate || inverse.used || inverse.kind == ir.RelationBelongsTo {
			continue
		}
		if inverse.owner.ID != source.target.ID || inverse.target.ID != source.owner.ID {
			continue
		}
		if source.explicitName != "" || inverse.explicitName != "" {
			if source.explicitName == "" || source.explicitName != inverse.explicitName {
				continue
			}
		}
		if !equalFieldIDs(fieldIDList(source.local), fieldIDList(inverse.remote)) || !equalFieldIDs(fieldIDList(source.remote), fieldIDList(inverse.local)) {
			continue
		}
		result = append(result, inverse)
	}
	return result
}

func relationFieldID(declaration *declaration, registry *ir.IDRegistry, diagnostics *[]ir.Diagnostic) (ir.FieldID, string, bool) {
	for _, field := range declaration.owner.Fields {
		if field.GoName != declaration.raw.GoName {
			continue
		}
		if field.Kind != ir.FieldRelation {
			*diagnostics = append(*diagnostics, ir.NewError(codeFieldConflict, fmt.Sprintf("model %s already resolves %s as a non-relation field", declaration.owner.Go.Name, declaration.raw.GoName), declaration.raw.Span))
			return "", "", false
		}
		return field.ID, field.CanonicalIdentity, true
	}
	canonical := ir.OwnedIdentity(string(declaration.owner.ID), declaration.raw.GoName)
	if explicit, ok := attribute(declaration.raw, "id"); ok && explicit != nil {
		canonical = *explicit
	}
	identity, diagnostic := registry.Register(ir.ObjectField, canonical, declaration.raw.Span)
	if diagnostic != nil {
		*diagnostics = append(*diagnostics, *diagnostic)
		return "", "", false
	}
	return ir.FieldIDFrom(identity), identity.Canonical, true
}

func relationFieldFragment(declaration *declaration, relationID ir.RelationID, role ir.RelationEndpointRole) ir.FieldIR {
	return ir.FieldIR{
		ID: declaration.fieldID, CanonicalIdentity: declaration.fieldCanonical, GoName: declaration.raw.GoName, LogicalName: declaration.raw.GoName,
		DeclarationOrder: declaration.order, Kind: ir.FieldRelation,
		Relation: &ir.RelationFieldIR{RelationID: relationID, Role: role, Kind: declaration.kind},
	}
}

func stableCanonical(source *declaration) string {
	if explicit, ok := attribute(source.raw, "id"); ok && explicit != nil {
		return *explicit
	}
	return ir.OwnedIdentity(string(source.owner.ID), source.raw.GoName)
}

func foreignKeyCanonical(relationID ir.RelationID, source *declaration) string {
	return ir.OwnedIdentity(string(relationID), "foreign-key\x00"+strings.Join(stringFieldIDs(fieldIDList(source.local)), "\x00")+"\x00"+strings.Join(stringFieldIDs(fieldIDList(source.remote)), "\x00"))
}

func defaultForeignKeyName(source *declaration) ir.SQLIdentifier {
	return ir.SQLIdentifier("fk_" + string(source.owner.Table.PhysicalName) + "_" + strings.Join(columnNames(source.local), "_"))
}

func relationName(source *declaration) string {
	if source.explicitName != "" {
		return source.explicitName
	}
	return source.raw.GoName
}

func attribute(field *ir.RawFieldDecl, name string) (*string, bool) {
	for index := range field.GolemAttrs {
		if field.GolemAttrs[index].Name == name {
			return field.GolemAttrs[index].RawValue, true
		}
	}
	return nil, false
}

func mappingAttribute(field *ir.RawFieldDecl, name string) ([]string, bool) {
	value, ok := attribute(field, name)
	if !ok || value == nil {
		return nil, false
	}
	parts := strings.Split(*value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false
		}
		result = append(result, part)
	}
	return result, true
}

func firstDuplicate(values []string) string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return value
		}
		seen[value] = struct{}{}
	}
	return ""
}

func declarationLess(left, right *declaration) bool {
	if left.owner.ID != right.owner.ID {
		return left.owner.ID < right.owner.ID
	}
	if left.raw.GoName != right.raw.GoName {
		return left.raw.GoName < right.raw.GoName
	}
	return left.order < right.order
}

func modelPair(left, right ir.ModelID) string {
	if left > right {
		left, right = right, left
	}
	return string(left) + "\x00" + string(right)
}

func mappingKey(declaration *declaration) string {
	return string(declaration.owner.ID) + "\x00" + string(declaration.target.ID) + "\x00" +
		strings.Join(stringFieldIDs(fieldIDList(declaration.local)), ",") + "\x00" + strings.Join(stringFieldIDs(fieldIDList(declaration.remote)), ",")
}

func fieldIDList(fields []ir.FieldIR) []ir.FieldID {
	result := make([]ir.FieldID, len(fields))
	for index := range fields {
		result[index] = fields[index].ID
	}
	return result
}

func stringFieldIDs(fields []ir.FieldID) []string {
	result := make([]string, len(fields))
	for index := range fields {
		result[index] = string(fields[index])
	}
	return result
}

func columnNames(fields []ir.FieldIR) []string {
	result := make([]string, len(fields))
	for index := range fields {
		result[index] = string(fields[index].Scalar.Column)
	}
	return result
}

func equalFieldIDs(left, right []ir.FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func canonicalResult(result Result) Result {
	sort.Slice(result.Relations, func(i, j int) bool { return result.Relations[i].ID < result.Relations[j].ID })
	sort.Slice(result.Fragments, func(i, j int) bool { return result.Fragments[i].ModelID < result.Fragments[j].ModelID })
	for index := range result.Fragments {
		sort.Slice(result.Fragments[index].Fields, func(i, j int) bool {
			left, right := result.Fragments[index].Fields[i], result.Fragments[index].Fields[j]
			if left.DeclarationOrder != right.DeclarationOrder {
				return left.DeclarationOrder < right.DeclarationOrder
			}
			return left.ID < right.ID
		})
		if result.Fragments[index].Fields == nil {
			result.Fragments[index].Fields = []ir.FieldIR{}
		}
	}
	if result.Relations == nil {
		result.Relations = []ir.RelationIR{}
	}
	if result.Fragments == nil {
		result.Fragments = []ModelFragment{}
	}
	if result.Diagnostics == nil {
		result.Diagnostics = []ir.Diagnostic{}
	}
	ir.SortDiagnostics(result.Diagnostics)
	return result
}
