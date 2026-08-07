package p6metrics

import golem "github.com/eleven-am/golem/go/golem"

func (Metric) DefinePolicy(rules *golem.Rules[Metric], _ Actor) {
	rules.CanRead(golem.All[Metric]())
}

func (Category) DefinePolicy(rules *golem.Rules[Category], actor Actor) {
	rules.CanRead(Categories.Name.StartsWith(actor.CategoryPrefix))
}
