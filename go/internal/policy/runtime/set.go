// Package runtime builds execution-scoped, actor-specific policy sets from
// generated bindings and validates them against the active schema/provider.
package runtime

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

type Set struct {
	generation golem.SchemaDigest
	provider   ir.Provider
	policies   map[ir.ModelID]ir.Policy
}

func (set *Set) GenerationDigest() golem.SchemaDigest {
	if set == nil {
		return golem.SchemaDigest{}
	}
	return set.generation
}

func (set *Set) Provider() ir.Provider {
	if set == nil {
		return 0
	}
	return set.provider
}

func (set *Set) Policy(model ir.ModelID) (ir.Policy, bool) {
	if set == nil {
		return ir.Policy{}, false
	}
	policy, ok := set.policies[model]
	return policy, ok
}

type BuildRequest[A any] struct {
	Bindings     golem.ApplicationBindings[A]
	Actor        A
	Registry     *schema.Registry
	Provider     ir.Provider
	Capabilities policysql.CapabilityProof
}

func Build[A any](request BuildRequest[A]) (*Set, error) {
	if request.Registry == nil {
		return nil, fmt.Errorf("P2_RUNTIME_SCHEMA: registry is nil")
	}
	if request.Bindings.GenerationDigest() != request.Registry.GenerationDigest() {
		return nil, fmt.Errorf("P2_RUNTIME_GENERATION: bindings and schema generation digests differ")
	}
	fingerprint := [32]byte(request.Registry.ModelFingerprint())
	if request.Capabilities.Provider() != request.Provider || request.Capabilities.SchemaFingerprint() != fingerprint {
		return nil, fmt.Errorf("P2_RUNTIME_CAPABILITY: runtime proof does not match provider and schema")
	}
	generated, err := golem.BuildGeneratedPolicySet(request.Bindings, request.Actor)
	if err != nil {
		return nil, fmt.Errorf("P2_RUNTIME_FACTORY: %w", err)
	}
	if generated.GenerationDigest() != request.Registry.GenerationDigest() {
		return nil, fmt.Errorf("P2_RUNTIME_GENERATION: generated set and schema generation digests differ")
	}
	resolver := policysql.SchemaResolver(request.Registry)
	providers := resolver.Providers()
	if !providers.Valid() || !providers.Contains(request.Provider) {
		return nil, fmt.Errorf("P2_RUNTIME_PROVIDER: provider is not declared by the schema")
	}
	policies := make(map[ir.ModelID]ir.Policy)
	for index, frozen := range generated.Policies() {
		bound, bindErr := bind.Policy(frozen, request.Registry, providers)
		if bindErr != nil {
			return nil, fmt.Errorf("P2_RUNTIME_BIND: policy %d: %w", index, bindErr)
		}
		normalized, normalizeErr := normalize.Policy(bound)
		if normalizeErr != nil {
			return nil, fmt.Errorf("P2_RUNTIME_NORMALIZE: policy %d: %w", index, normalizeErr)
		}
		if _, duplicate := policies[normalized.ModelID()]; duplicate {
			return nil, fmt.Errorf("P2_RUNTIME_POLICY: duplicate model %x", normalized.ModelID())
		}
		for _, rule := range normalized.Rules() {
			condition, ok := rule.Condition()
			if !ok {
				continue
			}
			for _, requirement := range condition.Requirements() {
				if requirement.Providers().Contains(request.Provider) && (!resolver.Capability(request.Provider, requirement.Capability()) || !request.Capabilities.Has(requirement.Capability())) {
					return nil, fmt.Errorf("P2_RUNTIME_CAPABILITY: policy %d rule %d requires capability %d", index, rule.Position(), requirement.Capability())
				}
			}
			if err := requireConditionAgreement(condition, providers); err != nil {
				return nil, fmt.Errorf("P2_RUNTIME_AGREEMENT: policy %d rule %d: %w", index, rule.Position(), err)
			}
		}
		policies[normalized.ModelID()] = normalized
	}
	return &Set{generation: request.Registry.GenerationDigest(), provider: request.Provider, policies: policies}, nil
}

func requireConditionAgreement(condition ir.Condition, providers ir.ProviderSet) error {
	switch condition.Kind() {
	case ir.ConditionConstant:
		return nil
	case ir.ConditionLogical:
		_, children, _ := condition.Logical()
		for _, child := range children {
			if err := requireConditionAgreement(child, providers); err != nil {
				return err
			}
		}
		return nil
	case ir.ConditionRelation:
		operatorID, _ := condition.Operator()
		if err := operator.RequireAgreement(operatorID, providers); err != nil {
			return err
		}
		_, _, _, _, child, _ := condition.Relation()
		if child != nil {
			return requireConditionAgreement(*child, providers)
		}
		return nil
	default:
		operatorID, _ := condition.Operator()
		return operator.RequireAgreement(operatorID, providers)
	}
}
