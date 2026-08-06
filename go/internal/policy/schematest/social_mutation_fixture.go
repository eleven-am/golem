package schematest

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// SocialMutationFixture is the canonical six-model social graph used by the
// P4-E live acceptance corpus. It deliberately keeps the scalar vocabulary
// small while retaining every structural property the nested kernel must
// handle: two directed friendship edges, a recursive optional comment edge,
// two composite-primary-key join models, and a relation that targets a named
// unique key rather than a primary key.
type SocialMutationFixture struct {
	Bundle                   golem.SchemaBundle
	Registry                 *schema.Registry
	SQLite, PostgreSQL       physical.PhysicalSchema
	User, Post, Comment      golem.ModelID
	Friendship, Tag, PostTag golem.ModelID

	UserID, UserName, UserPosts, UserComments                  golem.FieldID
	UserFriendshipsFrom, UserFriendshipsTo                     golem.FieldID
	PostID, PostAuthorID, PostTitle, PostAuthor, PostComments  golem.FieldID
	PostPostTags                                               golem.FieldID
	CommentID, CommentPostID, CommentAuthorID, CommentParentID golem.FieldID
	CommentBody, CommentPost, CommentAuthor, CommentReplyTo    golem.FieldID
	CommentReplies                                             golem.FieldID
	FriendshipUserID, FriendshipFriendID, FriendshipUser       golem.FieldID
	FriendshipFriend                                           golem.FieldID
	TagID, TagName, TagPostTags                                golem.FieldID
	PostTagPostID, PostTagTagName, PostTagPost, PostTagTag     golem.FieldID

	PostAuthorship, CommentPostRelation, CommentAuthorship    golem.RelationID
	CommentThreading, FriendshipOrigin, FriendshipDestination golem.RelationID
	PostTagPostRelation, PostTagTagRelation                   golem.RelationID

	UserKey, PostKey, CommentKey, FriendshipKey golem.KeyID
	TagKey, TagNameKey, PostTagKey              golem.KeyID
}

func NewSubscribedSocialMutation(t testing.TB) SocialMutationFixture {
	return newSubscribedSocialMutation(t, "public", "_golem", false)
}

func NewSubscribedSocialMutationPostgreSQLNamespaces(t testing.TB, namespace, systemNamespace physical.PhysicalName) SocialMutationFixture {
	return newSubscribedSocialMutation(t, namespace, systemNamespace, false)
}

// NewSubscribedSocialMutationAdversarialRelationOrder keeps the canonical
// social graph semantics while assigning the recursive ordinary inverse field
// a lower stable ID than the required author source field. It prevents nested
// scheduling tests from accidentally passing because source dependencies sort
// first by FieldID.
func NewSubscribedSocialMutationAdversarialRelationOrder(t testing.TB) SocialMutationFixture {
	return newSubscribedSocialMutation(t, "public", "_golem", true)
}

func NewSubscribedSocialMutationAdversarialRelationOrderPostgreSQLNamespaces(t testing.TB, namespace, systemNamespace physical.PhysicalName) SocialMutationFixture {
	return newSubscribedSocialMutation(t, namespace, systemNamespace, true)
}

func newSubscribedSocialMutation(t testing.TB, postgresNamespace, postgresSystemNamespace physical.PhysicalName, adversarialRelationOrder bool) SocialMutationFixture {
	t.Helper()
	user, post, comment := compilerir.ModelID(id(401)), compilerir.ModelID(id(402)), compilerir.ModelID(id(403))
	friendship, tag, postTag := compilerir.ModelID(id(404)), compilerir.ModelID(id(405)), compilerir.ModelID(id(406))

	userID, userName := compilerir.FieldID(id(411)), compilerir.FieldID(id(412))
	userPosts, userComments := compilerir.FieldID(id(413)), compilerir.FieldID(id(414))
	userFriendshipsFrom, userFriendshipsTo := compilerir.FieldID(id(415)), compilerir.FieldID(id(416))
	postID, postAuthorID, postTitle := compilerir.FieldID(id(421)), compilerir.FieldID(id(422)), compilerir.FieldID(id(423))
	postAuthor, postComments, postPostTags := compilerir.FieldID(id(424)), compilerir.FieldID(id(425)), compilerir.FieldID(id(426))
	commentID, commentPostID, commentAuthorID := compilerir.FieldID(id(431)), compilerir.FieldID(id(432)), compilerir.FieldID(id(433))
	commentParentID, commentBody := compilerir.FieldID(id(434)), compilerir.FieldID(id(435))
	commentPost, commentAuthor := compilerir.FieldID(id(436)), compilerir.FieldID(id(437))
	commentReplyTo, commentReplies := compilerir.FieldID(id(438)), compilerir.FieldID(id(439))
	if adversarialRelationOrder {
		commentAuthor, commentReplies = commentReplies, commentAuthor
	}
	friendshipUserID, friendshipFriendID := compilerir.FieldID(id(441)), compilerir.FieldID(id(442))
	friendshipUser, friendshipFriend := compilerir.FieldID(id(443)), compilerir.FieldID(id(444))
	tagID, tagName, tagPostTags := compilerir.FieldID(id(451)), compilerir.FieldID(id(452)), compilerir.FieldID(id(453))
	postTagPostID, postTagTagName := compilerir.FieldID(id(461)), compilerir.FieldID(id(462))
	postTagPost, postTagTag := compilerir.FieldID(id(463)), compilerir.FieldID(id(464))

	postAuthorship, commentPostRelation := compilerir.RelationID(id(471)), compilerir.RelationID(id(472))
	commentAuthorship, commentThreading := compilerir.RelationID(id(473)), compilerir.RelationID(id(474))
	friendshipOrigin, friendshipDestination := compilerir.RelationID(id(475)), compilerir.RelationID(id(476))
	postTagPostRelation, postTagTagRelation := compilerir.RelationID(id(477)), compilerir.RelationID(id(478))

	userKey, postKey, commentKey := compilerir.KeyID(id(481)), compilerir.KeyID(id(482)), compilerir.KeyID(id(483))
	friendshipKey, tagKey, tagNameKey, postTagKey := compilerir.KeyID(id(484)), compilerir.KeyID(id(485)), compilerir.KeyID(id(486)), compilerir.KeyID(id(487))

	model := compilerir.ModelIR{
		FormatVersion: compilerir.ModelFormatVersion,
		Providers:     []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL},
		Models: []compilerir.ModelDeclIR{
			{ID: user, LogicalName: "User", Table: compilerir.TableBindingIR{PhysicalName: "users"}, Fields: []compilerir.FieldIR{
				scalar(userID, "ID", "id", compilerir.TypeUUID, false),
				scalar(userName, "Name", "name", compilerir.TypeString, false),
				relation(userPosts, "Posts", postAuthorship, compilerir.RelationInverse, compilerir.RelationHasMany),
				relation(userComments, "Comments", commentAuthorship, compilerir.RelationInverse, compilerir.RelationHasMany),
				relation(userFriendshipsFrom, "FriendshipsFrom", friendshipOrigin, compilerir.RelationInverse, compilerir.RelationHasMany),
				relation(userFriendshipsTo, "FriendshipsTo", friendshipDestination, compilerir.RelationInverse, compilerir.RelationHasMany),
			}, PrimaryKey: &compilerir.KeyIR{ID: userKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_users", Fields: []compilerir.FieldID{userID}}},
			{ID: post, LogicalName: "Post", Table: compilerir.TableBindingIR{PhysicalName: "posts"}, Fields: []compilerir.FieldIR{
				scalar(postID, "ID", "id", compilerir.TypeUUID, false),
				scalar(postAuthorID, "AuthorID", "author_id", compilerir.TypeUUID, false),
				scalar(postTitle, "Title", "title", compilerir.TypeString, false),
				relation(postAuthor, "Author", postAuthorship, compilerir.RelationSource, compilerir.RelationBelongsTo),
				relation(postComments, "Comments", commentPostRelation, compilerir.RelationInverse, compilerir.RelationHasMany),
				relation(postPostTags, "PostTags", postTagPostRelation, compilerir.RelationInverse, compilerir.RelationHasMany),
			}, PrimaryKey: &compilerir.KeyIR{ID: postKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_posts", Fields: []compilerir.FieldID{postID}}},
			{ID: comment, LogicalName: "Comment", Table: compilerir.TableBindingIR{PhysicalName: "comments"}, Fields: []compilerir.FieldIR{
				scalar(commentID, "ID", "id", compilerir.TypeUUID, false),
				scalar(commentPostID, "PostID", "post_id", compilerir.TypeUUID, false),
				scalar(commentAuthorID, "AuthorID", "author_id", compilerir.TypeUUID, false),
				scalar(commentParentID, "ParentID", "parent_id", compilerir.TypeUUID, true),
				scalar(commentBody, "Body", "body", compilerir.TypeString, false),
				relation(commentPost, "Post", commentPostRelation, compilerir.RelationSource, compilerir.RelationBelongsTo),
				relation(commentAuthor, "Author", commentAuthorship, compilerir.RelationSource, compilerir.RelationBelongsTo),
				relation(commentReplyTo, "ReplyTo", commentThreading, compilerir.RelationSource, compilerir.RelationBelongsTo),
				relation(commentReplies, "Replies", commentThreading, compilerir.RelationInverse, compilerir.RelationHasMany),
			}, PrimaryKey: &compilerir.KeyIR{ID: commentKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_comments", Fields: []compilerir.FieldID{commentID}}},
			{ID: friendship, LogicalName: "Friendship", Table: compilerir.TableBindingIR{PhysicalName: "friendships"}, Fields: []compilerir.FieldIR{
				scalar(friendshipUserID, "UserID", "user_id", compilerir.TypeUUID, false),
				scalar(friendshipFriendID, "FriendID", "friend_id", compilerir.TypeUUID, false),
				relation(friendshipUser, "User", friendshipOrigin, compilerir.RelationSource, compilerir.RelationBelongsTo),
				relation(friendshipFriend, "Friend", friendshipDestination, compilerir.RelationSource, compilerir.RelationBelongsTo),
			}, PrimaryKey: &compilerir.KeyIR{ID: friendshipKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_friendships", Fields: []compilerir.FieldID{friendshipUserID, friendshipFriendID}}},
			{ID: tag, LogicalName: "Tag", Table: compilerir.TableBindingIR{PhysicalName: "tags"}, Fields: []compilerir.FieldIR{
				scalar(tagID, "ID", "id", compilerir.TypeUUID, false),
				scalar(tagName, "Name", "name", compilerir.TypeString, false),
				relation(tagPostTags, "PostTags", postTagTagRelation, compilerir.RelationInverse, compilerir.RelationHasMany),
			}, PrimaryKey: &compilerir.KeyIR{ID: tagKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_tags", Fields: []compilerir.FieldID{tagID}}, Uniques: []compilerir.KeyIR{{ID: tagNameKey, Kind: compilerir.KeyUnique, LogicalName: "Name", PhysicalName: "uq_tags_name", Fields: []compilerir.FieldID{tagName}}}},
			{ID: postTag, LogicalName: "PostTag", Table: compilerir.TableBindingIR{PhysicalName: "post_tags"}, Fields: []compilerir.FieldIR{
				scalar(postTagPostID, "PostID", "post_id", compilerir.TypeUUID, false),
				scalar(postTagTagName, "TagName", "tag_name", compilerir.TypeString, false),
				relation(postTagPost, "Post", postTagPostRelation, compilerir.RelationSource, compilerir.RelationBelongsTo),
				relation(postTagTag, "Tag", postTagTagRelation, compilerir.RelationSource, compilerir.RelationBelongsTo),
			}, PrimaryKey: &compilerir.KeyIR{ID: postTagKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_post_tags", Fields: []compilerir.FieldID{postTagPostID, postTagTagName}}},
		},
		Relations: []compilerir.RelationIR{
			{ID: postAuthorship, Name: "PostAuthorship", SourceModel: post, TargetModel: user, SourceField: postAuthor, InverseField: &userPosts, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{postAuthorID}, RemoteFields: []compilerir.FieldID{userID}},
			{ID: commentPostRelation, Name: "CommentPost", SourceModel: comment, TargetModel: post, SourceField: commentPost, InverseField: &postComments, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{commentPostID}, RemoteFields: []compilerir.FieldID{postID}},
			{ID: commentAuthorship, Name: "CommentAuthorship", SourceModel: comment, TargetModel: user, SourceField: commentAuthor, InverseField: &userComments, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{commentAuthorID}, RemoteFields: []compilerir.FieldID{userID}},
			{ID: commentThreading, Name: "ReplyTree", SourceModel: comment, TargetModel: comment, SourceField: commentReplyTo, InverseField: &commentReplies, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{commentParentID}, RemoteFields: []compilerir.FieldID{commentID}},
			{ID: friendshipOrigin, Name: "Origin", SourceModel: friendship, TargetModel: user, SourceField: friendshipUser, InverseField: &userFriendshipsFrom, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{friendshipUserID}, RemoteFields: []compilerir.FieldID{userID}},
			{ID: friendshipDestination, Name: "Destination", SourceModel: friendship, TargetModel: user, SourceField: friendshipFriend, InverseField: &userFriendshipsTo, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{friendshipFriendID}, RemoteFields: []compilerir.FieldID{userID}},
			{ID: postTagPostRelation, Name: "PostTagPost", SourceModel: postTag, TargetModel: post, SourceField: postTagPost, InverseField: &postPostTags, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{postTagPostID}, RemoteFields: []compilerir.FieldID{postID}},
			{ID: postTagTagRelation, Name: "PostTagTag", SourceModel: postTag, TargetModel: tag, SourceField: postTagTag, InverseField: &tagPostTags, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{postTagTagName}, RemoteFields: []compilerir.FieldID{tagName}},
		},
	}

	contract := compilerir.ContractIR{FormatVersion: compilerir.ContractFormatVersion, Models: []compilerir.ModelContractIR{
		{ModelID: user, Fields: fieldContracts(userID, userName, userPosts, userComments, userFriendshipsFrom, userFriendshipsTo), Subscriptions: true},
		{ModelID: post, Fields: fieldContracts(postID, postAuthorID, postTitle, postAuthor, postComments, postPostTags), Subscriptions: true},
		{ModelID: comment, Fields: fieldContracts(commentID, commentPostID, commentAuthorID, commentParentID, commentBody, commentPost, commentAuthor, commentReplyTo, commentReplies), Subscriptions: true},
		{ModelID: friendship, Fields: fieldContracts(friendshipUserID, friendshipFriendID, friendshipUser, friendshipFriend), Subscriptions: true},
		{ModelID: tag, Fields: fieldContracts(tagID, tagName, tagPostTags), Subscriptions: true},
		{ModelID: postTag, Fields: fieldContracts(postTagPostID, postTagTagName, postTagPost, postTagTag), Subscriptions: true},
	}}

	modelDocument := document(t, uint32(compilerir.ModelFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalModel(model)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ModelFingerprint(model)
		return payload, fingerprint, err
	})
	contractDocument := document(t, uint32(compilerir.ContractFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalContract(contract)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ContractFingerprint(contract)
		return payload, fingerprint, err
	})

	sqliteSchema, err := sqliteprovider.New().Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatalf("lower SQLite social mutation fixture: %v", err)
	}
	postgresSchema, err := postgresprovider.New().Lower(context.Background(), model, physical.LowerOptions{Namespace: postgresNamespace})
	if err != nil {
		t.Fatalf("lower PostgreSQL social mutation fixture: %v", err)
	}
	sqliteSchema.System = graphSystemSchema("main")
	postgresSchema.System = graphSystemSchema(postgresSystemNamespace)

	bundle := golem.GeneratedSchemaBundle(golem.SchemaDigest{6}, "schematest", "p4-social-mutation", modelDocument, contractDocument,
		providerDocument(t, golem.SQLite, sqliteSchema), providerDocument(t, golem.PostgreSQL, postgresSchema))
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatalf("bootstrap social mutation schema: %v", err)
	}

	return SocialMutationFixture{
		Bundle: bundle, Registry: registry, SQLite: sqliteSchema, PostgreSQL: postgresSchema,
		User: golem.ModelID(mustFixed(t, string(user))), Post: golem.ModelID(mustFixed(t, string(post))), Comment: golem.ModelID(mustFixed(t, string(comment))),
		Friendship: golem.ModelID(mustFixed(t, string(friendship))), Tag: golem.ModelID(mustFixed(t, string(tag))), PostTag: golem.ModelID(mustFixed(t, string(postTag))),
		UserID: golem.FieldID(mustFixed(t, string(userID))), UserName: golem.FieldID(mustFixed(t, string(userName))), UserPosts: golem.FieldID(mustFixed(t, string(userPosts))), UserComments: golem.FieldID(mustFixed(t, string(userComments))),
		UserFriendshipsFrom: golem.FieldID(mustFixed(t, string(userFriendshipsFrom))), UserFriendshipsTo: golem.FieldID(mustFixed(t, string(userFriendshipsTo))),
		PostID: golem.FieldID(mustFixed(t, string(postID))), PostAuthorID: golem.FieldID(mustFixed(t, string(postAuthorID))), PostTitle: golem.FieldID(mustFixed(t, string(postTitle))), PostAuthor: golem.FieldID(mustFixed(t, string(postAuthor))), PostComments: golem.FieldID(mustFixed(t, string(postComments))), PostPostTags: golem.FieldID(mustFixed(t, string(postPostTags))),
		CommentID: golem.FieldID(mustFixed(t, string(commentID))), CommentPostID: golem.FieldID(mustFixed(t, string(commentPostID))), CommentAuthorID: golem.FieldID(mustFixed(t, string(commentAuthorID))), CommentParentID: golem.FieldID(mustFixed(t, string(commentParentID))), CommentBody: golem.FieldID(mustFixed(t, string(commentBody))),
		CommentPost: golem.FieldID(mustFixed(t, string(commentPost))), CommentAuthor: golem.FieldID(mustFixed(t, string(commentAuthor))), CommentReplyTo: golem.FieldID(mustFixed(t, string(commentReplyTo))), CommentReplies: golem.FieldID(mustFixed(t, string(commentReplies))),
		FriendshipUserID: golem.FieldID(mustFixed(t, string(friendshipUserID))), FriendshipFriendID: golem.FieldID(mustFixed(t, string(friendshipFriendID))), FriendshipUser: golem.FieldID(mustFixed(t, string(friendshipUser))), FriendshipFriend: golem.FieldID(mustFixed(t, string(friendshipFriend))),
		TagID: golem.FieldID(mustFixed(t, string(tagID))), TagName: golem.FieldID(mustFixed(t, string(tagName))), TagPostTags: golem.FieldID(mustFixed(t, string(tagPostTags))),
		PostTagPostID: golem.FieldID(mustFixed(t, string(postTagPostID))), PostTagTagName: golem.FieldID(mustFixed(t, string(postTagTagName))), PostTagPost: golem.FieldID(mustFixed(t, string(postTagPost))), PostTagTag: golem.FieldID(mustFixed(t, string(postTagTag))),
		PostAuthorship: golem.RelationID(mustFixed(t, string(postAuthorship))), CommentPostRelation: golem.RelationID(mustFixed(t, string(commentPostRelation))), CommentAuthorship: golem.RelationID(mustFixed(t, string(commentAuthorship))), CommentThreading: golem.RelationID(mustFixed(t, string(commentThreading))),
		FriendshipOrigin: golem.RelationID(mustFixed(t, string(friendshipOrigin))), FriendshipDestination: golem.RelationID(mustFixed(t, string(friendshipDestination))), PostTagPostRelation: golem.RelationID(mustFixed(t, string(postTagPostRelation))), PostTagTagRelation: golem.RelationID(mustFixed(t, string(postTagTagRelation))),
		UserKey: golem.KeyID(mustFixed(t, string(userKey))), PostKey: golem.KeyID(mustFixed(t, string(postKey))), CommentKey: golem.KeyID(mustFixed(t, string(commentKey))), FriendshipKey: golem.KeyID(mustFixed(t, string(friendshipKey))),
		TagKey: golem.KeyID(mustFixed(t, string(tagKey))), TagNameKey: golem.KeyID(mustFixed(t, string(tagNameKey))), PostTagKey: golem.KeyID(mustFixed(t, string(postTagKey))),
	}
}
