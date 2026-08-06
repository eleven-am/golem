package schematest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// GraphFixture is the smallest schema where an immediate batched child owns a
// to-many relation count: User -> Posts (batched), Post -> Comments (counted).
type GraphFixture struct {
	Bundle     golem.SchemaBundle
	Registry   *schema.Registry
	SQLite     physical.PhysicalSchema
	PostgreSQL physical.PhysicalSchema

	User, Post, Comment                   golem.ModelID
	UserID, UserName                      golem.FieldID
	PostID, AuthorID, PostTitle           golem.FieldID
	CommentID, CommentPostID, CommentBody golem.FieldID
	UserPosts, PostAuthor                 golem.FieldID
	PostComments, CommentPost             golem.FieldID
	Authorship, Commenting                golem.RelationID
	UserKey, PostKey, CommentKey          golem.KeyID
}

func NewGraph(t testing.TB) GraphFixture {
	t.Helper()
	user, post, comment := compilerir.ModelID(id(101)), compilerir.ModelID(id(102)), compilerir.ModelID(id(103))
	userID, userName, userPosts := compilerir.FieldID(id(111)), compilerir.FieldID(id(112)), compilerir.FieldID(id(113))
	postID, authorID, postTitle, postAuthor, postComments := compilerir.FieldID(id(121)), compilerir.FieldID(id(122)), compilerir.FieldID(id(123)), compilerir.FieldID(id(124)), compilerir.FieldID(id(125))
	commentID, commentPostID, commentBody, commentPost := compilerir.FieldID(id(131)), compilerir.FieldID(id(132)), compilerir.FieldID(id(133)), compilerir.FieldID(id(134))
	authorship, commenting := compilerir.RelationID(id(141)), compilerir.RelationID(id(142))
	userKey, postKey, commentKey := compilerir.KeyID(id(151)), compilerir.KeyID(id(152)), compilerir.KeyID(id(153))

	model := compilerir.ModelIR{FormatVersion: compilerir.ModelFormatVersion, Providers: []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL}, Models: []compilerir.ModelDeclIR{
		{ID: user, LogicalName: "User", Table: compilerir.TableBindingIR{PhysicalName: "users"}, Fields: []compilerir.FieldIR{
			scalar(userID, "ID", "id", compilerir.TypeUUID, false), scalar(userName, "Name", "name", compilerir.TypeString, false), relation(userPosts, "Posts", authorship, compilerir.RelationInverse, compilerir.RelationHasMany),
		}, PrimaryKey: &compilerir.KeyIR{ID: userKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_users", Fields: []compilerir.FieldID{userID}}},
		{ID: post, LogicalName: "Post", Table: compilerir.TableBindingIR{PhysicalName: "posts"}, Fields: []compilerir.FieldIR{
			scalar(postID, "ID", "id", compilerir.TypeUUID, false), scalar(authorID, "AuthorID", "author_id", compilerir.TypeUUID, false), scalar(postTitle, "Title", "title", compilerir.TypeString, false),
			relation(postAuthor, "Author", authorship, compilerir.RelationSource, compilerir.RelationBelongsTo), relation(postComments, "Comments", commenting, compilerir.RelationInverse, compilerir.RelationHasMany),
		}, PrimaryKey: &compilerir.KeyIR{ID: postKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_posts", Fields: []compilerir.FieldID{postID}}},
		{ID: comment, LogicalName: "Comment", Table: compilerir.TableBindingIR{PhysicalName: "comments"}, Fields: []compilerir.FieldIR{
			scalar(commentID, "ID", "id", compilerir.TypeUUID, false), scalar(commentPostID, "PostID", "post_id", compilerir.TypeUUID, false), scalar(commentBody, "Body", "body", compilerir.TypeString, false), relation(commentPost, "Post", commenting, compilerir.RelationSource, compilerir.RelationBelongsTo),
		}, PrimaryKey: &compilerir.KeyIR{ID: commentKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_comments", Fields: []compilerir.FieldID{commentID}}},
	}, Relations: []compilerir.RelationIR{
		{ID: authorship, SourceModel: post, TargetModel: user, SourceField: postAuthor, InverseField: &userPosts, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{authorID}, RemoteFields: []compilerir.FieldID{userID}},
		{ID: commenting, SourceModel: comment, TargetModel: post, SourceField: commentPost, InverseField: &postComments, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{commentPostID}, RemoteFields: []compilerir.FieldID{postID}},
	}}
	contract := compilerir.ContractIR{FormatVersion: compilerir.ContractFormatVersion, Models: []compilerir.ModelContractIR{
		{ModelID: user, Fields: fieldContracts(userID, userName, userPosts)},
		{ModelID: post, Fields: fieldContracts(postID, authorID, postTitle, postAuthor, postComments)},
		{ModelID: comment, Fields: fieldContracts(commentID, commentPostID, commentBody, commentPost)},
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
	sqliteSchema := graphPhysicalSchema(compilerir.SQLite, user, post, comment, userID, userName, postID, authorID, postTitle, commentID, commentPostID, commentBody, userKey, postKey, commentKey)
	postgresSchema := graphPhysicalSchema(compilerir.PostgreSQL, user, post, comment, userID, userName, postID, authorID, postTitle, commentID, commentPostID, commentBody, userKey, postKey, commentKey)
	bundle := golem.GeneratedSchemaBundle(golem.SchemaDigest{2}, "schematest", "p3-graph", modelDocument, contractDocument,
		providerDocument(t, golem.SQLite, sqliteSchema), providerDocument(t, golem.PostgreSQL, postgresSchema))
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatalf("bootstrap graph schema fixture: %v", err)
	}
	return GraphFixture{
		Bundle: bundle, Registry: registry, SQLite: sqliteSchema, PostgreSQL: postgresSchema,
		User: golem.ModelID(mustFixed(t, string(user))), Post: golem.ModelID(mustFixed(t, string(post))), Comment: golem.ModelID(mustFixed(t, string(comment))),
		UserID: golem.FieldID(mustFixed(t, string(userID))), UserName: golem.FieldID(mustFixed(t, string(userName))), UserPosts: golem.FieldID(mustFixed(t, string(userPosts))),
		PostID: golem.FieldID(mustFixed(t, string(postID))), AuthorID: golem.FieldID(mustFixed(t, string(authorID))), PostTitle: golem.FieldID(mustFixed(t, string(postTitle))), PostAuthor: golem.FieldID(mustFixed(t, string(postAuthor))), PostComments: golem.FieldID(mustFixed(t, string(postComments))),
		CommentID: golem.FieldID(mustFixed(t, string(commentID))), CommentPostID: golem.FieldID(mustFixed(t, string(commentPostID))), CommentBody: golem.FieldID(mustFixed(t, string(commentBody))), CommentPost: golem.FieldID(mustFixed(t, string(commentPost))),
		Authorship: golem.RelationID(mustFixed(t, string(authorship))), Commenting: golem.RelationID(mustFixed(t, string(commenting))),
		UserKey: golem.KeyID(mustFixed(t, string(userKey))), PostKey: golem.KeyID(mustFixed(t, string(postKey))), CommentKey: golem.KeyID(mustFixed(t, string(commentKey))),
	}
}

func fieldContracts(fields ...compilerir.FieldID) []compilerir.FieldContractIR {
	result := make([]compilerir.FieldContractIR, len(fields))
	for index, field := range fields {
		result[index] = compilerir.FieldContractIR{FieldID: field}
	}
	return result
}

func graphPhysicalSchema(provider compilerir.Provider, user, post, comment compilerir.ModelID, userID, userName, postID, authorID, postTitle, commentID, commentPostID, commentBody compilerir.FieldID, userKey, postKey, commentKey compilerir.KeyID) physical.PhysicalSchema {
	manifest, namespace, uuid := postgresprovider.New().Manifest(), physical.PhysicalName("public"), physical.StoragePostgreSQLUUID
	text := physical.StorageType{Kind: physical.StoragePostgreSQLText}
	if provider == compilerir.SQLite {
		manifest, namespace, uuid = sqliteprovider.New().Manifest(), "main", physical.StorageSQLiteText
		text.Kind = physical.StorageSQLiteText
	}
	uuidColumn := func(field compilerir.FieldID, name string, ordinal uint32) physical.PhysicalColumn {
		return physical.PhysicalColumn{ID: field, Name: physical.PhysicalName(name), Ordinal: ordinal, Storage: physical.StorageType{Kind: uuid}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}
	}
	textColumn := func(field compilerir.FieldID, name string, ordinal uint32) physical.PhysicalColumn {
		return physical.PhysicalColumn{ID: field, Name: physical.PhysicalName(name), Ordinal: ordinal, Storage: text, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}
	}
	return physical.PhysicalSchema{Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion, Provider: manifest, Namespace: physical.Namespace{Name: namespace}, Tables: []physical.PhysicalTable{
		{ID: user, Name: "users", Columns: []physical.PhysicalColumn{uuidColumn(userID, "id", 0), textColumn(userName, "name", 1)}, PrimaryKey: &physical.PhysicalKey{ID: userKey, Name: "pk_users", Columns: []compilerir.FieldID{userID}}},
		{ID: post, Name: "posts", Columns: []physical.PhysicalColumn{uuidColumn(postID, "id", 0), uuidColumn(authorID, "author_id", 1), textColumn(postTitle, "title", 2)}, PrimaryKey: &physical.PhysicalKey{ID: postKey, Name: "pk_posts", Columns: []compilerir.FieldID{postID}}},
		{ID: comment, Name: "comments", Columns: []physical.PhysicalColumn{uuidColumn(commentID, "id", 0), uuidColumn(commentPostID, "post_id", 1), textColumn(commentBody, "body", 2)}, PrimaryKey: &physical.PhysicalKey{ID: commentKey, Name: "pk_comments", Columns: []compilerir.FieldID{commentID}}},
	}}
}
