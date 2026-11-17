package observation

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/dependency"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

// MaxPaths bounds subscription planning and the number of source-impact
// predicates one notice can require. It is independent of row hydration.
const MaxPaths = 256

type PathLimitError struct{}

func (*PathLimitError) Error() string {
	return fmt.Sprintf("policy observation: more than %d fact paths", MaxPaths)
}

// Hop retains a physical relationship and the local Boolean context in which
// changing that relationship can affect the enclosing expression.
type Hop struct {
	relation    ir.Condition
	guard       ir.Condition
	refinements []guardRefinement
	ordinary    []guardRefinement // authored ordinary context, before physical projection
}

type guardRefinement struct {
	condition ir.Condition
	negative  bool
}

func (hop Hop) Relation() ir.Condition { return hop.relation }
func (hop Hop) Guard() ir.Condition    { return hop.guard }

// Path ends at one observed physical model. Terminal is a local, actor-bound
// witness predicate; unrelated relationships are unknown rather than assumed
// absent. This keeps concurrent dependency revocations observable.
type Path struct {
	role              pathRole
	fact              ir.FactID
	hops              []Hop
	terminal          ir.Condition
	witness           ir.Condition
	terminalAuthority ir.Condition
	dependencies      []ir.FieldID
	owner             factOwner
}

type pathRole uint8

const (
	pathData pathRole = iota
	pathOwnerRow
	pathScope
)

func (path Path) FactID() ir.FactID       { return path.fact }
func (path Path) Hops() []Hop             { return append([]Hop(nil), path.hops...) }
func (path Path) Terminal() ir.Condition  { return path.terminal }
func (path Path) TargetModel() ir.ModelID { return path.terminal.ModelID() }
func (path Path) Dependencies() []ir.FieldID {
	return append([]ir.FieldID(nil), path.dependencies...)
}

// factOwner records the policy boundary once, before physical self-relations
// or nested fact references can make the same model appear again.
type factOwner struct {
	scope    ir.FactScope
	body     ir.Condition
	offset   int
	binding  ir.FactBinding
	bound    bool
	ordinary []guardRefinement
}

type Plan struct {
	historical bool
	root       ir.ModelID
	filter     *ir.Condition
	paths      []Path
	factModels map[ir.ModelID]bool
	owners     []Path
}

func (plan Plan) ModelID() ir.ModelID { return plan.root }
func (plan Plan) Paths() []Path       { return append([]Path(nil), plan.paths...) }

// OwnerPaths retains direct fact-owner activity independently of physical
// body/scope descendants. Operation invalidations have no ordinary owner-row
// stream, so they must subscribe to this inventory explicitly.
func (plan Plan) OwnerPaths() []Path { return append([]Path(nil), plan.owners...) }

// NewHistorical plans physical observation that requires a certified history
// graph. Runtime admission, not this pure planner, selects the writer contract.
func NewHistorical(row ir.Condition, filter *ir.Condition, masks ...ir.Condition) (Plan, error) {
	return newPlan(row, filter, masks, true)
}

// New derives fact paths from already-expanded policy reach. Source filters
// and ordinary relation readability must have been authorized before entry.
// Masks participate independently of row visibility: a hidden change may
// alter a selected field without changing whether its source row is readable.
func New(row ir.Condition, filter *ir.Condition, masks ...ir.Condition) (Plan, error) {
	return newPlan(row, filter, masks, false)
}

func newPlan(row ir.Condition, filter *ir.Condition, masks []ir.Condition, historical bool) (Plan, error) {
	questions := []ir.Condition{row}
	if filter != nil {
		questions = append(questions, *filter)
	}
	questions = append(questions, masks...)
	factModels := map[ir.ModelID]bool{}
	for _, question := range questions {
		if question.ModelID() != row.ModelID() {
			return Plan{}, fmt.Errorf("policy observation: condition model mismatch")
		}
		if err := question.Validate(); err != nil {
			return Plan{}, err
		}
		if ir.HasFacts(question) {
			if err := ir.CheckFactExpansion(question); err != nil {
				return Plan{}, err
			}
			if err := requireScopeEvidence(question, factModels, historical); err != nil {
				return Plan{}, err
			}
		}
		_, err := project(question, opaqueFacts)
		if err != nil {
			return Plan{}, err
		}
	}
	var ownedFilter *ir.Condition
	if filter != nil {
		copy := *filter
		ownedFilter = &copy
	}
	builder := pathBuilder{historical: historical, interesting: map[pathQuestion]bool{}, seen: map[string]bool{}, scopes: map[string]bool{}}
	truth, _ := ir.NewConstant(row.ModelID(), true)
	var walked map[ir.Fingerprint]bool
	if historical {
		walked = make(map[ir.Fingerprint]bool)
	}
	for index, question := range questions {
		if historical {
			// Identical row/mask questions already have their complete source
			// authority in the first walk. Compare complete conditions so that
			// different ordinary occurrences or bound scope arguments remain
			// independent questions.
			fingerprint, err := ir.ConditionFingerprint(question)
			if err != nil {
				return Plan{}, err
			}
			if walked[fingerprint] {
				continue
			}
			walked[fingerprint] = true
		}
		guard := truth
		var ordinary []guardRefinement
		if historical && index != 0 {
			// A filter or mask supplies another dependency question, never
			// another source audience. Retain the authored row scope before
			// projection, and its current ordinary guards even before a cut
			// that could not be captured.
			bounds, err := project(row, opaqueFacts)
			if err != nil {
				return Plan{}, err
			}
			guard = bounds.upper
			ordinary = []guardRefinement{{condition: row}}
		}
		if err := builder.walk(question, guard, nil, nil, ir.FactID{}, factOwner{}, pathData, ordinary); err != nil {
			return Plan{}, err
		}
	}
	return Plan{historical: historical, root: row.ModelID(), filter: ownedFilter, paths: builder.paths, factModels: factModels, owners: builder.owners}, nil
}

func requireScopeEvidence(condition ir.Condition, factModels map[ir.ModelID]bool, historical bool) error {
	pending := []ir.Condition{condition}
	for len(pending) != 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch current.Kind() {
		case ir.ConditionFact:
			factModels[current.ModelID()] = true
			scope, _ := current.FactScope()
			_, bound := scope.Binding()
			if scope.UsesRelations() && (!historical || !bound) {
				return &ir.FactScopeEvidenceError{ModelID: scope.ModelID()}
			}
			_, body, _ := current.Fact()
			pending = append(pending, body)
		case ir.ConditionLogical:
			_, children, _ := current.Logical()
			pending = append(pending, children...)
		case ir.ConditionRelation:
			_, _, _, _, child, _ := current.Relation()
			if child != nil {
				pending = append(pending, *child)
			}
		}
	}
	return nil
}

type pathQuestion struct {
	condition ir.Condition
	physical  bool
	related   bool
}

type pathBuilder struct {
	historical  bool
	scopes      map[string]bool
	paths       []Path
	owners      []Path
	interesting map[pathQuestion]bool
	seen        map[string]bool
}

func (builder *pathBuilder) observes(input ir.Condition, physical, related bool) bool {
	question := pathQuestion{condition: input, physical: physical, related: related}
	if answer, known := builder.interesting[question]; known {
		return answer
	}
	answer := false
	switch input.Kind() {
	case ir.ConditionFact:
		_, body, _ := input.Fact()
		answer = !physical || builder.observes(body, true, related)
	case ir.ConditionRelation:
		_, _, _, _, child, _ := input.Relation()
		answer = physical || child != nil && builder.observes(*child, false, true)
	case ir.ConditionLogical:
		_, children, _ := input.Logical()
		for _, child := range children {
			answer = answer || builder.observes(child, physical, related)
		}
	}
	builder.interesting[question] = answer
	return answer
}

func (builder *pathBuilder) walk(input, guard ir.Condition, refinements []guardRefinement, prefix []Hop, fact ir.FactID, owner factOwner, role pathRole, ordinary []guardRefinement) error {
	physical := fact != (ir.FactID{})
	if !builder.observes(input, physical, len(prefix) != 0) {
		return nil
	}
	switch input.Kind() {
	case ir.ConditionFact:
		id, body, _ := input.Fact()
		scope, _ := input.FactScope()
		_, boundScope := scope.Binding()
		if scope.UsesRelations() && (!builder.historical || !boundScope) {
			return &ir.FactScopeEvidenceError{ModelID: scope.ModelID()}
		}
		// Authority is explicit and exact. Boolean bounds below may widen
		// relevance, but can never widen this policy-owned actor domain.
		if !physical {
			binding, bound := input.FactBinding()
			owner = factOwner{scope: scope, body: body, offset: len(prefix), binding: binding, bound: bound, ordinary: ordinary}
			if builder.historical {
				if err := builder.observeScope(scope, prefix, ordinary); err != nil {
					return err
				}
				// The authority wrapper already contributed its independent
				// scope paths. Its body is not a second physical data fact.
				if input.IsFactScope() {
					return nil
				}
			} else {
				// Scope ownership is retained independently of data paths, including
				// a direct source owner with no relationship or surviving body.
				builder.owners = append(builder.owners, Path{role: pathOwnerRow, fact: id, hops: append([]Hop(nil), prefix...), terminal: scope.Condition(), witness: scope.Condition(), terminalAuthority: scope.Condition(), owner: owner})
			}
			var err error
			guard, err = logical(input.ModelID(), ir.LogicalAnd, []ir.Condition{guard, scope.Condition()})
			if err != nil {
				return err
			}
		}
		if !physical && len(prefix) != 0 && !builder.historical {
			// Changes to the owning row's scope must recover independently of
			// its data facts. Its persisted image supplies exact local scope.
			if err := builder.add(id, prefix, scope.Condition(), scope.Condition(), owner, pathOwnerRow, body); err != nil {
				return err
			}
		}
		return builder.walk(body, guard, refinements, prefix, id, owner, role, nil)
	case ir.ConditionLogical:
		operation, children, _ := input.Logical()
		mode := opaqueFacts
		if physical {
			mode = opaqueRelations
		}
		for index, child := range children {
			if !builder.observes(child, physical, len(prefix) != 0) {
				continue
			}
			context := []ir.Condition{guard}
			refined := append([]guardRefinement(nil), refinements...)
			raw := append([]guardRefinement(nil), ordinary...)
			if operation != ir.LogicalNot {
				for sibling, other := range children {
					if sibling == index {
						continue
					}
					if builder.historical && !physical {
						raw = append(raw, guardRefinement{condition: other, negative: operation != ir.LogicalAnd})
					}
					value, err := project(other, mode)
					if err != nil {
						return err
					}
					needed := value.upper
					if operation == ir.LogicalOr {
						needed, err = negate(value.lower)
						if err != nil {
							return err
						}
					}
					context = append(context, needed)
					if physical {
						refined = append(refined, guardRefinement{condition: other, negative: operation == ir.LogicalOr})
					}
				}
			}
			local, err := logical(input.ModelID(), ir.LogicalAnd, context)
			if err != nil {
				return err
			}
			if err := builder.walk(child, local, refined, prefix, fact, owner, role, raw); err != nil {
				return err
			}
		}
	case ir.ConditionRelation:
		_, _, target, _, child, _ := input.Relation()
		path := append(append([]Hop(nil), prefix...), Hop{relation: input, guard: guard, refinements: refinements, ordinary: ordinary})
		terminal, _ := ir.NewConstant(target, true)
		if child != nil {
			terminal = *child
		}
		if physical {
			if err := builder.add(fact, path, terminal, ir.Condition{}, owner, role); err != nil {
				return err
			}
		}
		if child != nil {
			truth, _ := ir.NewConstant(target, true)
			return builder.walk(*child, truth, nil, path, fact, owner, role, nil)
		}
	}
	return nil
}

func (builder *pathBuilder) add(fact ir.FactID, hops []Hop, input, authority ir.Condition, owner factOwner, role pathRole, watch ...ir.Condition) error {
	dependencies, err := dependency.Collect(append([]ir.Condition{input}, watch...)...)
	if err != nil {
		return err
	}
	terminal := input
	operation, _ := hops[len(hops)-1].relation.Operator()
	if authority.ModelID() == (ir.ModelID{}) && operation == ir.OperatorRelationEvery {
		terminal, err = negate(terminal)
		if err != nil {
			return err
		}
	}
	value, err := project(terminal, opaqueRelations)
	if err != nil {
		return err
	}
	key, err := observationPathKey(fact, hops, authority, owner, role)
	if err != nil {
		return err
	}
	if builder.seen[key] {
		return nil
	}
	if len(builder.paths) >= MaxPaths {
		return &PathLimitError{}
	}
	builder.seen[key] = true
	builder.paths = append(builder.paths, Path{role: role, fact: fact, hops: hops, terminal: value.upper, witness: terminal, terminalAuthority: authority, dependencies: dependencies.Requires(), owner: owner})
	return nil
}

func observationPathKey(fact ir.FactID, hops []Hop, authority ir.Condition, owner factOwner, role pathRole) (string, error) {
	scopeFingerprint, err := ir.FactScopeFingerprint(owner.scope)
	if err != nil {
		return "", err
	}
	key := string(fact[:]) + string(scopeFingerprint[:]) + fmt.Sprintf("/owner:%d/role:%d/ordinary:%d/", owner.offset, role, len(owner.ordinary))
	if authority.ModelID() != (ir.ModelID{}) {
		fingerprint, err := ir.ConditionFingerprint(authority)
		if err != nil {
			return "", err
		}
		key += string(fingerprint[:])
	}
	for _, refinement := range owner.ordinary {
		fingerprint, err := ir.ConditionFingerprint(refinement.condition)
		if err != nil {
			return "", err
		}
		key += string(fingerprint[:]) + fmt.Sprint(refinement.negative)
	}
	for _, hop := range hops {
		key += fmt.Sprintf("/ordinary:%d/", len(hop.ordinary))
		for _, refinement := range hop.ordinary {
			fingerprint, err := ir.ConditionFingerprint(refinement.condition)
			if err != nil {
				return "", err
			}
			key += string(fingerprint[:]) + fmt.Sprint(refinement.negative)
		}
		for _, condition := range []ir.Condition{hop.relation, hop.guard} {
			fingerprint, err := ir.ConditionFingerprint(condition)
			if err != nil {
				return "", err
			}
			key += string(fingerprint[:])
		}
		key += fmt.Sprintf("/%d/", len(hop.refinements))
		for _, refinement := range hop.refinements {
			fingerprint, err := ir.ConditionFingerprint(refinement.condition)
			if err != nil {
				return "", err
			}
			key += string(fingerprint[:]) + fmt.Sprint(refinement.negative)
		}
	}
	return key, nil
}

// observeScope runs once per exact ordinary occurrence, independently of data
// truth, polarity or use. The full scope remains the historical/current fence;
// its relationship paths merely locate changes to physical authority evidence.
func (builder *pathBuilder) observeScope(scope ir.FactScope, prefix []Hop, ordinary []guardRefinement) error {
	// Repeated data definitions on this same owner carry the same authority;
	// their truth must not split independent scope-observation occurrences.
	fingerprint, err := ir.FactScopeFingerprint(scope)
	if err != nil {
		return err
	}
	var context []guardRefinement
	for _, item := range ordinary {
		if other, present := item.condition.FactScope(); present {
			otherFingerprint, err := ir.FactScopeFingerprint(other)
			if err != nil {
				return err
			}
			if otherFingerprint == fingerprint {
				continue
			}
		}
		context = append(context, item)
	}
	ordinary = context
	witness, err := ir.NewFactScopeWitness(scope)
	if err != nil {
		return err
	}
	id, truth, _ := witness.Fact()
	owner := factOwner{scope: scope, body: truth, offset: len(prefix), ordinary: ordinary}
	key, err := observationPathKey(id, prefix, scope.Condition(), owner, pathOwnerRow)
	if err != nil {
		return err
	}
	if builder.scopes[key] {
		return nil
	}
	if len(builder.owners) >= MaxPaths {
		return &PathLimitError{}
	}
	builder.scopes[key] = true
	builder.owners = append(builder.owners, Path{role: pathOwnerRow, fact: id, hops: append([]Hop(nil), prefix...), terminal: scope.Condition(), witness: scope.Condition(), terminalAuthority: scope.Condition(), owner: owner})
	if len(prefix) != 0 {
		if err := builder.add(id, prefix, scope.Condition(), scope.Condition(), owner, pathOwnerRow); err != nil {
			return err
		}
	}
	return builder.walk(scope.Condition(), truth, nil, prefix, id, owner, pathScope, nil)
}
