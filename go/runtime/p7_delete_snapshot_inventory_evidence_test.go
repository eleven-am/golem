package runtime

import (
	"context"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// TestP7DeleteSnapshotDependencyInventoryAndEveryMutationPath crosses the
// production runtime boundary instead of merely inspecting planner inputs. It
// executes caller/system root, batch, and nested deletes and then independently
// checks every durable delete fact against the compiler-owned event inventory.
func TestP7DeleteSnapshotDependencyInventoryAndEveryMutationPath(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{}, assertP7DeleteSnapshotMutationPaths)
}

func assertP7DeleteSnapshotMutationPaths(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	ctx := context.Background()
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	userTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
	postTarget := func(id byte) golem.MutationTarget[mutationResultPost] {
		return golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
	}
	nestedUpdate := func(label string, nested golem.NestedUpdateValue[mutationResultUser]) golem.UpdateInput[mutationResultUser] {
		return golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, label), nested)
	}
	seed := func(ids ...byte) {
		t.Helper()
		for _, id := range ids {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "private-delete-dependency")); err != nil {
				t.Fatal(err)
			}
		}
	}
	clear := func() {
		t.Helper()
		if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
			t.Fatal(err)
		}
	}
	run := func(name string, ids []byte, mutate func() error) {
		t.Helper()
		seed(ids...)
		clear()
		if err := mutate(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		assertP7DurableDeleteSnapshots(t, fixture, len(ids))
	}

	run("caller-root", []byte{171}, func() error {
		_, err := CallerDelete(ctx, caller, fixture.postDescriptor, fixture.target(171))
		return err
	})
	run("system-root", []byte{172}, func() error {
		_, err := SystemDelete(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(172))
		return err
	})
	run("caller-batch", []byte{173, 174}, func() error {
		count, err := CallerDeleteMany(ctx, caller, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 173}, golem.UUID{15: 174}))
		if err == nil && count != 2 {
			t.Fatalf("caller batch count=%d", count)
		}
		return err
	})
	run("system-batch", []byte{175, 176}, func() error {
		count, err := SystemDeleteMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 175}, golem.UUID{15: 176}))
		if err == nil && count != 2 {
			t.Fatalf("system batch count=%d", count)
		}
		return err
	})
	run("caller-nested-delete", []byte{177}, func() error {
		value := golem.GeneratedNestedDelete[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(177))
		_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget, nestedUpdate("caller-nested-delete", value))
		return err
	})
	run("system-nested-delete", []byte{178}, func() error {
		value := golem.GeneratedNestedDelete[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(178))
		_, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, userTarget, nestedUpdate("system-nested-delete", value))
		return err
	})
	run("caller-nested-delete-many", []byte{179, 180}, func() error {
		value := golem.GeneratedNestedDeleteMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post,
			fixture.postID.In(golem.UUID{15: 179}, golem.UUID{15: 180}))
		_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget, nestedUpdate("caller-nested-delete-many", value))
		return err
	})
	run("system-nested-delete-many", []byte{181, 182}, func() error {
		value := golem.GeneratedNestedDeleteMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post,
			fixture.postID.In(golem.UUID{15: 181}, golem.UUID{15: 182}))
		_, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, userTarget, nestedUpdate("system-nested-delete-many", value))
		return err
	})
}

func assertP7DurableDeleteSnapshots(t testing.TB, fixture mutationResultFixture, want int) {
	t.Helper()
	type storedFact struct {
		ModelID        string `db:"model_id"`
		Action         string `db:"action"`
		Metadata       []byte `db:"metadata"`
		DeleteSnapshot []byte `db:"delete_snapshot"`
	}
	var stored []storedFact
	query := `SELECT "model_id","action","metadata","delete_snapshot" FROM ` + nestedAcceptanceOutbox(fixture.app) + ` WHERE "action"='deleted' ORDER BY "transaction_ordinal"`
	if err := fixture.app.database.SelectContext(context.Background(), &stored, query); err != nil {
		t.Fatal(err)
	}
	if len(stored) != want {
		t.Fatalf("durable delete facts=%d want=%d", len(stored), want)
	}
	model, ok := fixture.schema.Registry.Model(fixture.schema.Post)
	if !ok {
		t.Fatal("Post registry model is absent")
	}
	_, inventory, ok := model.EventSchema()
	if !ok || len(inventory) == 0 {
		t.Fatal("compiler-owned Post delete inventory is absent")
	}
	wantFields := make([]policyir.FieldID, len(inventory))
	for index, field := range inventory {
		wantFields[index] = policyir.FieldID(field)
	}
	for index, row := range stored {
		envelope, err := decodeCurrentMutationFact(fixture.schema.Registry, policyir.ModelID(fixture.schema.Post), row.Metadata, row.DeleteSnapshot)
		if err != nil {
			t.Fatalf("fact %d: %v", index, err)
		}
		if envelope.Action() != mutationir.FactDeleted || envelope.DeleteSnapshotState() != mutationir.DeleteSnapshotStoredScalars {
			t.Fatalf("fact %d action/state=%d/%d", index, envelope.Action(), envelope.DeleteSnapshotState())
		}
		snapshot, present := envelope.PrivateDeleteSnapshot()
		if !present {
			t.Fatalf("fact %d has no private snapshot", index)
		}
		complete, err := snapshot.IsComplete(fixture.schema.Registry)
		if err != nil || !complete {
			t.Fatalf("fact %d snapshot complete=%t err=%v", index, complete, err)
		}
		gotFields := make([]policyir.FieldID, 0, len(snapshot.Cells()))
		for _, cell := range snapshot.Cells() {
			gotFields = append(gotFields, cell.FieldID())
		}
		if !reflect.DeepEqual(gotFields, wantFields) && !sameP7FieldSet(gotFields, wantFields) {
			t.Fatalf("fact %d snapshot fields=%x want=%x", index, gotFields, wantFields)
		}
		if _, err := mutationfact.DecodeDeleteSnapshot(row.DeleteSnapshot, fixture.schema.Registry); err != nil {
			t.Fatalf("fact %d standalone private snapshot: %v", index, err)
		}
	}
}

func sameP7FieldSet(left, right []policyir.FieldID) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[policyir.FieldID]int, len(left))
	for _, field := range left {
		values[field]++
	}
	for _, field := range right {
		values[field]--
	}
	for _, count := range values {
		if count != 0 {
			return false
		}
	}
	return true
}
