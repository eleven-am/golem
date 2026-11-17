# Static policy template contract

`Subscriptions(ScopeTemplate(name, func(args Args) Predicate[Model] { ... }))`
declares one parameterized positive authority expression. The function is
interpreted statically. It contains one return expression using ordinary
predicate constructors, constants, generated field/relation handles and direct
exported argument fields. Application calls, captured values and control flow
are rejected. Argument types are author-owned named structs.

The compiler owns template identity, typed parameter inventory, normalization
and fingerprinting. The fingerprint includes derived capture keys, fields and
correlations. Capture projections are derived from the expression instead of
being authored or persisted separately. Independent existential branches retain
their path occurrences; composite correlation component order remains exact.
Every participating model must enable subscriptions. Unsupported through
relations fail explicitly until complete junction correlation evidence exists.

`FactTemplate(name, scopeName, func(args Args) Predicate[Model] { ... })`
declares a data predicate associated with a scope on the same model. Both
families share parsing, typed parameters, scalar binding and capture projection
owners. Data expressions also admit negation, negative/universal relations,
ordered scalar comparisons and presence tests. Authority expressions retain
their positive relation grammar. JSON/list predicate algebra and implicit
through relations are explicitly unsupported by static templates.

The scope footprint describes authority paths, not arbitrary data-fact paths.
For example, an unrestricted owner scope has no relation footprint even when
its readiness facts inspect hidden children. Complete producer inventory must
also come from static fact templates whose generated bindings are the actual
fact bodies, associated with the policy's one scope binding. Fact fingerprints
include that authority contract. Body projection retains paths in dead branches
and distinguishes repeated relation occurrences. Capture must derive the union
of authority and body footprints, including complete empty extents for negative
questions. Neither fake dead branches in an unrestricted scope nor separately
authored approximate watch predicates establish coverage. Capture activation
must verify registered provenance for every participating physical definition;
inline definitions alone do not prove coverage.

ContractIR v8 carries this metadata and the generated ABI is `p8-go-abi-v8`.
The runtime registry recomputes the contract and exposes immutable copies.
Older generated contracts follow the existing regenerate/drain refusal. A
mixed-version reader/writer and historical-backlog compatibility window remains
a separate acceptance requirement; this version bump does not prove it.

Code generation emits a `<Model>Scopes` namespace. Each method accepts the
author-owned argument type and returns an opaque `golem.BoundFactScope[Model]`
for `rules.BindFactScope(...)`. This value cannot enter caller predicates or
selections. The generated expression uses stable field/relation handles; the
declaration function never executes to construct policy truth.

The matching `<Model>Facts` methods return opaque `golem.BoundFact[Model]`
values. `rules.BindFact(...)` creates a reference owned by that rules builder.
The registry-backed binder verifies the name, template, body, arguments and
exact authority association. The bound value cannot enter caller expressions;
the resulting policy reference follows the existing fact ownership rules.

The public freezer owns exact typed arguments, including empty sets and full
timestamp precision. Frozen policies with template bindings use version 4;
ordinary policies and inline fact scopes retain versions 2 and 3. The runtime
binder checks the template identity, digest, complete parameter inventory and
types, then reconstructs its expression through ordinary scalar binding and
compares normalized executable truth. Attached metadata alone is insufficient.
Scope and fact templates use shared immutable `TemplateArgument` values.
Bound provenance survives policy normalization, fact resolution and authority
gates even when a body becomes constant. Scope provenance also survives when
data definitions are unused.

The registry derives an immutable union of private capture fields across all
registered templates. Scalar SQL projects these fields independently of caller
field grants and public result selections. Batch and cascade images already
contain complete rows. Schemas without templates retain their prior SQL.

Successful write applications feed a transaction-local image journal before
mutation-capable observers or hooks run. Savepoint-owned contributions retain
the first before-image and final after-image, including deletion/recreation;
rolling back a nested write restores the earlier surviving after-image. Known
absence is distinct from an incomplete image. Cascaded changes and actual
model-row version stamps participate; global clock bookkeeping does not.
The journal has a one-MiB scalar payload budget (`policy_history_bytes`) and a
bounded application count. It is cleared on commit or discard.

The private snapshot reader applies the journal to bounded physical equality
queries. Before-state reads remove current matching changed rows and restore
every matching first-before image, including rows whose old correlation no
longer matches current SQL. Final-state reads use final after-images. Missing
fields, changed write epochs and overflow are errors, never empty extents.
Queries use the existing scalar codecs and project only static capture fields;
the overflow row is a sentinel. Scalar payload and graph work are bounded by
`policy_history_bytes` and `policy_history_nodes`. A failed reader cannot be
reused as complete evidence.

Snapshot admission requires an owned captured write transaction. SQLite uses
its held write transaction; PostgreSQL requires an explicitly Serializable
transaction. Default PostgreSQL isolation is refused. These checks do not yet
constitute the full provider/concurrency acceptance matrix.

The registry also retains each template owner's immutable path inventory.
The private graph assembler follows changed images backwards in each phase
to discover affected roots, then walks the declared paths forwards to capture
complete relation extents. Root absence and complete empty extents are explicit;
an unrecorded root or relation remains unknown. Independent predicate branches
share physical rows without filtering their witnesses or multiplying joins.
The ordinary policy evaluator consumes only the condition's finite dependency
tree from this graph. Its work budget includes recursive immutable-record
construction, so deep paths with wide leaves cannot evade the capture bounds.

The shared immutable graph has a canonical version-1 codec. It binds the exact
producer generation, the static capture fingerprint, and an ordered commitment
to the original V3 fact contents and private before-images. V4 facts reference
that graph; only the first fact stores it in a versioned private-evidence
container in the existing outbox. The ordinary V3 row blob remains unchanged
inside that container. The graph and all container overhead count toward the
existing one-MiB combined outbox limit. Sealing rechecks both physical-write and
fact-buffer revisions before staging, and flush refuses later changes.

The publisher validates the whole V4 causation and its unique first-fact graph
before publication. Transport V1 preserves V4 metadata and before-images while
omitting the graph. Model-routed subscribers may never receive the first event,
so consumers must load and validate the graph from the producing outbox; a
missing retained graph cannot become current-state evidence. No new physical
table or independently retained graph is introduced.

Automatic writers still emit V3. Historical consumer lookup, source routing
and authority evaluation remain required before activating V4 writing or
related scopes. Related scopes continue to fail runtime activation; no V3
bytes are reinterpreted as historical route evidence.
