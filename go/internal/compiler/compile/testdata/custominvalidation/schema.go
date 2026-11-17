package custominvalidation

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync/atomic"

	"github.com/eleven-am/golem/go/golem"
)

type Principal string
type Actor struct {
	AccountID     golem.UUID
	Revision      int32
	AllowProgress bool
}
type Args struct{ Category string }
type AccountArgs struct{ AccountID golem.UUID }
type EmptyArgs struct{}

type State struct {
	_         struct{}   `golem:"model;table=feed_states"`
	ID        golem.UUID `db:"id" golem:"pk"`
	AccountID golem.UUID `db:"account_id" golem:"unique"`
	Revision  int32      `db:"revision"`
}

type Progress struct {
	_         struct{}   `golem:"model;table=progress"`
	ID        golem.UUID `db:"id" golem:"pk"`
	AccountID golem.UUID `db:"account_id"`
	Category  string     `db:"category"`
	Position  int32      `db:"position"`
	Noise     string     `db:"noise"`
}

type Owner struct {
	_         struct{}   `golem:"model;table=fact_owners"`
	ID        golem.UUID `db:"id" golem:"pk"`
	AccountID golem.UUID `db:"account_id"`
	Ready     bool       `db:"ready"`
}

func DefineSchema(schema *golem.Schema) {
	golem.WithDataIncarnation(schema)
	golem.SchemaName(schema, "custom_invalidation")
	golem.Actor[Actor](schema)
	golem.Model[State](schema)
	golem.Model[Progress](schema)
	golem.Model[Owner](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (State) DefinePolicy(rules *golem.Rules[State], actor Actor) {
	rules.CanRead(States.AccountID.Eq(actor.AccountID))
}

func (Progress) GolemModel() golem.ModelSpec[Progress] {
	return golem.DefineModel(golem.Subscriptions[Progress](
		golem.DeleteSnapshot(Progresses.AccountID, Progresses.Category, Progresses.Position),
	))
}
func (Progress) DefinePolicy(rules *golem.Rules[Progress], actor Actor) {
	if actor.AllowProgress {
		rules.CanRead(Progresses.AccountID.Eq(actor.AccountID))
	} else {
		rules.CanRead(golem.None[Progress]())
	}
	rules.CannotReadFields(golem.All[Progress](), Progresses.ID)
}

func (Owner) GolemModel() golem.ModelSpec[Owner] {
	return golem.DefineModel(golem.Subscriptions[Owner](
		golem.ScopeTemplate("account", func(args AccountArgs) golem.Predicate[Owner] {
			return Owners.AccountID.Eq(args.AccountID)
		}),
		golem.FactTemplate("ready", "account", func(args EmptyArgs) golem.Predicate[Owner] {
			return Owners.Ready.Eq(true)
		}),
	))
}
func (Owner) DefinePolicy(rules *golem.Rules[Owner], actor Actor) {
	rules.BindFactScope(OwnerScopes.Account(AccountArgs{AccountID: actor.AccountID}))
	_ = rules.BindFact(OwnerFacts.Ready(EmptyArgs{}))
	// The named fact grants scoped observation, never a readable Owner row.
	rules.CanRead(golem.None[Owner]())
}

type Segment struct {
	Index    int32
	Revision int32
}

var ResolverCalls atomic.Int64
var BinderCalls atomic.Int64

func BindProgress(ctx context.Context, actor Actor, args Args) (golem.Predicate[Progress], error) {
	BinderCalls.Add(1)
	if golem.ActorFrom[Actor](ctx) != actor {
		return golem.Predicate[Progress]{}, fmt.Errorf("invalidation binder lacks admitted actor")
	}
	return Progresses.AccountID.Eq(actor.AccountID).And(Progresses.Category.Eq(args.Category)), nil
}

func BindOwner(ctx context.Context, actor Actor, args Args) (golem.BoundFactScope[Owner], error) {
	BinderCalls.Add(1)
	if golem.ActorFrom[Actor](ctx) != actor {
		return golem.BoundFactScope[Owner]{}, fmt.Errorf("fact binder lacks admitted actor")
	}
	return OwnerScopes.Account(AccountArgs{AccountID: actor.AccountID}), nil
}

func DefineGraphQL(schema *golem.GraphQLSchema) {
	golem.Output[Segment](schema, "Segment")
	golem.Query(schema, "progressState", ProgressState,
		golem.InvalidateRows[Progress]("progress", BindProgress, Progresses.Position))
	golem.Query(schema, "factState", FactState,
		golem.InvalidateFacts[Owner]("availability", "account", BindOwner, "ready"))
	golem.Query(schema, "staticState", StaticState)
	golem.PageQuery(schema, "feed", Feed,
		golem.PageLimits(25, 25, 50), golem.PageRevision("immutable-sixty-v1"),
		golem.InvalidateRows[Progress]("progress", BindProgress, Progresses.Position),
		golem.InvalidateFacts[Owner]("availability", "account", BindOwner, "ready"))
}

func readSegment(ctx context.Context, caller *Caller[Principal]) (Segment, error) {
	ResolverCalls.Add(1)
	actor := golem.ActorFrom[Actor](ctx)
	rows, err := caller.States.FindMany(ctx, golem.Where(States.AccountID.Eq(actor.AccountID)), golem.Select[State](States.Revision), golem.Take[State](1))
	if err != nil {
		return Segment{}, err
	}
	if len(rows) != 1 {
		return Segment{}, fmt.Errorf("fixture state is absent")
	}
	revision, present := golem.Value(rows[0], States.Revision).Get()
	if !present {
		return Segment{}, fmt.Errorf("fixture state revision was not selected")
	}
	return Segment{Revision: revision}, nil
}

func ProgressState(ctx context.Context, caller *Caller[Principal], args Args) (Segment, error) {
	return readSegment(ctx, caller)
}
func FactState(ctx context.Context, caller *Caller[Principal], args Args) (Segment, error) {
	return readSegment(ctx, caller)
}
func StaticState(ctx context.Context, caller *Caller[Principal], args Args) (Segment, error) {
	return readSegment(ctx, caller)
}

func Feed(ctx context.Context, caller *Caller[Principal], request golem.PageRequest[Args]) (golem.PageBatch[Segment], error) {
	segment, err := readSegment(ctx, caller)
	if err != nil {
		return golem.PageBatch[Segment]{}, err
	}
	ordinal := request.Consumed()
	state := request.State()
	if ordinal > 60 || ordinal == 0 && len(state) != 0 || ordinal != 0 && (len(state) != 8 || binary.BigEndian.Uint64(state) != ordinal) {
		return golem.PageBatch[Segment]{}, fmt.Errorf("invalid immutable candidate checkpoint")
	}
	count := min(uint64(request.Take()), 60-ordinal)
	items := make([]Segment, count)
	for index := range items {
		items[index] = Segment{Index: int32(ordinal) + int32(index) + 1, Revision: segment.Revision}
	}
	var next []byte
	if ordinal+count < 60 {
		next = make([]byte, 8)
		binary.BigEndian.PutUint64(next, ordinal+count)
	}
	return golem.PageItems(items, uint32(count), next), nil
}
