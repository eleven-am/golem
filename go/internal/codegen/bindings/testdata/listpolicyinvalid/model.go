package listpolicyinvalid

import g "github.com/eleven-am/golem/go/golem"

type Actor struct{}

type Post struct {
	Tags g.List[string]
}

func (Post) DefinePolicy(rules *g.Rules[Post], _ Actor) {
	rules.CanRead(Posts.Tags.Has("go"))
}
