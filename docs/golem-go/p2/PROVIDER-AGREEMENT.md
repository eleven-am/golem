# P2 provider agreement and acceptance matrix

Status: **P2-B/P2-H controlling acceptance contract; evaluator, SQLite,
PostgreSQL, both live PostgreSQL profiles, runtime agreement promotion, and the
complete CI gate are verified**

Authority: [`../BIBLE.md`](../BIBLE.md), especially sections 2, 4, 7, 20, and
21. [`../01-operators.md`](../01-operators.md) is the detailed semantic and
mutation source after applying the resolutions in this document. P1's accepted
logical and physical schema is authoritative for storage. The Phase 0 package is
an independent policy-resolution oracle, not production operator code.

This document answers one question: which predicates may enter a frozen policy,
and what evidence proves that the Go evaluator, SQLite, and PostgreSQL give them
the same two-valued meaning?

### Current provider implementation boundaries

- SQLite installs three deterministic modernc scalar functions for ASCII folding,
  scalar-list operations, and exact JSON operations. Startup probes those
  functions through the active connection pool and binds the resulting proof to
  the physical schema fingerprint. These functions execute inside the SQL
  predicate; they are not a Go post-filter.
- PostgreSQL uses guarded, schema-qualified `jsonb` and scalar expressions.
  Exact public JSON numbers are encoded without `float64`, but PostgreSQL
  `jsonb` stores numbers through PostgreSQL `numeric`. A coefficient/exponent
  outside that documented physical range is refused by the codec before SQL
  execution.
- The Go value/evaluator layer retains exact canonical JSON numbers as sign,
  coefficient, and base-10 exponent. It has no JavaScript `2^53-1` ceiling and
  does not silently narrow a number merely because one provider has a smaller
  physical range.

## 1. Non-negotiable gate

An operator cell is public only when all of the following exist together:

1. a typed authoring method for the exact logical field kind;
2. a closed operator identity and canonical operand codec;
3. input, null, and provider-capability validation;
4. one Go evaluator implementation;
5. one SQLite rendering and parameter-encoding implementation;
6. one PostgreSQL rendering and parameter-encoding implementation;
7. a fixture probe comparing the same stable row identities in all three
   engines;
8. a probe proving that the SQL fragment itself never returns `NULL`; and
9. a named mutation which makes a named test fail.

Missing evidence closes the cell. It never means “evaluate after loading rows,”
“let the database coerce it,” or “use the nearest similar operator.” A schema
whose provider set cannot satisfy a frozen predicate is rejected before provider
execution.

### 1.1 Matrix notation

| Mark | Meaning |
|---|---|
| `P` | Portable. Required and agreement-proved on SQLite and PostgreSQL. |
| `PG` | Explicit PostgreSQL-only extension. It is absent from a dual-provider or SQLite schema. |
| `X` | Unsupported for this logical kind. Construction or freeze fails. |
| `N/A` | The nullable dimension does not apply to this node. |

There are no accepted SQLite-only operators. P2-B should not initially expose a
`PG` cell: the accepted Bible inventory below has a specified portable plan. A
future PostgreSQL extension must use a different public capability-bearing
method/operator identity; it cannot silently broaden a `P` method.

“Portable” in this document is a completion requirement, not a statement that
the current repository already contains the renderer. Until P2-H passes, every
new cell remains unavailable at runtime even if its public method compiles.

## 2. Logical-kind to operator matrix

The nullable column has two meanings:

- `non-null`: value operators are available, but `IsNull`/`IsNotNull` are not;
- `nullable`: the same value operators remain available and the two presence
  methods are added. Their null-row results are fixed in section 5.

No method accepts a null value operand. Presence uses explicit methods. List
elements are non-null by P1 construction. JSON has distinct typed sentinels for
SQL/absent versus JSON null at a path; those sentinels are not Go `nil`.

### 2.1 Scalars

| Logical kind | `Eq` `Ne` `In` `NotIn` | `LT` `LTE` `GT` `GTE` | `Contains` `StartsWith` `EndsWith` | Sensitive | ASCII-insensitive | Non-null presence | Nullable presence | SQLite | PostgreSQL |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `Bool` | P | X | X | N/A | N/A | X | P | P | P |
| `Int16` | P | P | X | N/A | N/A | X | P | P | P |
| `Int32` | P | P | X | N/A | N/A | X | P | P | P |
| `Int64` | P | P | X | N/A | N/A | X | P | P | P |
| `Float32` | P | P | X | N/A | N/A | X | P | P | P |
| `Float64` | P | P | X | N/A | N/A | X | P | P | P |
| `Decimal(p,s)` | P | P | X | N/A | N/A | X | P | P | P |
| `String` | P | P | P | P | P | X | P | P | P |
| `Bytes` | P | X | X | N/A | N/A | X | P | P | P |
| `UUID` | P | X | X | N/A | N/A | X | P | P | P |
| `Date` | P | P | X | N/A | N/A | X | P | P | P |
| `Time(p)` | P | P | X | N/A | N/A | X | P | P | P |
| UTC `DateTime(p)` | P | P | X | N/A | N/A | X | P | P | P |
| closed `Enum` | P | X | X | N/A | N/A | X | P | P | P |

Notes:

- `Ne` and `NotIn` are exact boolean complements of `Eq` and `In`; they are not
  bare SQL `<>`/`NOT IN` on nullable columns.
- `String` ordering is UTF-8 byte ordering. Sensitive text matching is literal;
  `%`, `_`, and `\` are not wildcards supplied by the author.
- ASCII-insensitive mode folds only bytes `A` through `Z`. SQLite must provide a
  provider-owned deterministic ASCII-fold function and probe it on every opened
  pool. PostgreSQL uses expressions proven equivalent under `COLLATE "C"`.
  On `String`, the mode applies to equality, membership, ordering, and text
  methods as one filter-wide comparison mode. Unicode case folding and
  locale-aware comparison are unsupported.
- Enum has equality/membership only. The TypeScript implementation's text and
  lexical ordering behavior is not carried into Go.
- Bytes equality is length- and byte-sensitive. Bytes have no lexical ordering
  in P2.
- Cross-kind operands are impossible through generated handles and are also
  rejected by the binder. A dynamic fallback builder does not exist.

### 2.2 Scalar lists

P1 accepts `ScalarList(E)` as capability `scalar-list:json-array:v1`, stored as
canonical JSON-array text on SQLite and `jsonb` on PostgreSQL. It does **not** use
PostgreSQL native arrays. Therefore every accepted list operator is a portable
cell or no cell; there is no native-array-only shortcut.

| Element kind `E` | `Eq(List[E])` | `Has(E)` | `HasEvery(...E)` | `HasSome(...E)` | `IsEmpty(bool)` | Non-null presence | Nullable presence | SQLite | PostgreSQL |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `Bool` | P | P | P | P | P | X | P | P | P |
| `Int16`/`Int32`/`Int64` | P | P | P | P | P | X | P | P | P |
| `Float32`/`Float64` | P | P | P | P | P | X | P | P | P |
| `Decimal(p,s)` | P | P | P | P | P | X | P | P | P |
| `String` | P | P | P | P | P | X | P | P | P |
| `UUID` | P | P | P | P | P | X | P | P | P |
| `Date`/`Time(p)`/`DateTime(p)` | P | P | P | P | P | X | P | P | P |
| closed `Enum` | P | P | P | P | P | X | P | P | P |
| `Bytes`, `JSON`, relation, nested list, nullable element | X | X | X | X | X | X | X | X | X |

SQLite lowers through JSON1 plus provider-owned exact element comparison where
JSON1 would otherwise coerce a number through binary floating point. PostgreSQL
lowers through `jsonb_array_elements`/indexed `jsonb` access, not `ANY` or native
array containment. Whole-list equality is length-, order-, logical-type-, and
value-sensitive. Provider JSON textual formatting is never the semantic test.

The provider must treat a stored element of the wrong JSON type, or a stored JSON
`null` element, as a non-match and must remain two-valued. The current P1 database
checks prove “valid array,” not every element's logical type, so these malformed
but physically insertable rows belong in the P2 corpus.

### 2.3 JSON

The public path is provider-neutral: an ordered typed sequence of `Key(string)`
and `Index(uint32)` segments. An empty sequence addresses the document root.
Authors never supply PostgreSQL `text[]`, SQLite JSONPath text, or raw path SQL.
Each renderer derives its own path representation from the typed sequence.

`Absent` is an evaluator state, not an operand and not SQL `NULL`. The three
singleton operands have these meanings:

| Sentinel | Meaning |
|---|---|
| `DbNull` | SQL-null document or absent addressed path |
| `JsonNull` | present addressed JSON literal `null` |
| `AnyNull` | either `DbNull` or `JsonNull` |

| JSON operator | Accepted operand/guard | Non-null JSON field | Nullable JSON field | SQLite | PostgreSQL | Classification |
|---|---|---:|---:|---:|---:|---:|
| `IsNull`, `IsNotNull` | none | X | P | P | P | P |
| path `Eq`, `Ne` sentinel | `DbNull`, `JsonNull`, `AnyNull` | P | P | P | P | P |
| path `Eq`, `Ne` value | canonical bool, exact number, string, array, object, JSON null | P | P | P | P | P |
| path `LT`, `LTE`, `GT`, `GTE` | exact JSON number against number, or string against string | P | P | P | P | P |
| `StringContains` | string slot and string operand | P | P | P | P | P |
| `StringStartsWith` | string slot and string operand | P | P | P | P | P |
| `StringEndsWith` | string slot and string operand | P | P | P | P | P |
| JSON string mode | sensitive or ASCII-insensitive | P | P | P | P | P |
| `ArrayContains` | canonical JSON candidate | P | P | P | P | P |
| `ArrayStartsWith` | first element structurally equals candidate | P | P | P | P | P |
| `ArrayEndsWith` | last element structurally equals candidate | P | P | P | P | P |

Portable JSON does not mean “SQLite happens to compare JSON text.” SQLite must
install and capability-probe deterministic policy functions backed by Golem's
canonical exact JSON codec for structural equality, exact numeric ordering, and
containment where JSON1 alone cannot preserve the contract. Those functions run
inside the SQL predicate before selection/pagination and return only integer `0`
or `1`; they are not a post-query evaluator fallback. PostgreSQL may use native
`jsonb` operations only where the agreement corpus proves the same meaning.

Every non-sentinel JSON operator has an explicit slot type guard. Missing paths,
SQL null, JSON null, and wrong-type values return false. `Ne(value)` is the exact
complement only over a present, correctly typed slot; sentinel `Ne` follows the
sentinel table. This distinction must be encoded in the registry rather than
inferred from provider syntax.

The exact codec uses `json.Number`-style decimal lexemes and the P1 canonical JSON
representation. There is no JavaScript `2^53-1` ceiling. Non-finite numbers,
duplicate object keys, invalid UTF-8, and values outside the declared public JSON
value union are rejected before freeze.

### 2.4 Relations

| Relation/cardinality | Operator | Nullable declaration changes methods? | SQLite | PostgreSQL | Classification |
|---|---|---:|---:|---:|---:|
| to-one | `Is(Predicate[R])` | no | P | P | P |
| to-one | `IsNot(Predicate[R])` | no | P | P | P |
| to-one | `IsNull()` | no | P | P | P |
| to-one | `IsNotNull()` | no | P | P | P |
| to-many | `Some(Predicate[R])` | no | P | P | P |
| to-many | `Every(Predicate[R])` | no | P | P | P |
| to-many | `None(Predicate[R])` | no | P | P | P |

All relation predicates use correlated `EXISTS` over descriptor-resolved local
and remote column pairs. `IsNull` means no related row, not “the local foreign-key
column is null.” This remains true for a required relation and for composite or
string-keyed correlations. `Every(C)` is `NOT EXISTS` of a related row for which
`C IS NOT TRUE`.

An evaluator row whose required relation dependency was not loaded is an error,
not an empty relation. A loaded empty to-many relation retains normal vacuous
semantics.

### 2.5 Logical nodes

| Node | Empty meaning | SQLite | PostgreSQL | Classification |
|---|---|---:|---:|---:|
| `All` / empty `And` | true | P | P | P |
| `None` / empty `Or` | false | P | P | P |
| `And(children...)` | all children | P | P | P |
| `Or(children...)` | any child | P | P | P |
| unary `Not(child)` | exact complement | P | P | P |

Only unary predicate `Not` is public. The TypeScript map grammar's `NOT: [A,B]`
NOR spelling does not require a separate Go node; it canonicalizes to
`Not(Or(A,B))` at a transport boundary.

### 2.6 Closed vocabulary

| Requested shape | Classification | Reason |
|---|---:|---|
| full-text `search` | X | no accepted provider-neutral evaluator contract |
| Mongo `isSet` | X | no SQL-provider meaning |
| regex or locale-aware text | X | no frozen cross-engine semantics |
| Unicode-insensitive mode | X | only ASCII folding is specified |
| enum ordering or text matching | X | enum is equality/membership only |
| bytes/list/JSON cross-kind coercion | X | logical kinds are exact |
| caller-provided JSONPath or PostgreSQL path text | X | typed path segments only |
| field-reference operand | X | literal canonical operands only |
| aggregate leaf | X | aggregates are separately authorized read operations |
| raw operator name, field name, identifier, or SQL | X | generated identities and closed nodes only |

Predicate-level unary `Not` is the only recursive negation primitive. A scalar
filter-object `not` from another transport must lower to `Ne` for a value or to
predicate `Not` for a nested filter; it does not add a separate registry family.

## 3. Provider capabilities and startup behavior

P1 currently proves these relevant physical facts:

| Provider | Floor/driver | Storage facts used by P2 |
|---|---|---|
| SQLite | 3.38+, `modernc.org/sqlite` | `INTEGER`, `REAL`, `TEXT`, `BLOB`; JSON1 runtime-probed; scalar lists are JSON-array `TEXT`; foreign keys are enabled on pooled connections. |
| PostgreSQL | 15+, `pgx/v5` | native scalar types; `jsonb` runtime availability; scalar lists are JSON arrays in `jsonb`; UTC/date-style session parameters are fixed. |

P2 adds provider-manifest capabilities with versioned identities for at least:

- binary string comparison and literal sensitive matching;
- deterministic ASCII fold/match;
- exact canonical JSON equality/order/containment;
- exact typed scalar-list JSON element comparison; and
- policy relation correlation.

SQLite provider functions must be registered for every connection created by the
pool and probed through the pool in the same manner as JSON1 and foreign keys.
The pinned `modernc.org/sqlite` driver exposes deterministic scalar-function
registration for all connections opened after registration, so registration must
finish before `Provider.Open` creates the pool. The provider functions call the
shared exact value/JSON routines; they do not invent a second semantic evaluator.
PostgreSQL capabilities must be proven against the selected server, not inferred
from placeholder syntax. A missing capability diagnostic names provider, model,
field/relation, operator, and capability ID.

A dual-provider schema exposes the intersection, which this contract defines as
all `P` cells above once proved. A single-provider schema does not automatically
gain an operator. Only a separately registered `PG` identity and compile fixture
may do that.

## 4. Canonical parameters

All values are copied into the frozen predicate. Bytes, JSON, list, and path
slices have no shared backing storage with application input. Identifiers come
only from generated descriptors. Operator names, casts, collations, functions,
and path syntax are fixed renderer tokens, never value interpolation.

| Logical value | Canonical frozen value | SQLite argument | PostgreSQL argument |
|---|---|---|---|
| `Bool` | one boolean bit | `int64(0|1)` | `bool` |
| `Int16/32/64` | signed exact width | checked `int64` | matching signed integer |
| `Float32` | finite IEEE value normalized to 32 bits | finite `float64` exactly representing that float32 | `float32`/explicit `real` cast |
| `Float64` | finite IEEE value; canonical `-0 == 0` decision recorded | finite `float64` | `float64`/explicit `double precision` cast |
| `Decimal(p,s)` | signed coefficient plus declared scale | checked scaled `int64` coefficient | canonical decimal digits with explicit `numeric(p,s)` cast |
| `String`/enum | valid UTF-8 bytes / validated enum wire label | `string` with explicit `BINARY` comparison | `string` with explicit `COLLATE "C"` comparison |
| `Bytes` | copied byte sequence | `[]byte` BLOB | `[]byte` bytea |
| `UUID` | 16 bytes | canonical lowercase hyphenated text | 16-byte/driver UUID or canonical text with `uuid` cast |
| `Date` | validated `YYYY-MM-DD` | canonical text | canonical text with `date` cast |
| `Time(p)` | microseconds since midnight normalized to `p` | canonical fixed-precision text | canonical text with `time(p)` cast |
| `DateTime(p)` | UTC Unix microseconds, monotonic data removed | checked `int64` microseconds | UTC `time.Time` normalized to `p` or canonical text with `timestamptz(p)` cast |
| JSON | copied canonical JSON bytes with exact numbers | canonical text or provider-function argument | canonical text with `jsonb` cast |
| `ScalarList(E)` | copied ordered canonical element vector and canonical JSON array | canonical JSON text plus typed element metadata owned by the operator | canonical JSON text with `jsonb` cast plus typed element metadata |
| JSON path | copied typed segment vector | renderer-built JSONPath or canonical path blob as a bound value | renderer-built `text[]` path arguments; segments remain values |

Unsigned integers, bare Go `int`, arbitrary structs/maps, non-finite floats, and
unvalidated decimal/date/time strings are not parameter forms. PostgreSQL's
ambient type inference and SQLite's coercion rules never define semantics.

## 5. Two-valued obligations

For nullable scalar subject `x` and non-null operand `v`:

| Predicate | `x = NULL` |
|---|---:|
| `Eq(v)`, `In(vs...)` | false |
| `Ne(v)`, `NotIn(vs...)` | true |
| ordered/text operation | false |
| `IsNull()` | true |
| `IsNotNull()` | false |

Empty operands are fixed:

| Form | Result |
|---|---|
| `In()` | false |
| `NotIn()` | true |
| `HasEvery()` | true only for a present list |
| `HasSome()` | false |
| `Some` on loaded empty relation | false |
| `Every` or `None` on loaded empty relation | true |

Every registry entry declares `SQLIsTwoValued=true`. Each provider renderer must
make that declaration measurable with both leaf and composed probes. Typical
mechanisms are null-safe equality, explicit presence branches, type guards,
`EXISTS`, and provider functions which return non-null booleans. A final
`COALESCE(fragment, false)` may defend the authorization boundary but does not
excuse a leaf whose own unknown-count probe is non-zero.

For each rendered probe `F`, execute these statements with the exact same bound
arguments:

```sql
-- identity agreement input
SELECT stable_id FROM probe_root WHERE F ORDER BY stable_id;

-- must return zero
SELECT count(*) FROM probe_root WHERE (F) IS NULL;

-- sets must be identical
SELECT stable_id FROM probe_root WHERE (F) IS NOT TRUE ORDER BY stable_id;
SELECT stable_id FROM probe_root WHERE NOT (F) ORDER BY stable_id;
```

Also execute an unguarded nullable comparison as a control. It must have a
non-zero unknown count and different polarity results; otherwise the harness is
not capable of detecting three-valued SQL.

## 6. Canonical fixture corpus

One logical fixture definition is lowered and migrated through the P1 provider
pipeline for both engines. Seed data is provider-neutral canonical values; only
the provider seeder knows physical encodings. Expected results are stable logical
identity tuples, never row counts or provider row numbers.

### 6.1 Root scalar rows

The corpus includes non-null and nullable columns for every scalar kind and rows
covering:

- booleans both ways;
- signed minimum, zero, maximum, `2^53`, and `2^53+1` integer values;
- finite float negatives, `-0`, `0`, fractions, and float32 rounding boundaries;
- decimal zero spellings, sign, maximum `Decimal(18,s)` coefficient, and adjacent
  exact values;
- empty string, ASCII case pairs, `%`, `_`, `\`, combining forms, `Å`/`å`, an
  astral-plane value, U+E000 private-use, and deliberately invalid wildcard
  lookalikes;
- empty bytes, embedded NUL, `00ff`, and differing lengths;
- first/middle/last UUID values;
- date/time/datetime precision boundaries and equal instants with different input
  offsets before canonicalization;
- every enum label; and
- a row with every nullable column null.

Predicate operands include an equal, lower, higher, absent, empty, and boundary
value as applicable. Every non-constant probe is also wrapped in unary `Not`.

### 6.2 Scalar-list rows

For every accepted element kind: null list column, empty list, singleton, ordered
pair, reversed pair, duplicate values, and a non-matching value. Additional
physically insertable adversarial rows contain a JSON null element, a wrong JSON
element type, and non-canonical-but-valid JSON text on SQLite. The last case must
either be rejected by a strengthened storage constraint or interpreted by the
same canonical codec; raw text comparison is not accepted.

The list probes cover empty and non-empty operands, exact list equality, order,
duplicates, each membership quantifier, both `IsEmpty` flags, presence methods,
and negation.

### 6.3 JSON rows

Include SQL null; JSON null; booleans; exact numbers on both sides of `2^53`;
decimal exponents; strings with every text boundary; empty/non-empty arrays and
objects; nested arrays/objects; duplicate array elements; absent keys; present
keys holding JSON null; out-of-range indices; scalar traversal; keys containing
dots, quotes, brackets, and backslashes; and canonical objects authored with
different input key order.

Every sentinel, scalar/document equality, ordering, string, array, path, wrong
type, absent path, and insensitive-mode form gets both agreement and unknown-count
probes.

### 6.4 Relations and social shape

The social fixture contains `User`, `Post`, recursive `Comment`, `Friendship`,
`Tag`, and `PostTag`, including:

- nullable and required to-one relations;
- empty, singleton, and multi-row to-many relations;
- a related row with nullable leaves;
- recursive comments to at least depth three;
- a composite-key join relation;
- a relation correlated by a string key with ASCII/non-ASCII boundary values;
- friendship directions that select different rows; and
- a dedicated drift subfixture whose relation descriptor has a non-null local
  key but no matching target row.

The dangling relation fixture is isolated from the normally constrained migrated
schema. It may use a descriptor-backed test table without a physical FK; it must
not disable foreign-key enforcement on the shared provider pool.

The evaluator loader marks each relation as `missing`, `loaded empty`, or `loaded
with rows`. Missing must refuse. SQL and evaluator compare only after the exact
dependency tree has been loaded.

### 6.5 Generated cases and discrimination

A seeded generator produces bounded well-typed trees across fields, relations,
and combinators. Shrinking preserves model/type validity. The checked-in seed and
expected identity sets make failures reproducible. Coverage asserts:

- registry operator identities exactly equal probe operator identities;
- every logical-kind/provider/nullability cell has a positive or negative probe;
- every renderer branch and parameter codec is visited;
- answer sets exceed a documented diversity floor; and
- most non-constant cases select a strict non-empty subset.

## 7. Live harness design

The shared harness owns corpus traversal and comparison, not provider semantics.
Its per-case protocol is:

1. freeze and bind one typed predicate against the P1 logical registry;
2. evaluate it against exact decoded logical records with dependencies loaded;
3. render it with the SQLite physical schema, execute one selection statement,
   and run its unknown/polarity probes;
4. render it independently with the PostgreSQL physical schema and do the same;
5. compare full stable identity tuples from evaluator, SQLite, and PostgreSQL;
6. record errors as outcomes, never skips; and
7. assert exactly one selection statement per provider per case so client-side
   post-filtering cannot hide in the harness.

SQLite uses a named file under `t.TempDir()`, opened through
`sqlite.Provider.Open`; private `:memory:` databases are forbidden by the current
verified multi-connection pool. The harness applies the generated physical schema
and verifies JSON1 plus every P2 provider function through at least two pooled
connections.

PostgreSQL uses `postgresql.Provider.Open`, PostgreSQL 15+, and a unique quoted
schema derived from the test name and random run suffix. Cleanup drops only that
resolved schema. Tests never use ambient libpq configuration. Session timezone,
date style, interval style, and standard-conforming strings remain provider-owned.

The collation proof runs against both a `C`-default cluster and a linguistic
default cluster. Both must agree with Go and with each other while the deliberately
unforced control differs on the linguistic cluster. This is required to make M11
load-bearing rather than a dead textual assertion.

Provider-specific negative cases run through a manifest with the named capability
removed and assert refusal before any selection statement. The normal production
SQLite and PostgreSQL manifests must satisfy every `P` cell.

## 8. Named mutation ownership

The detailed chapter's mutation names remain review anchors even where P1 changed
the physical representation. The property, not obsolete TypeScript SQL text, is
controlling.

| Mutation | P2 owner and required failing test |
|---|---|
| M1 | relation renderer: `every_nullable_child` identity and polarity probe |
| M2 | ordered renderer: `ordered_nullable_unknown_count` |
| M3 | equality renderer: `equals_null_safe_unknown_count` |
| M4 | inequality renderer: `not_exact_complement_nullable` |
| M5 | membership renderer: `in_nullable_unknown_count` |
| M6 | membership renderer: `not_in_includes_null_subject` |
| M7 | normalization/renderer: `in_empty_is_none` |
| M8 | normalization/renderer: `not_in_empty_is_all` |
| M9 | text codec: `literal_like_metacharacters_sensitive` |
| M10 | text codec: `literal_like_metacharacters_insensitive` |
| M11 | both renderers: `binary_collation_two_postgres_defaults` plus SQLite/Go agreement |
| M12 | ASCII fold: `non_ascii_uppercase_is_not_folded` |
| M13 | text renderer: `empty_needle_excludes_null_subject` |
| M14 | list renderer: `list_has_null_column_unknown_count`; JSON-array equivalent of the old native-array guard |
| M15 | list renderer: `list_is_empty_null_column_unknown_count` |
| M16 | list renderer: `list_equals_malformed_null_element_is_two_valued` |
| M17 | relation renderer: `to_one_presence_uses_related_existence` drift fixture |
| M18 | relation evaluator/renderers: `to_many_quantifier_matrix` |
| M19 | transport canonicalizer: `not_array_is_nor`; mutation must be changed to `OR(NOT A, NOT B)`, because `AND(NOT A, NOT B)` is equivalent under the required two-valued invariant |
| M20 | normalization: `empty_combinator_constants` |
| M21 | exact values: `adjacent_integer_above_2pow53` and exact JSON/list counterparts |
| M22 | P3 read decoding owns the cast/projection mutation; P2 additionally has `oracle_exact_decode_no_float64` so its evaluator corpus cannot mask loss |
| M23 | binder/evaluator: `bool_never_numeric` negative compile/freeze test |
| M24 | string comparator: `astral_vs_private_use_utf8_order` |
| M25 | ordered evaluator/renderers: `null_never_orders` |
| M26 | JSON renderer: all JSON entries participate in `json_unknown_count_zero` |
| M27 | registry/oracle: `registry_probe_bijection` |
| M28 | binder: `comparison_mode_rejected_on_non_text_operation` |
| M29 | capability gate: `ascii_insensitive_missing_capability_refuses_before_sql`; production SQLite supplies the proved capability rather than inheriting the old refusal |
| M30 | capability gate: `scalar_list_json_capability_missing_refuses_before_sql`; native PostgreSQL arrays are no longer assumed |

No mutation is satisfied by checking generated SQL text alone. Each applicable
mutation must change identities, produce unknowns, bypass refusal, or violate a
declared exact-value outcome.

The checked-in owner mapping is:

- M1, M17–M20, M29, and M30:
  `policy/sql.TestCompileNamedRelationAndCombinatorMutations` and
  `TestCompileNamedCapabilityMutationsRefuseBeforeDialectRendering`;
- M2–M10 and M13:
  `sqlite.TestPolicySQLiteNamedScalarMutationMatrix`, which executes production
  fragments and separately asserts zero SQL unknowns;
- M11: `oracle.TestPostgreSQLProviderAgreementLiveProfiles` and its forced versus
  unforced collation control in `verifyPostgreSQLCollationProfile`;
- M12 and M19–M26:
  `evaluate.TestNamedEvaluatorMutationInvariants`, with provider-side witnesses
  in the SQLite/PostgreSQL agreement suites;
- M14–M16: `sqlite.TestPolicySQLiteNamedMutationSemanticsAndUnknownCount` plus
  the portable live oracle;
- M18: the SQL quantifier matrix above plus
  `evaluate.TestRelationLoadedEmptyVacuityComplementsAndNestedRows`;
- M21: the evaluator invariant, exact JSON/list SQLite cases, and PostgreSQL
  `adjacent_integer_above_2pow53` live subtest;
- M23 and M28: `operator.TestNamedValidationMutations`; and
- M26 and M27: the JSON unknown-count provider cases plus
  `oracle.TestSocialCorpusHasCanonicalShapeAndOperatorBijection`.

M22's original read-projection mutation remains P3-owned. Its P2 owner proves
only exact oracle/value decoding and must not be cited as a completed P3 read
test.

## 9. Contradictions and fail-closed resolutions

These are source conflicts, not implementation discretion:

1. **Authority wording inside `01-operators.md`.** Its header calls the Bible
   authoritative, then says TypeScript is right on disagreement. Resolution:
   Bible, accepted P1 storage, this contract, then measured TypeScript evidence.
   TypeScript cannot overrule the Go exact-value or dual-provider decisions.
2. **PostgreSQL-first versus equal providers.** The detailed chapter says Go need
   not ship SQLite. Resolution: rejected by Bible sections 2, 4, 20, 21, and 23.
   Every `P` cell requires live SQLite and PostgreSQL.
3. **Scalar-list storage.** The chapter specifies PostgreSQL native arrays and
   SQLite refusal. P1 accepts canonical JSON arrays on both providers. Resolution:
   list operators target P1 JSON-array storage. `ANY`, `cardinality`, and array
   indexing are historical examples, not required SQL.
4. **Insensitive SQLite.** The chapter refuses SQLite because its built-in
   `LIKE` cannot provide byte-order-sensitive and ASCII-only behavior. Resolution:
   do not use built-in `LIKE` as an approximation. The Go SQLite provider installs
   a deterministic ASCII function, probes it on pooled connections, and must pass
   the oracle; otherwise the methods are unavailable for a dual-provider schema.
5. **JSON paths and support.** The chapter exposes different public path shapes
   and refuses several SQLite JSON operations. Resolution: public paths are typed
   provider-neutral segments. Provider-owned exact SQLite functions now close
   JSON1's semantic gaps. The full portable handle is generated, and the
   promoted runtime gate accepts the proved portable inventory while refusing
   unproved additions.
6. **JSON numeric precision.** The chapter inherits the JavaScript `2^53-1`
   ceiling and float64 structural equality. Bible/P1 require exact canonical JSON
   numbers. Resolution: exact values with no JavaScript ceiling; any provider path
   that coerces through float is forbidden.
7. **Null operands.** The chapter accepts literal null operands for several scalar
   operations; the typed baseline uses explicit presence methods. Resolution:
   public Go scalar/list methods never accept null. JSON's typed null sentinels
   remain because SQL null, absent path, and JSON null are distinct values.
8. **Enum behavior.** The chapter casts enum to text and thereby permits textual
   operations. The accepted P2-A surface is equality-only. Resolution: enum
   equality/membership only.
9. **Missing relation data.** The chapter treats an unloaded relation as empty;
   the P2 plan requires missing-dependency refusal. Resolution: refusal. Empty
   quantifier semantics apply only to a loaded empty relation.
10. **`has(nil)`.** The chapter accepts it as false while rejecting null inside
    other list operands. P1 list elements and generated `Has(E)` are non-null.
    Resolution: unrepresentable through public types and rejected by the binder.
11. **Unsigned and bare integer parameters.** The chapter describes unsigned
    binding. P1 rejects unsigned fields and bare `int`. Resolution: only declared
    signed widths are accepted.
12. **M19 is not a discriminating mutation as written.** Under two-valued logic,
    `NOT(A OR B)` equals `NOT A AND NOT B`. Resolution: retain NOR semantics but
    mutate to `NOT A OR NOT B`, or mutate transport grouping, so the test can fail.
13. **M22 belongs to read decoding.** It mutates projection/JSON decoding rather
    than a policy predicate. Resolution: P3 owns the original mutation; P2 must
    still prove its oracle loader uses exact codecs. P2 completion must not claim
    the future P3 projection test already exists.
14. **PostgreSQL collation coverage.** Release verification provisions both `C`
    and linguistic defaults; local development may run one profile.
15. **Canonical JSON is a logical contract but current checks are weaker.** P1
    says SQLite stores canonical JSON, while its generated checks currently prove
    valid JSON/array shape rather than canonical object order, numeric spelling,
    or every scalar-list element kind. Resolution: P2 never uses raw SQLite text
    equality. The agreement corpus includes physically valid non-canonical and
    wrong-element rows; either P1 later strengthens storage checks or P2's exact
    provider function canonicalizes before comparison.

Any rejected resolution re-closes the affected matrix cells until a replacement
with equal or stronger three-engine evidence is accepted.

## 10. Completion commands and CI contract

The ordinary local package suite may skip the live PostgreSQL profile test when
either DSN is absent. That convenience does not constitute completion evidence:
the P2 completion/release profile supplies both variables, so setup, agreement,
and collation failures are hard failures and no PostgreSQL profile is skipped.

```sh
cd go
go test -p=1 -count=1 ./...
go test -p=1 -count=2 ./...
go test -shuffle=on -count=10 ./internal/policy/...
go test -p=1 -race -count=1 ./...
go vet ./...
test -z "$(find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
git diff --check
```

The P2 completion/release job must provide explicit PostgreSQL 15+ DSNs:

```sh
GOLEM_TEST_POSTGRES_DSN='postgres://.../golem_c?...' \
GOLEM_TEST_POSTGRES_LINGUISTIC_DSN='postgres://.../golem_linguistic?...' \
go test -count=1 ./internal/policy/oracle ./internal/provider/sqlite ./internal/provider/postgresql
```

`GOLEM_TEST_POSTGRES_DSN` is the existing repository convention and must point to
the `C`-default test cluster for this profile. The second variable is mandatory
for P2 completion because M11 must be measured. CI creates disposable databases
or grants creation/drop of per-test schemas; tests do not drop `public`, shared
schemas, or databases.

P2-H is complete only when the checked-in registry/probe inventory, identity
record, unknown-count record, mutation-to-test map, provider capability report,
and both live profiles pass without an allowed skip. Public shells or renderer
goldens alone are not completion evidence.
