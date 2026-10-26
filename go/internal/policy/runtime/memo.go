package runtime

import (
	"sync"
	"sync/atomic"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

type MemoKind uint8

const (
	MemoRowConstraint MemoKind = iota + 1
	MemoFieldCondition
	MemoReadExpansion
	MemoReadFieldReach
	MemoWriteRowReach
	MemoWriteFieldReach
)

const memoKinds = int(MemoWriteFieldReach)

const memoLimit = 4096

type MemoKey struct {
	Kind   MemoKind
	Model  ir.ModelID
	Action ir.Action
	Field  ir.FieldID
	Start  int
	Depth  int
}

type Source interface {
	GenerationDigest() golem.SchemaDigest
	Provider() ir.Provider
	Policy(ir.ModelID) (ir.Policy, bool)
}

type memo struct {
	owner      *Set
	conditions [memoKinds]memoTable
	grants     memoTable
}

type memoTable struct {
	entries sync.Map
	count   atomic.Int64
}

type memoEntry struct {
	condition ir.Condition
	touched   []ir.ModelID
}

type grantKey struct {
	operation golem.OperationID
	model     ir.ModelID
	action    ir.Action
	field     ir.FieldID
	hasField  bool
}

func (table *memoTable) store(key, value any) {
	if table.count.Add(1) > memoLimit {
		table.count.Add(-1)
		return
	}
	if _, loaded := table.entries.LoadOrStore(key, value); loaded {
		table.count.Add(-1)
	}
}

func (table *memoTable) size() int {
	return int(table.count.Load())
}

type memoSource struct {
	set     *Set
	touched []ir.ModelID
}

func (source *memoSource) GenerationDigest() golem.SchemaDigest { return source.set.GenerationDigest() }

func (source *memoSource) Provider() ir.Provider { return source.set.Provider() }

func (source *memoSource) Policy(model ir.ModelID) (ir.Policy, bool) {
	seen := false
	for _, touched := range source.touched {
		if touched == model {
			seen = true
			break
		}
	}
	if !seen {
		source.touched = append(source.touched, model)
	}
	return source.set.Policy(model)
}

func (set *Set) Memoized(key MemoKey, compute func(Source) (ir.Condition, error)) (ir.Condition, bool, error) {
	if set == nil || set.memo == nil || set.aggregate || key.Kind < MemoRowConstraint || int(key.Kind) > memoKinds {
		return ir.Condition{}, false, nil
	}
	table := &set.memo.conditions[key.Kind-1]
	switch {
	case set == set.memo.owner:
		if value, ok := table.entries.Load(key); ok {
			return value.(memoEntry).condition, true, nil
		}
		source := &memoSource{set: set}
		condition, err := compute(source)
		if err == nil {
			table.store(key, memoEntry{condition: condition, touched: source.touched})
		}
		return condition, true, err
	case set.root == set.memo.owner && set.scope != nil:
		value, ok := table.entries.Load(key)
		if !ok {
			return ir.Condition{}, false, nil
		}
		entry := value.(memoEntry)
		if !set.scope.released.Load() && set.grantsAny(entry.touched) {
			return ir.Condition{}, false, nil
		}
		return entry.condition, true, nil
	default:
		return ir.Condition{}, false, nil
	}
}

func (set *Set) grantsAny(models []ir.ModelID) bool {
	for _, model := range models {
		entry, ok := set.policies[model]
		if !ok {
			return true
		}
		_, operations, ok := entry.resolve()
		if !ok {
			return true
		}
		if _, present := operations[set.operation]; present {
			return true
		}
	}
	return false
}

func (set *Set) grantAddsNothing(scoped, base ir.Policy, action ir.Action, model ir.ModelID, field *ir.FieldID) bool {
	if set.memo == nil || set.root != set.memo.owner {
		return grantAddsNothing(scoped, base, action, model, field)
	}
	key := grantKey{operation: set.operation, model: model, action: action}
	if field != nil {
		key.field, key.hasField = *field, true
	}
	if value, ok := set.memo.grants.entries.Load(key); ok {
		return value.(bool)
	}
	result := grantAddsNothing(scoped, base, action, model, field)
	set.memo.grants.store(key, result)
	return result
}
