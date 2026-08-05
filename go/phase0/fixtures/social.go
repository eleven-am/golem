package fixtures

import "github.com/eleven-am/golem/go/phase0"

type Actor struct {
	ID    string
	Admin bool
}

// Post and User are marker types standing in for future generated model types.
// Phase 0 does not prescribe how application structs or generation will work.
type Post struct{}
type User struct{}

var (
	PostID        = phase0.NewField[Post, string]("Post", "id", phase0.ScalarString)
	PostAuthorID  = phase0.NewField[Post, string]("Post", "authorId", phase0.ScalarString)
	PostTitle     = phase0.NewField[Post, string]("Post", "title", phase0.ScalarString)
	PostBody      = phase0.NewField[Post, string]("Post", "body", phase0.ScalarString)
	PostPublished = phase0.NewField[Post, bool]("Post", "published", phase0.ScalarBoolean)

	UserID    = phase0.NewField[User, string]("User", "id", phase0.ScalarString)
	UserName  = phase0.NewField[User, string]("User", "name", phase0.ScalarString)
	UserEmail = phase0.NewField[User, string]("User", "email", phase0.ScalarString)
	UserPhone = phase0.NewField[User, string]("User", "phone", phase0.ScalarString)

	PostAuthor  = phase0.NewToOneRelation[Post, User]("Post", "author", "User")
	UserFriends = phase0.NewToManyRelation[User, User]("User", "friends", "User")
)

type PostPolicy struct{}

func (PostPolicy) Define(r *phase0.Rules[Post], actor Actor) {
	if actor.Admin {
		r.CanRead(phase0.All[Post]())
		r.CanCreate(phase0.All[Post]())
		r.CanDelete(phase0.All[Post]())
		r.CanUpdateFields(phase0.All[Post](), PostTitle, PostBody, PostPublished)
		return
	}

	owned := PostAuthorID.Eq(actor.ID)
	r.CanRead(PostPublished.Eq(true).Or(owned))
	r.CanCreate(owned)
	r.CanDelete(owned)
	r.CanUpdateFields(owned, PostTitle, PostBody, PostPublished)
	r.CannotUpdateFields(phase0.All[Post](), PostAuthorID)
}

type UserPolicy struct{}

func (UserPolicy) Define(r *phase0.Rules[User], actor Actor) {
	self := UserID.Eq(actor.ID)
	r.CanRead(phase0.All[User]())
	r.CannotReadFields(phase0.All[User](), UserEmail, UserPhone)
	r.CanReadFields(self, UserEmail, UserPhone)
	r.CanUpdateFields(self, UserName, UserEmail)
	r.CannotUpdateFields(phase0.All[User](), UserID)
}
