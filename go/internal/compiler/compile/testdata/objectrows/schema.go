package objectrows

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
)

type Actor struct{ ID string }
type Movie struct {
	_     struct{}   `golem:"model;id=objectrows.Movie;table=movies"`
	ID    golem.UUID `db:"id" golem:"pk"`
	Owner string     `db:"owner"`
	Title string     `db:"title"`
}
type Episode struct {
	_     struct{}   `golem:"model;id=objectrows.Episode;table=episodes"`
	ID    golem.UUID `db:"id" golem:"pk"`
	Owner string     `db:"owner"`
	Name  string     `db:"name"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "object_rows")
	golem.Actor[Actor](schema)
	golem.Model[Movie](schema)
	golem.Model[Episode](schema)
	golem.Providers(schema, golem.SQLite)
}
func (Movie) DefinePolicy(rules *golem.Rules[Movie], actor Actor) {
	rules.CanRead(Movies.Owner.Eq(actor.ID))
	rules.CanUpdate(Movies.Owner.Eq(actor.ID))
}
func (Episode) DefinePolicy(rules *golem.Rules[Episode], actor Actor) {
	rules.CanRead(Episodes.Owner.Eq(actor.ID))
}

type ContinueWatchingItem struct {
	Playhead int64
	Media    golem.Row[Movie]
	Episode  *golem.Row[Episode]
}
type RequiredItem struct{ Episode golem.Row[Episode] }
type Args struct{}
type TouchArgs struct{ ID golem.UUID }
type NarrowInput struct{ Value int16 }
type NarrowArgs struct{ Input NarrowInput }
type ComputedMetadata struct{ Label string }

var Results []ContinueWatchingItem
var RequiredResult RequiredItem

func DefineGraphQL(schema *golem.GraphQLSchema) {
	golem.Output[ContinueWatchingItem](schema, "ContinueWatchingItem", golem.ObjectRowGate("Media"))
	golem.Output[RequiredItem](schema, "RequiredItem")
	golem.Output[ComputedMetadata](schema, "ComputedMetadata")
	golem.Input[NarrowInput](schema, "NarrowInput")
	golem.Query(schema, "watching", Watching, golem.MaxItems(10))
	golem.Query(schema, "required", Required)
	golem.Query(schema, "narrow", Narrow)
	golem.Mutation(schema, "touch", Touch)
}
func Watching(context.Context, *Caller[string], Args) ([]ContinueWatchingItem, error) {
	return Results, nil
}
func Required(context.Context, *Caller[string], Args) (*RequiredItem, error) {
	return &RequiredResult, nil
}

func Narrow(_ context.Context, _ *Caller[string], args NarrowArgs) (int32, error) {
	return int32(args.Input.Value), nil
}

func (Movie) DefineGraphQL(schema *golem.GraphQLModel[Movie]) {
	golem.ComputedField(schema, "metadata", golem.GraphQLOutput[*ComputedMetadata]().NonNull(), Movie{}.Metadata, golem.Requires(Movies.Title))
	golem.ComputedField(schema, "optionalMetadata", golem.GraphQLOutput[*ComputedMetadata](), Movie{}.OptionalMetadata)
	golem.ComputedField(schema, "watching", golem.GraphQLList(golem.GraphQLOutput[ContinueWatchingItem]().NonNull(), 10).NonNull(), Movie{}.Watching)
	golem.BatchedComputedField(schema, "batchWatching", golem.GraphQLList(golem.GraphQLOutput[ContinueWatchingItem]().NonNull(), 10).NonNull(), Movies.ID, LoadWatching, 4)
}

func (Movie) Metadata(_ context.Context, row golem.Row[Movie], args NarrowArgs) (*ComputedMetadata, error) {
	title, _ := golem.Value(row, Movies.Title).Get()
	return &ComputedMetadata{Label: fmt.Sprintf("%s:%d", title, args.Input.Value)}, nil
}

func (Movie) OptionalMetadata(context.Context, golem.Row[Movie], Args) (*ComputedMetadata, error) {
	return nil, nil
}

func (Movie) Watching(context.Context, golem.Row[Movie], Args) ([]ContinueWatchingItem, error) {
	return Results, nil
}

func LoadWatching(_ context.Context, keys []golem.UUID, _ Args) (map[golem.UUID][]ContinueWatchingItem, error) {
	result := make(map[golem.UUID][]ContinueWatchingItem, len(keys))
	for _, key := range keys {
		result[key] = Results
	}
	return result, nil
}

func Touch(ctx context.Context, caller *Caller[string], args TouchArgs) (*RequiredItem, error) {
	if _, err := caller.Movies.Update(ctx, Movies.ByID.Value(args.ID), Movies.Update(Movies.Title.Set("committed"))); err != nil {
		return nil, err
	}
	return &RequiredResult, nil
}
