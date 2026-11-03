package rowversion

import (
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/scalar"
)

func Apply(compilation *ir.CompilationIR) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	contracts := make(map[ir.ModelID]*ir.ModelContractIR, len(compilation.Contract.Models))
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		contracts[contract.ModelID] = contract
	}
	for index := range compilation.Model.Models {
		model := &compilation.Model.Models[index]
		contract := contracts[model.ID]
		diagnostics = append(diagnostics, reservedNames(*model, contract)...)
		if contract == nil || !contract.Subscriptions {
			continue
		}
		defaultValue, defaultDiagnostics := scalar.ResolveDefault(ir.LogicalTypeIR{Kind: ir.TypeInt64}, "0", scalar.DefaultContext{})
		if len(defaultDiagnostics) != 0 {
			diagnostics = append(diagnostics, defaultDiagnostics...)
			continue
		}
		identity := ir.OwnedIdentity(string(model.ID), ir.RowVersionGoName)
		registry := ir.NewIDRegistry()
		stable, diagnostic := registry.Register(ir.ObjectField, identity, ir.SourceSpan{})
		if diagnostic != nil {
			diagnostics = append(diagnostics, *diagnostic)
			continue
		}
		fieldID := ir.FieldIDFrom(stable)
		var order uint32
		for _, field := range model.Fields {
			if field.DeclarationOrder >= order {
				order = field.DeclarationOrder + 1
			}
		}
		model.Fields = append(model.Fields, ir.FieldIR{
			ID: fieldID, CanonicalIdentity: stable.Canonical, GoName: ir.RowVersionGoName, LogicalName: ir.RowVersionGoName,
			DeclarationOrder: order, Kind: ir.FieldScalar,
			Scalar: &ir.ScalarFieldIR{Column: ir.RowVersionColumn, Type: ir.LogicalTypeIR{Kind: ir.TypeInt64}, Default: defaultValue, DatabaseReadOnly: true},
		})
		contract.Fields = append(contract.Fields, ir.FieldContractIR{
			FieldID: fieldID, GraphQLName: ir.RowVersionGraphQLName, Modes: []ir.FieldMode{ir.ModeVisible, ir.ModeReadOnly},
		})
	}
	return diagnostics
}

var reservedColumns = []string{string(ir.RowVersionColumn), "_golem_version", "_version"}

func reservedNames(model ir.ModelDeclIR, contract *ir.ModelContractIR) []ir.Diagnostic {
	var diagnostics []ir.Diagnostic
	graphql := map[ir.FieldID]string{}
	if contract != nil {
		for _, field := range contract.Fields {
			graphql[field.FieldID] = field.GraphQLName
		}
	}
	for _, field := range model.Fields {
		reserved := field.GoName == ir.RowVersionGoName || field.LogicalName == ir.RowVersionGoName || graphql[field.ID] == ir.RowVersionGraphQLName
		if field.Scalar != nil {
			for _, name := range reservedColumns {
				reserved = reserved || strings.EqualFold(string(field.Scalar.Column), name)
			}
		}
		if reserved {
			diagnostics = append(diagnostics, ir.NewError("P1_ROW_VERSION_RESERVED", fmt.Sprintf("model %s field %s uses a name reserved for the golem row version (%s, %s, %s)", model.LogicalName, field.GoName, ir.RowVersionGoName, ir.RowVersionColumn, ir.RowVersionGraphQLName), ir.SourceSpan{}))
		}
	}
	return diagnostics
}
