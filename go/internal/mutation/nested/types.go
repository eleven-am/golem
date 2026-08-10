// Package nested binds and plans the closed nested-mutation vocabulary before
// any transaction or provider boundary. It produces only canonical mutation IR
// and explicit bounded database-expansion requirements.
package nested

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type ErrorCode string

const (
	CodeInput          ErrorCode = "P4_NESTED_INPUT"
	CodeRelation       ErrorCode = "P4_NESTED_RELATION"
	CodeShape          ErrorCode = "P4_NESTED_SHAPE"
	CodeExposure       ErrorCode = "P4_NESTED_EXPOSURE"
	CodeDepth          ErrorCode = "P4_NESTED_DEPTH"
	CodeBinding        ErrorCode = "P4_NESTED_BINDING"
	CodePolicy         ErrorCode = "P4_NESTED_POLICY"
	CodeClassification ErrorCode = "P4_NESTED_CLASSIFICATION"
	CodeIR             ErrorCode = "P4_NESTED_IR"
)

type Error struct {
	Code   ErrorCode
	Model  golem.ModelID
	Field  golem.FieldID
	Detail string
	Cause  error
}

func (failure *Error) Error() string {
	return fmt.Sprintf("%s: model=%x field=%x: %s", failure.Code, failure.Model, failure.Field, failure.Detail)
}
func (failure *Error) Unwrap() error { return failure.Cause }

// NotFoundError is the execution-time result of resolving one exact nested
// selector/current-to-one position to no row. It is deliberately typed so the
// public runtime can give missing and caller-invisible targets identical
// NOT_FOUND semantics without parsing provider/cardinality error strings.
type NotFoundError struct {
	Model policyir.ModelID
	Field policyir.FieldID
}

func (failure *NotFoundError) Error() string {
	return fmt.Sprintf("P4_NESTED_NOT_FOUND: model=%x field=%x: selected nested record was not found", failure.Model, failure.Field)
}

type Classifier interface {
	Fields(classify.Request) (classify.Plan, error)
}
type defaultClassifier struct{}

func (defaultClassifier) Fields(request classify.Request) (classify.Plan, error) {
	return classify.Fields(request)
}

type Request struct {
	Root      mutationir.NodeInput
	Mutations []golem.FrozenNestedMutation
	Stance    mutationir.Stance
	Registry  *schema.Registry
	Policies  mutationplan.PolicySet
	// HookInventory returns generated hook metadata for the exact owning model.
	// A nil callback is the explicit empty inventory used by schemas with no
	// hooks; runtime supplies this callback from generated bindings.
	HookInventory func(policyir.ModelID) mutationplan.HookInventory
	// SourceOffset reserves runtime provenance identities already retained by
	// an enclosing dynamic compilation. It does not affect semantic graph
	// ordinals or canonical plan encoding.
	SourceOffset uint32
	Classifier   Classifier
	MaxDepth     uint16
	MaxRows      uint32
	// RuntimeValues materializes application-owned defaults/updated fields
	// after stable runtime source slots have been assigned and before semantic
	// graph ordinals are frozen. Nil means no runtime-owned materialization.
	RuntimeValues func(mutationir.NodeInput) (mutationir.NodeInput, error)
}

// PositionAudit is proof that one selector/filter position was classified
// during the pre-graph phase. Zero Fields is valid only for explicit All.
type PositionAudit struct {
	parent policyir.ModelID
	field  policyir.FieldID
	model  policyir.ModelID
	action mutationir.Operation
	use    classify.UseKind
	fields []policyir.FieldID
}

func (audit PositionAudit) ParentModelID() policyir.ModelID { return audit.parent }
func (audit PositionAudit) FieldID() policyir.FieldID       { return audit.field }
func (audit PositionAudit) ModelID() policyir.ModelID       { return audit.model }
func (audit PositionAudit) Operation() mutationir.Operation { return audit.action }
func (audit PositionAudit) UseKind() classify.UseKind       { return audit.use }
func (audit PositionAudit) Fields() []policyir.FieldID {
	return append([]policyir.FieldID(nil), audit.fields...)
}

type Result struct {
	graph            mutationir.Graph
	audits           []PositionAudit
	sources          map[uint32]HookSource
	sourceUpperBound uint32
}

func (result Result) Graph() mutationir.Graph { return result.graph }
func (result Result) PositionAudits() []PositionAudit {
	output := make([]PositionAudit, len(result.audits))
	copy(output, result.audits)
	for index := range output {
		output[index].fields = append([]policyir.FieldID(nil), result.audits[index].fields...)
	}
	return output
}

// HookSource retains the frozen generated branch that produced one nested
// write node. It is runtime provenance, not mutation-plan semantics.
type HookSource struct {
	parent    golem.ModelID
	field     golem.FieldID
	relation  golem.RelationID
	target    golem.ModelID
	action    golem.MutationRelationAction
	branch    golem.FrozenNestedMutationBranch
	hasBranch bool
}

func (source HookSource) ParentModelID() golem.ModelID         { return source.parent }
func (source HookSource) FieldID() golem.FieldID               { return source.field }
func (source HookSource) RelationID() golem.RelationID         { return source.relation }
func (source HookSource) TargetModelID() golem.ModelID         { return source.target }
func (source HookSource) Action() golem.MutationRelationAction { return source.action }
func (source HookSource) Branch() (golem.FrozenNestedMutationBranch, bool) {
	return source.branch, source.hasBranch
}

func (result Result) HookSource(node mutationir.Node) (HookSource, bool) {
	id, ok := node.RuntimeSourceID()
	if !ok {
		return HookSource{}, false
	}
	source, ok := result.sources[id]
	return source, ok
}

func (result Result) SourceUpperBound() uint32 { return result.sourceUpperBound }

func fail(code ErrorCode, model golem.ModelID, field golem.FieldID, detail string, cause error) error {
	return &Error{Code: code, Model: model, Field: field, Detail: detail, Cause: cause}
}
