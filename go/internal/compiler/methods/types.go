// Package methods type-checks and statically interprets GolemModel and
// GraphQL extension declarations.
//
// The interpreter consumes generated descriptor overlays; it never executes
// application code and never recovers meaning by reparsing source text.
package methods

import (
	"context"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/keyindex"
	"github.com/eleven-am/golem/go/internal/compiler/schemaexpr"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
)

// Config contains the resolved model graph and the package locations needed
// to type-check the registered model packages on a clean checkout.
type Config struct {
	Dir             string
	ModulePath      string
	Env             []string
	Compilation     ir.CompilationIR
	Packages        []modelcodegen.PackageSpec
	Bootstrap       modelcodegen.Result
	Registry        *schemaexpr.Registry
	IDRegistry      *ir.IDRegistry
	GolemImportPath string
}

// RelationOptionDeclaration is kept separate from keyindex input because
// relation linking owns foreign-key construction.
type RelationOptionDeclaration struct {
	ModelID       ir.ModelID
	RelationID    ir.RelationID
	RelationField ir.FieldID
	OnUpdate      *ir.ReferentialAction
	OnDelete      *ir.ReferentialAction
	Provider      ir.ProviderScope
	Span          ir.SourceSpan
}

type Result struct {
	Advanced        []keyindex.AdvancedModelDeclarations
	RelationOptions []RelationOptionDeclaration
	GraphQLModels   []graphqlcontract.ModelPatch
	GraphQLComputed []graphqlextension.ComputedDeclaration
	GraphQLCustom   []graphqlextension.CustomOperationDeclaration
	Diagnostics     []ir.Diagnostic
}

// Interpret loads every registered model and schema package with the fresh
// generated overlay, type-checks it, and interprets each exact GolemModel,
// model DefineGraphQL, and schema DefineGraphQL declaration.
func Interpret(ctx context.Context, config Config) Result {
	return interpret(ctx, config)
}
