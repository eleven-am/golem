package completion

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCompletionCatalogsFreezeExactRequiredGatesAndProfiles(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		spec     Spec
		packages int
		tests    int
		watch    int
	}{
		{name: "docs", spec: DocumentationSpec(root, 30*time.Minute), packages: 1, tests: 11, watch: 4},
		{name: "compat", spec: CompatibilitySpec(root, 15*time.Minute), packages: 2, tests: 14, watch: 1},
		{name: "failure", spec: FailureSpec(root, 30*time.Minute), packages: 1, tests: 20, watch: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !validSpec(test.spec) || len(test.spec.Packages) != test.packages || len(test.spec.WatchPaths) != test.watch {
				t.Fatalf("invalid catalog shape: %#v", test.spec)
			}
			count := 0
			for _, pkg := range test.spec.Packages {
				count += len(pkg.Tests)
			}
			if count != test.tests {
				t.Fatalf("catalog test count=%d want=%d", count, test.tests)
			}
			if len(test.spec.Profiles) != 3 || test.spec.Profiles[0] != "postgresql-c" || test.spec.Profiles[1] != "postgresql-linguistic" || test.spec.Profiles[2] != "sqlite" {
				t.Fatalf("catalog profiles=%#v", test.spec.Profiles)
			}
		})
	}
}
