# 02 — Policy resolution

> **Specification status:** detailed supporting research. The merged
> [`BIBLE.md`](./BIBLE.md) is authoritative, including model-attached policy
> authoring, typed model/field identities, and semantic discharge.

How a set of rules becomes a row constraint, a single field's readability condition, and a field
classification. These are three different walks over the same rules. Conflating them is the classic
error, and every mistake it produces is a disclosure, not a crash.

This document is normative for the Go port. It is written so that it can be implemented without
reading the TypeScript. Where the TypeScript does something non-obvious, the non-obvious thing is
stated together with what breaks if it is done the obvious way, and with the empirical evidence that
it breaks.

- Companion documents: the condition-tree and operator specification (document 01) defines
  `Condition`, its node kinds, its SQL rendering and its in-memory evaluation. `03-classification.md`
  defines the constraint-implication check this document calls `Implies`.
- Terminology from CASL is not used. CASL does not exist in Go. The Go policy is a plain slice of
  `Rule` values built per request, and every algorithm below is stated directly over that slice.

---

## 1. Scope

Three questions are answered here, and only these three:

| Question | Input | Output | Consumer |
| --- | --- | --- | --- |
| **Row lens** — which rows may this caller touch for this action on this model? | rules, action, model | a `Condition` (possibly the TRUE or FALSE constant) | merged into the statement's `WHERE` |
| **Field lens** — under what condition is *this one field* readable on a row? | rules, action, model, field | a `Condition` | the per-row mask: rendered into SQL or evaluated in memory against the fetched row |
| **Classification** — is this field always readable, conditionally readable, or never readable, and if conditional, what does the condition depend on and has the statement's own constraint already settled it? | rules, action, model, a set of fields | a `Classification` per field | refusing filters/order-by/aggregates over unreadable fields; deciding which columns must be hydrated so the mask can be evaluated |

Out of scope: how a `Condition` renders to SQL or evaluates against a row (01), how one constraint is
shown to imply another (03), how the rules themselves are assembled from an application's policy
code, and how masking is applied to a result set.

---

## 2. Vocabulary

Every term used later, defined once.

- **Action** — one of `read`, `create`, `update`, `delete`. A closed enum. Nothing else exists.
- **Model** — a generated entity type. Identified by a value produced by code generation, never by a
  free-form string typed by a human at the call site.
- **Field** — a column or relation field of exactly one model. Identified by a generated value. See
  §9: a field is never a bare string on this path.
- **Condition** — an inspectable tree of nodes describing a predicate over one row of one model.
  Never a closure. Every node carries the model it belongs to. Constructed only by generated typed
  columns.
- **Two-valued** — every condition, on every row, is either true or false. There is no third value.
  Document 01 requires every operator to render two-valued SQL (a `NULL` column under `NOT` still
  yields a definite answer). Several results below are *only* valid because of this invariant; they
  are flagged where they occur.
- **TRUE constant / FALSE constant** — the two degenerate conditions. `And()` over zero branches is
  TRUE (selects everything, renders as `TRUE`, merges away to nothing). `Or()` over zero branches is
  FALSE (selects nothing, renders as `FALSE`). The port must have exactly these two constants and
  must not represent "no constraint" as `nil` in a way that a caller can confuse with "no rows".
- **Rule** — one grant or one denial. §3.
- **Chain** — the ordered, filtered subsequence of rules that is relevant to one question. §5. The
  chain is always ordered highest-priority-first.
- **Grant / forward rule** — a rule with `Inverted == false`.
- **Denial / inverted rule** — a rule with `Inverted == true`.
- **Row constraint** — the answer of the row lens.
- **Field condition** — the answer of the field lens.
- **Statement constraint** — the row constraint that decides which rows the statement being executed
  can reach. For a read it is the `read` row constraint. For `update`, `updateMany`, `delete` and
  `deleteMany` it is the `update` / `delete` row constraint. This distinction is load-bearing; §8.5.
- **Discharge** — the statement constraint already guarantees a conditionally-readable field is
  readable on *every* row the statement can reach, so the field may be named in a filter, an
  order-by, an aggregate or a grouping key without disclosing anything the caller cannot already
  read. §8.4.

---

## 3. The rule model

```go
package policy

type Action uint8

const (
    ActionRead Action = iota
    ActionCreate
    ActionUpdate
    ActionDelete
)

type Rule struct {
    Action    Action
    Model     ModelID
    Fields    []FieldID
    Inverted  bool
    Condition Condition
}

type Policy []Rule
```

`Policy` is a plain slice, built fresh per request. It has no index, no builder state and no
behaviour of its own beyond the functions in this document. The `policy` package imports nothing
concrete: `ModelID`, `FieldID` and `Condition` are defined by the package itself and produced by
generated code.

### 3.1 `Condition` — nil versus empty

| Value | Meaning | Effect |
| --- | --- | --- |
| `nil` | **Unconditional.** The rule applies to every row, with no test. | Terminates the walk in both lenses (§6, §7): no lower-priority rule can ever be reached past it. |
| non-nil | **Conditional.** The rule applies to the rows its condition selects. | Contributes a branch or a negation. |

There is no third state. **The port must make an "empty condition" unconstructable.** A condition is
a node; there is no node that means "no test". If a builder API can produce a condition object with
zero children, it must normalise it to `nil` at construction time, or refuse it.

Why this matters: in the TypeScript, a rule written with an empty condition object is *truthy*, so it
is treated as conditional by the classifier while behaving as always-true at evaluation time. The
observable result is a rule that grants everything but classifies every field as `never`
(demonstrated: a lone grant with an empty condition object yields row constraint `{"OR":[{}]}` and
classification `never` for every field). It fails closed, so it is not a disclosure, but it is a
silent, inexplicable denial. Making the state unconstructable removes the case rather than
specifying it.

### 3.2 `Fields` — nil versus empty

| Value | Meaning |
| --- | --- |
| `nil` | **Whole-row rule.** The rule speaks about the row as a whole, and therefore about *every* field of it. |
| non-empty | **Field-scoped rule.** The rule speaks only about the listed fields. |
| non-nil, length 0 | **Invalid.** Must be rejected at construction with an error. |

Getting `nil` and empty backwards inverts a permission: a whole-row grant read as "grants no fields"
denies everything, and an empty field list read as "grants every field" turns a deny that names
nothing into a deny of nothing — i.e. into a no-op. The TypeScript ground truth rejects an empty
field array at rule construction with a hard error, and treats an absent field list as
"matches every field". The Go port must do the same. Prefer a constructor that cannot express the
invalid state:

```go
func WholeRow(action Action, model ModelID, cond Condition) Rule
func ForFields(action Action, model ModelID, first FieldID, rest ...FieldID) Rule
```

### 3.3 Construction invariants

Checked once, when the policy is built, before any resolution runs:

1. `Rule.Condition == nil || Rule.Condition.Model() == Rule.Model`. A rule may not carry a condition
   rooted at another model. (Conditions *reach* other models through relation operators; that is a
   node inside the tree, and the node carries that other model. The root does not.)
2. Every `FieldID` in `Rule.Fields` belongs to `Rule.Model`.
3. `Rule.Fields` is `nil` or non-empty.
4. Every operator used in `Rule.Condition` is one the port can render exactly. A rule the port cannot
   render must be refused loudly at policy-build time, naming the rule and the operator — never
   silently dropped, and never approximated. (The TypeScript raises a dedicated error listing the
   supported operators and combinators; the Go port must keep that behaviour, because a dropped rule
   is usually a dropped *denial*.)

### 3.4 No field patterns

The TypeScript matches field names as glob patterns, so a denial written `t*` covers `title`. **The
Go port must not support patterns.** Fields are generated identities; a pattern reintroduces
string-shaped, unresolvable names through the back door (§9). If an application wants to deny a set
of fields, it lists them; the generator can offer "all fields of this model" as a typed helper that
expands to the concrete list at build time.

---

## 4. Rule order and precedence

**Rules are an ordered override chain. The last rule declared has the highest priority. Precedence is
positional; nothing is absolute.**

Stated exactly:

- Rules are declared in order. Assign each rule a priority equal to its position counted from the end
  of the slice, so the last-declared rule has priority 0 (highest) and the first-declared has the
  lowest.
- Every chain (§5) is ordered by descending priority — last declared first.
- **The decision procedure.** For a row `r`, walk the chain in order and stop at the first rule whose
  condition matches `r` (a `nil` condition matches every row). The answer is `true` if that rule is a
  grant and `false` if it is a denial. If no rule matches, the answer is `false`.

That decision procedure is the entire semantics. Everything else in this document is a compilation of
it — into a `WHERE` clause (§6, §7) or into a static summary (§8). Any in-memory check the port
offers (`Check`, `CheckField`) must implement exactly this procedure, and the compiled constraints
must agree with it row for row (§11.1).

Consequences that must not be left implicit:

- **A denial is not absolute.** `cannot(read, User, [phone])` followed by
  `can(read, User, [phone], id == me)` leaves phone readable on your own row, because the later grant
  outranks the earlier denial. Reordering the two makes phone unreadable everywhere. An
  implementation that treats denials as an absolute veto (filter them all out first, then intersect)
  gets this backwards and is caught by §11.
- **A grant is not absolute either.** `can(read, Post)` followed by
  `cannot(read, Post, [title], status == DRAFT)` hides `title` on drafts.
- **Unconditional rules terminate.** No rule below an unconditional rule of either polarity can ever
  be reached, because an unconditional rule matches every row. Both lenses must stop walking there.
  Dropping this is not merely an optimisation loss: keeping a later grant alive past an unconditional
  denial re-grants what was denied.
- **Merging.** If the port ever supports wildcard rules (an "any model" or "any action" rule), the
  wildcard rules must be *merged into* the chain by declaration order, not appended to either end.
  Appending changes which rule wins.

---

## 5. Chain selection — the same rules, two different subsequences

Before either lens runs, the rules are filtered to those relevant to the question. **This is where
the row lens and the field lens actually differ.** The walk that follows is nearly identical (§7.4);
the chains are not.

```go
func chainForRow(p Policy, a Action, m ModelID) []Rule
func chainForField(p Policy, a Action, m ModelID, f FieldID) []Rule
```

Both preserve descending priority order. Both keep only rules with `Action == a && Model == m`.
They then differ:

**`chainForField(f)` keeps a rule when:**

- `rule.Fields == nil` (a whole-row rule speaks about every field), or
- `f` is in `rule.Fields`.

Polarity is irrelevant here. Both grants and denials that mention `f` are in the chain.

**`chainForRow` keeps a rule when:**

- `rule.Fields == nil` — both polarities, or
- `rule.Fields != nil && !rule.Inverted` — a **field-scoped grant grants the whole row**.

A **field-scoped denial is dropped from the row chain entirely.** This is the rule people get wrong.

Why each half is right:

- A field-scoped *denial* must not remove the row. `cannot(read, User, [phone])` means "you may see
  users, but their phone is blank" — not "you may not see users". If the denial reached the row
  chain, the row constraint would exclude every user row and the caller would see nothing at all.
- A field-scoped *grant* must admit the row. `can(read, Post, [title], status == PUBLISHED)` is the
  only reason the caller can see a published post at all; if it did not contribute to the row
  constraint the caller would be handed zero rows and never get to the title.

The asymmetry is deliberate and it is exactly why the row lens cannot answer a field question. Run
the row lens to decide whether `title` is readable and every field-scoped denial has silently
vanished — the answer is "readable" and the field leaks. The TypeScript pins this with a differential
test that runs the row lens against per-row ground truth and asserts that it *disagrees* on six named
scenarios and on a large generated sweep; the port must keep an equivalent test (§11.3).

---

## 6. The row lens

### 6.1 Algorithm

```go
func RowConstraint(p Policy, a Action, m ModelID) Condition {
    chain := chainForRow(p, a, m)

    var branches []Condition
    var denials  []Condition
    openGrant := false

    for _, r := range chain {
        if r.Inverted {
            if r.Condition == nil {
                break
            }
            denials = append(denials, Not(r.Condition))
            continue
        }
        if r.Condition == nil {
            openGrant = true
            break
        }
        b := r.Condition
        if len(denials) > 0 {
            b = And(append([]Condition{b}, denials...)...)
        }
        branches = append(branches, b)
    }

    if openGrant {
        switch {
        case len(denials) == 0:
            return True
        case len(branches) == 0:
            return And(denials...)
        default:
            branches = append(branches, And(denials...))
        }
    }
    if len(branches) == 0 {
        return False
    }
    return Or(branches...)
}
```

Read out in words:

1. A **conditional grant** contributes one disjunct: its own condition, conjoined with the negation
   of every conditional denial seen *above* it.
2. A **conditional denial** contributes no disjunct; it contributes a negation that is conjoined onto
   every *lower*-priority grant.
3. An **unconditional grant** ends the walk. If no denial has been accumulated it collapses the whole
   answer to TRUE — there is no constraint. If denials have been accumulated it contributes a final
   disjunct that is their conjunction ("everything except what was denied above").
4. An **unconditional denial** ends the walk contributing nothing. Whatever grants were accumulated
   above it survive; nothing below it exists.
5. **No branches at all means FALSE.** This is the fail-closed default and it covers the two cases
   that matter: no rule matched the action/model pair at all, and an unconditional denial sitting at
   the top of the chain.

### 6.2 The result is merged, never replaced

The constraint is conjoined with the caller's own filter: `merge(where, c)` returns `where` when `c`
is TRUE, returns `c` when `where` is empty, and otherwise returns `And(where, c)`. It is never
substituted for the caller's filter and never dropped because it "looks empty".

### 6.3 The permission gate

The provider contract also has a cheap "may this caller do this at all" question, asked before a
statement is prepared and answered without touching a row. It is not a separate algorithm:

> **The gate refuses exactly when `RowConstraint` returns the FALSE constant.**

Implement it that way — one algorithm, not two. (This equivalence holds for the algorithm above:
the walk produces no branches precisely when the first rule that would decide a bare "any row"
question is an unconditional denial, or when the chain is empty.) A conditional grant passes the
gate and is then enforced by the constraint.

### 6.4 What must fail closed

- Empty chain → FALSE. An ability that grants `update` but never mentions `read` yields a FALSE read
  constraint, and the read gate refuses. Verified against the ground truth: the read chain is empty
  and the constraint is the FALSE constant.
- Returning TRUE, `nil`, or an empty struct in that situation is the single most dangerous mutation
  in this document. It converts "no permission" into "no filter" and returns the whole table.

---

## 7. The field lens

The question: **on which rows is field `f` readable?** The answer is a `Condition` over the same
model, suitable for rendering into a `CASE WHEN` mask in SQL or evaluating in memory against a
fetched row.

### 7.1 Algorithm

```go
func FieldCondition(p Policy, a Action, m ModelID, f FieldID) Condition {
    chain := chainForField(p, a, m, f)

    var branches        []Condition
    var unmatchedEarlier []Condition

    for _, r := range chain {
        if r.Condition == nil {
            if !r.Inverted {
                branches = append(branches, conjoin(unmatchedEarlier))
            }
            return disjoin(branches)
        }
        if !r.Inverted {
            branches = append(branches, conjoin(append([]Condition{r.Condition}, unmatchedEarlier...)))
        }
        unmatchedEarlier = append(unmatchedEarlier, Not(r.Condition))
    }
    return disjoin(branches)
}

func conjoin(cs []Condition) Condition {
    switch len(cs) {
    case 0:
        return True
    case 1:
        return cs[0]
    default:
        return And(cs...)
    }
}

func disjoin(cs []Condition) Condition {
    switch len(cs) {
    case 0:
        return False
    case 1:
        return cs[0]
    default:
        return Or(cs...)
    }
}
```

Four clauses, all load-bearing:

1. **A conditional grant contributes one disjunct**: its own condition conjoined with
   `unmatchedEarlier` — the negation of the condition of *every* higher-priority rule, grant and
   denial alike.
2. **A conditional denial contributes no disjunct at all**, and its negation joins
   `unmatchedEarlier`, so it is conjoined onto every lower-priority grant. A denial does not subtract
   from a finished answer; it *narrows every grant beneath it*.
3. **Every rule, whatever its polarity, appends its negation to `unmatchedEarlier` and carries it
   forward.** This is the accumulation the walk exists for.
4. **An unconditional rule ends the walk.** A grant contributes the accumulated
   `conjoin(unmatchedEarlier)` as a final disjunct — "readable on every row no higher rule spoke
   about". A denial contributes nothing. Either way the answer is `disjoin(branches)`, and
   `disjoin(nil)` is the FALSE constant: **an unreachable field is a condition that selects no row**,
   not an absent condition.

### 7.2 Why this is the decision procedure, compiled

The answer produced is
`⋁ over grants i ( Cᵢ ∧ ⋀ over all j<i ¬Cⱼ )`, with an unconditional grant's `Cᵢ` read as TRUE.
That is the standard disjunctive normal form of "the first rule that matches is a grant": disjunct
`i` is true exactly on the rows where rule `i` matches and no higher rule did. The disjuncts are
mutually exclusive by construction. When no grant is reachable, the disjunction is empty and the
answer is FALSE — which is the "no rule matched" clause of §4.

### 7.3 The naive versions, and the rows on which they lie

Each of these is a plausible implementation. Each was run against per-row ground truth over an
exhaustive sweep of every rule chain of length ≤ 3 drawn from a 10-rule alphabet (1110 chains × 2
fields × 24 rows). The counts are the number of chain/field pairs on which the mutant's condition
disagrees with the per-row decision procedure. The reference algorithm disagrees on zero.

| Naive version | Disagreements | Witness |
| --- | --- | --- |
| **Drop `unmatchedEarlier` entirely** — every grant contributes its bare condition. | **250** | `can(read, Post)` then `cannot(read, Post, status == DRAFT)`. Naive answer: TRUE. Correct answer: `NOT(status == DRAFT)`. Every draft's `title` leaks. |
| **Treat an inverted rule as merely absent** — skip denials, do not negate, do not stop. | **517** | `can(read, Post)` then `cannot(read, Post)`. Naive answer: TRUE. Correct answer: FALSE. |
| **Return TRUE when an unconditional grant is reached**, discarding what was accumulated. | **146** | `can(read, Post)` then `cannot(read, Post, status == DRAFT)`. Naive answer: TRUE. Correct answer: `NOT(status == DRAFT)`. The unconditional grant is the *lowest*-priority rule; collapsing to TRUE erases the denial above it. |
| **Return the TRUE constant instead of FALSE when nothing is granted.** | **981** | A lone `cannot(read, Post, [title])`. Naive answer: TRUE. Correct answer: FALSE. |
| **Run the row lens** (row chain, §5) to answer the field question. | disagrees on six named scenarios and on a large generated sweep | `can(read, Post)` then `cannot(read, Post, [title], status == DRAFT)`. The field-scoped denial is not in the row chain, so the row lens answers TRUE and `title` leaks on every draft. |

Worked out longhand, the first row of that table:

> Declared: `can(read, Post)`, then `cannot(read, Post, [title], status == DRAFT)`.
> Chain for `title`, highest priority first: `[ deny(status==DRAFT), grant(unconditional) ]`.
> Reference: rule 1 is a conditional denial — no branch, `unmatchedEarlier = [ NOT(status==DRAFT) ]`.
> Rule 2 is an unconditional grant — push `conjoin(unmatchedEarlier)` = `NOT(status==DRAFT)`, return
> it. `title` is readable exactly on non-drafts. ✔
> Without the carry-forward: rule 1 contributes nothing at all, rule 2 pushes `conjoin(nil)` = TRUE,
> answer TRUE. The denial has evaporated. Every draft title is returned to a caller who may not read
> it. ✘

### 7.4 What the two lenses really share (and a warning about "simplifying")

Compare §6.1 and §7.1 and the family resemblance is exact: the row lens is the field lens with
`unmatchedEarlier` restricted to the negations of *denials* only, plus a TRUE short-circuit. Under
the two-valued invariant those two accumulations are logically equivalent — the negation of a
higher-priority *grant* can only ever remove rows that the grant's own disjunct already covers. The
sweep confirms it: a mutant that keeps only the denial negations disagrees on **zero** chains.

Two conclusions, both required:

1. **The difference between the lenses lives in chain selection (§5), not in the walk.** If you
   remember one sentence from this document, that is the one.
2. **Implement §7.1 as written anyway.** It produces the mutually exclusive DNF, it is the shape the
   golden outputs are pinned to, and its equivalence to the reduced form depends on the two-valued
   invariant. Do not "simplify" it into the row lens walk on a hunch; that hunch is one operator away
   from being false, and the reduced form is not what the pinned outputs contain.

---

## 8. Classification

```go
type Access uint8

const (
    AccessAlways Access = iota
    AccessConditional
    AccessNever
)

type Classification struct {
    Access                Access
    Requires              []FieldID
    Dependencies          DependencyTree
    DischargedByConstraint bool
}

func Classify(p Policy, a Action, m ModelID, fields []FieldID) map[FieldID]Classification
```

Classification is a *static* summary of the same chain the field lens walks. It answers questions a
per-row condition cannot: may this field be named in a `WHERE`, an `ORDER BY`, a `DISTINCT`, a
grouping key or an aggregate — positions where there is no row to mask, and where the answer leaks
through counts and orderings rather than through values.

### 8.1 The walk

For each requested field, take `chainForField` and walk it highest-priority-first, accumulating:

```
requires     : ordered set of FieldID, first-seen order
dependencies : DependencyTree (§8.3)
discharged   : bool, starts true
result       : unset
```

```
for i, r := range chain:
    if r.Condition != nil:
        collectRequires(r.Condition, &requires)
        collectDependencies(r.Condition, &dependencies)
        if r.Fields != nil || r.Inverted:
            discharged = false
        continue

    // r is unconditional: it decides the field, and nothing below it is reachable
    if requires is empty:
        result = Classification{Access: never if r.Inverted else always}
    else:
        for _, later := range chain[i:]:
            if later.Condition == nil: continue
            collectRequires(later.Condition, &requires)
            collectDependencies(later.Condition, &dependencies)
            if later.Fields != nil || later.Inverted:
                discharged = false
        result = Classification{Conditional, requires, dependencies, discharged}
    break

if result is unset:
    if requires is non-empty:
        result = Classification{Conditional, requires, dependencies, discharged}
    else:
        result = Classification{Never}
```

Reading the three exits:

- **Always** — an unconditional grant was reached and no conditional rule sat above it. Nothing
  restricts this field. No mask, no hydration, usable anywhere.
- **Never** — either an unconditional denial was reached with no conditional rule above it, or the
  chain ran out with nothing conditional in it (including the empty chain). The field is refused
  outright wherever it is named, including in a projection.
- **Conditional** — at least one conditional rule bears on the field. The field is masked row by row
  using the §7 condition, and may be named in a filter only if `DischargedByConstraint` holds *and*
  the statement's constraint check of §8.5 passes.

Note the fail-closed shape of the final fallback: a chain of purely conditional rules with no
unconditional rule at the bottom classifies as *conditional*, not as *always*. Combined with §8.4 —
where every such chain that contains a denial or a field-scoped rule sets `discharged = false` — the
outcome for the caller is refusal.

**The tail loop** (`chain[i:]`, the rules below the unconditional one) is deliberate conservatism,
not correctness. Those rules are unreachable, so they cannot affect the field's actual readability.
Folding them in can only *add* names to `Requires` (a longer error message, extra columns hydrated)
and can only flip `discharged` from true to false (refusing more). Reproduce it for parity with the
ground truth; know that removing it is not a disclosure, and that if you remove it you must say so
because the golden outputs change.

### 8.2 `Requires` — the fields the condition reads *on this model*

`collectRequires` descends through the boolean combinators (`AND`, `OR`, `NOT`) and, for every other
node, records the field of *this model* that the node is rooted at. It does **not** descend into the
far side of a relation node.

So `author.is(organization.isNot(suspended == true))` contributes exactly `author`. The far-side
columns are the business of `Dependencies` (§8.3) and, when a far-side field is itself named by a
caller's filter, of a *separate* classification against that related model.

Order is first-seen along the chain, highest priority first, and it is observable: it determines the
order of names in the refusal message. Use an insertion-ordered set.

`Requires` is consumed by the read path to decide which of this model's own columns must be present
in the fetched row so the mask can be evaluated. A name in `Requires` that is not a field of the
model must fail closed — in Go it cannot arise (§9), which is the point.

### 8.3 `Dependencies` — the hydration plan

`Requires` is flat and stops at the relation. `Dependencies` is the tree, and it exists for exactly
one reason: **so that a mask evaluated in memory is never evaluated against an under-selected row.**
If the mask asks whether `post.author.organization.suspended` is true and the query only selected
`post.secret`, an in-memory check reads `undefined`, decides "not suspended", and hands back the
secret. The ground truth pins this with an end-to-end test: the engine must expand the caller's
`select { secret }` into `select { secret, author { organization { suspended } } }` before the query
runs, and must strip the added columns from the result.

```go
type DependencyTree map[FieldID]Dependency

type Dependency struct {
    Scalar   bool
    Model    ModelID
    Children DependencyTree
}
```

Construction, from the condition tree of every conditional rule in the chain:

- Boolean combinators are erased — their branches are merged into the parent level.
- A node on a scalar field of this model yields a `Scalar` leaf.
- A relation node yields a child entry keyed by the relation field, carrying the related model and,
  recursively, the tree of that relation's condition.
- Trees from several rules are **merged**, not replaced: on a key collision, a deeper tree wins over
  a leaf, and two trees are merged recursively. Two rules that read `owner.organizationId` and
  `reviewer.organizationId` produce both.

Deliberate divergence from the TypeScript, which the port must take: the TypeScript walks untyped
JSON and therefore cannot tell a field name from an operator name, so its tree contains operator keys
(`status: { equals: true }`) and relation-operator keys (`author: { is: { … } }`). Those keys carry no
information a Go implementation needs — the typed condition already knows which node is an operator.
Emit the *semantic* tree above: fields and relations only. What must be preserved exactly is the
observable consequence: the set of columns hydrated, at every depth, on every model, is the same.

If a dependency cannot be hydrated — a relation the projection cannot reach, a shape the datamodel
cannot resolve — the read must be refused, not attempted. Silently skipping hydration is the
under-selection bug above.

### 8.4 Discharge — what it means

> **`DischargedByConstraint` asserts: the constraint that selects the rows this statement can reach
> already guarantees this field is readable on every one of them.**

If that holds, naming the field in a `WHERE` or an `ORDER BY` discloses nothing, because every row
the filter can interrogate is a row on which the caller may read the value anyway. If it does not
hold, the filter is an oracle: `where: { phone: { startsWith: "+44" } }` recovers a hidden value
character by character out of which rows come back, and `count` reports it even when no value is
projected. This is why the flag exists, and why it must be conservative.

The canonical case for discharge is the **model-level scoped grant**: `can(read, Stat, userId == me)`
and nothing else. Every field's condition *is* the row constraint, so the constraint discharges it,
and every field is filterable. That ability shape is the common one, and it is why most applications
never encounter a refusal here.

**Reference computation** (as the walk in §8.1 does it): `discharged` starts true and is falsified by
any conditional rule in the chain that is **field-scoped or inverted**. In other words, discharge
survives only when every condition bearing on this field came from a whole-row grant — the same rules
that build the row constraint, in the same order, contributing the same branches.

Pinned consequences:

- whole-row conditional grant only → `discharged = true`
- a field-scoped conditional grant anywhere in the field's chain → `false`
- a conditional *denial* anywhere in the field's chain → `false`, even a whole-row one; a denial
  narrows the field's condition below the row constraint

**Normative definition, and a required strengthening.** The reference computation is a syntactic
proxy for a semantic claim, and the proxy is not sound in one reachable case. Define discharge
semantically:

> `DischargedByConstraint` iff `Implies(RowConstraint(read), FieldCondition(field))` — the read row
> constraint entails the field's readability condition.

The syntactic proxy diverges from this when the model carries a **field-scoped conditional grant for
some *other* field**. Such a grant is dropped from the other field's chain (so the proxy says
"discharged") but is *kept* in the row chain as a whole-row grant (§5), where it adds a disjunct that
widens the row constraint beyond the field's condition. Concretely, verified against the ground
truth:

```
can(read, Post, authorId == me)
can(read, Post, [title], status == PUBLISHED)
```
```
row constraint     : OR[ status == PUBLISHED , authorId == me ]
field condition body: authorId == me
classification body: conditional, requires [authorId], dischargedByConstraint = TRUE
```

A published post by another author is returned to the caller with `body` masked, yet the caller is
permitted to filter on `body`. The Go port must not reproduce this. Either compute discharge by the
implication check above (see `03-classification.md`), or keep the proxy and add the missing guard:
falsify `discharged` whenever the model's rule set for this action contains any field-scoped
conditional grant that is not in this field's own chain. The implication check is preferred — it is
the definition, the proxy is an optimisation, and 03 already owns the check.

### 8.5 The 0.6.0 correction: discharge is judged against the rows the *statement* selects

The load-bearing correction, and the one an implementer will get wrong by default.

Discharge is a claim about *the rows a statement can reach*. For a **read**, the statement is merged
with the read constraint, so the constraint discharge was judged against is the constraint that
selects the rows — it discharges itself, and nothing further is needed.

For **`update`, `updateMany`, `delete` and `deleteMany`, the statement selects with the write
constraint, not the read constraint.** A `where` on such a statement is still a read — it
interrogates the database and answers through the count and through which rows changed — but the rows
it interrogates are the rows the *write* constraint admits. So before a conditionally-readable field
may be named in the `where` of one of those statements:

```go
Implies(RowConstraint(writeAction, model), RowConstraint(ActionRead, model))
```

must hold: the write reach must be inside the read reach. An ability that reads `Post` only where
`published` but updates every `Post` fails this check and the filter is refused, because the count
such a statement reports ranges over rows the caller may not read. Where the write reach equals the
read reach, is one branch of it, or is narrower still, the filter is allowed exactly as before.

Pinned behaviours (all four are in the ground-truth suite):

- `read = OR[published == true]`, `update = TRUE` → `updateMany(where: title startsWith 'a')`
  refused, naming the field and the undischarged requirement. The statement must not be issued.
- same for `deleteMany`, and for a single `update` whose unique `where` carries an ordinary filter
  beside the unique field.
- `read = OR[published]`, `update = OR[published]` → allowed.
- `read = OR[published, authorId == me]`, `delete = OR[authorId == me]` → allowed (write reach is one
  branch of the read reach).
- `read = OR[published]`, `update = AND[OR[published], authorId == me]` → allowed (narrower still).

Three positions deliberately keep discharging against the **read** constraint, because no single
statement constraint describes the rows they interrogate:

1. a field reached across a relation (`{ author: { is: { phone: … } } }`), classified against the
   related model — no statement narrows the interrogated rows to *that* model's read constraint;
2. a filter nested inside `data`, which selects the children of whatever parent row the statement
   matched;
3. the probe `where` of an upsert, classified before the branch is chosen.

A consequence worth stating because it looks like a bug and is not: **an ability that grants `update`
without `read` cannot filter an `updateMany` or a `deleteMany` at all, not even by `id`.** With no
read rule the read chain is empty, every field classifies `never`, and a `where` is a read. That is
deliberate.

The implication check itself — how one constraint is shown to entail another, how `AND` is flattened,
how a disjunction is matched branch-wise — belongs to `03-classification.md`. This document only
fixes *which two constraints* are compared.

---

## 9. Fields are identities, not strings — a property the Go port gets for free

In the TypeScript, `classifyFields` is handed arbitrary strings. A whole-row grant matches *any*
name, including a name that is not a column at all. So under a blanket `can(read, Post)`, asking
about `not_a_column` answers **`always`** — the unknown field **fails open**. The system as a whole
still failed closed only because the unknown name was later rejected by Prisma's own validator: the
fail-closed property was *borrowed* from a downstream component. 0.6.0 stopped borrowing it and began
checking filter keys against the datamodel itself before classification.

In Go the question cannot be asked. A condition node can only be built by a generated typed column;
a `FieldID` can only be produced by generated code; an unknown field is unconstructable. State this
as a requirement rather than letting it be a happy accident:

1. **No exported function on this path may accept a field name as a `string`.** Not
   `Classify`, not `FieldCondition`, not `Rule` construction, not the `Requires` set, not the
   dependency tree. The compiler is the fail-closed check.
2. `FieldID` must be a comparable value carrying its model, produced only by generated code, with no
   exported constructor taking a string. A test that asserts `policy` exposes no
   `string`-keyed field API is cheap and worth having.
3. Any boundary that unavoidably receives a name from outside — a GraphQL field name, a JSON filter
   key, a raw column in a user-supplied order-by — resolves it through the generated
   name → `FieldID` map **first**, and refuses the request when the lookup misses. Refuse; do not
   pass the name along for someone downstream to reject, and do not classify it.
4. The same applies to models and to actions: both are closed, generated sets.

---

## 10. Worked examples

Every output below was produced from the ground-truth implementation, not written by hand. Rules are
listed **in declaration order**; remember that the chain reverses them.

### 10.1 A plain conditional grant

```
can(read, Stat, userId == "u1")
```

| Answer | Value |
| --- | --- |
| chain for any field | `[ grant(userId == "u1") ]` |
| row constraint (read) | `userId == "u1"` |
| field condition, `wordsRead` | `userId == "u1"` |
| classification, `wordsRead` | `conditional`, requires `[userId]`, dependencies `{userId: scalar}`, discharged **true** |

The row constraint and the field condition coincide, which is exactly what discharge means. Every
field of this model may be filtered, ordered, grouped and aggregated. This is the shape most
applications have, and nothing about it is refused.

### 10.2 "Hide phone except your own" — a denial then a grant

```
can(read, User)
cannot(read, User, [phone])
can(read, User, [phone], id == "me")
```

Chain for `phone`, highest priority first:
`[ grant([phone], id=="me") , deny([phone], nil) , grant(nil, nil) ]`

Walk: rule 1 is a conditional grant → branch `id == "me"`; `unmatchedEarlier = [ NOT(id=="me") ]`.
Rule 2 is an **unconditional denial** → no branch, walk ends. Answer: `disjoin([id=="me"])`.

| Answer | Value |
| --- | --- |
| field condition, `phone` | `id == "me"` |
| field condition, `email` | TRUE (chain is just the whole-row grant) |
| row constraint (read) | **TRUE** |
| classification, `phone` | `conditional`, requires `[id]`, discharged **false** |
| classification, `email` | `always` |

Three things to take from this example:

- The row constraint is TRUE. The field-scoped denial was dropped from the row chain and the
  field-scoped grant contributed nothing beyond what the whole-row grant already gave. The caller
  sees every user row — with `phone` blank on all but their own. That is the intended behaviour, and
  an implementation that let the denial into the row chain would return zero users.
- Reordering the last two rules makes `phone` unreadable everywhere. Precedence is positional.
- `phone` is not filterable, not even by the caller's own row, because the condition deciding it came
  from a field-scoped rule and the row constraint (TRUE) plainly does not entail `id == "me"`.
  `where: { phone: { startsWith: "+44" } }` is refused with
  *readability depends on id, which the query constraint does not discharge*.

### 10.3 A relation-crossing condition

```
can(read, Post)
cannot(read, Post, [secret], author.is(organization.is(suspended == true)))
```

Chain for `secret`: `[ deny([secret], author.is(…)) , grant(nil, nil) ]`.
Rule 1 is a conditional denial → no branch, `unmatchedEarlier = [ NOT(author.is(…)) ]`. Rule 2 is an
unconditional grant → push `conjoin(unmatchedEarlier)` and return.

| Answer | Value |
| --- | --- |
| field condition, `secret` | `NOT( author.is( organization.is( suspended == true ) ) )` |
| row constraint (read) | TRUE |
| classification, `secret` | `conditional`, requires `[author]`, discharged **false** |
| dependencies, `secret` | `{ author → { organization → { suspended: scalar } } }` |
| classification, `title` | `always` |

`Requires` stops at `author`; the depth lives in `Dependencies`, and the depth is what the read path
must hydrate. A caller asking for `select { secret }` must have the query rewritten to
`select { secret, author { organization { suspended } } }`, the mask evaluated against the hydrated
row, and the added columns stripped from the response. Skip the hydration and the in-memory mask
reads a missing value, concludes "not suspended", and returns the secret — pinned by an end-to-end
test in the ground truth.

Filtering posts by `secret` is refused. Filtering posts by `author.organization.suspended` is a
different question: that field is classified against `Organization`, under `Organization`'s own
rules.

### 10.4 An ability granting update without read

```
can(update, Post, authorId == "me")
```

| Question | Answer |
| --- | --- |
| read chain for any field | **empty** |
| row constraint (read) | **FALSE** |
| read permission gate | refuses |
| field condition, `title` (read) | **FALSE** |
| classification, `title` (read) | `never` |
| row constraint (update) | `authorId == "me"` |

`updateMany(where: { title: { startsWith: "a" } })` is refused — `title` classifies `never`, and a
`where` is a read. So is `deleteMany`, and so is a filter by `id`. The caller may update their own
posts by supplying a unique selector the policy can satisfy, but they may not interrogate the table.

This is the example that kills the "empty constraint on no match" mutation: if the empty read chain
produced TRUE instead of FALSE, this ability would read every post in the table.

### 10.5 The discharge divergence (see §8.4)

```
can(read, Post, authorId == "me")
can(read, Post, [title], status == "PUBLISHED")
```

| Answer | Value |
| --- | --- |
| row constraint (read) | `OR[ status == "PUBLISHED" , authorId == "me" ]` |
| field condition, `body` | `authorId == "me"` |
| field condition, `title` | `OR[ status == "PUBLISHED" , AND[ authorId == "me" , NOT(status == "PUBLISHED") ] ]` |
| classification, `body` (ground truth) | `conditional`, requires `[authorId]`, discharged **true** |
| classification, `body` (required in Go) | discharged **false** |

The row constraint admits a published post by another author; `body` is masked on that row; the
ground truth nevertheless permits filtering by `body`. `Implies(rowConstraint, authorId == "me")` is
false, so the semantic definition of discharge rejects it. §8.4 requires the Go port to reject it
too. Note that `title`'s own classification is already `false` here, by the field-scoped rule
clause — it is the *other* fields of the model that are misclassified.

---

## 11. Acceptance criteria

Each criterion names a mutation to the implementation and states what must fail. A test suite that
does not fail under every mutation below has not pinned this document.

### 11.1 The oracle (the test that makes the rest possible)

Build a differential test before anything else, because it catches mutations nobody thought to name.

- **Ground truth**: the §4 decision procedure implemented directly and independently — walk the
  chain, first matching rule wins, no match means false — evaluated against a concrete row.
- **Under test**: `FieldCondition`, evaluated against the same row by the in-memory condition
  evaluator (document 01).
- **Rows**: a small table exercising every operator shape in the alphabet — scalar equality, a
  to-one relation, a to-many relation with 0, 1 and 2 elements, and rows that discriminate on every
  condition used. 24 rows suffice.
- **Chains**: every sequence of length ≤ 3 drawn from an alphabet of ~10 rules covering
  {whole-row, field-scoped} × {grant, deny} × {unconditional, scalar condition, to-one relation
  condition, to-many relation condition}. That is 1110 chains; run each for a field the field-scoped
  rules name and for one they do not.
- **Assertion**: zero disagreements, for every chain, every field, every row.
- **Coverage assertion**: across the sweep, the answers must include chains that are all-true, chains
  that are all-false and chains that discriminate. A sweep that never discriminates proves nothing.

Do the same for `RowConstraint` against a row-level decision procedure over the row chain.

### 11.2 Named mutations of the field lens

| Name | Mutation | Must fail |
| --- | --- | --- |
| `M-FIELD-NO-CARRY` | Remove the `unmatchedEarlier` accumulation; each grant contributes its bare condition. | The oracle, on ≥ 250 of 2220 chain/field pairs. Minimum explicit case: `can(read,Post)` + `cannot(read,Post,status==DRAFT)` must not yield TRUE. |
| `M-DENY-ABSENT` | Treat an inverted rule as merely absent: no negation, no carry, no stop. | The oracle, on ≥ 517 pairs. Minimum: `can(read,Post)` + `cannot(read,Post)` must yield FALSE, not TRUE. |
| `M-OPEN-GRANT-WINS` | On reaching an unconditional grant, return TRUE instead of `conjoin(unmatchedEarlier)`. | The oracle, on ≥ 146 pairs. Minimum: a denial declared *after* an unconditional grant must survive. |
| `M-EMPTY-OPEN` | Return the TRUE constant (or `nil`, or an empty struct) when no branch was produced. | The oracle, on ≥ 981 pairs. Minimum: a lone field-scoped denial must yield FALSE. |
| `M-ROW-LENS-FOR-FIELD` | Answer the field question with `RowConstraint`, or with the field lens run over `chainForRow`. | ≥ 6 named scenarios and a large fraction of the sweep. Minimum: a field-scoped denial must not vanish. |
| `M-FIELD-PRIORITY-BLIND` | Ignore order: OR all grant conditions, AND all denial negations. | The interleaved scenario `can(read,Post)` + `cannot(read,Post,[title],status==DRAFT)` + `can(read,Post,[title],authorId==42)` — the grant declared last must win on its rows. |
| `M-CHAIN-ORDER-FORWARD` | Order the chain in declaration order instead of reverse, so the first-declared rule wins. | §10.2: the walk would stop at the leading `can(read, User)` and answer TRUE, leaking `phone` on every row. Swapping the last two rules of §10.2 must likewise change the answer — if it does not, order is not being honoured. |

### 11.3 Named mutations of the row lens

| Name | Mutation | Must fail |
| --- | --- | --- |
| `M-ROW-EMPTY-OPEN` | Return TRUE / `nil` / empty when no rule matched. | §10.4: an ability with no read rule must not read the table. This is the highest-severity mutation in the document. |
| `M-ROW-KEEPS-FIELD-DENY` | Keep field-scoped denials in the row chain. | §10.2: the caller must still receive every user row. |
| `M-ROW-DROPS-FIELD-GRANT` | Drop field-scoped grants from the row chain. | An ability whose only grant is field-scoped must still return rows. |
| `M-ROW-DENY-NOT-CARRIED` | Do not conjoin accumulated denials onto lower-priority grants. | `can(read,Post,authorId==me)` under a higher-priority `cannot(read,Post,status==DRAFT)` must not return the caller's own drafts. |
| `M-GATE-SEPARATE` | Implement the permission gate as an independent algorithm that can disagree with `RowConstraint`. | Any case where the gate passes and the constraint is FALSE, or vice versa. Derive the gate from the constraint. |

### 11.4 Named mutations of classification

| Name | Mutation | Must fail |
| --- | --- | --- |
| `M-DISCHARGE-IGNORES-FIELD-SCOPE` | Do not falsify `discharged` for a field-scoped conditional rule. | A field-scoped conditional grant beside a whole-row scoped grant must classify the field's own discharge as false. |
| `M-DISCHARGE-IGNORES-DENIAL` | Do not falsify `discharged` for an inverted conditional rule. | `can(read,M,userId==me)` + `cannot(read,M,archived==true)` must classify `discharged = false`. |
| `M-DISCHARGE-FROM-READ-ON-WRITE` | Judge discharge for `update`/`updateMany`/`delete`/`deleteMany` against the read constraint, i.e. skip the write⇒read implication. | `read = OR[published]`, `update = TRUE`, `updateMany(where: title startsWith 'a')` must be refused and the statement must never be issued. The three allowed shapes of §8.5 must still pass. |
| `M-DISCHARGE-SIBLING-GRANT` | Ignore field-scoped conditional grants that belong to *other* fields when deciding discharge (i.e. keep the ground truth's proxy unstrengthened). | §10.5: `body` must classify `discharged = false`. |
| `M-NEVER-BECOMES-ALWAYS` | Classify a field with an empty chain as `always`. | An unknown or ungranted field must classify `never`. |
| `M-REQUIRES-DESCENDS-RELATIONS` | Let `Requires` descend past a relation node and collect far-side column names. | `Requires` for §10.3 must be exactly `[author]`; far-side names belong to `Dependencies` and to the related model's own classification. |
| `M-DEPS-REPLACE-NOT-MERGE` | On a key collision, replace the dependency subtree instead of merging. | Two rules reading `owner.organizationId` and `reviewer.organizationId` must both be hydrated; a leaf must never overwrite a deeper tree. |
| `M-DEPS-SKIPPED` | Skip hydration when a dependency cannot be resolved, instead of refusing the read. | The relation-crossing mask must never be evaluated against an under-selected row; §10.3's end-to-end case must return the field masked, not leaked. |
| `M-REQUIRES-UNORDERED` | Use an unordered set for `Requires`. | Golden refusal messages, which name the fields in first-seen chain order. |

### 11.5 Structural criteria

| Name | Mutation | Must fail |
| --- | --- | --- |
| `M-STRING-FIELD` | Add an exported function on this path taking a field name as `string`. | A test asserting the package exposes no string-keyed field API. This must be a compile-time or API-shape assertion, not a runtime one. |
| `M-EMPTY-FIELDS-ALLOWED` | Accept `Fields: []FieldID{}` as a valid rule. | Rule construction must return an error. |
| `M-EMPTY-CONDITION` | Allow a condition node with zero children instead of normalising to `nil`. | Construction must reject or normalise; the classifier must never see a non-nil condition that tests nothing. |
| `M-UNSUPPORTED-OPERATOR-DROPPED` | Silently ignore a rule whose operator cannot be rendered. | Policy construction must fail loudly, naming the rule and the operator. A dropped rule is usually a dropped denial. |
| `M-PATTERN-FIELDS` | Support glob patterns in `Fields`. | The API must not accept them (§3.4). |

---

## 12. Ambiguities and findings to resolve

Recorded because they were found while reading the ground truth, and a reader of this document should
not have to rediscover them.

1. **The discharge proxy is unsound for sibling field-scoped grants** (§8.4, §10.5). This is not a
   hypothetical: it is reproducible against the shipped implementation, and one of its instances is
   *asserted* by an existing end-to-end test (a model-level scoped grant beside a field-scoped
   conditional grant classifies the untouched field as `dischargedByConstraint: true`, while the read
   constraint has been widened by the field-scoped grant to `OR[flag == true, userId == "u1"]`). The
   disclosure it permits is a filter over a field that is masked on some reachable rows. It is
   narrow — it needs an ability that mixes a whole-row conditional grant with a field-scoped
   conditional grant — but it is real. **The Go port is specified to close it.** Whether the
   TypeScript should also be fixed, and whether that e2e assertion should be inverted, is a decision
   for the maintainer, not for this document.

2. **The tail loop in classification is conservatism, not correctness** (§8.1). It folds unreachable
   rules into `Requires`, `Dependencies` and `discharged`. It only ever refuses more and hydrates
   more. Reproduce it for parity; if the port drops it, say so explicitly, because golden outputs
   change and the change is in the permissive direction.

3. **The two accumulations are provably equivalent** (§7.4) under the two-valued invariant, so a
   reviewer who "simplifies" the field lens into the row-lens walk will not see a test go red on
   semantics — only on shape. This is worth a comment in the test, not in the code: the equivalence
   is a consequence of an invariant that a future operator could violate.

4. **`requires` ordering is observable** through refusal messages. It is specified as first-seen
   order. If the port sorts, the golden messages must be regenerated deliberately.

5. **The dependency tree shape diverges deliberately** (§8.3). The TypeScript tree contains operator
   keys because it walks untyped JSON. The Go tree should not. What must be preserved is the set of
   hydrated columns, which is what any test should assert — asserting the tree's literal shape would
   pin an artifact.

6. **`create` is not discussed here.** The row lens and the gate apply to it, but a `create` has no
   rows to select and no field to mask on the way in; write-side field permissions are a different
   walk and belong to their own document. Nothing in this document should be read as specifying
   `create`.
