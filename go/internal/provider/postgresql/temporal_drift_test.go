package postgresql

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func TestCatalogTemporalStorageRequiresExactTimeZoneSpelling(t *testing.T) {
	for _, testCase := range []struct {
		text      string
		kind      physical.StorageKind
		precision uint32
	}{
		{text: "time(6) without time zone", kind: physical.StoragePostgreSQLTime, precision: 6},
		{text: "time(0) without time zone", kind: physical.StoragePostgreSQLTime, precision: 0},
		{text: "timestamp(3) with time zone", kind: physical.StoragePostgreSQLTimestampTZ, precision: 3},
	} {
		storage, err := parseCatalogStorage(testCase.text)
		if err != nil || storage.Kind != testCase.kind || storage.Length != testCase.precision {
			t.Fatalf("parseCatalogStorage(%q) = %#v, %v", testCase.text, storage, err)
		}
	}
	for _, text := range []string{"timestamp(6) without time zone", "time(6) with time zone", "timestamp without time zone", "timestamp(6)", "time(6)", "timestamp(6) with time zone extra", "timestamp(x) with time zone"} {
		if storage, err := parseCatalogStorage(text); err == nil {
			t.Fatalf("parseCatalogStorage(%q) = %#v, want refusal", text, storage)
		}
	}
}

func TestLiveVerifyDetectsTemporalColumnLosingTimeZone(t *testing.T) {
	dsn := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
	provider := New()
	db, _, err := provider.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := provider.Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "golem_tz_drift"})
	if err != nil {
		t.Fatal(err)
	}
	if err = provider.ApplyInitial(context.Background(), db, schema); err != nil {
		t.Fatal(err)
	}
	if err = provider.Verify(context.Background(), db, schema); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ drift, restore string }{
		{drift: `ALTER TABLE "golem_tz_drift"."posts" ALTER COLUMN "created_at" TYPE timestamp(6) without time zone`, restore: `ALTER TABLE "golem_tz_drift"."posts" ALTER COLUMN "created_at" TYPE timestamp(6) with time zone`},
		{drift: `ALTER TABLE "golem_tz_drift"."posts" ALTER COLUMN "clock" TYPE time(6) with time zone`, restore: `ALTER TABLE "golem_tz_drift"."posts" ALTER COLUMN "clock" TYPE time(6) without time zone`},
	} {
		if _, err = db.Exec(change.drift); err != nil {
			t.Fatal(err)
		}
		if err = provider.Verify(context.Background(), db, schema); err == nil {
			t.Fatalf("%s verified as current", change.drift)
		}
		if _, err = db.Exec(change.restore); err != nil {
			t.Fatal(err)
		}
		if err = provider.Verify(context.Background(), db, schema); err != nil {
			t.Fatalf("after restore: %v", err)
		}
	}
}
