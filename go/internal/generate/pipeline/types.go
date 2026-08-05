// Package pipeline composes the complete P1 generation graph in memory. It
// deliberately stops before publication, CLI behavior, or migration history.
package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/internal/codegen/bindings"
	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

type ProviderOptions struct {
	Provider ir.Provider
	Options  physical.LowerOptions
}

type Request struct {
	Compile            compile.Config
	AppPackage         modelcodegen.PackageSpec
	Lowerers           []physical.Lowerer
	LowerOptions       []ProviderOptions
	PreviousManifest   *manifest.Manifest
	GolemImportPath    string
	GeneratorVersion   string
	TemplateABIVersion string
	Env                []string
}

type ProviderResult struct {
	Provider          physical.ProviderManifest
	Schema            physical.PhysicalSchema
	Fingerprint       ir.Fingerprint
	SystemFingerprint ir.Fingerprint
}

type Result struct {
	Prospective         manifest.Result
	Compilation         ir.CompilationIR
	ModelFingerprint    ir.Fingerprint
	ContractFingerprint ir.Fingerprint
	ModulePath          string `json:"-"`
	ModuleDir           string `json:"-"`
	Bindings            []bindings.Entry
	Providers           []ProviderResult
}

type DiagnosticsError struct {
	Diagnostics []ir.Diagnostic
}

func (e *DiagnosticsError) Error() string {
	parts := make([]string, len(e.Diagnostics))
	for index, diagnostic := range e.Diagnostics {
		parts[index] = fmt.Sprintf("%s: %s", diagnostic.Code, diagnostic.Message)
	}
	return strings.Join(parts, "; ")
}

func Build(ctx context.Context, request Request) (Result, error) {
	return build(ctx, request)
}
