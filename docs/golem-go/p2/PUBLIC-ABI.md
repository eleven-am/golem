# P2-B public authoring ABI

Status: **controlling contract; the complete portable authoring surface and
generated handle families are implemented for SQLite and PostgreSQL**

Authority: [`../BIBLE.md`](../BIBLE.md), especially sections 0, 6–8,
20, and 21. The detailed operator and policy-resolution chapters apply after
the resolutions recorded here. [`OPERATOR-ABI.md`](./OPERATOR-ABI.md) remains
the accepted P2-A baseline; this document records the complete authoring cells
implemented after that baseline.

## 1. Scope and fixed outcomes

This document freezes the proposed Go spelling of the complete P2-B authoring
surface. It covers:

- ordered model grants and denials;
- ordered field grants and denials;
- sealed generated field identities;
- scalar, relation, comparison-mode, list, and JSON authoring;
- provider-aware generated handle selection;
- the boundary between generation, policy freeze, startup binding, and planning;
- positive and negative compile obligations.

It does not specify the internal condition node layout, rule-resolution
algorithm, evaluator, SQL renderer, or implication engine. Those components must
consume this ABI without making application authors name models, fields,
relations, operators, or SQL identifiers as strings.

The following outcomes are fixed:

1. Existing P2-A sensitive methods retain their names and meaning.
2. Policy rule declaration order is observable and must be preserved exactly.
3. Every public predicate remains rooted in one Go model type.
4. A field rule cannot be expressed with zero fields.
5. Provider support changes generated Go types; it never changes the meaning of
   a method that was generated.
6. Null scalar operands are not part of the Go ABI. Nullable fields use explicit
   presence methods.
7. JSON paths are typed provider-neutral segments. Application policy code does
   not author PostgreSQL arrays or SQLite JSONPath strings.
8. Unsupported operators are absent from generated handle types whenever the
   limitation is knowable during generation.

## 2. Ordered rule surface

### 2.1 Exact signatures

`Rules[M]` remains opaque and constructible only through `NewRules[M]`. The
complete public method set is:

```go
type Rules[M any] struct {
    // unexported ordered builder state
}

func NewRules[M any]() *Rules[M]

func (*Rules[M]) CanRead(Predicate[M])
func (*Rules[M]) CannotRead(Predicate[M])
func (*Rules[M]) CanCreate(Predicate[M])
func (*Rules[M]) CannotCreate(Predicate[M])
func (*Rules[M]) CanUpdate(Predicate[M])
func (*Rules[M]) CannotUpdate(Predicate[M])
func (*Rules[M]) CanDelete(Predicate[M])
func (*Rules[M]) CannotDelete(Predicate[M])

func (*Rules[M]) CanReadFields(
    Predicate[M],
    Field[M],
    ...Field[M],
)
func (*Rules[M]) CannotReadFields(
    Predicate[M],
    Field[M],
    ...Field[M],
)
func (*Rules[M]) CanCreateFields(
    Predicate[M],
    Field[M],
    ...Field[M],
)
func (*Rules[M]) CannotCreateFields(
    Predicate[M],
    Field[M],
    ...Field[M],
)
func (*Rules[M]) CanUpdateFields(
    Predicate[M],
    Field[M],
    ...Field[M],
)
func (*Rules[M]) CannotUpdateFields(
    Predicate[M],
    Field[M],
    ...Field[M],
)

func (*Rules[M]) Freeze(ModelID) (FrozenPolicy, error)
```

The second argument of every field method is deliberately non-variadic. The Go
type checker therefore rejects an empty field rule. Delete has no field-scoped
methods because delete has no field-data authorization.

Every call appends exactly one rule. Calls do not merge, sort, deduplicate, or
replace earlier rules. Freeze may reject a duplicate field within one field
rule, but it must not reorder rules or fields.

### 2.2 Unconditional rules

Application code spells an unconditional rule with `All[M]()`:

```go
rules.CannotRead(golem.All[Post]())
```

Freeze converts that exact root constant to the internal unconditional
representation. Application code never passes `nil` as a `Predicate[M]`, and a
zero `Predicate[M]` is invalid rather than another spelling of unconditional.

### 2.3 Rule example

```go
func (User) DefinePolicy(r *golem.Rules[User], actor Actor) {
    self := Users.ID.Eq(actor.ID)

    r.CanRead(golem.All[User]())
    r.CannotReadFields(golem.All[User](), Users.Email, Users.Phone)
    r.CanReadFields(self, Users.Email, Users.Phone)

    r.CannotUpdate(golem.All[User]())
    r.CanUpdateFields(self, Users.DisplayName, Users.Avatar)
}
```

The last applicable declaration wins. The example grants conditional update
reach through the positive field rule while limiting writable fields to the two
listed identities, exactly as required by the distinct row and field lenses.

## 3. Sealed field identity

### 3.1 Public constraint

`Field[M]` is an exported constraint with an unexported method set:

```go
type Field[M any] interface {
    fieldModel(M)
    fieldIdentity() FieldID
}
```

Application packages can use `Field[M]` as a parameter type, but cannot implement
it. Every generated scalar, enum, byte, list, JSON, to-one, and to-many handle
implements it. `Field[M]` exposes no public string name. The
`Generated*Field` constructors required by emitted packages remain exported, but
their IDs are always registry-validated during binding; calling one manually
cannot create or reassign a schema field.

`Column[M]` remains the narrower scalar/schema-expression capability. Relations
implement `Field[M]`, not `Column[M]`.

### 3.2 Relation handles must carry both identities

A relation member is both a model field and a relation hop. Its generated handle
therefore carries its relation-field `FieldID` and its `RelationID`:

```go
type ToOne[M, R any] struct {
    // unexported FieldID, RelationID, and type witnesses
}

type ToMany[M, R any] struct {
    // unexported FieldID, RelationID, and type witnesses
}

func GeneratedToOne[M, R any](FieldID, RelationID) ToOne[M, R]
func GeneratedToMany[M, R any](FieldID, RelationID) ToMany[M, R]
```

This is a required generated-template ABI change. The P2-A constructors that
accept only `RelationID` cannot support relation field rules without reconstructing
field identity from a side table. The target `ModelID` is resolved and validated
from the relation registry by the binder; `R` remains the compile-time target
type witness.

### 3.3 Identity validation

The model type parameter rejects ordinary cross-model field use at compile time.
Freeze and the internal binder still validate that every embedded `FieldID`
belongs to the supplied `ModelID`, because Go type identity is not the schema
registry and stale generated packages must fail closed.

## 4. Predicate constants and relations

The P2-A logical and relation signatures remain:

```go
func All[M any]() Predicate[M]
func None[M any]() Predicate[M]
func And[M any](...Predicate[M]) Predicate[M]
func Or[M any](...Predicate[M]) Predicate[M]
func Not[M any](Predicate[M]) Predicate[M]

func (Predicate[M]) And(...Predicate[M]) Predicate[M]
func (Predicate[M]) Or(...Predicate[M]) Predicate[M]
func (Predicate[M]) Not() Predicate[M]

func (ToOne[M, R]) Is(Predicate[R]) Predicate[M]
func (ToOne[M, R]) IsNot(Predicate[R]) Predicate[M]
func (ToOne[M, R]) IsNull() Predicate[M]
func (ToOne[M, R]) IsNotNull() Predicate[M]

func (ToMany[M, R]) Some(Predicate[R]) Predicate[M]
func (ToMany[M, R]) Every(Predicate[R]) Predicate[M]
func (ToMany[M, R]) None(Predicate[R]) Predicate[M]
```

There is no bare relation shorthand in Go; `relation.Is(predicate)` is the typed
spelling. `ToOne.IsNull` and `IsNotNull` are exposed even for a required relation
because they test related-row existence, not foreign-key nullability. To-many
quantifiers reject a zero `Predicate[R]` during freeze. Authors use `All[R]()` to
ask only about existence.

No relation method accepts a comparison mode. A nested predicate chooses its own
mode at its own scalar or JSON leaf.

## 5. Scalar handles

### 5.1 Sensitive baseline

The existing P2-A signatures remain the default, case-sensitive surface:

```go
func (EqualField[M, V]) Eq(V) Predicate[M]
func (EqualField[M, V]) Ne(V) Predicate[M]
func (EqualField[M, V]) In(...V) Predicate[M]
func (EqualField[M, V]) NotIn(...V) Predicate[M]

func (OrderedField[M, V]) LT(V) Predicate[M]
func (OrderedField[M, V]) LTE(V) Predicate[M]
func (OrderedField[M, V]) GT(V) Predicate[M]
func (OrderedField[M, V]) GTE(V) Predicate[M]

func (TextField[M, V]) Contains(V) Predicate[M]
func (TextField[M, V]) StartsWith(V) Predicate[M]
func (TextField[M, V]) EndsWith(V) Predicate[M]

func (BytesField[M]) Eq([]byte) Predicate[M]
func (BytesField[M]) Ne([]byte) Predicate[M]
func (BytesField[M]) In(...[]byte) Predicate[M]
func (BytesField[M]) NotIn(...[]byte) Predicate[M]

func (ListField[M, E]) Has(E) Predicate[M]
func (ListField[M, E]) HasEvery(...E) Predicate[M]
func (ListField[M, E]) HasSome(...E) Predicate[M]
func (ListField[M, E]) IsEmpty(bool) Predicate[M]
func (ListField[M, E]) Eq(List[E]) Predicate[M]
```

Every nullable handle embeds its non-null counterpart and adds only:

```go
func (NullableXField[M, ...]) IsNull() Predicate[M]
func (NullableXField[M, ...]) IsNotNull() Predicate[M]
```

`Eq`, `Ne`, ordering, and text methods never accept `nil`. For nullable fields,
`IsNull` and `IsNotNull` are the only null operand spellings. A nullable list's
`Eq` operand is a present list; list-column null remains distinct from an empty
list.

Boolean, UUID, and enum handles remain equality-only. Bytes remain byte-equality
only. Enum lexical ordering and enum text matching from the historical
TypeScript implementation are not exposed.

### 5.2 Comparison modes

Comparison modes are sealed values:

```go
type ComparisonMode interface {
    comparisonMode()
}

func DefaultComparison() ComparisonMode
func ASCIIInsensitive() ComparisonMode
```

The direct methods in section 5.1 are default/sensitive. A schema whose complete
declared provider set supports agreement-proved insensitive comparison receives
a mode-capable text handle:

```go
type ModeTextField[M any, V ~string] struct {
    TextField[M, V]
}

type NullableModeTextField[M any, V ~string] struct {
    NullableTextField[M, V]
}

type TextComparison[M any, V ~string] struct {
    // unexported field identity and mode
}

func GeneratedModeTextField[M any, V ~string](FieldID) ModeTextField[M, V]
func GeneratedNullableModeTextField[M any, V ~string](FieldID) NullableModeTextField[M, V]

func (ModeTextField[M, V]) Compare(ComparisonMode) TextComparison[M, V]
func (NullableModeTextField[M, V]) Compare(ComparisonMode) TextComparison[M, V]

func (TextComparison[M, V]) Eq(V) Predicate[M]
func (TextComparison[M, V]) Ne(V) Predicate[M]
func (TextComparison[M, V]) In(...V) Predicate[M]
func (TextComparison[M, V]) NotIn(...V) Predicate[M]
func (TextComparison[M, V]) LT(V) Predicate[M]
func (TextComparison[M, V]) LTE(V) Predicate[M]
func (TextComparison[M, V]) GT(V) Predicate[M]
func (TextComparison[M, V]) GTE(V) Predicate[M]
func (TextComparison[M, V]) Contains(V) Predicate[M]
func (TextComparison[M, V]) StartsWith(V) Predicate[M]
func (TextComparison[M, V]) EndsWith(V) Predicate[M]
```

Example:

```go
Users.Name.Compare(golem.ASCIIInsensitive()).StartsWith(actor.Prefix)
```

`ComparisonMode` is sealed so an application cannot invent a locale or Unicode
folding mode. `ASCIIInsensitive` means exactly the agreement-corpus ASCII fold;
it does not promise Unicode case folding or locale behavior.

The current generator emits `ModeTextField` because the SQLite and PostgreSQL
implementations and agreement corpus now exist. There is no PostgreSQL-only
public text operator cell. Runtime startup still validates the provider proof and
closed agreement inventory before accepting a policy.

## 6. Typed JSON authoring

### 6.1 Why the Go path syntax is provider-neutral

The detailed TypeScript evidence records PostgreSQL string-segment arrays and
SQLite JSONPath strings. That syntax reflects provider client inputs, not the
Bible's provider-neutral condition-tree requirement. The Go ABI resolves the
difference once, before either renderer, with typed segments:

```go
type JSONPath struct {
    // unexported copied segments
}

type JSONPathSegment interface {
    jsonPathSegment()
}

func JSONKey(string) JSONPathSegment
func JSONIndex(uint32) JSONPathSegment
func NewJSONPath(JSONPathSegment, ...JSONPathSegment) JSONPath
```

Whole-document operations use `Root()`. `NewJSONPath` requires one segment, so
an empty path cannot be confused with the root. Renderers lower the same path to
their provider syntax. Keys are data, not SQL identifiers, and path values are
copied into the predicate.

### 6.2 Exact JSON values and sentinels

JSON operands use sealed exact values rather than `any`:

```go
type JSONValue interface {
    jsonValue()
}

type JSONScalarValue interface {
    JSONValue
    jsonScalarValue()
}

type JSONOrderedValue interface {
    JSONScalarValue
    jsonOrderedValue()
}

type JSONEqualityOperand interface {
    jsonEqualityOperand()
}

type JSONStringValue struct { /* opaque */ }
type JSONNumberValue struct { /* opaque exact base-10 number */ }
type JSONBoolValue struct { /* opaque */ }
type JSONArrayValue struct { /* opaque copied values */ }
type JSONObjectValue struct { /* opaque copied map */ }

func JSONString(string) JSONStringValue
func JSONNumber(Decimal) JSONNumberValue
func ParseJSONNumber(string) (JSONNumberValue, error)
func JSONBool(bool) JSONBoolValue
func JSONArray(...JSONValue) JSONArrayValue
func JSONObject(map[string]JSONValue) JSONObjectValue
func ParseJSON([]byte) (JSONValue, error)
```

`JSONNumber` is the convenience bridge for a portable `Decimal`. The parser
accepts the wider exact JSON-number grammar without routing through `float64` or
the P1 `Decimal(18,s)` storage ceiling. `JSONStringValue` and
`JSONNumberValue` implement `JSONOrderedValue`.
`JSONBoolValue` and the JSON-null sentinel implement `JSONScalarValue`.
All five concrete value families implement `JSONValue` and
`JSONEqualityOperand`.

The three null sentinels are exported constants of distinct unexported concrete
types. They are not mutable package variables and cannot be forged from strings:

```go
const DBNull   /* unexported db-null sentinel type */
const JSONNull /* unexported JSON-null sentinel type */
const AnyNull  /* unexported any-null sentinel type */
```

`DBNull`, `JSONNull`, and `AnyNull` implement `JSONEqualityOperand`.
`JSONNull` also implements `JSONScalarValue` and `JSONValue`; the other two do
not. This permits JSON literal null inside an array or object without allowing
SQL-null or any-null to masquerade as document data.

Constructors copy input bytes, slices, and maps. `ParseJSON` uses exact number
decoding, rejects duplicate object keys and trailing data, and returns a canonical
value. `JSONObject(nil)` and `JSONArray()` are valid empty containers. Freeze
rejects nil interface elements, non-canonical values, non-finite values entering
through any future numeric constructor, and a zero `JSONValue` implementation.

### 6.3 Eventual portable JSON target

There is one eventual public JSON vocabulary. It is emitted only after every
method cell below has an exact Go evaluator, SQLite implementation, PostgreSQL
implementation, and live agreement proof:

```go
type JSONField[M any] struct { /* opaque FieldID */ }
type NullableJSONField[M any] struct { JSONField[M] }

type JSONTarget[M any] struct { /* opaque FieldID and path */ }

func GeneratedJSONField[M any](FieldID) JSONField[M]
func GeneratedNullableJSONField[M any](FieldID) NullableJSONField[M]

func (JSONField[M]) Root() JSONTarget[M]
func (JSONField[M]) At(JSONPath) JSONTarget[M]
func (NullableJSONField[M]) IsNull() Predicate[M]
func (NullableJSONField[M]) IsNotNull() Predicate[M]

func (JSONTarget[M]) Eq(JSONEqualityOperand) Predicate[M]
func (JSONTarget[M]) Ne(JSONEqualityOperand) Predicate[M]
func (JSONTarget[M]) LT(JSONOrderedValue) Predicate[M]
func (JSONTarget[M]) LTE(JSONOrderedValue) Predicate[M]
func (JSONTarget[M]) GT(JSONOrderedValue) Predicate[M]
func (JSONTarget[M]) GTE(JSONOrderedValue) Predicate[M]
func (JSONTarget[M]) StringContains(string) Predicate[M]
func (JSONTarget[M]) StringStartsWith(string) Predicate[M]
func (JSONTarget[M]) StringEndsWith(string) Predicate[M]
func (JSONTarget[M]) ArrayContains(JSONValue) Predicate[M]
func (JSONTarget[M]) ArrayStartsWith(JSONValue) Predicate[M]
func (JSONTarget[M]) ArrayEndsWith(JSONValue) Predicate[M]
```

`NullableJSONField.IsNull` tests SQL-column null. At a path, `Eq(DBNull)` tests
an absent addressed slot, `Eq(JSONNull)` tests a present JSON literal null, and
`Eq(AnyNull)` accepts either. The same sentinels under `Ne` use the exact
two-valued complements specified by the operator table.

Comparison-mode support is a separate proved capability layer:

```go
type ModeJSONField[M any] struct { JSONField[M] }
type NullableModeJSONField[M any] struct { ModeJSONField[M] }
type ModeJSONTarget[M any] struct { /* opaque FieldID and path */ }
type JSONStringComparison[M any] struct { /* opaque target and mode */ }

func GeneratedModeJSONField[M any](FieldID) ModeJSONField[M]
func GeneratedNullableModeJSONField[M any](FieldID) NullableModeJSONField[M]

func (ModeJSONField[M]) Root() ModeJSONTarget[M]
func (ModeJSONField[M]) At(JSONPath) ModeJSONTarget[M]
func (NullableModeJSONField[M]) IsNull() Predicate[M]
func (NullableModeJSONField[M]) IsNotNull() Predicate[M]

func (ModeJSONTarget[M]) Eq(JSONEqualityOperand) Predicate[M]
func (ModeJSONTarget[M]) Ne(JSONEqualityOperand) Predicate[M]
func (ModeJSONTarget[M]) LT(JSONOrderedValue) Predicate[M]
func (ModeJSONTarget[M]) LTE(JSONOrderedValue) Predicate[M]
func (ModeJSONTarget[M]) GT(JSONOrderedValue) Predicate[M]
func (ModeJSONTarget[M]) GTE(JSONOrderedValue) Predicate[M]
func (ModeJSONTarget[M]) StringContains(string) Predicate[M]
func (ModeJSONTarget[M]) StringStartsWith(string) Predicate[M]
func (ModeJSONTarget[M]) StringEndsWith(string) Predicate[M]
func (ModeJSONTarget[M]) ArrayContains(JSONValue) Predicate[M]
func (ModeJSONTarget[M]) ArrayStartsWith(JSONValue) Predicate[M]
func (ModeJSONTarget[M]) ArrayEndsWith(JSONValue) Predicate[M]
func (ModeJSONTarget[M]) Compare(ComparisonMode) JSONStringComparison[M]

func (JSONStringComparison[M]) Contains(string) Predicate[M]
func (JSONStringComparison[M]) StartsWith(string) Predicate[M]
func (JSONStringComparison[M]) EndsWith(string) Predicate[M]
```

The mode view intentionally exposes only the three JSON string operators.
Insensitive JSON equality, ordering, and array operations do not compile because
the detailed contract applies JSON `mode` only to string matching.

The current generator emits `ModeJSONField`, which includes the sensitive JSON
matrix and the mode-bearing string operations. No PostgreSQL-only JSON handle is
part of the public vocabulary. SQLite supplies deterministic registered modernc
functions for semantics JSON1 cannot prove exactly; PostgreSQL uses guarded
`jsonb` operations. Both remain subject to startup capability proof and the live
agreement gate.

### 6.4 JSON conjunction

The Go API does not reproduce a multi-key JSON filter object. Authors conjoin
leaves with predicate combinators:

```go
path := golem.NewJSONPath(golem.JSONKey("profile"), golem.JSONKey("name"))

visible := Posts.Metadata.At(path).StringStartsWith("A").And(
    Posts.Metadata.At(path).Ne(golem.JSONNull),
)
```

This produces the same conjunction without an open struct containing operator
strings, mutually invalid keys, or `any` operands.

## 7. Provider-aware handle exposure

### 7.1 Generation algorithm

For each generated field, code generation computes the intersection of operator
capabilities across the model schema's complete declared provider set and storage
representation. It then emits the narrowest public handle whose entire method set
is supported:

| Schema capability | Generated handle |
|---|---|
| equality scalar | `EqualField` / nullable form |
| ordered scalar | `OrderedField` / nullable form |
| sensitive string | `TextField` / nullable form |
| insensitive agreement proved on SQLite and PostgreSQL | `ModeTextField` / nullable form |
| bytes equality | `BytesField` / nullable form |
| scalar-list storage/operators proved on SQLite and PostgreSQL | `ListField` / nullable form |
| complete sensitive JSON matrix proved on SQLite and PostgreSQL | `JSONField` / nullable form |
| sensitive JSON plus insensitive strings proved on both | `ModeJSONField` / nullable form |
| opaque JSON without accepted filter capability | existing `OpaqueField` / nullable form |

The P2 vocabulary is portable or closed and contains no PostgreSQL-only public
operator handle. The current generator emits list and mode-aware JSON/text
handles because their provider implementations and oracle inventory are present.
Unsupported storage and provider facts still refuse during compilation, binding,
capability proof, or runtime startup.

### 7.2 Validation timing

Capability enforcement has four deliberately separate moments:

1. **Generation/type checking:** knowable operator-family limitations change the
   generated handle and therefore fail as missing Go methods.
2. **Freeze:** actor-derived operands, zero predicates, JSON values and paths,
   duplicate rule fields, and builder-local shape errors are validated before a
   `FrozenPolicy` is returned.
3. **Binding/startup:** frozen IDs, model ownership, relation transitions,
   schema digest, and declared provider capabilities are revalidated against the
   decoded P1 schema bundle. Stale generated packages fail before an engine is
   accepted for execution.
4. **Planning:** only operation-specific context is checked, such as an explicitly
   supplied related-model constraint. Planning may not discover that a public
   operator lacks a provider implementation.

Actor-specific policy code cannot be executed at application startup, so
actor-derived value validation necessarily occurs when that execution's policy is
frozen. This is not a provider capability delay: unsupported provider methods were
already absent at generation time.

## 8. Frozen public boundary

`Predicate[M]`, `FrozenPredicate`, and `FrozenPolicy` remain representation-opaque.
Application code cannot construct them from IDs, operator names, raw nodes, or
canonical bytes. The public freeze/view entry points are:

```go
func (Predicate[M]) Freeze(ModelDescriptor[M]) (FrozenPredicate, error)
func (FrozenPredicate) View() FrozenPredicateView
func (FrozenPolicy) View() FrozenPolicyView
```

`Rules[M].Freeze(ModelID)` remains the generated policy-bridge entry point from
section 2.1. It preserves rule order and returns the opaque `FrozenPolicy`.

The exact sealed node/rule accessor interfaces are owned by the internal-IR/binder
contract so there is one representation seam rather than two. Those views must:

- be sealed with an unexported marker;
- return copies of bytes, lists, paths, fields, and children;
- expose fixed `ModelID`, `FieldID`, and `RelationID` values, never names;
- preserve rule declaration order;
- carry a version and canonical encoding;
- permit no mutation or application-provided implementation; and
- avoid a `go/golem` import of `internal/policy/*`.

The internal-IR/binder contract must freeze those read-only accessor signatures
before predicate representation code lands.

## 9. Required compile fixtures

### 9.1 Positive fixtures

Generated packages must compile examples covering:

- all eight model-wide grant/deny methods;
- all six field-scoped methods with one and several scalar/relation fields;
- mixed ordered rule declarations proving source order reaches the binding;
- every sensitive scalar method family;
- nullable presence for every nullable handle family;
- `ToOne.Is`, `IsNot`, `IsNull`, `IsNotNull` on optional and required relations;
- `ToMany.Some`, `Every`, and `None`;
- default and insensitive text views after the dual-provider capability gate;
- JSON root/path, equality, ordering, three sentinels, string, array, and
  insensitive-string methods after their dual-provider capability gates;
- scalar-list methods after their dual-provider capability gate;
- `Field[M]` helpers authored in the application package without application
  implementations of the sealed interface.

### 9.2 Negative fixtures

Each forbidden family needs an isolated fixture so one compiler error cannot
hide another. At minimum, generation/bootstrap type checking must reject:

1. a `Predicate[Post]` passed to `Rules[User]`;
2. a `Field[Post]` passed to a `Rules[User]` field method;
3. a field-rule call with no field argument;
4. a delete field-rule method, which does not exist;
5. `GT` on bool, UUID, enum, bytes, list, or JSON handles without the specific
   JSON ordered target;
6. text methods on enum, numeric, bool, UUID, bytes, or list handles;
7. `IsNull` on a non-null scalar/list/JSON handle;
8. `Some` on `ToOne` and `Is` on `ToMany`;
9. a nested relation predicate rooted in the wrong related model;
10. `Compare` on a generated dual-provider text handle lacking insensitive
    agreement;
11. a caller-defined implementation of `ComparisonMode`, `Field[M]`,
    `JSONPathSegment`, or any JSON operand interface;
12. an empty JSON path constructor call;
13. every JSON method while the generated field remains `OpaqueField` before
    the dual-provider gate;
14. insensitive JSON equality, ordering, or array operations after the sensitive
    JSON handle is enabled;
15. `DBNull` or `AnyNull` used as an array/object JSON value;
16. scalar operators on a relation and relation operators on a scalar;
17. scalar-list methods before the dual-provider agreement gate;
18. scalar-list methods in a schema whose storage declaration rejects
    scalar lists;
19. arbitrary strings used where a field identity, relation identity, operator,
    path segment, or comparison mode is required.

Freeze/binder tests, rather than compile fixtures, own values that Go's type
system cannot exclude: a zero predicate, nil interface inside a JSON container,
duplicate field identities in one rule, stale IDs, mismatched schema digests, and
corrupt generated handles constructed by package-internal tests.

## 10. Compatibility and ABI changes

This proposal requires a generated-template ABI bump because it adds rule methods,
adds `Field[M]`, changes generated relation constructors to accept both IDs, adds
new provider-aware field families, and replaces JSON opacity where capabilities
have passed acceptance.

The following P2-A source remains compatible where the generated field retains
the corresponding proved capability:

- direct sensitive scalar and bytes methods;
- nullable presence methods;
- relation predicate method names;
- predicate combinators;
- four existing `Can*` calls.

Generated code must be regenerated because relation initializers and eligible
field handle types changed. `ListField`, `ModeTextField`, and `ModeJSONField` are
now emitted by the current template ABI.

## 11. Contradictions and required resolutions

The following source contradictions were found. The proposed resolution is stated
where authority is sufficient; genuinely unresolved items remain explicit.

### 11.1 Resolved by the Bible's authority

- `01-operators.md` says the Go port need not ship SQLite. Bible sections 2, 4,
  20, and 21 require SQLite and PostgreSQL equally. This proposal uses the Bible.
- The detailed JSON chapter exposes provider input path spellings. Bible section
  7 requires a provider-neutral condition tree. This proposal uses typed segments
  and leaves provider spelling to renderers.
- The detailed scalar table accepts null operands for ordering/text and gives them
  constant-false meaning. Bible section 7.3 permits one documented null rule and
  the accepted P2-A Go contract chose explicit presence methods. This proposal
  preserves the typed Go rule and does not import TypeScript null operands.
- Historical enum-to-text behavior permits lexical and text operations. The
  accepted typed baseline intentionally makes enums equality-only. This proposal
  does not reopen that TypeScript leak.
- The detailed evaluator treats an unloaded to-many relation as empty. The P2
  plan requires missing dependency data to be distinct and fail closed. The P2
  plan/Bible security invariant wins.

### 11.2 Resolved during implementation

1. **Portable JSON implementation.** Exact JSON equality, ordering, typed paths,
   null sentinels, string operations, and array operations now have one Go
   evaluator, deterministic SQLite functions, PostgreSQL `jsonb` lowering, and a
   shared corpus. PostgreSQL rejects exact JSON numbers outside `jsonb`'s physical
   `numeric` range before execution.
2. **Portable scalar-list implementation.** `ListField` targets P1's canonical
   JSON-array storage on both providers; it never assumes PostgreSQL native
   arrays.
3. **Frozen inspection accessors.** The sealed copy-isolated views live once in
   `go/golem`, and `internal/policy/bind` is their sole production consumer.
4. **Decimal and JSON numbers.** Public `Decimal` remains the portable
   precision-18 scalar. Exact JSON numbers use their separate canonical
   coefficient/exponent representation and never route through `float64`.

None of these permits renderer-time approximation. Provider physical limits are
fail-closed boundaries, not coercion rules.

## 12. P2-B ABI definition of done

The public ABI portion of P2-B is complete only when:

1. this proposal's accepted signatures are reconciled into the controlling P2
   contracts;
2. generated positive fixtures compile for each declared provider capability;
3. every negative family has an isolated stable diagnostic fixture;
4. relation handles carry both field and relation identities;
5. no exported authoring API accepts field/model/relation/operator names or raw
   SQL strings;
6. generated method availability is the intersection of declared provider
   capabilities;
7. freeze and binder validation timings are tested independently;
8. all input bytes, lists, JSON maps, paths, and rule fields are copy-isolated;
9. template ABI and deterministic generated goldens are updated; and
10. documentation records the exact inventory whose evaluator and both live
    provider agreement gates passed, while keeping future unproved cells closed.
