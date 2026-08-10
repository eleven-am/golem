package batch

import (
	"fmt"
	"math"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// Verify consumes provider-decoded complete images only after every prepared
// statement met its exact cardinality. It verifies set equality again by
// primary identity, reorders results to capture order, computes truthful diffs,
// and allocates one fact specification per captured row.
func (prepared Prepared) Verify(applied, after []mutationdecode.Row, factOrdinalBase uint32) (Verification, error) {
	return prepared.VerifyAuthorized(nil, applied, after, factOrdinalBase)
}

// VerifyAuthorized consumes the SQL-evaluated grants captured from every
// AuthorizePreImage statement. The locked rows are the before side of the exact
// logical diff. A false or missing grant matters only when that exact diff says
// the corresponding caller-authored field actually changed.
func (prepared Prepared) VerifyAuthorized(authorized []AuthorizedRow, applied, after []mutationdecode.Row, factOrdinalBase uint32) (Verification, error) {
	want := len(prepared.before)
	if len(applied) != want {
		return Verification{}, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, fmt.Sprintf("apply returned %d rows; captured %d", len(applied), want), nil)
	}
	if prepared.operation == mutationir.UpdateMany && len(after) != want {
		return Verification{}, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, fmt.Sprintf("rehydration returned %d rows; captured %d", len(after), want), nil)
	}
	if prepared.operation == mutationir.DeleteMany && len(after) != 0 {
		return Verification{}, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, "delete-many cannot have after images", nil)
	}
	if want > 0 && uint64(factOrdinalBase)+uint64(want)-1 > math.MaxUint32 {
		return Verification{}, fail(CodeLimit, prepared.context.node.ModelID(), policyir.FieldID{}, "fact ordinal range exceeds uint32", nil)
	}
	applyByKey, err := prepared.indexRows(applied, "apply")
	if err != nil {
		return Verification{}, err
	}
	afterByKey := map[string]mutationdecode.Row{}
	if prepared.operation == mutationir.UpdateMany {
		afterByKey, err = prepared.indexRows(after, "after")
		if err != nil {
			return Verification{}, err
		}
	}
	authorizedByKey := map[string]AuthorizedRow{}
	if prepared.operation == mutationir.UpdateMany && prepared.context.plan.Stance() == mutationir.Caller && len(prepared.context.node.FieldAuthorizations()) != 0 {
		if len(authorized) != want {
			return Verification{}, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, fmt.Sprintf("authorization returned %d rows; captured %d", len(authorized), want), nil)
		}
		authorizedByKey, err = prepared.indexAuthorizedRows(authorized)
		if err != nil {
			return Verification{}, err
		}
	}
	authorizations := make(map[policyir.FieldID]mutationir.FieldAuthorization)
	for _, authorization := range prepared.context.node.FieldAuthorizations() {
		authorizations[authorization.FieldID()] = authorization
	}
	authoredInput := make([]policyir.FieldID, 0, len(prepared.context.node.ScalarOperations()))
	for _, operation := range prepared.context.node.ScalarOperations() {
		if !operation.RuntimeOwned() {
			authoredInput = append(authoredInput, operation.FieldID())
		}
	}
	result := Verification{rows: make([]RowVerification, 0, want)}
	factRequirement := prepared.context.node.Fact()
	factAction, _ := factRequirement.Action()
	if factRequirement.Enabled() {
		result.facts = make([]FactSpec, 0, want)
	}
	for index, capturedBefore := range prepared.before {
		_, key, keyErr := prepared.context.encodedPrimary(capturedBefore)
		if keyErr != nil {
			return Verification{}, keyErr
		}
		if _, ok := applyByKey[key]; !ok {
			return Verification{}, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, "apply result changed or omitted a captured identity", nil)
		}
		before := capturedBefore
		var grants map[policyir.FieldID]bool
		if len(authorizedByKey) != 0 {
			locked, ok := authorizedByKey[key]
			if !ok {
				return Verification{}, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, "authorization omitted a captured identity", nil)
			}
			before, grants = locked.before, locked.grants
		}
		row := RowVerification{ordinal: uint32(index), before: before}
		if prepared.operation == mutationir.UpdateMany {
			persisted, ok := afterByKey[key]
			if !ok {
				return Verification{}, fail(CodeIdentity, prepared.context.node.ModelID(), policyir.FieldID{}, "update-many changed or omitted a primary identity", nil)
			}
			row.after = &persisted
			row.authored, err = mutationdecode.AuthoredFields(prepared.context.registry, before, persisted, authoredInput)
			if err != nil {
				return Verification{}, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, "exact authored diff failed", err)
			}
			row.conditions = make([]mutationir.FieldAuthorization, 0, len(row.authored))
			for _, field := range row.authored {
				authorization, ok := authorizations[field]
				if prepared.context.plan.Stance() == mutationir.Caller && !ok {
					return Verification{}, fail(CodeInput, prepared.context.node.ModelID(), field, "changed caller-authored field has no authorization requirement", nil)
				}
				if ok {
					granted, precomputed := grants[field]
					if !precomputed {
						return Verification{}, fail(CodeSet, prepared.context.node.ModelID(), field, "changed caller-authored field has no precomputed locked-preimage grant", nil)
					}
					if !granted {
						return Verification{}, fail(CodeForbidden, prepared.context.node.ModelID(), field, "changed caller-authored field was denied on the locked pre-image", nil)
					}
					row.conditions = append(row.conditions, authorization)
				}
			}
		}
		result.rows = append(result.rows, row)
		if factRequirement.Enabled() {
			fact := FactSpec{action: factAction, ordinal: factOrdinalBase + uint32(index), before: before, after: row.after}
			result.facts = append(result.facts, fact)
		}
	}
	if len(applyByKey) != want || prepared.operation == mutationir.UpdateMany && len(afterByKey) != want {
		return Verification{}, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, "provider returned an identity outside the captured set", nil)
	}
	return result, nil
}

func (prepared Prepared) indexAuthorizedRows(rows []AuthorizedRow) (map[string]AuthorizedRow, error) {
	result := make(map[string]AuthorizedRow, len(rows))
	for index, row := range rows {
		before := row.before
		if before.ModelID() != prepared.context.node.ModelID() {
			return nil, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, fmt.Sprintf("authorization row %d belongs to another model", index), nil)
		}
		complete, completeErr := before.IsComplete(prepared.context.registry)
		if completeErr != nil || !complete {
			return nil, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, fmt.Sprintf("authorization row %d is not a complete locked scalar image", index), completeErr)
		}
		_, key, err := prepared.context.encodedPrimary(before)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, "authorization returned a duplicate identity", nil)
		}
		result[key] = AuthorizedRow{before: before, grants: cloneGrants(row.grants)}
	}
	return result, nil
}

func cloneGrants(values map[policyir.FieldID]bool) map[policyir.FieldID]bool {
	result := make(map[policyir.FieldID]bool, len(values))
	for field, granted := range values {
		result[field] = granted
	}
	return result
}

func (prepared Prepared) indexRows(rows []mutationdecode.Row, phase string) (map[string]mutationdecode.Row, error) {
	result := make(map[string]mutationdecode.Row, len(rows))
	for index, row := range rows {
		if row.ModelID() != prepared.context.node.ModelID() {
			return nil, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, fmt.Sprintf("%s row %d belongs to another model", phase, index), nil)
		}
		complete, completeErr := row.IsComplete(prepared.context.registry)
		if completeErr != nil || !complete {
			return nil, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, fmt.Sprintf("%s row %d is not a complete scalar image", phase, index), completeErr)
		}
		_, key, err := prepared.context.encodedPrimary(row)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fail(CodeSet, prepared.context.node.ModelID(), policyir.FieldID{}, phase+" returned a duplicate identity", nil)
		}
		result[key] = row
	}
	return result, nil
}
