package ir

import (
	"strings"
	"testing"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestUpdateFactRequirementCarriesAnExplicitSnapshotDecision(t *testing.T) {
	identity := []policyir.FieldID{testField(1)}
	stored := []policyir.FieldID{testField(1), testField(2)}
	fact, err := NewUpdateFactRequirement(identity, identity, DeleteSnapshotStoredScalars, stored)
	if err != nil {
		t.Fatal(err)
	}
	if action, _ := fact.Action(); action != FactUpdated || fact.DeleteSnapshotState() != DeleteSnapshotStoredScalars || len(fact.PrivateDeleteSnapshot()) != 2 {
		t.Fatalf("update fact: action=%d state=%d fields=%d", action, fact.DeleteSnapshotState(), len(fact.PrivateDeleteSnapshot()))
	}
	if err := validateFact(Update, fact); err != nil {
		t.Fatal(err)
	}
	unverifiable, err := NewUpdateFactRequirement(identity, identity, DeleteSnapshotUnverifiable, nil)
	if err != nil || unverifiable.DeleteSnapshotState() != DeleteSnapshotUnverifiable || len(unverifiable.PrivateDeleteSnapshot()) != 0 {
		t.Fatalf("unverifiable update: state=%d err=%v", unverifiable.DeleteSnapshotState(), err)
	}
	if _, err := NewUpdateFactRequirement(identity, identity, DeleteSnapshotUnverifiable, stored); err == nil || !strings.Contains(err.Error(), "unverifiable snapshot cannot name fields") {
		t.Fatalf("an unverifiable update named fields: %v", err)
	}
	if _, err := NewUpdateFactRequirement(identity, identity, DeleteSnapshotNotApplicable, nil); err == nil || !strings.Contains(err.Error(), "invalid verification state") {
		t.Fatalf("an update without a decision was built: %v", err)
	}
	if _, err := NewFactRequirement(FactCreated, nil, identity, stored); err == nil || !strings.Contains(err.Error(), "only for update and delete") {
		t.Fatalf("a created fact took a before snapshot: %v", err)
	}
	legacy, err := NewFactRequirement(FactUpdated, identity, identity, nil)
	if err != nil || legacy.DeleteSnapshotState() != DeleteSnapshotUnverifiable {
		t.Fatalf("an update built without a snapshot must be unverifiable: state=%d err=%v", legacy.DeleteSnapshotState(), err)
	}
}

func TestGraphValidationRequiresASnapshotDecisionOnUpdatesAndRefusesOneOnCreates(t *testing.T) {
	identity := []policyir.FieldID{testField(1)}
	undecided := FactRequirement{enabled: true, action: FactUpdated, beforeIdentity: identity, afterIdentity: identity}
	if err := validateFact(Update, undecided); err == nil || !strings.Contains(err.Error(), "no verification decision") {
		t.Fatalf("an update fact without a decision validated: %v", err)
	}
	unverifiableWithFields := FactRequirement{enabled: true, action: FactUpdated, beforeIdentity: identity, afterIdentity: identity, deleteSnapshotState: DeleteSnapshotUnverifiable, privateDeleteSnapshot: identity}
	if err := validateFact(Update, unverifiableWithFields); err == nil || !strings.Contains(err.Error(), "unverifiable fact carries snapshot fields") {
		t.Fatalf("an unverifiable update naming fields validated: %v", err)
	}
	created := FactRequirement{enabled: true, action: FactCreated, afterIdentity: identity, deleteSnapshotState: DeleteSnapshotStoredScalars}
	if err := validateFact(Create, created); err == nil || !strings.Contains(err.Error(), "created fact carries snapshot semantics") {
		t.Fatalf("a created fact with snapshot semantics validated: %v", err)
	}
}
