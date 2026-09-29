package methods

import (
	"context"
	"strings"
	"testing"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
)

func TestNewCompilationEmitsVersionedFullTextNormalization(t *testing.T) {
	result := Interpret(context.Background(), Config{
		Dir:         moduleDirectory(t),
		Compilation: fixtureCompilation(),
		Packages:    []modelcodegen.PackageSpec{{ImportPath: fixturePackage, PackageName: "basic", Directory: fixtureDirectory(t, "basic")}},
	})
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
	found := 0
	for _, extension := range result.Extensions {
		if extension.Kind != fulltextcontract.IndexKind {
			continue
		}
		found++
		index, err := fulltextcontract.Decode(extension.Payload)
		if err != nil || index.Normalization != fulltextcontract.NormalizationNFCLower || !strings.Contains(extension.Payload, `"normalization":"nfc-lower"`) {
			t.Fatalf("full-text payload=%s index=%#v err=%v; want normalization %q", extension.Payload, index, err, fulltextcontract.NormalizationNFCLower)
		}
	}
	if found == 0 {
		t.Fatal("fixture compiled no full-text index")
	}
}
