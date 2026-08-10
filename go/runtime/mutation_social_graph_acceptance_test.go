package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type socialMutationUser struct{}
type socialMutationPost struct{}
type socialMutationComment struct{}
type socialMutationFriendship struct{}
type socialMutationTag struct{}
type socialMutationPostTag struct{}

type socialMutationFixture struct {
	app    *App[graphMutationPrincipal, graphMutationActor]
	schema schematest.SocialMutationFixture

	userDescriptor       golem.ModelDescriptor[socialMutationUser]
	postDescriptor       golem.ModelDescriptor[socialMutationPost]
	commentDescriptor    golem.ModelDescriptor[socialMutationComment]
	friendshipDescriptor golem.ModelDescriptor[socialMutationFriendship]
	tagDescriptor        golem.ModelDescriptor[socialMutationTag]
	postTagDescriptor    golem.ModelDescriptor[socialMutationPostTag]

	userID             golem.EqualField[socialMutationUser, golem.UUID]
	userName           golem.TextField[socialMutationUser, string]
	postID             golem.EqualField[socialMutationPost, golem.UUID]
	postTitle          golem.TextField[socialMutationPost, string]
	commentID          golem.EqualField[socialMutationComment, golem.UUID]
	commentBody        golem.TextField[socialMutationComment, string]
	tagID              golem.EqualField[socialMutationTag, golem.UUID]
	tagName            golem.TextField[socialMutationTag, string]
	friendshipUserID   golem.EqualField[socialMutationFriendship, golem.UUID]
	friendshipFriendID golem.EqualField[socialMutationFriendship, golem.UUID]
	postTagPostID      golem.EqualField[socialMutationPostTag, golem.UUID]
	postTagTagName     golem.EqualField[socialMutationPostTag, string]
}

type socialHookLog struct {
	lock        sync.Mutex
	before      []string
	after       []string
	afterCommit []string
}

func (log *socialHookLog) append(target *[]string, model string) {
	log.lock.Lock()
	defer log.lock.Unlock()
	*target = append(*target, model)
}

func TestNestedMutationVocabularyExecutesCompleteSocialGraph(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		log := &socialHookLog{}
		assertCompleteSocialMutationGraph(t, newSocialMutationFixture(t, golem.ModelID{}, log), log)
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			log := &socialHookLog{}
			assertCompleteSocialMutationGraph(t, newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, log), log)
		})
	}
}

func TestCompleteSocialGraphNestedDenialRollsBackEveryDepthAcrossProviders(t *testing.T) {
	type denialCase struct {
		name      string
		model     func(schematest.SocialMutationFixture) golem.ModelID
		lateReply bool
	}
	cases := []denialCase{
		{name: "depth-0-root-user", model: func(schema schematest.SocialMutationFixture) golem.ModelID { return schema.User }},
		{name: "depth-1-post", model: func(schema schematest.SocialMutationFixture) golem.ModelID { return schema.Post }},
		{name: "depth-1-late-friendship", model: func(schema schematest.SocialMutationFixture) golem.ModelID { return schema.Friendship }},
		{name: "depth-2-comment", model: func(schema schematest.SocialMutationFixture) golem.ModelID { return schema.Comment }},
		{name: "depth-2-late-post-tag", model: func(schema schematest.SocialMutationFixture) golem.ModelID { return schema.PostTag }},
		{name: "depth-3-reply", lateReply: true},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Run("sqlite", func(t *testing.T) {
				fixture := newSocialMutationFixture(t, golem.ModelID{}, nil)
				if test.lateReply {
					fixture = reopenSocialMutationWithReplyDenied(t, fixture)
				} else {
					fixture = reopenSocialMutationWithCreateDenied(t, fixture, test.model(fixture.schema))
				}
				assertCompleteSocialDenialRollback(t, fixture)
			})
			for _, profile := range postgresAcceptanceProfiles() {
				profile := profile
				t.Run("postgresql-"+profile.name, func(t *testing.T) {
					if profile.dsn == "" {
						t.Skip(profile.env + " is not configured")
					}
					fixture := newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil)
					if test.lateReply {
						fixture = reopenSocialMutationWithReplyDenied(t, fixture)
					} else {
						fixture = reopenSocialMutationWithCreateDenied(t, fixture, test.model(fixture.schema))
					}
					assertCompleteSocialDenialRollback(t, fixture)
				})
			}
		})
	}
}

func assertCompleteSocialDenialRollback(t testing.TB, fixture socialMutationFixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.userCreate(2, "friend")); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.tagDescriptor, fixture.tagCreate(30, "deep-tag")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CallerCreate(ctx, caller, fixture.userDescriptor, fixture.deepSocialCreate())
	var public *golem.Error
	if !errors.As(err, &public) || public.Code != golem.CodeForbidden {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("social denial: %T: %v", cause, cause)
		}
		t.Fatalf("social denial=%#v err=%v", public, err)
	}
	assertSocialCounts(t, fixture, map[golem.ModelID]int{
		fixture.schema.User: 1, fixture.schema.Post: 0, fixture.schema.Comment: 0,
		fixture.schema.Friendship: 0, fixture.schema.Tag: 1, fixture.schema.PostTag: 0,
	}, 0)
}

func assertCompleteSocialMutationGraph(t testing.TB, fixture socialMutationFixture, log *socialHookLog) {
	t.Helper()
	ctx := context.Background()
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.userCreate(2, "friend")); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.tagDescriptor, fixture.tagCreate(30, "deep-tag")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	log.before, log.after, log.afterCommit = nil, nil, nil

	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(ctx, caller, fixture.userDescriptor, fixture.deepSocialCreate()); err != nil {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("complete social create: %T: %v", cause, cause)
		}
		t.Fatal(err)
	}

	assertSocialCounts(t, fixture, map[golem.ModelID]int{
		fixture.schema.User: 2, fixture.schema.Post: 1, fixture.schema.Comment: 2,
		fixture.schema.Friendship: 1, fixture.schema.Tag: 1, fixture.schema.PostTag: 1,
	}, 6)
	wantBefore := []string{"user", "post", "comment", "comment", "postTag", "friendship"}
	wantReverse := []string{"friendship", "postTag", "comment", "comment", "post", "user"}
	if !reflect.DeepEqual(log.before, wantBefore) || !reflect.DeepEqual(log.after, wantReverse) || !reflect.DeepEqual(log.afterCommit, wantReverse) {
		t.Fatalf("complete graph hooks before=%v after=%v afterCommit=%v", log.before, log.after, log.afterCommit)
	}
	assertSocialFactModels(t, fixture, []golem.ModelID{
		fixture.schema.User, fixture.schema.Post, fixture.schema.Comment, fixture.schema.Comment,
		fixture.schema.PostTag, fixture.schema.Friendship,
	})

	assertEverySocialRelationDirection(t, fixture, caller)
	assertEverySocialNestedOperation(t, fixture, caller)
}

func (fixture socialMutationFixture) deepSocialCreate() golem.CreateInput[socialMutationUser] {
	reply := golem.GeneratedCreateInput[socialMutationComment](fixture.schema.Comment,
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentID, golem.UUID{15: 21}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedEqualField[socialMutationComment, golem.UUID](fixture.schema.CommentPostID), golem.UUID{15: 10}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedEqualField[socialMutationComment, golem.UUID](fixture.schema.CommentAuthorID), golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentBody, "reply"),
	)
	comment := golem.GeneratedCreateInput[socialMutationComment](fixture.schema.Comment,
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentID, golem.UUID{15: 20}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedEqualField[socialMutationComment, golem.UUID](fixture.schema.CommentAuthorID), golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(fixture.schema.Comment, fixture.commentBody, "comment"),
		golem.GeneratedNestedCreate[socialMutationComment, socialMutationComment](fixture.schema.Comment, fixture.schema.CommentReplies, fixture.schema.CommentThreading, fixture.schema.Comment, reply),
	)
	postTag := golem.GeneratedCreateInput[socialMutationPostTag](fixture.schema.PostTag,
		golem.GeneratedCreateFieldValue(fixture.schema.PostTag, fixture.postTagTagName, "deep-tag"),
	)
	post := golem.GeneratedCreateInput[socialMutationPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 10}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "post"),
		golem.GeneratedNestedCreate[socialMutationPost, socialMutationComment](fixture.schema.Post, fixture.schema.PostComments, fixture.schema.CommentPostRelation, fixture.schema.Comment, comment),
		golem.GeneratedNestedCreate[socialMutationPost, socialMutationPostTag](fixture.schema.Post, fixture.schema.PostPostTags, fixture.schema.PostTagPostRelation, fixture.schema.PostTag, postTag),
	)
	friendship := golem.GeneratedCreateInput[socialMutationFriendship](fixture.schema.Friendship,
		golem.GeneratedCreateFieldValue(fixture.schema.Friendship, fixture.friendshipFriendID, golem.UUID{15: 2}),
	)
	return golem.GeneratedCreateInput[socialMutationUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "owner"),
		golem.GeneratedNestedCreate[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post, post),
		golem.GeneratedNestedCreate[socialMutationUser, socialMutationFriendship](fixture.schema.User, fixture.schema.UserFriendshipsFrom, fixture.schema.FriendshipOrigin, fixture.schema.Friendship, friendship),
	)
}

func assertSocialCounts(t testing.TB, fixture socialMutationFixture, counts map[golem.ModelID]int, facts int) {
	t.Helper()
	for model, want := range counts {
		var got int
		if err := fixture.app.database.Get(&got, `SELECT COUNT(*) FROM `+nestedAcceptanceTable(fixture.app, model)); err != nil || got != want {
			t.Fatalf("model %x rows=%d want=%d err=%v", model, got, want, err)
		}
	}
	var gotFacts int
	if err := fixture.app.database.Get(&gotFacts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil || gotFacts != facts {
		t.Fatalf("social facts=%d want=%d err=%v", gotFacts, facts, err)
	}
}

func assertSocialFactModels(t testing.TB, fixture socialMutationFixture, models []golem.ModelID) {
	t.Helper()
	type row struct {
		Model     string `db:"model_id"`
		Causation string `db:"causation_id"`
		Ordinal   int64  `db:"transaction_ordinal"`
		Metadata  []byte `db:"metadata"`
	}
	var rows []row
	if err := fixture.app.database.Select(&rows, `SELECT "model_id","causation_id","transaction_ordinal","metadata" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(models) {
		t.Fatalf("social fact count=%d want=%d", len(rows), len(models))
	}
	for index, model := range models {
		if rows[index].Model != hex.EncodeToString(model[:]) || rows[index].Ordinal != int64(index+1) || rows[index].Causation == "" || index > 0 && rows[index].Causation != rows[0].Causation {
			t.Fatalf("social fact[%d]=%#v want model=%x ordinal=%d", index, rows[index], model, index+1)
		}
		envelope, err := decodeCurrentMutationFactMetadata(fixture.schema.Registry, policyir.ModelID(model), rows[index].Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if envelope.ModelID() != policyir.ModelID(model) || envelope.Action() != mutationir.FactCreated || envelope.TransactionOrdinal() != uint32(index+1) {
			t.Fatalf("social envelope[%d] model=%x action=%d ordinal=%d", index, envelope.ModelID(), envelope.Action(), envelope.TransactionOrdinal())
		}
		if _, present := envelope.BeforeIdentity(); present {
			t.Fatalf("created social envelope[%d] has a before identity", index)
		}
		after, present := envelope.AfterIdentity()
		if !present {
			t.Fatalf("created social envelope[%d] has no after identity", index)
		}
		assertExactSocialCreatedIdentity(t, fixture, index, after)
	}
}

func assertExactSocialCreatedIdentity(t testing.TB, fixture socialMutationFixture, index int, identity mutationdecode.Identity) {
	t.Helper()
	stringValue := func(value string) policyir.Value {
		result, err := policyir.StringValue(value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	uuidValue := func(value byte) policyir.Value { return policyir.UUIDValue([16]byte(golem.UUID{15: value})) }
	var key golem.KeyID
	var want []policyir.Value
	switch index {
	case 0:
		key, want = fixture.schema.UserKey, []policyir.Value{uuidValue(1)}
	case 1:
		key, want = fixture.schema.PostKey, []policyir.Value{uuidValue(10)}
	case 2:
		key, want = fixture.schema.CommentKey, []policyir.Value{uuidValue(20)}
	case 3:
		key, want = fixture.schema.CommentKey, []policyir.Value{uuidValue(21)}
	case 4:
		key, want = fixture.schema.PostTagKey, []policyir.Value{uuidValue(10), stringValue("deep-tag")}
	case 5:
		key, want = fixture.schema.FriendshipKey, []policyir.Value{uuidValue(1), uuidValue(2)}
	default:
		t.Fatalf("unexpected social fact index %d", index)
	}
	components := identity.Components()
	if identity.KeyID() != key || len(components) != len(want) {
		t.Fatalf("social identity[%d] key=%x components=%d want key=%x components=%d", index, identity.KeyID(), len(components), key, len(want))
	}
	for componentIndex, component := range components {
		got, present := component.PolicyValue()
		if !present || !mutationdecode.EqualValue(got, want[componentIndex]) {
			t.Fatalf("social identity[%d].component[%d]=%#v present=%t want=%#v", index, componentIndex, got, present, want[componentIndex])
		}
	}
}

func (fixture socialMutationFixture) userTarget(id byte) golem.MutationTarget[socialMutationUser] {
	return golem.GeneratedUniqueSelectorValue[socialMutationUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: id}))
}

func (fixture socialMutationFixture) postTarget(id byte) golem.MutationTarget[socialMutationPost] {
	return golem.GeneratedUniqueSelectorValue[socialMutationPost](fixture.schema.Post, fixture.schema.PostKey,
		golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
}

func (fixture socialMutationFixture) commentTarget(id byte) golem.MutationTarget[socialMutationComment] {
	return golem.GeneratedUniqueSelectorValue[socialMutationComment](fixture.schema.Comment, fixture.schema.CommentKey,
		golem.GeneratedSelectorComponent(fixture.schema.CommentID, golem.UUID{15: id}))
}

func (fixture socialMutationFixture) tagNameTarget(name string) golem.MutationTarget[socialMutationTag] {
	return golem.GeneratedUniqueSelectorValue[socialMutationTag](fixture.schema.Tag, fixture.schema.TagNameKey,
		golem.GeneratedSelectorComponent(fixture.schema.TagName, name))
}

func (fixture socialMutationFixture) userCreate(id byte, name string) golem.CreateInput[socialMutationUser] {
	return golem.GeneratedCreateInput[socialMutationUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, name))
}

func (fixture socialMutationFixture) postCreate(id byte, title string) golem.CreateInput[socialMutationPost] {
	return golem.GeneratedCreateInput[socialMutationPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, title))
}

func (fixture socialMutationFixture) postRootCreate(id, author byte, title string) golem.CreateInput[socialMutationPost] {
	return golem.GeneratedCreateInput[socialMutationPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, golem.GeneratedEqualField[socialMutationPost, golem.UUID](fixture.schema.PostAuthorID), golem.UUID{15: author}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, title))
}

func (fixture socialMutationFixture) postUpdate(title string) golem.UpdateInput[socialMutationPost] {
	return golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postTitle, title))
}

func (fixture socialMutationFixture) postUpdateMany(title string) golem.UpdateManyInput[socialMutationPost] {
	return golem.GeneratedUpdateManyInput[socialMutationPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postTitle, title))
}

func (fixture socialMutationFixture) tagCreate(id byte, name string) golem.CreateInput[socialMutationTag] {
	return golem.GeneratedCreateInput[socialMutationTag](fixture.schema.Tag,
		golem.GeneratedCreateFieldValue(fixture.schema.Tag, fixture.tagID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Tag, fixture.tagName, name))
}

func newSocialMutationFixture(t testing.TB, deniedCreate golem.ModelID, log *socialHookLog) socialMutationFixture {
	t.Helper()
	ctx := context.Background()
	schemaFixture := schematest.NewSubscribedSocialMutation(t)
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "social-mutation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schemaFixture.SQLite); err != nil {
		t.Fatal(err)
	}
	return openSocialMutationFixture(t, database, golem.SQLite, schemaFixture, deniedCreate, socialMutationHooks(schemaFixture, log))
}

func newPostgresSocialMutationFixture(t testing.TB, profile postgresAcceptanceProfile, deniedCreate golem.ModelID, log *socialHookLog) socialMutationFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p4_social_%s_%d_%d", profile.name, os.Getpid(), suffix))
	systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_social_system_%s_%d_%d", profile.name, os.Getpid(), suffix))
	schemaFixture := schematest.NewSubscribedSocialMutationPostgreSQLNamespaces(t, namespace, systemNamespace)
	provider := postgresprovider.New()
	database, _, err := provider.Open(ctx, profile.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
		_ = database.Close()
	})
	if err := provider.ApplyInitial(ctx, database, schemaFixture.PostgreSQL); err != nil {
		t.Fatal(err)
	}
	return openSocialMutationFixture(t, database, golem.PostgreSQL, schemaFixture, deniedCreate, socialMutationHooks(schemaFixture, log))
}

func openSocialMutationFixture(t testing.TB, database *sqlx.DB, provider golem.Provider, schema schematest.SocialMutationFixture, deniedCreate golem.ModelID, hooks []golem.HookBinding[graphMutationActor]) socialMutationFixture {
	t.Helper()
	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)
	commentIdentity := golem.GeneratedIdentityMetadata(schema.Comment, schema.CommentKey, golem.PrimaryIdentity, schema.CommentID)
	friendshipIdentity := golem.GeneratedIdentityMetadata(schema.Friendship, schema.FriendshipKey, golem.PrimaryIdentity, schema.FriendshipUserID, schema.FriendshipFriendID)
	tagIdentity := golem.GeneratedIdentityMetadata(schema.Tag, schema.TagKey, golem.PrimaryIdentity, schema.TagID)
	tagNameIdentity := golem.GeneratedIdentityMetadata(schema.Tag, schema.TagNameKey, golem.UniqueIdentity, schema.TagName)
	postTagIdentity := golem.GeneratedIdentityMetadata(schema.PostTag, schema.PostTagKey, golem.PrimaryIdentity, schema.PostTagPostID, schema.PostTagTagName)

	userDescriptor := golem.GeneratedModelDescriptor[socialMutationUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{
			golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.PostAuthorship, golem.RelationInverse, golem.RelationToMany),
			golem.GeneratedRelationMetadata(schema.User, schema.Comment, schema.UserComments, schema.CommentAuthorship, golem.RelationInverse, golem.RelationToMany),
			golem.GeneratedRelationMetadata(schema.User, schema.Friendship, schema.UserFriendshipsFrom, schema.FriendshipOrigin, golem.RelationInverse, golem.RelationToMany),
			golem.GeneratedRelationMetadata(schema.User, schema.Friendship, schema.UserFriendshipsTo, schema.FriendshipDestination, golem.RelationInverse, golem.RelationToMany),
		}))
	postDescriptor := golem.GeneratedModelDescriptor[socialMutationPost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostID, schema.PostAuthorID, schema.PostTitle}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{
			golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.PostAuthorship, golem.RelationSource, golem.RelationToOne),
			golem.GeneratedRelationMetadata(schema.Post, schema.Comment, schema.PostComments, schema.CommentPostRelation, golem.RelationInverse, golem.RelationToMany),
			golem.GeneratedRelationMetadata(schema.Post, schema.PostTag, schema.PostPostTags, schema.PostTagPostRelation, golem.RelationInverse, golem.RelationToMany),
		}))
	commentDescriptor := golem.GeneratedModelDescriptor[socialMutationComment](schema.Comment, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.CommentID, schema.CommentPostID, schema.CommentAuthorID, schema.CommentParentID, schema.CommentBody}, nil, []golem.IdentityMetadata{commentIdentity}, []golem.RelationMetadata{
			golem.GeneratedRelationMetadata(schema.Comment, schema.Post, schema.CommentPost, schema.CommentPostRelation, golem.RelationSource, golem.RelationToOne),
			golem.GeneratedRelationMetadata(schema.Comment, schema.User, schema.CommentAuthor, schema.CommentAuthorship, golem.RelationSource, golem.RelationToOne),
			golem.GeneratedRelationMetadata(schema.Comment, schema.Comment, schema.CommentReplyTo, schema.CommentThreading, golem.RelationSource, golem.RelationToOne),
			golem.GeneratedRelationMetadata(schema.Comment, schema.Comment, schema.CommentReplies, schema.CommentThreading, golem.RelationInverse, golem.RelationToMany),
		}))
	friendshipDescriptor := golem.GeneratedModelDescriptor[socialMutationFriendship](schema.Friendship, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.FriendshipUserID, schema.FriendshipFriendID}, nil, []golem.IdentityMetadata{friendshipIdentity}, []golem.RelationMetadata{
			golem.GeneratedRelationMetadata(schema.Friendship, schema.User, schema.FriendshipUser, schema.FriendshipOrigin, golem.RelationSource, golem.RelationToOne),
			golem.GeneratedRelationMetadata(schema.Friendship, schema.User, schema.FriendshipFriend, schema.FriendshipDestination, golem.RelationSource, golem.RelationToOne),
		}))
	tagDescriptor := golem.GeneratedModelDescriptor[socialMutationTag](schema.Tag, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.TagID, schema.TagName}, nil, []golem.IdentityMetadata{tagIdentity, tagNameIdentity}, []golem.RelationMetadata{
			golem.GeneratedRelationMetadata(schema.Tag, schema.PostTag, schema.TagPostTags, schema.PostTagTagRelation, golem.RelationInverse, golem.RelationToMany),
		}))
	postTagDescriptor := golem.GeneratedModelDescriptor[socialMutationPostTag](schema.PostTag, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostTagPostID, schema.PostTagTagName}, nil, []golem.IdentityMetadata{postTagIdentity}, []golem.RelationMetadata{
			golem.GeneratedRelationMetadata(schema.PostTag, schema.Post, schema.PostTagPost, schema.PostTagPostRelation, golem.RelationSource, golem.RelationToOne),
			golem.GeneratedRelationMetadata(schema.PostTag, schema.Tag, schema.PostTagTag, schema.PostTagTagRelation, golem.RelationSource, golem.RelationToOne),
		}))

	descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(),
		userDescriptor.Metadata(), postDescriptor.Metadata(), commentDescriptor.Metadata(), friendshipDescriptor.Metadata(), tagDescriptor.Metadata(), postTagDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	policies := []golem.PolicyBinding[graphMutationActor]{
		allowSocialMutationPolicy[socialMutationUser](schema.User, deniedCreate),
		allowSocialMutationPolicy[socialMutationPost](schema.Post, deniedCreate),
		allowSocialMutationPolicy[socialMutationComment](schema.Comment, deniedCreate),
		allowSocialMutationPolicy[socialMutationFriendship](schema.Friendship, deniedCreate),
		allowSocialMutationPolicy[socialMutationTag](schema.Tag, deniedCreate),
		allowSocialMutationPolicy[socialMutationPostTag](schema.PostTag, deniedCreate),
	}
	bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), policies, hooks))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[graphMutationPrincipal, graphMutationActor]{
		Database: p8RuntimeTestDatabase(database, provider), Bundle: schema.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, graphMutationPrincipal) (graphMutationActor, error) {
			return graphMutationActor{}, nil
		},
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return socialMutationFixture{
		app: app, schema: schema,
		userDescriptor: userDescriptor, postDescriptor: postDescriptor, commentDescriptor: commentDescriptor,
		friendshipDescriptor: friendshipDescriptor, tagDescriptor: tagDescriptor, postTagDescriptor: postTagDescriptor,
		userID: golem.GeneratedEqualField[socialMutationUser, golem.UUID](schema.UserID), userName: golem.GeneratedTextField[socialMutationUser, string](schema.UserName),
		postID: golem.GeneratedEqualField[socialMutationPost, golem.UUID](schema.PostID), postTitle: golem.GeneratedTextField[socialMutationPost, string](schema.PostTitle),
		commentID: golem.GeneratedEqualField[socialMutationComment, golem.UUID](schema.CommentID), commentBody: golem.GeneratedTextField[socialMutationComment, string](schema.CommentBody),
		tagID: golem.GeneratedEqualField[socialMutationTag, golem.UUID](schema.TagID), tagName: golem.GeneratedTextField[socialMutationTag, string](schema.TagName),
		friendshipUserID: golem.GeneratedEqualField[socialMutationFriendship, golem.UUID](schema.FriendshipUserID), friendshipFriendID: golem.GeneratedEqualField[socialMutationFriendship, golem.UUID](schema.FriendshipFriendID),
		postTagPostID: golem.GeneratedEqualField[socialMutationPostTag, golem.UUID](schema.PostTagPostID), postTagTagName: golem.GeneratedEqualField[socialMutationPostTag, string](schema.PostTagTagName),
	}
}

func allowSocialMutationPolicy[M any](model golem.ModelID, deniedCreate golem.ModelID) golem.PolicyBinding[graphMutationActor] {
	return golem.GeneratedPolicyBinding[graphMutationActor, M](model, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[M]()
		rules.CanRead(golem.All[M]())
		if deniedCreate != model {
			rules.CanCreate(golem.All[M]())
		}
		rules.CanUpdate(golem.All[M]())
		rules.CanDelete(golem.All[M]())
		return rules.Freeze(model)
	})
}

func reopenSocialMutationWithCreateDenied(t testing.TB, fixture socialMutationFixture, denied golem.ModelID) socialMutationFixture {
	t.Helper()
	policies := []golem.PolicyBinding[graphMutationActor]{
		allowSocialMutationPolicy[socialMutationUser](fixture.schema.User, denied),
		allowSocialMutationPolicy[socialMutationPost](fixture.schema.Post, denied),
		allowSocialMutationPolicy[socialMutationComment](fixture.schema.Comment, denied),
		allowSocialMutationPolicy[socialMutationFriendship](fixture.schema.Friendship, denied),
		allowSocialMutationPolicy[socialMutationTag](fixture.schema.Tag, denied),
		allowSocialMutationPolicy[socialMutationPostTag](fixture.schema.PostTag, denied),
	}
	return reopenSocialMutationWithPolicies(t, fixture, policies)
}

func reopenSocialMutationWithReplyDenied(t testing.TB, fixture socialMutationFixture) socialMutationFixture {
	t.Helper()
	commentPolicy := golem.GeneratedPolicyBinding[graphMutationActor, socialMutationComment](fixture.schema.Comment, func(graphMutationActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[socialMutationComment]()
		rules.CanRead(golem.All[socialMutationComment]())
		// The depth-two comment has body "comment"; its depth-three reply has
		// body "reply". This lets the outer write proceed far enough to prove
		// that a deepest-node denial rolls the complete transaction back.
		rules.CanCreate(fixture.commentBody.Eq("comment"))
		rules.CanUpdate(golem.All[socialMutationComment]())
		rules.CanDelete(golem.All[socialMutationComment]())
		return rules.Freeze(fixture.schema.Comment)
	})
	policies := []golem.PolicyBinding[graphMutationActor]{
		allowSocialMutationPolicy[socialMutationUser](fixture.schema.User, golem.ModelID{}),
		allowSocialMutationPolicy[socialMutationPost](fixture.schema.Post, golem.ModelID{}),
		commentPolicy,
		allowSocialMutationPolicy[socialMutationFriendship](fixture.schema.Friendship, golem.ModelID{}),
		allowSocialMutationPolicy[socialMutationTag](fixture.schema.Tag, golem.ModelID{}),
		allowSocialMutationPolicy[socialMutationPostTag](fixture.schema.PostTag, golem.ModelID{}),
	}
	return reopenSocialMutationWithPolicies(t, fixture, policies)
}

func reopenSocialMutationWithPolicies(t testing.TB, fixture socialMutationFixture, policies []golem.PolicyBinding[graphMutationActor]) socialMutationFixture {
	t.Helper()
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), policies, nil))
	if err != nil {
		t.Fatal(err)
	}
	provider := golem.SQLite
	if fixture.app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[graphMutationPrincipal, graphMutationActor]{
		Database: p8RuntimeTestDatabase(fixture.app.database, provider), Bundle: fixture.schema.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: func(context.Context, graphMutationPrincipal) (graphMutationActor, error) {
			return graphMutationActor{}, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func socialMutationHooks(schema schematest.SocialMutationFixture, log *socialHookLog) []golem.HookBinding[graphMutationActor] {
	if log == nil {
		return nil
	}
	var hooks []golem.HookBinding[graphMutationActor]
	hooks = append(hooks, socialCreateHooks[graphMutationActor, socialMutationUser](schema.User, "user", log)...)
	hooks = append(hooks, socialCreateHooks[graphMutationActor, socialMutationPost](schema.Post, "post", log)...)
	hooks = append(hooks, socialCreateHooks[graphMutationActor, socialMutationComment](schema.Comment, "comment", log)...)
	hooks = append(hooks, socialCreateHooks[graphMutationActor, socialMutationFriendship](schema.Friendship, "friendship", log)...)
	hooks = append(hooks, socialCreateHooks[graphMutationActor, socialMutationTag](schema.Tag, "tag", log)...)
	hooks = append(hooks, socialCreateHooks[graphMutationActor, socialMutationPostTag](schema.PostTag, "postTag", log)...)
	return hooks
}

func socialCreateHooks[A, M any](model golem.ModelID, name string, log *socialHookLog) []golem.HookBinding[A] {
	if log == nil {
		return nil
	}
	return []golem.HookBinding[A]{
		golem.GeneratedBeforeHookBinding[A, M, golem.CreateHookRequest[M]](model, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[M]) error { log.append(&log.before, name); return nil }),
		golem.GeneratedAfterHookBinding[A, M, golem.CreateHookResult[M]](model, golem.HookCreate, func(context.Context, golem.CreateHookResult[M]) error { log.append(&log.after, name); return nil }),
		golem.GeneratedAfterCommitHookBinding[A, M, golem.CreateHookResult[M]](model, golem.HookCreate, func(context.Context, golem.CreateHookResult[M]) error { log.append(&log.afterCommit, name); return nil }),
	}
}

func assertEverySocialRelationDirection(t testing.TB, fixture socialMutationFixture, caller *Caller[graphMutationPrincipal, graphMutationActor]) {
	t.Helper()
	ctx := context.Background()
	system := fixture.app.System()
	for _, user := range []struct {
		id   byte
		name string
	}{{3, "third"}, {4, "vocabulary-owner"}} {
		if _, err := SystemCreate(ctx, system, fixture.userDescriptor, fixture.userCreate(user.id, user.name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.postRootCreate(11, 1, "second-post")); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemCreate(ctx, system, fixture.tagDescriptor, fixture.tagCreate(31, "other-tag")); err != nil {
		t.Fatal(err)
	}

	// Post.Author (source) and User.Posts (inverse).
	postAuthor := golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post,
		golem.GeneratedNestedConnect[socialMutationPost, socialMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.PostAuthorship, fixture.schema.User, fixture.userTarget(2)))
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(10), postAuthor); err != nil {
		t.Fatalf("Post.Author source connect: %v", err)
	}
	userPosts := golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User,
		golem.GeneratedNestedConnect[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post, fixture.postTarget(10)))
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, fixture.userTarget(1), userPosts); err != nil {
		t.Fatalf("User.Posts inverse connect: %v", err)
	}

	// Comment.Post/User source endpoints and both inverse endpoints.
	commentSources := golem.GeneratedUpdateInput[socialMutationComment](fixture.schema.Comment,
		golem.GeneratedNestedConnect[socialMutationComment, socialMutationPost](fixture.schema.Comment, fixture.schema.CommentPost, fixture.schema.CommentPostRelation, fixture.schema.Post, fixture.postTarget(11)),
		golem.GeneratedNestedConnect[socialMutationComment, socialMutationUser](fixture.schema.Comment, fixture.schema.CommentAuthor, fixture.schema.CommentAuthorship, fixture.schema.User, fixture.userTarget(2)))
	if _, err := CallerUpdate(ctx, caller, fixture.commentDescriptor, fixture.commentTarget(20), commentSources); err != nil {
		t.Fatalf("Comment source relations: %v", err)
	}
	postComments := golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post,
		golem.GeneratedNestedConnect[socialMutationPost, socialMutationComment](fixture.schema.Post, fixture.schema.PostComments, fixture.schema.CommentPostRelation, fixture.schema.Comment, fixture.commentTarget(20)))
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(10), postComments); err != nil {
		t.Fatalf("Post.Comments inverse connect: %v", err)
	}
	userComments := golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User,
		golem.GeneratedNestedConnect[socialMutationUser, socialMutationComment](fixture.schema.User, fixture.schema.UserComments, fixture.schema.CommentAuthorship, fixture.schema.Comment, fixture.commentTarget(20)))
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, fixture.userTarget(1), userComments); err != nil {
		t.Fatalf("User.Comments inverse connect: %v", err)
	}

	// Comment.ReplyTo (source) and Comment.Replies (inverse), including the
	// optional disconnect success that required relations cannot expose.
	replySource := golem.GeneratedUpdateInput[socialMutationComment](fixture.schema.Comment,
		golem.GeneratedNestedConnect[socialMutationComment, socialMutationComment](fixture.schema.Comment, fixture.schema.CommentReplyTo, fixture.schema.CommentThreading, fixture.schema.Comment, fixture.commentTarget(20)))
	if _, err := CallerUpdate(ctx, caller, fixture.commentDescriptor, fixture.commentTarget(21), replySource); err != nil {
		t.Fatalf("Comment.ReplyTo source connect: %v", err)
	}
	repliesInverse := golem.GeneratedUpdateInput[socialMutationComment](fixture.schema.Comment,
		golem.GeneratedNestedConnect[socialMutationComment, socialMutationComment](fixture.schema.Comment, fixture.schema.CommentReplies, fixture.schema.CommentThreading, fixture.schema.Comment, fixture.commentTarget(21)))
	if _, err := CallerUpdate(ctx, caller, fixture.commentDescriptor, fixture.commentTarget(20), repliesInverse); err != nil {
		t.Fatalf("Comment.Replies inverse connect: %v", err)
	}
	disconnect := golem.GeneratedUpdateInput[socialMutationComment](fixture.schema.Comment,
		golem.GeneratedSetFieldValue(fixture.schema.Comment, fixture.commentBody, "disconnected"),
		golem.GeneratedNestedDisconnect[socialMutationComment, socialMutationComment](fixture.schema.Comment, fixture.schema.CommentReplies, fixture.schema.CommentThreading, fixture.schema.Comment, fixture.commentTarget(21)))
	if _, err := CallerUpdate(ctx, caller, fixture.commentDescriptor, fixture.commentTarget(20), disconnect); err != nil {
		t.Fatalf("optional Comment.Replies disconnect: %v", err)
	}

	// Both directed friendship inverses, then both source endpoints as no-op
	// relation-only updates of the composite identity.
	from := golem.GeneratedCreateInput[socialMutationFriendship](fixture.schema.Friendship,
		golem.GeneratedCreateFieldValue(fixture.schema.Friendship, fixture.friendshipFriendID, golem.UUID{15: 1}))
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, fixture.userTarget(2), golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User,
		golem.GeneratedNestedCreate[socialMutationUser, socialMutationFriendship](fixture.schema.User, fixture.schema.UserFriendshipsFrom, fixture.schema.FriendshipOrigin, fixture.schema.Friendship, from))); err != nil {
		t.Fatalf("User.FriendshipsFrom inverse create: %v", err)
	}
	to := golem.GeneratedCreateInput[socialMutationFriendship](fixture.schema.Friendship,
		golem.GeneratedCreateFieldValue(fixture.schema.Friendship, fixture.friendshipUserID, golem.UUID{15: 2}))
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, fixture.userTarget(3), golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User,
		golem.GeneratedNestedCreate[socialMutationUser, socialMutationFriendship](fixture.schema.User, fixture.schema.UserFriendshipsTo, fixture.schema.FriendshipDestination, fixture.schema.Friendship, to))); err != nil {
		t.Fatalf("User.FriendshipsTo inverse create: %v", err)
	}
	friendshipTarget := golem.GeneratedUniqueSelectorValue[socialMutationFriendship](fixture.schema.Friendship, fixture.schema.FriendshipKey,
		golem.GeneratedSelectorComponent(fixture.schema.FriendshipUserID, golem.UUID{15: 1}),
		golem.GeneratedSelectorComponent(fixture.schema.FriendshipFriendID, golem.UUID{15: 2}))
	friendshipSources := golem.GeneratedUpdateInput[socialMutationFriendship](fixture.schema.Friendship,
		golem.GeneratedNestedConnect[socialMutationFriendship, socialMutationUser](fixture.schema.Friendship, fixture.schema.FriendshipUser, fixture.schema.FriendshipOrigin, fixture.schema.User, fixture.userTarget(1)),
		golem.GeneratedNestedConnect[socialMutationFriendship, socialMutationUser](fixture.schema.Friendship, fixture.schema.FriendshipFriend, fixture.schema.FriendshipDestination, fixture.schema.User, fixture.userTarget(2)))
	if _, err := CallerUpdate(ctx, caller, fixture.friendshipDescriptor, friendshipTarget, friendshipSources); err != nil {
		t.Fatalf("Friendship source relations: %v", err)
	}

	// PostTag source endpoints and both inverse endpoints. Tag.PostTags proves
	// relation binding through Tag.Name, not Tag's primary UUID identity.
	postSide := golem.GeneratedCreateInput[socialMutationPostTag](fixture.schema.PostTag,
		golem.GeneratedCreateFieldValue(fixture.schema.PostTag, fixture.postTagTagName, "other-tag"))
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(11), golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post,
		golem.GeneratedNestedCreate[socialMutationPost, socialMutationPostTag](fixture.schema.Post, fixture.schema.PostPostTags, fixture.schema.PostTagPostRelation, fixture.schema.PostTag, postSide))); err != nil {
		t.Fatalf("Post.PostTags inverse create: %v", err)
	}
	tagSide := golem.GeneratedCreateInput[socialMutationPostTag](fixture.schema.PostTag,
		golem.GeneratedCreateFieldValue(fixture.schema.PostTag, fixture.postTagPostID, golem.UUID{15: 10}))
	if _, err := CallerUpdate(ctx, caller, fixture.tagDescriptor, fixture.tagNameTarget("other-tag"), golem.GeneratedUpdateInput[socialMutationTag](fixture.schema.Tag,
		golem.GeneratedNestedCreate[socialMutationTag, socialMutationPostTag](fixture.schema.Tag, fixture.schema.TagPostTags, fixture.schema.PostTagTagRelation, fixture.schema.PostTag, tagSide))); err != nil {
		t.Fatalf("Tag.PostTags inverse create: %v", err)
	}
	postTagTarget := golem.GeneratedUniqueSelectorValue[socialMutationPostTag](fixture.schema.PostTag, fixture.schema.PostTagKey,
		golem.GeneratedSelectorComponent(fixture.schema.PostTagPostID, golem.UUID{15: 10}),
		golem.GeneratedSelectorComponent(fixture.schema.PostTagTagName, "deep-tag"))
	postTagSources := golem.GeneratedUpdateInput[socialMutationPostTag](fixture.schema.PostTag,
		golem.GeneratedNestedConnect[socialMutationPostTag, socialMutationPost](fixture.schema.PostTag, fixture.schema.PostTagPost, fixture.schema.PostTagPostRelation, fixture.schema.Post, fixture.postTarget(10)),
		golem.GeneratedNestedConnect[socialMutationPostTag, socialMutationTag](fixture.schema.PostTag, fixture.schema.PostTagTag, fixture.schema.PostTagTagRelation, fixture.schema.Tag, fixture.tagNameTarget("deep-tag")))
	if _, err := CallerUpdate(ctx, caller, fixture.postTagDescriptor, postTagTarget, postTagSources); err != nil {
		t.Fatalf("PostTag source relations: %v", err)
	}

	assertSocialRelationRows(t, fixture)
}

func assertEverySocialNestedOperation(t testing.TB, fixture socialMutationFixture, caller *Caller[graphMutationPrincipal, graphMutationActor]) {
	t.Helper()
	ctx := context.Background()
	root := fixture.userTarget(4)
	updateRoot := func(operation golem.NestedUpdateValue[socialMutationUser]) error {
		_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, root, golem.GeneratedUpdateInput[socialMutationUser](fixture.schema.User, operation))
		return err
	}

	if err := updateRoot(golem.GeneratedNestedCreate[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post, fixture.postCreate(50, "create"))); err != nil {
		t.Fatalf("nested create: %v", err)
	}
	if err := updateRoot(golem.GeneratedNestedCreateMany[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post,
		fixture.postCreate(51, "many-a"), fixture.postCreate(52, "many-b"))); err != nil {
		t.Fatalf("nested createMany: %v", err)
	}
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postRootCreate(53, 2, "connect-before")); err != nil {
		t.Fatal(err)
	}
	if err := updateRoot(golem.GeneratedNestedConnect[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post, fixture.postTarget(53))); err != nil {
		t.Fatalf("nested connect: %v", err)
	}
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postRootCreate(54, 2, "coc-before")); err != nil {
		t.Fatal(err)
	}
	unusedUser := fixture.userCreate(9, "unused")
	coc := golem.GeneratedNestedConnectOrCreate[socialMutationPost, socialMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.PostAuthorship, fixture.schema.User, fixture.userTarget(4), unusedUser)
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.postTarget(54), golem.GeneratedUpdateInput[socialMutationPost](fixture.schema.Post, coc)); err != nil {
		t.Fatalf("nested connectOrCreate: %v", err)
	}
	set := golem.GeneratedNestedSet[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post,
		fixture.postTarget(50), fixture.postTarget(51), fixture.postTarget(52), fixture.postTarget(53), fixture.postTarget(54))
	if err := updateRoot(set); err != nil {
		t.Fatalf("nested set: %v", err)
	}
	if err := updateRoot(golem.GeneratedNestedUpdate[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post, fixture.postTarget(50), fixture.postUpdate("updated"))); err != nil {
		t.Fatalf("nested update: %v", err)
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	updateMany := golem.GeneratedNestedUpdateMany[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post,
		fixture.postID.In(golem.UUID{15: 51}, golem.UUID{15: 52}), fixture.postUpdateMany("bulk-updated"))
	if err := updateRoot(updateMany); err != nil {
		t.Fatalf("nested updateMany: %v", err)
	}
	assertSocialPostFacts(t, fixture, 51, 52)
	upsertUpdate := golem.GeneratedNestedUpsert[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post,
		fixture.postTarget(50), fixture.postCreate(50, "unused"), fixture.postUpdate("upsert-updated"))
	if err := updateRoot(upsertUpdate); err != nil {
		t.Fatalf("nested upsert update: %v", err)
	}
	upsertCreate := golem.GeneratedNestedUpsert[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post,
		fixture.postTarget(55), fixture.postCreate(55, "upsert-created"), fixture.postUpdate("unused"))
	if err := updateRoot(upsertCreate); err != nil {
		t.Fatalf("nested upsert create: %v", err)
	}
	if err := updateRoot(golem.GeneratedNestedDelete[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post, fixture.postTarget(55))); err != nil {
		t.Fatalf("nested delete: %v", err)
	}
	deleteMany := golem.GeneratedNestedDeleteMany[socialMutationUser, socialMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.PostAuthorship, fixture.schema.Post,
		fixture.postID.In(golem.UUID{15: 51}, golem.UUID{15: 52}))
	if err := updateRoot(deleteMany); err != nil {
		t.Fatalf("nested deleteMany: %v", err)
	}

	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	var title, author string
	query := fixture.app.database.Rebind(`SELECT "title","author_id" FROM ` + posts + ` WHERE "id"=?`)
	if err := fixture.app.database.QueryRowxContext(ctx, query, mutationResultUUIDText(50)).Scan(&title, &author); err != nil || title != "upsert-updated" || author != mutationResultUUIDText(4) {
		t.Fatalf("nested vocabulary survivor title=%q author=%q err=%v", title, author, err)
	}
	var removed int
	query = fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + posts + ` WHERE "id" IN (?, ?, ?)`)
	if err := fixture.app.database.GetContext(ctx, &removed, query, mutationResultUUIDText(51), mutationResultUUIDText(52), mutationResultUUIDText(55)); err != nil || removed != 0 {
		t.Fatalf("nested deletes remaining=%d err=%v", removed, err)
	}
}

func assertSocialRelationRows(t testing.TB, fixture socialMutationFixture) {
	t.Helper()
	ctx := context.Background()
	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	var author string
	query := fixture.app.database.Rebind(`SELECT "author_id" FROM ` + posts + ` WHERE "id"=?`)
	if err := fixture.app.database.GetContext(ctx, &author, query, mutationResultUUIDText(10)); err != nil || author != mutationResultUUIDText(1) {
		t.Fatalf("post relation roundtrip author=%q err=%v", author, err)
	}
	comments := nestedAcceptanceTable(fixture.app, fixture.schema.Comment)
	var postID, authorID string
	var parentID *string
	query = fixture.app.database.Rebind(`SELECT "post_id","author_id","parent_id" FROM ` + comments + ` WHERE "id"=?`)
	if err := fixture.app.database.QueryRowxContext(ctx, query, mutationResultUUIDText(20)).Scan(&postID, &authorID, &parentID); err != nil || postID != mutationResultUUIDText(10) || authorID != mutationResultUUIDText(1) {
		t.Fatalf("comment relation roundtrip post=%q author=%q parent=%v err=%v", postID, authorID, parentID, err)
	}
	query = fixture.app.database.Rebind(`SELECT "parent_id" FROM ` + comments + ` WHERE "id"=?`)
	if err := fixture.app.database.GetContext(ctx, &parentID, query, mutationResultUUIDText(21)); err != nil || parentID != nil {
		t.Fatalf("optional recursive disconnect parent=%v err=%v", parentID, err)
	}
	for model, want := range map[golem.ModelID]int{fixture.schema.Friendship: 3, fixture.schema.PostTag: 3} {
		var count int
		if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM `+nestedAcceptanceTable(fixture.app, model)); err != nil || count != want {
			t.Fatalf("relation model %x rows=%d want=%d err=%v", model, count, want, err)
		}
	}
}

func assertSocialPostFacts(t testing.TB, fixture socialMutationFixture, ids ...byte) {
	t.Helper()
	type factRow struct {
		Model     string `db:"model_id"`
		Causation string `db:"causation_id"`
		Ordinal   int64  `db:"transaction_ordinal"`
	}
	var rows []factRow
	if err := fixture.app.database.Select(&rows, `SELECT "model_id","causation_id","transaction_ordinal" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(ids)+1 {
		t.Fatalf("nested batch facts=%#v want child IDs=%v", rows, ids)
	}
	wantUser, wantPost := hex.EncodeToString(fixture.schema.User[:]), hex.EncodeToString(fixture.schema.Post[:])
	for index, row := range rows {
		wantModel := wantPost
		if index == 0 {
			wantModel = wantUser
		}
		if row.Model != wantModel || row.Ordinal != int64(index+1) || row.Causation == "" || index > 0 && row.Causation != rows[0].Causation {
			t.Fatalf("nested batch fact[%d]=%#v ids=%v", index, row, ids)
		}
	}
}
