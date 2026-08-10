package phase0

// Action is the stable authorization vocabulary shared with the TypeScript
// engine. Transport operation names map into these four meanings.
type Action string

const (
	Read   Action = "read"
	Create Action = "create"
	Update Action = "update"
	Delete Action = "delete"
)

// policyRule preserves declaration priority. TypeScript/CASL evaluates the
// newest applicable rule first; fields is empty for a model-wide rule.
type policyRule[M any] struct {
	predicate Predicate[M]
	inverted  bool
	fields    map[string]struct{}
}

// Rules is the model-specific builder passed to a policy's Define method.
// Its zero value is ready to use and denies every action by default.
//
// Rule declaration order is semantic: the last applicable declaration has the
// highest priority, matching the TypeScript CASL rule chain.
type Rules[M any] struct {
	rules map[Action][]policyRule[M]
}

func (r *Rules[M]) CanRead(predicate Predicate[M])   { r.addRule(Read, predicate, false) }
func (r *Rules[M]) CanCreate(predicate Predicate[M]) { r.addRule(Create, predicate, false) }
func (r *Rules[M]) CanUpdate(predicate Predicate[M]) { r.addRule(Update, predicate, false) }
func (r *Rules[M]) CanDelete(predicate Predicate[M]) { r.addRule(Delete, predicate, false) }

func (r *Rules[M]) CannotRead(predicate Predicate[M])   { r.addRule(Read, predicate, true) }
func (r *Rules[M]) CannotCreate(predicate Predicate[M]) { r.addRule(Create, predicate, true) }
func (r *Rules[M]) CannotUpdate(predicate Predicate[M]) { r.addRule(Update, predicate, true) }
func (r *Rules[M]) CannotDelete(predicate Predicate[M]) { r.addRule(Delete, predicate, true) }

func (r *Rules[M]) CanReadFields(predicate Predicate[M], fields ...ModelField[M]) {
	if len(fields) == 0 {
		return
	}
	r.addRule(Read, predicate, false, fields...)
}

func (r *Rules[M]) CanCreateFields(predicate Predicate[M], fields ...ModelField[M]) {
	if len(fields) == 0 {
		return
	}
	r.addRule(Create, predicate, false, fields...)
}

func (r *Rules[M]) CanUpdateFields(predicate Predicate[M], fields ...ModelField[M]) {
	if len(fields) == 0 {
		return
	}
	r.addRule(Update, predicate, false, fields...)
}

func (r *Rules[M]) CannotReadFields(predicate Predicate[M], fields ...ModelField[M]) {
	if len(fields) == 0 {
		return
	}
	r.addRule(Read, predicate, true, fields...)
}

func (r *Rules[M]) CannotCreateFields(predicate Predicate[M], fields ...ModelField[M]) {
	if len(fields) == 0 {
		return
	}
	r.addRule(Create, predicate, true, fields...)
}

func (r *Rules[M]) CannotUpdateFields(predicate Predicate[M], fields ...ModelField[M]) {
	if len(fields) == 0 {
		return
	}
	r.addRule(Update, predicate, true, fields...)
}

// Effective derives the row constraint for an action. As in CASL, a positive
// field-scoped rule grants the action for its matching rows, while a
// field-scoped denial does not hide the whole row.
func (r *Rules[M]) Effective(action Action) Predicate[M] {
	applicable := make([]policyRule[M], 0, len(r.rules[action]))
	for _, rule := range r.rules[action] {
		if len(rule.fields) == 0 || !rule.inverted {
			applicable = append(applicable, rule)
		}
	}
	return effectiveRuleChain(applicable)
}

// EffectiveField derives the action constraint for one field. Model-wide rules
// and rules naming this field form one priority chain. A model-wide grant
// therefore grants every field unless a higher-priority field rule overrides it.
func (r *Rules[M]) EffectiveField(action Action, field ModelField[M]) Predicate[M] {
	name := field.Descriptor().Name
	applicable := make([]policyRule[M], 0, len(r.rules[action]))
	for _, rule := range r.rules[action] {
		if len(rule.fields) == 0 {
			applicable = append(applicable, rule)
			continue
		}
		if _, matches := rule.fields[name]; matches {
			applicable = append(applicable, rule)
		}
	}
	return effectiveRuleChain(applicable)
}

func (r *Rules[M]) addRule(
	action Action,
	predicate Predicate[M],
	inverted bool,
	fields ...ModelField[M],
) {
	if r.rules == nil {
		r.rules = make(map[Action][]policyRule[M])
	}
	var names map[string]struct{}
	if len(fields) > 0 {
		names = make(map[string]struct{}, len(fields))
		for _, field := range fields {
			names[field.Descriptor().Name] = struct{}{}
		}
	}
	r.rules[action] = append(r.rules[action], policyRule[M]{
		predicate: predicate,
		inverted:  inverted,
		fields:    names,
	})
}

// effectiveRuleChain is the predicate form of CASL's priority evaluation. It
// walks newest-to-oldest. Each conditional denial excludes rows from older
// grants; each conditional grant becomes a branch guarded by all newer denials.
// The first unconditional rule ends the reachable chain.
func effectiveRuleChain[M any](rules []policyRule[M]) Predicate[M] {
	denials := make([]Predicate[M], 0)
	branches := make([]Predicate[M], 0)

	for index := len(rules) - 1; index >= 0; index-- {
		rule := rules[index]
		unconditional := Classify(rule.predicate) == Always
		if rule.inverted {
			if unconditional {
				break
			}
			denials = append(denials, rule.predicate.Not())
			continue
		}

		branch := rule.predicate
		if len(denials) > 0 {
			branch = branch.And(denials...)
		}
		branches = append(branches, branch)
		if unconditional {
			break
		}
	}

	return Normalize(Or(branches...))
}

type FieldAccess string

const (
	Always      FieldAccess = "always"
	Conditional FieldAccess = "conditional"
	Never       FieldAccess = "never"
)

func Classify[M any](predicate Predicate[M]) FieldAccess {
	switch Normalize(predicate).node.Operator {
	case OpAll:
		return Always
	case OpNone:
		return Never
	default:
		return Conditional
	}
}
