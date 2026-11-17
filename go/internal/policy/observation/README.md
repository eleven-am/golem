# Policy fact authority

Named facts inspect physical relationships without granting access to the
related rows. Their observation contract permits conservative source
invalidation, so an author must explicitly authorize the domain in which that
activity may be disclosed.

One immutable `ir.FactScope` belongs to the whole fact-owning policy. Every
resolved fact retains this same scope identity. Effective row and selected-field
conditions intersect the scope **after** ordered rule resolution, for every
caller action. In particular, negative facts, denials and unconditional
fallback grants cannot escape the domain. Raw fact-reference resolution retains
ordinary Boolean meaning; it does not inject a scope underneath negation.

The first release requires configured subscriptions for the owner and every
scope/body participant, including unused definitions. Policy activation checks
this inventory before query or write grants become available, rather than
waiting for a stream to open. Target read grants remain independent: a subscribed
physical dependency may still be unreadable to that caller.

Fact declaration freezing retains authored logical branches until binding;
`true OR relationship` cannot erase an inline participant before observation
admission checks it. Compiled templates independently retain their complete
static inventory through parameter normalization. Template matching compares
normalized copies and returns the authored condition, so additional dead
participants cannot disappear during equivalence checking either. Both use
the ordinary predicate freezer and evaluator; ordinary caller predicate
normalization and the later policy truth normalization keep their existing
semantics.

The shared `policy/capture` validator checks every authored definition against
the registered scope/body projections before normalization, including unused
definitions and dead branches. References retain their complete physical
prefix when coverage is checked. Admission has one aggregate work budget;
delivery reuses the same static checks under its matcher budget and cancellation.
Registered model coverage is distinct from a row's presence as a graph root:
additional captured scalar scopes still use their exact historical row proof.

Scopes support scalar predicates, Boolean composition, explicit constants and
positive correlated `Is`/`Some` witnesses in the predicate IR. Actor grant keys
come from the authoritative actor snapshot;
they are not a cache of current fact truth. An explicit `true` scope authorizes
conservative activity observation throughout the root domain. A missing scope
is invalid. Related authority gates retain a distinct physical boundary during
normalization, query planning and mutation final verification. Negative
relationship predicates remain data/rule truth inside a positive authority
cover; a historical authority claim must not depend on proving absence.

Ordinary runtime admission rejects every named-fact declaration with a
route-evidence error, including scalar, unused and operation-only definitions.
A local body can still be observed through a relationship, so its syntax cannot
establish a safe reader-only exemption. Internal historical admission/planning
accepts facts under the captured writer contract; related scopes also require
registered bindings. Runtime selects this mode only when the actual binary
writer and registered capture inventory both require history. The default
reader-release binary writes V3 and does not activate named-fact policies.

Observation has two separate questions:

- **Authority:** the exact policy scope plus the source route established by
  captured correlation keys.
- **Relevance:** conservative Boolean bounds on whether a dependency could
  affect the source. Unknown relevance may widen work inside the domain; it
  must never widen authority.

Missing data-witness fields therefore retain a scoped route for conservative
recovery. Missing fields in a historical scope remain an error. A negative
relationship quantifier may invert data relevance; it never inverts authority.

The policy scope is applied once when entering a fact region. A nested fact
reference beneath a self-relation still belongs to the original policy root;
it must not rebind that scope to the related row. A related fact-owning row is
also observed independently. Historical planning walks scope relationships
separately from data relationships, with a constant scope-only body and no
unused data-template prerequisite. Scope, data and owner-row roles retain
distinct path identities. Shared scope walks deduplicate only within the same
exact bound scope and ordinary source occurrence, so concurrent loss of data
truth cannot hide a domain change.

A stream keeps its initial observation graph, not an initial Caller execution.
Before a dependency notice can invalidate a fact-owning stream, the runtime
resolves the principal again and fingerprints the current expanded row,
caller/hook filters and complete selected-field masks. Ordinary readable
members and physical members share this freshness boundary. A changed contract
ends the stream with a generic resync, without attributing the signal to hidden
activity. Reopening builds a new graph. Configured periodic revalidation uses
the same comparison, including policies whose fact definitions are unused.
Newly broadened graphs still need reopening, periodic revalidation, expiration
or explicit policy invalidation when no notice reaches the initial graph.

Full selections lift child row policies, selected masks and count policies
through their parent relation, retaining each child's own scope and its local
and ancestor read context. Scope ownership is tracked independently of surviving
fact references. An unused fact-owning child therefore still receives fresh
principal checks and a scope-only historical witness; its unused data bodies do
not become observation dependencies. Lifted questions share the fact expansion
budget, in addition to the observation path limit.

Fresh planning invokes source read-before hooks per dependency notice and per
configured periodic check. This work runs outside the hub lock and follows
both subscriber and hub cancellation. Hooks must tolerate repeated execution;
plan limits and queues bound work, but representative workload measurements
are still required before making latency or capacity claims.

Old and new persisted routes are both needed for deletes and reparenting.
Missing routing or authorization evidence cannot be replaced by a universal
audience. Runtime evidence failures and history compatibility still require
integration acceptance before release. This is not a release claim for the
complete event contract. Historical paths retain every fact-owning scope on
their captured ordinary chain, including positive conjunctive to-one side
witnesses. Each requirement names its exact captured primary identity, complete
scope and occurrence. A non-root captured owner needs present images in both
phases; an explicit root retains its certified absence semantics. Incomplete
evidence remains unknown.

Current scope, ordinary source and local guard probes share one SQL statement.
Each candidate keeps its own conjunction even when probes share execution.
Historical scopes must hold together in the same phase. Generic cleanup may
waive a currently false scope only when this causation certified that scope's
loss; a different scope revoked later cannot borrow the waiver.

Filter and selected-field questions inherit the source row's authority and
ordinary guards. They do not create separate source audiences. Data-fact truth
stays conservatively projected, so a false data fact does not erase authority
or prevent recovery. Identical complete questions reuse one walk. Local data
facts sharing one scope retain their Boolean algebra; conditional or negative
side scope boundaries and distinct-scope alternatives remain explicit coverage
errors. Physical fact/scope traversal never imports target read scopes.

Authority beneath an uncaptured ordinary prefix can move forward only through
matching positive to-one main-path edges. Source and intermediate fact-free
guards remain current; the carried child scope and local guards are certified
at the exact captured cut. An unrecorded source-owned scope or an unrelated
side edge remains unknown. This lets profile- and playlist-owned rows recover
through a recorded Video without treating their ownership as a historical fact.

For example, a copy's before image can name Video V after its last granted
copy is deleted. If another transaction reparents V from Media M1 to M2 before
delivery, joining that old copy with current V invents authority for M2 and
loses the M1 route. Complete same-causation delivery cannot repair this
cross-causation ambiguity.

Generated scope and fact templates now define the actor-independent path and
projection inventory. Bound predicates retain the template and typed argument
provenance; there is no separate author-supplied capture declaration. The
registry fingerprints the complete inventory, including independent authority
branches. Capture is independent of connected subscribers.

The mutation scope owns first-before and final-after row images, including
savepoint rollback. The graph assembler requires a provider-certified snapshot
and explicit complete relationship extents. Outbox ordinals describe declared
graph order, not physical write chronology. Witness reads never become
synthetic updates, and membership truth is never cached.

The staged V4 format stores one private graph anchor and commits it to the
complete ordered causation. Transport carries references rather than the
graph. A consumer recovers and verifies the bounded complete causation from
one database statement, checks exact notice membership and producer schema,
and retains only immutable physical evidence for that notice. A subscriber's
canceled load cannot poison another subscriber's evidence lookup.

Historical conditions use the ordinary policy evaluator with exact producing
descriptors. A missing extent is unknown; partial to-many rows never establish
completeness. A missing to-one extent may be derived only from same-phase
correlation and a declared unique target key, or exact primary-key absence.
Present captured rows and known-absent roots remain different operations.

The V4 consumer discovers owners only in the committed graph. It enumerates
complete recorded sibling routes and certifies the suffix between a stable
cut identity and its exact owner. The current ordinary source prefix stops at
that cut's primary key; it cannot rejoin today's cut-to-owner relationship or
cross another fact-owning policy. A matching suffix is not a historical row
visibility claim or current authorization.

The cut's own ordinary guard is checked now as well as historically. Each
recorded path retains the exact intermediate primary identities; its mutable
ordinary guards become identity-bound current questions. Historical truth and
current guards must belong to that same path. Distinct paths remain alternatives
and cannot borrow each other's successful predicates. Current guards reject
fact-owning boundaries, including sibling branches whose fact nodes disappeared
during Boolean projection.

Current source-prefix, intermediate-guard and explicit owner-scope probes share
one SQL statement per owner. Named-unique and composite cut routes retain the
stable primary identity even when a unique value moves to another row.
Current rule bodies, field masks and fact truth cannot replace the scope fence.
A certified true-to-false scope loss permits generic, unattributed cleanup;
an unchanged historical scope cannot attribute activity after current scope is
revoked. Direct-owner deletion may remove the current source row as well.
An uncaptured source prefix additionally relies on its ordinary source-event
cleanup when that prefix changes; the local SQLite regression verifies this
backstop without disclosing the source to another actor.

Physical dependency delivery always uses the historical handler. Missing
history is refused before any current-target snapshot or live route probe;
there is no V3 physical live-join fallback. Ordinary V3 members keep their
existing judges and dispatch. Historical observation plans refuse `BindChange`
even for a local/empty plan. The default writer remains V3. Controlled
writer-release rehearsals use the native
writer, publisher and subscription factory to verify related-scope gain/loss,
false/unused data facts, actor isolation and delayed data reparenting on SQLite.
The freshly generated six-model Frames kernel fixture also exercises read and
private-write equivalence on SQLite and PostgreSQL, then native hidden-copy
update, deletion and recreation through Media, Video, Episode, WatchProgress
and PlaylistVideo streams. Related filters retain source authority, foreign
rows stay silent, certified losses are unattributed, and delayed reparenting
cannot borrow the new Media's authority. Full application adoption, GraphQL
selection coverage beyond this fixture, remaining route-loss and compatibility
gates are separate release requirements. The same generated application's HTTP
handler checks actor parity and nested progress/playlist ownership. A physical
fact can grant a source row while ordinary `some: {}` still finds no readable
target. Selecting a target model with no read grant retains its ordinary
`FORBIDDEN` / `NOT_GRANTED` refusal.

History finalization has one pre-flush owner after application callbacks and
fact ordering. It uses the existing binding, registry and historical resolver;
it neither opens another transaction nor holds the state mutex across capture
reads. Empty/uncaptured transactions retain V3. A sealed graph is reusable only
while both history and fact epochs match, and a failed required capture cannot
fall back to V3.

The binary's automatic writer format also controls its transaction snapshot
requirement. A V4 writer with registered capture templates selects PostgreSQL
Serializable before beginning any of the six mutation transaction families;
an explicit weaker option is refused before application callbacks. A successful
driver begin is the only issuer of that binding's certificate. Reader-mode V3
and no-template applications retain their previous isolation behavior, including
nil/default driver options. SQLite retains its existing write-transaction
ownership. Driver tests prove requested options and refusal. Disposable
PostgreSQL 17.9 tests additionally check the server isolation level, committed
reparent history, and a real two-connection conflict retry whose replacement
graph uses the fresh attempt's before state. A separate disjoint-write case
reads a complete relation extent while another transaction inserts a member;
the resulting write skew must cause a serialization refusal, and fresh replay
must capture the winner's committed state. Giving a RepeatableRead transaction
a false Serializable certificate makes that regression fail. Broader provider
and deployment gates remain pending.

The same binary writer contract controls producer admission and legacy fact
routing. A V4 writer with capture inventory rejects current `CDCAdapters` at
startup: their changed-row images cannot certify the source transaction's
complete historical relationships. Its physical fact consumers also refuse
retained V3 notices with generic revalidation before decoding images or probing
current routes. They never attribute old activity through today's intermediate
relationships. Ordinary event members, reader-mode V3 and no-template
applications retain their existing compatibility. Activating this contract
requires handling incompatible backlog; it does not reconstruct old evidence.

This explicit authority contract supersedes inference from the upper bound of
the source's data facts and the special repair for shared to-one paths.
