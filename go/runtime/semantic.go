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

// CallerSearch executes ordinary caller authorization before distance
// evaluation. Only authorized rows with readable primary identities become
// candidates for provider-native ranking.
func CallerSearch[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.SemanticResult[M], error) {
	if caller == nil || caller.app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "search", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
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
	model := semanticModelID(descriptor.Metadata())
	if err := caller.app.semantic.Refresh(ctx, model, indexName); err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, caller.app, descriptor, take, rows, "", func(ctx context.Context, model ir.ModelID, keys []string) ([]semanticruntime.Rank, error) {
		return caller.app.semantic.Query(ctx, model, indexName, query, keys, take)
	})
}

// CallerSimilar ranks authorized rows against the stored vector of a source row
// the caller is authorized to read. Resolving the source through an ordinary
// authorized unique read is what keeps the neighbourhood of a hidden row from
// becoming a readable projection of it.
func CallerSimilar[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], indexName string, source golem.UniqueSelectorValue[M], take int, predicates ...golem.Predicate[M]) ([]golem.SemanticResult[M], error) {
	if caller == nil || caller.app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "similar", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	options, err := semanticCandidateOptions(predicates, take)
	if err != nil {
		return nil, err
	}
	sourceRow, err := CallerFindUnique(ctx, caller, descriptor, source)
	if err != nil {
		return nil, err
	}
	sourceKey, err := semanticSourceKey(descriptor, sourceRow)
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
	model := semanticModelID(descriptor.Metadata())
	if err := caller.app.semantic.Refresh(ctx, model, indexName); err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, caller.app, descriptor, take, rows, sourceKey, func(ctx context.Context, model ir.ModelID, keys []string) ([]semanticruntime.Rank, error) {
		return caller.app.semantic.QueryByKey(ctx, model, indexName, sourceKey, keys, take)
	})
}

func SystemSearch[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.SemanticResult[M], error) {
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
	model := semanticModelID(descriptor.Metadata())
	if err := system.app.semantic.Refresh(ctx, model, indexName); err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, system.app, descriptor, take, rows, "", func(ctx context.Context, model ir.ModelID, keys []string) ([]semanticruntime.Rank, error) {
		return system.app.semantic.Query(ctx, model, indexName, query, keys, take)
	})
}

func SystemSimilar[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], indexName string, source golem.UniqueSelectorValue[M], take int, predicates ...golem.Predicate[M]) ([]golem.SemanticResult[M], error) {
	if system.app == nil || ctx == nil {
		return nil, fmt.Errorf("P9_SEMANTIC_RUNTIME: system execution is unavailable")
	}
	options, err := semanticCandidateOptions(predicates, take)
	if err != nil {
		return nil, err
	}
	sourceRow, err := SystemFindUnique(ctx, system, descriptor, source)
	if err != nil {
		return nil, err
	}
	sourceKey, err := semanticSourceKey(descriptor, sourceRow)
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
	model := semanticModelID(descriptor.Metadata())
	if err := system.app.semantic.Refresh(ctx, model, indexName); err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, system.app, descriptor, take, rows, sourceKey, func(ctx context.Context, model ir.ModelID, keys []string) ([]semanticruntime.Rank, error) {
		return system.app.semantic.QueryByKey(ctx, model, indexName, sourceKey, keys, take)
	})
}

func semanticReadOptions[M any](predicates []golem.Predicate[M], query string, take int) ([]golem.ReadOption[M], error) {
	if query == "" {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query, result limit, or predicate is invalid"))
	}
	if _, err := embedding.NewInput("query", query); err != nil {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query is invalid"))
	}
	return semanticCandidateOptions(predicates, take)
}

func semanticCandidateOptions[M any](predicates []golem.Predicate[M], take int) ([]golem.ReadOption[M], error) {
	if take < 1 || take > semanticruntime.MaximumResults || len(predicates) > 1 {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query, result limit, or predicate is invalid"))
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

func semanticPrimaryIdentity[M any](descriptor golem.ModelDescriptor[M]) ([]golem.FieldID, error) {
	for _, identity := range descriptor.Metadata().Identities() {
		if identity.Kind() == golem.PrimaryIdentity {
			return identity.Fields(), nil
		}
	}
	return nil, fmt.Errorf("P9_SEMANTIC_SCHEMA: model has no primary identity")
}

// semanticSourceKey reports the source row's record key, and refuses with the
// findUnique refusal when the row cannot carry a semantic identity. A masked
// primary key must not be a distinguishable third state: search skips such rows
// as candidates, but a source that cannot be identified is indistinguishable
// from a source the caller may not read.
func semanticSourceKey[M any](descriptor golem.ModelDescriptor[M], row golem.Row[M]) (string, error) {
	primary, err := semanticPrimaryIdentity(descriptor)
	if err != nil {
		return "", err
	}
	key, err := golem.RuntimeSemanticRecordKey(row, primary)
	if err != nil {
		return "", golem.RuntimeReadError(golem.CodeNotFound, "findUnique", descriptor.Metadata().ModelID(), golem.FieldID{}, "record not found", nil)
	}
	return key, nil
}

func rankSemanticRows[P, A, M any](ctx context.Context, app *App[P, A], descriptor golem.ModelDescriptor[M], take int, rows []golem.Row[M], exclude string, rank func(ctx context.Context, model ir.ModelID, keys []string) ([]semanticruntime.Rank, error)) ([]golem.SemanticResult[M], error) {
	// The read deliberately requests one row past the portable ceiling. Enforce
	// that ceiling before extracting readable identities: a conditionally masked
	// primary key must not turn an oversized authorized universe into an
	// arbitrary, silently truncated candidate set.
	if err := validateSemanticCandidateCount(len(rows)); err != nil {
		return nil, err
	}
	primary, err := semanticPrimaryIdentity(descriptor)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]golem.Row[M], len(rows))
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		key, err := golem.RuntimeSemanticRecordKey(row, primary)
		if err != nil {
			continue
		}
		// Dropping the excluded identity from both the key list and the row map
		// keeps the source out of its own results regardless of predicate, and
		// leaves the escape invariant below to reject a backend that returns it.
		if key == exclude {
			continue
		}
		if _, duplicate := byKey[key]; duplicate {
			continue
		}
		byKey[key] = row
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return []golem.SemanticResult[M]{}, nil
	}
	ranks, err := rank(ctx, semanticModelID(descriptor.Metadata()), keys)
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

func semanticModelID(metadata golem.ModelMetadata) ir.ModelID {
	model := metadata.ModelID()
	return ir.ModelID(hex.EncodeToString(model[:]))
}

func validateSemanticCandidateCount(count int) error {
	if count > semanticruntime.MaximumCandidates {
		return embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic candidate limit exceeded"))
	}
	return nil
}
