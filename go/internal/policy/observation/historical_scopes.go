package observation

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/history"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// HistoricalScope is an exact ordinary policy occurrence on one candidate's
// recorded chain. It contains no cached authority answer. Runtime must compare
// the fresh scope and check its exact identity in the current SQL snapshot.
type HistoricalScope struct {
	identity   mutationdecode.Row
	key        string
	scope      ir.FactScope
	offset     int
	occurrence string
}

func (scope HistoricalScope) Identity() mutationdecode.Row { return scope.identity }
func (scope HistoricalScope) Key() string                  { return scope.key }
func (scope HistoricalScope) Scope() ir.FactScope          { return scope.scope }
func (scope HistoricalScope) Offset() int                  { return scope.offset }

func (candidate HistoricalCandidate) AdditionalScopes() []HistoricalScope {
	return append([]HistoricalScope(nil), candidate.additional...)
}

type historicalScopeCollector struct {
	matcher   *historicalMatcher
	path      Path
	evaluator *history.Evaluator
	ownerKey  string
	scopes    []HistoricalScope
	guards    []ir.Condition
	seen      map[string]bool
	allowed   bool
	facts     map[ir.Condition]bool
}

// These scans inspect only ordinary syntax. A Fact's physical body and scope
// are separately evaluated and must not introduce ordinary policy owners.
func (collector *historicalScopeCollector) hasFacts(condition ir.Condition) (bool, error) {
	if err := collector.matcher.step(1); err != nil {
		return false, err
	}
	if answer, present := collector.facts[condition]; present {
		return answer, nil
	}
	answer := false
	switch condition.Kind() {
	case ir.ConditionFact, ir.ConditionFactReference:
		answer = true
	case ir.ConditionLogical:
		if err := collector.matcher.step(condition.ChildCount()); err != nil {
			return false, err
		}
		_, children, _ := condition.Logical()
		for _, child := range children {
			found, err := collector.hasFacts(child)
			if err != nil {
				return false, err
			}
			answer = answer || found
		}
	case ir.ConditionRelation:
		_, _, _, _, child, _ := condition.Relation()
		if child != nil {
			var err error
			answer, err = collector.hasFacts(*child)
			if err != nil {
				return false, err
			}
		}
	}
	collector.facts[condition] = answer
	return answer, nil
}

// Primary-owner data facts can occur under arbitrary Boolean relevance. Their
// one scope already fences this path; this is not permission to project a side
// policy's scope through an OR or NOT.
func (collector *historicalScopeCollector) primaryContext(condition ir.Condition, row mutationdecode.Row) (bool, error) {
	key, err := history.RowKey(collector.matcher.registry, collector.matcher.graph.Provider(), row)
	if err != nil {
		return false, err
	}
	if key != collector.ownerKey {
		return false, nil
	}
	if err := collector.matcher.step(1); err != nil {
		return false, err
	}
	hasFacts, err := collector.hasFacts(condition)
	if err != nil || !hasFacts {
		return !hasFacts, err
	}
	switch condition.Kind() {
	case ir.ConditionFact:
		scope, _ := condition.FactScope()
		actual, err := ir.FactScopeFingerprint(scope)
		if err != nil {
			return false, err
		}
		owner, err := ir.FactScopeFingerprint(collector.path.owner.scope)
		return actual == owner, err
	case ir.ConditionLogical:
		if err := collector.matcher.step(condition.ChildCount()); err != nil {
			return false, err
		}
		_, children, _ := condition.Logical()
		for _, child := range children {
			primary, err := collector.primaryContext(child, row)
			if err != nil || !primary {
				return false, err
			}
		}
		return true, nil
	default:
		return false, nil
	}
}

// Homogeneous local facts share one complete policy-owned audience regardless
// of their data Boolean algebra. Do not descend physical bodies, and never
// lift an ordinary side policy out of its relation or conditional branch.
func (collector *historicalScopeCollector) localScope(condition ir.Condition) (ir.FactScope, bool, bool, error) {
	if err := collector.matcher.step(1); err != nil {
		return ir.FactScope{}, false, false, err
	}
	switch condition.Kind() {
	case ir.ConditionFact:
		scope, _ := condition.FactScope()
		return scope, true, true, nil
	case ir.ConditionLogical:
		if err := collector.matcher.step(condition.ChildCount()); err != nil {
			return ir.FactScope{}, false, false, err
		}
		var scope ir.FactScope
		var fingerprint ir.Fingerprint
		found := false
		for index := 0; index < condition.ChildCount(); index++ {
			child, present, homogeneous, err := collector.localScope(condition.Child(index))
			if err != nil || !homogeneous {
				return ir.FactScope{}, false, false, err
			}
			if !present {
				continue
			}
			actual, err := ir.FactScopeFingerprint(child)
			if err != nil {
				return ir.FactScope{}, false, false, err
			}
			if found && actual != fingerprint {
				return ir.FactScope{}, false, false, nil
			}
			scope, fingerprint, found = child, actual, true
		}
		return scope, found, true, nil
	default:
		facts, err := collector.hasFacts(condition)
		return ir.FactScope{}, false, !facts, err
	}
}

// carryPrefix separates current guards from authority below the exact next
// main-path edge. It never reads an uncaptured row or assumes a side witness.
// Only positive conjunctive to-one continuations can name the same child as
// the current prefix; other authority needs recorded evidence of its own.
func (collector *historicalScopeCollector) carryPrefix(condition ir.Condition, index int) (ir.Condition, []guardRefinement, error) {
	matcher := collector.matcher
	if err := matcher.step(1); err != nil {
		return ir.Condition{}, nil, err
	}
	main := collector.path.hops[index].relation
	if condition.ModelID() != main.ModelID() {
		return ir.Condition{}, nil, coverage(collector.path, "ordinary prefix authority changed model")
	}
	facts, err := collector.hasFacts(condition)
	if err != nil {
		return ir.Condition{}, nil, err
	}
	if !facts {
		return condition, nil, nil
	}
	switch condition.Kind() {
	case ir.ConditionLogical:
		operation, _ := condition.LogicalOperator()
		if operation != ir.LogicalAnd {
			return ir.Condition{}, nil, coverage(collector.path, "conditional or negative authority precedes the captured cut")
		}
		if err := matcher.step(condition.ChildCount()); err != nil {
			return ir.Condition{}, nil, err
		}
		parts := make([]ir.Condition, 0, condition.ChildCount())
		var carried []guardRefinement
		for child := 0; child < condition.ChildCount(); child++ {
			residual, next, err := collector.carryPrefix(condition.Child(child), index)
			if err != nil {
				return ir.Condition{}, nil, err
			}
			if err := matcher.step(len(next)); err != nil {
				return ir.Condition{}, nil, err
			}
			parts = append(parts, residual)
			carried = append(carried, next...)
		}
		residual, err := logical(condition.ModelID(), ir.LogicalAnd, parts)
		return residual, carried, err
	case ir.ConditionRelation:
		field, relation, target, cardinality, child, _ := condition.Relation()
		operation, _ := condition.Operator()
		mainField, mainRelation, mainTarget, mainCardinality, _, _ := main.Relation()
		mainOperation, _ := main.Operator()
		if operation != ir.OperatorRelationIs || mainOperation != ir.OperatorRelationIs || cardinality != ir.RelationToOne || mainCardinality != ir.RelationToOne || field != mainField || relation != mainRelation || target != mainTarget || child == nil {
			return ir.Condition{}, nil, coverage(collector.path, "ordinary authority does not follow the positive captured prefix")
		}
		endpoint, present := matcher.registry.RelationEndpoint(golem.ModelID(condition.ModelID()), golem.FieldID(field), golem.RelationID(relation))
		if !present || ir.ModelID(endpoint.TargetModelID()) != target || endpoint.Cardinality() != compilerir.RelationOne {
			return ir.Condition{}, nil, coverage(collector.path, "ordinary prefix authority endpoint differs from schema")
		}
		// Main endpoint provenance was checked against the registry. Matching
		// the complete endpoint (not merely its target model) keeps this
		// authority on the same occurrence and correlation as the cut.
		if err := matcher.step(1); err != nil {
			return ir.Condition{}, nil, err
		}
		truth, err := ir.NewConstant(condition.ModelID(), true)
		return truth, []guardRefinement{{condition: *child}}, err
	default:
		return ir.Condition{}, nil, coverage(collector.path, "ordinary authority precedes the captured cut")
	}
}

// ordinaryScopeRequirements certifies only captured ordinary occurrences. A
// physical fact/scope path never imports the read policy of its target model.
// The returned path is private to this candidate, with certified side edges
// removed from current guards, while suffix proofs retain the original path.
func (matcher *historicalMatcher) ordinaryScopeRequirements(path Path, offset int, chain []mutationdecode.Row, evaluator *history.Evaluator) (Path, []HistoricalScope, []ir.Condition, bool, error) {
	if err := matcher.step(len(path.hops)); err != nil {
		return Path{}, nil, nil, false, err
	}
	copy := path
	copy.hops = append([]Hop(nil), path.hops...)
	ownerKey, err := history.RowKey(matcher.registry, matcher.graph.Provider(), chain[len(chain)-1])
	if err != nil {
		return Path{}, nil, nil, false, err
	}
	collector := historicalScopeCollector{matcher: matcher, path: path, evaluator: evaluator, ownerKey: ownerKey, seen: map[string]bool{}, facts: map[ir.Condition]bool{}, allowed: true}
	var carried []guardRefinement
	for index := 0; index <= path.owner.offset; index++ {
		if err := matcher.step(1); err != nil {
			return Path{}, nil, nil, false, err
		}
		raw := path.owner.ordinary
		if index < path.owner.offset {
			raw = path.hops[index].ordinary
		}
		arrived := len(carried) != 0
		if arrived {
			if err := matcher.step(len(raw) + len(carried)); err != nil {
				return Path{}, nil, nil, false, err
			}
			combined := make([]guardRefinement, 0, len(raw)+len(carried))
			combined = append(combined, raw...)
			raw = append(combined, carried...)
			carried = nil
		}
		if len(raw) == 0 {
			continue
		}
		if index < offset {
			if err := matcher.step(len(raw)); err != nil {
				return Path{}, nil, nil, false, err
			}
			parts := make([]ir.Condition, 0, len(raw))
			for _, item := range raw {
				hasFacts, err := collector.hasFacts(item.condition)
				if err != nil {
					return Path{}, nil, nil, false, err
				}
				if hasFacts && item.negative {
					return Path{}, nil, nil, false, coverage(path, "negative ordinary authority precedes the captured cut")
				}
				residual, next, err := collector.carryPrefix(item.condition, index)
				if err != nil {
					return Path{}, nil, nil, false, err
				}
				if item.negative {
					residual, err = negate(residual)
					if err != nil {
						return Path{}, nil, nil, false, err
					}
				}
				if err := matcher.step(len(next)); err != nil {
					return Path{}, nil, nil, false, err
				}
				parts = append(parts, residual)
				carried = append(carried, next...)
			}
			// The normal current prefix retains its main edge to the exact
			// cut. Remove only the duplicated projected connective from its
			// guard; keeping it would rejoin the cut's old owner after B.
			copy.hops[index].guard, err = logical(path.hops[index].relation.ModelID(), ir.LogicalAnd, parts)
			if err != nil {
				return Path{}, nil, nil, false, err
			}
			continue
		}
		row := chain[index-offset]
		if err := matcher.step(len(raw)); err != nil {
			return Path{}, nil, nil, false, err
		}
		parts := make([]ir.Condition, 0, len(raw))
		for _, item := range raw {
			if err := matcher.step(1); err != nil {
				return Path{}, nil, nil, false, err
			}
			var condition ir.Condition
			hasFacts, factsErr := collector.hasFacts(item.condition)
			if factsErr != nil {
				return Path{}, nil, nil, false, factsErr
			}
			primary, primaryErr := collector.primaryContext(item.condition, row)
			if primaryErr != nil {
				return Path{}, nil, nil, false, primaryErr
			}
			if hasFacts && !primary {
				if item.negative {
					return Path{}, nil, nil, false, coverage(path, "conditional or negative ordinary authority is unsupported")
				}
				condition, err = collector.collect(item.condition, row, index, "")
			} else {
				value, projectErr := project(item.condition, opaqueFacts)
				err = projectErr
				condition = value.upper
				if err == nil && item.negative {
					condition, err = negate(value.lower)
				}
			}
			if err != nil {
				return Path{}, nil, nil, false, err
			}
			if !collector.allowed {
				return Path{}, nil, nil, false, nil
			}
			parts = append(parts, condition)
		}
		if index < path.owner.offset {
			copy.hops[index].guard, err = logical(row.ModelID(), ir.LogicalAnd, parts)
			if err != nil {
				return Path{}, nil, nil, false, err
			}
			if arrived {
				// Original suffix guards need not contain a separately authored
				// source constraint carried from above the cut. Certify that
				// residual on this exact captured row as well as checking it now.
				key, err := history.RowKey(matcher.registry, matcher.graph.Provider(), row)
				if err != nil {
					return Path{}, nil, nil, false, err
				}
				matches, err := evaluator.EvaluateCaptured(key, copy.hops[index].guard)
				if err != nil {
					return Path{}, nil, nil, false, fmt.Errorf("%w: %v", coverage(path, "carried ordinary guard evidence is unknown"), err)
				}
				if !matches {
					return Path{}, nil, nil, false, nil
				}
			}
		} else {
			// A local ordinary guard on the owner is still fundamental. Keep
			// it pinned here so a main-chain candidate cannot bypass a guard
			// also retained by the equivalent side-occurrence candidate.
			residual, err := logical(row.ModelID(), ir.LogicalAnd, parts)
			if err != nil {
				return Path{}, nil, nil, false, err
			}
			if _, constant := residual.Constant(); !constant {
				matches, err := evaluator.EvaluateCaptured(ownerKey, residual)
				if err != nil {
					return Path{}, nil, nil, false, err
				}
				if !matches {
					return Path{}, nil, nil, false, nil
				}
				identity, err := matcher.identityCondition(row)
				if err != nil {
					return Path{}, nil, nil, false, err
				}
				guard, err := logical(row.ModelID(), ir.LogicalAnd, []ir.Condition{identity, residual})
				if err != nil {
					return Path{}, nil, nil, false, err
				}
				collector.guards = append(collector.guards, guard)
			}
		}
	}
	return copy, collector.scopes, collector.guards, true, nil
}

func (collector *historicalScopeCollector) collect(condition ir.Condition, row mutationdecode.Row, offset int, via string) (ir.Condition, error) {
	matcher := collector.matcher
	if err := matcher.step(1); err != nil {
		return ir.Condition{}, err
	}
	if condition.ModelID() != row.ModelID() {
		return ir.Condition{}, coverage(collector.path, "ordinary scope occurrence changed model")
	}
	truth, _ := ir.NewConstant(row.ModelID(), true)
	hasFacts, err := collector.hasFacts(condition)
	if err != nil {
		return ir.Condition{}, err
	}
	if !hasFacts {
		return condition, nil
	}
	primary, err := collector.primaryContext(condition, row)
	if err != nil {
		return ir.Condition{}, err
	}
	if primary {
		value, err := project(condition, opaqueFacts)
		return value.upper, err
	}
	local, present, homogeneous, err := collector.localScope(condition)
	if err != nil {
		return ir.Condition{}, err
	}
	if present && homogeneous {
		if err := collector.addScope(local, row, offset, via); err != nil {
			return ir.Condition{}, err
		}
		value, err := project(condition, opaqueFacts)
		return value.upper, err
	}
	switch condition.Kind() {
	case ir.ConditionFact:
		scope, _ := condition.FactScope()
		if err := collector.addScope(scope, row, offset, via); err != nil {
			return ir.Condition{}, err
		}
		// The data body is relevance, not an ordinary current read check.
		// Its complete policy-owned scope is independently fenced above.
		return truth, nil
	case ir.ConditionLogical:
		if err := matcher.step(condition.ChildCount()); err != nil {
			return ir.Condition{}, err
		}
		operation, children, _ := condition.Logical()
		if operation != ir.LogicalAnd {
			return ir.Condition{}, coverage(collector.path, "conditional or negative ordinary scope boundary is unsupported")
		}
		if err := matcher.step(len(children)); err != nil {
			return ir.Condition{}, err
		}
		parts := make([]ir.Condition, 0, len(children))
		for _, child := range children {
			part, err := collector.collect(child, row, offset, via)
			if err != nil {
				return ir.Condition{}, err
			}
			if !collector.allowed {
				return truth, nil
			}
			parts = append(parts, part)
		}
		return logical(row.ModelID(), ir.LogicalAnd, parts)
	case ir.ConditionRelation:
		field, relation, target, cardinality, child, _ := condition.Relation()
		operation, _ := condition.Operator()
		if cardinality != ir.RelationToOne || operation != ir.OperatorRelationIs || child == nil {
			return ir.Condition{}, coverage(collector.path, "ordinary side scope requires a positive to-one witness")
		}
		endpoint, present := matcher.registry.RelationEndpoint(golem.ModelID(row.ModelID()), golem.FieldID(field), golem.RelationID(relation))
		if !present || ir.ModelID(endpoint.TargetModelID()) != target {
			return ir.Condition{}, coverage(collector.path, "ordinary side endpoint differs from schema")
		}
		key, err := history.RowKey(matcher.registry, matcher.graph.Provider(), row)
		if err != nil {
			return ir.Condition{}, err
		}
		rows, complete, err := collector.evaluator.CertifiedChildren(key, field, relation)
		if err != nil {
			return ir.Condition{}, err
		}
		if !complete {
			return ir.Condition{}, coverage(collector.path, "ordinary side extent is unknown")
		}
		if len(rows) == 0 {
			collector.allowed = false
			return truth, nil
		}
		if len(rows) != 1 {
			return ir.Condition{}, coverage(collector.path, "ordinary to-one side has multiple witnesses")
		}
		if err := matcher.step(len(via)/16 + 4); err != nil {
			return ir.Condition{}, err
		}
		model := row.ModelID()
		next := via + string(model[:]) + string(field[:]) + string(relation[:]) + string(target[:])
		residual, err := collector.collect(*child, rows[0], offset, next)
		if err != nil || !collector.allowed {
			return truth, err
		}
		childKey, err := history.RowKey(matcher.registry, matcher.graph.Provider(), rows[0])
		if err != nil {
			return ir.Condition{}, err
		}
		matches, err := collector.evaluator.EvaluateCaptured(childKey, residual)
		if err != nil {
			return ir.Condition{}, fmt.Errorf("%w: %v", coverage(collector.path, "ordinary side local evidence is unknown"), err)
		}
		if !matches {
			collector.allowed = false
			return truth, nil
		}
		if _, constant := residual.Constant(); !constant {
			if err := matcher.step(1); err != nil {
				return ir.Condition{}, err
			}
			identity, err := matcher.identityCondition(rows[0])
			if err != nil {
				return ir.Condition{}, err
			}
			guard, err := logical(rows[0].ModelID(), ir.LogicalAnd, []ir.Condition{identity, residual})
			if err != nil {
				return ir.Condition{}, err
			}
			collector.guards = append(collector.guards, guard)
		}
		// Only this graph-certified edge is cut from the current query. Any
		// separately authored parent FK/scalar guard remains in its own term.
		return truth, nil
	default:
		return ir.Condition{}, coverage(collector.path, "ordinary scope context is unsupported")
	}
}

func (collector *historicalScopeCollector) addScope(scope ir.FactScope, row mutationdecode.Row, offset int, via string) error {
	matcher := collector.matcher
	if err := matcher.step(1); err != nil {
		return err
	}
	if scope.ModelID() != row.ModelID() {
		return coverage(collector.path, "ordinary scope belongs to another captured identity")
	}
	if err := matcher.capture.CapturedScope(scope); err != nil {
		return err
	}
	key, err := history.RowKey(matcher.registry, matcher.graph.Provider(), row)
	if err != nil {
		return err
	}
	fingerprint, err := ir.FactScopeFingerprint(scope)
	if err != nil {
		return err
	}
	if err := matcher.step(1 + len(via)/16); err != nil {
		return err
	}
	unique := fmt.Sprint(offset) + "/" + via + key + string(fingerprint[:])
	if collector.seen[unique] {
		return nil
	}
	matches, err := collector.evaluator.EvaluateCaptured(key, scope.Condition())
	if err != nil {
		return fmt.Errorf("%w: %v", coverage(collector.path, "ordinary scope evidence is unknown"), err)
	}
	if !matches {
		collector.allowed = false
		return nil
	}
	collector.seen[unique] = true
	ownerFingerprint, err := ir.FactScopeFingerprint(collector.path.owner.scope)
	if err != nil {
		return err
	}
	if key == collector.ownerKey && fingerprint == ownerFingerprint {
		return nil
	}
	identity, err := historicalPrimaryRow(matcher.registry, row)
	if err != nil {
		return err
	}
	collector.scopes = append(collector.scopes, HistoricalScope{identity: identity, key: key, scope: scope, offset: offset, occurrence: via})
	return nil
}
