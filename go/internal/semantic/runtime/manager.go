package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
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
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
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
	SQL     string
	Args    []any
	Columns []string
	NewScan func() IdentityScan
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

func (manager *Manager) RefreshAll(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return fmt.Errorf("P9_SEMANTIC_RUNTIME: context and manager are required")
	}
	return manager.refreshIndexes(ctx, manager.indexes)
}

func (manager *Manager) Refresh(ctx context.Context, model ir.ModelID, name string) error {
	if manager == nil || ctx == nil || model == "" || name == "" {
		return fmt.Errorf("P9_SEMANTIC_RUNTIME: context, manager, model, and index are required")
	}
	selected, ok := manager.index(model, name)
	if !ok {
		return fmt.Errorf("P9_SEMANTIC_SCHEMA: semantic index is absent")
	}
	return manager.refreshIndexes(ctx, []Index{selected})
}

func (manager *Manager) refreshIndexes(ctx context.Context, indexes []Index) error {
	// Refresh serializes provider work, but arbitrary observer code must never be
	// invoked while that lock is held. Buffer closed records and flush only after
	// releasing it so observer re-entry cannot deadlock semantic refresh.
	deferred := observeexec.NewDeferredObserver(manager.observer)
	err := func() error {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		for _, index := range indexes {
			model := semanticObservationModel(index.Descriptor.ModelID)
			refreshContext, refreshSpan := observeexec.Begin(ctx, deferred, golem.Provider(manager.provider), model, observe.KindSemantic, observe.OperationSemanticRefresh, observe.PhaseFinish)
			refreshErr := manager.refresh(refreshContext, index, refreshSpan)
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
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query is invalid"))
	}
	invalidContext := ctx == nil
	ctx, rankSpan := observeexec.Begin(ctx, manager.observer, golem.Provider(manager.provider), semanticObservationModel(model), observe.KindSemantic, observe.OperationSemanticRank, observe.PhaseFinish)
	defer func() { finishSemanticObservation(rankSpan, resultErr) }()
	if invalidContext || name == "" || query == "" || take < 1 || take > MaximumResults {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query is invalid"))
	}
	selected, ok := manager.index(model, name)
	if !ok {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic index is absent"))
	}
	if err := validateCandidates(selected, candidates); err != nil {
		return nil, err
	}
	input, err := embedding.NewInput("query", query)
	if err != nil {
		return nil, embedding.NewError(embedding.CodeInvalidInput, err)
	}
	vectors, err := manager.embed(ctx, semanticObservationModel(model), selected.Provider, selected.Specification, []embedding.Input{input})
	if err != nil {
		return nil, err
	}
	vector, err := manager.vectorValue(vectors[0])
	if err != nil {
		return nil, embedding.NewError(embedding.CodeProvider, err)
	}
	ranks, err := manager.rankVector(ctx, selected, vector, candidates, "", take)
	if err != nil {
		return nil, err
	}
	rankSpan.SetAggregateCount(int64(len(ranks)))
	return ranks, nil
}

func validateCandidates(index Index, candidates Candidates) error {
	invalid := embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query is invalid"))
	if candidates.SQL == "" || candidates.NewScan == nil || len(index.Descriptor.Identity) == 0 {
		return invalid
	}
	if len(candidates.Columns) != len(index.Descriptor.Identity) {
		return invalid
	}
	for position, column := range index.Descriptor.Identity {
		if candidates.Columns[position] != string(column.Name) {
			return invalid
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
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query is invalid"))
	}
	invalidContext := ctx == nil
	ctx, rankSpan := observeexec.Begin(ctx, manager.observer, golem.Provider(manager.provider), semanticObservationModel(model), observe.KindSemantic, observe.OperationSemanticRank, observe.PhaseFinish)
	defer func() { finishSemanticObservation(rankSpan, resultErr) }()
	if invalidContext || name == "" || sourceKey == "" || take < 1 || take > MaximumResults {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query is invalid"))
	}
	selected, ok := manager.index(model, name)
	if !ok {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic index is absent"))
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

func (manager *Manager) sourceVector(ctx context.Context, index Index, sourceKey string) (any, error) {
	unavailable := fmt.Errorf("P9_SEMANTIC_QUERY: semantic source vector is unavailable")
	statePlaceholder, vectorProjection := "?", "embedding"
	if manager.provider == ir.PostgreSQL {
		statePlaceholder, vectorProjection = "$1", "embedding::text"
	}
	var status string
	observeexec.RecordStatement(ctx)
	if err := manager.database.GetContext(ctx, &status, "SELECT status FROM "+manager.hidden(index, "_state")+" WHERE record_key="+statePlaceholder, sourceKey); err != nil {
		return nil, unavailable
	}
	if status != "ready" {
		return nil, unavailable
	}
	statement := "SELECT " + vectorProjection + " FROM " + manager.hidden(index, "_vec") + " WHERE record_key=" + statePlaceholder
	observeexec.RecordStatement(ctx)
	if manager.provider == ir.SQLite {
		var encoded []byte
		if err := manager.database.GetContext(ctx, &encoded, statement, sourceKey); err != nil || len(encoded) == 0 {
			return nil, unavailable
		}
		return encoded, nil
	}
	var text string
	if err := manager.database.GetContext(ctx, &text, statement, sourceKey); err != nil || text == "" {
		return nil, unavailable
	}
	return text, nil
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
	key      string
	text     string
	hash     [32]byte
	identity []any
}

type stateRecord struct {
	hash        []byte
	fingerprint string
	status      string
}

func (manager *Manager) refresh(ctx context.Context, index Index, span *observeexec.Span) error {
	table, ok := semanticOwnerTable(manager.schema, index.Descriptor.ModelID)
	if !ok || table.PrimaryKey == nil || len(table.PrimaryKey.Columns) == 0 {
		return fmt.Errorf("P9_SEMANTIC_SCHEMA: semantic model has no physical primary identity")
	}
	records, err := manager.scanSources(ctx, table, index)
	if err != nil {
		return fmt.Errorf("P9_SEMANTIC_REFRESH: source scan failed")
	}
	states, err := manager.scanStates(ctx, index)
	if err != nil {
		return fmt.Errorf("P9_SEMANTIC_REFRESH: state scan failed")
	}
	fingerprint := hex.EncodeToString(index.SpaceFingerprint[:])
	dirty := make([]sourceRecord, 0)
	present := make(map[string]bool, len(records))
	for _, record := range records {
		present[record.key] = true
		state, exists := states[record.key]
		if !exists || state.status != "ready" || state.fingerprint != fingerprint || !equalBytes(state.hash, record.hash[:]) {
			dirty = append(dirty, record)
		}
	}
	maximum := index.Specification.MaximumBatch()
	stale := 0
	for key := range states {
		if !present[key] {
			stale++
		}
	}
	span.SetAggregateCount(int64(len(dirty) + stale))
	for offset := 0; offset < len(dirty); offset += maximum {
		end := offset + maximum
		if end > len(dirty) {
			end = len(dirty)
		}
		batch := dirty[offset:end]
		inputs := make([]embedding.Input, len(batch))
		for position, record := range batch {
			// Provider results are positional. Use a batch-local correlation key so
			// the provider never receives the canonical database identity retained
			// by Golem's private state and vector tables.
			input, inputErr := embedding.NewInput("source-"+strconv.Itoa(position), record.text)
			if inputErr != nil {
				return embedding.NewError(embedding.CodeInvalidInput, inputErr)
			}
			inputs[position] = input
		}
		vectors, embedErr := manager.embed(ctx, semanticObservationModel(index.Descriptor.ModelID), index.Provider, index.Specification, inputs)
		if embedErr != nil {
			return embedErr
		}
		if err := manager.storeBatch(ctx, index, fingerprint, batch, vectors); err != nil {
			return fmt.Errorf("P9_SEMANTIC_REFRESH: vector storage failed")
		}
	}
	for key := range states {
		if !present[key] {
			if err := manager.deleteRecord(ctx, index, key); err != nil {
				return fmt.Errorf("P9_SEMANTIC_REFRESH: stale vector cleanup failed")
			}
		}
	}
	return nil
}

func (manager *Manager) scanSources(ctx context.Context, table physical.PhysicalTable, index Index) ([]sourceRecord, error) {
	columns := make(map[ir.FieldID]physical.PhysicalColumn, len(table.Columns))
	for _, column := range table.Columns {
		columns[column.ID] = column
	}
	selected := make([]ir.FieldID, 0, len(table.PrimaryKey.Columns)+len(index.Descriptor.Fields))
	positions := make(map[ir.FieldID]int)
	appendField := func(field ir.FieldID) error {
		if _, exists := positions[field]; exists {
			return nil
		}
		if _, exists := columns[field]; !exists {
			return fmt.Errorf("field is absent")
		}
		positions[field] = len(selected)
		selected = append(selected, field)
		return nil
	}
	for _, field := range table.PrimaryKey.Columns {
		if err := appendField(field); err != nil {
			return nil, err
		}
	}
	for _, field := range index.Descriptor.Fields {
		if err := appendField(field); err != nil {
			return nil, err
		}
	}
	names := make([]string, len(selected))
	for position, field := range selected {
		names[position] = manager.quote(columns[field].Name)
	}
	order := make([]string, len(table.PrimaryKey.Columns))
	for position, field := range table.PrimaryKey.Columns {
		order[position] = manager.quote(columns[field].Name)
	}
	query := "SELECT " + strings.Join(names, ",") + " FROM " + manager.table(table.Name) + " ORDER BY " + strings.Join(order, ",")
	observeexec.RecordStatement(ctx)
	rows, err := manager.database.QueryxContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]sourceRecord, 0)
	for rows.Next() {
		values := make([]any, len(selected))
		destinations := make([]any, len(selected))
		for position := range values {
			destinations[position] = &values[position]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		keys := make([]any, len(table.PrimaryKey.Columns))
		for position, field := range table.PrimaryKey.Columns {
			keys[position] = values[positions[field]]
		}
		key, err := semantickey.Encode(keys)
		if err != nil {
			return nil, err
		}
		var document strings.Builder
		document.WriteString("golem-semantic-document:v1")
		for _, field := range index.Descriptor.Fields {
			text, present, err := semanticText(values[positions[field]])
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

func (manager *Manager) scanStates(ctx context.Context, index Index) (map[string]stateRecord, error) {
	query := "SELECT " + manager.quote("record_key") + "," + manager.quote("source_hash") + "," + manager.quote("space_fingerprint") + "," + manager.quote("status") + " FROM " + manager.hidden(index, "_state")
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
		if err := rows.Scan(&key, &hash, &fingerprint, &status); err != nil {
			return nil, err
		}
		result[key] = stateRecord{hash: append([]byte(nil), hash...), fingerprint: fingerprint, status: status}
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
	for position, record := range records {
		if len(record.identity) != len(index.Descriptor.Identity) {
			return fmt.Errorf("P9_SEMANTIC_REFRESH: identity width does not match the shadow contract")
		}
		vectorValue, err := manager.vectorValue(vectors[position])
		if err != nil {
			return err
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
			observeexec.RecordStatement(ctx)
			arguments := append([]any{record.key, record.hash[:], fingerprint, time.Now().UTC().UnixMicro()}, record.identity...)
			if _, err := transaction.ExecContext(ctx, "INSERT INTO "+manager.hidden(index, "_state")+" (record_key,source_hash,space_fingerprint,status,attempt_count,error_code,updated_at"+columns+") VALUES (?,?,?,'ready',1,NULL,?"+values+") ON CONFLICT(record_key) DO UPDATE SET source_hash=excluded.source_hash,space_fingerprint=excluded.space_fingerprint,status='ready',attempt_count=attempt_count+1,error_code=NULL,updated_at=excluded.updated_at"+assignments, arguments...); err != nil {
				return err
			}
		} else {
			observeexec.RecordStatement(ctx)
			if _, err := transaction.ExecContext(ctx, "INSERT INTO "+manager.hidden(index, "_vec")+" (record_key,embedding) VALUES ($1,$2::vector) ON CONFLICT(record_key) DO UPDATE SET embedding=excluded.embedding", record.key, vectorValue); err != nil {
				return err
			}
			observeexec.RecordStatement(ctx)
			arguments := append([]any{record.key, record.hash[:], fingerprint, time.Now().UTC().UnixMicro()}, record.identity...)
			if _, err := transaction.ExecContext(ctx, "INSERT INTO "+manager.hidden(index, "_state")+" (record_key,source_hash,space_fingerprint,status,attempt_count,error_code,updated_at"+columns+") VALUES ($1,$2,$3,'ready',1,NULL,$4"+values+") ON CONFLICT(record_key) DO UPDATE SET source_hash=excluded.source_hash,space_fingerprint=excluded.space_fingerprint,status='ready',attempt_count="+manager.hidden(index, "_state")+".attempt_count+1,error_code=NULL,updated_at=excluded.updated_at"+assignments, arguments...); err != nil {
				return err
			}
		}
	}
	return transaction.Commit()
}

func (manager *Manager) deleteRecord(ctx context.Context, index Index, key string) error {
	transaction, err := manager.database.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	placeholder := "?"
	if manager.provider == ir.PostgreSQL {
		placeholder = "$1"
	}
	for _, suffix := range []string{"_vec", "_state"} {
		observeexec.RecordStatement(ctx)
		if _, err := transaction.ExecContext(ctx, "DELETE FROM "+manager.hidden(index, suffix)+" WHERE "+manager.quote("record_key")+"="+placeholder, key); err != nil {
			return err
		}
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
		return nil, fmt.Errorf("embedding provider failed")
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
		err = embedding.NewError(embedding.CodeProvider, err)
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
