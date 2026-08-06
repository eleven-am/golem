package plan

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// NodeImageRequest is the smallest immutable planner handoff needed to derive
// truthful before/after images for a row-changing mutation node. Callers must
// supply the node only after all policy, hook, and fact decoration is complete.
type NodeImageRequest struct {
	Registry              *schema.Registry
	Node                  mutationir.NodeInput
	Result                mutationir.ImageRequirements
	PrivateDeleteSnapshot []policyir.FieldID
}

// DeriveNodeImages collects every direct and relation-traversing dependency
// used by selection, row, and field authorization. Transaction-after and
// after-commit hooks additionally receive complete persisted scalar snapshots
// for the scalar row operation they observe.
func DeriveNodeImages(request NodeImageRequest) (mutationir.ImageRequirements, mutationir.ImageRequirements, error) {
	node := request.Node
	before := newImageBuilder(node.Model, request.Registry)
	after := newImageBuilder(node.Model, request.Registry)
	if request.Registry == nil {
		return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, fmt.Errorf("P4_MUTATION_IMAGES: registry is absent")
	}
	model, ok := request.Registry.Model(golem.ModelID(node.Model))
	if !ok {
		return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, fmt.Errorf("P4_MUTATION_IMAGES: model is absent")
	}
	primary := make([]policyir.FieldID, len(model.PrimaryKey()))
	for index, field := range model.PrimaryKey() {
		primary[index] = policyir.FieldID(field)
	}
	before.addFields(primary...)
	after.addFields(primary...)

	if needsPersistedHookSnapshot(node.Hooks) {
		for _, publicField := range model.Fields() {
			field, present := request.Registry.Field(golem.ModelID(node.Model), publicField)
			if !present || field.Kind() == compilerir.FieldRelation {
				continue
			}
			scalar := policyir.FieldID(publicField)
			switch node.Operation {
			case mutationir.Update, mutationir.Delete, mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation:
				before.addFields(scalar)
			}
			switch node.Operation {
			case mutationir.Create, mutationir.Update, mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation:
				after.addFields(scalar)
			}
		}
	}

	for _, operation := range node.ScalarOperations {
		before.addFields(operation.FieldID())
		after.addFields(operation.FieldID())
	}
	if node.Selection != nil {
		if err := before.addCondition(node.Selection.Constraint()); err != nil {
			return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
		}
	}
	for _, authorization := range node.FieldConditions {
		if node.Operation == mutationir.Create {
			if err := after.addCondition(authorization.Condition()); err != nil {
				return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
			}
		} else if err := before.addCondition(authorization.Condition()); err != nil {
			return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
		}
	}
	if node.RowPostcondition != nil {
		if err := after.addCondition(*node.RowPostcondition); err != nil {
			return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
		}
	}
	if node.Operation == mutationir.Delete || node.Operation == mutationir.DeleteMany {
		before.addFields(request.PrivateDeleteSnapshot...)
	}
	if request.Result.ModelID() != (policyir.ModelID{}) {
		if node.Operation == mutationir.Delete {
			if err := before.addImage(request.Result); err != nil {
				return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
			}
		} else if node.Operation == mutationir.Create || node.Operation == mutationir.Update {
			if err := after.addImage(request.Result); err != nil {
				return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
			}
		}
	}
	beforeResult, err := before.build()
	if err != nil {
		return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
	}
	afterResult, err := after.build()
	return beforeResult, afterResult, err
}

func needsPersistedHookSnapshot(hooks []mutationir.HookRequirement) bool {
	for _, hook := range hooks {
		if hook.Phase() == mutationir.TransactionAfterHook || hook.Phase() == mutationir.AfterCommitHook {
			return true
		}
	}
	return false
}
