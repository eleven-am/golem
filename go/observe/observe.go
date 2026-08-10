// Package observe exposes Golem's immutable, bounded, label-only production
// observation stream. Records never contain SQL, bind values, errors, GraphQL
// documents, variables, rows, principals, DSNs, or event payload bytes.
package observe

import (
	"context"
	"time"

	"github.com/eleven-am/golem/go/golem"
	internalvalue "github.com/eleven-am/golem/go/internal/observation"
)

type Kind string
type Phase string
type Outcome string
type Reason string
type Operation string

const (
	KindRuntime      Kind = "runtime"
	KindRead         Kind = "read"
	KindMutation     Kind = "mutation"
	KindGraphQL      Kind = "graphql"
	KindAnalytics    Kind = "analytics"
	KindScopedRead   Kind = "scoped_read"
	KindTransaction  Kind = "transaction"
	KindHook         Kind = "hook"
	KindRelationLoad Kind = "relation_load"
	KindMigration    Kind = "migration"
	KindEvent        Kind = "event"
	KindSubscription Kind = "subscription"
	KindCDC          Kind = "cdc"
	KindShutdown     Kind = "shutdown"
)

const (
	PhaseStart       Phase = "start"
	PhaseFinish      Phase = "finish"
	PhaseAttempt     Phase = "attempt"
	PhaseRetry       Phase = "retry"
	PhaseCommit      Phase = "commit"
	PhaseRollback    Phase = "rollback"
	PhaseBefore      Phase = "before"
	PhaseAfter       Phase = "after"
	PhaseAfterCommit Phase = "after_commit"
	PhaseClaim       Phase = "claim"
	PhaseAcknowledge Phase = "acknowledge"
	PhaseDeliver     Phase = "deliver"
	PhaseSuppress    Phase = "suppress"
	PhaseOverflow    Phase = "overflow"
	PhaseCancel      Phase = "cancel"
	PhaseOpen        Phase = "open"
	PhaseClose       Phase = "close"
	PhaseVerify      Phase = "verify"
	PhaseApply       Phase = "apply"
)

const (
	OutcomeSuccess    Outcome = "success"
	OutcomeFailure    Outcome = "failure"
	OutcomeRefused    Outcome = "refused"
	OutcomeCancelled  Outcome = "cancelled"
	OutcomeSuppressed Outcome = "suppressed"
	OutcomeRetrying   Outcome = "retrying"
	OutcomeOverflow   Outcome = "overflow"
)

const (
	ReasonNone                   Reason = ""
	ReasonAuthorization          Reason = "authorization"
	ReasonInvalidInput           Reason = "invalid_input"
	ReasonNotFound               Reason = "not_found"
	ReasonConflict               Reason = "conflict"
	ReasonProvider               Reason = "provider"
	ReasonCapability             Reason = "capability"
	ReasonSchemaDrift            Reason = "schema_drift"
	ReasonMigrationHistory       Reason = "migration_history"
	ReasonLimit                  Reason = "limit"
	ReasonTimeout                Reason = "timeout"
	ReasonPanic                  Reason = "panic"
	ReasonTransport              Reason = "transport"
	ReasonQueueFull              Reason = "queue_full"
	ReasonRevalidation           Reason = "revalidation"
	ReasonFiltered               Reason = "filtered"
	ReasonDeletionUnverifiable   Reason = "deletion_unverifiable"
	ReasonEntityAbsent           Reason = "entity_absent"
	ReasonCorrelatedGolemWrite   Reason = "correlated_golem_write"
	ReasonIncompatibleGeneration Reason = "incompatible_generation"
	ReasonClosed                 Reason = "closed"
)

const (
	OperationRuntimeOpen               Operation = "runtime.open"
	OperationReadFindUnique            Operation = "read.find_unique"
	OperationReadFindFirst             Operation = "read.find_first"
	OperationReadFindMany              Operation = "read.find_many"
	OperationReadCount                 Operation = "read.count"
	OperationMutationCreate            Operation = "mutation.create"
	OperationMutationUpdate            Operation = "mutation.update"
	OperationMutationUpsert            Operation = "mutation.upsert"
	OperationMutationDelete            Operation = "mutation.delete"
	OperationMutationUpdateMany        Operation = "mutation.update_many"
	OperationMutationDeleteMany        Operation = "mutation.delete_many"
	OperationMutationConnect           Operation = "mutation.connect"
	OperationMutationDisconnect        Operation = "mutation.disconnect"
	OperationMutationSetRelation       Operation = "mutation.set_relation"
	OperationGraphQLRequest            Operation = "graphql.request"
	OperationGraphQLQuery              Operation = "graphql.query"
	OperationGraphQLMutation           Operation = "graphql.mutation"
	OperationGraphQLSubscription       Operation = "graphql.subscription"
	OperationGraphQLCustomQuery        Operation = "graphql.custom_query"
	OperationGraphQLCustomMutation     Operation = "graphql.custom_mutation"
	OperationGraphQLComputed           Operation = "graphql.computed"
	OperationGraphQLBatchedComputed    Operation = "graphql.batched_computed"
	OperationAnalyticsAggregate        Operation = "analytics.aggregate"
	OperationAnalyticsGroupBy          Operation = "analytics.group_by"
	OperationAnalyticsRelationGroupBy  Operation = "analytics.relation_group_by"
	OperationScopedRead                Operation = "scoped.read"
	OperationCallerTransaction         Operation = "transaction.caller"
	OperationSystemTransaction         Operation = "transaction.system"
	OperationHookFindOne               Operation = "hook.find_one"
	OperationHookFindFirst             Operation = "hook.find_first"
	OperationHookFindMany              Operation = "hook.find_many"
	OperationHookCreate                Operation = "hook.create"
	OperationHookUpdate                Operation = "hook.update"
	OperationHookDelete                Operation = "hook.delete"
	OperationHookUpdateMany            Operation = "hook.update_many"
	OperationHookDeleteMany            Operation = "hook.delete_many"
	OperationRelationLoad              Operation = "relation.load"
	OperationEventPublisherClaim       Operation = "event.publisher_claim"
	OperationEventPublisherAttempt     Operation = "event.publisher_attempt"
	OperationEventPublisherAcknowledge Operation = "event.publisher_acknowledge"
	OperationEventPublisherRetry       Operation = "event.publisher_retry"
	OperationEventPublisherBlock       Operation = "event.publisher_block"
	OperationEventDepthPending         Operation = "event.depth_pending"
	OperationEventDepthBlocked         Operation = "event.depth_blocked"
	OperationEventDepthRetired         Operation = "event.depth_retired"
	OperationEventRetention            Operation = "event.retention"
	OperationEventTransportReceive     Operation = "event.transport_receive"
	OperationEventTransportReconnect   Operation = "event.transport_reconnect"
	OperationSubscriptionMembership    Operation = "subscription.membership"
	OperationSubscriptionEvaluation    Operation = "subscription.evaluation"
	OperationSubscriptionDelivery      Operation = "subscription.delivery"
	OperationSubscriptionSuppression   Operation = "subscription.suppression"
	OperationSubscriptionOverflow      Operation = "subscription.overflow"
	OperationSubscriptionCancellation  Operation = "subscription.cancellation"
	OperationCDCReceive                Operation = "cdc.receive"
	OperationCDCAcknowledge            Operation = "cdc.acknowledge"
	OperationMigrationInspect          Operation = "migration.inspect"
	OperationShutdownHTTP              Operation = "shutdown.http"
	OperationShutdownPublisher         Operation = "shutdown.publisher"
)

// Observation is immutable and can only be produced through Golem's validated
// runtime bridge.
type Observation struct{ value internalvalue.Value }

func (value Observation) Kind() Kind               { return Kind(value.value.KindValue) }
func (value Observation) Phase() Phase             { return Phase(value.value.PhaseValue) }
func (value Observation) Outcome() Outcome         { return Outcome(value.value.OutcomeValue) }
func (value Observation) Reason() Reason           { return Reason(value.value.ReasonValue) }
func (value Observation) Provider() golem.Provider { return value.value.ProviderValue }
func (value Observation) ModelID() golem.ModelID   { return value.value.ModelIDValue }
func (value Observation) Operation() Operation     { return Operation(value.value.OperationValue) }
func (value Observation) Duration() time.Duration  { return value.value.DurationValue }
func (value Observation) StatementCount() int      { return value.value.StatementCountValue }
func (value Observation) Attempt() int             { return value.value.AttemptValue }
func (value Observation) QueueDepth() int          { return value.value.QueueDepthValue }
func (value Observation) QueueLimit() int          { return value.value.QueueLimitValue }
func (value Observation) AggregateCount() int64    { return value.value.AggregateCountValue }

type Observer interface {
	ObserveGolem(context.Context, Observation)
}

const observationDeadline = 100 * time.Millisecond

const (
	maximumObservationDuration = 24 * time.Hour
	maximumObservationCount    = int64(^uint32(0) >> 1)
)

func init() {
	internalvalue.RegisterEmitter(func(candidate any, value internalvalue.Value) {
		observer, ok := candidate.(Observer)
		if !ok || observer == nil || !valid(value) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), observationDeadline)
		defer cancel()
		observation := Observation{value: value}
		func() {
			defer func() { _ = recover() }()
			observer.ObserveGolem(ctx, observation)
		}()
	})
}

func valid(value internalvalue.Value) bool {
	if !validKind(Kind(value.KindValue)) || !validPhase(Phase(value.PhaseValue)) ||
		!validOutcome(Outcome(value.OutcomeValue)) || !validReason(Reason(value.ReasonValue)) ||
		!validOperation(Operation(value.OperationValue)) ||
		(value.ProviderValue != "" && value.ProviderValue != golem.SQLite && value.ProviderValue != golem.PostgreSQL) ||
		value.DurationValue < 0 || value.DurationValue > maximumObservationDuration ||
		value.StatementCountValue < 0 || int64(value.StatementCountValue) > maximumObservationCount ||
		value.AttemptValue < 0 || int64(value.AttemptValue) > maximumObservationCount ||
		value.QueueDepthValue < 0 || int64(value.QueueDepthValue) > maximumObservationCount ||
		value.QueueLimitValue < 0 || int64(value.QueueLimitValue) > maximumObservationCount ||
		value.AggregateCountValue < 0 || value.AggregateCountValue > maximumObservationCount {
		return false
	}
	return value.QueueLimitValue == 0 || value.QueueDepthValue <= value.QueueLimitValue
}

func validKind(value Kind) bool {
	switch value {
	case KindRuntime, KindRead, KindMutation, KindGraphQL, KindAnalytics, KindScopedRead,
		KindTransaction, KindHook, KindRelationLoad, KindMigration, KindEvent,
		KindSubscription, KindCDC, KindShutdown:
		return true
	default:
		return false
	}
}

func validPhase(value Phase) bool {
	switch value {
	case PhaseStart, PhaseFinish, PhaseAttempt, PhaseRetry, PhaseCommit, PhaseRollback,
		PhaseBefore, PhaseAfter, PhaseAfterCommit, PhaseClaim, PhaseAcknowledge,
		PhaseDeliver, PhaseSuppress, PhaseOverflow, PhaseCancel, PhaseOpen, PhaseClose,
		PhaseVerify, PhaseApply:
		return true
	default:
		return false
	}
}

func validOutcome(value Outcome) bool {
	switch value {
	case OutcomeSuccess, OutcomeFailure, OutcomeRefused, OutcomeCancelled,
		OutcomeSuppressed, OutcomeRetrying, OutcomeOverflow:
		return true
	default:
		return false
	}
}

func validReason(value Reason) bool {
	switch value {
	case ReasonNone, ReasonAuthorization, ReasonInvalidInput, ReasonNotFound, ReasonConflict,
		ReasonProvider, ReasonCapability, ReasonSchemaDrift, ReasonMigrationHistory, ReasonLimit,
		ReasonTimeout, ReasonPanic, ReasonTransport, ReasonQueueFull, ReasonRevalidation,
		ReasonFiltered, ReasonDeletionUnverifiable, ReasonEntityAbsent, ReasonCorrelatedGolemWrite,
		ReasonIncompatibleGeneration, ReasonClosed:
		return true
	default:
		return false
	}
}

func validOperation(value Operation) bool {
	switch value {
	case OperationRuntimeOpen, OperationReadFindUnique, OperationReadFindFirst, OperationReadFindMany,
		OperationReadCount, OperationMutationCreate, OperationMutationUpdate, OperationMutationUpsert,
		OperationMutationDelete, OperationMutationUpdateMany, OperationMutationDeleteMany,
		OperationMutationConnect, OperationMutationDisconnect, OperationMutationSetRelation,
		OperationGraphQLRequest, OperationGraphQLQuery, OperationGraphQLMutation, OperationGraphQLSubscription,
		OperationGraphQLCustomQuery, OperationGraphQLCustomMutation, OperationGraphQLComputed,
		OperationGraphQLBatchedComputed, OperationAnalyticsAggregate, OperationAnalyticsGroupBy,
		OperationAnalyticsRelationGroupBy, OperationScopedRead, OperationCallerTransaction,
		OperationSystemTransaction, OperationHookFindOne, OperationHookFindFirst, OperationHookFindMany,
		OperationHookCreate, OperationHookUpdate, OperationHookDelete,
		OperationHookUpdateMany, OperationHookDeleteMany, OperationRelationLoad,
		OperationEventPublisherClaim, OperationEventPublisherAttempt, OperationEventPublisherAcknowledge,
		OperationEventPublisherRetry, OperationEventPublisherBlock, OperationEventRetention,
		OperationEventDepthPending, OperationEventDepthBlocked, OperationEventDepthRetired,
		OperationEventTransportReceive, OperationEventTransportReconnect, OperationSubscriptionMembership,
		OperationSubscriptionEvaluation, OperationSubscriptionDelivery, OperationSubscriptionSuppression,
		OperationSubscriptionOverflow, OperationSubscriptionCancellation, OperationCDCReceive,
		OperationCDCAcknowledge, OperationMigrationInspect, OperationShutdownHTTP,
		OperationShutdownPublisher:
		return true
	default:
		return false
	}
}
