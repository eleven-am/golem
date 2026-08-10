package p5social

import (
	"sync"

	golem "github.com/eleven-am/golem/go/golem"
)

var policyProbe = struct {
	sync.Mutex
	models []string
}{}

func ResetPolicyProbe() {
	policyProbe.Lock()
	policyProbe.models = nil
	policyProbe.Unlock()
}

func PolicyProbe() []string {
	policyProbe.Lock()
	defer policyProbe.Unlock()
	return append([]string(nil), policyProbe.models...)
}

func recordPolicy(model string) {
	policyProbe.Lock()
	policyProbe.models = append(policyProbe.models, model)
	policyProbe.Unlock()
}

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	recordPolicy("User")
	all := golem.All[User]()
	own := Users.ID.Eq(actor.UserID)
	rules.CanRead(all)
	rules.CanReadFields(all, Users.ID)
	rules.CannotReadFields(all, Users.Name, Users.Posts, Users.Comments, Users.FriendshipsFrom, Users.FriendshipsTo)
	rules.CanReadFields(own, Users.Name, Users.Posts, Users.Comments, Users.FriendshipsFrom, Users.FriendshipsTo)
}

func (Post) DefinePolicy(rules *golem.Rules[Post], _ Actor) {
	recordPolicy("Post")
	all := golem.All[Post]()
	open := Posts.Title.EndsWith("-open")
	rules.CanRead(all)
	rules.CanReadFields(all, Posts.ID, Posts.AuthorID, Posts.Title, Posts.Author)
	rules.CannotReadFields(all, Posts.Comments, Posts.PostTags)
	rules.CanReadFields(open, Posts.Comments, Posts.PostTags)
}

func (Comment) DefinePolicy(rules *golem.Rules[Comment], _ Actor) {
	recordPolicy("Comment")
	all := golem.All[Comment]()
	open := Comments.Body.EndsWith("-open")
	rules.CanRead(all)
	rules.CanReadFields(all, Comments.ID, Comments.PostID, Comments.AuthorID, Comments.ParentID, Comments.Body, Comments.Post, Comments.Author)
	rules.CannotReadFields(all, Comments.ReplyTo, Comments.Replies)
	rules.CanReadFields(open, Comments.ReplyTo, Comments.Replies)
}

func (Friendship) DefinePolicy(rules *golem.Rules[Friendship], _ Actor) {
	recordPolicy("Friendship")
	all := golem.All[Friendship]()
	rules.CanRead(all)
	rules.CanReadFields(all, Friendships.UserID, Friendships.FriendID, Friendships.User, Friendships.Friend)
}

func (Tag) DefinePolicy(rules *golem.Rules[Tag], actor Actor) {
	recordPolicy("Tag")
	reach := Tags.Name.Eq(actor.AllowedTag)
	rules.CanRead(reach)
	rules.CanReadFields(reach, Tags.ID, Tags.Name, Tags.PostTags)
}

func (PostTag) DefinePolicy(rules *golem.Rules[PostTag], _ Actor) {
	recordPolicy("PostTag")
	all := golem.All[PostTag]()
	rules.CanRead(all)
	rules.CanReadFields(all, PostTags.PostID, PostTags.TagName, PostTags.Post, PostTags.Tag)
}
