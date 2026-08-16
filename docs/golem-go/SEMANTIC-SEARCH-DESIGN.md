# Semantic search over authorized data

How `golem` (Go) implements vector search on rows a caller may not be allowed to
read. Written for an engineer building something similar. Every claim below is
cited to the implementation.

Repository paths are relative to `go/` in `github.com/eleven-am/golem`.

---

## 1. The problem

A model has an embedding index. A caller runs `search(query, take)`. The caller
is subject to row- and field-level authorization: some rows are invisible to
them, and some fields are masked on rows they can otherwise see.

The naive implementation is:

1. embed the query,
2. ANN search the vector index for the nearest `take` rows,
3. load those rows through the authorized read path,
4. return whatever survives.

**This leaks, and it leaks badly.**

- **Membership disclosure.** A row the caller cannot read still occupies a slot
  in the top-`take`. Asking for 10 results and receiving 7 tells the caller that
  3 rows exist which they may not see, and roughly how semantically close those
  rows are to their query.
- **Content disclosure by probing.** The attacker controls the query string.
  Distances are a similarity oracle over the hidden corpus: binary-search the
  embedding space and you reconstruct the content of rows you were never allowed
  to read. This is the same class of attack as filtering on an unreadable
  column — the result *set* discloses the value even when the value is never
  returned.
- **Masked fields are not protected by masking.** If `body` is masked but the
  embedding was computed *from* `body`, the vector is a lossy but useful
  encoding of the masked content. Masking the projection does nothing.

The severity is worth stating plainly: rank-then-authorize turns a search
endpoint into a read primitive for the entire corpus.

---

## 2. The ordering

Golem inverts it. **Authorize first, rank second.**

`runtime/semantic.go`:

```go
// buildSemanticOptions — an ordinary authorized read
options := make([]golem.ReadOption[M], 0, 2)
if len(predicates) == 1 {
    options = append(options, golem.Where(predicates[0]))
}
options = append(options, golem.Take[M](semanticruntime.MaximumCandidates+1))
```

That read goes through the normal policy-enforced caller path — the same code
that serves any other query. It returns `[]golem.Row[M]`: the rows this caller
may see, with masked fields already absent.

Only then does ranking happen, and it ranks **within** that set
(`runtime/semantic.go:115`):

```go
ranks, err := app.semantic.Query(ctx, semanticModelID(...), indexName, query, keys, take)
```

`keys` is the list of primary identities extracted from the authorized rows. The
vector layer is never asked "what is nearest to this query?" — it is asked "of
*these* identities, which are nearest?"

**Consequence:** an unreadable row cannot occupy a result slot, cannot influence
the ordering, and cannot be probed for. The search surface discloses exactly
what the read surface discloses, and nothing more. There is no second
authorization model to keep in sync — which is the property that matters most,
because two authorization models always drift.

---

## 3. Fail closed at the ceiling

Authorize-then-rank has an obvious cost: the candidate set can be enormous. A
caller authorized to read a million rows cannot have a million vectors ranked
per query.

Golem bounds it and **refuses rather than truncating**
(`internal/semantic/runtime/manager.go:41-42`):

```go
MaximumCandidates = 10000
MaximumResults    = 1000
```

The read deliberately asks for one row *past* the ceiling
(`runtime/semantic.go:80`):

```go
// Fetch one row past the portable candidate ceiling so a larger authorized
// universe fails closed instead of silently ranking an arbitrary prefix.
options = append(options, golem.Take[M](semanticruntime.MaximumCandidates+1))
```

and then checks the count before doing anything else
(`runtime/semantic.go:89`, `validateSemanticCandidateCount`).

**Why the +1 matters.** Taking exactly `MaximumCandidates` is
indistinguishable from taking the first `MaximumCandidates` of a much larger
set. The extra row is what turns "I have a full page" into "there is more than I
am allowed to rank", which is a different fact and the one you must act on.

**Why the check runs before key extraction** — the subtle case, called out in
the source (`runtime/semantic.go:85-88`):

> a conditionally masked primary key must not turn an oversized authorized
> universe into an arbitrary, silently truncated candidate set.

Key extraction skips rows whose primary identity is masked. If you extracted
keys first and counted after, a corpus of 10,050 rows with 60 masked keys would
yield 9,990 keys — under the ceiling, no error, and a silently arbitrary
candidate set. The overflow check must therefore run against the *rows*, before
any filtering that could shrink the count.

This is a general lesson: **when a limit protects correctness, check it on the
quantity the limit is about, at the earliest point that quantity is known.**

---

## 4. The escape invariant

After ranking, the results are mapped back through `byKey`
(`runtime/semantic.go:120-124`):

```go
row, ok := byKey[rank.Key]
if !ok {
    return nil, fmt.Errorf("P9_SEMANTIC_QUERY: ranked identity escaped authorized candidates")
}
```

The ranker was only given authorized keys, so this cannot fire. It is there
because *if it ever does* — a bug in the vector layer, a stale index returning a
deleted key, a provider quirk — the failure mode must be an error, not a
returned row.

Cheap invariants at trust boundaries are worth writing even when unreachable by
construction. The cost is three lines; the alternative failure is silent
disclosure.

---

## 5. Storage: shadow tables, not columns

Embeddings live beside the owner table, never inside it
(`internal/provider/sqlite/render.go:99-100`,
`internal/provider/postgresql/render.go:122-124`):

```
<storage>_state    bookkeeping
<storage>_vec      the vectors
<storage>_hnsw     the ANN index (PostgreSQL)
```

The owner table is untouched. This matters for several reasons:

- **The vector is derived data, not user data.** It is reconstructible from the
  source columns and the embedding model. Putting it in the owner table conflates
  a cache with a fact.
- **Re-embedding never rewrites user rows.** Changing dimensions or switching
  models drops and recreates the shadow tables under a stable identity — the
  owner table is not migrated at all.
- **The authorized read path stays unaware of it.** `SELECT` on the model never
  drags a vector column along, and no policy has to know about a field the user
  never declared.

Provider-native throughout: `vec0` virtual tables via sqlite-vec on SQLite, and
`vector(N)` with an HNSW index using `vector_cosine_ops` on PostgreSQL
(`postgresql/render.go:137`). Not an abstraction over both — two native
implementations behind one contract.

---

## 6. Staleness: content-addressed, not timestamped

`<storage>_state` (`sqlite/render.go:101-113`):

```sql
record_key         TEXT NOT NULL PRIMARY KEY
source_hash        BLOB NOT NULL
space_fingerprint  TEXT NOT NULL
status             TEXT NOT NULL CHECK (status IN ('pending','ready','failed'))
attempt_count      INTEGER NOT NULL DEFAULT 0
error_code         TEXT
updated_at         INTEGER NOT NULL
```

A record is re-embedded when any of four things is true
(`manager.go:332`):

```go
if !exists || state.status != "ready" || state.fingerprint != fingerprint ||
   !equalBytes(state.hash, record.hash[:]) {
    dirty = append(dirty, record)
}
```

- **`!exists`** — new row.
- **`status != "ready"`** — a previous attempt failed or is incomplete. Failure
  is durable, so a crash mid-refresh resumes rather than silently leaving a row
  unindexed.
- **`fingerprint` mismatch** — the *embedding space* changed: different model,
  revision, or dimensions. Every vector produced by the old space is invalid, and
  this catches all of them without tracking which model produced what.
- **`hash` mismatch** — the source content changed.

**Content-addressed, not timestamped.** An `updated_at` comparison would
re-embed on every touch that didn't change the embedded text, and would miss a
change that restored an older timestamp. Hashing the source is exact: the same
input never re-embeds, a changed input always does.

Deletion is handled by the same pass — keys present in `_state` but absent from
the source scan are removed (`manager.go:339-343`, `:371-372`).

The whole thing is one reconciliation, invoked explicitly by the application
(`runtime/runtime.go:301`, `App.RefreshSemanticIndexes`). There is no trigger, no
background daemon, no write-path coupling. A failed embedding provider degrades
the index's freshness and nothing else — writes to the owner table are never
blocked on an external API.

---

## 7. What transfers

Independent of golem or Go:

**Order authorization before ranking, always.** Any similarity search over
access-controlled data has this shape. If ranking happens first, the ranking
itself is the leak, and no amount of filtering afterwards closes it.

**Make search reuse the read path rather than reimplement it.** The single most
valuable property here is that there is exactly one authorization
implementation. A separate "search ACL" is a second model that will drift from
the first, and the drift is invisible until it is a breach.

**Refuse rather than truncate when a bound protects correctness.** Silent
truncation converts a security property into a performance heuristic. Fetch one
past the limit so you can tell "full" from "overflowing".

**Check the bound on the right quantity, at the right time.** Anything that can
shrink a count between measurement and enforcement — masking, deduplication,
filtering — will hide an overflow.

**Keep derived data in its own tables.** Vectors are a cache. Storing them in the
owner table couples user-data migrations to embedding-model changes and forces
every read to reason about a column the user never declared.

**Content-address staleness.** Hash the input; compare the hash. Timestamps
over-trigger and under-trigger.

**Version the embedding space, not just the vector.** A fingerprint over
(model, revision, dimensions) invalidates every stale vector at once. Without it
you cannot tell which vectors came from which model.

**Assert invariants at trust boundaries even when unreachable.** "This cannot
happen" costs three lines; the failure it prevents is silent disclosure.

---

## Reading order in the source

1. `runtime/semantic.go` — the ordering, ceilings, and escape invariant. Start here.
2. `internal/semantic/runtime/manager.go` — refresh reconciliation and staleness.
3. `internal/provider/sqlite/render.go:99` and
   `internal/provider/postgresql/render.go:122` — the two storage layouts.
4. `internal/graphql/extension/semantic_search.go` — the generated GraphQL root
   and how `take` is capped against the model's page-size limit.
