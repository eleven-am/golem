// Package methods type-checks and statically interprets GolemModel methods.
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
	Diagnostics     []ir.Diagnostic
}

// Interpret loads every registered model package with the fresh generated
// overlay, type-checks it, and interprets each exact GolemModel declaration.
func Interpret(ctx context.Context, config Config) Result {
	return interpret(ctx, config)
}
