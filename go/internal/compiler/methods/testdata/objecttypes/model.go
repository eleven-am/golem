package objecttypes

import (
	"context"
	"github.com/eleven-am/golem/go/golem"
)

type Principal struct{}
type Caller[P any] struct{}
type User struct {
	ID   int64
	Name string
}
type HomeFeedPage struct {
	Results []HomeSegment
	Seed    int32
}
type HomeSegment struct {
	Kind  string
	Index int32
}
type FeedInput struct {
	Range  *RangeInput
	Note   golem.InputValue[string]
	Target *PlaybackTarget
}
type PlaybackTarget struct {
	VideoID         golem.InputValue[golem.UUID] `golem:"graphql=videoId"`
	MediaID         golem.InputValue[golem.UUID] `golem:"graphql=mediaId"`
	PlaylistID      golem.InputValue[golem.UUID] `golem:"graphql=playlistId"`
	PlaylistVideoID golem.InputValue[golem.UUID] `golem:"graphql=playlistVideoId"`
	PlayQueueItemID golem.InputValue[golem.UUID] `golem:"graphql=playQueueItemId"`
}
type RangeInput struct{ Seed int32 }
type HomeArgs struct {
	Input FeedInput
	Tag   golem.InputValue[string]
}

func DefineGraphQL(schema *golem.GraphQLSchema) {
	golem.Query(schema, "homeFeed", HomeFeed)
	golem.Input[FeedInput](schema, "HomeInput")
	golem.Output[HomeFeedPage](schema, "HomeFeedPage", golem.ObjectFieldMaxItems("Results", 25))
	golem.Output[HomeSegment](schema, "HomeSegment")
	golem.Input[RangeInput](schema, "PageRange")
	golem.Input[PlaybackTarget](schema, "PlaybackTarget", golem.InputOneOf())
}

func HomeFeed(context.Context, *Caller[Principal], HomeArgs) (HomeFeedPage, error) {
	return HomeFeedPage{}, nil
}

func (User) DefineGraphQL(schema *golem.GraphQLModel[User]) {
	golem.ComputedField(schema, "feed", golem.GraphQLOutput[HomeFeedPage]().NonNull(), User{}.Feed)
}

func (User) Feed(context.Context, golem.Row[User], HomeArgs) (HomeFeedPage, error) {
	return HomeFeedPage{}, nil
}
