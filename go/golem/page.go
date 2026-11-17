package golem

import (
	"bytes"
	"fmt"
	"slices"
	"time"

	"github.com/eleven-am/golem/go/internal/pagecontract"
)

// PageQuery declares a custom read with runtime-owned continuation admission
// and a bounded candidate batch. Its resolver receives PageRequest[Args] and
// returns PageBatch[Item]. PageLimits and PageRevision are both required.
func PageQuery[Resolver any](_ *GraphQLSchema, _ string, _ Resolver, _ ...PageOption) {}

type PageOption interface{ pageOption() }
type pageOption struct{}

func (pageOption) pageOption() {}

// PageLimits declares the default and maximum requested items and the maximum
// candidates consumed per request. Denied/deleted candidates still consume the
// budget; a batch may produce an empty page with a continuation.
func PageLimits(_ uint32, _ uint32, _ uint32) PageOption { return pageOption{} }

// PageRevision identifies the ordering/snapshot contract. Changing it
// invalidates continuations issued for an earlier revision.
func PageRevision(_ string) PageOption { return pageOption{} }

// ContinuationKey is deployment-owned AES-256 key material. Every serving
// instance must share its keyring. Retain retiring decrypt keys through the
// maximum chain lifetime, and rotate before 2^32 encryptions per shared key.
type ContinuationKey struct {
	ID       string
	Material [32]byte
}

// PageInput starts or resumes a custom page. Take zero uses the declaration's
// default. After is opaque and never supplies authorization.
type PageInput struct {
	Take  uint32
	After string
}

// PageRequest contains admitted arguments and authenticated traversal state.
// The resolver must preserve its declared immutable ordering or rank snapshot;
// a seed alone does not freeze a changing ranked candidate set.
type PageRequest[A any] struct {
	arguments A
	state     RuntimePageState
}

func (request PageRequest[A]) Arguments() A           { return request.arguments }
func (request PageRequest[A]) Take() uint32           { return request.state.take }
func (request PageRequest[A]) CandidateLimit() uint32 { return request.state.candidates }
func (request PageRequest[A]) Consumed() uint64       { return request.state.consumed }
func (request PageRequest[A]) State() []byte          { return bytes.Clone(request.state.position) }
func (request PageRequest[A]) Expires() time.Time     { return request.state.expires }

// PageBatch is a resolver result, distinct from the served GraphQL page. Only the
// runtime can turn its private checkpoint into an authenticated continuation.
type PageBatch[T any] struct {
	items    []T
	consumed uint32
	next     []byte
	more     bool
}

// PageItems returns a candidate batch. Consumed includes candidates removed by
// authorization and output gates. A nil nextState ends traversal; a non-nil
// checkpoint requires positive consumption and different, nonempty state.
func PageItems[T any](items []T, consumed uint32, nextState []byte) PageBatch[T] {
	// Keep a bounded invalid witness rather than copying an arbitrarily large
	// programmer error. The extra element/byte exceeds every legal declared
	// maximum, so registered execution refuses it; no truncated page is served.
	if len(items) > pagecontract.MaxItems+1 {
		items = items[:pagecontract.MaxItems+1]
	}
	if len(nextState) > pagecontract.MaxCheckpointBytes+1 {
		nextState = nextState[:pagecontract.MaxCheckpointBytes+1]
	}
	return PageBatch[T]{items: append([]T(nil), items...), consumed: consumed, next: bytes.Clone(nextState), more: nextState != nil}
}

// RuntimePageState is the representation-opaque generated-glue bridge. It is
// admitted by the registered runtime page dispatcher, not by a resolver.
type RuntimePageState struct {
	take       uint32
	candidates uint32
	consumed   uint64
	position   []byte
	expires    time.Time
}

func RuntimePageStateValue(take, candidates uint32, consumed uint64, position []byte, expires time.Time) RuntimePageState {
	return RuntimePageState{take: take, candidates: candidates, consumed: consumed, position: bytes.Clone(position), expires: expires}
}

func RuntimePageRequestValue[A any](arguments A, state RuntimePageState) PageRequest[A] {
	state.position = bytes.Clone(state.position)
	return PageRequest[A]{arguments: arguments, state: state}
}

func RuntimePageBatchParts[T any](batch PageBatch[T]) ([]T, uint32, []byte, bool) {
	return append([]T(nil), batch.items...), batch.consumed, bytes.Clone(batch.next), batch.more
}

// RuntimePageResult defers token issuance until the result adapter has
// validated/hydrated the batch. It is generated glue, not a resolver result.
type RuntimePageResult struct {
	items    []any
	finalize func() (string, error)
}

func RuntimePageResultValue(items []any, finalize func() (string, error)) RuntimePageResult {
	return RuntimePageResult{items: append([]any(nil), items...), finalize: finalize}
}

func (result RuntimePageResult) Items() []any { return append([]any(nil), result.items...) }
func (result RuntimePageResult) Finalize() (string, error) {
	if result.finalize == nil {
		return "", fmt.Errorf("page result is not owned by a runtime execution")
	}
	return result.finalize()
}

// GeneratedPageQuery is the sealed registration for a pageable read. It is
// deliberately separate from operation registrations that can carry Within
// grants: registering a page must never grant a mutation capability.
type GeneratedPageQuery struct {
	reference   operationReference
	extensionID string
}

func GeneratedPageQueryOperation[R any](extensionID string, resolver R) GeneratedPageQuery {
	return GeneratedPageQuery{reference: operationReferenceOf(resolver), extensionID: extensionID}
}

func GeneratedApplicationPageQueries[A any](bindings ApplicationBindings[A], queries ...GeneratedPageQuery) (ApplicationBindings[A], error) {
	if bindings.generation == (SchemaDigest{}) {
		return ApplicationBindings[A]{}, fmt.Errorf("generated page queries: application bindings are unstamped")
	}
	table := make(map[uintptr]GeneratedPageQuery, len(queries))
	ids := make(map[string]bool, len(queries))
	for _, query := range queries {
		if query.reference.entry == 0 || query.extensionID == "" || ids[query.extensionID] {
			return ApplicationBindings[A]{}, fmt.Errorf("generated page queries require unique registered resolver identities")
		}
		if _, duplicate := table[query.reference.entry]; duplicate {
			return ApplicationBindings[A]{}, fmt.Errorf("generated page query resolver is registered twice")
		}
		if _, mutation := bindings.operations[query.reference.entry]; mutation {
			return ApplicationBindings[A]{}, fmt.Errorf("generated page query cannot also be a mutation or transaction operation")
		}
		table[query.reference.entry], ids[query.extensionID] = query, true
	}
	result := bindings
	result.pages = table
	return result, nil
}

func (bindings ApplicationBindings[A]) PageQuery(resolver any) (string, bool) {
	query, ok := bindings.pages[operationReferenceOf(resolver).entry]
	return query.extensionID, ok
}

func (bindings ApplicationBindings[A]) PageQueryInventory() []string {
	ids := make([]string, 0, len(bindings.pages))
	for _, query := range bindings.pages {
		ids = append(ids, query.extensionID)
	}
	slices.Sort(ids)
	return ids
}
