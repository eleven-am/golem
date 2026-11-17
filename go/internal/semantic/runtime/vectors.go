package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

// VectorSources is the closed read renderer's mapping from input positions to
// owner identities. Candidates carries the independently authorized row and
// index-field scope; joining both in one statement keeps one database snapshot.
type VectorSources struct {
	SQL            string
	Args           []any
	Columns        []string
	PositionColumn string
	Count          int
}

func (manager *Manager) VectorsOn(ctx context.Context, queryer sqlx.QueryerContext, model ir.ModelID, name string, candidates Candidates, sources VectorSources) (result map[int]embedding.QueryVector, resultErr error) {
	if manager == nil || queryer == nil || ctx == nil {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic vector read requires a manager, executor and context")
	}
	ctx, span := observeexec.Begin(ctx, manager.observer, golem.Provider(manager.provider), semanticObservationModel(model), observe.KindSemantic, observe.OperationSemanticVectors, observe.PhaseFinish)
	defer func() { finishSemanticObservation(span, resultErr) }()
	if sources.Count < 1 || sources.Count > MaximumResults || sources.SQL == "" || sources.PositionColumn == "" {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic vector source request is invalid")
	}
	index, err := manager.rankIndex(model, name, candidates, sources.Count)
	if err != nil {
		return nil, err
	}
	if len(sources.Columns) != len(index.Descriptor.Identity) {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic source identity width does not match the index")
	}
	for i, column := range index.Descriptor.Identity {
		if sources.Columns[i] != string(column.Name) {
			return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic source identity column does not match the index")
		}
	}
	provider := policyir.ProviderSQLite
	if manager.provider == ir.PostgreSQL {
		provider = policyir.ProviderPostgreSQL
	}
	requestedSQL := policysql.RebasePlaceholders(sources.SQL, len(candidates.Args), provider)
	position := manager.quote(physical.PhysicalName(sources.PositionColumn))
	matchesColumn := manager.quote(physical.PhysicalName(sources.PositionColumn + "_matches"))
	projection := []string{"golem_requested." + position}
	joins, stateJoins := make([]string, len(sources.Columns)), make([]string, len(sources.Columns))
	for i, name := range sources.Columns {
		column := manager.quote(physical.PhysicalName(name))
		projection = append(projection, "golem_requested."+column)
		joins[i] = "golem_requested." + column + "=golem_allowed." + column
		stateJoins[i] = "golem_sources." + column + "=golem_ss." + column
	}
	// Count before readiness joins: a nullable unique selector can match more
	// than one authorized owner, even when only one has a ready vector.
	projection = append(projection, "COUNT(*) OVER (PARTITION BY golem_requested."+position+") AS "+matchesColumn)
	authorized := "SELECT " + strings.Join(projection, ",") + " FROM (" + requestedSQL + ") AS golem_requested JOIN (" + candidates.SQL + ") AS golem_allowed ON " + strings.Join(joins, " AND ")
	embeddingColumn := "golem_sv.embedding"
	if manager.provider == ir.PostgreSQL {
		embeddingColumn += "::text"
	}
	args := append(append([]any{}, candidates.Args...), sources.Args...)
	args = append(args, hex.EncodeToString(index.SpaceFingerprint[:]))
	statement := "SELECT golem_sources." + position + ",golem_sources." + matchesColumn + ",CASE WHEN golem_sv.record_key IS NULL THEN 0 ELSE 1 END," + embeddingColumn +
		" FROM (" + authorized + ") AS golem_sources LEFT JOIN " + manager.hidden(index, "_state") + " AS golem_ss ON " + strings.Join(stateJoins, " AND ") +
		" AND golem_ss.space_fingerprint=" + manager.placeholder(len(args)) + " AND golem_ss.status='ready'" +
		" LEFT JOIN " + manager.hidden(index, "_vec") + " AS golem_sv ON golem_sv.record_key=golem_ss.record_key ORDER BY golem_sources." + position
	if len(args) > candidates.MaxStatementParameters {
		return nil, golem.RuntimeLimitError("vectors", semanticObservationModel(model), golem.LimitStatementParameters, candidates.MaxStatementParameters, "semantic vector read exceeds the statement parameter limit", nil)
	}
	if err := readsql.ValidateStatementComplexity(candidates.Model, statement, candidates.MaxStatementBytes, candidates.MaxStatementAliases); err != nil {
		return nil, err
	}
	recordQueryerStatement(ctx, queryer)
	rows, err := queryer.QueryxContext(ctx, statement, args...)
	if err != nil {
		return nil, embedding.Failf(embedding.CodeUnavailable, err, "semantic vector read failed")
	}
	defer rows.Close()
	result = make(map[int]embedding.QueryVector)
	seen := make(map[int]bool)
	for rows.Next() {
		var ordinal, matches, available int
		var encoded []byte
		if err := rows.Scan(&ordinal, &matches, &available, &encoded); err != nil {
			return nil, embedding.Failf(embedding.CodeProvider, err, "semantic vector row could not be decoded")
		}
		if ordinal < 0 || ordinal >= sources.Count || matches != 1 || seen[ordinal] {
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "vectors", semanticObservationModel(model), golem.FieldID{}, "unique read returned an invalid cardinality", nil)
		}
		seen[ordinal] = true
		if available == 0 {
			continue
		}
		vector, err := manager.decodeStoredVector(index, encoded)
		if err != nil {
			return nil, embedding.Failf(embedding.CodeProvider, err, "stored semantic vector is invalid")
		}
		result[ordinal] = vector
	}
	if err := rows.Err(); err != nil {
		return nil, embedding.Failf(embedding.CodeUnavailable, err, "semantic vector read failed")
	}
	span.SetAggregateCount(int64(len(result)))
	return result, nil
}

func (manager *Manager) decodeStoredVector(index Index, encoded []byte) (embedding.QueryVector, error) {
	var values []float32
	var err error
	if manager.provider == ir.SQLite {
		values, err = sqlitevec.Deserialize(encoded, index.Specification.Dimensions())
	} else {
		err = json.Unmarshal(encoded, &values)
		if err == nil {
			err = sqlitevec.ValidateVector(values, index.Specification.Dimensions())
		}
	}
	if err != nil {
		return embedding.QueryVector{}, err
	}
	vector, err := embedding.NewVector(values)
	if err != nil {
		return embedding.QueryVector{}, err
	}
	return embedding.NewQueryVector(index.Specification, vector)
}
