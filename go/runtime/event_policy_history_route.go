package runtime

import (
	"context"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/observation"
)

// historicalFactDependencyChanged uses committed identities for every policy
// boundary. Current scope and ordinary guard probes share one SQL snapshot;
// current relationships never invent a replacement historical audience.
func (app *App[P, A]) historicalFactDependencyChanged(ctx context.Context, fresh *Caller[P, A], source golem.ModelID, shared *eventNoticeShared, plan observation.Plan) (bool, error) {
	evidence, err := app.policyHistoryForNotice(ctx, shared)
	if err != nil {
		return false, err
	}
	if golem.ModelID(plan.ModelID()) != source {
		return false, eventPolicyHistoryFailure(ctx)
	}
	fact := shared.envelope.Fact()
	change := observation.HistoricalChange{Model: fact.ModelID()}
	if identity, present := fact.BeforeIdentity(); present {
		change.Before = &identity
	}
	if identity, present := fact.AfterIdentity(); present {
		change.After = &identity
	}
	candidates, err := plan.HistoricalCandidates(ctx, evidence.registry, evidence.value.Graph(), change)
	if err != nil {
		return false, eventPolicyHistoryFailure(ctx)
	}
	owners := map[string]bool{}
	evaluation := &eventHistoryScopeEvaluation{}
	var alternatives []eventHistoryRouteAlternative
	var authorities []eventHistoryRouteAuthority
	for _, candidate := range candidates {
		if candidate.Prefix().ModelID() != plan.ModelID() {
			return false, eventPolicyHistoryFailure(ctx)
		}
		key := candidate.OwnerKey()
		if !owners[key] {
			if len(owners) >= observation.MaxPaths {
				return false, eventPolicyHistoryFailure(ctx)
			}
			owners[key] = true
		}
		proof := eventHistoryScopeProof{identity: candidate.OwnerIdentity(), scope: candidate.Scope()}
		verdict, probe, err := app.prepareEventHistoryScopeProof(ctx, fresh, evidence, key, &proof, evaluation)
		if err != nil {
			return false, err
		}
		authority := eventHistoryRouteAuthority{phase: candidate.Phase(), scopes: []eventHistoryScopeVerdict{verdict}}
		alternative := eventHistoryRouteAlternative{prefix: candidate.Prefix(), guards: candidate.Guards(), scopes: []*deleteProbe{probe}}
		for _, additional := range candidate.AdditionalScopes() {
			verdict, probe, err := app.prepareEventHistoryAdditionalScopeWithEvaluation(ctx, fresh, evidence, additional, evaluation)
			if err != nil {
				return false, err
			}
			authority.scopes = append(authority.scopes, verdict)
			alternative.scopes = append(alternative.scopes, probe)
		}
		eligible, err := authority.historical()
		if err != nil {
			return false, eventPolicyHistoryFailure(ctx)
		}
		if !eligible {
			continue
		}
		// Only the exact recorded source identity can waive its missing current
		// row for cleanup. A same-model owner elsewhere in a chain cannot.
		if candidate.CutOffset() == 0 && golem.ModelID(candidate.CutIdentity().ModelID()) == source {
			cutKey, err := policyhistory.RowKey(evidence.registry, app.provider, candidate.CutIdentity())
			if err != nil {
				return false, eventPolicyHistoryFailure(ctx)
			}
			before, beforeKnown := evidence.value.Graph().FindRoot(policyhistory.Before, cutKey)
			after, afterKnown := evidence.value.Graph().FindRoot(policyhistory.After, cutKey)
			authority.deletedSource = beforeKnown && afterKnown && before != nil && after == nil
		}
		alternatives = append(alternatives, alternative)
		authorities = append(authorities, authority)
	}
	if len(alternatives) == 0 {
		return false, ctx.Err()
	}
	matches, err := app.matchEventHistoryRoutes(ctx, fresh, source, alternatives)
	if err != nil {
		return false, err
	}
	activity, cleanup := false, false
	for index, match := range matches {
		changed, lost, err := authorities[index].outcome(match)
		if err != nil {
			return false, eventPolicyHistoryFailure(ctx)
		}
		activity, cleanup = activity || changed, cleanup || lost
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if activity {
		return true, nil
	}
	if cleanup {
		return false, events.Failure(events.CodeSubscriptionResync)
	}
	return false, nil
}

type eventHistoryRouteAlternative struct {
	prefix policyir.Condition
	guards []policyir.Condition
	scopes []*deleteProbe // primary then additional, retaining this chain's order
}

type eventHistoryRouteMatch struct {
	prefix bool
	guards bool
	scopes []bool
}

type eventHistoryRouteAuthority struct {
	phase         policyhistory.Phase
	scopes        []eventHistoryScopeVerdict
	deletedSource bool
}

func (authority eventHistoryRouteAuthority) historical() (bool, error) {
	if len(authority.scopes) == 0 || authority.phase != policyhistory.Before && authority.phase != policyhistory.After {
		return false, events.Failure(events.CodeSubscriptionRevalidation)
	}
	for _, scope := range authority.scopes {
		if authority.phase == policyhistory.Before && !scope.before || authority.phase == policyhistory.After && !scope.after {
			return false, nil
		}
	}
	return true, nil
}

// Only the same candidate's phase and current answers may be combined. Cleanup
// exempts a currently false fence only when this causation certified its loss.
func (authority eventHistoryRouteAuthority) outcome(match eventHistoryRouteMatch) (bool, bool, error) {
	eligible, err := authority.historical()
	if err != nil || len(authority.scopes) != len(match.scopes) {
		return false, false, events.Failure(events.CodeSubscriptionRevalidation)
	}
	if !eligible {
		return false, false, nil
	}
	before, after, current, retained := true, true, true, true
	for index, scope := range authority.scopes {
		before, after, current = before && scope.before, after && scope.after, current && match.scopes[index]
		// An unchanged scope revoked by later B cannot borrow A's certified
		// loss of another scope, even for unattributed cleanup.
		retained = retained && (match.scopes[index] || scope.before && !scope.after)
	}
	if current {
		return match.prefix && match.guards, false, nil
	}
	cleanup := before && !after && retained && match.guards && (match.prefix || authority.deletedSource)
	return false, cleanup, nil
}

// Every alternative is one recorded chain. Probe deduplication saves SQL work
// but never merges its scope/guard conjunction with another chain's answers.
func (app *App[P, A]) matchEventHistoryRoutes(ctx context.Context, fresh *Caller[P, A], source golem.ModelID, alternatives []eventHistoryRouteAlternative) ([]eventHistoryRouteMatch, error) {
	if len(alternatives) == 0 {
		return nil, eventPolicyHistoryFailure(ctx)
	}
	var probes []*deleteProbe
	byCondition := map[string]*deleteProbe{}
	keyBytes := 0
	add := func(probe *deleteProbe) (*deleteProbe, error) {
		if probe == nil || probe.target == (golem.ModelID{}) || probe.condition.ModelID() != policyir.ModelID(probe.target) || policyir.HasFacts(probe.condition) {
			return nil, events.Failure(events.CodeSubscriptionRevalidation)
		}
		key, err := deleteProbeKey(*probe)
		if err != nil {
			return nil, err
		}
		if existing := byCondition[key]; existing != nil {
			return existing, nil
		}
		limits := app.readLimits.plan
		if limits.MaxStatementAliases > 0 && len(probes) >= limits.MaxStatementAliases || limits.MaxStatementBytes > 0 && len(key) > limits.MaxStatementBytes-keyBytes {
			return nil, events.Failure(events.CodeSubscriptionRevalidation)
		}
		keyBytes += len(key)
		// Never reuse answered flags from a previous evaluation, including
		// when a caller supplies the same prepared probe again after a write.
		owned := &deleteProbe{target: probe.target, condition: probe.condition}
		byCondition[key] = owned
		probes = append(probes, owned)
		return owned, nil
	}
	type probeChain struct {
		prefix *deleteProbe
		guards []*deleteProbe
		scopes []*deleteProbe
	}
	chains := make([]probeChain, 0, len(alternatives))
	for _, alternative := range alternatives {
		if alternative.prefix.ModelID() != policyir.ModelID(source) || len(alternative.scopes) == 0 {
			return nil, eventPolicyHistoryFailure(ctx)
		}
		prefix, err := add(&deleteProbe{target: source, condition: alternative.prefix})
		if err != nil {
			return nil, eventPolicyHistoryFailure(ctx)
		}
		chain := probeChain{prefix: prefix}
		for _, guard := range alternative.guards {
			probe, err := add(&deleteProbe{target: golem.ModelID(guard.ModelID()), condition: guard})
			if err != nil {
				return nil, eventPolicyHistoryFailure(ctx)
			}
			chain.guards = append(chain.guards, probe)
		}
		for _, scope := range alternative.scopes {
			probe, err := add(scope)
			if err != nil {
				return nil, eventPolicyHistoryFailure(ctx)
			}
			chain.scopes = append(chain.scopes, probe)
		}
		chains = append(chains, chain)
	}
	if err := app.runDeleteProbes(ctx, fresh, probes); err != nil {
		return nil, eventPolicyHistoryFailure(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, probe := range probes {
		if !probe.answered {
			return nil, eventPolicyHistoryFailure(ctx)
		}
	}
	result := make([]eventHistoryRouteMatch, len(chains))
	for index, chain := range chains {
		match := eventHistoryRouteMatch{prefix: chain.prefix.exists, guards: true, scopes: make([]bool, len(chain.scopes))}
		for _, guard := range chain.guards {
			match.guards = match.guards && guard.exists
		}
		for index, scope := range chain.scopes {
			match.scopes[index] = scope.exists
		}
		result[index] = match
	}
	return result, nil
}
