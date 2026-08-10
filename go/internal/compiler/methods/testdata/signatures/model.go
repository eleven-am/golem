package signatures

import g "github.com/eleven-am/golem/go/golem"

type Pointer struct{ ID int64 }

func (*Pointer) GolemModel() g.ModelSpec[Pointer] {
	return g.DefineModel(g.PrimaryKey("pk_pointer", Pointers.ID))
}

type WrongResult struct{ ID int64 }
type Other struct{ ID int64 }

func (WrongResult) GolemModel() g.ModelSpec[Other] {
	return g.DefineModel(g.PrimaryKey("pk_other", Others.ID))
}
