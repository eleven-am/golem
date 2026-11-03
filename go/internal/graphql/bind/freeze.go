package bind

import (
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// FreezePredicate converts an already-bound P2 condition into the frozen
// public predicate boundary shared by P3/P4 runtime adapters. Scalar parsing,
// enum GraphQL-name resolution, provider/operator validation, and resource
// limits have already run in Binder; the conversion preserves their exact value
// and stable-identity result without reinterpreting public names.
func (b *Binder) FreezePredicate(condition policyir.Condition) (golem.FrozenPredicate, error) {
	if b == nil {
		return golem.FrozenPredicate{}, compileerr.Invariantf("binder is absent")
	}
	frozen, err := policybind.FreezeCondition(golem.ModelID(condition.ModelID()), condition, b.freezeEnumLabels())
	if err != nil {
		return golem.FrozenPredicate{}, compileerr.Ensure(err)
	}
	return frozen, nil
}

func (b *Binder) freezeEnumLabels() policybind.EnumLabels {
	return func(enum policyir.EnumID, member policyir.EnumValueID) (string, bool) {
		label, ok := b.enumLabels[enumMember{enum: enum, value: member}]
		return label, ok
	}
}
