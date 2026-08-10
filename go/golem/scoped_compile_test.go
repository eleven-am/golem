package golem_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestP6ScopedPublicCompileFailRedTeam(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate module")
	}
	module := filepath.Dir(filepath.Dir(filename))
	tests := []struct{ name, source string }{
		{"raw SQL method", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
func invalid(query g.ScopedQuery[Model]) { query.Raw("SELECT secret") }
`},
		{"raw identifier constructor", `package fixture
import g "github.com/eleven-am/golem/go/golem"
var _ = g.Identifier("private_column")
`},
		{"custom ON predicate", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
func invalid(query g.ScopedQuery[Model], root g.Scope[Model]) { query.JoinOn(root, "1 = 1") }
`},
		{"DDL method", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
func invalid(query g.ScopedQuery[Model]) { query.CreateTable("exfiltration") }
`},
		{"subquery method", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
func invalid(query g.ScopedQuery[Model]) { query.Subquery() }
`},
		{"CTE method", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
func invalid(query g.ScopedQuery[Model]) { query.With("leak", query) }
`},
		{"union method", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
func invalid(query g.ScopedQuery[Model]) { query.Union(query) }
`},
		{"window method", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
func invalid(query g.ScopedQuery[Model]) { query.Window("row_number() over ()") }
`},
		{"forged selection node", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
type raw struct{}
func invalid(root g.Scope[Model]) { _ = g.From(root).Select(raw{}) }
`},
		{"forged join relation", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
type Other struct{}
type rawRelation struct{}
func invalid(root g.Scope[Model]) { _ = g.InnerJoin[Model, Other](root, rawRelation{}) }
`},
		{"write and connection escape", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
func invalid(query g.ScopedQuery[Model]) { query.Exec(); query.DB(); query.Delete() }
`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			goMod := fmt.Sprintf("module example.test/p6scopedcompile\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => %s\n", module)
			if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(goMod), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "fixture.go"), []byte(test.source), 0o644); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("go", "test", "-mod=mod", ".")
			command.Dir = directory
			command.Env = append(os.Environ(), "GOWORK=off")
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatalf("invalid scoped program compiled:\n%s", output)
			}
		})
	}
}
