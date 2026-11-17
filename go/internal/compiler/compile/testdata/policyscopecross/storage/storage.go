package storage

import "github.com/eleven-am/golem/go/golem"

type Storage struct {
	_  struct{}   `golem:"model;table=storage"`
	ID golem.UUID `db:"id" golem:"pk"`
}

func (Storage) GolemModel() golem.ModelSpec[Storage] {
	return golem.DefineModel(golem.Subscriptions[Storage]())
}
