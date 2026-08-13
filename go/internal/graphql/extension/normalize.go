// Package extension owns provider-neutral computed-field and custom-operation
// metadata. It may change ContractIR only; persistence lowering and migrations
// must never import this package.
package extension

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const HardMaxBatchSize uint32 = 4096

type ComputedDeclaration struct {
	ModelID ir.ModelID
	Field   ir.ComputedFieldContractIR
	Span    ir.SourceSpan
}

type CustomOperationDeclaration struct {
	Operation ir.CustomOperationContractIR
	Span      ir.SourceSpan
}

// SortDeclarations freezes traversal-order independence before normalization
// and diagnostic emission.
func SortDeclarations(computed []ComputedDeclaration, custom []CustomOperationDeclaration) {
	sort.SliceStable(computed, func(i, j int) bool {
		if computed[i].ModelID != computed[j].ModelID {
			return computed[i].ModelID < computed[j].ModelID
		}
		if computed[i].Field.Name != computed[j].Field.Name {
			return computed[i].Field.Name < computed[j].Field.Name
		}
		return computed[i].Field.ExtensionID < computed[j].Field.ExtensionID
	})
	sort.SliceStable(custom, func(i, j int) bool {
		if custom[i].Operation.Operation != custom[j].Operation.Operation {
			return custom[i].Operation.Operation < custom[j].Operation.Operation
		}
		if custom[i].Operation.Name != custom[j].Operation.Name {
			return custom[i].Operation.Name < custom[j].Operation.Name
		}
		return custom[i].Operation.ExtensionID < custom[j].Operation.ExtensionID
	})
}

// Normalize validates extension declarations against the complete normalized
// model and GraphQL contract. Application is atomic: one diagnostic leaves the
// caller's CompilationIR unchanged.
func Normalize(compilation *ir.CompilationIR, computed []ComputedDeclaration, custom []CustomOperationDeclaration) []ir.Diagnostic {
	if compilation == nil {
		return []ir.Diagnostic{ir.NewError("P5_EXTENSION_COMPILATION_REQUIRED", "GraphQL extension normalization requires a compilation", ir.SourceSpan{})}
	}

	modelByID := make(map[ir.ModelID]ir.ModelDeclIR, len(compilation.Model.Models))
	modelName := make(map[string]ir.ModelID, len(compilation.Model.Models))
	fieldOwner := map[ir.FieldID]ir.ModelID{}
	fieldByID := map[ir.FieldID]ir.FieldIR{}
	for _, model := range compilation.Model.Models {
		modelByID[model.ID] = model
		for _, field := range model.Fields {
			fieldOwner[field.ID] = model.ID
			fieldByID[field.ID] = field
		}
	}
	enumNames := make(map[string]struct{}, len(compilation.Contract.Enums))
	for _, enum := range compilation.Contract.Enums {
		enumNames[enum.GraphQLName] = struct{}{}
	}

	contracts := append([]ir.ModelContractIR(nil), compilation.Contract.Models...)
	contractIndex := make(map[ir.ModelID]int, len(contracts))
	versionedModelName := map[string]bool{}
	for index := range contracts {
		contracts[index].Computed = append([]ir.ComputedFieldContractIR(nil), contracts[index].Computed...)
		contractIndex[contracts[index].ModelID] = index
		if contracts[index].Exposed {
			modelName[contracts[index].GraphQLName] = contracts[index].ModelID
			if contracts[index].OptimisticConcurrency != nil {
				versionedModelName[contracts[index].GraphQLName] = true
			}
		}
	}
	customOperations := append([]ir.CustomOperationContractIR(nil), compilation.Contract.CustomOperations...)

	typeContext := graphQLTypeContext{models: modelName, enums: enumNames, versioned: versionedModelName}
	extensionIDs := map[ir.ExtensionID]string{}
	var diagnostics []ir.Diagnostic
	reserveID := func(id ir.ExtensionID, owner string, span ir.SourceSpan) bool {
		if id == "" {
			diagnostics = append(diagnostics, ir.NewError("P5_EXTENSION_ID_REQUIRED", owner+" requires a stable extension identity", span))
			return false
		}
		if previous, duplicate := extensionIDs[id]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P5_EXTENSION_ID_DUPLICATE", fmt.Sprintf("%s and %s share extension identity %s", previous, owner, id), span))
			return false
		}
		extensionIDs[id] = owner
		return true
	}
	for _, contract := range contracts {
		for _, field := range contract.Computed {
			reserveID(field.ExtensionID, fmt.Sprintf("computed field %s.%s", contract.GraphQLName, field.Name), ir.SourceSpan{})
		}
	}
	for _, operation := range customOperations {
		reserveID(operation.ExtensionID, fmt.Sprintf("custom %s %s", operation.Operation, operation.Name), ir.SourceSpan{})
	}

	SortDeclarations(computed, custom)
	for _, declaration := range computed {
		field := declaration.Field
		owner := fmt.Sprintf("computed field %s.%s", declaration.ModelID, field.Name)
		valid := reserveID(field.ExtensionID, owner, declaration.Span)
		index, exists := contractIndex[declaration.ModelID]
		model, modelExists := modelByID[declaration.ModelID]
		if !exists || !modelExists {
			diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_MODEL_UNKNOWN", fmt.Sprintf("computed field %q references unknown model %s", field.Name, declaration.ModelID), declaration.Span))
			valid = false
		} else if !contracts[index].Exposed {
			diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_MODEL_HIDDEN", fmt.Sprintf("computed field %q cannot extend hidden model %s", field.Name, declaration.ModelID), declaration.Span))
			valid = false
		}
		if !validName(field.Name) {
			diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_NAME", fmt.Sprintf("%q is not a legal computed-field name", field.Name), declaration.Span))
			valid = false
		}
		if typeDiagnostics := typeContext.validate(field.Result, typeComputedResult, declaration.Span); len(typeDiagnostics) != 0 {
			diagnostics = append(diagnostics, typeDiagnostics...)
			valid = false
		}
		if argumentDiagnostics := typeContext.validateArguments(field.Arguments, typeComputedArgument, declaration.Span); len(argumentDiagnostics) != 0 {
			diagnostics = append(diagnostics, argumentDiagnostics...)
			valid = false
		}
		if modelExists {
			resolverValid := validComputedResolver(field.Resolver, model)
			if field.Batch != nil {
				resolverValid = validCallable(field.Resolver) && field.Resolver.Name == field.Batch.Loader.Name && field.Resolver.PackagePath == field.Batch.Loader.PackagePath && field.Resolver.Receiver == field.Batch.Loader.Receiver
			}
			if !resolverValid {
				diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_RESOLVER", fmt.Sprintf("computed field %s.%s requires a typed resolver bound to its model declaration", model.Go.Name, field.Name), declaration.Span))
				valid = false
			}
		}
		seenDependencies := map[ir.FieldID]bool{}
		for _, dependency := range field.Requires {
			if dependency == "" || fieldOwner[dependency] != declaration.ModelID {
				diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_DEPENDENCY", fmt.Sprintf("computed field %q dependency %s does not belong to model %s", field.Name, dependency, declaration.ModelID), declaration.Span))
				valid = false
			} else if seenDependencies[dependency] {
				diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_DEPENDENCY_DUPLICATE", fmt.Sprintf("computed field %q repeats dependency %s", field.Name, dependency), declaration.Span))
				valid = false
			}
			seenDependencies[dependency] = true
		}
		if field.Batch != nil {
			batch := field.Batch
			key := fieldByID[batch.KeyField]
			if fieldOwner[batch.KeyField] != declaration.ModelID || key.Kind == ir.FieldRelation {
				diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_BATCH_KEY", fmt.Sprintf("computed field %q batch key must be a scalar field of model %s", field.Name, declaration.ModelID), declaration.Span))
				valid = false
			}
			if batch.MaxBatchSize == 0 || batch.MaxBatchSize > HardMaxBatchSize {
				diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_BATCH_LIMIT", fmt.Sprintf("computed field %q batch size must be between 1 and %d", field.Name, HardMaxBatchSize), declaration.Span))
				valid = false
			}
			if !validCallable(batch.Loader) {
				diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_BATCH_LOADER", fmt.Sprintf("computed field %q requires a typed batch loader", field.Name), declaration.Span))
				valid = false
			}
			if batch.CacheKey != nil && !validCallable(*batch.CacheKey) {
				diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_CACHE_KEY", fmt.Sprintf("computed field %q has an invalid cache-key codec", field.Name), declaration.Span))
				valid = false
			}
		}
		if valid {
			contracts[index].Computed = append(contracts[index].Computed, field)
		}
	}

	for _, declaration := range custom {
		operation := declaration.Operation
		owner := fmt.Sprintf("custom %s %s", operation.Operation, operation.Name)
		valid := reserveID(operation.ExtensionID, owner, declaration.Span)
		if operation.Operation != ir.CustomOperationQuery && operation.Operation != ir.CustomOperationMutation {
			diagnostics = append(diagnostics, ir.NewError("P5_CUSTOM_OPERATION_KIND", fmt.Sprintf("custom operation %q must be a query or mutation", operation.Name), declaration.Span))
			valid = false
		}
		if operation.Capability != ir.CustomOperationCallerOnly {
			diagnostics = append(diagnostics, ir.NewError("P5_CUSTOM_CALLER_ONLY", fmt.Sprintf("custom operation %q may request only the generated Caller capability", operation.Name), declaration.Span))
			valid = false
		}
		if !validName(operation.Name) {
			diagnostics = append(diagnostics, ir.NewError("P5_CUSTOM_NAME", fmt.Sprintf("%q is not a legal custom-operation name", operation.Name), declaration.Span))
			valid = false
		}
		if argumentDiagnostics := typeContext.validateArguments(operation.Arguments, typeCustomArgument, declaration.Span); len(argumentDiagnostics) != 0 {
			diagnostics = append(diagnostics, argumentDiagnostics...)
			valid = false
		}
		if typeDiagnostics := typeContext.validate(operation.Result, typeCustomResult, declaration.Span); len(typeDiagnostics) != 0 {
			diagnostics = append(diagnostics, typeDiagnostics...)
			valid = false
		}
		if !validCallable(operation.Resolver) {
			diagnostics = append(diagnostics, ir.NewError("P5_CUSTOM_RESOLVER", fmt.Sprintf("custom operation %q requires a typed resolver function", operation.Name), declaration.Span))
			valid = false
		}
		if valid {
			customOperations = append(customOperations, operation)
		}
	}

	diagnostics = append(diagnostics, validateCollisions(contracts, customOperations)...)
	ir.SortDiagnostics(diagnostics)
	if hasErrors(diagnostics) {
		return diagnostics
	}
	compilation.Contract.Models = contracts
	compilation.Contract.CustomOperations = customOperations
	return diagnostics
}

type typeUse uint8

const (
	typeComputedResult typeUse = iota
	typeComputedArgument
	typeCustomResult
	typeCustomArgument
)

type graphQLTypeContext struct {
	models    map[string]ir.ModelID
	enums     map[string]struct{}
	versioned map[string]bool
}

var scalarNames = map[string]struct{}{
	"Boolean": {}, "Int": {}, "Float": {}, "String": {}, "BigInt": {}, "Decimal": {},
	"UUID": {}, "Date": {}, "Time": {}, "DateTime": {}, "Bytes": {}, "JSON": {},
}

func (context graphQLTypeContext) validate(value ir.GraphQLTypeIR, use typeUse, span ir.SourceSpan) []ir.Diagnostic {
	if value.Kind == ir.GraphQLTypeList {
		if value.Name != "" || value.Element == nil {
			return []ir.Diagnostic{ir.NewError("P5_EXTENSION_TYPE", "GraphQL list types require exactly one element and no name", span)}
		}
		return context.validate(*value.Element, use, span)
	}
	if value.Element != nil || value.Name == "" {
		return []ir.Diagnostic{ir.NewError("P5_EXTENSION_TYPE", "GraphQL leaf types require a name and no element", span)}
	}
	if value.Kind == ir.GraphQLTypeUpdateManyInput && context.versioned[value.Name] {
		return []ir.Diagnostic{ir.NewError("P5_EXTENSION_CONCURRENCY_BATCH", fmt.Sprintf("GraphQL update-many input for optimistic-concurrency model %q is not available", value.Name), span)}
	}
	known := false
	switch value.Kind {
	case ir.GraphQLTypeScalar:
		_, known = scalarNames[value.Name]
	case ir.GraphQLTypeEnum:
		_, known = context.enums[value.Name]
	case ir.GraphQLTypeModel:
		_, known = context.models[value.Name]
		known = known && (use == typeComputedResult || use == typeCustomResult)
	case ir.GraphQLTypePredicate, ir.GraphQLTypeSelector, ir.GraphQLTypeCreateInput, ir.GraphQLTypeUpdateInput, ir.GraphQLTypeUpdateManyInput:
		_, known = context.models[value.Name]
		known = known && use == typeCustomArgument
	}
	if !known {
		return []ir.Diagnostic{ir.NewError("P5_EXTENSION_TYPE", fmt.Sprintf("GraphQL %s type %q is not recognized in this position", value.Kind, value.Name), span)}
	}
	return nil
}

func (context graphQLTypeContext) validateArguments(arguments []ir.GraphQLArgumentContractIR, use typeUse, span ir.SourceSpan) []ir.Diagnostic {
	seen := map[string]bool{}
	var diagnostics []ir.Diagnostic
	for _, argument := range arguments {
		if !validName(argument.Name) {
			diagnostics = append(diagnostics, ir.NewError("P5_EXTENSION_ARGUMENT_NAME", fmt.Sprintf("%q is not a legal GraphQL argument name", argument.Name), span))
		} else if seen[argument.Name] {
			diagnostics = append(diagnostics, ir.NewError("P5_EXTENSION_ARGUMENT_DUPLICATE", fmt.Sprintf("GraphQL argument %q is declared more than once", argument.Name), span))
		}
		seen[argument.Name] = true
		diagnostics = append(diagnostics, context.validate(argument.Type, use, span)...)
	}
	return diagnostics
}

func validComputedResolver(resolver ir.AttachedMethodIR, model ir.ModelDeclIR) bool {
	return resolver.Name != "" && resolver.Receiver == model.Go && resolver.ModelID != nil && *resolver.ModelID == model.ID
}

func validCallable(resolver ir.AttachedMethodIR) bool {
	if resolver.Name == "" || (resolver.Receiver.PackagePath == "") != (resolver.Receiver.Name == "") {
		return false
	}
	if resolver.Receiver == (ir.GoNamedTypeIR{}) {
		return resolver.PackagePath != ""
	}
	return resolver.PackagePath == "" || resolver.PackagePath == resolver.Receiver.PackagePath
}

func validateCollisions(models []ir.ModelContractIR, custom []ir.CustomOperationContractIR) []ir.Diagnostic {
	queryRoots := map[string]string{}
	mutationRoots := map[string]string{}
	var diagnostics []ir.Diagnostic
	for _, model := range models {
		if !model.Exposed {
			continue
		}
		fields := map[string]string{}
		for _, field := range model.Fields {
			fields[field.GraphQLName] = "field " + string(field.FieldID)
		}
		for _, field := range model.Computed {
			if previous, duplicate := fields[field.Name]; duplicate {
				diagnostics = append(diagnostics, ir.NewError("P5_COMPUTED_COLLISION", fmt.Sprintf("computed field %s.%s collides with %s", model.GraphQLName, field.Name, previous), ir.SourceSpan{}))
			} else {
				fields[field.Name] = "computed field " + string(field.ExtensionID)
			}
		}
		enabled := map[ir.Operation]bool{}
		for _, operation := range model.Operations {
			enabled[operation] = true
		}
		generated := []struct {
			name     string
			mutation bool
			enabled  bool
			reserved bool
			owner    string
		}{
			{model.Roots.FindOne, false, enabled[ir.OperationFindOne], false, string(model.ModelID) + ".findOne"},
			{model.Roots.FindMany, false, enabled[ir.OperationFindMany], false, string(model.ModelID) + ".findMany"},
			{model.Roots.Create, true, enabled[ir.OperationCreate], false, string(model.ModelID) + ".create"},
			{model.Roots.Update, true, enabled[ir.OperationUpdate], false, string(model.ModelID) + ".update"},
			{model.Roots.Upsert, true, enabled[ir.OperationUpsert], false, string(model.ModelID) + ".upsert"},
			{model.Roots.Delete, true, enabled[ir.OperationDelete], false, string(model.ModelID) + ".delete"},
			{model.Roots.UpdateMany, true, enabled[ir.OperationUpdateMany], false, string(model.ModelID) + ".updateMany"},
			{model.Roots.DeleteMany, true, enabled[ir.OperationDeleteMany], false, string(model.ModelID) + ".deleteMany"},
			{model.Roots.Aggregate, false, model.Roots.Aggregate != "", true, string(model.ModelID) + ".reservedAggregate"},
			{model.Roots.GroupBy, false, model.Roots.GroupBy != "", true, string(model.ModelID) + ".reservedGroupBy"},
			{model.Roots.Events, false, model.Roots.Events != "", true, string(model.ModelID) + ".reservedEvents"},
		}
		for _, root := range generated {
			if !root.enabled {
				continue
			}
			registry := queryRoots
			if root.mutation {
				registry = mutationRoots
			}
			if previous, duplicate := registry[root.name]; duplicate && root.reserved {
				diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_RESERVED_ROOT_COLLISION", fmt.Sprintf("%s and %s both reserve root %q", previous, root.owner, root.name), ir.SourceSpan{}))
			} else if !duplicate {
				registry[root.name] = root.owner
			}
		}
	}
	for _, operation := range custom {
		registry := queryRoots
		if operation.Operation == ir.CustomOperationMutation {
			registry = mutationRoots
		}
		owner := fmt.Sprintf("custom %s %s", operation.Operation, operation.ExtensionID)
		if previous, duplicate := registry[operation.Name]; duplicate {
			diagnostics = append(diagnostics, ir.NewError("P5_CUSTOM_ROOT_COLLISION", fmt.Sprintf("%s and %s both use root %q", previous, owner, operation.Name), ir.SourceSpan{}))
		} else {
			registry[operation.Name] = owner
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

func hasErrors(diagnostics []ir.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == ir.SeverityError {
			return true
		}
	}
	return false
}
