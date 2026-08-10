// Package social is a complete Golem application authored only through the
// public API. The zz_golem_*.gen.go files beside this source are generated.
package social

import (
	"time"

	"github.com/eleven-am/golem/go/golem"
)

type PostMetadata struct {
	Language string `json:"language"`
	Pinned   bool   `json:"pinned"`
}

type Visibility string

const (
	VisibilityPublic    Visibility = "PUBLIC"
	VisibilityFollowers Visibility = "FOLLOWERS"
	VisibilityPrivate   Visibility = "PRIVATE"
)

func (Visibility) GolemEnum() golem.EnumSpec[Visibility] {
	return golem.DefineEnum(
		golem.EnumValue(VisibilityPublic),
		golem.EnumValue(VisibilityFollowers),
		golem.EnumValue(VisibilityPrivate),
	)
}

type User struct {
	_ struct{} `golem:"model;id=example.social.User;table=users;graphql=User"`
	_ struct{} `golem:"unique=uq_users_handle(handle)"`
	_ struct{} `golem:"index=idx_users_created(created_at,id)"`

	ID        golem.UUID `db:"id" golem:"id=example.social.User.ID;pk;default=uuid"`
	Handle    string     `db:"handle" golem:"type=varchar(40)"`
	Email     string     `db:"email" golem:"type=varchar(255)"`
	CreatedAt time.Time  `db:"created_at" golem:"default=now;readonly"`
	Sessions  []Session  `db:"-" golem:"relation=has_many;fields=id;references=user_id;hidden"`
	Posts     []Post     `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
	Comments  []Comment  `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
}

func (User) GolemModel() golem.ModelSpec[User] {
	return golem.DefineModel(
		golem.GraphQL[User](golem.GraphQLPageSizes(25, 100)),
	)
}

type Session struct {
	_ struct{} `golem:"model;id=example.social.Session;table=sessions;graphql=Session"`
	_ struct{} `golem:"unique=uq_sessions_token_hash(token_hash)"`
	_ struct{} `golem:"index=idx_sessions_user_expires(user_id,expires_at)"`

	ID        golem.UUID `db:"id" golem:"id=example.social.Session.ID;pk;default=uuid"`
	UserID    golem.UUID `db:"user_id"`
	TokenHash []byte     `db:"token_hash"`
	ExpiresAt time.Time  `db:"expires_at"`
	CreatedAt time.Time  `db:"created_at" golem:"default=now;readonly"`
	User      *User      `db:"-" golem:"relation=belongs_to;fields=user_id;references=id"`
}

func (Session) GolemModel() golem.ModelSpec[Session] {
	return golem.DefineModel(
		golem.GraphQL[Session](golem.GraphQLHidden()),
		golem.RelationOptions(Sessions.User).OnDelete(golem.Cascade),
	)
}

type Post struct {
	_ struct{} `golem:"model;id=example.social.Post;table=posts;graphql=Post"`
	_ struct{} `golem:"index=idx_posts_feed(published,created_at,id)"`
	_ struct{} `golem:"index=idx_posts_author_created(author_id,created_at,id)"`

	ID         golem.UUID               `db:"id" golem:"id=example.social.Post.ID;pk;default=uuid"`
	AuthorID   golem.UUID               `db:"author_id" golem:"immutable"`
	Title      string                   `db:"title" golem:"type=varchar(160)"`
	Body       string                   `db:"body"`
	Published  bool                     `db:"published" golem:"default=false"`
	Reactions  int16                    `db:"reactions" golem:"default=0"`
	Priority   int32                    `db:"priority" golem:"default=0"`
	Views      int64                    `db:"views" golem:"default=0"`
	Momentum   float32                  `db:"momentum" golem:"default=0"`
	Rating     float64                  `db:"rating" golem:"default=0"`
	Budget     golem.Decimal            `db:"budget" golem:"default=0.00"`
	LiveDate   golem.Date               `db:"live_date"`
	LiveTime   golem.Time               `db:"live_time"`
	Metadata   golem.JSON[PostMetadata] `db:"metadata" golem:"type=json"`
	Visibility Visibility               `db:"visibility" golem:"default=PUBLIC"`
	Topics     golem.List[string]       `db:"topics"`
	CreatedAt  time.Time                `db:"created_at" golem:"default=now;readonly"`
	UpdatedAt  time.Time                `db:"updated_at" golem:"updated;readonly"`
	Author     *User                    `db:"-" golem:"relation=belongs_to;fields=author_id;references=id;immutable"`
	Comments   []Comment                `db:"-" golem:"relation=has_many;fields=id;references=post_id"`
	PostTags   []PostTag                `db:"-" golem:"relation=has_many;fields=id;references=post_id"`
}

func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel(
		golem.ScopedReads[Post](),
		golem.Analytics[Post](
			golem.AnalyticsDimensions(Posts.AuthorID, Posts.Published, Posts.Visibility),
			golem.AnalyticsMeasures(Posts.ID, Posts.Views, Posts.Rating, Posts.Budget),
			golem.AnalyticsRelationDimensions(
				golem.NamedRelationDimension("authorHandle", golem.Via(Posts.Author, golem.DimensionField(Users.Handle))),
			),
			golem.AnalyticsLimits[Post](100, 10_000),
		),
		golem.GraphQL[Post](
			golem.GraphQLOperations(
				golem.GraphQLFindOne, golem.GraphQLFindMany,
				golem.GraphQLCreate, golem.GraphQLUpdate, golem.GraphQLUpsert,
				golem.GraphQLDelete, golem.GraphQLUpdateMany, golem.GraphQLDeleteMany,
				golem.GraphQLAggregate, golem.GraphQLGroupBy, golem.GraphQLRelationGroupBy,
			),
			golem.GraphQLHookOwned(Posts.AuthorID),
		),
		golem.Subscriptions[Post](),
		golem.RelationOptions(Posts.Author).OnDelete(golem.Cascade),
	)
}

type Comment struct {
	_ struct{} `golem:"model;id=example.social.Comment;table=comments;graphql=Comment"`
	_ struct{} `golem:"index=idx_comments_post_parent_created(post_id,parent_id,created_at,id)"`

	ID        golem.UUID             `db:"id" golem:"id=example.social.Comment.ID;pk;default=uuid"`
	PostID    golem.UUID             `db:"post_id"`
	AuthorID  golem.UUID             `db:"author_id"`
	ParentID  golem.Null[golem.UUID] `db:"parent_id"`
	Body      string                 `db:"body"`
	CreatedAt time.Time              `db:"created_at" golem:"default=now;readonly"`
	Post      *Post                  `db:"-" golem:"relation=belongs_to;fields=post_id;references=id"`
	Author    *User                  `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
	Parent    *Comment               `db:"-" golem:"relation=belongs_to;name=CommentTree;fields=parent_id;references=id"`
	Replies   []Comment              `db:"-" golem:"relation=has_many;name=CommentTree;fields=id;references=parent_id"`
}

func (Comment) GolemModel() golem.ModelSpec[Comment] {
	return golem.DefineModel(
		golem.Subscriptions[Comment](),
		golem.RelationOptions(Comments.Post).OnDelete(golem.Cascade),
		golem.RelationOptions(Comments.Author).OnDelete(golem.Cascade),
		golem.RelationOptions(Comments.Parent).OnDelete(golem.Cascade),
	)
}

type Tag struct {
	_ struct{} `golem:"model;id=example.social.Tag;table=tags;graphql=Tag"`
	_ struct{} `golem:"unique=uq_tags_name(name)"`

	ID       golem.UUID `db:"id" golem:"id=example.social.Tag.ID;pk;default=uuid"`
	Name     string     `db:"name" golem:"type=varchar(64)"`
	PostTags []PostTag  `db:"-" golem:"relation=has_many;fields=name;references=tag_name"`
}

type PostTag struct {
	_ struct{} `golem:"model;id=example.social.PostTag;table=post_tags;graphql=PostTag"`
	_ struct{} `golem:"primary=pk_post_tags(post_id,tag_name)"`
	_ struct{} `golem:"index=idx_post_tags_tag_post(tag_name,post_id)"`

	PostID  golem.UUID `db:"post_id"`
	TagName string     `db:"tag_name" golem:"type=varchar(64)"`
	Post    *Post      `db:"-" golem:"relation=belongs_to;fields=post_id;references=id"`
	Tag     *Tag       `db:"-" golem:"relation=belongs_to;fields=tag_name;references=name"`
}

func (PostTag) GolemModel() golem.ModelSpec[PostTag] {
	return golem.DefineModel(
		golem.RelationOptions(PostTags.Post).OnDelete(golem.Cascade),
		golem.RelationOptions(PostTags.Tag).OnDelete(golem.Cascade),
	)
}
