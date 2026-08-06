package nested

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// ExecutionBoundary is the provider/runtime seam for the nested engine. One
// Begin call covers the complete graph; the engine never opens a transaction
// per node or per dynamically expanded row.
type ExecutionBoundary interface {
	BeginNested(context.Context) (ExecutionTransaction, error)
}

// ExecutionTransaction is implemented by the runtime adapter. ExpandNested
// resolves target sets and truthful branches using provider SQL. ApplyNested
// delegates row writes to the scalar, batch, and upsert kernels. VerifyNested
// is called in reverse depth-first order after every write has succeeded.
type ExecutionTransaction interface {
	ExpandNested(context.Context, ExpansionRequest) (RuntimeExpansion, error)
	ApplyNested(context.Context, ApplyRequest) (ApplyResult, error)
	VerifyNested(context.Context, AppliedNode) error
	CommitNested(context.Context) error
	RollbackNested(context.Context) error
}

// DynamicTransformTransaction is the exact selected-child Before-hook seam.
//
// Direct scalar branches are offered at TransformPreExpand only after their
// COC/upsert container selected them and before selector/filter expansion SQL.
// Membership owner updates are offered at TransformPostExpand after an exact
// owner RuntimeWork is locked but before Apply SQL. A replacement is a local,
// deterministic graph; Entry identifies its selected root while any dummy
// anchor node is not executed. Local ordinals need not match the discarded
// global subtree. The engine supplies the already-applied external parent and
// relation anchor to Entry. Membership replacement must retain Work's exact
// identity; the runtime validator rejects a changed target before returning it.
type DynamicTransformTransaction interface {
	TransformNested(context.Context, TransformRequest) (SubtreeReplacement, bool, error)
}

// BeforeParentTransaction preserves declared graph observability when a
// source dependency must execute physically before its parent row. The
// transaction may checkpoint and restore graph-ordered side effects such as
// outbox ordinals around the physical dependency segment.
type BeforeParentTransaction interface {
	BeginBeforeParent(context.Context, mutationir.Node) error
	CompleteBeforeParent(context.Context, mutationir.Node) error
}

type FinalGraphTransaction interface {
	FinalizeNested(context.Context, []AppliedNode) error
}

type TransformStage uint8

const (
	TransformPreExpand TransformStage = iota + 1
	TransformPostExpand
)

type TransformRequest struct {
	stage  TransformStage
	node   mutationir.Node
	parent *AppliedNode
	anchor *AppliedNode
	work   *RuntimeWork
}

func (request TransformRequest) Stage() TransformStage { return request.stage }
func (request TransformRequest) Node() mutationir.Node { return request.node }
func (request TransformRequest) Parent() (AppliedNode, bool) {
	if request.parent == nil {
		return AppliedNode{}, false
	}
	return request.parent.clone(), true
}
func (request TransformRequest) RelationAnchor() (AppliedNode, bool) {
	if request.anchor == nil {
		return AppliedNode{}, false
	}
	return request.anchor.clone(), true
}
func (request TransformRequest) Work() (RuntimeWork, bool) {
	if request.work == nil {
		return RuntimeWork{}, false
	}
	return request.work.clone(), true
}

type SubtreeReplacement struct {
	graph mutationir.Graph
	entry uint32
}

func NewSubtreeReplacement(graph mutationir.Graph, entry uint32) (SubtreeReplacement, error) {
	nodes := graph.Nodes()
	if len(nodes) == 0 || int(entry) >= len(nodes) {
		return SubtreeReplacement{}, fmt.Errorf("P4_NESTED_TRANSFORM: replacement entry is absent")
	}
	return SubtreeReplacement{graph: graph, entry: entry}, nil
}
func (replacement SubtreeReplacement) Graph() mutationir.Graph { return replacement.graph }
func (replacement SubtreeReplacement) EntryOrdinal() uint32    { return replacement.entry }

type RuntimeWork struct {
	model      policyir.ModelID
	identity   *mutationdecode.Identity
	orderKey   []byte
	resolved   *mutationdecode.Row
	membership MembershipEffect
	batch      []mutationdecode.Row
}

func NewCreateWork(model policyir.ModelID, orderKey []byte) (RuntimeWork, error) {
	return newRuntimeWork(model, nil, orderKey)
}

func NewExistingWork(model policyir.ModelID, identity mutationdecode.Identity, orderKey []byte) (RuntimeWork, error) {
	copy, err := cloneIdentity(identity)
	if err != nil {
		return RuntimeWork{}, err
	}
	return newRuntimeWork(model, &copy, orderKey)
}

// NewResolvedRelationWork retains the locked related row required by a later
// membership write. workModel is the FK owner and may differ from row.ModelID
// for a source endpoint. effect is non-zero only for set-difference work.
func NewResolvedRelationWork(workModel policyir.ModelID, identity mutationdecode.Identity, orderKey []byte, row mutationdecode.Row, effect MembershipEffect) (RuntimeWork, error) {
	if row.ModelID() == (policyir.ModelID{}) || effect > MembershipDisconnect {
		return RuntimeWork{}, fmt.Errorf("P4_NESTED_EXEC_WORK: resolved relation row or membership effect is invalid")
	}
	work, err := NewExistingWork(workModel, identity, orderKey)
	if err != nil {
		return RuntimeWork{}, err
	}
	value := row
	work.resolved, work.membership = &value, effect
	return work, nil
}

// NewBatchWork represents one exact, already-locked dynamic batch. Apply is
// invoked once so the batch kernel can authorize all chunks before any write;
// touched-row accounting still uses the exact row cardinality.
func NewBatchWork(model policyir.ModelID, rows []mutationdecode.Row, orderKey []byte) (RuntimeWork, error) {
	if model == (policyir.ModelID{}) || len(orderKey) == 0 {
		return RuntimeWork{}, fmt.Errorf("P4_NESTED_EXEC_WORK: batch model and canonical order key are required")
	}
	result := RuntimeWork{model: model, orderKey: append([]byte(nil), orderKey...), batch: make([]mutationdecode.Row, len(rows))}
	copy(result.batch, rows)
	for index, row := range result.batch {
		if row.ModelID() != model {
			return RuntimeWork{}, fmt.Errorf("P4_NESTED_EXEC_WORK: batch row %d belongs to another model", index)
		}
	}
	return result, nil
}

func newRuntimeWork(model policyir.ModelID, identity *mutationdecode.Identity, orderKey []byte) (RuntimeWork, error) {
	if model == (policyir.ModelID{}) || len(orderKey) == 0 {
		return RuntimeWork{}, fmt.Errorf("P4_NESTED_EXEC_WORK: model and canonical order key are required")
	}
	return RuntimeWork{model: model, identity: identity, orderKey: append([]byte(nil), orderKey...)}, nil
}

func (work RuntimeWork) ModelID() policyir.ModelID { return work.model }
func (work RuntimeWork) Identity() (mutationdecode.Identity, bool) {
	if work.identity == nil {
		return mutationdecode.Identity{}, false
	}
	copy, _ := cloneIdentity(*work.identity)
	return copy, true
}
func (work RuntimeWork) OrderKey() []byte { return append([]byte(nil), work.orderKey...) }
func (work RuntimeWork) ResolvedRelationRow() (mutationdecode.Row, bool) {
	if work.resolved == nil {
		return mutationdecode.Row{}, false
	}
	return *work.resolved, true
}
func (work RuntimeWork) MembershipEffect() MembershipEffect { return work.membership }
func (work RuntimeWork) BatchRows() ([]mutationdecode.Row, bool) {
	if work.batch == nil {
		return nil, false
	}
	rows := make([]mutationdecode.Row, len(work.batch))
	copy(rows, work.batch)
	return rows, true
}
func (work RuntimeWork) TouchedRows() uint32 {
	if work.batch != nil {
		return uint32(len(work.batch))
	}
	return 1
}
func (work RuntimeWork) clone() RuntimeWork {
	cloned := RuntimeWork{model: work.model, orderKey: work.OrderKey(), membership: work.membership}
	if work.batch != nil {
		cloned.batch = make([]mutationdecode.Row, len(work.batch))
		copy(cloned.batch, work.batch)
	}
	if work.identity != nil {
		value, _ := cloneIdentity(*work.identity)
		cloned.identity = &value
	}
	if work.resolved != nil {
		value := *work.resolved
		cloned.resolved = &value
	}
	return cloned
}

func cloneIdentity(identity mutationdecode.Identity) (mutationdecode.Identity, error) {
	return mutationdecode.NewIdentity(identity.KeyID(), identity.Components())
}

type RuntimeExpansion struct {
	works  []RuntimeWork
	branch mutationir.Branch
}

func NewRuntimeExpansion(works []RuntimeWork, branch mutationir.Branch) (RuntimeExpansion, error) {
	result := RuntimeExpansion{works: make([]RuntimeWork, len(works)), branch: branch}
	for index := range works {
		if works[index].model == (policyir.ModelID{}) || len(works[index].orderKey) == 0 {
			return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXEC_EXPANSION: work %d is invalid", index)
		}
		result.works[index] = works[index].clone()
	}
	return result, nil
}

func (expansion RuntimeExpansion) Works() []RuntimeWork {
	result := make([]RuntimeWork, len(expansion.works))
	for index := range expansion.works {
		result[index] = expansion.works[index].clone()
	}
	return result
}
func (expansion RuntimeExpansion) Branch() mutationir.Branch { return expansion.branch }

type ApplyResult struct {
	before *mutationdecode.Row
	after  *mutationdecode.Row
	hook   *golem.RuntimeMutationHookResult
}

func WithRuntimeHookResult(result ApplyResult, hook golem.RuntimeMutationHookResult) ApplyResult {
	value := hook
	result.hook = &value
	return result
}

func NewApplyResult(before, after *mutationdecode.Row) ApplyResult {
	result := ApplyResult{}
	if before != nil {
		value := *before
		result.before = &value
	}
	if after != nil {
		value := *after
		result.after = &value
	}
	return result
}

func (result ApplyResult) Before() (mutationdecode.Row, bool) {
	if result.before == nil {
		return mutationdecode.Row{}, false
	}
	return *result.before, true
}
func (result ApplyResult) After() (mutationdecode.Row, bool) {
	if result.after == nil {
		return mutationdecode.Row{}, false
	}
	return *result.after, true
}
func (result ApplyResult) RuntimeHookResult() (golem.RuntimeMutationHookResult, bool) {
	if result.hook == nil {
		return golem.RuntimeMutationHookResult{}, false
	}
	return *result.hook, true
}

type ExpansionRequest struct {
	node   mutationir.Node
	parent *AppliedNode
	anchor *AppliedNode
}

func (request ExpansionRequest) Node() mutationir.Node { return request.node }
func (request ExpansionRequest) Parent() (AppliedNode, bool) {
	if request.parent == nil {
		return AppliedNode{}, false
	}
	return request.parent.clone(), true
}
func (request ExpansionRequest) RelationAnchor() (AppliedNode, bool) {
	if request.anchor == nil {
		return AppliedNode{}, false
	}
	return request.anchor.clone(), true
}

type ApplyRequest struct {
	node         mutationir.Node
	work         RuntimeWork
	parent       *AppliedNode
	anchor       *AppliedNode
	dependencies []AppliedNode
}

func (request ApplyRequest) Node() mutationir.Node { return request.node }
func (request ApplyRequest) Work() RuntimeWork     { return request.work.clone() }
func (request ApplyRequest) Parent() (AppliedNode, bool) {
	if request.parent == nil {
		return AppliedNode{}, false
	}
	return request.parent.clone(), true
}
func (request ApplyRequest) RelationAnchor() (AppliedNode, bool) {
	if request.anchor == nil {
		return AppliedNode{}, false
	}
	return request.anchor.clone(), true
}
func (request ApplyRequest) Dependencies() []AppliedNode { return cloneApplied(request.dependencies) }

type AppliedNode struct {
	node   mutationir.Node
	work   RuntimeWork
	result ApplyResult
	order  []uint32
}

func (applied AppliedNode) Node() mutationir.Node { return applied.node }
func (applied AppliedNode) Work() RuntimeWork     { return applied.work.clone() }
func (applied AppliedNode) Result() ApplyResult {
	result := NewApplyResult(applied.result.before, applied.result.after)
	if hook, ok := applied.result.RuntimeHookResult(); ok {
		result = WithRuntimeHookResult(result, hook)
	}
	return result
}
func (applied AppliedNode) clone() AppliedNode {
	return AppliedNode{node: applied.node, work: applied.work.clone(), result: applied.Result(), order: append([]uint32(nil), applied.order...)}
}

type ExecutionReceipt struct {
	touched uint32
	applied []AppliedNode
}

func (receipt ExecutionReceipt) TouchedRows() uint32 { return receipt.touched }
func (receipt ExecutionReceipt) Applied() []AppliedNode {
	result := make([]AppliedNode, len(receipt.applied))
	for index := range receipt.applied {
		result[index] = receipt.applied[index].clone()
	}
	return result
}

// Execute performs deterministic depth-first expansion and writes, then
// reverse-order verification, on exactly one transaction. Any expansion,
// write, verification, limit, context, or commit error attempts rollback.
func Execute(ctx context.Context, graph mutationir.Graph, maxTouchedRows uint32, boundary ExecutionBoundary) (receipt ExecutionReceipt, err error) {
	if ctx == nil || boundary == nil || maxTouchedRows == 0 {
		return ExecutionReceipt{}, fmt.Errorf("P4_NESTED_EXEC_INPUT: context, boundary, and positive touched-row limit are required")
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return ExecutionReceipt{}, contextErr
	}
	nodes := graph.Nodes()
	if len(nodes) == 0 || nodes[0].Ordinal() != 0 {
		return ExecutionReceipt{}, fmt.Errorf("P4_NESTED_EXEC_INPUT: frozen graph is absent or invalid")
	}
	transaction, err := boundary.BeginNested(ctx)
	if err != nil {
		return ExecutionReceipt{}, err
	}
	if transaction == nil {
		return ExecutionReceipt{}, fmt.Errorf("P4_NESTED_EXEC_INPUT: transaction boundary returned nil")
	}
	committed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = transaction.RollbackNested(ctx)
			panic(recovered)
		}
		if !committed {
			if rollbackErr := transaction.RollbackNested(ctx); rollbackErr != nil {
				if err == nil {
					err = rollbackErr
				} else {
					err = errors.Join(err, rollbackErr)
				}
			}
		}
	}()

	engine := executionEngine{ctx: ctx, transaction: transaction, nodes: nodes, maxTouched: maxTouchedRows}
	if err = engine.executeNode(nodes[0], nil); err != nil {
		return ExecutionReceipt{}, err
	}
	// Always order by the global execution path: a dynamic replacement may
	// introduce a pre-parent dependency even when the frozen original graph had
	// none. Ordinary graphs retain their depth-first order under this stable
	// sort; dependency/replacement paths gain their canonical global position.
	sort.SliceStable(engine.applied, func(i, j int) bool {
		return compareExecutionOrder(engine.applied[i].order, engine.applied[j].order) < 0
	})
	if finalizer, ok := transaction.(FinalGraphTransaction); ok {
		if err = finalizer.FinalizeNested(ctx, cloneApplied(engine.applied)); err != nil {
			return ExecutionReceipt{}, err
		}
	}
	for index := len(engine.applied) - 1; index >= 0; index-- {
		if contextErr := ctx.Err(); contextErr != nil {
			return ExecutionReceipt{}, contextErr
		}
		if err = transaction.VerifyNested(ctx, engine.applied[index].clone()); err != nil {
			return ExecutionReceipt{}, err
		}
	}
	if err = transaction.CommitNested(ctx); err != nil {
		return ExecutionReceipt{}, err
	}
	committed = true
	return ExecutionReceipt{touched: engine.touched, applied: cloneApplied(engine.applied)}, nil
}

type executionEngine struct {
	ctx               context.Context
	transaction       ExecutionTransaction
	nodes             []mutationir.Node
	maxTouched        uint32
	touched           uint32
	applied           []AppliedNode
	dependencyResults map[uint32]AppliedNode
	preexpanded       map[uint32]RuntimeExpansion
	orderPrefix       []uint32
	orderEntry        uint32
	hasOrderEntry     bool
	dependencyDepth   uint32
}

func (engine *executionEngine) executeNode(node mutationir.Node, inherited map[uint32]AppliedNode) error {
	return engine.executeNodeWithContext(node, inherited, nil, nil, true)
}

func (engine *executionEngine) executeNodeWithContext(node mutationir.Node, inherited map[uint32]AppliedNode, externalParent, externalAnchor *AppliedNode, allowTransform bool) error {
	if contextErr := engine.ctx.Err(); contextErr != nil {
		return contextErr
	}
	path := clonePath(inherited)
	parent, anchor, err := engine.contextFor(node, path)
	if err != nil {
		return err
	}
	if externalParent != nil {
		value := externalParent.clone()
		parent = &value
	}
	if externalAnchor != nil {
		value := externalAnchor.clone()
		anchor = &value
	}
	if allowTransform && !container(node.Operation()) && !postExpansionTransform(node) {
		if transformer, ok := engine.transaction.(DynamicTransformTransaction); ok {
			replacement, replaced, transformErr := transformer.TransformNested(engine.ctx, TransformRequest{stage: TransformPreExpand, node: node, parent: parent, anchor: anchor})
			if transformErr != nil {
				return transformErr
			}
			if replaced {
				return engine.executeReplacement(replacement, parent, anchor, node.Ordinal())
			}
		}
	}
	dependencies, deferredApplied, err := engine.executeBeforeParentChildren(node, path)
	if err != nil {
		return err
	}
	expansion, prepared := engine.preexpanded[node.Ordinal()]
	if prepared {
		delete(engine.preexpanded, node.Ordinal())
	} else {
		expansion, err = engine.transaction.ExpandNested(engine.ctx, ExpansionRequest{node: node, parent: parent, anchor: anchor})
		if err != nil {
			return err
		}
	}
	works := expansion.Works()
	if err := validateExpansion(node, works, expansion.branch); err != nil {
		return err
	}
	if container(node.Operation()) {
		return engine.executeChildren(node, path, expansion.branch)
	}
	sort.Slice(works, func(i, j int) bool { return bytes.Compare(works[i].orderKey, works[j].orderKey) < 0 })
	for index, work := range works {
		if index > 0 && bytes.Equal(works[index-1].orderKey, work.orderKey) {
			return fmt.Errorf("P4_NESTED_EXEC_SET: node %d expansion contains duplicate canonical identities", node.Ordinal())
		}
		if work.model != node.ModelID() {
			return fmt.Errorf("P4_NESTED_EXEC_SET: node %d expansion belongs to another model", node.Ordinal())
		}
		if allowTransform && postExpansionTransform(node) {
			if transformer, ok := engine.transaction.(DynamicTransformTransaction); ok {
				workCopy := work.clone()
				replacement, replaced, transformErr := transformer.TransformNested(engine.ctx, TransformRequest{stage: TransformPostExpand, node: node, parent: parent, anchor: anchor, work: &workCopy})
				if transformErr != nil {
					return transformErr
				}
				if replaced {
					if err := engine.executeReplacement(replacement, parent, anchor, node.Ordinal()); err != nil {
						return err
					}
					continue
				}
			}
		}
		if writesRow(node.Operation()) && uint64(engine.touched)+uint64(work.TouchedRows()) > uint64(engine.maxTouched) {
			return fmt.Errorf("P4_NESTED_EXEC_LIMIT: touched rows exceed %d", engine.maxTouched)
		}
		if writesRow(node.Operation()) {
			engine.touched += work.TouchedRows()
		}
		result, applyErr := engine.transaction.ApplyNested(engine.ctx, ApplyRequest{node: node, work: work, parent: parent, anchor: anchor, dependencies: dependencies})
		if applyErr != nil {
			return applyErr
		}
		if resultErr := validateApplyResult(node, result); resultErr != nil {
			return resultErr
		}
		if len(dependencies) != 0 {
			if ordered, ok := engine.transaction.(BeforeParentTransaction); ok {
				if orderErr := ordered.CompleteBeforeParent(engine.ctx, node); orderErr != nil {
					return orderErr
				}
			}
		}
		applied := AppliedNode{node: node, work: work.clone(), result: result, order: engine.executionOrder(node)}
		if node.ExecutesBeforeParent() || node.Operation() == mutationir.BranchProbe {
			if engine.dependencyResults == nil {
				engine.dependencyResults = make(map[uint32]AppliedNode)
			}
			engine.dependencyResults[node.Ordinal()] = applied.clone()
		}
		if writesRow(node.Operation()) {
			engine.applied = append(engine.applied, applied)
			// Pre-parent dependencies execute physically before this write, but
			// retain their declared graph position after their parent for reverse
			// After/AfterCommit verification and public execution receipts.
			engine.applied = append(engine.applied, cloneApplied(deferredApplied)...)
		}
		childPath := clonePath(path)
		childPath[node.Ordinal()] = applied
		if childErr := engine.executeChildren(node, childPath, 0); childErr != nil {
			return childErr
		}
	}
	return nil
}

func (engine *executionEngine) executeBeforeParentChildren(node mutationir.Node, path map[uint32]AppliedNode) ([]AppliedNode, []AppliedNode, error) {
	var dependencies []AppliedNode
	var deferred []AppliedNode
	// A conditional dependency wrapper must select exactly one branch before
	// executing it; its branch nodes are also marked pre-parent only so they can
	// run anchorless inside the selected wrapper.
	if node.ExecutesBeforeParent() && container(node.Operation()) {
		return nil, nil, nil
	}
	hasDependency := false
	for _, ordinal := range node.ChildOrdinals() {
		if int(ordinal) < len(engine.nodes) && engine.nodes[ordinal].ExecutesBeforeParent() {
			hasDependency = true
			break
		}
	}
	if hasDependency {
		if ordered, ok := engine.transaction.(BeforeParentTransaction); ok {
			if err := ordered.BeginBeforeParent(engine.ctx, node); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, ordinal := range node.ChildOrdinals() {
		if int(ordinal) >= len(engine.nodes) {
			return nil, nil, fmt.Errorf("P4_NESTED_EXEC_GRAPH: node %d names an absent dependency", node.Ordinal())
		}
		child := engine.nodes[ordinal]
		if !child.ExecutesBeforeParent() {
			continue
		}
		start := len(engine.applied)
		engine.dependencyDepth++
		if err := engine.executeNode(child, path); err != nil {
			engine.dependencyDepth--
			return nil, nil, err
		}
		engine.dependencyDepth--
		produced := cloneApplied(engine.applied[start:])
		engine.applied = engine.applied[:start]
		deferred = append(deferred, produced...)
		found := false
		for index := range produced {
			if produced[index].node.Ordinal() == child.Ordinal() {
				dependencies = append(dependencies, produced[index].clone())
				found = true
				break
			}
		}
		if !found {
			for index := range produced {
				candidate := produced[index]
				if candidate.node.ModelID() == child.ModelID() {
					dependencies = append(dependencies, candidate.clone())
					found = true
					break
				}
			}
		}
		if applied, ok := engine.dependencyResults[child.Ordinal()]; !found && ok {
			dependencies = append(dependencies, applied.clone())
			found = true
		}
		if !found {
			// Conditional dependency containers do not apply a row themselves.
			// Exactly one selected direct branch does, or records a probe result.
			for _, branchOrdinal := range child.ChildOrdinals() {
				if applied, ok := engine.dependencyResults[branchOrdinal]; ok {
					dependencies = append(dependencies, applied.clone())
					found = true
					break
				}
				for index := range produced {
					if produced[index].node.Ordinal() == branchOrdinal {
						dependencies = append(dependencies, produced[index].clone())
						found = true
						break
					}
				}
				if found {
					break
				}
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("P4_NESTED_EXEC_GRAPH: node %d produced no dependency result", child.Ordinal())
		}
	}
	return dependencies, deferred, nil
}

func membershipOperation(operation mutationir.Operation) bool {
	return operation == mutationir.Connect || operation == mutationir.Disconnect || operation == mutationir.SetRelation
}

func postExpansionTransform(node mutationir.Node) bool {
	if membershipOperation(node.Operation()) {
		return true
	}
	position, ok := node.RelationPosition()
	return ok && position.Kind() == mutationir.PositionCurrentToOne
}

func (engine *executionEngine) executeReplacement(replacement SubtreeReplacement, parent, anchor *AppliedNode, logicalOrdinal uint32) error {
	nodes := replacement.graph.Nodes()
	if len(nodes) == 0 || int(replacement.entry) >= len(nodes) {
		return fmt.Errorf("P4_NESTED_TRANSFORM: replacement graph or entry is invalid")
	}
	original := engine.nodes
	originalPrefix := engine.orderPrefix
	originalEntry, originalHasEntry := engine.orderEntry, engine.hasOrderEntry
	engine.nodes = nodes
	if len(originalPrefix) == 0 {
		category := uint32(2)
		if engine.dependencyDepth != 0 {
			category = 1
		}
		engine.orderPrefix = []uint32{category, logicalOrdinal}
	} else {
		category := uint32(2)
		if engine.dependencyDepth != 0 {
			category = 1
		}
		engine.orderPrefix = append(append(append([]uint32(nil), originalPrefix...), category), logicalOrdinal)
	}
	engine.orderEntry, engine.hasOrderEntry = replacement.entry, true
	defer func() {
		engine.nodes = original
		engine.orderPrefix = originalPrefix
		engine.orderEntry, engine.hasOrderEntry = originalEntry, originalHasEntry
	}()
	// The replacement graph contains a non-executed compiler anchor at ordinal
	// zero. Seed it with the exact already-applied external owner so ordinary
	// parent/anchor graph validation remains active for the selected entry.
	inherited := make(map[uint32]AppliedNode)
	entry := nodes[replacement.entry]
	if ordinal, ok := entry.ParentOrdinal(); ok {
		if parent != nil {
			inherited[ordinal] = parent.clone()
		} else if anchor != nil {
			inherited[ordinal] = anchor.clone()
		}
	}
	if ordinal, ok := entry.RelationAnchorOrdinal(); ok {
		if anchor != nil {
			inherited[ordinal] = anchor.clone()
		} else if parent != nil {
			inherited[ordinal] = parent.clone()
		}
	}
	return engine.executeNodeWithContext(entry, inherited, parent, anchor, false)
}

func (engine *executionEngine) executionOrder(node mutationir.Node) []uint32 {
	if len(engine.orderPrefix) != 0 {
		result := append([]uint32(nil), engine.orderPrefix...)
		if engine.hasOrderEntry && node.Ordinal() == engine.orderEntry {
			// The replacement entry occupies the original node's logical path.
			// Its pre-parent dependencies execute physically first but are
			// observable descendants of that logical entry, so they extend this
			// prefix rather than sorting ahead of it.
			return result
		}
		category := uint32(2)
		if engine.dependencyDepth != 0 {
			category = 1
		}
		return append(result, category, node.Ordinal())
	}
	if node.Ordinal() == 0 && engine.dependencyDepth == 0 {
		return []uint32{0}
	}
	category := uint32(2)
	if engine.dependencyDepth != 0 {
		category = 1
	}
	return []uint32{category, node.Ordinal()}
}

func compareExecutionOrder(left, right []uint32) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func (engine *executionEngine) executeChildren(node mutationir.Node, path map[uint32]AppliedNode, selected mutationir.Branch) error {
	if err := engine.preexpandCoordinatedDeletes(node, path, selected); err != nil {
		return err
	}
	for _, ordinal := range node.ChildOrdinals() {
		if int(ordinal) >= len(engine.nodes) {
			return fmt.Errorf("P4_NESTED_EXEC_GRAPH: node %d names an absent child", node.Ordinal())
		}
		child := engine.nodes[ordinal]
		if child.ExecutesBeforeParent() && !node.ExecutesBeforeParent() {
			continue
		}
		if container(node.Operation()) && !selectedChild(node.Operation(), child.Branch(), selected) {
			continue
		}
		if err := engine.executeNode(child, path); err != nil {
			return err
		}
	}
	return nil
}

// preexpandCoordinatedDeletes locks and authorizes the related target of the
// optional source to-one Delete choreography before its preceding Disconnect
// changes relation state. The ordinary child execution later consumes this
// exact RuntimeExpansion, so there is no second target lookup between the
// owner update and target delete.
func (engine *executionEngine) preexpandCoordinatedDeletes(parent mutationir.Node, path map[uint32]AppliedNode, selected mutationir.Branch) error {
	children := parent.ChildOrdinals()
	for index, ordinal := range children {
		if int(ordinal) >= len(engine.nodes) {
			return fmt.Errorf("P4_NESTED_EXEC_GRAPH: node %d names an absent child", parent.Ordinal())
		}
		candidate := engine.nodes[ordinal]
		if container(parent.Operation()) && !selectedChild(parent.Operation(), candidate.Branch(), selected) || !coordinatedCurrentDelete(candidate, children[:index], engine.nodes) {
			continue
		}
		candidateParent, anchor, err := engine.contextFor(candidate, path)
		if err != nil {
			return err
		}
		expansion, err := engine.transaction.ExpandNested(engine.ctx, ExpansionRequest{node: candidate, parent: candidateParent, anchor: anchor})
		if err != nil {
			return err
		}
		if err := validateExpansion(candidate, expansion.Works(), expansion.branch); err != nil {
			return err
		}
		if engine.preexpanded == nil {
			engine.preexpanded = make(map[uint32]RuntimeExpansion)
		}
		engine.preexpanded[ordinal] = expansion
	}
	return nil
}

func coordinatedCurrentDelete(candidate mutationir.Node, preceding []uint32, nodes []mutationir.Node) bool {
	if candidate.Operation() != mutationir.Delete {
		return false
	}
	position, ok := candidate.RelationPosition()
	if !ok || position.Kind() != mutationir.PositionCurrentToOne {
		return false
	}
	for _, ordinal := range preceding {
		if int(ordinal) >= len(nodes) {
			continue
		}
		sibling := nodes[ordinal]
		siblingPosition, present := sibling.RelationPosition()
		if sibling.Operation() == mutationir.Disconnect && present && siblingPosition.Kind() == mutationir.PositionCurrentToOne &&
			siblingPosition.ParentModelID() == position.ParentModelID() && siblingPosition.FieldID() == position.FieldID() &&
			siblingPosition.RelationID() == position.RelationID() && siblingPosition.TargetModelID() == position.TargetModelID() {
			return true
		}
	}
	return false
}

func (engine *executionEngine) contextFor(node mutationir.Node, path map[uint32]AppliedNode) (*AppliedNode, *AppliedNode, error) {
	var parent, anchor *AppliedNode
	if ordinal, ok := node.ParentOrdinal(); ok {
		if value, present := path[ordinal]; present {
			copy := value.clone()
			parent = &copy
		} else if !node.ExecutesBeforeParent() && !container(engine.nodes[ordinal].Operation()) {
			return nil, nil, fmt.Errorf("P4_NESTED_EXEC_GRAPH: node %d lacks its applied parent", node.Ordinal())
		}
	}
	if ordinal, ok := node.RelationAnchorOrdinal(); ok {
		value, present := path[ordinal]
		if !present {
			if node.ExecutesBeforeParent() {
				return parent, nil, nil
			}
			return nil, nil, fmt.Errorf("P4_NESTED_EXEC_GRAPH: node %d lacks its relation anchor", node.Ordinal())
		}
		copy := value.clone()
		anchor = &copy
	}
	return parent, anchor, nil
}

func validateExpansion(node mutationir.Node, works []RuntimeWork, branch mutationir.Branch) error {
	switch node.Operation() {
	case mutationir.ConnectOrCreate:
		if len(works) != 0 || branch != mutationir.ConnectOrCreateConnectBranch && branch != mutationir.ConnectOrCreateCreateBranch {
			return fmt.Errorf("P4_NESTED_EXEC_BRANCH: connect-or-create node %d returned an invalid branch", node.Ordinal())
		}
	case mutationir.Upsert:
		if len(works) != 0 || branch != mutationir.UpsertCreateBranch && branch != mutationir.UpsertUpdateBranch {
			return fmt.Errorf("P4_NESTED_EXEC_BRANCH: upsert node %d returned an invalid branch", node.Ordinal())
		}
	case mutationir.CreateMany:
		if len(works) != 0 || branch != 0 {
			return fmt.Errorf("P4_NESTED_EXEC_BRANCH: create-many container %d returned runtime work", node.Ordinal())
		}
	default:
		if branch != 0 {
			return fmt.Errorf("P4_NESTED_EXEC_BRANCH: non-container node %d selected a branch", node.Ordinal())
		}
		position, related := node.RelationPosition()
		requireOne := node.Operation() == mutationir.Create || node.Operation() == mutationir.Update || node.Operation() == mutationir.Delete || node.Operation() == mutationir.BranchProbe
		if related {
			requireOne = position.Kind() == mutationir.PositionEndpoint || position.Kind() == mutationir.PositionCurrentToOne || position.Kind() == mutationir.PositionRelatedTarget || position.Kind() == mutationir.PositionBranchResult
			var expanded uint64
			for _, work := range works {
				expanded += uint64(work.TouchedRows())
			}
			if expansion, bounded := position.Expansion(); bounded && expanded > uint64(expansion.MaxRows()) {
				return fmt.Errorf("P4_NESTED_EXEC_LIMIT: node %d expansion exceeds its row bound %d", node.Ordinal(), expansion.MaxRows())
			}
		}
		// A locked membership expansion may prove that the exact correlation
		// tuple already has the requested value. Zero work is then the complete,
		// successful result; missing selected rows are rejected by expansion
		// before this boundary and cannot masquerade as a no-op.
		membershipNoOp := membershipOperation(node.Operation()) && len(works) == 0
		optionalProbeNoOp := node.Operation() == mutationir.BranchProbe && related && position.Kind() == mutationir.PositionCurrentToOne && len(works) == 0
		if requireOne && len(works) != 1 && !membershipNoOp && !optionalProbeNoOp {
			return fmt.Errorf("P4_NESTED_EXEC_SET: node %d requires exactly one runtime row", node.Ordinal())
		}
	}
	return nil
}

func validateApplyResult(node mutationir.Node, result ApplyResult) error {
	before, hasBefore := result.Before()
	after, hasAfter := result.After()
	if hasBefore && before.ModelID() != node.ModelID() || hasAfter && after.ModelID() != node.ModelID() {
		return fmt.Errorf("P4_NESTED_EXEC_RESULT: node %d returned a foreign image", node.Ordinal())
	}
	switch node.Operation() {
	case mutationir.Create:
		if hasBefore || !hasAfter {
			return fmt.Errorf("P4_NESTED_EXEC_RESULT: create node %d requires only an after image", node.Ordinal())
		}
	case mutationir.Delete, mutationir.DeleteMany:
		if node.Operation() == mutationir.DeleteMany && !hasBefore && !hasAfter {
			return nil
		}
		if !hasBefore || hasAfter {
			return fmt.Errorf("P4_NESTED_EXEC_RESULT: delete node %d requires only a before image", node.Ordinal())
		}
	case mutationir.BranchProbe:
		if hasBefore || !hasAfter {
			return fmt.Errorf("P4_NESTED_EXEC_RESULT: branch probe %d requires one selected image", node.Ordinal())
		}
	default:
		if node.Operation() == mutationir.UpdateMany && !hasBefore && !hasAfter {
			return nil
		}
		if !hasBefore || !hasAfter {
			return fmt.Errorf("P4_NESTED_EXEC_RESULT: write node %d requires before and after images", node.Ordinal())
		}
	}
	return nil
}

func selectedChild(operation mutationir.Operation, branch, selected mutationir.Branch) bool {
	if operation == mutationir.CreateMany {
		return branch == mutationir.BatchItemBranch
	}
	return branch == selected
}

func container(operation mutationir.Operation) bool {
	return operation == mutationir.CreateMany || operation == mutationir.ConnectOrCreate || operation == mutationir.Upsert
}

func writesRow(operation mutationir.Operation) bool {
	return !container(operation) && operation != mutationir.BranchProbe
}

func clonePath(input map[uint32]AppliedNode) map[uint32]AppliedNode {
	result := make(map[uint32]AppliedNode, len(input)+1)
	for ordinal, applied := range input {
		result[ordinal] = applied.clone()
	}
	return result
}

func cloneApplied(input []AppliedNode) []AppliedNode {
	result := make([]AppliedNode, len(input))
	for index := range input {
		result[index] = input[index].clone()
	}
	return result
}
