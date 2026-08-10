package invalid

import (
	"context"

	g "github.com/eleven-am/golem/go/golem"
)

type Actor struct{ ID int64 }
type User struct{ ID int64 }

func (*User) DefinePolicy(_ *g.Rules[User], _ Actor)                   {}
func (User) BeforeCreate(_ context.Context, _ UserCreateRequest) error { return nil }
func (User) BeforeUpsert()                                             {}
