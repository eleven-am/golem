// Package queue declares durable background jobs that run against the
// application's own database.
//
// A job type is declared once with Register, which binds a payload type to a
// handler and returns the only value able to construct work for that type. The
// package is a contract: it holds no database handle, starts no goroutine, and
// imports nothing from the rest of the module. The worker and the enqueue
// entry points live in the runtime.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// JobID is the durable identity of one enqueued job.
type JobID string

// MaximumPayloadBytes is the absolute ceiling on one marshalled payload. It
// bounds both Type.New and Limits.MaxPayloadBytes.
const MaximumPayloadBytes = 1 << 20

// MaximumKeyBytes is the ceiling on a dedupe or exclusivity key.
const MaximumKeyBytes = 256

// RetentionDisabled turns automatic retention off. Assigned to
// Limits.RetentionEvery it stops a worker deleting terminal job rows at all,
// so job history survives until an operator calls RunRetention. It is the only
// negative duration Limits accepts.
const RetentionDisabled = time.Duration(-1)

const (
	defaultMaxAttempts   = 5
	defaultTimeout       = 10 * time.Minute
	defaultBackoffBase   = 5 * time.Second
	defaultBackoffCap    = 5 * time.Minute
	defaultConcurrency   = 4
	defaultClaimBatch    = 16
	defaultLeaseDuration = 30 * time.Second
	defaultPollInterval  = 250 * time.Millisecond
	defaultShutdownGrace = 15 * time.Second
	defaultAbandonGrace  = 5 * time.Second
	maximumDelay         = 24 * time.Hour
)

var canonicalName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// ErrorCode is the closed set of queue failure classifications.
type ErrorCode string

const (
	// CodeConfigInvalid reports a registration or limits value refused before
	// any background work begins.
	CodeConfigInvalid ErrorCode = "QUEUE_CONFIG_INVALID"
	// CodePayloadInvalid reports a payload that cannot be marshalled or that
	// exceeds the payload ceiling.
	CodePayloadInvalid ErrorCode = "QUEUE_PAYLOAD_INVALID"
	// CodeJobNotFound reports an operator lookup for an absent job.
	CodeJobNotFound ErrorCode = "QUEUE_JOB_NOT_FOUND"
	// CodeWorkerRunning reports a second concurrent worker on one application.
	CodeWorkerRunning ErrorCode = "QUEUE_WORKER_RUNNING"
	// CodeStoreFailure reports a durable store failure.
	CodeStoreFailure ErrorCode = "QUEUE_STORE_FAILURE"
)

type queueError struct {
	code   ErrorCode
	detail string
}

func (failure queueError) Error() string { return string(failure.code) + ": " + failure.detail }

// Fail builds a classified queue error. It exists so the worker and runtime
// report the same closed vocabulary this package defines.
func Fail(code ErrorCode, format string, arguments ...any) error {
	return queueError{code: code, detail: fmt.Sprintf(format, arguments...)}
}

// CodeOf reports the classification of a queue failure. It recognises wrapped
// errors and never classifies from message text.
func CodeOf(err error) (ErrorCode, bool) {
	var failure queueError
	if errors.As(err, &failure) {
		return failure.code, true
	}
	return "", false
}

// Backoff schedules retries as exponential full jitter under a ceiling.
type Backoff struct {
	Base time.Duration
	Cap  time.Duration
}

// Job is one claimed unit of work handed to a handler.
type Job[T any] struct {
	ID          JobID
	Payload     T
	Attempt     int
	MaxAttempts int
	EnqueuedAt  time.Time
}

// Definition declares one job type. Zero-valued MaxAttempts, Timeout and
// Backoff fields take the package defaults.
type Definition[T any] struct {
	Type          string
	Handle        func(context.Context, Job[T]) error
	MaxAttempts   int
	Timeout       time.Duration
	MaxConcurrent int
	Backoff       Backoff
	ExclusiveBy   func(T) string
}

// Resource is one fleet-wide weighted concurrency budget. Costs maps locally
// registered job types to the capacity consumed by one active lease. Workers
// with disjoint registries may share a name, but every active worker must agree
// on its meaning. Drain workers before changing capacity or costs.
type Resource struct {
	Name        string
	Concurrency int
	Costs       map[string]int
}

// Meta is the durable job state a type-erased handler receives alongside the
// undecoded payload.
type Meta struct {
	ID          JobID
	Attempt     int
	MaxAttempts int
	EnqueuedAt  time.Time
}

// Handler is the type-erased handler the worker invokes. Register builds one
// per definition; applications never write one.
type Handler func(ctx context.Context, payload []byte, meta Meta) error

// Registration is one resolved definition with its defaults applied. It is the
// worker's view of a registered type.
type Registration struct {
	Type          string
	MaxAttempts   int
	Timeout       time.Duration
	MaxConcurrent int
	Backoff       Backoff
	Handle        Handler
}

// Registry holds every registered job type for one application.
type Registry struct {
	mutex sync.RWMutex
	types map[string]Registration
	order []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{types: make(map[string]Registration)}
}

// Clone returns an independent registry containing the same registrations.
func (registry *Registry) Clone() *Registry {
	result := NewRegistry()
	if registry == nil {
		return result
	}
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	result.order = append(result.order, registry.order...)
	for name, registration := range registry.types {
		result.types[name] = registration
	}
	return result
}

// Lookup returns the registration for a type name.
func (registry *Registry) Lookup(name string) (Registration, bool) {
	if registry == nil {
		return Registration{}, false
	}
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	registration, found := registry.types[name]
	return registration, found
}

// Registrations returns every registration in declaration order.
func (registry *Registry) Registrations() []Registration {
	if registry == nil {
		return nil
	}
	registry.mutex.RLock()
	defer registry.mutex.RUnlock()
	result := make([]Registration, 0, len(registry.order))
	for _, name := range registry.order {
		result = append(result, registry.types[name])
	}
	return result
}

// Type is the sole constructor of work for one registered job type.
type Type[T any] struct {
	name        string
	maxAttempts int
	exclusiveBy func(T) string
}

// Register binds a payload type to a handler and returns the constructor for
// that job type. Every refusal is CodeConfigInvalid and happens before any
// background work exists.
func Register[T any](registry *Registry, definition Definition[T]) (Type[T], error) {
	if registry == nil {
		return Type[T]{}, Fail(CodeConfigInvalid, "registry is nil")
	}
	if !canonicalName.MatchString(definition.Type) {
		return Type[T]{}, Fail(CodeConfigInvalid, "job type %q is not canonical", definition.Type)
	}
	if definition.Handle == nil {
		return Type[T]{}, Fail(CodeConfigInvalid, "job type %q has no handler", definition.Type)
	}
	if definition.MaxAttempts < 0 || definition.Timeout < 0 || definition.MaxConcurrent < 0 {
		return Type[T]{}, Fail(CodeConfigInvalid, "job type %q has a negative bound", definition.Type)
	}
	if definition.Backoff.Base < 0 || definition.Backoff.Cap < 0 {
		return Type[T]{}, Fail(CodeConfigInvalid, "job type %q has a negative backoff", definition.Type)
	}
	resolved := Registration{
		Type:          definition.Type,
		MaxAttempts:   orDefaultInt(definition.MaxAttempts, defaultMaxAttempts),
		Timeout:       orDefaultDuration(definition.Timeout, defaultTimeout),
		MaxConcurrent: definition.MaxConcurrent,
		Backoff: Backoff{
			Base: orDefaultDuration(definition.Backoff.Base, defaultBackoffBase),
			Cap:  orDefaultDuration(definition.Backoff.Cap, defaultBackoffCap),
		},
	}
	if resolved.Backoff.Cap < resolved.Backoff.Base {
		return Type[T]{}, Fail(CodeConfigInvalid, "job type %q has a backoff cap below its base", definition.Type)
	}
	handle := definition.Handle
	resolved.Handle = func(ctx context.Context, payload []byte, meta Meta) error {
		var value T
		if err := json.Unmarshal(payload, &value); err != nil {
			return Terminal(Fail(CodePayloadInvalid, "job %s payload does not decode", meta.ID))
		}
		return handle(ctx, Job[T]{ID: meta.ID, Payload: value, Attempt: meta.Attempt, MaxAttempts: meta.MaxAttempts, EnqueuedAt: meta.EnqueuedAt})
	}
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	if _, exists := registry.types[definition.Type]; exists {
		return Type[T]{}, Fail(CodeConfigInvalid, "job type %q is registered twice", definition.Type)
	}
	registry.types[definition.Type] = resolved
	registry.order = append(registry.order, definition.Type)
	return Type[T]{name: definition.Type, maxAttempts: resolved.MaxAttempts, exclusiveBy: definition.ExclusiveBy}, nil
}

type enqueueOptions struct {
	delay  time.Duration
	dedupe string
	err    error
}

// Option adjusts one enqueue.
type Option func(*enqueueOptions)

// After schedules the job to become claimable only once the delay has passed.
func After(delay time.Duration) Option {
	return func(options *enqueueOptions) {
		if delay < 0 || delay > maximumDelay {
			options.err = Fail(CodeConfigInvalid, "enqueue delay must be within 0..24h")
			return
		}
		options.delay = delay
	}
}

// Dedupe coalesces this enqueue with any active job already carrying the key.
func Dedupe(key string) Option {
	return func(options *enqueueOptions) {
		if key == "" || len(key) > MaximumKeyBytes {
			options.err = Fail(CodeConfigInvalid, "dedupe key must be within 1..%d bytes", MaximumKeyBytes)
			return
		}
		options.dedupe = key
	}
}

// Pending is one validated job awaiting a durable enqueue. It can only be
// produced by the Type its payload belongs to.
type Pending struct {
	typeName     string
	payload      []byte
	delay        time.Duration
	dedupeKey    string
	exclusiveKey string
	maxAttempts  int
}

// New marshals the payload, resolves the exclusivity key, and returns work
// ready to enqueue. An unmarshalable or oversized payload is refused here.
func (jobType Type[T]) New(payload T, options ...Option) (Pending, error) {
	if jobType.name == "" {
		return Pending{}, Fail(CodeConfigInvalid, "job type is unregistered")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Pending{}, Fail(CodePayloadInvalid, "job type %q payload does not encode", jobType.name)
	}
	if len(encoded) > MaximumPayloadBytes {
		return Pending{}, Fail(CodePayloadInvalid, "job type %q payload is %d bytes, above %d", jobType.name, len(encoded), MaximumPayloadBytes)
	}
	var decoded T
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return Pending{}, Fail(CodePayloadInvalid, "job type %q payload does not round-trip", jobType.name)
	}
	resolved := enqueueOptions{}
	for _, option := range options {
		if option == nil {
			return Pending{}, Fail(CodeConfigInvalid, "job type %q received a nil option", jobType.name)
		}
		option(&resolved)
	}
	if resolved.err != nil {
		return Pending{}, resolved.err
	}
	pending := Pending{typeName: jobType.name, payload: encoded, delay: resolved.delay, dedupeKey: resolved.dedupe, maxAttempts: jobType.maxAttempts}
	if jobType.exclusiveBy != nil {
		key := jobType.exclusiveBy(payload)
		if len(key) > MaximumKeyBytes {
			return Pending{}, Fail(CodeConfigInvalid, "job type %q exclusivity key is above %d bytes", jobType.name, MaximumKeyBytes)
		}
		pending.exclusiveKey = key
	}
	return pending, nil
}

// TypeName reports the registered job type this work belongs to.
func (pending Pending) TypeName() string { return pending.typeName }

// Payload returns a private copy of the marshalled payload.
func (pending Pending) Payload() []byte { return append([]byte(nil), pending.payload...) }

// Delay reports how long after enqueue the job becomes claimable.
func (pending Pending) Delay() time.Duration { return pending.delay }

// DedupeKey reports the coalescing key, empty when unset.
func (pending Pending) DedupeKey() string { return pending.dedupeKey }

// ExclusiveKey reports the exclusivity key, empty when the type is not
// exclusive.
func (pending Pending) ExclusiveKey() string { return pending.exclusiveKey }

// MaxAttempts reports the attempt ceiling recorded with the durable row.
func (pending Pending) MaxAttempts() int { return pending.maxAttempts }

// Limits bound one worker. Zero-valued fields take the package defaults.
type Limits struct {
	Concurrency   int
	ClaimBatch    int
	LeaseDuration time.Duration
	PollInterval  time.Duration
	ShutdownGrace time.Duration
	// AbandonGrace is how long the worker waits for a handler to return after
	// cancelling it, on shutdown and on a per-type Timeout. Go cannot kill a
	// goroutine: once the grace elapses the worker stops waiting and records
	// the durable outcome while the handler may still be running, so an
	// abandoned timed-out job can execute concurrently with its own retry.
	// Handlers that must not run twice have to honour their context.
	AbandonGrace    time.Duration
	MaxPayloadBytes int
	// RetentionAge is how long a terminal job row survives automatic
	// retention. It bounds only succeeded, failed and canceled rows; live work
	// is never deleted.
	RetentionAge time.Duration
	// RetentionEvery is how often a worker runs automatic retention.
	// RetentionDisabled turns it off entirely and keeps every terminal row
	// until an operator calls RunRetention.
	RetentionEvery time.Duration
	RetentionRows  int
}

// DefaultLimits returns the documented worker defaults. Automatic retention is
// on: a worker deletes terminal job rows older than thirty days every minute.
// Applications that keep job history for longer must set RetentionAge, or
// RetentionEvery to RetentionDisabled.
func DefaultLimits() Limits {
	return Limits{
		Concurrency:     defaultConcurrency,
		ClaimBatch:      defaultClaimBatch,
		LeaseDuration:   defaultLeaseDuration,
		PollInterval:    defaultPollInterval,
		ShutdownGrace:   defaultShutdownGrace,
		AbandonGrace:    defaultAbandonGrace,
		MaxPayloadBytes: MaximumPayloadBytes,
		RetentionAge:    30 * 24 * time.Hour,
		RetentionEvery:  time.Minute,
		RetentionRows:   MaximumOperatorBatch,
	}
}

// Resolved returns the limits with every zero-valued field replaced by its
// default.
func (limits Limits) Resolved() Limits {
	defaults := DefaultLimits()
	return Limits{
		Concurrency:     orDefaultInt(limits.Concurrency, defaults.Concurrency),
		ClaimBatch:      orDefaultInt(limits.ClaimBatch, defaults.ClaimBatch),
		LeaseDuration:   orDefaultDuration(limits.LeaseDuration, defaults.LeaseDuration),
		PollInterval:    orDefaultDuration(limits.PollInterval, defaults.PollInterval),
		ShutdownGrace:   orDefaultDuration(limits.ShutdownGrace, defaults.ShutdownGrace),
		AbandonGrace:    orDefaultDuration(limits.AbandonGrace, defaults.AbandonGrace),
		MaxPayloadBytes: orDefaultInt(limits.MaxPayloadBytes, defaults.MaxPayloadBytes),
		RetentionAge:    orDefaultDuration(limits.RetentionAge, defaults.RetentionAge),
		RetentionEvery:  orDefaultDuration(limits.RetentionEvery, defaults.RetentionEvery),
		RetentionRows:   orDefaultInt(limits.RetentionRows, defaults.RetentionRows),
	}
}

// RetentionEnabled reports whether a worker deletes terminal job rows on its
// own. It is false only when RetentionEvery is RetentionDisabled.
func (limits Limits) RetentionEnabled() bool {
	return limits.Resolved().RetentionEvery != RetentionDisabled
}

// Validate refuses limits a worker cannot honour. Zero-valued fields are the
// defaults and always pass.
func (limits Limits) Validate() error {
	resolved := limits.Resolved()
	switch {
	case limits.Concurrency < 0 || limits.ClaimBatch < 0 || limits.MaxPayloadBytes < 0 || limits.RetentionRows < 0:
		return Fail(CodeConfigInvalid, "limits carry a negative bound")
	case limits.LeaseDuration < 0 || limits.PollInterval < 0 || limits.ShutdownGrace < 0 || limits.AbandonGrace < 0 || limits.RetentionAge < 0:
		return Fail(CodeConfigInvalid, "limits carry a negative duration")
	case limits.RetentionEvery < 0 && limits.RetentionEvery != RetentionDisabled:
		return Fail(CodeConfigInvalid, "RetentionEvery must be positive or RetentionDisabled")
	case resolved.MaxPayloadBytes > MaximumPayloadBytes:
		return Fail(CodeConfigInvalid, "MaxPayloadBytes is above %d", MaximumPayloadBytes)
	case resolved.LeaseDuration < time.Second || resolved.LeaseDuration > 10*time.Minute:
		return Fail(CodeConfigInvalid, "LeaseDuration must be within 1s..10m")
	case resolved.PollInterval > time.Hour*24:
		return Fail(CodeConfigInvalid, "PollInterval must be at most 24h")
	case resolved.AbandonGrace > 2*time.Minute:
		return Fail(CodeConfigInvalid, "AbandonGrace must be at most 2m")
	case resolved.RetentionAge < time.Hour || resolved.RetentionAge > 10*365*24*time.Hour:
		return Fail(CodeConfigInvalid, "RetentionAge must be within 1h..10y")
	case resolved.RetentionEvery != RetentionDisabled && (resolved.RetentionEvery < time.Minute || resolved.RetentionEvery > 24*time.Hour):
		return Fail(CodeConfigInvalid, "RetentionEvery must be within 1m..24h or RetentionDisabled")
	case resolved.RetentionRows > MaximumRetentionRows:
		return Fail(CodeConfigInvalid, "RetentionRows is above %d", MaximumRetentionRows)
	}
	return nil
}

// State is the durable job state.
type State string

const (
	// StatePending is enqueued work waiting for its availability time.
	StatePending State = "pending"
	// StateLeased is work owned by one worker until its lease expires.
	StateLeased State = "leased"
	// StateSucceeded is terminal successful completion.
	StateSucceeded State = "succeeded"
	// StateFailed is terminal refusal or attempt exhaustion.
	StateFailed State = "failed"
	// StateCanceled is terminal external cancellation.
	StateCanceled State = "canceled"
)

// Status is the sanitized operator view of one job. It carries no payload.
type Status struct {
	ID              JobID
	Type            string
	State           State
	Attempt         int
	MaxAttempts     int
	AvailableAt     time.Time
	LastCode        string
	CancelRequested bool
	EnqueuedAt      time.Time
	FinishedAt      *time.Time
}

// MaximumOperatorBatch bounds one operator page or bulk control action.
const MaximumOperatorBatch = 256

// MaximumRetentionRows bounds one automatic or operator retention batch.
const MaximumRetentionRows = 4096

// FailedCursor is the stable position immediately after one failed job.
type FailedCursor struct {
	FinishedAt time.Time
	ID         JobID
}

// FailedQuery selects one bounded page of failed jobs. An empty Types list
// includes every stored type.
type FailedQuery struct {
	Types  []string
	Limit  int
	Before *FailedCursor
}

// FailedPage is one payload-redacted page of failed jobs.
type FailedPage struct {
	Jobs []Status
	Next *FailedCursor
}

// JobCursor is the stable position immediately after one listed job.
type JobCursor struct {
	EnqueuedAt time.Time
	ID         JobID
}

// JobQuery selects one bounded payload-free page of jobs. Empty Types and
// States lists include every stored type and state.
type JobQuery struct {
	Types  []string
	States []State
	Limit  int
	Before *JobCursor
}

// JobPage is one payload-redacted page of jobs.
type JobPage struct {
	Jobs []Status
	Next *JobCursor
}

// CountQuery selects the job types included in state counts. An empty Types
// list includes every stored type.
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

// RetentionPolicy bounds one retention run over terminal rows.
type RetentionPolicy struct {
	OlderThan time.Time
	MaxRows   int
	States    []State
}

// Operator is the durable job control surface. Every method acts on state the
// database owns; none of them reaches into a running handler. Cancellation is
// immediate for pending jobs and cooperative for leased jobs, so completion
// may win before a worker observes the request. RequeueFailed is retry-safe and
// may return a nonzero changed count together with an error. CancelMany has the
// same partial-progress contract.
type Operator interface {
	Inspect(ctx context.Context, id JobID) (Status, error)
	List(ctx context.Context, query JobQuery) (JobPage, error)
	ListFailed(ctx context.Context, query FailedQuery) (FailedPage, error)
	CountByState(ctx context.Context, query CountQuery) (StateCounts, error)
	Cancel(ctx context.Context, id JobID) (bool, error)
	CancelMany(ctx context.Context, ids []JobID) (int, error)
	Requeue(ctx context.Context, id JobID) (bool, error)
	RequeueFailed(ctx context.Context, ids []JobID) (int, error)
	RunRetention(ctx context.Context, policy RetentionPolicy) (int, error)
}

func orDefaultInt(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func orDefaultDuration(value, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return value
}
