package extension

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
)

const (
	CustomBoundHint   = "declare it with golem.MaxItems"
	ComputedBoundHint = "pass it as the second argument of golem.GraphQLList"
)

func SearchMaximum(contract ir.ModelContractIR) uint32 {
	maximum := uint32(semanticruntime.MaximumResults)
	for _, candidate := range []uint32{contract.Limits.MaxPageSize, contract.Limits.MaxTake} {
		if candidate > 0 && candidate < maximum {
			maximum = candidate
		}
	}
	return maximum
}

func searchResult(contract ir.ModelContractIR) ir.GraphQLTypeIR {
	return ir.GraphQLTypeIR{
		Kind: ir.GraphQLTypeList, Nullable: false, MaxItems: SearchMaximum(contract),
		Element: &ir.GraphQLTypeIR{Kind: ir.GraphQLTypeModel, Name: contract.GraphQLName, Nullable: false},
	}
}

func ResultBoundDiagnostics(owner, hint string, value ir.GraphQLTypeIR, span ir.SourceSpan) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	for _, violation := range value.ResultListBoundViolations() {
		switch violation.Fault {
		case ir.ListBoundMissing:
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_LIST_MAX_REQUIRED", fmt.Sprintf("%s result list level %d has no maximum item count: %s", owner, violation.Level+1, hint), span))
		case ir.ListBoundStray:
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_LIST_MAX_SCALAR", fmt.Sprintf("%s result level %d is not a list and cannot carry a maximum item count", owner, violation.Level+1), span))
		}
	}
	return diagnostics
}

func argumentBoundDiagnostics(owner string, arguments []ir.GraphQLArgumentContractIR, span ir.SourceSpan) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	for _, argument := range arguments {
		if len(argument.Type.ArgumentListBoundViolations()) != 0 {
			diagnostics = append(diagnostics, ir.NewError("P5_GRAPHQL_LIST_MAX_SCALAR", fmt.Sprintf("%s argument %s cannot carry a maximum item count", owner, argument.Name), span))
		}
	}
	return diagnostics
}

func ValidateListBounds(contract ir.ContractIR) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	for _, model := range contract.Models {
		for _, field := range model.Computed {
			owner := fmt.Sprintf("computed field %s.%s", model.GraphQLName, field.Name)
			diagnostics = append(diagnostics, ResultBoundDiagnostics(owner, ComputedBoundHint, field.Result, ir.SourceSpan{})...)
			diagnostics = append(diagnostics, argumentBoundDiagnostics(owner, field.Arguments, ir.SourceSpan{})...)
		}
	}
	for _, operation := range contract.CustomOperations {
		owner := fmt.Sprintf("custom %s %s", operation.Operation, operation.Name)
		diagnostics = append(diagnostics, ResultBoundDiagnostics(owner, CustomBoundHint, operation.Result, ir.SourceSpan{})...)
		diagnostics = append(diagnostics, argumentBoundDiagnostics(owner, operation.Arguments, ir.SourceSpan{})...)
	}
	return diagnostics
}

func SearchTakeDefault(compilation ir.CompilationIR, operation ir.CustomOperationContractIR) (uint32, bool) {
	if !(IsSemanticSearchOperation(compilation, operation) || IsSemanticSimilarOperation(compilation, operation) || IsFullTextSearchOperation(compilation, operation)) {
		return 0, false
	}
	for _, contract := range compilation.Contract.Models {
		if contract.GraphQLName != searchResultModelName(operation.Result) {
			continue
		}
		maximum := SearchMaximum(contract)
		if contract.Limits.DefaultPageSize < maximum {
			return contract.Limits.DefaultPageSize, true
		}
		return maximum, true
	}
	return 0, false
}

func searchResultModelName(result ir.GraphQLTypeIR) string {
	if result.Kind == ir.GraphQLTypeList && result.Element != nil {
		return result.Element.Name
	}
	return result.Name
}
