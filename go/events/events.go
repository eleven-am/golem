// Package events contains Golem's narrow event transport and operations ABI.
package events

import (
	"context"
	"regexp"
	"time"

	"github.com/eleven-am/golem/go/golem"
	internalvalue "github.com/eleven-am/golem/go/internal/event/value"
)

// Notice, EventBatch, and Subscription deliberately alias internal sealed
// representations. Callers can inspect them but cannot construct valid values.
type Notice = internalvalue.Notice
type EventBatch = internalvalue.EventBatch
type Subscription = internalvalue.Subscription

type Stream interface {
	Recv(context.Context) (Notice, error)
	Close() error
}

type EventTransport interface {
	Publish(context.Context, EventBatch) error
	Subscribe(context.Context, Subscription) (Stream, error)
}

type TransportScope string

const (
	TransportScopeProcessLocal TransportScope = "process-local"
	TransportScopeCrossProcess TransportScope = "cross-process"
)

const MaximumTransportIdentityBytes = 128

const canonicalTransportIdentityPattern = `^[A-Za-z0-9][A-Za-z0-9._+/\-]*$`

var canonicalTransportIdentity = regexp.MustCompile(canonicalTransportIdentityPattern)

type TransportCapabilities struct {
	identity string
	scope    TransportScope
	durable  bool
}

// NewTransportCapabilities validates the stable diagnostic identity reported
// by an EventTransport. Capabilities describe operational behavior only; they
// do not grant access to a transport, decoder, database, or event payload.
func NewTransportCapabilities(identity string, scope TransportScope, durable bool) (TransportCapabilities, error) {
	if len(identity) == 0 {
		return TransportCapabilities{}, Failf(CodeEventConfig, "identity must not be empty")
	}
	if len(identity) > MaximumTransportIdentityBytes {
		return TransportCapabilities{}, Failf(CodeEventConfig, "identity must not exceed %d bytes, got %d", MaximumTransportIdentityBytes, len(identity))
	}
	if !canonicalTransportIdentity.MatchString(identity) {
		return TransportCapabilities{}, Failf(CodeEventConfig, "identity must match %s", canonicalTransportIdentityPattern)
	}
	if scope != TransportScopeProcessLocal && scope != TransportScopeCrossProcess {
		return TransportCapabilities{}, Failf(CodeEventConfig, "scope must be \"process-local\" or \"cross-process\"")
	}
	return TransportCapabilities{identity: identity, scope: scope, durable: durable}, nil
}

func (capabilities TransportCapabilities) Identity() string      { return capabilities.identity }
func (capabilities TransportCapabilities) Scope() TransportScope { return capabilities.scope }
func (capabilities TransportCapabilities) Durable() bool         { return capabilities.durable }

type CapabilityReporter interface {
	TransportCapabilities() TransportCapabilities
}

// AvailabilityReporter is an optional operational SPI for transports whose
// external dependency can become unavailable. It reports one closed readiness
// bit only: implementations must not expose broker addresses, credentials,
// errors, backlog, or connection state through this boundary.
type AvailabilityReporter interface {
	TransportAvailable() bool
}

// PayloadLimitReporter is an optional closed capability SPI for transports
// backed by an external protocol. The value is the largest encoded event, in
// bytes, that the transport has verified it can accept. It must not be derived
// from payload contents or expose broker configuration through this boundary.
type PayloadLimitReporter interface {
	MaxEncodedEventBytes() int
}

// RuntimeBinding is the schema-bound decoding capability supplied by App.Open
// to an installed transport that implements RuntimeBindableTransport. It lets
// a broker adapter turn owned golem.event.v1 bytes into a sealed Notice without
// exposing a public raw Notice constructor. The binding owns validation,
// historical-schema resolution, size limits, and byte cloning.
//
// A transport must treat this as an infrastructure capability: retain no
// application callback other than this interface, never invoke it with a nil
// context, and return its sanitized error unchanged.
type RuntimeBinding interface {
	DecodeNotice(context.Context, []byte) (Notice, error)
}

// RuntimeBindableTransport is an optional SPI for transports that receive raw
// encoded events from an external broker. App.Open calls BindEventRuntime at
// most once before the App is published. Implementations must reject a second
// or conflicting binding and must not begin background work from this method.
// Process-local transports whose streams already carry sealed Notice values do
// not need to implement this interface.
type RuntimeBindableTransport interface {
	BindEventRuntime(RuntimeBinding) error
}

func CapabilitiesOf(transport EventTransport) TransportCapabilities {
	if reporter, ok := transport.(CapabilityReporter); ok {
		return reporter.TransportCapabilities()
	}
	return TransportCapabilities{}
}

// AvailabilityOf returns the closed readiness state of a configured transport.
// Transports without an external availability dependency remain available by
// default; a nil transport is never available.
func AvailabilityOf(transport EventTransport) bool {
	if transport == nil {
		return false
	}
	if reporter, ok := transport.(AvailabilityReporter); ok {
		return reporter.TransportAvailable()
	}
	return true
}

// PayloadLimitOf returns a transport's closed positive encoded-payload limit.
// The boolean is false when the transport does not report a limit or reports an
// invalid non-positive value.
func PayloadLimitOf(transport EventTransport) (int, bool) {
	reporter, ok := transport.(PayloadLimitReporter)
	if !ok {
		return 0, false
	}
	limit := reporter.MaxEncodedEventBytes()
	if limit <= 0 {
		return 0, false
	}
	return limit, true
}

// Capabilities is an immutable operational snapshot. Slice accessors return
// owned copies and the value contains no connection, payload, or principal
// data.
type Capabilities struct {
	provider               golem.Provider
	factCodecs             []string
	eventCodecs            []string
	transport              TransportCapabilities
	transportAvailable     bool
	publisherEnabled       bool
	publisherRunning       bool
	subscriptionModels     []golem.ModelID
	cdcAdapters            []string
	externalWritesObserved bool
}

// RuntimeCapabilities is the trusted application integration constructor.
// Values supplied here are diagnostic identities only; they confer no runtime
// capability.
func RuntimeCapabilities(provider golem.Provider, factCodecs, eventCodecs []string, transport TransportCapabilities, publisherEnabled, publisherRunning bool, subscriptionModels []golem.ModelID, cdcAdapters []string, externalWritesObserved bool) Capabilities {
	return RuntimeCapabilitiesWithTransportAvailability(provider, factCodecs, eventCodecs, transport, transport.Identity() != "", publisherEnabled, publisherRunning, subscriptionModels, cdcAdapters, externalWritesObserved)
}

// RuntimeCapabilitiesWithTransportAvailability is the trusted runtime
// constructor for a dynamic transport-readiness snapshot. The availability bit
// is diagnostic only and grants no transport or decoding capability.
func RuntimeCapabilitiesWithTransportAvailability(provider golem.Provider, factCodecs, eventCodecs []string, transport TransportCapabilities, transportAvailable, publisherEnabled, publisherRunning bool, subscriptionModels []golem.ModelID, cdcAdapters []string, externalWritesObserved bool) Capabilities {
	return Capabilities{
		provider: provider, factCodecs: append([]string(nil), factCodecs...),
		eventCodecs: append([]string(nil), eventCodecs...), transport: transport, transportAvailable: transportAvailable,
		publisherEnabled: publisherEnabled, publisherRunning: publisherRunning,
		subscriptionModels:     append([]golem.ModelID(nil), subscriptionModels...),
		cdcAdapters:            append([]string(nil), cdcAdapters...),
		externalWritesObserved: externalWritesObserved,
	}
}

func (capabilities Capabilities) Provider() golem.Provider { return capabilities.provider }
func (capabilities Capabilities) FactCodecVersions() []string {
	return append([]string(nil), capabilities.factCodecs...)
}
func (capabilities Capabilities) EventCodecVersions() []string {
	return append([]string(nil), capabilities.eventCodecs...)
}
func (capabilities Capabilities) Transport() TransportCapabilities { return capabilities.transport }
func (capabilities Capabilities) TransportAvailable() bool         { return capabilities.transportAvailable }
func (capabilities Capabilities) PublisherEnabled() bool           { return capabilities.publisherEnabled }
func (capabilities Capabilities) PublisherRunning() bool           { return capabilities.publisherRunning }
func (capabilities Capabilities) SubscriptionModelIDs() []golem.ModelID {
	return append([]golem.ModelID(nil), capabilities.subscriptionModels...)
}
func (capabilities Capabilities) CDCAdapterIdentities() []string {
	return append([]string(nil), capabilities.cdcAdapters...)
}
func (capabilities Capabilities) ExternalWritesObserved() bool {
	return capabilities.externalWritesObserved
}

type DeliveryStatus string

const (
	DeliveryPending   DeliveryStatus = "pending"
	DeliveryLeased    DeliveryStatus = "leased"
	DeliveryDelivered DeliveryStatus = "delivered"
	DeliveryBlocked   DeliveryStatus = "blocked"
	DeliveryRetired   DeliveryStatus = "retired"
)

type DeliveryFailureCode string

type Delivery interface {
	CausationID() golem.CausationID
	Status() DeliveryStatus
	AttemptCount() int64
	FailureCode() DeliveryFailureCode
	FactRows() int
	UpdatedAt() time.Time
}

type RetentionPolicy struct {
	olderThan time.Time
	maxRows   int
}

type RetentionResult struct {
	causations int
	facts      int
}

func NewRetentionPolicy(olderThan time.Time, maxRows int) (RetentionPolicy, error) {
	if olderThan.IsZero() {
		return RetentionPolicy{}, Failf(CodeEventConfig, "OlderThan must not be the zero time")
	}
	if maxRows <= 0 {
		return RetentionPolicy{}, Failf(CodeEventConfig, "MaxRows must be positive, got %d", maxRows)
	}
	if maxRows > maximumLimits.RetentionDeleteRows {
		return RetentionPolicy{}, Failf(CodeEventConfig, "MaxRows must not exceed %d, got %d", maximumLimits.RetentionDeleteRows, maxRows)
	}
	return RetentionPolicy{olderThan: olderThan, maxRows: maxRows}, nil
}

func (policy RetentionPolicy) OlderThan() time.Time { return policy.olderThan }
func (policy RetentionPolicy) MaxRows() int         { return policy.maxRows }

// NewRetentionResult is for trusted operator implementations. It contains
// aggregate counts only and cannot expose facts or identities.
func NewRetentionResult(causations, facts int) RetentionResult {
	return RetentionResult{causations: causations, facts: facts}
}
func (result RetentionResult) Causations() int { return result.causations }
func (result RetentionResult) Facts() int      { return result.facts }

type Operator interface {
	Inspect(context.Context, golem.CausationID) (Delivery, error)
	Resume(context.Context, golem.CausationID) error
	Retire(context.Context, golem.CausationID) error
	RunRetention(context.Context, RetentionPolicy) (RetentionResult, error)
}
