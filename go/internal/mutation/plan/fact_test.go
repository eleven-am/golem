package plan

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestFactWritePlansRetainPhysicalBeforeAndFinalGraphDependencies(t *testing.T) {
	handles := newWriteReachHandles(t)
	root := policyir.ModelID(handles.fixture.Post)
	public := golem.NewRules[reachPost]()
	public.CanRead(golem.All[reachPost]())
	hidden := golem.NewRules[reachUser]()
	for _, mode := range []struct{ negative, relatedScope bool }{{false, false}, {true, false}, {false, true}} {
		negative := mode.negative
		predicate := handles.writer.Is(handles.userName.Eq("enabled"))
		if negative {
			predicate = predicate.Not()
		}
		descriptor := golem.GeneratedModelDescriptor[reachPost](handles.fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
		frozen, err := predicate.Freeze(descriptor)
		if err != nil {
			t.Fatal(err)
		}
		body, err := policybind.Predicate(frozen, handles.fixture.Registry, policyir.PortableProviders())
		if err != nil {
			t.Fatal(err)
		}
		scope := unrestrictedFactScope(t, root)
		if mode.relatedScope {
			scope, err = policyir.NewFactScope(body)
			if err != nil {
				t.Fatal(err)
			}
			body, _ = policyir.NewConstant(root, true)
		}
		definition, _ := policyir.NewFactDefinition(root, "author_enabled", body)
		reference, _ := policyir.NewFactReference(root, definition.ID())
		policies := handles.policies(t, public, hidden)
		rules := policies.values[root].Rules()
		for _, action := range []policyir.Action{policyir.ActionUpdate, policyir.ActionDelete} {
			rule, _ := policyir.NewModelRule(action, policyir.EffectGrant, root, &reference, uint32(len(rules)))
			rules = append(rules, rule)
		}
		policies.values[root], err = policyir.NewPolicyWithFacts(root, rules, []policyir.FactDefinition{definition}, scope)
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range []mutationir.Operation{mutationir.Update, mutationir.Delete} {
			planned, err := BuildRoot(handles.request(t, policies, operation))
			if err != nil {
				t.Fatalf("negative=%v operation=%d: %v", negative, operation, err)
			}
			node, _ := planned.Graph().Root()
			selection, ok := node.SelectionRequirement()
			if !ok || !policyir.HasFacts(selection.Constraint()) {
				t.Fatal("mutation admission lost its fact boundary")
			}
			images := []mutationir.ImageRequirements{node.BeforeRequirements()}
			if operation == mutationir.Update {
				images = append(images, node.AfterRequirements())
				postcondition, ok := node.RowPostcondition()
				if !ok || !policyir.HasFacts(postcondition) {
					t.Fatal("mutation final authorization lost its fact boundary")
				}
			}
			for _, image := range images {
				found := false
				for _, item := range image.Dependencies() {
					path := item.Path()
					if item.FieldID() == policyir.FieldID(handles.fixture.UserName) && len(path) == 1 && path[0].FieldID() == policyir.FieldID(handles.fixture.PostAuthor) && path[0].TargetModelID() == policyir.ModelID(handles.fixture.User) {
						found = true
					}
				}
				if !found {
					t.Fatal("mutation image dropped a hidden physical dependency")
				}
			}
		}
	}
}
