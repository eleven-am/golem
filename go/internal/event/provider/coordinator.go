// Package provider defines the provider-neutral durable delivery coordination
// contract. Implementations own SQL and database-time semantics; callers never
// receive executable SQL or mutable outbox access.
package provider

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusLeased    Status = "leased"
	StatusDelivered Status = "delivered"
	StatusBlocked   Status = "blocked"
	StatusRetired   Status = "retired"
)

const (
	MaximumClaimGroups    = 1024
	DefaultClaimBytes     = 16 << 20
	MaximumClaimBytes     = 64 << 20
	MaximumCausationFacts = 1000
	MaximumRetentionRows  = 4096
	MaximumFailureCode    = 128
)

var failureCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// Delivery is sanitized operational state. It deliberately contains no fact
// bytes, identities, snapshots, SQL, or driver error text.
type Delivery struct {
	CausationID       string
	Status            Status
	FirstRecordedAt   time.Time
	AttemptCount      int64
	AvailableAt       time.Time
	LeaseToken        string
	LeaseUntil        *time.Time
	DeliveredAt       *time.Time
	LastFailureCode   string
	BlockedAt         *time.Time
	RetiredAt         *time.Time
	UpdatedAt         time.Time
	ImmutableFactRows int
}

// FactRow is the provider-level immutable outbox representation. It remains
// below mutation/fact so SQL providers do not import the mutation decoder
// graph. The publisher converts it into the codec-owned row and validates
// every duplicated column before any transport call.
type FactRow struct {
	EventID               string
	FactVersion           int64
	CodecIdentity         string
	GenerationFingerprint string
	ModelID               string
	Action                string
	BeforeIdentity        []byte
	AfterIdentity         []byte
	CausationID           string
	TransactionOrdinal    int64
	Metadata              []byte
	DeleteSnapshot        []byte
	RecordedAt            time.Time
}

// Lease is one complete causation. Facts are ordered by transaction ordinal
// and privately owned by the caller.
type Lease struct {
	Delivery Delivery
	Facts    []FactRow
}

type ClaimOptions struct {
	Groups        int
	LeaseDuration time.Duration
	MaxBytes      int
}

type RetentionPolicy struct {
	OlderThan time.Time
	MaxRows   int
}

type RetentionResult struct {
	Causations int
	Facts      int
}

// DepthSnapshot is an exact, transaction-time count of durable delivery rows
// in the three operator-actionable backlog states.
type DepthSnapshot struct {
	Pending int64
	Blocked int64
	Retired int64
}

type ClaimSnapshot struct {
	Leases []Lease
	Depth  DepthSnapshot
}

// ClaimDepthCoordinator is implemented by released SQL providers. The
// publisher samples its transaction-coupled snapshot instead of counting the
// complete delivery table on every poll.
type ClaimDepthCoordinator interface {
	ClaimWithDepth(context.Context, ClaimOptions) (ClaimSnapshot, error)
}

// Coordinator is the sole mutable seam for _golem_outbox_delivery. Every
// token-fenced transition returns changed=false for a stale lease rather than
// allowing it to mutate a newly owned row.
type Coordinator interface {
	Claim(context.Context, ClaimOptions) ([]Lease, error)
	Renew(context.Context, string, string, time.Duration) (bool, error)
	Acknowledge(context.Context, string, string) (bool, error)
	Retry(context.Context, string, string, time.Duration, string) (bool, error)
	Block(context.Context, string, string, string) (bool, error)
	Release(context.Context, string, string) (bool, error)
	Inspect(context.Context, string) (Delivery, error)
	Resume(context.Context, string) (bool, error)
	Retire(context.Context, string) (bool, error)
	RunRetention(context.Context, RetentionPolicy) (RetentionResult, error)
}

func ValidateClaim(options ClaimOptions) error {
	if options.Groups <= 0 || options.Groups > MaximumClaimGroups {
		return fmt.Errorf("P7_DELIVERY_LIMIT: claim groups must be within 1..%d", MaximumClaimGroups)
	}
	if options.LeaseDuration <= 0 || options.LeaseDuration > 10*time.Minute || options.LeaseDuration%time.Microsecond != 0 {
		return fmt.Errorf("P7_DELIVERY_LIMIT: lease duration must be positive, microsecond-exact, and at most 10m")
	}
	if options.MaxBytes < 0 || options.MaxBytes > MaximumClaimBytes {
		return fmt.Errorf("P7_DELIVERY_LIMIT: claim bytes must not exceed %d", MaximumClaimBytes)
	}
	return nil
}

func ClaimByteLimit(options ClaimOptions) int {
	if options.MaxBytes == 0 {
		return DefaultClaimBytes
	}
	return options.MaxBytes
}

func ValidateRetention(policy RetentionPolicy) error {
	if policy.OlderThan.IsZero() {
		return fmt.Errorf("P7_RETENTION_LIMIT: retention floor is zero")
	}
	if policy.MaxRows <= 0 || policy.MaxRows > MaximumRetentionRows {
		return fmt.Errorf("P7_RETENTION_LIMIT: retention rows must be within 1..%d", MaximumRetentionRows)
	}
	return nil
}

func ValidateDelay(delay time.Duration) error {
	if delay < 0 || delay > 24*time.Hour || delay%time.Microsecond != 0 {
		return fmt.Errorf("P7_DELIVERY_LIMIT: delay must be non-negative, microsecond-exact, and at most 24h")
	}
	return nil
}

func ValidateFailureCode(code string, required bool) error {
	if code == "" && !required {
		return nil
	}
	if !failureCodePattern.MatchString(code) {
		return fmt.Errorf("P7_DELIVERY_FAILURE_CODE: code is not canonical")
	}
	return nil
}

// NewLeaseToken returns a canonical UUIDv4 without importing a general UUID
// package into provider implementations.
func NewLeaseToken() (string, error) {
	var value [16]byte
	if _, err := cryptorand.Read(value[:]); err != nil {
		return "", fmt.Errorf("P7_DELIVERY_TOKEN: random source: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func CloneFacts(rows []FactRow) []FactRow {
	result := make([]FactRow, len(rows))
	for index, row := range rows {
		result[index] = row
		result[index].BeforeIdentity = append([]byte(nil), row.BeforeIdentity...)
		result[index].AfterIdentity = append([]byte(nil), row.AfterIdentity...)
		result[index].Metadata = append([]byte(nil), row.Metadata...)
		result[index].DeleteSnapshot = append([]byte(nil), row.DeleteSnapshot...)
	}
	return result
}
