package social

import (
	clock "time"

	declaration "github.com/eleven-am/golem/go/golem"
)

type Profile struct {
	DisplayName string
}

type Visibility string

const (
	VisibilityPublic  Visibility = "PUBLIC"
	VisibilityPrivate Visibility = "PRIVATE"
)

func (Visibility) GolemEnum() declaration.EnumSpec[Visibility] {
	return declaration.DefineEnum(
		declaration.EnumValue(VisibilityPublic),
		declaration.EnumValue(VisibilityPrivate),
	)
}

type User struct {
	_ struct{} `golem:"model;id=social.User;table=users;graphql=Person"`

	ID         declaration.UUID          `db:"id" golem:"id=social.User.ID;pk;default=uuid"`
	Nickname   *string                   `db:"nickname"`
	Biography  declaration.Null[string]  `db:"biography"`
	Metadata   declaration.JSON[Profile] `db:"metadata" golem:"type=json"`
	Scores     declaration.List[int32]   `db:"scores"`
	Visibility Visibility                `db:"visibility" golem:"default=PUBLIC"`
	UpdatedAt  clock.Time                `db:"updated_at" golem:"default=now;updated;readonly"`
	Secret     string                    `db:"secret" golem:"writeonly;immutable;graphql=secretValue"`
	Manager    *User                     `db:"-" golem:"relation=belongs_to;fields=manager_id;references=id"`
}

type Audit struct {
	_ struct{} `golem:"model;table=audits"`

	ID      int64  `db:"id" golem:"default=identity"`
	Message string `db:"message" golem:"type=varchar(200)"`
}
