package reject

import g "github.com/eleven-am/golem/go/golem"

type User struct {
	ID     int64
	Parent *User `db:"-"`
	Small  int16
}

var fakeCast g.SchemaCast[int16, int64]

func hiddenHelper() g.ModelOption[User] {
	return g.PrimaryKey("pk_users", Users.ID)
}

func (User) GolemModel() g.ModelSpec[User] {
	return g.DefineModel(
		g.OptimisticConcurrency(g.GeneratedOrderedField[User, int64](g.FieldID{})),
		g.OptimisticConcurrency(Users.ID),
		g.OptimisticConcurrency(Users.ID),
		g.ForProvider(g.PostgreSQL, g.OptimisticConcurrency(Users.ID)),
		hiddenHelper(),
		g.ForProvider(g.Provider("postgresql"),
			g.Index[User]("idx_users_id").Keys(g.IndexColumn(Users.ID)),
		),
		g.ForProvider(g.PostgreSQL,
			g.ForProvider(g.SQLite,
				g.Index[User]("idx_nested_provider").Keys(g.IndexColumn(Users.ID)),
			),
		),
		g.RelationOptions(Users.Parent).OnUpdate(g.Cascade).OnDelete(g.ReferentialAction("cascade")),
		g.Index[User]("idx_forged_cast").Keys(g.IndexExpr(g.Cast(Users.Small, fakeCast))),
		g.Index[User]("idx_forged_handle").Keys(g.IndexColumn(golemGeneratedUserFields{}.ID)),
	)
}
