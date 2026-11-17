package observation

import (
	"context"
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policycapture "github.com/eleven-am/golem/go/internal/policy/capture"
	"github.com/eleven-am/golem/go/internal/policy/history"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

// HistoricalChange identifies at most two exact primary identities from the
// validated notice. A model name alone must never borrow an unchanged sibling
// row from the same whole-causation graph.
type HistoricalChange struct {
	Model         ir.ModelID
	Before, After *mutationdecode.Identity
}

// HistoricalCandidate is a conservative, same-phase impact proof, not current
// authorization or evidence that the source was historically visible. Runtime
// must apply its fresh scope fence to this exact owner before probing Prefix.
type HistoricalCandidate struct {
	prefix     ir.Condition
	guards     []ir.Condition
	additional []HistoricalScope
	owner, cut mutationdecode.Row
	ownerKey   string
	scope      ir.FactScope
	phase      history.Phase
	changed    mutationdecode.Identity
	fact       ir.FactID
	scopeOnly  bool
	cutOffset  int
}

func (candidate HistoricalCandidate) Prefix() ir.Condition { return candidate.prefix }

// Guards are conjunctive current questions for this candidate's recorded chain.
// Different candidates are alternatives; their guards must never be mixed.
func (candidate HistoricalCandidate) Guards() []ir.Condition {
	return append([]ir.Condition(nil), candidate.guards...)
}
func (candidate HistoricalCandidate) OwnerIdentity() mutationdecode.Row { return candidate.owner }
func (candidate HistoricalCandidate) OwnerKey() string                  { return candidate.ownerKey }
func (candidate HistoricalCandidate) Scope() ir.FactScope               { return candidate.scope }
func (candidate HistoricalCandidate) Phase() history.Phase              { return candidate.phase }
func (candidate HistoricalCandidate) CutIdentity() mutationdecode.Row   { return candidate.cut }
func (candidate HistoricalCandidate) ChangedIdentity() mutationdecode.Identity {
	return candidate.changed
}
func (candidate HistoricalCandidate) FactID() ir.FactID { return candidate.fact }
func (candidate HistoricalCandidate) ScopeOnly() bool   { return candidate.scopeOnly }

// CutOffset is the number of ordinary hops left current. A nonzero prefix
// requires the ordinary-prefix-loss backstop before runtime activation.
func (candidate HistoricalCandidate) CutOffset() int { return candidate.cutOffset }

type HistoricalCoverageError = policycapture.Error

type historicalMatcher struct {
	ctx       context.Context
	registry  *schema.Registry
	graph     history.Graph
	resolver  policysql.Resolver
	providers ir.ProviderSet
	nodes     int
	capture   *policycapture.Validator
}

func (matcher *historicalMatcher) step(count int) error {
	if err := matcher.ctx.Err(); err != nil {
		return err
	}
	if count < 0 || count > history.MaxNodes-matcher.nodes {
		return &history.LimitError{Limit: golem.LimitPolicyHistoryNodes, Max: history.MaxNodes}
	}
	matcher.nodes += count
	return nil
}
func coverage(path Path, detail string) error {
	return &HistoricalCoverageError{Model: path.owner.scope.ModelID(), Fact: path.fact, Detail: detail}
}

// HistoricalCandidates consumes a freshly bound observation plan and immutable
// producer evidence. It never reads current relationships or current row truth.
// Exact authority is evaluated separately from conservative data relevance.
// No partial result is returned when any required proof is unavailable.
func (plan Plan) HistoricalCandidates(ctx context.Context, registry *schema.Registry, graph history.Graph, change HistoricalChange) ([]HistoricalCandidate, error) {
	if ctx == nil || registry == nil || plan.root == (ir.ModelID{}) || change.Model == (ir.ModelID{}) {
		return nil, fmt.Errorf("policy observation: context, bound plan, schema and exact change are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if graph.Generation() != registry.GenerationDigest() || graph.ProjectionFingerprint() != registry.PolicyCaptureFingerprint() {
		return nil, fmt.Errorf("policy observation: historical generation or capture contract differs")
	}
	matcher := historicalMatcher{ctx: ctx, registry: registry, graph: graph, resolver: policysql.SchemaResolver(registry)}
	matcher.providers = matcher.resolver.Providers()
	matcher.capture = policycapture.NewValidator(registry, matcher.step)
	changes := map[history.Phase]mutationdecode.Identity{}
	keys := map[history.Phase]string{}
	for _, phase := range []history.Phase{history.Before, history.After} {
		identity := change.Before
		if phase == history.After {
			identity = change.After
		}
		if identity == nil {
			continue
		}
		row, err := historicalChangeIdentity(registry, change.Model, *identity)
		if err != nil {
			return nil, err
		}
		key, err := history.RowKey(registry, graph.Provider(), row)
		if err != nil {
			return nil, err
		}
		changes[phase] = *identity
		keys[phase] = key
	}
	if len(changes) == 0 {
		return nil, fmt.Errorf("policy observation: at least one exact changed primary identity is required")
	}
	if len(plan.paths) > MaxPaths {
		return nil, &PathLimitError{}
	}
	roots, rootKeys := graph.Roots(), graph.RootKeys()
	paths := make([]Path, 0, len(plan.paths)+len(plan.owners))
	paths = append(paths, plan.paths...)
	paths = append(paths, plan.owners...)
	var result []HistoricalCandidate
	seen := map[string]bool{}
	for _, phase := range []history.Phase{history.Before, history.After} {
		identity, present := changes[phase]
		if !present {
			continue
		}
		evaluator, err := graph.NewEvaluator(ctx, registry, matcher.providers, phase)
		if err != nil {
			return nil, err
		}
		changed, recorded := graph.FindRow(phase, keys[phase])
		for _, path := range paths {
			if err := matcher.step(1); err != nil {
				return nil, err
			}
			if path.TargetModel() != change.Model {
				continue
			}
			if err := matcher.provenance(path); err != nil {
				return nil, err
			}
			if !recorded {
				continue
			} // No recorded owner route reaches this identity.
			for index, root := range roots {
				if err := matcher.step(1); err != nil {
					return nil, err
				}
				if root.Identity.ModelID() != path.owner.scope.ModelID() {
					continue
				}
				allowed, err := evaluator.EvaluateRoot(rootKeys[index], path.owner.scope.Condition())
				if err != nil {
					return nil, fmt.Errorf("%w: %v", coverage(path, "exact owning authority is unknown"), err)
				}
				if !allowed {
					continue
				}
				if path.role == pathOwnerRow && rootKeys[index] != keys[phase] {
					continue
				}
				witness, err := localWitness(path.witness, registry, matcher.resolver, graph.Provider())
				if err != nil {
					return nil, err
				}
				relevant, err := evaluator.EvaluateCaptured(keys[phase], witness.upper)
				if err != nil {
					return nil, fmt.Errorf("%w: %v", coverage(path, "terminal witness is not captured"), err)
				}
				if !relevant {
					continue
				}
				ownerRow, known := graph.FindRoot(phase, rootKeys[index])
				if !known || ownerRow == nil {
					return nil, coverage(path, "allowed owner row is absent")
				}
				cuts, offset, err := matcher.cuts(path, *ownerRow, evaluator)
				if err != nil {
					return nil, err
				}
				for _, chain := range cuts {
					if err := matcher.step(len(chain.rows)); err != nil {
						return nil, err
					}
					currentPath := path
					var additional []HistoricalScope
					var sideGuards []ir.Condition
					if plan.historical {
						var allowed bool
						currentPath, additional, sideGuards, allowed, err = matcher.ordinaryScopeRequirements(path, offset, chain.rows, evaluator)
						if err != nil {
							return nil, err
						}
						if !allowed {
							continue
						}
					}
					cut := chain.rows[0]
					condition, err := matcher.suffix(path, offset, changed, chain.rows)
					if err != nil {
						return nil, err
					}
					cutKey, err := history.RowKey(registry, graph.Provider(), cut)
					if err != nil {
						return nil, err
					}
					matches, err := evaluator.EvaluateCaptured(cutKey, condition)
					if err != nil {
						return nil, fmt.Errorf("%w: %v", coverage(path, "cut-to-owner suffix or guard is unknown"), err)
					}
					if !matches {
						continue
					}
					cutIdentity, err := historicalPrimaryRow(registry, cut)
					if err != nil {
						return nil, err
					}
					prefix, err := matcher.prefix(plan, currentPath, offset, cutIdentity)
					if err != nil {
						return nil, err
					}
					guards, err := matcher.ordinaryGuards(plan, currentPath, offset, chain.rows)
					if err != nil {
						return nil, err
					}
					guards = append(guards, sideGuards...)
					fingerprint, err := ir.ConditionFingerprint(prefix)
					if err != nil {
						return nil, err
					}
					key := string([]byte{byte(phase)}) + rootKeys[index] + chain.key + keys[phase] + string(path.fact[:]) + fmt.Sprint(path.role) + string(fingerprint[:])
					for _, guard := range guards {
						if err := matcher.step(1); err != nil {
							return nil, err
						}
						fingerprint, err := ir.ConditionFingerprint(guard)
						if err != nil {
							return nil, err
						}
						key += string(fingerprint[:])
					}
					for _, scope := range additional {
						if err := matcher.step(1 + len(scope.occurrence)/16); err != nil {
							return nil, err
						}
						fingerprint, err := ir.FactScopeFingerprint(scope.scope)
						if err != nil {
							return nil, err
						}
						key += fmt.Sprintf("/scope:%d/%d/", scope.offset, len(scope.occurrence)) + scope.occurrence + scope.key + string(fingerprint[:])
					}
					if seen[key] {
						continue
					}
					seen[key] = true
					result = append(result, HistoricalCandidate{prefix: prefix, guards: guards, additional: additional, owner: root.Identity, ownerKey: rootKeys[index], scope: path.owner.scope, phase: phase, cut: cutIdentity, changed: identity, fact: path.fact, scopeOnly: path.role != pathData, cutOffset: offset})
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func historicalChangeIdentity(registry *schema.Registry, model ir.ModelID, identity mutationdecode.Identity) (mutationdecode.Row, error) {
	if err := mutationdecode.ValidateIdentity(registry, model, identity); err != nil {
		return mutationdecode.Row{}, err
	}
	var cells []mutationdecode.Cell
	for _, component := range identity.Components() {
		value, present := component.PolicyValue()
		if !present || component.IsNull() {
			return mutationdecode.Row{}, fmt.Errorf("policy observation: changed primary identity is NULL")
		}
		cells = append(cells, mutationdecode.Value(component.FieldID(), value))
	}
	row, err := mutationdecode.NewRow(registry, model, cells)
	if err != nil {
		return mutationdecode.Row{}, err
	}
	primary, err := mutationdecode.PrimaryIdentity(registry, row)
	if err != nil {
		return mutationdecode.Row{}, err
	}
	if primary.KeyID() != identity.KeyID() || len(primary.Components()) != len(identity.Components()) {
		return mutationdecode.Row{}, fmt.Errorf("policy observation: change must name the exact primary identity")
	}
	return row, nil
}

func historicalPrimaryRow(registry *schema.Registry, row mutationdecode.Row) (mutationdecode.Row, error) {
	identity, err := mutationdecode.PrimaryIdentity(registry, row)
	if err != nil {
		return mutationdecode.Row{}, err
	}
	return historicalChangeIdentity(registry, row.ModelID(), identity)
}

func (matcher *historicalMatcher) provenance(path Path) error {
	scope := path.owner.scope
	if path.owner.offset < 0 || path.owner.offset > len(path.hops) || scope.ModelID() == (ir.ModelID{}) {
		return coverage(path, "owning boundary metadata is absent")
	}
	if len(path.hops) == 0 && (path.owner.offset != 0 || path.terminalAuthority.ModelID() == (ir.ModelID{})) {
		return coverage(path, "relation path is absent")
	}
	var binding *ir.FactBinding
	if path.owner.bound {
		value := path.owner.binding
		binding = &value
	}
	if err := matcher.capture.Owner(scope, path.owner.body, path.fact, binding); err != nil {
		return err
	}
	if len(path.hops) == 0 {
		return nil
	}
	current := path.hops[0].relation.ModelID()
	for index, hop := range path.hops {
		field, relation, target, _, _, valid := hop.relation.Relation()
		if !valid || hop.relation.ModelID() != current {
			return coverage(path, "path models do not form one correlated chain")
		}
		endpoint, found := matcher.registry.RelationEndpoint(golem.ModelID(current), golem.FieldID(field), golem.RelationID(relation))
		if !found || ir.ModelID(endpoint.TargetModelID()) != target {
			return coverage(path, "path relation differs from the schema")
		}
		if index == path.owner.offset && current != scope.ModelID() {
			return coverage(path, "fact-entry offset names another model")
		}
		current = target
	}
	if path.owner.offset == len(path.hops) && current != scope.ModelID() {
		return coverage(path, "terminal owner model differs")
	}
	return nil
}

// Reverse only through certified complete extents. Unknown reverse coverage
// stops the cut here; it never turns incidental graph rows into an exhaustive
// set of ordinary source roots. The remaining prefix is evaluated current.
type historicalChain struct {
	rows []mutationdecode.Row // ordered cut through exact owner, including both
	key  string               // concatenated fixed-size RowKey fingerprints in the same order
}

func (matcher *historicalMatcher) cuts(path Path, owner mutationdecode.Row, evaluator *history.Evaluator) ([]historicalChain, int, error) {
	if err := matcher.step(1); err != nil {
		return nil, 0, err
	}
	ownerKey, err := history.RowKey(matcher.registry, matcher.graph.Provider(), owner)
	if err != nil {
		return nil, 0, err
	}
	current := []historicalChain{{rows: []mutationdecode.Row{owner}, key: ownerKey}}
	offset := path.owner.offset
	for offset > 0 {
		hop := path.hops[offset-1]
		field, relation, target, _, _, _ := hop.relation.Relation()
		forward, found := matcher.registry.RelationEndpoint(golem.ModelID(hop.relation.ModelID()), golem.FieldID(field), golem.RelationID(relation))
		if !found {
			return nil, 0, coverage(path, "ordinary prefix endpoint is absent")
		}
		inverse, found, err := matcher.inverse(forward)
		if err != nil {
			return nil, 0, err
		}
		if !found {
			break
		}
		next := map[string]historicalChain{}
		complete := true
		for _, chain := range current {
			row := chain.rows[0]
			if err := matcher.step(1); err != nil {
				return nil, 0, err
			}
			if row.ModelID() != target {
				return nil, 0, coverage(path, "reverse cut reached another model")
			}
			key, err := history.RowKey(matcher.registry, matcher.graph.Provider(), row)
			if err != nil {
				return nil, 0, err
			}
			rows, known, err := evaluator.CertifiedChildren(key, ir.FieldID(inverse.FieldID()), ir.RelationID(inverse.RelationID()))
			if err != nil {
				return nil, 0, err
			}
			if !known {
				complete = false
				break
			}
			for _, parent := range rows {
				if err := matcher.step(1); err != nil {
					return nil, 0, err
				}
				key, err := history.RowKey(matcher.registry, matcher.graph.Provider(), parent)
				if err != nil {
					return nil, 0, err
				}
				// Charge the whole immutable chain copy before allocating it.
				if err := matcher.step(1 + len(chain.rows)); err != nil {
					return nil, 0, err
				}
				chainKey := key + chain.key
				if _, duplicate := next[chainKey]; !duplicate {
					rows := make([]mutationdecode.Row, 1+len(chain.rows))
					rows[0] = parent
					copy(rows[1:], chain.rows)
					next[chainKey] = historicalChain{rows: rows, key: chainKey}
				}
			}
		}
		if !complete {
			break
		}
		if err := matcher.step(len(next)); err != nil {
			return nil, 0, err
		}
		keys := make([]string, 0, len(next))
		for key := range next {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		current = nil
		for _, key := range keys {
			current = append(current, next[key])
		}
		offset--
	}
	return current, offset, nil
}

func (matcher *historicalMatcher) inverse(forward schema.RelationEndpoint) (schema.RelationEndpoint, bool, error) {
	model, found := matcher.registry.Model(forward.TargetModelID())
	if !found {
		return schema.RelationEndpoint{}, false, fmt.Errorf("policy observation: inverse model is absent")
	}
	for _, field := range model.Fields() {
		if err := matcher.step(1); err != nil {
			return schema.RelationEndpoint{}, false, err
		}
		endpoint, found := matcher.registry.RelationEndpoint(model.ID(), field, forward.RelationID())
		if !found || endpoint.TargetModelID() != forward.ModelID() || endpoint.FieldID() == forward.FieldID() && endpoint.ModelID() == forward.ModelID() {
			continue
		}
		left, right := forward.Correlation(), endpoint.Correlation()
		if len(left) != len(right) {
			continue
		}
		matches := true
		for index, pair := range left {
			if pair.ParentFieldID() != right[index].ChildFieldID() || pair.ChildFieldID() != right[index].ParentFieldID() {
				matches = false
				break
			}
		}
		if matches {
			return endpoint, true, nil
		}
	}
	return schema.RelationEndpoint{}, false, nil
}

func (matcher *historicalMatcher) suffix(path Path, offset int, changed mutationdecode.Row, chain []mutationdecode.Row) (ir.Condition, error) {
	if len(chain) != path.owner.offset-offset+1 {
		return ir.Condition{}, coverage(path, "recorded ordinary chain has the wrong length")
	}
	if err := matcher.step(len(path.hops) - offset + len(chain)); err != nil {
		return ir.Condition{}, err
	}
	owner := chain[len(chain)-1]
	anchor, err := matcher.identityCondition(owner)
	if err != nil {
		return ir.Condition{}, err
	}
	if offset == len(path.hops) {
		return anchor, nil
	}
	copy := path
	copy.hops = append([]Hop(nil), path.hops[offset:]...)
	// Pin every ordinary occurrence, not merely the cut and owner. Otherwise
	// an existential sibling could certify a different chain's current guards.
	for index, row := range chain {
		if index >= len(copy.hops) {
			break // a terminal owner is already the exact changed identity
		}
		identity, err := matcher.identityCondition(row)
		if err != nil {
			return ir.Condition{}, err
		}
		copy.hops[index].guard, err = logical(row.ModelID(), ir.LogicalAnd, []ir.Condition{copy.hops[index].guard, identity})
		if err != nil {
			return ir.Condition{}, err
		}
	}
	return copy.bind(changed, matcher.registry, matcher.graph.Provider(), matcher.resolver)
}

func (matcher *historicalMatcher) prefix(plan Plan, path Path, offset int, cut mutationdecode.Row) (ir.Condition, error) {
	// Projected guards cannot certify that a preceding policy had no scope:
	// retain that ownership information from the original expanded questions.
	model := plan.root
	for index := 0; index < path.owner.offset; index++ {
		if !plan.historical && plan.factModels[model] {
			return ir.Condition{}, coverage(path, "current prefix crosses another fact-owning policy")
		}
		_, _, model, _, _, _ = path.hops[index].relation.Relation()
	}
	if !plan.historical && offset == 0 && path.owner.offset > 0 && plan.factModels[plan.root] {
		return ir.Condition{}, coverage(path, "current source guard belongs to another fact-owning policy")
	}
	// Check each ordinary guard before composing the live prefix. Projection
	// can erase a sibling Fact while retaining a relation into its owner. The
	// selected path's model chain alone cannot detect that hidden boundary.
	// Include the cut's guard only when it precedes the separately fenced owner;
	// the terminal owner's stable identity is intentionally allowed in Prefix.
	for index := 0; index < path.owner.offset && index <= offset; index++ {
		guard, err := path.hops[index].bindGuard(matcher.registry, matcher.graph.Provider(), matcher.resolver)
		if err != nil {
			return ir.Condition{}, err
		}
		if err := matcher.currentGuardBoundary(plan, path, guard); err != nil {
			return ir.Condition{}, err
		}
	}
	var condition ir.Condition
	var err error
	if offset == 0 {
		condition, err = matcher.identityCondition(cut)
		if err == nil && path.owner.offset > 0 {
			guard, guardErr := path.hops[0].bindGuard(matcher.registry, matcher.graph.Provider(), matcher.resolver)
			if guardErr != nil {
				return ir.Condition{}, guardErr
			}
			condition, err = logical(plan.root, ir.LogicalAnd, []ir.Condition{condition, guard})
		}
	} else {
		copy := path
		copy.hops = append([]Hop(nil), path.hops[:offset]...)
		// Direct PK correlations preserve deleted cuts without joining their
		// current row. Named-unique endpoints instead keep the positive live
		// prefix ending at the stable cut PK, never a recorded mutable key.
		last := copy.hops[len(copy.hops)-1]
		field, relation, _, _, _, _ := last.relation.Relation()
		endpoint, found := matcher.registry.RelationEndpoint(golem.ModelID(last.relation.ModelID()), golem.FieldID(field), golem.RelationID(relation))
		if !found {
			return ir.Condition{}, coverage(path, "current cut endpoint is absent")
		}
		direct := len(endpoint.Correlation()) == len(cut.Cells())
		for _, pair := range endpoint.Correlation() {
			if _, present := cut.Cell(ir.FieldID(pair.ChildFieldID())); !present {
				direct = false
				break
			}
		}
		var cutGuard ir.Condition
		guardedCut := false
		if offset < path.owner.offset {
			cutGuard, err = path.hops[offset].bindGuard(matcher.registry, matcher.graph.Provider(), matcher.resolver)
			if err != nil {
				return ir.Condition{}, err
			}
			if ir.HasFacts(cutGuard) {
				return ir.Condition{}, coverage(path, "current cut guard retains a fact or scope boundary")
			}
			truth, constant := cutGuard.Constant()
			guardedCut = !constant || !truth
		}
		// Retain a mutable ordinary cut guard now as well as historically.
		// FK-only binding would bypass that guard, so keep the positive hop
		// to the stable cut PK plus guard even for a direct primary-key FK.
		direct = direct && !guardedCut
		if direct {
			condition, err = copy.bind(cut, matcher.registry, matcher.graph.Provider(), matcher.resolver)
		} else {
			condition, err = matcher.identityCondition(cut)
			if err == nil && guardedCut {
				condition, err = logical(cut.ModelID(), ir.LogicalAnd, []ir.Condition{condition, cutGuard})
			}
			for index := len(copy.hops) - 1; index >= 0 && err == nil; index-- {
				condition, err = copy.hops[index].bindParent(condition, matcher.registry, matcher.graph.Provider(), matcher.resolver)
			}
		}

	}
	if err != nil {
		return ir.Condition{}, fmt.Errorf("%w: %v", coverage(path, "current prefix cannot bind the stable cut primary identity"), err)
	}
	filter, err := plan.sourceRelevance()
	if err != nil {
		return ir.Condition{}, err
	}
	condition, err = logical(plan.root, ir.LogicalAnd, []ir.Condition{condition, filter})
	if err != nil {
		return ir.Condition{}, err
	}
	if ir.HasFacts(condition) {
		return ir.Condition{}, coverage(path, "current prefix retains a fact or scope boundary")
	}
	return condition, nil
}

// ordinaryGuards names current guard checks on the exact recorded intermediate
// identities. These predicates never rebuild the ordinary continuation to owner.
func (matcher *historicalMatcher) ordinaryGuards(plan Plan, path Path, offset int, chain []mutationdecode.Row) ([]ir.Condition, error) {
	var result []ir.Condition
	for index := offset + 1; index < path.owner.offset; index++ {
		if err := matcher.step(1); err != nil {
			return nil, err
		}
		guard, err := path.hops[index].bindGuard(matcher.registry, matcher.graph.Provider(), matcher.resolver)
		if err != nil {
			return nil, err
		}
		if err := matcher.currentGuardBoundary(plan, path, guard); err != nil {
			return nil, err
		}
		if _, constant := guard.Constant(); constant {
			continue // false already failed exact historical certification
		}
		identity, err := matcher.identityCondition(chain[index-offset])
		if err != nil {
			return nil, err
		}
		question, err := logical(guard.ModelID(), ir.LogicalAnd, []ir.Condition{identity, guard})
		if err != nil {
			return nil, err
		}
		result = append(result, question)
	}
	return result, nil
}

func (matcher *historicalMatcher) currentGuardBoundary(plan Plan, path Path, condition ir.Condition) error {
	if err := matcher.step(1); err != nil {
		return err
	}
	if condition.Kind() == ir.ConditionFact || !plan.historical && plan.factModels[condition.ModelID()] {
		return coverage(path, "current intermediate guard crosses a fact-owning policy")
	}
	switch condition.Kind() {
	case ir.ConditionLogical:
		_, children, _ := condition.Logical()
		for _, child := range children {
			if err := matcher.currentGuardBoundary(plan, path, child); err != nil {
				return err
			}
		}
	case ir.ConditionRelation:
		_, _, target, _, child, _ := condition.Relation()
		if !plan.historical && plan.factModels[target] {
			return coverage(path, "current intermediate guard enters a fact-owning policy")
		}
		if child != nil {
			return matcher.currentGuardBoundary(plan, path, *child)
		}
	}
	return nil
}

func (matcher *historicalMatcher) identityCondition(row mutationdecode.Row) (ir.Condition, error) {
	identity, err := historicalPrimaryRow(matcher.registry, row)
	if err != nil {
		return ir.Condition{}, err
	}
	var conditions []ir.Condition
	for _, cell := range identity.Cells() {
		descriptor, found := matcher.resolver.Field(matcher.graph.Provider(), row.ModelID(), cell.FieldID())
		if !found {
			return ir.Condition{}, fmt.Errorf("policy observation: primary scalar descriptor is absent")
		}
		value, _ := cell.PolicyValue()
		operand, err := ir.OneOperand(value)
		if err != nil {
			return ir.Condition{}, err
		}
		requirements, err := operator.ValidateShape(ir.OperatorEqual, operator.Shape{Node: ir.ConditionScalar, FieldType: descriptor.Type, Operand: operand, Mode: ir.ComparisonSensitive, Providers: matcher.providers})
		if err != nil {
			return ir.Condition{}, err
		}
		condition, err := ir.NewScalar(row.ModelID(), cell.FieldID(), descriptor.Type, ir.OperatorEqual, ir.ComparisonSensitive, operand, requirements)
		if err != nil {
			return ir.Condition{}, err
		}
		conditions = append(conditions, condition)
	}
	return logical(row.ModelID(), ir.LogicalAnd, conditions)
}
