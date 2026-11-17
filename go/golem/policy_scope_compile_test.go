package golem

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundTemplatesCannotEnterCallerPredicatesOrSelections(t *testing.T) {
	module, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	const prelude = `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Item struct{}
var scope g.BoundFactScope[Item]
func configure(rules *g.Rules[Item]) { rules.BindFactScope(scope) }
`
	for _, kind := range []string{"BoundFactScope", "BoundFact"} {
		t.Run(kind, func(t *testing.T) {
			sourcePrelude := prelude
			if kind == "BoundFact" {
				sourcePrelude = strings.ReplaceAll(strings.ReplaceAll(sourcePrelude, "BoundFactScope", "BoundFact"), "BindFactScope", "BindFact")
			}
			for _, test := range []struct {
				name, expression string
				valid            bool
			}{
				{"policy-binding", "", true},
				{"predicate-assignment", "var _ g.Predicate[Item] = scope", false},
				{"caller-where", "var _ = g.Where(scope)", false},
				{"caller-and", "var _ = g.All[Item]().And(scope)", false},
				{"caller-selection", "var _ = g.Select[Item](scope)", false},
			} {
				t.Run(test.name, func(t *testing.T) {
					directory := t.TempDir()
					mod := fmt.Sprintf("module example.test/scopeboundaries\n\ngo 1.26\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => %s\n", filepath.ToSlash(module))
					for name, source := range map[string]string{"go.mod": mod, "fixture.go": sourcePrelude + test.expression + "\n"} {
						if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0600); err != nil {
							t.Fatal(err)
						}
					}
					command := exec.Command("go", "test", "-mod=mod", ".")
					command.Dir, command.Env = directory, append(os.Environ(), "GOWORK=off")
					output, err := command.CombinedOutput()
					if test.valid && err != nil {
						t.Fatalf("policy binding did not compile: %v\n%s", err, output)
					}
					if !test.valid && (err == nil || !strings.Contains(string(output), kind)) {
						t.Fatalf("caller scope misuse was not rejected at its type boundary: err=%v\n%s", err, output)
					}
				})
			}
		})
	}
}
