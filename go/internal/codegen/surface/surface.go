// Package surface emits the in-memory generated declarations that source
// packages are type-checked against before their final artifacts exist. Every
// file comes from the emitter that produces the corresponding published
// artifact, run over the same compilation, so the surface declares every
// generated symbol the published code declares.
package surface

import (
	"fmt"
	"path/filepath"
	"strings"

	eventcodegen "github.com/eleven-am/golem/go/internal/codegen/event"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/codegen/registry"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/graphql/adaptersurface"
)

// Request selects the compilation whose generated surface is emitted and the
// package locations its files are overlaid onto.
type Request struct {
	Compilation     ir.CompilationIR
	Packages        []modelcodegen.PackageSpec
	AppPackage      modelcodegen.PackageSpec
	GolemImportPath string
}

// Emit returns the model descriptors, event values, application registry, and
// GraphQL adapter declarations of the compilation as one overlay-ready result
// whose manifest is the one the model emitter reports.
func Emit(request Request) (modelcodegen.Result, error) {
	if request.GolemImportPath == "" {
		request.GolemImportPath = modelcodegen.DefaultGolemImportPath
	}
	result, err := modelcodegen.Emit(modelcodegen.Request{Compilation: request.Compilation, Packages: request.Packages, GolemImportPath: request.GolemImportPath})
	if err != nil {
		return modelcodegen.Result{}, err
	}
	events, err := eventcodegen.Emit(eventcodegen.Request{
		Compilation: request.Compilation, Packages: request.Packages, GolemImportPath: request.GolemImportPath,
		FinalStamp: modelcodegen.FinalStamp{GenerationDigest: strings.Repeat("0", 64), GeneratorVersion: "bootstrap", TemplateABIVersion: "bootstrap"},
	})
	if err != nil {
		return modelcodegen.Result{}, err
	}
	for _, file := range events {
		result.Files = append(result.Files, modelcodegen.File{ImportPath: file.ImportPath, PackageName: file.PackageName, Path: file.Path, Source: file.Source})
	}
	application, err := registry.EmitSurface(registry.SurfaceRequest{
		AppPackage: request.AppPackage, ModelPackages: request.Packages, Actor: request.Compilation.Model.Schema.Actor,
		Model: request.Compilation.Model, Contract: request.Compilation.Contract, GolemImportPath: request.GolemImportPath,
	})
	if err != nil {
		return modelcodegen.Result{}, err
	}
	result.Files = append(result.Files, modelcodegen.File{ImportPath: application.ImportPath, PackageName: application.PackageName, Path: application.Path, Source: application.Source})
	adapter, err := adaptersurface.Emit(request.AppPackage.PackageName, "", request.GolemImportPath)
	if err != nil {
		return modelcodegen.Result{}, fmt.Errorf("GraphQL surface: %w", err)
	}
	path := adaptersurface.Filename
	if request.AppPackage.Directory != "" {
		path = filepath.Join(request.AppPackage.Directory, adaptersurface.Filename)
	}
	result.Files = append(result.Files, modelcodegen.File{ImportPath: request.AppPackage.ImportPath, PackageName: request.AppPackage.PackageName, Path: path, Source: adapter})
	return result, nil
}
