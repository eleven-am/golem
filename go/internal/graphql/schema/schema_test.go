package schema

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestSocialSDLIsDeterministicAndValid(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	first, err := Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	if first.SDL != second.SDL {
		t.Fatal("SDL generation is not deterministic")
	}
	if _, err := gqlparser.LoadSchema(astSource(first.SDL)); err != nil {
		t.Fatalf("invalid generated SDL: %v\n%s", err, first.SDL)
	}
	for _, expected := range []string{"type User", "type Post", "input CommentWhereInput", "input PostUpdateInput", "createPost", "updateManyPosts", "comments(where:"} {
		if !strings.Contains(first.SDL, expected) {
			t.Errorf("SDL missing %q", expected)
		}
	}
}

func TestGeneratedGraphQLSDLMatchesExposureOperationAndNullabilityMatrix(t *testing.T) {
	document := socialDocument(t)
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatal(err)
	}

	user := requireDefinition(t, parsed, "User")
	if user.Fields.ForName("email") != nil {
		t.Fatal("hidden User.email was emitted in the GraphQL output")
	}
	for _, fieldName := range []string{"id", "handle", "createdAt", "posts", "comments", "friendshipsFrom", "friendshipsTo", "_count"} {
		field := user.Fields.ForName(fieldName)
		if field == nil {
			t.Errorf("User output omitted exposed field %s", fieldName)
			continue
		}
		if field.Type.NonNull {
			t.Errorf("conditionally maskable User.%s is non-null: %s", fieldName, field.Type.String())
		}
	}

	query := requireDefinition(t, parsed, "Query")
	mutation := requireDefinition(t, parsed, "Mutation")
	for _, rootName := range []string{"user", "users", "post", "posts", "comment", "comments", "friendship", "friendships", "tag", "tags", "postTag", "postTags"} {
		if query.Fields.ForName(rootName) == nil {
			t.Errorf("enabled query root %s was not emitted", rootName)
		}
	}
	for _, rootName := range []string{"createPost", "updatePost", "upsertPost", "deletePost", "updateManyPosts", "deleteManyPosts"} {
		if mutation.Fields.ForName(rootName) == nil {
			t.Errorf("enabled mutation root %s was not emitted", rootName)
		}
	}
	for typeName, definition := range parsed.Types {
		if strings.Contains(typeName, "Aggregate") || strings.Contains(typeName, "GroupBy") || strings.Contains(typeName, "Subscription") {
			t.Errorf("pre-P6/P7 type %s was emitted as %s", typeName, definition.Kind)
		}
	}
	if parsed.Subscription != nil {
		t.Fatal("P5 schema emitted a subscription root before P7")
	}
}

func TestGeneratedGraphQLArtifactsAreByteIdenticalAcrossShuffledInput(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	first, err := Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	shuffled := *compiled.Compilation
	shuffled.Model.Models = append([]compilerir.ModelDeclIR(nil), shuffled.Model.Models...)
	shuffled.Model.Relations = append([]compilerir.RelationIR(nil), shuffled.Model.Relations...)
	shuffled.Contract.Models = append([]compilerir.ModelContractIR(nil), shuffled.Contract.Models...)
	shuffled.Contract.Enums = append([]compilerir.EnumContractIR(nil), shuffled.Contract.Enums...)
	reverse(shuffled.Model.Models)
	reverse(shuffled.Model.Relations)
	reverse(shuffled.Contract.Models)
	reverse(shuffled.Contract.Enums)
	for index := range shuffled.Model.Models {
		shuffled.Model.Models[index].Fields = append([]compilerir.FieldIR(nil), shuffled.Model.Models[index].Fields...)
		reverse(shuffled.Model.Models[index].Fields)
	}
	for index := range shuffled.Contract.Models {
		shuffled.Contract.Models[index].Fields = append([]compilerir.FieldContractIR(nil), shuffled.Contract.Models[index].Fields...)
		shuffled.Contract.Models[index].Selectors = append([]compilerir.SelectorContractIR(nil), shuffled.Contract.Models[index].Selectors...)
		reverse(shuffled.Contract.Models[index].Fields)
		reverse(shuffled.Contract.Models[index].Selectors)
	}
	second, err := Build(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if first.SDL != second.SDL {
		t.Fatal("SDL changed when unordered IR inventories were reversed")
	}
}

func astSource(value string) *ast.Source { return &ast.Source{Name: "generated.graphql", Input: value} }

func TestGraphQLNestedInputCompileAndSchemaFixturesExposeOnlyLegalCardinalityOperations(t *testing.T) {
	document := socialDocument(t)
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatal(err)
	}

	postUpdate := requireDefinition(t, parsed, "PostUpdateInput")
	postUpdateMany := requireDefinition(t, parsed, "PostUpdateManyInput")
	postCreate := requireDefinition(t, parsed, "PostCreateInput")
	if postCreate.Fields.ForName("authorID") != nil || postUpdate.Fields.ForName("authorID") != nil {
		t.Fatal("source relation-owned Post.authorID is authorable beside the author relation envelope")
	}
	if postUpdateMany.Fields.ForName("authorID") == nil {
		t.Fatal("relation-free PostUpdateManyInput unexpectedly lost authorID")
	}
	for _, relation := range []string{"author", "comments", "postTags"} {
		if postUpdate.Fields.ForName(relation) == nil {
			t.Errorf("PostUpdateInput omitted relation %s", relation)
		}
		if postUpdateMany.Fields.ForName(relation) != nil {
			t.Errorf("PostUpdateManyInput illegally contains relation %s", relation)
		}
	}
	for _, scalar := range []string{"id", "authorID", "title", "body"} {
		if postUpdateMany.Fields.ForName(scalar) == nil {
			t.Errorf("PostUpdateManyInput omitted scalar operation %s", scalar)
		}
	}
	for _, readOnly := range []string{"search", "createdAt"} {
		if postUpdate.Fields.ForName(readOnly) != nil || postUpdateMany.Fields.ForName(readOnly) != nil {
			t.Errorf("database/read-only field %s is writable", readOnly)
		}
	}

	requireFieldNames(t, requireDefinition(t, parsed, "UUIDUpdateOperationsInput"), "set")
	requireFieldNames(t, requireDefinition(t, parsed, "NullableUUIDUpdateOperationsInput"), "set", "setNull")
	requireFieldNames(t, requireDefinition(t, parsed, "BigIntUpdateOperationsInput"), "decrement", "increment", "set")
	if requireDefinition(t, parsed, "UUIDUpdateOperationsInput").Fields.ForName("setNull") != nil {
		t.Fatal("non-null UUID update envelope exposes setNull")
	}

	requireFieldNames(t, requireDefinition(t, parsed, "BooleanFilter"), "equals", "in", "not", "notIn")
	requireFieldNames(t, requireDefinition(t, parsed, "StringFilter"), "contains", "endsWith", "equals", "gt", "gte", "in", "lt", "lte", "mode", "not", "notIn", "startsWith")
	requireFieldNames(t, requireDefinition(t, parsed, "NullableStringFilter"), "contains", "endsWith", "equals", "gt", "gte", "in", "isNull", "lt", "lte", "mode", "not", "notIn", "startsWith")
	requireFieldNames(t, requireDefinition(t, parsed, "JSONFilter"), "arrayContains", "arrayEndsWith", "arrayStartsWith", "equals", "gt", "gte", "lt", "lte", "mode", "not", "path", "stringContains", "stringEndsWith", "stringStartsWith")
	if parsed.Types["BytesListFilter"] != nil || parsed.Types["NullableBytesListFilter"] != nil {
		t.Fatal("SDL exposes scalar-list operators for provider-ineligible Bytes elements")
	}

	author := postCreate.Fields.ForName("author")
	if author == nil || author.Type == nil || !author.Type.NonNull || author.Type.NamedType != "PostAuthorCreateRelationInput" {
		t.Fatalf("required source relation type = %#v", author)
	}
	requireFieldNames(t, requireDefinition(t, parsed, "PostAuthorCreateRelationInput"), "connect", "connectOrCreate", "create")
	requireFieldNames(t, requireDefinition(t, parsed, "PostAuthorUpdateRelationInput"), "connect", "connectOrCreate", "create", "update", "upsert")
	if definition := requireDefinition(t, parsed, "PostAuthorUpdateRelationInput"); definition.Fields.ForName("disconnect") != nil || definition.Fields.ForName("delete") != nil {
		t.Fatal("required source to-one relation exposes disconnect/delete")
	}
	requireFieldNames(t, requireDefinition(t, parsed, "CommentReplyToUpdateRelationInput"), "connect", "connectOrCreate", "create", "delete", "disconnect", "update", "upsert")
	for _, illegal := range []string{"createMany", "set", "updateMany", "deleteMany"} {
		if requireDefinition(t, parsed, "CommentReplyToUpdateRelationInput").Fields.ForName(illegal) != nil {
			t.Errorf("to-one update relation exposes %s", illegal)
		}
	}
	requireFieldNames(t, requireDefinition(t, parsed, "PostCommentsUpdateRelationInput"), "connect", "connectOrCreate", "create", "createMany", "delete", "deleteMany", "disconnect", "set", "update", "updateMany", "upsert")

	commentWithoutPost := requireDefinition(t, parsed, "CommentCreateWithoutPostInput")
	if commentWithoutPost.Fields.ForName("authorID") != nil || commentWithoutPost.Fields.ForName("author") == nil || !commentWithoutPost.Fields.ForName("author").Type.NonNull {
		t.Fatalf("CommentCreateWithoutPost relation ownership = %#v", commentWithoutPost.Fields)
	}
	postTagWithoutPost := requireDefinition(t, parsed, "PostTagCreateWithoutPostInput")
	if postTagWithoutPost.Fields.ForName("tagName") != nil || postTagWithoutPost.Fields.ForName("tag") == nil || !postTagWithoutPost.Fields.ForName("tag").Type.NonNull {
		t.Fatalf("PostTagCreateWithoutPost relation ownership = %#v", postTagWithoutPost.Fields)
	}
}

func TestNestedInputCompileAndSchemaFixturesExposeCompleteSocialWithoutGraph(t *testing.T) {
	document := socialDocument(t)
	parsed, err := gqlparser.LoadSchema(astSource(document.SDL))
	if err != nil {
		t.Fatal(err)
	}
	type edge struct {
		prefix, target, back string
		excluded             []string
	}
	edges := []edge{
		{prefix: "UserPosts", target: "Post", back: "Author", excluded: []string{"author", "authorID"}},
		{prefix: "UserComments", target: "Comment", back: "Author", excluded: []string{"author", "authorID"}},
		{prefix: "UserFriendshipsFrom", target: "Friendship", back: "User", excluded: []string{"user", "userID"}},
		{prefix: "UserFriendshipsTo", target: "Friendship", back: "Friend", excluded: []string{"friend", "friendID"}},
		{prefix: "PostAuthor", target: "User", back: "Posts", excluded: []string{"posts"}},
		{prefix: "PostComments", target: "Comment", back: "Post", excluded: []string{"post", "postID"}},
		{prefix: "PostPostTags", target: "PostTag", back: "Post", excluded: []string{"post", "postID"}},
		{prefix: "CommentPost", target: "Post", back: "Comments", excluded: []string{"comments"}},
		{prefix: "CommentAuthor", target: "User", back: "Comments", excluded: []string{"comments"}},
		{prefix: "CommentReplyTo", target: "Comment", back: "Replies", excluded: []string{"replies"}},
		{prefix: "CommentReplies", target: "Comment", back: "ReplyTo", excluded: []string{"replyTo", "parentID"}},
		{prefix: "FriendshipUser", target: "User", back: "FriendshipsFrom", excluded: []string{"friendshipsFrom"}},
		{prefix: "FriendshipFriend", target: "User", back: "FriendshipsTo", excluded: []string{"friendshipsTo"}},
		{prefix: "TagPostTags", target: "PostTag", back: "Tag", excluded: []string{"tag", "tagName"}},
		{prefix: "PostTagPost", target: "Post", back: "PostTags", excluded: []string{"postTags"}},
		{prefix: "PostTagTag", target: "Tag", back: "PostTags", excluded: []string{"postTags"}},
	}
	for _, value := range edges {
		t.Run(value.prefix, func(t *testing.T) {
			createName := value.target + "CreateWithout" + value.back + "Input"
			updateName := value.target + "UpdateWithout" + value.back + "Input"
			create := requireDefinition(t, parsed, createName)
			update := requireDefinition(t, parsed, updateName)
			for _, excluded := range value.excluded {
				if create.Fields.ForName(excluded) != nil || update.Fields.ForName(excluded) != nil {
					t.Errorf("%s/%s retained excluded back-edge field %s", createName, updateName, excluded)
				}
			}

			createRelation := requireDefinition(t, parsed, value.prefix+"CreateRelationInput")
			if field := createRelation.Fields.ForName("create"); field != nil && namedType(field.Type) != createName {
				t.Errorf("create routes to %s, want %s", namedType(field.Type), createName)
			}
			if field := createRelation.Fields.ForName("createMany"); field != nil && namedType(field.Type) != createName {
				t.Errorf("createMany routes to %s, want %s", namedType(field.Type), createName)
			}
			if field := createRelation.Fields.ForName("connectOrCreate"); field != nil {
				helperName := value.target + "ConnectOrCreateWithout" + value.back + "Input"
				if namedType(field.Type) != helperName {
					t.Errorf("connectOrCreate routes to %s, want %s", namedType(field.Type), helperName)
				}
				helper := requireDefinition(t, parsed, helperName)
				if got := namedType(helper.Fields.ForName("create").Type); got != createName {
					t.Errorf("%s.create routes to %s, want %s", helperName, got, createName)
				}
			}

			updateRelation := requireDefinition(t, parsed, value.prefix+"UpdateRelationInput")
			if field := updateRelation.Fields.ForName("create"); field != nil && namedType(field.Type) != createName {
				t.Errorf("nested update create routes to %s, want %s", namedType(field.Type), createName)
			}
			if field := updateRelation.Fields.ForName("update"); field != nil {
				got := namedType(field.Type)
				if strings.Contains(got, "WithWhere") {
					helper := requireDefinition(t, parsed, got)
					got = namedType(helper.Fields.ForName("data").Type)
				}
				if got != updateName {
					t.Errorf("nested update routes to %s, want %s", got, updateName)
				}
			}
			if field := updateRelation.Fields.ForName("upsert"); field != nil {
				helper := requireDefinition(t, parsed, namedType(field.Type))
				if got := namedType(helper.Fields.ForName("create").Type); got != createName {
					t.Errorf("upsert create routes to %s, want %s", got, createName)
				}
				if got := namedType(helper.Fields.ForName("update").Type); got != updateName {
					t.Errorf("upsert update routes to %s, want %s", got, updateName)
				}
			}
		})
	}
}

func TestExcludedRelationTargetFailsGenerationInsteadOfSilentPruning(t *testing.T) {
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	compilation := *compiled.Compilation
	compilation.Contract.Models = append([]compilerir.ModelContractIR(nil), compilation.Contract.Models...)
	for index := range compilation.Contract.Models {
		if compilation.Contract.Models[index].GraphQLName == "User" {
			compilation.Contract.Models[index].Exposed = false
		}
	}
	if _, err := Build(compilation); err == nil || !strings.Contains(err.Error(), "targets hidden model") {
		t.Fatalf("hidden relation target error = %v", err)
	}
}

func socialDocument(t *testing.T) Document {
	t.Helper()
	compiled := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(compiled.Diagnostics) != 0 || compiled.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", compiled.Diagnostics)
	}
	document, err := Build(*compiled.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func requireDefinition(t *testing.T, schema *ast.Schema, name string) *ast.Definition {
	t.Helper()
	definition := schema.Types[name]
	if definition == nil {
		t.Fatalf("SDL omitted definition %s", name)
	}
	return definition
}

func requireFieldNames(t *testing.T, definition *ast.Definition, expected ...string) {
	t.Helper()
	actual := make([]string, len(definition.Fields))
	for index, field := range definition.Fields {
		actual[index] = field.Name
	}
	sort.Strings(actual)
	want := append([]string(nil), expected...)
	sort.Strings(want)
	if strings.Join(actual, ",") != strings.Join(want, ",") {
		t.Errorf("%s fields = %v, want %v", definition.Name, actual, want)
	}
}

func namedType(value *ast.Type) string {
	if value == nil {
		return ""
	}
	if value.Elem != nil {
		return namedType(value.Elem)
	}
	return value.NamedType
}

func reverse[T any](values []T) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
