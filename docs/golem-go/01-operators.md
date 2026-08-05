# 01 — Operators

> **Specification status:** detailed supporting research. The merged
> [`BIBLE.md`](./BIBLE.md) is authoritative, especially for dual-provider
> support, exact Go numeric ranges, and the conflict resolutions in section 23.

**Status:** normative specification for the Go port.
**Ground truth:** the TypeScript implementation published as golem 0.6.1
(`typescript/packages/policy/src/{operators,sql,values,json,compile,scope}.ts` and the
suites under `typescript/packages/policy/test/`). Where this document and the
TypeScript disagree, the TypeScript is right and this document has a bug — but the
*reasons* recorded here are the part a reimplementation loses first, so read them.

This is the leaf document. Everything else in this set — the condition tree, the
code generator, the ability compiler, the query planner — assumes the semantics
defined here and does not restate them.

---

## 0. Terms

Define these before reading anything else. They are used with exactly these
meanings throughout.

**Condition.** An inspectable data tree. Not a closure. A condition is built by
generated, type-safe column constructors (`Users.Email.StartsWith("a")`) and each
leaf node carries its own model name, its own field name, an operator, and an
operand. There is no later step that works out which model a field belongs to.

**Operator.** A named comparison with a fixed arity and a fixed operand shape.
The set of operators is closed: it is a table, it is enumerated in §2, and nothing
outside the table is accepted. Operators are the only place where a value is
compared to anything.

**Operand.** The right-hand side supplied by the policy author. One of: a scalar,
a list of scalars, a nested condition object, or a boolean flag (`isEmpty`).

**Subject value.** The left-hand side. In the database it is a column or a
correlated subquery. In memory it is the decoded Go value read off a row.

**Two backends.** Every operator has exactly two implementations that must agree:

- **render** — produces a SQL fragment (a tree of text, identifiers, and bound
  parameters) for a `Dialect`.
- **evaluate** — decides the operator against an already-decoded in-memory value.

**Agreement.** For every condition and every row, `evaluate` and `render` must
select the same rows. This is enforced by an oracle (§7.2), not by inspection.

**Dialect.** The interface through which the policy package reaches physical SQL.
The policy package imports nothing concrete; the dialect supplies placeholder
syntax, identifier quoting, the null-safe equality spelling, the binary collation
name, `LIKE` support, and optional JSON support. Postgres is the target. A second
dialect exists in the TypeScript (SQLite) and its divergences are recorded here
because they explain *why* the Postgres rendering is shaped the way it is; the Go
port need not ship SQLite, but must keep the dialect seam.

**Names.** The interface through which the policy package resolves a model to a
physical table and a field to a physical column. It is separate from the dialect.
A model or field with no physical name is a hard error, never a fallback to the
logical name — falling back silently targets the wrong table under `@map`.

**Scope.** A resolved position in the query: a table, an alias, the ability to
produce a column reference for a field, a list-column reference for a list field,
and a relation hop for a relation field. A relation hop carries a child scope, a
to-many flag, and a `Wrap(predicate)` that produces a correlated `EXISTS`.

**Discharge.** Deciding whether a policy is satisfied. Two invariants govern it:

1. Every operator renders **two-valued** SQL — `TRUE` or `FALSE`, never `UNKNOWN`.
2. A filter is a read; discharge is decided against the rows the statement selects.

---

## 1. The two-valued invariant

### 1.1 The rule

> For every operator in the table, for every operand the operator accepts, and for
> every possible column contents including `NULL`, the rendered SQL fragment
> evaluates to `TRUE` or to `FALSE`. It never evaluates to `UNKNOWN`.

This is a property of *each individual fragment*, not merely of the whole `WHERE`
clause. It is declared per operator (`SQLIsTwoValued bool` on the table entry) and
it is *measured* against a live engine (§7.2.1).

### 1.2 Why it is not optional

SQL is three-valued. `NULL < 5` is `UNKNOWN`, not `FALSE`. `NOT UNKNOWN` is
`UNKNOWN`. A `WHERE` clause admits a row only when the predicate is `TRUE`, so at
the top level `UNKNOWN` and `FALSE` look the same and the sloppiness is invisible.
It stops being invisible the moment a predicate is negated or quantified.

**Consequence A — `every` becomes wrong.** Golem renders

```
{ children: { every: C } }
```

as a negated correlated `EXISTS` over the related rows that do **not** satisfy `C`:

```sql
NOT (EXISTS (SELECT 1 FROM "child" AS "t0_1"
             WHERE "t0_1"."parent_ref" = "t0"."id"
               AND (((C)) IS NOT TRUE)))
```

The `IS NOT TRUE` is the null-safe negation. The obvious rendering is `NOT (C)`.
They are equivalent **exactly when `C` is two-valued**, and they differ when it is
not — and they differ in the dangerous direction:

| `C` on a related row | `NOT (C)` | `(C) IS NOT TRUE` |
| --- | --- | --- |
| `TRUE` | `FALSE` | `FALSE` |
| `FALSE` | `TRUE` | `TRUE` |
| `UNKNOWN` | `UNKNOWN` | `TRUE` |

A related row that leaves `C` unknown is a row that does not satisfy `C`. It must
therefore violate `every`. Under `NOT (C)` the inner `EXISTS` does not see it, the
outer `NOT EXISTS` stays `TRUE`, and the parent is **granted** a permission that
one of its children does not support. This is measured: on the production-shaped
fixture in `test/support/library.ts`, the naive rendering returns groups
`2,4,5,6` and the null-safe rendering returns `4,6`. Groups 2 and 5 are granted by
the naive form and denied by both the null-safe SQL and the in-memory evaluator.

Golem wears both belts: `every` always renders `IS NOT TRUE`, **and** every leaf is
two-valued so the two forms coincide. The two-valued suite proves the coincidence
by running both polarities of every rendered fragment and requiring identical
answers. If the invariant ever breaks, `every` stays sound and the suite goes red;
if `every` is "simplified" to `NOT (…)`, the suite goes red on rows with a null in
the relation.

**Consequence B — a null column silently swallows a policy predicate.** Consider
a rule "deny anything whose `amount` is 5 or more", expressed as a negated grant.
If `amount < 5` renders as the bare `"t0"."amount" < $1`, then a row with
`amount = NULL` is `UNKNOWN` under the grant and `UNKNOWN` under its negation. The
row is admitted by neither branch, so the policy has no opinion about it — and
whichever branch happens to be the outermost `NOT` in the composed ability decides
it by accident. A single nullable column, which the policy author never mentioned,
quietly removes rows from the reach of the rule.

Golem renders it as `("t0"."amount" IS NOT NULL AND "t0"."amount" < $1)`. The
null row is `FALSE`, its negation is `TRUE`, and the rule means the same thing
under composition as it does alone.

**Consequence C — the two backends can agree at all.** The in-memory evaluator is
two-valued by construction: a Go predicate returns `bool`. If the SQL were
three-valued, agreement under negation would be impossible to state, let alone
test. Two-valuedness is what makes the oracle in §7.2 a well-formed question.

### 1.3 How two-valuedness is achieved

Three mechanisms, used consistently:

1. **Presence guard.** `(<col> IS NOT NULL AND <col> <op> $n)`. Used by every
   ordering comparison, every `LIKE`/`ILIKE` match, and `in`.
2. **Null-safe equality.** Postgres `IS NOT DISTINCT FROM`; SQLite `IS`. Two-valued
   by definition: `NULL IS NOT DISTINCT FROM NULL` is `TRUE`,
   `NULL IS NOT DISTINCT FROM 1` is `FALSE`.
3. **Explicit null branch.** `(<col> IS NULL OR <col> NOT IN (…))` for `notIn`;
   `COALESCE(<v> = ANY(<listcol>), FALSE)` for `has`.

`EXISTS` is inherently two-valued and needs no guard; the guard goes *inside* it,
on the correlated predicate.

### 1.4 Constant fragments

Two constant fragments exist and are used wherever an operator degenerates:

```
SQLTrue  = "(1 = 1)"
SQLFalse = "(1 = 0)"
```

They are written as comparisons rather than as `TRUE`/`FALSE` literals so they
render identically on every engine and can be dropped into any expression
position.

---

## 2. The operator table

### 2.0 Preliminaries

#### 2.0.1 Value kinds

Classification of any Go value, used by validation, by equality, and by ordering.

| Kind | Go values |
| --- | --- |
| `Null` | `nil`, a typed nil, an absent field |
| `Numeric` | any signed/unsigned integer, `float64`, `*big.Int`, `*big.Rat`, any decimal type |
| `String` | `string` |
| `Bool` | `bool` |
| `Date` | `time.Time` |
| `Unsupported` | anything else — structs, maps, slices, channels, functions |

`Unsupported` is not an error kind in the tree; it is what validation rejects and
what the evaluator refuses to compare.

Named kind sets used by the table:

```
ScalarKinds      = {Null, Numeric, String, Bool, Date}
OrderedKinds     = {Null, Numeric, String, Date}
TextKinds        = {Null, String}
ListElementKinds = {Numeric, String, Bool, Date}
```

`ListElementKinds` excludes `Null` deliberately: see §2.1.3.

#### 2.0.2 Equality — `ValuesEqual(l, r) bool`

```
1. If Kind(l) == Null or Kind(r) == Null:
       return Kind(l) == Null && Kind(r) == Null
2. If Kind(l) == Bool or Kind(r) == Bool:
       return Kind(l) == Bool && Kind(r) == Bool && l == r
3. return Compare(l, r) == (0, ok=true)
```

Rule 2 is load-bearing. Without it, `true` would compare equal to `1` through the
numeric path in any language that coerces, and a `Boolean` column filter would
match an `Int` operand. Golem never coerces across kinds.

#### 2.0.3 Ordering — `Compare(l, r) (int, bool)`

Returns `(sign, true)` or `(_, false)` when the pair is not comparable.

```
1. If either side is Null or Unsupported            -> not comparable
2. Numeric x Numeric  -> exact decimal comparison (see 2.0.4)
3. String  x String   -> byte-wise comparison of the UTF-8 bytes
4. Either side is Date:
       the other side must be Date or String;
       a String is parsed as an instant, failure -> not comparable;
       compare the two instants
5. Anything else (String x Numeric, Bool x anything, ...) -> not comparable
```

Not-comparable is **not** an error. Every operator that uses `Compare` treats
not-comparable as a non-match. This is what makes `lt` against a null column a
plain `false` rather than a panic (§5).

#### 2.0.4 Exact numeric comparison

`Numeric x Numeric` must be **exact**, across mixed representations. The corpus
deliberately contains `9007199254740992` and `9007199254740993` — adjacent
integers that are the same `float64`. If the comparison routes through `float64`,
`{big: {equals: 9007199254740993}}` matches the row holding `9007199254740992`
in memory while the database says no, and the oracle goes red.

Required behaviour:

- Normalise each operand to `(sign, integerDigits, fractionDigits)` with leading
  zeros stripped from the integer part and trailing zeros stripped from the
  fraction, or equivalently to a `*big.Rat` / arbitrary-precision decimal.
- Compare sign first; if both are zero they are equal regardless of spelling
  (`-0`, `0`, `0.000` are one value).
- Compare integer digit **length** first, then integer digits lexically, then
  zero-padded fraction digits lexically.
- A non-finite `float64` (`NaN`, `±Inf`) is not comparable.
- Scientific notation in a decimal string (`1e3`) must normalise to `1000`.

Implementation note for Go: `*big.Rat` with `Cmp` satisfies all of the above and
is the recommended route. `float64` inputs must be converted through their exact
shortest decimal representation (`strconv.FormatFloat(v, 'g', -1, 64)`), not
through a binary conversion, so that `0.1` compares equal to the decimal `0.1`.

#### 2.0.5 Column references

`Scope.Column(field)` produces the subject SQL node for a scalar field. The
rendering depends on the declared field type:

| Field kind/type | Postgres node |
| --- | --- |
| scalar, type `String` | `"t0"."col" COLLATE "C"` |
| enum | `CAST("t0"."col" AS TEXT) COLLATE "C"` |
| any other scalar | `"t0"."col"` |

`Scope.ListColumn(field)` produces `"t0"."col"` with **no** collation for a scalar
list column. Collation is a property of a text comparison; applying it to an
array type is a syntax error, and the list operators compare elements through
`= ANY(...)` where the array's own element collation applies.

The `COLLATE "C"` on every string and enum column is not decoration. See §3.

Requesting `Column` on a relation field, on a list field, or on a field with no
physical column name is a render error, never a guess.

#### 2.0.6 Relation hops

`Scope.Relation(field)` returns a hop or nothing. The hop's `Wrap(P)` produces:

```sql
EXISTS (SELECT 1 FROM "child_table" AS "t0_1"
        WHERE <corr_1> AND … AND <corr_k> AND (P))
```

where each `corr_i` is `<childColumnRef> = <parentColumnRef>` built through
`Column`-style references on both sides — so a string foreign key is correlated
`"t0_1"."slug" COLLATE "C" = "t0"."slug" COLLATE "C"`. Aliases are derived from
the root alias by depth: `t0`, `t0_1`, `t0_2`, … A to-one hop uses the declared
`relationFromFields`/`relationToFields`; a to-many hop finds the single field on
the related model that holds the foreign key back, and refuses if there are zero
(implicit many-to-many through a join table golem cannot see) or more than one
(ambiguous without relation names).

#### 2.0.7 Escaping and parameters

Nothing is ever interpolated into SQL text except identifiers (quoted by the
dialect, with the quote character doubled) and fixed keywords. Every value is a
bound parameter. Postgres placeholders are `$1, $2, …` numbered in emission
order; SQLite uses `?`.

`LIKE`/`ILIKE` patterns are built by escaping `\`, `%` and `_` in the operand with
a leading `\`, then wrapping with `%` as the operator requires, and the fragment
always carries an explicit `ESCAPE '\'`. The escape character is dialect-supplied
(`Dialect.Like.Escape`), because engines disagree about whether backslash is an
escape by default.

#### 2.0.8 Table entry shape

Each operator carries, as data:

```go
type Operator struct {
    Name             string
    Operand          OperandShape        // Scalar | ScalarList | Conditions | Flag
    AcceptedKinds    []Kind
    NullValue        NullValueBehaviour  // never | always | when-operand-null | when-operand-not-null
    NullOperand      NullOperandBehaviour// allowed | rejected | never-matches
    SQLIsTwoValued   bool                // must be true for every entry
}
```

`NullValue` describes what the operator does when the **subject** is null.
`NullOperand` describes what it does when the **operand** is null. Both are
declarations that the suites check against measured behaviour; they are not
free-text notes.

---

### 2.1 Scalar operators

The eleven scalar operators. Every one of them accepts `mode: insensitive`
(§2.2); the folded rendering is given inline.

Throughout, `<col>` is `Scope.Column(field)` from §2.0.5, and `$n` is the next
bound parameter.

---

#### 2.1.1 `equals`

| | |
| --- | --- |
| Operand shape | scalar |
| Accepted kinds | `Null, Numeric, String, Bool, Date` |
| Arity | 1 |
| Null subject | matches **iff** the operand is null |
| Null operand | allowed |
| Two-valued | yes |

**Postgres, sensitive, operand non-null**

```sql
(<col> IS NOT DISTINCT FROM $1)
```

**Postgres, sensitive, operand null**

```sql
(<col> IS NULL)
```

**Postgres, insensitive, operand is a string**

```sql
(<col> IS NOT NULL AND <col> ILIKE $1 ESCAPE '\')
```

with `$1 = escapeLike(operand)` — **no** `%` wrapping. This is a full-string
case-insensitive match with every wildcard neutralised. See §3.3; this is the one
deliberate divergence from Prisma.

**Postgres, insensitive, operand null** — same as the sensitive null form,
`(<col> IS NULL)`, wrapped in the dialect assertion that insensitive matching is
supported at all.

**SQLite** — `IS NOT DISTINCT FROM` becomes `IS`; insensitive is refused
(§3.4).

**Evaluate**

```
sensitive:   ValuesEqual(value, operand)
insensitive: if both value and operand are strings -> foldASCII(value) == foldASCII(operand)
             otherwise -> ValuesEqual(value, operand)
```

**Null behaviour**

| subject | operand | result |
| --- | --- | --- |
| null | null | `true` |
| null | non-null | `false` |
| non-null | null | `false` |
| non-null | non-null | ordinary equality |

Note the shorthand: a field filter whose value is **not** an object is `equals`.
`{name: "alpha"}` and `{name: {equals: "alpha"}}` are the same node. `{rel: nil}`
on a *relation* field is not `equals`; it is the relation-null form (§2.3.1).

---

#### 2.1.2 `not`

The exact negation of `equals`, including the null cases. It is *not* SQL `<>`.

| | |
| --- | --- |
| Operand shape | scalar |
| Accepted kinds | `Null, Numeric, String, Bool, Date` |
| Arity | 1 |
| Null subject | matches **iff** the operand is not null |
| Null operand | allowed |
| Two-valued | yes |

**Postgres, sensitive, operand non-null**

```sql
NOT (<col> IS NOT DISTINCT FROM $1)
```

**Postgres, sensitive, operand null**

```sql
(<col> IS NOT NULL)
```

**Postgres, insensitive, operand is a string**

```sql
NOT ((<col> IS NOT NULL AND <col> ILIKE $1 ESCAPE '\'))
```

**Evaluate** — the boolean complement of `equals` with the same operand.

**Null behaviour**

| subject | operand | result |
| --- | --- | --- |
| null | null | `false` |
| null | non-null | `true` |
| non-null | null | `true` |
| non-null | non-null | ordinary inequality |

The row "subject null, operand non-null → `true`" is where golem departs from
Prisma most visibly and most often: Prisma emits `col <> $1`, which is `UNKNOWN`
for a null column and therefore excludes the row. Golem includes it. `not` means
"is not equal to", and a null column is not equal to `'alpha'`. See §4.

---

#### 2.1.3 `in`

| | |
| --- | --- |
| Operand shape | scalar list |
| Accepted element kinds | `Numeric, String, Bool, Date` |
| Arity | 1 list |
| Null subject | never matches |
| Null **element** | **rejected at validation** |
| Two-valued | yes |

A `nil` inside the list is a validation error, not a runtime behaviour:

> operator "in" does not accept null in its list: a null makes the SQL predicate
> unknown for every row

`col IN (1, NULL)` is `UNKNOWN` for every row that is not `1`, so the operator
could not be two-valued while accepting one. Refusing at build time is better
than silently dropping the null or silently rewriting the predicate.

**Postgres, sensitive, list non-empty**

```sql
(<col> IS NOT NULL AND <col> IN ($1, $2, …))
```

**Postgres, sensitive, list empty**

```sql
(1 = 0)
```

**Postgres, insensitive, all elements strings, list non-empty**

```sql
(<col> IS NOT NULL AND ((<col> ILIKE $1 ESCAPE '\') OR (<col> ILIKE $2 ESCAPE '\') …))
```

each `$i = escapeLike(element)`.

**Postgres, insensitive, list empty** — `(1 = 0)`.

**Evaluate**

```
Kind(value) != Null && any element e: ValuesEqual(value, e)
insensitive: same with foldedEquals
```

An empty list is `false` for every row, including null rows.

---

#### 2.1.4 `notIn`

| | |
| --- | --- |
| Operand shape | scalar list |
| Accepted element kinds | `Numeric, String, Bool, Date` |
| Arity | 1 list |
| Null subject | **always matches** |
| Null element | rejected at validation |
| Two-valued | yes |

**Postgres, sensitive, list non-empty**

```sql
(<col> IS NULL OR <col> NOT IN ($1, $2, …))
```

**Postgres, sensitive, list empty**

```sql
(1 = 1)
```

**Postgres, insensitive, all elements strings, list non-empty**

```sql
(<col> IS NULL OR NOT ((<col> ILIKE $1 ESCAPE '\') OR (<col> ILIKE $2 ESCAPE '\') …))
```

**Postgres, insensitive, list empty** — `(1 = 1)`.

**Evaluate**

```
Kind(value) == Null || no element e satisfies ValuesEqual(value, e)
```

The `<col> IS NULL OR` branch is the whole point. Prisma emits
`col NOT IN (…)`, which is `UNKNOWN` for a null column and drops the row. A null
`region` is not in `['emea','apac']`, so `notIn` must admit it. Dropping this
branch is mutation **M6** in §7.1 and it is measured against Prisma in §4.

---

#### 2.1.5 `lt`, `lte`, `gt`, `gte`

Four entries with one implementation parameterised by symbol and accept-predicate.

| | |
| --- | --- |
| Operand shape | scalar |
| Accepted kinds | `Null, Numeric, String, Date` — **not** `Bool` |
| Arity | 1 |
| Null subject | never matches |
| Null operand | never matches (accepted, renders `(1 = 0)`) |
| Two-valued | yes |

| Name | Symbol | Accepts |
| --- | --- | --- |
| `lt` | `<` | `sign < 0` |
| `lte` | `<=` | `sign <= 0` |
| `gt` | `>` | `sign > 0` |
| `gte` | `>=` | `sign >= 0` |

**Postgres, sensitive, operand non-null**

```sql
(<col> IS NOT NULL AND <col> < $1)
```

**Postgres, sensitive, operand null**

```sql
(1 = 0)
```

Note: golem *accepts* `{amount: {lt: nil}}` and answers "no rows". Prisma throws
`PrismaClientValidationError`. This is a deliberate divergence (§4): a policy
built from a user attribute that happens to be null must not crash the request,
and "nothing is less than nothing" is the honest answer. All 32 `prisma-rejects`
entries in the measured record are this case and its negation.

**Postgres, insensitive, operand is a string**

```sql
(<col> IS NOT NULL AND lower(<col>) COLLATE "C" < $1)
```

with `$1 = foldASCII(operand)`. `<col>` already carries `COLLATE "C"` from
§2.0.5, so the emitted text is

```sql
("t0"."name" COLLATE "C" IS NOT NULL AND lower("t0"."name" COLLATE "C") COLLATE "C" < $1)
```

Both collations are required and neither is redundant:

- The **inner** one makes `lower()` fold ASCII only. Postgres `lower()` under a
  linguistic collation folds `É` to `é`; the evaluator's `foldASCII` does not.
  Without the inner `COLLATE "C"`, SQL and memory disagree on every non-ASCII
  uppercase letter. This is mutation **M12**.
- The **outer** one makes the resulting comparison byte-ordered, for the same
  reason every other string comparison is (§3).

**Evaluate**

```
sensitive:   sign, ok := Compare(value, operand); ok && accept(sign)
insensitive: value must be a string;
             sign, ok := Compare(foldASCII(value), foldASCII(operand)); ok && accept(sign)
```

**Null behaviour** — never matches, in any combination. A comparison against a
null column is `false`; a comparison against a null operand is `false`; both null
is `false`. Its negation under `NOT` therefore admits the null row, which is what
"not less than 5" should mean for a row with no amount.

**Cross-kind operands.** `{name: {lt: 5}}` passes validation (both `String` and
`Numeric` are in `OrderedKinds`) but `Compare` returns not-comparable for every
row, so the answer is "no rows". In SQL, Postgres would raise a type error on
`text < integer`. **This is an unresolved asymmetry** — see §8. The generated
typed columns are expected to make it unreachable: `Users.Name` only produces
string operands. A Go port that keeps generated constructors as the only way to
build a node inherits that guarantee; a port that also exposes a dynamic builder
must reject cross-kind operands at validation.

---

#### 2.1.6 `contains`, `startsWith`, `endsWith`

| | |
| --- | --- |
| Operand shape | scalar |
| Accepted kinds | `Null, String` |
| Arity | 1 |
| Null subject | never matches |
| Null operand | never matches (accepted, renders `(1 = 0)`) |
| Two-valued | yes |
| Field restriction | `String` scalar or enum only |

The field restriction is enforced at validation when the datamodel is known:

> operator "contains" matches text, and "Parent.count" is a Int column; Prisma
> generates contains, startsWith and endsWith on a String field only, and SQLite
> would answer this by coercing the column to text where the evaluator matches
> nothing at all

Postgres raises `operator does not exist: integer ~~ text` for
`"count" LIKE '%0%'`, so Postgres is safe by accident. SQLite is not: it coerces
the integer to text and answers, while the evaluator's `value.(string)` assertion
fails and answers `false`. Refusing at validation is the only way both engines
mean the same thing.

**Postgres, operand non-empty**

```sql
(<col> IS NOT NULL AND <col> LIKE $1 ESCAPE '\')
```

with the pattern built as:

| Operator | Pattern |
| --- | --- |
| `contains` | `'%' + escapeLike(operand) + '%'` |
| `startsWith` | `escapeLike(operand) + '%'` |
| `endsWith` | `'%' + escapeLike(operand)` |

**Postgres, insensitive** — identical with `ILIKE` in place of `LIKE`.

**Postgres, operand is the empty string**

```sql
(<col> IS NOT NULL)
```

Every string contains, starts with, and ends with `""` — so the operator degrades
to a presence test. Emitting `LIKE '%%'` would be equivalent but the explicit
form documents the intent and keeps the empty case out of the pattern builder.

**Postgres, operand null** — `(1 = 0)`.

**SQLite** — `Dialect.Like` is nil, and the operator renders without `LIKE` at
all, because SQLite's `LIKE` ignores collation and folds ASCII case
unconditionally, which would make a sensitive `contains` insensitive:

```sql
contains:   (<col> IS NOT NULL AND instr(<col>, ?) > 0)
startsWith: (<col> IS NOT NULL AND instr(<col>, ?) = 1)
endsWith:   (<col> IS NOT NULL AND substr(<col>, ?) = ?)
```

The `endsWith` offset parameter is `-len([]rune(operand))` — **code points**, not
bytes and not UTF-16 units, because SQLite's `substr` counts characters. Getting
this wrong breaks on any non-ASCII needle. Insensitive mode is refused entirely on
SQLite (§3.4).

**Evaluate**

```
value must be a string; operand must be a string, else false
sensitive:   strings.Contains / HasPrefix / HasSuffix (value, operand)
insensitive: same over foldASCII(value), foldASCII(operand)
```

The match is on the raw string. No normalisation, no case folding beyond ASCII,
no Unicode equivalence. `"Ångström"` contains `"ström"` and does not contain
`"STRÖM"` even under `mode: insensitive`, because `Ö` is outside ASCII. This is
measured — `text/contains/insensitive/ÅNGSTRÖM` selects no rows while
`text/contains/insensitive/Ström` selects rows 9 and 10.

---

### 2.2 `mode` — and the operator that is not there

`mode` is **not an operator**. It is a sibling key inside a scalar filter object
that changes how the other keys in the same object render.

```
{ name: { contains: "alp", mode: "insensitive" } }
```

Rules:

1. `mode` accepts exactly `"default"` and `"insensitive"`. Anything else is a
   validation error naming the received value.
2. `"default"` is a no-op.
3. `"insensitive"` wraps every operand in the same filter object whose key is one
   of the **eleven folded operators** — `equals, not, in, notIn, lt, lte, gt,
   gte, contains, startsWith, endsWith` — marking it for case-folded rendering.
   `mode` itself is removed from the operator entry list.
4. If the filter also carries a key that is a known operator but is **not** in the
   folded set (`is`, `isNot`, `some`, `every`, `none`), the filter is refused:

   > "mode" set to "insensitive" folds case for equals, not, in, notIn, lt, lte,
   > gt, gte, contains, startsWith, endsWith, and this filter also carries some,
   > which Prisma's string filter does not generate; golem refuses rather than
   > leaving some case-sensitive under a filter that asks for case-insensitive
   > matching

   The reasoning transfers directly: a partially-honoured `mode` is worse than a
   refused one, because the author reads "insensitive" and believes it.

5. Golem folds case for **all eleven**, where Prisma folds only for the string
   operators in its own emitted SQL — but measurement shows Prisma also answers
   `mode: insensitive` beside `equals`, `not`, `in`, `notIn`, `lt`, `gte` (with
   `ILIKE`/`LOWER(` in its emitted SQL), and golem's forced-byte-order rendering
   **agrees with Prisma on every one of those shapes**. Golem accepts every shape
   Prisma accepts; it does not refuse what Prisma answers.

**`search`.** Prisma has a `search` operator (full-text). Golem does not. `search`
is on the reserved-key list so that it is recognised as a filter key rather than
mistaken for a relation shorthand, and it is then refused:

> operator "search" is not supported; supported operators are equals, not, in,
> notIn, lt, lte, gt, gte, contains, startsWith, endsWith, is, isNot, some,
> every, none

`isSet` (Mongo) is handled identically. **Do not implement either.** Full-text
search has no in-memory equivalent that could agree with the database, so it
cannot satisfy the agreement oracle and cannot be a policy predicate.

---

### 2.3 Relation operators

Four forms reach a relation: the bare condition-object shorthand, `is`/`isNot`
for to-one, and `some`/`every`/`none` for to-many. Applying a to-one form to a
to-many relation, or the reverse, is a render error with a message that names the
correct form. Mixing `is`/`isNot` with `some`/`every`/`none` in one filter is a
validation error. Mixing `some`/`every`/`none` with any other key is a validation
error — a to-many relation filter carries only quantifiers, never a bare
condition object on the related model.

#### 2.3.1 To-one: bare shorthand, `is`, `isNot`

| | |
| --- | --- |
| Operand shape | nested conditions, or `nil` |
| Arity | 1 |
| Null subject (`is`, shorthand) | matches iff operand is `nil` |
| Null subject (`isNot`) | matches iff operand is not `nil` |
| Two-valued | yes |

**`{ author: C }` (shorthand) and `{ author: { is: C } }`** render identically:

```sql
(EXISTS (SELECT 1 FROM "users" AS "t0_1"
         WHERE "t0_1"."id" = "t0"."author_id" AND (<C>)))
```

**`{ author: nil }` and `{ author: { is: nil } }`** — "there is no related row":

```sql
NOT (EXISTS (SELECT 1 FROM "users" AS "t0_1"
             WHERE "t0_1"."id" = "t0"."author_id" AND ((1 = 1))))
```

Rendered as a negated `EXISTS` and **not** as `"t0"."author_id" IS NULL`. The
foreign key can be non-null and still dangle if the referenced row was deleted
without a constraint, or if the relation is defined the other way round. The
`EXISTS` asks the question the operator actually means.

**`{ author: { isNot: C } }`** — the outer negation of the `is` form:

```sql
NOT ((EXISTS (SELECT 1 FROM "users" AS "t0_1"
              WHERE "t0_1"."id" = "t0"."author_id" AND (<C>))))
```

**`{ author: { isNot: nil } }`** — double negation, "there is a related row":

```sql
NOT (NOT (EXISTS (SELECT 1 FROM "users" AS "t0_1"
                  WHERE "t0_1"."id" = "t0"."author_id" AND ((1 = 1)))))
```

The double `NOT` is not simplified. Simplification is a rendering optimisation
with no semantic content, and every simplification is a place a bug can hide.

**Empty nested conditions.** `{ author: {} }` and `{ author: { is: {} } }` render
the `EXISTS` with `(1 = 1)` inside — "there is a related row, of any shape".

**Evaluate**

```
isRelationObject(v) := v is a non-nil map/struct that is not a slice,
                       not a time.Time, and not a decimal

operand == nil: !isRelationObject(value)
operand != nil: isRelationObject(value) && nestedPredicate(value)
isNot:          the boolean complement of the above
```

The `isRelationObject` test excludes `time.Time` and decimal types explicitly.
Without those exclusions a `DateTime` column carrying a struct value would be
mistaken for an embedded relation and handed to the nested predicate.

#### 2.3.2 To-many: `some`, `every`, `none`

| | |
| --- | --- |
| Operand shape | nested conditions (an object; `nil` is **rejected**) |
| Arity | 1 |
| Null/absent subject — `some` | never matches |
| Null/absent subject — `every`, `none` | always matches |
| Two-valued | yes |

**`some`**

```sql
(EXISTS (SELECT 1 FROM "comments" AS "t0_1"
         WHERE "t0_1"."post_id" = "t0"."id" AND (<C>)))
```

**`none`**

```sql
NOT (EXISTS (SELECT 1 FROM "comments" AS "t0_1"
             WHERE "t0_1"."post_id" = "t0"."id" AND (<C>)))
```

**`every`**

```sql
NOT (EXISTS (SELECT 1 FROM "comments" AS "t0_1"
             WHERE "t0_1"."post_id" = "t0"."id" AND (((<C>)) IS NOT TRUE)))
```

`IS NOT TRUE` is mandatory. `NOT (<C>)` is mutation **M1** and §1.2 explains what
it grants.

**Empty nested conditions:**

```sql
some:  (EXISTS (… AND (1 = 1)))              -- has at least one related row
every: NOT (EXISTS (… AND (((1 = 1)) IS NOT TRUE)))  -- vacuously true
none:  NOT (EXISTS (… AND (1 = 1)))          -- has no related row
```

**Evaluate**

```
list := value as a slice, or the empty slice if value is nil/not a slice
matches(e) := isRelationObject(e) && nestedPredicate(e)

some:  any e in list satisfies matches(e)
none:  no  e in list satisfies matches(e)
every: all e in list satisfy  matches(e)
```

`every` over an empty list is `true` — the standard vacuous quantifier — and this
matches the SQL, where `NOT EXISTS` over no rows is `TRUE`. `some` over an empty
list is `false`, matching `EXISTS` over no rows. An absent relation (the field was
not loaded, so the Go value is nil) is treated as the empty list. That is
deliberate and it is a hazard: the caller must load the relation before evaluating
in memory, or `every` silently succeeds. The SQL side never has this problem
because the `EXISTS` reaches the table directly. See §7.2 for the oracle
requirement that in-memory rows are loaded with the relations the corpus touches.

---

### 2.4 Scalar-list operators

These filter a **list column** (`String[]`, `Int[]`, …), not a relation. They are
a separate registry from the scalar operators, keyed by the same names where they
collide (`equals` exists in both).

**Postgres only.** Every one of them renders through a guard:

```
if dialect is not postgres:
    render error: operator "has" filters the scalar list column "tags", and the
    sqlite provider has no scalar list columns: Prisma refuses a list field on
    sqlite at the schema level, so this policy cannot be answered here
```

This is a *render*-time refusal, not a validation-time one, because whether the
policy is answerable depends on the engine, and the same condition tree may be
valid against a Postgres deployment.

**Exactly one operator per list filter.** `{tags: {}}` and
`{tags: {has: "a", isEmpty: false}}` are both validation errors:

> the filter on list column "Post.tags" carries has, isEmpty; a scalar list
> filter carries exactly one of has, hasEvery, hasSome, isEmpty, equals, and
> Prisma refuses both an empty filter and two operators at once

Applying a scalar-list operator to a non-list field, or a scalar operator to a
list field, is a validation error naming the position of the field.

`<lcol>` below is `Scope.ListColumn(field)` — no collation (§2.0.5).

#### 2.4.1 `has`

| | |
| --- | --- |
| Operand shape | scalar |
| Accepted kinds | `Null, Numeric, String, Bool, Date` |
| Null subject (null array) | never matches |
| Null operand | never matches |

```sql
COALESCE($1 = ANY(<lcol>), FALSE)
```

operand null:

```sql
(1 = 0)
```

The `COALESCE` is the two-valued guard. `x = ANY(NULL::text[])` is `NULL`, not
`FALSE`; a row whose array column is `NULL` would be `UNKNOWN` without it. This is
mutation **M14**.

**Evaluate** — `value` must be a slice; the operand must be non-null; `true` iff
some element satisfies `ValuesEqual(element, operand)`.

#### 2.4.2 `hasEvery`, `hasSome`

| | |
| --- | --- |
| Operand shape | scalar list |
| Accepted element kinds | `Numeric, String, Bool, Date` |
| Null element | **rejected at validation** |
| Null subject (null array) | never matches |

> operator "hasEvery" does not accept null in its list: Prisma types a scalar
> list filter as T[], and Postgres containment never finds a null element

**Non-empty operand list:**

```sql
hasEvery: (COALESCE($1 = ANY(<lcol>), FALSE) AND COALESCE($2 = ANY(<lcol>), FALSE))
hasSome:  (COALESCE($1 = ANY(<lcol>), FALSE) OR  COALESCE($2 = ANY(<lcol>), FALSE))
```

**Empty operand list:**

```sql
hasEvery: (<lcol> IS NOT NULL)
hasSome:  (1 = 0)
```

`hasEvery: []` is the vacuous "contains all of nothing" — true for every row that
*has* an array, false for a row whose array column is null. `hasSome: []` is
"contains at least one of nothing" — false everywhere.

**Evaluate** — `value` must be a slice, else `false`; then `every`/`some` over the
operand elements using the `has` element test.

#### 2.4.3 `isEmpty`

| | |
| --- | --- |
| Operand shape | flag |
| Accepted kinds | `Bool` only |
| Null subject (null array) | never matches |
| Null operand | rejected |

```sql
isEmpty: true   -> (<lcol> IS NOT NULL AND cardinality(<lcol>) = 0)
isEmpty: false  -> (<lcol> IS NOT NULL AND cardinality(<lcol>) > 0)
```

A non-boolean operand is a validation error naming the received value. Note that
a `NULL` array is neither empty nor non-empty: it fails both. This is deliberate —
`cardinality(NULL)` is `NULL`, and the presence guard turns that into `FALSE`
rather than letting `isEmpty: true` quietly claim a null column.

**Evaluate** — `value` must be a slice, else `false`; then `len == 0` or `len > 0`.

#### 2.4.4 `equals` (list form)

| | |
| --- | --- |
| Operand shape | scalar list, or `nil` |
| Accepted element kinds | `Numeric, String, Bool, Date` |
| Null subject | matches iff operand is `nil` |
| Null operand | allowed |

**Operand `nil`:**

```sql
(<lcol> IS NULL)
```

**Operand `[a, b]`:**

```sql
(<lcol> IS NOT NULL AND cardinality(<lcol>) = 2
 AND <lcol>[1] IS NOT DISTINCT FROM $1
 AND <lcol>[2] IS NOT DISTINCT FROM $2)
```

Position-wise, one-based, with null-safe element equality — **not** `<lcol> = $1`
with an array parameter. Two reasons: array literal binding differs per driver and
per element type, and array `=` in Postgres compares element-wise under the
element type's own equality, which for text is collation-sensitive. Rendering the
comparison explicitly keeps it byte-ordered and keeps element nulls two-valued.

**Empty operand list** renders `(<lcol> IS NOT NULL AND cardinality(<lcol>) = 0)`.

**Evaluate** — `value` must be a slice of the same length, with `ValuesEqual` at
every index. Order matters: `["a","b"]` does not equal `["b","a"]`.

**Disambiguation without a datamodel.** When the field's position is unknown
(no datamodel in context), a filter whose only key is `equals` with an array
operand is treated as a list filter. This is the one heuristic in the system and
it exists only for the context-free path; with a datamodel the field's `isList`
flag decides.

---

### 2.5 Json path filters

A `Json`-typed field is filtered by a **Json filter object**, which is a different
grammar from a scalar filter. There is no bare-value shorthand:
`{meta: 5}` is a validation error.

Keys: `path`, `mode`, and twelve operators:

```
equals, not, lt, lte, gt, gte,
string_contains, string_starts_with, string_ends_with,
array_contains, array_starts_with, array_ends_with
```

Any other key is refused, naming the supported set. Multiple operators in one
filter are **conjoined**: `(n1 AND n2 AND …)`; a single operator renders bare.
A filter with `path` but no operator renders `(1 = 1)` and evaluates `true`.

#### 2.5.1 `path`

Two spellings, one per engine, and golem will not translate between them:

- **Postgres** — an array of string segments: `["a", "b"]`, `["list", "0"]`. A
  non-string element is a validation error.
- **SQLite** — a JSONPath string rooted at `$`, built from `.name`, `."quoted
  name"` and `[0]` steps: `"$.a.b"`, `"$.list[1].c"`. Anything golem cannot parse
  is a validation error rather than a guess.

Supplying the wrong form for the dialect is a **render** error, on the grounds
that Prisma's client for that engine rejects the other form too.

Absent `path` means the whole document.

**Postgres rendering** of the addressed value:

```sql
path absent or empty:  <col>
otherwise:             (<col> #> ARRAY[$1, $2, …]::text[])
```

**Navigation in memory** — walk the segments:

- A segment whose text is all digits carries an integer index as well.
- Against a slice: the segment must have an index, and the index must be in range,
  else the result is **absent**.
- Against a map: the key must be present, else **absent**.
- Against a JSON `null` or an already-absent slot: **absent**.
- Against a scalar: **absent**.

**Absent is a third state, distinct from JSON `null`.** The Go type is
effectively `JsonSlot = JsonValue | Absent`. This is not the same as the
two-valued invariant, which is about SQL truth values; `Absent` is a *value*, and
every operator maps it to a definite boolean.

#### 2.5.2 Null sentinels

`equals` and `not` accept three sentinels, matching Prisma's `DbNull`, `JsonNull`
and `AnyNull`. In Go, model them as three distinct singleton values recognised by
identity, not by string matching on user data.

| Sentinel | Means | Postgres `equals` | Evaluate `equals` |
| --- | --- | --- | --- |
| `DbNull` | the column (or the addressed slot) is SQL `NULL` / absent | `(<v> IS NULL)` | `slot == Absent` |
| `JsonNull` | the addressed value is the JSON literal `null` | `(<v> IS NOT NULL AND <typeIs null>)` | `slot == JSON null` |
| `AnyNull` | either | `((<v> IS NULL) OR <typeIs null>)` | `slot == Absent \|\| slot == null` |

`not` with a sentinel:

| Sentinel | Postgres `not` | Evaluate `not` |
| --- | --- | --- |
| `DbNull` | `(<v> IS NOT NULL)` | `slot != Absent` |
| `JsonNull` | `(<v> IS NOT NULL AND NOT <typeIs null>)` | `slot != Absent && slot != null` |
| `AnyNull` | `(<v> IS NOT NULL AND NOT <typeIs null>)` | `slot != Absent && slot != null` |

#### 2.5.3 The type guard

Every Json operator except the sentinel forms is guarded by a type test:

```sql
typeIs(v, T) := (jsonb_typeof(<v>) IS NOT NULL AND jsonb_typeof(<v>) IN ('string'))
```

The `IS NOT NULL` conjunct is the two-valued guard: `jsonb_typeof(NULL)` is
`NULL`, and `NULL IN ('string')` is `UNKNOWN`. Dropping it is mutation **M26**.

Type name sets are dialect data, because SQLite spells them differently:

| Golem type | Postgres `jsonb_typeof` | SQLite `json_type` |
| --- | --- | --- |
| null | `null` | `null` |
| boolean | `boolean` | `true`, `false` |
| number | `number` | `integer`, `real` |
| string | `string` | `text` |
| array | `array` | `array` |
| object | `object` | `object` |

#### 2.5.4 `equals` / `not` against a value

Postgres normalises documents (`jsonb` has a canonical form), so the comparison is
against a whole document:

```sql
equals: (<v> IS NOT NULL AND <v> = $1::jsonb)
not:    (<v> IS NOT NULL AND NOT (<v> IS NOT NULL AND <v> = $1::jsonb))
```

`$1` is `json.Marshal(operand)`. Object key order and number spelling do not
matter, because `jsonb` normalises both.

On an engine that does **not** normalise (SQLite), comparing a JSON object or
array is **refused**:

> operator "equals" was given a JSON object to compare, and sqlite compares JSON
> documents as text: object key order and number formatting would decide the
> answer, so a row storing `{"b":1,"a":2}` would not equal the operand
> `{"a":2,"b":1}` that the evaluator calls equal. Compare a scalar at a "path"
> instead

and a scalar comparison falls back to extracting text and comparing:

```sql
(typeIs(v, T) AND <text(v)>[ COLLATE "C" if T is string] = $1)
```

**Evaluate**

```
slot == Absent -> false
otherwise      -> jsonEquals(slot, operand)   (negated for `not`)
```

`jsonEquals` is structural: same type; arrays equal element-wise **in order**;
objects equal iff the same key set and every value equal; scalars by Go `==` on
the decoded JSON representation. Note the asymmetry with `Compare`: JSON numbers
are compared as decoded `float64`, so a document number of `1` does not equal a
document string of `"1"`, and two numbers that decode to the same `float64` are
equal. That is why the validator refuses operands beyond `2^53` — see §2.5.9.

#### 2.5.5 `lt`, `lte`, `gt`, `gte`

Only available where the dialect declares ordering support (Postgres yes, SQLite
no — a refusal naming the engine). Operand must be a JSON **number** or **string**;
any other JSON type is a validation error:

> operator "lt" orders a JSON number against a number or a JSON string against a
> string, and golem will not order a JSON object: SQL orders documents by a rule
> the evaluator cannot reproduce

**String operand**

```sql
(typeIs(v, 'string') AND (<v> #>> ARRAY[]::text[]) COLLATE "C" < $1)
```

**Number operand**

```sql
(typeIs(v, 'number') AND <v> < $1::jsonb)
```

**Evaluate** — the slot must be present and of the same JSON type as the operand,
then a plain `<`/`<=`/`>`/`>=` on the decoded `float64` or `string`.

#### 2.5.6 `string_contains`, `string_starts_with`, `string_ends_with`

```sql
(typeIs(v, 'string') AND (<v> #>> ARRAY[]::text[]) COLLATE "C" LIKE $1 ESCAPE '\')
```

Empty operand renders the bare type guard `typeIs(v, 'string')`. Operand must be
a string; anything else is a validation error.

`mode: "insensitive"` swaps `LIKE` for `ILIKE`, and applies **only** to these
three. If `mode: insensitive` appears beside any other Json operator, the filter
is refused for the same reason as §2.2 rule 4.

**Evaluate** — the slot must be a string; substring/prefix/suffix, with
`foldASCII` on both sides under insensitive mode.

#### 2.5.7 `array_contains`

Only where the dialect declares containment support (Postgres yes, SQLite no).

```sql
(typeIs(v, 'array') AND (<v> @> $1::jsonb))
```

**Evaluate** — `jsonContains(slot, candidate)`:

```
if slot is an array and candidate is a scalar (not array, not object):
        any element e of slot satisfies containsDeep(e, candidate)
else    containsDeep(slot, candidate)

containsDeep(target, candidate):
  both arrays  -> every element of candidate is containsDeep-matched by
                  some element of target
  both objects -> every key of candidate is present in target and
                  containsDeep-matches
  otherwise    -> jsonEquals(target, candidate)
```

This mirrors Postgres `@>` including its two surprises: an array contains a bare
scalar if any element matches, and `[[1,2]] @> [1]` is **false** while
`[[1,2]] @> [[1]]` is **true**.

#### 2.5.8 `array_starts_with`, `array_ends_with`

```sql
(typeIs(v, 'array') AND <documentEquals(child, operand)>)
```

where `child` is `(<v> -> 0)` for `starts_with` and `(<v> -> -1)` for
`ends_with`, and `documentEquals` is §2.5.4 applied to that child.

**Evaluate** — the slot must be a non-empty array; take the first/last element and
`jsonEquals` it against the operand. An empty array matches neither.

#### 2.5.9 Operand validation for Json

Beyond shape, the validator refuses operands that have no faithful JSON
representation:

- Non-finite numbers (`NaN`, `±Inf`).
- Any number with `|v| > 2^53 - 1`, with this message:

  > the number 9007199254740993 is beyond 9007199254740991, where a JSON document
  > and a JavaScript number stop agreeing: the database compares the digits it
  > stored, and the evaluator compares the nearest double, so 9007199254740992
  > and 9007199254740993 are two documents on the server and one value in memory.

  **This reasoning survives the port to Go.** Go's `encoding/json` decodes into
  `float64` by default, so the same collision exists unless the decoder uses
  `json.Number`. If the Go port decodes with `UseNumber()` and compares numbers
  exactly (§2.0.4), it *may* relax this restriction — but relaxing it changes the
  measured answers, so it must be a deliberate, recorded decision with its own
  oracle coverage, not an accident of using a different decoder.

- Any value that is not JSON-representable — times, structs, channels — recursed
  through arrays and objects.

#### 2.5.10 Json and the two-valued suite

**The Json operators are not currently covered by the two-valued probe suite.**
The suite enumerates the scalar registry and the scalar-list registry only. Every
Json rendering above *is* two-valued by inspection (each is a conjunction rooted
at a presence or type guard), and the Json agreement suites check SQL-versus-memory
answers — but nothing measures unknown-count for a Json fragment. The Go port
should extend the probe corpus to the Json registry; see §7.1 mutation **M26** and
§8.

---

### 2.6 Combinators

Three combinators, each accepting either a single condition object or an array of
them. They are not operators — they sit at the condition-object level, beside
field names, not inside a field filter.

| Combinator | Empty | Non-empty SQL | Evaluate |
| --- | --- | --- | --- |
| `AND` | `(1 = 1)` | `(<a> AND <b> AND …)` | all branches true |
| `OR` | `(1 = 0)` | `(<a> OR <b> OR …)` | any branch true |
| `NOT` | `(1 = 1)` | `NOT (<a> OR <b> OR …)` | **no** branch true |

`NOT` over an array is a **NOR**, not a list of negations: `NOT: [A, B]` means
"neither A nor B". That is De Morgan applied to Prisma's shape, and it is the
same thing the evaluator computes (`every branch is false`).

A condition object with several field keys is a conjunction of those keys. A field
filter with several operator keys is a conjunction of those operators. In both
cases a single node renders bare and two or more render inside parentheses joined
by ` AND `.

An empty condition object `{}` renders `(1 = 1)` and evaluates `true`.

**Empty `AND` and empty `NOT` are the three `prisma-superset` divergences.**
Prisma answers `{NOT: {AND: []}}` with every row; golem answers with none,
because golem's empty `AND` is `TRUE` and negating it is `FALSE`. Golem is
strictly more conservative here, which for a policy is the safe direction: a
degenerate rule denies rather than grants.

---

## 3. String comparison and collation

### 3.1 The rule

> Golem compares strings by **byte order on every engine**, and treats
> `mode: "insensitive"` as the explicit opt-in for case folding.

Mechanically: every `String` column reference is emitted as
`"alias"."col" COLLATE "C"`, every enum column as
`CAST("alias"."col" AS TEXT) COLLATE "C"`, and both sides of a relation
correlation on a string key carry the same collation.

### 3.2 Why

Prisma emits a bare comparison and lets the server's collation decide. On a
Postgres created with a linguistic collation — which is the default on most
managed providers, `en_US.UTF-8` — `'alpha' < 'Zulu'` is `TRUE`, because a
linguistic collation orders case-insensitively at the primary strength and puts
`a` before `Z`. On a `C`-collation server the same expression is `FALSE`, because
`'a'` is `0x61` and `'Z'` is `0x5A`.

For a policy this is not a cosmetic difference. **A linguistic-collation Postgres
is more permissive than the rule intends.** A rule "the tenant slug must be less
than `M`" written to fence off half the estate matches a different half — and a
larger one — depending on the database's locale, which the policy author never
saw and cannot see from the rule.

It is also unrepeatable. The measured record contains sixteen cases whose answer
changes with the server's collation family. `name/lt/'Zulu'` selects
`1,2,3,5,6,7,8` on a linguistic server and `2,5` on a byte-order server. The same
policy, the same rows, two answers.

And it makes the two backends impossible to reconcile. The in-memory evaluator
compares Go strings, which is byte order. There is no collation-aware comparison
available in memory that would reproduce a specific server's ICU version and
locale, and even if there were, it would change under the server's next minor
upgrade. Forcing `COLLATE "C"` is what makes SQL and memory the same function.

The suite proves the necessity: it runs the whole matrix against **two** Postgres
servers, one `C` and one linguistic, and asserts that every answer is identical
across them, and that the unforced form differs from the forced form on the
linguistic server exactly when the server is linguistic.

**Go note.** Go's `<` on `string` compares the underlying UTF-8 bytes, which is
exactly Postgres `C` collation. This is *better* than the TypeScript original,
which compares UTF-16 code units and therefore orders an astral-plane character
(U+1F44D, `👍`) *before* U+E000, where UTF-8 byte order puts it after. That is a
latent divergence in the TypeScript that the current corpus does not reach; the
Go implementation gets it right by doing the obvious thing. **Do not** reach for
`strings.Compare` on decoded runes or for `golang.org/x/text/collate`.

### 3.3 How insensitivity is rendered

`mode: "insensitive"` folds **ASCII case only**, on both sides, in both backends.

```
foldASCII(s): for each byte b in s, if 'A' <= b <= 'Z' then b+32 else b
```

A byte-level loop is correct and safe for UTF-8: bytes in the ASCII range never
appear inside a multi-byte sequence. Do **not** use `strings.ToLower`, which
folds Unicode: `É` would become `é` in memory while Postgres under `COLLATE "C"`
would leave it alone, and the two backends would part company.

Per operator family:

| Family | Insensitive rendering |
| --- | --- |
| `equals` | `(<col> IS NOT NULL AND <col> ILIKE $1 ESCAPE '\')`, `$1 = escapeLike(operand)` |
| `not` | `NOT ((<col> IS NOT NULL AND <col> ILIKE $1 ESCAPE '\'))` |
| `in` | `(<col> IS NOT NULL AND (<ILIKE₁> OR <ILIKE₂> …))` |
| `notIn` | `(<col> IS NULL OR NOT (<ILIKE₁> OR <ILIKE₂> …))` |
| `lt/lte/gt/gte` | `(<col> IS NOT NULL AND lower(<col>) COLLATE "C" <op> $1)`, `$1 = foldASCII(operand)` |
| `contains/startsWith/endsWith` | the sensitive form with `ILIKE` for `LIKE` |

Two things make `ILIKE` and `lower()` fold ASCII only rather than Unicode: the
column already carries `COLLATE "C"`, and both operations take their case rules
from the operand collation. That is why the collation must be applied at the
column reference and not, say, wrapped around the finished comparison. Measured
confirmation: `{text: {equals: "ÅNGSTRÖM", mode: insensitive}}` selects no rows
while `{text: {equals: "Ångström", mode: insensitive}}` selects the row — the `Å`
and `Ö` are not folded, exactly as `foldASCII` does not fold them.

### 3.4 Engines with no case-insensitive match

If the dialect declares no `LIKE` support, or `LIKE` support with no insensitive
variant, every insensitive rendering **refuses**:

> mode "insensitive" is not supported on sqlite, which offers no case-insensitive
> match golem can force onto byte order; folding case any other way would not
> agree with the evaluator

The refusal is a render error and it fires for every folded operator, including
the degenerate cases (`in: []`, `equals: nil`) where the rendered SQL contains no
comparison at all. This is deliberate: a policy that asks for insensitive matching
must not silently become sensitive because the operand happened to be empty. The
suite asserts that *every* insensitive probe throws on SQLite.

### 3.5 The insensitive `equals` escape — a deliberate divergence

Prisma renders `{ equals: x, mode: 'insensitive' }` as a bare `ILIKE x`, with no
escaping and no `ESCAPE` clause. `%` and `_` in `x` are therefore **wildcards**:
`{ equals: '100%', mode: 'insensitive' }` matches `'100'`, `'100 percent'`, and
`'1000'` — anything beginning `100`.

Golem escapes the operand. `{ equals: '100%', mode: 'insensitive' }` matches the
literal string `100%` and nothing else. This is the one place golem deliberately
does not match Prisma.

The reasoning: **a policy author writing `100%` means the literal string.** A
filter is a convenience; a policy is a security boundary. In a filter a stray
wildcard returns some extra rows and someone notices. In a policy a stray wildcard
grants a permission over a set the author did not describe, and nobody notices
until it matters. Prisma's shape is a leaked implementation detail of choosing
`ILIKE` as the case-folding mechanism, not an intended feature — no part of
Prisma's typed API says `equals` takes a pattern.

The measured record confirms the divergence is real and narrow:
`text/equals/insensitive/100%` selects only the row whose text is exactly `100%`.

---

## 4. Deliberate divergences from Prisma

The policy suite measures golem against a live Prisma client on a live Postgres
across a matrix of 8 columns × 3–4 operands × 7 operator shapes, plus relation and
combinator cases, plus every case again under `NOT`. It records both the exact
per-case difference and a classification.

Measured classes on a byte-order server:

| Class | Count | Meaning |
| --- | --- | --- |
| `golem-superset` | 110 | Prisma's rows are a strict subset of golem's — golem admits rows Prisma drops |
| `prisma-rejects` | 32 | Prisma throws; golem answers |
| `prisma-superset` | 3 | golem's rows are a strict subset of Prisma's — golem is stricter |

145 recorded differences in total. On a linguistic-collation server a further 16
collation-family cases are recorded separately. There are **no** `disjoint` cases
and **no** `golem-rejects` cases: golem never refuses something Prisma answers,
and the two never select overlapping-but-incompatible sets.

### 4.1 The table

| # | Case | Prisma | Golem | Reasoning |
| --- | --- | --- | --- | --- |
| 1 | `{col: {not: v}}` on a nullable column | emits `col <> $1`; a null row is `UNKNOWN`, dropped | `NOT (col IS NOT DISTINCT FROM $1)`; a null row matches | A null column is not equal to `v`. `not` is the exact complement of `equals`, so that `NOT{equals}` and `{not}` cannot drift apart under composition. 4 columns × 3 operands = the bulk of `golem-superset`. |
| 2 | `{col: {notIn: [a,b]}}` on a nullable column | `col NOT IN (…)`; null dropped | `(col IS NULL OR col NOT IN (…))`; null matches | Same rule. A null region is not in the list. |
| 3 | `{col: {lt: nil}}` (and `lte`, `gt`, `gte`) | throws `PrismaClientValidationError` | renders `(1 = 0)`, matches nothing | A policy is built from subject attributes at request time; one of them being null must not turn into a 500. "Nothing is less than nothing" is the honest answer, and it is two-valued. All 32 `prisma-rejects` are this and its negation. |
| 4 | `NOT {col: {lt: v}}` on a nullable column | inherits three-valued `UNKNOWN`, so the null row is in neither the rule nor its negation | the null row is in the negation | §1.2 consequence B. |
| 5 | `{col: {contains: v}}` where `col` is null | in `@casl/prisma`'s in-memory matcher, **threw** | non-match | A null column is not a string containing `v`. Throwing from `ability.can()` on ordinary data is not a defensible behaviour. Recorded in the 0.6.0 release notes as a fix. |
| 6 | `{col: {lt: v}}` in memory where `col` is null | `lt` matched through JavaScript coercion (`null < 5` is `true`) while `gte` did not | never matches, in either direction | Coercion made the operator set internally inconsistent. |
| 7 | string ordering | bare comparison; the server's collation decides | `COLLATE "C"` forced | §3.2. 16 cases change answer with the server's collation under Prisma; golem's answers are identical on both. |
| 8 | `{col: {equals: v, mode: insensitive}}` | bare `ILIKE v`; `%` and `_` are wildcards | `ILIKE escapeLike(v) ESCAPE '\'` | §3.5. **The only case where golem is deliberately not Prisma-compatible on a shape Prisma answers.** |
| 9 | `{NOT: {AND: []}}`, `{NOT: {NOT: []}}`, `{NOT: {}}` | every row | no rows | Golem's empty `AND` is `TRUE`, so its negation is `FALSE`. Strictly more conservative; a degenerate rule denies. The 3 `prisma-superset` cases. |
| 10 | `{col: {search: …}}`, `{col: {isSet: …}}` | answers (Postgres FTS / Mongo) | refused at validation | No in-memory equivalent can agree with the database, so it cannot satisfy the oracle. |
| 11 | `contains`/`startsWith`/`endsWith` on a non-`String` column | answers on SQLite by coercing to text | refused at validation | §2.1.6. Postgres refuses it too, but only by accident of type checking. |
| 12 | anything golem cannot render exactly | approximates | refused when the ability is built | The whole design stance. A rule that previously passed a condition golem could not express now fails on the first request from a user whose rules include it, naming the rule and the operator. |

### 4.2 What the divergences have in common

Every `golem-superset` entry is a null row that Prisma drops and golem admits.
That is one rule stated in §5, applied consistently. Every `prisma-rejects` entry
is a null operand where Prisma throws. Every `prisma-superset` entry is a
degenerate empty combinator where golem denies. There is no case where golem is
*more permissive* than Prisma on a non-null row.

That asymmetry is the point. Golem's differences all move in a direction that a
policy author can reason about: null is a value, degenerate rules deny, and
comparison does not depend on the database's locale.

---

## 5. Null semantics

Stated as rules. Examples belong in the tests.

**N1.** Null is an ordinary value that fails to equal things. It is not an error,
not a wildcard, and not a missing datum. `equals: nil` matches null and nothing
else; `not: nil` matches everything except null.

**N2.** In memory, an absent field and a nil field are the same thing. Reading a
field off a row that does not carry it yields the null value. There is no
"undefined" behaviour distinct from null anywhere in the operator table. (The Json
`Absent` slot in §2.5.1 is a *different* concept, internal to Json navigation, and
it is distinguished from JSON `null` only by the three sentinels.)

**N3.** A comparison never matches null in either direction. `lt`, `lte`, `gt`,
`gte` are `false` when the subject is null, `false` when the operand is null, and
`false` when both are null. There is no ordering position for null: it is not
before everything and not after everything.

**N4.** A text match against null is a non-match, not an error. `contains`,
`startsWith`, `endsWith` are `false` when the subject is null and `false` when the
operand is null. This is a behaviour change from the CASL matcher golem replaced,
which threw.

**N5.** `in` never matches null; `notIn` always matches null. These are exact
complements over the same list, so `NOT{in}` and `{notIn}` select the same rows.

**N6.** A null inside a *list operand* is refused at validation, for `in`,
`notIn`, `hasEvery`, `hasSome`. It cannot be given a two-valued meaning in SQL
and dropping it silently would change the predicate.

**N7.** A null **list column** is neither empty nor non-empty, contains nothing,
and has no elements. `isEmpty: true`, `isEmpty: false`, `has`, `hasEvery`,
`hasSome` are all `false` against it. Only `equals: nil` matches it.

**N8.** An absent to-one relation matches `is: nil` and `isNot: C`; it fails
`is: C`. An empty or absent to-many relation matches `every` and `none`
vacuously and fails `some`.

**N9.** Every rendering of every rule above is two-valued. A null never produces
`UNKNOWN`; it produces `FALSE` (or `TRUE`, for the operators whose null branch is
explicit). §1.

**N10.** The two backends agree on all of the above. Anywhere the in-memory rule
and the SQL rule could differ on a null, the SQL carries an explicit guard rather
than relying on the engine's default.

---

## 6. Value coercion and binding

### 6.1 Binding a Go value as a SQL parameter

`toParameter(v)` maps a Go value to a bound parameter, or fails:

| Go value | Bound as | Notes |
| --- | --- | --- |
| `nil` | SQL `NULL` | |
| `string` | text | |
| signed/unsigned integers | `int64` | fits by construction for Go's own integer types |
| `float64` | double | see §6.2 |
| `bool` | boolean | on SQLite, `1`/`0` |
| `time.Time` | timestamp | on SQLite, the RFC3339 string with milliseconds |
| `*big.Int` beyond `int64` | numeric | bind through a type the driver maps to `numeric`, **never** through `float64` |
| decimal | its exact decimal **string** | the TypeScript binds `String(decimal)` and lets the server infer `numeric` from the comparison |
| anything else | **render error** | `value of type X cannot be bound as a SQL parameter` |

The render error is deliberate and must be kept. Silently stringifying an unknown
type produces a comparison that succeeds against a text column and fails against
every other, which is the worst possible failure mode for a policy.

### 6.2 The traps

**Never route a comparison through `float64`.** Two adjacent integers above
`2^53` are the same `float64`. The corpus contains `9007199254740992` and
`9007199254740993` precisely to catch this, in both directions:

- In memory, `Compare` must be exact (§2.0.4). `big.Rat` or a decimal-string
  comparison. Not `float64(a) < float64(b)`.
- On the wire, a bigint must reach the server as digits, not as a double.

**Never round-trip a bigint or a decimal through JSON.** This is the rule that
costs the most to rediscover. It applies on the **read** side, not inside the
predicate, and the mechanism is:

1. Golem's compiled reads project nested relations as aggregated JSON, decoded in
   the client. `encoding/json` decodes a JSON number into `float64` by default.
   `9007199254740993` becomes `9007199254740992` on the way through.
2. Drivers also differ on how they hand back `int8` and `numeric`; some give a
   double, some a string, some a driver-specific type.

The rule:

> **Every column whose logical kind is `bigint` or `decimal` is selected as
> `CAST(<col> AS TEXT)` and decoded back from the digit string.**

Where it applies:

- Every root-level projected column of a golem-compiled read whose kind is
  `bigint` or `decimal`.
- Every aggregate measure whose decode kind is `bigint` or `decimal`
  (`_sum` over an integer column, and every measure over a decimal column).
- Every such column inside a nested relation projection, because those go through
  JSON.

Where it does **not** apply:

- Inside a predicate. `("t0"."big" IS NOT DISTINCT FROM $1)` compares in the
  database, at the column's own type, with no round trip. Casting the column to
  text there would defeat the index and change the comparison from numeric to
  lexical (`"10" < "9"`).

Decode direction: `bigint` ← `big.Int` parsed from the digit string;
`decimal` ← the decimal type constructed from the exact string, never from a
float. A count that must become a plain integer is checked against the safe range
and **errors** rather than truncating:

> Integer value in column 'hits' is too large to represent as a JavaScript number
> without loss of precision, got: … Consider using BigInt type.

In Go the equivalent boundary is `int` / `int64`; keep the explicit range check
and the error rather than letting the conversion wrap.

**Date operands.** A `time.Time` binds as a timestamp. In memory, `Compare` will
also parse a **string** operand as an instant when the other side is a date, so
`{at: {lt: "2020-01-01T00:00:00.000Z"}}` compares against a `DateTime` column.
The TypeScript uses `Date.parse`, which accepts a wide and loosely-specified set
of formats. **The Go port must fix the accepted set explicitly** — RFC3339 with
optional fractional seconds is the recommended floor — and the oracle must cover
it, because the SQL side binds whatever string it is given and lets Postgres parse
it with *its* rules. See §8.

**Booleans.** A boolean operand only ever compares to a boolean subject (§2.0.2
rule 2). Do not let a `bool` reach the numeric path.

**Enums.** An enum column is cast to text and collated before comparison
(§2.0.5), so an enum operand is bound as its string name. This is what makes
`{status: {in: ["ACTIVE","TRIAL"]}}` work without knowing the enum's Postgres OID.

---

## 7. Acceptance criteria

### 7.1 Named mutations

Each entry names a change to the implementation and states what must fail. An
implementer should be able to apply each one and watch the named suite go red. A
mutation that does not bite means the suite is not covering the property, and the
suite is what is wrong.

| # | Mutation | Must fail |
| --- | --- | --- |
| **M1** | Render `every` as `NOT (<C>)` instead of `NOT (… (<C>) IS NOT TRUE)` | The two-valued suite's polarity check on rows with a null in the relation; the production-shaped `group/every-access-allow` case, which must select `4,6` and would select `2,4,5,6` |
| **M2** | Drop the presence guard from `lt`/`lte`/`gt`/`gte`: render `<col> < $1` | The two-valued unknown-count probe: rows 2 and 4 (null `amount`) appear as `UNKNOWN` |
| **M3** | Render `equals` as `<col> = $1` | Two-valued unknown-count on the null-column probes; the `equals/null` probe stops matching |
| **M4** | Render `not` as `<col> <> $1` | Two-valued unknown-count; the agreement oracle on every nullable column; the recorded `golem-superset` count drops |
| **M5** | Drop the presence guard from `in` | Two-valued unknown-count on `in/two` |
| **M6** | Drop the `<col> IS NULL OR` branch from `notIn` | Two-valued unknown-count; `notIn` stops matching null rows; the measured Prisma divergence for `notIn` disappears |
| **M7** | Render `in: []` as `(1 = 1)` | Agreement oracle: SQL selects everything, memory selects nothing |
| **M8** | Render `notIn: []` as `(1 = 0)` | Same, mirrored |
| **M9** | Drop `escapeLike` and the `ESCAPE` clause from `contains`/`startsWith`/`endsWith` | The string corpus: `text/contains/100%` must select exactly the `100%` row, `text/contains/%` must select the three rows containing a literal `%` |
| **M10** | Escape nothing in insensitive `equals` (adopt Prisma's shape) | `text/equals/insensitive/100%` must select only the literal row; §3.5 |
| **M11** | Drop `COLLATE "C"` from string and enum column references | The paired-collation suite: the linguistic server and the `C` server return different answers, and the linguistic server disagrees with the in-memory evaluator |
| **M12** | Render insensitive ordering as `lower(<col>)` without the inner `COLLATE "C"` | Any corpus row with a non-ASCII uppercase letter (`ÉCOLE`, `Ångström`): SQL folds it, memory does not |
| **M13** | Render `contains: ""` as `(1 = 1)` instead of `(<col> IS NOT NULL)` | Agreement oracle on the nullable text column: SQL admits the null rows, memory does not |
| **M14** | Render `has` as `$1 = ANY(<lcol>)` without `COALESCE(…, FALSE)` | Two-valued unknown-count for `has/tag` on the row whose array column is `NULL` |
| **M15** | Drop the presence guard from `isEmpty` | Two-valued unknown-count for `isEmpty/true` and `isEmpty/false` |
| **M16** | Render list `equals` element comparisons as `<lcol>[i] = $i` | Two-valued unknown-count for a list containing a null element |
| **M17** | Render `{rel: nil}` as `<fkcol> IS NULL` instead of the negated `EXISTS` | The oracle on a fixture with a dangling foreign key |
| **M18** | Swap `some` and `none` (or drop the `NOT` from `none`) | The oracle on every to-many case |
| **M19** | Render `NOT: [A, B]` as `(NOT A AND …)` per-branch rather than `NOT (A OR B)` | The combinator cases; also the in-memory/SQL agreement, since the evaluator computes "no branch true" |
| **M20** | Render empty `AND` as `(1 = 0)` or empty `OR` as `(1 = 1)` | The combinator cases and the recorded `prisma-superset` count |
| **M21** | Compare numerics via `float64` | The `9007199254740992` / `9007199254740993` cases: memory says equal, SQL says not |
| **M22** | Select a `bigint` or `decimal` column without `CAST(… AS TEXT)`, or decode a nested relation payload with a default JSON decoder | The read-decode suites; a `bigint` beyond `2^53` comes back changed |
| **M23** | Drop the boolean guard from `ValuesEqual` | `{flag: {equals: 1}}` starts matching a `true` row in memory while SQL refuses the comparison |
| **M24** | Compare strings by decoded runes/UTF-16 order rather than bytes | A corpus row containing an astral-plane character compared against one in U+E000–U+FFFF (this case must be **added** — see §8) |
| **M25** | Let a comparison match null in either direction | Two-valued suite and the oracle; the `lt/null` probe must select nothing |
| **M26** | Drop the `jsonb_typeof(<v>) IS NOT NULL` conjunct from the Json type guard | The Json two-valued probes (which must be **added** — §2.5.10) |
| **M27** | Add a new operator to the registry without adding a probe | The coverage assertion: the set of operator names in the probe corpus must equal the registry's key set, sorted |
| **M28** | Accept `mode: insensitive` beside a non-folded operator instead of refusing | The validation suite |
| **M29** | Render an insensitive operator on a dialect with no insensitive `LIKE` instead of refusing | The suite asserting that *every* insensitive probe throws on that dialect, including the degenerate empty-operand ones |
| **M30** | Render a scalar-list operator on a dialect with no array columns | The suite asserting every scalar-list probe throws on that dialect |

### 7.2 The agreement oracle

The oracle is the primary correctness mechanism. It is not a unit test; it is a
property check over a corpus, run against a real database.

#### 7.2.1 The two-valued check

**Fixture.** One self-referencing table with four rows: two fully populated, two
with `NULL` in every nullable position, arranged so that both relation directions
(to-one parent, to-many children) reach a null-bearing row. A `TEXT[]` column with
one non-empty array, one empty array, and two `NULL`s.

**Corpus.** One probe per operator per interesting operand shape, built directly
from the registry entries — not from a hand-written list of names.

**Checks.**

1. *Declaration.* Every entry in every registry declares `SQLIsTwoValued == true`.
   Enumerate; the list of entries that do not must be empty.
2. *Coverage.* `sortedDistinct(probe.Operator for all probes) == sortedKeys(registry)`,
   for the scalar registry, the scalar-list registry, and (to be added) the Json
   registry. Also: every probe's declared operator name must equal the `Name`
   field of the registry entry it renders through.
3. *No unknowns.* For every probe, `SELECT id FROM fixture AS t0 WHERE (<fragment>) IS NULL`
   returns no rows.
4. *Polarity.* For every probe, `WHERE (<fragment>) IS NOT TRUE` and
   `WHERE NOT (<fragment>)` return **the same** rows.
5. *Non-vacuity.* A deliberately three-valued control fragment
   (`"t0"."amount_value" < 5`, unguarded) must produce a non-empty unknown set
   and *different* answers for the two polarities. Without this, checks 3 and 4
   could pass on a broken harness.
6. *Discrimination.* Across all probes, the number of distinct answer sets must
   exceed a floor. Without this, an implementation that renders everything as
   `(1 = 0)` passes 3 and 4.

#### 7.2.2 The agreement check

**The question.** For one condition tree, one datamodel, one seeded database:
does the set of rows selected by the rendered SQL equal the set of rows selected
by the in-memory evaluator over the same rows?

**Procedure per case.**

1. Render the condition against a scope built from the datamodel, producing
   `(text, parameters, table, alias)`.
2. Execute `SELECT <alias>.<idcol> AS id FROM <table> AS <alias> WHERE <text>`
   with the parameters. Collect the ids.
3. Load the same rows through the ordinary read path, **including every relation
   the corpus touches**, and run the evaluator over them. Collect the ids.
4. Compare the two id sets as full decoded values. Not counts. Not a sample.
5. A render error, an execution error, or an evaluator panic is a *recorded
   answer* (`error:<Type>`) and compares unequal to any row set — so a case that
   starts throwing shows up as a disagreement rather than as a skip.

**Corpus requirements.**

- The full operator matrix: every column kind × every operator × several operands
  including the boundary ones (`""`, `0`, `2^53`, `2^53+1`, the earliest and
  latest date, a `%`, a `_`, a `\`, a non-ASCII string, an astral-plane string).
- Every case again wrapped in `NOT`, because negation is where three-valuedness
  shows.
- Nullable and non-nullable variants of every column kind.
- Relation cases at several hop depths, including a hop on a **string** foreign
  key (which is where the forced collation matters), a dangling foreign key, an
  empty to-many, and a to-many whose members have a null leaf.
- Scalar-list cases including a `NULL` array and an empty array.
- Json cases including an absent path, a JSON `null`, and all three sentinels.
- Combinator cases including the empty forms.
- A generated component: randomly sampled condition trees from a seeded
  generator, so the corpus is not limited to what the author thought of.
- A production-shaped fixture — a real to-many policy schema — so that the
  `every` case is exercised the way applications actually write it.

**Engine requirements.**

- Run the whole corpus against **two** Postgres servers: one created with `C`
  collation, one with a linguistic collation. Assert (a) both agree with the
  evaluator, and (b) both return **identical** answers to each other. (b) is what
  proves the forced collation works; without it, a passing single-server run
  proves only that the run's own locale happens to match.
- Assert that removing the forced collation *would* change the answer on the
  linguistic server and would not on the `C` server — i.e. that the guard is
  load-bearing rather than dead code.
- Assert one statement executed and one answer read per case, so the harness
  cannot be quietly doing client-side filtering.
- Assert that the corpus is discriminating: for most cases the selected set must
  be a strict, non-empty subset of the rows. An oracle where every case selects
  everything proves nothing.

**Record files.** Both the exact per-case divergence map and the classification
counts are checked into the repository and asserted as literals. A change to
either is a reviewable diff, not a silent drift. Recreate this: the value is
that a behaviour change cannot be merged without someone writing down what
changed and why.

---

## 8. Open questions and things that look wrong

Recorded honestly, for the implementer to resolve rather than inherit.

1. **Cross-kind ordering operands are accepted but never match.**
   `{name: {lt: 5}}` passes validation (`String` and `Numeric` are both in
   `OrderedKinds`) and evaluates to `false` for every row, while the SQL would
   raise a Postgres type error if it were ever rendered against a text column.
   The two backends therefore disagree — memory answers "no rows", SQL answers
   "error". The generated typed constructors make it unreachable in practice, and
   the corpus never builds such a node, so the oracle does not catch it. **The Go
   port should reject a cross-kind operand at validation** using the field's
   declared type, which the datamodel already carries.

2. **Date-versus-string comparison is under-specified.** `Compare` parses a string
   operand as an instant when the other side is a date, using JavaScript's
   `Date.parse`, which accepts formats no standard defines. The SQL side hands the
   string to Postgres, which parses it with different rules. The corpus only ever
   uses ISO-8601 with milliseconds and `Z`, so the divergence is unmeasured. **Fix
   the accepted format set explicitly in Go and add oracle coverage.**

3. **String ordering in the TypeScript is UTF-16, not UTF-8.** JavaScript `<` on
   strings compares UTF-16 code units, so `"👍" < ""` is `true` in memory
   while Postgres `COLLATE "C"` says `false`. Go gets this right by default. The
   corpus contains an astral-plane row (`x👍y`) but never orders it against a
   character in U+E000–U+FFFF, so the divergence is latent. **Add that case**; it
   should pass in Go and would have failed in TypeScript.

4. **Json operators are outside the two-valued probe suite.** §2.5.10. They look
   two-valued by inspection but nothing measures it. **Extend the probe corpus.**

5. **`has` accepts a null operand at validation but declares `NullOperand:
   never-matches`, while `hasEvery`/`hasSome` declare `rejected`.** `{tags:
   {has: nil}}` validates and renders `(1 = 0)`; `{tags: {hasEvery: [nil]}}` is a
   validation error. The behaviours are both defensible but the inconsistency is
   accidental rather than designed. Pick one and record the choice.

6. **The recorded divergence count is 145, not 144.** 110 + 32 + 3. If a brief
   or a downstream document says 144, it is stale by one; the record file is the
   authority.

7. **`sqlScalarEquals` is unreachable on Postgres.** Because Postgres declares
   `normalisesDocuments: true`, the document-equality path always wins and the
   scalar-extraction path only ever runs on SQLite. If the Go port ships Postgres
   only, that code has no reason to exist — but the *type guard* it contains does,
   so do not delete it wholesale when trimming the dialect.

8. **Simplifications are deliberately not performed.** `NOT (NOT (EXISTS …))` is
   emitted as written. Do not add a peephole pass. Every simplification is a place
   where a two-valued rendering can be turned into a three-valued one by someone
   who reasons about it classically.
