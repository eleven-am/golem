package history

import (
	"context"
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

// Evaluate applies the ordinary policy evaluator to immutable physical evidence.
// The registry must describe the exact producing generation and capture contract;
// providers is that registry's complete provider set, as used to bind condition.
// Missing evidence is an error, while a known absent root evaluates to false.
// Traversal follows finite condition dependencies, never live reads or cached
// actor decisions, even when the physical graph contains cycles.
func (graph Graph) Evaluate(ctx context.Context, registry *schema.Registry, providers policyir.ProviderSet, phase Phase, rootKey string, condition policyir.Condition) (bool, error) {
	owner, err := graph.NewEvaluator(ctx, registry, providers, phase)
	if err != nil {
		return false, err
	}
	return owner.EvaluateRoot(rootKey, condition)
}

// EvaluateCaptured applies the same bounded evaluator to a present captured
// row, including a non-root row. Missing same-phase evidence is an error; an
// absent root or a row captured only in the other phase cannot supply it.
// Callers select immutable graph evidence by key and cannot supply their own row.
func (graph Graph) EvaluateCaptured(ctx context.Context, registry *schema.Registry, providers policyir.ProviderSet, phase Phase, rowKey string, condition policyir.Condition) (bool, error) {
	owner, err := graph.NewEvaluator(ctx, registry, providers, phase)
	if err != nil {
		return false, err
	}
	return owner.EvaluateCaptured(rowKey, condition)
}

// Evaluator owns the aggregate work of one sequential historical matching
// operation in one phase. Repeated conditions and relation proofs share their
// budgets; restarting the per-condition loader cannot multiply graph work.
// It retains no policy answers and must not be shared across goroutines or
// retained as an actor authorization cache.
type Evaluator struct {
	ctx         context.Context
	graph       Graph
	providers   policyir.ProviderSet
	phase       Phase
	descriptors conditionDescriptors
	loader      recordLoader
}

func (graph Graph) NewEvaluator(ctx context.Context, registry *schema.Registry, providers policyir.ProviderSet, phase Phase) (*Evaluator, error) {
	if ctx == nil {
		return nil, fmt.Errorf("policy history: evaluation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if graph.data == nil || registry == nil || graph.Generation() != registry.GenerationDigest() || graph.ProjectionFingerprint() != registry.PolicyCaptureFingerprint() {
		return nil, fmt.Errorf("policy history: exact producing schema and capture contract are required")
	}
	if !providers.Valid() || providers != policysql.SchemaResolver(registry).Providers() || !providers.Contains(graph.Provider()) {
		return nil, fmt.Errorf("policy history: evaluation providers differ from the producing schema")
	}
	if phase != Before && phase != After {
		return nil, fmt.Errorf("policy history: explicit before/after phase is required")
	}
	return &Evaluator{
		ctx: ctx, graph: graph, providers: providers, phase: phase,
		descriptors: conditionDescriptors{registry: registry, resolver: policysql.SchemaResolver(registry), provider: graph.Provider()},
		loader:      recordLoader{graph: graph, registry: registry, phase: phase},
	}, nil
}

func (owner *Evaluator) EvaluateRoot(key string, condition policyir.Condition) (bool, error) {
	return owner.evaluateAt(key, condition, true)
}

func (owner *Evaluator) EvaluateCaptured(key string, condition policyir.Condition) (bool, error) {
	return owner.evaluateAt(key, condition, false)
}

func (owner *Evaluator) CertifiedChildren(parentKey string, field policyir.FieldID, relation policyir.RelationID) ([]mutationdecode.Row, bool, error) {
	if owner == nil || owner.ctx == nil {
		return nil, false, fmt.Errorf("policy history: evaluation owner is required")
	}
	if err := owner.ctx.Err(); err != nil {
		return nil, false, err
	}
	rows, complete, err := owner.graph.certifiedChildren(owner.loader.registry, owner.phase, parentKey, field, relation, &owner.loader.relations)
	if err != nil {
		return nil, false, err
	}
	if err := owner.ctx.Err(); err != nil {
		return nil, false, err
	}
	return rows, complete, nil
}

func (owner *Evaluator) evaluateAt(key string, condition policyir.Condition, rootOnly bool) (bool, error) {
	if owner == nil || owner.ctx == nil {
		return false, fmt.Errorf("policy history: evaluation owner is required")
	}
	ctx, graph, phase, providers := owner.ctx, owner.graph, owner.phase, owner.providers
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var row *mutationdecode.Row
	var model policyir.ModelID
	if rootOnly {
		index, known := graph.data.roots[key]
		if !known {
			return false, fmt.Errorf("policy history: source root existence is unknown")
		}
		model = graph.data.input.Roots[index].Identity.ModelID()
		row, _ = graph.FindRoot(phase, key)
	} else {
		captured, present := graph.FindRow(phase, key)
		if !present {
			return false, fmt.Errorf("policy history: requested same-phase captured row is unknown")
		}
		model, row = captured.ModelID(), &captured
	}
	if condition.ModelID() != model {
		return false, fmt.Errorf("policy history: condition and selected evidence models differ")
	}
	if err := owner.descriptors.check(ctx, condition, 0); err != nil {
		return false, err
	}
	if err := operator.ValidateCondition(condition, providers); err != nil {
		return false, err
	}
	plan, err := dependency.Collect(condition)
	if err != nil {
		return false, err
	}
	if row == nil {
		return false, ctx.Err()
	}
	record, err := owner.loader.record(ctx, *row, plan.Dependencies(), 0)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	matched, err := evaluate.Condition(condition, record, providers)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return matched, nil
}

// conditionDescriptors checks every static branch before root-absence or
// Boolean shortcuts. A caller resolving the right historical registry must
// not accidentally evaluate descriptors bound against a different generation.
// SchemaResolver owns logical TypeRef construction; compare its full value
// directly instead of introducing another type codec or compatibility rule.
type conditionDescriptors struct {
	registry *schema.Registry
	resolver policysql.Resolver
	provider policyir.Provider
	nodes    int
}

func (owner *conditionDescriptors) check(ctx context.Context, condition policyir.Condition, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth >= policyir.MaxFactDepth {
		return fmt.Errorf("policy history: condition descriptor path exceeds the policy expansion depth")
	}
	if owner.nodes >= MaxNodes {
		return &LimitError{Limit: golem.LimitPolicyHistoryNodes, Max: MaxNodes}
	}
	owner.nodes++
	if !owner.registry.HasModel(golem.ModelID(condition.ModelID())) {
		return fmt.Errorf("policy history: condition model is absent from the producing schema")
	}
	switch condition.Kind() {
	case policyir.ConditionConstant:
		return nil
	case policyir.ConditionFact:
		_, body, _ := condition.Fact()
		return owner.check(ctx, body, depth+1)
	case policyir.ConditionLogical:
		_, children, _ := condition.Logical()
		if len(children) > MaxNodes-owner.nodes {
			return &LimitError{Limit: golem.LimitPolicyHistoryNodes, Max: MaxNodes}
		}
		for _, child := range children {
			if err := owner.check(ctx, child, depth+1); err != nil {
				return err
			}
		}
		return nil
	case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
		field, _ := condition.Field()
		typ, _ := condition.FieldType()
		descriptor, found := owner.resolver.Field(owner.provider, condition.ModelID(), field)
		if !found || descriptor.Model != condition.ModelID() || descriptor.ID != field || !reflect.DeepEqual(descriptor.Type, typ) || descriptor.Nullable != typ.Nullable() {
			return fmt.Errorf("policy history: condition field type differs from the producing schema")
		}
		if condition.Kind() != policyir.ConditionScalar || typ.Kind() == policyir.ValueJSON || typ.Kind() == policyir.ValueScalarList {
			return fmt.Errorf("policy history: condition requires unsupported non-scalar evidence")
		}
		return nil
	case policyir.ConditionRelation:
		field, relation, target, cardinality, child, _ := condition.Relation()
		endpoint, found := owner.registry.RelationEndpoint(golem.ModelID(condition.ModelID()), golem.FieldID(field), golem.RelationID(relation))
		if !found || policyir.ModelID(endpoint.ModelID()) != condition.ModelID() || policyir.FieldID(endpoint.FieldID()) != field || policyir.RelationID(endpoint.RelationID()) != relation || policyir.ModelID(endpoint.TargetModelID()) != target {
			return fmt.Errorf("policy history: condition relation differs from the producing schema")
		}
		switch endpoint.Cardinality() {
		case compilerir.RelationOne:
			if cardinality != policyir.RelationToOne {
				return fmt.Errorf("policy history: condition relation cardinality differs from the producing schema")
			}
		case compilerir.RelationMany:
			if cardinality != policyir.RelationToMany {
				return fmt.Errorf("policy history: condition relation cardinality differs from the producing schema")
			}
		default:
			return fmt.Errorf("policy history: producing relation cardinality is unknown")
		}
		if child != nil {
			return owner.check(ctx, *child, depth+1)
		}
		return nil
	default:
		return fmt.Errorf("policy history: unsupported condition descriptor kind")
	}
}

type recordLoader struct {
	relations relationProofBudget
	graph     Graph
	registry  *schema.Registry
	phase     Phase
	nodes     int
	bytes     int
}

func (loader *recordLoader) charge(depth int) error {
	// Relation construction and NewRecord each clone descendants once at
	// every ancestor. Bound that real work before allocating a deep/wide
	// evaluator view, rather than counting only unique physical graph rows.
	cost := 1 + 2*depth
	if cost > MaxNodes-loader.nodes {
		return &LimitError{Limit: golem.LimitPolicyHistoryNodes, Max: MaxNodes}
	}
	loader.nodes += cost
	return nil
}

func (loader *recordLoader) record(ctx context.Context, row mutationdecode.Row, tree dependency.Tree, depth int) (evaluate.Record, error) {
	if err := ctx.Err(); err != nil {
		return evaluate.Record{}, err
	}
	if depth > 64 {
		return evaluate.Record{}, fmt.Errorf("policy history: evaluation path exceeds the captured template depth")
	}
	if err := loader.charge(depth); err != nil {
		return evaluate.Record{}, err
	}
	if row.ModelID() != tree.ModelID() {
		return evaluate.Record{}, fmt.Errorf("policy history: historical row and dependency models differ")
	}
	registry := loader.registry
	key, err := RowKey(registry, loader.graph.Provider(), row)
	if err != nil {
		return evaluate.Record{}, err
	}
	entries := tree.Entries()
	fields := make([]evaluate.Field, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return evaluate.Record{}, err
		}
		if err := loader.charge(depth); err != nil {
			return evaluate.Record{}, err
		}
		var field evaluate.Field
		switch entry.Kind() {
		case dependency.Scalar:
			cell, present := row.Cell(entry.FieldID())
			if !present {
				return evaluate.Record{}, fmt.Errorf("policy history: historical scalar dependency is unknown")
			}
			if cell.IsNull() {
				field = evaluate.NullField(entry.FieldID())
			} else {
				value, valid := cell.PolicyValue()
				if !valid {
					return evaluate.Record{}, fmt.Errorf("policy history: historical scalar has no typed value")
				}
				size, err := value.ScalarCanonicalSize()
				if err != nil {
					return evaluate.Record{}, err
				}
				// Repeated paths may revisit one physical row. Charge each
				// materialized value so factorized storage cannot cause an
				// unbounded evaluator tree to be allocated.
				if size > MaxScalarBytes-loader.bytes {
					return evaluate.Record{}, &LimitError{Limit: golem.LimitPolicyHistoryBytes, Max: MaxScalarBytes}
				}
				loader.bytes += size
				field, err = evaluate.ValueField(entry.FieldID(), value)
				if err != nil {
					return evaluate.Record{}, err
				}
			}
		case dependency.Relation:
			descriptor, present := registry.Field(golem.ModelID(row.ModelID()), golem.FieldID(entry.FieldID()))
			if !present {
				return evaluate.Record{}, fmt.Errorf("policy history: historical relation descriptor is unknown")
			}
			relation, valid := descriptor.RelationID()
			if !valid {
				return evaluate.Record{}, fmt.Errorf("policy history: relation dependency is not a relation field")
			}
			endpoint, present := registry.RelationEndpoint(golem.ModelID(row.ModelID()), golem.FieldID(entry.FieldID()), relation)
			target, _ := entry.TargetModel()
			if !present || policyir.ModelID(endpoint.TargetModelID()) != target {
				return evaluate.Record{}, fmt.Errorf("policy history: historical relation endpoint differs from the dependency")
			}
			rows, complete, err := loader.graph.certifiedChildren(registry, loader.phase, key, entry.FieldID(), policyir.RelationID(relation), &loader.relations)
			if err != nil {
				return evaluate.Record{}, err
			}
			if !complete {
				return evaluate.Record{}, fmt.Errorf("policy history: historical relation extent is unknown")
			}
			// Reserve before allocation, including children with no scalar
			// dependencies (relation presence still loads physical rows).
			if len(rows) > MaxNodes-loader.nodes {
				return evaluate.Record{}, &LimitError{Limit: golem.LimitPolicyHistoryNodes, Max: MaxNodes}
			}
			children := make([]evaluate.Record, 0, len(rows))
			childTree := entry.Children()
			for _, child := range rows {
				record, err := loader.record(ctx, child, childTree, depth+1)
				if err != nil {
					return evaluate.Record{}, err
				}
				children = append(children, record)
			}
			switch endpoint.Cardinality() {
			case compilerir.RelationOne:
				field, err = evaluate.ToOneField(entry.FieldID(), target, children...)
			case compilerir.RelationMany:
				field, err = evaluate.ToManyField(entry.FieldID(), target, children...)
			default:
				err = fmt.Errorf("policy history: historical relation cardinality is unknown")
			}
			if err != nil {
				return evaluate.Record{}, err
			}
		default:
			return evaluate.Record{}, fmt.Errorf("policy history: unsupported dependency kind")
		}
		fields = append(fields, field)
	}
	return evaluate.NewRecord(row.ModelID(), fields...)
}
