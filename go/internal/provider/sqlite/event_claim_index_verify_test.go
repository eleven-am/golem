package sqlite_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

func releasedV053Schema(admitted physical.PhysicalSchema) physical.PhysicalSchema {
	released := admitted
	released.Unmanaged = nil
	for _, object := range admitted.Unmanaged {
		if object.Name != physical.OutboxDeliveryClaimIndex {
			released.Unmanaged = append(released.Unmanaged, object)
		}
	}
	return released
}

func admittedClaimIndexSchema(t *testing.T) physical.PhysicalSchema {
	t.Helper()
	admitted := schematest.New(t).SQLite
	admitted.Unmanaged = append(physical.QueueUnmanagedObjects(), physical.OutboxDeliveryUnmanagedObjects()...)
	return admitted
}

func openClaimIndexDatabase(t *testing.T, schema physical.PhysicalSchema) *sqlx.DB {
	t.Helper()
	ctx := context.Background()
	database, _, err := sqlite.New().Open(ctx, filepath.Join(t.TempDir(), "claim-index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	return database
}

func recordCausation(t *testing.T, database *sqlx.DB) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO "_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES ('00000000-0000-4000-8000-000000000001',1,'c','g','m','created',x'01','00000000-0000-4000-8000-00000000000a',0,x'00',1)`); err != nil {
		t.Fatal(err)
	}
}

func deliverOnce(t *testing.T, coordinator eventprovider.Coordinator) {
	t.Helper()
	ctx := context.Background()
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 4, LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 {
		t.Fatalf("claimed %d groups, want 1", len(leases))
	}
	acknowledged, err := coordinator.Acknowledge(ctx, leases[0].Delivery.CausationID, leases[0].Delivery.LeaseToken)
	if err != nil || !acknowledged {
		t.Fatalf("acknowledge=%v err=%v", acknowledged, err)
	}
}

func claimIndexCount(t *testing.T, database *sqlx.DB) int {
	t.Helper()
	var count int
	if err := database.Get(&count, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='golem_outbox_delivery_claim'`); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSQLiteReleasedV053DatabaseOpensAndDeliversUnchanged(t *testing.T) {
	released := releasedV053Schema(admittedClaimIndexSchema(t))
	database := openClaimIndexDatabase(t, released)
	if err := sqlite.New().Verify(context.Background(), database, released); err != nil {
		t.Fatalf("a go/v0.5.3 database failed verification: %v", err)
	}
	recordCausation(t, database)
	coordinator, err := sqlite.New().EventCoordinatorAdmitting(database, released.Unmanaged)
	if err != nil {
		t.Fatal(err)
	}
	deliverOnce(t, coordinator)
	if claimIndexCount(t, database) != 0 {
		t.Fatal("upgrading the library alone created the claim index")
	}
	if err := sqlite.New().Verify(context.Background(), database, released); err != nil {
		t.Fatalf("a go/v0.5.3 database failed verification after delivering: %v", err)
	}
}

func TestSQLiteAdmittedSchemaVerifiesWithAndWithoutTheClaimIndex(t *testing.T) {
	admitted := admittedClaimIndexSchema(t)
	database := openClaimIndexDatabase(t, admitted)
	for _, surface := range []struct {
		name  string
		check func() error
	}{
		{name: "startup", check: func() error { return sqlite.New().Verify(context.Background(), database, admitted) }},
		{name: "doctor", check: func() error {
			_, err := sqlite.New().Introspect(context.Background(), database, admitted)
			return err
		}},
	} {
		if err := surface.check(); err != nil {
			t.Fatalf("%s refused an admitted schema without the claim index: %v", surface.name, err)
		}
	}
	recordCausation(t, database)
	coordinator, err := sqlite.New().EventCoordinatorAdmitting(database, admitted.Unmanaged)
	if err != nil {
		t.Fatal(err)
	}
	deliverOnce(t, coordinator)
	if claimIndexCount(t, database) != 1 {
		t.Fatal("an admitted coordinator did not create the claim index")
	}
	if err := sqlite.New().Verify(context.Background(), database, admitted); err != nil {
		t.Fatalf("startup refused golem's own claim index: %v", err)
	}
	if _, err := sqlite.New().Introspect(context.Background(), database, admitted); err != nil {
		t.Fatalf("doctor refused golem's own claim index: %v", err)
	}
}

func TestSQLiteClaimIndexVerificationRefusesWrongAndForeignIndexes(t *testing.T) {
	admitted := admittedClaimIndexSchema(t)
	for _, probe := range []struct {
		name   string
		schema physical.PhysicalSchema
		ddl    string
		want   string
	}{
		{name: "wrong definition", schema: admitted, ddl: `CREATE INDEX "golem_outbox_delivery_claim" ON "_golem_outbox_delivery" ("first_recorded_at","causation_id")`, want: "golem_outbox_delivery_claim"},
		{name: "foreign index", schema: admitted, ddl: `CREATE INDEX "delivery_by_status" ON "_golem_outbox_delivery" ("status")`, want: "delivery_by_status"},
		{name: "claim index under a v0.5.3 schema", schema: releasedV053Schema(admitted), ddl: `CREATE INDEX "golem_outbox_delivery_claim" ON "_golem_outbox_delivery" ("first_recorded_at","causation_id","available_at","lease_until") WHERE "status" IN ('pending','leased')`, want: "golem_outbox_delivery_claim"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			database := openClaimIndexDatabase(t, probe.schema)
			if _, err := database.Exec(probe.ddl); err != nil {
				t.Fatal(err)
			}
			err := sqlite.New().Verify(context.Background(), database, probe.schema)
			if err == nil || !strings.Contains(err.Error(), probe.want) {
				t.Fatalf("startup accepted it: %v", err)
			}
			if _, err := sqlite.New().Introspect(context.Background(), database, probe.schema); err == nil || !strings.Contains(err.Error(), probe.want) {
				t.Fatalf("doctor accepted it: %v", err)
			}
		})
	}
}
