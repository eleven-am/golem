package provider_test

import (
	"fmt"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestP8PublicProviderAPICompilesFromCleanExternalModule(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve provider test path")
	}
	moduleRoot := filepath.Dir(filepath.Dir(file))
	consumer := filepath.Join(t.TempDir(), "consumer")
	if err := os.MkdirAll(consumer, 0o755); err != nil {
		t.Fatal(err)
	}
	consumer, err := filepath.EvalSymlinks(consumer)
	if err != nil {
		t.Fatal(err)
	}
	consumerModule := `module example.com/golem-provider-consumer

go 1.25.0
`
	if strings.Contains(consumerModule, "replace") {
		t.Fatal("clean consumer module must not contain a replace directive")
	}
	consumerSource := `package consumer

import (
	"context"

	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

func compileFrozenProviderAPI(ctx context.Context, database *provider.Database) {
	_, _ = sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:app.sqlite"})
	_ = sqlite.CheckpointForBackup(ctx, database)
	_, _ = postgresql.Open(ctx, postgresql.Config{
		DataSourceName: "postgresql://user@localhost/app",
		Pool: postgresql.PoolConfig{MaximumOpen: 4, MaximumIdle: 2},
	})
	_ = database.Provider()
	_ = database.Capabilities().Provider()
	_ = database.Capabilities().ServerVersion()
	_ = database.Capabilities().Features()
	_ = database.Pool().MaximumOpen()
	_ = database.Pool().MaximumIdle()
	_ = database.Pool().ConnectionMaximumLifetime()
	_ = database.Pool().ConnectionMaximumIdleTime()
	_ = database.UnsafeSQLX()
	_ = database.Close()
	_, _ = provider.CodeOf(nil)
}
`
	if strings.Contains(consumerSource, "/internal/") {
		t.Fatal("clean consumer imports a Golem internal package")
	}
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(consumerModule), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer, "provider_test.go"), []byte(consumerSource), 0o644); err != nil {
		t.Fatal(err)
	}

	// This workspace proves the local pre-release API without putting a replace
	// directive in the consumer. Clean tag resolution without go.work is the
	// separate release-artifact gate in P8 evidence row 22.
	workspace := filepath.Join(t.TempDir(), "go.work")
	workspaceSource := fmt.Sprintf("go 1.25.0\n\nuse (\n\t%q\n\t%q\n)\n", moduleRoot, consumer)
	if err := os.WriteFile(workspace, []byte(workspaceSource), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = consumer
	command.Env = append(os.Environ(), "GOWORK="+workspace)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile clean provider consumer: %v\n%s", err, output)
	}
}

func TestP8PublicPackageInventoryHasNoInternalTypeLeak(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve provider test path")
	}
	moduleRoot := filepath.Dir(filepath.Dir(file))
	paths := []string{
		"github.com/eleven-am/golem/go/provider",
		"github.com/eleven-am/golem/go/provider/sqlite",
		"github.com/eleven-am/golem/go/provider/postgresql",
	}
	loaded, err := packages.Load(&packages.Config{
		Dir:  moduleRoot,
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
	}, paths...)
	if err != nil {
		t.Fatal(err)
	}
	if packages.PrintErrors(loaded) != 0 || len(loaded) != len(paths) {
		t.Fatalf("load public provider packages: loaded=%d want=%d", len(loaded), len(paths))
	}

	expectedObjects := map[string][]string{
		paths[0]: {
			"Capabilities", "Code", "CodeClose", "CodeConfig", "CodeMaintenance", "CodeOf", "CodeOpen", "Database", "Feature",
			"FeatureAdvisoryLocks", "FeatureAnalyticsExact", "FeatureForeignKeys", "FeatureGeneratedColumns", "FeatureJSON",
			"FeaturePolicyASCIIText", "FeaturePolicyBinaryText", "FeaturePolicyExactJSON", "FeaturePolicyRelation", "FeaturePolicyScalarList",
			"PoolStatus", "Version",
		},
		paths[1]: {"CheckpointForBackup", "Config", "Open"},
		paths[2]: {"Config", "Open", "PoolConfig"},
	}
	expectedMethods := map[string]map[string][]string{
		paths[0]: {
			"Capabilities": {"Features", "Provider", "ServerVersion"},
			"Database":     {"Capabilities", "Close", "Pool", "Provider", "UnsafeSQLX"},
			"PoolStatus":   {"ConnectionMaximumIdleTime", "ConnectionMaximumLifetime", "MaximumIdle", "MaximumOpen"},
		},
	}
	expectedFields := map[string]map[string][]string{
		paths[0]: {"Version": {"Major", "Minor", "Patch"}},
		paths[1]: {"Config": {"DataSourceName"}},
		paths[2]: {
			"Config":     {"DataSourceName", "Pool"},
			"PoolConfig": {"ConnectionMaximumIdleTime", "ConnectionMaximumLifetime", "MaximumIdle", "MaximumOpen"},
		},
	}

	for _, loadedPackage := range loaded {
		path := loadedPackage.PkgPath
		scope := loadedPackage.Types.Scope()
		actualObjects := exportedScopeNames(scope)
		if !equalStrings(actualObjects, expectedObjects[path]) {
			t.Fatalf("%s exported objects=%v want=%v", path, actualObjects, expectedObjects[path])
		}
		for _, name := range actualObjects {
			object := scope.Lookup(name)
			assertNoInternalType(t, path+"."+name, object.Type())
		}
		for typeName, methods := range expectedMethods[path] {
			named := namedType(t, scope, typeName)
			actual := exportedMethodNames(types.NewMethodSet(types.NewPointer(named)))
			if !equalStrings(actual, methods) {
				t.Fatalf("%s.%s methods=%v want=%v", path, typeName, actual, methods)
			}
			for index := 0; index < types.NewMethodSet(types.NewPointer(named)).Len(); index++ {
				method := types.NewMethodSet(types.NewPointer(named)).At(index).Obj()
				if method.Exported() {
					assertNoInternalType(t, path+"."+typeName+"."+method.Name(), method.Type())
				}
			}
		}
		for typeName, fields := range expectedFields[path] {
			named := namedType(t, scope, typeName)
			structure, ok := named.Underlying().(*types.Struct)
			if !ok {
				t.Fatalf("%s.%s is not a struct", path, typeName)
			}
			actual := make([]string, 0, structure.NumFields())
			for index := 0; index < structure.NumFields(); index++ {
				field := structure.Field(index)
				if field.Exported() {
					actual = append(actual, field.Name())
					assertNoInternalType(t, path+"."+typeName+"."+field.Name(), field.Type())
				}
			}
			sort.Strings(actual)
			if !equalStrings(actual, fields) {
				t.Fatalf("%s.%s fields=%v want=%v", path, typeName, actual, fields)
			}
		}
	}
}

func exportedScopeNames(scope *types.Scope) []string {
	result := make([]string, 0, scope.Len())
	for _, name := range scope.Names() {
		if scope.Lookup(name).Exported() {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func namedType(t *testing.T, scope *types.Scope, name string) *types.Named {
	t.Helper()
	object, ok := scope.Lookup(name).(*types.TypeName)
	if !ok {
		t.Fatalf("%s is not a named type", name)
	}
	named, ok := object.Type().(*types.Named)
	if !ok {
		t.Fatalf("%s is an alias", name)
	}
	return named
}

func exportedMethodNames(set *types.MethodSet) []string {
	result := make([]string, 0, set.Len())
	for index := 0; index < set.Len(); index++ {
		method := set.At(index).Obj()
		if method.Exported() {
			result = append(result, method.Name())
		}
	}
	sort.Strings(result)
	return result
}

func assertNoInternalType(t *testing.T, owner string, value types.Type) {
	t.Helper()
	representation := types.TypeString(value, func(pkg *types.Package) string { return pkg.Path() })
	if strings.Contains(representation, "/internal/") {
		t.Fatalf("%s leaks internal type in %s", owner, representation)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
