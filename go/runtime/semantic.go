package runtime

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
)

// CallerSimilar executes ordinary caller authorization before distance
// evaluation. Only authorized rows with readable primary identities become
// candidates for provider-native ranking.
func CallerSimilar[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.SemanticResult[M], error) {
	if caller == nil || caller.app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "similar", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	options, err := semanticReadOptions(predicates, query, take)
	if err != nil {
		return nil, err
	}
	rows, err := CallerFindMany(ctx, caller, descriptor, options...)
	if err != nil {
		return nil, err
	}
	if err := validateSemanticCandidateCount(len(rows)); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []golem.SemanticResult[M]{}, nil
	}
	if err := caller.app.semantic.RefreshAll(ctx); err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, caller.app, descriptor, indexName, query, take, rows)
}

func SystemSimilar[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.SemanticResult[M], error) {
	if system.app == nil || ctx == nil {
		return nil, fmt.Errorf("P9_SEMANTIC_RUNTIME: system execution is unavailable")
	}
	options, err := semanticReadOptions(predicates, query, take)
	if err != nil {
		return nil, err
	}
	rows, err := SystemFindMany(ctx, system, descriptor, options...)
	if err != nil {
		return nil, err
	}
	if err := validateSemanticCandidateCount(len(rows)); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []golem.SemanticResult[M]{}, nil
	}
	if err := system.app.semantic.RefreshAll(ctx); err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, system.app, descriptor, indexName, query, take, rows)
}

func semanticReadOptions[M any](predicates []golem.Predicate[M], query string, take int) ([]golem.ReadOption[M], error) {
	if query == "" || take < 1 || take > semanticruntime.MaximumResults || len(predicates) > 1 {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query, result limit, or predicate is invalid"))
	}
	if _, err := embedding.NewInput("query", query); err != nil {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query is invalid"))
	}
	options := make([]golem.ReadOption[M], 0, 2)
	if len(predicates) == 1 {
		options = append(options, golem.Where(predicates[0]))
	}
	// Fetch one row past the portable candidate ceiling so a larger authorized
	// universe fails closed instead of silently ranking an arbitrary prefix.
	options = append(options, golem.Take[M](semanticruntime.MaximumCandidates+1))
	return options, nil
}

func rankSemanticRows[P, A, M any](ctx context.Context, app *App[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, rows []golem.Row[M]) ([]golem.SemanticResult[M], error) {
	// The read deliberately requests one row past the portable ceiling. Enforce
	// that ceiling before extracting readable identities: a conditionally masked
	// primary key must not turn an oversized authorized universe into an
	// arbitrary, silently truncated candidate set.
	if err := validateSemanticCandidateCount(len(rows)); err != nil {
		return nil, err
	}
	var primary []golem.FieldID
	for _, identity := range descriptor.Metadata().Identities() {
		if identity.Kind() == golem.PrimaryIdentity {
			primary = identity.Fields()
			break
		}
	}
	if len(primary) == 0 {
		return nil, fmt.Errorf("P9_SEMANTIC_SCHEMA: model has no primary identity")
	}
	byKey := make(map[string]golem.Row[M], len(rows))
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		key, err := golem.RuntimeSemanticRecordKey(row, primary)
		if err != nil {
			continue
		}
		if _, duplicate := byKey[key]; duplicate {
			continue
		}
		byKey[key] = row
		keys = append(keys, key)
	}
	model := descriptor.Metadata().ModelID()
	ranks, err := app.semantic.Query(ctx, ir.ModelID(hex.EncodeToString(model[:])), indexName, query, keys, take)
	if err != nil {
		return nil, err
	}
	result := make([]golem.SemanticResult[M], 0, len(ranks))
	for _, rank := range ranks {
		row, ok := byKey[rank.Key]
		if !ok {
			return nil, fmt.Errorf("P9_SEMANTIC_QUERY: ranked identity escaped authorized candidates")
		}
		item, err := golem.RuntimeSemanticResult(row, rank.Distance)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func validateSemanticCandidateCount(count int) error {
	if count > semanticruntime.MaximumCandidates {
		return embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic candidate limit exceeded"))
	}
	return nil
}
