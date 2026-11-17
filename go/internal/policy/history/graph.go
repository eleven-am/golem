// Package history owns immutable physical evidence for finite, compiler-owned
// capture paths. Validation establishes schema coherence and explicit extent
// completeness declarations, not database snapshot truth. Only the producer's
// transaction reader can establish that the declared extents are exhaustive.
package history

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

const (
	MaxNodes       = 16 * 1024
	MaxScalarBytes = 1 << 20
)

type Phase uint8

const (
	Before Phase = iota + 1
	After
)

// RowRef is one based; zero denotes absence and never identifies a row.
type RowRef uint32

type PhaseRow struct {
	Phase Phase
	Row   mutationdecode.Row
}

type Presence struct {
	Present bool
	Row     RowRef
}

type Root struct {
	// Identity contains exactly the declared primary-key cells, not another
	// full row image or an opaque provider-specific encoded identity.
	Identity      mutationdecode.Row
	Before, After Presence
}

type Extent struct {
	Phase    Phase
	Parent   RowRef
	Field    policyir.FieldID
	Relation policyir.RelationID
	Children []RowRef
}

type Input struct {
	Rows    []PhaseRow
	Roots   []Root
	Extents []Extent
}

type LimitError struct {
	Limit golem.Limit
	Max   int
}

func (failure *LimitError) Error() string {
	return fmt.Sprintf("policy history: %s exceeds %d", failure.Limit, failure.Max)
}

type rowIndex struct {
	phase Phase
	key   string
}
type extentIndex struct {
	phase    Phase
	parent   RowRef
	field    policyir.FieldID
	relation policyir.RelationID
}

// Graph's only authoritative rows, roots and extents are private. Its indexes
// contain references only. Accessors own their containers; decode.Row and its
// scalar values already enforce immutable cell access.
type Graph struct{ data *graphData }
type graphData struct {
	provider               policyir.Provider
	generation, projection golem.SchemaDigest
	input                  Input
	rows                   map[rowIndex]RowRef
	roots                  map[string]int
	extents                map[extentIndex]int
}

func (graph Graph) Provider() policyir.Provider {
	if graph.data == nil {
		return 0
	}
	return graph.data.provider
}
func (graph Graph) Generation() golem.SchemaDigest {
	if graph.data == nil {
		return golem.SchemaDigest{}
	}
	return graph.data.generation
}
func (graph Graph) ProjectionFingerprint() golem.SchemaDigest {
	if graph.data == nil {
		return golem.SchemaDigest{}
	}
	return graph.data.projection
}
func (graph Graph) Rows() []PhaseRow {
	if graph.data == nil {
		return nil
	}
	return append([]PhaseRow(nil), graph.data.input.Rows...)
}
func (graph Graph) Roots() []Root {
	if graph.data == nil {
		return nil
	}
	return append([]Root(nil), graph.data.input.Roots...)
}
func (graph Graph) RootKeys() []string {
	if graph.data == nil {
		return nil
	}
	result := make([]string, len(graph.data.input.Roots))
	for key, index := range graph.data.roots {
		result[index] = key
	}
	return result
}
func (graph Graph) Extents() []Extent {
	if graph.data == nil {
		return nil
	}
	result := append([]Extent(nil), graph.data.input.Extents...)
	for index := range result {
		result[index].Children = append([]RowRef(nil), result[index].Children...)
	}
	return result
}
func (graph Graph) Input() Input {
	return Input{Rows: graph.Rows(), Roots: graph.Roots(), Extents: graph.Extents()}
}
func (graph Graph) Row(reference RowRef) (PhaseRow, bool) {
	if graph.data == nil || reference == 0 || uint64(reference) > uint64(len(graph.data.input.Rows)) {
		return PhaseRow{}, false
	}
	return graph.data.input.Rows[reference-1], true
}
func (graph Graph) FindRow(phase Phase, key string) (mutationdecode.Row, bool) {
	if graph.data == nil {
		return mutationdecode.Row{}, false
	}
	row, found := graph.Row(graph.data.rows[rowIndex{phase, key}])
	return row.Row, found
}
func (graph Graph) FindRoot(phase Phase, key string) (*mutationdecode.Row, bool) {
	if graph.data == nil || phase != Before && phase != After {
		return nil, false
	}
	index, found := graph.data.roots[key]
	if !found {
		return nil, false
	}
	root := graph.data.input.Roots[index]
	presence := root.Before
	if phase == After {
		presence = root.After
	}
	if !presence.Present {
		return nil, true
	}
	row, _ := graph.Row(presence.Row)
	return &row.Row, true
}
func (graph Graph) Children(phase Phase, parentKey string, field policyir.FieldID, relation policyir.RelationID) ([]mutationdecode.Row, bool) {
	if graph.data == nil {
		return nil, false
	}
	parent, found := graph.data.rows[rowIndex{phase, parentKey}]
	if !found {
		return nil, false
	}
	index, found := graph.data.extents[extentIndex{phase, parent, field, relation}]
	if !found {
		return nil, false
	}
	extent := graph.data.input.Extents[index]
	rows := make([]mutationdecode.Row, len(extent.Children))
	for index, reference := range extent.Children {
		row, _ := graph.Row(reference)
		rows[index] = row.Row
	}
	return rows, true
}

// RowKey indexes an exact typed primary identity using the existing policy IR
// canonical owner. It is private-index material, never a wire identity codec.
// New also compares actual typed identities whenever these digests coincide.
func RowKey(registry *schema.Registry, provider policyir.Provider, row mutationdecode.Row) (string, error) {
	identity, err := mutationdecode.PrimaryIdentity(registry, row)
	if err != nil {
		return "", err
	}
	if err := mutationdecode.ValidateIdentity(registry, row.ModelID(), identity); err != nil {
		return "", err
	}
	resolver := policysql.SchemaResolver(registry)
	var conditions []policyir.Condition
	for _, cell := range identity.Components() {
		if cell.IsNull() {
			return "", fmt.Errorf("policy history: primary identity is NULL")
		}
		value, valid := cell.PolicyValue()
		if !valid {
			return "", fmt.Errorf("policy history: primary identity value is absent")
		}
		field, found := resolver.Field(provider, row.ModelID(), cell.FieldID())
		if !found {
			return "", fmt.Errorf("policy history: primary identity descriptor is absent")
		}
		operand, err := policyir.OneOperand(value)
		if err != nil {
			return "", err
		}
		condition, err := policyir.NewScalar(row.ModelID(), cell.FieldID(), field.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, nil)
		if err != nil {
			return "", err
		}
		conditions = append(conditions, condition)
	}
	if len(conditions) == 0 {
		return "", fmt.Errorf("policy history: primary identity is empty")
	}
	condition := conditions[0]
	if len(conditions) > 1 {
		condition, err = policyir.NewLogical(row.ModelID(), policyir.LogicalAnd, conditions)
		if err != nil {
			return "", err
		}
	}
	fingerprint, err := policyir.ConditionFingerprint(condition)
	if err != nil {
		return "", err
	}
	model := row.ModelID()
	return string(model[:]) + string(fingerprint[:]), nil
}

type validation struct {
	registry     *schema.Registry
	provider     policyir.Provider
	nodes, bytes int
}

func (owner *validation) charge(count int) error {
	if count < 0 || count > MaxNodes-owner.nodes {
		return &LimitError{golem.LimitPolicyHistoryNodes, MaxNodes}
	}
	owner.nodes += count
	return nil
}
func (owner *validation) row(row mutationdecode.Row, identityOnly bool) (mutationdecode.Row, error) {
	model, found := owner.registry.Model(golem.ModelID(row.ModelID()))
	if !found {
		return mutationdecode.Row{}, fmt.Errorf("policy history: row model is absent")
	}
	fields := owner.registry.PolicyCaptureFields(golem.ModelID(row.ModelID()))
	if identityOnly {
		fields = model.PrimaryKey()
	}
	cells := row.Cells()
	if len(fields) == 0 || len(cells) != len(fields) {
		return mutationdecode.Row{}, fmt.Errorf("policy history: row field inventory differs from the exact capture contract")
	}
	if err := owner.charge(len(cells)); err != nil {
		return mutationdecode.Row{}, err
	}
	want := map[policyir.FieldID]bool{}
	for _, field := range fields {
		want[policyir.FieldID(field)] = true
	}
	size := 20
	for _, cell := range cells {
		if !want[cell.FieldID()] {
			return mutationdecode.Row{}, fmt.Errorf("policy history: row has an undeclared capture scalar")
		}
		size += 21
		if !cell.IsNull() {
			value, ok := cell.PolicyValue()
			if !ok {
				return mutationdecode.Row{}, fmt.Errorf("policy history: scalar value is absent")
			}
			if enum, member, enumerated := value.Enum(); enumerated {
				if _, found := policysql.SchemaResolver(owner.registry).EnumWire(enum, member); !found {
					return mutationdecode.Row{}, fmt.Errorf("policy history: enum member is absent from the active schema")
				}
			}
			length, err := value.ScalarCanonicalSize()
			if err != nil {
				return mutationdecode.Row{}, err
			}
			if length > MaxScalarBytes-size {
				return mutationdecode.Row{}, &LimitError{golem.LimitPolicyHistoryBytes, MaxScalarBytes}
			}
			size += length
		}
	}
	if size > MaxScalarBytes-owner.bytes {
		return mutationdecode.Row{}, &LimitError{golem.LimitPolicyHistoryBytes, MaxScalarBytes}
	}
	owner.bytes += size
	validated, err := mutationdecode.NewRow(owner.registry, row.ModelID(), cells)
	if err != nil {
		return mutationdecode.Row{}, err
	}
	if !reflect.DeepEqual(row, validated) {
		return mutationdecode.Row{}, fmt.Errorf("policy history: row is not canonical under the active schema")
	}
	return validated, nil
}

// New accepts portable typed rows and references, normalizes ordering, and
// rejects duplicates instead of silently merging contradictory declarations.
// Missing extents are errors, including extents whose correlation is NULL.
func New(registry *schema.Registry, provider policyir.Provider, input Input) (Graph, error) {
	if registry == nil || provider != policyir.ProviderSQLite && provider != policyir.ProviderPostgreSQL {
		return Graph{}, fmt.Errorf("policy history: active schema and supported provider are required")
	}
	if !policysql.SchemaResolver(registry).Providers().Contains(provider) {
		return Graph{}, fmt.Errorf("policy history: provider is absent from the active schema")
	}
	if len(registry.PolicyCaptureRoots()) == 0 {
		return Graph{}, fmt.Errorf("policy history: no registered capture contract")
	}
	owner := validation{registry: registry, provider: provider}
	if err := owner.charge(len(input.Rows) + len(input.Roots) + len(input.Extents)); err != nil {
		return Graph{}, err
	}
	data := &graphData{provider: provider, generation: registry.GenerationDigest(), projection: registry.PolicyCaptureFingerprint(), rows: map[rowIndex]RowRef{}, roots: map[string]int{}, extents: map[extentIndex]int{}}
	graph := Graph{data}
	type indexedRow struct {
		old RowRef
		key string
		row PhaseRow
	}
	rows := make([]indexedRow, 0, len(input.Rows))
	identities := map[string]mutationdecode.Row{}
	remember := func(key string, row mutationdecode.Row) error {
		identity, err := primaryRow(registry, row)
		if err != nil {
			return err
		}
		if prior, found := identities[key]; found && !reflect.DeepEqual(prior, identity) {
			return fmt.Errorf("policy history: primary identity digest collision")
		}
		identities[key] = identity
		return nil
	}
	for index, entry := range input.Rows {
		if entry.Phase != Before && entry.Phase != After {
			return Graph{}, fmt.Errorf("policy history: row phase is invalid")
		}
		row, err := owner.row(entry.Row, false)
		if err != nil {
			return Graph{}, err
		}
		key, err := RowKey(registry, provider, row)
		if err != nil {
			return Graph{}, err
		}
		if err := remember(key, row); err != nil {
			return Graph{}, err
		}
		rows = append(rows, indexedRow{RowRef(index + 1), key, PhaseRow{entry.Phase, row}})
	}
	sort.Slice(rows, func(a, b int) bool {
		if rows[a].row.Phase != rows[b].row.Phase {
			return rows[a].row.Phase < rows[b].row.Phase
		}
		return rows[a].key < rows[b].key
	})
	remap := make(map[RowRef]RowRef, len(rows))
	for _, entry := range rows {
		key := rowIndex{entry.row.Phase, entry.key}
		if _, found := data.rows[key]; found {
			return Graph{}, fmt.Errorf("policy history: duplicate row primary identity in one phase")
		}
		data.input.Rows = append(data.input.Rows, entry.row)
		reference := RowRef(len(data.input.Rows))
		remap[entry.old] = reference
		data.rows[key] = reference
	}
	owners := map[policyir.ModelID]bool{}
	for _, model := range registry.PolicyCaptureRoots() {
		owners[policyir.ModelID(model)] = true
	}
	type indexedRoot struct {
		key  string
		root Root
	}
	roots := make([]indexedRoot, 0, len(input.Roots))
	for _, entry := range input.Roots {
		if !owners[entry.Identity.ModelID()] {
			return Graph{}, fmt.Errorf("policy history: root model does not own a capture template")
		}
		identity, err := owner.row(entry.Identity, true)
		if err != nil {
			return Graph{}, err
		}
		key, err := RowKey(registry, provider, identity)
		if err != nil {
			return Graph{}, err
		}
		if err := remember(key, identity); err != nil {
			return Graph{}, err
		}
		root := Root{Identity: identity}
		for _, phase := range []Phase{Before, After} {
			presence := entry.Before
			if phase == After {
				presence = entry.After
			}
			if presence.Present != (presence.Row != 0) {
				return Graph{}, fmt.Errorf("policy history: root presence has inconsistent reference payload")
			}
			if presence.Present {
				reference, found := remap[presence.Row]
				if !found {
					return Graph{}, fmt.Errorf("policy history: root row reference is out of range")
				}
				row, _ := graph.Row(reference)
				actual, err := RowKey(registry, provider, row.Row)
				if err != nil {
					return Graph{}, err
				}
				if row.Phase != phase || actual != key {
					return Graph{}, fmt.Errorf("policy history: root presence points to another phase or identity")
				}
				presence.Row = reference
			} else if _, found := data.rows[rowIndex{phase, key}]; found {
				return Graph{}, fmt.Errorf("policy history: root absence contradicts a captured row")
			}
			if phase == Before {
				root.Before = presence
			} else {
				root.After = presence
			}
		}
		if !root.Before.Present && !root.After.Present {
			return Graph{}, fmt.Errorf("policy history: affected root is absent in both phases")
		}
		roots = append(roots, indexedRoot{key, root})
	}
	sort.Slice(roots, func(a, b int) bool { return roots[a].key < roots[b].key })
	for _, entry := range roots {
		if _, found := data.roots[entry.key]; found {
			return Graph{}, fmt.Errorf("policy history: duplicate affected root")
		}
		data.roots[entry.key] = len(data.input.Roots)
		data.input.Roots = append(data.input.Roots, entry.root)
	}
	for _, entry := range input.Extents {
		if entry.Phase != Before && entry.Phase != After {
			return Graph{}, fmt.Errorf("policy history: extent phase is invalid")
		}
		parent, found := remap[entry.Parent]
		if !found {
			return Graph{}, fmt.Errorf("policy history: extent parent reference is absent")
		}
		row, _ := graph.Row(parent)
		if row.Phase != entry.Phase {
			return Graph{}, fmt.Errorf("policy history: extent parent belongs to another phase")
		}
		endpoint, found := registry.RelationEndpoint(golem.ModelID(row.Row.ModelID()), golem.FieldID(entry.Field), golem.RelationID(entry.Relation))
		if !found {
			return Graph{}, fmt.Errorf("policy history: extent relation endpoint is absent")
		}
		if endpoint.Cardinality() != compilerir.RelationOne && endpoint.Cardinality() != compilerir.RelationMany || endpoint.Cardinality() == compilerir.RelationOne && len(entry.Children) > 1 {
			return Graph{}, fmt.Errorf("policy history: extent cardinality differs from the schema")
		}
		if err := owner.charge(len(entry.Children)); err != nil {
			return Graph{}, err
		}
		extent := Extent{Phase: entry.Phase, Parent: parent, Field: entry.Field, Relation: entry.Relation, Children: make([]RowRef, 0, len(entry.Children))}
		seen := map[RowRef]bool{}
		for _, child := range entry.Children {
			reference, found := remap[child]
			if !found || seen[reference] {
				return Graph{}, fmt.Errorf("policy history: extent child reference is missing or duplicated")
			}
			seen[reference] = true
			childRow, _ := graph.Row(reference)
			if childRow.Phase != entry.Phase || childRow.Row.ModelID() != policyir.ModelID(endpoint.TargetModelID()) {
				return Graph{}, fmt.Errorf("policy history: extent child belongs to another phase or model")
			}
			if err := owner.edge(row.Row, childRow.Row, endpoint); err != nil {
				return Graph{}, err
			}
			extent.Children = append(extent.Children, reference)
		}
		sort.Slice(extent.Children, func(a, b int) bool { return extent.Children[a] < extent.Children[b] })
		data.input.Extents = append(data.input.Extents, extent)
	}
	sort.Slice(data.input.Extents, func(a, b int) bool {
		left, right := data.input.Extents[a], data.input.Extents[b]
		if left.Phase != right.Phase {
			return left.Phase < right.Phase
		}
		if left.Parent != right.Parent {
			return left.Parent < right.Parent
		}
		if left.Field != right.Field {
			return string(left.Field[:]) < string(right.Field[:])
		}
		return string(left.Relation[:]) < string(right.Relation[:])
	})
	for index, entry := range data.input.Extents {
		key := extentIndex{entry.Phase, entry.Parent, entry.Field, entry.Relation}
		if _, found := data.extents[key]; found {
			return Graph{}, fmt.Errorf("policy history: duplicate complete extent")
		}
		data.extents[key] = index
	}
	if err := owner.complete(graph); err != nil {
		return Graph{}, err
	}
	return graph, nil
}

func primaryRow(registry *schema.Registry, row mutationdecode.Row) (mutationdecode.Row, error) {
	model, found := registry.Model(golem.ModelID(row.ModelID()))
	if !found {
		return mutationdecode.Row{}, fmt.Errorf("policy history: identity model is absent")
	}
	fields := model.PrimaryKey()
	cells := make([]mutationdecode.Cell, 0, len(fields))
	for _, field := range fields {
		cell, found := row.Cell(policyir.FieldID(field))
		if !found || cell.IsNull() {
			return mutationdecode.Row{}, fmt.Errorf("policy history: primary identity scalar is absent")
		}
		cells = append(cells, cell)
	}
	return mutationdecode.NewRow(registry, row.ModelID(), cells)
}

func (owner *validation) edge(parent, child mutationdecode.Row, endpoint schema.RelationEndpoint) error {
	resolver := policysql.SchemaResolver(owner.registry)
	providers, err := policyir.NewProviderSet(owner.provider)
	if err != nil {
		return err
	}
	for _, pair := range endpoint.Correlation() {
		if err := owner.charge(1); err != nil {
			return err
		}
		left, found := parent.Cell(policyir.FieldID(pair.ParentFieldID()))
		if !found || left.IsNull() {
			return fmt.Errorf("policy history: edge has absent or NULL parent correlation")
		}
		right, found := child.Cell(policyir.FieldID(pair.ChildFieldID()))
		if !found || right.IsNull() {
			return fmt.Errorf("policy history: edge has absent or NULL child correlation")
		}
		value, _ := left.PolicyValue()
		operand, err := policyir.OneOperand(value)
		if err != nil {
			return err
		}
		field, found := resolver.Field(owner.provider, child.ModelID(), right.FieldID())
		if !found {
			return fmt.Errorf("policy history: edge scalar descriptor is absent")
		}
		requirements, err := operator.ValidateShape(policyir.OperatorEqual, operator.Shape{Node: policyir.ConditionScalar, FieldType: field.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: providers})
		if err != nil {
			return err
		}
		condition, err := policyir.NewScalar(child.ModelID(), right.FieldID(), field.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if err != nil {
			return err
		}
		target, _ := right.PolicyValue()
		entry, err := evaluate.ValueField(right.FieldID(), target)
		if err != nil {
			return err
		}
		record, err := evaluate.NewRecord(child.ModelID(), entry)
		if err != nil {
			return err
		}
		matched, err := evaluate.Condition(condition, record, providers)
		if err != nil {
			return err
		}
		if !matched {
			return fmt.Errorf("policy history: child does not match its declared correlation")
		}
	}
	return nil
}

func (owner *validation) complete(graph Graph) error {
	usedRows := map[RowRef]bool{}
	usedExtents := map[extentIndex]bool{}
	// Registry accessors own their returned slices. Take one local immutable
	// copy per owner model, rather than cloning the static path tree for every
	// affected identity and phase.
	paths := map[policyir.ModelID][]scopecontract.CaptureProjection{}
	for _, root := range graph.data.input.Roots {
		modelID := root.Identity.ModelID()
		projections, cached := paths[modelID]
		if !cached {
			if err := owner.charge(1); err != nil {
				return err
			}
			projections = owner.registry.PolicyCaptureProjections(golem.ModelID(modelID))
			paths[modelID] = projections
		}
		for _, phase := range []Phase{Before, After} {
			presence := root.Before
			if phase == After {
				presence = root.After
			}
			if !presence.Present {
				continue
			}
			usedRows[presence.Row] = true
			for _, projection := range projections {
				if err := owner.charge(1); err != nil {
					return err
				}
				current := []RowRef{presence.Row}
				model := golem.ModelID(root.Identity.ModelID())
				for _, hop := range projection.Path {
					endpoint, err := CaptureEndpoint(owner.registry, model, hop)
					if err != nil {
						return err
					}
					model = endpoint.TargetModelID()
					next := map[RowRef]bool{}
					for _, parent := range current {
						if err := owner.charge(1); err != nil {
							return err
						}
						key := extentIndex{phase, parent, policyir.FieldID(endpoint.FieldID()), policyir.RelationID(endpoint.RelationID())}
						index, known := graph.data.extents[key]
						if !known {
							return fmt.Errorf("policy history: registered path has an unknown relation extent")
						}
						usedExtents[key] = true
						for _, child := range graph.data.input.Extents[index].Children {
							if err := owner.charge(1); err != nil {
								return err
							}
							usedRows[child] = true
							next[child] = true
						}
					}
					current = current[:0]
					for child := range next {
						current = append(current, child)
					}
					sort.Slice(current, func(a, b int) bool { return current[a] < current[b] })
				}
				if compilerir.ModelID(hex.EncodeToString(model[:])) != projection.ModelID {
					return fmt.Errorf("policy history: registered path terminal model differs")
				}
			}
		}
	}
	if len(usedRows) != len(graph.data.input.Rows) || len(usedExtents) != len(graph.data.input.Extents) {
		return fmt.Errorf("policy history: evidence contains rows or extents outside the registered root paths")
	}
	return nil
}

// CaptureEndpoint is the shared interpretation of a compiler-owned projection
// hop. It requires every identity, direction and correlation pair to match one
// exact schema endpoint; callers cannot substitute a similar physical path.
func CaptureEndpoint(registry *schema.Registry, model golem.ModelID, hop scopecontract.ProjectionHop) (schema.RelationEndpoint, error) {
	if registry == nil {
		return schema.RelationEndpoint{}, fmt.Errorf("policy history: active schema is required for a capture endpoint")
	}
	if compilerir.ModelID(hex.EncodeToString(model[:])) != hop.ModelID {
		return schema.RelationEndpoint{}, fmt.Errorf("policy history: registered path parent differs")
	}
	field, err := fixedID(string(hop.FieldID))
	if err != nil {
		return schema.RelationEndpoint{}, err
	}
	relation, err := fixedID(string(hop.RelationID))
	if err != nil {
		return schema.RelationEndpoint{}, err
	}
	endpoint, found := registry.RelationEndpoint(model, golem.FieldID(field), golem.RelationID(relation))
	target := endpoint.TargetModelID()
	if !found || compilerir.ModelID(hex.EncodeToString(target[:])) != hop.TargetModelID {
		return schema.RelationEndpoint{}, fmt.Errorf("policy history: registered path endpoint differs")
	}
	pairs := endpoint.Correlation()
	if len(pairs) == 0 || len(pairs) != len(hop.ParentFields) || len(pairs) != len(hop.ChildFields) {
		return schema.RelationEndpoint{}, fmt.Errorf("policy history: registered path correlation is incomplete")
	}
	for index, pair := range pairs {
		parent, child := pair.ParentFieldID(), pair.ChildFieldID()
		if compilerir.FieldID(hex.EncodeToString(parent[:])) != hop.ParentFields[index] || compilerir.FieldID(hex.EncodeToString(child[:])) != hop.ChildFields[index] {
			return schema.RelationEndpoint{}, fmt.Errorf("policy history: registered path correlation differs")
		}
	}
	if endpoint.Cardinality() != compilerir.RelationOne && endpoint.Cardinality() != compilerir.RelationMany {
		return schema.RelationEndpoint{}, fmt.Errorf("policy history: registered path relation cardinality is unknown")
	}
	return endpoint, nil
}
func fixedID(text string) ([16]byte, error) {
	var id [16]byte
	decoded, err := hex.DecodeString(text)
	if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != text {
		return id, fmt.Errorf("policy history: stable path identity is invalid")
	}
	copy(id[:], decoded)
	return id, nil
}
