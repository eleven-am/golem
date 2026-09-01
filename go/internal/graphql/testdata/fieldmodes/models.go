package fieldmodes

import (
	golem "github.com/eleven-am/golem/go/golem"
)

type Account struct {
	_ struct{} `golem:"model;id=fieldmodes.Account;table=accounts;graphql=Account"`

	ID       golem.UUID `db:"id" golem:"id=fieldmodes.Account.ID;pk;default=uuid"`
	Handle   string     `db:"handle" golem:"type=varchar(40)"`
	Secret   string     `db:"secret" golem:"type=varchar(64);writeonly"`
	Email    string     `db:"email" golem:"type=varchar(255);hidden"`
	Sequence int64      `db:"sequence" golem:"readonly"`
}
