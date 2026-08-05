package basic

import g "github.com/eleven-am/golem/go/golem"

type User struct {
	ID    int64
	Age   int64
	Name  string
	Score int64
	Small int16
}

const adultAge int64 = 18

func (User) GolemModel() g.ModelSpec[User] {
	return g.DefineModel(
		g.PrimaryKey("pk_users", Users.ID),
		g.Unique("uq_users_name", Users.Name),
		g.Index[User]("idx_users_name_age").Keys(
			g.IndexColumn(Users.Name).Desc(),
			g.IndexExpr(g.Lower(Users.Name)),
			g.IndexExpr(g.Cast(Users.Small, g.Int16ToInt64)),
		).Where(Users.Age.Expr().GTE(adultAge)),
		g.Check("ck_users_age", Users.Age.Expr().GTE(0)),
		g.ForProvider(g.PostgreSQL,
			g.Generated(Users.Score, Users.Age.Expr().Add(g.SchemaValueOf[User](int64(1))), g.Stored),
		),
	)
}

// Audit deliberately has no GolemModel. Advanced declarations are optional.
type Audit struct {
	ID int64
}
