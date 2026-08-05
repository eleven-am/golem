# P1 controlling contract

Status: **accepted integration contract**<br>
Scope: model declaration, compiler ABI, logical and physical schema IR,
generation, migrations, and P1 verification<br>
Authority: subordinate only to [`../BIBLE.md`](../BIBLE.md)

This document reconciles the three independently authored Wave 0 contracts:

- [`01-schema-authoring-and-logical-ir.md`](./01-schema-authoring-and-logical-ir.md);
- [`02-provider-schema-and-migrations.md`](./02-provider-schema-and-migrations.md);
- [`03-compiler-codegen-abi.md`](./03-compiler-codegen-abi.md).

Those documents remain the detailed specifications for their subjects. This
contract resolves their disagreements. If a supporting P1 document conflicts
with this file, this file wins. If this file conflicts with the Bible, the Bible
wins.

---

## 1. P1 outcome

P1 is complete when one clean command can:

1. statically select an application schema and actor;
2. parse complete SQL model declarations from Go source;
3. resolve them into one deterministic versioned logical model IR;
4. generate same-package typed model/field/relation descriptors;
5. validate model-attached schema, policy, and hook method signatures through an
   in-memory generated overlay;
6. lower the same logical IR into normalized SQLite and PostgreSQL physical
   schemas;
7. plan deterministic initial and incremental migrations;
8. render, apply, introspect, and verify both providers;
9. publish generated artifacts without leaving a mixed version; and
10. detect stale generated code, rewritten migrations, or live schema drift.

P1 does not implement CRUD, authorization semantics, GraphQL SDL/resolvers,
mutation execution, subscriptions, or runtime auto-migration.

---

## 2. Accepted authoring root

The accepted root is the closed static function from the compiler ABI contract,
not the alternative schema-root struct proposal:

```go
package social

import "github.com/eleven-am/golem/go/golem"

func DefineSchema(schema *golem.Schema) {
	// Stable logical identity, not a physical PostgreSQL namespace.
	golem.SchemaName(schema, "social")

	// Exactly one named actor type.
	golem.Actor[Actor](schema)

	// Registration order is not semantic.
	golem.Model[User](schema)
	golem.Model[Post](schema)
	golem.Model[Comment](schema)
	golem.Model[Tag](schema)
	golem.Model[PostTag](schema)

	// Portable is the default. Restriction must be explicit.
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
```

The compiler recognizes the function statically and never executes it. The body
permits only direct registered declaration calls with literals, named types, and
registered typed constants. It rejects helper calls, assignments, control flow,
closures, environment reads, reflection, and arbitrary computation.

CLI selection is:

```text
golem generate --schema <package-pattern> --root DefineSchema ...
golem check    --schema <package-pattern> --root DefineSchema ...
```

`--root` defaults to `DefineSchema`. The selected package must contain exactly
one matching function.

### 2.1 Provider targets

Golem releases MUST implement and prove SQLite and PostgreSQL equally. An
application schema defaults to both providers.

An application MAY explicitly restrict itself to one provider only when it uses
a typed semantic provider extension that cannot be represented on the other.
The restriction is embedded in generated artifacts and startup refuses another
provider before traffic or migration work. Provider-specific SQL strings are
never accepted as metadata.

Physical-only tuning, such as an extra PostgreSQL access-path index, MAY be scoped
to one provider without making the application's logical data semantics
provider-specific.

---

## 3. Model declaration layers

P1 accepts three declaration layers:

1. Go field types state the application value representation.
2. `db` and `golem` tags state common column, model, relation, and exposure facts.
3. statically interpreted typed model methods state advanced indexes,
   constraints, generated expressions, relation actions, and provider extensions.

Tags never contain raw SQL.

```go
type Post struct {
	_ struct{} `golem:"model;id=social.Post;table=posts;graphql=Post"`

	ID        golem.UUID `db:"id" golem:"id=social.Post.ID;pk;default=uuid"`
	AuthorID  golem.UUID `db:"author_id" golem:"id=social.Post.AuthorID"`
	Slug      string     `db:"slug" golem:"type=varchar(120)"`
	Metadata  golem.JSON[PostMetadata] `db:"metadata" golem:"type=json"`
	CreatedAt time.Time  `db:"created_at" golem:"default=now;readonly"`

	Author *User `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
}
```

Advanced model schema uses the accepted two-pass handle bootstrap:

```go
func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel[Post](
		golem.Unique("uq_posts_author_slug", Posts.AuthorID, Posts.Slug),
		golem.Index[Post]("idx_posts_author_created").Keys(
			golem.IndexColumn(Posts.AuthorID),
			golem.IndexColumn(Posts.CreatedAt).Desc(),
		),
		golem.RelationOptions(Posts.Author).
			OnUpdate(golem.Cascade).
			OnDelete(golem.Restrict),
	)
}
```

The compiler statically interprets the closed constructor tree. It does not call
`GolemModel`.

---

## 4. Accepted IR pipeline

P1 uses four distinct semantic representations:

```text
RawDeclIR
    source-located, unresolved declarations
        ↓
ModelIR + ContractIR
    normalized provider-neutral persistence and Golem metadata
        ↓
PhysicalSchema[provider]
    normalized semantic database objects, no DDL strings
        ↓
MigrationPlan[provider]
    typed dependency-ordered operations, no handwritten SQL
```

Downstream code consumes only normalized IR. Provider implementations and code
generators MUST NOT reread tags or derive relation/type facts independently.

### 4.1 Persistence versus contract metadata

`ModelIR` owns facts that affect persisted logical schema:

- model, enum, field, relation, key, index, and constraint identities;
- logical SQL types and parameters;
- nullability, defaults, generated values, and updated behavior;
- ordered primary/unique/index/foreign-key components;
- physical table/column bindings; and
- typed provider requirements/extensions.

`ContractIR` owns facts that do not independently alter DDL:

- Go and GraphQL names;
- exposure modes and operation declarations;
- policy/hook binding inventory;
- API/resource limits; and
- GraphQL-facing scalar/identity/relation metadata.

P1 emits transport-neutral GraphQL metadata only. P5 creates SDL and resolvers.

---

## 5. Stable identities and renames

Stable identity is not a slice ordinal and is not a physical name.

Default stable identity input is domain-separated from:

```text
schema stable name
object kind
owning stable ID
qualified Go declaration identity or declared logical name
```

The compiler retains the canonical identity string in snapshots and generates a
typed 128-bit descriptor ID from its SHA-256 digest, with full and truncated
collision checks.

Authors SHOULD declare explicit durable IDs on models and fields that appear in
events, public identities, or long-lived migrations:

```go
`golem:"model;id=social.Post;table=posts"`
`golem:"id=social.Post.AuthorID"`
```

Rules:

- physical table/column/index/constraint renames do not change logical identity;
- a Go/logical rename changes default identity unless an explicit durable ID or
  reviewed `renameFrom` transfers the previous snapshot identity;
- `renameFrom` must resolve exactly one object of the same kind in the prior
  reviewed snapshot;
- the migration planner never infers a rename from spelling similarity;
- snapshots preserve transferred IDs after the one-time rename declaration is
  removed; and
- events and descriptors serialize stable IDs, never positional ordinals.

---

## 6. Accepted logical SQL types

The portable baseline includes:

- `Bool`;
- signed `Int16`, `Int32`, and `Int64`;
- `Float32` and `Float64`, with non-finite values refused until separately
  accepted;
- exact `Decimal(p,s)` with `1 <= p <= 18` and `0 <= s <= p`;
- `String` with optional maximum rune length;
- `Bytes` with optional maximum length;
- `UUID`;
- `Date`, `Time(p)`, and UTC `DateTime` with precision up to microseconds;
- closed string-backed `Enum`;
- canonical `JSON`; and
- explicit `ScalarList(T)` using the accepted JSON-array capability.

Bare Go `int`, unsigned integers, arbitrary slices/maps/interfaces, and unknown
nullable wrappers are rejected in v1. `[]byte` is the built-in slice exception;
relations and scalar lists require explicit declarations.

### 6.1 Portable Decimal decision

Portable Decimal is accepted as:

- SQLite: signed scaled `INTEGER` coefficient;
- PostgreSQL: `numeric(p,s)`; and
- Go: exact `golem.Decimal` codec.

The precision ceiling is 18 so every valid coefficient fits signed 64-bit SQLite
storage. Wider PostgreSQL `numeric` is an explicit PostgreSQL-only extension.
SQLite `NUMERIC` and `REAL` MUST NOT masquerade as exact Decimal storage.

### 6.2 Canonical JSON decision

JSON parsing rejects duplicate object keys and never passes numbers through
`float64`. The owned codec represents numbers as arbitrary-precision signed
decimal coefficient plus base-10 exponent and emits one normalized decimal
lexeme. Object keys are UTF-8 strings sorted by code-point order for canonical
storage; arrays preserve order. Invalid UTF-8 and non-JSON numeric values are
rejected.

SQLite stores canonical UTF-8 JSON text and requires JSON1 validation.
PostgreSQL stores `jsonb`; decoding and agreement tests compare the logical
canonical form rather than provider textual formatting. P2 separately gates JSON
operators whose provider numeric behavior cannot satisfy agreement.

---

## 7. Defaults and generated values

Default ownership is accepted as:

| Default | Owner | Physical default |
|---|---|---|
| typed literal | database | provider-rendered literal |
| `identity` | database | accepted identity/autoincrement mechanism |
| `uuid` | Golem mutation planner | none |
| `now` | Golem mutation planner | none |
| `updated` | Golem mutation planner | none |
| typed provider default | declared provider | registered provider expression |

Runtime-owned values are normalized before SQL and therefore have one exact
meaning on both providers. P4 owns injection and retry behavior. Adding a
required runtime-defaulted column to an existing table still requires an
explicit migration backfill; the runtime default does not rewrite history.

Raw default SQL is forbidden.

---

## 8. Keys, indexes, constraints, and relations

- Primary keys and non-null unique constraints create typed identity selectors.
- Composite component order is semantic and preserved everywhere.
- Unique indexes are access paths and do not automatically create public unique
  selectors, especially when partial or expression-based.
- Common indexes are portable ascending B-tree column indexes.
- Advanced portable indexes may use typed deterministic expression keys,
  direction, and typed partial predicates.
- Included columns, provider methods, operator classes, and provider collations
  are explicit provider extensions.
- Check and generated-column expressions use a closed schema-expression AST,
  not the authorization predicate language and not raw SQL.
- A `belongs_to` relation owns one physical foreign key; inverse relation fields
  do not create duplicate constraints.
- Foreign-key local/reference arity, order, types, nullability, uniqueness, and
  referential actions are validated globally.
- Default update/delete action is explicit `NO ACTION`.
- Implicit many-to-many generation is refused in the first P1 slice; authors use
  an explicit join model with a composite identity.

---

## 9. Provider physical schema and capability policy

`PhysicalSchema` is semantic and provider-specific but contains no rendered DDL.
It records provider/version/capabilities, namespace, tables, columns, physical
storage types, defaults, generated expressions, constraints, indexes, extensions,
and Golem system objects.

Portable declarations must lower, migrate, introspect, and verify on SQLite and
PostgreSQL. A capability is not supported merely because a renderer can emit
syntax; it needs lowering, diff, execution, introspection, fingerprinting, and
live tests.

Provider version floors and driver selections are implementation manifest facts,
not permission for a worker to assume a feature. Until the manifest pins them,
provider code must use explicit capability probes and fixtures. Updating a floor
requires both-provider CI and this contract's capability matrix to remain true.

---

## 10. Migration workflow

P1 includes initial and incremental migration planning.

Commands are separated deliberately:

```text
golem generate ...
    regenerate Go/descriptors/metadata against the current reviewed schema head;
    never create, overwrite, or delete migration history

golem migration new --name <name> ...
    diff the prior reviewed ModelIR/PhysicalSchema snapshots against current IR;
    require explicit rename and destructive approvals;
    write new immutable provider migrations and manifest entry

golem migration apply ...
    explicit operational command; never application startup behavior

golem check ...
    read-only verification of generated outputs, migration checksums, snapshots,
    and configured live schema when requested
```

Migration planning uses a typed dependency DAG. Renderers decide how to execute
an accepted operation, not what the operation means.

Destructive or potentially lossy operations require exact object-scoped approval
recorded in the new migration manifest. A global force flag is forbidden.

Manual provider SQL, when unavoidable, lives in a separately reviewed companion
file. It is provider-scoped and checksummed, and its manifest entry declares the
expected normalized `PhysicalSchema` postcondition. It is not a tag or schema DSL
escape hatch.

SQLite table rebuild and PostgreSQL nontransactional phases follow the detailed
provider contract. Migration failure MUST NOT advance the ledger or final
fingerprint prematurely.

---

## 11. Fingerprints and artifact consistency

P1 uses distinct domain-separated digests:

1. `ModelFingerprint`: canonical versioned persisted logical `ModelIR`;
2. `PhysicalFingerprint[provider]`: canonical normalized provider physical
   schema;
3. `ContractFingerprint`: exposure, names, limits, and typed binding inventory;
4. `GenerationDigest`: model + contract fingerprints, generator/template/ABI
   versions, and generated artifact inventory; and
5. `MigrationChainHash[provider]`: ordered immutable migration IDs, file
   checksums, and before/after physical fingerprints.

Changing GraphQL exposure regenerates contract artifacts without pretending the
database changed. Different providers legitimately have different physical
fingerprints. Matching ledger state does not excuse physical drift, and matching
physical state does not excuse rewritten history.

Golem system tables are fingerprinted in a separate versioned system-schema
domain and cannot collide with application objects.

---

## 12. Compiler bootstrap and generated Go ABI

Generation is one command from a clean checkout:

1. syntax-load schema root, models, tags, enums, and method declarations;
2. produce and globally resolve preliminary IR without generated identifiers;
3. emit same-package descriptor and ABI shells into an in-memory
   `go/packages` overlay;
4. reload and fully type-check all participating packages;
5. use `go/types` method sets to validate advanced schema, policy, enum, and hook
   methods;
6. statically interpret closed schema declaration bodies;
7. freeze final model/contract/physical/migration artifacts;
8. format and compile-check the complete prospective output set; and
9. publish through the manifest-owned crash-recoverable journal protocol.

Generated value namespaces are valid:

```go
Posts.ID
Posts.AuthorID
Posts.Author
```

Generated types are package-level because Go values cannot contain types:

```go
PostDescriptor
PostCreateInput
PostCreateRequest
PostCreateResult
```

`Posts.CreateInput` and `Posts.CreateRequest` are forbidden spellings.

Relations store stable IDs and resolve through an immutable registry; generated
global values do not contain cyclic Go pointers.

---

## 13. P1/P2/P3/P4/P5 ABI seams

P1 freezes only the type-level shells required to compile and discover attached
methods.

- P2 owns final predicate/operator behavior and may refine handle decomposition
  before descriptor Gate 3, but may not remove typed model ownership or introduce
  string field identities.
- P3/P4 own the fields and semantics of operation request/result shells. P1 owns
  their valid package-level names and recognized hook signature table.
- P5 owns GraphQL SDL/resolvers. P1 emits transport-neutral metadata only.
- P1 never attempts to infer conditional masks by analyzing policy bodies. Until
  explicit declarative maskability exists, P5 treats visible scalar/enum fields
  on policy-bearing models as potentially nullable.

These ABI seams do not block Wave 1 source loading, IR, type/default resolution,
or test-harness work. They must freeze before typed descriptor/binding generation.

---

## 14. Publication and diagnostics

- Generation is deterministic under shuffled file, declaration, and map order.
- Diagnostics contain stable codes and module-relative source positions and are
  sorted canonically.
- Absolute paths, timestamps, environment values, temporary paths, and host
  details never enter semantic output.
- The prospective artifact set is validated completely before publication.
- A journaled sibling-temp/backup protocol recovers from multi-directory crash;
  P1 does not claim an impossible cross-filesystem atomic rename.
- Only prior manifest-owned files with the generated header may be removed as
  stale. Globs and directory-wide deletion are forbidden.
- Existing immutable migration files are never overwritten by `generate`.

---

## 15. Wave 1 implementation boundary

Wave 1 may begin against this accepted contract with three non-overlapping work
packages:

### W1-A — source and declaration front end

Owns schema-root discovery, closed grammar validation, Go package loading,
source-positioned `RawDeclIR`, and common `db`/`golem` lexical parsing.

It does not resolve types, relations, providers, or emit code.

### W1-B — logical type/default registry and normalized IR substrate

Owns versioned Raw/Model/Contract IR Go types, stable typed IDs, canonical
encoding, scalar/enum/nullability/default validation, and hand-authored fixture
IRs.

It does not load Go packages, render SQL, or generate application code.

### W1-C — deterministic fixture and provider test harness

Owns golden comparison, randomized traversal/determinism helpers, temporary
SQLite lifecycle, PostgreSQL service/container adapter boundary, canonical
fixture schemas, and shared apply/introspect/compare interfaces implemented first
with fakes.

It does not define IR semantics or provider DDL.

The Wave 1 integration gate is one social schema producing deterministic
source-located raw declarations and normalized scalar/default IR, with valid and
invalid fixtures and no provider or codegen inference outside the shared
contracts.

---

## 16. P1 final acceptance

The complete phase must satisfy the detailed acceptance matrices in all three
supporting contracts. At minimum:

- valid and invalid schema/tag/type/key/index/relation/exposure fixtures;
- clean-checkout one-command generated-handle bootstrap;
- exact composite identities across descriptors and provider schemas;
- byte-identical repeated and shuffled generation;
- deterministic initial and multi-step migrations;
- live blank and incremental migration on SQLite and PostgreSQL;
- introspection round-trip and individual drift mutation refusal;
- immutable history and stale generated artifact refusal;
- safe SQLite rebuild and PostgreSQL phased DDL failure recovery; and
- no CRUD, authorization SQL, GraphQL runtime, auto-migration, MySQL, or raw
  caller SQL accidentally entering P1.
