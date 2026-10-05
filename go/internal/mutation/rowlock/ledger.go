package rowlock

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"

	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/jmoiron/sqlx"
)

const (
	guardPrefix = "\x00"
	rowPrefix   = "\x01"
	lockAlias   = physical.PhysicalName("golem_lock")
)

type Mode uint8

const (
	KeyShare Mode = iota + 1
	Update
)

func (mode Mode) clause() string {
	if mode == KeyShare {
		return " FOR KEY SHARE"
	}
	return " FOR UPDATE"
}

type Key struct {
	order    string
	mode     Mode
	model    policyir.ModelID
	identity mutationdecode.Identity
}

func (key Key) ModelID() policyir.ModelID { return key.model }

func RowKey(registry *schema.Registry, row mutationdecode.Row) (Key, error) {
	identity, err := mutationdecode.PrimaryIdentity(registry, row)
	if err != nil {
		return Key{}, fmt.Errorf("P4_ROW_LOCK_IDENTITY: %w", err)
	}
	return identityKey(row.ModelID(), identity, Update)
}

func identityKey(model policyir.ModelID, identity mutationdecode.Identity, mode Mode) (Key, error) {
	encoded, err := mutationfact.EncodeIdentity(identity)
	if err != nil {
		return Key{}, fmt.Errorf("P4_ROW_LOCK_IDENTITY: %w", err)
	}
	return Key{order: rowPrefix + string(model[:]) + string(encoded), mode: mode, model: model, identity: identity}, nil
}

func RowKeys(registry *schema.Registry, rows []mutationdecode.Row) ([]Key, error) {
	keys := make([]Key, len(rows))
	for index, row := range rows {
		key, err := RowKey(registry, row)
		if err != nil {
			return nil, err
		}
		keys[index] = key
	}
	return keys, nil
}

func guardOrder(token [32]byte) string { return guardPrefix + string(token[:]) }

func GuardLockKey(token [32]byte) int64 { return int64(binary.BigEndian.Uint64(token[:8])) }

type ConflictError struct {
	Model  policyir.ModelID
	Detail string
}

func (failure *ConflictError) Error() string {
	return fmt.Sprintf("P4_ROW_LOCK_CONFLICT: model=%x: %s", failure.Model, failure.Detail)
}

type ContentionError struct{}

func (failure *ContentionError) Error() string {
	return "P4_ROW_LOCK_CONTENTION: an out-of-order lock is held by another transaction"
}

type acquisition struct {
	order    string
	previous Mode
}

type Ledger struct {
	mu   sync.Mutex
	held map[string]Mode
	log  []acquisition
}

func NewLedger() *Ledger { return &Ledger{held: map[string]Mode{}} }

func (ledger *Ledger) Mark() int {
	if ledger == nil {
		return 0
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return len(ledger.log)
}

func (ledger *Ledger) RollbackTo(mark int) {
	if ledger == nil {
		return
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if mark < 0 || mark >= len(ledger.log) {
		return
	}
	for index := len(ledger.log) - 1; index >= mark; index-- {
		entry := ledger.log[index]
		if entry.previous == 0 {
			delete(ledger.held, entry.order)
		} else {
			ledger.held[entry.order] = entry.previous
		}
	}
	ledger.log = ledger.log[:mark]
}

func (ledger *Ledger) snapshot() (map[string]Mode, string) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	held := make(map[string]Mode, len(ledger.held))
	highest := ""
	for order, mode := range ledger.held {
		held[order] = mode
		if order > highest {
			highest = order
		}
	}
	return held, highest
}

func (ledger *Ledger) record(keys []Key) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	for _, key := range keys {
		previous := ledger.held[key.order]
		if previous >= key.mode {
			continue
		}
		ledger.held[key.order] = key.mode
		ledger.log = append(ledger.log, acquisition{order: key.order, previous: previous})
	}
}

type Session struct {
	Ledger        *Ledger
	Queryer       sqlx.QueryerContext
	Registry      *schema.Registry
	Provider      policyir.Provider
	MaxParameters uint32
}

func (session Session) postgres() error {
	if session.Provider != policyir.ProviderPostgreSQL {
		return fmt.Errorf("P4_ROW_LOCK_PROVIDER: row locks are taken only on PostgreSQL")
	}
	if session.Ledger == nil || session.Queryer == nil || session.Registry == nil || session.MaxParameters == 0 {
		return fmt.Errorf("P4_ROW_LOCK_SESSION: ledger, queryer, registry, and parameter bound are required")
	}
	return nil
}

type pendingLock struct {
	key    Key
	nowait bool
}

func (session Session) Lock(ctx context.Context, keys []Key) (bool, error) {
	if err := session.postgres(); err != nil {
		return false, err
	}
	held, highest := session.Ledger.snapshot()
	var pending []pendingLock
	for _, key := range strongestSorted(keys) {
		current, present := held[key.order]
		if present && current >= key.mode {
			continue
		}
		pending = append(pending, pendingLock{key: key, nowait: present || key.order < highest})
	}
	if len(pending) == 0 {
		return false, nil
	}
	for start := 0; start < len(pending); {
		end := start + 1
		for end < len(pending) && pending[end].key.model == pending[start].key.model && pending[end].key.mode == pending[start].key.mode && pending[end].nowait == pending[start].nowait {
			end++
		}
		group := make([]Key, end-start)
		for index := range group {
			group[index] = pending[start+index].key
		}
		if err := session.lockModel(ctx, group, pending[start].nowait); err != nil {
			return false, err
		}
		start = end
	}
	return true, nil
}

func (session Session) lockModel(ctx context.Context, keys []Key, nowait bool) error {
	model := keys[0].model
	resolver := policysql.SchemaResolver(session.Registry)
	physicalModel, ok := resolver.Model(session.Provider, model)
	if !ok {
		return fmt.Errorf("P4_ROW_LOCK_SCHEMA: model %x has no physical descriptor", model)
	}
	width := len(keys[0].identity.Components())
	if width == 0 {
		return fmt.Errorf("P4_ROW_LOCK_IDENTITY: model %x identity is empty", model)
	}
	chunk := int(session.MaxParameters) / width
	if chunk < 1 {
		return fmt.Errorf("P4_ROW_LOCK_LIMIT: parameter bound cannot hold one identity of model %x", model)
	}
	for start := 0; start < len(keys); start += chunk {
		end := min(start+chunk, len(keys))
		if err := session.lockChunk(ctx, resolver, physicalModel, keys[start:end], nowait); err != nil {
			return err
		}
	}
	return nil
}

func (session Session) lockChunk(ctx context.Context, resolver policysql.Resolver, physicalModel policysql.Model, keys []Key, nowait bool) error {
	dialect := postgresprovider.NewPolicyDialect()
	args := make([]any, 0, len(keys)*len(keys[0].identity.Components()))
	matches := make([]string, len(keys))
	order := make([]string, len(keys))
	for index, key := range keys {
		components := key.identity.Components()
		parts := make([]string, len(components))
		for position, component := range components {
			value, valued := component.PolicyValue()
			field, found := resolver.Field(session.Provider, key.model, component.FieldID())
			if component.IsNull() || !valued || !found {
				return fmt.Errorf("P4_ROW_LOCK_IDENTITY: identity component %x of model %x is not lockable", component.FieldID(), key.model)
			}
			encoded, err := encode(dialect, resolver, field.Type, value)
			if err != nil {
				return fmt.Errorf("P4_ROW_LOCK_IDENTITY: identity component %x of model %x cannot be encoded: %w", component.FieldID(), key.model, err)
			}
			args = append(args, encoded)
			parts[position] = dialect.Quote(lockAlias) + "." + dialect.Quote(field.Column) + " = " + dialect.Placeholder(len(args))
		}
		matches[index] = "(" + strings.Join(parts, " AND ") + ")"
		order[index] = fmt.Sprintf("WHEN %s THEN %d", matches[index], index)
	}
	ordinal := "CASE " + strings.Join(order, " ") + " END"
	suffix := keys[0].mode.clause()
	if nowait {
		suffix += " NOWAIT"
	}
	text := "SELECT " + ordinal + " FROM " + dialect.Table(physicalModel) + " AS " + dialect.Quote(lockAlias) + " WHERE " + strings.Join(matches, " OR ") + " ORDER BY " + ordinal + suffix
	rows, err := session.Queryer.QueryxContext(ctx, text, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var locked []Key
	for rows.Next() {
		var index int64
		if err := rows.Scan(&index); err != nil {
			return err
		}
		if index < 0 || int(index) >= len(keys) {
			return fmt.Errorf("P4_ROW_LOCK_RESULT: lock statement returned ordinal %d of %d", index, len(keys))
		}
		locked = append(locked, keys[index])
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if keys[0].mode == Update && len(locked) != len(keys) {
		return &ConflictError{Model: keys[0].model, Detail: fmt.Sprintf("%d of %d enumerated rows remained to lock", len(locked), len(keys))}
	}
	session.Ledger.record(locked)
	return nil
}

func (session Session) Guard(ctx context.Context, token [32]byte) error {
	if err := session.postgres(); err != nil {
		return err
	}
	order := guardOrder(token)
	held, highest := session.Ledger.snapshot()
	if _, present := held[order]; present {
		return nil
	}
	if highest > order {
		var acquired bool
		if err := session.Queryer.QueryRowxContext(ctx, "SELECT pg_catalog.pg_try_advisory_xact_lock($1)", GuardLockKey(token)).Scan(&acquired); err != nil {
			return err
		}
		if !acquired {
			return &ContentionError{}
		}
	} else {
		rows, err := session.Queryer.QueryxContext(ctx, "SELECT pg_catalog.pg_advisory_xact_lock($1)", GuardLockKey(token))
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	session.Ledger.record([]Key{{order: order, mode: Update}})
	return nil
}

type enumeratedFaultKey struct{}

func WithEnumeratedFault(ctx context.Context, fault func(context.Context) error) context.Context {
	return context.WithValue(ctx, enumeratedFaultKey{}, fault)
}

func Select[T any](ctx context.Context, session Session, enumerate func(context.Context) (T, []Key, error)) (T, error) {
	var zero T
	first, keys, err := enumerate(ctx)
	if err != nil || session.Provider != policyir.ProviderPostgreSQL {
		return first, err
	}
	if fault, ok := ctx.Value(enumeratedFaultKey{}).(func(context.Context) error); ok && fault != nil {
		if err := fault(ctx); err != nil {
			return zero, err
		}
	}
	acquired, err := session.Lock(ctx, keys)
	if err != nil {
		return zero, err
	}
	if !acquired {
		return first, nil
	}
	second, again, err := enumerate(ctx)
	if err != nil {
		return zero, err
	}
	if !sameOrders(keys, again) {
		model := policyir.ModelID{}
		if len(keys) != 0 {
			model = keys[0].model
		}
		return zero, &ConflictError{Model: model, Detail: "the selected rows changed between enumeration and locking"}
	}
	return second, nil
}

func strongestSorted(keys []Key) []Key {
	result := append([]Key(nil), keys...)
	sort.SliceStable(result, func(i, j int) bool { return result[i].order < result[j].order })
	unique := result[:0]
	for _, key := range result {
		if len(unique) != 0 && unique[len(unique)-1].order == key.order {
			if key.mode > unique[len(unique)-1].mode {
				unique[len(unique)-1].mode = key.mode
			}
			continue
		}
		unique = append(unique, key)
	}
	return unique
}

func sameOrders(left, right []Key) bool {
	leftKeys, rightKeys := strongestSorted(left), strongestSorted(right)
	if len(leftKeys) != len(rightKeys) {
		return false
	}
	for index := range leftKeys {
		if leftKeys[index].order != rightKeys[index].order {
			return false
		}
	}
	return true
}

func encode(dialect policysql.Dialect, resolver policysql.Resolver, typ policyir.TypeRef, value policyir.Value) (any, error) {
	bound := policysql.BoundValue{Value: value, Type: typ}
	if typ.Kind() == policyir.ValueEnum {
		enum, member, ok := value.Enum()
		if !ok {
			return nil, fmt.Errorf("enum value is invalid")
		}
		wire, found := resolver.EnumWire(enum, member)
		if !found {
			return nil, fmt.Errorf("enum wire value is absent")
		}
		bound.EnumWires = []string{wire}
	}
	return dialect.Encode(bound)
}
