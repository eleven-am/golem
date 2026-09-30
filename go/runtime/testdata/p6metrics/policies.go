package p6metrics

import golem "github.com/eleven-am/golem/go/golem"

func (Metric) DefinePolicy(rules *golem.Rules[Metric], _ Actor) {
	rules.CanRead(golem.All[Metric]())
}

func (Category) DefinePolicy(rules *golem.Rules[Category], actor Actor) {
	visible := Categories.Name.StartsWith(actor.CategoryPrefix)
	if actor.AlsoCategory != "" {
		visible = visible.Or(Categories.Name.Eq(actor.AlsoCategory))
	}
	rules.CanRead(visible)
}
