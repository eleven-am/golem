package policyscopecross

import (
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile/testdata/policyscopecross/storage"
)

type Actor struct{}
type ScopeArgs struct{ StorageIDs []golem.UUID }

type Access struct {
	_         struct{}         `golem:"model;table=accesses"`
	ID        golem.UUID       `db:"id" golem:"pk"`
	StorageID golem.UUID       `db:"storage_id"`
	Storage   *storage.Storage `db:"-" golem:"relation=belongs_to;fields=storage_id;references=id"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "imported_policy_scope")
	golem.Actor[Actor](schema)
	golem.Model[Access](schema)
	golem.Model[storage.Storage](schema)
	golem.Providers(schema, golem.SQLite)
	golem.WithoutGraphQL(schema)
}

func (Access) GolemModel() golem.ModelSpec[Access] {
	return golem.DefineModel(golem.Subscriptions(
		golem.ScopeTemplate("storage", func(args ScopeArgs) golem.Predicate[Access] {
			return Accesses.Storage.Is(storage.Storages.ID.In(args.StorageIDs...))
		}),
	))
}
