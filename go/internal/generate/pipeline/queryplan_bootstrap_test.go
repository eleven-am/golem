package pipeline

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/manifest"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
)

func TestQueryPlanDeclarationDiscoverySupersetAndFinalExactRegistryBothCompile(t *testing.T) {
	directory := filepath.Join(moduleRoot(t), "internal", "compiler", "compile", "testdata", "queryplan")
	result, err := Build(context.Background(), Request{
		Compile: compile.Config{Dir: moduleRoot(t), Pattern: "./internal/compiler/compile/testdata/queryplan", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{
			ImportPath:  "github.com/eleven-am/golem/go/internal/compiler/compile/testdata/queryplan",
			PackageName: "queryplanfixture",
			Directory:   directory,
		},
		Lowerers: defaultLowerers(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var registrySource []byte
	for _, artifact := range result.Prospective.Artifacts {
		if artifact.Kind == manifest.ArtifactRegistryGo {
			registrySource = artifact.Content
		}
	}
	for _, method := range []string{"ExplainFindMany", "ExplainFindFirst", "ExplainFindUnique", "ExplainCount", "ExplainAggregate", "ExplainGroupBy", "ExplainRelationGroupBy", "ExplainScoped"} {
		if !bytes.Contains(registrySource, []byte("func (client CallerPostClient[P]) "+method+"(")) {
			t.Errorf("prospective registry is missing CallerPostClient.%s:\n%s", method, registrySource)
		}
		for _, forbidden := range []string{"SystemPostClient", "CallerTxPostClient", "SystemTxPostClient"} {
			if bytes.Contains(registrySource, []byte("func (client "+forbidden+"[P]) "+method+"(")) {
				t.Errorf("prospective registry leaked %s.%s:\n%s", forbidden, method, registrySource)
			}
		}
	}
}
