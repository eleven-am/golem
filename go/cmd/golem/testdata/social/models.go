package social

import (
	"time"

	golem "github.com/eleven-am/golem/go/golem"
)

type User struct {
	_ struct{} `golem:"model;id=social.User;table=users;graphql=User"`
	_ struct{} `golem:"unique=uq_users_handle(handle)"`
	_ struct{} `golem:"index=idx_users_created(handle,created_at)"`

	ID              golem.UUID   `db:"id" golem:"id=social.User.ID;pk;default=uuid"`
	Handle          string       `db:"handle" golem:"type=varchar(40);immutable"`
	Email           string       `db:"email" golem:"type=varchar(255);hidden"`
	CreatedAt       time.Time    `db:"created_at" golem:"default=now;readonly"`
	Posts           []Post       `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
	Comments        []Comment    `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
	FriendshipsFrom []Friendship `db:"-" golem:"relation=has_many;name=Origin;fields=id;references=user_id"`
	FriendshipsTo   []Friendship `db:"-" golem:"relation=has_many;name=Destination;fields=id;references=friend_id"`
}

type Post struct {
	_ struct{} `golem:"model;id=social.Post;table=posts"`
	_ struct{} `golem:"index=idx_posts_author_created(author_id,created_at)"`

	ID        golem.UUID `db:"id" golem:"id=social.Post.ID;pk;default=uuid"`
	AuthorID  golem.UUID `db:"author_id"`
	Title     string     `db:"title" golem:"type=varchar(160)"`
	Body      string     `db:"body"`
	Search    string     `db:"search" golem:"type=varchar(160);readonly"`
	CreatedAt time.Time  `db:"created_at" golem:"default=now;readonly"`
	Author    *User      `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
	Comments  []Comment  `db:"-" golem:"relation=has_many;fields=id;references=post_id"`
	PostTags  []PostTag  `db:"-" golem:"relation=has_many;fields=id;references=post_id"`
}

func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel[Post](
		golem.Unique[Post]("uq_posts_author_title", Posts.AuthorID, Posts.Title),
		golem.Index[Post]("idx_posts_lower_title").Keys(golem.IndexExpr(golem.Lower(Posts.Title)).Desc()).Where(Posts.Title.Expr().IsNotNull()),
		golem.Check[Post]("ck_posts_title", Posts.Title.Expr().IsNotNull()),
		golem.Generated[Post](Posts.Search, golem.Lower(Posts.Title), golem.Stored),
		golem.RelationOptions(Posts.Author).OnDelete(golem.Cascade),
	)
}

type Comment struct {
	_ struct{} `golem:"model;id=social.Comment;table=comments"`
	_ struct{} `golem:"index=idx_comments_post_parent(post_id,parent_id)"`

	ID        golem.UUID             `db:"id" golem:"id=social.Comment.ID;pk;default=uuid"`
	PostID    golem.UUID             `db:"post_id"`
	AuthorID  golem.UUID             `db:"author_id"`
	ParentID  golem.Null[golem.UUID] `db:"parent_id"`
	Body      string                 `db:"body"`
	CreatedAt time.Time              `db:"created_at" golem:"default=now;readonly"`
	Post      *Post                  `db:"-" golem:"relation=belongs_to;fields=post_id;references=id"`
	Author    *User                  `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
	ReplyTo   *Comment               `db:"-" golem:"relation=belongs_to;name=ReplyTree;fields=parent_id;references=id"`
	Replies   []Comment              `db:"-" golem:"relation=has_many;name=ReplyTree;fields=id;references=parent_id"`
}

type Friendship struct {
	_ struct{} `golem:"model;id=social.Friendship;table=friendships"`
	_ struct{} `golem:"primary=pk_friendships(user_id,friend_id)"`
	_ struct{} `golem:"index=idx_friendships_friend_user(friend_id,user_id)"`

	UserID    golem.UUID `db:"user_id"`
	FriendID  golem.UUID `db:"friend_id"`
	CreatedAt time.Time  `db:"created_at" golem:"default=now;readonly"`
	User      *User      `db:"-" golem:"relation=belongs_to;name=Origin;fields=user_id;references=id"`
	Friend    *User      `db:"-" golem:"relation=belongs_to;name=Destination;fields=friend_id;references=id"`
}

type Tag struct {
	_ struct{} `golem:"model;id=social.Tag;table=tags"`

	ID       golem.UUID `db:"id" golem:"id=social.Tag.ID;pk;default=uuid"`
	Name     string     `db:"name" golem:"type=varchar(64)"`
	PostTags []PostTag  `db:"-" golem:"relation=has_many;fields=name;references=tag_name"`
}

func (Tag) GolemModel() golem.ModelSpec[Tag] {
	return golem.DefineModel[Tag](golem.Unique[Tag]("uq_tags_name", Tags.Name))
}

type PostTag struct {
	_ struct{} `golem:"model;id=social.PostTag;table=post_tags"`
	_ struct{} `golem:"primary=pk_post_tags(post_id,tag_name)"`

	PostID  golem.UUID `db:"post_id"`
	TagName string     `db:"tag_name" golem:"type=varchar(64)"`
	Post    *Post      `db:"-" golem:"relation=belongs_to;fields=post_id;references=id"`
	Tag     *Tag       `db:"-" golem:"relation=belongs_to;fields=tag_name;references=name"`
}

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	rules.CanRead(Users.ID.Eq(actor.UserID))
}

func (Post) DefinePolicy(rules *golem.Rules[Post], actor Actor) {
	rules.CanRead(Posts.AuthorID.Eq(actor.UserID))
}

func (Comment) DefinePolicy(rules *golem.Rules[Comment], actor Actor) {
	rules.CanRead(Comments.AuthorID.Eq(actor.UserID))
}

func (Friendship) DefinePolicy(rules *golem.Rules[Friendship], actor Actor) {
	rules.CanRead(Friendships.UserID.Eq(actor.UserID))
}

func (Tag) DefinePolicy(rules *golem.Rules[Tag], actor Actor) {
	rules.CanRead(Tags.ID.Eq(actor.UserID))
}

func (PostTag) DefinePolicy(rules *golem.Rules[PostTag], actor Actor) {
	rules.CanRead(PostTags.PostID.Eq(actor.UserID))
}
