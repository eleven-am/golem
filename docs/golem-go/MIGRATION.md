# Migrating from go/v0.6.x to go/v0.7.0

go/v0.7.0 changes behaviour, error codes and a few signatures. This page lists
every change that can break an application written for v0.6.x, and what to do
about each. [RELEASE-NOTES.md](./RELEASE-NOTES.md) describes everything else
the release adds.

## The upgrade, in order

1. **Drain the event outbox** of the v0.6.x deployment. See
   [Events](#events): v0.7.0 cannot deliver events v0.6.x recorded.
2. **Update the module**: `go get github.com/eleven-am/golem/go@v0.7.0`.
3. **Regenerate**: `golem generate`, then `golem check`. Every generated
   application changes, because the generator and every event schema changed.
4. **Fix compile errors.** Only three kinds of code stop compiling; see
   [Signatures](#signatures).
5. **Review error handling** that tests codes or message text; see
   [Error codes and messages](#error-codes-and-messages).
6. **Review policies** for the purity contract and for write rules; see
   [Policies](#policies).
7. **Handle `GOLEM_SUBSCRIPTION_RESYNC`** in every event consumer, if you do
   not already.

## Error codes and messages

**Limit refusals are `LIMIT_EXCEEDED`.** Every refusal for a configured limit
now has the new code `LIMIT_EXCEEDED` (`golem.CodeLimitExceeded`), reason
`LIMIT_EXCEEDED`, and names the limit in `Error.Limit` with the maximum in
force in `Error.LimitMax`. Before, these were `BAD_USER_INPUT` for mutations,
`FORBIDDEN` with no detail for analytics, and the outbox and event-count
overflows escaped untyped, so GraphQL showed them as `INTERNAL_SERVER_ERROR`.
GraphQL errors carry `extensions.limit` and `extensions.max`. The limit names
are listed in GUIDE.md,
[Limits and error codes](./GUIDE.md#limits-and-error-codes).

*What to do:* replace checks for `BAD_USER_INPUT` that meant "too much" with
`errors.Is(err, golem.CodeLimitExceeded)`, and read `Limit` if you need to
know which.

**A missing grant is `FORBIDDEN`, not `BAD_USER_INPUT`.** An update, delete,
upsert, `UpdateMany` or `DeleteMany` by a caller whose policy has no grant for
that action on the model failed with `BAD_USER_INPUT`. It is now `FORBIDDEN`
with reason `NOT_GRANTED`. A write golem cannot prove reaches only readable
rows is `FORBIDDEN` with reason `AUTHORIZATION_UNPROVEN`. A policy cycle or
depth overflow found while expanding a nested write's filter is `FORBIDDEN`
with reason `POLICY_INVALID`.

*What to do:* treat these as authorization failures. On GraphQL, the reason is
in `extensions.reason`.

**Value refusals carry a reason and a field.** A value refused for its field
is `BAD_USER_INPUT` with reason `VALUE_TOO_LONG`, `VALUE_PRECISION`,
`VALUE_RANGE` or `INVALID_VALUE`, and `Error.Field` names the field. A decimal
or time value finer than its field's precision used to fail as an internal
error; it is now `BAD_USER_INPUT` with `VALUE_PRECISION`.

**`Field` is populated.** It was always empty. It now names the field a
refusal is about, but a read refusal names a field only when your request
named it. GUIDE.md, [`Field`](./GUIDE.md#field), has the rules.

**Public messages changed.** Refusals with a reason use a fixed message per
reason, such as `FORBIDDEN: operation is not granted` and `LIMIT_EXCEEDED:
request exceeds the touched_rows limit of 1000`. Messages never contain the
value you sent.

*What to do:* match on `Code`, `Reason` and `Limit`, never on message text.
`*golem.Error` now has an `Is` method, so `errors.Is(err, golem.CodeForbidden)`
and `errors.Is(err, golem.ReasonNotGranted)` both work.

**`events.Error` unwraps.** An event stream error now wraps its cause. `Recv`
returning because the context passed to that call ended now wraps the
context's error, and the stream stays open. `Error()` text gains the cause as
a suffix, such as `GOLEM_SUBSCRIPTION_CANCELLED: context deadline exceeded`.
`events.CodeOf` is unchanged.

*What to do:* if you compared `err.Error()` with a bare code, use
`events.CodeOf`. If a `Recv` deadline used to end your loop, check
`errors.Is(err, context.DeadlineExceeded)` and call `Recv` again.

## Datetimes

**Writes truncate instead of refusing.** A datetime or time value finer than
its field's precision, such as a `time.Now()` with nanoseconds on Linux, was
refused. It is now truncated toward the past to the field's precision and
stored. A filter or policy operand finer than a microsecond is rewritten to an
exact equivalent instead of failing with `FreezeInvalidValue`. Unique
selectors, cursors, list operators, scoped reads and analytics still refuse
such an operand, now with `BAD_USER_INPUT` and `VALUE_PRECISION`.

*What to do:* code that truncated values itself keeps working. Code that
compared a value it wrote with the stored value must compare with the value
golem returned. GUIDE.md, [Datetimes](./GUIDE.md#datetimes), gives the
rewrite table.

## Policies

**Reads that failed now succeed.** A filter or rule with a relation check
that did not repeat the target's read rule, such as
`Comments.Post.Is(Posts.Priority.Eq(0))`, failed every read with `FORBIDDEN`.
Relation checks now range over the rows the caller can read, so these reads
succeed. SQL field masks follow the same rule: a mask no longer tests a
related row the caller cannot read. For negative checks (`IsNot`, `IsNull`,
`None`, `Every`), the SQL mask now agrees with what reads already returned.

**Write rules narrow to readable rows.** An update or delete rule wider than
the read rule was refused when the statement was planned, with
`BAD_USER_INPUT`, unless it repeated the read rule's conditions. It is now
narrowed to the rows the caller can read, silently. A single update or delete
of a row outside that set fails with `NOT_FOUND`; `UpdateMany` and
`DeleteMany` skip such rows. Relation checks in write rules see only readable
targets.

*What to do:* if your write rules repeated read conditions only to satisfy
golem, you can remove them. If you relied on a write rule reaching rows the
caller cannot read, that write now does less. Grant read, or move the write
into an [operation](./GUIDE.md#operations-on-a-transaction).

**An upsert's create branch needs a create grant.** An upsert or
`ConnectOrCreate` whose target is missing or unreadable takes its create
branch. A caller without a create grant is now refused there with `FORBIDDEN`
and `NOT_GRANTED`, before any hook runs.

**`DefinePolicy` must be a pure function of the actor.** Policies are cached
per actor: `DefinePolicy` runs at most once per distinct actor and model, not
once per `ForPrincipal`. A policy that reads the clock, a global or the
database is frozen at its first result.

*What to do:* move anything a policy depends on into the actor through
`ResolvePrincipal`, which still runs on every `ForPrincipal`. If your actor
type is only valid through `SnapshotActor`, set `Config.PolicyCacheKey` to
enable the cache; without it, policies are built on every call as before. Use
`Config.PolicyCache.Disabled` to opt out, or `InvalidatePolicies` after a
change the actor does not carry. See GUIDE.md,
[Policies are cached per actor](./GUIDE.md#policies-are-cached-per-actor).

**Policy errors surface on first use.** Every model's policy was built in
`ForPrincipal`, so one broken policy made `ForPrincipal` fail. Each model's
policy is now built when a call first needs it. `ForPrincipal` succeeds, and
calls that need the broken model fail with `UNAUTHENTICATED: principal policy
could not be established`. A panic in `DefinePolicy` is raised in that first
call rather than in `ForPrincipal`.

*What to do:* a test that expected `ForPrincipal` to fail on a broken policy
must now exercise the model.

## Events

**The event schema changed for every model with subscriptions.** A deleted
row's snapshot is now a declared set of fields, by default its key, its
foreign keys and its bounded scalars, instead of the whole row. That changes
the event schema fingerprint of every subscribable model. Facts recorded by
v0.6.x and still undelivered when v0.7.0 starts do not match the new schema,
and the streams that receive them end with `GOLEM_SUBSCRIPTION_SOURCE_CLOSED`.

*What to do:* before deploying, stop the v0.6.x writers and let its event
publisher deliver every committed event. To keep fields in the snapshot that
the default drops, declare them with `golem.DeleteSnapshot`.

**Filtered subscriptions receive deletes.** A subscription with an
`EventWhere` never received deleted events. It now does, judged against the
delete snapshot. When the filter cannot be decided from the snapshot and the
caller could read the row, the delete is delivered; ignore deletes for IDs you
do not hold.

**Some streams end with `RESYNC` instead of missing deletes.** A delete whose
authorization could not be established was dropped silently, unless the
policy was local to the row or crossed exactly one relation. golem now
evaluates every read rule, and when the answer is unknown, for example because
the parent was updated in the same transaction or the rule tests a field
outside the snapshot, it ends that subscriber's stream with
`GOLEM_SUBSCRIPTION_RESYNC`.

*What to do:* on `RESYNC`, reload the state you derived from events and
subscribe again. Consumers already had to do this for transport loss. If one
model's streams resync often, see GUIDE.md,
[Who receives a delete](./GUIDE.md#who-receives-a-delete).

**Models hidden from GraphQL can have events.** `golem.Subscriptions` on a
model with `GraphQLHidden()` failed with `P7_SUBSCRIPTION_MODEL_HIDDEN`. It is
now allowed, and GraphQL still does not expose the model.

## WebSocket and SSE

**Going over the per-connection cap is an operation error.** A subscription
beyond `EventLimits.MaxSubscriptionsPerConnection` closed the whole WebSocket
connection with code 4400. It is now an error on that operation alone, with
`GOLEM_SUBSCRIPTION_LIMIT`, and the connection stays open.

**`KeepAlive` replaces `WebSocketKeepAlive`.** `events.Limits.KeepAlive` sets
the keep-alive interval for WebSocket and SSE. `WebSocketKeepAlive` remains a
deprecated alias, used when `KeepAlive` is zero. Setting both to different
values is refused with `GOLEM_EVENT_CONFIG`.

*What to do:* rename the field. Nothing breaks if you set only the old one.

## Signatures

**`Transaction` takes options.** `Transaction` on generated callers and system
clients, and `runtime.CallerTransaction` and `runtime.SystemTransaction`, take
variadic `golem.TransactionOption` values. Every call compiles unchanged.
Assigning the method or function to a variable of the old function type does
not; add `...golem.TransactionOption` to the type.

**`golem.Subscriptions` takes options.** It is now
`func[M any](...SubscriptionOption[M]) ModelOption[M]`. `golem.Subscriptions[M]()`
compiles unchanged; a function value of the old type does not.

**Struct literals without field names break.** Fields were added to:

| Type | New fields |
|---|---|
| `events.Limits` | `KeepAlive`, `RequireExclusiveProcessLocalPublisher`, `RevalidateEvery`, `MaxSubscriptionsPerPrincipal` |
| `runtime.Config` and the generated `Config` | `PolicyCache`, `PolicyCacheKey`, `SubscriptionPrincipalKey` |
| `graphql.Config` and the generated `GraphQLConfig` | `SubscriptionRecheck` |
| `runtime.MutationLimits` | `MaxDeleteSnapshotBytes` |

*What to do:* use field names in these literals. Keyed literals are
unaffected.

## Moving a kernel-mode application off `SystemEscape`

This section is for an application that uses golem only from Go and reached
for `SystemEscape` or an in-process mutex where policy could not express what
it needed. Each step is independent.

**1. Remove the GraphQL layer.** Add `golem.WithoutGraphQL(schema)` to
`DefineSchema` and run `golem generate`. It deletes
`zz_golem_graphql.schema.graphqls`, `zz_golem_graphql.gen.go` and the three
files in `golemgqlgen/`; remove the empty directory. Run `go mod tidy` to drop
gqlgen and gqlparser. No migration is needed: the model, contract and
migration fingerprints do not change. If the schema declares `golem.Query` or
`golem.ComputedField`, generation fails with `P5_GRAPHQL_DISABLED_EXTENSION`;
call those resolvers as plain functions instead. See GUIDE.md,
[kernel mode](./GUIDE.md#using-golem-from-go-without-graphql-kernel-mode).

**2. Replace `SystemEscape` writes with transaction operations.** Code that
holds a `CallerTx` and calls `SystemEscape(tx)` to make a write the caller may
not make should become a transaction operation that the policy grants exactly
that write:

```go
func DefineOperations(operations *golem.Operations) {
	golem.TxOperation(operations, "recordChange", RecordChange)
}

func (DocumentChange) DefinePolicy(rules *golem.Rules[DocumentChange], actor Actor) {
	rules.CanRead(DocumentChanges.ActorID.Eq(actor.ID))
	golem.Within(rules, RecordChange).CanCreate(DocumentChanges.ActorID.Eq(actor.ID))
}
```

Then call `schema.Run(ctx, tx, schema.RecordChange, args)` where the escape
was. The operation runs on the same transaction, keeps its hooks and
predicates, and its grant ends when it returns. See GUIDE.md,
[Operations on a transaction](./GUIDE.md#operations-on-a-transaction).

**3. Replace `SystemEscape` counts with `CanCount` and `CanAggregate`.** A
quota or budget check that counted rows the caller cannot read becomes an
operation with an aggregate grant:

```go
golem.Within(rules, CountPendingInvites).CanCount(
	Invites.OwnerID.Eq(actor.ID).And(Invites.Status.Eq("pending")))
golem.Within(rules, CommitAssetBudget).CanAggregate(
	Assets.Artifact.Is(Artifacts.OwnerID.Eq(actor.ID)), Assets.Size)
```

Inside the operation, `Count` and `Aggregate` see the granted rows; nothing
else does. Name in `CanAggregate` only the fields you measure: `MIN` and
`MAX` disclose their values. See GUIDE.md,
[Counts and aggregates an operation owns](./GUIDE.md#counts-and-aggregates-an-operation-owns).

**4. Replace in-process mutexes with lock keys.** A mutex around a
check-then-write, such as "sum the sizes, then insert if under budget", only
serializes one process. Take a lock key as the first statement of the
transaction instead:

```go
if err := tx.LockKeys(ctx, golem.MustLockKey("asset-budget", artifactID.String())); err != nil {
	return err
}
```

On PostgreSQL it serializes every replica. See GUIDE.md,
[Lock keys serialize check-then-write](./GUIDE.md#lock-keys-serialize-check-then-write);
`golem.Isolation(golem.Serializable)` with `golem.RetryConflicts` is the
alternative when no single key covers the invariant.

**5. Allow events on hidden models.** Models hidden from GraphQL can now
declare `golem.Subscriptions`; remove any workaround that mirrored them into a
visible model.

**6. Check the event topology.** If you run more than one process against one
PostgreSQL database with `events.NewMemoryTransport`, each event reaches only
the subscribers in the process whose publisher claimed it. v0.7.0 reports a
second publisher to the
observer; set `EventLimits.RequireExclusiveProcessLocalPublisher` to refuse
it, or move to `events/nats`. See GUIDE.md,
[Event transports](./GUIDE.md#event-transports).
