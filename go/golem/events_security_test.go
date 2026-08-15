package golem

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExternalCodeCannotForgeEventMetadataRuntimeCapability(t *testing.T) {
	module, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"lookalike view": `package fixture
import (
  "time"
  g "github.com/eleven-am/golem/go/golem"
)
type forged struct{}
func (forged) RuntimeEventID() g.EventID { return g.EventID{1} }
func (forged) RuntimeEventAction() g.EventAction { return g.EventCreated }
func (forged) RuntimeEventCausationID() g.CausationID { return g.CausationID{1} }
func (forged) RuntimeEventTransactionOrdinal() uint32 { return 1 }
func (forged) RuntimeEventRecordedAt() time.Time { return time.Now() }
func (forged) RuntimeEventGenerationDigest() g.SchemaDigest { return g.SchemaDigest{1} }
func (forged) RuntimeEventSchemaDigest() (g.EventSchemaDigest, bool) { return g.EventSchemaDigest{1}, true }
func (forged) RuntimeEventModelID() g.ModelID { return g.ModelID{1} }
var _, _ = g.RuntimeValidatedEventMetadata(forged{})
`,
		"internal token import": `package fixture
import m "github.com/eleven-am/golem/go/internal/event/metadatavalue"
var _ = m.New(m.Input{})
`,
		"private representation": `package fixture
import g "github.com/eleven-am/golem/go/golem"
var _ = g.EventMetadata{eventID: g.EventID{1}}
`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			goMod := fmt.Sprintf("module example.test/p7metadata\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => %s\n", module)
			if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(goMod), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "fixture.go"), []byte(source), 0o644); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("go", "test", "-mod=mod", ".")
			command.Dir = directory
			command.Env = append(os.Environ(), "GOWORK=off")
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatalf("external metadata forgery compiled:\n%s", output)
			}
		})
	}
}
