// Package model emits the same-package descriptor bootstrap used by generated
// Golem model code. Emission is deliberately in-memory so callers can pass the
// returned files directly to go/packages through an Overlay.
package model

import (
	"path/filepath"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

const (
	// DefaultGolemImportPath is the public package imported by generated source.
	DefaultGolemImportPath = "github.com/eleven-am/golem/go/golem"
	// BootstrapFilename is the stable filename used for every emitted package.
	BootstrapFilename = "zz_golem_bootstrap.go"
)

// PackageSpec supplies the Go package information that is intentionally absent
// from the canonical compiler IR. Directory may be empty when callers only need
// source; set it when Result.Overlay will be passed to go/packages.
type PackageSpec struct {
	ImportPath  string
	PackageName string
	Directory   string
}

// Request describes one deterministic bootstrap emission.
type Request struct {
	Compilation     ir.CompilationIR
	Packages        []PackageSpec
	GolemImportPath string
}

// File is one generated, formatted Go source file.
type File struct {
	ImportPath  string
	PackageName string
	Path        string
	Source      []byte
}

// SymbolKind classifies a generated public symbol.
type SymbolKind string

const (
	SymbolModelDescriptor SymbolKind = "model_descriptor"
	SymbolNamespace       SymbolKind = "namespace"
	SymbolField           SymbolKind = "field"
	SymbolRelation        SymbolKind = "relation"
	SymbolSelector        SymbolKind = "selector"
)

// Symbol maps a generated Go symbol back to the canonical stable identifier
// consumed by later typed interpretation. Namespace is empty for package-level
// symbols; Name is the member name for namespace fields.
type Symbol struct {
	PackagePath string
	Namespace   string
	Name        string
	Kind        SymbolKind

	ModelID    ir.ModelID
	FieldID    ir.FieldID
	RelationID ir.RelationID
	KeyID      ir.KeyID
}

// Manifest is sorted by package, namespace, name, and kind.
type Manifest struct {
	Symbols []Symbol
}

// Result contains generated files and their stable symbol map.
type Result struct {
	Files    []File
	Manifest Manifest
}

// Overlay returns a fresh path-to-source map suitable for packages.Config.
func (r Result) Overlay() map[string][]byte {
	overlay := make(map[string][]byte, len(r.Files))
	for _, file := range r.Files {
		path := file.Path
		if path == "" {
			path = filepath.Join(file.ImportPath, BootstrapFilename)
		}
		overlay[path] = append([]byte(nil), file.Source...)
	}
	return overlay
}
