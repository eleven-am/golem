# P1 provider schema and migration contract

Status: **Wave 0 proposed normative contract**<br>
Scope: logical-to-physical schema lowering, migrations, introspection, and drift
verification for SQLite and PostgreSQL<br>
Authority: subordinate to [`../BIBLE.md`](../BIBLE.md)

This document defines one shared schema contract with two complete provider
implementations. It does not define authorization SQL, CRUD, or GraphQL. Those
later phases consume the descriptors and capabilities fixed here.

The governing pipeline is:

```text
validated Model IR v1
        │
        ├── capability validation
        ├── lower(SQLite) ──────┐
        └── lower(PostgreSQL) ──┤
                                ▼
                  normalized PhysicalSchema
                    │                   │
           previous reviewed         live database
             schema snapshot             │
                    │                    ▼
                    ▼               introspection
              deterministic diff         │
                    │                    ▼
                    ▼           normalized PhysicalSchema
             MigrationPlan DAG            │
                │         │                ▼
         SQLite SQL   PostgreSQL SQL   fingerprint compare
                └─────────┬───────────────┘
                          ▼
             immutable migration manifest
```

Generation MUST diff reviewed schema snapshots, never a developer's live
database. Introspection verifies deployed state; it is not a schema-authoring
mechanism. Application startup MUST NOT auto-migrate.

## 1. Required decisions and terminology

### 1.1 Schema representations

P1 uses three distinct representations:

- **Model IR** is the versioned, provider-neutral desired logical model compiled
  from Go source.
- **PhysicalSchema** is a normalized semantic description of one provider's
  database objects. It contains no rendered DDL.
- **MigrationPlan** is a typed operation DAG from one PhysicalSchema to another.
  It contains no handwritten SQL.

Renderers consume MigrationPlan. Introspectors produce PhysicalSchema. The same
canonical encoder fingerprints both generated and introspected PhysicalSchema.

### 1.2 Portable and provider-extension declarations

A declaration is one of:

1. `portable`: its semantics, migration, introspection, and later runtime codecs
   are required on SQLite and PostgreSQL;
2. `sqlite`: an explicit SQLite extension;
3. `postgresql`: an explicit PostgreSQL extension.

The default is `portable`. A provider extension MUST be explicit in Model IR and
MUST cause generation for the other provider to fail naming the model, field or
index, capability, and selected provider. Provider-specific SQL text is never a
valid tag value.

An application that claims portability MUST compile every model using the
intersection of the two capability sets. Equal first-class providers means equal
quality and proof, not that every provider-native feature is made portable.

### 1.3 Version floor

The provider contract MUST record a tested minimum server/library version and
the runtime MUST verify it before migration or schema verification. Physical
capabilities are derived from `(provider, version, compile options)`, not merely
the provider name.

The exact version floors are an owner decision in section 15. Until they are
ratified, no implementation may silently assume features such as SQLite JSON1,
generated columns, `DROP COLUMN`, or PostgreSQL concurrent indexes.

## 2. Namespaces and identifiers

### 2.1 Portable namespace rule

Portable model declarations do not carry a database namespace.

- SQLite lowers the application namespace to `main` and MUST reject attached
  database references in generated schema objects.
- PostgreSQL lowers it to one application-configured schema. The recommended
  default is `public`, but the selected value is embedded in PhysicalSchema.

PostgreSQL renderers MUST fully qualify application tables, enum types, migration
tables, and sequences. They MUST NOT depend on `search_path`. SQLite renderers
MUST use the normalized unqualified object name in `main`.

Per-model PostgreSQL schemas and SQLite attached databases are explicit provider
extensions and are outside portable P1.

### 2.2 Identifier validation

Portable physical identifiers MUST:

- match `[a-z][a-z0-9_]*`;
- be at most 63 UTF-8 bytes;
- be unique under ASCII case folding within their namespace and object kind;
- not use the reserved `_golem_` prefix; and
- be rendered with provider quoting even when not reserved words.

Logical and GraphQL names have their own validation and are not physical names.
Explicit `db` names are never silently normalized. An invalid explicit name is a
generation error.

Generated constraint, temporary-table, and index names use a deterministic
human-readable prefix plus a lowercase digest suffix. The generator MUST fail
rather than silently truncate an explicit author-supplied name. Digest input
uses stable IDs and ordered components, not source positions.

### 2.3 Stable identities

Every schema object that can survive a rename carries a stable semantic ID:

```text
SchemaID, ModelID, FieldID, KeyID, ForeignKeyID, IndexID, CheckID
```

IDs are part of Model IR and the checked-in schema snapshot. They MUST NOT be
recomputed from the current logical or physical spelling during an incremental
migration. Initial generation may derive an ID once; subsequent generations
preserve it from the previous snapshot or transfer it through an explicit
`renameFrom` declaration.

## 3. Logical-to-physical scalar lowering

The table below is the portable baseline.

| Logical kind | SQLite | PostgreSQL | Portable constraints and codec |
|---|---|---|---|
| `String` | `TEXT` | `text` | UTF-8 text; portable ordering uses the separately accepted Golem collation, never a database default assumed equivalent |
| `Boolean` | `INTEGER` | `boolean` | SQLite has `CHECK (c IS NULL OR c IN (0,1))`; decoder accepts only 0/1 |
| `Int32` | `INTEGER` | `integer` | SQLite has a signed 32-bit range check; Go `int` is not accepted as a stable schema type |
| `Int64` / `BigInt` | `INTEGER` | `bigint` | Signed 64-bit exact integer |
| `Float64` | `REAL` | `double precision` | Portable writes reject NaN and infinities until cross-provider semantics are separately accepted |
| `Decimal(p,s)` | scaled `INTEGER` | `numeric(p,s)` | Portable baseline requires `1 <= p <= 18`, `0 <= s <= p`; SQLite stores the exact integer scaled by `10^s` |
| `UUID` | canonical lowercase UUID `TEXT` | `uuid` | SQLite has a generated canonical-shape check; codec validates all values |
| `DateTime` | UTC Unix microseconds `INTEGER` | `timestamptz(6)` | Codec normalizes to UTC and microsecond precision before persistence |
| `Bytes` | `BLOB` | `bytea` | Lossless byte sequence |
| `JSON` | canonical JSON `TEXT` | `jsonb` | SQLite requires JSON1 and `CHECK (c IS NULL OR json_valid(c))`; writes use Golem's canonical JSON codec |
| `Enum(E)` | `TEXT` plus membership check | `text` plus membership check | Same value spelling and ordered descriptor on both providers |
| `ScalarList(T)` | canonical JSON-array `TEXT` | `jsonb` array | Requires explicit `storage=json_array`; schema checks array shape; public operators remain gated on P2 agreement |

Nullability is represented separately and wraps every generated check so SQL
`NULL` remains allowed on nullable columns.

### 3.1 Exact Decimal

Portable Decimal uses a fixed scale. SQLite persists the scaled integer; it does
not use `NUMERIC`, `REAL`, or decimal text. This gives exact equality and ordering
without relying on SQLite affinity conversion or a process-local SQL function.

The PostgreSQL codec MUST still enforce the same precision/scale and canonical
rounding refusal as SQLite. Values with excess fractional digits or magnitude
are rejected before execution.

Precision above 18 is an explicit PostgreSQL `numeric` extension until SQLite
has an accepted arbitrary-precision storage, comparison, aggregation, migration,
and agreement implementation. It may not masquerade as portable Decimal.

### 3.2 JSON and JSONB

Logical `JSON` is one semantic kind despite different physical storage:

- input is decoded with duplicate object keys rejected;
- numbers use the exact JSON-number representation selected by the codec, never
  an intermediate `float64` requirement;
- object keys are canonicalized for persistence and fingerprint fixtures;
- insignificant whitespace and object key order are not observable guarantees;
- top-level scalar, object, array, and JSON null remain distinct from SQL NULL.

PostgreSQL uses `jsonb`, not `json`. SQLite uses canonical text plus JSON1
validation. Later JSON operators MUST pass the P2 evaluator/SQLite/PostgreSQL
agreement corpus before being exposed.

### 3.3 Enums

Portable enums are text plus a named check constraint on both providers. Native
PostgreSQL enum types are an explicit extension because they have different
evolution and transactional behavior.

Enum value order in Model IR is stable and is used by generated Go/GraphQL
artifacts. Database ordering of enum values is not portable; an order operation
must use the accepted logical ordering or be rejected later by the planner.

Removing or renaming an enum value is destructive unless a migration supplies an
explicit data transform. Adding a value rewrites the membership check and is
non-destructive.

### 3.4 Scalar lists

Portable scalar lists use a canonical JSON array on both providers. They are not
inferred from every Go slice: the model must declare `storage=json_array`.

The portable baseline permits lists of `String`, `Boolean`, `Int32`, `Int64`,
`Float64`, fixed portable `Decimal`, `UUID`, `DateTime`, and enums. Nested lists,
`Bytes`, and arbitrary `JSON` elements are refused initially. List elements are
non-null in v1; nullable elements require a separate codec/operator decision.

PostgreSQL native arrays are an explicit provider extension. They produce a
different PhysicalSchema and capability requirement. They do not automatically
enable public scalar-list filters.

## 4. Defaults and generated values

Defaults have a semantic owner:

```text
NoDefault
InputDefault(literal)
RuntimeDefault(UUID | Now)
DatabaseDefault(normalized portable literal)
ProviderDefault(provider, normalized expression)
```

### 4.1 Portable rules

- `default=uuid`, `default=now`, and `updated` are **runtime-owned** portable
  defaults. The generated mutation layer supplies them, and neither provider
  receives a volatile physical default.
- Boolean, integer, fixed Decimal, string, enum, empty-list, and JSON literal
  defaults are normalized typed literals and MAY be emitted as physical defaults
  when both providers can represent them exactly.
- A required field with a runtime default is optional in generated create input,
  but remains `NOT NULL` physically.
- System-client writes use the same generated default materialization. Raw SQL is
  outside that promise and must supply runtime-owned values itself.
- Default expressions are never copied from raw tag text.

The physical fingerprint includes physical defaults. The model fingerprint also
includes runtime-owned defaults even though they emit no DDL.

### 4.2 Incremental migration effect

Adding a required column to a possibly non-empty table is not made safe by a
runtime-owned default. It requires one of:

1. an exact physical constant default usable for backfill;
2. a typed declarative backfill step accepted by both providers; or
3. an explicit manual migration step with reviewed SQL and a declared
   postcondition PhysicalSchema.

Without one, generation refuses the migration. It MUST NOT assume the table is
empty.

Provider-specific defaults require an explicit provider extension and a
normalized expression kind known to that provider. Arbitrary SQL expressions
are never fingerprinted as if they were portable.

## 5. Keys, foreign keys, checks, and generated columns

### 5.1 Keys

- Primary and unique keys preserve declared field order.
- Key fields must use scalar storage with equality semantics accepted for the
  selected provider.
- Primary-key columns are non-null.
- Portable unique constraints follow the shared SQLite/PostgreSQL behavior that
  multiple rows may contain NULL in a nullable unique key.
- Identity and unique constraints are distinct from ordinary indexes in
  PhysicalSchema even where a provider implements them with an index.

### 5.2 Foreign keys

Foreign keys preserve ordered local and referenced columns. Arity and physical
types must match after lowering. The referenced columns must form a primary or
unique key.

Portable actions are:

```text
NoAction (default), Restrict, Cascade, SetNull
```

`SetNull` requires every affected local field to be nullable. `SetDefault` is
deferred until default/action agreement is proven. Actions are explicit for both
`ON UPDATE` and `ON DELETE` in rendered DDL.

Portable deferrability is:

```text
NotDeferrable (default)
DeferrableInitiallyImmediate
DeferrableInitiallyDeferred
```

Both providers must enable and verify foreign-key enforcement. `Restrict` may
not be combined with a deferred constraint because SQLite gives RESTRICT an
immediate action even on a deferred key. Use `NoAction` for a deferred check.

### 5.3 Check constraints

Check constraints are generated from a typed, versioned schema-expression IR.
Raw SQL checks are not portable declarations. P1's required checks are those
generated for booleans, integer ranges, UUID shape, enum membership, JSON shape,
and scalar-list shape.

An author-defined portable check surface is deferred until its expression
language and null semantics are specified. PhysicalSchema nevertheless models
checks now so introspection and fingerprints cannot ignore them.

### 5.4 Generated columns

PhysicalSchema represents generated columns and whether they are stored or
virtual. Portable generated columns are not exposed in the v1 authoring surface.
When added, the first portable form MUST be `STORED` with a deterministic typed
expression supported and introspected identically by both providers.

SQLite virtual columns and newer PostgreSQL generated-column variants are
provider extensions. An introspector that cannot recover a generated expression
semantically MUST reject verification rather than omit it from the fingerprint.

## 6. Index capability contract

The portable v1 index is:

- a named ordinary or unique B-tree-equivalent index;
- over one or more physical columns in declared order;
- using the default ascending key direction;
- with no expression, predicate, included column, operator class, or provider
  collation; and
- created transactionally.

An index descriptor records stable ID, name, table, uniqueness, ordered key
columns, method, directions, null ordering, optional predicate, optional included
columns, and creation mode even when v1 accepts only the portable subset. This
prevents later features from requiring a second metadata model.

Provider extensions include:

- PostgreSQL `gin`, `gist`, `hash`, `brin`, operator classes, included columns,
  and `CREATE INDEX CONCURRENTLY`;
- SQLite partial and expression indexes after typed-expression acceptance;
- provider-specific collations; and
- full-text indexes on either provider.

Partial indexes are not portable merely because both engines have syntax. Their
predicate needs one accepted typed expression and introspection contract first.

Foreign keys are not automatically indexed. The generator SHOULD diagnose an
unindexed relation key because later read planning uses this metadata, but it
MUST NOT silently create an index that the model did not declare.

## 7. PhysicalSchema IR

The following is a semantic shape, not a required Go spelling:

```go
type PhysicalSchema struct {
    Version      uint32
    Provider     ProviderIdentity
    Namespace    Namespace
    Capabilities []CapabilityID
    Tables       []PhysicalTable
    System       SystemSchema
}

type PhysicalTable struct {
    ID          ModelID
    Name        PhysicalName
    Columns     []PhysicalColumn
    PrimaryKey *PhysicalKey
    Uniques     []PhysicalKey
    ForeignKeys []PhysicalForeignKey
    Checks      []PhysicalCheck
    Indexes     []PhysicalIndex
}

type PhysicalColumn struct {
    ID          FieldID
    Name        PhysicalName
    Storage     StorageType
    Nullable    bool
    Default     PhysicalDefault
    Generated  *GeneratedExpression
    Collation   *CollationID
}
```

All collections have explicit canonical ordering:

1. namespace and table physical name, then stable ID;
2. columns in declared ordinal order;
3. key columns in declared order;
4. constraints and indexes by stable ID;
5. capabilities by capability ID.

Source positions, comments, Go package paths used only for diagnostics, driver
versions above the declared semantic floor, statistics, row counts, ownership,
and provider-generated object IDs are excluded.

PhysicalSchema contains Golem-managed application objects and Golem system
objects. Unknown objects in the managed namespace are drift unless explicitly
listed in a reviewed unmanaged-object allowlist. The allowlist itself is
fingerprinted and cannot hide an object whose name collides with a managed one.

## 8. Migration operation algebra and DAG

MigrationPlan contains these operation families:

```text
CreateNamespace
CreateTable
RenameTable
DropTable

AddColumn
RenameColumn
AlterColumnType
AlterColumnNullability
SetColumnDefault
DropColumnDefault
DropColumn

AddPrimaryKey / DropPrimaryKey
AddUnique / DropUnique
AddForeignKey / DropForeignKey
AddCheck / DropCheck
CreateIndex / DropIndex / RenameIndex

BackfillColumn
RebuildTable             // provider-lowered compound operation
ValidateConstraint
ManualStep               // explicit reviewed provider SQL + postcondition
RecordSchemaVersion
```

Every operation records:

- stable operation ID;
- semantic before and after fragments;
- dependency IDs;
- provider capability requirements;
- transaction mode;
- destructive/risk classification;
- optional data transform;
- idempotence/resume metadata for nontransactional work; and
- diagnostic logical object path.

The diff engine emits provider-neutral semantic operations where possible.
Provider planning may replace an accepted subgraph with one compound provider
operation such as SQLite `RebuildTable`, but the before/after schema and risk
classification do not change.

### 8.1 Dependency order

The plan is a DAG. A deterministic topological sort uses `(stage, object stable
ID, operation kind, operation ID)` as its tie-breaker. Required dependencies
include:

- namespace before contained objects;
- table and columns before keys/checks/indexes;
- referenced unique key before foreign key;
- dependent foreign keys/indexes/checks removed before a destructive column or
  table change;
- data backfill before `NOT NULL` or validating a new constraint;
- replacement object valid before old object removal where the provider permits;
- migration ledger update last.

Cycle handling is provider planning, not nondeterministic sorting. Initial
PostgreSQL creation may create tables first and add cyclic foreign keys later.
Initial SQLite creation may render foreign keys in table definitions while
foreign-key enforcement is disabled for the controlled migration and validated
before commit.

## 9. Initial and incremental migrations

### 9.1 Initial migration

The initial plan is a diff from the provider's canonical empty managed schema to
the desired PhysicalSchema. It includes migration system tables. It MUST be
reproducible byte-for-byte from the same Model IR, provider identity, generator
version, and accepted capability set.

An initial migration MUST apply successfully to a truly empty provider database,
then introspect to the expected physical fingerprint.

### 9.2 Incremental migration

Incremental generation consumes:

1. the previous immutable manifest;
2. its previous expected PhysicalSchema snapshot;
3. current Model IR; and
4. the selected provider capability set.

It does not consume a live database. Before applying, the runner proves that the
live ledger and introspected schema match the migration's declared starting
state.

Safe additive and reversible changes may be generated automatically. A change
requiring application-specific data meaning becomes `ManualStep` or is refused;
the generator never invents a cast, fill value, enum mapping, or row transform.

### 9.3 `renameFrom`

Renames are never inferred from spelling similarity.

- A model or field may declare exactly one `renameFrom` referring to an object in
  the immediately previous reviewed snapshot.
- The reference is resolved in logical-object scope and transfers the stable ID.
- It must be unambiguous, must match object kind, and may not target an object
  also claimed by another declaration.
- Chained historical aliases are unnecessary; immutable history records earlier
  names.
- Changing only a logical name while retaining the same physical name emits no
  physical rename.
- Changing a physical name with transferred identity emits `RenameTable` or
  `RenameColumn` when supported, otherwise an equivalent provider plan.

Removing `renameFrom` after its migration is generated does not rewrite history;
the new snapshot already owns the transferred ID.

### 9.4 Destructive approval

Operations are classified:

```text
Safe
Locking
Rewrite
DataLoss
Manual
```

Generation refuses `DataLoss` or `Manual` plans unless the author passes an
explicit destructive-generation approval. Approval is recorded in the manifest
with the operation IDs and exact before/after digests. A global flag is not a
runtime bypass: changing the plan invalidates the recorded approval.

Examples requiring approval include dropping a table/column, narrowing a type,
reducing Decimal precision/scale, removing enum values, adding a uniqueness or
check constraint to existing data without validated preconditions, and replacing
an unrecognized cast.

The migration runner still applies reviewed destructive migrations without an
interactive prompt. Deployment safety comes from immutable review and exact
precondition verification, not a production `--force` switch.

## 10. SQLite migration semantics

SQLite provider planning emits `RebuildTable` whenever the accepted version
cannot express the semantic operation directly and safely, or when direct DDL
would lose constraint/default/generated-column fidelity.

A rebuild contains:

- complete desired temporary table definition;
- explicit old-to-new column mapping by stable FieldID;
- typed casts or backfills, if explicitly accepted;
- ordered data copy;
- removal and rename steps;
- recreation of every managed index and dependent object; and
- post-rebuild foreign-key and schema verification.

The temporary name is `_golem_tmp_<table-prefix>_<digest>` and is checked for a
collision before work starts.

The migration runner performs a rebuild as follows:

1. verify the starting ledger and physical fingerprint;
2. acquire the migration lock;
3. read and remember `PRAGMA foreign_keys`;
4. disable foreign-key enforcement outside the transaction when required;
5. begin `IMMEDIATE` so the migration does not start as an upgradeable reader;
6. create the complete temporary table;
7. copy data with an explicit column list and accepted transforms;
8. drop the old table and rename the temporary table;
9. recreate managed indexes and dependent objects;
10. run `PRAGMA foreign_key_check` and semantic introspection checks;
11. update the migration ledger and commit; and
12. restore foreign-key enforcement on every success or failure path.

No `SELECT *` data copy is permitted. A failed copy, check, or fingerprint rolls
back and does not advance the ledger.

SQLite objects outside Golem's managed schema, especially triggers and views
that reference a rebuilt table, are drift unless reviewed in the unmanaged
allowlist. A rebuild MUST NOT silently drop or rewrite them.

## 11. PostgreSQL migration semantics

Portable PostgreSQL migrations are transactional. The runner acquires a
transaction-scoped advisory migration lock, verifies the starting state, applies
the plan through one transaction, introspects required postconditions, writes the
ledger, and commits.

DDL is fully schema-qualified. Constraint and index names are explicit.
Potentially locking or table-rewrite operations retain their `Locking` or
`Rewrite` classification in the manifest so deployment tooling can refuse them
under a stricter operational policy.

### 11.1 Nontransactional operations

Operations such as `CREATE INDEX CONCURRENTLY` are explicit PostgreSQL
extensions with `AutocommitOnly` transaction mode. They are never selected as an
automatic rendering optimization.

A migration containing autocommit steps is segmented into deterministic phases.
The ledger records step completion and expected intermediate fingerprints so a
crash can resume safely. Every autocommit step must be idempotently detectable
through introspection. The final migration version is recorded only after all
steps and the final physical fingerprint succeed.

Runtime startup encountering an incomplete migration refuses with a stable
schema-state error. It does not resume migrations automatically.

Native PostgreSQL enum evolution and other DDL with version-dependent transaction
rules use the same explicit transaction-mode mechanism.

## 12. Introspection and normalization

Introspection returns PhysicalSchema, not driver-specific rows or reconstructed
Go models. It must recover every semantic property used in the physical
fingerprint. If it cannot faithfully recover a managed feature, verification
fails as unsupported rather than omitting that property.

### 12.1 Shared normalization

Normalization MUST:

- map physical objects to stable IDs from the expected snapshot where semantic
  identity matches;
- preserve column and key ordinal order;
- normalize provider type aliases into StorageType;
- normalize literal defaults into typed values;
- normalize actions and deferrability;
- separate unique constraints from author-declared indexes;
- exclude provider-generated object IDs, statistics, ownership, and comments;
- distinguish explicit names from provider-generated implicit names; and
- report missing, extra, and changed objects structurally.

Raw DDL text, whitespace, catalog output ordering, and provider pretty-printing
are never fingerprint inputs.

### 12.2 SQLite

SQLite introspection uses `sqlite_schema` plus the appropriate `PRAGMA`
interfaces, including `table_xinfo`, `foreign_key_list`, `index_list`, and
`index_xinfo`. It ignores `sqlite_*` implementation objects and normalizes
`sqlite_autoindex_*` into the key/constraint that caused them.

Recovering generated expressions, checks, collations, or defaults that are not
fully represented by PRAGMA requires a real SQLite DDL parser for the accepted
grammar. Regex extraction is forbidden. Until that parser supports a feature,
the feature is unavailable as a managed declaration.

Verification checks `PRAGMA foreign_keys = 1`, the expected SQLite version, and
required compile options such as JSON1 on the actual connection.

### 12.3 PostgreSQL

PostgreSQL introspection uses `pg_catalog`, constrained to the configured
namespace and Golem system namespace. `information_schema` alone is insufficient
for complete indexes, checks, generated expressions, deferrability, and provider
types.

PostgreSQL expression output is parsed into the accepted normalized expression
IR. It is not compared as `pg_get_expr` text. Implicit primary/unique indexes are
attached to their constraint and are not reported as extra author indexes.

Verification records the actual server version and rejects a database below the
manifest floor or missing an explicitly required extension/capability.

## 13. Fingerprints, manifest, and drift verification

### 13.1 Three independent digests

P1 defines:

1. **Model fingerprint**: hash of canonical versioned Model IR, including logical
   defaults and explicit capability requirements.
2. **Physical fingerprint**: hash of provider identity plus canonical normalized
   PhysicalSchema.
3. **Migration chain hash**: hash of ordered migration IDs, parent chain hash,
   immutable file checksums, and declared before/after physical fingerprints.

The hash algorithm and canonical encoding version are explicit manifest fields.
The recommended initial algorithm is SHA-256 over a length-delimited canonical
binary encoding. JSON map serialization is not a canonical encoding.

These digests MUST remain separate. Equivalent physical state does not prove
that reviewed migration history was not rewritten, and the two providers
legitimately have different physical fingerprints for one model fingerprint.

### 13.2 Immutable manifest

Each provider has an ordered manifest containing:

- format and canonical-encoding versions;
- generator version;
- provider identity and minimum version;
- required capabilities/extensions;
- migration ID, parent ID, and parent chain hash;
- migration file path and checksum;
- transaction phases and operation risk classifications;
- destructive approvals;
- before and after model/physical fingerprints;
- embedded before and after PhysicalSchema snapshots; and
- unmanaged-object allowlist digest.

An existing migration file or manifest entry is immutable. Regeneration that
would change its bytes fails and instructs the author to create a new migration.
Formatting SQL is therefore a history change, not a harmless rewrite.

The database ledger stores migration ID, parent chain hash, file checksum,
before/after physical fingerprints, phase status, and application time. Golem
system tables are versioned physical objects but are fingerprinted separately
from application tables so runtime upgrades cannot disguise application drift.

### 13.3 Runtime verification

Before serving application operations, startup performs read-only verification:

1. select the manifest for the configured provider;
2. verify embedded manifest and migration checksums;
3. read the database migration ledger;
4. require the exact expected ordered chain and no incomplete phase;
5. verify provider version and capabilities;
6. introspect the managed schema;
7. compare the actual physical fingerprint to the manifest head; and
8. report a structural mismatch using logical public object names where safe.

Missing introspection privileges are a verification failure. Extra tables,
columns, constraints, or indexes in the managed namespace are drift unless
explicitly allowed. Missing objects, altered defaults/checks/collations, disabled
foreign keys, and unexpected provider extensions are drift.

The error includes expected migration ID and non-sensitive mismatch categories.
It does not expose credentials or arbitrary database DDL. Startup never repairs,
migrates, or updates the ledger automatically.

## 14. Capability matrix

Legend: **P** portable baseline, **E** explicit provider extension, **N** not
accepted in P1.

| Capability | SQLite | PostgreSQL | Contract |
|---|---:|---:|---|
| String, bool, Int32, Int64, finite Float64 | P | P | Lowering and codec in section 3 |
| Fixed Decimal `p <= 18` | P | P | Scaled integer versus `numeric(p,s)` |
| Decimal precision above 18 | N | E | PostgreSQL `numeric`; no portable claim |
| UUID storage | P | P | Canonical text versus native `uuid` |
| Runtime UUID default | P | P | Generated runtime value; no physical volatile default |
| UTC microsecond DateTime | P | P | Integer epoch versus `timestamptz(6)` |
| Runtime `now`/`updated` | P | P | Generated runtime value |
| Bytes | P | P | BLOB versus bytea |
| Canonical JSON | P* | P | `*` requires JSON1; text versus jsonb |
| Portable enum | P | P | Text plus check |
| Native enum | N | E | PostgreSQL-specific type/evolution |
| JSON-array scalar list | P* | P | `*` requires JSON1; operators gated on P2 |
| Native scalar array | N | E | PostgreSQL-specific |
| Composite PK/unique/FK | P | P | Ordered components |
| FK NoAction/Restrict/Cascade/SetNull | P | P | Restrictions in section 5 |
| Deferred FK | P | P | No deferred Restrict |
| Ordinary/unique column index | P | P | Ordered default-ascending keys |
| Partial/expression index | E | E | Typed predicate/expression required |
| Included columns/operator classes | N | E | PostgreSQL-specific |
| Concurrent index creation | N | E | Autocommit phased migration |
| Full-text index | E | E | Separate provider feature, never portable by name alone |
| Generated stored column | E | E | Portable authoring deferred pending expression agreement |
| Generated virtual column | E | E | Version/provider-specific |
| Generated required checks | P | P | Typed known expressions only |
| Author-defined raw check/default | N | N | Raw SQL is not schema metadata |
| Transactional portable migration | P | P | SQLite rebuild protocol / PostgreSQL transaction |
| Per-model namespace | N | E | PostgreSQL only |

An asterisk is a runtime capability probe, not permission to downgrade silently.
If JSON1 is unavailable, a model using JSON or JSON-array lists fails before DDL
planning.

## 15. Owner decisions requiring ratification

The source documents do not determine these operational choices. The
recommendations above assume the proposed answers shown here; owners MUST ratify
or amend them before implementation freezes Model IR v1.

1. **Minimum versions and drivers.** Select the SQLite driver/build and minimum
   SQLite/PostgreSQL versions. Recommendation: choose versions with generated
   columns, reliable rename/drop support, JSON1, and current supported PostgreSQL
   catalogs, while still testing rebuild paths rather than depending on every
   direct SQLite ALTER.
2. **Portable Decimal envelope.** Ratify `p <= 18` scaled-integer SQLite storage.
   A larger portable envelope requires an accepted sortable arbitrary-precision
   encoding and later aggregate semantics.
3. **Volatile default ownership.** Ratify runtime-owned `uuid`, `now`, and
   `updated`. Database-owned volatile defaults cannot currently provide one exact
   SQLite/PostgreSQL value contract without provider-specific machinery.
4. **PostgreSQL default schema.** Ratify `public` or choose another configured
   default. The implementation will qualify it explicitly either way.
5. **Canonical JSON number codec.** Choose the exact Go representation and
   canonical binary/text encoding before JSON fingerprints and operators become
   executable.
6. **Manual migration format.** Decide whether `ManualStep` SQL lives inside the
   generated migration file or a separately reviewed companion file. In either
   case it is provider-specific, checksummed, and must declare a PhysicalSchema
   postcondition.

No other item in this document is intentionally left to provider implementers.

## 16. Dual-provider acceptance suite

P1 is complete only when all tests below execute live against both providers.

### 16.1 Golden and determinism tests

- Repeated parsing/lowering/diff/render runs are byte-identical.
- Randomized Go-file, declaration, and map traversal produces the same Model IR,
  PhysicalSchema, MigrationPlan ordering, SQL, and fingerprints.
- Every supported scalar/default/key/FK/index fixture has Model IR, SQLite
  physical, PostgreSQL physical, and migration goldens.
- Composite IDs, compound unique keys, and compound relation keys preserve order
  through every artifact.
- Every capability refusal names provider plus logical model/field/index.

### 16.2 Initial migration tests

For the same social-network fixture on SQLite and PostgreSQL:

1. create a truly empty database;
2. apply the initial migration;
3. introspect it;
4. assert expected physical fingerprint;
5. assert exact ledger and chain hash; and
6. rerun verification without changing state.

Fixtures include nullable and required fields, runtime and literal defaults,
every portable scalar, enum, JSON, scalar list, composite key, cyclic relation,
all accepted FK actions/deferrability, indexed and unindexed FKs, and exact
physical names.

### 16.3 Incremental migration tests

- Safe add table, nullable column, literal-default column, index, unique, check,
  and FK.
- Explicit model, table, field, column, and index rename through `renameFrom`.
- Rename omission produces drop/add or refusal, never inferred rename.
- Required-column add refuses without a backfill/default.
- Decimal narrowing, enum removal, column/table drop, and unsafe uniqueness
  require exact destructive approval.
- Alter nullability/default/type preserves data where accepted.
- A multi-step history migrates from every supported previous release head to
  current, not merely empty-to-current.
- PostgreSQL cyclic FKs and SQLite table rebuilds reach equivalent logical state.

### 16.4 SQLite rebuild tests

- Rebuild preserves all mapped rows exactly, including nulls, bytes, scaled
  Decimal, timestamp microseconds, canonical JSON, enum, and composite keys.
- Indexes, foreign keys, checks, defaults, and generated metadata are recreated.
- `foreign_key_check` failure rolls back and leaves the ledger unchanged.
- Copy/cast/backfill failure rolls back and restores `PRAGMA foreign_keys`.
- Concurrent access cannot turn the migration into a stale read-to-write
  upgrade; `BEGIN IMMEDIATE` behavior is asserted with separate connections.
- An unmanaged dependent trigger/view causes explicit drift/refusal.

### 16.5 PostgreSQL migration tests

- Portable migration failure rolls back schema and ledger atomically.
- Schema qualification works under a hostile `search_path`.
- Constraint/index normalization ignores implicit backing indexes but detects an
  altered explicit index.
- Locking/rewrite classification appears in the manifest.
- A concurrent-index extension runs only in autocommit phases, resumes after
  injected failure, and does not record the final migration early.
- Native enum/array extensions fail SQLite generation explicitly.

### 16.6 Introspection and drift mutation tests

For each provider, mutate one live property at a time:

- add/drop/rename table or column;
- alter storage type, nullability, default, collation, or generated expression;
- alter PK/unique/FK fields, order, action, or deferrability;
- add/drop/change check or explicit index;
- disable SQLite foreign keys or remove a required provider capability;
- rewrite a migration checksum or ledger parent hash; and
- leave an incomplete nontransactional phase.

Every mutation must fail verification. Provider-generated names, catalog row
ordering, statistics, and harmless formatting differences must not change the
fingerprint.

### 16.7 Capability parity tests

- Every declaration marked portable successfully lowers, migrates, introspects,
  and verifies on both providers.
- Removing either provider implementation for a portable capability fails a
  registry completeness test.
- Every provider extension succeeds only on its declared provider and is refused
  before partial work on the other.
- JSON/JSON-array declarations fail before DDL when SQLite JSON1 is absent.
- No accepted feature exists only in a renderer; it must also have lowering,
  diff, introspection, fingerprint, and live-test coverage.

## 17. Definition of done

This contract's part of P1 is done only when:

- Model IR v1 and PhysicalSchema canonical encodings are frozen and versioned;
- both lowerers implement the capability matrix;
- the shared diff produces deterministic operation DAGs;
- both renderers pass initial and incremental live migrations;
- both introspectors round-trip every managed feature;
- model, physical, and migration-chain fingerprints are independently verified;
- immutable-history and every drift mutation test fails as intended; and
- startup verification is read-only and refuses mismatch without auto-migration.

Placeholder rebinding, raw DDL string comparison, SQLite-only rebuild testing,
or PostgreSQL-only success cannot satisfy this definition.
