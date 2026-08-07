package p7oracle

import "fmt"

// Social is the six-model graph required by the P7 evidence contract. The
// values are intentionally plain Go data: expected answers never come from a
// production model decoder or authorization evaluator.
type Social struct {
	Users       []User
	Posts       []Post
	Comments    []Comment
	Friendships []Friendship
	Tags        []Tag
	PostTags    []PostTag
}

type User struct {
	ID      string
	Name    string
	Enabled bool
}

type Post struct {
	ID, AuthorID, Title string
}

type Comment struct {
	ID, PostID, AuthorID, Body string
	ParentID                   *string
}

type Friendship struct{ UserID, FriendID string }
type Tag struct{ ID, Name string }
type PostTag struct{ PostID, TagName string }

func UUID(number int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", number)
}

// CanonicalSocial is independently enumerated and includes recursive comments
// and both compound-key models. Enabled is oracle-only actor state; it is kept
// out of the physical User row so direct SQL can also prove that authorization
// state is not inferred from model decoding.
func CanonicalSocial() Social {
	root := UUID(31)
	return Social{
		Users: []User{
			{ID: UUID(1), Name: "alice", Enabled: true},
			{ID: UUID(2), Name: "bob", Enabled: true},
			{ID: UUID(3), Name: "revoked", Enabled: false},
		},
		Posts: []Post{
			{ID: UUID(11), AuthorID: UUID(1), Title: "public-alpha"},
			{ID: UUID(12), AuthorID: UUID(2), Title: "friends-only"},
			{ID: UUID(13), AuthorID: UUID(3), Title: "hidden-revoked"},
		},
		Comments: []Comment{
			{ID: root, PostID: UUID(11), AuthorID: UUID(2), Body: "root"},
			{ID: UUID(32), PostID: UUID(11), AuthorID: UUID(1), ParentID: &root, Body: "reply"},
		},
		Friendships: []Friendship{{UserID: UUID(1), FriendID: UUID(2)}, {UserID: UUID(2), FriendID: UUID(1)}},
		Tags:        []Tag{{ID: UUID(41), Name: "go"}, {ID: UUID(42), Name: "I-ı-é"}},
		PostTags:    []PostTag{{PostID: UUID(11), TagName: "go"}, {PostID: UUID(11), TagName: "I-ı-é"}},
	}
}

// VisiblePostIDs is the independent policy/filter oracle used by provider
// tests. Alice may read her own posts and posts written by a direct friend;
// both authorization and the caller filter must hold.
func (social Social) VisiblePostIDs(principalID, titlePrefix string) []string {
	friends := map[string]bool{}
	for _, edge := range social.Friendships {
		if edge.UserID == principalID {
			friends[edge.FriendID] = true
		}
	}
	enabled := map[string]bool{}
	for _, user := range social.Users {
		enabled[user.ID] = user.Enabled
	}
	result := make([]string, 0)
	for _, post := range social.Posts {
		if !enabled[principalID] || len(post.Title) < len(titlePrefix) || post.Title[:len(titlePrefix)] != titlePrefix {
			continue
		}
		if post.AuthorID == principalID || friends[post.AuthorID] {
			result = append(result, post.ID)
		}
	}
	return result
}
