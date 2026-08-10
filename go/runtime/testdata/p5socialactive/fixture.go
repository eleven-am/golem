package p5socialactive

import (
	"context"
	"sync"

	"github.com/eleven-am/golem/go/golem"
)

type Principal struct {
	DenyPostCreate bool
	DenyPostUpdate bool
}

type Actor = Principal

type User struct {
	_               struct{}     `golem:"model;id=p5active.User;table=users"`
	ID              golem.UUID   `db:"id" golem:"pk"`
	Name            string       `db:"name"`
	Posts           []Post       `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
	Comments        []Comment    `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
	FriendshipsFrom []Friendship `db:"-" golem:"relation=has_many;name=Origin;fields=id;references=user_id"`
	FriendshipsTo   []Friendship `db:"-" golem:"relation=has_many;name=Destination;fields=id;references=friend_id"`
}

type Post struct {
	_        struct{}          `golem:"model;id=p5active.Post;table=posts"`
	ID       golem.UUID        `db:"id" golem:"pk"`
	AuthorID golem.UUID        `db:"author_id"`
	Title    string            `db:"title"`
	Counter  int32             `db:"counter" golem:"default=7"`
	Optional golem.Null[int32] `db:"optional" golem:"default=11"`
	Author   *User             `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
	Comments []Comment         `db:"-" golem:"relation=has_many;fields=id;references=post_id"`
	PostTags []PostTag         `db:"-" golem:"relation=has_many;fields=id;references=post_id"`
}

type Comment struct {
	_        struct{}               `golem:"model;id=p5active.Comment;table=comments"`
	ID       golem.UUID             `db:"id" golem:"pk"`
	PostID   golem.UUID             `db:"post_id"`
	AuthorID golem.UUID             `db:"author_id"`
	ParentID golem.Null[golem.UUID] `db:"parent_id"`
	Body     string                 `db:"body"`
	Post     *Post                  `db:"-" golem:"relation=belongs_to;fields=post_id;references=id"`
	Author   *User                  `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
	ReplyTo  *Comment               `db:"-" golem:"relation=belongs_to;name=ReplyTree;fields=parent_id;references=id"`
	Replies  []Comment              `db:"-" golem:"relation=has_many;name=ReplyTree;fields=id;references=parent_id"`
}

type Friendship struct {
	_        struct{}   `golem:"model;id=p5active.Friendship;table=friendships"`
	_        struct{}   `golem:"primary=pk_friendships(user_id,friend_id)"`
	UserID   golem.UUID `db:"user_id"`
	FriendID golem.UUID `db:"friend_id"`
	User     *User      `db:"-" golem:"relation=belongs_to;name=Origin;fields=user_id;references=id"`
	Friend   *User      `db:"-" golem:"relation=belongs_to;name=Destination;fields=friend_id;references=id"`
}

type Tag struct {
	_        struct{}   `golem:"model;id=p5active.Tag;table=tags"`
	_        struct{}   `golem:"unique=uq_tags_name(name)"`
	ID       golem.UUID `db:"id" golem:"pk"`
	Name     string     `db:"name"`
	PostTags []PostTag  `db:"-" golem:"relation=has_many;fields=name;references=tag_name"`
}

type PostTag struct {
	_       struct{}   `golem:"model;id=p5active.PostTag;table=post_tags"`
	_       struct{}   `golem:"primary=pk_post_tags(post_id,tag_name)"`
	PostID  golem.UUID `db:"post_id"`
	TagName string     `db:"tag_name"`
	Post    *Post      `db:"-" golem:"relation=belongs_to;fields=post_id;references=id"`
	Tag     *Tag       `db:"-" golem:"relation=belongs_to;fields=tag_name;references=name"`
}

func allowAll[M any](rules *golem.Rules[M]) {
	rules.CanRead(golem.All[M]())
	rules.CanCreate(golem.All[M]())
	rules.CanUpdate(golem.All[M]())
	rules.CanDelete(golem.All[M]())
}

func (User) DefinePolicy(rules *golem.Rules[User], _ Actor)             { allowAll(rules) }
func (Comment) DefinePolicy(rules *golem.Rules[Comment], _ Actor)       { allowAll(rules) }
func (Friendship) DefinePolicy(rules *golem.Rules[Friendship], _ Actor) { allowAll(rules) }
func (Tag) DefinePolicy(rules *golem.Rules[Tag], _ Actor)               { allowAll(rules) }
func (PostTag) DefinePolicy(rules *golem.Rules[PostTag], _ Actor)       { allowAll(rules) }
func (Post) DefinePolicy(rules *golem.Rules[Post], actor Actor) {
	rules.CanRead(golem.All[Post]())
	if !actor.DenyPostCreate {
		rules.CanCreate(golem.All[Post]())
	}
	if !actor.DenyPostUpdate {
		rules.CanUpdate(golem.All[Post]())
	}
	rules.CanDelete(golem.All[Post]())
}

type HookSnapshot struct{ BeforeCreate, AfterCreate, AfterCommitCreate, BeforeUpdate, AfterUpdate, AfterCommitUpdate int }

var hookProbe struct {
	sync.Mutex
	value HookSnapshot
}

func ResetHooks()                 { hookProbe.Lock(); hookProbe.value = HookSnapshot{}; hookProbe.Unlock() }
func SnapshotHooks() HookSnapshot { hookProbe.Lock(); defer hookProbe.Unlock(); return hookProbe.value }

func (Post) BeforeCreate(context.Context, *PostCreateHookRequest) error {
	hookProbe.Lock()
	hookProbe.value.BeforeCreate++
	hookProbe.Unlock()
	return nil
}
func (Post) AfterCreate(context.Context, PostCreateHookResult) error {
	hookProbe.Lock()
	hookProbe.value.AfterCreate++
	hookProbe.Unlock()
	return nil
}
func (Post) AfterCommitCreate(context.Context, PostCreateHookResult) error {
	hookProbe.Lock()
	hookProbe.value.AfterCommitCreate++
	hookProbe.Unlock()
	return nil
}
func (Post) BeforeUpdate(context.Context, *PostUpdateHookRequest) error {
	hookProbe.Lock()
	hookProbe.value.BeforeUpdate++
	hookProbe.Unlock()
	return nil
}
func (Post) AfterUpdate(context.Context, PostUpdateHookResult) error {
	hookProbe.Lock()
	hookProbe.value.AfterUpdate++
	hookProbe.Unlock()
	return nil
}
func (Post) AfterCommitUpdate(context.Context, PostUpdateHookResult) error {
	hookProbe.Lock()
	hookProbe.value.AfterCommitUpdate++
	hookProbe.Unlock()
	return nil
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "p5_social_active")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Model[Post](schema)
	golem.Model[Comment](schema)
	golem.Model[Friendship](schema)
	golem.Model[Tag](schema)
	golem.Model[PostTag](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
