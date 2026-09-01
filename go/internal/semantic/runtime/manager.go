package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

// Manager owns refresh and nearest-neighbour storage operations. One manager
// serializes refreshes so duplicate application requests never call an
// embedding provider for the same observed source generation concurrently.
type Manager struct {
	database *sqlx.DB
	provider ir.Provider
	schema   physical.PhysicalSchema
	indexes  []Index
	observer observe.Observer
	mu       sync.Mutex
}

const MaximumResults = 1000

// semanticStaleProbe bounds how many marked records one drain pass claims, and
// semanticKeyChunk bounds how many record keys one statement binds.
const (
	semanticStaleProbe = 256
	semanticKeyChunk   = 128
	semanticMarkChunk  = 128
	semanticMarkBinds  = 900
)

// IdentityScan decodes one ranked row's owner identity columns. The caller owns
// the typed scan slots so identity values mirrored into shadow storage decode
// exactly like the same columns read from the owner table.
type IdentityScan interface {
	Destinations() []any
	RawValues() []any
}

// Candidates is the authorized candidate subquery joined into one rank
// statement through the owner's real identity columns. Columns names the
// subquery projection in physical key order; ranking refuses any projection
// that does not match the shadow table's identity contract.
type Candidates struct {
	SQL                 string
	Args                []any
	Columns             []string
	Model               policyir.ModelID
	MaxStatementBytes   int
	MaxStatementAliases int
	NewScan             func() IdentityScan
}

type Rank struct {
	Key      string
	Distance float64
	Identity []any
}

func NewManager(database *sqlx.DB, provider ir.Provider, schema physical.PhysicalSchema, inventory Inventory, observers ...observe.Observer) (*Manager, error) {
	if database == nil || provider != ir.SQLite && provider != ir.PostgreSQL {
		return nil, fmt.Errorf("P9_SEMANTIC_RUNTIME: database and provider are required")
	}
	if len(observers) > 1 {
		return nil, fmt.Errorf("P9_SEMANTIC_RUNTIME: at most one observer is allowed")
	}
	var observer observe.Observer
	if len(observers) == 1 {
		observer = observers[0]
	}
	return &Manager{database: database, provider: provider, schema: schema, indexes: inventory.Indexes(), observer: observer}, nil
}

// DrainJobType and ReconcileJobType name the durable job types Golem registers
// for its own index maintenance. They are exported so the write path enqueues
// by symbol rather than by a string literal that could drift from the handler
// it is meant to wake. Applications never register or enqueue them.
const (
	DrainJobType     = "semantic.drain"
	ReconcileJobType = "semantic.reconcile"
)

// IndexRef names one declared semantic index. It is the runtime's handle for
// scheduling work against an index without exposing the embedding provider or
// the physical storage behind it.
type IndexRef struct {
	Model ir.ModelID
	Name  string
}

func (manager *Manager) IndexRefs() []IndexRef {
	if manager == nil {
		return nil
	}
	result := make([]IndexRef, 0, len(manager.indexes))
	for _, index := range manager.indexes {
		result = append(result, IndexRef{Model: index.Descriptor.ModelID, Name: index.Descriptor.Name})
	}
	return result
}

func (manager *Manager) RefreshAll(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return fmt.Errorf("P9_SEMANTIC_RUNTIME: context and manager are required")
	}
	return manager.reconcileIndexes(ctx, manager.indexes)
}

func (manager *Manager) Refresh(ctx context.Context, model ir.ModelID, name string) error {
	if manager == nil || ctx == nil || model == "" || name == "" {
		return fmt.Errorf("P9_SEMANTIC_RUNTIME: context, manager, model, and index are required")
	}
	selected, ok := manager.index(model, name)
	if !ok {
		return fmt.Errorf("P9_SEMANTIC_SCHEMA: semantic index is absent")
	}
	return manager.reconcileIndexes(ctx, []Index{selected})
}

// Drain advances one index towards its sources without reading the owner table
// as a whole. It reports whether stale records remained once the pass finished:
// the queue coalesces an enqueue against the job that is still holding its
// lease, so a mark committed during the pass can only reach a worker if this
// pass hands its own successor forward.
func (manager *Manager) Drain(ctx context.Context, model ir.ModelID, name string) (bool, error) {
	if manager == nil || ctx == nil || model == "" || name == "" {
		return false, fmt.Errorf("P9_SEMANTIC_RUNTIME: context, manager, model, and index are required")
	}
	selected, ok := manager.index(model, name)
	if !ok {
		return false, fmt.Errorf("P9_SEMANTIC_SCHEMA: semantic index is absent")
	}
	deferred := observeexec.NewDeferredObserver(manager.observer)
	var pending bool
	err := func() error {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		observed := semanticObservationModel(selected.Descriptor.ModelID)
		drainContext, drainSpan := observeexec.Begin(ctx, deferred, golem.Provider(manager.provider), observed, observe.KindSemantic, observe.OperationSemanticRefresh, observe.PhaseFinish)
		remaining, drainErr := manager.refresh(drainContext, selected, drainSpan)
		finishSemanticObservation(drainSpan, drainErr)
		pending = remaining
		return drainErr
	}()
	deferred.Flush()
	return pending, err
}

func (manager *Manager) reconcileIndexes(ctx context.Context, indexes []Index) error {
	// Reconcile serializes provider work, but arbitrary observer code must never
	// be invoked while that lock is held. Buffer closed records and flush only
	// after releasing it so observer re-entry cannot deadlock semantic refresh.
	deferred := observeexec.NewDeferredObserver(manager.observer)
	err := func() error {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		for _, index := range indexes {
			model := semanticObservationModel(index.Descriptor.ModelID)
			refreshContext, refreshSpan := observeexec.Begin(ctx, deferred, golem.Provider(manager.provider), model, observe.KindSemantic, observe.OperationSemanticRefresh, observe.PhaseFinish)
			refreshErr := manager.reconcile(refreshContext, index, refreshSpan)
			finishSemanticObservation(refreshSpan, refreshErr)
			if refreshErr != nil {
				return refreshErr
			}
		}
		return nil
	}()
	deferred.Flush()
	return err
}

func (manager *Manager) index(model ir.ModelID, name string) (Index, bool) {
	for _, index := range manager.indexes {
		if index.Descriptor.ModelID == model && index.Descriptor.Name == name {
			return index, true
		}
	}
	return Index{}, false
}

func (manager *Manager) Query(ctx context.Context, model ir.ModelID, name, query string, candidates Candidates, take int) (result []Rank, resultErr error) {
	if manager == nil {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic manager is not configured")
	}
	invalidContext := ctx == nil
	ctx, rankSpan := observeexec.Begin(ctx, manager.observer, golem.Provider(manager.provider), semanticObservationModel(model), observe.KindSemantic, observe.OperationSemanticRank, observe.PhaseFinish)
	defer func() { finishSemanticObservation(rankSpan, resultErr) }()
	if invalidContext {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic query requires a non-nil context")
	}
	if name == "" {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic query requires an index name")
	}
	if query == "" {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic query text is empty")
	}
	if take < 1 || take > MaximumResults {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic result limit is %d, outside 1..%d", take, MaximumResults)
	}
	selected, ok := manager.index(model, name)
	if !ok {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "the requested model has no semantic index with the requested name")
	}
	if err := validateCandidates(selected, candidates); err != nil {
		return nil, err
	}
	input, err := embedding.NewInput("query", query)
	if err != nil {
		return nil, embedding.Failf(embedding.CodeInvalidInput, err, "semantic query text of %d bytes is not a valid embedding input", len(query))
	}
	vectors, err := manager.embed(ctx, semanticObservationModel(model), selected.Provider, selected.Specification, []embedding.Input{input})
	if err != nil {
		return nil, err
	}
	vector, err := manager.vectorValue(vectors[0])
	if err != nil {
		return nil, embedding.Failf(embedding.CodeProvider, err, "provider vector of %d dimensions could not be encoded for storage", vectors[0].Dimensions())
	}
	ranks, err := manager.rankVector(ctx, selected, vector, candidates, "", take)
	if err != nil {
		return nil, err
	}
	rankSpan.SetAggregateCount(int64(len(ranks)))
	return ranks, nil
}

func validateCandidates(index Index, candidates Candidates) error {
	if candidates.SQL == "" {
		return embedding.Failf(embedding.CodeInvalidInput, nil, "semantic candidate set carries no SQL")
	}
	if candidates.NewScan == nil {
		return embedding.Failf(embedding.CodeInvalidInput, nil, "semantic candidate set carries no row scanner")
	}
	if len(index.Descriptor.Identity) == 0 {
		return embedding.Failf(embedding.CodeInvalidInput, nil, "semantic index declares no identity columns")
	}
	if len(candidates.Columns) != len(index.Descriptor.Identity) {
		return embedding.Failf(embedding.CodeInvalidInput, nil, "semantic candidate set projects %d columns but the index identifies rows by %d", len(candidates.Columns), len(index.Descriptor.Identity))
	}
	for position, column := range index.Descriptor.Identity {
		if candidates.Columns[position] != string(column.Name) {
			return embedding.Failf(embedding.CodeInvalidInput, nil, "semantic candidate column %d does not match the index identity column at that position", position)
		}
	}
	return nil
}

// QueryByKey ranks candidates against a stored vector rather than an embedded
// query. The source vector is read from managed storage, so a similarity
// request performs no embedding-provider call and compares vectors that were
// produced under the same canonical document framing.
func (manager *Manager) QueryByKey(ctx context.Context, model ir.ModelID, name, sourceKey string, candidates Candidates, take int) (result []Rank, resultErr error) {
	if manager == nil {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic manager is not configured")
	}
	invalidContext := ctx == nil
	ctx, rankSpan := observeexec.Begin(ctx, manager.observer, golem.Provider(manager.provider), semanticObservationModel(model), observe.KindSemantic, observe.OperationSemanticRank, observe.PhaseFinish)
	defer func() { finishSemanticObservation(rankSpan, resultErr) }()
	if invalidContext {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic similarity search requires a non-nil context")
	}
	if name == "" {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic similarity search requires an index name")
	}
	if sourceKey == "" {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic similarity source key is empty")
	}
	if take < 1 || take > MaximumResults {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic result limit is %d, outside 1..%d", take, MaximumResults)
	}
	selected, ok := manager.index(model, name)
	if !ok {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "the requested model has no semantic index with the requested name")
	}
	if err := validateCandidates(selected, candidates); err != nil {
		return nil, err
	}
	vector, err := manager.sourceVector(ctx, selected, sourceKey)
	if err != nil {
		return nil, err
	}
	ranks, err := manager.rankVector(ctx, selected, vector, candidates, sourceKey, take)
	if err != nil {
		return nil, err
	}
	rankSpan.SetAggregateCount(int64(len(ranks)))
	return ranks, nil
}

// sourceVector resolves a similarity source by its last stored vector, not by
// its freshness. A record the write path has marked stale still ranks, and
// still serves as a similarity source, on the vector it was last embedded
// with; a record that has never been embedded simply has no row here.
func (manager *Manager) sourceVector(ctx context.Context, index Index, sourceKey string) (any, error) {
	statePlaceholder, vectorProjection := "?", "embedding"
	if manager.provider == ir.PostgreSQL {
		statePlaceholder, vectorProjection = "$1", "embedding::text"
	}
	statement := "SELECT " + vectorProjection + " FROM " + manager.hidden(index, "_vec") + " WHERE record_key=" + statePlaceholder
	observeexec.RecordStatement(ctx)
	if manager.provider == ir.SQLite {
		var encoded []byte
		err := manager.database.GetContext(ctx, &encoded, statement, sourceKey)
		return classifySourceVector(encoded, err)
	}
	var text string
	err := manager.database.GetContext(ctx, &text, statement, sourceKey)
	return classifySourceVector(text, err)
}

func classifySourceVector[Vector ~string | ~[]byte](vector Vector, err error) (any, error) {
	switch {
	case errors.Is(err, context.Canceled):
		return nil, fmt.Errorf("P9_SEMANTIC_QUERY: semantic source vector read cancelled: %w", context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return nil, fmt.Errorf("P9_SEMANTIC_QUERY: semantic source vector read timed out: %w", context.DeadlineExceeded)
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("P9_SEMANTIC_QUERY: semantic source vector is unavailable")
	case err != nil:
		return nil, fmt.Errorf("P9_SEMANTIC_QUERY: semantic source vector read failed")
	case len(vector) == 0:
		return nil, fmt.Errorf("P9_SEMANTIC_QUERY: semantic source vector is empty")
	}
	return vector, nil
}

// rankVector ranks exactly the authorized candidate rows. The candidate
// subquery is the row source, so an unreadable row can neither occupy a result
// slot nor influence ordering. PostgreSQL adds +0.0 to the ordering distance:
// the resulting expression no longer matches the HNSW pathkey, so the planner
// must rank the joined candidates exactly instead of serving an approximate
// neighbourhood. Ties order by the binary collation of the opaque record key,
// matching the portable read ordering on every supported locale.
func (manager *Manager) rankVector(ctx context.Context, index Index, vector any, candidates Candidates, exclude string, take int) ([]Rank, error) {
	statement := manager.rankStatement(index, candidates, exclude != "")
	if err := readsql.ValidateStatementComplexity(candidates.Model, statement, candidates.MaxStatementBytes, candidates.MaxStatementAliases); err != nil {
		return nil, embedding.Failf(embedding.CodeInvalidInput, err, "semantic ranking statement exceeds configured complexity")
	}
	arguments := make([]any, 0, len(candidates.Args)+3)
	arguments = append(arguments, vector)
	arguments = append(arguments, candidates.Args...)
	if exclude != "" {
		arguments = append(arguments, exclude)
	}
	arguments = append(arguments, take)
	observeexec.RecordStatement(ctx)
	rows, err := manager.database.QueryxContext(ctx, statement, arguments...)
	if err != nil {
		return nil, fmt.Errorf("P9_SEMANTIC_QUERY: ranking failed")
	}
	defer rows.Close()
	return decodeRanks(rows, take, candidates)
}

// rankStatement binds the query vector first, the authorized candidate
// subquery next, then the optional excluded source key and the page size.
func (manager *Manager) rankStatement(index Index, candidates Candidates, exclude bool) string {
	vectors, state := manager.hidden(index, "_vec"), manager.hidden(index, "_state")
	identity := make([]string, len(index.Descriptor.Identity))
	joins := make([]string, len(index.Descriptor.Identity))
	for position, column := range index.Descriptor.Identity {
		identity[position] = "golem_ss." + manager.quote(column.Name)
		joins[position] = "golem_sq." + manager.quote(column.Name) + "=golem_ss." + manager.quote(column.Name)
	}
	distance := "vec_distance_cosine(golem_sv.embedding," + manager.placeholder(1) + ")"
	candidateSQL := policysql.RebasePlaceholders(candidates.SQL, 1, policyir.ProviderSQLite)
	order := "distance,golem_sv.record_key"
	if manager.provider == ir.PostgreSQL {
		distance = "(golem_sv.embedding <=> " + manager.placeholder(1) + "::vector)::double precision"
		candidateSQL = policysql.RebasePlaceholders(candidates.SQL, 1, policyir.ProviderPostgreSQL)
		order = "(" + distance + " + 0.0),golem_sv.record_key COLLATE \"C\""
	}
	statement := "SELECT golem_sv.record_key," + distance + " AS distance," + strings.Join(identity, ",") +
		" FROM " + vectors + " AS golem_sv" +
		" JOIN " + state + " AS golem_ss ON golem_ss.record_key=golem_sv.record_key" +
		" JOIN (" + candidateSQL + ") AS golem_sq ON " + strings.Join(joins, " AND ")
	position := len(candidates.Args) + 1
	if exclude {
		position++
		statement += " WHERE golem_sv.record_key<>" + manager.placeholder(position)
	}
	return statement + " ORDER BY " + order + " LIMIT " + manager.placeholder(position+1)
}

// placeholder mirrors the provider policy dialects, which both emit ordinal
// placeholders. The rank statement reuses the query vector, so its binds cannot
// be positional.
func (manager *Manager) placeholder(position int) string {
	if manager.provider == ir.PostgreSQL {
		return "$" + strconv.Itoa(position)
	}
	return "?" + strconv.Itoa(position)
}

func decodeRanks(rows *sqlx.Rows, capacity int, candidates Candidates) ([]Rank, error) {
	result := make([]Rank, 0, capacity)
	for rows.Next() {
		var rank Rank
		scan := candidates.NewScan()
		destinations := make([]any, 0, len(candidates.Columns)+2)
		destinations = append(destinations, &rank.Key, &rank.Distance)
		destinations = append(destinations, scan.Destinations()...)
		if len(destinations) != len(candidates.Columns)+2 {
			return nil, fmt.Errorf("P9_SEMANTIC_QUERY: ranking identity projection mismatch")
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("P9_SEMANTIC_QUERY: ranking decode failed")
		}
		if math.IsNaN(rank.Distance) || math.IsInf(rank.Distance, 0) || rank.Distance < 0 || rank.Distance > 2.000001 {
			return nil, fmt.Errorf("P9_SEMANTIC_QUERY: invalid cosine distance")
		}
		rank.Identity = scan.RawValues()
		result = append(result, rank)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("P9_SEMANTIC_QUERY: ranking stream failed")
	}
	return result, nil
}

type sourceRecord struct {
	key       string
	text      string
	hash      [32]byte
	identity  []any
	updatedAt int64
}

type stateRecord struct {
	hash        []byte
	fingerprint string
	status      string
	updatedAt   int64
}

type staleRecord struct {
	key         string
	hash        []byte
	fingerprint string
	updatedAt   int64
}

// observedRecord names one shadow state row by the updated_at value the pass
// actually read. Every state write a pass makes is conditioned on that value: a
// mark rewrites updated_at inside the writing transaction, so a record marked
// again while the pass was calling the embedding provider no longer matches its
// guard and is left stale for the next probe instead of being flipped back to
// ready under the vector the pass computed from the older content.
type observedRecord struct {
	key       string
	updatedAt int64
}

// semanticUnobserved guards a record whose state row the pass never read.
// updated_at is CHECKed non-negative, so it matches no stored row: a state row
// that appeared during the pass keeps whatever the writer put there.
const semanticUnobserved = -1

// MarkRecord is one record a write touched. Identity carries the record's
// primary key as LOGICAL values in physical primary-key order — [16]byte for
// UUID, int64, string, []byte, time.Time — and the Manager converts them to
// each index's mirrored identity columns. Key is the caller's own encoding of
// that identity; it is cross-checked rather than trusted, because a key that
// disagrees with the identity would mark a record that no drain can ever find.
type MarkRecord struct {
	Key      string
	Identity []any
}

type markRow struct {
	key      string
	identity []any
}

// MarkStale records that the write path changed these records, on the caller's
// executor so the marks commit and roll back with the write itself. A record
// that has been indexed before keeps its stored source hash, space fingerprint,
// and mirrored identity untouched: the drain re-derives the document and flips
// an unchanged record back to ready without an embedding-provider call. A
// record that has never been indexed is inserted with an EMPTY source hash,
// which no real document hash can equal, so the drain must embed it.
func (manager *Manager) MarkStale(ctx context.Context, executor sqlx.ExecerContext, model ir.ModelID, maximumParameters int, records []MarkRecord) error {
	if manager == nil || ctx == nil || executor == nil {
		return fmt.Errorf("P9_SEMANTIC_MARK: context, manager, and executor are required")
	}
	if len(records) == 0 {
		return nil
	}
	selected := make([]Index, 0, len(manager.indexes))
	for _, index := range manager.indexes {
		if index.Descriptor.ModelID == model {
			selected = append(selected, index)
		}
	}
	if len(selected) == 0 {
		return nil
	}
	for _, index := range selected {
		rows, err := manager.markRows(index, records)
		if err != nil {
			return err
		}
		if err := manager.markIndex(ctx, executor, index, maximumParameters, rows); err != nil {
			return err
		}
	}
	return nil
}

// markRows converts every record's logical identity into the index's mirrored
// physical representation, re-derives the record key from those converted
// values, and refuses any record whose caller-supplied key disagrees. Deriving
// from the converted values is what keeps one encoder between this path and the
// refresh scan: the values bound here are the values that column yields when
// read back. Rows are deduplicated and ordered by key so concurrent writers
// take shadow-row locks in one order.
func (manager *Manager) markRows(index Index, records []MarkRecord) ([]markRow, error) {
	width := len(index.Descriptor.Identity)
	result := make([]markRow, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if len(record.Identity) != width || width == 0 {
			return nil, fmt.Errorf("P9_SEMANTIC_MARK: identity width does not match the shadow contract")
		}
		identity := make([]any, width)
		for position, column := range index.Descriptor.Identity {
			converted, err := manager.markIdentityValue(column, record.Identity[position])
			if err != nil {
				return nil, err
			}
			identity[position] = converted
		}
		key, err := semantickey.Encode(identity)
		if err != nil {
			return nil, fmt.Errorf("P9_SEMANTIC_MARK: identity is not encodable")
		}
		if record.Key != key {
			return nil, fmt.Errorf("P9_SEMANTIC_MARK: record key does not match its primary identity")
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, markRow{key: key, identity: identity})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].key < result[right].key })
	return result, nil
}

// markIdentityValue converts one logical primary-key value into the physical
// representation the owner column actually stores. This is not cosmetic: the
// drain reaches owner rows by joining the shadow table's mirrored identity
// columns, so a value in the wrong representation joins nothing, is classified
// as an absent owner, and has its vector deleted. Every combination this cannot
// convert with certainty is refused rather than bound approximately.
func (manager *Manager) markIdentityValue(column semanticstorage.IdentityColumn, value any) (any, error) {
	if value == nil {
		return nil, fmt.Errorf("P9_SEMANTIC_MARK: identity column %q is null", column.Name)
	}
	unsupported := fmt.Errorf("P9_SEMANTIC_MARK: identity column %q cannot bind %T into %s storage", column.Name, value, column.Storage.Kind)
	switch column.Storage.Kind {
	case physical.StorageSQLiteText, physical.StoragePostgreSQLText, physical.StoragePostgreSQLVarchar, physical.StoragePostgreSQLUUID:
		switch typed := value.(type) {
		case string:
			return typed, nil
		case [16]byte:
			return markUUIDText(typed), nil
		}
		return nil, unsupported
	case physical.StorageSQLiteInteger:
		switch typed := value.(type) {
		case int64:
			return typed, nil
		case time.Time:
			return typed.UTC().UnixMicro(), nil
		}
		return nil, unsupported
	case physical.StoragePostgreSQLBoolean:
		if typed, ok := value.(bool); ok {
			return typed, nil
		}
		return nil, unsupported
	case physical.StoragePostgreSQLBigInt:
		if typed, ok := value.(int64); ok {
			return typed, nil
		}
		return nil, unsupported
	case physical.StoragePostgreSQLInteger:
		if typed, ok := value.(int64); ok {
			if typed > math.MaxInt32 || typed < math.MinInt32 {
				return nil, fmt.Errorf("P9_SEMANTIC_MARK: identity column %q is outside integer storage range", column.Name)
			}
			return int32(typed), nil
		}
		return nil, unsupported
	case physical.StoragePostgreSQLSmallInt:
		if typed, ok := value.(int64); ok {
			if typed > math.MaxInt16 || typed < math.MinInt16 {
				return nil, fmt.Errorf("P9_SEMANTIC_MARK: identity column %q is outside smallint storage range", column.Name)
			}
			return int16(typed), nil
		}
		return nil, unsupported
	case physical.StorageSQLiteBlob, physical.StoragePostgreSQLBytea:
		if typed, ok := value.([]byte); ok {
			return typed, nil
		}
		return nil, unsupported
	case physical.StoragePostgreSQLTimestampTZ:
		// Both providers store instants at microsecond resolution, so a caller's
		// nanoseconds are not part of the stored identity and must not be part of
		// the key derived from it.
		if typed, ok := value.(time.Time); ok {
			return typed.UTC().Truncate(time.Microsecond), nil
		}
		return nil, unsupported
	}
	return nil, unsupported
}

func markUUIDText(value [16]byte) string {
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

// markIndex writes one statement per chunk. The conflict arm deliberately
// leaves source_hash, space_fingerprint, and the mirrored identity columns
// alone: a record that has been indexed before already carries the identity
// storeBatch copied out of the owner table, which is more trustworthy than any
// conversion performed here.
func (manager *Manager) markIndex(ctx context.Context, executor sqlx.ExecerContext, index Index, maximumParameters int, rows []markRow) error {
	if len(rows) == 0 {
		return nil
	}
	width := len(index.Descriptor.Identity)
	columns := ""
	for _, column := range index.Descriptor.Identity {
		columns += "," + manager.quote(column.Name)
	}
	empty := "x''"
	if manager.provider == ir.PostgreSQL {
		empty = "''::bytea"
	}
	fingerprint := hex.EncodeToString(index.SpaceFingerprint[:])
	generation := manager.quote("updated_at") + "+1"
	if manager.provider == ir.PostgreSQL {
		generation = manager.hidden(index, "_state") + "." + manager.quote("updated_at") + "+1"
	}
	chunk := semanticMarkChunkSize(maximumParameters, width)
	if chunk < 1 {
		return fmt.Errorf("P9_SEMANTIC_MARK: identity is too wide to mark")
	}
	for offset := 0; offset < len(rows); offset += chunk {
		end := offset + chunk
		if end > len(rows) {
			end = len(rows)
		}
		arguments := make([]any, 0, 2+(end-offset)*(1+width))
		arguments = append(arguments, fingerprint, time.Now().UTC().UnixMicro())
		tuples := make([]string, 0, end-offset)
		for _, row := range rows[offset:end] {
			slots := make([]string, 0, width)
			arguments = append(arguments, row.key)
			keySlot := manager.placeholder(len(arguments))
			for _, value := range row.identity {
				arguments = append(arguments, value)
				slots = append(slots, manager.placeholder(len(arguments)))
			}
			tuple := "(" + keySlot + "," + empty + "," + manager.placeholder(1) + ",'pending',0,NULL," + manager.placeholder(2)
			if width > 0 {
				tuple += "," + strings.Join(slots, ",")
			}
			tuples = append(tuples, tuple+")")
		}
		statement := "INSERT INTO " + manager.hidden(index, "_state") +
			" (" + manager.quote("record_key") + "," + manager.quote("source_hash") + "," + manager.quote("space_fingerprint") + "," +
			manager.quote("status") + "," + manager.quote("attempt_count") + "," + manager.quote("error_code") + "," + manager.quote("updated_at") + columns + ")" +
			" VALUES " + strings.Join(tuples, ",") +
			" ON CONFLICT(" + manager.quote("record_key") + ") DO UPDATE SET " +
			manager.quote("status") + "='pending'," + manager.quote("updated_at") + "=" + generation
		observeexec.RecordStatement(ctx)
		if _, err := executor.ExecContext(ctx, statement, arguments...); err != nil {
			return fmt.Errorf("P9_SEMANTIC_MARK: stale mark failed")
		}
	}
	return nil
}

func semanticMarkChunkSize(maximumParameters, width int) int {
	if maximumParameters > semanticMarkBinds {
		maximumParameters = semanticMarkBinds
	}
	capacity := (maximumParameters - 2) / (1 + width)
	if capacity < semanticMarkChunk {
		return capacity
	}
	return semanticMarkChunk
}

// refresh advances one index by exactly the records the write path marked
// stale. The owner table is never read as a whole: the stale partial index
// names the records, and their owner rows are reached through the identity
// columns the shadow state table mirrors.
func (manager *Manager) refresh(ctx context.Context, index Index, span *observeexec.Span) (bool, error) {
	table, ok := semanticOwnerTable(manager.schema, index.Descriptor.ModelID)
	if !ok || table.PrimaryKey == nil || len(table.PrimaryKey.Columns) == 0 {
		return false, fmt.Errorf("P9_SEMANTIC_SCHEMA: semantic model has no physical primary identity")
	}
	stale, err := manager.scanStale(ctx, index, semanticStaleProbe)
	if err != nil {
		return false, fmt.Errorf("P9_SEMANTIC_REFRESH: stale probe failed")
	}
	if err := manager.applyStale(ctx, index, table, stale, span); err != nil {
		return false, err
	}
	remaining, err := manager.scanStale(ctx, index, 1)
	if err != nil {
		return false, fmt.Errorf("P9_SEMANTIC_REFRESH: stale probe failed")
	}
	return len(remaining) > 0, nil
}

// applyStale sorts one probed page into the three outcomes a mark can have.
// A mark carries no evidence that the document actually changed, so the
// recomputed content hash decides: an unchanged document is flipped back to
// ready without reaching the embedding provider at all, which is what makes
// marking every write affordable and what contains a mark that lies.
func (manager *Manager) applyStale(ctx context.Context, index Index, table physical.PhysicalTable, stale []staleRecord, span *observeexec.Span) error {
	if len(stale) == 0 {
		span.SetAggregateCount(0)
		return nil
	}
	owners, err := manager.scanOwners(ctx, table, index, stale)
	if err != nil {
		return fmt.Errorf("P9_SEMANTIC_REFRESH: source fetch failed")
	}
	fingerprint := hex.EncodeToString(index.SpaceFingerprint[:])
	dirty := make([]sourceRecord, 0, len(stale))
	unchanged := make([]observedRecord, 0, len(stale))
	absent := make([]observedRecord, 0, len(stale))
	for _, record := range stale {
		observed := observedRecord{key: record.key, updatedAt: record.updatedAt}
		source, exists := owners[record.key]
		if !exists {
			absent = append(absent, observed)
			continue
		}
		if len(record.hash) == sha256.Size && record.fingerprint == fingerprint && equalBytes(record.hash, source.hash[:]) {
			unchanged = append(unchanged, observed)
			continue
		}
		source.updatedAt = record.updatedAt
		dirty = append(dirty, source)
	}
	span.SetAggregateCount(int64(len(dirty) + len(unchanged) + len(absent)))
	// Every write below happens after the provider round trip, so all three
	// outcomes are guarded against the same window: whatever the pass decided
	// about a record only lands while that record still looks the way it did
	// when the pass read it.
	if err := manager.embedDirty(ctx, index, fingerprint, dirty); err != nil {
		return err
	}
	if err := manager.markReady(ctx, index, unchanged); err != nil {
		return fmt.Errorf("P9_SEMANTIC_REFRESH: unchanged record flip failed")
	}
	for _, record := range absent {
		if err := manager.deleteRecord(ctx, index, record); err != nil {
			return fmt.Errorf("P9_SEMANTIC_REFRESH: stale vector cleanup failed")
		}
	}
	return nil
}

func (manager *Manager) reconcile(ctx context.Context, index Index, span *observeexec.Span) error {
	table, ok := semanticOwnerTable(manager.schema, index.Descriptor.ModelID)
	if !ok || table.PrimaryKey == nil || len(table.PrimaryKey.Columns) == 0 {
		return fmt.Errorf("P9_SEMANTIC_SCHEMA: semantic model has no physical primary identity")
	}
	states, err := manager.scanStates(ctx, index)
	if err != nil {
		return fmt.Errorf("P9_SEMANTIC_REFRESH: state scan failed")
	}
	records, err := manager.scanSources(ctx, table, index)
	if err != nil {
		return fmt.Errorf("P9_SEMANTIC_REFRESH: source scan failed")
	}
	fingerprint := hex.EncodeToString(index.SpaceFingerprint[:])
	dirty := make([]sourceRecord, 0)
	unchanged := make([]observedRecord, 0)
	present := make(map[string]bool, len(records))
	for _, record := range records {
		present[record.key] = true
		state, exists := states[record.key]
		record.updatedAt = semanticUnobserved
		if exists {
			record.updatedAt = state.updatedAt
		}
		if !exists || len(state.hash) != sha256.Size || state.fingerprint != fingerprint || !equalBytes(state.hash, record.hash[:]) {
			dirty = append(dirty, record)
			continue
		}
		// A record the write path marked but never changed is settled by the same
		// hash the drain uses, so reconciling an application that marks every
		// write costs no embedding it would not otherwise have paid for. A
		// quarantined record whose document matches its stored hash is settled the
		// same way: that hash was written with the vector that still stands.
		if state.status != "ready" {
			unchanged = append(unchanged, observedRecord{key: record.key, updatedAt: state.updatedAt})
		}
	}
	stale := 0
	for key := range states {
		if !present[key] {
			stale++
		}
	}
	span.SetAggregateCount(int64(len(dirty) + len(unchanged) + stale))
	if err := manager.embedDirty(ctx, index, fingerprint, dirty); err != nil {
		return err
	}
	if err := manager.markReady(ctx, index, unchanged); err != nil {
		return fmt.Errorf("P9_SEMANTIC_REFRESH: unchanged record flip failed")
	}
	for key, state := range states {
		if !present[key] {
			if err := manager.deleteRecord(ctx, index, observedRecord{key: key, updatedAt: state.updatedAt}); err != nil {
				return fmt.Errorf("P9_SEMANTIC_REFRESH: stale vector cleanup failed")
			}
		}
	}
	return nil
}

// embedDirty embeds one provider batch at a time and isolates a document the
// provider will not accept. A refused batch is retried one record at a time, so
// an un-embeddable document costs its own batch a single extra pass through the
// provider and quarantines exactly itself: every other record in that batch, and
// every later batch, is still embedded and stored. A refusal that arrives after
// the context ended is not evidence about any document, so it fails the pass.
func (manager *Manager) embedDirty(ctx context.Context, index Index, fingerprint string, dirty []sourceRecord) error {
	maximum := index.Specification.MaximumBatch()
	for offset := 0; offset < len(dirty); offset += maximum {
		end := offset + maximum
		if end > len(dirty) {
			end = len(dirty)
		}
		batch := make([]sourceRecord, 0, end-offset)
		inputs := make([]embedding.Input, 0, end-offset)
		for position, record := range dirty[offset:end] {
			// Provider results are positional. Use a batch-local correlation key so
			// the provider never receives the canonical database identity retained
			// by Golem's private state and vector tables.
			input, inputErr := embedding.NewInput("source-"+strconv.Itoa(position), record.text)
			if inputErr != nil {
				if err := manager.quarantine(ctx, index, record, embedding.Failf(embedding.CodeInvalidInput, inputErr, "indexed document text of %d bytes is not a valid embedding input", len(record.text))); err != nil {
					return err
				}
				continue
			}
			batch = append(batch, record)
			inputs = append(inputs, input)
		}
		if len(batch) == 0 {
			continue
		}
		vectors, embedErr := manager.embed(ctx, semanticObservationModel(index.Descriptor.ModelID), index.Provider, index.Specification, inputs)
		if embedErr != nil {
			if ctx.Err() != nil {
				return embedErr
			}
			if code, ok := embedding.CodeOf(embedErr); ok && code == embedding.CodeUnavailable {
				return embedErr
			}
			if len(batch) > 1 {
				for _, record := range batch {
					if err := manager.embedDirty(ctx, index, fingerprint, []sourceRecord{record}); err != nil {
						return err
					}
				}
				continue
			}
			if err := manager.quarantine(ctx, index, batch[0], embedErr); err != nil {
				return err
			}
			continue
		}
		if err := manager.storeBatch(ctx, index, fingerprint, batch, vectors); err != nil {
			return fmt.Errorf("P9_SEMANTIC_REFRESH: vector storage failed")
		}
	}
	return nil
}

// quarantine parks one record no provider call can settle. Its stored hash and
// its vector are left alone, so it keeps ranking on whatever it was last
// embedded with, and only the closed failure code is recorded — never the
// provider's own message. A quarantined record is invisible to the stale probe:
// it is retried when a write marks it again, or when a reconcile compares its
// document against the stored hash. Nothing re-probes it on its own, so a
// document the provider will never accept cannot spend the budget twice.
func (manager *Manager) quarantine(ctx context.Context, index Index, record sourceRecord, cause error) error {
	code := string(embedding.CodeProvider)
	if value, ok := embedding.CodeOf(cause); ok {
		code = string(value)
	}
	if record.updatedAt == semanticUnobserved {
		columns := ""
		values := ""
		arguments := []any{record.key, []byte{}, hex.EncodeToString(index.SpaceFingerprint[:]), code, time.Now().UTC().UnixMicro()}
		for position, column := range index.Descriptor.Identity {
			columns += "," + manager.quote(column.Name)
			arguments = append(arguments, record.identity[position])
			values += "," + manager.placeholder(len(arguments))
		}
		statement := "INSERT INTO " + manager.hidden(index, "_state") +
			" (" + manager.quote("record_key") + "," + manager.quote("source_hash") + "," + manager.quote("space_fingerprint") + "," +
			manager.quote("status") + "," + manager.quote("attempt_count") + "," + manager.quote("error_code") + "," + manager.quote("updated_at") + columns + ")" +
			" VALUES (" + manager.placeholder(1) + "," + manager.placeholder(2) + "," + manager.placeholder(3) + ",'failed',1," + manager.placeholder(4) + "," + manager.placeholder(5) + values + ")" +
			" ON CONFLICT(" + manager.quote("record_key") + ") DO NOTHING"
		observeexec.RecordStatement(ctx)
		if _, err := manager.database.ExecContext(ctx, statement, arguments...); err != nil {
			return fmt.Errorf("P9_SEMANTIC_REFRESH: quarantine failed")
		}
		return nil
	}
	statement := "UPDATE " + manager.hidden(index, "_state") +
		" SET " + manager.quote("status") + "='failed'," + manager.quote("error_code") + "=" + manager.placeholder(1) + "," +
		manager.quote("attempt_count") + "=" + manager.quote("attempt_count") + "+1," +
		manager.quote("updated_at") + "=" + manager.placeholder(2) +
		" WHERE " + manager.quote("record_key") + "=" + manager.placeholder(3) +
		" AND " + manager.quote("updated_at") + "=" + manager.placeholder(4)
	observeexec.RecordStatement(ctx)
	if _, err := manager.database.ExecContext(ctx, statement, code, time.Now().UTC().UnixMicro(), record.key, record.updatedAt); err != nil {
		return fmt.Errorf("P9_SEMANTIC_REFRESH: quarantine failed")
	}
	return nil
}

func (manager *Manager) markReady(ctx context.Context, index Index, records []observedRecord) error {
	for offset := 0; offset < len(records); offset += semanticKeyChunk {
		end := offset + semanticKeyChunk
		if end > len(records) {
			end = len(records)
		}
		arguments := make([]any, 0, 2*(end-offset)+1)
		arguments = append(arguments, time.Now().UTC().UnixMicro())
		marks := make([]string, 0, end-offset)
		for _, record := range records[offset:end] {
			arguments = append(arguments, record.key, record.updatedAt)
			marks = append(marks, "("+manager.quote("record_key")+"="+manager.placeholder(len(arguments)-1)+
				" AND "+manager.quote("updated_at")+"="+manager.placeholder(len(arguments))+")")
		}
		if len(marks) == 0 {
			continue
		}
		statement := "UPDATE " + manager.hidden(index, "_state") +
			" SET " + manager.quote("status") + "='ready'," + manager.quote("error_code") + "=NULL," + manager.quote("updated_at") + "=" + manager.placeholder(1) +
			" WHERE " + strings.Join(marks, " OR ")
		observeexec.RecordStatement(ctx)
		if _, err := manager.database.ExecContext(ctx, statement, arguments...); err != nil {
			return err
		}
	}
	return nil
}

type sourceProjection struct {
	selected  []ir.FieldID
	positions map[ir.FieldID]int
	columns   map[ir.FieldID]physical.PhysicalColumn
	order     []string
}

func (manager *Manager) projection(table physical.PhysicalTable, index Index) (sourceProjection, error) {
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(table.Columns))
	for _, column := range table.Columns {
		columns[column.ID] = column
	}
	result := sourceProjection{positions: make(map[ir.FieldID]int), columns: columns}
	appendField := func(field ir.FieldID) error {
		if _, exists := result.positions[field]; exists {
			return nil
		}
		if _, exists := columns[field]; !exists {
			return fmt.Errorf("field is absent")
		}
		result.positions[field] = len(result.selected)
		result.selected = append(result.selected, field)
		return nil
	}
	for _, field := range table.PrimaryKey.Columns {
		if err := appendField(field); err != nil {
			return sourceProjection{}, err
		}
	}
	for _, field := range index.Descriptor.Fields {
		if err := appendField(field); err != nil {
			return sourceProjection{}, err
		}
	}
	result.order = make([]string, len(table.PrimaryKey.Columns))
	for position, field := range table.PrimaryKey.Columns {
		result.order[position] = manager.quote(columns[field].Name)
	}
	return result, nil
}

func (manager *Manager) projectionList(projection sourceProjection, prefix string) string {
	names := make([]string, len(projection.selected))
	for position, field := range projection.selected {
		names[position] = prefix + manager.quote(projection.columns[field].Name)
	}
	return strings.Join(names, ",")
}

func (manager *Manager) scanSources(ctx context.Context, table physical.PhysicalTable, index Index) ([]sourceRecord, error) {
	projection, err := manager.projection(table, index)
	if err != nil {
		return nil, err
	}
	query := "SELECT " + manager.projectionList(projection, "") + " FROM " + manager.table(table.Name) + " ORDER BY " + strings.Join(projection.order, ",")
	return manager.decodeSources(ctx, table, index, projection, query)
}

// scanOwners reads exactly the marked records' owner rows. The join predicate
// is the owner's own primary key, mirrored onto the shadow state table when the
// record was last stored, so the stale partial index drives the read and the
// owner table is only ever touched by identity.
func (manager *Manager) scanOwners(ctx context.Context, table physical.PhysicalTable, index Index, stale []staleRecord) (map[string]sourceRecord, error) {
	projection, err := manager.projection(table, index)
	if err != nil {
		return nil, err
	}
	if len(index.Descriptor.Identity) != len(table.PrimaryKey.Columns) {
		return nil, fmt.Errorf("identity width does not match the owner primary key")
	}
	joins := make([]string, len(index.Descriptor.Identity))
	for position, column := range index.Descriptor.Identity {
		joins[position] = "golem_so." + manager.quote(column.Name) + "=golem_ss." + manager.quote(column.Name)
	}
	result := make(map[string]sourceRecord, len(stale))
	for offset := 0; offset < len(stale); offset += semanticKeyChunk {
		end := offset + semanticKeyChunk
		if end > len(stale) {
			end = len(stale)
		}
		arguments := make([]any, 0, end-offset)
		marks := make([]string, 0, end-offset)
		for position, record := range stale[offset:end] {
			marks = append(marks, manager.placeholder(position+1))
			arguments = append(arguments, record.key)
		}
		query := "SELECT " + manager.projectionList(projection, "golem_so.") +
			" FROM " + manager.table(table.Name) + " AS golem_so" +
			" JOIN " + manager.hidden(index, "_state") + " AS golem_ss ON " + strings.Join(joins, " AND ") +
			" WHERE golem_ss." + manager.quote("record_key") + " IN (" + strings.Join(marks, ",") + ")"
		records, decodeErr := manager.decodeSources(ctx, table, index, projection, query, arguments...)
		if decodeErr != nil {
			return nil, decodeErr
		}
		for _, record := range records {
			result[record.key] = record
		}
	}
	return result, nil
}

func (manager *Manager) decodeSources(ctx context.Context, table physical.PhysicalTable, index Index, projection sourceProjection, query string, arguments ...any) ([]sourceRecord, error) {
	observeexec.RecordStatement(ctx)
	rows, err := manager.database.QueryxContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]sourceRecord, 0)
	for rows.Next() {
		values := make([]any, len(projection.selected))
		destinations := make([]any, len(projection.selected))
		for position := range values {
			destinations[position] = &values[position]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		keys := make([]any, len(table.PrimaryKey.Columns))
		for position, field := range table.PrimaryKey.Columns {
			keys[position] = values[projection.positions[field]]
		}
		key, err := semantickey.Encode(keys)
		if err != nil {
			return nil, err
		}
		var document strings.Builder
		document.WriteString("golem-semantic-document:v1")
		for _, field := range index.Descriptor.Fields {
			text, present, err := semanticText(values[projection.positions[field]])
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&document, "\x00%s\x00%t\x00%d:", field, present, len(text))
			document.WriteString(text)
		}
		canonical := document.String()
		result = append(result, sourceRecord{key: key, text: canonical, hash: sha256.Sum256([]byte(canonical)), identity: keys})
	}
	return result, rows.Err()
}

// scanStale claims one page of marked records. A quarantined record is not part
// of that page, and the leading term is the partial stale index's own predicate
// so excluding it still reads through that index. The drain chains a successor
// whenever anything remains stale, so a record the provider refuses would
// otherwise be probed, refused, and chained again for as long as the deployment
// lives.
//
// source_hash is projected last on purpose. A record that has been marked but
// never embedded stores an empty hash, and the embedded SQLite driver zeroes
// every column scanned after an empty blob, which would silently hand the pass
// an updated_at of 0 and a guard that can never match.
func (manager *Manager) scanStale(ctx context.Context, index Index, limit int) ([]staleRecord, error) {
	query := "SELECT " + manager.quote("record_key") + "," + manager.quote("space_fingerprint") + "," + manager.quote("updated_at") + "," + manager.quote("source_hash") +
		" FROM " + manager.hidden(index, "_state") +
		" WHERE " + manager.quote("status") + "<>'ready' AND " + manager.quote("status") + "<>'failed'" +
		" ORDER BY " + manager.quote("record_key") + " LIMIT " + strconv.Itoa(limit)
	observeexec.RecordStatement(ctx)
	rows, err := manager.database.QueryxContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]staleRecord, 0, limit)
	for rows.Next() {
		var record staleRecord
		var hash []byte
		if err := rows.Scan(&record.key, &record.fingerprint, &record.updatedAt, &hash); err != nil {
			return nil, err
		}
		record.hash = append([]byte(nil), hash...)
		result = append(result, record)
	}
	return result, rows.Err()
}

// scanStates projects source_hash last for the reason scanStale does.
func (manager *Manager) scanStates(ctx context.Context, index Index) (map[string]stateRecord, error) {
	query := "SELECT " + manager.quote("record_key") + "," + manager.quote("space_fingerprint") + "," + manager.quote("status") + "," + manager.quote("updated_at") + "," + manager.quote("source_hash") + " FROM " + manager.hidden(index, "_state")
	observeexec.RecordStatement(ctx)
	rows, err := manager.database.QueryxContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]stateRecord)
	for rows.Next() {
		var key, fingerprint, status string
		var hash []byte
		var updatedAt int64
		if err := rows.Scan(&key, &fingerprint, &status, &updatedAt, &hash); err != nil {
			return nil, err
		}
		result[key] = stateRecord{hash: append([]byte(nil), hash...), fingerprint: fingerprint, status: status, updatedAt: updatedAt}
	}
	return result, rows.Err()
}

func (manager *Manager) storeBatch(ctx context.Context, index Index, fingerprint string, records []sourceRecord, vectors []embedding.Vector) error {
	transaction, err := manager.database.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	columns, values, assignments := "", "", ""
	for position, column := range index.Descriptor.Identity {
		columns += "," + manager.quote(column.Name)
		values += "," + manager.placeholder(position+5)
		assignments += "," + manager.quote(column.Name) + "=excluded." + manager.quote(column.Name)
	}
	// The state row is written first and its guard decides the record: the
	// conflict arm only fires while the row still carries the updated_at the pass
	// observed, so a record marked again mid-embed keeps its mark and its vector
	// is not overwritten by the older content this pass embedded.
	guard := manager.placeholder(len(index.Descriptor.Identity) + 5)
	for position, record := range records {
		if len(record.identity) != len(index.Descriptor.Identity) {
			return fmt.Errorf("P9_SEMANTIC_REFRESH: identity width does not match the shadow contract")
		}
		vectorValue, err := manager.vectorValue(vectors[position])
		if err != nil {
			return err
		}
		arguments := append([]any{record.key, record.hash[:], fingerprint, time.Now().UTC().UnixMicro()}, record.identity...)
		arguments = append(arguments, record.updatedAt)
		var state sql.Result
		if manager.provider == ir.SQLite {
			observeexec.RecordStatement(ctx)
			state, err = transaction.ExecContext(ctx, "INSERT INTO "+manager.hidden(index, "_state")+" (record_key,source_hash,space_fingerprint,status,attempt_count,error_code,updated_at"+columns+") VALUES (?,?,?,'ready',1,NULL,?"+values+") ON CONFLICT(record_key) DO UPDATE SET source_hash=excluded.source_hash,space_fingerprint=excluded.space_fingerprint,status='ready',attempt_count=attempt_count+1,error_code=NULL,updated_at=excluded.updated_at"+assignments+" WHERE updated_at="+guard, arguments...)
		} else {
			observeexec.RecordStatement(ctx)
			state, err = transaction.ExecContext(ctx, "INSERT INTO "+manager.hidden(index, "_state")+" (record_key,source_hash,space_fingerprint,status,attempt_count,error_code,updated_at"+columns+") VALUES ($1,$2,$3,'ready',1,NULL,$4"+values+") ON CONFLICT(record_key) DO UPDATE SET source_hash=excluded.source_hash,space_fingerprint=excluded.space_fingerprint,status='ready',attempt_count="+manager.hidden(index, "_state")+".attempt_count+1,error_code=NULL,updated_at=excluded.updated_at"+assignments+" WHERE "+manager.hidden(index, "_state")+".updated_at="+guard, arguments...)
		}
		if err != nil {
			return err
		}
		settled, err := state.RowsAffected()
		if err != nil {
			return err
		}
		if settled == 0 {
			continue
		}
		if manager.provider == ir.SQLite {
			observeexec.RecordStatement(ctx)
			if _, err := transaction.ExecContext(ctx, "DELETE FROM "+manager.hidden(index, "_vec")+" WHERE "+manager.quote("record_key")+"=?", record.key); err != nil {
				return err
			}
			observeexec.RecordStatement(ctx)
			if _, err := transaction.ExecContext(ctx, "INSERT INTO "+manager.hidden(index, "_vec")+" ("+manager.quote("record_key")+","+manager.quote("embedding")+") VALUES (?,?)", record.key, vectorValue); err != nil {
				return err
			}
			continue
		}
		observeexec.RecordStatement(ctx)
		if _, err := transaction.ExecContext(ctx, "INSERT INTO "+manager.hidden(index, "_vec")+" (record_key,embedding) VALUES ($1,$2::vector) ON CONFLICT(record_key) DO UPDATE SET embedding=excluded.embedding", record.key, vectorValue); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

// deleteRecord removes a record whose owner row the pass could not reach. The
// state row is deleted under the same guard the rest of the pass writes under,
// so a record deleted, drained, and created again mid-pass keeps the state row
// its re-creation wrote, and its vector is left for the next pass to settle.
func (manager *Manager) deleteRecord(ctx context.Context, index Index, record observedRecord) error {
	transaction, err := manager.database.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	observeexec.RecordStatement(ctx)
	state, err := transaction.ExecContext(ctx, "DELETE FROM "+manager.hidden(index, "_state")+
		" WHERE "+manager.quote("record_key")+"="+manager.placeholder(1)+
		" AND "+manager.quote("updated_at")+"="+manager.placeholder(2), record.key, record.updatedAt)
	if err != nil {
		return err
	}
	settled, err := state.RowsAffected()
	if err != nil {
		return err
	}
	if settled == 0 {
		return nil
	}
	observeexec.RecordStatement(ctx)
	if _, err := transaction.ExecContext(ctx, "DELETE FROM "+manager.hidden(index, "_vec")+
		" WHERE "+manager.quote("record_key")+"="+manager.placeholder(1), record.key); err != nil {
		return err
	}
	return transaction.Commit()
}

func (manager *Manager) vectorValue(vector embedding.Vector) (any, error) {
	if manager.provider == ir.SQLite {
		return sqlitevec.Serialize(vector.Values(), vector.Dimensions())
	}
	values := vector.Values()
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.FormatFloat(float64(value), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}

func (manager *Manager) quote(name physical.PhysicalName) string {
	return `"` + strings.ReplaceAll(string(name), `"`, `""`) + `"`
}
func (manager *Manager) table(name physical.PhysicalName) string {
	if manager.provider == ir.PostgreSQL {
		return manager.quote(manager.schema.Namespace.Name) + "." + manager.quote(name)
	}
	return manager.quote(name)
}
func (manager *Manager) hidden(index Index, suffix string) string {
	return manager.table(physical.PhysicalName(string(index.Descriptor.Storage) + suffix))
}

func semanticOwnerTable(schema physical.PhysicalSchema, model ir.ModelID) (physical.PhysicalTable, bool) {
	for _, table := range schema.Tables {
		if table.ID == model {
			return table, true
		}
	}
	return physical.PhysicalTable{}, false
}

func semanticText(value any) (string, bool, error) {
	switch typed := value.(type) {
	case nil:
		return "", false, nil
	case string:
		return typed, true, nil
	case []byte:
		return string(typed), true, nil
	default:
		return "", false, fmt.Errorf("semantic source value is not text")
	}
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func callEmbeddingProvider(ctx context.Context, provider embedding.Provider, inputs []embedding.Input) (vectors []embedding.Vector, err error) {
	defer func() {
		if recover() != nil {
			vectors = nil
			err = fmt.Errorf("embedding provider panicked")
		}
	}()
	vectors, err = provider.Embed(ctx, inputs)
	if err != nil {
		// Provider implementations are private application infrastructure. Their
		// errors may contain credentials, request bodies, model names, or remote
		// payloads, so even embedding.Error.Unwrap must see only this closed cause.
		code := embedding.CodeProvider
		if classified, ok := embedding.CodeOf(err); ok {
			code = classified
		}
		return nil, embedding.NewError(code, fmt.Errorf("embedding provider failed"))
	}
	return vectors, nil
}

func (manager *Manager) embed(ctx context.Context, model golem.ModelID, provider embedding.Provider, specification embedding.Specification, inputs []embedding.Input) ([]embedding.Vector, error) {
	_, span := observeexec.BeginChild(ctx, model, observe.KindSemantic, observe.OperationSemanticProvider, observe.PhaseAttempt)
	span.SetAggregateCount(int64(len(inputs)))
	vectors, err := callEmbeddingProvider(ctx, provider, inputs)
	if err == nil {
		err = embedding.ValidateResult(specification, inputs, vectors)
	}
	if err != nil {
		code := embedding.CodeProvider
		if classified, ok := embedding.CodeOf(err); ok {
			code = classified
		}
		err = embedding.Failf(code, err, "embedding provider did not return a valid result for a batch of %d inputs", len(inputs))
	}
	finishSemanticObservation(span, err)
	return vectors, err
}

func semanticObservationModel(model ir.ModelID) golem.ModelID {
	var result golem.ModelID
	decoded, err := hex.DecodeString(string(model))
	if err == nil && len(decoded) == len(result) {
		copy(result[:], decoded)
	}
	return result
}

func finishSemanticObservation(span *observeexec.Span, err error) {
	outcome, reason := observe.OutcomeSuccess, observe.ReasonNone
	if err != nil {
		outcome, reason = observe.OutcomeFailure, observe.ReasonProvider
		if errors.Is(err, context.Canceled) {
			outcome, reason = observe.OutcomeCancelled, observe.ReasonNone
		} else if errors.Is(err, context.DeadlineExceeded) {
			outcome, reason = observe.OutcomeCancelled, observe.ReasonTimeout
		} else if code, ok := embedding.CodeOf(err); ok && code == embedding.CodeInvalidInput {
			outcome, reason = observe.OutcomeRefused, observe.ReasonInvalidInput
		}
	}
	observeexec.Finish(span, outcome, reason)
}
