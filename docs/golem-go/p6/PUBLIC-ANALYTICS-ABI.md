# P6 public analytics and scoped-read ABI

Status: **controlling planned ABI; not implemented**

This document freezes the application-facing P6 surface. Names shown for a
generated `social` model package and `socialapp` application package are exact
patterns. Representations remain opaque and constructors remain sealed. Small
internal signature changes are allowed only when the examples and compile
properties here remain true; a visible change requires updating this ABI and
its evidence before implementation continues.

## 1. Authoring analytics

Local programmatic analytics is generated for every readable stored scalar or
enum with a supported capability. A model declaration controls its public
GraphQL allowlists, named relation dimensions, limits, and scoped-root opt-in:

```go
func (Post) GolemModel() golem.ModelSpec[Post] {
    return golem.DefineModel(
        golem.Analytics[Post](
            golem.AnalyticsDimensions(
                Posts.AuthorID,
                Posts.Published,
                Posts.CreatedAt,
            ),
            golem.AnalyticsMeasures(
                Posts.Views,
                Posts.Rating,
                Posts.CreatedAt,
                Posts.Title,
            ),
            golem.AnalyticsRelationDimensions(
                golem.NamedRelationDimension(
                    "authorCountry",
                    golem.Via(
                        Posts.Author,
                        golem.DimensionField(Users.Country),
                    ),
                ),
            ),
            golem.AnalyticsLimits[Post](100, 10_000),
        ),
        golem.ScopedReads[Post](),
        golem.GraphQL[Post](
            golem.GraphQLOperations(
                golem.GraphQLFindOne,
                golem.GraphQLFindMany,
                golem.GraphQLAggregate,
                golem.GraphQLGroupBy,
                golem.GraphQLRelationGroupBy,
            ),
            golem.GraphQLRoots(golem.GraphQLRootNames{
                FindOne:        "post",
                FindMany:       "posts",
                Aggregate:      "aggregatePosts",
                GroupBy:        "groupByPosts",
                RelationGroupBy:"relationGroupByPosts",
            }),
        ),
    )
}
```

The statically interpreted declaration surface is:

```go
func Analytics[M any](options ...AnalyticsOption[M]) ModelOption[M]
func AnalyticsDimensions[M any](fields ...Column[M]) AnalyticsOption[M]
func AnalyticsMeasures[M any](fields ...Column[M]) AnalyticsOption[M]
func AnalyticsRelationDimensions[M any](
    dimensions ...RelationDimensionSpec[M],
) AnalyticsOption[M]
func NamedRelationDimension[M, V any](
    name string,
    path RelationDimensionPath[M, V],
) RelationDimensionSpec[M]
func AnalyticsLimits[M any](
    graphqlMaxGroups int,
    relationMaxIntermediateGroups int,
) AnalyticsOption[M]
func DimensionField[M, V any](field ScalarColumn[M, V]) RelationDimensionPath[M, V]
func Via[M, R, V any](
    relation ToOneRelation[M, R],
    tail RelationDimensionPath[R, V],
) RelationDimensionPath[M, V]
func ScopedReads[M any]() ModelOption[M]
```

`AnalyticsDimensions` and `AnalyticsMeasures` are GraphQL allowlists. Omission
means all GraphQL-visible fields with the applicable capability. An explicit
empty allowlist is invalid when its operation is enabled. Programmatic local
analytics remains available for non-GraphQL readable fields and is still
authorized at execution.

`NamedRelationDimension` names one public/generated terminal dimension. Names are
lower-camel GraphQL names and exported upper-camel generated Go fields. Every
path component must be a declared forward to-one relation. All relation
dimensions used in one request must share the exact relation-ID chain.

The GraphQL operation constants become:

```go
const (
    GraphQLFindOne         GraphQLOperation = "findOne"
    GraphQLFindMany        GraphQLOperation = "findMany"
    GraphQLCreate          GraphQLOperation = "create"
    GraphQLUpdate          GraphQLOperation = "update"
    GraphQLUpsert          GraphQLOperation = "upsert"
    GraphQLDelete          GraphQLOperation = "delete"
    GraphQLUpdateMany      GraphQLOperation = "updateMany"
    GraphQLDeleteMany      GraphQLOperation = "deleteMany"
    GraphQLAggregate       GraphQLOperation = "aggregate"
    GraphQLGroupBy         GraphQLOperation = "groupBy"
    GraphQLRelationGroupBy GraphQLOperation = "relationGroupBy"
)
```

Analytics operations are never added by the default P5 ordinary-operation set.
They require `Analytics` plus the exact GraphQL operation opt-in. Enabling a
relation root without a relation dimension is a compiler error.

All declarations in this section affect only ContractIR and generated
application/GraphQL artifacts. They never alter ModelIR, DDL, migrations, or the
model fingerprint.

## 2. Exact result scalar types

P6 adds immutable exact analytical numbers whose representation is private:

```go
type ExactInteger struct { /* opaque arbitrary-precision signed integer */ }

func NewExactInteger(value int64) ExactInteger
func ParseExactInteger(text string) (ExactInteger, error)
func MustParseExactInteger(text string) ExactInteger
func (value ExactInteger) String() string
func (value ExactInteger) Int64() (int64, bool)
func (value ExactInteger) Cmp(other ExactInteger) int

type ExactDecimal struct { /* opaque arbitrary-precision fixed decimal */ }

func ExactDecimalFrom(value golem.Decimal) ExactDecimal
func ParseExactDecimal(text string) (ExactDecimal, error)
func MustParseExactDecimal(text string) ExactDecimal
func (value ExactDecimal) String() string
func (value ExactDecimal) Decimal() (golem.Decimal, bool)
func (value ExactDecimal) Cmp(other ExactDecimal) int
```

`String` is canonical base ten: no exponent, no leading plus, no unnecessary
leading or trailing zeros, and no negative zero. Decimal arithmetic rounds or
sums at the declared field scale, then canonical transport may remove trailing
fractional zeros. `ExactDecimal` retains an arbitrary-precision coefficient even
when it exceeds the 18-digit storage envelope. `Decimal()` succeeds only when
the result fits the portable P1 `golem.Decimal` envelope.

These types do not implement mutable `math/big` accessors. GraphQL encodes
`ExactInteger` through `BigInt` and `ExactDecimal` through `Decimal`, both as
exact strings. Parsing and database decoding accept at most 128 significant
decimal digits and Decimal scale 18. A larger value returns the stable
analytical-overflow error; it is never rounded or truncated to fit this envelope.

## 3. Capability and result matrix

The generator gives each field only the methods in this table. Nullable and
non-null stored fields have the same analytical capabilities; SQL nullability
changes result state.

| Logical field | Dimension | Count field | Sum result | Average result | Min/max result |
| --- | ---: | ---: | --- | --- | --- |
| Bool | yes | `int64` | — | — | — |
| Int16 | yes | `int64` | `ExactInteger` | `float64` | `int16` |
| Int32 | yes | `int64` | `ExactInteger` | `float64` | `int32` |
| Int64 | yes | `int64` | `ExactInteger` | `float64` | `int64` |
| Float32/Float64 | yes | `int64` | `float64` | `float64` | field Go type |
| Decimal(p,s) | yes | `int64` | `ExactDecimal` | `ExactDecimal` | `golem.Decimal` |
| String | yes | `int64` | — | — | `string` |
| UUID | yes | `int64` | — | — | — |
| Date | yes | `int64` | — | — | `golem.Date` |
| Time | yes | `int64` | — | — | `golem.Time` |
| DateTime | yes | `int64` | — | — | `time.Time` |
| Enum | yes | `int64` | — | — | — |
| Bytes, JSON, scalar list, opaque, relation | — | — | — | — | — |

`count(*)` and field-count are never null and return zero for an empty set.
Sum, average, minimum, and maximum return `ReadNull` for an empty set or when
every contributing value is null. A numeric sum is not changed to zero.

Integer average intentionally remains `float64`, matching the TypeScript
programmatic contract. Decimal average is exact to the declared scale and uses
half-away-from-zero rounding. Float inputs and outputs must be finite.

## 4. Generated measure and dimension values

The public core types are opaque:

```go
type Dimension[M, V any] struct { /* sealed local dimension */ }
type RelationDimension[M, V any] struct { /* sealed configured path dimension */ }
type Measure[M, V any] struct { /* sealed aggregate operator and result type */ }

type LocalGroupDimension[M any] interface { /* sealed local dimension */ }
type RelationGroupDimension[M any] interface { /* sealed local or configured relation dimension */ }
type OrderedGroupDimension[M, V any] interface { /* sealed ordered-predicate capability */ }
type OrderedAnalyticsValue interface { /* closed: ordered scalar/enum/temporal values; excludes Bool and UUID */ }
type AggregateMeasure[M any] interface { /* sealed typed measure */ }
type GroupPredicate[M any] struct { /* opaque */ }
type GroupOrder[M any] struct { /* opaque */ }
```

Generated field values gain typed constructors. Illustrative exact signatures
for a `Post.Views int32`, `Post.Rating Decimal(18,4)`, `Post.Title string`, and
`Post.Published bool` are:

```go
func (golemGeneratedPostViewsField) Dimension() golem.Dimension[Post, int32]
func (golemGeneratedPostViewsField) Count() golem.Measure[Post, int64]
func (golemGeneratedPostViewsField) Sum() golem.Measure[Post, golem.ExactInteger]
func (golemGeneratedPostViewsField) Avg() golem.Measure[Post, float64]
func (golemGeneratedPostViewsField) Min() golem.Measure[Post, int32]
func (golemGeneratedPostViewsField) Max() golem.Measure[Post, int32]

func (golemGeneratedPostRatingField) Dimension() golem.Dimension[Post, golem.Decimal]
func (golemGeneratedPostRatingField) Count() golem.Measure[Post, int64]
func (golemGeneratedPostRatingField) Sum() golem.Measure[Post, golem.ExactDecimal]
func (golemGeneratedPostRatingField) Avg() golem.Measure[Post, golem.ExactDecimal]
func (golemGeneratedPostRatingField) Min() golem.Measure[Post, golem.Decimal]
func (golemGeneratedPostRatingField) Max() golem.Measure[Post, golem.Decimal]

func (golemGeneratedPostTitleField) Dimension() golem.Dimension[Post, string]
func (golemGeneratedPostTitleField) Count() golem.Measure[Post, int64]
func (golemGeneratedPostTitleField) Min() golem.Measure[Post, string]
func (golemGeneratedPostTitleField) Max() golem.Measure[Post, string]

func (golemGeneratedPostPublishedField) Dimension() golem.Dimension[Post, bool]
func (golemGeneratedPostPublishedField) Count() golem.Measure[Post, int64]
```

The model namespace supplies row count:

```go
Posts.CountAll() // golem.Measure[Post, int64]
```

Each configured relation dimension is generated on the root namespace with its
terminal Go type:

```go
Posts.AuthorCountry // golem.RelationDimension[Post, string]
```

Dimensions and measures are immutable identity-bearing values. Calling a
constructor does not perform I/O. Zero values and values from another generated
schema are rejected again when frozen.

Measures and dimensions expose closed `having`/order constructors:

```go
total.GT(golem.MustParseExactInteger("1000"))
total.GTE(...)
total.LT(...)
total.LTE(...)
total.Eq(...)
total.Ne(...)
total.IsNull()
total.IsNotNull()

dimension.Eq(value)
dimension.Ne(value)
dimension.IsNull()
dimension.IsNotNull()

golem.DimensionLT(orderedDimension, value)
golem.DimensionLTE(orderedDimension, value)
golem.DimensionGT(orderedDimension, value)
golem.DimensionGTE(orderedDimension, value)

total.Asc()
total.Desc()
dimension.Asc()
dimension.Desc()
```

The four `DimensionLT`/`LTE`/`GT`/`GTE` functions constrain `V` to
`OrderedAnalyticsValue`; this is the compile-time mechanism that makes Bool and
UUID invalid ordered predicates while keeping them valid equality dimensions.

`MustParseExactInteger` and `MustParseExactDecimal` exist for package-level
constants/tests and panic on invalid input, like other `Must...` constructors.
Application request input should use the error-returning parsers.

All groupable dimensions have canonical equality and ordering for result order.
The four ordered-predicate functions accept only a sealed
`OrderedGroupDimension[M,V]`; Bool and UUID dimensions do not satisfy it.
`golem.AndGroup`, `golem.OrGroup`, and `golem.NotGroup` combine group predicates.
Free, string-constrained text-measure functions expose the same
default/ASCII-insensitive text operators accepted by P2. Unsupported aggregate
constructors are absent from generated field types.

```go
func TextMeasureContains[M any, V ~string](measure Measure[M, V], value V, mode ComparisonMode) GroupPredicate[M]
func TextMeasureStartsWith[M any, V ~string](measure Measure[M, V], value V, mode ComparisonMode) GroupPredicate[M]
func TextMeasureEndsWith[M any, V ~string](measure Measure[M, V], value V, mode ComparisonMode) GroupPredicate[M]
```

Callers pass `DefaultComparison()` or `ASCIIInsensitive()`. Equality and
ordering predicates remain sensitive/binary; `In`/`NotIn` are not part of the
P6 group-predicate surface.

## 5. Programmatic aggregate

Generated namespaces build one immutable request:

```go
totalViews := Posts.Views.Sum()
averageRating := Posts.Rating.Avg()
latest := Posts.CreatedAt.Max()

request := Posts.Aggregate(
    Posts.AggregateWhere(
        Posts.Published.Eq(true),
    ),
    Posts.AggregateSelect(
        Posts.CountAll(),
        totalViews,
        averageRating,
        latest,
    ),
)

result, err := caller.Posts.Aggregate(ctx, request)
if err != nil {
    return err
}

count := golem.AggregateValue(result, Posts.CountAll())
sum := golem.AggregateValue(result, totalViews)
rating := golem.AggregateValue(result, averageRating)
```

The exact public family is:

```go
type AggregateRequest[M any] struct { /* opaque */ }
type AggregateOption[M any] interface { /* sealed */ }
type AggregateResult[M any] struct { /* opaque */ }

func AggregateValue[M, V any](
    result AggregateResult[M],
    measure Measure[M, V],
) ReadValue[V]
```

Generated namespace methods are:

```go
Posts.Aggregate(options ...golem.AggregateOption[Post]) golem.AggregateRequest[Post]
Posts.AggregateWhere(predicate golem.Predicate[Post]) golem.AggregateOption[Post]
Posts.AggregateSelect(
    first golem.AggregateMeasure[Post],
    rest ...golem.AggregateMeasure[Post],
) golem.AggregateOption[Post]
```

Exactly one non-empty select option is required. Duplicate measure identity is
invalid. Aggregate accepts `where`; it does not accept order, cursor, distinct,
skip, or take.

## 6. Programmatic local group-by

```go
author := Posts.AuthorID.Dimension()
published := Posts.Published.Dimension()
count := Posts.CountAll()
total := Posts.Views.Sum()

request := Posts.GroupBy(
    Posts.GroupDimensions(author, published),
    Posts.GroupMeasures(count, total),
    Posts.GroupWhere(Posts.CreatedAt.GTE(since)),
    Posts.GroupHaving(total.GT(golem.NewExactInteger(1_000))),
    Posts.GroupOrderBy(total.Desc(), author.Asc(), published.Asc()),
    Posts.GroupSkip(0),
    Posts.GroupTake(20),
)

rows, err := caller.Posts.GroupBy(ctx, request)
for _, row := range rows {
    authorID := golem.GroupValue(row, author)
    views := golem.GroupValue(row, total)
    _ = authorID
    _ = views
}
```

The result access contract is:

```go
type GroupRequest[M any] struct { /* opaque */ }
type GroupOption[M any] interface { /* sealed */ }
type GroupRow[M any] struct { /* opaque */ }
type GroupCell[M, V any] interface { /* sealed dimension or measure */ }

func GroupValue[M, V any](row GroupRow[M], cell GroupCell[M, V]) ReadValue[V]
```

Generated namespace methods are:

```go
Posts.GroupBy(options ...golem.GroupOption[Post]) golem.GroupRequest[Post]
Posts.GroupDimensions(first golem.LocalGroupDimension[Post], rest ...golem.LocalGroupDimension[Post]) golem.GroupOption[Post]
Posts.GroupMeasures(measures ...golem.AggregateMeasure[Post]) golem.GroupOption[Post]
Posts.GroupWhere(predicate golem.Predicate[Post]) golem.GroupOption[Post]
Posts.GroupHaving(predicate golem.GroupPredicate[Post]) golem.GroupOption[Post]
Posts.GroupOrderBy(first golem.GroupOrder[Post], rest ...golem.GroupOrder[Post]) golem.GroupOption[Post]
Posts.GroupTake(value int) golem.GroupOption[Post]
Posts.GroupSkip(value int) golem.GroupOption[Post]
```

Exactly one non-empty dimension option is required. Measures are optional.
`having` and order may reference a measure that is not selected; it is computed
privately but still classified. A selected or ordered dimension must be in the
group key. Duplicate dimensions/options, negative skip, zero take, and an
absolute take beyond the programmatic limit are invalid.

A negative take follows the P3 signed-take contract: an inner SQL query reverses
the complete effective ordering to choose rows and an outer SQL query restores
the requested order. Go does not reorder the page. The full grouped key is
always appended as a stable tie-break.

## 7. Programmatic relation group-by

Configured forward-to-one relation dimensions use a distinct request and method:

```go
country := Posts.AuthorCountry
published := Posts.Published.Dimension()
total := Posts.Views.Sum()

request := Posts.RelationGroupBy(
    Posts.RelationGroupDimensions(country, published),
    Posts.RelationGroupMeasures(Posts.CountAll(), total),
    Posts.RelationGroupWhere(Posts.CreatedAt.GTE(since)),
    Posts.RelationGroupHaving(total.GT(golem.NewExactInteger(100))),
    Posts.RelationGroupOrderBy(total.Desc(), country.Asc()),
    Posts.RelationGroupTake(20),
)

rows, err := caller.Posts.RelationGroupBy(ctx, request)
for _, row := range rows {
    value := golem.RelationGroupValue(row, country)
    _ = value
}
```

`RelationGroupDimensions` requires at least one configured relation dimension
and may include local dimensions. All relation dimensions must share one path.
Measures belong to `Post`; a `User` measure does not satisfy the root-model
interface. Missing/invisible path targets are excluded by inner semantics.

The relation request has the same where/having/order/take/skip behavior and
result cell contract as local grouping, but is a distinct opaque
`RelationGroupRequest[M]` so a local group request cannot be passed accidentally.

```go
type RelationGroupRequest[M any] struct { /* opaque */ }
type RelationGroupOption[M any] interface { /* sealed */ }
type RelationGroupRow[M any] struct { /* opaque */ }
type RelationGroupCell[M, V any] interface { /* sealed local/relation dimension or root measure */ }

func RelationGroupValue[M, V any](
    row RelationGroupRow[M],
    cell RelationGroupCell[M, V],
) ReadValue[V]

Posts.RelationGroupBy(options ...golem.RelationGroupOption[Post]) golem.RelationGroupRequest[Post]
Posts.RelationGroupDimensions(first golem.RelationGroupDimension[Post], rest ...golem.RelationGroupDimension[Post]) golem.RelationGroupOption[Post]
Posts.RelationGroupMeasures(measures ...golem.AggregateMeasure[Post]) golem.RelationGroupOption[Post]
Posts.RelationGroupWhere(predicate golem.Predicate[Post]) golem.RelationGroupOption[Post]
Posts.RelationGroupHaving(predicate golem.GroupPredicate[Post]) golem.RelationGroupOption[Post]
Posts.RelationGroupOrderBy(first golem.GroupOrder[Post], rest ...golem.GroupOrder[Post]) golem.RelationGroupOption[Post]
Posts.RelationGroupTake(value int) golem.RelationGroupOption[Post]
Posts.RelationGroupSkip(value int) golem.RelationGroupOption[Post]
```

## 8. Generated model clients

Every generated Caller, System, CallerTx, and SystemTx model client has:

```go
func (client CallerPostClient[P]) Aggregate(
    ctx context.Context,
    request golem.AggregateRequest[Post],
) (golem.AggregateResult[Post], error)

func (client CallerPostClient[P]) GroupBy(
    ctx context.Context,
    request golem.GroupRequest[Post],
) ([]golem.GroupRow[Post], error)

func (client CallerPostClient[P]) RelationGroupBy(
    ctx context.Context,
    request golem.RelationGroupRequest[Post],
) ([]golem.RelationGroupRow[Post], error)
```

`RelationGroupBy` is emitted only when the model declares at least one relation
dimension. The existing method remains unchanged:

```go
func (client CallerPostClient[P]) Count(
    ctx context.Context,
    options ...golem.ReadOption[Post],
) (int64, error)
```

P3 Count continues to accept only its existing legal count options. P6 proves
that it uses the same root authorization and provider semantics; it does not
force callers to replace Count with Aggregate.

System methods have identical requests/results but bypass caller policy and
hooks. Tx methods execute on the existing transaction. Analytics and scoped
reads run no model read hooks for Caller or System.

## 9. Scoped read builder

Only a model with `golem.ScopedReads[M]()` receives `Scope` and `Scoped`:

```go
posts := Posts.Scope()
author := golem.InnerJoin(posts, Posts.Author)

postID := Posts.ID.At(posts)
country := Users.Country.At(author)
views := Posts.Views.At(posts)
total := views.Sum()

query := golem.From(posts).
    Join(author).
    Where(
        golem.AndScoped(
            Posts.Published.At(posts).Eq(true),
            Users.Active.At(author).Eq(true),
        ),
    ).
    GroupBy(country).
    Having(total.GT(golem.NewExactInteger(100))).
    Select(country, total).
    OrderBy(total.Desc(), country.Asc()).
    Take(20)

rows, err := caller.Posts.Scoped(ctx, query)
for _, row := range rows {
    c := golem.ScopedValue(row, country)
    v := golem.ScopedValue(row, total)
    _, _ = c, v
}
```

The root/join family is:

```go
type Scope[M any] struct { /* opaque query identity and occurrence */ }
type ScopedQuery[M any] struct { /* opaque */ }
type ScopedRow struct { /* opaque */ }

func InnerJoin[M, R any](from Scope[M], relation Relation[M, R]) Scope[R]
func LeftJoin[M, R any](from Scope[M], relation Relation[M, R]) Scope[R]
func From[M any](root Scope[M]) ScopedQuery[M]

func ScopedValue[V any](row ScopedRow, expression ScopedResult[V]) ReadValue[V]
```

Generated scalar fields expose `At(scope)` with the narrow predicate and
aggregate methods appropriate to their logical type. Generated relations are
the only accepted join edges. `Join` accepts a derived scope and adds its
complete parent chain exactly once. A left-joined target field is `ReadNull`
when the row is absent or caller-invisible.

`ScopedQuery` is immutable. Its chain accepts only:

```go
Join(scope)
Where(scopedPredicate)
GroupBy(firstDimension, rest...)
Having(groupPredicate)
Select(firstResult, rest...)
OrderBy(firstOrder, rest...)
Take(int)
Skip(int)
```

There is no method or accepted value for raw SQL, table/column name, alias,
custom join predicate, insert, update, delete, DDL, execution callback,
connection, transaction, subquery, CTE, union, or window expression.

The generated client method is:

```go
func (client CallerPostClient[P]) Scoped(
    ctx context.Context,
    query golem.ScopedQuery[Post],
) ([]golem.ScopedRow, error)
```

and is mirrored by System, CallerTx, and SystemTx. The generic root type prevents
a Post query being passed to another model client. Opaque query identities and
runtime binding reject mixed roots or stale/foreign scope values that Go's type
system alone cannot distinguish.

An explicit to-many join multiplies rows. `count(*)` after that join counts
joined pairs, not distinct root models. P6 performs no implicit deduplication.

## 10. Scoped auditing and runtime limits

Generated application configuration gains:

```go
type AnalyticsLimits struct {
    MaxMeasures           int
    MaxDimensions         int
    MaxRelationDepth      int
    MaxContributionRows   int
    MaxIntermediateGroups int
    MaxProgrammaticGroups int
    MaxScopedJoins        int
    MaxScopedSelections   int
    MaxScopedPredicateNodes int
}

type Config[P any] struct {
    // existing P3/P4 fields...
    AnalyticsLimits  AnalyticsLimits
    AuditPrincipal   func(P) string
    ReportScopedQuery func(context.Context, golem.ScopedAuditRecord)
}
```

If no scoped root is enabled, the two audit functions may be nil. If any scoped
root is enabled, both are required at `Open`; there is no unaudited scoped mode.
`AuditPrincipal` is called once when a fresh Caller execution is created. Its
returned application-controlled identifier is snapshot with that execution.

`ScopedAuditRecord` is immutable and exposes getters for:

```go
Models() []golem.ModelID
Relations() []golem.RelationID
Fields() []golem.FieldID
JoinKinds() []golem.ScopedJoinKind
ExpressionKinds() []golem.ScopedExpressionKind
PrincipalAuditID() string
ExecutionID() uint64
IsSystem() bool
Provider() golem.Provider
ShapeFingerprint() golem.SchemaDigest
SQLFingerprint() golem.SchemaDigest
Duration() time.Duration
RowCount() int64
Outcome() golem.ScopedOutcome
```

Returned slices are copies. The record never exposes SQL text, bind values,
physical identifiers, principal values, actor values, or raw errors.

## 11. Generated GraphQL schema

P6 intentionally uses output selection to request measures. For `Post`, the
conventional roots are:

```graphql
type Query {
  aggregatePosts(where: PostWhereInput): PostAggregate!

  groupByPosts(
    by: [PostGroupField!]!
    where: PostWhereInput
    having: PostGroupHavingInput
    orderBy: [PostGroupOrderByInput!]
    skip: Int
    take: Int
  ): [PostGroup!]!

  relationGroupByPosts(
    by: [PostRelationGroupField!]!
    where: PostWhereInput
    having: PostRelationGroupHavingInput
    orderBy: [PostRelationGroupOrderByInput!]
    skip: Int
    take: Int
  ): [PostRelationGroup!]!
}
```

There is no separate `countPosts` root. `aggregatePosts { count }` is the
GraphQL count surface; the generated Go client retains its specialized Count.

Illustrative outputs are:

```graphql
type PostAggregate {
  count: BigInt!
  countFields: PostCountAggregate!
  sum: PostSumAggregate!
  avg: PostAvgAggregate!
  min: PostMinAggregate!
  max: PostMaxAggregate!
}

type PostCountAggregate {
  views: BigInt!
  rating: BigInt!
  createdAt: BigInt!
  title: BigInt!
}

type PostSumAggregate {
  views: BigInt
  rating: Decimal
}

type PostAvgAggregate {
  views: Float
  rating: Decimal
}

type PostMinAggregate {
  views: Int
  rating: Decimal
  createdAt: DateTime
  title: String
}

type PostMaxAggregate {
  views: Int
  rating: Decimal
  createdAt: DateTime
  title: String
}

type PostGroup {
  key: PostGroupKey!
  count: BigInt!
  countFields: PostCountAggregate!
  sum: PostSumAggregate!
  avg: PostAvgAggregate!
  min: PostMinAggregate!
  max: PostMaxAggregate!
}

type PostGroupKey {
  authorID: UUID
  published: Boolean
  createdAt: DateTime
}
```

Measure subobjects are non-null containers; nullable measure fields preserve
empty/all-null semantics. Count fields are non-null. A key field's nullability
matches its declared storage/path terminal nullability. Selecting a key absent
from `by` is `BAD_USER_INPUT` before SQL.

`PostGroupField` contains only configured local dimensions.
`PostRelationGroupField` contains configured local dimensions plus
`authorCountry`. The relation output key has the corresponding typed terminal
fields.

Having inputs are closed trees with `AND`, `OR`, and `NOT`, plus `key`, `count`,
`countFields`, `sum`, `avg`, `min`, and `max` typed subinputs. Order inputs have
the same key/measure grouping with `SortOrder`. A having/order measure need not
be selected, but it is authorized and computed privately. Unsupported
field/operator pairs are absent from SDL.

Example:

```graphql
query PostsByCountry($since: DateTime!) {
  relationGroupByPosts(
    by: [authorCountry]
    where: { createdAt: { gte: $since } }
    having: { sum: { views: { gt: "1000" } } }
    orderBy: [{ sum: { views: desc } }, { key: { authorCountry: asc } }]
    take: 20
  ) {
    key { authorCountry }
    count
    sum { views rating }
  }
}
```

The `views` sum filter uses exact `BigInt` string input. P5's active gqlgen
scalar codecs, error presenter, principal boundary, request complexity, and
selection occurrence rules remain in force.

## 12. Error and hook contract

Invalid shape, unsupported capability, group/row limit, arithmetic overflow,
bad exact numeric input, and provider capability absence map to the existing
stable error taxonomy. Authorization refusal maps to `FORBIDDEN`. No error
includes SQL, physical identifiers, bind values, policy internals, or driver
text.

Aggregate, group-by, relation-group-by, Count, and scoped operations do not run
model read or mutation hooks. This is a frozen P6 behavior, not an accidental
omission. They still use caller policy, transaction binding, cancellation,
limits, tracing, and scoped audit where applicable.
