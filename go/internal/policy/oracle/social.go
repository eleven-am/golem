package oracle

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
)

// SocialCorpus returns a fresh view of the checked-in P2-H social fixture. It
// deliberately uses User, Post, recursive Comment, Friendship, Tag, and PostTag
// so provider adapters exercise single and composite identities, UUID and
// string correlation, empty/present relations, nulls, exact numbers, lists,
// JSON, and a dangling to-one endpoint.
func SocialCorpus() Corpus {
	ids := socialIDsValue
	types := socialTypes()
	models := socialModels(ids)
	fields := socialFields(ids, types)
	relations := socialRelations(ids)
	rows := socialRows(ids)
	probes := socialProbes(ids, types)
	matrix := scalarMatrixFixture()
	models = append(models, matrix.model)
	fields = append(fields, matrix.fields...)
	rows = append(rows, matrix.rows...)
	probes = append(probes, matrix.probes...)
	listMatrix := scalarListMatrixFixture()
	models = append(models, listMatrix.model)
	fields = append(fields, listMatrix.fields...)
	rows = append(rows, listMatrix.rows...)
	probes = append(probes, listMatrix.probes...)
	jsonMatrix := jsonMatrixFixture()
	models = append(models, jsonMatrix.model)
	fields = append(fields, jsonMatrix.fields...)
	rows = append(rows, jsonMatrix.rows...)
	probes = append(probes, jsonMatrix.probes...)
	return Corpus{
		seed:      CanonicalSeed,
		models:    models,
		fields:    fields,
		relations: relations,
		rows:      rows,
		probes:    probes,
	}
}

type socialIDs struct {
	user, post, comment, friendship, tag, postTag ir.ModelID

	userID, userName, userScore, userTags, userProfile, userPosts ir.FieldID
	userFriendshipsFrom, userFriendshipsTo                        ir.FieldID

	postID, postAuthorID, postTitle, postRating ir.FieldID
	postAuthor, postComments, postTags          ir.FieldID

	commentID, commentPostID, commentAuthorID, commentParentID ir.FieldID
	commentBody, commentPost, commentReplyTo, commentReplies   ir.FieldID

	friendshipUserID, friendshipFriendID                ir.FieldID
	friendshipUser, friendshipFriend, friendshipReverse ir.FieldID
	tagName, tagPostTags                                ir.FieldID
	postTagPostID, postTagTagName                       ir.FieldID
	postTagPost, postTagTag                             ir.FieldID

	postAuthorRelation, userPostsRelation            ir.RelationID
	postCommentsRelation, commentPostRelation        ir.RelationID
	commentReplyRelation, commentReplyToRelation     ir.RelationID
	friendshipFromRelation, friendshipToRelation     ir.RelationID
	friendshipUserRelation, friendshipFriendRelation ir.RelationID
	friendshipReverseRelation                        ir.RelationID
	postPostTagsRelation, tagPostTagsRelation        ir.RelationID
	postTagPostRelation, postTagTagRelation          ir.RelationID
}

var socialIDsValue = func() socialIDs {
	return socialIDs{
		user: modelID(1), post: modelID(2), comment: modelID(3),
		friendship: modelID(4), tag: modelID(5), postTag: modelID(6),
		userID: fieldID(1, 1), userName: fieldID(1, 2), userScore: fieldID(1, 3),
		userTags: fieldID(1, 4), userProfile: fieldID(1, 5), userPosts: fieldID(1, 6),
		userFriendshipsFrom: fieldID(1, 7), userFriendshipsTo: fieldID(1, 8),
		postID: fieldID(2, 1), postAuthorID: fieldID(2, 2), postTitle: fieldID(2, 3), postRating: fieldID(2, 4),
		postAuthor: fieldID(2, 5), postComments: fieldID(2, 6), postTags: fieldID(2, 7),
		commentID: fieldID(3, 1), commentPostID: fieldID(3, 2), commentAuthorID: fieldID(3, 3),
		commentParentID: fieldID(3, 4), commentBody: fieldID(3, 5),
		commentPost: fieldID(3, 6), commentReplyTo: fieldID(3, 7), commentReplies: fieldID(3, 8),
		friendshipUserID: fieldID(4, 1), friendshipFriendID: fieldID(4, 2),
		friendshipUser: fieldID(4, 3), friendshipFriend: fieldID(4, 4), friendshipReverse: fieldID(4, 5),
		tagName: fieldID(5, 1), tagPostTags: fieldID(5, 2),
		postTagPostID: fieldID(6, 1), postTagTagName: fieldID(6, 2),
		postTagPost: fieldID(6, 3), postTagTag: fieldID(6, 4),
		postAuthorRelation: relationID(1), userPostsRelation: relationID(2),
		postCommentsRelation: relationID(3), commentPostRelation: relationID(4),
		commentReplyRelation: relationID(5), friendshipFromRelation: relationID(6),
		friendshipToRelation: relationID(7), friendshipUserRelation: relationID(8),
		friendshipFriendRelation: relationID(9), postPostTagsRelation: relationID(10),
		tagPostTagsRelation: relationID(11), postTagPostRelation: relationID(12),
		postTagTagRelation: relationID(13), commentReplyToRelation: relationID(14),
		friendshipReverseRelation: relationID(15),
	}
}()

type socialTypeSet struct {
	uuid, text, int64Nullable, stringList, json ir.TypeRef
}

func socialTypes() socialTypeSet {
	textElement := mustType(ir.ValueString, false, nil, 0)
	return socialTypeSet{
		uuid:          mustType(ir.ValueUUID, false, nil, 0),
		text:          mustType(ir.ValueString, false, nil, 0),
		int64Nullable: mustType(ir.ValueInt64, true, nil, 0),
		stringList:    mustType(ir.ValueScalarList, true, &textElement, ir.CapabilityScalarListJSON),
		json:          mustType(ir.ValueJSON, true, nil, 0),
	}
}

func socialModels(ids socialIDs) []ModelSpec {
	return []ModelSpec{
		{id: ids.user, name: "User", table: "users", identityFields: []ir.FieldID{ids.userID}},
		{id: ids.post, name: "Post", table: "posts", identityFields: []ir.FieldID{ids.postID}},
		{id: ids.comment, name: "Comment", table: "comments", identityFields: []ir.FieldID{ids.commentID}},
		{id: ids.friendship, name: "Friendship", table: "friendships", identityFields: []ir.FieldID{ids.friendshipUserID, ids.friendshipFriendID}},
		{id: ids.tag, name: "Tag", table: "tags", identityFields: []ir.FieldID{ids.tagName}},
		{id: ids.postTag, name: "PostTag", table: "post_tags", identityFields: []ir.FieldID{ids.postTagPostID, ids.postTagTagName}},
	}
}

func socialFields(ids socialIDs, types socialTypeSet) []FieldSpec {
	field := func(model ir.ModelID, id ir.FieldID, name, column string, typ ir.TypeRef) FieldSpec {
		return FieldSpec{id: id, model: model, name: name, column: column, typeRef: typ, nullable: typ.Nullable()}
	}
	return []FieldSpec{
		field(ids.user, ids.userID, "ID", "id", types.uuid),
		field(ids.user, ids.userName, "Name", "name", types.text),
		field(ids.user, ids.userScore, "Score", "score", types.int64Nullable),
		field(ids.user, ids.userTags, "Tags", "tags", types.stringList),
		field(ids.user, ids.userProfile, "Profile", "profile", types.json),
		field(ids.post, ids.postID, "ID", "id", types.uuid),
		field(ids.post, ids.postAuthorID, "AuthorID", "author_id", types.uuid),
		field(ids.post, ids.postTitle, "Title", "title", types.text),
		field(ids.post, ids.postRating, "Rating", "rating", types.int64Nullable),
		field(ids.comment, ids.commentID, "ID", "id", types.uuid),
		field(ids.comment, ids.commentPostID, "PostID", "post_id", types.uuid),
		field(ids.comment, ids.commentAuthorID, "AuthorID", "author_id", types.uuid),
		field(ids.comment, ids.commentParentID, "ParentID", "parent_id", mustType(ir.ValueUUID, true, nil, 0)),
		field(ids.comment, ids.commentBody, "Body", "body", types.text),
		field(ids.friendship, ids.friendshipUserID, "UserID", "user_id", types.uuid),
		field(ids.friendship, ids.friendshipFriendID, "FriendID", "friend_id", types.uuid),
		field(ids.tag, ids.tagName, "Name", "name", types.text),
		field(ids.postTag, ids.postTagPostID, "PostID", "post_id", types.uuid),
		field(ids.postTag, ids.postTagTagName, "TagName", "tag_name", types.text),
	}
}

func socialRelations(ids socialIDs) []RelationSpec {
	return []RelationSpec{
		relationSpec(ids.postAuthorRelation, ids.post, ids.postAuthor, ids.user, ir.RelationToOne, ids.postAuthorID, ids.userID),
		relationSpec(ids.userPostsRelation, ids.user, ids.userPosts, ids.post, ir.RelationToMany, ids.userID, ids.postAuthorID),
		relationSpec(ids.postCommentsRelation, ids.post, ids.postComments, ids.comment, ir.RelationToMany, ids.postID, ids.commentPostID),
		relationSpec(ids.commentPostRelation, ids.comment, ids.commentPost, ids.post, ir.RelationToOne, ids.commentPostID, ids.postID),
		relationSpec(ids.commentReplyToRelation, ids.comment, ids.commentReplyTo, ids.comment, ir.RelationToOne, ids.commentParentID, ids.commentID),
		relationSpec(ids.commentReplyRelation, ids.comment, ids.commentReplies, ids.comment, ir.RelationToMany, ids.commentID, ids.commentParentID),
		relationSpec(ids.friendshipFromRelation, ids.user, ids.userFriendshipsFrom, ids.friendship, ir.RelationToMany, ids.userID, ids.friendshipUserID),
		relationSpec(ids.friendshipToRelation, ids.user, ids.userFriendshipsTo, ids.friendship, ir.RelationToMany, ids.userID, ids.friendshipFriendID),
		relationSpec(ids.friendshipUserRelation, ids.friendship, ids.friendshipUser, ids.user, ir.RelationToOne, ids.friendshipUserID, ids.userID),
		relationSpec(ids.friendshipFriendRelation, ids.friendship, ids.friendshipFriend, ids.user, ir.RelationToOne, ids.friendshipFriendID, ids.userID),
		{id: ids.friendshipReverseRelation, model: ids.friendship, field: ids.friendshipReverse, target: ids.friendship, cardinality: ir.RelationToOne, correlation: []Correlation{{parent: ids.friendshipUserID, child: ids.friendshipFriendID}, {parent: ids.friendshipFriendID, child: ids.friendshipUserID}}},
		relationSpec(ids.postPostTagsRelation, ids.post, ids.postTags, ids.postTag, ir.RelationToMany, ids.postID, ids.postTagPostID),
		relationSpec(ids.tagPostTagsRelation, ids.tag, ids.tagPostTags, ids.postTag, ir.RelationToMany, ids.tagName, ids.postTagTagName),
		relationSpec(ids.postTagPostRelation, ids.postTag, ids.postTagPost, ids.post, ir.RelationToOne, ids.postTagPostID, ids.postID),
		relationSpec(ids.postTagTagRelation, ids.postTag, ids.postTagTag, ids.tag, ir.RelationToOne, ids.postTagTagName, ids.tagName),
	}
}

func relationSpec(id ir.RelationID, model ir.ModelID, field ir.FieldID, target ir.ModelID, cardinality ir.RelationCardinality, parent, child ir.FieldID) RelationSpec {
	return RelationSpec{id: id, model: model, field: field, target: target, cardinality: cardinality, correlation: []Correlation{{parent: parent, child: child}}}
}

func socialRows(ids socialIDs) []Row {
	u1, u2, u3, u4, u5 := uuid(1), uuid(2), uuid(3), uuid(4), uuid(5)
	p1, p2, p3, p4, dangling := uuid(11), uuid(12), uuid(13), uuid(14), uuid(99)
	c1, c2, c3 := uuid(21), uuid(22), uuid(23)

	post1 := mustRecord(ids.post, mustValueField(ids.postTitle, stringValue("Go and databases")), evaluate.NullField(ids.postRating))
	post2 := mustRecord(ids.post, mustValueField(ids.postTitle, stringValue("Other")), mustValueField(ids.postRating, signedValue(1)))
	post3 := mustRecord(ids.post, mustValueField(ids.postTitle, stringValue("GO deep")), mustValueField(ids.postRating, signedValue(2)))
	ada := mustRecord(ids.user, mustValueField(ids.userName, stringValue("Ada")))
	bob := mustRecord(ids.user, mustValueField(ids.userName, stringValue("bob")))
	alice := mustRecord(ids.user, mustValueField(ids.userName, stringValue("ALICE_%\\")))

	profile1 := jsonObject(
		jsonMember(`a.b["\\]`, jsonString("literal-path")),
		jsonMember("arr", jsonArray(jsonNumber("1"), jsonString("tail"))),
		jsonMember("count", jsonNumber("9007199254740993")),
		jsonMember("slot", jsonString("AÉBackend")),
	)
	profile3 := jsonObject(
		jsonMember("arr", jsonArray()),
		jsonMember("count", jsonNumber("1")),
		jsonMember("slot", ir.JSONNullValue()),
	)
	profile4 := jsonObject(
		jsonMember("arr", jsonArray(jsonString("go"), jsonString("go"))),
		jsonMember("count", jsonNumber("9007199254740992")),
		jsonMember("slot", jsonString("Å/å_%\\")),
	)

	u1Record := mustRecord(ids.user,
		mustValueField(ids.userID, ir.UUIDValue(u1)),
		mustValueField(ids.userName, stringValue("Ada")),
		mustValueField(ids.userScore, signedValue(9_007_199_254_740_993)),
		mustListField(ids.userTags, stringValue("go"), stringValue("db")),
		mustValueField(ids.userProfile, jsonValue(profile1)),
		mustToMany(ids.userPosts, ids.post, post1),
	)
	u2Record := mustRecord(ids.user,
		mustValueField(ids.userID, ir.UUIDValue(u2)), mustValueField(ids.userName, stringValue("bob")),
		evaluate.NullField(ids.userScore), evaluate.NullField(ids.userTags), evaluate.NullField(ids.userProfile),
		mustToMany(ids.userPosts, ids.post, post2),
	)
	u3Record := mustRecord(ids.user,
		mustValueField(ids.userID, ir.UUIDValue(u3)), mustValueField(ids.userName, stringValue("ALICE_%\\")),
		mustValueField(ids.userScore, signedValue(2)), mustListField(ids.userTags),
		mustValueField(ids.userProfile, jsonValue(profile3)), mustToMany(ids.userPosts, ids.post, post3),
	)
	badElements := mustListFieldElements(ids.userTags,
		mustListElement(stringValue("go")), evaluate.InvalidListElement(), mustListElement(signedValue(7)),
	)
	u4Record := mustRecord(ids.user,
		mustValueField(ids.userID, ir.UUIDValue(u4)), mustValueField(ids.userName, stringValue("Å")),
		mustValueField(ids.userScore, signedValue(0)), badElements,
		mustValueField(ids.userProfile, jsonValue(profile4)), mustToMany(ids.userPosts, ids.post),
	)
	u5Record := mustRecord(ids.user,
		mustValueField(ids.userID, ir.UUIDValue(u5)), mustValueField(ids.userName, stringValue("😀")),
		mustValueField(ids.userScore, signedValue(-1)), mustListField(ids.userTags),
		evaluate.NullField(ids.userProfile), mustToMany(ids.userPosts, ids.post),
	)

	p1Record := mustRecord(ids.post,
		mustValueField(ids.postID, ir.UUIDValue(p1)), mustValueField(ids.postAuthorID, ir.UUIDValue(u1)),
		mustValueField(ids.postTitle, stringValue("Go and databases")), evaluate.NullField(ids.postRating), mustToOne(ids.postAuthor, ids.user, ada),
	)
	p2Record := mustRecord(ids.post,
		mustValueField(ids.postID, ir.UUIDValue(p2)), mustValueField(ids.postAuthorID, ir.UUIDValue(u2)),
		mustValueField(ids.postTitle, stringValue("Other")), mustValueField(ids.postRating, signedValue(1)), mustToOne(ids.postAuthor, ids.user, bob),
	)
	p3Record := mustRecord(ids.post,
		mustValueField(ids.postID, ir.UUIDValue(p3)), mustValueField(ids.postAuthorID, ir.UUIDValue(u3)),
		mustValueField(ids.postTitle, stringValue("GO deep")), mustValueField(ids.postRating, signedValue(2)), mustToOne(ids.postAuthor, ids.user, alice),
	)
	// This separate row is the isolated dangling-relation witness: the local
	// key is non-null but the descriptor traversal is loaded empty.
	p4Record := mustRecord(ids.post,
		mustValueField(ids.postID, ir.UUIDValue(p4)), mustValueField(ids.postAuthorID, ir.UUIDValue(dangling)),
		mustValueField(ids.postTitle, stringValue("dangling")), evaluate.NullField(ids.postRating), mustToOne(ids.postAuthor, ids.user),
	)

	c3Record := mustRecord(ids.comment,
		mustValueField(ids.commentID, ir.UUIDValue(c3)), mustValueField(ids.commentBody, stringValue("depth three")),
		mustToMany(ids.commentReplies, ids.comment),
	)
	c2Record := mustRecord(ids.comment,
		mustValueField(ids.commentID, ir.UUIDValue(c2)), mustValueField(ids.commentBody, stringValue("depth two")),
		mustToMany(ids.commentReplies, ids.comment, c3Record),
	)
	c1Record := mustRecord(ids.comment,
		mustValueField(ids.commentID, ir.UUIDValue(c1)), mustValueField(ids.commentBody, stringValue("depth one")),
		mustToMany(ids.commentReplies, ids.comment, c2Record),
	)
	friend12Base := mustRecord(ids.friendship, mustValueField(ids.friendshipUserID, ir.UUIDValue(u1)), mustValueField(ids.friendshipFriendID, ir.UUIDValue(u2)))
	friend21Base := mustRecord(ids.friendship, mustValueField(ids.friendshipUserID, ir.UUIDValue(u2)), mustValueField(ids.friendshipFriendID, ir.UUIDValue(u1)))
	friend12 := mustRecord(ids.friendship,
		mustValueField(ids.friendshipUserID, ir.UUIDValue(u1)), mustValueField(ids.friendshipFriendID, ir.UUIDValue(u2)),
		mustToOne(ids.friendshipReverse, ids.friendship, friend21Base),
	)
	friend21 := mustRecord(ids.friendship,
		mustValueField(ids.friendshipUserID, ir.UUIDValue(u2)), mustValueField(ids.friendshipFriendID, ir.UUIDValue(u1)),
		mustToOne(ids.friendshipReverse, ids.friendship, friend12Base),
	)
	postTagGo := mustRecord(ids.postTag, mustValueField(ids.postTagPostID, ir.UUIDValue(p1)), mustValueField(ids.postTagTagName, stringValue("Go")))
	postTagRing := mustRecord(ids.postTag, mustValueField(ids.postTagPostID, ir.UUIDValue(p3)), mustValueField(ids.postTagTagName, stringValue("Å")))
	tagGo := mustRecord(ids.tag, mustValueField(ids.tagName, stringValue("Go")), mustToMany(ids.tagPostTags, ids.postTag, postTagGo))
	tagRing := mustRecord(ids.tag, mustValueField(ids.tagName, stringValue("Å")), mustToMany(ids.tagPostTags, ids.postTag, postTagRing))

	return []Row{
		seedRow("user:1", ids.user, u1Record,
			mustValueCell(ids.userID, ir.UUIDValue(u1)), mustValueCell(ids.userName, stringValue("Ada")),
			mustValueCell(ids.userScore, signedValue(9_007_199_254_740_993)), mustValueCell(ids.userTags, listValue(stringValue("go"), stringValue("db"))),
			mustValueCell(ids.userProfile, jsonValue(profile1))),
		seedRow("user:2", ids.user, u2Record,
			mustValueCell(ids.userID, ir.UUIDValue(u2)), mustValueCell(ids.userName, stringValue("bob")),
			NullCell(ids.userScore), NullCell(ids.userTags), NullCell(ids.userProfile)),
		seedRow("user:3", ids.user, u3Record,
			mustValueCell(ids.userID, ir.UUIDValue(u3)), mustValueCell(ids.userName, stringValue("ALICE_%\\")),
			mustValueCell(ids.userScore, signedValue(2)), mustValueCell(ids.userTags, listValue()), mustValueCell(ids.userProfile, jsonValue(profile3))),
		seedRow("user:4", ids.user, u4Record,
			mustValueCell(ids.userID, ir.UUIDValue(u4)), mustValueCell(ids.userName, stringValue("Å")),
			mustValueCell(ids.userScore, signedValue(0)), mustRawJSONCell(ids.userTags, `["go",null,7]`), mustValueCell(ids.userProfile, jsonValue(profile4))),
		seedRow("user:5", ids.user, u5Record,
			mustValueCell(ids.userID, ir.UUIDValue(u5)), mustValueCell(ids.userName, stringValue("😀")),
			mustValueCell(ids.userScore, signedValue(-1)), mustValueCell(ids.userTags, listValue()), NullCell(ids.userProfile)),
		seedRow("post:11", ids.post, p1Record, mustValueCell(ids.postID, ir.UUIDValue(p1)), mustValueCell(ids.postAuthorID, ir.UUIDValue(u1)), mustValueCell(ids.postTitle, stringValue("Go and databases")), NullCell(ids.postRating)),
		seedRow("post:12", ids.post, p2Record, mustValueCell(ids.postID, ir.UUIDValue(p2)), mustValueCell(ids.postAuthorID, ir.UUIDValue(u2)), mustValueCell(ids.postTitle, stringValue("Other")), mustValueCell(ids.postRating, signedValue(1))),
		seedRow("post:13", ids.post, p3Record, mustValueCell(ids.postID, ir.UUIDValue(p3)), mustValueCell(ids.postAuthorID, ir.UUIDValue(u3)), mustValueCell(ids.postTitle, stringValue("GO deep")), mustValueCell(ids.postRating, signedValue(2))),
		seedDriftRow("post:14", ids.post, p4Record, mustValueCell(ids.postID, ir.UUIDValue(p4)), mustValueCell(ids.postAuthorID, ir.UUIDValue(dangling)), mustValueCell(ids.postTitle, stringValue("dangling")), NullCell(ids.postRating)),
		seedRow("comment:21", ids.comment, c1Record, mustValueCell(ids.commentID, ir.UUIDValue(c1)), mustValueCell(ids.commentPostID, ir.UUIDValue(p1)), mustValueCell(ids.commentAuthorID, ir.UUIDValue(u1)), NullCell(ids.commentParentID), mustValueCell(ids.commentBody, stringValue("depth one"))),
		seedRow("comment:22", ids.comment, c2Record, mustValueCell(ids.commentID, ir.UUIDValue(c2)), mustValueCell(ids.commentPostID, ir.UUIDValue(p1)), mustValueCell(ids.commentAuthorID, ir.UUIDValue(u2)), mustValueCell(ids.commentParentID, ir.UUIDValue(c1)), mustValueCell(ids.commentBody, stringValue("depth two"))),
		seedRow("comment:23", ids.comment, c3Record, mustValueCell(ids.commentID, ir.UUIDValue(c3)), mustValueCell(ids.commentPostID, ir.UUIDValue(p1)), mustValueCell(ids.commentAuthorID, ir.UUIDValue(u1)), mustValueCell(ids.commentParentID, ir.UUIDValue(c2)), mustValueCell(ids.commentBody, stringValue("depth three"))),
		seedRow("friendship:1/2", ids.friendship, friend12, mustValueCell(ids.friendshipUserID, ir.UUIDValue(u1)), mustValueCell(ids.friendshipFriendID, ir.UUIDValue(u2))),
		seedRow("friendship:2/1", ids.friendship, friend21, mustValueCell(ids.friendshipUserID, ir.UUIDValue(u2)), mustValueCell(ids.friendshipFriendID, ir.UUIDValue(u1))),
		seedRow("tag:Go", ids.tag, tagGo, mustValueCell(ids.tagName, stringValue("Go"))),
		seedRow("tag:Å", ids.tag, tagRing, mustValueCell(ids.tagName, stringValue("Å"))),
		seedRow("post_tag:11/Go", ids.postTag, postTagGo, mustValueCell(ids.postTagPostID, ir.UUIDValue(p1)), mustValueCell(ids.postTagTagName, stringValue("Go"))),
		seedRow("post_tag:13/Å", ids.postTag, postTagRing, mustValueCell(ids.postTagPostID, ir.UUIDValue(p3)), mustValueCell(ids.postTagTagName, stringValue("Å"))),
	}
}

func socialProbes(ids socialIDs, types socialTypeSet) []Probe {
	entries := operator.Entries()
	probes := make([]Probe, 0, len(entries)*2+48)
	for _, entry := range entries {
		condition := representativeCondition(ids, types, entry.ID())
		primary := Probe{
			name:       "operator/" + entry.Name(),
			operatorID: entry.ID(),
			condition:  condition,
			primary:    true,
			mutation:   "wrong-polarity/" + entry.Name(),
		}
		probes = append(probes, primary)
		negated := mustLogical(condition.ModelID(), ir.LogicalNot, condition)
		probes = append(probes, Probe{
			name:       "not/" + entry.Name(),
			operatorID: entry.ID(),
			condition:  negated,
		})
	}
	probes = append(probes, socialVariantProbes(ids, types)...)
	return probes
}

func socialVariantProbes(ids socialIDs, types socialTypeSet) []Probe {
	equalAda := scalarCondition(ids.user, ids.userName, types.text, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(stringValue("Ada")))
	exactLarge := scalarCondition(ids.user, ids.userScore, types.int64Nullable, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(signedValue(9_007_199_254_740_993)))
	scoreZero := scalarCondition(ids.user, ids.userScore, types.int64Nullable, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(signedValue(0)))
	nameBob := scalarCondition(ids.user, ids.userName, types.text, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(stringValue("bob")))
	trueConstant, err := ir.NewConstant(ids.user, true)
	trueConstant = must(trueConstant, err)
	falseConstant, err := ir.NewConstant(ids.user, false)
	falseConstant = must(falseConstant, err)
	or := mustLogical(ids.user, ir.LogicalOr, equalAda, nameBob)

	variants := []Probe{
		variantProbe("logical/all-empty", ir.OperatorEqual, trueConstant),
		variantProbe("logical/none-empty", ir.OperatorEqual, falseConstant),
		variantProbe("logical/and", ir.OperatorEqual, mustLogical(ids.user, ir.LogicalAnd, equalAda, exactLarge)),
		variantProbe("logical/or", ir.OperatorEqual, or),
		variantProbe("logical/nor", ir.OperatorEqual, mustLogical(ids.user, ir.LogicalNot, or)),
		variantProbe("scalar/exact-adjacent-above-2pow53", ir.OperatorEqual, exactLarge),
		variantProbe("scalar/in-empty", ir.OperatorIn, scalarCondition(ids.user, ids.userScore, types.int64Nullable, ir.OperatorIn, ir.ComparisonSensitive, manyOperand())),
		variantProbe("scalar/not-in-empty", ir.OperatorNotIn, scalarCondition(ids.user, ids.userScore, types.int64Nullable, ir.OperatorNotIn, ir.ComparisonSensitive, manyOperand())),
		variantProbe("scalar/literal-percent-underscore-backslash", ir.OperatorContains, scalarCondition(ids.user, ids.userName, types.text, ir.OperatorContains, ir.ComparisonSensitive, oneOperand(stringValue("%_\\")))),
		variantProbe("scalar/astral", ir.OperatorEqual, scalarCondition(ids.user, ids.userName, types.text, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(stringValue("😀")))),
		variantProbe("scalar/non-ascii-not-folded", ir.OperatorEqual, scalarCondition(ids.user, ids.userName, types.text, ir.OperatorEqual, ir.ComparisonASCIIInsensitive, oneOperand(stringValue("å")))),
		variantProbe("list/equal-empty", ir.OperatorListEqual, listCondition(ids.user, ids.userTags, types.stringList, ir.OperatorListEqual, oneOperand(listValue()))),
		variantProbe("list/has-every-empty", ir.OperatorListHasEvery, listCondition(ids.user, ids.userTags, types.stringList, ir.OperatorListHasEvery, manyOperand())),
		variantProbe("list/has-some-empty", ir.OperatorListHasSome, listCondition(ids.user, ids.userTags, types.stringList, ir.OperatorListHasSome, manyOperand())),
		variantProbe("list/is-empty-false", ir.OperatorListIsEmpty, listCondition(ids.user, ids.userTags, types.stringList, ir.OperatorListIsEmpty, ir.FlagOperand(false))),
		variantProbe("json/db-null-missing-path", ir.OperatorJSONEqual, jsonCondition(ids.user, ids.userProfile, types.json, ir.OperatorJSONEqual, ir.ComparisonSensitive, jsonPathKey("missing"), jsonNullOperand(ir.JSONDbNull))),
		variantProbe("json/document-null", ir.OperatorJSONEqual, jsonCondition(ids.user, ids.userProfile, types.json, ir.OperatorJSONEqual, ir.ComparisonSensitive, jsonPathKey("slot"), jsonNullOperand(ir.JSONDocumentNull))),
		variantProbe("json/any-null", ir.OperatorJSONEqual, jsonCondition(ids.user, ids.userProfile, types.json, ir.OperatorJSONEqual, ir.ComparisonSensitive, jsonPathKey("slot"), jsonNullOperand(ir.JSONAnyNull))),
		variantProbe("json/not-db-null", ir.OperatorJSONNotEqual, jsonCondition(ids.user, ids.userProfile, types.json, ir.OperatorJSONNotEqual, ir.ComparisonSensitive, jsonPathKey("missing"), jsonNullOperand(ir.JSONDbNull))),
		variantProbe("json/literal-special-key", ir.OperatorJSONEqual, jsonCondition(ids.user, ids.userProfile, types.json, ir.OperatorJSONEqual, ir.ComparisonSensitive, jsonPathKey(`a.b["\\]`), oneOperand(jsonValue(jsonString("literal-path"))))),
		variantProbe("json/wrong-type-not-equal", ir.OperatorJSONNotEqual, jsonCondition(ids.user, ids.userProfile, types.json, ir.OperatorJSONNotEqual, ir.ComparisonSensitive, jsonPathKey("slot"), oneOperand(jsonValue(jsonNumber("1"))))),
	}

	ratingOne := scalarCondition(ids.post, ids.postRating, types.int64Nullable, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(signedValue(1)))
	variants = append(variants,
		variantProbe("relation/every-null-bearing-child", ir.OperatorRelationEvery, relationCondition(ids.user, ids.userPosts, ids.userPostsRelation, ids.post, ir.RelationToMany, ir.OperatorRelationEvery, &ratingOne)),
	)
	postTagNameGo := scalarCondition(ids.postTag, ids.postTagTagName, types.text, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(stringValue("Go")))
	variants = append(variants,
		variantProbe("relation/string-key-correlation", ir.OperatorRelationSome, relationCondition(ids.tag, ids.tagPostTags, ids.tagPostTagsRelation, ids.postTag, ir.RelationToMany, ir.OperatorRelationSome, &postTagNameGo)),
	)
	friendTrue, err := ir.NewConstant(ids.friendship, true)
	friendTrue = must(friendTrue, err)
	variants = append(variants,
		variantProbe("relation/composite-correlation", ir.OperatorRelationIs, relationCondition(ids.friendship, ids.friendshipReverse, ids.friendshipReverseRelation, ids.friendship, ir.RelationToOne, ir.OperatorRelationIs, &friendTrue)),
	)
	depthThree := scalarCondition(ids.comment, ids.commentBody, types.text, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(stringValue("depth three")))
	nestedReplies := relationCondition(ids.comment, ids.commentReplies, ids.commentReplyRelation, ids.comment, ir.RelationToMany, ir.OperatorRelationSome, &depthThree)
	variants = append(variants,
		variantProbe("relation/recursive-depth-three", ir.OperatorRelationSome, relationCondition(ids.comment, ids.commentReplies, ids.commentReplyRelation, ids.comment, ir.RelationToMany, ir.OperatorRelationSome, &nestedReplies)),
	)

	leaves := []ir.Condition{equalAda, exactLarge, scoreZero, nameBob,
		scalarCondition(ids.user, ids.userName, types.text, ir.OperatorStartsWith, ir.ComparisonASCIIInsensitive, oneOperand(stringValue("a"))),
		scalarCondition(ids.user, ids.userScore, types.int64Nullable, ir.OperatorGreaterThan, ir.ComparisonSensitive, oneOperand(signedValue(0))),
	}
	for iteration := uint32(0); iteration < 16; iteration++ {
		state := uint64(DerivedSeed(iteration))
		left := leaves[int(nextDeterministic(&state)%uint64(len(leaves)))]
		right := leaves[int(nextDeterministic(&state)%uint64(len(leaves)))]
		logical := ir.LogicalAnd
		if nextDeterministic(&state)&1 != 0 {
			logical = ir.LogicalOr
		}
		tree := mustLogical(ids.user, logical, left, right)
		if nextDeterministic(&state)&1 != 0 {
			tree = mustLogical(ids.user, ir.LogicalNot, tree)
		}
		variants = append(variants, variantProbe(fmt.Sprintf("generated/seed-%d", DerivedSeed(iteration)), ir.OperatorEqual, tree))
	}
	return variants
}

func variantProbe(name string, operatorID ir.OperatorID, condition ir.Condition) Probe {
	return Probe{name: name, operatorID: operatorID, condition: condition}
}

func nextDeterministic(state *uint64) uint64 {
	*state ^= *state << 13
	*state ^= *state >> 7
	*state ^= *state << 17
	return *state
}

func representativeCondition(ids socialIDs, types socialTypeSet, operatorID ir.OperatorID) ir.Condition {
	switch operatorID {
	case ir.OperatorEqual:
		return scalarCondition(ids.user, ids.userName, types.text, operatorID, ir.ComparisonSensitive, oneOperand(stringValue("Ada")))
	case ir.OperatorNotEqual:
		return scalarCondition(ids.user, ids.userScore, types.int64Nullable, operatorID, ir.ComparisonSensitive, oneOperand(signedValue(2)))
	case ir.OperatorIn, ir.OperatorNotIn:
		return scalarCondition(ids.user, ids.userScore, types.int64Nullable, operatorID, ir.ComparisonSensitive, manyOperand(signedValue(0), signedValue(2)))
	case ir.OperatorLessThan, ir.OperatorLessThanOrEqual, ir.OperatorGreaterThan, ir.OperatorGreaterThanOrEqual:
		return scalarCondition(ids.user, ids.userScore, types.int64Nullable, operatorID, ir.ComparisonSensitive, oneOperand(signedValue(1)))
	case ir.OperatorContains:
		return scalarCondition(ids.user, ids.userName, types.text, operatorID, ir.ComparisonASCIIInsensitive, oneOperand(stringValue("ali")))
	case ir.OperatorStartsWith:
		return scalarCondition(ids.user, ids.userName, types.text, operatorID, ir.ComparisonASCIIInsensitive, oneOperand(stringValue("a")))
	case ir.OperatorEndsWith:
		return scalarCondition(ids.user, ids.userName, types.text, operatorID, ir.ComparisonSensitive, oneOperand(stringValue("a")))
	case ir.OperatorIsNull, ir.OperatorIsNotNull:
		return scalarCondition(ids.user, ids.userScore, types.int64Nullable, operatorID, ir.ComparisonSensitive, ir.NoOperand())
	case ir.OperatorListEqual:
		return listCondition(ids.user, ids.userTags, types.stringList, operatorID, oneOperand(listValue(stringValue("go"), stringValue("db"))))
	case ir.OperatorListHas:
		return listCondition(ids.user, ids.userTags, types.stringList, operatorID, oneOperand(stringValue("go")))
	case ir.OperatorListHasEvery, ir.OperatorListHasSome:
		return listCondition(ids.user, ids.userTags, types.stringList, operatorID, manyOperand(stringValue("go"), stringValue("db")))
	case ir.OperatorListIsEmpty:
		return listCondition(ids.user, ids.userTags, types.stringList, operatorID, ir.FlagOperand(true))
	case ir.OperatorListIsNull, ir.OperatorListIsNotNull:
		return listCondition(ids.user, ids.userTags, types.stringList, operatorID, ir.NoOperand())
	case ir.OperatorJSONIsNull, ir.OperatorJSONIsNotNull:
		return jsonCondition(ids.user, ids.userProfile, types.json, operatorID, ir.ComparisonSensitive, jsonPath(), ir.NoOperand())
	case ir.OperatorJSONEqual:
		return jsonCondition(ids.user, ids.userProfile, types.json, operatorID, ir.ComparisonSensitive, jsonPathKey("slot"), oneOperand(jsonValue(jsonString("AÉBackend"))))
	case ir.OperatorJSONNotEqual:
		return jsonCondition(ids.user, ids.userProfile, types.json, operatorID, ir.ComparisonSensitive, jsonPathKey("slot"), oneOperand(jsonValue(jsonString("other"))))
	case ir.OperatorJSONLessThan, ir.OperatorJSONLessThanOrEqual, ir.OperatorJSONGreaterThan, ir.OperatorJSONGreaterThanOrEqual:
		return jsonCondition(ids.user, ids.userProfile, types.json, operatorID, ir.ComparisonSensitive, jsonPathKey("count"), oneOperand(jsonValue(jsonNumber("9007199254740992"))))
	case ir.OperatorJSONStringContains:
		return jsonCondition(ids.user, ids.userProfile, types.json, operatorID, ir.ComparisonASCIIInsensitive, jsonPathKey("slot"), oneOperand(jsonValue(jsonString("backend"))))
	case ir.OperatorJSONStringStartsWith:
		return jsonCondition(ids.user, ids.userProfile, types.json, operatorID, ir.ComparisonASCIIInsensitive, jsonPathKey("slot"), oneOperand(jsonValue(jsonString("aÉ"))))
	case ir.OperatorJSONStringEndsWith:
		return jsonCondition(ids.user, ids.userProfile, types.json, operatorID, ir.ComparisonSensitive, jsonPathKey("slot"), oneOperand(jsonValue(jsonString("Backend"))))
	case ir.OperatorJSONArrayContains:
		return jsonCondition(ids.user, ids.userProfile, types.json, operatorID, ir.ComparisonSensitive, jsonPathKey("arr"), oneOperand(jsonValue(jsonString("tail"))))
	case ir.OperatorJSONArrayStartsWith:
		return jsonCondition(ids.user, ids.userProfile, types.json, operatorID, ir.ComparisonSensitive, jsonPathKey("arr"), oneOperand(jsonValue(jsonNumber("1"))))
	case ir.OperatorJSONArrayEndsWith:
		return jsonCondition(ids.user, ids.userProfile, types.json, operatorID, ir.ComparisonSensitive, jsonPathKey("arr"), oneOperand(jsonValue(jsonString("tail"))))
	case ir.OperatorRelationIs, ir.OperatorRelationIsNot, ir.OperatorRelationIsNull, ir.OperatorRelationIsNotNull:
		var child *ir.Condition
		if operatorID == ir.OperatorRelationIs || operatorID == ir.OperatorRelationIsNot {
			value := scalarCondition(ids.user, ids.userName, types.text, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(stringValue("Ada")))
			child = &value
		}
		return relationCondition(ids.post, ids.postAuthor, ids.postAuthorRelation, ids.user, ir.RelationToOne, operatorID, child)
	case ir.OperatorRelationSome, ir.OperatorRelationEvery, ir.OperatorRelationNone:
		child := scalarCondition(ids.post, ids.postTitle, types.text, ir.OperatorContains, ir.ComparisonASCIIInsensitive, oneOperand(stringValue("go")))
		return relationCondition(ids.user, ids.userPosts, ids.userPostsRelation, ids.post, ir.RelationToMany, operatorID, &child)
	default:
		panic(fmt.Sprintf("policy oracle: no representative for operator %d", operatorID))
	}
}

func scalarCondition(model ir.ModelID, field ir.FieldID, typ ir.TypeRef, id ir.OperatorID, mode ir.ComparisonMode, operand ir.Operand) ir.Condition {
	requirements := mustRequirements(id, operator.Shape{Node: ir.ConditionScalar, FieldType: typ, Operand: operand, Mode: mode, Providers: ir.PortableProviders()})
	value, err := ir.NewScalar(model, field, typ, id, mode, operand, requirements)
	return must(value, err)
}

func listCondition(model ir.ModelID, field ir.FieldID, typ ir.TypeRef, id ir.OperatorID, operand ir.Operand) ir.Condition {
	requirements := mustRequirements(id, operator.Shape{Node: ir.ConditionList, FieldType: typ, Operand: operand, Mode: ir.ComparisonSensitive, Providers: ir.PortableProviders()})
	value, err := ir.NewList(model, field, typ, id, operand, requirements)
	return must(value, err)
}

func jsonCondition(model ir.ModelID, field ir.FieldID, typ ir.TypeRef, id ir.OperatorID, mode ir.ComparisonMode, path ir.JSONPath, operand ir.Operand) ir.Condition {
	requirements := mustRequirements(id, operator.Shape{Node: ir.ConditionJSON, FieldType: typ, Operand: operand, Mode: mode, Path: path, Providers: ir.PortableProviders()})
	value, err := ir.NewJSON(model, field, typ, id, mode, path, operand, requirements)
	return must(value, err)
}

func relationCondition(model ir.ModelID, field ir.FieldID, relation ir.RelationID, target ir.ModelID, cardinality ir.RelationCardinality, id ir.OperatorID, child *ir.Condition) ir.Condition {
	requirements := mustRequirements(id, operator.Shape{Node: ir.ConditionRelation, Operand: ir.NoOperand(), Mode: ir.ComparisonSensitive, Cardinality: cardinality, HasChild: child != nil, Providers: ir.PortableProviders()})
	value, err := ir.NewRelation(model, field, relation, target, cardinality, id, child, requirements)
	return must(value, err)
}

func mustRequirements(id ir.OperatorID, shape operator.Shape) []ir.Requirement {
	value, err := operator.ValidateShape(id, shape)
	return must(value, err)
}

func mustLogical(model ir.ModelID, operatorID ir.LogicalOperator, children ...ir.Condition) ir.Condition {
	value, err := ir.NewLogical(model, operatorID, children)
	return must(value, err)
}

func seedRow(identity Identity, model ir.ModelID, record evaluate.Record, cells ...SeedCell) Row {
	return Row{identity: identity, model: model, scope: SeedNormal, record: record, cells: append([]SeedCell(nil), cells...)}
}

func seedDriftRow(identity Identity, model ir.ModelID, record evaluate.Record, cells ...SeedCell) Row {
	return Row{identity: identity, model: model, scope: SeedDanglingRelation, record: record, cells: append([]SeedCell(nil), cells...)}
}

func mustRecord(model ir.ModelID, fields ...evaluate.Field) evaluate.Record {
	value, err := evaluate.NewRecord(model, fields...)
	return must(value, err)
}

func mustValueField(field ir.FieldID, value ir.Value) evaluate.Field {
	result, err := evaluate.ValueField(field, value)
	return must(result, err)
}

func mustListField(field ir.FieldID, values ...ir.Value) evaluate.Field {
	elements := make([]evaluate.ListElement, len(values))
	for index, value := range values {
		elements[index] = mustListElement(value)
	}
	return mustListFieldElements(field, elements...)
}

func mustListFieldElements(field ir.FieldID, elements ...evaluate.ListElement) evaluate.Field {
	value, err := evaluate.ListField(field, elements...)
	return must(value, err)
}

func mustListElement(value ir.Value) evaluate.ListElement {
	result, err := evaluate.ValidListElement(value)
	return must(result, err)
}

func mustToOne(field ir.FieldID, target ir.ModelID, rows ...evaluate.Record) evaluate.Field {
	value, err := evaluate.ToOneField(field, target, rows...)
	return must(value, err)
}

func mustToMany(field ir.FieldID, target ir.ModelID, rows ...evaluate.Record) evaluate.Field {
	value, err := evaluate.ToManyField(field, target, rows...)
	return must(value, err)
}

func mustValueCell(field ir.FieldID, value ir.Value) SeedCell {
	result, err := ValueCell(field, value)
	return must(result, err)
}

func mustRawJSONCell(field ir.FieldID, raw string) SeedCell {
	result, err := RawJSONCell(field, raw)
	return must(result, err)
}

func mustType(kind ir.ValueKind, nullable bool, element *ir.TypeRef, capability ir.Capability) ir.TypeRef {
	precision, scale := uint16(0), uint16(0)
	value, err := ir.NewTypeRef(kind, nullable, precision, scale, ir.EnumID{}, element, capability)
	return must(value, err)
}

func stringValue(text string) ir.Value {
	value, err := ir.StringValue(text)
	return must(value, err)
}

func signedValue(value int64) ir.Value {
	result, err := ir.SignedValue(ir.ValueInt64, value)
	return must(result, err)
}

func listValue(values ...ir.Value) ir.Value {
	result, err := ir.NewListValue(values)
	return must(result, err)
}

func jsonValue(value ir.JSONValue) ir.Value {
	result, err := ir.NewJSONValue(value)
	return must(result, err)
}

func jsonString(text string) ir.JSONValue {
	value, err := ir.JSONStringValue(text)
	return must(value, err)
}

func jsonNumber(coefficient string) ir.JSONValue {
	number, err := ir.NewJSONNumber(false, []byte(coefficient), 0)
	number = must(number, err)
	value, err := ir.JSONNumberValueOf(number)
	return must(value, err)
}

func jsonArray(values ...ir.JSONValue) ir.JSONValue {
	value, err := ir.JSONArrayValue(values)
	return must(value, err)
}

func jsonMember(key string, value ir.JSONValue) ir.JSONMember {
	member, err := ir.NewJSONMember(key, value)
	return must(member, err)
}

func jsonObject(members ...ir.JSONMember) ir.JSONValue {
	value, err := ir.JSONObjectValue(members)
	return must(value, err)
}

func jsonPath() ir.JSONPath {
	value, err := ir.NewJSONPath()
	return must(value, err)
}

func jsonPathKey(key string) ir.JSONPath {
	segment, err := ir.JSONKeySegment(key)
	segment = must(segment, err)
	value, err := ir.NewJSONPath(segment)
	return must(value, err)
}

func oneOperand(value ir.Value) ir.Operand {
	result, err := ir.OneOperand(value)
	return must(result, err)
}

func manyOperand(values ...ir.Value) ir.Operand {
	result, err := ir.ManyOperand(values)
	return must(result, err)
}

func jsonNullOperand(kind ir.JSONNullKind) ir.Operand {
	result, err := ir.JSONNullOperand(kind)
	return must(result, err)
}

func uuid(value byte) (result [16]byte) {
	result[0], result[15] = 0xa5, value
	return result
}

func modelID(value byte) (result ir.ModelID) {
	result[0], result[15] = 0x51, value
	return result
}

func fieldID(model, value byte) (result ir.FieldID) {
	result[0], result[1], result[15] = 0x52, model, value
	return result
}

func relationID(value byte) (result ir.RelationID) {
	result[0], result[15] = 0x53, value
	return result
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
