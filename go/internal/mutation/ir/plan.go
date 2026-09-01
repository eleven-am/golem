package ir

import (
	"fmt"
	"unicode/utf8"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type StatementBounds struct {
	maxParameters uint32
	maxRows       uint32
}

func NewStatementBounds(maxParameters, maxRows uint32) (StatementBounds, error) {
	if maxParameters == 0 || maxRows == 0 {
		return StatementBounds{}, fmt.Errorf("P4_MUTATION_IR_BOUNDS: statement parameter and row bounds must be positive")
	}
	return StatementBounds{maxParameters: maxParameters, maxRows: maxRows}, nil
}

func (bounds StatementBounds) MaxParameters() uint32 { return bounds.maxParameters }
func (bounds StatementBounds) MaxRows() uint32       { return bounds.maxRows }

// FactCodecRequirement fixes the durable fact envelope boundary while leaving
// provider storage and P7 delivery out of P4's provider-neutral plan.
type FactCodecRequirement struct {
	formatVersion uint16
	codecID       string
	generation    [32]byte
}

func NewFactCodecRequirement(formatVersion uint16, codecID string, generation [32]byte) (FactCodecRequirement, error) {
	if formatVersion == 0 || codecID == "" || !utf8.ValidString(codecID) || generation == ([32]byte{}) {
		return FactCodecRequirement{}, fmt.Errorf("P4_MUTATION_IR_FACT_CODEC: version, UTF-8 codec identity, and generation fingerprint are required")
	}
	return FactCodecRequirement{formatVersion: formatVersion, codecID: codecID, generation: generation}, nil
}

func (requirement FactCodecRequirement) FormatVersion() uint16 { return requirement.formatVersion }
func (requirement FactCodecRequirement) CodecID() string       { return requirement.codecID }
func (requirement FactCodecRequirement) Generation() [32]byte  { return requirement.generation }

type PlanInput struct {
	Stance    Stance
	Graph     Graph
	Result    ImageRequirements
	Providers []ProviderRequirement
	Retry     RetryClass
	Bounds    StatementBounds
	FactCodec *FactCodecRequirement
	// SemanticIndexed is the compiler decision that writes to this model must
	// be recorded for semantic re-embedding. It is derived from the registry,
	// never from the operation list, so a write to an indexed model is always
	// marked whether or not an indexed field changed.
	SemanticIndexed bool
}

type Plan struct {
	stance    Stance
	graph     Graph
	result    ImageRequirements
	providers []ProviderRequirement
	retry     RetryClass
	bounds    StatementBounds
	factCodec *FactCodecRequirement
	semantic  bool
}

func NewPlan(input PlanInput) (Plan, error) {
	providers, err := normalizeProviderRequirements(input.Providers)
	if err != nil {
		return Plan{}, err
	}
	result := Plan{stance: input.Stance, graph: input.Graph.clone(), result: input.Result.clone(), providers: providers, retry: input.Retry, bounds: input.Bounds, semantic: input.SemanticIndexed}
	if result.result.model == (policyir.ModelID{}) {
		if root, ok := result.graph.Root(); ok {
			result.result = emptyImage(root.model)
		}
	}
	if input.FactCodec != nil {
		value := *input.FactCodec
		result.factCodec = &value
	}
	if err := result.validate(); err != nil {
		return Plan{}, err
	}
	return result, nil
}

func (plan Plan) Stance() Stance                        { return plan.stance }
func (plan Plan) Graph() Graph                          { return plan.graph.clone() }
func (plan Plan) ResultRequirements() ImageRequirements { return plan.result.clone() }
func (plan Plan) ProviderRequirements() []ProviderRequirement {
	return append([]ProviderRequirement(nil), plan.providers...)
}
func (plan Plan) RetryClass() RetryClass  { return plan.retry }
func (plan Plan) Bounds() StatementBounds { return plan.bounds }
func (plan Plan) FactCodecRequirement() (FactCodecRequirement, bool) {
	if plan.factCodec == nil {
		return FactCodecRequirement{}, false
	}
	return *plan.factCodec, true
}
func (plan Plan) SemanticIndexed() bool { return plan.semantic }
func (plan Plan) Validate() error       { return plan.validate() }

func (plan Plan) validate() error {
	if plan.stance != Caller && plan.stance != System {
		return fmt.Errorf("P4_MUTATION_IR_PLAN: invalid stance")
	}
	if err := plan.graph.validate(); err != nil {
		return err
	}
	if plan.bounds.maxParameters == 0 || plan.bounds.maxRows == 0 {
		return fmt.Errorf("P4_MUTATION_IR_PLAN: statement bounds are absent")
	}
	if len(plan.providers) == 0 {
		return fmt.Errorf("P4_MUTATION_IR_PLAN: provider requirements are absent")
	}
	if _, err := normalizeProviderRequirements(plan.providers); err != nil {
		return err
	}
	if plan.retry < NoRetry || plan.retry > CallerTransactionNoReplay {
		return fmt.Errorf("P4_MUTATION_IR_PLAN: invalid retry class")
	}
	root := plan.graph.nodes[0]
	if plan.result.model != root.model {
		return fmt.Errorf("P4_MUTATION_IR_PLAN: result requirement model does not match root")
	}
	if plan.retry == EngineOwnedUpsertRetry && root.operation != Upsert {
		return fmt.Errorf("P4_MUTATION_IR_PLAN: engine-owned retry is valid only for upsert")
	}
	if root.operation == Upsert && plan.retry == NoRetry {
		return fmt.Errorf("P4_MUTATION_IR_PLAN: upsert must declare retry ownership")
	}
	hasFact := false
	hasEventSchemaFact := false
	missingEventSchemaFact := false
	for _, node := range plan.graph.nodes {
		if node.fact.enabled {
			hasFact = true
			if node.fact.eventSchema == ([32]byte{}) {
				missingEventSchemaFact = true
			} else {
				hasEventSchemaFact = true
			}
		}
		if plan.stance == System {
			if node.selection != nil || node.rowPostcondition != nil || len(node.fieldConditions) != 0 || len(node.hooks) != 0 {
				return fmt.Errorf("P4_MUTATION_IR_PLAN: system nodes cannot carry caller policy or hooks")
			}
			continue
		}
		if node.operation.existingRows() && node.selection == nil {
			return fmt.Errorf("P4_MUTATION_IR_PLAN: caller existing-row node lacks a selecting constraint")
		}
		switch node.operation {
		case Create, Update, UpdateMany, Connect, Disconnect, SetRelation:
			if node.rowPostcondition == nil {
				return fmt.Errorf("P4_MUTATION_IR_PLAN: caller write node lacks a row postcondition")
			}
		case Delete, DeleteMany:
			if node.rowPostcondition != nil {
				return fmt.Errorf("P4_MUTATION_IR_PLAN: delete cannot carry an after-image postcondition")
			}
		}
	}
	if hasFact != (plan.factCodec != nil) {
		return fmt.Errorf("P4_MUTATION_IR_PLAN: fact-producing nodes and codec requirement must be present together")
	}
	if plan.factCodec != nil {
		if plan.factCodec.formatVersion == 0 || plan.factCodec.codecID == "" || plan.factCodec.generation == ([32]byte{}) {
			return fmt.Errorf("P4_MUTATION_IR_PLAN: invalid fact codec requirement")
		}
		if plan.factCodec.formatVersion >= 2 && missingEventSchemaFact || plan.factCodec.formatVersion < 2 && hasEventSchemaFact || hasEventSchemaFact && missingEventSchemaFact {
			return fmt.Errorf("P7_MUTATION_IR_PLAN: fact codec and per-node event-schema requirements disagree")
		}
	}
	return nil
}

func emptyImage(model policyir.ModelID) ImageRequirements {
	value, _ := NewImageRequirements(model, nil, nil)
	return value
}
