# Public policy testing kit implementation contract

Status: **accepted implementation contract; not shipped**.

Audience: the engineer implementing the next Go roadmap slice and the reviewer
deciding whether that implementation is complete. This document is deliberately
self-contained. It does not authorize changes to authentication, sessions,
GraphQL, events, semantic indexes, NATS, migrations, or application business
logic.

Implementation handoff rule: treat every “must” in this file as acceptance
criteria. If the existing code makes one requirement impossible or internally
contradictory, stop and document that exact conflict before broadening the API
or adding a substitute feature.

## 1. Product decision

Golem will expose a small public Go package at:

```text
github.com/eleven-am/golem/go/golemtest
```

The package lets an application test the policy it already authored for one
actor without opening a database or starting a Golem application. It answers
two static questions using the **same policy kernel as production**:

1. What is the resolved row constraint for this action and actor?
2. How will requested result fields be classified as always readable,
   conditionally readable, or never readable for this statement reach?

This is a policy inspection and proof kit. It is not a second authorization
engine and it does not decide whether an arbitrary synthetic object is allowed.
Relation-aware execution remains an integration test through the generated
`Caller` surface.

## 2. Why this boundary is useful

Today generated applications already expose the safe raw ingredients:

- `GolemGeneratedApplicationBindings()` returns stamped
  `golem.ApplicationBindings[Actor]`;
- `GolemGeneratedApplicationDescriptors()` returns stamped
  `golem.ApplicationDescriptors`;
- `golem.BuildGeneratedPolicySet` builds a fresh actor-specific immutable
  policy set;
- `golem.FrozenPolicy.View()` exposes closed immutable rules and predicates;
  and
- the production policy resolver, implication checker, dependency collector,
  and field classifier exist under `internal/policy`.

Application tests can inspect frozen rules today, but they cannot safely ask the
production resolver for the effective constraint or classification. Copying
that logic into an application would create exactly the kind of authorization
fork Golem is meant to prevent. `golemtest` is the narrow public bridge.

## 3. Non-goals

Version 1 must not add any of the following:

- a policy language, policy editor, or string-based policy DSL;
- an authentication, login, token, session, role, or tenant framework;
- a mock database or in-memory relation database;
- a synthetic row evaluator that reimplements provider/runtime semantics;
- SQL rendering, SQL strings, query execution, or provider connections;
- application hooks, GraphQL, HTTP, events, observations, or background work;
- policy mutation, policy bypass, system authority, or a way to manufacture
  model/field/relation identities;
- human field/model names as authority-bearing input; or
- a snapshot format whose text becomes more authoritative than generated
  identities and the production policy kernel.

In particular, do not add `CanRead(actor, map[string]any)` or an equivalent
flat-row API. Such an API is unable to reproduce relation predicates,
quantifiers, null/missing-target semantics, provider collation behavior, or
field dependency hydration honestly.

## 4. Required public API

The implementation may improve comments and ordinary Go naming, but it must
preserve the following shape and semantics. Any material API change must update
this contract before code is merged.

```go
package golemtest

type Kit[A any] struct { /* opaque */ }

func New[A any](
    bindings golem.ApplicationBindings[A],
    descriptors golem.ApplicationDescriptors,
    bundle golem.SchemaBundle,
) (*Kit[A], error)

type PolicySet struct { /* opaque; contains no original actor instance */ }

func (kit *Kit[A]) ForActor(actor A) (PolicySet, error)

type ModelPolicy[M any] struct { /* opaque */ }

func Model[M any](
    policies PolicySet,
    descriptor golem.ModelDescriptor[M],
) (ModelPolicy[M], error)

type Constraint[M any] struct { /* opaque */ }

func (policy ModelPolicy[M]) RowConstraint(
    action golem.FrozenAction,
) (Constraint[M], error)

func (constraint Constraint[M]) Constant() (value bool, constant bool)
func (constraint Constraint[M]) View() golem.FrozenPredicateView
func (constraint Constraint[M]) CanonicalBytes() []byte

func Equivalent[M any](
    constraint Constraint[M],
    expected golem.Predicate[M],
) (bool, error)

func Implies[M any](
    constraint Constraint[M],
    expected golem.Predicate[M],
) (bool, error)
```

`Equivalent` means implication in both directions after the same normalization
used by production. It must not mean byte equality only. `Implies` means every
row admitted by `constraint` satisfies `expected`. Both functions must freeze
the expected predicate against the exact model descriptor retained by the
constraint and must use the production implication kernel and its existing
bounds/refusals.

### 4.1 Read-field classification

Field classification is deliberately named as a read/output operation. It must
not imply that a field is writable. The selecting action tells the classifier
which action row constraint defines statement reach; fields are still judged
through the read policy, exactly as the runtime does when returning rows from a
read or mutation.

```go
type UseKind uint8

const (
    UseProjection UseKind = iota + 1
    UseFilter
    UseSelector
    UseOrder
    UseCursor
    UseDistinct
    UseAggregateMeasure
    UseGroupDimension
    UseHaving
    UseAggregateOrder
    UseComputedDependency
)

type Access uint8

const (
    AccessAlways Access = iota + 1
    AccessConditional
    AccessNever
)

type ReadPlan[M any] struct { /* opaque */ }

func (policy ModelPolicy[M]) ClassifyReadFields(
    use UseKind,
    selectingAction golem.FrozenAction,
    fields ...golem.Field[M],
) (ReadPlan[M], error)

func (policy ModelPolicy[M]) ClassifyReadFieldsWithReach(
    use UseKind,
    selectingAction golem.FrozenAction,
    reach golem.Predicate[M],
    fields ...golem.Field[M],
) (ReadPlan[M], error)

func (plan ReadPlan[M]) ModelID() golem.ModelID
func (plan ReadPlan[M]) UseKind() UseKind
func (plan ReadPlan[M]) SelectingAction() golem.FrozenAction
func (plan ReadPlan[M]) Fields() []FieldClassification[M]
func (plan ReadPlan[M]) Field(field golem.Field[M]) (FieldClassification[M], bool)

type FieldClassification[M any] struct { /* opaque */ }

func (value FieldClassification[M]) FieldID() golem.FieldID
func (value FieldClassification[M]) Access() Access
func (value FieldClassification[M]) Condition() (Constraint[M], bool)
func (value FieldClassification[M]) RequiredFields() []golem.FieldID
func (value FieldClassification[M]) Dependencies() DependencyTree
func (value FieldClassification[M]) DischargedByConstraint() bool
```

The no-reach form uses the resolved row constraint for `selectingAction`. The
with-reach form combines/validates the supplied narrower statement predicate in
the same way production does. It must refuse a supplied predicate that widens
the actor's action constraint.

Classification preserves the caller's first-seen field order and removes
duplicates after the first occurrence. `DischargedByConstraint` is meaningful
only for `AccessConditional`: always-readable fields need no proof and
never-readable fields cannot be made readable by a caller filter.

### 4.2 Public dependency tree

The result must preserve relation dependencies rather than flattening them or
turning them into names.

```go
type DependencyKind uint8

const (
    DependencyScalar DependencyKind = iota + 1
    DependencyRelation
)

type DependencyTree struct { /* immutable */ }
type DependencyEntry struct { /* immutable */ }

func (tree DependencyTree) ModelID() golem.ModelID
func (tree DependencyTree) Entries() []DependencyEntry
func (entry DependencyEntry) FieldID() golem.FieldID
func (entry DependencyEntry) Kind() DependencyKind
func (entry DependencyEntry) TargetModelID() (golem.ModelID, bool)
func (entry DependencyEntry) Children() DependencyTree
```

Every slice/accessor returns a copy. Relation entries retain the target model
even when the child tree is empty because relation presence and empty
quantifiers still require the relation itself to be known.

### 4.3 One narrow `golem` bridge

`golem.Field[M]` is correctly sealed with unexported identity methods, so a
separate public package cannot currently extract its stable ID. Add exactly one
read-only helper in package `golem`:

```go
func FieldIdentity[M any](field Field[M]) (FieldID, bool)
```

It returns `false` for nil/zero/malformed handles. It must not expose a
constructor, reflection escape, name lookup, or any write/runtime capability.
The public inventory test must prove this is the only new authority-bearing
bridge needed by `golemtest`.

## 5. Required construction semantics

`New` must:

1. require non-zero, equal generation digests on bindings, descriptors, and the
   schema bundle;
2. construct the same validated schema registry used by production from the
   supplied schema bundle, through the same constructor production uses;
3. reject duplicate, missing, foreign-generation, zero-ID, or malformed model,
   field, and relation metadata before any policy factory runs;
4. retain immutable copies only; and
5. open no database, start no goroutine, invoke no hook, and emit no runtime
   observation.

`ForActor` must:

1. invoke every generated policy factory exactly once for that call;
2. use `golem.BuildGeneratedPolicySet` rather than independently replaying
   application declarations;
3. build a fresh set for every actor/call, with no global or cross-call cache;
4. discard the original actor object after construction; the frozen policy may
   necessarily retain actor-derived predicate operands, but it must not retain
   unrelated credentials/session material or the actor instance itself; and
5. recover a policy-factory panic at this public testing boundary, discard the
   panic payload, and return a closed error.

`Model` must require the typed descriptor's complete metadata/model identity to
match the descriptor registered in the same kit. It must reject a descriptor
from another generated app even if its Go model type happens to match.

## 6. Semantic source of truth

The package lives inside the Golem module so it may call the existing internal
policy implementation. It must be an adapter, not a fork.

The following production algorithms remain the sole source of truth:

- ordered last-authored policy resolution;
- row constraints for read/create/update/delete;
- field conditions;
- predicate normalization;
- logical implication/discharge;
- dependency collection; and
- field classification for every `UseKind`.

No copy of these algorithms may be placed in `golemtest`. Public result types
adapt immutable internal results into public IDs and closed enums. A source
inventory test must fail if the package grows its own rule-resolution or
predicate-evaluation implementation.

## 7. Error and privacy contract

Errors must be closed and classifiable at least as:

- invalid input;
- generation/descriptor mismatch;
- policy factory failure; and
- policy analysis/proof refusal.

Expose that classification without requiring callers to compare error text:

```go
type ErrorCode string

const (
    ErrorInvalidInput       ErrorCode = "INVALID_INPUT"
    ErrorGenerationMismatch ErrorCode = "GENERATION_MISMATCH"
    ErrorPolicyFactory       ErrorCode = "POLICY_FACTORY_FAILED"
    ErrorPolicyAnalysis      ErrorCode = "POLICY_ANALYSIS_FAILED"
)

func CodeOf(err error) (ErrorCode, bool)
```

`CodeOf` must preserve classification through ordinary `%w` wrapping. It must
not expose the wrapped application cause or panic value.

Exact wording is not an ABI. Errors and test diagnostics must never contain:

- the actor value or its formatted representation;
- token, session, email, tenant, database, or row values;
- panic payloads or raw application/provider causes;
- raw predicate operands; or
- internal package/type names.

Stable `ModelID` and `FieldID` values may be reported. Public exported
signatures must contain no type whose package path includes `/internal/`.

## 8. Example application test

The intended usage is small and typed:

```go
func TestAlicePostPolicy(t *testing.T) {
    bindings, err := social.GolemGeneratedApplicationBindings()
    if err != nil { t.Fatal(err) }
    descriptors, err := social.GolemGeneratedApplicationDescriptors()
    if err != nil { t.Fatal(err) }

    kit, err := golemtest.New(bindings, descriptors)
    if err != nil { t.Fatal(err) }
    policies, err := kit.ForActor(social.Actor{
        UserID: aliceID,
        Authenticated: true,
    })
    if err != nil { t.Fatal(err) }

    posts, err := golemtest.Model(policies, social.GolemGeneratedPostDescriptor)
    if err != nil { t.Fatal(err) }

    read, err := posts.RowConstraint(golem.FrozenActionRead)
    if err != nil { t.Fatal(err) }
    expected := social.Posts.Published.Eq(true).
        Or(social.Posts.AuthorID.Eq(aliceID))
    equivalent, err := golemtest.Equivalent(read, expected)
    if err != nil || !equivalent {
        t.Fatalf("read constraint equivalent=%v err=%v", equivalent, err)
    }

    plan, err := posts.ClassifyReadFields(
        golemtest.UseProjection,
        golem.FrozenActionRead,
        social.Posts.Title,
        social.Posts.Body,
    )
    if err != nil { t.Fatal(err) }
    body, ok := plan.Field(social.Posts.Body)
    if !ok || body.Access() != golemtest.AccessConditional {
        t.Fatalf("Body classification=%v present=%v", body.Access(), ok)
    }
}
```

The example intentionally asserts semantic equivalence and closed
classification, not canonical JSON text or private SQL.

## 9. Mandatory acceptance evidence

The implementation is incomplete until all of these exact gates exist and pass:

1. `TestPolicyTestKitBuildsFreshActorScopedPoliciesWithoutDatabase`
2. `TestPolicyTestKitRowConstraintMatchesProductionResolver`
3. `TestPolicyTestKitFieldClassificationMatchesRuntimeMasking`
4. `TestPolicyTestKitNarrowerReachDischargesButNeverWidensPolicy`
5. `TestPolicyTestKitRelationDependencyTreeMatchesRuntimeHydration`
6. `TestPolicyTestKitRejectsForeignGenerationModelAndFieldHandles`
7. `TestPolicyTestKitConcurrentActorsNeverSharePolicyState`
8. `TestPolicyTestKitFactoryPanicAndErrorsAreClosedAndRedacted`
9. `TestPolicyTestKitPublicAPIContainsNoInternalTypesOrAuthority`
10. `TestPolicyTestKitExternalGeneratedApplicationCompilesAndRuns`

The external generated-application gate must use a clean `example.com`
consumer, `GOWORK=off`, and only public packages. It must exercise the same
policy fixtures through:

- `golemtest` with no database;
- the generated `Caller` on SQLite;
- the generated `Caller` on PostgreSQL C collation; and
- the generated `Caller` on PostgreSQL linguistic collation.

It must include owner/private/public rows, an always-readable field, a
conditionally masked field, a never-readable field, a to-one dependency, a
to-many quantifier, and a missing/invisible relation target. Static
classification and actual Caller behavior must agree; pairwise provider
agreement alone is not sufficient evidence.

Run the public package and external gates under `-race`. Provider-required
evidence must fail rather than skip when mandatory PostgreSQL mode is enabled.

## 10. Mutation resistance

At minimum, the exact gates above must kill mutants that:

- drop a deny rule during resolution;
- change conditional access to always;
- omit a scalar or relation dependency;
- mark an unproved conditional field as discharged;
- accept a reach that widens the action row constraint;
- reuse one actor's built policy set for another actor;
- accept a descriptor/field from another generation; or
- expose a policy-factory panic payload.

A compile failure is invalid mutation evidence. Each mutant must compile, the
baseline must pass, and the semantic gate must fail.

## 11. Documentation and compatibility work

Implementation must add a short application-author section to `QUICKSTART.md`
and a complete policy-testing section to `PRODUCTION.md`. It must also update:

- the public Go API inventory and digest;
- the compatibility manifest/trust digest if that inventory is release-bound;
- package documentation for `golemtest` and the single `golem.FieldIdentity`
  bridge. Godoc on an exported symbol of a public package is documentation for
  an external consumer, not internal commentary: it is what `go doc` and
  pkg.go.dev render, and it is the only description an application author gets
  of an API they cannot read the source of. It is therefore required here, and
  the repository ban on explanatory comments does not reach it. That ban still
  applies in full to unexported symbols, function bodies, and test files, where
  a comment describes logic rather than a contract; and
- the roadmap status from unshipped to implemented only after the mandatory
  evidence passes.

Do not change the meaning of existing policy declarations or regenerate
application artifacts unless the public ABI inventory requires it.

## 12. Completion definition

This slice is complete only when an external application can build a fresh
actor policy, inspect exact row reach, classify typed fields and relation
dependencies, and prove constraint equivalence without a database—and the same
answers are independently shown to agree with real SQLite and PostgreSQL Caller
behavior. Anything less is a convenient rule viewer, not a trustworthy public
policy testing kit.

## 13. Recorded limitations

Found while implementing the construction spine. Each is a boundary of the
current design rather than a defect, recorded so a later reader can tell it was
decided rather than missed.

**Cross-generation rejection in `Model` is metadata-based, not digest-based.**
`golem.ModelDescriptor[M]` carries no generation stamp, so `Model` enforces kit
membership plus full metadata equality against the registered model. This is
sufficient while no two generations produce a byte-identical model, which the
fixtures confirm, but it is not a hard guarantee. Making it one requires a
generation stamp on `ModelDescriptor`, which changes a generated artifact and
the public ABI.

**"Retain immutable copies only" (§5.4) is met by construction, not by
copying.** `ApplicationDescriptors.Models()` clones. `ApplicationBindings`
exposes no accessor for its packages or factories, so it cannot be deep-copied
from outside `golem`; it is unreachable rather than duplicated. The property
holds; the mechanism differs from the wording.

**A relation is keyed by (RelationID, Role), never by RelationID alone.** A
self-referencing model shares one RelationID across both roles — in the social
example `Comment.ReplyTo` is the source side and `Comment.Replies` the inverse,
on the same model with the same identity. Keying by RelationID alone rejects
every self-relation.

**Gate 10 carries two mechanical caveats.** With `GOWORK=off` and no module
proxy, an in-tree consumer requires `replace` directives; a genuinely
replace-free consumer needs the `file://` proxy machinery in
`internal/release`. Separately, `./golemtest` is absent from the pattern list in
`internal/compatibility/corpus_test.go`, which is why the public Go API diff
gate passes without it. Adding it, as §11 requires, means regenerating
`public-go-api.json` and `PublicGoAPICorpusSHA256`.

**The delegation table in `source_inventory_test.go` is package-scoped, not
symbol-scoped.** A row requires that the package import the delegate, not that
the named symbol use it. So a row whose delegate is already imported for another
symbol is satisfied trivially — `View` and `CanonicalBytes` both name
`internal/policy/bind`, and removing `View`'s delegation alone does not fail the
guard. The rows still bite for a symbol whose delegate is otherwise unimported,
which is why the Phase 3 entries are declared before their symbols exist. What
actually pins a symbol's delegation is its own behavioural gate.
