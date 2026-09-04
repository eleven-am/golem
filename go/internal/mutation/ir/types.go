// Package ir owns the closed, immutable, provider-neutral representation of a
// bound mutation. It contains no SQL and deliberately consumes the stable P1
// identities and exact P2 logical values rather than defining another schema
// or value registry.
package ir

import (
	"fmt"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// P7 adds an explicit delete-snapshot verification state to fact semantics.
// Empty captured inventory and an unverifiable delete must fingerprint
// differently, so this is a canonical mutation-plan format change. Version 5
// adds the hook-authored bit to a scalar operation for the same reason.
const CanonicalFormatVersion uint16 = 5

type Operation uint8

const (
	Create Operation = iota + 1
	CreateMany
	Connect
	ConnectOrCreate
	Disconnect
	SetRelation
	Update
	UpdateMany
	Upsert
	Delete
	DeleteMany
	// BranchProbe is an internal, non-writing branch node used when a truthful
	// connect-or-create probe and its conditional membership-owner effect belong
	// to different models. Public mutation action ordinals remain unchanged.
	BranchProbe
)

func (operation Operation) valid() bool { return operation >= Create && operation <= BranchProbe }

func (operation Operation) existingRows() bool {
	switch operation {
	case Connect, ConnectOrCreate, Disconnect, SetRelation, Update, UpdateMany, Upsert, Delete, DeleteMany, BranchProbe:
		return true
	default:
		return false
	}
}

func (operation Operation) rootAllowed() bool {
	switch operation {
	case Create, Update, UpdateMany, Upsert, Delete, DeleteMany:
		return true
	default:
		return false
	}
}

type Stance uint8

const (
	Caller Stance = iota + 1
	System
)

type Branch uint8

const (
	MainBranch Branch = iota + 1
	UpsertCreateBranch
	UpsertUpdateBranch
	ConnectOrCreateCreateBranch
	ConnectOrCreateConnectBranch
	BatchItemBranch
)

type HookPhase uint8

const (
	BeforeHook HookPhase = iota + 1
	TransactionAfterHook
	AfterCommitHook
)

type HookOperation uint8

const (
	HookCreate HookOperation = iota + 1
	HookUpdate
	HookDelete
	HookUpdateMany
	HookDeleteMany
)

type RetryClass uint8

const (
	NoRetry RetryClass = iota + 1
	EngineOwnedUpsertRetry
	CallerTransactionNoReplay
)

type ProviderCapability uint16

const (
	CapabilityTransaction ProviderCapability = iota + 1
	CapabilitySavepoint
	CapabilityTargetLock
	CapabilityExactAffectedIdentities
	CapabilitySelectorGuard
	CapabilityPersistedResult
	CapabilityAtomicNumericUpdate
)

type FactAction uint8

const (
	FactCreated FactAction = iota + 1
	FactUpdated
	FactDeleted
)

func validateModel(model policyir.ModelID, path string) error {
	if model == (policyir.ModelID{}) {
		return fmt.Errorf("P4_MUTATION_IR_%s: model identity is zero", path)
	}
	return nil
}
