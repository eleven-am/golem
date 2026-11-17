package continuationpages

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync/atomic"

	"github.com/eleven-am/golem/go/golem"
)

type Principal string
type Actor struct {
	Profile  string
	Revision int32
}

type Entry struct {
	_       struct{}   `golem:"model;table=entries"`
	ID      golem.UUID `db:"id" golem:"pk"`
	Ordinal int32      `db:"ordinal"`
	Rank    int32      `db:"rank"`
	Even    bool       `db:"even"`
	Visible bool       `db:"visible"`
	Title   string     `db:"title"`
}

func DefineSchema(schema *golem.Schema) {
	golem.WithDataIncarnation(schema)
	golem.SchemaName(schema, "continuation_pages")
	golem.Actor[Actor](schema)
	golem.Model[Entry](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func (Entry) DefinePolicy(rules *golem.Rules[Entry], actor Actor) {
	predicate := Entries.Visible.Eq(true)
	switch actor.Profile {
	case "full", "privateID":
	case "even":
		predicate = predicate.And(Entries.Even.Eq(true))
	case "tail":
		predicate = predicate.And(Entries.Ordinal.GT(25))
	default:
		predicate = predicate.And(Entries.Ordinal.GT(1000))
	}
	if actor.Profile == "full" {
		rules.CanCreate(golem.All[Entry]())
	}
	rules.CanRead(predicate)
	if actor.Profile == "privateID" {
		rules.CannotReadFields(golem.All[Entry](), Entries.ID)
	}
}

type FeedArgs struct {
	Seed int32
	Mode string
}
type RankedItem struct {
	Ordinal int32
	Tie     int32
	Entry   golem.Row[Entry]
}

var ResolverCalls atomic.Int64
var LargestCandidateWindow atomic.Uint32
var ReadHookCalls atomic.Int64

// A no-op application hook still passes through the typed public-options
// roundtrip. Runtime-owned hydration restrictions must survive that boundary.
func (Entry) BeforeFindMany(_ context.Context, _ *EntryFindManyRequest) error {
	ReadHookCalls.Add(1)
	return nil
}

// Populated once from real System reads by the consumer before requests start.
// These sealed rows witness candidate identities; their old cells are never
// trusted as response cells. IdentityRows has no System or database handle.
var CandidateWitnesses [60]golem.Row[Entry]

func DefineGraphQL(schema *golem.GraphQLSchema) {
	golem.Output[RankedItem](schema, "RankedItem", golem.ObjectRowGate("Entry"))
	golem.PageQuery(schema, "rankedFeed", RankedFeed, golem.PageLimits(25, 50, 50), golem.PageRevision("immutable-sixty-v1"))
	golem.PageQuery(schema, "rankedRows", RankedRows, golem.PageLimits(25, 50, 50), golem.PageRevision("immutable-sixty-v1"))
	golem.PageQuery(schema, "identityRows", IdentityRows, golem.PageLimits(25, 50, 50), golem.PageRevision("immutable-sixty-v1"))
}

// This fixture's snapshot is the immutable sequence of IDs 1..60, ordered by
// (floor((ordinal-1)/5), UUID). Database Rank changes and newly inserted rows
// cannot change it. It is deliberately not a durable ranked snapshot store;
// independently initialized replacement databases carry a different durable
// data incarnation; restoring or rotating that identity is outside this fixture.
func CandidateID(ordinal uint32) golem.UUID {
	var id golem.UUID
	binary.BigEndian.PutUint32(id[12:], ordinal)
	return id
}

func candidateWindow(ctx context.Context, request golem.PageRequest[FeedArgs], refill bool) ([]golem.UUID, []byte, error) {
	ResolverCalls.Add(1)
	actor := golem.ActorFrom[Actor](ctx)
	if actor.Profile == "" || actor.Profile == "forged" {
		return nil, nil, fmt.Errorf("page resolver lacks admitted actor")
	}
	const snapshot uint32 = 1
	ordinal := request.Consumed()
	state := request.State()
	if len(state) != 0 {
		if len(state) != 12 || binary.BigEndian.Uint32(state[:4]) != snapshot {
			return nil, nil, fmt.Errorf("unknown immutable candidate snapshot")
		}
		if binary.BigEndian.Uint64(state[4:]) != ordinal {
			return nil, nil, fmt.Errorf("checkpoint does not match consumed candidates")
		}
	}
	if len(state) == 0 && ordinal != 0 || ordinal > 60 {
		return nil, nil, fmt.Errorf("checkpoint does not match consumed candidates")
	}
	limit := request.Take()
	if refill {
		limit = request.CandidateLimit()
	}
	count := min(uint64(limit), 60-ordinal)
	if count > uint64(request.CandidateLimit()) {
		return nil, nil, fmt.Errorf("candidate window exceeds admitted budget")
	}
	for old := LargestCandidateWindow.Load(); uint32(count) > old && !LargestCandidateWindow.CompareAndSwap(old, uint32(count)); old = LargestCandidateWindow.Load() {
	}
	ids := make([]golem.UUID, count)
	for index := range ids {
		ids[index] = CandidateID(uint32(ordinal) + uint32(index) + 1)
	}
	return ids, candidateCheckpoint(ordinal + count), nil
}

func candidateCheckpoint(ordinal uint64) []byte {
	if ordinal >= 60 {
		return nil
	}
	next := make([]byte, 12)
	binary.BigEndian.PutUint32(next[:4], 1)
	binary.BigEndian.PutUint64(next[4:], ordinal)
	return next
}

func rankedCandidates(ctx context.Context, caller *Caller[Principal], request golem.PageRequest[FeedArgs]) ([]RankedItem, uint32, []byte, error) {
	ids, _, err := candidateWindow(ctx, request, request.Arguments().Mode == "refill")
	if err != nil {
		return nil, 0, nil, err
	}
	ordinal, count := request.Consumed(), len(ids)
	var rows []golem.Row[Entry]
	if count != 0 {
		var err error
		rows, err = caller.Entries.FindMany(ctx, golem.Where(Entries.ID.In(ids...)), golem.Take[Entry](int(count)))
		if err != nil {
			return nil, 0, nil, err
		}
	}
	visible := make(map[golem.UUID]golem.Row[Entry], len(rows))
	for _, row := range rows {
		id, present := golem.Value(row, Entries.ID).Get()
		if !present {
			return nil, 0, nil, fmt.Errorf("candidate identity was not selected")
		}
		visible[id] = row
	}
	items := make([]RankedItem, 0, min(len(rows), int(request.Take())))
	consumed := uint32(0)
	for index, id := range ids {
		consumed++
		if row, present := visible[id]; present {
			position := int32(ordinal) + int32(index) + 1
			items = append(items, RankedItem{Ordinal: position, Tie: (position - 1) / 5, Entry: row})
		}
		if len(items) == int(request.Take()) {
			break
		}
	}
	next := candidateCheckpoint(ordinal + uint64(consumed))
	switch request.Arguments().Mode {
	case "stuck":
		if ordinal != 0 {
			next = request.State()
		}
	case "oversized":
		if len(items) != 0 {
			for len(items) <= int(request.Take()) {
				items = append(items, items[0])
			}
		}
	}
	return items, consumed, next, nil
}

func RankedFeed(ctx context.Context, caller *Caller[Principal], request golem.PageRequest[FeedArgs]) (golem.PageBatch[RankedItem], error) {
	items, consumed, next, err := rankedCandidates(ctx, caller, request)
	return golem.PageItems(items, consumed, next), err
}

func RankedRows(ctx context.Context, caller *Caller[Principal], request golem.PageRequest[FeedArgs]) (golem.PageBatch[golem.Row[Entry]], error) {
	items, consumed, next, err := rankedCandidates(ctx, caller, request)
	rows := make([]golem.Row[Entry], len(items))
	for index := range items {
		rows[index] = items[index].Entry
	}
	return golem.PageItems(rows, consumed, next), err
}

func IdentityRows(ctx context.Context, _ *Caller[Principal], request golem.PageRequest[FeedArgs]) (golem.PageBatch[golem.Row[Entry]], error) {
	ids, next, err := candidateWindow(ctx, request, false)
	if err != nil {
		return golem.PageBatch[golem.Row[Entry]]{}, err
	}
	rows := make([]golem.Row[Entry], len(ids))
	for index := range rows {
		rows[index] = CandidateWitnesses[int(request.Consumed())+index]
	}
	return golem.PageItems(rows, uint32(len(ids)), next), nil
}
