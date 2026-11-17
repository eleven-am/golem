package runtime

import (
	"context"
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	"github.com/jmoiron/sqlx"
)

const maxMutationHistoryNodes = policyhistory.MaxNodes

type mutationHistoryPhase uint8

const (
	mutationHistoryBefore mutationHistoryPhase = iota + 1
	mutationHistoryAfter
)

// This reader owns one write-transaction snapshot. Current rows are rewound
// with first-before images for the before phase, including rows whose changed
// correlation no longer matches the SQL query. Missing or truncated evidence
// never becomes a known empty relationship.
type mutationHistorySnapshot struct {
	state         *mutationState
	registry      *schema.Registry
	queryer       sqlx.QueryerContext
	provider      policyir.Provider
	images        []mutationHistoryImage
	epoch         uint64
	bytes, nodes  int
	maxParameters uint32
	failure       error
}

func newMutationHistorySnapshot(binding *executionBinding, registry *schema.Registry) (*mutationHistorySnapshot, error) {
	if binding == nil || registry == nil || !binding.mutation.enabled || binding.transaction == nil && !binding.scoped {
		return nil, fmt.Errorf("policy history: an owned write transaction is required")
	}
	provider := binding.mutation.provider
	if provider != policyir.ProviderSQLite && (provider != policyir.ProviderPostgreSQL || !binding.historySerializable) {
		return nil, fmt.Errorf("policy history: provider requires a certified transaction snapshot; PostgreSQL requires Serializable")
	}
	state, err := binding.mutationState()
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	epoch := state.historyEpoch
	available := len(state.history) != 0 && !state.flushed && !state.finished
	state.mu.Unlock()
	if !available {
		return nil, fmt.Errorf("policy history: no active captured write establishes snapshot ownership")
	}
	images := state.historyImages()
	queryer, err := binding.queryerFor(binding.database)
	if err != nil {
		return nil, err
	}
	snapshot := &mutationHistorySnapshot{state: state, registry: registry, queryer: queryer, provider: provider, images: images, epoch: epoch, maxParameters: uint32(binding.mutation.limits.statementParameters)}
	if err := snapshot.check(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (snapshot *mutationHistorySnapshot) check() error {
	if snapshot.failure != nil {
		return fmt.Errorf("policy history: snapshot construction previously failed: %w", snapshot.failure)
	}
	snapshot.state.mu.Lock()
	defer snapshot.state.mu.Unlock()
	if snapshot.state.finished || snapshot.state.flushed || snapshot.state.historyEpoch != snapshot.epoch {
		return fmt.Errorf("policy history: captured transaction changed or ended during graph construction")
	}
	return nil
}

func (snapshot *mutationHistorySnapshot) read(ctx context.Context, model policyir.ModelID, equalities []mutationdecode.Cell, phase mutationHistoryPhase, maxRows uint32) (_ []mutationdecode.Row, readErr error) {
	defer func() {
		if readErr != nil && snapshot.failure == nil {
			snapshot.failure = readErr
		}
	}()
	if phase != mutationHistoryBefore && phase != mutationHistoryAfter {
		return nil, fmt.Errorf("policy history: explicit before/after phase is required")
	}
	if maxRows == 0 || maxRows > maxMutationHistoryNodes {
		return nil, fmt.Errorf("policy history: row bound is outside the graph budget")
	}
	if err := snapshot.check(); err != nil {
		return nil, err
	}
	fields := snapshot.registry.PolicyCaptureFields(golem.ModelID(model))
	known := make(map[policyir.FieldID]bool, len(fields))
	for _, field := range fields {
		known[policyir.FieldID(field)] = true
	}
	for _, cell := range equalities {
		if !known[cell.FieldID()] {
			return nil, fmt.Errorf("policy history: equality requires an uncaptured field")
		}
	}
	match, err := mutationdecode.NewRow(snapshot.registry, model, equalities)
	if err != nil {
		return nil, err
	}
	statement, err := mutationbatch.RenderPolicyCapture(snapshot.registry, snapshot.provider, model, match.Cells(), maxRows, snapshot.maxParameters)
	if err != nil {
		return nil, err
	}
	if snapshot.nodes >= maxMutationHistoryNodes {
		return nil, &mutationLimitError{limit: golem.LimitPolicyHistoryNodes, max: maxMutationHistoryNodes}
	}
	snapshot.nodes++
	current, err := snapshot.query(ctx, model, statement, fields, maxRows)
	if err != nil {
		return nil, err
	}
	indexed := make(map[string]mutationdecode.Row, len(current))
	for _, row := range current {
		key, err := mutationbatch.PrimaryKey(snapshot.registry, snapshot.provider, row)
		if err != nil {
			return nil, err
		}
		if _, duplicate := indexed[key]; duplicate {
			return nil, fmt.Errorf("policy history: snapshot returned duplicate primary identities")
		}
		indexed[key] = row
	}
	// Remove every changed identity first. Add every matching old/final image
	// afterwards, including images absent from the current SQL matching set.
	for _, image := range snapshot.images {
		row := image.after
		if row == nil {
			row = image.before
		}
		if row == nil || row.ModelID() != model {
			continue
		}
		delete(indexed, image.key)
		selected := image.after
		if phase == mutationHistoryBefore {
			selected = image.before
		}
		if selected == nil {
			continue
		}
		matches, err := snapshot.matches(*selected, match.Cells())
		if err != nil {
			return nil, err
		}
		if matches {
			if snapshot.nodes >= maxMutationHistoryNodes {
				return nil, &mutationLimitError{limit: golem.LimitPolicyHistoryNodes, max: maxMutationHistoryNodes}
			}
			_, size, err := projectMutationHistory(snapshot.registry, selected, fields)
			if err != nil {
				return nil, err
			}
			if size > maxMutationHistoryBytes-snapshot.bytes {
				return nil, &mutationLimitError{limit: golem.LimitPolicyHistoryBytes, max: maxMutationHistoryBytes}
			}
			snapshot.bytes += size
			snapshot.nodes++
			indexed[image.key] = *selected
		}
	}
	if uint64(len(indexed)) > uint64(maxRows) {
		return nil, &mutationLimitError{limit: golem.LimitPolicyHistoryNodes, max: int(maxRows)}
	}
	keys := make([]string, 0, len(indexed))
	for key := range indexed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]mutationdecode.Row, 0, len(keys))
	for _, key := range keys {
		result = append(result, indexed[key])
	}
	if err := snapshot.check(); err != nil {
		return nil, err
	}
	return result, nil
}

func (snapshot *mutationHistorySnapshot) query(ctx context.Context, model policyir.ModelID, statement mutationbatch.Statement, fields []golem.FieldID, maxRows uint32) ([]mutationdecode.Row, error) {
	order := make([]policyir.FieldID, len(statement.Columns()))
	for index, column := range statement.Columns() {
		order[index] = column.FieldID()
	}
	decoder, err := readdecode.NewFields(model, snapshot.registry, snapshot.provider, order)
	if err != nil {
		return nil, err
	}
	recordQueryerStatement(ctx, snapshot.queryer)
	rows, err := snapshot.queryer.QueryxContext(ctx, statement.SQL(), batchStatementArguments(statement)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []mutationdecode.Row
	for rows.Next() {
		if uint64(len(result)) >= uint64(maxRows) {
			return nil, &mutationLimitError{limit: golem.LimitPolicyHistoryNodes, max: int(maxRows)}
		}
		if snapshot.nodes >= maxMutationHistoryNodes {
			return nil, &mutationLimitError{limit: golem.LimitPolicyHistoryNodes, max: maxMutationHistoryNodes}
		}
		scan := decoder.NewScan()
		if err := rows.Scan(scan.Destinations()...); err != nil {
			return nil, err
		}
		cells, err := scan.Decode()
		if err != nil {
			return nil, err
		}
		row, err := mutationdecode.FromReadCells(snapshot.registry, model, cells)
		if err != nil {
			return nil, err
		}
		projected, size, err := projectMutationHistory(snapshot.registry, &row, fields)
		if err != nil {
			return nil, err
		}
		if size > maxMutationHistoryBytes-snapshot.bytes {
			return nil, &mutationLimitError{limit: golem.LimitPolicyHistoryBytes, max: maxMutationHistoryBytes}
		}
		snapshot.bytes += size
		snapshot.nodes++
		result = append(result, *projected)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (snapshot *mutationHistorySnapshot) matches(row mutationdecode.Row, equalities []mutationdecode.Cell) (bool, error) {
	fields := make([]evaluate.Field, 0, len(row.Cells()))
	for _, cell := range row.Cells() {
		if cell.IsNull() {
			fields = append(fields, evaluate.NullField(cell.FieldID()))
			continue
		}
		value, _ := cell.PolicyValue()
		field, err := evaluate.ValueField(cell.FieldID(), value)
		if err != nil {
			return false, err
		}
		fields = append(fields, field)
	}
	record, err := evaluate.NewRecord(row.ModelID(), fields...)
	if err != nil {
		return false, err
	}
	providers, err := policyir.NewProviderSet(snapshot.provider)
	if err != nil {
		return false, err
	}
	resolver := policysql.SchemaResolver(snapshot.registry)
	for _, cell := range equalities {
		field, present := resolver.Field(snapshot.provider, row.ModelID(), cell.FieldID())
		if !present {
			return false, fmt.Errorf("policy history: equality descriptor is absent")
		}
		operation, operand := policyir.OperatorIsNull, policyir.NoOperand()
		if !cell.IsNull() {
			operation = policyir.OperatorEqual
			value, _ := cell.PolicyValue()
			operand, err = policyir.OneOperand(value)
			if err != nil {
				return false, err
			}
		}
		requirements, err := operator.ValidateShape(operation, operator.Shape{Node: policyir.ConditionScalar, FieldType: field.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: providers})
		if err != nil {
			return false, err
		}
		condition, err := policyir.NewScalar(row.ModelID(), cell.FieldID(), field.Type, operation, policyir.ComparisonSensitive, operand, requirements)
		if err != nil {
			return false, err
		}
		matched, err := evaluate.Condition(condition, record, providers)
		if err != nil || !matched {
			return false, err
		}
	}
	return true, nil
}
