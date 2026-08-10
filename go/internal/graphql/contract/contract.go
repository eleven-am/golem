// Package contract materializes and validates provider-neutral GraphQL facts.
// It is deliberately isolated from persistence lowering: changing anything in
// this package may change ContractIR, never ModelIR or a migration plan.
package contract

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
)

const ABIVersion uint16 = 4

const (
	DefaultPageSize    uint32 = 50
	DefaultMaxPageSize uint32 = 500
)

var ordinaryOperations = []ir.Operation{
	ir.OperationFindOne,
	ir.OperationFindMany,
	ir.OperationCreate,
	ir.OperationUpdate,
	ir.OperationUpsert,
	ir.OperationDelete,
	ir.OperationUpdateMany,
	ir.OperationDeleteMany,
}

// ModelPatch is the statically interpreted, source-located GraphQL portion of
// one GolemModel declaration. Nil members mean that the author did not override
// the normalized convention.
type ModelPatch struct {
	ModelID               ir.ModelID
	Operations            *[]ir.Operation
	Plural                *string
	Roots                 *ir.GraphQLRootNamesIR
	DefaultPage           *uint32
	MaximumPage           *uint32
	Hidden                bool
	HookOwnedCreateFields []ir.FieldID
	Subscriptions         *bool
	Span                  ir.SourceSpan
}

func Operations() []ir.Operation { return append([]ir.Operation(nil), ordinaryOperations...) }

// LowerCamel implements Golem's frozen initialism-aware GraphQL convention.
// It intentionally does not use a mutable process-global acronym registry.
func LowerCamel(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	end := 0
	for end < len(runes) && unicode.IsUpper(runes[end]) {
		end++
	}
	if end == 0 {
		return value
	}
	if end > 1 && end < len(runes) && unicode.IsLower(runes[end]) {
		end--
	}
	for index := 0; index < end; index++ {
		runes[index] = unicode.ToLower(runes[index])
	}
	return string(runes)
}

// Pluralize is the versioned P5 English rule table. Explicit GraphQLPlural is
// the escape hatch for domain words and future vocabulary changes.
func Pluralize(value string) string {
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	irregular := map[string]string{
		"person": "people", "man": "men", "woman": "women", "child": "children",
		"mouse": "mice", "goose": "geese", "tooth": "teeth", "foot": "feet",
	}
	if plural, ok := irregular[lower]; ok {
		if value == lower {
			return plural
		}
		return strings.ToUpper(value[:1]) + plural[1:]
	}
	for _, suffix := range []string{"s", "x", "z", "ch", "sh"} {
		if strings.HasSuffix(lower, suffix) {
			return value + "es"
		}
	}
	if strings.HasSuffix(lower, "y") && len(lower) > 1 && !strings.ContainsRune("aeiou", rune(lower[len(lower)-2])) {
		return value[:len(value)-1] + "ies"
	}
	if strings.HasSuffix(lower, "fe") {
		return value[:len(value)-2] + "ves"
	}
	if strings.HasSuffix(lower, "f") {
		return value[:len(value)-1] + "ves"
	}
	return value + "s"
}

func DefaultRoots(graphqlName, plural string) ir.GraphQLRootNamesIR {
	singularRoot := LowerCamel(graphqlName)
	pluralRoot := LowerCamel(plural)
	return ir.GraphQLRootNamesIR{
		FindOne: singularRoot, FindMany: pluralRoot,
		Create: "create" + graphqlName, Update: "update" + graphqlName,
		Upsert: "upsert" + graphqlName, Delete: "delete" + graphqlName,
		UpdateMany: "updateMany" + plural, DeleteMany: "deleteMany" + plural,
		Aggregate: "aggregate" + plural, GroupBy: "groupBy" + plural,
		RelationGroupBy: "relationGroupBy" + plural,
		Events:          singularRoot + "Events",
	}
}

// Normalize materializes defaults, applies interpreted overrides, and rejects
// every schema-name collision before a GraphQL artifact can be generated.
func Normalize(compilation *ir.CompilationIR, patches []ModelPatch) []ir.Diagnostic {
	if compilation == nil {
		return []ir.Diagnostic{ir.NewError("P5_CONTRACT_REQUIRED", "GraphQL contract normalization requires a compilation", ir.SourceSpan{})}
	}
	compilation.Contract.GraphQLABIVersion = ABIVersion
	byModel := make(map[ir.ModelID]ModelPatch, len(patches))
	var diagnostics []ir.Diagnostic
	for _, patch := range patches {
		if _, duplicate := byModel[patch.ModelID]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_MODEL_DUPLICATE", fmt.Sprintf("model %s declares GraphQL options more than once", patch.ModelID), patch.Span))
		}
		byModel[patch.ModelID] = patch
	}
	for index := range compilation.Contract.Models {
		model := &compilation.Contract.Models[index]
		patch, hasPatch := byModel[model.ModelID]
		if model.GraphQLPlural == "" {
			model.GraphQLPlural = Pluralize(model.GraphQLName)
		}
		model.Roots = overlayRoots(DefaultRoots(model.GraphQLName, model.GraphQLPlural), model.Roots)
		if len(model.Operations) == 0 {
			model.Operations = Operations()
		}
		if model.Limits.DefaultPageSize == 0 {
			model.Limits.DefaultPageSize = DefaultPageSize
		}
		if model.Limits.MaxPageSize == 0 {
			model.Limits.MaxPageSize = DefaultMaxPageSize
		}
		if hasPatch {
			if patch.Operations != nil {
				model.Operations = append([]ir.Operation(nil), (*patch.Operations)...)
			}
			if patch.Plural != nil {
				model.GraphQLPlural = *patch.Plural
				model.Roots = DefaultRoots(model.GraphQLName, model.GraphQLPlural)
			}
			if patch.Roots != nil {
				model.Roots = overlayRoots(model.Roots, *patch.Roots)
			}
			if patch.DefaultPage != nil {
				model.Limits.DefaultPageSize = *patch.DefaultPage
			}
			if patch.MaximumPage != nil {
				model.Limits.MaxPageSize = *patch.MaximumPage
			}
			if patch.Hidden {
				model.Exposed = false
				model.Operations = []ir.Operation{}
			}
			model.HookOwnedCreateFields = append([]ir.FieldID(nil), patch.HookOwnedCreateFields...)
			if patch.Subscriptions != nil {
				model.Subscriptions = *patch.Subscriptions
			}
		}
		diagnostics = append(diagnostics, normalizeEventContract(compilation.Model, model, patch.Span)...)
		diagnostics = append(diagnostics, validateHookOwnedShape(compilation.Model, *model, patch.Span)...)
		diagnostics = append(diagnostics, validateModel(*model, patch.Span)...)
	}
	diagnostics = append(diagnostics, validateEnums(compilation.Contract.Enums)...)
	diagnostics = append(diagnostics, validateSchemaCollisions(compilation.Contract)...)
	// Extension validation runs over the same complete ContractIR so computed
	// fields and custom roots cannot bypass generated/reserved-name checks when
	// a caller supplies already-interpreted declarations.
	diagnostics = append(diagnostics, graphqlextension.Normalize(compilation, nil, nil)...)
	ir.SortDiagnostics(diagnostics)
	return diagnostics
}

// ValidateHookOwnedMethods closes the portion of GraphQLHookOwned validation
// that depends on typed binding discovery. It must run after recognized model
// hooks have been merged into ContractIR and before fingerprinting/codegen.
func ValidateHookOwnedMethods(compilation ir.CompilationIR) []ir.Diagnostic {
	beforeCreate := map[ir.ModelID]bool{}
	for _, method := range compilation.Contract.Methods {
		if method.ModelID != nil && method.Kind == "hook" && method.Name == "BeforeCreate" {
			beforeCreate[*method.ModelID] = true
		}
	}
	var diagnostics []ir.Diagnostic
	for _, model := range compilation.Contract.Models {
		if len(model.HookOwnedCreateFields) != 0 && !beforeCreate[model.ModelID] {
			diagnostics = append(diagnostics, ir.NewError("P8_GRAPHQL_HOOK_OWNED_BEFORE_CREATE", fmt.Sprintf("model %s declares GraphQLHookOwned without a recognized BeforeCreate hook", model.ModelID), ir.SourceSpan{}))
		}
	}
	ir.SortDiagnostics(diagnostics)
	return diagnostics
}

func validateHookOwnedShape(logical ir.ModelIR, contract ir.ModelContractIR, span ir.SourceSpan) []ir.Diagnostic {
	if len(contract.HookOwnedCreateFields) == 0 {
		return nil
	}
	var model *ir.ModelDeclIR
	for index := range logical.Models {
		if logical.Models[index].ID == contract.ModelID {
			model = &logical.Models[index]
			break
		}
	}
	if model == nil {
		return []ir.Diagnostic{ir.NewError("P8_GRAPHQL_HOOK_OWNED_MODEL", fmt.Sprintf("hook-owned model %s is absent", contract.ModelID), span)}
	}
	fields := make(map[ir.FieldID]ir.FieldIR, len(model.Fields))
	for _, field := range model.Fields {
		fields[field.ID] = field
	}
	identity := map[ir.FieldID]bool{}
	if model.PrimaryKey != nil {
		for _, field := range model.PrimaryKey.Fields {
			identity[field] = true
		}
	}
	for _, key := range model.Uniques {
		for _, field := range key.Fields {
			identity[field] = true
		}
	}
	owned := map[ir.FieldID]bool{}
	var diagnostics []ir.Diagnostic
	for _, fieldID := range contract.HookOwnedCreateFields {
		if owned[fieldID] {
			diagnostics = append(diagnostics, ir.NewError("P8_GRAPHQL_HOOK_OWNED_DUPLICATE", fmt.Sprintf("model %s repeats hook-owned field %s", model.ID, fieldID), span))
			continue
		}
		owned[fieldID] = true
		field, ok := fields[fieldID]
		if !ok || field.Scalar == nil {
			diagnostics = append(diagnostics, ir.NewError("P8_GRAPHQL_HOOK_OWNED_FIELD", fmt.Sprintf("model %s hook-owned field %s is not a scalar", model.ID, fieldID), span))
			continue
		}
		if field.Scalar.DatabaseReadOnly || field.Scalar.Generation != nil {
			diagnostics = append(diagnostics, ir.NewError("P8_GRAPHQL_HOOK_OWNED_WRITABLE", fmt.Sprintf("model %s hook-owned field %s is not programmatic-create writable", model.ID, fieldID), span))
		}
		if identity[fieldID] {
			diagnostics = append(diagnostics, ir.NewError("P8_GRAPHQL_HOOK_OWNED_IDENTITY", fmt.Sprintf("model %s hook-owned field %s participates in an identity key", model.ID, fieldID), span))
		}
	}
	validatedRelations := map[ir.RelationID]bool{}
	for fieldID := range owned {
		var matches []ir.RelationIR
		for _, relation := range logical.Relations {
			if relation.SourceModel == model.ID && containsField(relation.LocalFields, fieldID) {
				matches = append(matches, relation)
			}
		}
		if len(matches) == 0 {
			// Hook-owned scalars do not have to back a relation. Generated slugs,
			// tenant metadata, and similar trusted values are valid uses.
			continue
		}
		if len(matches) > 1 {
			diagnostics = append(diagnostics, ir.NewError("P8_GRAPHQL_HOOK_OWNED_RELATION", fmt.Sprintf("model %s hook-owned field %s participates in more than one source relation", model.ID, fieldID), span))
			continue
		}
		relation := matches[0]
		if validatedRelations[relation.ID] {
			continue
		}
		validatedRelations[relation.ID] = true
		relationField, ok := fields[relation.SourceField]
		if !ok || relationField.Relation == nil || relationField.Relation.Role != ir.RelationSource || relationField.Relation.Kind != ir.RelationBelongsTo {
			diagnostics = append(diagnostics, ir.NewError("P8_GRAPHQL_HOOK_OWNED_BELONGS_TO", fmt.Sprintf("model %s hook-owned relation %s is not its canonical belongs-to field", model.ID, relation.ID), span))
		}
		if len(relation.LocalFields) == 0 {
			diagnostics = append(diagnostics, ir.NewError("P8_GRAPHQL_HOOK_OWNED_RELATION_KEY", fmt.Sprintf("model %s hook-owned relation %s has no local key", model.ID, relation.ID), span))
			continue
		}
		for _, localID := range relation.LocalFields {
			local, exists := fields[localID]
			if !owned[localID] {
				diagnostics = append(diagnostics, ir.NewError("P8_GRAPHQL_HOOK_OWNED_PARTIAL_COMPOSITE", fmt.Sprintf("model %s hook-owned relation %s does not own complete local key; field %s is missing", model.ID, relation.ID, localID), span))
			}
			if !exists || local.Scalar == nil || local.Scalar.Nullable {
				diagnostics = append(diagnostics, ir.NewError("P8_GRAPHQL_HOOK_OWNED_REQUIRED", fmt.Sprintf("model %s hook-owned relation %s local field %s must be non-null", model.ID, relation.ID, localID), span))
			}
		}
	}
	ir.SortDiagnostics(diagnostics)
	return diagnostics
}

func containsField(values []ir.FieldID, field ir.FieldID) bool {
	for _, value := range values {
		if value == field {
			return true
		}
	}
	return false
}

var eventMetadataFields = []string{
	"eventID", "type", "id", "entity",
	"causationID", "transactionOrdinal", "recordedAt",
}

func normalizeEventContract(logical ir.ModelIR, contract *ir.ModelContractIR, span ir.SourceSpan) []ir.Diagnostic {
	if !contract.Subscriptions {
		contract.Event = nil
		return nil
	}
	var model *ir.ModelDeclIR
	for index := range logical.Models {
		if logical.Models[index].ID == contract.ModelID {
			model = &logical.Models[index]
			break
		}
	}
	if model == nil {
		return []ir.Diagnostic{ir.NewError("P7_EVENT_MODEL_MISSING", fmt.Sprintf("subscription model %s has no logical model", contract.ModelID), span)}
	}
	if !contract.Exposed {
		return []ir.Diagnostic{ir.NewError("P7_SUBSCRIPTION_MODEL_HIDDEN", fmt.Sprintf("model %s cannot enable subscriptions while hidden", contract.ModelID), span)}
	}
	if model.PrimaryKey == nil || len(model.PrimaryKey.Fields) == 0 {
		return []ir.Diagnostic{ir.NewError("P7_SUBSCRIPTION_PRIMARY_KEY", fmt.Sprintf("model %s requires a primary key for subscriptions", contract.ModelID), span)}
	}
	fieldContracts := make(map[ir.FieldID]ir.FieldContractIR, len(contract.Fields))
	for _, field := range contract.Fields {
		fieldContracts[field.FieldID] = field
	}
	var diagnostics []ir.Diagnostic
	for _, fieldID := range model.PrimaryKey.Fields {
		field, exists := fieldContracts[fieldID]
		if !exists || !eventIdentityFieldExposed(field) {
			diagnostics = append(diagnostics, ir.NewError("P7_SUBSCRIPTION_IDENTITY_HIDDEN", fmt.Sprintf("subscription identity field %s on model %s must be exposed", fieldID, contract.ModelID), span))
		}
	}
	if len(diagnostics) != 0 {
		return diagnostics
	}
	// P7 event schema v1 deliberately captures the complete locally stored
	// scalar pre-image. Policy factories are actor-dependent runtime code, so a
	// static minimal dependency inventory would be unsound. Declaration order is
	// preserved to make the private row shape deterministic and reconstructible.
	snapshot := make([]ir.FieldID, 0, len(model.Fields))
	for _, field := range model.Fields {
		if field.Kind != ir.FieldRelation && field.Scalar != nil {
			snapshot = append(snapshot, field.ID)
		}
	}
	shape, err := ir.BuildEventSchemaShape(*model, logical.Enums, snapshot)
	if err != nil {
		return []ir.Diagnostic{ir.NewError("P7_EVENT_SCHEMA_INVALID", err.Error(), span)}
	}
	fingerprint, err := ir.EventSchemaFingerprint(shape)
	if err != nil {
		return []ir.Diagnostic{ir.NewError("P7_EVENT_SCHEMA_FINGERPRINT", err.Error(), span)}
	}
	identityType := ""
	if len(shape.IdentityFields) > 1 {
		identityType = contract.GraphQLName + "EventIdentity"
	}
	contract.Event = &ir.EventContractIR{
		PayloadTypeName: contract.GraphQLName + "Event", IdentityTypeName: identityType,
		MetadataFields: append([]string(nil), eventMetadataFields...), DeleteSnapshotFull: true,
		Schema: shape, SchemaFingerprint: fingerprint,
	}
	return nil
}

func eventIdentityFieldExposed(field ir.FieldContractIR) bool {
	for _, mode := range field.Modes {
		if mode == ir.ModeHidden || mode == ir.ModeWriteOnly {
			return false
		}
	}
	return field.FieldID != "" && field.GraphQLName != ""
}

func overlayRoots(base, override ir.GraphQLRootNamesIR) ir.GraphQLRootNamesIR {
	fields := []*string{&base.FindOne, &base.FindMany, &base.Create, &base.Update, &base.Upsert, &base.Delete, &base.UpdateMany, &base.DeleteMany, &base.Aggregate, &base.GroupBy, &base.RelationGroupBy, &base.Events}
	values := []string{override.FindOne, override.FindMany, override.Create, override.Update, override.Upsert, override.Delete, override.UpdateMany, override.DeleteMany, override.Aggregate, override.GroupBy, override.RelationGroupBy, override.Events}
	for index, value := range values {
		if value != "" {
			*fields[index] = value
		}
	}
	return base
}

func validateModel(model ir.ModelContractIR, span ir.SourceSpan) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	if !validName(model.GraphQLName) {
		diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_MODEL_NAME", fmt.Sprintf("%q is not a legal GraphQL model name", model.GraphQLName), span))
	}
	if !validName(model.GraphQLPlural) {
		diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_PLURAL_NAME", fmt.Sprintf("%q is not a legal GraphQL plural name", model.GraphQLPlural), span))
	}
	seenFields := map[string]ir.FieldID{}
	for _, field := range model.Fields {
		if !validName(field.GraphQLName) {
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_FIELD_NAME", fmt.Sprintf("%q is not a legal GraphQL field name", field.GraphQLName), span))
		}
		if previous, duplicate := seenFields[field.GraphQLName]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_FIELD_COLLISION", fmt.Sprintf("fields %s and %s both map to %q", previous, field.FieldID, field.GraphQLName), span))
		}
		seenFields[field.GraphQLName] = field.FieldID
	}
	allowed := map[ir.Operation]bool{}
	for _, operation := range ordinaryOperations {
		allowed[operation] = true
	}
	allowed[ir.OperationAggregate] = true
	allowed[ir.OperationGroupBy] = true
	allowed[ir.OperationRelationGroupBy] = true
	seenOperations := map[ir.Operation]bool{}
	for _, operation := range model.Operations {
		if !allowed[operation] {
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_OPERATION_UNKNOWN", fmt.Sprintf("model %s enables unknown GraphQL operation %q", model.ModelID, operation), span))
		}
		if seenOperations[operation] {
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_OPERATION_DUPLICATE", fmt.Sprintf("model %s enables GraphQL operation %q more than once", model.ModelID, operation), span))
		}
		seenOperations[operation] = true
	}
	if seenOperations[ir.OperationAggregate] || seenOperations[ir.OperationGroupBy] || seenOperations[ir.OperationRelationGroupBy] {
		if model.Aggregation == nil || !model.Aggregation.Enabled {
			diagnostics = append(diagnostics, ir.NewError("P6_GRAPHQL_ANALYTICS_REQUIRED", fmt.Sprintf("model %s enables analytics roots without Analytics", model.ModelID), span))
		}
	}
	if model.Aggregation != nil {
		if seenOperations[ir.OperationGroupBy] && model.Aggregation.DimensionsExplicit && len(model.Aggregation.Dimensions) == 0 {
			diagnostics = append(diagnostics, ir.NewError("P6_GRAPHQL_DIMENSION_ALLOWLIST_EMPTY", fmt.Sprintf("model %s enables groupBy with an explicit empty dimension allowlist", model.ModelID), span))
		}
		if (seenOperations[ir.OperationAggregate] || seenOperations[ir.OperationGroupBy] || seenOperations[ir.OperationRelationGroupBy]) && model.Aggregation.MeasuresExplicit && len(model.Aggregation.Measures) == 0 {
			diagnostics = append(diagnostics, ir.NewError("P6_GRAPHQL_MEASURE_ALLOWLIST_EMPTY", fmt.Sprintf("model %s enables analytics with an explicit empty measure allowlist", model.ModelID), span))
		}
	}
	if seenOperations[ir.OperationRelationGroupBy] && (model.Aggregation == nil || len(model.Aggregation.RelationDimensions) == 0) {
		diagnostics = append(diagnostics, ir.NewError("P6_GRAPHQL_RELATION_DIMENSION_REQUIRED", fmt.Sprintf("model %s enables relationGroupBy without a named relation dimension", model.ModelID), span))
	}
	if model.Limits.DefaultPageSize == 0 || model.Limits.MaxPageSize == 0 || model.Limits.DefaultPageSize > model.Limits.MaxPageSize {
		diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_PAGE_LIMIT", fmt.Sprintf("model %s requires 0 < default page size <= maximum page size", model.ModelID), span))
	}
	return diagnostics
}

func validateEnums(enums []ir.EnumContractIR) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	for _, enum := range enums {
		if !validName(enum.GraphQLName) {
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_ENUM_NAME", fmt.Sprintf("%q is not a legal GraphQL enum name", enum.GraphQLName), ir.SourceSpan{}))
		}
		seen := map[string]ir.EnumValueID{}
		for _, value := range enum.Values {
			if !validName(value.GraphQLName) {
				diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_ENUM_VALUE_NAME", fmt.Sprintf("%q is not a legal GraphQL enum value name", value.GraphQLName), ir.SourceSpan{}))
			}
			if previous, duplicate := seen[value.GraphQLName]; duplicate {
				diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_ENUM_VALUE_COLLISION", fmt.Sprintf("enum values %s and %s both map to %q", previous, value.ValueID, value.GraphQLName), ir.SourceSpan{}))
			}
			seen[value.GraphQLName] = value.ValueID
		}
	}
	return diagnostics
}

func validateSchemaCollisions(contract ir.ContractIR) []ir.Diagnostic {
	types := map[string]string{"Query": "reserved root", "Mutation": "reserved root", "Subscription": "reserved root", "BatchPayload": "reserved generated type"}
	for _, model := range contract.Models {
		if model.Exposed && model.Subscriptions {
			types["GolemEventType"] = "reserved generated event enum"
			break
		}
	}
	queryRoots := map[string]string{}
	mutationRoots := map[string]string{}
	subscriptionRoots := map[string]string{}
	var diagnostics []ir.Diagnostic
	for _, model := range contract.Models {
		if !model.Exposed {
			continue
		}
		if owner, duplicate := types[model.GraphQLName]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_TYPE_COLLISION", fmt.Sprintf("model %s type %q collides with %s", model.ModelID, model.GraphQLName, owner), ir.SourceSpan{}))
		} else {
			types[model.GraphQLName] = "model " + string(model.ModelID)
		}
		if model.Subscriptions && model.Event != nil {
			for _, generated := range []string{model.Event.PayloadTypeName, model.Event.IdentityTypeName} {
				if generated == "" {
					continue
				}
				if owner, duplicate := types[generated]; duplicate {
					diagnostics = append(diagnostics, ir.NewError("P7_EVENT_TYPE_COLLISION", fmt.Sprintf("model %s event type %q collides with %s", model.ModelID, generated, owner), ir.SourceSpan{}))
				} else {
					types[generated] = "event type for model " + string(model.ModelID)
				}
			}
		}
		for _, enum := range contract.Enums {
			if enum.GraphQLName == model.GraphQLName {
				diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_TYPE_COLLISION", fmt.Sprintf("model %s and enum %s both use type %q", model.ModelID, enum.EnumID, model.GraphQLName), ir.SourceSpan{}))
			}
		}
		enabled := make(map[ir.Operation]bool, len(model.Operations))
		for _, operation := range model.Operations {
			enabled[operation] = true
		}
		roots := []struct {
			name, owner string
			mutation    bool
			enabled     bool
		}{
			{model.Roots.FindOne, string(model.ModelID) + ".findOne", false, enabled[ir.OperationFindOne]},
			{model.Roots.FindMany, string(model.ModelID) + ".findMany", false, enabled[ir.OperationFindMany]},
			{model.Roots.Create, string(model.ModelID) + ".create", true, enabled[ir.OperationCreate]},
			{model.Roots.Update, string(model.ModelID) + ".update", true, enabled[ir.OperationUpdate]},
			{model.Roots.Upsert, string(model.ModelID) + ".upsert", true, enabled[ir.OperationUpsert]},
			{model.Roots.Delete, string(model.ModelID) + ".delete", true, enabled[ir.OperationDelete]},
			{model.Roots.UpdateMany, string(model.ModelID) + ".updateMany", true, enabled[ir.OperationUpdateMany]},
			{model.Roots.DeleteMany, string(model.ModelID) + ".deleteMany", true, enabled[ir.OperationDeleteMany]},
			{model.Roots.Aggregate, string(model.ModelID) + ".aggregate", false, enabled[ir.OperationAggregate]},
			{model.Roots.GroupBy, string(model.ModelID) + ".groupBy", false, enabled[ir.OperationGroupBy]},
			{model.Roots.RelationGroupBy, string(model.ModelID) + ".relationGroupBy", false, enabled[ir.OperationRelationGroupBy]},
		}
		for _, root := range roots {
			if !root.enabled {
				continue
			}
			if !validName(root.name) {
				diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_ROOT_NAME", fmt.Sprintf("%q is not a legal GraphQL root name", root.name), ir.SourceSpan{}))
				continue
			}
			registry := queryRoots
			if root.mutation {
				registry = mutationRoots
			}
			if previous, duplicate := registry[root.name]; duplicate {
				diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_ROOT_COLLISION", fmt.Sprintf("%s and %s both use root %q", previous, root.owner, root.name), ir.SourceSpan{}))
			} else {
				registry[root.name] = root.owner
			}
		}
		if model.Subscriptions {
			if !validName(model.Roots.Events) {
				diagnostics = append(diagnostics, ir.NewError("P7_EVENT_ROOT_NAME", fmt.Sprintf("%q is not a legal GraphQL subscription root name", model.Roots.Events), ir.SourceSpan{}))
			} else if previous, duplicate := subscriptionRoots[model.Roots.Events]; duplicate {
				diagnostics = append(diagnostics, ir.NewError("P7_EVENT_ROOT_COLLISION", fmt.Sprintf("%s and %s.events both use subscription root %q", previous, model.ModelID, model.Roots.Events), ir.SourceSpan{}))
			} else {
				subscriptionRoots[model.Roots.Events] = string(model.ModelID) + ".events"
			}
		}
	}
	for _, enum := range contract.Enums {
		if owner, duplicate := types[enum.GraphQLName]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_TYPE_COLLISION", fmt.Sprintf("enum %s type %q collides with %s", enum.EnumID, enum.GraphQLName, owner), ir.SourceSpan{}))
		} else {
			types[enum.GraphQLName] = "enum " + string(enum.EnumID)
		}
	}
	return diagnostics
}

func validName(value string) bool {
	if value == "" || strings.HasPrefix(value, "__") {
		return false
	}
	for index, character := range value {
		if index == 0 {
			if character != '_' && !unicode.IsLetter(character) {
				return false
			}
		} else if character != '_' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return false
		}
	}
	return true
}

// SortPatches makes diagnostics and application deterministic for callers that
// collect declarations while traversing packages.
func SortPatches(values []ModelPatch) {
	sort.SliceStable(values, func(i, j int) bool { return values[i].ModelID < values[j].ModelID })
}
