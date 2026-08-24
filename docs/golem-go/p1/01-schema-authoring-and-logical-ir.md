# P1 schema authoring and normalized logical IR

Status: **controlling schema authoring contract**<br>
Scope: P1 model discovery, schema authoring, normalized logical model IR, and
the contract consumed by descriptor and physical-schema generation<br>
This document freezes the boundary between application Go source and every
compiler consumer. The
compiler, provider lowerers, migration planner, descriptor generator, and later
policy/runtime phases must consume this boundary rather than rediscovering model
facts independently.

P1 uses three representations:

1. `RawDeclIR`: source-located declarations, before cross-reference and semantic
   validation;
2. `ModelIR`: one versioned, normalized, provider-neutral logical model; and
3. a provider physical schema IR, defined by the next P1 contract, produced only
   from `ModelIR`.

`ModelIR` is not SQL text, a migration diff, a GraphQL schema, or a serialization
of `go/ast`. It is the immutable semantic registry from which those products are
derived.

---

## 1. Decisions at a glance

1. A generation unit has one explicit exported schema-root struct. Root fields
   select the exact model types. A relation target absent from the root is an
   error, never an implicitly discovered or silently pruned model.
2. Every selected model is an exported named struct carrying a blank model marker.
3. Common facts use `db` and `golem` struct tags. Multi-column and advanced SQL
   facts use statically interpreted, typed model-attached specifications.
4. Advanced specification methods are declarative call trees. The generator
   type-checks but never executes application code.
5. The first pass parses common declarations and emits an in-memory generated
   handle overlay. The second pass type-checks and interprets advanced model,
   policy, and hook declarations against that overlay. A clean checkout needs
   one generation command.
6. Stable IDs are logical identities, distinct from Go, GraphQL, table, column,
   constraint, and index names. Renames preserve identity only through an
   explicit stable ID.
7. Logical scalar kinds and provider storage are separate. No provider renderer
   infers a database type from a Go type.
8. Primary keys and unique constraints create identities. A unique index is an
   access-path object and does not create a public selector.
9. Relations are canonical logical edges. A `belongs_to` edge owns the physical
   foreign key; its inverse does not create a second constraint.
10. Exposure, policy/hook binding, limits, and operation configuration are kept
    in a separate `ContractIR`. They never affect the persistence fingerprint.
11. Unordered declarations are canonical-sorted; ordered key/index/relation
    components preserve author order.
12. Diagnostics are structured, source-located, deterministic, and free of
    absolute machine paths.

---

## 2. Generation unit and schema-root selection

### 2.1 Root form

A generation unit is selected by the fully qualified name of an exported root
struct:

```go
package social

type SocialSchema struct {
	_ struct{} `golem:"schema;id=example.social;providers=sqlite,postgresql"`

	User       User       `golem:"model"`
	Post       Post       `golem:"model"`
	Comment    Comment    `golem:"model"`
	Tag        Tag        `golem:"model"`
	PostTag    PostTag    `golem:"model"`
}
```

The command accepts `-schema=<import-path>.SocialSchema`. If the loaded package
pattern contains exactly one schema root, the flag may be omitted. Zero roots or
multiple roots without `-schema` is an error.

The root rules are:

- the root is an exported named non-generic struct;
- it carries exactly one blank field with `golem:"schema"`;
- every non-blank root field must carry `golem:"model"` and have an exported
  named non-pointer struct type;
- a model type may appear once only;
- a model may live in another package; the generator groups generated files by
  owning package;
- root field names are documentary and do not affect `ModelID` or model names;
- relation targets and explicit join models must be selected by the same root;
- generated framework/system tables are not model fields and enter the later
  physical schema through a separately versioned system-schema contribution.

The root is an explicit persistence boundary, not a GraphQL exposure list. A
selected model may later be unexposed while remaining available to relations,
policies, migrations, and the system client.

### 2.2 Schema marker attributes

| Attribute | Required | Meaning |
|---|---:|---|
| `schema` | yes | declares the blank field as the schema marker |
| `id=<stable-id>` | yes | stable generation-unit identity |
| `providers=<csv>` | no | target artifacts; defaults to `sqlite,postgresql` |

Provider names are exactly `sqlite` and `postgresql`. Unknown or duplicate names
fail generation.

### 2.3 Provider target policy

The compiler and product implement SQLite and PostgreSQL equally. A portable
schema targets both. A schema may deliberately target one provider only when it
uses an explicitly provider-scoped storage feature. Generated artifacts record
that restriction, and starting the application with another provider fails
before opening traffic.

> **OWNER APPROVAL REQUIRED — provider-restricted roots.** This proposal permits
> `providers=postgresql` for an application that deliberately uses a typed
> PostgreSQL-only storage extension. The alternative is to require every
> application schema to compile for both providers, which would ban supported
> provider-specific capabilities. The
> recommendation is to approve provider-restricted application schemas while
> retaining dual-provider implementation and conformance as a Golem release gate.

---

## 3. Model and field declaration

### 3.1 Model marker

Each selected model repeats its own marker so a root cannot accidentally select
an arbitrary application struct:

```go
type User struct {
	_ struct{} `golem:"model;id=example.social.User;table=users;graphql=User"`

	ID        string    `db:"id" golem:"id=example.social.User.ID;pk;default=uuid"`
	Email     string    `db:"email" golem:"unique"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at" golem:"default=now;readonly"`

	Posts []Post `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
}
```

Model marker attributes:

| Attribute | Required | Meaning |
|---|---:|---|
| `model` | yes | marks the struct as a model |
| `id=<stable-id>` | no | explicit stable `ModelID` source |
| `table=<identifier>` | yes | unqualified physical table name |
| `graphql=<name>` | no | GraphQL model name stored in `ContractIR` |

A table name is one validated identifier, not quoted or schema-qualified SQL.
Provider namespace/schema placement is a typed schema-level provider extension.

### 3.2 Persisted scalar fields

A persisted scalar or enum field is exported, is not blank, has `db:"<column>"`,
and has no `relation` attribute. Its common `golem` attributes are:

| Attribute | Meaning |
|---|---|
| `id=<stable-id>` | explicit stable `FieldID` source |
| `pk` | single-column primary key |
| `unique` | single-column unique constraint |
| `default=<token>` | typed logical default, section 7 |
| `updated` | application-managed update timestamp |
| `readonly` | readable but absent from public create/update inputs |
| `writeonly` | writable but absent from reads/filter/order/identity surfaces |
| `immutable` | writable on create but absent from updates |
| `hidden` | absent from every public surface |
| `graphql=<name>` | transport name in `ContractIR` |

`pk`, `unique`, and `default` are persistence facts. `readonly`, `writeonly`,
`immutable`, `hidden`, and `graphql` are Golem contract metadata and do not alter
physical DDL.

The `db` column name is required. P1 never derives it from the Go field name.
Column names must be unique within the table.

### 3.3 Blank common directives

Blank fields carry table-level common declarations:

```go
type Membership struct {
	_ struct{} `golem:"model;id=example.social.Membership;table=memberships"`
	_ struct{} `golem:"primary=pk_memberships(tenant_id,user_id)"`
	_ struct{} `golem:"unique=uq_memberships_tenant_handle(tenant_id,handle)"`
	_ struct{} `golem:"index=idx_memberships_user_created(user_id,created_at)"`

	TenantID string    `db:"tenant_id"`
	UserID   string    `db:"user_id"`
	Handle   string    `db:"handle"`
	Created  time.Time `db:"created_at" golem:"default=now"`
}
```

The exact common directive grammar is:

```text
primary = physical_name(column_name, column_name, ...)
unique  = physical_name(column_name, column_name, ...)
index   = physical_name(column_name, column_name, ...)
```

Column references in common tags use the declared `db` column spelling. This
preserves the accepted authoring examples. The parser immediately resolves them to
stable `FieldID`s; no downstream component retains those strings as references.

Common indexes are non-unique B-tree indexes with ascending keys, no include
columns, no predicate, and no expressions. Anything more advanced uses section 4.

### 3.4 Attribute lexical rules

- Semicolons separate attributes.
- An attribute appears at most once on one tag.
- Commas are meaningful only inside an attribute value.
- Identifiers match `[A-Za-z_][A-Za-z0-9_]*` and are stored unquoted.
- Stable IDs match `[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}`.
- Unknown attributes and empty tokens fail generation.
- Whitespace surrounding tokens is ignored; whitespace inside identifiers is not.
- Tags never contain SQL expressions, clauses, quoting, or provider type names.

---

## 4. Typed model-attached advanced schema definitions

Struct tags remain intentionally small. Composite definitions needing typed
fields, expressions, referential actions, index methods, opclasses, or provider
scope belong to one optional model method:

```go
func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel[Post](
		golem.PrimaryKey("pk_posts", Posts.TenantID, Posts.ID),
		golem.Unique("uq_posts_tenant_slug", Posts.TenantID, Posts.Slug),
		golem.Index[Post]("idx_posts_author_created").
			Keys(
				golem.IndexColumn(Posts.AuthorID),
				golem.IndexColumn(Posts.CreatedAt).Desc(),
			).
			Where(Posts.DeletedAt.IsNull()),
		golem.Check("ck_posts_score", Posts.Score.GTE(0)),
		golem.Generated(
			Posts.SearchTitle,
			golem.Lower(Posts.Title),
			golem.Stored,
		),
		golem.RelationOptions(Posts.Author).
			OnUpdate(golem.Cascade).
			OnDelete(golem.Restrict),
	)
}
```

This is valid Go against the generated authoring API. Generated scalar handles
implement `golem.Column[Model]`, so a heterogeneous list of field value types is
valid without an impossible homogeneous generic variadic.

For a concrete model named `Post`, the exact method contract is:

```go
func (Post) GolemModel() golem.ModelSpec[Post]
```

The receiver and `ModelSpec` type argument must be the same named model type.
This is a signature pattern stated with real Go syntax, not a fictitious generic
method declaration.

The receiver is the value form of the model. There is at most one such method.
The body must be one return statement whose expression is a tree of registered
schema DSL constructors, typed constants, literals, generated field/relation
handles, and fluent option calls. Local variables, control flow, loops,
closures, helper calls, I/O, environment reads, and arbitrary function calls are
rejected. The compiler resolves constructor identity through `go/types`, not
source spelling, so import aliases are harmless.

The method is **statically interpreted and never executed**. This keeps
generation deterministic and prevents schema generation from running application
code.

### 4.1 Common versus advanced precedence

The same semantic object may be declared in one place only:

- field `pk` and advanced `PrimaryKey` are mutually exclusive;
- a common blank primary/unique/index and an advanced object with the same
  stable identity or physical name is a duplicate error;
- advanced relation options refine one common relation declaration; they do not
  create an unrelated relation field;
- `Generated` owns the column expression and is incompatible with `default` and
  `updated`;
- provider-scoped objects may coexist with portable objects only when their
  physical names and stable IDs do not collide for a target provider.

### 4.2 Two-pass handle bootstrap

Application policy and advanced schema methods reference generated handles, so
the generator follows this fixed single-command pipeline:

1. parse schema/model roots, fields, tags, enum signatures, and method syntax
   without requiring the package to type-check generated handle references;
2. resolve the preliminary field/relation set;
3. generate handle source into an in-memory `go/packages` overlay;
4. type-check every owning package with that overlay;
5. inspect and statically interpret `GolemSchema`, `GolemEnum`, `GolemModel`,
   policy, and hook signatures/bodies;
6. normalize and validate the final IR; and
7. write generated files atomically only after the whole generation unit succeeds.

No checked-in generated file is trusted as input. A stale generated file is
excluded by its generated header. Users never need to generate twice.

### 4.3 Optimistic concurrency version token

One model-owned `int64` scalar may be nominated as the model's version token:

```go
func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel[Post](
		golem.OptimisticConcurrency(Posts.Version),
	)
}
```

The token is a portable persistence fact. It cannot be provider-scoped, and a
model declares at most one. The nominated handle must be a generated scalar
field handle of the declaring model.

The field itself must be stored, non-null, and logically `int64`. It may not be
a primary-key or unique-key component, nor a local or remote foreign-key field,
because identity and reference columns already carry a meaning that a rewritten
version would corrupt. It may not carry an authored default, a generated-column
expression, an `updated` producer, or database read-only ownership, since each
of those is a competing writer for the same column. Exposure is constrained in
both directions: the field may not be hidden or write-only, because a caller
who cannot read the version can never supply the next expectation; and it may
not be immutable or read-only, because the engine must rewrite it. Each failure
has its own stable `P1_CONCURRENCY_*` diagnostic.

Ownership of the column's value is total and belongs to the engine. Create
installs the initial value, update advances it by one under a compare-and-swap
on the expected value, and an application operation that authors the field is
rejected rather than honored. The declaration is therefore not a hint the
runtime may skip.

What a caller passes, and which generated methods a declared token withdraws
from the root and nested mutation surfaces, is the P4 contract; see
[Mutations and transactions](../p4/PUBLIC-MUTATION-ABI.md).

---

## 5. Logical SQL type vocabulary

Go bindings and logical SQL types are explicit. Provider types are a later
lowering concern.

| Logical kind | Accepted Go declaration | Required parameters | Portable intent |
|---|---|---|---|
| `Bool` | `bool` | none | boolean |
| `Int16` | `int16` | none | signed 16-bit integer |
| `Int32` | `int32` | none | signed 32-bit integer |
| `Int64` | `int64` | none | signed 64-bit integer / GraphQL BigInt |
| `Float32` | `float32` | none | IEEE binary32 |
| `Float64` | `float64` | none | IEEE binary64 |
| `Decimal` | `golem.Decimal` | precision 1..18, scale 0..precision | exact fixed-point |
| `String` | `string` | optional maximum rune length | Unicode text |
| `Bytes` | `[]byte` | optional maximum byte length | opaque bytes |
| `UUID` | `golem.UUID` | none | 128-bit UUID |
| `Date` | `golem.Date` | none | calendar date |
| `Time` | `golem.Time` | precision 0..6 | time without zone |
| `DateTime` | `time.Time` | precision 0..6, fixed at UTC semantics | instant |
| `JSON` | `json.RawMessage` or `golem.JSON[T]` | schema identity optional | JSON value |
| `Enum` | named string type with `GolemEnum` | declared ordered values | closed text enum |
| `ScalarList` | `golem.List[T]` | element kind and provider capability | explicit list storage |

Pointer forms and `golem.Null[T]` make a scalar nullable. No other wrapper is
implicitly accepted in v1. In particular, bare `int`, `uint*`, aliases of
platform-width integers, arbitrary slices, maps, interfaces, complex numbers,
and unrecognized `database/sql` nullable wrappers fail with a typed diagnostic.

`[]byte` is the sole built-in slice scalar. A `[]Model` with a relation tag is a
to-many relation. Any other slice without the explicit `golem.List[T]` capability
is rejected.

### 5.1 Type parameters

Non-default type parameters use typed advanced declarations:

```go
func (Invoice) GolemModel() golem.ModelSpec[Invoice] {
	return golem.DefineModel[Invoice](
		golem.DecimalType(Invoices.Total, 18, 4),
		golem.StringType(Invoices.Reference, golem.MaxRunes(120)),
		golem.DateTimeType(Invoices.CreatedAt, golem.Precision(6)),
	)
}
```

Defaults are Decimal `(18, 2)`, unbounded String/Bytes, Time precision 6, and
DateTime precision 6.

### 5.2 Portable physical intent

The logical registry fixes these lowerings so later provider work cannot choose
independently:

| Logical kind | SQLite intent | PostgreSQL intent |
|---|---|---|
| Bool | INTEGER plus generated 0/1 check | BOOLEAN |
| Int16 | INTEGER plus range check | SMALLINT |
| Int32 | INTEGER plus range check | INTEGER |
| Int64 | INTEGER | BIGINT |
| Float32 | REAL | REAL |
| Float64 | REAL | DOUBLE PRECISION |
| Decimal(p,s) | scaled signed INTEGER, scale in descriptor | NUMERIC(p,s) |
| String | TEXT plus optional length check | TEXT plus optional length check |
| Bytes | BLOB plus optional length check | BYTEA plus optional length check |
| UUID | canonical lowercase TEXT plus shape check | UUID |
| Date | canonical ISO-8601 TEXT | DATE |
| Time | canonical ISO-8601 TEXT | TIME(p) WITHOUT TIME ZONE |
| DateTime | signed Unix microseconds INTEGER | TIMESTAMPTZ(6), normalized UTC |
| JSON | canonical UTF-8 TEXT plus `json_valid` check | JSONB |
| Enum | TEXT plus membership check | TEXT plus membership check |

Portable `Decimal` is restricted to precision 18 because SQLite stores its
scaled coefficient in a signed 64-bit integer. This gives exact equality,
ordering, and storage on stock SQLite without pretending that SQLite NUMERIC is
arbitrary precision. PostgreSQL-only wider decimal is a typed provider extension
and makes the schema PostgreSQL-only.

> **OWNER APPROVAL REQUIRED — portable Decimal representation.** Approve the
> scaled-integer SQLite / `NUMERIC` PostgreSQL mapping and precision-18 portable
> ceiling, or remove Decimal fields from P1 until an alternative exact SQLite
> codec/operator plan is accepted. Mapping portable Decimal to SQLite NUMERIC is
> explicitly rejected because it would make the P2/P6 exactness promise false.

PostgreSQL native enum types are not the portable default because their migration
lifecycle differs materially from SQLite checks. They remain a typed
PostgreSQL-only storage extension.

---

## 6. Nullability

Nullability is a property of a persisted scalar column, not of GraphQL output:

- `T` is `NOT NULL`;
- `*T` and `golem.Null[T]` are nullable;
- primary-key components must be non-null;
- a generated expression whose nullability cannot be proven non-null requires a
  nullable Go field;
- a composite foreign key is either all non-null or all nullable in portable v1;
  mixed-null composite keys are rejected;
- a relation's database requiredness comes from its local FK columns, not from
  whether its Go relation field is a pointer.

To-one relation fields are pointers even for required foreign keys. This avoids
recursive value types and allows caller authorization to withhold a related row.
To-many relation fields are slices. Relation shape therefore does not redefine
column nullability.

GraphQL authorization nullability belongs to `ContractIR`/P5 and never changes
the physical `NOT NULL` constraint.

---

## 7. Defaults and generated-on-write fields

`DefaultIR` distinguishes where a value is produced:

| Source token | Logical kind | Producer | Physical default |
|---|---|---|---|
| type-correct literal | `Literal` | database | bound/quoted provider literal |
| `now` | `Now` | database | provider current timestamp/date/time expression |
| `uuid` | `UUID` or `String` | Golem mutation planner | none |
| `identity` | `Identity` | database | provider identity/autoincrement mechanism |
| typed provider default | `Provider` | provider-defined | provider extension |

The common tag parser is type-directed:

- booleans: `true`, `false`;
- signed integers and floats: Go lexical form without units;
- Decimal: canonical base-10 text;
- strings and bytes: a Go-quoted string after the tag itself is decoded;
- enums: declared enum wire value, such as `PUBLIC`;
- Date/Time/DateTime: RFC 3339-compatible quoted literal of the corresponding
  logical type;
- `now`, `uuid`, and `identity` are reserved words.

For example:

```go
Status Visibility `db:"visibility" golem:"default=PUBLIC"`
Title  string     `db:"title" golem:"default=\"untitled\""`
ID     string     `db:"id" golem:"pk;default=uuid"`
```

`uuid` is application-generated because stock SQLite has no portable database
UUID function. On `UUID` it produces the logical UUID value; on `String` it
produces the canonical lowercase textual form required by the public contract's
models. A String with this default remains logically String and does not acquire
UUID comparison or validation semantics for arbitrary subsequently written
values. The default still makes the create input optional; P4 must inject it
before the SQL write. `identity` is accepted only on a single-column signed
integer primary key and lowers to the provider's database-managed identity.

`updated` is also application-managed. It is accepted only on a non-null
DateTime field, normally with `default=now`, and it creates no hidden trigger.

A provider default is declared only through a typed provider extension. There is
no `default_sql` string escape hatch.

---

## 8. Enums

An enum is a named string type with one exact attached declaration:

```go
type Visibility string

const (
	VisibilityPublic  Visibility = "PUBLIC"
	VisibilityFriends Visibility = "FRIENDS"
	VisibilityPrivate Visibility = "PRIVATE"
)

func (Visibility) GolemEnum() golem.EnumSpec[Visibility] {
	return golem.DefineEnum(
		golem.EnumValue(VisibilityPublic),
		golem.EnumValue(VisibilityFriends),
		golem.EnumValue(VisibilityPrivate),
	)
}
```

For the concrete enum `Visibility`, the exact signature is:

```go
func (Visibility) GolemEnum() golem.EnumSpec[Visibility]
```

The receiver and `EnumSpec` type argument must be the same named string type.

The body obeys the same static-declaration restrictions as `GolemModel`. Values
must be typed constants of `E`, non-empty, unique, and explicitly ordered.
Unlisted constants do not become database values. A named string type used by a
model without `GolemEnum` is rejected rather than treated as String.

`EnumID` defaults from schema ID plus qualified Go type. `EnumValueID` defaults
from `EnumID` plus wire value. Typed `StableID` options preserve identity across
renames. Changing a wire value without preserving an explicit ID is a drop/add
schema change.

---

## 9. Keys and unique constraints

`KeyIR` has an explicit kind, stable ID, logical/physical name, and ordered
non-empty field IDs.

Rules:

- a model has zero or one primary key;
- every primary-key component is a persisted non-null scalar;
- a single field `pk` creates the primary key; composite/named keys use a blank
  directive or typed advanced declaration;
- unique constraints contain persisted scalar fields and preserve order;
- nullable unique fields are accepted, but do not become a portable identity
  selector unless every component is non-null;
- identity selectors arise only from the primary key and non-null unique
  constraints;
- duplicate component fields and duplicate physical names fail;
- two selectors that derive the same public name fail even if their physical
  names differ.

An unnamed public compound selector is derived by joining logical field names
with `_`. Physical constraint names never determine public selector spelling.

---

## 10. Indexes

An index is an access path, represented exactly as:

```go
type IndexIR struct {
	ID           IndexID
	ModelID      ModelID
	PhysicalName SQLIdentifier
	Unique       bool
	Method       IndexMethod
	Keys         []IndexKeyIR        // ordered, non-empty
	Include      []FieldID           // ordered, no duplicates
	Predicate    *SchemaPredicateIR
	Provider     ProviderScope
	Extensions   []ProviderExtensionIR
}

type IndexKeyIR struct {
	Column    *FieldID
	Expr      *SchemaExprIR
	Direction SortDirection // Asc or Desc
	Nulls     NullsOrder     // Default, First, Last
	Collation *CollationRef
	OpClass   *ProviderSymbolRef
}
```

Exactly one of `Column` and `Expr` is set. Ordered keys preserve declaration
order. Include fields do not participate in uniqueness or `EqualityIndexed`.

Portable indexes support:

- B-tree method only;
- column or portable deterministic expression keys;
- ascending/descending keys;
- a portable partial predicate;
- no include columns, opclass, or provider collation.

Although PostgreSQL supports `INCLUDE`, SQLite does not; therefore include is a
PostgreSQL-scoped option. PostgreSQL methods (`hash`, `gin`, `gist`, `spgist`,
`brin`), opclasses, native collations, and PostgreSQL expression functions are
typed provider extensions. SQLite collation and virtual-table indexes are typed
SQLite extensions.

Example:

```go
func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel[Post](
		golem.Index[Post]("idx_posts_lower_title").
			Keys(golem.IndexExpr(golem.Lower(Posts.Title))).
			Where(Posts.DeletedAt.IsNull()),
		golem.ForProvider[Post](
			golem.PostgreSQL,
			pgschema.Index("idx_posts_search").
				Using(pgschema.GIN).
				Keys(
					pgschema.ExpressionKey(
						pgschema.ToTSVector("simple", Posts.Title),
					).OpClass(pgschema.TSVectorOps),
				),
		),
	)
}
```

No constructor accepts raw SQL. Method and opclass values are registered typed
symbols, not user strings.

A unique index, especially a partial or expression index, does not create a
Golem unique selector. Authors who need an identity declare a `Unique` constraint.

`EqualityIndexed(field)` is true only when a non-full-text/non-inverted index has
that field as its first plain column key, or the field leads a primary/unique
constraint. Expression keys and include columns never satisfy it.

---

## 11. Schema expressions, checks, and generated columns

P1 defines a closed, typed, provider-neutral schema-expression AST distinct from
the authorization predicate AST.

Portable value expressions are:

- generated column references and typed literals;
- `+`, `-`, `*`, `/`, and remainder where the logical type supports them;
- string concatenation;
- `Lower`, `Upper`, `Length`, and `Coalesce`;
- explicit casts registered by the scalar registry.

Portable predicates are:

- `Eq`, `NE`, `LT`, `LTE`, `GT`, `GTE`;
- `IsNull`, `IsNotNull`, `In`;
- `And`, `Or`, and `Not`.

There are no subqueries, relation traversal, aggregates, windows, sequence
reads, current-user/session reads, or unregistered functions. Provider functions
must be typed provider extensions with declared volatility and result type.

### 11.1 Check constraints

```go
golem.Check(
	"ck_posts_score",
	Posts.Score.IsNull().Or(Posts.Score.GTE(0)),
)
```

Checks use SQL-standard check semantics: `FALSE` violates; `TRUE` and `UNKNOWN`
pass. This is deliberately not the policy engine's two-valued authorization
boundary. Authors who require null rejection state `IsNotNull` or make the
column non-null.

Checks have stable IDs, validated physical names, a provider scope, and a typed
predicate. They cannot be declared in common tags because that would require a
string expression language or raw SQL.

### 11.2 Generated columns

```go
SearchTitle string `db:"search_title" golem:"readonly"`

func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel[Post](
		golem.Generated(
			Posts.SearchTitle,
			golem.Lower(Posts.Title),
			golem.Stored,
		),
	)
}
```

Portable generated columns are stored. Virtual generated columns are a typed
provider extension. A generated column:

- is automatically database-read-only;
- has no `default`, `updated`, or identity generation;
- references columns of its own row only;
- uses deterministic/immutable expressions only;
- cannot participate in a portable primary key;
- must have a Go nullability capable of representing the expression's inferred
  nullability; and
- is topologically ordered with other generated columns; cycles fail generation.

---

## 12. Relations and foreign keys

### 12.1 Common relation fields

```go
type Post struct {
	_ struct{} `golem:"model;table=posts"`

	ID       string `db:"id" golem:"pk;default=uuid"`
	AuthorID string `db:"author_id"`

	Author *User `db:"-" golem:"relation=belongs_to;name=PostAuthor;fields=author_id;references=id"`
}

type User struct {
	_ struct{} `golem:"model;table=users"`
	ID string `db:"id" golem:"pk;default=uuid"`

	Posts []Post `db:"-" golem:"relation=has_many;name=PostAuthor;fields=id;references=author_id"`
}
```

Supported relation kinds are `belongs_to`, `has_one`, `has_many`, and
`many_to_many`.

Common relation attributes:

| Attribute | Meaning |
|---|---|
| `relation=<kind>` | cardinality/direction |
| `name=<logical-name>` | required for self-relations or multiple edges between the same pair |
| `fields=<csv>` | ordered local `db` column names |
| `references=<csv>` | ordered target `db` column names |
| `through=<table>` | many-to-many join table |
| `source=<column>` | join column(s) toward the source |
| `target=<column>` | join column(s) toward the target |

Relation fields always use `db:"-"`. A to-one/has-one/belongs-to field uses
`*Target`; a to-many/many-to-many field uses `[]Target`.

### 12.2 Canonical relation IR

Paired source/inverse declarations normalize to one edge plus two relation-field
views:

```go
type RelationIR struct {
	ID                RelationID
	Name              string
	SourceModel       ModelID
	TargetModel       ModelID
	SourceField       FieldID
	InverseField      *FieldID
	Cardinality       RelationCardinality
	LocalFields       []FieldID // ordered
	RemoteFields      []FieldID // ordered
	ForeignKey        *ForeignKeyIR
	Through           *ThroughRelationIR
}
```

The `belongs_to` declaration owns the physical FK. `has_one` and `has_many` are
inverse views and do not create another constraint. One side may omit the inverse
field, but if both sides exist their names and mappings must agree exactly.

Validation requires:

- equal non-zero component counts and order;
- compatible logical types;
- remote fields forming a primary or non-null unique constraint;
- all-nullable or all-non-null local composite components;
- a unique local FK for `has_one`/one-to-one;
- unambiguous inverse pairing, including self-relations;
- no relation target outside the schema root; and
- no write-only relation metadata.

Relation requiredness comes from local FK nullability. A pointer relation field
does not make a required FK optional.

### 12.3 FK contract

Every canonical `belongs_to` relation creates a physical foreign-key constraint.
The portable defaults are:

```text
ON UPDATE NO ACTION
ON DELETE NO ACTION
NOT DEFERRABLE
MATCH SIMPLE
```

Advanced relation options may select `Restrict`, `Cascade`, `SetNull`, or
`SetDefault`; the linker validates column nullability/default requirements.
Deferrability and provider-only actions are typed provider options. Physical FK
names are deterministic and may be overridden with a validated name/stable ID.

Foreign keys are **not auto-indexed**. The compiler emits a deterministic warning
when a to-many correlation's foreign-key columns lack a usable leading index,
because later reads will choose the batched path. A warning never silently adds
an access path the author did not declare.

### 12.4 Many-to-many

Normalized IR supports both explicit join models and generated through tables.
The first P1 vertical slice uses an explicit `PostTag` model with a composite PK.
Implicit `many_to_many` is rejected with `P1_RELATION_IMPLICIT_M2M_UNAVAILABLE`
until through-table stable IDs, composite source/target mappings, generated
constraints/indexes, and migration fixtures are implemented together. It must
not partially create a join table.

---

## 13. Provider extensions

Provider extensions are typed declarations, never unstructured maps or SQL:

```go
type ProviderExtensionIR struct {
	ID       ExtensionID
	Provider Provider
	Version  uint16
	Owner    ObjectID
	Kind     ProviderExtensionKind
	Payload  ProviderExtensionPayload
}
```

Each payload is a registered tagged union known to the corresponding provider
package. Examples include PostgreSQL namespace, native enum storage, wider
numeric, index method/opclass, and SQLite collation or virtual generated-column
storage.

Schema-level example:

```go
func (SocialSchema) GolemSchema() golem.SchemaSpec {
	return golem.DefineSchema(
		golem.ForProviderSchema(
			golem.PostgreSQL,
			pgschema.Namespace("social"),
		),
	)
}
```

For the concrete root `SocialSchema`, the exact root method is:

```go
func (SocialSchema) GolemSchema() golem.SchemaSpec
```

It follows the same static interpretation restrictions. Provider payloads may
contain validated identifiers, typed numbers/enums, and typed expression nodes.
No payload may contain SQL text, quoted identifiers, or a callback renderer.

An extension declares whether it is:

- physical-only, so the other provider may omit the access-path/storage tuning;
  or
- semantic, so the schema root must target only providers implementing it.

The provider registry rejects unknown extension versions before physical
planning.

---

## 14. Stable identity and rename behavior

Stable IDs are opaque semantic identities used by generated descriptors,
migration comparison, events, and fingerprints.

Defaults:

```text
SchemaID = explicit root id
ModelID  = SchemaID + "/model/" + importPath + "." + GoType
FieldID  = ModelID  + "/field/" + GoField
EnumID   = SchemaID + "/enum/"  + importPath + "." + GoType
RelationID = owner ModelID + "/relation/" + GoField
```

Constraint/index IDs derive from owner ID, kind, and ordered referenced stable
IDs. Physical names are not part of a derived stable ID.

Rules:

- IDs are unique across the generation unit and validated before linking;
- changing a Go name without an explicit ID changes identity and is interpreted
  as remove/add;
- to preserve identity across a Go/package/logical rename, copy the prior ID into
  `id=...` or the typed `StableID(...)` option;
- changing only a table, column, constraint, or index physical name while keeping
  the stable ID is a rename candidate for the migration planner;
- GraphQL renames live in `ContractIR` and do not affect persistence identity;
- the migration planner never guesses a rename from similarity;
- generated numeric ordinals may optimize one compiled artifact but are never
  serialized as stable identity.

Diagnostics for a remove/add caused by a likely rename may suggest the prior ID,
but generation remains deterministic and non-interactive.

---

## 15. Exposure and model-attached Golem metadata

The normalized compilation result separates persistence from application
surface metadata:

```go
type CompilationIR struct {
	Model    ModelIR
	Contract ContractIR
}

type ContractIR struct {
	Models  []ModelContractIR
	Enums   []EnumContractIR
	Methods []AttachedMethodIR
}

type ModelContractIR struct {
	ModelID       ModelID
	GraphQLName   string
	FieldModes    []FieldModeIR
	Operations    []Operation
	Subscriptions bool
	Aggregation   *AggregationContractIR
	Limits        LimitContractIR
	Exposed       bool
}
```

P1 records and validates contract metadata but P5 materializes GraphQL exposure.
The root selects persistence models. Exposure configuration selects which of
those models become public.

Intrinsic field modes may be declared in common field tags. Model exposure,
operation allowlists, subscriptions, aggregations, and limits belong to a
statically parseable generated-application configuration consumed after model
IR construction; they are not database schema facts.

Validation includes:

- hidden or write-only identity components are forbidden;
- write-only relations and database-generated/read-only columns are forbidden;
- only `writeonly+immutable` may overlap;
- unknown field/model/operation names fail;
- an exposed relation target that is not exposed requires an explicit P5 choice
  to hide that relation or expose a computed replacement; it is never silently
  pruned;
- contract-only changes do not produce migrations.

`AttachedMethodIR` records typed symbol identity and model/actor association for
recognized schema, policy, and hook methods. It does not store executable
callbacks or affect the persistence fingerprint.

### 15.1 Fingerprint separation

P1 produces three domain-separated hashes:

1. `ModelFingerprint`: normalized persisted logical schema;
2. `ProviderFingerprint[provider]`: normalized provider physical schema; and
3. `ContractFingerprint`: Golem exposure/method metadata.

Source spans, comments, input file order, generator timestamps, generated
formatting, and method bodies outside recognized declarations are excluded.
Policy/hook or exposure changes cannot masquerade as database drift.

---

## 16. Raw declaration IR

`RawDeclIR` is serializable for golden tests but is not a public compatibility
format. It preserves authored order and source evidence without embedding
`go/ast` pointers.

```go
type RawDeclIR struct {
	FormatVersion uint16
	Root          RawSchemaDecl
	Models        []RawModelDecl
	Enums         []RawEnumDecl
	Methods       []RawMethodDecl
}

type SourceSpan struct {
	ModulePath   string
	RelativeFile string
	StartLine    uint32
	StartColumn  uint32
	EndLine      uint32
	EndColumn    uint32
}

type RawSchemaDecl struct {
	PackagePath string
	GoName      string
	Marker      []RawAttribute
	Models      []RawModelRef
	Span        SourceSpan
}

type RawModelRef struct {
	FieldName       string
	ModelPackage    string
	ModelGoName     string
	Attributes      []RawAttribute
	Span            SourceSpan
}

type RawModelDecl struct {
	PackagePath string
	GoName      string
	Marker      []RawAttribute
	Fields      []RawFieldDecl
	Directives  []RawDirectiveDecl
	Span        SourceSpan
}

type RawFieldDecl struct {
	GoName       string
	TypeSyntax   string
	DBTag        *string
	GolemAttrs   []RawAttribute
	IsBlank      bool
	Span         SourceSpan
}

type RawAttribute struct {
	Name     string
	RawValue *string
	Span     SourceSpan
}

type RawDirectiveDecl struct {
	Kind       string
	Name       string
	Components []string
	Attributes []RawAttribute
	Span       SourceSpan
}

type RawEnumDecl struct {
	PackagePath string
	GoName      string
	Underlying  string
	Values      []RawEnumValue
	Method      RawMethodRef
	Span        SourceSpan
}

type RawEnumValue struct {
	GoName    string
	WireValue string
	StableID  *string
	Ordinal   uint32
	Span      SourceSpan
}

type RawMethodRef struct {
	ReceiverPackage string
	ReceiverGoName  string
	Name            string
	Span            SourceSpan
}

type RawMethodDecl struct {
	ReceiverPackage string
	ReceiverGoName  string
	Name            string
	Signature       string
	BodySyntax      string
	Span            SourceSpan
}
```

`BodySyntax` exists for deterministic static interpretation and diagnostics; it
is not hashed directly. The normalized constructor tree is hashed.

---

## 17. Normalized logical Model IR

The following Go-shaped contract is normative in structure. Concrete package
names and unexported implementation helpers may vary.

```go
type ModelIR struct {
	FormatVersion uint16
	Schema        SchemaIdentityIR
	Providers     []Provider // canonical order: sqlite, postgresql
	Enums         []EnumIR
	Models        []ModelDeclIR
	Relations     []RelationIR
	Extensions    []ProviderExtensionIR
}

type EnumIR struct {
	ID          EnumID
	Go          GoNamedTypeIR
	LogicalName string
	Values      []EnumValueIR
}

type EnumValueIR struct {
	ID        EnumValueID
	GoName    string
	WireValue string
}

type SchemaIdentityIR struct {
	ID          SchemaID
	PackagePath string
	GoName      string
}

type ModelDeclIR struct {
	ID          ModelID
	Go          GoNamedTypeIR
	LogicalName string
	Table       TableBindingIR
	Fields      []FieldIR
	PrimaryKey  *KeyIR
	Uniques     []KeyIR
	Indexes     []IndexIR
	Checks      []CheckIR
}

type TableBindingIR struct {
	PhysicalName SQLIdentifier
}

type FieldIR struct {
	ID               FieldID
	GoName           string
	LogicalName      string
	DeclarationOrder uint32
	Kind             FieldKind // Scalar, Enum, ScalarList, Relation
	Scalar           *ScalarFieldIR
	Relation         *RelationFieldIR
}

type ScalarFieldIR struct {
	Column       SQLIdentifier
	Type         LogicalTypeIR
	Nullable     bool
	Default      *DefaultIR
	Generation   *GeneratedColumnIR
	Updated      bool
	DatabaseReadOnly bool
}

type LogicalTypeIR struct {
	Kind         LogicalTypeKind
	EnumID       *EnumID
	Element      *LogicalTypeIR
	Precision    *uint16
	Scale        *uint16
	MaxLength    *uint32
	JSONSchemaID *string
	Capability   *CapabilityID
}

type DefaultIR struct {
	Kind     DefaultKind
	Producer DefaultProducer // Database, Application, Provider
	Literal  *TypedLiteralIR
	Provider *ProviderSymbolRef
}

type KeyIR struct {
	ID           KeyID
	Kind         KeyKind // Primary, Unique
	LogicalName  string
	PhysicalName SQLIdentifier
	Fields       []FieldID
	SelectorName *string
}

type CheckIR struct {
	ID           CheckID
	PhysicalName SQLIdentifier
	Predicate    SchemaPredicateIR
	Provider     ProviderScope
}

type GeneratedColumnIR struct {
	Expr     SchemaExprIR
	Storage  GeneratedStorage // Stored or provider-specific Virtual
	Provider ProviderScope
}

type RelationFieldIR struct {
	RelationID RelationID
	Role       RelationEndpointRole // Source or Inverse
	Kind       RelationKind
}

type RelationIR struct {
	ID           RelationID
	Name         string
	SourceModel  ModelID
	TargetModel  ModelID
	SourceField  FieldID
	InverseField *FieldID
	Cardinality  RelationCardinality
	LocalFields  []FieldID
	RemoteFields []FieldID
	ForeignKey   *ForeignKeyIR
	Through      *ThroughRelationIR
}

type ForeignKeyIR struct {
	ID           ForeignKeyID
	PhysicalName SQLIdentifier
	OnUpdate     ReferentialAction
	OnDelete     ReferentialAction
	Match        MatchKind
	Deferrable   Deferrability
}
```

All references use stable IDs. Lookups/maps are derived immutable indexes and are
not serialized as second copies of metadata.

### 17.1 Normalized ordering

Canonical output orders:

1. providers by fixed registry order;
2. enums by `EnumID`, enum values by declared order;
3. models by `ModelID`;
4. scalar fields by `FieldID` in registry serialization, while a separate
   `DeclarationOrder` ordinal remains available for generated Go layout;
5. primary/unique component fields in author order;
6. indexes by `IndexID`, keys/include fields in author order;
7. checks by `CheckID`;
8. relation fields remain in their owning model's field registry; canonical
   relations sort by `RelationID`, with local/remote components in author order;
9. extensions by provider, owner ID, kind, ID.

No map iteration order enters generated code, SQL, diagnostics, or fingerprints.

---

## 18. Diagnostics

```go
type Diagnostic struct {
	Code     string
	Severity Severity
	Message  string
	Primary  SourceSpan
	Related  []DiagnosticLabel
	Hint     string
}
```

Rules:

- codes are stable (`P1_TAG_UNKNOWN`, `P1_RELATION_TYPE_MISMATCH`, and so on);
- the primary span points to the narrowest authored token;
- cross-object failures include related spans for the other declaration;
- messages name Go/logical objects; provider failures additionally name the
  provider and capability;
- paths are module-relative with `/` separators, never absolute;
- diagnostics are sorted by relative path, start position, code, then related
  stable ID;
- independent errors may be collected, but an invalid node is never guessed into
  normalized IR;
- an unsupported feature is an error, not an omitted generated artifact;
- generator panics and raw `go/types` errors are wrapped into stable diagnostics.

Warnings are limited to behavior-preserving concerns, such as an unindexed FK
that will force batched relation loading. Portability, type, identity, relation,
and provider-capability failures are errors.

---

## 19. Acceptance fixtures

P1 must add the following source, RawDeclIR, ModelIR, descriptor, and eventual
provider-plan goldens.

### 19.1 Valid fixtures

1. **User/Post baseline**
   - String UUID application default;
   - nullable `*string`;
   - DateTime `now` and `updated`;
   - single PK and unique constraint;
   - ordered compound index;
   - named required belongs-to plus has-many inverse.

2. **Membership composite identity**
   - composite PK preserving order;
   - named compound unique selector;
   - composite FK to a composite target key;
   - SQLite/PostgreSQL deterministic identity descriptors.

3. **Self relation**
   - nullable `ParentID`;
   - named `Parent`/`Replies` inverse pair;
   - explicit `SET NULL` delete action and required supporting index.

4. **Exact scalar matrix**
   - every logical type, pointer and `golem.Null[T]` forms;
   - Decimal parameters, String/Bytes limits, temporal precision;
   - enum declaration/order;
   - JSON and explicit scalar-list capability behavior.

5. **Advanced portable schema**
   - expression and descending index keys;
   - portable partial predicate;
   - check with nullable logic;
   - stored generated column and dependency ordering.

6. **Provider extension**
   - PostgreSQL schema namespace and GIN/opclass index;
   - explicit PostgreSQL-only root;
   - typed extension version in normalized IR.

7. **Rename stability**
   - Go, table, column, and constraint rename with preserved explicit IDs;
   - fingerprints show logical identity preserved and physical schema changed;
   - same rename without IDs is deterministic remove/add.

8. **Exposure separation**
   - identical `ModelFingerprint` before/after operation/field exposure changes;
   - changed `ContractFingerprint`;
   - no provider migration diff.

### 19.2 Invalid fixture matrix

- missing/multiple schema roots and duplicate model selection;
- unmarked, unexported, pointer, anonymous, or generic model types;
- malformed/unknown/duplicate tags and invalid identifiers;
- unsupported Go types/wrappers and ambiguous slices;
- duplicate stable IDs, tables, columns, physical constraint/index names;
- zero/duplicate/nullable PK components and conflicting PK declarations;
- bad key/index component, duplicate selector name, partial unique used as identity;
- unsupported portable include/method/opclass/collation;
- raw SQL attempt in any tag/provider extension;
- check/generated expression type error, volatile function, cross-row reference,
  generated cycle, and nullability mismatch;
- relation missing target, mismatched component count/order/type, non-unique
  reference, mixed-null composite FK, ambiguous/self inverse, incompatible shape;
- `SET NULL` on non-null FK and `SET DEFAULT` without a default;
- implicit many-to-many before its complete feature gate;
- provider semantic extension outside the root provider set;
- malformed advanced method signature/body/helper call;
- hidden/write-only identity, write-only relation, and illegal access-mode overlap.

### 19.3 Determinism and bootstrap acceptance

- a clean checkout generates successfully in one command;
- the same source generated repeatedly is byte-identical;
- source file discovery order and map iteration are shuffled in tests without
  changing RawDeclIR canonical output, ModelIR, diagnostics, or generated files;
- generated overlays are not written when a second-pass error occurs;
- stale generated files do not influence parsing/type checking;
- changing comments or file locations without semantic changes leaves all three
  fingerprints unchanged;
- every physical identifier in normalized IR traces to a validated declaration
  or deterministic generated-name function.

---

## 20. P1 scope boundary

This contract enables later work but does not implement it. P1 schema authoring
does not add CRUD, authorization SQL, GraphQL execution, mutation behavior,
events, aggregates, or runtime auto-migration.

P1 generates descriptor substrate: stable model/field/type/key/index/relation
metadata, typed handles, scan/write column metadata, method bindings, provider
schema plans, migration artifacts, and fingerprints. Typed CRUD requests and
transport types arrive in their owning phases while consuming the same IDs and
IR.

The first vertical slice implements the User/Post baseline plus the explicit
Membership/PostTag composite fixture, simple indexes, and direct relations. It
may parse and explicitly refuse gated advanced objects before their provider
lowering lands. A refusal is complete behavior; silent omission is not.
