package objecttypesinvalid

import (
	"context"
	"github.com/eleven-am/golem/go/golem"
)

type User struct {
	ID   int64
	Name string
}
type Recursive struct{ Next *Recursive }
type Unbounded struct{ Values []string }
type Private struct{ value string }
type Unregistered struct{ Data Missing }
type Missing struct{ Value string }
type Args struct{}
type BadPresence struct {
	Pointer    golem.InputValue[*string]
	Nested     golem.InputValue[golem.InputValue[string]]
	Capability golem.InputValue[golem.Predicate[User]]
}

func (User) DefineGraphQL(schema *golem.GraphQLModel[User]) {
	golem.ComputedField(schema, "missing", golem.GraphQLOutput[Missing](), User{}.MissingOutput)
	golem.ComputedField(schema, "scalar", golem.GraphQLOutput[string](), User{}.ScalarOutput)
}

func (User) MissingOutput(context.Context, golem.Row[User], Args) (Missing, error) {
	return Missing{}, nil
}
func (User) ScalarOutput(context.Context, golem.Row[User], Args) (string, error) { return "", nil }

func DefineGraphQL(schema *golem.GraphQLSchema) {
	golem.Output[string](schema, "NotAStruct")
	golem.Output[Recursive](schema, "Recursive")
	golem.Output[Unbounded](schema, "Unbounded")
	golem.Output[Private](schema, "Private")
	golem.Output[Unregistered](schema, "Unregistered")
	golem.Input[BadPresence](schema, "BadPresence")
	golem.Input[Missing](schema, "DuplicateChoice", golem.InputOneOf(), golem.InputOneOf())
}
