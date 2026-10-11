package runtime

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func runNestedOrderAcrossProviders(t *testing.T, assert func(t *testing.T, fixture socialMutationFixture)) {
	t.Run("sqlite", func(t *testing.T) {
		assert(t, newSocialMutationFixture(t, golem.ModelID{}, nil))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			assert(t, newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil))
		})
	}
}

func seedNestedOrderGraph(t *testing.T, fixture socialMutationFixture) *Caller[graphMutationPrincipal, graphMutationActor] {
	t.Helper()
	ctx := context.Background()
	system := fixture.app.System()
	for _, user := range []golem.CreateInput[socialMutationUser]{fixture.userCreate(1, "owner"), fixture.userCreate(2, "friend")} {
		if _, err := SystemCreate(ctx, system, fixture.userDescriptor, user); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.postRootCreate(10, 1, "existing")); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []golem.CreateInput[socialMutationTag]{fixture.tagCreate(30, "go"), fixture.tagCreate(31, "rust")} {
		if _, err := SystemCreate(ctx, system, fixture.tagDescriptor, tag); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []byte{20, 21, 22} {
		comment := golem.GeneratedCreateInput[socialMutationComment](fixture.schema.Comment,
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedEqualField[socialMutationComment, golem.UUID](fixture.schema.CommentPostID), golem.UUID{15: 10}),
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedEqualField[socialMutationComment, golem.UUID](fixture.schema.CommentAuthorID), golem.UUID{15: 1}),
			golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentBody, "comment"))
		if _, err := SystemCreate(ctx, system, fixture.commentDescriptor, comment); err != nil {
			t.Fatal(err)
		}
	}
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func (fixture socialMutationFixture) relinkPostTag(name string) golem.UpdateInput[socialMutationPost] {
	create := golem.GeneratedCreateInput[socialMutationPostTag](fixture.schema.PostTag,
		golem.GeneratedCreateFieldValue(fixture.schema.PostTag, fixture.postTagTagName, name))
	return golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post,
		golem.GeneratedNestedCreate[socialMutationPost, socialMutationPostTag](fixture.schema.Post, fixture.schema.PostPostTags, fixture.schema.PostTagPostRelation, fixture.schema.PostTag, create),
		golem.GeneratedNestedDeleteMany[socialMutationPost, socialMutationPostTag](fixture.schema.Post, fixture.schema.PostPostTags, fixture.schema.PostTagPostRelation, fixture.schema.PostTag, fixture.postTagTagName.Eq(name)))
}

func nestedOrderPostTagNames(t *testing.T, fixture socialMutationFixture, post byte) []string {
	t.Helper()
	var names []string
	query := fixture.app.database.Rebind(`SELECT "tag_name" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.PostTag) + ` WHERE "post_id"=? ORDER BY "tag_name"`)
	if err := fixture.app.database.SelectContext(context.Background(), &names, query, mutationResultUUIDText(post)); err != nil {
		t.Fatal(err)
	}
	return names
}

func nestedOrderReplyParents(t *testing.T, fixture socialMutationFixture) map[string]string {
	t.Helper()
	type row struct {
		ID     string  `db:"id"`
		Parent *string `db:"parent_id"`
	}
	var rows []row
	if err := fixture.app.database.SelectContext(context.Background(), &rows, `SELECT "id","parent_id" FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(rows))
	for _, value := range rows {
		parent := ""
		if value.Parent != nil {
			parent = *value.Parent
		}
		result[value.ID] = parent
	}
	return result
}

func TestNestedRelinkDeleteManyAndCreateInOneUpdateAcrossProviders(t *testing.T) {
	runNestedOrderAcrossProviders(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		caller := seedNestedOrderGraph(t, fixture)
		existing := golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post,
			golem.GeneratedNestedCreate[socialMutationPost, socialMutationPostTag](fixture.schema.Post, fixture.schema.PostPostTags, fixture.schema.PostTagPostRelation, fixture.schema.PostTag,
				golem.GeneratedCreateInput[socialMutationPostTag](fixture.schema.PostTag, golem.GeneratedCreateFieldValue(fixture.schema.PostTag, fixture.postTagTagName, "rust"))))
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(10), existing); err != nil {
			t.Fatalf("seed existing link: %v", err)
		}
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(10), fixture.relinkPostTag("rust")); err != nil {
			t.Errorf("relink existing link: %v", err)
		}
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(10), fixture.relinkPostTag("go")); err != nil {
			t.Errorf("relink absent link: %v", err)
		}
		if names := nestedOrderPostTagNames(t, fixture, 10); len(names) != 2 || names[0] != "go" || names[1] != "rust" {
			t.Fatalf("after relinking tags=%v want [go rust]", names)
		}
	})
}

func TestNestedCreateIsNotTouchedBySiblingUpdateManyAcrossProviders(t *testing.T) {
	runNestedOrderAcrossProviders(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		caller := seedNestedOrderGraph(t, fixture)
		input := golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User,
			golem.GeneratedNestedCreate[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post, fixture.postCreate(60, "fresh")),
			golem.GeneratedNestedUpdateMany[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post, fixture.postID.In(golem.UUID{15: 10}, golem.UUID{15: 60}), fixture.postUpdateMany("bulk")))
		if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, fixture.userTarget(1), input); err != nil {
			t.Fatalf("create with sibling updateMany: %v", err)
		}
		titles := map[byte]string{}
		for _, id := range []byte{10, 60} {
			var title string
			query := fixture.app.database.Rebind(`SELECT "title" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
			if err := fixture.app.database.GetContext(ctx, &title, query, mutationResultUUIDText(id)); err != nil {
				t.Fatal(err)
			}
			titles[id] = title
		}
		if titles[10] != "bulk" || titles[60] != "fresh" {
			t.Fatalf("titles=%v want existing post bulk-updated and created post untouched", titles)
		}
	})
}

func TestNestedSetAndDisconnectRunBeforeConnectAcrossProviders(t *testing.T) {
	runNestedOrderAcrossProviders(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		caller := seedNestedOrderGraph(t, fixture)
		replies := func(operations ...golem.UpdateValue[socialMutationComment]) golem.UpdateInput[socialMutationComment] {
			return golem.GeneratedUpdateInput[socialMutationComment](fixture.schema.Comment, operations...)
		}
		connect := golem.GeneratedNestedConnect[socialMutationComment, socialMutationComment](fixture.schema.Comment, fixture.schema.CommentReplies, fixture.schema.CommentThreading, fixture.schema.Comment, fixture.commentTarget(22))
		set := golem.GeneratedNestedSet[socialMutationComment, socialMutationComment](fixture.schema.Comment, fixture.schema.CommentReplies, fixture.schema.CommentThreading, fixture.schema.Comment, fixture.commentTarget(21))
		if _, err := CallerUpdate(ctx, caller, fixture.commentDescriptor, fixture.commentTarget(20), replies(connect, set)); err != nil {
			t.Fatalf("connect with set: %v", err)
		}
		parent := mutationResultUUIDText(20)
		if parents := nestedOrderReplyParents(t, fixture); parents[mutationResultUUIDText(21)] != parent || parents[mutationResultUUIDText(22)] != parent {
			t.Fatalf("after set+connect parents=%v want replies 21 and 22 under 20", parents)
		}
		disconnect := golem.GeneratedNestedDisconnect[socialMutationComment, socialMutationComment](fixture.schema.Comment, fixture.schema.CommentReplies, fixture.schema.CommentThreading, fixture.schema.Comment, fixture.commentTarget(22))
		if _, err := CallerUpdate(ctx, caller, fixture.commentDescriptor, fixture.commentTarget(20), replies(connect, disconnect)); err != nil {
			t.Fatalf("connect with disconnect: %v", err)
		}
		if parents := nestedOrderReplyParents(t, fixture); parents[mutationResultUUIDText(21)] != parent || parents[mutationResultUUIDText(22)] != parent {
			t.Fatalf("after disconnect+connect parents=%v want reply 22 still under 20", parents)
		}
	})
}

func TestNestedRowRemovedBeforeFinalVerificationIsNotReportedForbiddenAcrossProviders(t *testing.T) {
	runNestedOrderAcrossProviders(t, func(t *testing.T, fixture socialMutationFixture) {
		ctx := context.Background()
		caller := seedNestedOrderGraph(t, fixture)
		createField, createRelation, deleteField, deleteRelation := fixture.schema.UserFriendshipsFrom, fixture.schema.FriendshipOrigin, fixture.schema.UserFriendshipsTo, fixture.schema.FriendshipDestination
		selfLink := golem.GeneratedCreateInput[socialMutationFriendship](fixture.schema.Friendship,
			golem.GeneratedCreateFieldValue(fixture.schema.Friendship, fixture.friendshipFriendID, golem.UUID{15: 1}))
		if bytes.Compare(createField[:], deleteField[:]) > 0 {
			createField, createRelation, deleteField, deleteRelation = deleteField, deleteRelation, createField, createRelation
			selfLink = golem.GeneratedCreateInput[socialMutationFriendship](fixture.schema.Friendship,
				golem.GeneratedCreateFieldValue(fixture.schema.Friendship, fixture.friendshipUserID, golem.UUID{15: 1}))
		}
		input := golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User,
			golem.GeneratedNestedCreate[socialMutationUser, socialMutationFriendship](fixture.schema.User, createField, createRelation, fixture.schema.Friendship, selfLink),
			golem.GeneratedNestedDeleteMany[socialMutationUser, socialMutationFriendship](fixture.schema.User, deleteField, deleteRelation, fixture.schema.Friendship, fixture.friendshipUserID.Eq(golem.UUID{15: 1})))
		_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, fixture.userTarget(1), input)
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeConflict || !strings.Contains(failure.Message, "removed") {
			t.Fatalf("created row removed by a sibling relation err=%v want CONFLICT naming the removal", err)
		}
		var count int
		if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Friendship)); err != nil || count != 0 {
			t.Fatalf("friendships after rollback=%d err=%v", count, err)
		}
	})
}
