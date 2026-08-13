package migration

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func Order(plan Plan) ([]Operation, error) {
	byID := make(map[OperationID]Operation, len(plan.Operations))
	indegree := make(map[OperationID]int, len(plan.Operations))
	dependents := make(map[OperationID][]OperationID)
	for _, operation := range plan.Operations {
		if operation.ID == "" {
			return nil, fmt.Errorf("migration operation has empty ID")
		}
		if _, duplicate := byID[operation.ID]; duplicate {
			return nil, fmt.Errorf("duplicate migration operation ID %s", operation.ID)
		}
		byID[operation.ID] = operation
		indegree[operation.ID] = 0
	}
	for _, operation := range plan.Operations {
		seen := map[OperationID]bool{}
		for _, dependency := range operation.Dependencies {
			if dependency == operation.ID {
				return nil, fmt.Errorf("operation %s depends on itself", operation.ID)
			}
			if _, exists := byID[dependency]; !exists {
				return nil, fmt.Errorf("operation %s depends on unknown operation %s", operation.ID, dependency)
			}
			if seen[dependency] {
				return nil, fmt.Errorf("operation %s repeats dependency %s", operation.ID, dependency)
			}
			seen[dependency] = true
			indegree[operation.ID]++
			dependents[dependency] = append(dependents[dependency], operation.ID)
		}
	}
	var ready []Operation
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, byID[id])
		}
	}
	less := func(a, b Operation) bool {
		if a.Stage != b.Stage {
			return a.Stage < b.Stage
		}
		if a.ObjectID != b.ObjectID {
			return a.ObjectID < b.ObjectID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	}
	result := make([]Operation, 0, len(plan.Operations))
	for len(ready) != 0 {
		sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
		current := ready[0]
		ready = ready[1:]
		result = append(result, current)
		for _, id := range dependents[current.ID] {
			indegree[id]--
			if indegree[id] == 0 {
				ready = append(ready, byID[id])
			}
		}
	}
	if len(result) != len(plan.Operations) {
		return nil, fmt.Errorf("migration operation dependency cycle")
	}
	return result, nil
}

// ValidatePlanShape verifies the complete provider-neutral operation and phase
// graph without applying reviewed-approval policy. It is the single shape gate
// shared by prospective explanation and reviewed execution.
func ValidatePlanShape(plan Plan) error {
	if plan.Provider != ir.SQLite && plan.Provider != ir.PostgreSQL {
		return fmt.Errorf("migration plan provider is invalid")
	}
	if plan.snapshotFacts == nil {
		if plan.Initial {
			return fmt.Errorf("initial migration plan lacks typed snapshot facts")
		}
	} else {
		before, after := plan.snapshotFacts.before, plan.snapshotFacts.after
		if before.Provider.Provider != plan.Provider || after.Provider.Provider != plan.Provider {
			return fmt.Errorf("migration plan provider differs from typed snapshot facts")
		}
		expectedInitial := emptySystemSchema(before.System) && len(before.Tables) == 0 && len(before.Extensions) == 0 && len(before.Unmanaged) == 0 && len(after.System.Objects) != 0
		if plan.Initial != expectedInitial {
			return fmt.Errorf("migration plan initial classification differs from typed snapshot facts")
		}
	}
	if err := validateDigest("plan before fingerprint", plan.BeforeFingerprint, false); err != nil {
		return err
	}
	if err := validateDigest("plan after fingerprint", plan.AfterFingerprint, false); err != nil {
		return err
	}
	ordered, err := Order(plan)
	if err != nil {
		return err
	}
	if !sameOrderedOperations(ordered, plan.Operations) {
		return fmt.Errorf("migration operation inventory is not in deterministic DAG order")
	}
	validRisks := map[Risk]bool{
		RiskSafe: true, RiskLocking: true, RiskRewrite: true,
		RiskDataLoss: true, RiskManual: true,
	}
	for _, operation := range ordered {
		if err := validateOperationDigests(operation); err != nil {
			return err
		}
		if !validRisks[operation.Risk] {
			return fmt.Errorf("operation %s has invalid risk", operation.ID)
		}
		if operation.Mode != Transactional && operation.Mode != AutocommitOnly {
			return fmt.Errorf("operation %s has invalid transaction mode", operation.ID)
		}
		if operation.Mode == AutocommitOnly && operation.Resume == nil {
			return fmt.Errorf("autocommit operation %s lacks resume metadata", operation.ID)
		}
	}
	seen := map[OperationID]bool{}
	var phased []OperationID
	expectedBefore := plan.BeforeFingerprint
	for index, phase := range plan.Phases {
		if err := validateDigest(fmt.Sprintf("phase %d before fingerprint", index), phase.BeforeFingerprint, false); err != nil {
			return err
		}
		if err := validateDigest(fmt.Sprintf("phase %d after fingerprint", index), phase.AfterFingerprint, false); err != nil {
			return err
		}
		if phase.Ordinal != uint32(index) {
			return fmt.Errorf("phase ordinal %d is noncanonical", phase.Ordinal)
		}
		if len(phase.Operations) == 0 {
			return fmt.Errorf("phase %d is empty", index)
		}
		if phase.Mode != Transactional && phase.Mode != AutocommitOnly {
			return fmt.Errorf("phase %d has invalid transaction mode", index)
		}
		if phase.Mode == AutocommitOnly && len(phase.Operations) != 1 {
			return fmt.Errorf("autocommit phase %d must contain exactly one operation", index)
		}
		if index > 0 && phase.Mode == Transactional && plan.Phases[index-1].Mode == Transactional {
			return fmt.Errorf("adjacent transactional phases %d and %d are noncanonical", index-1, index)
		}
		if phase.BeforeFingerprint != expectedBefore {
			return fmt.Errorf("phase %d fingerprint chain is discontinuous", index)
		}
		for _, id := range phase.Operations {
			operation, ok := findOperation(ordered, id)
			if !ok || seen[id] {
				return fmt.Errorf("phase %d references missing or repeated operation %s", index, id)
			}
			if operation.Mode != phase.Mode {
				return fmt.Errorf("phase %d mixes transaction modes", index)
			}
			seen[id] = true
			phased = append(phased, id)
		}
		expectedBefore = phase.AfterFingerprint
	}
	if len(seen) != len(ordered) || expectedBefore != plan.AfterFingerprint {
		return fmt.Errorf("phases do not cover plan or final fingerprint")
	}
	if len(ordered) == 0 {
		if len(plan.Phases) != 0 || plan.BeforeFingerprint != plan.AfterFingerprint {
			return fmt.Errorf("no-change plan must have equal fingerprints and no phases")
		}
	} else if len(plan.Phases) == 0 {
		return fmt.Errorf("changed plan must have operations and phases")
	}
	if plan.snapshotFacts != nil && reflect.DeepEqual(plan.snapshotFacts.before, plan.snapshotFacts.after) && (len(plan.Operations) != 0 || len(plan.Phases) != 0) && !matchesExactHistoricalV1IdentityPlan(plan) {
		return fmt.Errorf("identical normalized snapshots must not contain migration work")
	}
	for index := range ordered {
		if phased[index] != ordered[index].ID {
			return fmt.Errorf("phased operation order is not the deterministic DAG order")
		}
	}
	return nil
}

// matchesExactHistoricalV1IdentityPlan admits only the operation graph emitted
// by the frozen v1 algebra. Released v1 provider histories recorded a schema
// version even when a provider's typed snapshots were identical. Current plans
// continue to represent identical snapshots with no operations or phases.
func matchesExactHistoricalV1IdentityPlan(plan Plan) bool {
	if plan.snapshotFacts == nil {
		return false
	}
	before, after := plan.snapshotFacts.before, plan.snapshotFacts.after
	if before.Version != 1 || before.CanonicalVersion != 1 || after.Version != 1 || after.CanonicalVersion != 1 {
		return false
	}
	expected, err := diffHistoricalV1Tagged(before, after)
	if err != nil {
		return false
	}
	return plan.Provider == expected.Provider &&
		plan.Initial == expected.Initial &&
		plan.BeforeFingerprint == expected.BeforeFingerprint &&
		plan.AfterFingerprint == expected.AfterFingerprint &&
		reflect.DeepEqual(plan.Operations, expected.Operations) &&
		reflect.DeepEqual(plan.Phases, expected.Phases)
}

func sameOrderedOperations(left, right []Operation) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !reflect.DeepEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

func ValidatePlan(plan Plan, approvals []Approval) error {
	if err := ValidatePlanShape(plan); err != nil {
		return err
	}
	approved := make(map[OperationID]Approval, len(approvals))
	for _, approval := range approvals {
		if err := validateDigest(fmt.Sprintf("approval %s before fragment", approval.OperationID), approval.Before, true); err != nil {
			return err
		}
		if err := validateDigest(fmt.Sprintf("approval %s after fragment", approval.OperationID), approval.After, true); err != nil {
			return err
		}
		if _, duplicate := approved[approval.OperationID]; duplicate {
			return fmt.Errorf("duplicate approval for %s", approval.OperationID)
		}
		approved[approval.OperationID] = approval
	}
	for _, operation := range plan.Operations {
		approval, exists := approved[operation.ID]
		required := RequiresApproval(operation)
		if required && (!exists || approval.Risk != operation.Risk || approval.Before != operation.Before || approval.After != operation.After) {
			return fmt.Errorf("operation %s requires exact object-scoped approval", operation.ID)
		}
		if exists && !required {
			return fmt.Errorf("operation %s does not accept destructive approval", operation.ID)
		}
		delete(approved, operation.ID)
	}
	if len(approved) != 0 {
		return fmt.Errorf("approval references unknown operation")
	}
	return nil
}

// RequiresApproval reports whether an operation may appear in reviewed history
// only together with an exact object-scoped approval binding its ID, risk, and
// before/after content digests.
//
// Data-loss and manual risk always require one. AlterColumnType and
// BackfillColumn additionally require one at every risk classification: the
// approval digest pair is the only artifact binding a human review to the
// exact typed before/after metadata of a type change, or to the exact target
// of a reviewed backfill, so a value-preserving widening or a reviewed
// backfill must never become an unreviewed automatic operation.
func RequiresApproval(operation Operation) bool {
	switch operation.Kind {
	case AlterColumnType, BackfillColumn, InitializeConcurrencyColumn:
		return true
	}
	return operation.Risk == RiskDataLoss || operation.Risk == RiskManual
}

func validateOperationDigests(operation Operation) error {
	for _, digest := range []struct {
		label string
		value Digest
	}{{"before fragment", operation.Before}, {"after fragment", operation.After}} {
		if err := validateDigest(fmt.Sprintf("operation %s %s", operation.ID, digest.label), digest.value, true); err != nil {
			return err
		}
	}
	if operation.Transform != nil {
		if err := validateDigest(fmt.Sprintf("operation %s transform input", operation.ID), operation.Transform.Input, false); err != nil {
			return err
		}
	}
	if operation.Resume != nil {
		if err := validateDigest(fmt.Sprintf("operation %s resume fingerprint", operation.ID), operation.Resume.ExpectedFingerprint, false); err != nil {
			return err
		}
	}
	return nil
}

func findOperation(operations []Operation, id OperationID) (Operation, bool) {
	for _, operation := range operations {
		if operation.ID == id {
			return operation, true
		}
	}
	return Operation{}, false
}

func BuildPhases(ordered []Operation, before, after Digest) ([]Phase, error) {
	var phases []Phase
	for _, operation := range ordered {
		if len(phases) == 0 || phases[len(phases)-1].Mode != operation.Mode || operation.Mode == AutocommitOnly {
			phases = append(phases, Phase{Ordinal: uint32(len(phases)), Mode: operation.Mode})
		}
		phase := &phases[len(phases)-1]
		phase.Operations = append(phase.Operations, operation.ID)
	}
	byID := map[OperationID]Operation{}
	for _, operation := range ordered {
		byID[operation.ID] = operation
	}
	current := before
	for index := range phases {
		phases[index].BeforeFingerprint = current
		if index == len(phases)-1 {
			phases[index].AfterFingerprint = after
		} else {
			last := byID[phases[index].Operations[len(phases[index].Operations)-1]]
			if last.Resume == nil || last.Resume.ExpectedFingerprint == "" {
				return nil, fmt.Errorf("non-final phase %d lacks explicit intermediate fingerprint", index)
			}
			phases[index].AfterFingerprint = last.Resume.ExpectedFingerprint
		}
		current = phases[index].AfterFingerprint
	}
	return phases, nil
}
