package models

import "github.com/eleven-am/golem/go/golem"

type User struct {
	_  struct{} `golem:"model;id=pipeline.InvalidUser;table=users"`
	ID int64    `db:"id" golem:"id=pipeline.InvalidUser.ID;pk"`
}

// The recognized method deliberately uses the wrong actor type.
func (User) DefinePolicy(_ *golem.Rules[User], _ string) {}
