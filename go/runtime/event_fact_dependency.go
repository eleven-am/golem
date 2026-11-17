package runtime

import (
	"bytes"
	"context"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/observation"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type eventFactDependency struct {
	evaluate func(context.Context, *eventDependencyMember, *eventNoticeShared) (bool, error)
}

// Fact dependencies retain their source authority even when the observed model
// also has an ordinary readable dependency. The two subscriber members must
// never be merged into a target-readable judge.
func factEventDependencies(plan observation.Plan, registry *schema.Registry) ([]eventDependency, error) {
	return factEventPathDependencies(plan.Paths(), registry)
}

func factEventPathDependencies(paths []observation.Path, registry *schema.Registry) ([]eventDependency, error) {
	var order []golem.ModelID
	fields := map[golem.ModelID]map[golem.FieldID]bool{}
	for _, path := range paths {
		model := golem.ModelID(path.TargetModel())
		if fields[model] == nil {
			order = append(order, model)
			fields[model] = map[golem.FieldID]bool{}
		}
		var watch []golem.FieldID
		if len(path.Hops()) != 0 {
			var err error
			watch, err = path.WatchFields(registry)
			if err != nil {
				return nil, err
			}
		}
		for _, field := range watch {
			fields[model][field] = true
		}
	}
	result := make([]eventDependency, 0, len(order))
	for _, model := range order {
		var watch []golem.FieldID
		for field := range fields[model] {
			watch = append(watch, field)
		}
		sort.Slice(watch, func(i, j int) bool { return bytes.Compare(watch[i][:], watch[j][:]) < 0 })
		result = append(result, eventDependency{model: model, fields: watch, membership: true, physical: true})
	}
	return result, nil
}
