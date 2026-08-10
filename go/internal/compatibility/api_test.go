package compatibility

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildAPIInventoryLoadFailureIsClosedAndRedacted(t *testing.T) {
	canary := "p8-secret-path-canary"
	_, err := BuildAPIInventory(context.Background(), APIRequest{
		Directory: filepath.Join(t.TempDir(), canary, "missing"),
		Patterns:  []string{"./..."},
	})
	if err == nil {
		t.Fatal("expected package load failure")
	}
	if got := err.Error(); got != "load API inventory" || strings.Contains(got, canary) || strings.Contains(got, t.TempDir()) {
		t.Fatalf("package load failure was not closed: %q", got)
	}
}

func TestPublicAPISemanticDiffSeparatesAdditiveAndBreakingChanges(t *testing.T) {
	base := APIInventory{FormatVersion: APIInventoryFormatVersion, Packages: []APIPackage{{
		Path: "example.test/api",
		Declarations: []APIDeclaration{
			{Name: "Client", Kind: "type", TypeKind: "struct", Signature: "struct{}", Methods: []string{"pointer:DoThingfunc(string) error"}},
			{Name: "Open", Kind: "func", Signature: "func(string) (*example.test/api.Client, error)", Methods: []string{}},
		},
	}}}
	additive := cloneAPIInventory(base)
	additive.Packages[0].Declarations = append(additive.Packages[0].Declarations,
		APIDeclaration{Name: "Version", Kind: "const", Signature: "string=\"v1\"", Methods: []string{}},
	)
	methodAdditive := cloneAPIInventory(base)
	methodAdditive.Packages[0].Declarations[0].Methods = append(methodAdditive.Packages[0].Declarations[0].Methods, "pointer:Pingfunc() error")
	breaking := cloneAPIInventory(base)
	breaking.Packages[0].Declarations[1].Signature = "func([]byte) (*example.test/api.Client, error)"
	if got := CompareAPI(base, base); got != LayerUnchanged {
		t.Fatalf("unchanged classification = %q", got)
	}
	if got := CompareAPI(base, additive); got != LayerAdditive {
		t.Fatalf("declaration addition classification = %q", got)
	}
	if got := CompareAPI(base, methodAdditive); got != LayerAdditive {
		t.Fatalf("method addition classification = %q", got)
	}
	if got := CompareAPI(base, breaking); got != LayerBreaking {
		t.Fatalf("signature change classification = %q", got)
	}
}

func TestInterfaceMethodAdditionIsBreaking(t *testing.T) {
	base := APIInventory{FormatVersion: APIInventoryFormatVersion, Packages: []APIPackage{{
		Path:         "example.test/api",
		Declarations: []APIDeclaration{{Name: "Runner", Kind: "type", TypeKind: "interface", Signature: "interface{Runfunc() error}", Methods: []string{"value:Runfunc() error"}}},
	}}}
	current := cloneAPIInventory(base)
	current.Packages[0].Declarations[0].Methods = []string{"value:Closefunc() error", "value:Runfunc() error"}
	if got := CompareAPI(base, current); got != LayerBreaking {
		t.Fatalf("interface method addition classification = %q", got)
	}
}

func TestGenericAndEmbeddedInterfaceMethodAdditionsAreBreaking(t *testing.T) {
	generic := APIInventory{FormatVersion: APIInventoryFormatVersion, Packages: []APIPackage{{
		Path: "example.test/api",
		Declarations: []APIDeclaration{{
			Name: "Decoder", Kind: "type", TypeKind: "interface",
			Signature: "[T any]interface{Decodefunc([]byte) (T, error)}",
			Methods:   []string{"value:Decodefunc([]byte) (T, error)"},
		}},
	}}}
	grown := cloneAPIInventory(generic)
	grown.Packages[0].Declarations[0].Methods = append(grown.Packages[0].Declarations[0].Methods, "value:Resetfunc()")
	if got := CompareAPI(generic, grown); got != LayerBreaking {
		t.Fatalf("generic interface method addition classification = %q", got)
	}

	embedded := cloneAPIInventory(generic)
	embedded.Packages[0].Declarations[0].Methods = []string{"value:Closefunc() error", "value:Decodefunc([]byte) (T, error)"}
	if got := CompareAPI(generic, embedded); got != LayerBreaking {
		t.Fatalf("embedded interface expansion classification = %q", got)
	}
}

func TestExportedStructFieldAdditionAndRemovalAreBreaking(t *testing.T) {
	base := APIInventory{FormatVersion: APIInventoryFormatVersion, Packages: []APIPackage{{
		Path:         "example.test/api",
		Declarations: []APIDeclaration{{Name: "Config", Kind: "type", TypeKind: "struct", Signature: "struct{Name string}", Methods: []string{}}},
	}}}
	added := cloneAPIInventory(base)
	added.Packages[0].Declarations[0].Signature = "struct{Name string;Limit int}"
	removed := cloneAPIInventory(base)
	removed.Packages[0].Declarations[0].Signature = "struct{}"
	if got := CompareAPI(base, added); got != LayerBreaking {
		t.Fatalf("exported struct field addition classification = %q", got)
	}
	if got := CompareAPI(base, removed); got != LayerBreaking {
		t.Fatalf("exported struct field removal classification = %q", got)
	}
}

func cloneAPIInventory(value APIInventory) APIInventory {
	result := APIInventory{FormatVersion: value.FormatVersion, Packages: make([]APIPackage, len(value.Packages))}
	for index, pkg := range value.Packages {
		result.Packages[index] = APIPackage{Path: pkg.Path, Declarations: make([]APIDeclaration, len(pkg.Declarations))}
		for declarationIndex, declaration := range pkg.Declarations {
			result.Packages[index].Declarations[declarationIndex] = declaration
			result.Packages[index].Declarations[declarationIndex].Methods = append([]string(nil), declaration.Methods...)
		}
	}
	return result
}
