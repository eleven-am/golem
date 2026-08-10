package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
)

func TestP5CompleteSocialGraphQLReadAcrossSQLiteAndPostgreSQLProfiles(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		assertP5CompleteSocialGraphQLRead(t, newSocialMutationFixture(t, golem.ModelID{}, nil))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			assertP5CompleteSocialGraphQLRead(t, newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, nil))
		})
	}
}

func assertP5CompleteSocialGraphQLRead(t testing.TB, fixture socialMutationFixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.userCreate(2, "friend")); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.tagDescriptor, fixture.tagCreate(30, "deep-tag")); err != nil {
		t.Fatal(err)
	}
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(ctx, caller, fixture.userDescriptor, fixture.deepSocialCreate()); err != nil {
		t.Fatal(err)
	}

	harness := newP5SocialGraphQLHarness(t, fixture)
	response := harness.server.Execute(ctx, graphMutationPrincipal{}, publicgraphql.Request{Query: `query CompleteSocialRead {
  user(where: {id: "00000000-0000-0000-0000-000000000001"}) {
    id
    name
    posts(orderBy: [{id: asc}], take: 1) {
      id
      title
      author { id name }
      comments(orderBy: [{body: asc}], take: 2) {
        id
        body
        author { id name }
        post { id title }
        replies(orderBy: [{id: asc}], take: 1) {
          id
          body
          replyTo { id body }
        }
      }
      postTags(take: 1) {
        postID
        tagName
        post { id title }
        tag { id name }
      }
    }
    friendshipsFrom(take: 1) {
      userID
      friendID
      user { id name }
      friend { id name }
    }
  }
  tag(where: {name: "deep-tag"}) {
    id
    name
    postTags(take: 1) { post { id title } }
  }
}`})
	if len(response.Errors) != 0 {
		t.Fatalf("complete social GraphQL errors=%#v", response.Errors)
	}
	data, ok := response.Data.(map[string]any)
	if !ok {
		t.Fatalf("complete social response data=%#v", response.Data)
	}
	user := p5SocialObject(t, data["user"], "user")
	p5SocialScalar(t, user, "id", p5UUID(1))
	p5SocialScalar(t, user, "name", "owner")

	posts := p5SocialList(t, user["posts"], "user.posts", 1)
	post := p5SocialObject(t, posts[0], "user.posts[0]")
	p5SocialScalar(t, post, "id", p5UUID(10))
	p5SocialScalar(t, post, "title", "post")
	author := p5SocialObject(t, post["author"], "user.posts[0].author")
	p5SocialScalar(t, author, "id", p5UUID(1))
	p5SocialScalar(t, author, "name", "owner")

	comments := p5SocialList(t, post["comments"], "user.posts[0].comments", 2)
	comment := p5SocialObject(t, comments[0], "user.posts[0].comments[0]")
	p5SocialScalar(t, comment, "id", p5UUID(20))
	p5SocialScalar(t, comment, "body", "comment")
	p5SocialScalar(t, p5SocialObject(t, comment["author"], "comment.author"), "id", p5UUID(1))
	p5SocialScalar(t, p5SocialObject(t, comment["post"], "comment.post"), "id", p5UUID(10))
	replies := p5SocialList(t, comment["replies"], "comment.replies", 1)
	reply := p5SocialObject(t, replies[0], "comment.replies[0]")
	p5SocialScalar(t, reply, "id", p5UUID(21))
	p5SocialScalar(t, reply, "body", "reply")
	p5SocialScalar(t, p5SocialObject(t, reply["replyTo"], "reply.replyTo"), "id", p5UUID(20))
	secondComment := p5SocialObject(t, comments[1], "user.posts[0].comments[1]")
	p5SocialScalar(t, secondComment, "id", p5UUID(21))
	p5SocialScalar(t, secondComment, "body", "reply")

	postTags := p5SocialList(t, post["postTags"], "user.posts[0].postTags", 1)
	postTag := p5SocialObject(t, postTags[0], "user.posts[0].postTags[0]")
	p5SocialScalar(t, postTag, "postID", p5UUID(10))
	p5SocialScalar(t, postTag, "tagName", "deep-tag")
	p5SocialScalar(t, p5SocialObject(t, postTag["post"], "postTag.post"), "id", p5UUID(10))
	p5SocialScalar(t, p5SocialObject(t, postTag["tag"], "postTag.tag"), "name", "deep-tag")

	friendships := p5SocialList(t, user["friendshipsFrom"], "user.friendshipsFrom", 1)
	friendship := p5SocialObject(t, friendships[0], "user.friendshipsFrom[0]")
	p5SocialScalar(t, friendship, "userID", p5UUID(1))
	p5SocialScalar(t, friendship, "friendID", p5UUID(2))
	p5SocialScalar(t, p5SocialObject(t, friendship["user"], "friendship.user"), "name", "owner")
	p5SocialScalar(t, p5SocialObject(t, friendship["friend"], "friendship.friend"), "name", "friend")

	tag := p5SocialObject(t, data["tag"], "tag")
	p5SocialScalar(t, tag, "id", p5UUID(30))
	p5SocialScalar(t, tag, "name", "deep-tag")
	tagLinks := p5SocialList(t, tag["postTags"], "tag.postTags", 1)
	p5SocialScalar(t, p5SocialObject(t, p5SocialObject(t, tagLinks[0], "tag.postTags[0]")["post"], "tag.postTags[0].post"), "id", p5UUID(10))
}

func p5SocialObject(t testing.TB, value any, path string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s=%#v, want object", path, value)
	}
	return object
}

func p5SocialList(t testing.TB, value any, path string, length int) []any {
	t.Helper()
	list, ok := value.([]any)
	if !ok || len(list) != length {
		t.Fatalf("%s=%#v, want list length %d", path, value, length)
	}
	return list
}

func p5SocialScalar(t testing.TB, object map[string]any, field string, want any) {
	t.Helper()
	if got := object[field]; got != want {
		t.Fatalf("field %s=%#v want=%#v in %#v", field, got, want, object)
	}
}
