# P2 internal policy IR and freeze/bind contract

Status: **controlling design; closed IR, schema registry, binder, canonical
encoding, normalization, ordered resolution, dependency planning, evaluation,
implication, and classification are implemented; later P2 consumers remain
incomplete**

Authority: [`../BIBLE.md`](../BIBLE.md), especially sections 0, 2, 4,
7–8, and 20–21. [`P2-PLAN.md`](./P2-PLAN.md) owns delivery order and
[`OPERATOR-ABI.md`](./OPERATOR-ABI.md) owns public authoring spelling. The
detailed operator, policy-resolution, and classification chapters own their
expanded algorithms after the Bible's conflict resolutions.

This document freezes the representation boundary required before P2-C. SQL
renderers remain later work; the rule kernel, dependency collector,
provider-neutral evaluator, conservative implication prover, and typed
classifier now consume this boundary.

## 1. Scope and fixed decisions

P2 has two representations with one explicit boundary:

1. `go/golem` owns generic authoring builders and schema-agnostic, opaque,
   copy-isolated frozen public values.
2. `go/internal/policy/ir` owns the closed, non-generic, schema-validated
   production representation consumed by normalization, rule resolution,
   classification, evaluation, and SQL compilation.

`go/internal/policy/bind` is the only conversion path between them. It receives
a frozen public value, a validated runtime schema registry, and the schema's
declared provider set. It never reconstructs schema facts using reflection, Go
field names, caller strings, database introspection, or public handle method
sets.

The following decisions are fixed:

- A public predicate is schema-agnostic. It may be authored before an
  application runtime exists.
- A public policy contains the exact declaration order produced by one
  invocation of one model's `DefinePolicy` method.
- Public freeze performs representation, value, and copy-isolation checks.
  Internal bind performs schema, ownership, logical-type, relation, enum, and
  provider-capability checks.
- The internal tree is immutable after successful bind. Every collection is
  owned, ordered, and inaccessible for mutation through a getter.
- The internal tree contains fixed-width IDs and closed numeric enums. Logical
  or physical names never participate in authorization identity.
- SQL identifiers are absent from predicate and policy IR. They are resolved
  later from the validated runtime registry.
- Operator requirements are derived by the binder from the operator registry,
  field storage, mode, path, and declared providers. Requirements supplied by a
  public view are never trusted.
- Conditions may be canonicalized. Policy rule order may not be sorted,
  deduplicated, or otherwise canonicalized away.

## 2. Package dependency graph

The allowed production dependency graph is:

```text
go/golem
   ^
   | public frozen views and fixed-width ID types
   |
internal/policy/ir              compiler/ir + physical
   ^                                  ^
   |                                  |
internal/policy/schema ---------------+
   ^
   |
internal/policy/bind <--------- go/golem frozen views
   |
   +--> internal/policy/operator
   +--> internal/policy/ir

internal/policy/normalize ----> internal/policy/ir
internal/policy/resolve ------> internal/policy/ir + normalize
internal/policy/dependency ---> internal/policy/ir
internal/policy/evaluate -----> internal/policy/ir + operator + dependency
internal/policy/imply --------> internal/policy/ir + normalize
internal/policy/classify -----> internal/policy/ir + resolve + dependency + imply
internal/policy/sql ----------> internal/policy/ir + operator + schema
                                      ^
                                      | dialect implementation
internal/provider/sqlite ------------+
internal/provider/postgresql --------+
```

More precisely:

- `go/golem` MUST NOT import any `go/internal/policy/*` package.
- `internal/policy/ir` MAY import `go/golem` only to alias the canonical public
  fixed-width `ModelID`, `FieldID`, and `RelationID` types. It imports no
  compiler, physical-schema, provider, evaluator, or SQL package.
- `internal/policy/schema` is the one-time runtime decoder and index. It may
  import `go/golem`, `internal/compiler/ir`, and `internal/physical`.
- `internal/policy/bind` imports public frozen views plus `ir`, `schema`, and the
  read-only operator registry.
- `internal/policy/operator` declares semantics and renderer capabilities. It
  imports `ir`, but not concrete providers.
- `internal/policy/dependency` derives ordered typed hydration plans without a
  provider or record representation.
- `internal/policy/evaluate` owns immutable loaded/null/missing record states and
  the single exact in-memory evaluator. It imports `dependency`, `operator`, and
  `ir`, but no provider.
- `internal/policy/imply` first applies the canonical structural
  conjunction/disjunction rules, then may use an exact truth-table proof over at
  most twelve canonical opaque leaves. It never assigns meaning to a leaf;
  oversized proofs and vacuous proofs from an unsatisfiable selector fail
  closed. Failure to prove is not a claim of logical impossibility.
- `internal/policy/classify` validates typed model and field uses against the
  runtime schema registry, derives selecting/read constraints from one policy,
  and computes access, hydration, and semantic discharge without field names.
- `internal/policy/sql` defines a dialect interface and calls it. Concrete
  provider packages implement that interface; `policy/sql` never imports a
  provider package.
  Its shared walk owns deterministic aliases, bind order, logical composition,
  composite correlation, and relation quantifiers. Compilation requires the
  bound schema fingerprint plus a matching runtime capability proof and rejects
  stale field/relation shapes before a dialect is called.
- Provider packages MUST NOT own a second evaluator, rule resolver,
  classifier, or operator meaning table.
- Test-only `internal/policy/oracle` may import all three engines. No production
  package imports the oracle.

This direction prevents the fatal cycle `golem -> policy/ir -> golem` and the
semantic cycle `operator -> provider -> operator`.

## 3. Public opaque freeze/view seam

### 3.1 Ownership

`Predicate[M]` and `Rules[M]` own unexported mutable builder state only until
freeze. Field methods and combinators copy caller-owned inputs at the call
boundary. Freeze returns values with no method that can mutate them:

```go
type FrozenPredicate struct { /* unexported */ }
type FrozenPolicy struct { /* unexported */ }

func (Predicate[M]) Freeze(root ModelDescriptor[M]) (FrozenPredicate, error)
func (*Rules[M]) Freeze(model ModelID) (FrozenPolicy, error)
func (FrozenPredicate) View() FrozenPredicateView
func (FrozenPolicy) View() FrozenPolicyView
```

`Rules.Freeze` consumes a snapshot, not the builder. Later calls on the builder
cannot affect the returned policy. Calling freeze twice without modifying the
builder returns byte-identical views. A failed freeze returns no partially
usable value.

An authored `All[M]()` remains the true constant when used inside a larger
predicate. When it is the complete condition supplied to a rule method, policy
freeze records the rule as unconditional. Internal policy IR therefore never
confuses an unconditional rule with an empty or malformed condition. `None[M]()`
remains the false constant and is not converted to an absent condition.

### 3.2 Sealed read-only views

The internal binder must not depend on public struct layout. `go/golem` exposes
sealed read-only view interfaces or equivalently opaque concrete views with
unexported fields. The minimum information is:

```go
type FrozenPredicateView interface {
	sealedFrozenPredicateView()
	RootModelID() ModelID
	Root() FrozenConditionView
}

type FrozenConditionView interface {
	sealedFrozenConditionView()
	Kind() FrozenConditionKind
	Operator() FrozenOperator
	FieldID() (FieldID, bool)
	Relation() (FrozenRelationRef, bool)
	Mode() FrozenComparisonMode
	Path() FrozenJSONPathView
	Operand() FrozenOperandView
	Children() []FrozenConditionView
}

type FrozenRuleView interface {
	sealedFrozenRuleView()
	Action() FrozenAction
	Effect() FrozenEffect
	ModelID() ModelID
	IsUnconditional() bool
	Condition() (FrozenPredicateView, bool)
	Fields() (fields []FieldID, modelWide bool)
	Position() uint32
}

type FrozenPolicyView interface {
	sealedFrozenPolicyView()
	ModelID() ModelID
	Rules() []FrozenRuleView
}
```

The exact public names may follow the final `OPERATOR-ABI.md`; the information,
sealing, and ownership rules above may not change. A view getter returning bytes,
JSON, a path, a list, fields, children, or rules returns a fresh deep copy. A
getter returning another view returns another immutable view. No getter returns a
pointer into builder or frozen storage.

The view is evidence, not authority. The binder revalidates every tag, arity,
ID, value, transition, and requirement.

### 3.3 No author-facing raw construction

There is no public `NewPredicate`, `NewCondition`, `NewRule`, or `NewPolicy` that
accepts IDs, operator numbers/strings, raw nodes, raw SQL, maps, or `any`.
Application policy code reaches IDs only through generated typed handles.

Go has no friend-package mechanism: generated code in an application model
package cannot initialize unexported fields in `go/golem`. Consequently, any
exported `Generated*` bootstrap constructor mechanically callable by generated
code is also callable by other Go code in that module. It MUST be documented as
generator ABI, not authoring API, and MUST NOT be treated as an authorization
boundary. Its output is untrusted until bind validates it against the embedded
schema registry. `unsafe`, `go:linkname`, hidden global registration, and stack
inspection are forbidden attempts to simulate friend access.

The existing raw-ID `Generated*Field`/`GeneratedTo*` functions are therefore a
bootstrap ABI debt, not proof that IDs are unforgeable. P2-B SHOULD replace them
with the narrowest practical generated metadata construction seam, but security
still rests on bind-time registry validation.

### 3.4 Minimum relation-handle correction

A relation handle participates in both predicate traversal and field-scoped
rules. It therefore MUST carry one immutable generated endpoint reference:

```go
type GeneratedRelationRef struct {
	// representation private to go/golem
}

// Semantically present in the reference:
//   FieldID      FieldID      // the relation field on this endpoint
//   RelationID   RelationID   // the normalized logical edge
```

Equivalently, `ToOne` and `ToMany` may store both values directly. The
owner model is supplied by the current bind context and the target model is
resolved from the validated relation endpoint. The generic `M` and `R` remain
compile-time witnesses only. Carrying only `RelationID`, as the P2-A handles
currently do, is insufficient: the field
lens and dependency collector need the endpoint's relation `FieldID`, and a
self-relation or inverse endpoint cannot infer it from model type parameters at
runtime.

The generated constructor and code generator must be changed atomically. Bind
requires:

- `FieldID` exists, belongs to that model, and is a relation field;
- the field's `RelationID` equals the supplied edge ID;
- endpoint cardinality agrees with `ToOne` or `ToMany`; and
- the nested child is bound recursively using the registry endpoint's target
  model.

## 4. Closed internal types

The following is the semantic shape of `internal/policy/ir`. It is non-generic.
Concrete fields remain unexported and are exposed through copy-returning accessors.
Explicit numeric assignments are persisted ABI and MUST NOT use an unpinned
`iota` sequence.

### 4.1 Identities and closed enums

```go
type ModelID = golem.ModelID
type FieldID = golem.FieldID
type RelationID = golem.RelationID
type EnumID [16]byte
type EnumValueID [16]byte

type Action uint8
const (
	ActionRead   Action = 1
	ActionCreate Action = 2
	ActionUpdate Action = 3
	ActionDelete Action = 4
)

type Effect uint8
const (
	EffectGrant Effect = 1
	EffectDeny  Effect = 2
)

type ConditionKind uint8
const (
	ConditionConstant ConditionKind = 1
	ConditionLogical  ConditionKind = 2
	ConditionScalar   ConditionKind = 3
	ConditionList     ConditionKind = 4
	ConditionJSON     ConditionKind = 5
	ConditionRelation ConditionKind = 6
)

type LogicalOperator uint8
const (
	LogicalAnd LogicalOperator = 1
	LogicalOr  LogicalOperator = 2
	LogicalNot LogicalOperator = 3
)

type ComparisonMode uint8
const (
	ComparisonSensitive        ComparisonMode = 1
	ComparisonASCIIInsensitive ComparisonMode = 2
)

type Provider uint8
const (
	ProviderSQLite     Provider = 1
	ProviderPostgreSQL Provider = 2
)

type ProviderSet uint8
```

`ProviderSet` is a validated bit set containing only the two provider bits. An
empty declared set is invalid. Public/compiler string spellings are decoded once
at runtime bootstrap and do not survive in policy IR.

`OperatorID` is a closed `uint16` registry key with explicit stable numbers. Its
inventory includes scalar equality/membership/order/text/presence, list
equality/membership/emptiness, JSON equality/order/string/array operations, and
relation existence/quantifiers from the accepted operator ABI. Human-readable
operator names exist only in diagnostics. They are never parsed to decide
semantics.

### 4.2 Logical type reference

The binder attaches the complete logical type needed by later stages:

```go
type ValueKind uint8 // Bool, Int16, Int32, Int64, Float32, Float64,
                     // Decimal, String, Bytes, UUID, Date, Time, DateTime,
                     // Enum, JSON, ScalarList

type TypeRef struct {
	kind       ValueKind
	nullable   bool
	precision  uint16       // Decimal/Time/DateTime only
	scale      uint16       // Decimal only
	enum       EnumID       // Enum only
	element    *TypeRef     // ScalarList only; owned immutable copy
	capability Capability   // zero or a decoded, registered storage capability
}
```

Unused fields are zero and rejected if populated. A `ScalarList` element is
non-null and cannot itself be Bytes, JSON, relation, or list. `TypeRef` is
validated from ModelIR; it is not copied from the public handle's generic type.

### 4.3 Canonical value union

No predicate operand is `any` or `interface{}`. The owned union is:

```go
type Value struct {
	kind     ValueKind
	boolean  bool
	signed   int64
	float32  uint32       // normalized IEEE-754 bits
	float64  uint64       // normalized IEEE-754 bits
	decimal  DecimalValue
	text     string
	bytes    []byte
	uuid     [16]byte
	date     DateValue
	time     TimeValue
	instant  DateTimeValue
	enum     EnumValue
	json     JSONValue
	list     []Value
}

type DecimalValue struct {
	coefficient int64
	scale       uint8
}

type DateValue struct {
	year  int16
	month uint8
	day   uint8
}

type TimeValue struct {
	microseconds int64 // since 00:00:00, already quantized to field precision
}

type DateTimeValue struct {
	unixSeconds int64
	nanosecond  uint32 // UTC instant, quantized to <= microseconds
}

type EnumValue struct {
	enum  EnumID
	value EnumValueID
}
```

Only the member selected by `kind` may be populated. Bool false, integer zero,
empty string, empty bytes, and empty list remain distinguishable by the kind and
operand shape. SQL NULL is not a `Value`; presence operators have no operand.
JSON null is represented inside `JSONValue` and is distinct from SQL NULL or an
absent JSON path.

Finite float bits are retained so float32 cannot accidentally become float64.
Both signed zero encodings normalize to positive zero because SQLite,
PostgreSQL, and Go numeric equality treat them as equal. NaN and infinities are
rejected before public freeze. Decimal is exact scaled integer because P1's
portable contract is precision <= 18; a future wider PostgreSQL extension needs
a new value representation/version rather than overloading this one.

`DateTimeValue` never stores a Go monotonic clock reading or location. Public
freeze records the UTC instant; bind quantizes it to the field's declared
precision. Values not exactly representable by the declared field contract are
rejected unless the public operator ABI explicitly defines truncation. Time and
DateTime precision is at most microseconds under P1.

### 4.4 Exact canonical JSON

JSON is a second closed union, not `map[string]any`:

```go
type JSONKind uint8 // Null, Bool, Number, String, Array, Object

type JSONNumber struct {
	negative    bool
	coefficient []byte // canonical ASCII digits, no leading/trailing zero noise
	exponent    int32  // base-10 exponent
}

type JSONMember struct {
	key   string
	value JSONValue
}

type JSONValue struct {
	kind    JSONKind
	boolean bool
	number  JSONNumber
	text    string
	array   []JSONValue
	object  []JSONMember
}

type JSONNullKind uint8
const (
	JSONDbNull   JSONNullKind = 1
	JSONDocumentNull JSONNullKind = 2
	JSONAnyNull  JSONNullKind = 3
)
```

The parser rejects invalid UTF-8, duplicate object keys, non-JSON numbers, and
trailing input. It uses exact number tokens and never decodes through `float64`.
Coefficient/exponent normalization maps all equal decimal spellings to one
representation, including zero. Object members are sorted by Unicode code-point
order after duplicate detection. Arrays preserve order. Every nested byte slice
and value is owned.

The three null sentinels are a closed enum used only by JSON equality/inequality
operands. They are not strings and cannot collide with document data. Runtime
JSON navigation additionally has an internal `SlotAbsent` state; it is an
evaluator result, never a storable operand value.

JSON path IR is likewise typed:

```go
type JSONPath struct {
	segments []JSONPathSegment
}

type JSONPathSegment struct {
	key        string
	arrayIndex uint64
	isIndex    bool
}
```

The public typed provider-neutral path is copied during freeze into this segment
sequence. Provider renderers receive segments, never a raw PostgreSQL text-array
expression or SQLite JSONPath string. An absent path
and an explicitly empty path normalize to one whole-document form.

### 4.5 Operand union

Operator arity is represented without `any`:

```go
type OperandKind uint8
const (
	OperandNone     OperandKind = 1
	OperandOne      OperandKind = 2
	OperandMany     OperandKind = 3
	OperandFlag     OperandKind = 4
	OperandJSONNull OperandKind = 5
)

type Operand struct {
	kind     OperandKind
	one      Value
	many     []Value
	flag     bool
	jsonNull JSONNullKind
}
```

Empty `In`/`NotIn`/`HasEvery`/`HasSome` is represented by `OperandMany` with an
owned non-nil zero-length slice. It is not confused with `OperandNone`.
`IsEmpty(false)` remains distinguishable from no operand. The operator registry,
not a switch distributed across consumers, validates the allowed operand kind,
value kind, arity, emptiness, and comparison mode.

### 4.6 Condition variants

`Condition` is a value wrapper around a package-closed node interface. Only the
following variants implement it:

```go
type Condition struct { node conditionNode }

type constantNode struct {
	model ModelID
	truth bool
}

type logicalNode struct {
	model        ModelID
	operator     LogicalOperator
	children     []Condition
	requirements []Requirement
}

type scalarNode struct {
	model        ModelID
	field        FieldID
	fieldType    TypeRef
	operator     OperatorID
	mode         ComparisonMode
	operand      Operand
	requirements []Requirement
}

type listNode struct {
	model        ModelID
	field        FieldID
	fieldType    TypeRef
	operator     OperatorID
	operand      Operand
	requirements []Requirement
}

type jsonNode struct {
	model        ModelID
	field        FieldID
	fieldType    TypeRef
	operator     OperatorID
	mode         ComparisonMode
	path         JSONPath
	operand      Operand
	requirements []Requirement
}

type relationNode struct {
	model        ModelID
	field        FieldID
	relation     RelationID
	target       ModelID
	operator     OperatorID
	child        *Condition
	requirements []Requirement
}
```

Every node carries its own model. Logical children have the same model. Scalar,
list, and JSON fields belong to the node model and match the variant. A relation
node's field belongs to the parent model; its relation endpoint resolves to the
target model; a present child is rooted at that target.

`logicalNode` invariants are:

- `Not` has exactly one child;
- normalized `And` and `Or` never have zero or one child;
- all children have the same root model;
- constants are represented only by `constantNode`, never empty children.

`relationNode.child` is non-nil for `Is`, `IsNot`, `Some`, `Every`, and `None`.
It is nil only for to-one presence/absence operators. Cardinality and operator
must agree. Relation-local and remote correlation fields do not live in the
condition; they are immutable registry facts selected by the endpoint.

### 4.7 Requirements

```go
type Capability uint16

type Requirement struct {
	providers  ProviderSet
	capability Capability
}
```

Capability numbers are assigned by one closed runtime registry. Bootstrap maps
known P1 capability identities to them and refuses an unknown capability needed
by a P2 operation. Requirements are sorted by `(providers, capability)` and
deduplicated. A logical node owns the union of its children; a relation node owns
the child union plus relation/correlation requirements.

Requirements are positive proof obligations, not optimistic flags. Bind fails
if the declared provider set is not a subset of the operator's agreement-proved
provider set, or if the selected physical field/storage lacks a required
capability. SQL planning repeats the check against the active, runtime-probed
provider manifest before returning a fragment.

### 4.8 Ordered rules and policy

```go
type Rule struct {
	action    Action
	effect    Effect
	model     ModelID
	condition *Condition // nil means unconditional; non-nil is a valid node
	fields    []FieldID  // nil means model-wide; non-nil is non-empty
	position  uint32     // zero-based declaration position, oldest first
}

type Policy struct {
	model ModelID
	rules []Rule // declaration order, oldest to newest
}
```

`Rule.fields` preserves first declaration order. Repeated field handles in one
public call normalize to the first occurrence; they are not sorted. A non-nil
empty list is invalid. Delete field rules are invalid. Every rule model equals
the policy model, every field belongs to that model, and a non-nil condition is
rooted at that model. Positions are contiguous and exactly equal to slice index;
they are evidence for stable traces, not a separately sortable priority.

Resolution iterates `rules` from the end. Normalization may replace a rule's
condition with an equivalent canonical condition, but MUST NOT reorder, merge,
deduplicate, or eliminate rules. Even an apparently redundant rule can be
observable in a policy trace and rule order is semantic.

The application-level execution policy set introduced in P2-I is an immutable
ID-indexed collection of one `Policy` per generated model binding. It is built
fresh for one actor/execution. No actor-specific `Policy`, rule constraint, or
classification result is stored globally.

## 5. Value codecs and canonical encoding

### 5.1 Public value ingestion

Public constructors/parsers own syntax. Bind owns schema compatibility:

- Bool and signed integers retain their declared width.
- Floats reject NaN and infinities and normalize negative zero.
- Decimal rejects precision/scale overflow and stores a signed scaled integer.
- String validates UTF-8 where required by the transport/storage contract.
- Bytes are copied byte-for-byte.
- UUID stores exactly 16 bytes; text parsing accepts only the documented
  canonical forms and emits lowercase canonical text where a provider needs it.
- Date validates a real Gregorian calendar date.
- Time validates the range `[00:00:00, 24:00:00)` before precision quantization.
- DateTime strips monotonic data, preserves the instant, normalizes UTC, and is
  checked against both declared providers' supported range.
- Enum bind resolves the authored label against the field's `EnumID` and stores
  `EnumValueID`; no later stage compares an unvalidated label string.
- JSON follows section 4.4.
- A scalar list is homogeneous, contains no null elements, preserves order, and
  deep-copies every element.

Cross-kind coercion is forbidden. An `Int32` operand does not become `Int64`; an
enum is not a string; a UUID is not bytes; and a DateTime string is not parsed by
the evaluator or database. Conversion, if offered publicly, occurs in an
explicit typed constructor before freeze.

### 5.2 Canonical bytes

P2 canonical encoding is a versioned, domain-separated, length-delimited binary
format written explicitly without `encoding/json`, reflection, maps, gob, or Go
type names.

```text
golem:policy-condition:v1 NUL <root-model-id> <node>
golem:policy:v1           NUL <model-id> <ordered-rules>
```

Each enum uses its fixed unsigned number. IDs are exactly 16 bytes. Signed
integers use one specified big-endian/two's-complement or zig-zag encoding;
floats use normalized IEEE bits; strings and bytes use unsigned length followed
by bytes; slices use count followed by ordered elements. Optional values use one
presence byte. JSON objects are already key-sorted. The encoder rejects an
invalid internal value rather than emitting bytes for it.

Condition fingerprints hash normalized condition bytes with a separate domain.
Policy fingerprints hash ordered rule bytes. The policy encoder does not sort
rules or field lists. Canonical equality compares kind and canonical members
directly or compares canonical bytes; it never uses `reflect.DeepEqual`.

## 6. Freeze, bootstrap, and bind lifecycle

### 6.1 Public freeze

Public freeze performs only checks that do not need schema metadata:

1. reject a zero/uninitialized handle or malformed public node;
2. validate closed public operator, mode, and operand shapes;
3. validate and canonicalize exact public values;
4. copy every mutable input recursively;
5. normalize explicit logical constants and combinator arity;
6. preserve policy declaration order and field first-seen order; and
7. return a sealed immutable view.

Public views carry only the predicate root model. Nested-node model ownership is
not inferred or stamped from erased Go type parameters: bind walks with an
explicit current-model context and changes it only through a validated relation
endpoint. Public freeze does not claim that a field exists, belongs to the supplied model,
has the implied Go logical type, accepts the enum label, supports the operator,
or can lower on a declared provider. Those are binder questions.

### 6.2 One-time runtime schema bootstrap

P2 runtime startup decodes one `golem.SchemaBundle` into one immutable validated
registry keyed only by fixed-width IDs. Bootstrap:

1. validates bundle format, canonical versions, generation digest, and all
   document fingerprints;
2. decodes and validates canonical ModelIR and ContractIR;
3. decodes and validates each declared provider PhysicalSchema;
4. proves the provider document inventory exactly matches ModelIR providers;
5. indexes models, fields, enum values, relation endpoints, physical tables,
   physical columns, storage kinds, and capabilities by IDs;
6. constructs both source and inverse relation endpoints with ordered
   correlation pairs; and
7. publishes the registry only after all checks succeed.

The registry has typed lookup methods, not exported maps:

```go
Model(ModelID) (Model, bool)
Field(ModelID, FieldID) (Field, bool)
RelationEndpoint(ModelID, FieldID, RelationID) (RelationEndpoint, bool)
EnumValue(EnumID, authoredLabel) (EnumValueID, bool)
PhysicalField(Provider, ModelID, FieldID) (PhysicalField, bool)
```

The label parameter is value data at this validation boundary; model/field
identity remains IDs. Provider SQL obtains physical identifiers only from
`PhysicalField`/model registry results.

For a source relation endpoint, ordered correlation is parent `LocalFields` to
child `RemoteFields`. For its inverse endpoint, it is parent `RemoteFields` to
child `LocalFields`. Every pair must resolve to persisted scalar fields of
compatible logical types and equal non-zero arity.

### 6.3 Internal bind

The production API is conceptually:

```go
func Predicate(
	frozen golem.FrozenPredicate,
	registry *schema.Registry,
	providers ir.ProviderSet,
) (ir.Condition, error)

func Policy(
	frozen golem.FrozenPolicy,
	registry *schema.Registry,
	providers ir.ProviderSet,
) (ir.Policy, error)
```

`FrozenPolicy` remains schema-agnostic. The binder, not `Rules.Freeze`, receives
the runtime registry and declared provider set. This permits generated policy
factories to stay actor-only while P2-I binds their result during execution or
validated startup composition.

Bind recursively checks:

- the root model exists and equals the rule/binding model;
- every node model exists;
- every field exists, belongs to the node model, and has the expected scalar,
  list, JSON, or relation kind;
- field logical type and nullability admit the operator;
- operand kind and exact value kind match the field/operator registry cell;
- enum identity and value are valid for that exact field enum;
- null-presence operations target nullable scalar/list/JSON fields, or relation
  existence as defined by the relation operator contract;
- relation endpoint identity, field, role, cardinality, target, ordered
  correlation mapping, and child root all agree;
- logical children have the same model and required arity;
- rule action/effect is closed, field rules use an allowed action, field lists
  are non-empty, and positions are contiguous;
- every declared provider supports the requested operator/mode/path/storage
  combination; and
- all derived requirements are present in the logical and physical registry.

Any failure returns one stable P2 diagnostic including rule position and typed
IDs where applicable. No malformed or partially bound tree is returned. Unknown
IDs fail closed even if they came from a `Generated*` public constructor.

## 7. Normalization boundaries

Normalization begins only after successful bind, except for public constant and
arity cleanup needed to freeze safely. It is pure: input trees are never mutated.

Allowed condition rewrites are:

- recursively normalize children;
- `And()` to true and `Or()` to false;
- flatten nested `And` within `And` and nested `Or` within `Or`;
- remove true identities from `And` and false identities from `Or`;
- collapse an `And` containing false to false and an `Or` containing true to
  true;
- remove duplicate children by canonical typed equality;
- sort commutative `And`/`Or` children by canonical bytes;
- collapse a one-child `And`/`Or` to its child;
- `Not(true)` to false, `Not(false)` to true, and `Not(Not(x))` to `x`; and
- normalize absent versus empty JSON path to whole-document path.

Forbidden without a new equivalence proof and agreement corpus are:

- De Morgan rewrites;
- distributing `And` over `Or` or the reverse;
- rewriting nullable comparison or membership through `Not`;
- replacing `Every` with a naive negation;
- rewriting to-one absence from FK nullability;
- folding relation predicates using assumed referential integrity;
- cross-kind numeric/string/time coercion;
- provider-specific simplification in the provider-neutral tree; and
- any rule-chain reorder, merge, deduplication, or dead-rule deletion.

Derived requirements are recomputed after normalization. A removed branch cannot
leave a stale capability requirement; a retained branch cannot lose one.

## 8. Copy isolation and concurrency

The following tests are mandatory at the P2-B/P2-C boundary:

- mutate a byte operand after `Eq`; frozen and bound bytes do not change;
- mutate every input slice after `In`, `HasEvery`, `HasSome`, list equality,
  `And`, `Or`, JSON path, field rule, and policy freeze; output does not change;
- mutate nested JSON arrays/objects and list elements through every public
  getter; the frozen and bound values do not change;
- mutate slices returned by every view/IR getter; a second read is unchanged;
- modify the `Rules` builder after freeze; the prior `FrozenPolicy` is unchanged;
- freeze and bind concurrently from one already-frozen value under `-race`; all
  canonical bytes are equal and no race is reported; and
- build policies for distinct actors concurrently; no node, value, rule slice,
  or requirement slice is shared mutably across policies.

Immutable values may share storage only when that storage is unreachable for
mutation through public or internal APIs. Returning an owned copy is preferred
over documenting aliasing.

## 9. Provider and SQL boundary

Internal policy IR is provider-neutral but provider-aware through derived
requirements. It does not contain SQL syntax, placeholders, aliases, quoted
identifiers, JSON extraction text, collation names, or driver values.

The SQL compiler receives a validated condition, validated runtime registry,
one active provider, root model, and a deterministic alias allocator. It resolves
every table, column, storage codec, and relation correlation pair from the
registry. Values become ordered parameters. The dialect implementation owns
placeholder and expression syntax but cannot reinterpret an `OperatorID`.

An operator registry cell is publishable only when it declares and tests:

- accepted field and operand kinds;
- mode/path validity;
- null truth table and two-valuedness;
- canonical value validation;
- Go evaluation;
- SQLite capability/render support;
- PostgreSQL capability/render support;
- parameter encoding and result decoding; and
- required runtime-probed capabilities.

A dual-provider schema binds only the agreement-proved intersection. A
provider-specific predicate binds only when the schema itself declares that
provider restriction. Degenerate operands such as empty `In` do not bypass a
provider capability check when their public meaning belongs to an unsupported
operator family; capability is checked before constant folding.

## 10. Actionable P1 metadata and ABI gaps

P2 must close these concrete gaps; none is permission to infer data at runtime.

### 10.1 Public descriptor metadata is insufficient for bind

Current `ApplicationDescriptors`/`ModelMetadata` provide model ID, scan/write
field order, identities, and shallow relation metadata. They do not provide:

- a field inventory with owner model, kind, logical type, nullability, enum ID,
  or storage capability;
- enum value identities and labels;
- relation local/remote correlation field pairs; or
- provider physical table, column, storage, collation, and capability facts.

P2 MUST NOT infer these facts from Go reflection or extend each policy node with
duplicated author-controlled metadata. The complete facts already exist in the
embedded canonical ModelIR and provider PhysicalSchema documents. The required
fix is the one-time validated runtime schema decoder/index in section 6.2.

### 10.2 PhysicalSchema has no runtime decoder

ModelIR and ContractIR canonical payloads are JSON and can be decoded then
revalidated/fingerprinted. `physical.CanonicalEncode` currently emits a custom
reflection-driven binary form, but the repository has no matching decoder. Thus
the generated `SchemaBundle` is not yet consumable as the P2 physical registry.

Before provider SQL work, P1/P2 integration must either:

1. implement a versioned, bounds-checked `physical.CanonicalDecode` that rejects
   unknown types/fields, trailing bytes, invalid schema, and fingerprint
   mismatch; or
2. generate a separate immutable typed runtime physical registry whose canonical
   bytes and fingerprint are proven equal to the PhysicalSchema document.

Option 1 is the minimal coherent fix. Re-encoding after decode must reproduce
the embedded bytes exactly. Using reflection for the new policy canonical
encoder is still forbidden; this exception only describes compatibility with
the already-frozen P1 physical document format.

### 10.3 Relation handles lack endpoint FieldID

Current P2-A `ToOne`/`ToMany` values store only `RelationID`. This cannot support
typed field rules or unambiguous dependency identity. Apply the minimum endpoint
reference change in section 3.4 and update generated code/template ABI.

### 10.4 Freeze has no schema input

Current `Rules.Freeze(ModelID)` and actor-only `PolicyFactory` have no schema
registry. This is compatible only if freeze remains schema-agnostic. Full
validation therefore occurs when the generated binding's frozen result is bound
against the application's validated registry and provider set. Do not add a
process-global registry lookup inside `Freeze`; that would break application
isolation, tests, and multi-schema processes.

### 10.5 Generated IDs are not runtime-unforgeable

Public IDs are fixed-width arrays and current generated handle constructors
accept them directly. Go cannot distinguish generated calls from handwritten
calls in an application package. The binder must treat all public IDs as
untrusted and prove registry membership/ownership. Compile-time typed handles
prevent ordinary mistakes; bind-time validation is the security boundary.

No ModelIR migration is required for P2 conditions: existing ModelIR already
contains stable field/relation/enum identities, exact logical types, endpoint
mappings, and provider declarations. Runtime decoding/indexing and the public
relation endpoint handle are implemented by the schema registry, binder, and
generated ABI.

## 11. Explicit rejection of Phase 0 production shapes

`go/phase0` remains an oracle and fixture only. Production P2 MUST NOT copy these
shapes:

| Phase 0 shape | Production rejection |
|---|---|
| `Model string`, `Field string`, relation names, and string-keyed row maps | Fixed `ModelID`, `FieldID`, and `RelationID`; typed registry lookup; no field-name authorization API. Strings remain only legitimate scalar/JSON values and diagnostics. |
| `Operator string` | Closed, explicitly numbered `OperatorID`; diagnostic names are one-way metadata. |
| `Value any`, `[]any`, `map[string]any` | Closed `Value`, `Operand`, and exact `JSONValue` unions. |
| `reflect.DeepEqual` for operands or nodes | Type-directed equality or versioned canonical bytes. |
| JSON marshaling of arbitrary structs as canonical identity | Explicit length-delimited encoder over validated variants. |
| Missing map entry interpreted as a scalar non-match or relation empty | Evaluator row input has an explicit loaded/missing state; missing dependencies refuse or mask fail-closed, while genuine SQL NULL and loaded empty relations have separate values. |
| Field maps that silently collapse declaration order | Ordered rule fields and first-seen dependency sets. |
| Empty field rule silently ignored | Public method requires `first Field[M]`; malformed view is rejected. |
| Provider support represented by optimistic booleans | Derived, versioned capability requirements plus runtime probes and live agreement. |

Phase 0's rule-resolution behavior remains oracle evidence. Its representation,
evaluator record, equality implementation, and capability map are not production
building blocks.

## 12. Definition of done for this contract

This representation slice is implemented only when:

1. public frozen values and sealed copy-isolated views exist;
2. relation handles carry endpoint field and edge IDs;
3. the physical schema document can be decoded or an equivalent generated typed
   registry is proven against it;
4. one validated runtime schema registry is built from `SchemaBundle` and is
   indexed only by typed IDs;
5. the binder produces only the closed variants in this document and rejects all
   malformed ID/type/relation/provider fixtures;
6. value constructors/codecs cover every accepted P2 operator operand without
   `any` or lossy numeric conversion;
7. public and internal mutation-isolation tests pass under `-race`;
8. canonical bytes are deterministic under repeated construction and permitted
   commutative shuffles;
9. policy rule bytes retain declaration order; and
10. repository audits find no production policy identity keyed by model/field
    strings, no operand `any`, and no `reflect.DeepEqual`.

Passing this gate establishes the safe representation on which P2-D through P2-H
can work. It does not by itself complete P2.
