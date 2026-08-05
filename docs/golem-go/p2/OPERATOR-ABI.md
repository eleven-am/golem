# P2 portable operator ABI

Status: **accepted P2-A typed baseline; not the complete P2 contract**

Scope: typed policy predicates, row grants, in-memory evaluation, and SQL lowering
Authority: subordinate only to [`../BIBLE.md`](../BIBLE.md); this file owns the
P2-A public operator ABI. Supporting operator research is non-controlling where it
conflicts with this file. The complete P2 scope, including the Bible-required rule
surface and advanced accepted operators, is controlled by
[`P2-PLAN.md`](./P2-PLAN.md) and its linked P2-B contracts.

This file freezes the already implemented baseline so later work does not mutate
its method meanings. Its explicit deferrals are deferrals from P2-A, not permission
to declare the complete P2 policy kernel finished without them.

## 1. Contract boundary

V1 policies are inspectable typed predicate trees. They are not callbacks, raw
SQL, provider expressions, or maps of string operators. Every accepted predicate
MUST:

1. be well typed when authored;
2. normalize deterministically;
3. evaluate to exactly `true` or `false`, including on nulls;
4. render only identifiers from generated descriptors and bound operands; and
5. select the same rows in memory, SQLite, and PostgreSQL.

The only V1 rule methods are the four P1 methods:

```go
func (*Rules[M]) CanRead(Predicate[M])
func (*Rules[M]) CanCreate(Predicate[M])
func (*Rules[M]) CanUpdate(Predicate[M])
func (*Rules[M]) CanDelete(Predicate[M])
```

Each call adds a grant. Grants for one action are ORed; no grant means deny. V1
has no rule priority, deny rule, or field rule.

## 2. Frozen authoring vocabulary

Generated fields use the narrowest capability handle in this table. Nullable
forms preserve the listed non-null operators and add `IsNull` and `IsNotNull`.
Methods that do not appear in the table MUST NOT be generated.

| Logical type | Go value | Public operators |
|---|---|---|
| `Bool` | `bool` | equality |
| `Int16`, `Int32`, `Int64` | `int16`, `int32`, `int64` | equality, ordered |
| `Float32`, `Float64` | `float32`, `float64` | equality, ordered |
| `Decimal(p,s)` | `golem.Decimal` | equality, ordered |
| `String` | `string` | equality, ordered, text |
| `Bytes` | `[]byte` | equality, byte-for-byte |
| `UUID` | `golem.UUID` | equality |
| `Date` | `golem.Date` | equality, ordered |
| `Time(p)` | `golem.Time` | equality, ordered |
| UTC `DateTime(p)` | `time.Time` | equality, ordered |
| closed `Enum` | its named string-backed Go type | equality only |
| canonical `JSON` | `golem.JSON[T]` or `golem.JSON[any]` | no value operator; nullable presence only |
| `ScalarList(E)` | `golem.List[E]` | list operators |

Pointer declarations and `golem.Null[T]` both generate the same nullable field
capability over the non-null value `T`; pointer identity is not part of the
predicate ABI. Non-finite floats are rejected before a predicate is frozen.

The method families are:

```go
// EqualField[M, V], including NullableEqualField[M, V].
Eq(V) Predicate[M]
Ne(V) Predicate[M]
In(...V) Predicate[M]
NotIn(...V) Predicate[M]

// OrderedField[M, V] and NullableOrderedField[M, V], in addition to equality.
LT(V) Predicate[M]
LTE(V) Predicate[M]
GT(V) Predicate[M]
GTE(V) Predicate[M]

// TextField[M, V] and NullableTextField[M, V], in addition to equality and ordering.
Contains(V) Predicate[M]
StartsWith(V) Predicate[M]
EndsWith(V) Predicate[M]

// BytesField[M] and NullableBytesField[M].
Eq([]byte) Predicate[M]
Ne([]byte) Predicate[M]
In(...[]byte) Predicate[M]
NotIn(...[]byte) Predicate[M]

// NullableEqualField, NullableOrderedField, NullableTextField,
// NullableListField, NullableBytesField, and NullableOpaqueField only.
IsNull() Predicate[M]
IsNotNull() Predicate[M]

// ListField[M, E] and NullableListField[M, E]. Eq is ordered whole-list equality.
Eq(golem.List[E]) Predicate[M]
Has(E) Predicate[M]
HasEvery(...E) Predicate[M]
HasSome(...E) Predicate[M]
IsEmpty(bool) Predicate[M]
```

`E` may be Bool, integer, finite float, Decimal, String, UUID, Date, Time,
DateTime, or Enum. Bytes, JSON, relations, and nested lists are not V1 list
elements.

Relations expose:

```go
// ToOne[M, R]
Is(Predicate[R]) Predicate[M]
IsNot(Predicate[R]) Predicate[M]
IsNull() Predicate[M]
IsNotNull() Predicate[M]

// ToMany[M, R]
Some(Predicate[R]) Predicate[M]
Every(Predicate[R]) Predicate[M]
None(Predicate[R]) Predicate[M]
```

There is no separate nullable to-one handle in V1. Generated required and
nullable to-one relations both use `ToOne[M, R]` and expose all four methods.
Presence is determined from related-row existence for both forms; code generation
MUST NOT invent a second relation handle type merely to hide those methods.

Predicates expose both constructors and fluent sugar:

```go
golem.All[M]()
golem.None[M]()
golem.And[M](predicates...)
golem.Or[M](predicates...)
golem.Not(predicate)

predicate.And(more...)
predicate.Or(more...)
predicate.Not()
```

`All` and `None` are the explicit true and false constants. The constructor
forms are controlling because `And` and `Or` can represent empty input. Fluent
forms normalize to the same nodes.

## 3. Exact semantics

### 3.1 Equality and order

`Eq` is null-safe equality over one declared logical type. `Ne` is its exact
two-valued negation. `In` is an OR of equality tests; `NotIn` is its exact
negation. There is no cross-type coercion.

Numeric comparison preserves the declared value exactly. Decimal never routes
through a float. String equality, ordering, and text matching use canonical UTF-8
binary order, independent of database locale. Bytes compare by length and byte
sequence. UUID compares its 16 bytes. Date, Time, and DateTime compare their
normalized logical values. Enum compares labels for equality only.

Text operators are case-sensitive literal substring/prefix/suffix tests. `%`,
`_`, and `\` are ordinary operand characters and MUST be escaped when lowered to
SQL patterns.

### 3.2 Null truth table

Every leaf is two-valued. For a nullable subject `x` and non-null operand `v`:

| Predicate when `x` is null | Result |
|---|---|
| `x.Eq(v)` | false |
| `x.Ne(v)` | true |
| `x.In(vs...)` | false |
| `x.NotIn(vs...)` | true |
| ordered or text predicate | false |
| `x.IsNull()` | true |
| `x.IsNotNull()` | false |

Null is never passed as a value operand. Authors use the presence methods. This
deliberately avoids TypeScript's `undefined == null` and constant-false null
ordering/text operands.

For a nullable list, every list value operator, including whole-list `Eq`, is
false on null; the `Eq` operand itself is always a non-null list. Presence is
expressed solely with `IsNull`/`IsNotNull`. In particular, both `IsEmpty(true)`
and `IsEmpty(false)` are false on null.

For a nullable to-one relation, `Is(p)` is false on null and `IsNot(p)` is true;
the latter is exact logical negation. Presence methods test relation existence.
For every to-one relation, presence means existence of the related row and is
evaluated/rendered through the relation identity. Required field nullability alone
does not prove presence; a relation may be dangling under database drift.

### 3.3 Empty operands and quantifiers

| Predicate | Result |
|---|---|
| `In()` | false |
| `NotIn()` | true |
| `And()` / `All()` | true |
| `Or()` / `None()` | false |
| `HasEvery()` | true for a present list, false for null |
| `HasSome()` | false |
| `Some(p)` on no related rows | false |
| `Every(p)` on no related rows | true |
| `None(p)` on no related rows | true |

List `Eq` is order- and length-sensitive. `HasEvery` is universal over operands;
`HasSome` is existential. `IsEmpty(true)` means present with length zero and
`IsEmpty(false)` means present with positive length.

`Some` is existential over related rows, `Every` is universal, and `None` is
negated existence. SQL lowering of `Every` MUST detect a related row for which
the nested predicate is not true; a bare SQL `NOT(predicate)` is not sufficient
unless two-valuedness is already proved.

## 4. Provider capability gate

The public operator name never selects a provider approximation. The compiler
records required semantic capabilities in the frozen predicate. Schema checking
and engine startup MUST reject a predicate if any declared provider lacks an
agreement-proved lowering.

V1 scalar operators are portable across SQLite and PostgreSQL. `ListField` is
authorable only where the P1 schema/provider capability gate accepted
`ScalarList`; SQL-lowering and evaluator equivalence are required before P2
activation. This contract does not claim that cross-provider scalar-list lowering
already exists. A provider-specific future list storage or operator requires an
explicit single-provider schema restriction; it MUST NOT change the meaning of
an existing portable method.

The same rule governs every future advanced operator: capability-gate at schema
compile/startup, never at the first request, and never fall back to row-by-row
policy evaluation after pagination.

## 5. Explicit V1 deferrals

The following are outside this P2-A ABI and require a later P2 ABI revision before
the complete P2 gate:

- case-insensitive text and locale/collation modes;
- typed JSON equality, ordering, containment, paths, or array/string operators;
- aggregation leaves such as count, sum, average, minimum, or maximum;
- field-vs-field operands or any dynamic field reference;
- recursive scalar `not` filter objects; use predicate-level `Not`;
- deny rules, rule priority, and read/create/update field policies; and
- reusing or refactoring the P1 schema-expression IR as the policy IR.

JSON is intentionally opaque in V1. Only nullable JSON fields expose `IsNull` and
`IsNotNull`; required JSON fields expose no predicate method. Aggregation remains
a read operation constrained by the resulting row predicate, not a policy leaf.

## 6. Minimal social policy corpus

This corpus is normative authoring syntax. It uses only the frozen four `Can*`
methods; descriptor declarations are omitted.

```go
func acceptedFriend(actor Actor) golem.Predicate[User] {
	return Users.Friendships.Some(
		Friendships.UserID.Eq(actor.ID).
			And(Friendships.Status.Eq(FriendshipAccepted)),
	)
}

func (User) DefinePolicy(r *golem.Rules[User], actor Actor) {
	self := Users.ID.Eq(actor.ID)
	discoverable := Users.Handle.StartsWith(actor.HandlePrefix).
		And(Users.CreatedAt.LTE(actor.Now)).
		And(Users.Interests.HasSome("go", "security")).
		And(Users.Profile.IsNotNull()).
		And(Users.SessionDigest.Eq(actor.SessionDigest))

	r.CanRead(self.Or(discoverable, acceptedFriend(actor)))
	r.CanCreate(self.And(Users.Interests.IsEmpty(false)))
	r.CanUpdate(self.And(Users.DeletedAt.IsNull()))
	r.CanDelete(self)
}

func (Post) DefinePolicy(r *golem.Rules[Post], actor Actor) {
	own := Posts.AuthorID.Eq(actor.ID)
	public := Posts.Visibility.Eq(VisibilityPublic)
	friends := Posts.Visibility.Eq(VisibilityFriends).
		And(Posts.Author.Is(acceptedFriend(actor)))
	clean := Posts.Comments.None(Comments.Hidden.Eq(true))

	r.CanRead(golem.Or(public, own, friends).And(clean))
	r.CanCreate(own.And(Posts.PublishedAt.IsNull()))
	r.CanUpdate(own.And(Posts.ArchivedAt.IsNull()))
	r.CanDelete(own)
}
```

## 7. TypeScript evidence and deliberate divergences

The TypeScript table establishes the useful semantic oracle: its scalar,
relation, list, and logical inventories are centralized in
[`operators.ts`](../../../typescript/packages/policy/src/operators.ts#L34-L56),
with two-valued null declarations on every entry. Equality/membership null
behavior is implemented at
[`operators.ts`](../../../typescript/packages/policy/src/operators.ts#L577-L772),
list behavior at
[`operators.ts`](../../../typescript/packages/policy/src/operators.ts#L854-L1029),
relation quantifiers at
[`operators.ts`](../../../typescript/packages/policy/src/operators.ts#L1097-L1202),
and empty logical identities at
[`operators.ts`](../../../typescript/packages/policy/src/operators.ts#L1296-L1339).
Exact mixed numeric and temporal comparison is defined in
[`values.ts`](../../../typescript/packages/policy/src/values.ts#L115-L217).

V1 deliberately does **not** copy all TypeScript behavior:

- TypeScript folds ASCII for `mode: "insensitive"`, with dialect-dependent SQL
  support ([`operators.ts`](../../../typescript/packages/policy/src/operators.ts#L333-L440)); V1 is sensitive only.
- TypeScript accepts null/undefined as scalar operands and treats undefined as
  null ([`values.ts`](../../../typescript/packages/policy/src/values.ts#L38-L41));
  V1 uses typed presence methods.
- TypeScript casts enum columns to text, permitting lexical/text behavior outside
  Prisma's enum surface ([`scope.ts`](../../../typescript/packages/policy/src/scope.ts#L107-L115));
  V1 enums are equality-only.
- TypeScript has a provider-sensitive typed JSON filter language
  ([`json.ts`](../../../typescript/packages/policy/src/json.ts#L22-L70)), while
  ordinary CASL rule validation runs without datamodel context
  ([`policy.ts`](../../../typescript/packages/authorizer/src/policy.ts#L58-L67));
  V1 keeps JSON opaque.
- TypeScript does not recognize byte arrays as scalar values
  ([`values.ts`](../../../typescript/packages/policy/src/values.ts#L38-L57)); V1
  adds explicit byte-for-byte equality.
- TypeScript accepts only literal scalar kinds, so Prisma field-reference objects
  are unsupported; V1 freezes the same literal-only stance.
- TypeScript aggregation groups are a separate read surface, not condition
  operators ([`aggregations.ts`](../../../typescript/packages/core/src/aggregations.ts#L45-L104));
  V1 preserves that separation.
