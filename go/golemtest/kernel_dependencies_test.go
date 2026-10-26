package golemtest

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var kernelPackages = []string{"./golem", "./runtime", "./events/...", "./queue/...", "./provider/...", "./observe/...", "./embedding/...", "./queryplan/..."}

var forbiddenKernelDependencyPrefixes = []string{
	"github.com/99designs/gqlgen",
	"github.com/vektah/gqlparser",
	"github.com/eleven-am/golem/go/internal/graphql",
}

func TestKernelPackagesDoNotDependOnGraphQL(t *testing.T) {
	command := exec.Command("go", append([]string{"list", "-deps"}, kernelPackages...)...)
	command.Dir = filepath.Dir(packageDirectory(t))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < len(kernelPackages) {
		t.Fatalf("go list -deps returned %d packages", len(lines))
	}
	for _, line := range lines {
		if violation := forbiddenKernelDependency(strings.TrimSpace(line)); violation != "" {
			t.Errorf("kernel package closure depends on %s", violation)
		}
	}
}

func forbiddenKernelDependency(path string) string {
	if path == "github.com/eleven-am/golem/go/graphql" || strings.HasPrefix(path, "github.com/eleven-am/golem/go/graphql/") {
		return path
	}
	for _, prefix := range forbiddenKernelDependencyPrefixes {
		if strings.HasPrefix(path, prefix) {
			return path
		}
	}
	return ""
}
