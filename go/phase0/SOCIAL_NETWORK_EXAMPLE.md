# Social-network application authoring example

This is the intended consumer experience, not compiling Phase 0 code. Names such
as `generated.App`, `golem.Config`, and generated input helpers are an API sketch;
the semantic behavior and division between handwritten/generated code are fixed
by `P0_DECISIONS.md`.

The application has users, friendships, posts, recursive comments, tags, and a
many-to-many post/tag relation.

## What the application author writes

### `models.go`

```go
package social

import "time"

type Visibility string

const (
    VisibilityPublic  Visibility = "PUBLIC"
    VisibilityFriends Visibility = "FRIENDS"
    VisibilityPrivate Visibility = "PRIVATE"
)

type FriendshipStatus string

const (
    FriendshipPending  FriendshipStatus = "PENDING"
    FriendshipAccepted FriendshipStatus = "ACCEPTED"
)

type User struct {
    _ struct{} `golem:"model;table=users"`

    ID        string    `db:"id" golem:"pk;default=uuid"`
    Handle    string    `db:"handle" golem:"unique"`
    Email     string    `db:"email"`
    Name      string    `db:"name"`
    CreatedAt time.Time `db:"created_at" golem:"default=now;readonly"`

    Posts                []Post       `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
    Comments             []Comment    `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
    RequestedFriendships []Friendship `db:"-" golem:"relation=has_many;fields=id;references=requester_id"`
    ReceivedFriendships  []Friendship `db:"-" golem:"relation=has_many;fields=id;references=addressee_id"`
}

type Friendship struct {
    _ struct{} `golem:"model;table=friendships"`
    _ struct{} `golem:"unique=uq_friendship_pair(requester_id,addressee_id)"`

    ID           string           `db:"id" golem:"pk;default=uuid"`
    RequesterID  string           `db:"requester_id"`
    AddresseeID  string           `db:"addressee_id"`
    Status       FriendshipStatus `db:"status" golem:"default=PENDING"`
    CreatedAt    time.Time        `db:"created_at" golem:"default=now;readonly"`

    Requester *User `db:"-" golem:"relation=belongs_to;fields=requester_id;references=id"`
    Addressee *User `db:"-" golem:"relation=belongs_to;fields=addressee_id;references=id"`
}

type Post struct {
    _ struct{} `golem:"model;table=posts"`
    _ struct{} `golem:"index=idx_posts_author_created(author_id,created_at)"`

    ID         string     `db:"id" golem:"pk;default=uuid"`
    AuthorID   string     `db:"author_id" golem:"readonly"`
    Title      string     `db:"title"`
    Body       string     `db:"body"`
    Visibility Visibility `db:"visibility" golem:"default=PUBLIC"`
    CreatedAt  time.Time  `db:"created_at" golem:"default=now;readonly"`
    UpdatedAt  time.Time  `db:"updated_at" golem:"default=now;readonly;updated"`

    Author   *User     `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
    Comments []Comment `db:"-" golem:"relation=has_many;fields=id;references=post_id"`
    Tags     []Tag     `db:"-" golem:"relation=many_to_many;through=post_tags;source=post_id;target=tag_id"`
}

type Comment struct {
    _ struct{} `golem:"model;table=comments"`
    _ struct{} `golem:"index=idx_comments_post_created(post_id,created_at)"`

    ID        string    `db:"id" golem:"pk;default=uuid"`
    PostID    string    `db:"post_id"`
    AuthorID  string    `db:"author_id" golem:"readonly"`
    ParentID  *string   `db:"parent_id"`
    Body      string    `db:"body"`
    CreatedAt time.Time `db:"created_at" golem:"default=now;readonly"`

    Post    *Post     `db:"-" golem:"relation=belongs_to;fields=post_id;references=id"`
    Author  *User     `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
    Parent  *Comment  `db:"-" golem:"relation=belongs_to;fields=parent_id;references=id"`
    Replies []Comment `db:"-" golem:"relation=has_many;fields=id;references=parent_id"`
}

type Tag struct {
    _ struct{} `golem:"model;table=tags"`

    ID   string `db:"id" golem:"pk;default=uuid"`
    Name string `db:"name" golem:"unique"`

    Posts []Post `db:"-" golem:"relation=many_to_many;through=post_tags;source=tag_id;target=post_id"`
}
```

The `PostTag` join table is generated from the many-to-many relation in the final
product. If the first migration implementation requires explicit joins, the same
logical relation can temporarily be represented by an explicit `PostTag` model;
the public GraphQL contract remains `Post.tags` and `Tag.posts`.

Golem emits typed descriptors such as `Users.ID` and `Posts.AuthorID` into this
same `social` package. Policy methods can therefore live on the model receivers
without importing a generated package that imports `social` back.

### `policies.go`

```go
package social

import "github.com/eleven-am/golem-go/golem"

type Actor struct {
    ID    string
    Admin bool
}

func acceptedFriendOf(actor Actor) golem.Predicate[User] {
    requested := Users.RequestedFriendships.Some(
        Friendships.AddresseeID.Eq(actor.ID).
            And(Friendships.Status.Eq(FriendshipAccepted)),
    )
    received := Users.ReceivedFriendships.Some(
        Friendships.RequesterID.Eq(actor.ID).
            And(Friendships.Status.Eq(FriendshipAccepted)),
    )
    return requested.Or(received)
}

func readablePost(actor Actor) golem.Predicate[Post] {
    own := Posts.AuthorID.Eq(actor.ID)
    public := Posts.Visibility.Eq(VisibilityPublic)
    friends := Posts.Visibility.Eq(VisibilityFriends).
        And(Posts.Author.Is(acceptedFriendOf(actor)))
    return public.Or(own, friends)
}

func (User) DefinePolicy(r *golem.Rules[User], actor Actor) {
    self := Users.ID.Eq(actor.ID)

    // Profiles are visible, but email is private except on your own row.
    r.CanRead(golem.All[User]())
    r.CannotReadFields(golem.All[User](), Users.Email)
    r.CanReadFields(self, Users.Email)

    r.CanUpdateFields(self, Users.Handle, Users.Email, Users.Name)
}

func (Friendship) DefinePolicy(r *golem.Rules[Friendship], actor Actor) {
    participant := Friendships.RequesterID.Eq(actor.ID).
        Or(Friendships.AddresseeID.Eq(actor.ID))

    r.CanRead(participant)
    r.CanCreate(
        Friendships.RequesterID.Eq(actor.ID).
            And(Friendships.Status.Eq(FriendshipPending)),
    )
    r.CanUpdateFields(
        Friendships.AddresseeID.Eq(actor.ID),
        Friendships.Status,
    )
    r.CanDelete(participant)
}

func (Post) DefinePolicy(r *golem.Rules[Post], actor Actor) {
    if actor.Admin {
        r.CanRead(golem.All[Post]())
        r.CanCreate(golem.All[Post]())
        r.CanDelete(golem.All[Post]())
        r.CanUpdateFields(
            golem.All[Post](),
            Posts.Title,
            Posts.Body,
            Posts.Visibility,
        )
        return
    }

    own := Posts.AuthorID.Eq(actor.ID)
    r.CanRead(readablePost(actor))
    r.CanCreate(own)
    r.CanDelete(own)
    r.CanUpdateFields(own, Posts.Title, Posts.Body, Posts.Visibility)
}

func (Comment) DefinePolicy(r *golem.Rules[Comment], actor Actor) {
    own := Comments.AuthorID.Eq(actor.ID)
    postIsReadable := Comments.Post.Is(readablePost(actor))
    postOwnedByActor := Comments.Post.Is(Posts.AuthorID.Eq(actor.ID))

    r.CanRead(postIsReadable)
    r.CanCreate(own.And(postIsReadable))
    r.CanUpdateFields(own, Comments.Body)
    r.CanDelete(own.Or(postOwnedByActor))
}

func (Tag) DefinePolicy(r *golem.Rules[Tag], actor Actor) {
    r.CanRead(golem.All[Tag]())
    if actor.Admin {
        r.CanCreate(golem.All[Tag]())
        r.CanUpdateFields(golem.All[Tag](), Tags.Name)
        r.CanDelete(golem.All[Tag]())
    }
}
```

There is no loaded `Post` passed to `CanRead`. These predicates can scope SQL
before pagination and can also verify a persisted create/update result. Each
policy is attached to its model receiver; the model compiler discovers it and
generates the binding. Shared domain conditions such as `readablePost` remain
ordinary helper functions.

### `hooks.go`

```go
package social

import (
    "context"

    "github.com/eleven-am/golem-go/golem"
)

func (Post) BeforeCreate(
    ctx context.Context,
    request *Posts.CreateRequest,
) error {
    actor := golem.Principal[Actor](ctx)

    // AuthorID is read-only in public inputs, so callers cannot forge it.
    request.Data.AuthorID = actor.ID
    return nil
}

func (Comment) BeforeCreate(
    ctx context.Context,
    request *Comments.CreateRequest,
) error {
    actor := golem.Principal[Actor](ctx)
    request.Data.AuthorID = actor.ID
    return nil
}

func (Comment) AfterCommitCreate(
    ctx context.Context,
    comment Comment,
) error {
    // Email, queue dispatch, and webhooks belong after commit. A retryable
    // transaction hook does not perform irreversible external work.
    return nil
}
```

### `main.go`

```go
package main

import (
    "log"
    "net/http"

    "github.com/jmoiron/sqlx"
    "github.com/eleven-am/golem-go/golem"
    "myapp/internal/generated/socialapp"
    "myapp/internal/social"
)

func main() {
    db := sqlx.MustConnect("pgx", mustDatabaseURL())

    app, err := socialapp.New(golem.Config[social.Actor]{
        DB:       db,
        Provider: golem.PostgreSQL,
        ResolvePrincipal: sessions.ResolveActor,
        Models: socialapp.ModelConfig{
            Post:    golem.Expose().Subscriptions().MaxTake(100),
            Comment: golem.Expose().Subscriptions().MaxTake(200),
            Tag:     golem.Expose(),
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    http.Handle("/graphql", app.GraphQL())
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

## What programmatic application code looks like

Caller-authorized code uses the generated client:

```go
caller, err := app.ForPrincipal(actor)
if err != nil {
    return err
}

feed, err := caller.Posts.FindMany(ctx,
    Posts.Where(Posts.Visibility.In(VisibilityPublic, VisibilityFriends)),
    Posts.OrderBy(Posts.CreatedAt.Desc()),
    Posts.Take(25),
    Posts.Select(
        Posts.ID,
        Posts.Title,
        Posts.CreatedAt,
        Posts.Author.Select(Users.ID, Users.Handle),
        Posts.Tags.Select(Tags.ID, Tags.Name),
    ),
)
```

The caller filter asks for public/friends posts, but it cannot widen
`readablePost(actor)`. Golem intersects both predicates in SQL before `take 25`.

A policy-enforced transaction stays policy-enforced:

```go
err = caller.Transaction(ctx, func(tx *socialapp.CallerTx) error {
    post, err := tx.Posts.Create(ctx, Posts.CreateInput{
        Title:      "Hello",
        Body:       "My first post",
        Visibility: VisibilityFriends,
    })
    if err != nil {
        return err
    }

    _, err = tx.Comments.Create(ctx, Comments.CreateInput{
        PostID: post.ID,
        Body:   "First comment",
    })
    return err
})
```

Trusted maintenance is deliberately explicit:

```go
_, err := app.System().Tags.Upsert(ctx, Tags.UpsertInput{
    Where: Tags.Name.Eq("golang"),
    Create: Tags.CreateInput{Name: "golang"},
    Update: Tags.UpdateInput{},
})
```

## What Golem generates automatically

The author does not write resolver/service/repository/DTO layers. From the models
and registration above, Golem generates:

- SQLite/PostgreSQL migrations and schema fingerprint;
- typed model, field, relation, filter, selection, and mutation descriptors;
- system and caller clients;
- authorized read/mutation execution;
- GraphQL model/output/filter/order/unique/create/update types;
- CRUD queries and mutations;
- compound identity support;
- recursive comment selections;
- field nullability needed for conditional email masking;
- per-row committed events and opted-in subscriptions; and
- stable error mapping.

The resulting GraphQL can be used immediately:

```graphql
mutation CreatePost {
  createPost(data: {
    title: "Hello"
    body: "My first post"
    visibility: FRIENDS
    tags: { connect: [{ name: "golang" }] }
  }) {
    id
    title
    author { id handle }
    tags { id name }
  }
}

mutation ReplyToComment($parentId: ID!, $postId: ID!) {
  createComment(data: {
    post: { connect: { id: $postId } }
    parent: { connect: { id: $parentId } }
    body: "A nested reply"
  }) {
    id
    body
    parent { id }
  }
}

query Feed {
  posts(orderBy: [{ createdAt: desc }], take: 25) {
    id
    title
    body
    visibility
    author { id handle }
    tags { id name }
    comments {
      id
      body
      author { id handle }
      replies {
        id
        body
        author { id handle }
      }
    }
  }
}

subscription PostChanges {
  postEvents {
    type
    id
    entity {
      id
      title
      visibility
      author { id handle }
    }
  }
}
```

For a non-owner, a private post is absent rather than returned and rejected after
pagination. A friends-only post is returned only when the persisted friendship is
accepted. `User.email` is nullable in GraphQL and becomes `null` outside the
user's own row. Creating a post/comment cannot forge `authorId`; the hook injects
it and create policy verifies the persisted row. Recursive comments require no
custom resolver.
