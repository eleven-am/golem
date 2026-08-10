package capabilityinvalid

import g "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }
type Payload struct{ Label string }
type Status string

type JSONFailure struct{ Metadata g.JSON[Payload] }
type BoolFailure struct{ Published bool }
type EnumFailure struct{ Status Status }
type NonNullFailure struct{ ID int64 }
type RelationFailure struct{ Owner *Other }
type CrossFailure struct{ ID int64 }
type Other struct{ ID int64 }

func (JSONFailure) DefinePolicy(rules *g.Rules[JSONFailure], _ Actor) {
	rules.CanRead(JSONFailures.Metadata.GTE(g.JSON[Payload]{}))
}

func (BoolFailure) DefinePolicy(rules *g.Rules[BoolFailure], _ Actor) {
	rules.CanRead(BoolFailures.Published.GT(true))
}

func (EnumFailure) DefinePolicy(rules *g.Rules[EnumFailure], _ Actor) {
	rules.CanRead(EnumFailures.Status.Contains(Status("live")))
}

func (NonNullFailure) DefinePolicy(rules *g.Rules[NonNullFailure], _ Actor) {
	rules.CanRead(NonNullFailures.ID.IsNull())
}

func (RelationFailure) DefinePolicy(rules *g.Rules[RelationFailure], _ Actor) {
	rules.CanRead(RelationFailures.Owner.Some(g.All[Other]()))
}

func (CrossFailure) DefinePolicy(rules *g.Rules[CrossFailure], actor Actor) {
	rules.CanRead(Others.ID.Eq(actor.ID))
}

func (Other) DefinePolicy(rules *g.Rules[Other], _ Actor) {
	rules.CanRead(g.All[Other]())
}
