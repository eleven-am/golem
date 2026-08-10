package golem_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestP6PublicABICapabilityCompileMatrix(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate module")
	}
	module := filepath.Dir(filepath.Dir(filename))
	tests := []struct {
		name    string
		source  string
		compile bool
	}{
		{"ordered integer dimension", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
var dimension g.Dimension[Model, int64]
var _ = g.DimensionGT(dimension, int64(1))
`, true},
		{"generated supported aggregate capability matrix", `package fixture
import (
	g "github.com/eleven-am/golem/go/golem"
	m "github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)
var _ = m.Metrics.AggregateSelect(
	m.Metrics.CountAll(),
	m.Metrics.Small.Sum(),
	m.Metrics.Small.Avg(),
	m.Metrics.Small.Min(),
	m.Metrics.Small.Max(),
	m.Metrics.Float.Sum(),
	m.Metrics.Double.Avg(),
	m.Metrics.Amount.Sum(),
	m.Metrics.Amount.Avg(),
	m.Metrics.Label.Min(),
	m.Metrics.Label.Max(),
)
var _ = m.Metrics.RelationGroupBy(
	m.Metrics.RelationGroupDimensions(m.Metrics.CategoryParentName),
	m.Metrics.RelationGroupMeasures(m.Metrics.Small.Sum()),
	m.Metrics.RelationGroupHaving(g.TextMeasureContains(m.Metrics.Label.Min(), "needle", g.DefaultComparison())),
)
`, true},
		{"bool has no ordered predicate", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
var dimension g.Dimension[Model, bool]
var _ = g.DimensionGT(dimension, true)
`, false},
		{"uuid has no ordered predicate", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
var dimension g.Dimension[Model, g.UUID]
var _ = g.DimensionGT(dimension, g.UUID{})
`, false},
		{"foreign model measure", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
type Other struct{}
func accept(g.AggregateMeasure[Model]) {}
func invalid(value g.Measure[Other, int64]) { accept(value) }
`, false},
		{"bool exposes no sum constructor", `package fixture
import m "github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
var _ = m.Metrics.Flag.Sum()
`, false},
		{"uuid exposes no minimum constructor", `package fixture
import m "github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
var _ = m.Metrics.Reference.Min()
`, false},
		{"enum exposes no average constructor", `package fixture
import m "github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
var _ = m.Metrics.State.Avg()
`, false},
		{"string exposes no sum constructor", `package fixture
import m "github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
var _ = m.Metrics.Label.Sum()
`, false},
		{"bytes exposes no dimension constructor", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
var field = g.GeneratedBytesField[Model](g.FieldID{})
var _ = field.Dimension()
`, false},
		{"bytes exposes no count constructor", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
var field = g.GeneratedBytesField[Model](g.FieldID{})
var _ = field.Count()
`, false},
		{"bytes exposes no sum constructor", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
var field = g.GeneratedBytesField[Model](g.FieldID{})
var _ = field.Sum()
`, false},
		{"JSON exposes no dimension constructor", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
var field = g.GeneratedJSONField[Model](g.FieldID{})
var _ = field.Dimension()
`, false},
		{"JSON exposes no count constructor", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
var field = g.GeneratedJSONField[Model](g.FieldID{})
var _ = field.Count()
`, false},
		{"JSON exposes no sum constructor", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
var field = g.GeneratedJSONField[Model](g.FieldID{})
var _ = field.Sum()
`, false},
		{"dimension is not an aggregate measure", `package fixture
import m "github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
var _ = m.Metrics.AggregateSelect(m.Metrics.Label.Dimension())
`, false},
		{"relation dimension is not a local dimension", `package fixture
import m "github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
var _ = m.Metrics.GroupDimensions(m.Metrics.CategoryParentName)
`, false},
		{"relation dimension is not a measure", `package fixture
import m "github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
var _ = m.Metrics.RelationGroupMeasures(m.Metrics.CategoryParentName)
`, false},
		{"related model measure cannot aggregate roots", `package fixture
import m "github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
var _ = m.Metrics.RelationGroupMeasures(m.Categories.Name.Min())
`, false},
		{"text measure predicates reject non text measures", `package fixture
import (
	g "github.com/eleven-am/golem/go/golem"
	m "github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)
var _ = g.TextMeasureContains(m.Metrics.Amount.Min(), g.Decimal{}, g.DefaultComparison())
`, false},
		{"interfaces remain sealed", `package fixture
import g "github.com/eleven-am/golem/go/golem"
type Model struct{}
type forged struct{}
func invalid() g.LocalGroupDimension[Model] { return forged{} }
`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			goMod := fmt.Sprintf("module example.test/p6compile\n\ngo 1.25\n\nrequire github.com/eleven-am/golem/go v0.0.0\nreplace github.com/eleven-am/golem/go => %s\n", module)
			if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(goMod), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "fixture.go"), []byte(test.source), 0o644); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("go", "test", "-mod=mod", ".")
			command.Dir = directory
			command.Env = append(os.Environ(), "GOWORK=off")
			output, err := command.CombinedOutput()
			if test.compile && err != nil {
				t.Fatalf("valid capability did not compile: %v\n%s", err, output)
			}
			if !test.compile && err == nil {
				t.Fatalf("invalid capability compiled:\n%s", output)
			}
		})
	}
}
