package history_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestGraphCanonicalReferencesAndImmutableOwnership(t *testing.T) {
	fixture, input := graphInput(t)
	graph, err := history.New(fixture.Registry, policyir.ProviderSQLite, input)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Generation() != fixture.Registry.GenerationDigest() || graph.ProjectionFingerprint() != fixture.Registry.PolicyCaptureFingerprint() || graph.Provider() != policyir.ProviderSQLite {
		t.Fatal("graph lost its exact generation/capture/provider provenance")
	}
	canonical := graph.Input()
	// Reverse every row and extent input position, keeping references intact.
	count := len(input.Rows)
	for left, right := 0, count-1; left < right; left, right = left+1, right-1 {
		input.Rows[left], input.Rows[right] = input.Rows[right], input.Rows[left]
	}
	remap := func(ref history.RowRef) history.RowRef {
		if ref == 0 {
			return 0
		}
		return history.RowRef(count+1) - ref
	}
	for index := range input.Roots {
		input.Roots[index].Before.Row = remap(input.Roots[index].Before.Row)
		input.Roots[index].After.Row = remap(input.Roots[index].After.Row)
	}
	for index := range input.Extents {
		input.Extents[index].Parent = remap(input.Extents[index].Parent)
		for child := range input.Extents[index].Children {
			input.Extents[index].Children[child] = remap(input.Extents[index].Children[child])
		}
	}
	for left, right := 0, len(input.Extents)-1; left < right; left, right = left+1, right-1 {
		input.Extents[left], input.Extents[right] = input.Extents[right], input.Extents[left]
	}
	normalized, err := history.New(fixture.Registry, policyir.ProviderSQLite, input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(canonical, normalized.Input()) {
		t.Fatal("input order leaked into canonical row references")
	}
	key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, canonical.Roots[0].Identity)
	if err != nil {
		t.Fatal(err)
	}
	if row, known := graph.FindRoot(history.Before, key); !known || row == nil {
		t.Fatal("present source root became unknown")
	}
	if _, known := graph.FindRoot(history.After, "unregistered identity"); known {
		t.Fatal("unknown source became known absent")
	}
	if _, known := graph.Row(0); known {
		t.Fatal("zero reference names a row")
	}
	for _, phase := range []history.Phase{history.Before, history.After} {
		for _, root := range graph.Roots() {
			key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, root.Identity)
			if err != nil {
				t.Fatal(err)
			}
			children, known := graph.Children(phase, key, policyir.FieldID(fixture.UserPosts), policyir.RelationID(fixture.Authorship))
			if !known {
				t.Fatal("complete extent became unknown")
			}
			if len(children) > 0 {
				children[0] = mutationdecode.Row{}
			}
		}
	}
	input.Rows[0].Row = mutationdecode.Row{}
	input.Roots[0].Identity = mutationdecode.Row{}
	for index := range input.Extents {
		if len(input.Extents[index].Children) > 0 {
			input.Extents[index].Children[0] = 0
		}
	}
	returned := graph.Input()
	returned.Rows[0].Row = mutationdecode.Row{}
	returned.Roots[0].Identity = mutationdecode.Row{}
	for index := range returned.Extents {
		if len(returned.Extents[index].Children) > 0 {
			returned.Extents[index].Children[0] = 0
		}
	}
	keys := graph.RootKeys()
	keys[0] = "modified"
	if !reflect.DeepEqual(canonical, graph.Input()) {
		t.Fatal("constructor or accessor retained a mutable caller container")
	}
}

func TestGraphRefusesUnknownContradictoryOrUnregisteredEvidence(t *testing.T) {
	fixture, baseline := graphInput(t)
	tests := []struct {
		name   string
		mutate func(*history.Input)
	}{
		{"missing explicit empty extent", func(input *history.Input) { input.Extents = append(input.Extents[:2], input.Extents[3:]...) }},
		{"cross phase child", func(input *history.Input) { input.Extents[0].Children = []history.RowRef{6} }},
		{"wrong correlation", func(input *history.Input) { input.Extents[1].Children = []history.RowRef{3} }},
		{"zero child", func(input *history.Input) { input.Extents[0].Children = []history.RowRef{0} }},
		{"out of range parent", func(input *history.Input) { input.Extents[0].Parent = 99 }},
		{"duplicate child", func(input *history.Input) { input.Extents[0].Children = []history.RowRef{3, 3} }},
		{"duplicate extent", func(input *history.Input) { input.Extents = append(input.Extents, input.Extents[0]) }},
		{"duplicate row", func(input *history.Input) { input.Rows = append(input.Rows, input.Rows[0]) }},
		{"duplicate root", func(input *history.Input) { input.Roots = append(input.Roots, input.Roots[0]) }},
		{"inactive presence payload", func(input *history.Input) { input.Roots[0].Before.Present = false }},
		{"presence without row", func(input *history.Input) { input.Roots[0].Before.Row = 0 }},
		{"absence contradicts row", func(input *history.Input) { input.Roots[0].Before = history.Presence{} }},
		{"root cross phase", func(input *history.Input) { input.Roots[0].Before.Row = 4 }},
		{"both phases absent", func(input *history.Input) {
			input.Roots[0].Before = history.Presence{}
			input.Roots[0].After = history.Presence{}
		}},
		{"foreign endpoint", func(input *history.Input) { input.Extents[0].Relation = policyir.RelationID{0xff} }},
		{"unregistered relation path", func(input *history.Input) {
			input.Extents = append(input.Extents, history.Extent{Phase: history.Before, Parent: 3, Field: policyir.FieldID(fixture.PostAuthor), Relation: policyir.RelationID(fixture.Authorship), Children: []history.RowRef{1}})
		}},
		{"unreachable row", func(input *history.Input) {
			input.Rows = append(input.Rows, history.PhaseRow{Phase: history.Before, Row: graphPost(t, fixture, 52, 1, "extra")})
		}},
		{"missing capture scalar", func(input *history.Input) {
			input.Rows[2].Row = graphRow(t, fixture.Registry, fixture.Post, mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: 51})), mutationdecode.Value(policyir.FieldID(fixture.AuthorID), policyir.UUIDValue([16]byte{15: 1})))
		}},
		{"extra capture scalar", func(input *history.Input) {
			cells := input.Rows[2].Row.Cells()
			version, err := policyir.SignedValue(policyir.ValueInt64, 1)
			if err != nil {
				t.Fatal(err)
			}
			cells = append(cells, mutationdecode.Value(policyir.FieldID(fixture.PostRowVersion), version))
			input.Rows[2].Row = graphRow(t, fixture.Registry, fixture.Post, cells...)
		}},
		{"non PK root cells", func(input *history.Input) { input.Roots[0].Identity = input.Rows[0].Row }},
		{"non owner root model", func(input *history.Input) {
			input.Roots = append(input.Roots, history.Root{Identity: graphRow(t, fixture.Registry, fixture.Post, mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: 51}))), Before: history.Presence{Present: true, Row: 3}, After: history.Presence{Present: true, Row: 6}})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneGraphInput(baseline)
			test.mutate(&input)
			graph, err := history.New(fixture.Registry, policyir.ProviderSQLite, input)
			if err == nil || len(graph.Rows()) != 0 {
				t.Fatalf("invalid evidence was accepted: %v", err)
			}
		})
	}
}

func TestGraphKnownAbsenceIsDistinctFromEmptyAndUnknown(t *testing.T) {
	fixture, input := graphInput(t)
	// Both sources were newly created. Before-phase rows and extents must
	// disappear altogether; absence is represented on the source identities.
	input.Rows = input.Rows[3:]
	for index := range input.Roots {
		input.Roots[index].Before = history.Presence{}
		input.Roots[index].After.Row -= 3
	}
	input.Extents = input.Extents[2:]
	for index := range input.Extents {
		input.Extents[index].Parent -= 3
		for child := range input.Extents[index].Children {
			input.Extents[index].Children[child] -= 3
		}
	}
	graph, err := history.New(fixture.Registry, policyir.ProviderSQLite, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range graph.Roots() {
		key, err := history.RowKey(fixture.Registry, policyir.ProviderSQLite, root.Identity)
		if err != nil {
			t.Fatal(err)
		}
		if row, known := graph.FindRoot(history.Before, key); !known || row != nil {
			t.Fatal("source absence became unknown or present")
		}
		if _, known := graph.Children(history.Before, key, policyir.FieldID(fixture.UserPosts), policyir.RelationID(fixture.Authorship)); known {
			t.Fatal("absent parent acquired a fabricated complete extent")
		}
	}
}

func TestGraphBoundsAllInputNodesAndScalarPayload(t *testing.T) {
	fixture, input := graphInput(t)
	input.Extents[0].Children = make([]history.RowRef, history.MaxNodes+1)
	_, err := history.New(fixture.Registry, policyir.ProviderSQLite, input)
	var limit *history.LimitError
	if !errors.As(err, &limit) || limit.Limit != golem.LimitPolicyHistoryNodes {
		t.Fatalf("edge budget: %v", err)
	}
	_, input = graphInput(t)
	input.Rows[0].Row = graphUser(t, fixture, 1, strings.Repeat("x", history.MaxScalarBytes+1))
	_, err = history.New(fixture.Registry, policyir.ProviderSQLite, input)
	if !errors.As(err, &limit) || limit.Limit != golem.LimitPolicyHistoryBytes {
		t.Fatalf("scalar budget: %v", err)
	}
}

func graphInput(t *testing.T) (schematest.GraphFixture, history.Input) {
	t.Helper()
	fixture := schematest.NewPolicyCaptureGraph(t)
	input := history.Input{Rows: []history.PhaseRow{
		{Phase: history.Before, Row: graphUser(t, fixture, 1, "source")}, {Phase: history.Before, Row: graphUser(t, fixture, 2, "source")}, {Phase: history.Before, Row: graphPost(t, fixture, 51, 1, "before")},
		{Phase: history.After, Row: graphUser(t, fixture, 1, "source")}, {Phase: history.After, Row: graphUser(t, fixture, 2, "source")}, {Phase: history.After, Row: graphPost(t, fixture, 51, 2, "after")},
	}}
	for index, id := range []byte{1, 2} {
		input.Roots = append(input.Roots, history.Root{Identity: graphRow(t, fixture.Registry, fixture.User, mutationdecode.Value(policyir.FieldID(fixture.UserID), policyir.UUIDValue([16]byte{15: id}))), Before: history.Presence{Present: true, Row: history.RowRef(index + 1)}, After: history.Presence{Present: true, Row: history.RowRef(index + 4)}})
	}
	for _, entry := range []struct {
		phase    history.Phase
		parent   history.RowRef
		children []history.RowRef
	}{{history.Before, 1, []history.RowRef{3}}, {history.Before, 2, nil}, {history.After, 4, nil}, {history.After, 5, []history.RowRef{6}}} {
		input.Extents = append(input.Extents, history.Extent{Phase: entry.phase, Parent: entry.parent, Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship), Children: entry.children})
	}
	return fixture, input
}
func graphUser(t *testing.T, fixture schematest.GraphFixture, id byte, name string) mutationdecode.Row {
	return graphRow(t, fixture.Registry, fixture.User, mutationdecode.Value(policyir.FieldID(fixture.UserID), policyir.UUIDValue([16]byte{15: id})), mutationdecode.Value(policyir.FieldID(fixture.UserName), graphText(t, name)))
}
func graphPost(t *testing.T, fixture schematest.GraphFixture, id, author byte, title string) mutationdecode.Row {
	return graphRow(t, fixture.Registry, fixture.Post, mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: id})), mutationdecode.Value(policyir.FieldID(fixture.AuthorID), policyir.UUIDValue([16]byte{15: author})), mutationdecode.Value(policyir.FieldID(fixture.PostTitle), graphText(t, title)))
}
func graphText(t *testing.T, text string) policyir.Value {
	t.Helper()
	value, err := policyir.StringValue(text)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func graphRow(t *testing.T, registry *schema.Registry, model golem.ModelID, cells ...mutationdecode.Cell) mutationdecode.Row {
	t.Helper()
	row, err := mutationdecode.NewRow(registry, policyir.ModelID(model), cells)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func cloneGraphInput(input history.Input) history.Input {
	result := history.Input{Rows: append([]history.PhaseRow(nil), input.Rows...), Roots: append([]history.Root(nil), input.Roots...), Extents: append([]history.Extent(nil), input.Extents...)}
	for index := range result.Extents {
		result.Extents[index].Children = append([]history.RowRef(nil), result.Extents[index].Children...)
	}
	return result
}
