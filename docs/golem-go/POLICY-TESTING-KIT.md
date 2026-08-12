# Public policy testing kit implementation contract

Status: **locally complete; integrated release evidence pending**. All ten
mandatory gates in §9 exist, and every §10 executable mutant has been observed
with a passing baseline and a failing semantic gate. The checked-social reviewed
physical-v2 publication is complete, and its external generated-application gate
passes under `-race` with SQLite and both mandatory PostgreSQL profiles. The
repository-wide release evidence remains serialized with the later ABI freeze.

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

### 4.3 Narrow generated-identity bridges

`golem.Field[M]` is correctly sealed with unexported identity methods, so a
separate public package needs read-only access to both its stable ID and the
generation that minted it:

```go
func FieldIdentity[M any](field Field[M]) (FieldID, bool)
func FieldGenerationDigest[M any](field Field[M]) (SchemaDigest, bool)
func (descriptor ModelDescriptor[M]) GenerationDigest() SchemaDigest
```

The field helpers return `false` for nil, zero, malformed, or unstamped handles.
They must not expose a constructor, reflection escape, name lookup, or any
write/runtime capability. Final generated model, scalar-field, and relation
handles carry the compiler's final generation digest. Provisional bootstrap
handles remain unstamped and therefore cannot cross the `golemtest` authority
boundary.

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

`Model` must require the typed descriptor's generation digest and complete
metadata/model identity to match the descriptor registered in the same kit.
Every field and relation handle accepted by classification must carry that same
digest. Byte-identical metadata from another generation is foreign and must be
rejected even when its Go model type and stable IDs happen to match.

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

    kit, err := golemtest.New(
        bindings,
        descriptors,
        social.GolemGeneratedSchemaBundle(),
    )
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

## 10. Regression coverage

The exact gates above must directly reject implementations that:

- drop a deny rule during resolution;
- change conditional access to always;
- omit a scalar or relation dependency;
- mark an unproved conditional field as discharged;
- accept a reach that widens the action row constraint;
- reuse one actor's built policy set for another actor;
- accept a descriptor/field from another generation; or
- expose a policy-factory panic payload.

These are ordinary, maintained regression tests. They run with the rest of the
product suite and exercise the public policy-testing API; there is no separate
mutation runner or evidence format to maintain.

## 11. Documentation and compatibility work

Implementation must add a short application-author section to `QUICKSTART.md`
and a complete policy-testing section to `PRODUCTION.md`. It must also update:

- the public Go API inventory and digest;
- the compatibility manifest/trust digest if that inventory is release-bound;
- package documentation for `golemtest` and the narrow generated-identity
  bridges in §4.3. Godoc on an exported symbol of a public package is documentation for
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

**Cross-generation rejection is generation-bound.** Final code generation
stamps each model descriptor, scalar field, and relation handle with the same
immutable schema digest used by application bindings, descriptors, and the
schema bundle. `Model` and field classification require exact digest equality
before comparing stable metadata. The older metadata-only path is covered by a
byte-identical foreign-generation regression and by separate descriptor and
field mutation records. Unstamped provisional or hand-built compatibility
handles are deliberately refused at this boundary.

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

**Gate 10 carried two mechanical caveats, both now settled.** With `GOWORK=off`
and no module proxy, an in-tree consumer requires a `replace` directive; a
genuinely replace-free consumer needs the `file://` proxy machinery in
`internal/release`. Separately, `./golemtest` was absent from the pattern list in
`internal/compatibility/corpus_test.go`, which is why the public Go API diff gate
passed without it; it has since been added and the corpus regenerated. Both
outcomes are recorded below.

**The delegation table in `source_inventory_test.go` is package-scoped, not
symbol-scoped.** A row requires that the package import the delegate, not that
the named symbol use it. So a row whose delegate is already imported for another
symbol is satisfied trivially — `View` and `CanonicalBytes` both name
`internal/policy/bind`, and removing `View`'s delegation alone does not fail the
guard. The rows still bite for a symbol whose delegate is otherwise unimported,
which is why the Phase 3 entries are declared before their symbols exist. What
actually pins a symbol's delegation is its own behavioural gate.

**A kit-versus-runtime comparison cannot, on its own, kill a mutation in the
shared kernel.** `golemtest` and the runtime read planner both call
`internal/policy/classify` and `internal/policy/dependency`. A mutant inside
either package moves both answers together, so a gate that only checks the two
against each other still passes. Gates 3 and 5 therefore assert two things: that
the kit's answer is the runtime's answer, and — absolutely — that the answer is
the one the fixture policy declares (a named field is conditional, its condition
is provably the authored predicate, a denied field is refused by the planner, a
relation dependency retains its hop and its target subtree). The relative half
is what makes the gate names true; the absolute half is what makes them
mutation-resistant.

**Classification uses the resolved row constraint; the runtime read planner uses
the relation-expanded one.** §4.1 fixes the no-reach form as "the resolved row
constraint for `selectingAction`", which is `resolve.RowConstraint`.
`internal/read/plan` additionally closes every relation hop in that constraint
over the target model's own read constraint before using it as statement reach.
`Access` and `Condition` are unaffected — the field condition itself is never
expanded — but `DischargedByConstraint` can differ for an actor whose read row
constraint contains a relation predicate, in either direction (an existential
hop narrows, a null hop widens). Gate 3 asserts canonical equality of the two
reaches as an explicit premise, so a fixture that ever violates it fails loudly
instead of comparing two different questions. Making the kit agree
unconditionally would mean either exporting the read planner's expander into the
policy kernel or restating §4.1 in terms of it; both are contract changes.

**The generated-identity bridges are additive but not free.** §4.1's
field-taking signatures are the only way to reach a `FieldID` and its generation
from outside package `golem`, so §4.3's bridges are required. Adding them
reclassifies the public Go API corpus as `additive` and requires regenerating
`internal/compatibility/testdata/public-go-api.json`,
`PublicGoAPICorpusSHA256`, `compatibility/manifest.json`, and
`TrustedManifestSHA256`. The generated-ABI and GraphQL corpora are unchanged.
`./golemtest` has since been added to the corpus pattern list as well, so the
kit's own public surface is now inside the frozen inventory.

**Gate 10 uses its own generated fixture application, not `examples/social`.**
The consumer module is generated from a schema authored for this contract:
`Article` carries an owner key, a public flag, an always-required title, an
owner-conditional field, a to-one relation-conditional field, a to-many
quantifier-conditional field, and a denied field, with `Author` and `Comment`
as relation targets. `examples/social` was not used because §9's fixture
requires a denied field, a missing relation target, and an invisible relation
target that the example's authored policy does not declare, and changing that
policy is outside this contract.

**Gate 10's oracle is the database evaluating a kit-certified predicate, never a
second provider.** For each answer the kit gives, the gate first proves through
`Equivalent` that the answer is a named typed predicate, then evaluates that
predicate through the generated `System` client — which is policy-free, so the
provider decides the row set — and then requires the authorized `Caller` to
return exactly those rows, and to expose a conditional field on exactly those
rows and mask it elsewhere. A never-readable field must be refused by the
`Caller` and accepted by `System`, so the refusal is provably the policy rather
than the schema. Every provider repeats the whole comparison against the one
static answer, so three providers agreeing with each other is never sufficient
for the gate to pass.

**An `AccessAlways` field and a non-constant read row constraint cannot coexist
on one model.** `resolve.chainForRow` includes field-scoped grants and
short-circuits on the first unconditional grant it reaches, so the unconditional
field grant that makes a field always readable also makes that model's row
constraint the `true` constant. A field with no field rule is therefore
`AccessConditional` with the model read condition, discharged by the statement
reach, rather than `AccessAlways`. Gate 10's fixture consequently places §9's
always-readable field on the model whose read grant is unconditional and its
row-filtered evidence on another model. This is a property of the resolver, not
of `golemtest`; changing it would change what a field grant means for row reach.

**A relation hop inside a field condition is decided only over target rows the
actor may read.** The kit reports the authored condition, which does not mention
the target model's own read policy, but the runtime refuses to decide the
condition from a target row the actor cannot read: gate 10's article whose
author is verified but unlisted has its conditional field masked. The gate
therefore composes its runtime expectation from two kit answers — the field
condition and the target model's read row constraint, each proved by
`Equivalent` — rather than from the field condition alone. Verified here for a
to-one hop; gate 10's to-many target is unconditionally readable, so the
composition rule for a quantifier over a row-filtered target is not evidenced.

**An explicit relation projection that omits a condition dependency is refused,
not guessed.** Selecting `Article.Author` with only the target's identity while
a field condition reads another target field makes the runtime return
`P3_RUNTIME_MASK … P2_EVAL_MISSING`. The kit's `Dependencies` tree names exactly
the fields that must remain reachable, so this is a statement-shape requirement
rather than a disagreement, but it means a caller cannot narrow a relation
projection below the hydration a conditional field needs.

**Gate 10's consumer is not replace-free.** It declares
`replace github.com/eleven-am/golem/go => <module root>` and asserts that this
is its only replacement. With `GOWORK=off` and no module proxy there is no other
way for an in-tree consumer to resolve an unpublished module; a genuinely
replace-free consumer needs the `file://` proxy machinery in `internal/release`.
The consumer is otherwise clean: it is a fresh `example.com` module, its schema
is generated by the public CLI, and every Go file in it — handwritten and
generated — is proved to import no `/internal/` package.

**`./golemtest` is now inside the frozen public Go API corpus.** Adding it, as
§11 requires, changed the pattern list in `internal/compatibility/corpus_test.go`
and `internal/compatibility/cmd/freeze/main.go` and required regenerating
`internal/compatibility/testdata/public-go-api.json`, `PublicGoAPICorpusSHA256`,
`compatibility/manifest.json`, and `TrustedManifestSHA256`. The change is purely
additive; the generated-ABI and GraphQL corpora are byte-identical.
