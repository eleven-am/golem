package extensionsinvalid

import (
	"context"

	g "github.com/eleven-am/golem/go/golem"
	"github.com/jmoiron/sqlx"
)

type Principal struct{}
type Caller[P any] struct{}
type CallerTx[P any] struct{}
type System[P any] struct{}
type Unknown struct{ Value string }

type User struct {
	ID   int64
	Name string
}

type Args struct {
	Prefix string `golem:"graphql=prefix"`
}

type RawArgs struct {
	DB *sqlx.DB `golem:"graphql=db"`
}

type TxArgs struct {
	Tx *sqlx.Tx `golem:"graphql=tx"`
}

type UnknownArgs struct {
	Value Unknown `golem:"graphql=value"`
}

func (User) DefineGraphQL(graphql *g.GraphQLModel[User]) {
	g.ComputedField(graphql, "wrong", g.GraphQLString(), User{}.Wrong, g.Requires(Users.Name))
	g.BatchedComputedField(graphql, "wrongBatch", g.GraphQLString(), Users.ID, WrongBatch, 16, g.Requires(Users.Name))
}

func (User) Wrong(context.Context, g.Row[User], Args) (int64, error) {
	return 0, nil
}

func WrongBatch(context.Context, []string, Args) (map[string]string, error) {
	return nil, nil
}

func DefineGraphQL(graphql *g.GraphQLSchema) {
	g.Query(graphql, "unsafeSystem", UnsafeSystem)
	g.Query(graphql, "unsafeRaw", UnsafeRaw)
	g.Query(graphql, "unsafeTx", UnsafeTx)
	g.Query(graphql, "unsafeTxArgument", UnsafeTxArgument)
	g.Query(graphql, "unsafeUnknown", UnsafeUnknown)
	g.Query(graphql, "users", UsersRootCollision)
}

func UnsafeSystem(context.Context, *System[Principal], Args) (string, error) {
	return "", nil
}

func UnsafeRaw(context.Context, *Caller[Principal], RawArgs) (string, error) {
	return "", nil
}

func UnsafeTx(context.Context, *CallerTx[Principal], Args) (string, error) {
	return "", nil
}

func UnsafeTxArgument(context.Context, *Caller[Principal], TxArgs) (string, error) {
	return "", nil
}

func UnsafeUnknown(context.Context, *Caller[Principal], UnknownArgs) (string, error) {
	return "", nil
}

func UsersRootCollision(context.Context, *Caller[Principal], Args) (string, error) {
	return "", nil
}
