package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestMutableSingleRowIdentityUpdateRebindsResultAndFactAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		oldID, newID := golem.UUID{15: 181}, golem.UUID{15: 182}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(181, golem.UUID{15: 1}, "before-identity")); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+profile.outbox); err != nil {
			t.Fatal(err)
		}
		input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postID, newID),
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "after-identity"),
		)
		caller := mustMutationResultCaller(t, fixture)
		row, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(181), input, golem.Select[mutationResultPost](fixture.postID, fixture.title))
		if err != nil {
			t.Fatal(err)
		}
		if id, ok := golem.Value(row, fixture.postID).Get(); !ok || id != newID {
			t.Fatalf("public after identity=%v present=%t", id, ok)
		}
		if title, ok := golem.Value(row, fixture.title).Get(); !ok || title != "after-identity" {
			t.Fatalf("public after title=%q present=%t", title, ok)
		}
		var oldRows, newRows int
		query := `SELECT COUNT(*) FROM ` + profile.posts + ` WHERE "id"=` + profile.placeholder(1)
		if err := fixture.app.database.GetContext(ctx, &oldRows, query, mutationResultUUIDText(181)); err != nil {
			t.Fatal(err)
		}
		if err := fixture.app.database.GetContext(ctx, &newRows, query, mutationResultUUIDText(182)); err != nil {
			t.Fatal(err)
		}
		if oldRows != 0 || newRows != 1 {
			t.Fatalf("persisted identity old=%d new=%d", oldRows, newRows)
		}
		var metadata []byte
		if err := fixture.app.database.GetContext(ctx, &metadata, `SELECT "metadata" FROM `+profile.outbox+` WHERE "action"='updated'`); err != nil {
			t.Fatal(err)
		}
		envelope, err := mutationfact.Decode(metadata, fixture.schema.Registry)
		if err != nil {
			t.Fatal(err)
		}
		before, beforeOK := envelope.BeforeIdentity()
		after, afterOK := envelope.AfterIdentity()
		if !beforeOK || !afterOK {
			t.Fatalf("fact identities before=%t after=%t", beforeOK, afterOK)
		}
		assertMutationUUIDIdentity(t, before, oldID)
		assertMutationUUIDIdentity(t, after, newID)
	})
}

func TestReferencedIdentityUpdateFailsBeforeWriteAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		oldID, newID := golem.UUID{15: 1}, golem.UUID{15: 191}
		target := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, oldID))
		input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userID, newID),
		)
		_, err := CallerUpdate(ctx, mustMutationResultCaller(t, fixture), fixture.userDescriptor, target, input)
		var public *golem.Error
		if !errors.As(err, &public) || public.Code != golem.CodeBadUserInput {
			t.Fatalf("referential identity failure=%#v err=%v", public, err)
		}
		users := nestedAcceptanceTable(fixture.app, fixture.schema.User)
		var oldRows, newRows int
		query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + users + ` WHERE "id"=?`)
		if err := fixture.app.database.GetContext(ctx, &oldRows, query, mutationResultUUIDText(1)); err != nil {
			t.Fatal(err)
		}
		if err := fixture.app.database.GetContext(ctx, &newRows, query, mutationResultUUIDText(191)); err != nil {
			t.Fatal(err)
		}
		if oldRows != 1 || newRows != 0 {
			t.Fatalf("referential refusal wrote old=%d new=%d", oldRows, newRows)
		}
	})
}

func assertMutationUUIDIdentity(t testing.TB, identity mutationdecode.Identity, want golem.UUID) {
	t.Helper()
	components := identity.Components()
	if len(components) != 1 {
		t.Fatalf("identity components=%d", len(components))
	}
	value, ok := components[0].PolicyValue()
	wantValue := policyir.UUIDValue([16]byte(want))
	if !ok || !mutationdecode.EqualValue(value, wantValue) {
		t.Fatalf("identity value=%#v present=%t want=%#v", value, ok, wantValue)
	}
}
