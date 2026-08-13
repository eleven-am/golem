// Package explain owns the closed, provider-neutral presentation model for
// Golem migration plans. It contains no planning, SQL rendering, filesystem,
// database, or migration-publication authority.
package explain

import (
	"errors"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
)

const (
	reportFormatVersion uint16 = 1
	maxProviders               = 2
	maxCollectionItems         = 65_536
	maxStringBytes             = 4_096
	maxEncodedBytes            = 16 << 20
)

type Mode string

const (
	ModeProspective Mode = "prospective"
	ModeReviewed    Mode = "reviewed"

	modeProspective = ModeProspective
	modeReviewed    = ModeReviewed
)

type Status string

const (
	StatusNoChanges      Status = "NO_CHANGES"
	StatusReviewRequired Status = "REVIEW_REQUIRED"
)

type Effect string

const (
	EffectValuePreserving     Effect = "valuePreserving"
	EffectValueRewritten      Effect = "valueRewritten"
	EffectValueDeleted        Effect = "valueDeleted"
	EffectSchemaOnly          Effect = "schemaOnly"
	EffectManualDataTransform Effect = "manualDataTransform"
	EffectUnknown             Effect = "unknown"

	effectValuePreserving     = EffectValuePreserving
	effectValueRewritten      = EffectValueRewritten
	effectValueDeleted        = EffectValueDeleted
	effectSchemaOnly          = EffectSchemaOnly
	effectManualDataTransform = EffectManualDataTransform
)

type Warning string

const (
	WarningApprovalRequired          Warning = "APPROVAL_REQUIRED"
	WarningDataLoss                  Warning = "DATA_LOSS"
	WarningTableOrIndexRewrite       Warning = "TABLE_OR_INDEX_REWRITE"
	WarningStrongLockPossible        Warning = "STRONG_LOCK_POSSIBLE"
	WarningManualReview              Warning = "MANUAL_REVIEW"
	WarningReviewedBackfill          Warning = "REVIEWED_BACKFILL"
	WarningAutocommitBoundary        Warning = "AUTOCOMMIT_BOUNDARY"
	WarningZeroDowntimeNotGuaranteed Warning = "ZERO_DOWNTIME_NOT_GUARANTEED"
)

type Code string

const codeUnavailable Code = "MIGRATION_PLAN_UNAVAILABLE"

type Error struct{ code Code }

func (failure *Error) Error() string { return string(failure.code) }
func CodeOf(err error) (Code, bool) {
	var failure *Error
	if !errors.As(err, &failure) || failure == nil {
		return "", false
	}
	return failure.code, true
}
func isCode(err error, code Code) bool {
	actual, ok := CodeOf(err)
	return ok && actual == code
}
func unavailable() error { return &Error{code: codeUnavailable} }

type Guarantees struct{}

func (Guarantees) AppliesChanges() bool        { return false }
func (Guarantees) UsesReviewedTypedPlan() bool { return true }
func (Guarantees) ZeroDowntime() bool          { return false }
func (Guarantees) DurationEstimated() bool     { return false }

type Report struct {
	formatVersion uint16
	mode          Mode
	status        Status
	providers     []Provider
	warnings      []Warning
}

func (report Report) FormatVersion() uint16  { return report.formatVersion }
func (report Report) Mode() Mode             { return report.mode }
func (report Report) Status() Status         { return report.status }
func (report Report) Providers() []Provider  { return cloneProviders(report.providers) }
func (report Report) Warnings() []Warning    { return append([]Warning(nil), report.warnings...) }
func (report Report) Guarantees() Guarantees { return Guarantees{} }

type Provider struct {
	provider          ir.Provider
	initial           bool
	beforeFingerprint migration.Digest
	afterFingerprint  migration.Digest
	phases            []Phase
	artifacts         []Artifact
	warnings          []Warning
	riskCounts        []RiskCount
}

func (value Provider) Provider() ir.Provider               { return value.provider }
func (value Provider) Initial() bool                       { return value.initial }
func (value Provider) BeforeFingerprint() migration.Digest { return value.beforeFingerprint }
func (value Provider) AfterFingerprint() migration.Digest  { return value.afterFingerprint }
func (value Provider) Phases() []Phase                     { return clonePhases(value.phases) }
func (value Provider) Artifacts() []Artifact               { return append([]Artifact(nil), value.artifacts...) }
func (value Provider) Warnings() []Warning                 { return append([]Warning(nil), value.warnings...) }
func (value Provider) RiskCounts() []RiskCount             { return append([]RiskCount(nil), value.riskCounts...) }

type RiskCount struct {
	risk  migration.Risk
	count uint32
}

func (value RiskCount) Risk() migration.Risk { return value.risk }
func (value RiskCount) Count() uint32        { return value.count }

type Artifact struct {
	path   string
	sha256 migration.Digest
}

func (value Artifact) Path() string             { return value.path }
func (value Artifact) SHA256() migration.Digest { return value.sha256 }

type Phase struct {
	ordinal           uint32
	mode              migration.TransactionMode
	beforeFingerprint migration.Digest
	afterFingerprint  migration.Digest
	operations        []Operation
	warnings          []Warning
}

func (value Phase) Ordinal() uint32                            { return value.ordinal }
func (value Phase) TransactionMode() migration.TransactionMode { return value.mode }
func (value Phase) BeforeFingerprint() migration.Digest        { return value.beforeFingerprint }
func (value Phase) AfterFingerprint() migration.Digest         { return value.afterFingerprint }
func (value Phase) Operations() []Operation                    { return cloneOperations(value.operations) }
func (value Phase) Warnings() []Warning                        { return append([]Warning(nil), value.warnings...) }

type Operation struct {
	id               migration.OperationID
	kind             migration.OperationKind
	stage            uint16
	identity         Identity
	display          string
	risk             migration.Risk
	effect           Effect
	mode             migration.TransactionMode
	before           migration.Digest
	after            migration.Digest
	dependencies     []migration.OperationID
	capabilities     []ir.CapabilityID
	approvalRequired bool
	approvalPresent  bool
	manual           *ManualCompanion
	warnings         []Warning
}

func (value Operation) ID() migration.OperationID                  { return value.id }
func (value Operation) Kind() migration.OperationKind              { return value.kind }
func (value Operation) Stage() uint16                              { return value.stage }
func (value Operation) Identity() Identity                         { return value.identity }
func (value Operation) Display() string                            { return value.display }
func (value Operation) Risk() migration.Risk                       { return value.risk }
func (value Operation) Effect() Effect                             { return value.effect }
func (value Operation) TransactionMode() migration.TransactionMode { return value.mode }
func (value Operation) BeforeDigest() migration.Digest             { return value.before }
func (value Operation) AfterDigest() migration.Digest              { return value.after }
func (value Operation) Dependencies() []migration.OperationID {
	return append([]migration.OperationID(nil), value.dependencies...)
}
func (value Operation) Capabilities() []ir.CapabilityID {
	return append([]ir.CapabilityID(nil), value.capabilities...)
}
func (value Operation) ApprovalRequired() bool { return value.approvalRequired }
func (value Operation) ApprovalPresent() bool  { return value.approvalPresent }
func (value Operation) ManualCompanion() (ManualCompanion, bool) {
	if value.manual == nil {
		return ManualCompanion{}, false
	}
	return *value.manual, true
}
func (value Operation) Warnings() []Warning { return append([]Warning(nil), value.warnings...) }

// Identity contains only stable compiler identities. Physical schema, table,
// column, index, constraint, and namespace names have no representation in the
// report model.
type Identity struct {
	modelID     ir.ModelID
	fieldID     ir.FieldID
	indexID     ir.IndexID
	relationID  ir.RelationID
	keyID       ir.KeyID
	checkID     ir.CheckID
	extensionID ir.ExtensionID
}

func (value Identity) ModelID() ir.ModelID         { return value.modelID }
func (value Identity) FieldID() ir.FieldID         { return value.fieldID }
func (value Identity) IndexID() ir.IndexID         { return value.indexID }
func (value Identity) RelationID() ir.RelationID   { return value.relationID }
func (value Identity) KeyID() ir.KeyID             { return value.keyID }
func (value Identity) CheckID() ir.CheckID         { return value.checkID }
func (value Identity) ExtensionID() ir.ExtensionID { return value.extensionID }

type ManualCompanion struct {
	path          string
	sha256        migration.Digest
	postcondition migration.Digest
}

func (value ManualCompanion) Path() string                    { return value.path }
func (value ManualCompanion) SHA256() migration.Digest        { return value.sha256 }
func (value ManualCompanion) Postcondition() migration.Digest { return value.postcondition }

func cloneProviders(values []Provider) []Provider {
	result := make([]Provider, len(values))
	for index, value := range values {
		result[index] = value
		result[index].phases = clonePhases(value.phases)
		result[index].artifacts = append([]Artifact(nil), value.artifacts...)
		result[index].warnings = append([]Warning(nil), value.warnings...)
		result[index].riskCounts = append([]RiskCount(nil), value.riskCounts...)
	}
	return result
}

func clonePhases(values []Phase) []Phase {
	result := make([]Phase, len(values))
	for index, value := range values {
		result[index] = value
		result[index].operations = cloneOperations(value.operations)
		result[index].warnings = append([]Warning(nil), value.warnings...)
	}
	return result
}

func cloneOperations(values []Operation) []Operation {
	result := make([]Operation, len(values))
	for index, value := range values {
		result[index] = value
		result[index].dependencies = append([]migration.OperationID(nil), value.dependencies...)
		result[index].capabilities = append([]ir.CapabilityID(nil), value.capabilities...)
		result[index].warnings = append([]Warning(nil), value.warnings...)
		if value.manual != nil {
			manual := *value.manual
			result[index].manual = &manual
		}
	}
	return result
}
