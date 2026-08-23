# Similarity search

Declaring a semantic index generates two capabilities, not one: search by text,
and **find rows like this row**. Neither requires a custom resolver.

For a model `Post` with a semantic index named `related`, generation emits:

```go
caller.Posts.SearchRelated(ctx, "climate policy", 10)                 // by text
caller.Posts.SimilarRelated(ctx, social.Posts.ByID.Value(id), 10)     // by example
```

and two GraphQL roots:

```graphql
searchPostsByRelated(query: String!, take: Int, where: PostWhereInput): [Post!]!
similarPostsByRelated(source: PostWhereUniqueInput!, take: Int, where: PostWhereInput): [Post!]!
```

Both are available on `Caller` and `System` clients, and both accept an optional
predicate. Results are `SemanticResult` values with `Distance()` and
`Similarity()`; for similarity, the distance is between the candidate's stored
vector and the **source row's stored vector**.

## Why this is built in rather than hand-written

An application could try to implement "similar posts" by reading the row,
concatenating its fields, and passing that text to `SearchRelated`. That is
wrong, not merely slow.

Stored vectors are embeddings of a canonical framed document, not of raw field
text. Every source is framed as

```
golem-semantic-document:v1\x00<fieldID>\x00<present>\x00<len>:<text>...
```

so hand-composed query text is embedded under a *different encoding* than every
vector it is compared against, and it pays an embedding-provider round trip per
request to do it. The correct query vector for "like row 47" is row 47's stored
vector, which managed storage already holds. `SimilarRelated` looks it up.

**A similarity request makes zero embedding-provider calls.** The acceptance
tests assert this on both providers by counting calls into a fake provider.

## The disclosure rule

**The caller must be able to read the source row.**

This is not a convenience choice. Search authorizes before it ranks, because
rank-then-authorize turns distances into a similarity oracle over rows the
caller cannot read. Similarity would reintroduce exactly that oracle through the
*parameter* if the source were not authorized: answering `similarPosts(47)` for
a caller who cannot read post 47 discloses that 47 exists, that it is indexed,
and — through its neighbours — approximately what it is about. The stored vector
encodes the source fields, including fields policy masks. And unlike a query
string, a key parameter *enumerates*: walk the key space, harvest a
neighbourhood per hidden row.

So `SimilarRelated` resolves the source through an ordinary authorized
`FindUnique` first. Everything else follows from that one decision:

- **Unauthorized and nonexistent sources are indistinguishable.** Both produce
  the identical `CodeNotFound` "record not found" error — never an empty list,
  which would make similarity a second existence probe with different semantics
  than `findUnique` against the same selector.
- **A readable row whose primary identity is masked also fails as not found.**
  Search excludes such rows from candidacy; a source cannot be excluded, so
  it fails — with the same error, because "cannot participate in semantic
  identity" must not be a distinguishable third state.
- **`System` clients see every row**, consistent with system search ranking over
  a policy-free candidate set.

The GraphQL argument is the model's `WhereUniqueInput`, not an opaque record
key. The record-key encoding is internal to managed semantic storage; exposing
it would freeze that encoding as public ABI and hand callers a probe format. The
selector is already public ABI, already what `findUnique` accepts, and already
policy-checked by the read that resolves it.

## The source is excluded from its own results

"Rows like this row" is irreflexive. The source is dropped while the candidate
key set is built — *before* ranking — so no `where` predicate can reintroduce
it, and `take` means "up to `take` rows other than the source". Because the key
is removed from both the key list and the row map, the escape invariant rejects
a ranking backend that returns the source anyway.

## Failure modes

| Condition | Result |
|---|---|
| Source absent, unreadable, or identity masked | `CodeNotFound`, identical to `findUnique` |
| `take` out of range, or more than one predicate | `embedding.CodeInvalidInput` |
| Source vector absent or not `ready` after refresh | internal `P9_SEMANTIC_QUERY`, never an empty result |

The last row matters: there is no fallback that composes field text and embeds
it, because composed text is the wrong encoding — the exact problem this feature
exists to eliminate.

## Storage and cost

No new tables. Similarity reads the same `<storage>_vec` and `<storage>_state`
that search maintains, and reuses the same refresh reconciliation. In steady
state a similarity request costs two authorized reads and one vector ranking:
no provider round trip, and no `semantic.provider` observation span — only
`semantic.refresh` and `semantic.rank`, exactly as search emits.

See [SEMANTIC-INDEXES.md](./SEMANTIC-INDEXES.md) for declaring an index, and
[SEMANTIC-SEARCH-DESIGN.md](./SEMANTIC-SEARCH-DESIGN.md) for why search
authorizes before it ranks.
