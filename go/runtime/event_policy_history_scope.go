package runtime

import (
	"context"
	"reflect"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/observation"
)

// Scope truth is actor-bound and deliberately never retained with immutable
// notice evidence. The current fence is read again on every invocation.
type eventHistoryScopeVerdict struct {
	before, after, current bool
}

// Work owners live only for one delivery and retain no authorization answers.
type eventHistoryScopeEvaluation struct {
	before, after *policyhistory.Evaluator
	digest        golem.SchemaDigest
}

func (app *App[P, A]) eventHistoryScope(ctx context.Context, fresh *Caller[P, A], evidence *eventPolicyHistory, ownerKey string) (eventHistoryScopeVerdict, error) {
	verdict, probe, err := app.prepareEventHistoryScope(ctx, fresh, evidence, ownerKey)
	if err != nil {
		return eventHistoryScopeVerdict{}, err
	}
	if err := app.runDeleteProbes(ctx, fresh, []*deleteProbe{probe}); err != nil {
		return eventHistoryScopeVerdict{}, eventPolicyHistoryFailure(ctx)
	}
	if err := ctx.Err(); err != nil {
		return eventHistoryScopeVerdict{}, err
	}
	if !probe.answered {
		return eventHistoryScopeVerdict{}, eventPolicyHistoryFailure(ctx)
	}
	verdict.current = probe.exists
	return verdict, nil
}

// prepareEventHistoryScope owns admission and historical evaluation without
// executing SQL. Its scope probe can join other current fences in one statement.
func (app *App[P, A]) prepareEventHistoryScope(ctx context.Context, fresh *Caller[P, A], evidence *eventPolicyHistory, ownerKey string) (eventHistoryScopeVerdict, *deleteProbe, error) {
	return app.prepareEventHistoryScopeWithEvaluation(ctx, fresh, evidence, ownerKey, &eventHistoryScopeEvaluation{})
}

func (app *App[P, A]) prepareEventHistoryScopeWithEvaluation(ctx context.Context, fresh *Caller[P, A], evidence *eventPolicyHistory, ownerKey string, evaluation *eventHistoryScopeEvaluation) (eventHistoryScopeVerdict, *deleteProbe, error) {
	return app.prepareEventHistoryScopeProof(ctx, fresh, evidence, ownerKey, nil, evaluation)
}

// A proof is an exact matcher-selected occurrence, not authority by model. The
// optional captured fallback never turns a missing row into known absence.
type eventHistoryScopeProof struct {
	identity mutationdecode.Row
	scope    policyir.FactScope
	captured bool
}

func (app *App[P, A]) prepareEventHistoryAdditionalScopeWithEvaluation(ctx context.Context, fresh *Caller[P, A], evidence *eventPolicyHistory, requirement observation.HistoricalScope, evaluation *eventHistoryScopeEvaluation) (eventHistoryScopeVerdict, *deleteProbe, error) {
	if requirement.Offset() < 0 {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	proof := eventHistoryScopeProof{identity: requirement.Identity(), scope: requirement.Scope(), captured: true}
	return app.prepareEventHistoryScopeProof(ctx, fresh, evidence, requirement.Key(), &proof, evaluation)
}

// All root and additional-scope checks share this admission, provenance and
// evaluation owner. No actor-bound answers are stored with notice evidence.
func (app *App[P, A]) prepareEventHistoryScopeProof(ctx context.Context, fresh *Caller[P, A], evidence *eventPolicyHistory, ownerKey string, proof *eventHistoryScopeProof, evaluation *eventHistoryScopeEvaluation) (eventHistoryScopeVerdict, *deleteProbe, error) {
	if ctx == nil {
		return eventHistoryScopeVerdict{}, nil, events.Failure(events.CodeSubscriptionRevalidation)
	}
	if err := ctx.Err(); err != nil {
		return eventHistoryScopeVerdict{}, nil, err
	}
	if app == nil || app.registry == nil || app.database == nil || app.eventSchemas == nil || fresh == nil || fresh.app != app || fresh.executor == nil || fresh.policies == nil || fresh.execution == 0 || evidence == nil || evidence.registry == nil || ownerKey == "" {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	// An admitted transaction may still see a snapshot established before a
	// revocation. The current fence requires an unscoped database caller.
	if fresh.executor.scoped || fresh.executor.transaction != nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	if err := fresh.executor.requireAdmitted(ctx); err != nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	if _, err := fresh.executor.queryerFor(app.database); err != nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	graph := evidence.value.Graph()
	generation, capture := app.registry.GenerationDigest(), app.registry.PolicyCaptureFingerprint()
	provider, providerKnown := internalProvider(app.eventProvider)
	exact, known := app.eventSchemas.ResolvePolicyHistorySchema(generation, capture)
	// Initial admission requires the active schema exactly. Retaining an old
	// producer registry does not make current-bound scope descriptors compatible.
	if !providerKnown || provider != app.provider || !known || exact != evidence.registry || evidence.registry.GenerationDigest() != generation || evidence.registry.PolicyCaptureFingerprint() != capture || evidence.value.Generation() != generation || evidence.value.ProjectionFingerprint() != capture || graph.Generation() != generation || graph.ProjectionFingerprint() != capture || graph.Provider() != app.provider || fresh.policies.GenerationDigest() != generation || fresh.policies.Provider() != app.provider {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	if proof != nil {
		if proof.identity.ModelID() == (policyir.ModelID{}) || proof.identity.ModelID() != proof.scope.ModelID() {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
		canonical, err := mutationdecode.NewRow(evidence.registry, proof.identity.ModelID(), proof.identity.Cells())
		if err != nil || !reflect.DeepEqual(canonical, proof.identity) {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
		identity, err := mutationdecode.PrimaryIdentity(evidence.registry, canonical)
		if err != nil || len(canonical.Cells()) != len(identity.Components()) {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
		key, err := policyhistory.RowKey(evidence.registry, app.provider, canonical)
		if err != nil || key != ownerKey {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
	}
	before, beforeKnown := graph.FindRoot(policyhistory.Before, ownerKey)
	after, afterKnown := graph.FindRoot(policyhistory.After, ownerKey)
	root := beforeKnown && afterKnown
	if !root {
		if beforeKnown || afterKnown || proof == nil || !proof.captured {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
		// Only explicit roots certify absence. A captured ordinary owner must
		// be present in both phases, with every evaluated extent certified.
		left, leftKnown := graph.FindRow(policyhistory.Before, ownerKey)
		right, rightKnown := graph.FindRow(policyhistory.After, ownerKey)
		if !leftKnown || !rightKnown {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
		before, after = &left, &right
	}
	if before == nil && after == nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	owner := after
	if owner == nil {
		owner = before
	}
	model := owner.ModelID()
	if before != nil && before.ModelID() != model || after != nil && after.ModelID() != model {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	for _, row := range []*mutationdecode.Row{before, after} {
		if row == nil {
			continue
		}
		key, err := policyhistory.RowKey(evidence.registry, app.provider, *row)
		if err != nil || key != ownerKey || proof != nil && row.ModelID() != proof.identity.ModelID() {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
	}
	policy, present := fresh.policies.Policy(model)
	if !present || policy.ModelID() != model || len(policy.Facts()) == 0 || policy.FactScope().ModelID() != model {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	if proof != nil {
		expected, err := policyir.FactScopeFingerprint(proof.scope)
		if err != nil {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
		actual, err := policyir.FactScopeFingerprint(policy.FactScope())
		if err != nil || actual != expected {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
	}
	scope := policy.FactScope().Condition()
	providers, err := providerSet(evidence.registry)
	if err != nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	if evaluation == nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	if evaluation.before == nil && evaluation.after == nil {
		evaluation.before, err = graph.NewEvaluator(ctx, evidence.registry, providers, policyhistory.Before)
		if err != nil {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
		evaluation.after, err = graph.NewEvaluator(ctx, evidence.registry, providers, policyhistory.After)
		if err != nil {
			return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
		}
		evaluation.digest = evidence.value.Digest()
	}
	if evaluation.before == nil || evaluation.after == nil || evaluation.digest != evidence.value.Digest() {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	beforeQuestion, afterQuestion := evaluation.before.EvaluateRoot, evaluation.after.EvaluateRoot
	if !root {
		beforeQuestion, afterQuestion = evaluation.before.EvaluateCaptured, evaluation.after.EvaluateCaptured
	}
	var verdict eventHistoryScopeVerdict
	verdict.before, err = beforeQuestion(ownerKey, scope)
	if err != nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	verdict.after, err = afterQuestion(ownerKey, scope)
	if err != nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	identity, err := mutationdecode.PrimaryIdentity(evidence.registry, *owner)
	if err != nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	key, err := eventIdentityCondition(app, model, identity)
	if err != nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	condition, err := policyir.NewLogical(model, policyir.LogicalAnd, []policyir.Condition{key, scope})
	if err != nil {
		return eventHistoryScopeVerdict{}, nil, eventPolicyHistoryFailure(ctx)
	}
	if err := ctx.Err(); err != nil {
		return eventHistoryScopeVerdict{}, nil, err
	}
	return verdict, &deleteProbe{target: golem.ModelID(model), condition: condition}, nil
}
