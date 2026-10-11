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

func (ledger *Ledger) holds(key Key) bool {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.held[key.order] >= key.mode
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

type Reader struct {
	Alias   physical.PhysicalName
	Columns string
	Match   string
	Args    []any
}

type lockedRow[R any] struct {
	key     Key
	row     R
	matched bool
}

func (session Session) Lock(ctx context.Context, keys []Key) (bool, error) {
	if err := session.postgres(); err != nil {
		return false, err
	}
	pending := plan(session.Ledger, keys)
	if len(pending) == 0 {
		return false, nil
	}
	scan := func(rows *sqlx.Rows, trailing ...any) (struct{}, error) { return struct{}{}, rows.Scan(trailing...) }
	for start := 0; start < len(pending); {
		end := start + 1
		for end < len(pending) && sameRun(pending[end], pending[start]) {
			end++
		}
		if _, err := lockRun(ctx, session, keysOf(pending[start:end]), pending[start].nowait, nil, scan); err != nil {
			return false, err
		}
		start = end
	}
	return true, nil
}

func sameRun(left, right pendingLock) bool {
	return left.key.model == right.key.model && left.key.mode == right.key.mode && left.nowait == right.nowait
}

func keysOf(pending []pendingLock) []Key {
	keys := make([]Key, len(pending))
	for index := range pending {
		keys[index] = pending[index].key
	}
	return keys
}

func plan(ledger *Ledger, keys []Key) []pendingLock {
	held, highest := ledger.snapshot()
	var pending []pendingLock
	for _, key := range strongestSorted(keys) {
		current, present := held[key.order]
		if present && current >= key.mode {
			continue
		}
		pending = append(pending, pendingLock{key: key, nowait: present || key.order < highest})
	}
	return pending
}

func lockRun[R any](ctx context.Context, session Session, keys []Key, nowait bool, reader *Reader, scan func(*sqlx.Rows, ...any) (R, error)) ([]lockedRow[R], error) {
	model := keys[0].model
	resolver := policysql.SchemaResolver(session.Registry)
	physicalModel, ok := resolver.Model(session.Provider, model)
	if !ok {
		return nil, fmt.Errorf("P4_ROW_LOCK_SCHEMA: model %x has no physical descriptor", model)
	}
	width := len(keys[0].identity.Components())
	if width == 0 {
		return nil, fmt.Errorf("P4_ROW_LOCK_IDENTITY: model %x identity is empty", model)
	}
	fixed := 0
	if reader != nil {
		fixed = len(reader.Args)
	}
	chunk := (int(session.MaxParameters) - fixed) / width
	if chunk < 1 {
		return nil, fmt.Errorf("P4_ROW_LOCK_LIMIT: parameter bound cannot hold one identity of model %x", model)
	}
	var locked []lockedRow[R]
	for start := 0; start < len(keys); start += chunk {
		end := min(start+chunk, len(keys))
		rows, err := lockChunk(ctx, session, resolver, physicalModel, keys[start:end], nowait, reader, scan)
		if err != nil {
			return nil, err
		}
		locked = append(locked, rows...)
	}
	return locked, nil
}

func lockChunk[R any](ctx context.Context, session Session, resolver policysql.Resolver, physicalModel policysql.Model, keys []Key, nowait bool, reader *Reader, scan func(*sqlx.Rows, ...any) (R, error)) ([]lockedRow[R], error) {
	dialect := postgresprovider.NewPolicyDialect()
	alias, columns := lockAlias, "1"
	var args []any
	if reader != nil {
		alias, columns = reader.Alias, reader.Columns+", CASE WHEN ("+reader.Match+") THEN 1 ELSE 0 END"
		args = append(args, reader.Args...)
	}
	matches := make([]string, len(keys))
	order := make([]string, len(keys))
	for index, key := range keys {
		components := key.identity.Components()
		parts := make([]string, len(components))
		for position, component := range components {
			value, valued := component.PolicyValue()
			field, found := resolver.Field(session.Provider, key.model, component.FieldID())
			if component.IsNull() || !valued || !found {
				return nil, fmt.Errorf("P4_ROW_LOCK_IDENTITY: identity component %x of model %x is not lockable", component.FieldID(), key.model)
			}
			encoded, err := encode(dialect, resolver, field.Type, value)
			if err != nil {
				return nil, fmt.Errorf("P4_ROW_LOCK_IDENTITY: identity component %x of model %x cannot be encoded: %w", component.FieldID(), key.model, err)
			}
			args = append(args, encoded)
			parts[position] = dialect.Quote(alias) + "." + dialect.Quote(field.Column) + " = " + dialect.Placeholder(len(args))
		}
		matches[index] = "(" + strings.Join(parts, " AND ") + ")"
		order[index] = fmt.Sprintf("WHEN %s THEN %d", matches[index], index)
	}
	ordinal := "CASE " + strings.Join(order, " ") + " END"
	suffix := keys[0].mode.clause()
	if nowait {
		suffix += " NOWAIT"
	}
	text := "SELECT " + columns + ", " + ordinal + " FROM " + dialect.Table(physicalModel) + " AS " + dialect.Quote(alias) + " WHERE " + strings.Join(matches, " OR ") + " ORDER BY " + ordinal + suffix
	rows, err := session.Queryer.QueryxContext(ctx, text, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []lockedRow[R]
	var locked []Key
	for rows.Next() {
		var matched, index int64
		row, err := scan(rows, &matched, &index)
		if err != nil {
			session.Ledger.record(locked)
			return nil, err
		}
		if index < 0 || int(index) >= len(keys) {
			session.Ledger.record(locked)
			return nil, fmt.Errorf("P4_ROW_LOCK_RESULT: lock statement returned ordinal %d of %d", index, len(keys))
		}
		locked = append(locked, keys[index])
		result = append(result, lockedRow[R]{key: keys[index], row: row, matched: matched == 1})
	}
	session.Ledger.record(locked)
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
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

func enumeratedFault(ctx context.Context) error {
	if fault, ok := ctx.Value(enumeratedFaultKey{}).(func(context.Context) error); ok && fault != nil {
		return fault(ctx)
	}
	return nil
}

func Select[T any](ctx context.Context, session Session, enumerate func(ctx context.Context) (T, []Key, error)) (T, error) {
	var zero T
	first, keys, err := enumerate(ctx)
	if err != nil || session.Provider != policyir.ProviderPostgreSQL {
		return first, err
	}
	if err := enumeratedFault(ctx); err != nil {
		return zero, err
	}
	acquired, err := session.Lock(ctx, keys)
	if err != nil {
		return zero, err
	}
	if !acquired {
		return first, nil
	}
	requested := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key.mode == KeyShare {
			requested[key.order] = struct{}{}
		}
	}
	second, again, err := enumerate(ctx)
	if err != nil {
		return zero, err
	}
	for _, key := range again {
		if _, reference := requested[key.order]; session.Ledger.holds(key) || key.mode == KeyShare && reference {
			continue
		}
		return zero, &ConflictError{Model: key.model, Detail: "a row matched after enumeration and is not locked by this transaction"}
	}
	return second, nil
}

func SelectMatching[R any](ctx context.Context, session Session, readers []Reader, enumerate func(ctx context.Context, reader int) ([]R, []Key, error), scan func(reader int, rows *sqlx.Rows, trailing ...any) (R, error)) ([][]R, error) {
	enumerated, keys := make([][]R, len(readers)), make([][]Key, len(readers))
	var all []Key
	for reader := range readers {
		rows, rowKeys, err := enumerate(ctx, reader)
		if err != nil {
			return nil, err
		}
		if len(rows) != len(rowKeys) {
			return nil, fmt.Errorf("P4_ROW_LOCK_INVARIANT: reader %d enumerated %d rows with %d keys", reader, len(rows), len(rowKeys))
		}
		enumerated[reader], keys[reader], all = rows, rowKeys, append(all, rowKeys...)
	}
	if session.Provider != policyir.ProviderPostgreSQL {
		return enumerated, nil
	}
	if err := enumeratedFault(ctx); err != nil {
		return nil, err
	}
	if err := session.postgres(); err != nil {
		return nil, err
	}
	pending := plan(session.Ledger, all)
	if len(pending) == 0 {
		return enumerated, nil
	}
	merged := make([]bool, len(readers))
	for reader := range readers {
		merged[reader] = len(keys[reader]) != 0 && readers[reader].mergeable(session.MaxParameters, len(keys[reader][0].identity.Components()))
	}
	enumeratedBy := make(map[string][]int, len(all))
	for reader, readerKeys := range keys {
		for _, key := range readerKeys {
			if owners := enumeratedBy[key.order]; len(owners) == 0 || owners[len(owners)-1] != reader {
				enumeratedBy[key.order] = append(owners, reader)
			}
		}
	}
	type entry struct {
		lock   pendingLock
		reader int
	}
	var entries []entry
	for _, lock := range pending {
		fresh := false
		for _, reader := range enumeratedBy[lock.key.order] {
			if merged[reader] {
				entries = append(entries, entry{lock: lock, reader: reader})
			} else if !fresh {
				fresh = true
				entries = append(entries, entry{lock: lock, reader: -1})
			}
		}
	}
	locked := make([]map[string]R, len(readers))
	relocked := make([]map[string]struct{}, len(readers))
	for reader := range readers {
		locked[reader], relocked[reader] = map[string]R{}, map[string]struct{}{}
	}
	plain := func(rows *sqlx.Rows, trailing ...any) (struct{}, error) { return struct{}{}, rows.Scan(trailing...) }
	for start := 0; start < len(entries); {
		end := start + 1
		for end < len(entries) && entries[end].reader == entries[start].reader && sameRun(entries[end].lock, entries[start].lock) {
			end++
		}
		reader := entries[start].reader
		group := make([]Key, end-start)
		for index := range group {
			group[index] = entries[start+index].lock.key
		}
		if reader < 0 {
			if _, err := lockRun(ctx, session, group, entries[start].lock.nowait, nil, plain); err != nil {
				return nil, err
			}
			start = end
			continue
		}
		for _, key := range group {
			relocked[reader][key.order] = struct{}{}
		}
		rows, err := lockRun(ctx, session, group, entries[start].lock.nowait, &readers[reader], func(rows *sqlx.Rows, trailing ...any) (R, error) {
			return scan(reader, rows, trailing...)
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.matched {
				locked[reader][row.key.order] = row.row
			}
		}
		start = end
	}
	result := make([][]R, len(readers))
	for reader, readerKeys := range keys {
		if len(readerKeys) != 0 && !merged[reader] {
			rows, rowKeys, err := enumerate(ctx, reader)
			if err != nil {
				return nil, err
			}
			if len(rows) != len(rowKeys) {
				return nil, fmt.Errorf("P4_ROW_LOCK_INVARIANT: reader %d enumerated %d rows with %d keys", reader, len(rows), len(rowKeys))
			}
			for index, key := range rowKeys {
				if session.Ledger.holds(key) {
					result[reader] = append(result[reader], rows[index])
				}
			}
			continue
		}
		for position, key := range readerKeys {
			if _, read := relocked[reader][key.order]; !read {
				result[reader] = append(result[reader], enumerated[reader][position])
			} else if row, matched := locked[reader][key.order]; matched {
				result[reader] = append(result[reader], row)
			}
		}
	}
	return result, nil
}

func (reader Reader) mergeable(maxParameters uint32, width int) bool {
	return width != 0 && int(maxParameters)-len(reader.Args) >= width && readsOnlyTheTargetRow(reader.Columns) && readsOnlyTheTargetRow(reader.Match)
}

var targetRowCalls = map[string]struct{}{"COALESCE": {}, "NULLIF": {}, "GREATEST": {}, "LEAST": {}, "IN": {}, "AND": {}, "OR": {}, "NOT": {}, "WHEN": {}, "THEN": {}, "ELSE": {}, "CASE": {}, "IS": {}, "ANY": {}, "ALL": {}, "ARRAY": {}, "ROW": {}}

var otherRowWords = map[string]struct{}{"SELECT": {}, "FROM": {}, "JOIN": {}, "TABLE": {}, "LATERAL": {}, "VALUES": {}}

func wordRune(character rune) bool {
	return character == '_' || character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
}

func readsOnlyTheTargetRow(text string) bool {
	runes := []rune(text)
	for index := 0; index < len(runes); {
		switch character := runes[index]; {
		case character == '"' || character == '\'':
			closing := index + 1
			for closing < len(runes) {
				if runes[closing] == character {
					if closing+1 < len(runes) && runes[closing+1] == character {
						closing += 2
						continue
					}
					break
				}
				closing++
			}
			if closing >= len(runes) {
				return false
			}
			index = closing + 1
		case character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z':
			start := index
			for index < len(runes) && (wordRune(runes[index]) || runes[index] == '$') {
				index++
			}
			word := strings.ToUpper(string(runes[start:index]))
			if _, other := otherRowWords[word]; other {
				return false
			}
			next := index
			for next < len(runes) && (runes[next] == ' ' || runes[next] == '\t' || runes[next] == '\n') {
				next++
			}
			if next < len(runes) && runes[next] == '(' {
				_, allowed := targetRowCalls[word]
				qualified := start > len("pg_catalog") && runes[start-1] == '.' && string(runes[start-1-len("pg_catalog"):start-1]) == "pg_catalog" && (start-1-len("pg_catalog") == 0 || !wordRune(runes[start-2-len("pg_catalog")]))
				cast := start > 1 && runes[start-1] == ':' && runes[start-2] == ':'
				if !allowed && !qualified && !cast {
					return false
				}
			}
		default:
			index++
		}
	}
	return true
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
