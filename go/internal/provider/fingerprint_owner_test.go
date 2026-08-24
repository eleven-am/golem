package provider_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

var bypassedFingerprintOwners = map[string]bool{
	"PhysicalFingerprint":             true,
	"SystemFingerprint":               true,
	"HistoricalPhysicalFingerprint":   true,
	"HistoricalSystemFingerprint":     true,
	"HistoricalV3PhysicalFingerprint": true,
	"HistoricalV3SystemFingerprint":   true,
}

func TestSchemaVerificationRoutesThroughTheFingerprintOwner(t *testing.T) {
	for _, directory := range []string{"sqlite", "postgresql", "handle"} {
		t.Run(directory, func(t *testing.T) {
			fileSet := token.NewFileSet()
			packages, err := parser.ParseDir(fileSet, directory, func(entry fs.FileInfo) bool {
				return !strings.HasSuffix(entry.Name(), "_test.go")
			}, 0)
			if err != nil {
				t.Fatal(err)
			}
			scanned := 0
			for _, parsed := range packages {
				for path, file := range parsed.Files {
					scanned++
					ast.Inspect(file, func(node ast.Node) bool {
						selector, ok := node.(*ast.SelectorExpr)
						if !ok {
							return true
						}
						qualifier, ok := selector.X.(*ast.Ident)
						if !ok || qualifier.Name != "physical" || !bypassedFingerprintOwners[selector.Sel.Name] {
							return true
						}
						t.Errorf("%s bypasses the schema verification owner by calling physical.%s directly", path, selector.Sel.Name)
						return true
					})
				}
			}
			if scanned == 0 {
				t.Fatal("no provider sources were scanned")
			}
		})
	}
}
