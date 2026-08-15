// Package event emits deterministic typed event values and event-schema
// registries for subscription-enabled models.
package event

import (
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const Filename = "zz_golem_events.gen.go"

type Request struct {
	Compilation       ir.CompilationIR
	Packages          []modelcodegen.PackageSpec
	GolemImportPath   string
	RuntimeImportPath string
	FinalStamp        modelcodegen.FinalStamp
}

type File struct {
	ImportPath  string
	PackageName string
	Path        string
	Source      []byte
}
