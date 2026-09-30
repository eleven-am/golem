// Package runtime builds execution-scoped, actor-specific policy sets from
// generated bindings and validates them against the active schema/provider.
package runtime

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

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
	operations map[golem.OperationID]map[ir.ModelID]ir.Policy
	root       *Set
	scoped     map[ir.ModelID]ir.Policy
	scope      *operationScope
	lease      *Lease
}

// ErrOperationEnded refuses a commit whose write used Within grants after the
// operation that owned those grants ended.
var ErrOperationEnded = errors.New("P5_CUSTOM_OPERATION: the operation that authorised this write has ended")

var operationScopeOrder atomic.Uint64

type operationScope struct {
	mu       sync.Mutex
	released atomic.Bool
	order    uint64
}

func (scope *operationScope) release() {
	scope.mu.Lock()
	scope.released.Store(true)
	scope.mu.Unlock()
}

// Lease records whether one write planned under an operation set used the
// operation's Within grants. Such a write commits only while the operation is
// live, and ending the operation waits for a commit already in progress.
type Lease struct {
	scope *operationScope
	used  atomic.Bool
	ended atomic.Bool
}

func (lease *Lease) Used() bool  { return lease != nil && lease.used.Load() }
func (lease *Lease) Ended() bool { return lease != nil && lease.ended.Load() }

// Commit runs commit, holding the operation live for its duration when the
// write used the operation's grants. After the operation ended it runs
// nothing and returns ErrOperationEnded.
func (lease *Lease) Commit(commit func() error) error {
	return CommitLeases([]*Lease{lease}, commit)
}

// CommitLeases is Commit for the writes of one transaction.
func CommitLeases(leases []*Lease, commit func() error) error {
	var used []*Lease
	seen := map[*operationScope]bool{}
	for _, lease := range leases {
		if !lease.Used() || seen[lease.scope] {
			continue
		}
		seen[lease.scope] = true
		used = append(used, lease)
	}
	sort.Slice(used, func(i, j int) bool { return used[i].scope.order < used[j].scope.order })
	for _, lease := range used {
		lease.scope.mu.Lock()
		defer lease.scope.mu.Unlock()
	}
	for _, lease := range used {
		if lease.scope.released.Load() {
			for _, ended := range leases {
				if ended.Used() {
					ended.ended.Store(true)
				}
			}
			return ErrOperationEnded
		}
	}
	return commit()
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
	if set.scoped != nil && !set.scope.released.Load() {
		if policy, ok := set.scoped[model]; ok {
			if set.lease != nil {
				set.lease.used.Store(true)
			}
			return policy, true
		}
	}
	policy, ok := set.policies[model]
	return policy, ok
}

// Within returns the policy set of one running custom mutation: the caller's
// policies with that operation's Within grants appended. It always starts
// from the caller's own set, so operations never accumulate grants. After
// release, the returned set answers exactly as the caller's own set does.
func (set *Set) Within(operation golem.OperationID) (*Set, func()) {
	if set == nil {
		return nil, func() {}
	}
	root := set
	if set.root != nil {
		root = set.root
	}
	scope := &operationScope{order: operationScopeOrder.Add(1)}
	scoped := &Set{generation: root.generation, provider: root.provider, policies: root.policies, root: root, scoped: root.operations[operation], scope: scope}
	return scoped, scope.release
}

// Base returns the caller's own set, without any operation's grants.
func (set *Set) Base() *Set {
	if set == nil || set.root == nil {
		return set
	}
	return set.root
}

// Lease returns a view of an operation set that records whether a write
// planned through it used the operation's grants. A set carrying no grants
// is returned unchanged with a nil lease.
func (set *Set) Lease() (*Set, *Lease) {
	if set == nil || set.scoped == nil {
		return set, nil
	}
	lease := &Lease{scope: set.scope}
	view := *set
	view.lease = lease
	return &view, lease
}

// WithLease returns a view recording into lease when lease belongs to this
// set's operation, and the set unchanged otherwise.
func (set *Set) WithLease(lease *Lease) *Set {
	if set == nil || lease == nil || set.scoped == nil || set.scope != lease.scope {
		return set
	}
	view := *set
	view.lease = lease
	return &view
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
	policies, err := buildPolicies(request, resolver, providers, generated.Policies())
	if err != nil {
		return nil, err
	}
	operations := make(map[golem.OperationID]map[ir.ModelID]ir.Policy)
	for _, operation := range generated.Operations() {
		scoped, scopedErr := buildPolicies(request, resolver, providers, generated.OperationPolicies(operation))
		if scopedErr != nil {
			return nil, scopedErr
		}
		for model := range scoped {
			if _, present := policies[model]; !present {
				return nil, fmt.Errorf("P2_RUNTIME_POLICY: operation grants for model %x have no caller policy", model)
			}
		}
		operations[operation] = scoped
	}
	return &Set{generation: request.Registry.GenerationDigest(), provider: request.Provider, policies: policies, operations: operations}, nil
}

func buildPolicies[A any](request BuildRequest[A], resolver policysql.Resolver, providers ir.ProviderSet, frozenPolicies []golem.FrozenPolicy) (map[ir.ModelID]ir.Policy, error) {
	policies := make(map[ir.ModelID]ir.Policy)
	for index, frozen := range frozenPolicies {
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
	return policies, nil
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
