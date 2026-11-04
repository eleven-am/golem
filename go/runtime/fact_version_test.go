package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestFactCarriesVersion(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{}, assertFactsCarryTheWrittenVersion)
}

func rowVersionOf(t testing.TB, fixture mutationResultFixture, id byte) int64 {
	t.Helper()
	var version int64
	query := fixture.app.database.Rebind(`SELECT "golem_version" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id" = ?`)
	if err := fixture.app.database.GetContext(context.Background(), &version, query, mutationResultUUIDText(id)); err != nil {
		t.Fatal(err)
	}
	return version
}

func lastFactVersion(t testing.TB, fixture mutationResultFixture, action string) int64 {
	t.Helper()
	var stored []deleteSnapshotStoredFact
	query := `SELECT "action","before_identity","after_identity","metadata","delete_snapshot" FROM ` + nestedAcceptanceOutbox(fixture.app) + ` WHERE "action" = '` + action + `' ORDER BY "recorded_at" DESC,"transaction_ordinal" DESC`
	if err := fixture.app.database.SelectContext(context.Background(), &stored, query); err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 {
		t.Fatalf("no %s fact was recorded", action)
	}
	envelope, err := decodeCurrentMutationFact(fixture.schema.Registry, policyir.ModelID(fixture.schema.Post), stored[0].Metadata, stored[0].DeleteSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	return envelope.RowVersion()
}

func assertFactsCarryTheWrittenVersion(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	ctx := context.Background()
	system := fixture.app.System()
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(91, golem.UUID{15: 1}, "versioned")); err != nil {
		t.Fatal(err)
	}
	created := rowVersionOf(t, fixture, 91)
	if created <= 0 {
		t.Fatalf("created row version = %d", created)
	}
	if got := lastFactVersion(t, fixture, "created"); got != created {
		t.Fatalf("created fact version = %d, row version = %d", got, created)
	}
	if _, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(91), fixture.updateTitle("versioned-again")); err != nil {
		t.Fatal(err)
	}
	updated := rowVersionOf(t, fixture, 91)
	if updated <= created {
		t.Fatalf("update did not raise the version: %d then %d", created, updated)
	}
	if got := lastFactVersion(t, fixture, "updated"); got != updated {
		t.Fatalf("updated fact version = %d, row version = %d", got, updated)
	}
	if count, err := SystemUpdateMany(ctx, system, fixture.postDescriptor, fixture.title.Eq("versioned-again"), fixture.updateManyTitle("versioned-batch")); err != nil || count != 1 {
		t.Fatalf("batch count=%d err=%v", count, err)
	}
	batched := rowVersionOf(t, fixture, 91)
	if batched <= updated {
		t.Fatalf("update-many did not raise the version: %d then %d", updated, batched)
	}
	if got := lastFactVersion(t, fixture, "updated"); got != batched {
		t.Fatalf("update-many fact version = %d, row version = %d", got, batched)
	}
	if _, err := SystemDelete(ctx, system, fixture.postDescriptor, fixture.target(91)); err != nil {
		t.Fatal(err)
	}
	if got := lastFactVersion(t, fixture, "deleted"); got != batched {
		t.Fatalf("deleted fact version = %d, want the last version %d", got, batched)
	}
}

func TestOversizeDeleteSnapshotStillCarriesTheVersion(t *testing.T) {
	ctx := context.Background()
	fixture := openMutationResultFixture(t, schematest.NewSubscribedIndexedWithFullDeleteSnapshot(t), MutationLimits{}, nil, nil, nil, true)
	system := fixture.app.System()
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(223, golem.UUID{15: 1}, strings.Repeat("h", 100*1024))); err != nil {
		t.Fatal(err)
	}
	last := rowVersionOf(t, fixture, 223)
	if _, err := SystemDelete(ctx, system, fixture.postDescriptor, fixture.target(223)); err != nil {
		t.Fatal(err)
	}
	var stored []deleteSnapshotStoredFact
	query := `SELECT "action","before_identity","after_identity","metadata","delete_snapshot" FROM ` + nestedAcceptanceOutbox(fixture.app) + ` WHERE "action" = 'deleted'`
	if err := fixture.app.database.SelectContext(ctx, &stored, query); err != nil || len(stored) != 1 {
		t.Fatalf("delete facts=%d err=%v", len(stored), err)
	}
	envelope, err := decodeCurrentMutationFact(fixture.schema.Registry, policyir.ModelID(fixture.schema.Post), stored[0].Metadata, stored[0].DeleteSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.DeleteSnapshotState() != mutationir.DeleteSnapshotUnverifiable || len(stored[0].DeleteSnapshot) != 0 {
		t.Fatalf("the snapshot was not dropped: state=%d bytes=%d", envelope.DeleteSnapshotState(), len(stored[0].DeleteSnapshot))
	}
	if envelope.RowVersion() != last {
		t.Fatalf("oversize delete fact version = %d, last row version = %d", envelope.RowVersion(), last)
	}
}
