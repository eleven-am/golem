package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type cascadeFact struct {
	action string
	post   byte
}

func TestDeleteCascadeEmitsADeleteFactForEveryRemovedDependentAcrossProviders(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "cascade_facts", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, compilerir.ActionCascade)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(compilerir.ActionCascade), func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		seedCascadeUser(t, fixture, 3, "carol")
		for _, post := range []struct{ id, author byte }{{10, 2}, {11, 2}, {12, 1}, {13, 3}, {14, 3}} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post.id, golem.UUID{15: post.author}, "cascade")); err != nil {
				t.Fatal(err)
			}
		}
		clearCascadeOutbox(t, profile)
		caller := mustMutationResultCaller(t, fixture)
		if _, err := CallerDelete(ctx, caller, fixture.userDescriptor, cascadeUserTarget(fixture, 2)); err != nil {
			t.Fatalf("delete a parent with cascaded dependents: %v", err)
		}
		assertCascadeFacts(t, profile, []cascadeFact{{"deleted", 10}, {"deleted", 11}})
		assertCascadePosts(t, profile, map[byte]string{12: mutationResultUUIDText(1), 13: mutationResultUUIDText(3), 14: mutationResultUUIDText(3)})

		clearCascadeOutbox(t, profile)
		if count, err := CallerDeleteMany(ctx, caller, fixture.userDescriptor, fixture.userID.In(golem.UUID{15: 1}, golem.UUID{15: 3})); err != nil || count != 2 {
			t.Fatalf("delete-many parents count=%d err=%v", count, err)
		}
		assertCascadeFacts(t, profile, []cascadeFact{{"deleted", 12}, {"deleted", 13}, {"deleted", 14}})
		assertCascadePosts(t, profile, map[byte]string{})
	})
}

func TestDeleteSetNullEmitsAnUpdateFactForEveryClearedReferenceAcrossProviders(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "setnull_facts", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, compilerir.ActionSetNull)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(compilerir.ActionSetNull), func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		for _, post := range []struct{ id, author byte }{{20, 2}, {21, 2}, {22, 1}} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post.id, golem.UUID{15: post.author}, "set-null")); err != nil {
				t.Fatal(err)
			}
		}
		clearCascadeOutbox(t, profile)
		if _, err := SystemDelete(ctx, fixture.app.System(), fixture.userDescriptor, cascadeUserTarget(fixture, 2)); err != nil {
			t.Fatalf("system delete a parent with set-null dependents: %v", err)
		}
		assertCascadeFacts(t, profile, []cascadeFact{{"updated", 20}, {"updated", 21}})
		assertCascadePosts(t, profile, map[byte]string{20: "", 21: "", 22: mutationResultUUIDText(1)})
	})
}

func TestDeleteCascadeRollsBackAndIsBoundedAcrossProviders(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "cascade_bound", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, compilerir.ActionCascade)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(compilerir.ActionCascade), func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		for _, id := range []byte{30, 31, 32} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 2}, "bounded")); err != nil {
				t.Fatal(err)
			}
		}
		clearCascadeOutbox(t, profile)
		caller := mustMutationResultCaller(t, fixture)
		rollback := errors.New("roll back the cascade")
		err := CallerTransaction(ctx, caller, func(tx *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, err := CallerTxDelete(ctx, tx, fixture.userDescriptor, cascadeUserTarget(fixture, 2)); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("rolled back transaction err=%v", err)
		}
		assertCascadeFacts(t, profile, nil)
		assertCascadePosts(t, profile, map[byte]string{30: mutationResultUUIDText(2), 31: mutationResultUUIDText(2), 32: mutationResultUUIDText(2)})

		bounded := reopenMutationResultWithLimits(t, fixture, MutationLimits{MaxTouchedRows: 2})
		_, err = CallerDelete(ctx, mustMutationResultCaller(t, bounded), bounded.userDescriptor, cascadeUserTarget(bounded, 2))
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || failure.Message != "mutation exceeds the configured row limit" {
			t.Fatalf("cascade beyond the touched-row limit failure=%#v err=%v", failure, err)
		}
		_, err = CallerDeleteMany(ctx, mustMutationResultCaller(t, bounded), bounded.userDescriptor, bounded.userID.Eq(golem.UUID{15: 2}))
		if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || failure.Message != "batch mutation exceeds the configured row limit" {
			t.Fatalf("batch cascade beyond the touched-row limit failure=%#v err=%v", failure, err)
		}
		assertCascadeFacts(t, profile, nil)
		assertCascadePosts(t, profile, map[byte]string{30: mutationResultUUIDText(2), 31: mutationResultUUIDText(2), 32: mutationResultUUIDText(2)})
	})
}

func seedCascadeUser(t *testing.T, fixture mutationResultFixture, id byte, name string) {
	t.Helper()
	input := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, name))
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.userDescriptor, input); err != nil {
		t.Fatal(err)
	}
}

func cascadeUserTarget(fixture mutationResultFixture, id byte) golem.MutationTarget[mutationResultUser] {
	return golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: id}))
}

func clearCascadeOutbox(t *testing.T, profile mutationProviderAcceptanceFixture) {
	t.Helper()
	if _, err := profile.fixture.app.database.Exec(`DELETE FROM ` + nestedAcceptanceOutboxDelivery(profile.fixture.app)); err != nil {
		t.Fatal(err)
	}
	if _, err := profile.fixture.app.database.Exec(`DELETE FROM ` + profile.outbox); err != nil {
		t.Fatal(err)
	}
}

func assertCascadeFacts(t *testing.T, profile mutationProviderAcceptanceFixture, want []cascadeFact) {
	t.Helper()
	var stored []struct {
		ModelID        string `db:"model_id"`
		Action         string `db:"action"`
		BeforeIdentity []byte `db:"before_identity"`
		AfterIdentity  []byte `db:"after_identity"`
		Metadata       []byte `db:"metadata"`
		DeleteSnapshot []byte `db:"delete_snapshot"`
	}
	if err := profile.fixture.app.database.Select(&stored, `SELECT "model_id", "action", "before_identity", "after_identity", "metadata", "delete_snapshot" FROM `+profile.outbox); err != nil {
		t.Fatal(err)
	}
	rows := make([]mutationfact.OutboxRow, len(stored))
	for index, value := range stored {
		rows[index] = mutationfact.OutboxRow{ModelID: value.ModelID, Action: value.Action, BeforeIdentity: value.BeforeIdentity, AfterIdentity: value.AfterIdentity, Metadata: value.Metadata, DeleteSnapshot: value.DeleteSnapshot}
	}
	post := profile.fixture.schema.Post
	var got []cascadeFact
	for _, row := range rows {
		if !strings.EqualFold(row.ModelID, hex.EncodeToString(post[:])) {
			t.Fatalf("unexpected fact for model %s action %s", row.ModelID, row.Action)
		}
		if _, err := decodeRuntimeMutationFact(profile.fixture.app.registry, row); err != nil {
			t.Fatalf("cascade fact does not decode: %v", err)
		}
		payload := row.BeforeIdentity
		if row.Action != "deleted" && len(payload) == 0 {
			payload = row.AfterIdentity
		}
		identity, err := mutationfact.DecodeIdentity(payload)
		if err != nil || len(identity.Components()) != 1 {
			t.Fatalf("cascade fact identity err=%v", err)
		}
		value, _ := identity.Components()[0].PolicyValue()
		id, ok := value.UUID()
		if !ok {
			t.Fatal("cascade fact identity is not a UUID")
		}
		got = append(got, cascadeFact{action: row.Action, post: id[15]})
	}
	sort.Slice(got, func(i, j int) bool { return got[i].post < got[j].post })
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("cascade facts=%v want=%v", got, want)
	}
}

func assertCascadePosts(t *testing.T, profile mutationProviderAcceptanceFixture, want map[byte]string) {
	t.Helper()
	rows, err := profile.fixture.app.database.Query(`SELECT "id", "author_id" FROM ` + profile.posts)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[byte]string{}
	for rows.Next() {
		var id string
		var author *string
		if err := rows.Scan(&id, &author); err != nil {
			t.Fatal(err)
		}
		decoded, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
		if err != nil || len(decoded) != 16 {
			t.Fatalf("post id %q", id)
		}
		value := ""
		if author != nil {
			value = *author
		}
		got[decoded[15]] = value
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("posts=%v want=%v", got, want)
	}
}

func reopenMutationResultWithLimits(t *testing.T, fixture mutationResultFixture, limits MutationLimits) mutationResultFixture {
	t.Helper()
	provider := golem.SQLite
	if fixture.app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		Database: p8RuntimeTestDatabase(fixture.app.database, provider), Bundle: fixture.schema.Bundle, Bindings: fixture.app.bindings, Descriptors: fixture.app.descriptors,
		MutationLimits: limits, ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}
