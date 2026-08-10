package p8verify

import "testing"

func TestP8IndependentAuditInventoryIsExplicitlyIncomplete(t *testing.T) {
	inventory := ReleaseAuditInventory()
	if err := ValidateInventory(inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Status != "local-candidate-audit" || inventory.FormalEvidence != "PENDING" || len(inventory.PendingIntegrations) == 0 {
		t.Fatalf("P8-I local audit overstates completion: %#v", inventory)
	}
	for _, forbidden := range []string{"PASS", "complete", "production-expectation-helper"} {
		if inventory.Status == forbidden || inventory.FormalEvidence == forbidden {
			t.Fatalf("P8-I scaffold claims %q", forbidden)
		}
	}
}

func TestP8IndependentAuditInventoryRejectsMissingDomainProfileTestAndCommand(t *testing.T) {
	mutations := []func(*Inventory){
		func(value *Inventory) { value.Domains = value.Domains[1:] },
		func(value *Inventory) { value.ProviderProfiles = value.ProviderProfiles[:2] },
		func(value *Inventory) { value.RequiredTests = value.RequiredTests[:2] },
		func(value *Inventory) { value.RegisteredCommands = value.RegisteredCommands[:1] },
		func(value *Inventory) { value.PendingIntegrations = nil },
		func(value *Inventory) { value.Prohibitions = nil },
	}
	for index, mutate := range mutations {
		inventory := ReleaseAuditInventory()
		mutate(&inventory)
		if err := ValidateInventory(inventory); err == nil {
			t.Fatalf("inventory mutation %d was accepted", index)
		}
	}
}
