package history

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

// CertifiedChildren returns an explicit complete extent or proves a to-one
// result from captured same-phase evidence and a declared unique target key.
// A partial target inventory never proves absence (or to-many completeness).
// Derived results do not add extents to the graph or cache actor decisions.
func (graph Graph) CertifiedChildren(registry *schema.Registry, phase Phase, parentKey string, field policyir.FieldID, relation policyir.RelationID) ([]mutationdecode.Row, bool, error) {
	return graph.certifiedChildren(registry, phase, parentKey, field, relation, &relationProofBudget{})
}

// One evaluation shares this budget across all derived lookups, so repeatedly
// scanning a factorized graph cannot evade the finite materialization bound.
type relationProofBudget struct{ nodes, bytes int }

func (budget *relationProofBudget) charge(nodes int) error {
	if nodes > MaxNodes-budget.nodes {
		return &LimitError{Limit: golem.LimitPolicyHistoryNodes, Max: MaxNodes}
	}
	budget.nodes += nodes
	return nil
}
func (budget *relationProofBudget) scalar(value policyir.Value) error {
	size, err := value.ScalarCanonicalSize()
	if err != nil {
		return err
	}
	if size > MaxScalarBytes-budget.bytes {
		return &LimitError{Limit: golem.LimitPolicyHistoryBytes, Max: MaxScalarBytes}
	}
	budget.bytes += size
	return nil
}

func (graph Graph) certifiedChildren(registry *schema.Registry, phase Phase, parentKey string, field policyir.FieldID, relation policyir.RelationID, budget *relationProofBudget) ([]mutationdecode.Row, bool, error) {
	if graph.data == nil || registry == nil || graph.Generation() != registry.GenerationDigest() || graph.ProjectionFingerprint() != registry.PolicyCaptureFingerprint() {
		return nil, false, fmt.Errorf("policy history: exact producing schema and capture contract are required")
	}
	if phase != Before && phase != After {
		return nil, false, fmt.Errorf("policy history: explicit before/after phase is required")
	}
	if err := budget.charge(1); err != nil {
		return nil, false, err
	}
	parent, present := graph.FindRow(phase, parentKey)
	if !present {
		return nil, false, nil
	}
	endpoint, found := registry.RelationEndpoint(golem.ModelID(parent.ModelID()), golem.FieldID(field), golem.RelationID(relation))
	if !found {
		return nil, false, fmt.Errorf("policy history: relation endpoint is absent from the producing schema")
	}
	parentRef := graph.data.rows[rowIndex{phase, parentKey}]
	if index, complete := graph.data.extents[extentIndex{phase, parentRef, field, relation}]; complete {
		// Children returns an owned row container. Charge the entire copy
		// before allocation, including when many cuts reuse one wide extent.
		if err := budget.charge(len(graph.data.input.Extents[index].Children)); err != nil {
			return nil, false, err
		}
		rows, _ := graph.Children(phase, parentKey, field, relation)
		return rows, true, nil
	}
	if endpoint.Cardinality() != compilerir.RelationOne {
		return nil, false, nil
	}
	pairs := endpoint.Correlation()
	if len(pairs) == 0 {
		return nil, false, fmt.Errorf("policy history: relation correlation is absent")
	}
	if err := budget.charge(len(pairs)); err != nil {
		return nil, false, err
	}
	target, found := registry.Model(endpoint.TargetModelID())
	if !found {
		return nil, false, fmt.Errorf("policy history: relation target is absent")
	}
	remote := make(map[golem.FieldID]bool, len(pairs))
	for _, pair := range pairs {
		if remote[pair.ChildFieldID()] {
			return nil, false, fmt.Errorf("policy history: repeated target correlation field")
		}
		remote[pair.ChildFieldID()] = true
	}
	unique, primary := false, false
	for _, identity := range target.Identities() {
		fields := identity.Fields()
		if err := budget.charge(1 + len(fields)); err != nil {
			return nil, false, err
		}
		if identity.Kind() != compilerir.KeyPrimary && identity.Kind() != compilerir.KeyUnique || len(fields) != len(remote) {
			continue
		}
		matches := true
		for _, field := range fields {
			if !remote[field] {
				matches = false
				break
			}
		}
		if matches {
			unique = true
			primary = primary || identity.Kind() == compilerir.KeyPrimary
		}
	}
	if !unique {
		return nil, false, nil
	}
	values := make([]policyir.Value, len(pairs))
	null, missing := false, false
	for index, pair := range pairs {
		cell, found := parent.Cell(policyir.FieldID(pair.ParentFieldID()))
		if !found {
			missing = true
			continue
		}
		if cell.IsNull() {
			null = true
			continue
		}
		value, valid := cell.PolicyValue()
		if !valid {
			return nil, false, fmt.Errorf("policy history: parent correlation has no typed value")
		}
		if err := budget.scalar(value); err != nil {
			return nil, false, err
		}
		values[index] = value
	}
	if missing {
		return nil, false, nil
	}
	if null {
		return nil, true, nil
	}
	providers, err := policyir.NewProviderSet(graph.Provider())
	if err != nil {
		return nil, false, err
	}
	resolver := policysql.SchemaResolver(registry)
	conditions := make([]policyir.Condition, 0, len(pairs))
	identityCells := make([]mutationdecode.Cell, 0, len(pairs))
	for index, pair := range pairs {
		fieldID := policyir.FieldID(pair.ChildFieldID())
		descriptor, found := resolver.Field(graph.Provider(), policyir.ModelID(target.ID()), fieldID)
		if !found {
			return nil, false, fmt.Errorf("policy history: target correlation descriptor is absent")
		}
		operand, err := policyir.OneOperand(values[index])
		if err != nil {
			return nil, false, err
		}
		requirements, err := operator.ValidateShape(policyir.OperatorEqual, operator.Shape{Node: policyir.ConditionScalar, FieldType: descriptor.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: providers})
		if err != nil {
			return nil, false, err
		}
		condition, err := policyir.NewScalar(policyir.ModelID(target.ID()), fieldID, descriptor.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if err != nil {
			return nil, false, err
		}
		conditions = append(conditions, condition)
		identityCells = append(identityCells, mutationdecode.Value(fieldID, values[index]))
	}
	condition := conditions[0]
	if len(conditions) > 1 {
		condition, err = policyir.NewLogical(policyir.ModelID(target.ID()), policyir.LogicalAnd, conditions)
		if err != nil {
			return nil, false, err
		}
	}
	var matched *mutationdecode.Row
	// Use the private immutable inventory directly; Rows() would clone every
	// row container once per lookup. Scan all candidates to reject conflicting
	// captured values for the supposedly unique target key.
	for _, entry := range graph.data.input.Rows {
		if err := budget.charge(1); err != nil {
			return nil, false, err
		}
		if entry.Phase != phase || entry.Row.ModelID() != policyir.ModelID(target.ID()) {
			continue
		}
		match, err := matchCertifiedTarget(entry.Row, pairs, condition, providers, budget)
		if err != nil {
			return nil, false, err
		}
		if !match {
			continue
		}
		if matched != nil {
			return nil, false, fmt.Errorf("policy history: captured targets contradict declared uniqueness")
		}
		copy := entry.Row
		matched = &copy
	}
	if matched != nil {
		return []mutationdecode.Row{*matched}, true, nil
	}
	if !primary {
		return nil, false, nil
	}
	// Only the exact primary identity survives in an absent root. A named
	// unique value can move to another primary row, so it cannot use this proof.
	identity, err := mutationdecode.NewRow(registry, policyir.ModelID(target.ID()), identityCells)
	if err != nil {
		return nil, false, err
	}
	key, err := RowKey(registry, graph.Provider(), identity)
	if err != nil {
		return nil, false, err
	}
	rootIndex, known := graph.data.roots[key]
	if !known {
		return nil, false, nil
	}
	root := graph.data.input.Roots[rootIndex]
	match, err := matchCertifiedTarget(root.Identity, pairs, condition, providers, budget)
	if err != nil {
		return nil, false, err
	}
	if !match {
		return nil, false, nil
	}
	presence := root.Before
	if phase == After {
		presence = root.After
	}
	if !presence.Present {
		return nil, true, nil
	}
	return nil, false, nil
}

func matchCertifiedTarget(row mutationdecode.Row, pairs []schema.Correlation, condition policyir.Condition, providers policyir.ProviderSet, budget *relationProofBudget) (bool, error) {
	if err := budget.charge(len(pairs)); err != nil {
		return false, err
	}
	fields := make([]evaluate.Field, 0, len(pairs))
	for _, pair := range pairs {
		cell, found := row.Cell(policyir.FieldID(pair.ChildFieldID()))
		if !found || cell.IsNull() {
			return false, nil
		}
		value, valid := cell.PolicyValue()
		if !valid {
			return false, fmt.Errorf("policy history: target correlation has no typed value")
		}
		if err := budget.scalar(value); err != nil {
			return false, err
		}
		field, err := evaluate.ValueField(cell.FieldID(), value)
		if err != nil {
			return false, err
		}
		fields = append(fields, field)
	}
	record, err := evaluate.NewRecord(row.ModelID(), fields...)
	if err != nil {
		return false, err
	}
	return evaluate.Condition(condition, record, providers)
}
