// Package bindings emits and discovers P1 model-attached policy and hook
// bindings. Discovery is entirely static and uses go/types object identity.
package bindings

import (
	"context"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const Filename = "zz_golem_bindings.gen.go"

type BindingKind string
type HookOperation string
type HookPhase string

const (
	BindingPolicy BindingKind = "policy"
	BindingHook   BindingKind = "hook"

	OperationFindOne    HookOperation = "findOne"
	OperationFindFirst  HookOperation = "findFirst"
	OperationFindMany   HookOperation = "findMany"
	OperationCreate     HookOperation = "create"
	OperationUpdate     HookOperation = "update"
	OperationDelete     HookOperation = "delete"
	OperationUpdateMany HookOperation = "updateMany"
	OperationDeleteMany HookOperation = "deleteMany"

	PhaseBefore      HookPhase = "before"
	PhaseAfter       HookPhase = "after"
	PhaseAfterCommit HookPhase = "afterCommit"
)

type Entry struct {
	ModelID     ir.ModelID
	PackagePath string
	Receiver    string
	Method      string
	Kind        BindingKind
	Operation   HookOperation
	Phase       HookPhase
	Span        ir.SourceSpan
}

type File struct {
	ImportPath  string
	PackageName string
	Path        string
	Source      []byte
}

type Request struct {
	Compilation        ir.CompilationIR
	Packages           []modelcodegen.PackageSpec
	Entries            []Entry
	GolemImportPath    string
	GenerationDigest   string
	GeneratorVersion   string
	TemplateABIVersion string
}

type DiscoveryRequest struct {
	Dir                string
	ModulePath         string
	Env                []string
	Compilation        ir.CompilationIR
	Packages           []modelcodegen.PackageSpec
	ModelBootstrap     modelcodegen.Result
	GolemImportPath    string
	GenerationDigest   string
	GeneratorVersion   string
	TemplateABIVersion string
}

type Result struct {
	Files       []File
	Entries     []Entry
	Methods     []ir.AttachedMethodIR
	Diagnostics []ir.Diagnostic
}

func DiscoverAndEmit(ctx context.Context, request DiscoveryRequest) Result {
	return discoverAndEmit(ctx, request)
}

// EmitShells emits only the aliases required for the typed discovery overlay.
func EmitShells(request Request) ([]File, error) { return emit(request, false) }

// Emit emits aliases, direct bridges, and the package binding accessor.
func Emit(request Request) ([]File, error) { return emit(request, true) }
