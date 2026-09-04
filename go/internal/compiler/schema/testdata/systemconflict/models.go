package systemconflict

import declaration "github.com/eleven-am/golem/go/golem"

type Article struct {
	_ struct{} `golem:"model;table=articles"`

	ID       declaration.UUID `db:"id" golem:"pk;default=uuid"`
	TagCount int32            `db:"tag_count" golem:"system;readonly"`
	Author   *Article         `db:"-" golem:"relation=belongs_to;fields=author_id;references=id;system"`
}
