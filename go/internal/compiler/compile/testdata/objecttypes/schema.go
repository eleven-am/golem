package objecttypes

import (
	"context"
	"github.com/eleven-am/golem/go/golem"
)

type Actor struct{}
type Principal struct{}
type User struct {
	_    struct{}   `golem:"model;id=object.User;table=users"`
	ID   golem.UUID `db:"id" golem:"id=object.User.ID;pk;default=uuid"`
	Name string     `db:"name"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "object_types")
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, golem.SQLite)
}
func (User) DefinePolicy(rules *golem.Rules[User], _ Actor) { rules.CanRead(golem.All[User]()) }

func (User) DefineGraphQL(schema *golem.GraphQLModel[User]) {
	golem.ComputedField(schema, "metadata", golem.GraphQLOutput[TransportMetadata]().NonNull(), User{}.Metadata)
	golem.ComputedField(schema, "inputState", golem.GraphQLOutput[InputView]().NonNull(), User{}.InputState)
	golem.BatchedComputedField(schema, "batchInputState", golem.GraphQLOutput[InputView]().NonNull(), Users.ID, LoadInputStates, 8)
}

func (User) Metadata(ctx context.Context, _ golem.Row[User], args MetadataArgs) (TransportMetadata, error) {
	return Metadata(ctx, nil, args)
}

type HomeFeedPage struct {
	Page         int32
	TotalPages   int32
	TotalResults int32
	Exhausted    bool
	Results      []HomeSegment
}

type HomeSegmentKind string

const (
	HomeSegmentBasicPick      HomeSegmentKind = "basic-pick-wire"
	HomeSegmentEditorPick     HomeSegmentKind = "editor-pick-wire"
	HomeSegmentRecommendation HomeSegmentKind = "recommendation-wire"
)

func (HomeSegmentKind) GolemEnum() golem.EnumSpec[HomeSegmentKind] {
	return golem.DefineEnum(
		golem.EnumValue(HomeSegmentBasicPick, golem.GraphQLValue("BASIC_PICK")),
		golem.EnumValue(HomeSegmentEditorPick, golem.GraphQLValue("EDITOR_PICK")),
		golem.EnumValue(HomeSegmentRecommendation, golem.GraphQLValue("RECOMMENDATION")),
	)
}

type HomeSegment struct {
	Kind  HomeSegmentKind
	Index int32
}
type HomeInput struct{ Range *PageRange }
type PageRange struct{ Seed int32 }
type HomeArgs struct{ Input HomeInput }
type MetadataArgs struct{}
type HomeListArgs struct{ Take int32 }

type TransportMetadata struct {
	ID      string
	Version int64 `golem:"graphql=_version"`
	Maybe   *HomeSegment
	Items   []*HomeSegment
	Grid    [][]HomeSegment
}

func DefineGraphQL(schema *golem.GraphQLSchema) {
	golem.Query(schema, "homeFeed", HomeFeed)
	golem.Output[HomeFeedPage](schema, "HomeFeedPage", golem.ObjectFieldMaxItems("Results", 25))
	golem.Output[HomeSegment](schema, "HomeSegment")
	golem.Input[HomeInput](schema, "HomeInput")
	golem.Input[PageRange](schema, "PageRange")
	golem.Output[TransportMetadata](schema, "TransportMetadata", golem.ObjectFieldMaxItems("Items", 2), golem.ObjectFieldMaxItems("Grid", 2, 3))
	golem.Query(schema, "transportMetadata", Metadata)
	golem.Query(schema, "homeFeedList", HomeFeedList, golem.MaxItems(2))
	golem.Input[PatchInput](schema, "PatchInput")
	golem.Input[PatchChild](schema, "PatchChild")
	golem.Input[PlaybackTarget](schema, "PlaybackTarget", golem.InputOneOf())
	golem.Output[InputView](schema, "InputView", golem.ObjectFieldMaxItems("ValueStates", 4))
	golem.Query(schema, "inputState", InputState)
	golem.Mutation(schema, "choosePlayback", ChoosePlayback)
}

func Metadata(context.Context, *Caller[Principal], MetadataArgs) (TransportMetadata, error) {
	segment := HomeSegment{Kind: HomeSegmentBasicPick, Index: 4}
	return TransportMetadata{ID: "opaque-id", Version: 9007199254740993, Items: []*HomeSegment{nil, &segment}, Grid: [][]HomeSegment{{segment}, {}}}, nil
}

// Take is application-owned and intentionally does not narrow MaxItems. The
// HTTP acceptance test verifies that cost accounting uses the declared bound.
func HomeFeedList(ctx context.Context, caller *Caller[Principal], _ HomeListArgs) ([]HomeFeedPage, error) {
	page, err := HomeFeed(ctx, caller, HomeArgs{})
	return []HomeFeedPage{page, page}, err
}
func HomeFeed(_ context.Context, _ *Caller[Principal], args HomeArgs) (HomeFeedPage, error) {
	var seed int32
	if args.Input.Range != nil {
		seed = args.Input.Range.Seed
	}
	_ = seed // The generated HTTP acceptance probe observes the nested input.
	// Frames' exhausted page with one basic pick uses the one-based index 1.
	return HomeFeedPage{Page: 1, TotalPages: 1, TotalResults: 1, Exhausted: true, Results: []HomeSegment{{Kind: HomeSegmentBasicPick, Index: 1}}}, nil
}

type PatchChild struct{ Note golem.InputValue[string] }
type PatchInput struct {
	Note     golem.InputValue[string]
	Count    golem.InputValue[int16]
	Child    golem.InputValue[PatchChild]
	Values   golem.InputValue[[]golem.InputValue[string]]
	Document golem.InputValue[golem.JSON[any]]
}
type InputStateArgs struct {
	Patch PatchInput
	Tag   golem.InputValue[string]
}
type InputView struct {
	NoteState      string
	Note           string
	CountState     string
	Count          int32
	TagState       string
	ChildState     string
	ChildNoteState string
	ValuesState    string
	ValueStates    []string
	DocumentState  string
	Document       string
}
type PlaybackTarget struct {
	VideoID         golem.InputValue[golem.UUID] `golem:"graphql=videoId"`
	MediaID         golem.InputValue[golem.UUID] `golem:"graphql=mediaId"`
	PlaylistID      golem.InputValue[golem.UUID] `golem:"graphql=playlistId"`
	PlaylistVideoID golem.InputValue[golem.UUID] `golem:"graphql=playlistVideoId"`
	PlayQueueItemID golem.InputValue[golem.UUID] `golem:"graphql=playQueueItemId"`
}
type PlaybackArgs struct{ Target PlaybackTarget }

var InputChoiceCalls int

func valueState[T any](value golem.InputValue[T]) string {
	if !value.IsSet() {
		return "omitted"
	}
	if value.IsNull() {
		return "null"
	}
	return "value"
}
func describeInput(args InputStateArgs) InputView {
	note, _ := args.Patch.Note.Get()
	count, _ := args.Patch.Count.Get()
	view := InputView{NoteState: valueState(args.Patch.Note), Note: note, CountState: valueState(args.Patch.Count), Count: int32(count), TagState: valueState(args.Tag), ChildState: valueState(args.Patch.Child), ChildNoteState: "omitted", ValuesState: valueState(args.Patch.Values)}
	view.DocumentState = valueState(args.Patch.Document)
	if document, present := args.Patch.Document.Get(); present {
		view.Document = string(document.Bytes())
	}
	if child, present := args.Patch.Child.Get(); present {
		view.ChildNoteState = valueState(child.Note)
	}
	if values, present := args.Patch.Values.Get(); present {
		for _, value := range values {
			view.ValueStates = append(view.ValueStates, valueState(value))
		}
	}
	return view
}
func InputState(_ context.Context, _ *Caller[Principal], args InputStateArgs) (InputView, error) {
	return describeInput(args), nil
}
func (User) InputState(_ context.Context, _ golem.Row[User], args InputStateArgs) (InputView, error) {
	return describeInput(args), nil
}
func LoadInputStates(_ context.Context, keys []golem.UUID, args InputStateArgs) (map[golem.UUID]InputView, error) {
	result := make(map[golem.UUID]InputView, len(keys))
	for _, key := range keys {
		result[key] = describeInput(args)
	}
	return result, nil
}
func ChoosePlayback(_ context.Context, _ *Caller[Principal], args PlaybackArgs) (string, error) {
	InputChoiceCalls++
	for _, choice := range []struct {
		name  string
		value golem.InputValue[golem.UUID]
	}{{"videoId", args.Target.VideoID}, {"mediaId", args.Target.MediaID}, {"playlistId", args.Target.PlaylistID}, {"playlistVideoId", args.Target.PlaylistVideoID}, {"playQueueItemId", args.Target.PlayQueueItemID}} {
		if _, present := choice.value.Get(); present {
			return choice.name, nil
		}
	}
	return "invalid", nil
}
