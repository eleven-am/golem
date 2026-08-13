// Package queryplanbuild owns provider-neutral assembly of sanitized provider
// structure and typed operation plans into the validated query-plan report.
// It never accepts SQL, binds, provider names, or raw diagnostics.
package queryplanbuild

import (
	"math"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/analytics"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/eleven-am/golem/go/internal/queryplancapture"
	"github.com/eleven-am/golem/go/internal/queryplanreport"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	"github.com/eleven-am/golem/go/internal/scoped"
)

const (
	maxTypedPlanNodes = 256
	maxTypedPlanDepth = 32
)

type ReadInput struct {
	Provider     policyir.Provider
	Plan         readplan.Plan
	ProviderPlan queryplancapture.Plan
	Registry     *schema.Registry
	Capabilities policysql.CapabilityProof
}

type AnalyticsInput struct {
	Provider     policyir.Provider
	Plan         analytics.Plan
	ProviderPlan queryplancapture.Plan
}

type ScopedInput struct {
	Provider     policyir.Provider
	Plan         scoped.Plan
	ProviderPlan queryplancapture.Plan
}

type readFrame struct {
	cursor           readplan.QueryPlanFrame
	executions       uint64
	totalRows        uint64
	rowsPerExecution uint64
	finite           bool
	depth            uint32
}

// BuildRead combines one already-sanitized primary provider plan with the
// exact authorized typed read graph. Correlated branches remain facts of the
// provider tree; only branches whose keys depend on data execution become
// typed deferred statements.
func BuildRead(input ReadInput) (queryplanreport.Report, error) {
	provider, ok := reportProvider(input.Provider)
	operation, purpose, operationOK := readOperation(input.Plan.Operation())
	if !ok || !operationOK || input.Registry == nil || input.Plan.ModelID() == (policyir.ModelID{}) {
		return queryplanreport.Report{}, queryplanreport.NewError(queryplanreport.CodeInvalid)
	}
	statements := []queryplanreport.StatementInput{{Ordinal: 0, Purpose: purpose, Root: input.ProviderPlan.RootInput()}}

	rootMaximum, rootFinite := planRowBound(input.Plan)
	queue := []readFrame{{cursor: readplan.QueryPlanTraversal(input.Plan), executions: 1, totalRows: rootMaximum, rowsPerExecution: rootMaximum, finite: rootFinite, depth: 1}}
	walked := 0
	totalMaximum := uint64(1)
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		walked++
		if walked > maxTypedPlanNodes || current.depth > maxTypedPlanDepth {
			return queryplanreport.Report{}, queryplanreport.NewError(queryplanreport.CodeTooComplex)
		}
		currentPlan := current.cursor.Plan()

		for _, relationCursor := range current.cursor.Relations() {
			relation := relationCursor.Relation()
			strategy := readsql.ChooseRelationStrategy(currentPlan, relation, input.Registry, input.Provider)
			child := relationChildFrame(current, relationCursor, strategy, 0)
			if strategy == readsql.RelationBatch {
				maximum, capacity, err := appendDeferred(&statements, current, relationCursor, "relationBatch", input)
				if err != nil {
					return queryplanreport.Report{}, err
				}
				child = relationChildFrame(current, relationCursor, strategy, uint64(capacity))
				totalMaximum += uint64(maximum)
				if totalMaximum >= math.MaxUint32 {
					return queryplanreport.Report{}, queryplanreport.NewError(queryplanreport.CodeUnavailable)
				}
			}
			queue = append(queue, child)
		}
		for _, hydrationCursor := range current.cursor.Hydrations() {
			maximum, capacity, err := appendDeferred(&statements, current, hydrationCursor, "policyHydration", input)
			if err != nil {
				return queryplanreport.Report{}, err
			}
			totalMaximum += uint64(maximum)
			if totalMaximum >= math.MaxUint32 {
				return queryplanreport.Report{}, queryplanreport.NewError(queryplanreport.CodeUnavailable)
			}
			queue = append(queue, relationChildFrame(current, hydrationCursor, readsql.RelationBatch, uint64(capacity)))
		}
	}

	return queryplanreport.Build(queryplanreport.Input{
		Provider: provider, Operation: operation, RootModelID: golem.ModelID(input.Plan.ModelID()), Statements: statements,
		MinimumExecutionStatements: 1, MaximumExecutionStatements: uint32(totalMaximum),
	})
}

func appendDeferred(statements *[]queryplanreport.StatementInput, parent readFrame, relationCursor readplan.QueryPlanRelation, purpose string, input ReadInput) (uint32, uint32, error) {
	if !parent.finite {
		return 0, 0, queryplanreport.NewError(queryplanreport.CodeUnavailable)
	}
	relation := relationCursor.Relation()
	parentPlan := parent.cursor.Plan()
	childPlan := relationCursor.Child().Plan()
	endpoint, ok := input.Registry.RelationEndpoint(golem.ModelID(parentPlan.ModelID()), golem.FieldID(relation.FieldID()), golem.RelationID(relation.RelationID()))
	if !ok || endpoint.TargetModelID() != golem.ModelID(relation.TargetModelID()) {
		return 0, 0, queryplanreport.NewError(queryplanreport.CodeInvalid)
	}
	capacity, err := readsql.BatchKeyCapacity(childPlan, endpoint, input.Registry, input.Provider, input.Capabilities)
	if err != nil || capacity <= 0 || uint64(capacity) > math.MaxUint32 {
		return 0, 0, queryplanreport.NewError(queryplanreport.CodeUnavailable)
	}
	maximum, finite := deferredStatementBound(parent, uint64(capacity))
	if !finite {
		return 0, 0, queryplanreport.NewError(queryplanreport.CodeUnavailable)
	}
	if maximum >= math.MaxUint32 {
		return 0, 0, queryplanreport.NewError(queryplanreport.CodeUnavailable)
	}
	*statements = append(*statements, queryplanreport.StatementInput{
		Ordinal: uint32(len(*statements)), Purpose: purpose,
		Root: queryplanreport.NodeInput{
			Kind: "deferredBatch", Access: "none",
			ModelID: golem.ModelID(relation.TargetModelID()), HasModelID: true,
			RelationID: golem.RelationID(relation.RelationID()), HasRelationID: true,
			HasBatch: true, BatchCapacity: uint32(capacity), BatchMinimum: 0, BatchMaximum: uint32(maximum),
		},
	})
	return uint32(maximum), uint32(capacity), nil
}

func deferredStatementBound(parent readFrame, capacity uint64) (uint64, bool) {
	if !parent.finite || capacity == 0 {
		return 0, false
	}
	if parent.totalRows == 0 {
		return 0, true
	}
	perExecution := ceilingDivide(parent.rowsPerExecution, capacity)
	if perExecution != 0 && parent.executions > math.MaxUint32/perExecution {
		return 0, false
	}
	byExecution := parent.executions * perExecution
	if parent.totalRows < byExecution {
		return parent.totalRows, true
	}
	return byExecution, true
}

func relationChildFrame(parent readFrame, relationCursor readplan.QueryPlanRelation, strategy readsql.RelationStrategy, batchCapacity uint64) readFrame {
	relation := relationCursor.Relation()
	childCursor := relationCursor.Child()
	childPlan := childCursor.Plan()
	result := readFrame{cursor: childCursor, finite: parent.finite, depth: parent.depth + 1}
	if parent.finite && parent.totalRows == 0 {
		result.finite = true
		return result
	}
	perParent := uint64(1)
	if relation.ToMany() {
		var finite bool
		perParent, finite = planRowBound(childPlan)
		result.finite = result.finite && finite
	}
	if !result.finite || multiplicationUnavailable(parent.totalRows, perParent) || multiplicationUnavailable(parent.rowsPerExecution, perParent) {
		result.finite = false
		return result
	}
	result.totalRows = parent.totalRows * perParent
	if strategy == readsql.RelationBatch {
		statements, finite := deferredStatementBound(parent, batchCapacity)
		if !finite {
			result.finite = false
			return result
		}
		result.executions = statements
		keysPerStatement := parent.rowsPerExecution
		if batchCapacity < keysPerStatement {
			keysPerStatement = batchCapacity
		}
		result.rowsPerExecution = keysPerStatement * perParent
		return result
	}
	result.executions = parent.executions
	result.rowsPerExecution = parent.rowsPerExecution * perParent
	return result
}

func multiplicationUnavailable(left, right uint64) bool {
	return right != 0 && left > math.MaxUint32/right
}

func planRowBound(plan readplan.Plan) (uint64, bool) {
	switch plan.Operation() {
	case readir.FindUnique, readir.FindFirst, readir.Count:
		return 1, true
	case readir.FindMany:
		if limit := plan.ResultLimit(); limit > 0 {
			return uint64(limit), true
		}
		take, ok := plan.Take()
		if !ok {
			return 0, false
		}
		if take >= 0 {
			return uint64(take), true
		}
		if take == -take {
			return 0, false
		}
		return uint64(-take), true
	default:
		return 0, false
	}
}

func ceilingDivide(value, capacity uint64) uint64 {
	if value == 0 {
		return 0
	}
	return 1 + (value-1)/capacity
}

func BuildAnalytics(input AnalyticsInput) (queryplanreport.Report, error) {
	provider, providerOK := reportProvider(input.Provider)
	request := input.Plan.Request()
	operation, purpose, operationOK := analyticsOperation(request.Operation())
	if !providerOK || !operationOK || request.ModelID() == (golem.ModelID{}) {
		return queryplanreport.Report{}, queryplanreport.NewError(queryplanreport.CodeInvalid)
	}
	return buildSingle(provider, operation, purpose, request.ModelID(), input.ProviderPlan)
}

func BuildScoped(input ScopedInput) (queryplanreport.Report, error) {
	provider, providerOK := reportProvider(input.Provider)
	request := input.Plan.Request()
	operation, purpose, operationOK := scopedOperation()
	if !providerOK || !operationOK || request.RootModelID() == (golem.ModelID{}) {
		return queryplanreport.Report{}, queryplanreport.NewError(queryplanreport.CodeInvalid)
	}
	return buildSingle(provider, operation, purpose, request.RootModelID(), input.ProviderPlan)
}

func buildSingle(provider golem.Provider, operation, purpose string, root golem.ModelID, plan queryplancapture.Plan) (queryplanreport.Report, error) {
	return queryplanreport.Build(queryplanreport.Input{
		Provider: provider, Operation: operation, RootModelID: root,
		Statements:                 []queryplanreport.StatementInput{{Ordinal: 0, Purpose: purpose, Root: plan.RootInput()}},
		MinimumExecutionStatements: 1, MaximumExecutionStatements: 1,
	})
}

func reportProvider(provider policyir.Provider) (golem.Provider, bool) {
	switch provider {
	case policyir.ProviderSQLite:
		return golem.SQLite, true
	case policyir.ProviderPostgreSQL:
		return golem.PostgreSQL, true
	default:
		return "", false
	}
}

func readOperation(operation readir.Operation) (string, string, bool) {
	switch operation {
	case readir.FindUnique:
		return "findUnique", "root", true
	case readir.FindFirst:
		return "findFirst", "root", true
	case readir.FindMany:
		return "findMany", "root", true
	case readir.Count:
		return "count", "root", true
	default:
		return "", "", false
	}
}

func analyticsOperation(operation golem.AnalyticsOperation) (string, string, bool) {
	switch operation {
	case golem.AnalyticsAggregate:
		return "aggregate", "analytics", true
	case golem.AnalyticsGroupBy:
		return "groupBy", "analytics", true
	case golem.AnalyticsRelationGroupBy:
		return "relationGroupBy", "analytics", true
	default:
		return "", "", false
	}
}

func scopedOperation() (string, string, bool) { return "scoped", "scoped", true }
