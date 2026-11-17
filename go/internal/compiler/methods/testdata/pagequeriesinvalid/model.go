package pagequeriesinvalid

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
type Args struct{ Seed int32 }
type Reserved struct{ Take int32 }
type JSONInput struct{ Value golem.JSON[any] }
type JSONArgs struct{ Filter []JSONInput }
type PredicateArgs struct{ Where golem.Predicate[User] }

func DefineGraphQL(schema *golem.GraphQLSchema) {
	golem.Input[JSONInput](schema, "JSONInput")
	golem.PageQuery(schema, "missing", Page)
	golem.PageQuery(schema, "zero", Page, golem.PageLimits(0, 25, 50), golem.PageRevision("v1"))
	golem.PageQuery(schema, "over", Page, golem.PageLimits(1, 1001, 1001), golem.PageRevision("v1"))
	golem.PageQuery(schema, "candidates", Page, golem.PageLimits(25, 50, 25), golem.PageRevision("v1"))
	golem.PageQuery(schema, "revision", Page, golem.PageLimits(25, 25, 25), golem.PageRevision(""))
	golem.PageQuery(schema, "duplicate", Page, golem.PageLimits(1, 1, 1), golem.PageLimits(1, 1, 1), golem.PageRevision("v1"))
	golem.PageQuery(schema, "reserved", ReservedPage, golem.PageLimits(1, 1, 1), golem.PageRevision("v1"))
	golem.PageQuery(schema, "json", JSONPage, golem.PageLimits(1, 1, 1), golem.PageRevision("v1"))
	golem.PageQuery(schema, "predicate", PredicatePage, golem.PageLimits(1, 1, 1), golem.PageRevision("v1"))
	golem.PageQuery(schema, "signature", Ordinary, golem.PageLimits(1, 1, 1), golem.PageRevision("v1"))
	golem.PageQuery(schema, "nullable", NullablePage, golem.PageLimits(1, 1, 1), golem.PageRevision("v1"))
	golem.Query(schema, "ordinaryPage", Page)
}
func Page(context.Context, *Caller[Principal], golem.PageRequest[Args]) (golem.PageBatch[string], error) {
	var b golem.PageBatch[string]
	return b, nil
}
func ReservedPage(context.Context, *Caller[Principal], golem.PageRequest[Reserved]) (golem.PageBatch[string], error) {
	var b golem.PageBatch[string]
	return b, nil
}
func JSONPage(context.Context, *Caller[Principal], golem.PageRequest[JSONArgs]) (golem.PageBatch[string], error) {
	var b golem.PageBatch[string]
	return b, nil
}
func PredicatePage(context.Context, *Caller[Principal], golem.PageRequest[PredicateArgs]) (golem.PageBatch[string], error) {
	var b golem.PageBatch[string]
	return b, nil
}
func NullablePage(context.Context, *Caller[Principal], golem.PageRequest[Args]) (golem.PageBatch[*string], error) {
	var b golem.PageBatch[*string]
	return b, nil
}
func Ordinary(context.Context, *Caller[Principal], Args) (string, error) { return "", nil }
