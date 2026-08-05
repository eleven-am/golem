package golem

import (
	"reflect"
	"testing"
)

type declarationActor struct{ TenantID UUID }

type declarationUser struct {
	_           struct{} `golem:"model;id=test.User;table=users"`
	ID          UUID     `db:"id" golem:"id=test.User.ID;pk;default=uuid"`
	Score       int64    `db:"score"`
	Title       string   `db:"title"`
	SearchTitle string   `db:"search_title" golem:"readonly"`
	DeletedAt   Null[Date]
}

var declarationUsers = struct {
	ID          EqualField[declarationUser, UUID]
	Score       OrderedField[declarationUser, int64]
	Title       TextField[declarationUser, string]
	SearchTitle TextField[declarationUser, string]
	DeletedAt   NullableOrderedField[declarationUser, Date]
}{
	ID:          GeneratedEqualField[declarationUser, UUID](FieldID{0x01}),
	Score:       GeneratedOrderedField[declarationUser, int64](FieldID{0x02}),
	Title:       GeneratedTextField[declarationUser, string](FieldID{0x03}),
	SearchTitle: GeneratedTextField[declarationUser, string](FieldID{0x04}),
	DeletedAt:   GeneratedNullableOrderedField[declarationUser, Date](FieldID{0x05}),
}

var declarationCapabilities = struct {
	Flags            EqualField[declarationUser, bool]
	Aliases          ListField[declarationUser, string]
	OptionalAliases  NullableListField[declarationUser, string]
	Payload          BytesField[declarationUser]
	OptionalPayload  NullableBytesField[declarationUser]
	Metadata         OpaqueField[declarationUser, JSON[any]]
	OptionalMetadata NullableOpaqueField[declarationUser, JSON[any]]
}{
	Flags:            GeneratedEqualField[declarationUser, bool](FieldID{0x06}),
	Aliases:          GeneratedListField[declarationUser, string](FieldID{0x07}),
	OptionalAliases:  GeneratedNullableListField[declarationUser, string](FieldID{0x08}),
	Payload:          GeneratedBytesField[declarationUser](FieldID{0x09}),
	OptionalPayload:  GeneratedNullableBytesField[declarationUser](FieldID{0x0a}),
	Metadata:         GeneratedOpaqueField[declarationUser, JSON[any]](FieldID{0x0b}),
	OptionalMetadata: GeneratedNullableOpaqueField[declarationUser, JSON[any]](FieldID{0x0c}),
}

func DefineSchemaFixture(schema *Schema) {
	SchemaName(schema, "test")
	Actor[declarationActor](schema)
	Model[declarationUser](schema)
	Providers(schema, SQLite, PostgreSQL)
}

func (declarationUser) GolemModel() ModelSpec[declarationUser] {
	return DefineModel[declarationUser](
		PrimaryKey[declarationUser]("pk_users", declarationUsers.ID),
		Unique[declarationUser]("uq_users_id", declarationUsers.ID),
		Index[declarationUser]("idx_users_id").
			Keys(IndexColumn(declarationUsers.ID)),
		Index[declarationUser]("idx_users_lower_title").
			Keys(IndexExpr(Lower(declarationUsers.Title))).
			Where(declarationUsers.DeletedAt.Expr().IsNull()),
		Check[declarationUser]("ck_users_score", declarationUsers.Score.Expr().GTE(0)),
		Generated(declarationUsers.SearchTitle, Lower(declarationUsers.Title), Stored),
		ForProvider[declarationUser](PostgreSQL,
			Index[declarationUser]("idx_users_title_postgresql").Keys(IndexColumn(declarationUsers.Title)),
		),
	)
}

func TestDescriptorIDsAreFixedWidthAndNotStringConvertible(t *testing.T) {
	stringType := reflect.TypeOf("")
	for _, value := range []any{ModelID{}, FieldID{}, RelationID{}, KeyID{}} {
		idType := reflect.TypeOf(value)
		if idType.Kind() != reflect.Array || idType.Len() != 16 || idType.Elem().Kind() != reflect.Uint8 {
			t.Fatalf("%s is not a fixed 128-bit value: %s", idType.Name(), idType)
		}
		if stringType.AssignableTo(idType) || stringType.ConvertibleTo(idType) {
			t.Fatalf("string must not be assignable or convertible to %s", idType.Name())
		}
		if idType.AssignableTo(stringType) || idType.ConvertibleTo(stringType) {
			t.Fatalf("%s must not be assignable or convertible to string", idType.Name())
		}
	}
}

func TestGeneratedDescriptorConstructorsRequireTypedIDs(t *testing.T) {
	constructors := []struct {
		name string
		fn   any
		want reflect.Type
	}{
		{"GeneratedEqualField", GeneratedEqualField[declarationUser, UUID], reflect.TypeOf(FieldID{})},
		{"GeneratedOrderedField", GeneratedOrderedField[declarationUser, int64], reflect.TypeOf(FieldID{})},
		{"GeneratedTextField", GeneratedTextField[declarationUser, string], reflect.TypeOf(FieldID{})},
		{"GeneratedListField", GeneratedListField[declarationUser, string], reflect.TypeOf(FieldID{})},
		{"GeneratedBytesField", GeneratedBytesField[declarationUser], reflect.TypeOf(FieldID{})},
		{"GeneratedOpaqueField", GeneratedOpaqueField[declarationUser, JSON[any]], reflect.TypeOf(FieldID{})},
		{"GeneratedNullableEqualField", GeneratedNullableEqualField[declarationUser, UUID], reflect.TypeOf(FieldID{})},
		{"GeneratedNullableOrderedField", GeneratedNullableOrderedField[declarationUser, Date], reflect.TypeOf(FieldID{})},
		{"GeneratedNullableTextField", GeneratedNullableTextField[declarationUser, string], reflect.TypeOf(FieldID{})},
		{"GeneratedNullableListField", GeneratedNullableListField[declarationUser, string], reflect.TypeOf(FieldID{})},
		{"GeneratedNullableBytesField", GeneratedNullableBytesField[declarationUser], reflect.TypeOf(FieldID{})},
		{"GeneratedNullableOpaqueField", GeneratedNullableOpaqueField[declarationUser, JSON[any]], reflect.TypeOf(FieldID{})},
		{"GeneratedToOne", GeneratedToOne[declarationUser, declarationUser], reflect.TypeOf(RelationID{})},
		{"GeneratedToMany", GeneratedToMany[declarationUser, declarationUser], reflect.TypeOf(RelationID{})},
	}
	for _, constructor := range constructors {
		functionType := reflect.TypeOf(constructor.fn)
		if functionType.NumIn() != 1 || functionType.In(0) != constructor.want {
			t.Fatalf("%s accepts %v; want exactly %v", constructor.name, functionType, constructor.want)
		}
	}
	modelConstructor := reflect.TypeOf(GeneratedModelDescriptor[declarationUser])
	if modelConstructor.NumIn() != 2 || modelConstructor.In(0) != reflect.TypeOf(ModelID{}) || modelConstructor.In(1) != reflect.TypeOf(GeneratedModelShape{}) {
		t.Fatalf("GeneratedModelDescriptor accepts %v; want typed model ID and generated shape", modelConstructor)
	}
}

func TestPolicyHandleCapabilitiesAndModelOwnership(t *testing.T) {
	var _ func(UUID) Predicate[declarationUser] = declarationUsers.ID.Eq
	var _ func(UUID) Predicate[declarationUser] = declarationUsers.ID.Ne
	var _ func(...UUID) Predicate[declarationUser] = declarationUsers.ID.In
	var _ func(...UUID) Predicate[declarationUser] = declarationUsers.ID.NotIn
	var _ func(int64) Predicate[declarationUser] = declarationUsers.Score.LT
	var _ func(int64) Predicate[declarationUser] = declarationUsers.Score.LTE
	var _ func(int64) Predicate[declarationUser] = declarationUsers.Score.GT
	var _ func(int64) Predicate[declarationUser] = declarationUsers.Score.GTE
	var _ func(string) Predicate[declarationUser] = declarationUsers.Title.Contains
	var _ func(string) Predicate[declarationUser] = declarationUsers.Title.StartsWith
	var _ func(string) Predicate[declarationUser] = declarationUsers.Title.EndsWith
	var _ func() Predicate[declarationUser] = declarationUsers.DeletedAt.IsNull
	var _ func() Predicate[declarationUser] = declarationUsers.DeletedAt.IsNotNull

	var _ func(string) Predicate[declarationUser] = declarationCapabilities.Aliases.Has
	var _ func(...string) Predicate[declarationUser] = declarationCapabilities.Aliases.HasEvery
	var _ func(...string) Predicate[declarationUser] = declarationCapabilities.Aliases.HasSome
	var _ func(bool) Predicate[declarationUser] = declarationCapabilities.Aliases.IsEmpty
	var _ func(List[string]) Predicate[declarationUser] = declarationCapabilities.Aliases.Eq
	var _ func([]byte) Predicate[declarationUser] = declarationCapabilities.Payload.Eq
	var _ func(...[]byte) Predicate[declarationUser] = declarationCapabilities.Payload.NotIn
	var _ func() Predicate[declarationUser] = declarationCapabilities.OptionalAliases.IsNull
	var _ func() Predicate[declarationUser] = declarationCapabilities.OptionalPayload.IsNotNull
	var _ func() Predicate[declarationUser] = declarationCapabilities.OptionalMetadata.IsNull

	toOne := GeneratedToOne[declarationUser, declarationUser](RelationID{0x01})
	toMany := GeneratedToMany[declarationUser, declarationUser](RelationID{0x02})
	var _ func(Predicate[declarationUser]) Predicate[declarationUser] = toOne.Is
	var _ func(Predicate[declarationUser]) Predicate[declarationUser] = toOne.IsNot
	var _ func() Predicate[declarationUser] = toOne.IsNull
	var _ func() Predicate[declarationUser] = toOne.IsNotNull
	var _ func(Predicate[declarationUser]) Predicate[declarationUser] = toMany.Some
	var _ func(Predicate[declarationUser]) Predicate[declarationUser] = toMany.Every
	var _ func(Predicate[declarationUser]) Predicate[declarationUser] = toMany.None

	var _ Predicate[declarationUser] = All[declarationUser]()
	var _ Predicate[declarationUser] = None[declarationUser]()
	var _ Predicate[declarationUser] = And(declarationUsers.ID.Eq(UUID{}), declarationUsers.Score.GTE(0))
	var _ Predicate[declarationUser] = Or[declarationUser]()
	var _ Predicate[declarationUser] = Not(declarationUsers.ID.Eq(UUID{}))
	var _ Predicate[declarationUser] = declarationUsers.ID.Eq(UUID{}).And(declarationUsers.Score.GTE(0)).Or().Not()
}

func TestEveryFieldHandlePreservesSchemaDSL(t *testing.T) {
	var _ ScalarColumn[declarationUser, UUID] = declarationUsers.ID
	var _ ScalarColumn[declarationUser, int64] = declarationUsers.Score
	var _ ScalarColumn[declarationUser, string] = declarationUsers.Title
	var _ ScalarColumn[declarationUser, Date] = declarationUsers.DeletedAt
	var _ ScalarColumn[declarationUser, List[string]] = declarationCapabilities.Aliases
	var _ ScalarColumn[declarationUser, []byte] = declarationCapabilities.Payload
	var _ ScalarColumn[declarationUser, JSON[any]] = declarationCapabilities.Metadata
	var _ ScalarColumn[declarationUser, JSON[any]] = declarationCapabilities.OptionalMetadata

	_ = Lower(declarationUsers.Title)
	_ = declarationUsers.Score.Expr().GTE(0)
	_ = declarationUsers.DeletedAt.Expr().IsNull()
	_ = IndexColumn(declarationCapabilities.Payload)
	_ = Generated(declarationUsers.SearchTitle, Lower(declarationUsers.Title), Stored)
}

func TestSchemaPredicatesAreDistinctFromPolicyPredicates(t *testing.T) {
	schemaPredicate := reflect.TypeOf(declarationUsers.Score.Expr().GTE(0))
	policyPredicate := reflect.TypeOf(declarationUsers.Score.GTE(0))
	if schemaPredicate == policyPredicate {
		t.Fatalf("schema predicate %v must be distinct from policy predicate %v", schemaPredicate, policyPredicate)
	}

	whereType := reflect.TypeOf(Index[declarationUser]("idx").Where)
	if whereType.In(0) != schemaPredicate {
		t.Fatalf("IndexSpec.Where accepts %v; want %v", whereType.In(0), schemaPredicate)
	}
}

func TestForProviderRequiresTypedProvider(t *testing.T) {
	functionType := reflect.TypeOf(ForProvider[declarationUser])
	providerType := reflect.TypeOf(Provider(""))
	if functionType.In(0) != providerType {
		t.Fatalf("ForProvider accepts %v; want %v", functionType.In(0), providerType)
	}
	for _, provider := range []Provider{SQLite, PostgreSQL} {
		_ = ForProvider[declarationUser](provider,
			Index[declarationUser]("typed").Keys(IndexColumn(declarationUsers.ID)),
		)
	}
}
