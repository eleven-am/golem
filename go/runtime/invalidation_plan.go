package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/invalidationcontract"
	"github.com/eleven-am/golem/go/internal/operationargs"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/observation"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type invalidationMemberPlan struct {
	source     golem.ModelID
	dependency eventDependency
	judge      *eventJudgeSpec
	facts      observation.Plan
	historical bool
}

type invalidationPlan struct {
	members     []invalidationMemberPlan
	fingerprint [32]byte
}

func invalidationID(value string) ([16]byte, error) {
	var id [16]byte
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != len(id) {
		return id, fmt.Errorf("P7_INVALIDATION_CONFIG: invalid static identity")
	}
	copy(id[:], raw)
	return id, nil
}

func callInvalidationBinder[A any](ctx context.Context, binding golem.GeneratedInvalidationBinding[A], actor A, arguments map[string]any) (value golem.RuntimeInvalidationDependency, err error) {
	defer func() {
		if recover() != nil {
			value = golem.RuntimeInvalidationDependency{}
			err = events.Failure(events.CodeSubscriptionRevalidation)
		}
	}()
	return binding.Bind(ctx, actor, arguments)
}

func (app *App[P, A]) planInvalidations(ctx context.Context, caller *Caller[P, A], operation compilerir.CustomOperationContractIR, request operationargs.Request, canonical []byte) (invalidationPlan, error) {
	var result invalidationPlan
	hash := sha256.New()
	hash.Write([]byte("golem:operation-invalidation:v1\x00"))
	argumentDigest := sha256.Sum256(canonical)
	hash.Write(argumentDigest[:])
	generation := app.registry.GenerationDigest()
	hash.Write(generation[:])
	writeCount := func(value int) {
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(value))
		hash.Write(encoded[:])
	}
	writeText := func(value string) { writeCount(len(value)); hash.Write([]byte(value)) }
	writeText(string(operation.ExtensionID))
	audience, err := invalidationAudience(ctx, caller, operation, canonical)
	if err != nil {
		return result, err
	}
	writeText(audience)
	writeCount(len(operation.Invalidation.Dependencies))
	paths := 0
	for _, declaration := range operation.Invalidation.Dependencies {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		writeText(string(declaration.ID))
		binding, present := app.bindings.Invalidation(string(operation.ExtensionID), string(declaration.ID))
		if !present {
			return result, events.Failure(events.CodeSubscriptionInvalid)
		}
		value, err := callInvalidationBinder(CallerOperationContext(ctx, caller), binding, caller.actor, invalidationArguments(request, operation.Pagination != nil))
		if err != nil {
			return result, err
		}
		modelID, err := invalidationID(string(declaration.ModelID))
		if err != nil || value.ModelID() != golem.ModelID(modelID) || value.Kind() != string(declaration.Kind) {
			return result, events.Failure(events.CodeSubscriptionInvalid)
		}
		model := golem.ModelID(modelID)
		var row policyir.Condition
		var filter *policyir.Condition
		var masks []policyir.Condition
		var members []invalidationMemberPlan
		if declaration.Kind == compilerir.CustomInvalidationPolicyFacts {
			if !mutationHistoryWriterRequired(app.registry) {
				return result, fmt.Errorf("P7_INVALIDATION_HISTORY: policy-fact observation requires certified historical writing")
			}
			row, masks, err = app.invalidationFacts(caller, declaration, value)
		} else {
			row, filter, masks, members, err = app.invalidationReadable(ctx, caller, declaration, value)
		}
		if err != nil {
			return result, err
		}
		conditions := []policyir.Condition{row}
		if filter != nil {
			conditions = append(conditions, *filter)
		}
		conditions = append(conditions, masks...)
		writeCount(len(conditions))
		for _, condition := range conditions {
			fingerprint, err := policyir.ConditionFingerprint(condition)
			if err != nil {
				return result, err
			}
			hash.Write(fingerprint[:])
		}
		constructor := observation.New
		if mutationHistoryWriterRequired(app.registry) {
			constructor = observation.NewHistorical
		}
		facts, err := constructor(row, filter, masks...)
		if err != nil {
			return result, err
		}
		inventory := append(facts.Paths(), facts.OwnerPaths()...)
		paths += len(inventory)
		if paths > observation.MaxPaths {
			return result, events.Failure(events.CodeSubscriptionInvalid)
		}
		physical, err := factEventPathDependencies(inventory, app.registry)
		if err != nil {
			return result, err
		}
		if declaration.Kind == compilerir.CustomInvalidationPolicyFacts {
			// Paths cover descendants; local-only/unused definitions still observe
			// their owner. Do not replace this private member with a readable judge.
			found := false
			for _, member := range physical {
				found = found || member.model == model
			}
			if !found {
				physical = append(physical, eventDependency{model: model, membership: true, physical: true})
			}
		}
		for _, dependency := range physical {
			members = append(members, invalidationMemberPlan{source: model, dependency: dependency, facts: facts, historical: true})
		}
		if len(result.members)+len(members) > invalidationcontract.MaxMembers {
			return result, events.Failure(events.CodeSubscriptionInvalid)
		}
		writeCount(len(members))
		for _, member := range members {
			metadata, ok := app.registry.Model(member.dependency.model)
			if !ok || !metadata.SubscriptionsEnabled() {
				return result, events.Failure(events.CodeSubscriptionInvalid)
			}
			hash.Write(member.dependency.model[:])
			if member.historical {
				hash.Write([]byte{1})
			} else {
				hash.Write([]byte{0})
			}
			writeCount(len(member.dependency.fields))
			for _, field := range member.dependency.fields {
				hash.Write(field[:])
			}
			if member.judge != nil {
				writeCount(len(member.judge.conditions()))
				for _, condition := range member.judge.conditions() {
					fp, err := policyir.ConditionFingerprint(condition)
					if err != nil {
						return result, err
					}
					hash.Write(fp[:])
				}
			} else {
				writeCount(0)
			}
		}
		result.members = append(result.members, members...)
	}
	if len(result.members) == 0 {
		return result, events.Failure(events.CodeSubscriptionInvalid)
	}
	copy(result.fingerprint[:], hash.Sum(nil))
	return result, nil
}

func invalidationAudience[P, A any](ctx context.Context, caller *Caller[P, A], operation compilerir.CustomOperationContractIR, canonical []byte) (string, error) {
	if operation.Pagination == nil {
		return "", nil
	}
	_, audience, err := callerPageScope(ctx, caller, operation, canonical)
	return audience, err
}

func (app *App[P, A]) invalidationFacts(caller *Caller[P, A], declaration compilerir.CustomInvalidationDependencyIR, value golem.RuntimeInvalidationDependency) (policyir.Condition, []policyir.Condition, error) {
	fail := func() (policyir.Condition, []policyir.Condition, error) {
		return policyir.Condition{}, nil, events.Failure(events.CodeSubscriptionForbidden)
	}
	policy, present := caller.policies.Policy(policyir.ModelID(value.ModelID()))
	frozen, binding, valid := value.FactScope()
	if !present || !valid {
		return fail()
	}
	active := policy.FactScope()
	provenance, bound := active.Binding()
	id, err := invalidationID(binding.TemplateID())
	if err != nil || !bound || binding.TemplateID() != string(declaration.ScopeTemplateID) || provenance.TemplateID != id || provenance.Template != policyir.Fingerprint(binding.Fingerprint()) || provenance.Arguments != policyir.Fingerprint(sha256.Sum256(binding.CanonicalBytes())) {
		return fail()
	}
	condition, err := policybind.ExactPredicate(frozen, app.registry, app.providers)
	if err != nil {
		return fail()
	}
	condition, err = normalize.Condition(condition)
	if err != nil {
		return fail()
	}
	expected, err := normalize.Condition(active.Condition())
	if err != nil {
		return fail()
	}
	actualFP, err := policyir.ConditionFingerprint(condition)
	if err != nil {
		return fail()
	}
	expectedFP, err := policyir.ConditionFingerprint(expected)
	if err != nil || actualFP != expectedFP {
		return fail()
	}
	witness, err := policyir.NewFactScopeWitness(active)
	if err != nil {
		return fail()
	}
	var questions []policyir.Condition
	for _, template := range declaration.FactTemplateIDs {
		id, err := invalidationID(string(template))
		if err != nil {
			return fail()
		}
		found := false
		for _, definition := range policy.Facts() {
			binding, bound := definition.Binding()
			if !bound || binding.TemplateID != id {
				continue
			}
			reference, err := policyir.NewFactReference(policy.ModelID(), definition.ID())
			if err != nil {
				return fail()
			}
			question, err := resolve.Condition(policy, reference)
			if err != nil {
				return fail()
			}
			questions = append(questions, question)
			found = true
			break
		}
		if !found {
			return fail()
		}
	}
	return witness, questions, nil
}

func (app *App[P, A]) invalidationReadable(ctx context.Context, caller *Caller[P, A], declaration compilerir.CustomInvalidationDependencyIR, value golem.RuntimeInvalidationDependency) (policyir.Condition, *policyir.Condition, []policyir.Condition, []invalidationMemberPlan, error) {
	var zero policyir.Condition
	if len(app.eventAdapters) != 0 {
		return zero, nil, nil, nil, fmt.Errorf("P7_INVALIDATION_COVERAGE: readable operation invalidations require complete update and delete before images; configured CDC adapters do not certify this source evidence")
	}
	predicate, present := value.Predicate()
	if !present {
		return zero, nil, nil, nil, events.Failure(events.CodeSubscriptionInvalid)
	}
	original, err := policybind.Predicate(predicate, app.registry, app.providers)
	if err != nil {
		return zero, nil, nil, nil, err
	}
	// Hooks see an ordinary request. The binding predicate is conjoined again
	// by the existing event hook owner, so ReplaceOptions cannot erase it.
	frozen, err := golem.RuntimeFreezeReadRequest(golem.RuntimeReadRequestInput{Operation: golem.ReadFindMany, Model: value.ModelID(), Where: &predicate})
	if err != nil {
		return zero, nil, nil, nil, err
	}
	state := &callerEventSubscription[P]{model: value.ModelID(), readRequest: frozen, originalWhere: &original}
	_, prepared, err := app.eventHookedRead(ctx, caller, state)
	if err != nil {
		return zero, nil, nil, nil, err
	}
	var where *policyir.Condition
	if condition, ok := prepared.request.Where(); ok {
		where = &condition
	}
	// Authorize the ordinary predicate through the existing read planner, but
	// without selecting an identity or imposing a public primary-key ordering.
	// This is planning only: no count or other value is read or returned.
	validation, err := readir.NewRequest(readir.RequestInput{Operation: readir.Count, Model: policyir.ModelID(value.ModelID()), Where: where})
	if err != nil {
		return zero, nil, nil, nil, err
	}
	validationRead := prepared
	validationRead.request = validation
	if _, err := preparePlan(validationRead, app.registry, app.readLimits.plan); err != nil {
		var failure *readplan.Error
		if errors.As(err, &failure) && failure.Code == readplan.CodePolicy && failure.Reason == golem.ReasonNotGranted {
			return zero, nil, nil, nil, events.Failure(events.CodeSubscriptionForbidden)
		}
		return zero, nil, nil, nil, err
	}
	row, filter, err := readplan.CallerReach(caller.policies, policyir.ModelID(value.ModelID()), where, app.readLimits.plan.MaxRelationDepth)
	if err != nil {
		return zero, nil, nil, nil, err
	}
	if truth, constant := row.Constant(); constant && !truth {
		return zero, nil, nil, nil, events.Failure(events.CodeSubscriptionForbidden)
	}
	var masks []policyir.Condition
	var members []invalidationMemberPlan
	for _, fieldID := range declaration.Fields {
		id, err := invalidationID(string(fieldID))
		if err != nil {
			return zero, nil, nil, nil, err
		}
		field := golem.FieldID(id)
		mask, err := readplan.ReadFieldReach(caller.policies, row.ModelID(), policyir.FieldID(field), app.readLimits.plan.MaxRelationDepth)
		if err != nil {
			return zero, nil, nil, nil, err
		}
		masks = append(masks, mask)
		guarded, err := policyir.NewLogical(row.ModelID(), policyir.LogicalAnd, []policyir.Condition{row, mask})
		if err != nil {
			return zero, nil, nil, nil, err
		}
		watch := []golem.FieldID{field}
		metadata, _ := app.registry.Field(value.ModelID(), field)
		if metadata.Kind() == compilerir.FieldRelation {
			return zero, nil, nil, nil, fmt.Errorf("P7_INVALIDATION_COVERAGE: watched relation membership requires an explicit dependency on its source model")
		}
		judge := &eventJudgeSpec{row: guarded, filter: filter, watch: watch}
		members = append(members, invalidationMemberPlan{source: value.ModelID(), dependency: eventDependency{model: value.ModelID(), fields: watch, membership: true}, judge: judge})
	}
	conditions := []policyir.Condition{row}
	if filter != nil {
		conditions = append(conditions, *filter)
	}
	conditions = append(conditions, masks...)
	var watched []golem.FieldID
	for _, member := range members {
		watched = append(watched, member.dependency.fields...)
	}
	if err := validateInvalidationReadableCoverage(app.registry, value.ModelID(), watched, conditions...); err != nil {
		return zero, nil, nil, nil, err
	}
	return row, filter, masks, members, nil
}

// validateInvalidationReadableCoverage checks all ordinary questions together.
// A target-readable model/field union is not evidence of the old source route.
// Physical fact bodies are intentionally excluded: their genuine named scope
// and captured occurrence inventory are validated by the historical owner.
func validateInvalidationReadableCoverage(registry *schema.Registry, model golem.ModelID, watched []golem.FieldID, conditions ...policyir.Condition) error {
	collected, err := dependency.CollectReadable(conditions...)
	if err != nil {
		return err
	}
	for _, entry := range collected.Dependencies().Entries() {
		if entry.Kind() == dependency.Relation {
			return fmt.Errorf("P7_INVALIDATION_COVERAGE: ordinary related row, filter, or field-mask paths require historical source-membership evidence; declare a locally scoped source dependency or registered captured fact authority")
		}
	}
	metadata, present := registry.Model(model)
	if !present || collected.ModelID() != policyir.ModelID(model) {
		return fmt.Errorf("P7_INVALIDATION_COVERAGE: source observation model is unavailable")
	}
	_, fields, present := metadata.EventSchema()
	if !present {
		return fmt.Errorf("P7_INVALIDATION_COVERAGE: source before-image contract is unavailable")
	}
	captured := make(map[golem.FieldID]bool, len(fields))
	for _, field := range fields {
		captured[field] = true
	}
	required := append([]golem.FieldID(nil), watched...)
	for _, entry := range collected.Dependencies().Entries() {
		required = append(required, golem.FieldID(entry.FieldID()))
	}
	for _, field := range required {
		if !captured[field] {
			return fmt.Errorf("P7_INVALIDATION_COVERAGE: source before image must capture every ordinary row, filter, field-mask control and watched scalar; declare the complete DeleteSnapshot field inventory")
		}
	}
	return nil
}
