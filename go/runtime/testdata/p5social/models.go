package p5social

import golem "github.com/eleven-am/golem/go/golem"

type User struct {
	_ struct{} `golem:"model;id=p5social.User;table=users;graphql=User"`

	ID              golem.UUID   `db:"id" golem:"id=p5social.User.ID;pk"`
	Name            string       `db:"name" golem:"type=varchar(120)"`
	Posts           []Post       `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
	Comments        []Comment    `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
	FriendshipsFrom []Friendship `db:"-" golem:"relation=has_many;name=Origin;fields=id;references=user_id"`
	FriendshipsTo   []Friendship `db:"-" golem:"relation=has_many;name=Destination;fields=id;references=friend_id"`
}

type Post struct {
	_ struct{} `golem:"model;id=p5social.Post;table=posts;graphql=Post"`
	_ struct{} `golem:"index=idx_posts_author_title(author_id,title)"`

	ID       golem.UUID `db:"id" golem:"id=p5social.Post.ID;pk"`
	AuthorID golem.UUID `db:"author_id"`
	Title    string     `db:"title" golem:"type=varchar(160)"`
	Author   *User      `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
	Comments []Comment  `db:"-" golem:"relation=has_many;fields=id;references=post_id"`
	PostTags []PostTag  `db:"-" golem:"relation=has_many;fields=id;references=post_id"`
}

func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel(
		golem.ScopedReads[Post](),
		golem.Analytics[Post](
			golem.AnalyticsRelationDimensions(
				golem.NamedRelationDimension("authorName", golem.Via(Posts.Author, golem.DimensionField(Users.Name))),
			),
			golem.AnalyticsLimits[Post](100, 10_000),
		),
		golem.GraphQL[Post](
			golem.GraphQLOperations(
				golem.GraphQLFindOne,
				golem.GraphQLFindMany,
				golem.GraphQLCreate,
				golem.GraphQLUpdate,
				golem.GraphQLUpsert,
				golem.GraphQLDelete,
				golem.GraphQLUpdateMany,
				golem.GraphQLDeleteMany,
				golem.GraphQLAggregate,
				golem.GraphQLGroupBy,
				golem.GraphQLRelationGroupBy,
			),
		),
	)
}

type Comment struct {
	_ struct{} `golem:"model;id=p5social.Comment;table=comments;graphql=Comment"`
	_ struct{} `golem:"index=idx_comments_post_parent(post_id,parent_id)"`

	ID       golem.UUID             `db:"id" golem:"id=p5social.Comment.ID;pk"`
	PostID   golem.UUID             `db:"post_id"`
	AuthorID golem.UUID             `db:"author_id"`
	ParentID golem.Null[golem.UUID] `db:"parent_id"`
	Body     string                 `db:"body" golem:"type=varchar(240)"`
	Post     *Post                  `db:"-" golem:"relation=belongs_to;fields=post_id;references=id"`
	Author   *User                  `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
	ReplyTo  *Comment               `db:"-" golem:"relation=belongs_to;name=ReplyTree;fields=parent_id;references=id"`
	Replies  []Comment              `db:"-" golem:"relation=has_many;name=ReplyTree;fields=id;references=parent_id"`
}

type Friendship struct {
	_ struct{} `golem:"model;id=p5social.Friendship;table=friendships;graphql=Friendship"`
	_ struct{} `golem:"primary=pk_friendships(user_id,friend_id)"`

	UserID   golem.UUID `db:"user_id"`
	FriendID golem.UUID `db:"friend_id"`
	User     *User      `db:"-" golem:"relation=belongs_to;name=Origin;fields=user_id;references=id"`
	Friend   *User      `db:"-" golem:"relation=belongs_to;name=Destination;fields=friend_id;references=id"`
}

type Tag struct {
	_ struct{} `golem:"model;id=p5social.Tag;table=tags;graphql=Tag"`
	_ struct{} `golem:"unique=uq_tags_name(name)"`

	ID       golem.UUID `db:"id" golem:"id=p5social.Tag.ID;pk"`
	Name     string     `db:"name" golem:"type=varchar(64)"`
	PostTags []PostTag  `db:"-" golem:"relation=has_many;fields=name;references=tag_name"`
}

type PostTag struct {
	_ struct{} `golem:"model;id=p5social.PostTag;table=post_tags;graphql=PostTag"`
	_ struct{} `golem:"primary=pk_post_tags(post_id,tag_name)"`

	PostID  golem.UUID `db:"post_id"`
	TagName string     `db:"tag_name" golem:"type=varchar(64)"`
	Post    *Post      `db:"-" golem:"relation=belongs_to;fields=post_id;references=id"`
	Tag     *Tag       `db:"-" golem:"relation=belongs_to;fields=tag_name;references=name"`
}

func (PostTag) GolemModel() golem.ModelSpec[PostTag] {
	return golem.DefineModel(
		golem.Analytics[PostTag](golem.AnalyticsLimits[PostTag](100, 10_000)),
	)
}
