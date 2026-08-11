package migration

import (
	"fmt"
	"sort"
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

func ValidatePlan(plan Plan, approvals []Approval) error {
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
	approved := make(map[OperationID]Approval, len(approvals))
	validRisks := map[Risk]bool{
		RiskSafe: true, RiskLocking: true, RiskRewrite: true,
		RiskDataLoss: true, RiskManual: true,
	}
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
	seen := map[OperationID]bool{}
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
		}
		expectedBefore = phase.AfterFingerprint
	}
	if len(seen) != len(ordered) || expectedBefore != plan.AfterFingerprint {
		return fmt.Errorf("phases do not cover plan or final fingerprint")
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
	case AlterColumnType, BackfillColumn:
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
