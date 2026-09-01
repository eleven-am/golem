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

// ClaimOptions bounds one claim. An empty Types list claims nothing, which is
// how a worker whose every registered type is saturated stands down.
type ClaimOptions struct {
	Types         []string
	Limit         int
	LeaseDuration time.Duration
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

// CancelResult distinguishes immediate terminal cancellation from a durable
// request observed later by the lease owner.
type CancelResult struct {
	Changed      bool
	Terminal     bool
	Type         string
	AttemptCount int64
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
	Enqueue(ctx context.Context, executor Executor, request EnqueueRequest) (string, error)
	Claim(ctx context.Context, options ClaimOptions) ([]Record, error)
	Renew(ctx context.Context, id, token string, duration time.Duration) (Renewal, error)
	Succeed(ctx context.Context, id, token, code string) (bool, error)
	Fail(ctx context.Context, id, token, code string) (bool, error)
	RetryAt(ctx context.Context, id, token string, delay time.Duration, code string) (bool, error)
	MarkCanceled(ctx context.Context, id, token, code string) (bool, error)
	Release(ctx context.Context, id, token string) (bool, error)
	Inspect(ctx context.Context, id string) (Record, error)
	ListFailed(ctx context.Context, query FailedQuery) (FailedPage, error)
	Cancel(ctx context.Context, id string) (CancelResult, error)
	Requeue(ctx context.Context, id string) (bool, error)
	RequeueFailed(ctx context.Context, ids []string) (int, error)
	RunRetention(ctx context.Context, policy RetentionPolicy) (int, error)
}

func ValidateEnqueue(request EnqueueRequest) error {
	if request.ID == "" || len(request.ID) > 64 {
		return fmt.Errorf("QUEUE_ENQUEUE_LIMIT: job identity must be within 1..64 bytes")
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
	return ValidateLease(options.LeaseDuration)
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

func ValidateIdentity(id, token string) error {
	if id == "" || len(id) > 64 {
		return fmt.Errorf("QUEUE_IDENTITY: job identity must be within 1..64 bytes")
	}
	if token == "" || len(token) > 64 {
		return fmt.Errorf("QUEUE_IDENTITY: lease token must be within 1..64 bytes")
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
	return nil
}

// ValidateFailedQuery refuses unbounded or unstable operator discovery.
func ValidateFailedQuery(query FailedQuery) error {
	if query.Limit <= 0 || query.Limit > MaximumOperatorBatch {
		return fmt.Errorf("QUEUE_OPERATOR_LIMIT: page size must be within 1..%d", MaximumOperatorBatch)
	}
	if len(query.Types) > MaximumClaimTypes {
		return fmt.Errorf("QUEUE_OPERATOR_LIMIT: query covers more than %d types", MaximumClaimTypes)
	}
	seen := make(map[string]struct{}, len(query.Types))
	for _, name := range query.Types {
		if !canonicalName.MatchString(name) {
			return fmt.Errorf("QUEUE_OPERATOR_LIMIT: job type is not canonical")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("QUEUE_OPERATOR_LIMIT: job type is repeated")
		}
		seen[name] = struct{}{}
	}
	if query.Before != nil && (query.Before.FinishedAt.IsZero() || query.Before.ID == "" || len(query.Before.ID) > 64) {
		return fmt.Errorf("QUEUE_OPERATOR_LIMIT: failed cursor is invalid")
	}
	return nil
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
