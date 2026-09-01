// Package provider defines the provider-neutral durable job store contract.
// Implementations own their SQL, their DDL bootstrap, and database-time
// semantics; callers never receive executable SQL and never name a table.
package provider

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// State is the durable job state. It mirrors queue.State without importing the
// public package into provider implementations.
type State string

const (
	StatePending   State = "pending"
	StateLeased    State = "leased"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
)

const (
	MaximumClaimJobs      = 256
	MaximumClaimTypes     = 256
	MaximumRetentionRows  = 4096
	MaximumPayloadBytes   = 1 << 20
	MaximumKeyBytes       = 256
	MaximumOperatorBatch  = 256
	MaximumIdentityBytes  = 64
	CodeAttemptsExhausted = "attempts_exhausted"
	CodeCanceled          = "canceled"
)

// ErrNotFound reports an inspection of an absent job.
var ErrNotFound = errors.New("QUEUE_JOB_NOT_FOUND: job is absent")

var canonicalName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// Record is one sanitized durable job row. It carries the payload bytes a
// worker must decode and no SQL, driver text, or lease bookkeeping beyond the
// token that fences this owner's transitions.
type Record struct {
	ID              string
	Type            string
	Payload         []byte
	State           State
	AttemptCount    int64
	MaxAttempts     int64
	AvailableAt     time.Time
	LeaseToken      string
	LeaseUntil      *time.Time
	DedupeKey       string
	ExclusiveKey    string
	CancelRequested bool
	LastCode        string
	EnqueuedAt      time.Time
	FinishedAt      *time.Time
	UpdatedAt       time.Time
}

// EnqueueRequest is one durable insert. ID is the caller's proposed identity;
// a dedupe collision with active work returns the existing identity instead.
type EnqueueRequest struct {
	ID           string
	Type         string
	Payload      []byte
	MaxAttempts  int
	Delay        time.Duration
	DedupeKey    string
	ExclusiveKey string
}

// EnqueueResult identifies the active job selected by an enqueue and whether
// this request inserted it. State is the active row state observed while the
// enqueue decision was serialized.
type EnqueueResult struct {
	ID       string
	State    State
	Inserted bool
}

// ClaimOptions bounds one claim. An empty Types list claims nothing, which is
// how a worker whose every registered type is saturated stands down.
type ClaimOptions struct {
	Types         []string
	Limit         int
	LeaseDuration time.Duration
	Resource      *ClaimResource
}

// ClaimResource is one normalized fleet-wide weighted concurrency budget.
type ClaimResource struct {
	Name        string
	Concurrency int64
	Costs       map[string]int64
}

// Renewal is the result of one fenced heartbeat. CancelRequested carries the
// durable cancellation flag, so renewal doubles as the cancellation poll.
type Renewal struct {
	Renewed         bool
	CancelRequested bool
}

// RetentionPolicy bounds one retention run over terminal rows.
type RetentionPolicy struct {
	OlderThan time.Time
	MaxRows   int
	States    []State
}

// Summary is the payload-free projection used by queue operators.
type Summary struct {
	ID              string
	Type            string
	State           State
	AttemptCount    int64
	MaxAttempts     int64
	AvailableAt     time.Time
	CancelRequested bool
	LastCode        string
	EnqueuedAt      time.Time
	FinishedAt      *time.Time
}

// FailedCursor is the stable position immediately after one failed job.
type FailedCursor struct {
	FinishedAt time.Time
	ID         string
}

// FailedQuery selects one bounded page of failed jobs.
type FailedQuery struct {
	Types  []string
	Limit  int
	Before *FailedCursor
}

// FailedPage carries payload-free failed jobs and whether another page exists.
type FailedPage struct {
	Jobs []Summary
	More bool
}

// JobCursor is the stable position immediately after one listed job.
type JobCursor struct {
	EnqueuedAt time.Time
	ID         string
}

// JobQuery selects one bounded payload-free page of jobs.
type JobQuery struct {
	Types  []string
	States []State
	Limit  int
	Before *JobCursor
}

// JobPage carries payload-free jobs and whether another page exists.
type JobPage struct {
	Jobs []Summary
	More bool
}

// CountQuery selects the job types included in state counts.
type CountQuery struct {
	Types []string
}

// StateCounts is the number of stored jobs in each durable state.
type StateCounts struct {
	Pending   int64
	Leased    int64
	Succeeded int64
	Failed    int64
	Canceled  int64
}

// CancelResult distinguishes immediate terminal cancellation from a durable
// request observed later by the lease owner.
type CancelResult struct {
	Changed      bool
	Terminal     bool
	Type         string
	AttemptCount int64
}

// CancelBatch carries partial progress and the immediate terminal transitions
// an operator must observe.
type CancelBatch struct {
	Changed  int
	Terminal []CancelResult
}

// Executor is the seam a transactional enqueue runs on. Both *sqlx.DB and
// *sqlx.Tx satisfy it, so an enqueue can join the caller's transaction rather
// than escaping to the pool.
type Executor interface {
	ExecContext(ctx context.Context, query string, arguments ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row
}

// Store is the sole mutable seam for _golem_queue. Every token-fenced
// transition reports changed=false for a stale lease rather than allowing it to
// mutate a row another worker now owns.
type Store interface {
	EnsureSchema(ctx context.Context) error
	Enqueue(ctx context.Context, executor Executor, request EnqueueRequest) (EnqueueResult, error)
	Claim(ctx context.Context, options ClaimOptions) ([]Record, error)
	Renew(ctx context.Context, id, token string, duration time.Duration) (Renewal, error)
	Succeed(ctx context.Context, id, token, code string) (bool, error)
	Fail(ctx context.Context, id, token, code string) (bool, error)
	RetryAt(ctx context.Context, id, token string, delay time.Duration, code string, uncounted bool) (bool, error)
	MarkCanceled(ctx context.Context, id, token, code string) (bool, error)
	Release(ctx context.Context, id, token string) (bool, error)
	Inspect(ctx context.Context, id string) (Record, error)
	List(ctx context.Context, query JobQuery) (JobPage, error)
	ListFailed(ctx context.Context, query FailedQuery) (FailedPage, error)
	CountByState(ctx context.Context, query CountQuery) (StateCounts, error)
	Cancel(ctx context.Context, id string) (CancelResult, error)
	CancelMany(ctx context.Context, ids []string) (CancelBatch, error)
	Requeue(ctx context.Context, id string) (bool, error)
	RequeueFailed(ctx context.Context, ids []string) (int, error)
	RunRetention(ctx context.Context, policy RetentionPolicy) (int, error)
}

func ValidateEnqueue(request EnqueueRequest) error {
	if request.ID == "" || len(request.ID) > MaximumIdentityBytes {
		return fmt.Errorf("QUEUE_ENQUEUE_LIMIT: job identity must be within 1..%d bytes", MaximumIdentityBytes)
	}
	if !canonicalName.MatchString(request.Type) {
		return fmt.Errorf("QUEUE_ENQUEUE_LIMIT: job type is not canonical")
	}
	if len(request.Payload) == 0 || len(request.Payload) > MaximumPayloadBytes {
		return fmt.Errorf("QUEUE_ENQUEUE_LIMIT: payload must be within 1..%d bytes", MaximumPayloadBytes)
	}
	if request.MaxAttempts <= 0 {
		return fmt.Errorf("QUEUE_ENQUEUE_LIMIT: max attempts must be positive")
	}
	if len(request.DedupeKey) > MaximumKeyBytes || len(request.ExclusiveKey) > MaximumKeyBytes {
		return fmt.Errorf("QUEUE_ENQUEUE_LIMIT: keys must be at most %d bytes", MaximumKeyBytes)
	}
	return ValidateDelay(request.Delay)
}

func ValidateClaim(options ClaimOptions) error {
	if options.Limit <= 0 || options.Limit > MaximumClaimJobs {
		return fmt.Errorf("QUEUE_CLAIM_LIMIT: claim limit must be within 1..%d", MaximumClaimJobs)
	}
	if len(options.Types) > MaximumClaimTypes {
		return fmt.Errorf("QUEUE_CLAIM_LIMIT: claim covers more than %d types", MaximumClaimTypes)
	}
	for _, name := range options.Types {
		if !canonicalName.MatchString(name) {
			return fmt.Errorf("QUEUE_CLAIM_LIMIT: claimed type is not canonical")
		}
	}
	if options.Resource != nil {
		if err := ValidateClaimResource(*options.Resource); err != nil {
			return err
		}
		for _, name := range options.Types {
			if _, exists := options.Resource.Costs[name]; !exists {
				return fmt.Errorf("QUEUE_CLAIM_LIMIT: claimed type is absent from resource")
			}
		}
	}
	return ValidateLease(options.LeaseDuration)
}

// ValidateClaimResource refuses ambiguous or unbounded shared-resource plans.
func ValidateClaimResource(resource ClaimResource) error {
	if !canonicalName.MatchString(resource.Name) {
		return fmt.Errorf("QUEUE_CLAIM_LIMIT: resource name is not canonical")
	}
	if resource.Concurrency <= 0 {
		return fmt.Errorf("QUEUE_CLAIM_LIMIT: resource concurrency must be positive")
	}
	if len(resource.Costs) == 0 || len(resource.Costs) > MaximumClaimTypes {
		return fmt.Errorf("QUEUE_CLAIM_LIMIT: resource must cover within 1..%d types", MaximumClaimTypes)
	}
	for name, cost := range resource.Costs {
		if !canonicalName.MatchString(name) {
			return fmt.Errorf("QUEUE_CLAIM_LIMIT: resource type is not canonical")
		}
		if cost <= 0 || cost > resource.Concurrency {
			return fmt.Errorf("QUEUE_CLAIM_LIMIT: resource cost must be within its concurrency")
		}
	}
	return nil
}

func ValidateLease(duration time.Duration) error {
	if duration <= 0 || duration > 10*time.Minute || duration%time.Microsecond != 0 {
		return fmt.Errorf("QUEUE_CLAIM_LIMIT: lease duration must be positive, microsecond-exact, and at most 10m")
	}
	return nil
}

func ValidateDelay(delay time.Duration) error {
	if delay < 0 || delay > 24*time.Hour || delay%time.Microsecond != 0 {
		return fmt.Errorf("QUEUE_DELAY_LIMIT: delay must be non-negative, microsecond-exact, and at most 24h")
	}
	return nil
}

func ValidateCode(code string) error {
	if code == "" {
		return nil
	}
	if !canonicalName.MatchString(code) {
		return fmt.Errorf("QUEUE_CODE: code is not canonical")
	}
	return nil
}

func ValidateJobIdentity(id string) error {
	if id == "" || len(id) > MaximumIdentityBytes {
		return fmt.Errorf("QUEUE_IDENTITY: job identity must be within 1..%d bytes", MaximumIdentityBytes)
	}
	return nil
}

func ValidateIdentity(id, token string) error {
	if err := ValidateJobIdentity(id); err != nil {
		return err
	}
	if token == "" || len(token) > MaximumIdentityBytes {
		return fmt.Errorf("QUEUE_IDENTITY: lease token must be within 1..%d bytes", MaximumIdentityBytes)
	}
	return nil
}

func ValidateRetention(policy RetentionPolicy) error {
	if policy.OlderThan.IsZero() {
		return fmt.Errorf("QUEUE_RETENTION_LIMIT: retention floor is zero")
	}
	if policy.MaxRows <= 0 || policy.MaxRows > MaximumRetentionRows {
		return fmt.Errorf("QUEUE_RETENTION_LIMIT: retention rows must be within 1..%d", MaximumRetentionRows)
	}
	if len(policy.States) > 3 {
		return fmt.Errorf("QUEUE_RETENTION_LIMIT: retention covers more than 3 states")
	}
	seen := make(map[State]struct{}, len(policy.States))
	for _, state := range policy.States {
		switch state {
		case StateSucceeded, StateFailed, StateCanceled:
		default:
			return fmt.Errorf("QUEUE_RETENTION_LIMIT: retention state is not terminal")
		}
		if _, exists := seen[state]; exists {
			return fmt.Errorf("QUEUE_RETENTION_LIMIT: retention state is repeated")
		}
		seen[state] = struct{}{}
	}
	return nil
}

// RetentionStates returns the selected terminal states or every terminal state.
func RetentionStates(policy RetentionPolicy) []State {
	if len(policy.States) != 0 {
		return append([]State(nil), policy.States...)
	}
	return []State{StateSucceeded, StateFailed, StateCanceled}
}

// ValidateFailedQuery refuses unbounded or unstable operator discovery.
func ValidateFailedQuery(query FailedQuery) error {
	if query.Limit <= 0 || query.Limit > MaximumOperatorBatch {
		return fmt.Errorf("QUEUE_OPERATOR_LIMIT: page size must be within 1..%d", MaximumOperatorBatch)
	}
	if err := validateOperatorTypes(query.Types); err != nil {
		return err
	}
	if query.Before != nil && (query.Before.FinishedAt.IsZero() || query.Before.ID == "" || len(query.Before.ID) > 64) {
		return fmt.Errorf("QUEUE_OPERATOR_LIMIT: failed cursor is invalid")
	}
	return nil
}

// ValidateJobQuery refuses unbounded or unstable operator discovery.
func ValidateJobQuery(query JobQuery) error {
	if query.Limit <= 0 || query.Limit > MaximumOperatorBatch {
		return fmt.Errorf("QUEUE_OPERATOR_LIMIT: page size must be within 1..%d", MaximumOperatorBatch)
	}
	if err := validateOperatorTypes(query.Types); err != nil {
		return err
	}
	if len(query.States) > 5 {
		return fmt.Errorf("QUEUE_OPERATOR_LIMIT: query covers more than 5 states")
	}
	seen := make(map[State]struct{}, len(query.States))
	for _, state := range query.States {
		if !validState(state) {
			return fmt.Errorf("QUEUE_OPERATOR_LIMIT: job state is invalid")
		}
		if _, exists := seen[state]; exists {
			return fmt.Errorf("QUEUE_OPERATOR_LIMIT: job state is repeated")
		}
		seen[state] = struct{}{}
	}
	if query.Before != nil && (query.Before.EnqueuedAt.IsZero() || query.Before.ID == "" || len(query.Before.ID) > 64) {
		return fmt.Errorf("QUEUE_OPERATOR_LIMIT: job cursor is invalid")
	}
	return nil
}

// ValidateCountQuery refuses ambiguous operator counts.
func ValidateCountQuery(query CountQuery) error {
	return validateOperatorTypes(query.Types)
}

func validateOperatorTypes(types []string) error {
	if len(types) > MaximumClaimTypes {
		return fmt.Errorf("QUEUE_OPERATOR_LIMIT: query covers more than %d types", MaximumClaimTypes)
	}
	seen := make(map[string]struct{}, len(types))
	for _, name := range types {
		if !canonicalName.MatchString(name) {
			return fmt.Errorf("QUEUE_OPERATOR_LIMIT: job type is not canonical")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("QUEUE_OPERATOR_LIMIT: job type is repeated")
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validState(state State) bool {
	switch state {
	case StatePending, StateLeased, StateSucceeded, StateFailed, StateCanceled:
		return true
	default:
		return false
	}
}

// ValidateOperatorIDs refuses unbounded or ambiguous bulk recovery.
func ValidateOperatorIDs(ids []string) error {
	if len(ids) > MaximumOperatorBatch {
		return fmt.Errorf("QUEUE_OPERATOR_LIMIT: recovery batch must contain at most %d jobs", MaximumOperatorBatch)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 64 {
			return fmt.Errorf("QUEUE_OPERATOR_LIMIT: job identity is invalid")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("QUEUE_OPERATOR_LIMIT: job identity is repeated")
		}
		seen[id] = struct{}{}
	}
	return nil
}

// NewIdentifier returns a canonical UUIDv4 without importing a general UUID
// package into provider implementations. It supplies both job identities and
// per-claim lease tokens.
func NewIdentifier() (string, error) {
	var value [16]byte
	if _, err := cryptorand.Read(value[:]); err != nil {
		return "", fmt.Errorf("QUEUE_IDENTITY: random source: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

// CloneRecords copies every record's payload so a claimed batch cannot alias
// provider buffers.
func CloneRecords(records []Record) []Record {
	result := make([]Record, len(records))
	for index, record := range records {
		result[index] = record
		result[index].Payload = append([]byte(nil), record.Payload...)
	}
	return result
}
