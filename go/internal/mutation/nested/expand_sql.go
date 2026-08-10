package nested

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/jmoiron/sqlx"
)

type SQLExpansionRequest struct {
	Expansion     ExpansionRequest
	Queryer       sqlx.QueryerContext
	Registry      *schema.Registry
	Provider      policyir.Provider
	Capabilities  policysql.CapabilityProof
	MaxRows       uint32
	MaxParameters uint32
}

// ExpandRelationSQL is the production database-expansion half of the nested
// TransactionAdapter. It covers every relation-position kind, truthful branch
// selection, source/inverse ownership, and exact set-difference effects. Root
// scalar expansion remains runtime-owned because its locked image is produced
// by the scalar mutation program itself.
func ExpandRelationSQL(ctx context.Context, request SQLExpansionRequest) (RuntimeExpansion, error) {
	if ctx == nil || request.Queryer == nil || request.Registry == nil || request.MaxRows == 0 || request.MaxParameters == 0 {
		return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_INPUT: context, queryer, registry, and positive bounds are required")
	}
	node := request.Expansion.Node()
	position, relational := node.RelationPosition()
	if !relational {
		return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_INPUT: node %d is not relation-positioned", node.Ordinal())
	}
	if position.Kind() == mutationir.PositionEndpoint {
		if node.Operation() == mutationir.CreateMany {
			return NewRuntimeExpansion(nil, 0)
		}
		if node.Operation() != mutationir.Create {
			return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_INPUT: endpoint node %d is not a create", node.Ordinal())
		}
		var order [20]byte
		model := node.ModelID()
		copy(order[:16], model[:])
		binary.BigEndian.PutUint32(order[16:], node.Ordinal())
		work, err := NewCreateWork(node.ModelID(), order[:])
		if err != nil {
			return RuntimeExpansion{}, err
		}
		return NewRuntimeExpansion([]RuntimeWork{work}, 0)
	}
	var anchor mutationdecode.Row
	anchorApplied, ok := request.Expansion.RelationAnchor()
	if !ok {
		anchorlessDependency := node.ExecutesBeforeParent() && (node.Operation() == mutationir.BranchProbe || node.Operation() == mutationir.ConnectOrCreate)
		if !anchorlessDependency {
			return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_CONTEXT: node %d lacks its relation anchor", node.Ordinal())
		}
	} else {
		var err error
		anchor, err = appliedRow(anchorApplied)
		if err != nil {
			return RuntimeExpansion{}, err
		}
	}
	endpoint, ok := request.Registry.RelationEndpoint(
		golem.ModelID(position.ParentModelID()), golem.FieldID(position.FieldID()), golem.RelationID(position.RelationID()),
	)
	if !ok {
		return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_SCHEMA: exact relation endpoint is absent")
	}
	if position.Kind() == mutationir.PositionBranchResult {
		parentApplied, present := request.Expansion.Parent()
		if !present {
			return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_CONTEXT: branch-result node %d lacks its parent", node.Ordinal())
		}
		related, err := appliedRow(parentApplied)
		if err != nil {
			return RuntimeExpansion{}, err
		}
		effect := MembershipConnect
		if node.Operation() == mutationir.Disconnect {
			effect = MembershipDisconnect
		}
		change, err := membershipWouldChange(endpoint, anchor, related, effect)
		if err != nil {
			return RuntimeExpansion{}, err
		}
		if !change {
			return NewRuntimeExpansion(nil, 0)
		}
		work, err := membershipWork(request.Registry, endpoint, anchor, related, effect)
		if err != nil {
			return RuntimeExpansion{}, err
		}
		return NewRuntimeExpansion([]RuntimeWork{work}, 0)
	}
	program, err := RenderRelationExpansion(RelationExpansionSQLRequest{
		Node: node, Anchor: anchor, Registry: request.Registry, Provider: request.Provider,
		Capabilities: request.Capabilities, MaxRows: request.MaxRows, MaxParameters: request.MaxParameters,
	})
	if err != nil {
		return RuntimeExpansion{}, err
	}
	statements := program.Statements()
	if position.Kind() == mutationir.PositionSetDifference {
		return expandSetDifference(ctx, request, endpoint, anchor, statements)
	}
	if len(statements) != 1 {
		return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_PROGRAM: node %d rendered %d statements", node.Ordinal(), len(statements))
	}
	rows, err := ExecuteRelationSQL(ctx, request.Queryer, request.Registry, request.Provider, statements[0])
	if err != nil {
		return RuntimeExpansion{}, err
	}
	if len(rows) == 0 {
		exactProbe := node.Operation() == mutationir.BranchProbe && node.Branch() == mutationir.MainBranch
		exactTarget := position.Kind() == mutationir.PositionRelatedTarget && (exactProbe || node.Operation() != mutationir.BranchProbe && node.Operation() != mutationir.ConnectOrCreate && node.Operation() != mutationir.Upsert)
		missingCurrent := position.Kind() == mutationir.PositionCurrentToOne && (node.Operation() == mutationir.Update || node.Operation() == mutationir.Delete)
		if exactTarget || missingCurrent {
			return RuntimeExpansion{}, &NotFoundError{Model: position.TargetModelID(), Field: position.FieldID()}
		}
	}
	if node.Operation() == mutationir.ConnectOrCreate || node.Operation() == mutationir.Upsert {
		if len(rows) > 1 {
			return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_CARDINALITY: branch probe returned %d rows", len(rows))
		}
		branch := mutationir.ConnectOrCreateCreateBranch
		if node.Operation() == mutationir.Upsert {
			branch = mutationir.UpsertCreateBranch
		}
		if len(rows) == 1 {
			branch = mutationir.ConnectOrCreateConnectBranch
			if node.Operation() == mutationir.Upsert {
				branch = mutationir.UpsertUpdateBranch
			}
		}
		return NewRuntimeExpansion(nil, branch)
	}
	if position.Kind() == mutationir.PositionRelatedTarget && len(rows) != 1 {
		return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_CARDINALITY: related target returned %d rows; expected 1", len(rows))
	}
	if node.Operation() == mutationir.BranchProbe {
		// An optional source to-one Disconnect has no selected target when its
		// current FK is NULL. That is a truthful no-op: there is no target to
		// authorize and therefore no owner effect child to execute. Conversely,
		// a populated FK with no authorized probe row is policy-invisible and
		// must remain indistinguishable from a missing exact target.
		if len(rows) == 0 && position.Kind() == mutationir.PositionCurrentToOne {
			populated, err := sourceCorrelationPopulated(endpoint, anchor)
			if err != nil {
				return RuntimeExpansion{}, err
			}
			if populated {
				return RuntimeExpansion{}, &NotFoundError{Model: position.TargetModelID(), Field: position.FieldID()}
			}
			return NewRuntimeExpansion(nil, 0)
		}
		base, err := ExistingRowWork(request.Registry, rows[0])
		if err != nil {
			return RuntimeExpansion{}, err
		}
		identity, present := base.Identity()
		if !present {
			return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_IDENTITY: branch probe row has no primary identity")
		}
		resolved, err := NewResolvedRelationWork(node.ModelID(), identity, base.OrderKey(), rows[0], 0)
		if err != nil {
			return RuntimeExpansion{}, err
		}
		return NewRuntimeExpansion([]RuntimeWork{resolved}, 0)
	}
	if node.Operation() == mutationir.UpdateMany || node.Operation() == mutationir.DeleteMany {
		var order [20]byte
		model := node.ModelID()
		copy(order[:16], model[:])
		binary.BigEndian.PutUint32(order[16:], node.Ordinal())
		work, err := NewBatchWork(node.ModelID(), rows, order[:])
		if err != nil {
			return RuntimeExpansion{}, err
		}
		return NewRuntimeExpansion([]RuntimeWork{work}, 0)
	}
	effect := MembershipEffect(0)
	if node.Operation() == mutationir.Connect {
		effect = MembershipConnect
	} else if node.Operation() == mutationir.Disconnect {
		effect = MembershipDisconnect
	}
	works := make([]RuntimeWork, 0, len(rows))
	for _, row := range rows {
		if effect != 0 {
			change, changeErr := membershipWouldChange(endpoint, anchor, row, effect)
			if changeErr != nil {
				return RuntimeExpansion{}, changeErr
			}
			if !change {
				continue
			}
			work, workErr := membershipWork(request.Registry, endpoint, anchor, row, effect)
			if workErr != nil {
				return RuntimeExpansion{}, workErr
			}
			works = append(works, work)
		} else {
			work, workErr := ExistingRowWork(request.Registry, row)
			if workErr != nil {
				return RuntimeExpansion{}, workErr
			}
			works = append(works, work)
		}
	}
	return NewRuntimeExpansion(works, 0)
}

func sourceCorrelationPopulated(endpoint schema.RelationEndpoint, anchor mutationdecode.Row) (bool, error) {
	if endpoint.Role() != compilerir.RelationSource {
		return false, fmt.Errorf("P4_NESTED_EXPAND_INPUT: current authorization probe is not source-owned")
	}
	nulls := 0
	for _, pair := range endpoint.Correlation() {
		cell, present := anchor.Cell(policyir.FieldID(pair.ParentFieldID()))
		if !present {
			return false, fmt.Errorf("P4_NESTED_EXPAND_INPUT: source correlation field %x is absent", pair.ParentFieldID())
		}
		if cell.IsNull() {
			nulls++
		}
	}
	if nulls != 0 && nulls != len(endpoint.Correlation()) {
		return false, fmt.Errorf("P4_NESTED_EXPAND_INPUT: composite source correlation is partially NULL")
	}
	return nulls == 0, nil
}

func expandSetDifference(ctx context.Context, request SQLExpansionRequest, endpoint schema.RelationEndpoint, anchor mutationdecode.Row, statements []RelationSQLStatement) (RuntimeExpansion, error) {
	if len(statements) == 0 || statements[0].Role() != ExpandCurrentMembership {
		return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_PROGRAM: set difference lacks current membership")
	}
	current, err := ExecuteRelationSQL(ctx, request.Queryer, request.Registry, request.Provider, statements[0])
	if err != nil {
		return RuntimeExpansion{}, err
	}
	desired := make([]mutationdecode.Row, 0, len(statements)-1)
	for _, statement := range statements[1:] {
		rows, err := ExecuteRelationSQL(ctx, request.Queryer, request.Registry, request.Provider, statement)
		if err != nil {
			return RuntimeExpansion{}, err
		}
		if len(rows) != 1 {
			if len(rows) == 0 {
				return RuntimeExpansion{}, &NotFoundError{Model: policyir.ModelID(endpoint.TargetModelID()), Field: policyir.FieldID(endpoint.FieldID())}
			}
			return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_CARDINALITY: desired set target returned %d rows; expected 1", len(rows))
		}
		desired = append(desired, rows[0])
	}
	// A source to-one endpoint stores the membership on the anchor row. Moving
	// from one target to another is one FK overwrite, not a transient NULL then
	// SET pair. Emitting both differences would duplicate the same owner work
	// identity and would incorrectly reject required relations.
	if endpoint.Role() == compilerir.RelationSource && endpoint.Cardinality() == compilerir.RelationOne {
		if len(current) > 1 || len(desired) > 1 {
			return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXPAND_CARDINALITY: source to-one set has current=%d desired=%d", len(current), len(desired))
		}
		if len(desired) == 1 {
			change, changeErr := membershipWouldChange(endpoint, anchor, desired[0], MembershipConnect)
			if changeErr != nil {
				return RuntimeExpansion{}, changeErr
			}
			if !change {
				return NewRuntimeExpansion(nil, 0)
			}
			work, err := membershipWork(request.Registry, endpoint, anchor, desired[0], MembershipConnect)
			if err != nil {
				return RuntimeExpansion{}, err
			}
			return NewRuntimeExpansion([]RuntimeWork{work}, 0)
		}
		if len(current) == 1 {
			work, err := membershipWork(request.Registry, endpoint, anchor, current[0], MembershipDisconnect)
			if err != nil {
				return RuntimeExpansion{}, err
			}
			return NewRuntimeExpansion([]RuntimeWork{work}, 0)
		}
		return NewRuntimeExpansion(nil, 0)
	}
	connect, disconnect, err := RelationSetDifference(request.Registry, current, desired)
	if err != nil {
		return RuntimeExpansion{}, err
	}
	works := make([]RuntimeWork, 0, len(connect)+len(disconnect))
	for _, effectRows := range []struct {
		effect MembershipEffect
		rows   []mutationdecode.Row
	}{{MembershipConnect, connect}, {MembershipDisconnect, disconnect}} {
		for _, row := range effectRows.rows {
			work, err := membershipWork(request.Registry, endpoint, anchor, row, effectRows.effect)
			if err != nil {
				return RuntimeExpansion{}, err
			}
			works = append(works, work)
		}
	}
	return NewRuntimeExpansion(works, 0)
}

func membershipWork(registry *schema.Registry, endpoint schema.RelationEndpoint, anchor, related mutationdecode.Row, effect MembershipEffect) (RuntimeWork, error) {
	if endpoint.Role() == compilerir.RelationSource {
		return OwnerRelationWork(registry, anchor, related, effect)
	}
	works, err := ExistingRelationWorks(registry, []mutationdecode.Row{related}, effect)
	if err != nil {
		return RuntimeWork{}, err
	}
	return works[0], nil
}

// membershipWouldChange compares the complete ordered correlation tuple in
// exact logical-value space. It runs after the owner/target rows are locked
// and before RuntimeWork exists, so a no-op cannot consume touched-row budget,
// execute update hooks, emit a fact, or issue an UPDATE.
func membershipWouldChange(endpoint schema.RelationEndpoint, anchor, related mutationdecode.Row, effect MembershipEffect) (bool, error) {
	if effect != MembershipConnect && effect != MembershipDisconnect {
		return false, fmt.Errorf("P4_NESTED_EXPAND_INPUT: membership effect is invalid")
	}
	owner, valueRow := anchor, related
	if endpoint.Role() == compilerir.RelationInverse {
		owner, valueRow = related, anchor
	}
	matches := true
	for _, pair := range endpoint.Correlation() {
		ownerField, valueField := policyir.FieldID(pair.ParentFieldID()), policyir.FieldID(pair.ChildFieldID())
		if endpoint.Role() == compilerir.RelationInverse {
			ownerField, valueField = policyir.FieldID(pair.ChildFieldID()), policyir.FieldID(pair.ParentFieldID())
		}
		ownerCell, present := owner.Cell(ownerField)
		if !present {
			return false, fmt.Errorf("P4_NESTED_EXPAND_INPUT: owner correlation field %x is absent", ownerField)
		}
		valueCell, present := valueRow.Cell(valueField)
		if !present || valueCell.IsNull() {
			return false, fmt.Errorf("P4_NESTED_EXPAND_INPUT: relation value field %x is absent or NULL", valueField)
		}
		if ownerCell.IsNull() {
			matches = false
			continue
		}
		ownerValue, _ := ownerCell.PolicyValue()
		value, _ := valueCell.PolicyValue()
		if !mutationdecode.EqualValue(ownerValue, value) {
			matches = false
		}
	}
	if effect == MembershipConnect {
		return !matches, nil
	}
	return matches, nil
}

func appliedRow(applied AppliedNode) (mutationdecode.Row, error) {
	result := applied.Result()
	if row, ok := result.After(); ok {
		return row, nil
	}
	if row, ok := result.Before(); ok {
		return row, nil
	}
	return mutationdecode.Row{}, fmt.Errorf("P4_NESTED_EXPAND_CONTEXT: applied node %d has no row image", applied.Node().Ordinal())
}
