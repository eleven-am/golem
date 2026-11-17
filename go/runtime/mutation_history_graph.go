package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

// This is a transaction-local physical graph, not predicate truth or durable
// evidence. Rows are shared by canonical identity within each phase. Every
// relation entry is a complete extent; absence of an entry means unknown.
type mutationHistoryGraph struct {
	snapshot *mutationHistorySnapshot
	value    policyhistory.Graph
}

// Mutable assembly state is released when the shared Graph is sealed.
type mutationHistoryGraphDraft struct {
	snapshot *mutationHistorySnapshot
	roots    []mutationHistoryRoot
	phases   map[mutationHistoryPhase]*mutationHistoryGraphPhase
}

type mutationHistoryRoot struct {
	model policyir.ModelID
	key   string
}

type mutationHistoryGraphRelation struct {
	parent   string
	field    policyir.FieldID
	relation policyir.RelationID
}

type mutationHistoryGraphPhase struct {
	rows      map[string]mutationdecode.Row
	roots     map[string]bool // true: present, false: known absent
	relations map[mutationHistoryGraphRelation][]string
}

func (graph *mutationHistoryGraph) check() error {
	if graph == nil || graph.snapshot == nil {
		return fmt.Errorf("policy history: graph is unavailable")
	}
	return graph.snapshot.check()
}

func (graph *mutationHistoryGraph) affectedRoots() []mutationHistoryRoot {
	roots, keys := graph.value.Roots(), graph.value.RootKeys()
	result := make([]mutationHistoryRoot, len(roots))
	for index, root := range roots {
		result[index] = mutationHistoryRoot{root.Identity.ModelID(), keys[index]}
	}
	return result
}
func (graph *mutationHistoryGraph) root(phase mutationHistoryPhase, key string) (*mutationdecode.Row, bool) {
	return graph.value.FindRoot(policyhistory.Phase(phase), key)
}
func (graph *mutationHistoryGraph) row(phase mutationHistoryPhase, key string) (mutationdecode.Row, bool) {
	return graph.value.FindRow(policyhistory.Phase(phase), key)
}
func (graph *mutationHistoryGraph) children(phase mutationHistoryPhase, parent string, field policyir.FieldID, relation policyir.RelationID) ([]mutationdecode.Row, bool) {
	return graph.value.Children(policyhistory.Phase(phase), parent, field, relation)
}

type mutationHistoryGraphPath struct {
	root  policyir.ModelID
	model policyir.ModelID
	hops  []schema.RelationEndpoint
}

type mutationHistoryGraphCandidate struct {
	root     mutationHistoryRoot
	identity []mutationdecode.Cell
}

type mutationHistoryGraphBuilder struct {
	graph      *mutationHistoryGraphDraft
	paths      map[policyir.ModelID][]mutationHistoryGraphPath
	ending     map[policyir.ModelID][]mutationHistoryGraphPath
	candidates map[string]mutationHistoryGraphCandidate
	queries    map[string][]string
}

func newMutationHistoryGraph(ctx context.Context, snapshot *mutationHistorySnapshot) (_ *mutationHistoryGraph, graphErr error) {
	if snapshot == nil {
		return nil, fmt.Errorf("policy history: snapshot is required for graph construction")
	}
	defer func() {
		if graphErr != nil && snapshot.failure == nil {
			snapshot.failure = graphErr
		}
	}()
	if ctx == nil {
		return nil, fmt.Errorf("policy history: graph context is required")
	}
	if err := snapshot.check(); err != nil {
		return nil, err
	}
	graph := &mutationHistoryGraphDraft{snapshot: snapshot, phases: map[mutationHistoryPhase]*mutationHistoryGraphPhase{}}
	for _, phase := range []mutationHistoryPhase{mutationHistoryBefore, mutationHistoryAfter} {
		graph.phases[phase] = &mutationHistoryGraphPhase{rows: map[string]mutationdecode.Row{}, roots: map[string]bool{}, relations: map[mutationHistoryGraphRelation][]string{}}
	}
	builder := mutationHistoryGraphBuilder{graph: graph, paths: map[policyir.ModelID][]mutationHistoryGraphPath{}, ending: map[policyir.ModelID][]mutationHistoryGraphPath{}, candidates: map[string]mutationHistoryGraphCandidate{}, queries: map[string][]string{}}
	if err := builder.compile(ctx); err != nil {
		return nil, err
	}
	for _, phase := range []mutationHistoryPhase{mutationHistoryBefore, mutationHistoryAfter} {
		for _, image := range snapshot.images {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			row := image.before
			if phase == mutationHistoryAfter {
				row = image.after
			}
			if row == nil {
				continue
			}
			for _, path := range builder.ending[row.ModelID()] {
				if err := builder.charge(); err != nil {
					return nil, err
				}
				if err := builder.reverse(ctx, phase, *row, path); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, candidate := range builder.candidates {
		graph.roots = append(graph.roots, candidate.root)
	}
	sort.Slice(graph.roots, func(left, right int) bool { return graph.roots[left].key < graph.roots[right].key })
	for _, phase := range []mutationHistoryPhase{mutationHistoryBefore, mutationHistoryAfter} {
		for _, root := range graph.roots {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			candidate := builder.candidates[root.key]
			rows, err := builder.read(ctx, phase, root.model, candidate.identity)
			if err != nil {
				return nil, err
			}
			if len(rows) > 1 {
				return nil, fmt.Errorf("policy history: exact root identity returned multiple rows")
			}
			if err := builder.charge(); err != nil {
				return nil, err
			}
			graph.phases[phase].roots[root.key] = len(rows) == 1
			if len(rows) == 0 {
				continue
			}
			key, err := policyhistory.RowKey(snapshot.registry, snapshot.provider, rows[0])
			if err != nil {
				return nil, err
			}
			if key != root.key {
				return nil, fmt.Errorf("policy history: root query changed its exact identity")
			}
			for _, path := range builder.paths[root.model] {
				if err := builder.forward(ctx, phase, rows[0], path); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := snapshot.check(); err != nil {
		return nil, err
	}
	return builder.seal(ctx)
}

func (builder *mutationHistoryGraphBuilder) charge() error {
	snapshot := builder.graph.snapshot
	if snapshot.nodes >= maxMutationHistoryNodes {
		return &mutationLimitError{limit: golem.LimitPolicyHistoryNodes, max: maxMutationHistoryNodes}
	}
	snapshot.nodes++
	return nil
}

// Only exact physical paths are deduplicated. Independent declarations and
// expression branches keep all their descendants; no scalar predicate is
// evaluated to prune a branch or approximate authority.
func (builder *mutationHistoryGraphBuilder) compile(ctx context.Context) error {
	registry := builder.graph.snapshot.registry
	seen := map[string]bool{}
	for _, root := range registry.PolicyCaptureRoots() {
		for _, projection := range registry.PolicyCaptureProjections(root) {
			if err := ctx.Err(); err != nil {
				return err
			}
			path := mutationHistoryGraphPath{root: policyir.ModelID(root)}
			model := root
			key := string(root[:])
			for _, hop := range projection.Path {
				if err := ctx.Err(); err != nil {
					return err
				}
				endpoint, err := policyhistory.CaptureEndpoint(registry, model, hop)
				if err != nil {
					return err
				}
				field, relation := endpoint.FieldID(), endpoint.RelationID()
				key += string(field[:]) + string(relation[:])
				path.hops = append(path.hops, endpoint)
				model = endpoint.TargetModelID()
			}
			if compilerir.ModelID(hex.EncodeToString(model[:])) != projection.ModelID {
				return fmt.Errorf("policy history: capture path terminal model disagrees with projection")
			}
			if seen[key] {
				continue
			}
			// Retained path/hop nodes are graph work too, rather than an
			// unmetered side inventory alongside the row/extent budget.
			for index := 0; index <= len(path.hops); index++ {
				if err := builder.charge(); err != nil {
					return err
				}
			}
			seen[key] = true
			path.model = policyir.ModelID(model)
			builder.paths[path.root] = append(builder.paths[path.root], path)
			builder.ending[path.model] = append(builder.ending[path.model], path)
		}
	}
	return nil
}

func (builder *mutationHistoryGraphBuilder) reverse(ctx context.Context, phase mutationHistoryPhase, changed mutationdecode.Row, path mutationHistoryGraphPath) error {
	current := []mutationdecode.Row{changed}
	for index := len(path.hops) - 1; index >= 0; index-- {
		hop := path.hops[index]
		parents := map[string]mutationdecode.Row{}
		for _, child := range current {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := builder.charge(); err != nil {
				return err
			}
			match, empty, err := mutationHistoryGraphMatch(child, hop.Correlation(), false)
			if err != nil {
				return err
			}
			if empty {
				continue
			}
			rows, err := builder.read(ctx, phase, policyir.ModelID(hop.ModelID()), match)
			if err != nil {
				return err
			}
			for _, parent := range rows {
				if err := builder.charge(); err != nil {
					return err
				}
				key, err := policyhistory.RowKey(builder.graph.snapshot.registry, builder.graph.snapshot.provider, parent)
				if err != nil {
					return err
				}
				parents[key] = parent
			}
		}
		keys := make([]string, 0, len(parents))
		for key := range parents {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		current = current[:0]
		for _, key := range keys {
			current = append(current, parents[key])
		}
	}
	for _, row := range current {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := builder.charge(); err != nil {
			return err
		}
		if row.ModelID() != path.root {
			return fmt.Errorf("policy history: reverse capture did not reach its owner model")
		}
		key, err := policyhistory.RowKey(builder.graph.snapshot.registry, builder.graph.snapshot.provider, row)
		if err != nil {
			return err
		}
		if _, found := builder.candidates[key]; found {
			continue
		}
		model, found := builder.graph.snapshot.registry.Model(golem.ModelID(row.ModelID()))
		if !found {
			return fmt.Errorf("policy history: root model is absent")
		}
		identity := make([]mutationdecode.Cell, 0, len(model.PrimaryKey()))
		for _, field := range model.PrimaryKey() {
			cell, found := row.Cell(policyir.FieldID(field))
			if !found || cell.IsNull() {
				return fmt.Errorf("policy history: root primary identity is incomplete")
			}
			identity = append(identity, cell)
		}
		if err := builder.charge(); err != nil {
			return err
		}
		builder.candidates[key] = mutationHistoryGraphCandidate{mutationHistoryRoot{row.ModelID(), key}, identity}
	}
	return nil
}

func (builder *mutationHistoryGraphBuilder) forward(ctx context.Context, phase mutationHistoryPhase, root mutationdecode.Row, path mutationHistoryGraphPath) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := builder.charge(); err != nil {
		return err
	}
	current := []mutationdecode.Row{root}
	for _, hop := range path.hops {
		next := map[string]mutationdecode.Row{}
		for _, parent := range current {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := builder.charge(); err != nil {
				return err
			}
			key, err := policyhistory.RowKey(builder.graph.snapshot.registry, builder.graph.snapshot.provider, parent)
			if err != nil {
				return err
			}
			identity := mutationHistoryGraphRelation{key, policyir.FieldID(hop.FieldID()), policyir.RelationID(hop.RelationID())}
			phaseGraph := builder.graph.phases[phase]
			keys, known := phaseGraph.relations[identity]
			if !known {
				match, empty, err := mutationHistoryGraphMatch(parent, hop.Correlation(), true)
				if err != nil {
					return err
				}
				var rows []mutationdecode.Row
				if !empty {
					rows, err = builder.read(ctx, phase, policyir.ModelID(hop.TargetModelID()), match)
					if err != nil {
						return err
					}
				}
				if hop.Cardinality() == compilerir.RelationOne && len(rows) > 1 {
					return fmt.Errorf("policy history: to-one capture extent returned multiple identities")
				}
				if err := builder.charge(); err != nil {
					return err
				}
				keys = make([]string, 0, len(rows))
				for _, row := range rows {
					if err := builder.charge(); err != nil {
						return err
					}
					childKey, err := policyhistory.RowKey(builder.graph.snapshot.registry, builder.graph.snapshot.provider, row)
					if err != nil {
						return err
					}
					keys = append(keys, childKey)
				}
				phaseGraph.relations[identity] = keys
			}
			for _, child := range keys {
				if err := builder.charge(); err != nil {
					return err
				}
				next[child] = phaseGraph.rows[child]
			}
		}
		keys := make([]string, 0, len(next))
		for key := range next {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		current = current[:0]
		for _, key := range keys {
			current = append(current, next[key])
		}
	}
	return nil
}

// Null correlation components cannot join under the ordinary relationship
// contract. They prove an empty extent without a SQL query, in either direction.
func mutationHistoryGraphMatch(row mutationdecode.Row, pairs []schema.Correlation, forward bool) ([]mutationdecode.Cell, bool, error) {
	match := make([]mutationdecode.Cell, 0, len(pairs))
	for _, pair := range pairs {
		source, target := pair.ChildFieldID(), pair.ParentFieldID()
		if forward {
			source, target = pair.ParentFieldID(), pair.ChildFieldID()
		}
		cell, present := row.Cell(policyir.FieldID(source))
		if !present {
			return nil, false, fmt.Errorf("policy history: correlation scalar is absent from captured row")
		}
		if cell.IsNull() {
			return nil, true, nil
		}
		value, present := cell.PolicyValue()
		if !present {
			return nil, false, fmt.Errorf("policy history: correlation scalar value is absent")
		}
		match = append(match, mutationdecode.Value(policyir.FieldID(target), value))
	}
	return match, false, nil
}

func (builder *mutationHistoryGraphBuilder) read(ctx context.Context, phase mutationHistoryPhase, model policyir.ModelID, equalities []mutationdecode.Cell) ([]mutationdecode.Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := builder.charge(); err != nil {
		return nil, err
	}
	snapshot := builder.graph.snapshot
	match, err := mutationdecode.NewRow(snapshot.registry, model, equalities)
	if err != nil {
		return nil, err
	}
	// Fixed-length condition digests encode exact typed equality values without
	// another retained copy of large strings or byte parameters in cache keys.
	key := string([]byte{byte(phase)}) + string(model[:])
	resolver := policysql.SchemaResolver(snapshot.registry)
	for _, cell := range match.Cells() {
		field, found := resolver.Field(snapshot.provider, model, cell.FieldID())
		if !found {
			return nil, fmt.Errorf("policy history: equality scalar descriptor is absent")
		}
		operator, operand := policyir.OperatorIsNull, policyir.NoOperand()
		if !cell.IsNull() {
			operator = policyir.OperatorEqual
			value, _ := cell.PolicyValue()
			operand, err = policyir.OneOperand(value)
			if err != nil {
				return nil, err
			}
		}
		condition, err := policyir.NewScalar(model, cell.FieldID(), field.Type, operator, policyir.ComparisonSensitive, operand, nil)
		if err != nil {
			return nil, err
		}
		fingerprint, err := policyir.ConditionFingerprint(condition)
		if err != nil {
			return nil, err
		}
		key += string(fingerprint[:])
	}
	phaseGraph := builder.graph.phases[phase]
	keys, cached := builder.queries[key]
	if !cached {
		rows, err := snapshot.read(ctx, model, match.Cells(), phase, maxMutationHistoryNodes)
		if err != nil {
			return nil, err
		}
		keys = make([]string, 0, len(rows))
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := builder.charge(); err != nil {
				return nil, err
			}
			identity, err := policyhistory.RowKey(snapshot.registry, snapshot.provider, row)
			if err != nil {
				return nil, err
			}
			if existing, known := phaseGraph.rows[identity]; known && !reflect.DeepEqual(existing, row) {
				return nil, fmt.Errorf("policy history: one snapshot returned conflicting images for an identity")
			} else if !known {
				phaseGraph.rows[identity] = row
			}
			keys = append(keys, identity)
		}
		builder.queries[key] = keys
	}
	rows := make([]mutationdecode.Row, len(keys))
	for index, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := builder.charge(); err != nil {
			return nil, err
		}
		rows[index] = phaseGraph.rows[key]
	}
	return rows, nil
}

// seal drops reverse-discovery scratch rows which are not in a forward root
// closure. The shared owner then independently validates that every retained
// extent is registered and every registered surviving path is complete.
func (builder *mutationHistoryGraphBuilder) seal(ctx context.Context) (*mutationHistoryGraph, error) {
	snapshot := builder.graph.snapshot
	input := policyhistory.Input{}
	references := map[mutationHistoryPhase]map[string]policyhistory.RowRef{}
	for _, phase := range []mutationHistoryPhase{mutationHistoryBefore, mutationHistoryAfter} {
		current := builder.graph.phases[phase]
		adjacent := map[string][]mutationHistoryGraphRelation{}
		for identity := range current.relations {
			adjacent[identity.parent] = append(adjacent[identity.parent], identity)
		}
		reachable := map[string]bool{}
		var queue []string
		for _, root := range builder.graph.roots {
			present, known := current.roots[root.key]
			if !known {
				return nil, fmt.Errorf("policy history: root phase is unknown during sealing")
			}
			if present {
				reachable[root.key] = true
				queue = append(queue, root.key)
			}
		}
		for offset := 0; offset < len(queue); offset++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := builder.charge(); err != nil {
				return nil, err
			}
			for _, identity := range adjacent[queue[offset]] {
				for _, child := range current.relations[identity] {
					if err := builder.charge(); err != nil {
						return nil, err
					}
					if !reachable[child] {
						reachable[child] = true
						queue = append(queue, child)
					}
				}
			}
		}
		sort.Strings(queue)
		references[phase] = map[string]policyhistory.RowRef{}
		for _, key := range queue {
			row, found := current.rows[key]
			if !found {
				return nil, fmt.Errorf("policy history: reachable row is unknown during sealing")
			}
			input.Rows = append(input.Rows, policyhistory.PhaseRow{Phase: policyhistory.Phase(phase), Row: row})
			references[phase][key] = policyhistory.RowRef(len(input.Rows))
		}
		for identity, children := range current.relations {
			if !reachable[identity.parent] {
				continue
			}
			extent := policyhistory.Extent{Phase: policyhistory.Phase(phase), Parent: references[phase][identity.parent], Field: identity.field, Relation: identity.relation}
			for _, child := range children {
				reference, found := references[phase][child]
				if !found {
					return nil, fmt.Errorf("policy history: extent child is unknown during sealing")
				}
				extent.Children = append(extent.Children, reference)
			}
			input.Extents = append(input.Extents, extent)
		}
	}
	for _, root := range builder.graph.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidate := builder.candidates[root.key]
		identity, err := mutationdecode.NewRow(snapshot.registry, root.model, candidate.identity)
		if err != nil {
			return nil, err
		}
		entry := policyhistory.Root{Identity: identity}
		if reference, present := references[mutationHistoryBefore][root.key]; present {
			entry.Before = policyhistory.Presence{Present: true, Row: reference}
		}
		if reference, present := references[mutationHistoryAfter][root.key]; present {
			entry.After = policyhistory.Presence{Present: true, Row: reference}
		}
		input.Roots = append(input.Roots, entry)
	}
	value, err := policyhistory.New(snapshot.registry, snapshot.provider, input)
	if err != nil {
		var limit *policyhistory.LimitError
		if errors.As(err, &limit) {
			return nil, &mutationLimitError{limit: limit.Limit, max: limit.Max}
		}
		return nil, err
	}
	if err := snapshot.check(); err != nil {
		return nil, err
	}
	*builder = mutationHistoryGraphBuilder{}
	return &mutationHistoryGraph{snapshot: snapshot, value: value}, nil
}
