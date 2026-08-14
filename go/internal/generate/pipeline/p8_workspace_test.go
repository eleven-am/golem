package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProspectivePackageResolutionSupportsNestedExternalWorkspace(t *testing.T) {
	root := t.TempDir()
	consumer := filepath.Join(root, "consumer")
	if err := os.MkdirAll(consumer, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(root, "go.mod"):     "module example.test/framework\n\ngo 1.25.0\n",
		filepath.Join(consumer, "go.mod"): "module example.test/application\n\ngo 1.25.0\n\nrequire example.test/framework v0.0.0\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workspace := filepath.Join(root, "external.go.work")
	work := "go 1.25.0\n\nuse (\n\t" + filepath.ToSlash(root) + "\n\t" + filepath.ToSlash(consumer) + "\n)\n\nreplace example.test/framework v0.0.0 => " + filepath.ToSlash(root) + "\n"
	if err := os.WriteFile(workspace, []byte(work), 0o600); err != nil {
		t.Fatal(err)
	}

	environment := append(os.Environ(), "GOWORK="+workspace)
	flags, cleanup, err := prospectivePackageBuildFlags(context.Background(), consumer, environment)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !reflect.DeepEqual(flags, []string{"-mod=readonly"}) {
		t.Fatalf("workspace prospective flags = %v", flags)
	}
	if matches, err := filepath.Glob(filepath.Join(consumer, ".golem-prospective-*.mod")); err != nil || len(matches) != 0 {
		t.Fatalf("workspace resolution created alternate modfile: matches=%v err=%v", matches, err)
	}

	offEnvironment := append(os.Environ(), "GOWORK=off")
	flags, cleanup, err = prospectivePackageBuildFlags(context.Background(), consumer, offEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 2 || flags[0] != "-mod=mod" || !strings.HasPrefix(flags[1], "-modfile=") {
		t.Fatalf("ordinary prospective flags = %v", flags)
	}
	modfile := strings.TrimPrefix(flags[1], "-modfile=")
	if _, err := os.Stat(modfile); err != nil {
		t.Fatalf("ordinary resolution omitted alternate modfile: %v", err)
	}
	cleanup()
	if _, err := os.Stat(modfile); !os.IsNotExist(err) {
		t.Fatalf("ordinary alternate modfile survived cleanup: %v", err)
	}
}
