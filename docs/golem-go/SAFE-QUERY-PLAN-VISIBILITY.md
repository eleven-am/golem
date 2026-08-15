# Safe query-plan visibility implementation contract

Status: **implemented locally; the public report, renderer identity maps,
bounded SQLite/PostgreSQL capture, provider-neutral typed assembly, Caller
runtime orchestration, generated Caller surfaces, observation, the public
compatibility inventory, and the mandatory live SQLite/PostgreSQL external
evidence all exist and pass; operator documentation, live correlated-relation,
deferred-batch, and non-primary-key PostgreSQL fixtures, and integrated release
evidence pending**.

Audience: the engineer implementing the diagnostic and the reviewer deciding
whether it is complete. This contract covers a sanitized, explicitly requested
plan for an authorized typed read. It is not raw SQL/provider access.

Implementation handoff rule: every “must” is acceptance criteria. If a
provider plan node cannot be mapped safely into the closed report, classify it
as unknown or refuse the report. Never return the raw provider plan as a
fallback.

## 1. Product decision

Generated Caller read clients will expose explicit `Explain...` methods. They
prepare the same actor-authorized request as execution, ask the provider to plan
the rendered statement without executing the data query, and return a bounded
closed structural report.

The report helps an operator detect:

- a root full scan;
- primary/unique/ordinary index use;
- provider-selected join strategy;
- temporary sort/materialization;
- correlated versus deferred-batch relation loading;
- multi-statement shapes and their bounded range;
- aggregates/groups/scoped-query structure;
- unknown provider nodes; and
- SQLite/PostgreSQL plan divergence.

It does not expose raw SQL, bind values, database object names, cost/cardinality
estimates, timing, buffers, WAL statistics, rows, or query results.

## 2. Non-goals

Version 1 must not add:

- raw `EXPLAIN`/`EXPLAIN ANALYZE` output;
- `ANALYZE`, query execution, sampled rows, timing, costs, row-count estimates,
  buffers, or provider settings;
- SQL text, parameter values/types, physical table/index/schema names, DSNs, or
  credentials;
- query hints, forced indexes, optimizer settings, planner GUC changes, or
  provider-specific options;
- a GraphQL/HTTP query-plan root;
- automatic plan collection on every production request;
- a background sampler, cache, worker, queue, or plan-history database;
- mutation/migration/event/semantic-provider planning;
- policy bypass or a System-only unfiltered view; or
- a claim that a plan is stable across provider versions, statistics changes,
  schema changes, or database contents.

This feature diagnoses a plan. It does not optimize or approve it.

## 3. Public package and generated surface

Add a public immutable result package:

```text
github.com/eleven-am/golem/go/queryplan
```

For each generated Caller model client, add the read methods supported by that
model:

```go
ExplainFindMany(ctx, options...) (queryplan.Report, error)
ExplainFindFirst(ctx, options...) (queryplan.Report, error)
ExplainFindUnique(ctx, selector, options...) (queryplan.Report, error)
ExplainCount(ctx, options...) (queryplan.Report, error)
ExplainAggregate(ctx, request) (queryplan.Report, error)
ExplainGroupBy(ctx, request) (queryplan.Report, error)
ExplainRelationGroupBy(ctx, request) (queryplan.Report, error)
ExplainScoped(ctx, query) (queryplan.Report, error)
```

Only operations already generated for the model receive matching explain
methods. Version 1 does not generate them on `System`, `CallerTx`, `SystemTx`,
GraphQL, mutation clients, event clients, or custom/computed resolvers.

The application decides who may call these Go methods. Golem must not expose
them over a network automatically.

## 4. Preparation and authorization semantics

An explain call must use the same production preparation path as the matching
Caller operation:

1. validate/freeze the exact typed request and limits;
2. use the same immutable actor/policy snapshot already owned by that generated
   `Caller`; an explain method must not re-resolve a different policy snapshot
   than the matching execution method;
3. run the matching before-read hook exactly once when that production
   operation has a before hook, because it may narrow or transform the request;
4. re-freeze and re-authorize the transformed request;
5. compile the same policy, field classification, relation hydration,
   pagination, analytics/scoped, and provider statement shape;
6. ask the provider to plan the bounded statements without executing the data
   query;
7. sanitize/map the provider result into the closed report;
8. release database resources; and
9. invoke the ordinary panic-safe observer only after release.

After-read hooks, computed resolvers/loaders, event delivery, and application
row decoding do not run because no result rows exist.

Read hooks are application code and may themselves perform external side
effects. The documentation must say that `Explain...` runs the same before hook
as the real operation when that operation has one. Version 1 does not invent
new hooks for count, analytics, grouping, or scoped operations that have no
production hook today. Golem itself performs no application mutation.

A denied policy, invalid/masked filter/order/selector, invalid hook result, or
limit refusal follows the ordinary public error and executes zero provider
plan statements. Explain must never reveal the plan of a request the actor
could not execute.

## 5. Provider planning boundary

### 5.1 SQLite

Use bound `EXPLAIN QUERY PLAN` on the exact rendered statement. Do not use full
SQLite bytecode `EXPLAIN`. Parse only documented structural rows and immediately
discard their raw detail strings after mapping recognized table/index aliases
through the exact physical registry.

### 5.2 PostgreSQL

Use bound planning only:

```sql
EXPLAIN (
  FORMAT JSON,
  ANALYZE FALSE,
  VERBOSE FALSE,
  COSTS FALSE,
  SETTINGS FALSE,
  BUFFERS FALSE,
  WAL FALSE,
  SUMMARY FALSE
) <rendered statement>
```

Never use `ANALYZE TRUE`. Do not change `enable_*`, work memory, search path,
statistics, transaction isolation, or any other planner setting. Parse the JSON
into a private bounded shape and discard the raw bytes after sanitization.

### 5.3 Deferred relation batches

Some relation statements require parent keys that exist only after executing
the root query. Explain must not execute the root to obtain them and must not
invent representative key values.

For those statements the report contains a `DeferredBatch` node derived from
Golem's typed relation plan, including target model/relation IDs, batching
capacity, and minimum/maximum possible statement counts. It does not claim an
actual provider access path for the deferred statement.

If the typed request and configured read limits do not provide a finite bound
for a deferred branch, version 1 returns `PLAN_UNAVAILABLE`. It must not execute
the root query to discover a bound, invent representative cardinality, or use
`MaxUint32` as an undocumented infinity sentinel.

Correlated relations contained in the root SQL are covered by the provider
root plan normally.

## 6. Closed public report

The public package exposes immutable accessor types rather than exported
mutable structs:

```go
type Report struct { /* opaque */ }
type Statement struct { /* opaque */ }
type Node struct { /* opaque */ }

func (report Report) FormatVersion() uint16
func (report Report) Provider() golem.Provider
func (report Report) Operation() Operation
func (report Report) RootModelID() golem.ModelID
func (report Report) Statements() []Statement
func (report Report) MinimumExecutionStatements() uint32
func (report Report) MaximumExecutionStatements() uint32
func (report Report) Warnings() []Warning
func (report Report) CanonicalDigest() [32]byte

func (statement Statement) Ordinal() uint32
func (statement Statement) Purpose() StatementPurpose
func (statement Statement) Root() Node

func (node Node) Kind() NodeKind
func (node Node) Access() AccessKind
func (node Node) ModelID() (golem.ModelID, bool)
func (node Node) FieldIDs() []golem.FieldID
func (node Node) RelationID() (golem.RelationID, bool)
func (node Node) IndexID() (IndexID, bool)
func (node Node) BatchCapacity() (uint32, bool)
func (node Node) MinimumExecutionStatements() (uint32, bool)
func (node Node) MaximumExecutionStatements() (uint32, bool)
func (node Node) Children() []Node
func (node Node) Warnings() []Warning
```

The three bounded-batch accessors are present only on `deferredBatch` nodes.
They return `(0, false)` on every other node. A deferred batch has a positive
capacity, a finite maximum, and `minimum <= maximum`; zero is a valid minimum
because an authorized root query may produce no parent keys. These are typed
planning bounds, not observed or estimated row counts.

`IndexID` is a fixed `[16]byte`-equivalent value owned by `queryplan`; it has no
constructor from names and grants no query capability.

Required closed enums:

```text
Operation:
  findUnique | findFirst | findMany | count |
  aggregate | groupBy | relationGroupBy | scoped

StatementPurpose:
  root | relationBatch | policyHydration | analytics | scoped

NodeKind:
  access | join | sort | aggregate | materialize |
  correlatedRelation | deferredBatch | constant | unknown

AccessKind:
  none | primaryKey | uniqueIndex | index | bitmapIndex |
  fullScan | constant | unknown

Warning:
  FULL_SCAN
  TEMPORARY_SORT
  MATERIALIZATION
  DEFERRED_BATCH
  MULTI_STATEMENT
  UNKNOWN_PROVIDER_NODE
```

The first statement purpose is fixed by the operation: find/count operations
use `root`, aggregate/group operations use `analytics`, and scoped operations
use `scoped`. Any later statement is only a `relationBatch` or
`policyHydration`; the report producer rejects a second primary statement or a
purpose that contradicts the operation.

Boundedness refusal is an error, not a report warning: version 1 returns
`PLAN_UNAVAILABLE` or `PLAN_TOO_COMPLEX` and no partial `Report`, so it cannot
truthfully attach a warning to a value that does not exist.

Every slice/accessor returns a copy. Report format version starts at 1. The
canonical digest covers only the sanitized closed report, never raw SQL/plan
bytes.

## 7. Mapping and redaction

Provider table/index/alias strings are untrusted diagnostic input. Map them
through the exact registry used to render the statement:

- recognized root/relation aliases map to stable ModelID/RelationID;
- recognized physical indexes map to stable `queryplan.IndexID`;
- expressions map only to stable FieldIDs already present in the typed plan;
- an unrecognized name produces `unknown` plus
  `UNKNOWN_PROVIDER_NODE`; and
- raw names are discarded and never included in errors or trusted callbacks.

The mapping registry is emitted at the point each private renderer allocates an
alias; the sanitizer must not reverse-engineer alias naming conventions. Read,
analytics, scoped, and policy-relation SQL fragments each contribute immutable
alias facts for the exact statement. The physical registry likewise retains the
typed primary-key, unique-index, and ordinary-index identities needed to map a
recognized physical access object. Missing alias/access registration produces
`unknown` plus `UNKNOWN_PROVIDER_NODE`; it never falls back to parsing a name.
Every alias matcher must also be unambiguous within its exact rendered
statement. Independently compiled policy fragments may not reuse one flat alias
token and then ask the sanitizer to infer scope from provider-plan position;
the renderer must allocate statement-unique aliases or retain an equally exact
typed scope proof at allocation time.

The report must not contain:

- predicate operands or their hashes;
- selector IDs/values beyond stable schema identity;
- actor/principal/session values;
- literal limits, cursors, search strings, JSON paths containing authored keys,
  or GraphQL arguments;
- estimated/actual rows, costs, widths, timing, loops, memory, buffers, WAL, or
  provider version strings;
- SQL/placeholder text; or
- raw provider errors.

Stable model/field/relation/index identities, closed operation/strategy enums,
and statement bounds are the complete public vocabulary.

## 8. Boundedness

Version 1 limits one report to:

- 32 statements;
- 256 nodes total;
- depth 32;
- 64 field IDs per node;
- 64 warnings; and
- 256 KiB canonical encoded bytes.

The lower existing query/statement limits still apply. If raw provider output
or the sanitized report exceeds a bound, abort with closed
`PLAN_TOO_COMPLEX`; do not return a partial report or raw suffix.

Provider parsing must be iterative/bounded or explicitly depth-checked. It must
not recursively allocate from attacker-controlled provider strings without a
limit.

## 9. Errors

Add closed query-plan error classification:

```go
type ErrorCode string

const (
    ErrorUnavailable ErrorCode = "PLAN_UNAVAILABLE"
    ErrorTooComplex  ErrorCode = "PLAN_TOO_COMPLEX"
    ErrorInvalid     ErrorCode = "PLAN_INVALID"
)

func CodeOf(error) (ErrorCode, bool)
```

Ordinary authorization/input/hook failures remain ordinary `golem.Error`
values and codes. Query-plan errors have fixed public messages. A wrapped cause,
when retained, is a fixed sanitized internal classification rather than a raw
driver/provider error; `Error()`, trusted callbacks, and the report never expose
raw provider diagnostic input.

## 10. Observation contract

Add one truthful observation operation for an explicit explain call. It must
report:

- provider and root ModelID;
- one finish-phase record buffered until every provider row and connection is
  closed;
- exact number of provider `EXPLAIN` statements;
- aggregate count equal to sanitized node count;
- refused reason for input/policy/complexity/provider failure; and
- no SQL, names, values, report body, or plan digest as attributes.

The observer runs only after every query-plan row/connection is closed. A
blocking/panicking observer cannot alter the returned report or leak resources.
Update the closed observation inventory and dynamic production coverage rather
than emitting an unregistered operation.

## 11. Documentation and operational warnings

Documentation must state:

- call Explain only from trusted diagnostics/tests, not directly from an
  untrusted client endpoint;
- a matching before-read hook executes when the real operation has one;
- PostgreSQL/SQLite planning acquires a real pool connection;
- provider statistics/version may change a plan without source changes;
- a full scan may be correct for a small/selective query and an index does not
  guarantee performance;
- deferred batch access paths are intentionally unknown without executing the
  root query; and
- the report is evidence for investigation, not a performance guarantee.

## 12. Mandatory acceptance gates

The generated Caller surface is read-only and exact:

1. `TestQueryPlanDeclarationDiscoverySupersetAndFinalExactRegistryBothCompile`
   and the surface assertions inside
   `TestExternalGeneratedApplicationQueryPlanIsCallerOnlyTypedAndRedacted`

Authorization, hooks, and field policies match execution preparation:

2. `TestQueryPlanAuthorizationHooksAndExecutionPreparationAreOneBoundary`,
   `TestQueryPlanHookVetoAndTransformedFieldDenialExecuteZeroProviderStatements`,
   and `TestQueryPlanInvalidOriginalInputRunsNoHookAndNoProviderStatement`

Provider mapping discards raw detail:

3. `TestQueryPlanSQLiteMapsFullPrimaryAndOrdinaryIndexWithoutRawDetail` and
   `TestQueryPlanSQLiteDerivedAliasCannotBecomePhysicalAccess`
4. `TestQueryPlanPostgreSQLMapsScanJoinSortAndIndexWithoutRawJSON` and
   `TestQueryPlanPostgreSQLDerivedAliasCannotBecomePhysicalAccess`

Correlated, deferred, analytics, group, and scoped shapes are truthful, closed,
and bounded:

5. `TestCorrelatedProviderFactAndDeferredNestedHydrationAreTruthful`,
   `TestDeferredBatchZeroParentAndUnboundedParentRefusalReturnNoPartialReport`,
   and `TestBuildDeferredBatchFactsBoundsAndNoProviderClaim`
6. `TestAnalyticsAndScopedTypedPlansOwnOperationRootAndPrimaryPurpose` and
   `TestBuildRequiresTheExactPrimaryPurposeForEachOperation`

The data query never runs and no report leaks provider input:

7. `TestQueryPlanSQLiteNeverExecutesDataQueryAndClosesRows` and
   `TestQueryPlanPostgreSQLLiveBoundPlanningWithoutExecution`
8. `TestTypedAssemblyBoundaryContainsNoSQLOrProviderVocabulary`,
   `TestProducerInputVocabularyContainsOnlyClosedSanitizedFacts`, and the
   bind/name/value canary corpus inside
   `TestExternalGeneratedApplicationQueryPlanIsCallerOnlyTypedAndRedacted`

Unknown and oversized provider input fails closed:

9. `TestQueryPlanSQLiteUnknownAliasAndOversizeFailClosed`,
   `TestQueryPlanSQLiteOversizeAndProviderFailureAreSanitized`,
   `TestQueryPlanPostgreSQLUnknownOversizeAndDepthFailClosed`, and
   `TestBuildEnforcesExactComplexityLimits`

Observation counts resources and isolates the observer:

10. `TestQueryPlanReleasesMaxOpenOneConnectionBeforeBlockingOrPanickingObserver`
    and `TestQueryPlanProviderFailureIsClosedAndReturnsNoPartialReport`

The external application and the public API stay closed:

11. `TestQueryPlanSQLiteAndPostgreSQLExternalGeneratedApplication`, which drives
    `TestExternalGeneratedApplicationQueryPlanIsCallerOnlyTypedAndRedacted`
12. `TestQueryPlanPublicCoreSurfaceMatchesAcceptedContract` and
    `TestQueryPlanPublicAPIContainsOnlyAcceptedTypesConstantsAndAccessors`

Live evidence runs SQLite, PostgreSQL C, and PostgreSQL linguistic profiles.
Fixtures must force primary-key lookup, ordinary index lookup, full scan,
temporary sort, join, correlated relation, deferred batch, aggregate, and
scoped plans. Live SQLite fixtures force full scan, primary-key lookup,
ordinary index lookup, and temporary sort; live PostgreSQL fixtures force
primary-key lookup; the external application forces join, aggregate, group, and
scoped plans on both providers. Correlated-relation and deferred-batch shapes
are proven only in typed assembly, and non-primary-key PostgreSQL access only
through captured provider JSON, so those live fixtures are still owed.
PostgreSQL tests must prove `ANALYZE` never ran; SQLite tests must prove the
data statement never ran. Mandatory mode permits zero skips.

Run the external generated-app gate under `-race` and prove all plan rows,
connections, hook contexts, and observer callbacks are released.

## 13. Mutation resistance

Tests must kill compiling mutants that:

- use PostgreSQL `ANALYZE TRUE` or SQLite bytecode explain;
- return raw SQL, bind values, raw plan JSON/detail, names, costs, or estimates;
- explain before authorization or after a denied hook;
- skip the atomic field-policy/filter validation used by execution;
- map an unknown provider name to a known model/index;
- claim an access path for a deferred relation batch;
- return a partial oversized report;
- expose Explain through GraphQL/System or a mutation client;
- omit the provider statement from observation counts; or
- invoke the observer while the connection/rows are still held.

Compile/baseline failures are invalid mutation evidence.

## 14. Completion definition

This feature is complete only when trusted Go application code can explicitly
ask how an authorized typed read would be planned, receive a bounded structural
SQLite/PostgreSQL report, and learn scan/index/join/sort/batch shape without
executing the data query or receiving SQL, values, names, estimates, rows, or
provider authority.
