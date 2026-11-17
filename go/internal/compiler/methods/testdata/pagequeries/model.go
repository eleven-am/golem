package pagequeries

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
type Segment struct {
	Kind  string
	Index int32
}
type Filter struct {
	Seed  int32
	Label golem.InputValue[string]
}
type Args struct{ Filter Filter }

func DefineGraphQL(schema *golem.GraphQLSchema) {
	golem.Output[Segment](schema, "HomeSegment")
	golem.Input[Filter](schema, "HomeFilter")
	golem.PageQuery(schema, "homeFeed", HomeFeed, golem.PageLimits(25, 100, 200), golem.PageRevision("home-v1"))
}

func HomeFeed(context.Context, *Caller[Principal], golem.PageRequest[Args]) (golem.PageBatch[Segment], error) {
	var batch golem.PageBatch[Segment]
	return batch, nil
}
