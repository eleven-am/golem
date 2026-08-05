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
	ID          ScalarField[declarationUser, UUID]
	Score       ScalarField[declarationUser, int64]
	Title       ScalarField[declarationUser, string]
	SearchTitle ScalarField[declarationUser, string]
	DeletedAt   ScalarField[declarationUser, Date]
}{
	ID:          GeneratedScalarField[declarationUser, UUID](FieldID{0x01}),
	Score:       GeneratedScalarField[declarationUser, int64](FieldID{0x02}),
	Title:       GeneratedScalarField[declarationUser, string](FieldID{0x03}),
	SearchTitle: GeneratedScalarField[declarationUser, string](FieldID{0x04}),
	DeletedAt:   GeneratedScalarField[declarationUser, Date](FieldID{0x05}),
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
	for _, value := range []any{ModelID{}, FieldID{}, RelationID{}} {
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
		{"GeneratedScalarField", GeneratedScalarField[declarationUser, UUID], reflect.TypeOf(FieldID{})},
		{"GeneratedModelDescriptor", GeneratedModelDescriptor[declarationUser], reflect.TypeOf(ModelID{})},
		{"GeneratedToOne", GeneratedToOne[declarationUser, declarationUser], reflect.TypeOf(RelationID{})},
		{"GeneratedToMany", GeneratedToMany[declarationUser, declarationUser], reflect.TypeOf(RelationID{})},
	}
	for _, constructor := range constructors {
		functionType := reflect.TypeOf(constructor.fn)
		if functionType.NumIn() != 1 || functionType.In(0) != constructor.want {
			t.Fatalf("%s accepts %v; want exactly %v", constructor.name, functionType, constructor.want)
		}
	}
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
