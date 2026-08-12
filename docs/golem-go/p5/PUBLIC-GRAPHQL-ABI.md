# P5 public GraphQL ABI

Status: **controlling public ABI**

This document freezes the generated GraphQL schema patterns and the ordinary Go
server integration. Names shown for a generated social application are exact
patterns. Internals may use gqlgen, but application authors do not write CRUD
resolvers or run a second generator.

## 1. Public application integration

The generated application package exposes:

```go
type GraphQLLimits struct {
    MaxRequestBytes       int
    MaxVariableBytes      int
    MaxTokens             int
    MaxASTNodes           int
    MaxFragments          int
    MaxDepth              int
    MaxSelectedFields     int
    MaxAliases            int
    MaxInputDepth         int
    MaxInputNodes         int
    MaxListItems          int
    MaxComplexity         int
    MaxPageSize           int
    MaxResolverConcurrency int
    MaxComputedBatchSize  int
}

type GraphQLConfig[P any] struct {
    PrincipalFromContext func(context.Context) (P, bool)
    Limits               GraphQLLimits
    Introspection        bool
    ReportInternalError  func(context.Context, error)
}

func (app *App[P]) GraphQL(config GraphQLConfig[P]) (*GraphQLServer, error)

type GraphQLServer struct { /* opaque */ }

func (server *GraphQLServer) Handler() http.Handler
func (server *GraphQLServer) SDL() string
func (server *GraphQLServer) ContractFingerprint() golem.SchemaDigest
```

`PrincipalFromContext` is mandatory for the caller GraphQL server. Missing or
invalid principal resolution is `UNAUTHENTICATED` and performs no database work.
`ReportInternalError` is also mandatory so a sanitized public failure never
becomes an unobservable trusted failure. `Introspection` enables ordinary
introspection when true; disabling it changes handler behavior, not the embedded
SDL or contract fingerprint. There is no `SystemGraphQL`, DB accessor, or
executor escape hatch.

The P5 handler supports bounded GraphQL-over-HTTP query and mutation execution.
CORS, TLS, cookies/tokens, and application authentication middleware remain
application concerns. Subscription transports are added only in P7.

Zero limit fields select the following safe defaults. A configured value may
lower them. It may not exceed the portable hard maximum or the stricter P3/P4
limit:

| Limit | Zero-value default | Portable hard maximum |
| --- | ---: | ---: |
| request bytes | 1 MiB | 8 MiB |
| variable bytes | 512 KiB | 4 MiB |
| lexical tokens | 16,384 | 65,536 |
| AST nodes | 8,192 | 32,768 |
| fragments | 64 | 256 |
| GraphQL selection depth | 12 | 32 |
| selected occurrences | 256 | 2,048 |
| aliases | 128 | 1,024 |
| input depth | 16 | 32 |
| input nodes | 8,192 | 32,768 |
| items in one input list | 1,000 | 10,000 |
| calculated complexity | 10,000 | 100,000 |
| maximum page size | 500 | 1,000 |
| concurrent field resolvers | 64 | 256 |
| computed batch size | 256 | 4,096 |

The GraphQL selection-depth counter includes GraphQL object hops; P3 separately
enforces its relation-depth limit. Request bytes include variables, while the
variable limit adds a stricter sub-bound. The SDL page default is a generated
ContractIR fact—50 unless a model declaration overrides it—and must be positive
and no greater than the model's generated maximum. The runtime `MaxPageSize` may
only lower that generated maximum.

## 2. Model and enum authoring

GraphQL exposure belongs to the existing static model declaration. With no
GraphQL option, an exposed model enables every ordinary P5 operation for which a
valid input can be generated. An application narrows or renames the surface:

```go
func (Post) GolemModel() golem.ModelSpec[Post] {
    return golem.DefineModel(
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
            ),
            golem.GraphQLPlural("posts"),
            golem.GraphQLRoots(golem.GraphQLRootNames{
                FindOne:  "post",
                FindMany: "posts",
            }),
            golem.GraphQLPageSizes(25, 250),
        ),
    )
}
```

The statically interpreted declaration functions are:

```go
type GraphQLOperation string

const (
    GraphQLFindOne    GraphQLOperation = "findOne"
    GraphQLFindMany   GraphQLOperation = "findMany"
    GraphQLCreate     GraphQLOperation = "create"
    GraphQLUpdate     GraphQLOperation = "update"
    GraphQLUpsert     GraphQLOperation = "upsert"
    GraphQLDelete     GraphQLOperation = "delete"
    GraphQLUpdateMany GraphQLOperation = "updateMany"
    GraphQLDeleteMany GraphQLOperation = "deleteMany"
)

func GraphQL[M any](...GraphQLOption) ModelOption[M]
func GraphQLOperations(...GraphQLOperation) GraphQLOption
func GraphQLPlural(string) GraphQLOption
func GraphQLRoots(GraphQLRootNames) GraphQLOption
func GraphQLPageSizes(defaultSize, maximumSize int) GraphQLOption
func GraphQLHidden() GraphQLOption
func GraphQLHookOwned[M any](...Column[M]) GraphQLOption
```

`GraphQLHidden` makes the model absent from GraphQL but does not remove it from
ModelIR, migrations, generated Go clients, policies, relations, or P4 facts.
Relations from exposed models to a hidden model must themselves be hidden or
generation fails with the exact relation path.

`GraphQLHookOwned` is a create-input ownership boundary for values supplied by a
trusted model hook rather than by a GraphQL client. Each named scalar is omitted
from root create, upsert-create, and every recursive nested-create input. The
compiler requires the field to be writable by the programmatic create API,
non-identity, and backed by a recognized `BeforeCreate` hook. The generated
`CreateFieldCapability` remains available to typed callers and to
`golem.SetCreate` inside that hook.

A hook-owned scalar need not be a foreign key; generated slugs and tenant
metadata are valid examples. If it participates in a canonical belongs-to key,
the complete composite key must be hook-owned, non-null, and unambiguous, and
the corresponding belongs-to relation is omitted from create inputs as well.
Partial, nullable, or multiply-associated keys fail compilation. Output,
filter, order, and update exposure remain controlled by the ordinary field
modes; use `immutable` independently when the field or relation must not be
updated. This declaration changes ContractIR, the contract fingerprint, and the
GraphQL ABI only. It never changes ModelIR or creates a database migration. A
model that enables create or upsert must retain at least one client-owned create
input position; otherwise generation refuses it with
`P8_GRAPHQL_HOOK_OWNED_EMPTY_CREATE` instead of silently removing the authored
root.

Model and field `graphql=` tags remain exact overrides. Without an override,
models retain exported Go type spelling and fields use the frozen lower-camel
conversion.

Enum values may declare a GraphQL spelling independently of their persisted wire
value:

```go
func (Role) GolemEnum() golem.EnumSpec[Role] {
    return golem.DefineEnum(
        golem.EnumValue(RoleAdmin, golem.GraphQLValue("ADMIN")),
        golem.EnumValue(RoleMember, golem.GraphQLValue("MEMBER")),
    )
}
```

Changing any option in this section changes only ContractIR and GraphQL
artifacts. It never produces a database migration.

## 3. Conventional schema

For model `Post`, with all P5 operations enabled, the roots are:

```graphql
type Query {
  post(where: PostWhereUniqueInput!): Post
  posts(
    where: PostWhereInput
    orderBy: [PostOrderByInput!]
    cursor: PostWhereUniqueInput
    distinct: [PostScalarField!]
    skip: Int
    take: Int = 50
  ): [Post!]!
}

type Mutation {
  createPost(data: PostCreateInput!): Post!
  updatePost(where: PostWhereUniqueInput!, data: PostUpdateInput!): Post!
  upsertPost(
    where: PostWhereUniqueInput!
    create: PostCreateInput!
    update: PostUpdateInput!
  ): Post!
  deletePost(where: PostWhereUniqueInput!): Post!
  updateManyPosts(where: PostWhereInput!, data: PostUpdateManyInput!): BatchPayload!
  deleteManyPosts(where: PostWhereInput!): BatchPayload!
}

type BatchPayload {
  count: Int!
}
```

`where` is non-null on batch mutations. Mutating every reachable row requires an
explicit `where: { all: true }`; an empty object is not an all-rows request.

Disabled operations are absent from SDL. P5 emits no `Subscription`, aggregate,
group-by, standalone count, or find-first root.

## 4. Output types and relation arguments

Illustrative output:

```graphql
type Post {
  id: UUID
  title: String
  body: String
  author: User
  comments(
    where: CommentWhereInput
    orderBy: [CommentOrderByInput!]
    cursor: CommentWhereUniqueInput
    distinct: [CommentScalarField!]
    skip: Int
    take: Int = 50
  ): [Comment!]
  _count: PostCountOutput
}

type PostCountOutput {
  comments(where: CommentWhereInput): Int
}
```

The nullable fields above are intentional for a policy-bound model. When P3 can
return `ReadNull` for an occurrence, the SDL occurrence is nullable. A present
to-many relation is a non-null list of non-null authorized rows. A model proven
at generation to have no mask path may retain database-derived non-null output
types. Inputs do not inherit output masking nullability.

Aliases with different arguments are independent:

```graphql
query {
  post(where: { id: "018f..." }) {
    newest: comments(orderBy: [{ createdAt: desc }], take: 5) { id body }
    oldest: comments(orderBy: [{ createdAt: asc }], take: 5) { id body }
  }
}
```

Both lists are compiled into one occurrence-aware P3 operation tree; neither
overwrites the other.

## 5. Exposure matrix

| Mode | Output | Filter/order/selector | Create | Update |
| --- | ---: | ---: | ---: | ---: |
| normal | yes | yes | yes | yes |
| immutable | yes | yes | yes | no |
| read-only | yes | yes | no | no |
| write-only | no | no | yes | yes |
| write-only + immutable | no | no | yes | no |
| hidden | no | no | no | no |

The matrix applies recursively. Hidden/write-only primary-key components,
write-only relations, invalid mode overlaps, and empty required inputs are
generation errors.

## 6. Conditions, order, cursor, and distinct

Every model emits a typed where tree:

```graphql
input PostWhereInput {
  all: Boolean
  AND: [PostWhereInput!]
  OR: [PostWhereInput!]
  NOT: [PostWhereInput!]
  id: UUIDFilter
  title: StringFilter
  tags: StringListFilter
  metadata: JSONFilter
  author: UserRelationFilter
  comments: CommentListRelationFilter
}

input UserRelationFilter {
  is: UserWhereInput
  isNot: UserWhereInput
}

input CommentListRelationFilter {
  some: CommentWhereInput
  every: CommentWhereInput
  none: CommentWhereInput
}
```

Only operators accepted for the logical type and both configured providers are
emitted. Unknown fields/operators, an ambiguous empty object, contradictory
`all` plus other members, and over-deep trees are `BAD_USER_INPUT` before engine
work.

Ordering is a list because order is semantic. Every entry must contain exactly
one field:

```graphql
enum SortOrder { asc desc }

input PostOrderByInput {
  createdAt: SortOrder
  id: SortOrder
}
```

Unique selectors contain exactly one selector arm. Compound components are all
non-null and preserve key declaration order:

```graphql
input UserWhereUniqueInput @oneOf {
  id: UUID
  tenantHandle: UserTenantHandleCompoundUniqueInput
}

input UserTenantHandleCompoundUniqueInput {
  tenantID: UUID!
  handle: String!
}
```

The binder enforces one-of semantics even if a client/executor does not enforce
the directive.

## 7. Create, update, and update-many inputs

Create fields use direct values and model/default requiredness:

```graphql
input PostCreateInput {
  title: String!
  body: String
  author: PostAuthorCreateRelationInput!
  comments: PostCommentsCreateRelationInput
}
```

Update fields use exactly-one operation envelopes:

```graphql
input PostUpdateInput {
  title: StringUpdateOperationsInput
  body: NullableStringUpdateOperationsInput
  views: BigIntUpdateOperationsInput
  author: PostAuthorUpdateRelationInput
  comments: PostCommentsUpdateRelationInput
}

input StringUpdateOperationsInput @oneOf {
  set: String
}

input NullableStringUpdateOperationsInput @oneOf {
  set: String
  setNull: Boolean
}

input BigIntUpdateOperationsInput @oneOf {
  set: BigInt
  increment: BigInt
  decrement: BigInt
}
```

For nullable create fields, omission and explicit GraphQL null remain distinct.
Explicit null uses the planned P4 create-null value, which bypasses a database
default and persists SQL null. For nullable update fields, `setNull: true` is the
P4 null operation; `setNull: false` is invalid rather than a no-op. Omitting the
field performs no operation. Non-null fields have no `setNull`. An update input
must contain at least one actual operation after coercion.

`PostUpdateManyInput` contains only scalar/enum/JSON/bytes/list operations legal
for P4 update-many. It contains no relations.

## 8. Nested relation inputs

The complete generated vocabulary is:

```graphql
input PostCommentsCreateRelationInput {
  create: [CommentCreateWithoutPostInput!]
  createMany: [CommentCreateWithoutPostInput!]
  connect: [CommentWhereUniqueInput!]
  connectOrCreate: [CommentConnectOrCreateWithoutPostInput!]
}

input PostCommentsUpdateRelationInput {
  create: [CommentCreateWithoutPostInput!]
  createMany: [CommentCreateWithoutPostInput!]
  connect: [CommentWhereUniqueInput!]
  connectOrCreate: [CommentConnectOrCreateWithoutPostInput!]
  disconnect: [CommentWhereUniqueInput!]
  set: [CommentWhereUniqueInput!]
  update: [CommentUpdateWithWhereWithoutPostInput!]
  updateMany: [CommentUpdateManyWithWhereWithoutPostInput!]
  upsert: [CommentUpsertWithWhereWithoutPostInput!]
  delete: [CommentWhereUniqueInput!]
  deleteMany: [CommentWhereInput!]
}
```

To-one envelopes use singular members and omit illegal operations. A required
to-one has no disconnect. `WithoutX` inputs remove the back relation to prevent
unbounded immediate recursion and ambiguous ownership. Operations are slices
where order is observable. Duplicate/conflicting operations are rejected by the
P4 binder before partial execution.

## 9. Scalars and enums

The public scalars are:

```graphql
scalar BigInt
scalar Decimal
scalar UUID
scalar Date
scalar Time
scalar DateTime
scalar Bytes
scalar JSON
```

BigInt, Decimal, UUID, Date, Time, DateTime, and Bytes serialize as strings.
Decimal never accepts a JSON number. BigInt variables use exact decimal strings;
an inline GraphQL INT literal is also exact. JSON decoding retains `json.Number`
and applies configured byte/node/depth limits. Float refuses NaN and infinity.

Enum GraphQL names are explicit ContractIR facts and map to P1 wire values.
Changing only an enum GraphQL value changes the contract fingerprint, not the
database schema.

## 10. Computed fields

Computed fields attach to their model through a statically interpreted method:

```go
type GreetingArgs struct {
    Prefix string `golem:"graphql=prefix"`
}

func (User) DefineGraphQL(g *golem.GraphQLModel[User]) {
    golem.ComputedField(
        g,
        "greeting",
        golem.GraphQLString().NonNull(),
        User{}.Greeting,
        golem.Requires(Users.Name),
    )
}

func (User) Greeting(
    ctx context.Context,
    row golem.Row[User],
    args GreetingArgs,
) (string, error) {
    name, ok := golem.Value(row, Users.Name).Get()
    if !ok {
        return "", golem.MaskedDependency("User.greeting", "name")
    }
    return args.Prefix + " " + name, nil
}
```

The compiler validates the resolver signature, output type, argument fields,
dependencies, and collisions. `row` contains only selected/masked public values.

A batched declaration additionally supplies a typed key field, loader, cache-key
codec, and maximum batch size. The generated loader calls the function once with
`[]K` plus typed arguments and expects an exact key-to-result map. It is scoped to
one GraphQL execution and cleared by P4 writes.

## 11. Custom operations

The schema-root application package statically declares custom roots:

```go
type SearchUsersArgs struct {
    Where golem.Predicate[User] `golem:"graphql=where"`
    Take  *int32                `golem:"graphql=take"`
}

func DefineGraphQL(g *golem.GraphQLSchema) {
    golem.Query(g, "searchUsers", SearchUsers)
}

func SearchUsers(
    ctx context.Context,
    caller *Caller[Principal],
    args SearchUsersArgs,
) ([]golem.Row[User], error) {
    options := []golem.ReadOption[User]{
        Users.Where(args.Where),
        Users.Select(Users.ID, Users.Name),
    }
    if args.Take != nil {
        options = append(options, Users.Take(int(*args.Take)))
    }
    return caller.Users.FindMany(ctx, options...)
}
```

The exact generated caller type is supplied by the bootstrap generation pass.
Recognized argument/result types map to generated GraphQL types. Custom
operations cannot request System, DB, Tx, raw SQL, arbitrary named GraphQL type
strings, or unregistered object shapes. A custom mutation explicitly calls
`caller.Transaction` when it needs multi-operation atomicity.

## 12. Error shape

Every public error has a stable code and safe message:

```json
{
  "message": "Post not found",
  "path": ["updatePost"],
  "extensions": { "code": "NOT_FOUND" }
}
```

Allowed engine codes are `BAD_USER_INPUT`, `NOT_FOUND`, `CONFLICT`,
`UNAUTHENTICATED`, and `FORBIDDEN`. Transport codes are
`GRAPHQL_PARSE_FAILED`, `GRAPHQL_VALIDATION_FAILED`, and
`INTERNAL_SERVER_ERROR`. Trusted wrapped causes are reported out of band and are
never serialized.
