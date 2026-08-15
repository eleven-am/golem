package migration

import (
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
)

func TestPlatformUpgradeAddsOnlyDeliverySystemObject(t *testing.T) {
	before := schema()
	before.System = physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{
		{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
		{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
		physical.OutboxSystemObjectV1(),
		physical.UpsertGuardSystemObjectV1(),
	}}
	after := before
	after.System.Objects = append(append([]physical.SystemObject(nil), before.System.Objects...), physical.OutboxDeliverySystemObjectV1())
	beforeApplication, err := physical.PhysicalFingerprint(before)
	if err != nil {
		t.Fatal(err)
	}
	afterApplication, err := physical.PhysicalFingerprint(after)
	if err != nil {
		t.Fatal(err)
	}
	beforeSystem, _ := physical.SystemFingerprint(before.Provider, before.System)
	afterSystem, _ := physical.SystemFingerprint(after.Provider, after.System)
	if beforeApplication != afterApplication || beforeSystem == afterSystem || !reflect.DeepEqual(before.Tables, after.Tables) {
		t.Fatal("P7 platform upgrade crossed into application schema")
	}
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Initial || len(plan.Operations) != 2 || plan.Operations[0].Kind != AddSystemObject || plan.Operations[0].ObjectID != string(physical.OutboxDeliveryObjectIDV1) || plan.Operations[1].Kind != RecordSchemaVersion {
		t.Fatalf("platform upgrade operations=%#v", plan.Operations)
	}
	forged := after
	forged.System.Objects = append([]physical.SystemObject(nil), after.System.Objects...)
	forged.System.Objects[len(forged.System.Objects)-1].Name = "_golem_outbox_delivery_forged"
	if _, err := Diff(before, forged); err == nil {
		t.Fatal("non-registry delivery shape was accepted as the P7 platform upgrade")
	}
}
