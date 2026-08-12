// Package migration defines provider-neutral migration planning, immutable
// history, and execution contracts. It contains no rendered SQL.
package migration

import (
	"context"
	"time"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

type Digest string
type MigrationID string
type OperationID string

type OperationKind string

const (
	BootstrapSystemSchema       OperationKind = "bootstrapSystemSchema"
	AddSystemObject             OperationKind = "addSystemObject"
	CreateNamespace             OperationKind = "createNamespace"
	CreateTable                 OperationKind = "createTable"
	RenameTable                 OperationKind = "renameTable"
	DropTable                   OperationKind = "dropTable"
	AddColumn                   OperationKind = "addColumn"
	RenameColumn                OperationKind = "renameColumn"
	AlterColumnType             OperationKind = "alterColumnType"
	AlterColumnNullability      OperationKind = "alterColumnNullability"
	SetColumnDefault            OperationKind = "setColumnDefault"
	DropColumnDefault           OperationKind = "dropColumnDefault"
	DropColumn                  OperationKind = "dropColumn"
	AddPrimaryKey               OperationKind = "addPrimaryKey"
	DropPrimaryKey              OperationKind = "dropPrimaryKey"
	AddUnique                   OperationKind = "addUnique"
	DropUnique                  OperationKind = "dropUnique"
	AddForeignKey               OperationKind = "addForeignKey"
	DropForeignKey              OperationKind = "dropForeignKey"
	AddCheck                    OperationKind = "addCheck"
	DropCheck                   OperationKind = "dropCheck"
	CreateIndex                 OperationKind = "createIndex"
	DropIndex                   OperationKind = "dropIndex"
	RenameIndex                 OperationKind = "renameIndex"
	CreateProviderExtension     OperationKind = "createProviderExtension"
	DropProviderExtension       OperationKind = "dropProviderExtension"
	BackfillColumn              OperationKind = "backfillColumn"
	InitializeConcurrencyColumn OperationKind = "initializeConcurrencyColumn"
	RebuildTable                OperationKind = "rebuildTable"
	ValidateConstraint          OperationKind = "validateConstraint"
	ManualStep                  OperationKind = "manualStep"
	RecordSchemaVersion         OperationKind = "recordSchemaVersion"
)

type Risk string

const (
	RiskSafe     Risk = "safe"
	RiskLocking  Risk = "locking"
	RiskRewrite  Risk = "rewrite"
	RiskDataLoss Risk = "dataLoss"
	RiskManual   Risk = "manual"
)

type TransactionMode string

const (
	Transactional  TransactionMode = "transactional"
	AutocommitOnly TransactionMode = "autocommitOnly"
)

type DataTransform struct {
	Kind  string `json:"kind"`
	Input Digest `json:"input"`
}

type ResumeMetadata struct {
	DetectionKind       string `json:"detectionKind"`
	ExpectedFingerprint Digest `json:"expectedFingerprint"`
}

type Operation struct {
	ID           OperationID       `json:"id"`
	Kind         OperationKind     `json:"kind"`
	Stage        uint16            `json:"stage"`
	ObjectID     string            `json:"objectId"`
	Before       Digest            `json:"before,omitempty"`
	After        Digest            `json:"after,omitempty"`
	Dependencies []OperationID     `json:"dependencies"`
	Capabilities []ir.CapabilityID `json:"capabilities"`
	Mode         TransactionMode   `json:"mode"`
	Risk         Risk              `json:"risk"`
	Transform    *DataTransform    `json:"transform,omitempty"`
	Resume       *ResumeMetadata   `json:"resume,omitempty"`
	LogicalPath  string            `json:"logicalPath"`
}

type Phase struct {
	Ordinal           uint32          `json:"ordinal"`
	Mode              TransactionMode `json:"mode"`
	Operations        []OperationID   `json:"operations"`
	BeforeFingerprint Digest          `json:"beforeFingerprint"`
	AfterFingerprint  Digest          `json:"afterFingerprint"`
}

type Plan struct {
	Provider          ir.Provider `json:"provider"`
	Initial           bool        `json:"initial"`
	BeforeFingerprint Digest      `json:"beforeFingerprint"`
	AfterFingerprint  Digest      `json:"afterFingerprint"`
	Operations        []Operation `json:"operations"`
	Phases            []Phase     `json:"phases"`
	snapshotFacts     *PlanSnapshotFacts
}

// PlanSnapshotFacts is the detached, non-persisted typed evidence owned by a
// plan created by Diff or DiffReviewed. It is deliberately opaque to callers:
// accessors return fresh normalized copies so report adapters cannot mutate the
// facts that bind an operation graph to its before/after schemas.
type PlanSnapshotFacts struct {
	before physical.PhysicalSchema
	after  physical.PhysicalSchema
}

// SnapshotFacts returns the typed evidence attached by the sole migration diff
// owner. Hand-authored Plan values have no such evidence and cannot be
// explained as prospective plans.
func (plan Plan) SnapshotFacts() (PlanSnapshotFacts, bool) {
	if plan.snapshotFacts == nil {
		return PlanSnapshotFacts{}, false
	}
	return PlanSnapshotFacts{before: clonePlanSnapshot(plan.snapshotFacts.before), after: clonePlanSnapshot(plan.snapshotFacts.after)}, true
}

func (facts PlanSnapshotFacts) Before() physical.PhysicalSchema {
	return clonePlanSnapshot(facts.before)
}

func (facts PlanSnapshotFacts) After() physical.PhysicalSchema {
	return clonePlanSnapshot(facts.after)
}

type Approval struct {
	OperationID OperationID `json:"operationId"`
	Risk        Risk        `json:"risk"`
	Before      Digest      `json:"before"`
	After       Digest      `json:"after"`
}

type OperationRisk struct {
	OperationID OperationID `json:"operationId"`
	Risk        Risk        `json:"risk"`
}

type FileChecksum struct {
	Path   string `json:"path"`
	SHA256 Digest `json:"sha256"`
}

// ManualCompanion records reviewed provider SQL metadata, never its contents.
type ManualCompanion struct {
	OperationID   OperationID  `json:"operationId"`
	File          FileChecksum `json:"file"`
	Postcondition Digest       `json:"postcondition"`
}

type Manifest struct {
	FormatVersion    uint16                    `json:"formatVersion"`
	CanonicalVersion uint16                    `json:"canonicalVersion"`
	HashAlgorithm    string                    `json:"hashAlgorithm"`
	GeneratorVersion string                    `json:"generatorVersion"`
	Provider         physical.ProviderManifest `json:"provider"`
	Entries          []ManifestEntry           `json:"entries"`
}

type ManifestEntry struct {
	ID                       MigrationID             `json:"id"`
	ParentID                 MigrationID             `json:"parentId,omitempty"`
	ParentChainHash          Digest                  `json:"parentChainHash"`
	ChainHash                Digest                  `json:"chainHash"`
	Files                    []FileChecksum          `json:"files"`
	Manual                   []ManualCompanion       `json:"manual"`
	Operations               []Operation             `json:"operations"`
	Phases                   []Phase                 `json:"phases"`
	Risks                    []OperationRisk         `json:"risks"`
	Approvals                []Approval              `json:"approvals"`
	BeforeModel              Digest                  `json:"beforeModel"`
	AfterModel               Digest                  `json:"afterModel"`
	BeforePhysical           Digest                  `json:"beforePhysical"`
	AfterPhysical            Digest                  `json:"afterPhysical"`
	BeforeSnapshot           physical.PhysicalSchema `json:"beforeSnapshot"`
	AfterSnapshot            physical.PhysicalSchema `json:"afterSnapshot"`
	UnmanagedAllowlistDigest Digest                  `json:"unmanagedAllowlistDigest"`
}

type PhaseStatus string

const (
	PhasePending PhaseStatus = "pending"
	PhaseRunning PhaseStatus = "running"
	PhaseApplied PhaseStatus = "applied"
	PhaseFailed  PhaseStatus = "failed"
)

type LedgerEntry struct {
	MigrationID     MigrationID
	ParentChainHash Digest
	ChainHash       Digest
	Files           []FileChecksum
	BeforePhysical  Digest
	AfterPhysical   Digest
	Phases          []LedgerPhase
	AppliedAt       time.Time
}

type LedgerPhase struct {
	Ordinal          uint32
	Status           PhaseStatus
	AfterFingerprint Digest
}

type Introspector[S any] interface {
	Introspect(context.Context) (S, error)
	Fingerprint(S) (Digest, error)
}
type Executor[S any] interface {
	AcquireLock(context.Context) error
	ReleaseLock(context.Context) error
	ApplyTransactional(context.Context, Phase, []Operation) error
	ApplyAutocommit(context.Context, Operation) error
	ReadLedger(context.Context) ([]LedgerEntry, error)
	WriteLedger(context.Context, LedgerEntry) error
	Introspector() Introspector[S]
}
