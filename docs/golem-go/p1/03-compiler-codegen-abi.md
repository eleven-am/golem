# P1 compiler, code generation, and ABI contract

Status: **Wave 0 implementation contract**<br>
Scope: P1 source discovery, compiler orchestration, generated Go ABI, binding
discovery, deterministic publication, and the P1/P2/P4/P5 seams<br>
Authority: [`../BIBLE.md`](../BIBLE.md) remains controlling

This document makes the P1 generator boundary executable without moving policy
semantics, mutation semantics, or GraphQL execution into P1. It resolves the
bootstrap problem created by model-local generated descriptors: handwritten
policy and hook methods may refer to generated names, while generation must also
work from a clean checkout on which those names do not exist yet.

The compiler is static. It parses and type-checks application source; it never
imports and runs an application package, invokes `init`, executes a schema
function, or treats runtime reflection as schema authority.

---

## 1. Decisions fixed by this contract

P1 implements these decisions:

1. A schema has one explicit, closed-form `DefineSchema` source root.
2. `golem generate` uses a syntax/bootstrap pass and then a generated-overlay
   type-check pass. Intermediate generated source is not written to the worktree.
3. Structs and tags compile first to unresolved IR and then to one globally
   resolved, canonical IR. Relations are never resolved during a single struct
   walk.
4. Typed descriptor handles are emitted into each model's package. The generated
   application package imports model packages; model packages never import the
   generated application package.
5. Generated value namespaces such as `Posts.ID` are retained. Go types use
   ordinary package-level names such as `PostCreateRequest`; `Posts.CreateInput`
   is invalid Go and is not part of the API.
6. P1 provides the type-level shells needed to compile and discover policy and
   hook methods. P2 supplies policy meaning. P4 supplies operation and hook
   execution meaning.
7. Policy and hook discovery uses `go/types` method sets after the overlay has
   made the whole package compile. Method-name scraping is not sufficient.
8. Every registered model in one schema uses exactly one actor type.
9. P1 exposes transport-neutral naming, scalar, identity, relation, exposure,
   and nullability facts. P5 alone materializes GraphQL SDL, schema objects, and
   resolver bindings.
10. Generation is canonical and crash-recoverable. A generation failure does
    not publish a knowingly mixed artifact set, and the manifest is the sole
    authority for stale generated-file removal.

---

## 2. Public authoring root

### 2.1 `DefineSchema` shape

The schema package contains one function selected by the CLI. The default name
is `DefineSchema`.

```go
package social

import "github.com/eleven-am/golem/go/golem"

func DefineSchema(schema *golem.Schema) {
	// A stable schema name is part of generated identity. It is not inferred
	// from a module path or output directory.
	golem.SchemaName(schema, "social")

	// Exactly one actor declaration is required.
	golem.Actor[Actor](schema)

	// Model declaration order is not semantic.
	golem.Model[User](schema)
	golem.Model[Post](schema)
}
```

This is valid Go. `Schema`, `SchemaName`, `Actor`, and `Model` are small stable
declaration-shell APIs in the public `golem` package. The compiler recognizes
their package identity, not the local import alias.

`DefineSchema` is declarative source syntax. The compiler does **not** call it.
The public functions exist so ordinary Go tooling can parse and type-check the
declaration, but calling the function at application runtime has no role in
schema construction.

### 2.2 Closed grammar

The function must have exactly this signature:

```go
func <root>(schema *golem.Schema)
```

The body permits only direct expression statements in this grammar:

```text
SchemaName(schema, <string literal>)
Actor[<named actor type>](schema)
Model[<named struct type>](schema)
```

The package qualifier may use a normal import alias. Dot imports are rejected.
String concatenation, constants standing in for the schema-name literal,
assignment, return values, local variables, blocks, loops, conditionals,
closures, helper calls, method calls, reflection, and function values are
rejected. Comments and empty statements are ignored.

The restrictions are deliberate:

- generation cannot vary with process state, build-time side effects, or an
  arbitrary helper implementation;
- the first pass can identify registered types before generated identifiers
  exist; and
- a reviewer can see the whole schema membership list in one place.

Exactly one `SchemaName`, one `Actor`, and at least one `Model` call are required.
A type may be registered only once. The schema name must match
`^[a-z][a-z0-9_]{0,62}$`. It is a durable identity namespace, so changing it is
a schema-identity change rather than a cosmetic rename.

### 2.3 Package scope

The schema root may register exported named struct types from multiple packages
in the same main module. Generated descriptor files are written into the owning
model packages. Ordinary Go import rules already prevent cyclic handwritten
package relations; the compiler adds no mechanism to bypass them.

A model package belongs to one generated schema. Two schema roots attempting to
own the same package are unsupported because they would compete for the same
generated names and policy actor ABI. Split the models into distinct packages
or use one schema.

The actor may be an exported named type in any registered model package or in a
separate package importable by every model package that defines policy. Aliases,
unnamed structs, interfaces, pointers, and type parameters are not actor types
in P1.

---

## 3. CLI contract

The production command is a thin wrapper around the compiler pipeline:

```text
golem generate \
  --schema ./internal/social \
  --root DefineSchema \
  --app-out ./internal/generated/socialapp \
  --migrations ./migrations

golem check \
  --schema ./internal/social \
  --root DefineSchema \
  --app-out ./internal/generated/socialapp \
  --migrations ./migrations
```

Rules:

- `--schema` is a Go package pattern resolving to exactly one package. It
  defaults to `.`.
- `--root` is an unqualified function name and defaults to `DefineSchema`.
- `--app-out` is required for `generate`; it must be inside the current main
  module and must not be a model package.
- `--migrations` is required when migration generation is enabled and must be
  inside the current main module.
- package identity always comes from `go list`/`go/packages`; filesystem paths
  are not embedded as semantic package identities.
- `generate` stages, validates, and publishes outputs.
- `check` computes all outputs without modifying files and fails when generated
  files, the manifest, or migration metadata differ.
- a golden-update switch belongs to test tooling, not to the production CLI.
- the command has no environment-dependent configuration language. Provider and
  model metadata come from the schema source and tags.

A package may carry an ordinary directive:

```go
//go:generate go run github.com/eleven-am/golem/go/cmd/golem generate --schema . --app-out ../generated/socialapp --migrations ../../../migrations
```

The directive is convenience only. CI invokes `golem check` directly.

---

## 4. Compiler pipeline

### 4.1 Overview

```text
CLI/options
    |
    v
syntax load + DefineSchema extraction
    |
    v
unresolved declarations and raw tags
    |
    v
global symbol table + IR resolution + validation
    |
    v
canonical resolved IR
    |
    +----------+-------------+----------------+----------------+
    |          |             |                |                |
    v          v             v                v                v
bootstrap   canonical     SQLite DDL     PostgreSQL DDL   GraphQL-facing
Go source   fingerprints  plan/render    plan/render      metadata
    |
    v
go/packages overlay reload and complete type check
    |
    v
typed policy/hook discovery and direct bridges
    |
    v
final model files + application registry + manifests
    |
    v
gofmt + full package check + provider artifact checks
    |
    v
crash-recoverable publication
```

### 4.2 Pass A: syntax and unresolved IR

The first load requests syntax, files, imports, and partial type information. It
must tolerate only errors caused by absent known generated symbols. Syntax,
import, declaration, and handwritten type errors remain fatal.

The compiler:

1. locates the root by package and function name;
2. validates the closed grammar without executing it;
3. resolves registered named types from declarations and imports;
4. parses model marker fields, scalar fields, relation fields, enum declarations,
   `db` tags, and `golem` tags;
5. records module-relative source positions for diagnostics; and
6. emits unresolved IR containing symbolic references, not guessed relation
   targets.

Known generated names are determined from registered model names and the
reserved-name table. The loader must not suppress an arbitrary `undefined`
identifier merely because its spelling starts with `Golem` or ends with
`Request`.

### 4.3 Global IR resolution

Resolution is a separate pass over the complete schema:

1. establish the schema namespace and canonical package/type identities;
2. assign stable model, field, and relation identities;
3. resolve scalar and enum codecs;
4. preserve declared order for primary, unique, index, and relation field lists;
5. validate keys and indexes against scalar fields;
6. resolve all relation endpoints and verify arity, order, scalar kind, and
   nullability compatibility;
7. reject ambiguous inverses and physical-name collisions;
8. resolve exposure metadata and generated-name collisions; and
9. canonicalize unordered collections.

Self-relations, forward declarations, cross-file declarations, and two named
relations between the same model pair are ordinary cases. A resolver that
depends on source walk order is incorrect.

No provider renderer, Go emitter, or future GraphQL generator consumes
unresolved IR.

### 4.4 Bootstrap overlay

The resolved IR renders one prospective generated model file per model package.
The files are supplied to the second `go/packages` load through an overlay map.
They are not written to disk.

The bootstrap source and final model source share the same emitter. Bootstrap is
not a weaker handwritten stub language. It includes:

- model, scalar-field, and relation handles;
- stable identities;
- operation request type aliases required by recognized hook signatures;
- package binding result types; and
- generated-name reservations.

Using the final emitter prevents a successful bootstrap from hiding an invalid
final declaration.

### 4.5 Pass B: complete typed discovery

The compiler reloads every participating package with the overlay and requires a
clean type check. It then uses `go/types` method sets to discover policies and
hooks.

This pass is required because:

- a shared policy helper may be declared in another file;
- import aliases and type aliases cannot be validated reliably by spelling;
- actor sameness requires `types.Identical`;
- pointer and value method sets differ; and
- malformed recognized methods must fail rather than disappear.

Pass B emits direct binding functions. It does not emit an `init` function,
mutate a process-global registry, or retain a policy built during generation.

### 4.6 Final validation

Before publication, the compiler:

1. renders every final Go and metadata file in memory;
2. formats Go through `go/format`;
3. loads and type-checks the complete prospective package graph through an
   overlay containing all final files and excluding stale manifest-owned files;
4. checks that every artifact embeds the expected fingerprint and generator/IR
   version;
5. validates both provider artifacts from the same DDL plan; and
6. computes the new generated-file manifest.

No file is published if any validation fails.

---

## 5. Versioned IR and identities

### 5.1 IR layers

The compiler owns two internal representations:

- `RawSchema`: source-oriented declarations, raw tags, symbolic type and field
  references, and source spans;
- `Schema`: fully resolved, provider-neutral, versioned semantic data.

Only `Schema` is canonicalizable and consumable by emitters. It uses ordered
slices. Maps may be implementation indexes but are not serialization sources.

At minimum, resolved IR carries:

- IR version and stable schema name;
- canonical Go package path, Go type name, logical name, GraphQL name, table and
  column names as separate properties;
- stable typed model, field, and relation IDs;
- scalar/enum codec identity, Go type identity, required/null state, default,
  updated/read-only state, and storage capability;
- ordered primary, unique, normal-index, and relation key members;
- to-one/to-many cardinality, ownership, target, and inverse relation metadata;
- exposure mode and model exclusion/operation declarations;
- generated Go identifier inventory;
- policy/hook binding presence after Pass B; and
- source spans used only for diagnostics, never for semantic hashing.

### 5.2 Stable IDs

Positional ordinals are forbidden. Adding a model or field must not renumber
unrelated identities.

P1 derives IDs from SHA-256 over a domain-separated canonical identity:

```text
model:    "golem:model:v1\x00"    + schemaName + "\x00" + logicalModelName
field:    "golem:field:v1\x00"    + modelID    + "\x00" + logicalFieldName
relation: "golem:relation:v1\x00" + modelID    + "\x00" + logicalFieldName
```

The generated ID is the first 128 bits rendered as 32 lowercase hexadecimal
digits and held in a distinct Go type. The compiler detects collisions across
the full IDs before truncation and across every truncated ID in the schema. A
logical rename creates a new identity. Stable rename aliases are not introduced
in P1.

Go package paths and physical table/column names are deliberately absent from
identity derivation. Moving a package or renaming a physical column through a
reviewed migration does not silently change logical event and descriptor
identity.

### 5.3 Fingerprints

P1 produces two digests with different jobs:

- `SchemaFingerprint`: SHA-256 of the canonical physical schema projection. It
  covers provider-neutral tables, columns, scalar storage, nullability, defaults,
  primary/unique/index order, and foreign keys. Generated application code and
  migration metadata embed it; startup database verification compares it.
- `GenerationDigest`: SHA-256 of the complete canonical resolved IR plus actor
  identity, binding inventory, exposure metadata, generator version, and template
  ABI version. The manifest and every generated Go file embed it to detect stale
  mixed output.

A GraphQL exposure change therefore regenerates code without falsely requiring a
database migration. A storage change changes both digests.

Absolute paths, source positions, timestamps, local Go versions, environment
variables, and output directories are excluded from both digests.

---

## 6. Public descriptor ABI

### 6.1 Runtime identity types

The public runtime package defines distinct identity types:

```go
package golem

type ModelID string
type FieldID string
type RelationID string
```

Public operation APIs do not accept arbitrary IDs or physical identifiers.
Callers pass generated typed handles. The planner resolves the handle ID against
the immutable generated registry and rejects an unknown or model/type-mismatched
handle before SQL planning.

Generated-code constructors may be exported because generated code lives in an
application module, outside Golem's `internal` tree. They accept stable IDs and
logical shape only. Physical table and column names enter the engine solely
through the validated generated schema blob/registry, not through a caller
condition.

### 6.2 Typed handles

The type-level ABI has this shape:

```go
package golem

type Predicate[M any] struct {
	// opaque
}

type ScalarField[M any, V any] struct {
	// opaque generated reference
}

type ToOne[M any, R any] struct {
	// opaque generated relation reference
}

type ToMany[M any, R any] struct {
	// opaque generated relation reference
}

func (f ScalarField[M, V]) Eq(value V) Predicate[M]
func (f ScalarField[M, V]) Ne(value V) Predicate[M]
func (r ToOne[M, R]) Is(predicate Predicate[R]) Predicate[M]
func (r ToMany[M, R]) Some(predicate Predicate[R]) Predicate[M]
```

P1 must provide every accepted operator's **type signature** needed to compile a
policy package. P2 implements validation, normalization, evaluation, and SQL
rendering and may reject unsupported provider capabilities. P2 may not change
the model ownership represented by these generic types.

Methods unavailable for a value kind are represented by narrower generated or
runtime handle types rather than runtime panics. For example, ordering methods
must not appear on JSON merely because `ScalarField[M, V]` exists. The exact
operator-interface decomposition is frozen alongside the P2 operator inventory
before the binding gate.

### 6.3 Generated model namespace

Given:

```go
type Post struct {
	ID       string `db:"id" golem:"pk"`
	AuthorID string `db:"author_id"`
	Author   *User  `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
}
```

P1 emits valid same-package Go of this form:

```go
// Code generated by golem. DO NOT EDIT.
package social

import "github.com/eleven-am/golem/go/golem"

type postFields struct {
	ID       golem.ScalarField[Post, string]
	AuthorID golem.ScalarField[Post, string]
	Author   golem.ToOne[Post, User]
}

var Posts = postFields{
	ID:       golem.GeneratedScalarField[Post, string](golem.FieldID("...")),
	AuthorID: golem.GeneratedScalarField[Post, string](golem.FieldID("...")),
	Author:   golem.GeneratedToOne[Post, User](golem.RelationID("...")),
}
```

The generated namespace type is unexported; its fields and the plural namespace
value are exported. A user writes `Posts.AuthorID.Eq(actor.ID)`.

Relation handles contain stable IDs and generic root/target ownership. They do
not contain pointers to other package globals. The immutable registry resolves
the target descriptor after all model descriptors have been decoded. This
avoids Go initialization cycles for self-relations and mutually-related models.

Pluralization is deterministic but never silently guesses through a collision.
The compiler validates generated namespace names and reports the colliding model
and suggested explicit logical name. It does not add numeric suffixes.

### 6.4 Generated type names

Types are package-level identifiers:

```go
type PostCreateRequest = golem.CreateHookRequest[Post]
type PostUpdateRequest = golem.UpdateHookRequest[Post]
type PostDeleteRequest = golem.DeleteHookRequest[Post]
type PostUpdateManyRequest = golem.UpdateManyHookRequest[Post]
type PostDeleteManyRequest = golem.DeleteManyHookRequest[Post]
```

Future generated operation input/result types follow the same rule:

```go
type PostCreateInput struct { /* generated fields when P4 owns them */ }
type PostUpdateInput struct { /* generated fields when P4 owns them */ }
```

`Posts.CreateInput`, `Posts.CreateRequest`, and similar spellings are forbidden:
`Posts` is a value and Go has no nested type namespace beneath a value.

The prefix `GolemGenerated` and every compiler-predicted public identifier are
reserved within participating model packages. A collision with handwritten
source is a generation error, never an overwrite or suffixing opportunity.

---

## 7. Policy ABI and discovery

### 7.1 Recognized method

P1 recognizes exactly this method shape on a value receiver:

```go
func (Post) DefinePolicy(rules *golem.Rules[Post], actor Actor)
```

Requirements:

- receiver is the registered named model value type, not `*Post`;
- method name is exactly `DefinePolicy`;
- there are no method type parameters;
- first parameter is exactly `*golem.Rules[Post]` from the canonical runtime
  package;
- second parameter is exactly the schema's one registered actor type under
  `types.Identical`;
- there are no results and no variadic parameters.

If a method named `DefinePolicy` exists on a registered model but has another
shape, generation fails with the expected and actual signatures. It is never
treated as an unrelated user method.

An exposed caller model without `DefinePolicy` receives no policy binding and is
deny-by-default. P1 reports this as an error for a model configured for caller or
GraphQL exposure. A system-only model may omit it.

### 7.2 `Rules` shell

P1 owns the stable generic declaration and method signatures needed for package
type checking:

```go
package golem

type Rules[M any] struct {
	// opaque ordered builder state
}

func NewRules[M any]() *Rules[M]

func (r *Rules[M]) CanRead(predicate Predicate[M])
func (r *Rules[M]) CanCreate(predicate Predicate[M])
func (r *Rules[M]) CanUpdate(predicate Predicate[M])
func (r *Rules[M]) CanDelete(predicate Predicate[M])
```

The complete field-rule signatures are frozen with P2's accepted vocabulary.
P1 does not decide priority, normalization, classification, or provider support.
It supplies an opaque ordered recording shell so model packages and generated
bridges compile.

### 7.3 Fresh generated bridge

The same-package generated file emits a direct bridge conceptually equivalent
to:

```go
func golemBuildPostPolicy(actor Actor) (golem.FrozenPolicy, error) {
	rules := golem.NewRules[Post]()
	Post{}.DefinePolicy(rules, actor)
	return rules.Freeze(golem.GeneratedModelID[Post]())
}
```

The bridge is stored inside one exported package binding accessor generated for
the application package:

```go
func GolemGeneratedBindings() golem.PackageBindings[Actor]
```

`GolemGeneratedBindings` returns immutable binding descriptors and function
values. It does not construct a policy. The application registry calls the
policy function for each execution after actor resolution. There is no actor or
policy cache in generated package state.

The bridge itself remains unexported where possible; only the single package
binding accessor crosses into the generated application package. There is no
`init` registration.

---

## 8. Hook ABI and discovery

### 8.1 P1's ownership

P1 owns:

- valid Go type names for hook requests/results;
- opaque generic request shells sufficient for typed method signatures;
- typed free helpers that allow a before hook to address a generated field;
- exact method discovery and deterministic binding metadata; and
- direct method bridges with no reflection.

P4 owns request contents, mutation planning, authorization order, transaction
placement, retries, result verification, and after/after-commit execution.

### 8.2 Compilable before-hook shape

P1 replaces the invalid conceptual `*Posts.CreateRequest` syntax with:

```go
func (Post) BeforeCreate(
	ctx context.Context,
	request *PostCreateRequest,
) error {
	actor := golem.ActorFrom[Actor](ctx)
	return golem.SetCreate(request, Posts.AuthorID, actor.ID)
}
```

The helper is type-safe:

```go
func SetCreate[M, V any](
	request *CreateHookRequest[M],
	field ScalarField[M, V],
	value V,
) error
```

A `User` field cannot be applied to a `PostCreateRequest`, and a string field
cannot receive a boolean. P4 may add typed convenience APIs without changing
this foundational signature.

### 8.3 Closed recognized-name table

P1 reserves and discovers these model methods:

```text
BeforeFindOne       AfterFindOne
BeforeFindFirst     AfterFindFirst
BeforeFindMany      AfterFindMany
BeforeCreate        AfterCreate        AfterCommitCreate
BeforeUpdate        AfterUpdate        AfterCommitUpdate
BeforeDelete        AfterDelete        AfterCommitDelete
BeforeUpdateMany    AfterUpdateMany    AfterCommitUpdateMany
BeforeDeleteMany    AfterDeleteMany    AfterCommitDeleteMany
```

There is no upsert hook. Upsert runs the create or update branch's hooks. There
are no aggregate hooks in this ABI.

For every operation in the table, the same-package emitter creates both aliases
before typed discovery:

| Operation | Request alias target | Result alias target |
|---|---|---|
| `FindOne` | `golem.FindOneHookRequest[Post]` | `golem.FindOneHookResult[Post]` |
| `FindFirst` | `golem.FindFirstHookRequest[Post]` | `golem.FindFirstHookResult[Post]` |
| `FindMany` | `golem.FindManyHookRequest[Post]` | `golem.FindManyHookResult[Post]` |
| `Create` | `golem.CreateHookRequest[Post]` | `golem.CreateHookResult[Post]` |
| `Update` | `golem.UpdateHookRequest[Post]` | `golem.UpdateHookResult[Post]` |
| `Delete` | `golem.DeleteHookRequest[Post]` | `golem.DeleteHookResult[Post]` |
| `UpdateMany` | `golem.UpdateManyHookRequest[Post]` | `golem.UpdateManyHookResult[Post]` |
| `DeleteMany` | `golem.DeleteManyHookRequest[Post]` | `golem.DeleteManyHookResult[Post]` |

For example:

```go
type PostFindManyRequest = golem.FindManyHookRequest[Post]
type PostFindManyResult = golem.FindManyHookResult[Post]
type PostCreateRequest = golem.CreateHookRequest[Post]
type PostCreateResult = golem.CreateHookResult[Post]
```

The `Post` prefix is replaced by the registered Go model type name. These are
instantiated aliases, not generic aliases, and are valid under the P1 Go 1.23
module baseline.

Before methods have the exact shape:

```go
func (Model) Before<Operation>(context.Context, *ModelOperationRequest) error
```

After and after-commit methods have the exact shape:

```go
func (Model) After<Operation>(context.Context, ModelOperationResult) error
func (Model) AfterCommit<Operation>(context.Context, ModelOperationResult) error
```

The generated aliases point at stable opaque runtime shells in P1. P3/P4 fill
their operation-specific data and behavior. Read operations have no
after-commit method. A recognized name with a wrong receiver, context, request,
result, variadic flag, or return type is a generation error.

Only value receivers are supported. Model receiver state is not hook state; the
runtime invokes methods on the zero model value. Applications put dependencies
in context or generated application composition rather than a mutable receiver.

Go permits one method with a given name on a receiver, so per-model method order
is fixed by the operation-phase table above. Future application middleware is a
separate ordered composition input; source file order never orders hooks.

### 8.4 Binding inventory

Pass B emits immutable hook entries keyed by typed model ID, operation, and
phase. Each entry invokes a direct method expression or generated closure. It
contains no string lookup, reflection, or arbitrary function discovered through
`init`.

The presence of hook metadata is included in `GenerationDigest`, not
`SchemaFingerprint`.

---

## 9. Package and import direction

Recommended repository packages:

```text
go/cmd/golem                         thin CLI
go/golem                             public authoring/runtime ABI
go/internal/compiler/load            go/packages, syntax, overlays
go/internal/compiler/schema          DefineSchema grammar
go/internal/compiler/tags            db/golem tag parser
go/internal/compiler/ir              raw/resolved versioned IR
go/internal/compiler/resolve         keys, relations, names, stable IDs
go/internal/compiler/validate        cross-cutting validation/diagnostics
go/internal/compiler/discover        typed policy/hook method discovery
go/internal/codegen/model            same-package descriptors and shells
go/internal/codegen/bindings         package/app binding emitters
go/internal/codegen/graphqlmeta      transport-neutral metadata only
go/internal/codegen/manifest         canonical digests and output manifest
go/internal/migrate/plan             provider-neutral DDL plan
go/internal/migrate/sqlite           SQLite renderer/verifier
go/internal/migrate/postgres         PostgreSQL renderer/verifier
go/internal/gentest                  golden and fixture harness
```

Application direction:

```text
golem public ABI
    ^
    |
model packages + their generated same-package files
    ^
    |
generated application package
    ^
    |
application main
```

The public `golem` package imports no application package. Model packages never
import the generated application package. The generated application package may
import several model packages and the actor package. This is the only supported
direction.

Generated files are:

- exactly one `zz_golem_models.gen.go` in each participating model package;
- deterministic files beneath the configured generated application package;
- canonical provider migration artifacts and metadata beneath the configured
  migration directory; and
- one root manifest under `.golem/generated-manifest.json` at the main module
  root.

One same-package file avoids stale per-model files and makes the overlay/final
comparison exact.

---

## 10. GraphQL-facing metadata boundary

P1 stores facts; P5 creates GraphQL.

P1 metadata includes:

- logical and proposed GraphQL model/field names;
- scalar codec identities and transport exactness requirements;
- database requiredness, input requiredness from defaults, and list/null shape;
- ordered scalar/composite unique selectors;
- relation cardinality and target IDs;
- exposure mode: normal, immutable, read-only, write-only, or hidden;
- declared operation/exclusion configuration and configured limits; and
- deterministic collision and reserved-name diagnostics.

P1 does **not**:

- import a GraphQL implementation;
- emit SDL, schema objects, root fields, inputs, or resolvers;
- decide operator/filter exposure before P2 provider agreement;
- duplicate P3/P4 request or authorization logic; or
- infer field masking by inspecting arbitrary policy method bodies.

Policy methods can branch on actors and call shared helpers, so AST analysis
cannot soundly determine which fields may be conditionally masked. P1 marks a
model as policy-bearing. Until an explicit declarative refinement is accepted,
P5 treats every visible scalar or enum on a policy-bearing model as potentially
maskable and therefore nullable. This is conservative and preserves the Bible's
no-null-propagation security requirement. P5 may later narrow nullability only
from explicit generated metadata with a specified fail-closed contract, never
from best-effort policy-body analysis.

GraphQL metadata goldens in P1 mean this transport-neutral projection. SDL and
resolver goldens belong to P5.

---

## 11. Deterministic generation

### 11.1 Canonical ordering

Canonical generation uses these rules:

- schema membership: stable model ID;
- model fields: declaration order where struct/write/scan order matters, with a
  separate stable-ID index for lookup;
- primary, unique, index, and relation members: declared order;
- otherwise unordered descriptor/binding sets: typed ID, operation ordinal, then
  phase ordinal;
- packages: canonical import path;
- imports: `go/format`/standard Go import grouping from a deterministic import
  set; and
- diagnostics: package path, module-relative slash path, byte offset, model ID,
  field ID, then diagnostic code.

Map iteration is never observable. Tests must deliberately randomize map
insertion and source file enumeration.

Equivalent whitespace, comments, source-file order, registration order, and
unordered tag-attribute order do not change output. Order changes for compound
keys, indexes, relations, and any future ordered hook/middleware list do change
output.

### 11.2 Diagnostics

Diagnostics have stable codes and structured fields:

```text
P1REL004 internal/social/models.go:31: relation Post.Author references 2 fields but declares 1 local field
```

They never include absolute paths, temporary directories, nondeterministic type
printer addresses, SQL driver errors without normalization, or map-order lists.
The compiler accumulates independent diagnostics when safe, sorts them, and
returns them together. Cascading errors caused by one missing symbol are
suppressed by dependency, not by string matching.

### 11.3 Publication and stale files

There is no portable atomic rename for an arbitrary set of files in different
directories. P1 therefore promises a crash-recoverable publication protocol,
not fictional filesystem transactionality:

1. acquire a module-local generation lock;
2. recover or roll back any prior journal;
3. compute and validate the complete prospective artifact set;
4. write temporary sibling files and fsync them where supported;
5. write a journal containing old/new manifest digests, targets, temporary
   paths, and backup paths;
6. rename old manifest-owned files to backups and temporary files to targets;
7. verify installed file hashes;
8. install the manifest last;
9. remove backups and the journal.

On process interruption, the next `generate` or `check` completes or rolls back
the journal before doing other work. Every generated Go file embeds the same
`GenerationDigest`; mixed files fail `check` and generated application registry
validation.

The manifest records module-relative path, kind, content SHA-256,
`SchemaFingerprint`, `GenerationDigest`, generator version, and generated-header
marker. Only a path in the previous manifest with a matching generated header
may be removed as stale. Globs, prefix deletion, and directory-wide deletion are
forbidden.

Existing immutable migration files are never overwritten or deleted by ordinary
generation. If an initial migration path already exists with different content,
generation fails and directs the author to the explicit migration-history
workflow.

---

## 12. Integration gates

### Gate 0: ABI and grammar

- `DefineSchema` declaration shell compiles without generated files.
- valid generated type/value naming is accepted.
- schema name, stable-ID algorithm, tag grammar, actor constraints, reserved
  names, operator type shell, and hook signature table are frozen.
- the P1/P2/P4/P5 ownership boundaries in this document are accepted.

### Gate 1: source front end

- the two-model social fixture yields source-positioned unresolved IR;
- root selection resolves exactly one package and function;
- no application code executes; and
- non-generated source errors are not hidden by bootstrap tolerance.

### Gate 2: canonical resolved IR

- scalar, nullable, enum, compound key/index, to-one, to-many, self, and dual
  named relation fixtures resolve independently of file/walk order;
- invalid keys, relations, defaults, and exposure modes fail deterministically;
- resolved IR imports neither provider nor GraphQL types.

### Gate 3: clean-checkout bootstrap

- a fixture whose real policies use generated handles succeeds with no generated
  files on disk;
- real hooks use `PostCreateRequest` and typed field helpers;
- the overlay and final same-package emitter are identical; and
- all model packages type-check without an import cycle.

### Gate 4: typed binding discovery

- policy receiver, model parameter, and actor are validated by type identity;
- one actor type is enforced across packages;
- shared policy helpers remain ordinary typed Go;
- malformed recognized methods fail with stable diagnostics;
- bridges create fresh builders and binding accessors have no global registration
  side effect; and
- hook entries are deterministically keyed and directly callable.

### Gate 5: determinism and fingerprints

- repeated generation is byte-identical;
- shuffled file enumeration and map insertion are byte-identical;
- cosmetic source changes do not change either digest;
- exposure/binding changes change `GenerationDigest` only;
- storage changes change both digests; and
- every installed generated artifact reports the same expected digests.

### Gate 6: provider artifacts

- both provider renderers consume one provider-neutral DDL plan;
- initial artifacts apply to empty SQLite and PostgreSQL databases;
- live introspection agrees with physical IR and `SchemaFingerprint`; and
- unsupported provider-specific constructs fail before publication.

### Gate 7: publication safety

- an induced validation failure modifies nothing;
- interruption at each journal step is recoverable;
- concurrent generation is refused by the lock;
- stale removal touches only prior manifest-owned generated files; and
- immutable migration mismatch is never overwritten.

### Gate 8: P1 acceptance

- model, descriptor, transport-metadata, and both-provider goldens pass;
- all invalid fixtures below emit their pinned diagnostic codes;
- composite identity is present in every P1 descriptor/metadata projection;
- mismatch and stale-generator checks pass; and
- the exact first slice from Phase 0 `STATUS.md` migrates and verifies on both
  providers.

---

## 13. Fixture matrix

### 13.1 Required valid fixtures

1. Minimal social slice: two models, supported scalars, nullable field, ordered
   compound index, to-one and to-many relation.
2. Names: distinct Go, logical, GraphQL, table, and column names.
3. Exact types: signed/unsigned large integer declaration, exact decimal codec,
   time, bytes, enum, and JSON capability metadata.
4. Composite identity: ordered composite primary key, composite unique, composite
   foreign-key mapping, and generated selector metadata.
5. Relations: forward cross-file relation, self relation, nullable to-one, and
   two named relations between the same model pair.
6. Policy: generated handles, relation traversal, a shared helper, and all four
   rule actions using the one actor.
7. Hooks: at least one before, transaction-local after, and after-commit method
   using valid generated aliases.
8. Multiple model packages with one import direction and one actor type.
9. Determinism: semantically equivalent declaration/registration/file orders.
10. Identifier collision controls with explicit non-colliding logical names.

### 13.2 Required invalid fixtures

Schema-root fixtures:

- missing, duplicate, wrong-signature, generic, or method-valued root;
- missing/duplicate/nonliteral/invalid schema name;
- missing or multiple actor calls;
- no model, duplicate model, unnamed or non-struct model;
- loop, conditional, helper, closure, assignment, return, or other executable
  grammar in the root;
- dot-imported declaration API;
- model package claimed by competing schemas.

Model/tag/type fixtures:

- unknown, duplicate, empty, or malformed attributes;
- raw SQL or provider-specific metadata presented as portable;
- unsupported or ambiguous Go type;
- unmarked slice or invalid pointer/list/null wrapper;
- relation field without `db:"-"` or physical scalar field with it;
- incompatible default/value kind and invalid default capability;
- missing/duplicate primary key;
- unknown/repeated key or index member and duplicate physical name;
- reordered/mismatched composite relation arity or scalar kind;
- unknown relation target, ambiguous inverse, and invalid self relation;
- conflicting exposure modes, hidden/write-only identity, write-only relation,
  unknown configured field, and empty required generated input shape;
- GraphQL/logical/generated Go reserved-name collision; and
- handwritten collision with `Posts`, `PostCreateRequest`,
  `GolemGeneratedBindings`, or another predicted generated symbol.

Policy/binding fixtures:

- pointer receiver, wrong receiver, generic method, wrong method result;
- wrong `Rules` model, non-pointer `Rules`, wrong actor, alias that is not type
  identical, variadic parameter, and extra parameter;
- mixed actor types across model packages;
- exposed caller model with no policy binding; and
- an unrelated method that must not be mistaken for policy.

Hook fixtures:

- every recognized name with wrong receiver, context package, request alias,
  result alias, result count, variadic flag, or error result;
- forbidden upsert and aggregate hook names;
- pointer receiver and stateful receiver assumption;
- a request/field model mismatch that fails Go compilation; and
- a request/value type mismatch that fails Go compilation.

Generation/publication fixtures:

- nondeterministic input enumeration;
- absolute-path and temporary-directory independence;
- stale generated file with matching manifest/header;
- same path without generated header, which must be preserved and cause an
  error;
- prior interrupted journals at every publication step;
- existing immutable migration with different content; and
- mixed fingerprint generated package rejected by `check`.

---

## 14. Truly unresolved owner choices

Only these choices remain for named owners; none may be decided independently by
parallel implementation branches.

### 14.1 Operator handle decomposition — P2 owner, before Gate 0 closes

P1 needs compile-time method availability by value kind, but the final accepted
operator registry determines whether this is implemented as several public
handle types, generated wrapper types, or constrained helper functions. The
choice must preserve `Predicate[M]` ownership and must make invalid operators a
compile-time absence where Go permits it.

### 14.2 Full operation request/result payloads — P3/P4 owners

This contract freezes valid alias names and hook method signatures. P3/P4 still
own the fields and methods exposed by opaque request/result shells. Adding data
is compatible; changing the aliases or hook signature table requires this ABI
document to change.

### 14.3 Declarative conditional-mask refinement — P5 owner

P1 deliberately rejects AST inference. P5 may accept a future explicit
declaration that narrows conservative GraphQL nullability. Until then, all
visible scalar/enum fields on a policy-bearing model are treated as potentially
maskable.

### 14.4 Migration-history command set — migration owner

P1 fixes deterministic initial artifacts, schema verification, and immutable
file refusal. The commands for reviewed diffs, renames, destructive-change
approval, and production migration application require a separate migration
contract. Ordinary `generate` must not invent them.

---

## 15. Implementation sequencing

After Gate 0, work can fan out from the resolved IR contract:

```text
Wave A
  source/root loader
  raw/resolved IR + validation
  descriptor runtime/emitter against hand-built IR fixtures
  canonical fingerprints/manifest
  provider-neutral DDL plan

Wave B
  bootstrap overlay integration
  typed binding discovery
  SQLite renderer/verifier
  PostgreSQL renderer/verifier
  GraphQL-facing metadata projection

Wave C
  CLI/publication orchestration
  cross-package generated application registry
  determinism, invalid-fixture, crash-recovery, and live-provider gates
```

One owner controls IR and public ABI changes. Provider renderers, GraphQL-facing
metadata, and descriptor emitters may not add private semantic fields to shadow
missing IR. If an emitter needs a new fact, it changes the canonical IR contract
and all affected goldens in one review.

P1 is complete only at Gate 8. Interfaces or generated files existing in
isolation are not completion evidence.
