package runtime

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/jmoiron/sqlx"
)

type mutationState struct {
	mu sync.Mutex

	limits     normalizedMutationLimits
	causation  mutationfact.CausationID
	ordinal    uint32
	touched    int
	bytes      int
	dirty      bool
	flushed    bool
	finished   bool
	failure    error
	facts      []mutationfact.OutboxRow
	marks      []semanticMark
	markIndex  map[semanticMarkIdentity]struct{}
	after      []mutationAfterCommit
	invalidate func()
}

type mutationAfterCommit struct {
	operation golem.HookOperation
	model     golem.ModelID
	invoke    func(context.Context) error
}

type mutationScope struct {
	state      *mutationState
	ordinal    uint32
	touched    int
	bytes      int
	dirty      bool
	facts      int
	marks      int
	after      int
	failure    error
	rolledBack bool
	closed     bool
}

func newMutationState(limits normalizedMutationLimits, causation mutationfact.CausationID) (*mutationState, error) {
	if causation == (mutationfact.CausationID{}) {
		if _, err := cryptorand.Read(causation[:]); err != nil {
			return nil, fmt.Errorf("P4_MUTATION_STATE: create causation identity: %w", err)
		}
		causation[6] = causation[6]&0x0f | 0x40
		causation[8] = causation[8]&0x3f | 0x80
	}
	return &mutationState{limits: limits, causation: causation}, nil
}

func (state *mutationState) beginScope() (*mutationScope, error) {
	if state == nil {
		return nil, fmt.Errorf("P4_MUTATION_STATE: transaction state is unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.flushed || state.finished {
		return nil, fmt.Errorf("P4_MUTATION_STATE: transaction state is already finalized")
	}
	return &mutationScope{state: state, ordinal: state.ordinal, touched: state.touched, bytes: state.bytes, dirty: state.dirty, facts: len(state.facts), marks: len(state.marks), after: len(state.after), failure: state.failure}, nil
}

func (scope *mutationScope) release() error {
	if scope == nil || scope.state == nil || scope.closed {
		return fmt.Errorf("P4_MUTATION_STATE: mutation scope is unavailable")
	}
	scope.closed = true
	return nil
}

func (scope *mutationScope) rollback() error {
	if scope == nil || scope.state == nil || scope.closed {
		return fmt.Errorf("P4_MUTATION_STATE: mutation scope is unavailable")
	}
	state := scope.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.flushed || state.finished {
		return fmt.Errorf("P4_MUTATION_STATE: transaction state is already finalized")
	}
	state.ordinal = scope.ordinal
	state.touched = scope.touched
	state.bytes = scope.bytes
	state.dirty = scope.dirty
	state.facts = state.facts[:scope.facts]
	state.rewindMarks(scope.marks)
	state.after = state.after[:scope.after]
	state.failure = scope.failure
	scope.rolledBack = true
	scope.closed = true
	return nil
}

// markSemantic records that one semantic-indexed record was written inside this
// transaction. Marks are deduplicated per transaction, so a row rewritten many
// times still costs one shadow-state row and the buffer never emits more than
// one drain job per index.
func (state *mutationState) markSemantic(model golem.ModelID, key string, identity []any) error {
	if state == nil || model == (golem.ModelID{}) || key == "" || len(identity) == 0 {
		return fmt.Errorf("P9_SEMANTIC_MARK: semantic mark is incomplete")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.flushed || state.finished {
		return fmt.Errorf("P4_MUTATION_STATE: transaction state is already finalized")
	}
	identifier := semanticMarkIdentity{model: model, key: key}
	if _, duplicate := state.markIndex[identifier]; duplicate {
		return nil
	}
	if state.markIndex == nil {
		state.markIndex = make(map[semanticMarkIdentity]struct{})
	}
	state.markIndex[identifier] = struct{}{}
	state.marks = append(state.marks, semanticMark{model: model, key: key, identity: identity})
	return nil
}

// rewindMarks drops the marks a rolled-back scope contributed. The caller holds
// the state lock.
func (state *mutationState) rewindMarks(count int) {
	for _, mark := range state.marks[count:] {
		delete(state.markIndex, semanticMarkIdentity{model: mark.model, key: mark.key})
	}
	state.marks = state.marks[:count]
}

func (state *mutationState) semanticMarks() []semanticMark {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return append([]semanticMark(nil), state.marks...)
}

func (state *mutationState) touch(rows int) error {
	if state == nil || rows < 0 {
		return fmt.Errorf("P4_MUTATION_STATE: touched row count is invalid")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.flushed || state.finished {
		return fmt.Errorf("P4_MUTATION_STATE: transaction state is already finalized")
	}
	if rows > state.limits.touchedRows-state.touched {
		return fmt.Errorf("P4_MUTATION_LIMIT: touched rows exceed %d", state.limits.touchedRows)
	}
	state.touched += rows
	state.dirty = state.dirty || rows != 0
	return nil
}

// buildFact allocates the next transaction ordinal only after the exact fact
// bytes fit all execution-wide limits. Failed construction cannot leave a gap.
func (state *mutationState) buildFact(registry *schema.Registry, requirement mutationir.FactRequirement, before, after *mutationdecode.Row, recordedAt time.Time) (mutationfact.OutboxRow, error) {
	if state == nil {
		return mutationfact.OutboxRow{}, fmt.Errorf("P4_MUTATION_STATE: transaction state is unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.flushed || state.finished {
		return mutationfact.OutboxRow{}, fmt.Errorf("P4_MUTATION_STATE: transaction state is already finalized")
	}
	if len(state.facts) >= state.limits.facts {
		return mutationfact.OutboxRow{}, fmt.Errorf("P4_MUTATION_LIMIT: facts exceed %d", state.limits.facts)
	}
	if state.ordinal == ^uint32(0) {
		return mutationfact.OutboxRow{}, fmt.Errorf("P4_MUTATION_LIMIT: transaction ordinal exhausted")
	}
	ordinal := state.ordinal + 1
	event, err := newMutationEventID()
	if err != nil {
		return mutationfact.OutboxRow{}, err
	}
	var envelope mutationfact.Envelope
	if eventSchema, present := requirement.EventSchema(); present {
		envelope, err = mutationfact.NewV2(registry, golem.SchemaDigest(eventSchema), event, requirement, state.causation, ordinal, before, after)
	} else {
		envelope, err = mutationfact.New(registry, event, requirement, state.causation, ordinal, before, after)
	}
	if err != nil {
		return mutationfact.OutboxRow{}, err
	}
	row, err := envelope.OutboxRow(recordedAt)
	if err != nil {
		return mutationfact.OutboxRow{}, err
	}
	if row.EncodedBytes() > state.limits.outboxBytes-state.bytes {
		return mutationfact.OutboxRow{}, fmt.Errorf("P4_MUTATION_LIMIT: outbox bytes exceed %d", state.limits.outboxBytes)
	}
	state.ordinal = ordinal
	state.bytes += row.EncodedBytes()
	state.facts = append(state.facts, cloneOutboxRow(row))
	state.dirty = true
	return cloneOutboxRow(row), nil
}

// appendOutboxRow is the runtime integration seam for already encoded facts.
// It enforces the same causation, ordinal, count, and exact-byte invariants as
// buildFact and is intentionally private to the runtime package.
func (state *mutationState) appendOutboxRow(row mutationfact.OutboxRow) error {
	if state == nil {
		return fmt.Errorf("P4_MUTATION_STATE: transaction state is unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	wantOrdinal := state.ordinal + 1
	if row.CausationID != formatMutationUUID(state.causation) || row.TransactionOrdinal != int64(wantOrdinal) {
		return fmt.Errorf("P4_MUTATION_STATE: fact causation or ordinal does not match transaction")
	}
	if len(state.facts) >= state.limits.facts {
		return fmt.Errorf("P4_MUTATION_LIMIT: facts exceed %d", state.limits.facts)
	}
	if row.EncodedBytes() > state.limits.outboxBytes-state.bytes {
		return fmt.Errorf("P4_MUTATION_LIMIT: outbox bytes exceed %d", state.limits.outboxBytes)
	}
	state.ordinal = wantOrdinal
	state.bytes += row.EncodedBytes()
	state.facts = append(state.facts, cloneOutboxRow(row))
	state.dirty = true
	return nil
}

func (state *mutationState) beforeParentFactCheckpoint() (int, uint32, error) {
	if state == nil {
		return 0, 0, fmt.Errorf("P4_MUTATION_STATE: transaction state is unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.flushed || state.finished {
		return 0, 0, fmt.Errorf("P4_MUTATION_STATE: transaction state is already finalized")
	}
	return len(state.facts), state.ordinal, nil
}

// completeBeforeParentFacts moves the just-built parent fact ahead of the
// physically earlier dependency segment and canonically re-encodes every
// affected envelope with contiguous declared-graph ordinals.
func (state *mutationState) completeBeforeParentFacts(start int, ordinal uint32, parentFact bool, registry *schema.Registry) error {
	if state == nil || registry == nil || start < 0 {
		return fmt.Errorf("P4_MUTATION_STATE: dependency fact checkpoint is invalid")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.flushed || state.finished || start > len(state.facts) {
		return fmt.Errorf("P4_MUTATION_STATE: dependency fact checkpoint is stale")
	}
	segment := append([]mutationfact.OutboxRow(nil), state.facts[start:]...)
	if parentFact {
		if len(segment) == 0 {
			return fmt.Errorf("P4_MUTATION_STATE: dependency parent fact is absent")
		}
		segment = append([]mutationfact.OutboxRow{segment[len(segment)-1]}, segment[:len(segment)-1]...)
	}
	for index := range segment {
		envelope, err := decodeRuntimeMutationFact(registry, segment[index])
		if err != nil {
			return err
		}
		envelope, err = envelope.WithTransactionOrdinal(ordinal + uint32(index) + 1)
		if err != nil {
			return err
		}
		metadata, err := mutationfact.Encode(envelope)
		if err != nil {
			return err
		}
		segment[index].TransactionOrdinal = int64(ordinal) + int64(index) + 1
		segment[index].Metadata = metadata
	}
	copy(state.facts[start:], segment)
	state.ordinal = ordinal + uint32(len(segment))
	return nil
}

func (state *mutationState) addAfterCommit(operation golem.HookOperation, model golem.ModelID, invoke func(context.Context) error) error {
	if state == nil || operation == "" || model == (golem.ModelID{}) || invoke == nil {
		return fmt.Errorf("P4_MUTATION_STATE: after-commit work is incomplete")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.flushed || state.finished {
		return fmt.Errorf("P4_MUTATION_STATE: transaction state is already finalized")
	}
	state.after = append(state.after, mutationAfterCommit{operation: operation, model: model, invoke: invoke})
	return nil
}

func (state *mutationState) setInvalidation(invalidate func()) error {
	if state == nil || invalidate == nil {
		return fmt.Errorf("P4_MUTATION_STATE: invalidation callback is required")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.invalidate != nil {
		return fmt.Errorf("P4_MUTATION_STATE: invalidation callback is already configured")
	}
	state.invalidate = invalidate
	return nil
}

func (state *mutationState) poison(cause error) {
	if state == nil || cause == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failure == nil && !state.finished {
		state.failure = cause
	}
}

func (state *mutationState) flush(ctx context.Context, executor sqlx.ExecerContext, config executionMutationConfig) error {
	if state == nil || executor == nil {
		return fmt.Errorf("P4_MUTATION_STATE: outbox flush requires transaction state and executor")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.finished {
		return fmt.Errorf("P4_MUTATION_STATE: transaction state is already finalized")
	}
	if state.failure != nil {
		return fmt.Errorf("P4_MUTATION_STATE: transaction contains a failed mutation: %w", state.failure)
	}
	if state.flushed {
		return fmt.Errorf("P4_MUTATION_STATE: outbox was already flushed")
	}
	statements, err := mutationfact.RenderInsertsAt(config.provider, config.outboxNamespace, state.facts, state.limits.statementParameters)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		observeexec.RecordStatement(ctx)
		if _, err := executor.ExecContext(ctx, statement.SQL(), statement.Args()...); err != nil {
			return fmt.Errorf("P4_MUTATION_OUTBOX: insert facts: %w", err)
		}
	}
	if len(state.facts) != 0 {
		delivery, err := mutationfact.RenderDeliveryInsertAt(config.provider, config.outboxNamespace, state.facts)
		if err != nil {
			return err
		}
		observeexec.RecordStatement(ctx)
		if _, err := executor.ExecContext(ctx, delivery.SQL(), delivery.Args()...); err != nil {
			return fmt.Errorf("P7_MUTATION_DELIVERY: insert causal state: %w", err)
		}
	}
	// Semantic marks are written beside the outbox rows and on the same
	// executor, so a record's shadow-state row and the drain job that will
	// consume it commit or roll back with the write that produced them.
	if len(state.marks) != 0 {
		if config.flushSemantic == nil {
			return fmt.Errorf("P9_SEMANTIC_MARK: execution has semantic marks and no semantic flush owner")
		}
		if err := config.flushSemantic(ctx, executor, append([]semanticMark(nil), state.marks...)); err != nil {
			return err
		}
	}
	state.flushed = true
	return nil
}

func (state *mutationState) committed(ctx context.Context, report func(context.Context, golem.AfterCommitFailure)) {
	if state == nil {
		return
	}
	state.mu.Lock()
	if state.finished {
		state.mu.Unlock()
		return
	}
	state.finished = true
	invalidate := state.invalidate
	dirty := state.dirty
	after := append([]mutationAfterCommit(nil), state.after...)
	state.facts = nil
	state.marks, state.markIndex = nil, nil
	state.after = nil
	state.mu.Unlock()

	if dirty && invalidate != nil {
		invalidate()
	}
	for _, work := range after {
		if err := invokeAfterCommitSafely(ctx, work.invoke); err != nil && report != nil {
			reportAfterCommitSafely(ctx, report, golem.RuntimeAfterCommitFailure(work.operation, work.model, err))
		}
	}
}

func invokeAfterCommitSafely(ctx context.Context, invoke func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("P4_RUNTIME_AFTER_COMMIT: hook panicked: %v", recovered)
		}
	}()
	return invoke(ctx)
}

func reportAfterCommitSafely(ctx context.Context, report func(context.Context, golem.AfterCommitFailure), failure golem.AfterCommitFailure) {
	defer func() { _ = recover() }()
	report(ctx, failure)
}

func (state *mutationState) discarded() {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.finished = true
	state.facts = nil
	state.marks, state.markIndex = nil, nil
	state.after = nil
	state.invalidate = nil
}

func newMutationEventID() (mutationfact.EventID, error) {
	var event mutationfact.EventID
	if _, err := cryptorand.Read(event[:]); err != nil {
		return mutationfact.EventID{}, fmt.Errorf("P4_MUTATION_STATE: create event identity: %w", err)
	}
	event[6] = event[6]&0x0f | 0x40
	event[8] = event[8]&0x3f | 0x80
	return event, nil
}

func formatMutationUUID(value mutationfact.CausationID) string {
	const hexadecimal = "0123456789abcdef"
	encoded := make([]byte, 36)
	source, destination := 0, 0
	for source < len(value) {
		if destination == 8 || destination == 13 || destination == 18 || destination == 23 {
			encoded[destination] = '-'
			destination++
		}
		encoded[destination] = hexadecimal[value[source]>>4]
		encoded[destination+1] = hexadecimal[value[source]&15]
		source++
		destination += 2
	}
	return string(encoded)
}

func cloneOutboxRow(row mutationfact.OutboxRow) mutationfact.OutboxRow {
	row.BeforeIdentity = append([]byte(nil), row.BeforeIdentity...)
	row.AfterIdentity = append([]byte(nil), row.AfterIdentity...)
	row.Metadata = append([]byte(nil), row.Metadata...)
	row.DeleteSnapshot = append([]byte(nil), row.DeleteSnapshot...)
	return row
}
