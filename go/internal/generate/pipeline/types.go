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
	graphqlcodegen "github.com/eleven-am/golem/go/internal/graphql/codegen"
	"github.com/eleven-am/golem/go/internal/migration"
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
	// ReadOnlyDiagnostics skips only the prospective generated Go package load,
	// whose alternate modfile is necessarily created inside the consumer module.
	// Source compilation, binding discovery, lowering, and complete prospective
	// artifact construction remain enabled for read-only compatibility checks.
	ReadOnlyDiagnostics bool
	// ProspectiveModfileDir opts a read-only diagnostics request back into the
	// exact generated-graph compile while placing its alternate go.mod/go.sum
	// outside the consumer module. The caller owns and removes this directory.
	ProspectiveModfileDir string
	// ReviewedMigrations are verified immutable histories embedded into the
	// generated application. Production CLI generation supplies exactly one
	// non-empty history for every declared provider.
	ReviewedMigrations []ReviewedMigration
	// GraphQLExecutables shares pinned gqlgen runs across every Build that
	// receives the same cache. Build supplies its own cache when it is nil.
	GraphQLExecutables *graphqlcodegen.ExecutableCache
}

type ReviewedMigration struct {
	Manifest migration.Manifest
	Files    map[string][]byte
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

// Analyze runs every Build stage that reviewed migration history cannot
// change: source compilation, binding discovery, contract canonicalization,
// and provider lowering. Build is Analyze followed by Analysis.Build.
func Analyze(ctx context.Context, request Request) (*Analysis, error) {
	return analyze(ctx, request)
}

// Build emits, stamps, and prospectively compiles the analysed graph with the
// given reviewed migration history in place of Request.ReviewedMigrations.
func (analysis *Analysis) Build(ctx context.Context, reviewed []ReviewedMigration) (Result, error) {
	return analysis.build(ctx, reviewed)
}

func (analysis *Analysis) ModelFingerprint() ir.Fingerprint {
	return analysis.modelFingerprint
}

func (analysis *Analysis) Providers() []ProviderResult {
	return append([]ProviderResult(nil), analysis.providers...)
}
