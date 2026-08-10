package runtime

import (
	"context"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

// TestMutationJOIN_FOR_RELATION_FILTERUsesExistsWithoutChangingCardinality
// carries the JOIN_FOR_RELATION_FILTER mutation from 04-statement-shape. The
// first half proves a failed relation lookup cannot erase a satisfied local OR
// branch. The second proves a to-many filter cannot multiply roots before Take.
func TestMutationJOIN_FOR_RELATION_FILTERUsesExistsWithoutChangingCardinality(t *testing.T) {
	h := newOracleHarness(t)
	h.seedUser(t, "00000000-0000-0000-0000-000000000001", "match-author")
	h.seedUser(t, "00000000-0000-0000-0000-000000000002", "second")
	h.seedUser(t, "00000000-0000-0000-0000-000000000003", "third")
	h.seedPost(t, "00000000-0000-0000-0000-000000000011", "ffffffff-ffff-ffff-ffff-ffffffffffff", "local-match")
	h.seedPost(t, "00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "relation-match")
	for _, seed := range [][3]string{
		{"00000000-0000-0000-0000-000000000021", "00000000-0000-0000-0000-000000000001", "many-a"},
		{"00000000-0000-0000-0000-000000000022", "00000000-0000-0000-0000-000000000001", "many-b"},
		{"00000000-0000-0000-0000-000000000023", "00000000-0000-0000-0000-000000000002", "many-c"},
		{"00000000-0000-0000-0000-000000000024", "00000000-0000-0000-0000-000000000002", "many-d"},
		{"00000000-0000-0000-0000-000000000025", "00000000-0000-0000-0000-000000000003", "many-e"},
	} {
		h.seedPost(t, seed[0], seed[1], seed[2])
	}
	caller, err := h.app.ForPrincipal(context.Background(), oraclePrincipal{})
	if err != nil {
		t.Fatal(err)
	}

	posts, err := CallerFindMany(context.Background(), caller, h.posts,
		golem.Where(golem.Or(
			h.postAuthor.Is(h.userName.Eq("match-author")),
			h.postTitle.Eq("local-match"),
		)),
		golem.OrderBy(h.postID.Asc()),
		golem.Select[oraclePost](h.postID),
	)
	if err != nil {
		t.Fatal(err)
	}
	postIDs := make([]string, len(posts))
	for index, row := range posts {
		value, present := golem.Value(row, h.postID).Get()
		if !present {
			t.Fatalf("post %d ID is absent", index)
		}
		postIDs[index] = value.String()
	}
	wantPosts := []string{
		"00000000-0000-0000-0000-000000000011",
		"00000000-0000-0000-0000-000000000012",
		"00000000-0000-0000-0000-000000000021",
		"00000000-0000-0000-0000-000000000022",
	}
	if !reflect.DeepEqual(postIDs, wantPosts) {
		t.Fatalf("relation/local OR IDs=%v want=%v", postIDs, wantPosts)
	}

	users, err := CallerFindMany(context.Background(), caller, h.users,
		golem.Where(h.userPosts.Some(h.postTitle.StartsWith("many-"))),
		golem.OrderBy(h.userName.Asc()),
		golem.Take[oracleUser](2),
		golem.Select[oracleUser](h.userName),
	)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(users))
	for index, row := range users {
		value, present := golem.Value(row, h.userName).Get()
		if !present {
			t.Fatalf("user %d name is absent", index)
		}
		names[index] = value
	}
	if want := []string{"match-author", "second"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("to-many filtered page=%v want=%v", names, want)
	}
}
