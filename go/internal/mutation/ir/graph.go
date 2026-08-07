package ir

import (
	"fmt"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type IdentityBehavior uint8

const (
	IdentityProduced IdentityBehavior = iota + 1
	IdentityUnchanged
	IdentityMayChange
	IdentityBatchChangeRefused
)

type NodeInput struct {
	Operation         Operation
	Model             policyir.ModelID
	Relation          policyir.RelationID
	Branch            Branch
	Target            *Target
	Predicate         *policyir.Condition
	RelationPosition  *RelationPosition
	ScalarOperations  []ScalarOperation
	InfluencingFields []policyir.FieldID
	Before            ImageRequirements
	After             ImageRequirements
	Selection         *SelectionRequirement
	RowPostcondition  *policyir.Condition
	FieldConditions   []FieldAuthorization
	Hooks             []HookRequirement
	Fact              FactRequirement
	Identity          IdentityBehavior
	Children          []NodeInput
	// RuntimeSource is non-semantic nested-compiler provenance. Canonical plan
	// encoding deliberately excludes it.
	RuntimeSource      uint32
	RuntimeReplacement bool
	// BeforeParent marks a source-relation dependency whose target row must be
	// produced before a root create can persist its required local FK.
	// It is execution topology, not caller-authored mutation semantics.
	BeforeParent bool
}

type Node struct {
	operation          Operation
	model              policyir.ModelID
	relation           policyir.RelationID
	branch             Branch
	ordinal            uint32
	parent             uint32
	hasParent          bool
	depth              uint16
	target             *Target
	predicate          *policyir.Condition
	relationPosition   *RelationPosition
	relationAnchor     uint32
	hasRelationAnchor  bool
	scalarOperations   []ScalarOperation
	influencingFields  []policyir.FieldID
	before             ImageRequirements
	after              ImageRequirements
	selection          *SelectionRequirement
	rowPostcondition   *policyir.Condition
	fieldConditions    []FieldAuthorization
	hooks              []HookRequirement
	fact               FactRequirement
	identity           IdentityBehavior
	children           []uint32
	runtimeSource      uint32
	runtimeReplacement bool
	beforeParent       bool
}

func (node Node) Operation() Operation            { return node.operation }
func (node Node) ModelID() policyir.ModelID       { return node.model }
func (node Node) RelationID() policyir.RelationID { return node.relation }
func (node Node) Branch() Branch                  { return node.branch }
func (node Node) Ordinal() uint32                 { return node.ordinal }
func (node Node) ParentOrdinal() (uint32, bool)   { return node.parent, node.hasParent }
func (node Node) Depth() uint16                   { return node.depth }
func (node Node) ScalarOperations() []ScalarOperation {
	return append([]ScalarOperation(nil), node.scalarOperations...)
}
func (node Node) InfluencingFields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), node.influencingFields...)
}
func (node Node) BeforeRequirements() ImageRequirements { return node.before.clone() }
func (node Node) AfterRequirements() ImageRequirements  { return node.after.clone() }
func (node Node) Hooks() []HookRequirement              { return append([]HookRequirement(nil), node.hooks...) }
func (node Node) Fact() FactRequirement                 { return node.fact.clone() }
func (node Node) IdentityBehavior() IdentityBehavior    { return node.identity }
func (node Node) ChildOrdinals() []uint32               { return append([]uint32(nil), node.children...) }
func (node Node) RuntimeSourceID() (uint32, bool) {
	return node.runtimeSource, node.runtimeSource != 0
}
func (node Node) IsRuntimeReplacement() bool { return node.runtimeReplacement }
func (node Node) ExecutesBeforeParent() bool { return node.beforeParent }
func (node Node) Target() (Target, bool) {
	if node.target == nil {
		return Target{}, false
	}
	return node.target.clone(), true
}
func (node Node) Predicate() (policyir.Condition, bool) {
	if node.predicate == nil {
		return policyir.Condition{}, false
	}
	return *node.predicate, true
}
func (node Node) RelationPosition() (RelationPosition, bool) {
	if node.relationPosition == nil {
		return RelationPosition{}, false
	}
	return node.relationPosition.clone(), true
}

// RelationAnchorOrdinal is the exact ancestor row owning the relation
// endpoint. It usually equals ParentOrdinal, but conditional cross-model owner
// effects (source connect-or-create) deliberately anchor past branch nodes.
func (node Node) RelationAnchorOrdinal() (uint32, bool) {
	return node.relationAnchor, node.hasRelationAnchor
}
func (node Node) SelectionRequirement() (SelectionRequirement, bool) {
	if node.selection == nil {
		return SelectionRequirement{}, false
	}
	return *node.selection, true
}
func (node Node) RowPostcondition() (policyir.Condition, bool) {
	if node.rowPostcondition == nil {
		return policyir.Condition{}, false
	}
	return *node.rowPostcondition, true
}
func (node Node) FieldAuthorizations() []FieldAuthorization {
	return append([]FieldAuthorization(nil), node.fieldConditions...)
}

func (requirement FactRequirement) clone() FactRequirement {
	requirement.beforeIdentity = requirement.BeforeIdentity()
	requirement.afterIdentity = requirement.AfterIdentity()
	requirement.privateDeleteSnapshot = requirement.PrivateDeleteSnapshot()
	return requirement
}

func (node Node) clone() Node {
	copy := node
	if node.target != nil {
		value := node.target.clone()
		copy.target = &value
	}
	if node.predicate != nil {
		value := *node.predicate
		copy.predicate = &value
	}
	if node.relationPosition != nil {
		value := node.relationPosition.clone()
		copy.relationPosition = &value
	}
	if node.selection != nil {
		value := *node.selection
		copy.selection = &value
	}
	if node.rowPostcondition != nil {
		value := *node.rowPostcondition
		copy.rowPostcondition = &value
	}
	copy.fieldConditions = node.FieldAuthorizations()
	copy.scalarOperations = node.ScalarOperations()
	copy.influencingFields = node.InfluencingFields()
	copy.before = node.before.clone()
	copy.after = node.after.clone()
	copy.hooks = node.Hooks()
	copy.fact = node.fact.clone()
	copy.children = node.ChildOrdinals()
	return copy
}

type Graph struct{ nodes []Node }

func NewGraph(root NodeInput) (Graph, error) {
	builder := graphBuilder{}
	if _, err := builder.append(root, 0, false, 0); err != nil {
		return Graph{}, err
	}
	graph := Graph{nodes: builder.nodes}
	if err := graph.validate(); err != nil {
		return Graph{}, err
	}
	return graph, nil
}

func (graph Graph) Nodes() []Node {
	result := make([]Node, len(graph.nodes))
	for index := range graph.nodes {
		result[index] = graph.nodes[index].clone()
	}
	return result
}
func (graph Graph) Root() (Node, bool) {
	if len(graph.nodes) == 0 {
		return Node{}, false
	}
	return graph.nodes[0].clone(), true
}
func (graph Graph) MaxDepth() uint16 {
	var result uint16
	for _, node := range graph.nodes {
		if node.depth > result {
			result = node.depth
		}
	}
	return result
}
func (graph Graph) clone() Graph { return Graph{nodes: graph.Nodes()} }

type graphBuilder struct{ nodes []Node }

func (builder *graphBuilder) append(input NodeInput, parent uint32, hasParent bool, depth uint16) (uint32, error) {
	if len(builder.nodes) >= int(^uint32(0)) {
		return 0, fmt.Errorf("P4_MUTATION_IR_GRAPH: node count exceeds uint32")
	}
	ordinal := uint32(len(builder.nodes))
	node, err := nodeFromInput(input, ordinal, parent, hasParent, depth)
	if err != nil {
		return 0, fmt.Errorf("node %d: %w", ordinal, err)
	}
	if node.relationPosition != nil && hasParent {
		anchor, found := builder.relationAnchorForNode(node, parent)
		if !found {
			return 0, fmt.Errorf("node %d: P4_MUTATION_IR_GRAPH: relation endpoint has no matching ancestor owner", ordinal)
		}
		node.relationAnchor, node.hasRelationAnchor = anchor, true
	}
	builder.nodes = append(builder.nodes, node)
	if depth == ^uint16(0) && len(input.Children) != 0 {
		return 0, fmt.Errorf("P4_MUTATION_IR_GRAPH: depth exceeds uint16")
	}
	for _, child := range input.Children {
		childOrdinal, err := builder.append(child, ordinal, true, depth+1)
		if err != nil {
			return 0, err
		}
		builder.nodes[ordinal].children = append(builder.nodes[ordinal].children, childOrdinal)
	}
	return ordinal, nil
}

func (builder *graphBuilder) relationAnchorForNode(node Node, parent uint32) (uint32, bool) {
	owner := builder.nodes[parent]
	// A conditional branch is not a persisted row. Its selected child must
	// inherit the conditional owner's relation anchor, even when a recursive
	// relation makes the container model equal to the endpoint owner model.
	// Likewise, a branch-result membership effect has the newly created row as
	// its direct parent, but the relation owner remains the create branch's
	// inherited anchor. Model-only ancestor lookup cannot distinguish those two
	// rows for self-relations.
	if node.branch != MainBranch && containerOperation(owner.operation) || node.relationPosition.kind == PositionBranchResult {
		if owner.hasRelationAnchor {
			return owner.relationAnchor, true
		}
	}
	return builder.relationAnchor(parent, node.relationPosition.parentModel)
}

func containerOperation(operation Operation) bool {
	return operation == Upsert || operation == ConnectOrCreate || operation == CreateMany
}

func (builder *graphBuilder) relationAnchor(parent uint32, model policyir.ModelID) (uint32, bool) {
	for {
		candidate := builder.nodes[parent]
		if candidate.model == model {
			return parent, true
		}
		if !candidate.hasParent {
			return 0, false
		}
		parent = candidate.parent
	}
}

func nodeFromInput(input NodeInput, ordinal, parent uint32, hasParent bool, depth uint16) (Node, error) {
	node := Node{operation: input.Operation, model: input.Model, relation: input.Relation, branch: input.Branch, ordinal: ordinal, parent: parent, hasParent: hasParent, depth: depth, before: input.Before.clone(), after: input.After.clone(), hooks: append([]HookRequirement(nil), input.Hooks...), fact: input.Fact.clone(), identity: input.Identity, runtimeSource: input.RuntimeSource, runtimeReplacement: input.RuntimeReplacement, beforeParent: input.BeforeParent}
	if node.branch == 0 {
		node.branch = MainBranch
	}
	if input.Target != nil {
		target := input.Target.clone()
		node.target = &target
	}
	if input.Predicate != nil {
		predicate := *input.Predicate
		node.predicate = &predicate
	}
	if input.RelationPosition != nil {
		value := input.RelationPosition.clone()
		node.relationPosition = &value
	}
	if input.Selection != nil {
		selection := *input.Selection
		node.selection = &selection
	}
	if input.RowPostcondition != nil {
		condition := *input.RowPostcondition
		node.rowPostcondition = &condition
	}
	if node.before.model == (policyir.ModelID{}) {
		node.before = emptyImage(node.model)
	}
	if node.after.model == (policyir.ModelID{}) {
		node.after = emptyImage(node.model)
	}
	if node.before.model != node.model || node.after.model != node.model {
		return Node{}, fmt.Errorf("P4_MUTATION_IR_NODE: image requirement model mismatch")
	}
	var err error
	if node.scalarOperations, err = normalizeScalarOperations(input.ScalarOperations); err != nil {
		return Node{}, err
	}
	if node.influencingFields, err = normalizeFields(input.InfluencingFields); err != nil {
		return Node{}, err
	}
	if node.fieldConditions, err = normalizeFieldAuthorizations(node.model, input.FieldConditions); err != nil {
		return Node{}, err
	}
	if err := node.validateShape(); err != nil {
		return Node{}, err
	}
	return node, nil
}

func (node Node) validateShape() error {
	if !node.operation.valid() {
		return fmt.Errorf("P4_MUTATION_IR_NODE: invalid operation")
	}
	if err := validateModel(node.model, "NODE"); err != nil {
		return err
	}
	if node.branch < MainBranch || node.branch > BatchItemBranch {
		return fmt.Errorf("P4_MUTATION_IR_NODE: invalid branch")
	}
	if node.target != nil {
		if err := node.target.validate(); err != nil {
			return err
		}
		if node.target.model != node.model {
			return fmt.Errorf("P4_MUTATION_IR_NODE: target model mismatch")
		}
	}
	if node.predicate != nil {
		if err := node.predicate.Validate(); err != nil {
			return fmt.Errorf("P4_MUTATION_IR_NODE: invalid predicate: %w", err)
		}
		if node.predicate.ModelID() != node.model {
			return fmt.Errorf("P4_MUTATION_IR_NODE: predicate model mismatch")
		}
	}
	if node.relationPosition != nil {
		if err := node.relationPosition.validate(); err != nil {
			return err
		}
		if node.relation != (policyir.RelationID{}) && node.relation != node.relationPosition.relation || node.relation == (policyir.RelationID{}) && node.branch == MainBranch {
			return fmt.Errorf("P4_MUTATION_IR_NODE: relation position and node relation disagree")
		}
	}
	if node.selection != nil {
		if err := node.selection.constraint.Validate(); err != nil || node.selection.constraint.ModelID() != node.model {
			return fmt.Errorf("P4_MUTATION_IR_NODE: invalid selection requirement")
		}
		expected := policyir.ActionUpdate
		if node.operation == Delete || node.operation == DeleteMany {
			expected = policyir.ActionDelete
		}
		if node.selection.action != expected {
			return fmt.Errorf("P4_MUTATION_IR_NODE: selection action does not match operation")
		}
	}
	if node.rowPostcondition != nil {
		if err := node.rowPostcondition.Validate(); err != nil || node.rowPostcondition.ModelID() != node.model {
			return fmt.Errorf("P4_MUTATION_IR_NODE: invalid row postcondition")
		}
	}
	if err := validateNodeTargetShape(node); err != nil {
		return err
	}
	if err := validateScalarShape(node.operation, node.scalarOperations); err != nil {
		return err
	}
	if node.identity < IdentityProduced || node.identity > IdentityBatchChangeRefused {
		return fmt.Errorf("P4_MUTATION_IR_NODE: invalid identity behavior")
	}
	if (node.operation == UpdateMany || node.operation == DeleteMany) && node.identity != IdentityBatchChangeRefused {
		return fmt.Errorf("P4_MUTATION_IR_NODE: batch identity changes must be refused")
	}
	if err := validateHooks(node.operation, node.hooks); err != nil {
		return err
	}
	if err := validateFact(node.operation, node.fact); err != nil {
		return err
	}
	return nil
}

func validateNodeTargetShape(node Node) error {
	if node.relationPosition != nil {
		if node.target != nil || node.predicate != nil {
			return fmt.Errorf("P4_MUTATION_IR_NODE: relation-position node cannot carry a root target or predicate")
		}
		kind := node.relationPosition.kind
		switch node.operation {
		case Create, CreateMany:
			if kind != PositionEndpoint {
				return fmt.Errorf("P4_MUTATION_IR_NODE: nested create requires endpoint position")
			}
		case Connect:
			if kind != PositionRelatedTarget && kind != PositionBranchResult {
				return fmt.Errorf("P4_MUTATION_IR_NODE: nested connect requires related target position")
			}
		case ConnectOrCreate:
			if kind != PositionRelatedTarget {
				return fmt.Errorf("P4_MUTATION_IR_NODE: nested branch probe requires related target position")
			}
		case BranchProbe:
			if kind != PositionRelatedTarget && kind != PositionCurrentToOne {
				return fmt.Errorf("P4_MUTATION_IR_NODE: nested branch probe requires an existing target position")
			}
		case Disconnect:
			if kind != PositionCurrentToOne && kind != PositionRelatedTarget && kind != PositionEntireMembership && kind != PositionBranchResult {
				return fmt.Errorf("P4_MUTATION_IR_NODE: nested disconnect position is invalid")
			}
		case SetRelation:
			if kind != PositionSetDifference {
				return fmt.Errorf("P4_MUTATION_IR_NODE: nested set requires set-difference position")
			}
		case Update, Delete, Upsert:
			if kind != PositionCurrentToOne && kind != PositionRelatedTarget {
				return fmt.Errorf("P4_MUTATION_IR_NODE: nested single-row position is invalid")
			}
		case UpdateMany, DeleteMany:
			if kind != PositionRelatedPredicate {
				return fmt.Errorf("P4_MUTATION_IR_NODE: nested batch requires related predicate position")
			}
		default:
			return fmt.Errorf("P4_MUTATION_IR_NODE: operation cannot use a relation position")
		}
		return nil
	}
	switch node.operation {
	case Create, CreateMany:
		if node.target != nil || node.predicate != nil {
			return fmt.Errorf("P4_MUTATION_IR_NODE: create cannot carry a target or predicate")
		}
	case Update, Delete, Connect, ConnectOrCreate:
		if node.target == nil || node.predicate != nil {
			return fmt.Errorf("P4_MUTATION_IR_NODE: single-row operation requires exactly one target")
		}
	case Upsert:
		if node.target == nil || node.predicate != nil {
			return fmt.Errorf("P4_MUTATION_IR_NODE: upsert requires exactly one target")
		}
	case UpdateMany, DeleteMany:
		if node.target != nil || node.predicate == nil {
			return fmt.Errorf("P4_MUTATION_IR_NODE: batch operation requires exactly one predicate")
		}
	case Disconnect, SetRelation:
		if (node.target == nil) == (node.predicate == nil) {
			return fmt.Errorf("P4_MUTATION_IR_NODE: relation membership operation requires exactly one target form")
		}
	}
	return nil
}

func validateScalarShape(operation Operation, values []ScalarOperation) error {
	switch operation {
	case Create:
		for _, value := range values {
			if value.kind != ScalarSet && value.kind != ScalarNull {
				return fmt.Errorf("P4_MUTATION_IR_NODE: create accepts set and explicit-null operations only")
			}
		}
	case Update, UpdateMany:
	case CreateMany, Connect, ConnectOrCreate, Disconnect, SetRelation, Upsert, Delete, DeleteMany, BranchProbe:
		if len(values) != 0 {
			return fmt.Errorf("P4_MUTATION_IR_NODE: operation cannot carry scalar operations")
		}
	}
	return nil
}

func validateHooks(operation Operation, hooks []HookRequirement) error {
	expected := HookOperation(0)
	switch operation {
	case Create:
		expected = HookCreate
	case Update, Connect, Disconnect, SetRelation:
		expected = HookUpdate
	case Delete:
		expected = HookDelete
	case UpdateMany:
		expected = HookUpdateMany
	case DeleteMany:
		expected = HookDeleteMany
	}
	seen := map[HookPhase]bool{}
	last := HookPhase(0)
	for _, hook := range hooks {
		if expected == 0 || hook.operation != expected || hook.phase < BeforeHook || hook.phase > AfterCommitHook || seen[hook.phase] || hook.phase < last {
			return fmt.Errorf("P4_MUTATION_IR_HOOK: hook inventory is invalid or out of phase order")
		}
		seen[hook.phase], last = true, hook.phase
	}
	return nil
}

func validateFact(operation Operation, fact FactRequirement) error {
	if !fact.enabled {
		if fact.action != 0 || fact.eventSchema != ([32]byte{}) || len(fact.beforeIdentity) != 0 || len(fact.afterIdentity) != 0 || fact.deleteSnapshotState != DeleteSnapshotNotApplicable || len(fact.privateDeleteSnapshot) != 0 {
			return fmt.Errorf("P4_MUTATION_IR_FACT: disabled fact carries data")
		}
		return nil
	}
	expected := FactAction(0)
	switch operation {
	case Create:
		expected = FactCreated
	case Update, UpdateMany, Connect, Disconnect, SetRelation:
		expected = FactUpdated
	case Delete, DeleteMany:
		expected = FactDeleted
	}
	if fact.action != expected {
		return fmt.Errorf("P4_MUTATION_IR_FACT: fact action does not match actual row operation")
	}
	if fact.action == FactDeleted {
		if fact.deleteSnapshotState != DeleteSnapshotUnverifiable && fact.deleteSnapshotState != DeleteSnapshotStoredScalars {
			return fmt.Errorf("P7_MUTATION_IR_DELETE_SNAPSHOT: delete fact has no verification decision")
		}
		if fact.deleteSnapshotState == DeleteSnapshotUnverifiable && len(fact.privateDeleteSnapshot) != 0 {
			return fmt.Errorf("P7_MUTATION_IR_DELETE_SNAPSHOT: unverifiable delete fact carries snapshot fields")
		}
	} else if fact.deleteSnapshotState != DeleteSnapshotNotApplicable || len(fact.privateDeleteSnapshot) != 0 {
		return fmt.Errorf("P7_MUTATION_IR_DELETE_SNAPSHOT: non-delete fact carries snapshot semantics")
	}
	return nil
}

func (graph Graph) validate() error {
	if len(graph.nodes) == 0 {
		return fmt.Errorf("P4_MUTATION_IR_GRAPH: root is absent")
	}
	for index, node := range graph.nodes {
		if node.ordinal != uint32(index) {
			return fmt.Errorf("P4_MUTATION_IR_GRAPH: ordinals are not contiguous depth-first values")
		}
		if index == 0 {
			preParentDependency := node.beforeParent && node.operation == Create && node.relation != (policyir.RelationID{}) && node.relationPosition != nil
			ordinaryRoot := node.relation == (policyir.RelationID{}) && node.relationPosition == nil
			if node.hasParent || node.depth != 0 || (!ordinaryRoot && !preParentDependency) || node.branch != MainBranch || !node.operation.rootAllowed() {
				return fmt.Errorf("P4_MUTATION_IR_GRAPH: invalid root")
			}
		} else {
			if !node.hasParent || int(node.parent) >= index || graph.nodes[node.parent].depth+1 != node.depth {
				return fmt.Errorf("P4_MUTATION_IR_GRAPH: invalid parent/depth")
			}
			branchChild := node.branch != MainBranch
			if branchChild == (node.relation != (policyir.RelationID{})) {
				return fmt.Errorf("P4_MUTATION_IR_GRAPH: child needs exactly one relation or branch owner")
			}
			if node.relation != (policyir.RelationID{}) {
				if node.relationPosition == nil || !node.hasRelationAnchor || node.relationAnchor >= node.ordinal || node.relationPosition.parentModel != graph.nodes[node.relationAnchor].model || node.relationPosition.relation != node.relation {
					return fmt.Errorf("P4_MUTATION_IR_GRAPH: relation child lacks its exact parent endpoint")
				}
			} else if node.relationPosition != nil {
				owner := graph.nodes[node.parent].relationPosition
				if owner == nil || node.relationPosition.parentModel != owner.parentModel || node.relationPosition.field != owner.field || node.relationPosition.relation != owner.relation || node.relationPosition.targetModel != owner.targetModel {
					return fmt.Errorf("P4_MUTATION_IR_GRAPH: branch relation position does not inherit its owner endpoint")
				}
			}
		}
		for _, child := range node.children {
			if int(child) <= index || int(child) >= len(graph.nodes) || graph.nodes[child].parent != node.ordinal {
				return fmt.Errorf("P4_MUTATION_IR_GRAPH: invalid child ordinal")
			}
		}
		if err := validateBranchChildren(node, graph.nodes); err != nil {
			return err
		}
	}
	return nil
}

func validateBranchChildren(node Node, nodes []Node) error {
	var create, update, connect, batch int
	for _, ordinal := range node.children {
		switch nodes[ordinal].branch {
		case UpsertCreateBranch, ConnectOrCreateCreateBranch:
			create++
		case UpsertUpdateBranch:
			update++
		case ConnectOrCreateConnectBranch:
			connect++
		case BatchItemBranch:
			batch++
		}
	}
	switch node.operation {
	case Upsert:
		if len(node.children) != 2 || create != 1 || update != 1 || connect != 0 || batch != 0 {
			return fmt.Errorf("P4_MUTATION_IR_GRAPH: upsert requires one create and one update branch")
		}
		for _, ordinal := range node.children {
			child := nodes[ordinal]
			if child.model != node.model || child.branch == UpsertCreateBranch && child.operation != Create || child.branch == UpsertUpdateBranch && child.operation != Update {
				return fmt.Errorf("P4_MUTATION_IR_GRAPH: upsert branch model or operation is invalid")
			}
		}
	case ConnectOrCreate:
		if len(node.children) != 2 || create != 1 || connect != 1 || update != 0 || batch != 0 {
			return fmt.Errorf("P4_MUTATION_IR_GRAPH: connect-or-create requires one create and one connect branch")
		}
		for _, ordinal := range node.children {
			child := nodes[ordinal]
			if child.model != node.model || child.branch == ConnectOrCreateCreateBranch && child.operation != Create || child.branch == ConnectOrCreateConnectBranch && child.operation != Connect && child.operation != BranchProbe {
				return fmt.Errorf("P4_MUTATION_IR_GRAPH: connect-or-create branch model or operation is invalid")
			}
		}
	case CreateMany:
		if len(node.children) == 0 || batch != len(node.children) || create != 0 || update != 0 || connect != 0 {
			return fmt.Errorf("P4_MUTATION_IR_GRAPH: create-many requires one batch-item create branch per row")
		}
		for _, ordinal := range node.children {
			child := nodes[ordinal]
			if child.model != node.model || child.operation != Create {
				return fmt.Errorf("P4_MUTATION_IR_GRAPH: create-many item model or operation is invalid")
			}
		}
	default:
		if create != 0 || update != 0 || connect != 0 || batch != 0 {
			return fmt.Errorf("P4_MUTATION_IR_GRAPH: branch child belongs to an incompatible operation")
		}
	}
	return nil
}
