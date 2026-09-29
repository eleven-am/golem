package postgresql_test

import (
	"context"
	"strings"
	"testing"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	providerdrift "github.com/eleven-am/golem/go/internal/provider/drift"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

func admittedPostgreSQLClaimIndexSchema(t *testing.T) physical.PhysicalSchema {
	t.Helper()
	admitted := schematest.New(t).PostgreSQL
	admitted.Unmanaged = append(physical.QueueUnmanagedObjects(), physical.OutboxDeliveryUnmanagedObjects()...)
	return admitted
}

func releasedPostgreSQLV053Schema(admitted physical.PhysicalSchema) physical.PhysicalSchema {
	released := admitted
	released.Unmanaged = nil
	for _, object := range admitted.Unmanaged {
		if object.Name != physical.OutboxDeliveryClaimIndex {
			released.Unmanaged = append(released.Unmanaged, object)
		}
	}
	return released
}

func openPostgreSQLClaimIndexDatabase(t *testing.T, schema physical.PhysicalSchema) *sqlx.DB {
	t.Helper()
	ctx := context.Background()
	database, _, err := postgresprovider.New().Open(ctx, testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := postgresprovider.New().ApplyInitial(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	return database
}

func deliveryTable(schema physical.PhysicalSchema) string {
	return `"` + string(schema.System.Namespace.Name) + `"."_golem_outbox_delivery"`
}

func recordPostgreSQLCausation(t *testing.T, database *sqlx.DB, schema physical.PhysicalSchema) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO "` + string(schema.System.Namespace.Name) + `"."_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES ('00000000-0000-4000-8000-000000000001',1,'c','g','m','created','\x01','00000000-0000-4000-8000-00000000000a',0,'\x00',clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
}

func deliverPostgreSQLOnce(t *testing.T, coordinator eventprovider.Coordinator) {
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

func postgresqlClaimIndexCount(t *testing.T, database *sqlx.DB, schema physical.PhysicalSchema) int {
	t.Helper()
	var count int
	if err := database.Get(&count, `SELECT count(*) FROM pg_catalog.pg_indexes WHERE schemaname=$1 AND indexname='golem_outbox_delivery_claim'`, string(schema.System.Namespace.Name)); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPostgreSQLReleasedV053DatabaseOpensAndDeliversUnchanged(t *testing.T) {
	released := releasedPostgreSQLV053Schema(admittedPostgreSQLClaimIndexSchema(t))
	database := openPostgreSQLClaimIndexDatabase(t, released)
	if err := postgresprovider.New().Verify(context.Background(), database, released); err != nil {
		t.Fatalf("a go/v0.5.3 database failed verification: %v", err)
	}
	recordPostgreSQLCausation(t, database, released)
	coordinator, err := postgresprovider.New().EventCoordinatorAtAdmitting(database, released.System.Namespace.Name, released.Unmanaged)
	if err != nil {
		t.Fatal(err)
	}
	deliverPostgreSQLOnce(t, coordinator)
	if postgresqlClaimIndexCount(t, database, released) != 0 {
		t.Fatal("upgrading the library alone created the claim index")
	}
	if err := postgresprovider.New().Verify(context.Background(), database, released); err != nil {
		t.Fatalf("a go/v0.5.3 database failed verification after delivering: %v", err)
	}
}

func TestPostgreSQLAdmittedSchemaVerifiesWithAndWithoutTheClaimIndex(t *testing.T) {
	admitted := admittedPostgreSQLClaimIndexSchema(t)
	database := openPostgreSQLClaimIndexDatabase(t, admitted)
	if err := postgresprovider.New().Verify(context.Background(), database, admitted); err != nil {
		t.Fatalf("startup refused an admitted schema without the claim index: %v", err)
	}
	if _, err := postgresprovider.New().Introspect(context.Background(), database, admitted); err != nil {
		t.Fatalf("doctor refused an admitted schema without the claim index: %v", err)
	}
	recordPostgreSQLCausation(t, database, admitted)
	coordinator, err := postgresprovider.New().EventCoordinatorAtAdmitting(database, admitted.System.Namespace.Name, admitted.Unmanaged)
	if err != nil {
		t.Fatal(err)
	}
	deliverPostgreSQLOnce(t, coordinator)
	if postgresqlClaimIndexCount(t, database, admitted) != 1 {
		t.Fatal("an admitted coordinator did not create the claim index")
	}
	if err := postgresprovider.New().Verify(context.Background(), database, admitted); err != nil {
		t.Fatalf("startup refused golem's own claim index: %v", err)
	}
	if _, err := postgresprovider.New().Introspect(context.Background(), database, admitted); err != nil {
		t.Fatalf("doctor refused golem's own claim index: %v", err)
	}
}

func TestPostgreSQLClaimIndexVerificationRefusesWrongAndForeignIndexes(t *testing.T) {
	admitted := admittedPostgreSQLClaimIndexSchema(t)
	for _, probe := range []struct {
		name   string
		schema physical.PhysicalSchema
		ddl    string
		want   string
	}{
		{name: "wrong definition", schema: admitted, ddl: `CREATE INDEX "golem_outbox_delivery_claim" ON %s ("first_recorded_at","causation_id")`, want: "golem_outbox_delivery_claim"},
		{name: "foreign index", schema: admitted, ddl: `CREATE INDEX "delivery_by_status" ON %s ("status")`, want: "delivery_by_status"},
		{name: "claim index under a v0.5.3 schema", schema: releasedPostgreSQLV053Schema(admitted), ddl: `CREATE INDEX "golem_outbox_delivery_claim" ON %s ("first_recorded_at","causation_id","available_at","lease_until") WHERE "status" IN ('pending','leased')`, want: "golem_outbox_delivery_claim"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			database := openPostgreSQLClaimIndexDatabase(t, probe.schema)
			if _, err := database.Exec(strings.Replace(probe.ddl, "%s", deliveryTable(probe.schema), 1)); err != nil {
				t.Fatal(err)
			}
			err := postgresprovider.New().Verify(context.Background(), database, probe.schema)
			if object, ok := providerdrift.Inspect(err); !ok || object.Name != probe.want {
				t.Fatalf("startup did not refuse %s: %v", probe.want, err)
			}
			_, err = postgresprovider.New().Introspect(context.Background(), database, probe.schema)
			if object, ok := providerdrift.Inspect(err); !ok || object.Name != probe.want {
				t.Fatalf("doctor did not refuse %s: %v", probe.want, err)
			}
		})
	}
}
