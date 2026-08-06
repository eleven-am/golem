package plan

import (
	"github.com/eleven-am/golem/go/golem"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

// PolicySet is the narrow execution-scoped policy capability consumed by the
// planner. *policy/runtime.Set is the production implementation.
type PolicySet interface {
	GenerationDigest() golem.SchemaDigest
	Provider() policyir.Provider
	Policy(policyir.ModelID) (policyir.Policy, bool)
}

var _ PolicySet = (*policyruntime.Set)(nil)

// Classifier is injectable only so ordering and fail-closed behavior can be
// proven without a provider. The default delegates to policy/classify.Fields.
type Classifier interface {
	Fields(classify.Request) (classify.Plan, error)
}

type defaultClassifier struct{}

func (defaultClassifier) Fields(request classify.Request) (classify.Plan, error) {
	return classify.Fields(request)
}

// HookInventory is generated model metadata, not executable hook code. Upsert
// chooses exactly one branch inventory; there is deliberately no upsert hook.
type HookInventory struct {
	Create     []mutationir.HookPhase
	Update     []mutationir.HookPhase
	Delete     []mutationir.HookPhase
	UpdateMany []mutationir.HookPhase
	DeleteMany []mutationir.HookPhase
}

// RootRequest is the complete bound input to root scalar planning. Nested
// relation nodes are a later P4-C slice. Exactly the operation-appropriate
// target, predicate, and scalar inputs must be supplied.
type RootRequest struct {
	Stance    mutationir.Stance
	Operation mutationir.Operation
	Model     policyir.ModelID

	Registry *schema.Registry
	Policies PolicySet

	Target    *mutationbind.BoundTarget
	Predicate *policyir.Condition
	Create    *mutationbind.ScalarInput
	Update    *mutationbind.ScalarInput

	Result mutationir.ImageRequirements
	Hooks  HookInventory

	CaptureFacts          bool
	FactCodec             *mutationir.FactCodecRequirement
	PrivateDeleteSnapshot []policyir.FieldID
	// AuthorizedRuntimeFields are relation-owned correlation fields injected by
	// the nested compiler. They require the parent action's field grant even
	// though they are not caller-authored scalar values. Application defaults
	// deliberately never enter this inventory.
	AuthorizedRuntimeFields []policyir.FieldID

	Retry  mutationir.RetryClass
	Bounds mutationir.StatementBounds

	Classifier Classifier
}
