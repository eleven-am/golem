# Continuation envelopes

This package owns the private authenticated envelope used by custom page
execution. It does not authorize data, serialize application arguments, store
rank snapshots, or establish that an application ordering is immutable.
It does not install a pageable operation or expose a public token-signing API.

Registered `PageQuery` execution is owned by `runtime.DispatchCallerPage`.
Application startup requires the exact generated page-resolver inventory and
an explicit `Pages` configuration for schemas declaring pages. This table is
separate from custom mutation/transaction registrations and cannot carry
`Within` grants. The scope callback receives the admitted actor snapshot; the
dispatcher pins that actor in context before scope derivation or resolution.

`custom.PrepareCanonical` bounds declared types and incoming values before
ordinary preparation validates or clones them. The dispatcher binds effective
take plus ordinary arguments, excluding only `after`, and passes the same owned
prepared values to the generated resolver decoder. The resolver receives a
typed `PageRequest` and returns a `PageBatch`, which contains candidate
consumption and private checkpoint bytes rather than a signed token.
Ordinary custom execution also selects this bounded preparation automatically
for registered page declarations; native embeddings cannot reach an unbounded
copy step merely by bypassing the HTTP compiler.

The immutable batch constructor bounds its own copying too. Oversized item
lists/checkpoints retain at most one element/byte beyond the shared protocol
ceiling as an invalid witness. Every such witness exceeds all legal declaration
limits and is rejected before a response can be served. These ceilings have one
Go owner in `internal/pagecontract`, shared by declaration validation, batch
construction and continuation admission; request-specific take remains tighter.

The GraphQL adapter normalizes within the admitted item bound, applies ordinary
caller hydration and DTO row gates, drops unreadable direct-model page items,
and encodes once with a nullable continuation placeholder. Only successful
encoding without field failures can finalize a token. The selection owner then
fills selected `next` aliases in the encoded page. A denied candidate still
advances the private traversal; hydration and encoding failures issue no token.
Hydration matches candidates through a separately validated primary-key
correlation, so private identity cells do not become public filters or selected
fields. Typed before-read hooks retain that correlation through their public
option roundtrip; hook-authored filters and lookup windows still pass normal
authorization and bound checks.

`Bind` hashes length-framed canonical operation inputs: contract, registered
operation identity, ranking revision, data incarnation, arguments and the
admitted actor's durable audience/profile/revision. The operation owner supplies
those canonical bytes. Process-local caller counters and policy-cache keys are
not durable audience identities. A changed data incarnation invalidates tokens
issued before restoration even when the schema and encryption keys survive.

`Begin` fixes the chain's absolute deadline. `Resume` authenticates that scope
and the complete envelope before trusting its private position or candidate
count. `Advance` retains the original deadline and requires positive bounded
candidate consumption and different, nonempty state. Retryable replay is
intentional; each request must still authorize its items. Candidate consumption
includes denied/deleted candidates and is independent of emitted item count.
An empty page can therefore have a continuation. Terminal pages do not mint a
token.

The page dispatcher must enforce its own tighter operation limits and ordering
contract. An opaque position plus an increasing consumption counter cannot
prove an arbitrary keyset advanced. A ranked snapshot needs an immutable order,
a deterministic tie breaker, a validated ordinal and storage whose lifetime is
at least the chain deadline. Keyset traversal needs an immutable ordering and
defined population boundary. Neither may silently regenerate a changed order
when a token is resumed.

AES-256-GCM uses the standard library's random-nonce implementation. Version,
key identifier and scope digest are authenticated additional data. Private
state is encrypted, so hidden candidate identifiers are not disclosed by base64
decoding. The fixed ceilings are 2 KiB state, 8 KiB encoded token, 10,000 consumed
candidates per advancement and eight keys. The deployment explicitly supplies
a whole-second absolute lifetime of at most 24 hours; there is no generated
startup key or default lifetime.

All serving processes must share the configured keyring. Constructing a new
codec with a new active key and retained retiring keys rotates issuance; removing
a retiring key revokes its tokens. Retain decrypt keys through their maximum
token lifetime and rotate before 2^32 encryptions across every instance sharing
the key. A restarted process with the same configuration can resume tokens.
Reducing the configured maximum lifetime also rejects existing envelopes whose
original lifetime exceeds the new maximum. Configuration material is copied
into immutable cipher state.
