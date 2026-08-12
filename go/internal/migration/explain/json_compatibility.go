package explain

import (
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
)

// JSONCompatibilityDocument is the closed version-1 wire projection emitted
// by MarshalJSON. Report deliberately has no JSON tags or exported storage, so
// compatibility inventory must derive from this wire DTO rather than from the
// mutable in-memory presentation model.
type JSONCompatibilityDocument struct {
	FormatVersion uint16                      `json:"formatVersion"`
	Mode          Mode                        `json:"mode"`
	Status        Status                      `json:"status"`
	Providers     []JSONCompatibilityProvider `json:"providers"`
	Warnings      []Warning                   `json:"warnings"`
	Guarantees    JSONCompatibilityGuarantees `json:"guarantees"`
}

type JSONCompatibilityProvider struct {
	Provider                  compilerir.Provider              `json:"provider"`
	Initial                   bool                             `json:"initial"`
	BeforePhysicalFingerprint migration.Digest                 `json:"beforePhysicalFingerprint"`
	AfterPhysicalFingerprint  migration.Digest                 `json:"afterPhysicalFingerprint"`
	Phases                    []JSONCompatibilityPhase         `json:"phases"`
	Artifacts                 []JSONCompatibilityArtifact      `json:"artifacts"`
	Warnings                  []Warning                        `json:"warnings"`
	OperationCountsByRisk     []JSONCompatibilityOperationRisk `json:"operationCountsByRisk"`
}

type JSONCompatibilityArtifact struct {
	Path   string           `json:"path"`
	SHA256 migration.Digest `json:"sha256"`
}

type JSONCompatibilityOperationRisk struct {
	Risk  migration.Risk `json:"risk"`
	Count uint32         `json:"count"`
}

type JSONCompatibilityPhase struct {
	Ordinal           uint32                       `json:"ordinal"`
	TransactionMode   migration.TransactionMode    `json:"transactionMode"`
	BeforeFingerprint migration.Digest             `json:"beforeFingerprint"`
	AfterFingerprint  migration.Digest             `json:"afterFingerprint"`
	Operations        []JSONCompatibilityOperation `json:"operations"`
	Warnings          []Warning                    `json:"warnings"`
}

type JSONCompatibilityOperation struct {
	ID                migration.OperationID               `json:"id"`
	Kind              migration.OperationKind             `json:"kind"`
	Stage             uint16                              `json:"stage"`
	Identity          JSONCompatibilityIdentity           `json:"identity"`
	Display           string                              `json:"display,omitempty"`
	Risk              migration.Risk                      `json:"risk"`
	Effect            Effect                              `json:"effect"`
	TransactionMode   migration.TransactionMode           `json:"transactionMode"`
	BeforeDigest      migration.Digest                    `json:"beforeDigest"`
	AfterDigest       migration.Digest                    `json:"afterDigest"`
	Dependencies      []migration.OperationID             `json:"dependencies"`
	Capabilities      []compilerir.CapabilityID           `json:"capabilities"`
	Approval          JSONCompatibilityApproval           `json:"approval"`
	ReviewedCompanion *JSONCompatibilityReviewedCompanion `json:"reviewedCompanion,omitempty"`
	Warnings          []Warning                           `json:"warnings"`
}

type JSONCompatibilityIdentity struct {
	ModelID     compilerir.ModelID     `json:"modelId"`
	FieldID     compilerir.FieldID     `json:"fieldId"`
	IndexID     compilerir.IndexID     `json:"indexId"`
	RelationID  compilerir.RelationID  `json:"relationId"`
	KeyID       compilerir.KeyID       `json:"keyId"`
	CheckID     compilerir.CheckID     `json:"checkId"`
	ExtensionID compilerir.ExtensionID `json:"extensionId"`
}

type JSONCompatibilityApproval struct {
	Required bool `json:"required"`
	Present  bool `json:"present"`
}

type JSONCompatibilityReviewedCompanion struct {
	Path                string           `json:"path"`
	SHA256              migration.Digest `json:"sha256"`
	PostconditionDigest migration.Digest `json:"postconditionDigest"`
}

type JSONCompatibilityGuarantees struct {
	AppliesChanges        bool `json:"appliesChanges"`
	UsesReviewedTypedPlan bool `json:"usesReviewedTypedPlan"`
	ZeroDowntime          bool `json:"zeroDowntime"`
	DurationEstimated     bool `json:"durationEstimated"`
}

// JSONCompatibilitySource returns a fresh zero-value document carrying the
// exact format discriminator. It is the sole source consumed by the CLI
// compatibility inventory.
func JSONCompatibilitySource() JSONCompatibilityDocument {
	return JSONCompatibilityDocument{FormatVersion: reportFormatVersion}
}

func compatibilityJSONProjection(report Report) JSONCompatibilityDocument {
	value := JSONCompatibilityDocument{
		FormatVersion: report.formatVersion,
		Mode:          report.mode,
		Status:        report.status,
		Providers:     make([]JSONCompatibilityProvider, len(report.providers)),
		Warnings:      append([]Warning(nil), report.warnings...),
		Guarantees:    JSONCompatibilityGuarantees{UsesReviewedTypedPlan: true},
	}
	for providerIndex, provider := range report.providers {
		projected := JSONCompatibilityProvider{
			Provider:                  provider.provider,
			Initial:                   provider.initial,
			BeforePhysicalFingerprint: provider.beforeFingerprint,
			AfterPhysicalFingerprint:  provider.afterFingerprint,
			Phases:                    make([]JSONCompatibilityPhase, len(provider.phases)),
			Artifacts:                 make([]JSONCompatibilityArtifact, len(provider.artifacts)),
			Warnings:                  append([]Warning(nil), provider.warnings...),
			OperationCountsByRisk:     make([]JSONCompatibilityOperationRisk, len(provider.riskCounts)),
		}
		for index, artifact := range provider.artifacts {
			projected.Artifacts[index] = JSONCompatibilityArtifact{Path: artifact.path, SHA256: artifact.sha256}
		}
		for index, count := range provider.riskCounts {
			projected.OperationCountsByRisk[index] = JSONCompatibilityOperationRisk{Risk: count.risk, Count: count.count}
		}
		for phaseIndex, phase := range provider.phases {
			projectedPhase := JSONCompatibilityPhase{
				Ordinal: phase.ordinal, TransactionMode: phase.mode,
				BeforeFingerprint: phase.beforeFingerprint, AfterFingerprint: phase.afterFingerprint,
				Operations: make([]JSONCompatibilityOperation, len(phase.operations)), Warnings: append([]Warning(nil), phase.warnings...),
			}
			for operationIndex, operation := range phase.operations {
				projectedOperation := JSONCompatibilityOperation{
					ID: operation.id, Kind: operation.kind, Stage: operation.stage,
					Identity: JSONCompatibilityIdentity{
						ModelID: operation.identity.modelID, FieldID: operation.identity.fieldID, IndexID: operation.identity.indexID,
						RelationID: operation.identity.relationID, KeyID: operation.identity.keyID, CheckID: operation.identity.checkID, ExtensionID: operation.identity.extensionID,
					},
					Display: operation.display, Risk: operation.risk, Effect: operation.effect, TransactionMode: operation.mode,
					BeforeDigest: operation.before, AfterDigest: operation.after,
					Dependencies: append([]migration.OperationID(nil), operation.dependencies...), Capabilities: append([]compilerir.CapabilityID(nil), operation.capabilities...),
					Approval: JSONCompatibilityApproval{Required: operation.approvalRequired, Present: operation.approvalPresent},
					Warnings: append([]Warning(nil), operation.warnings...),
				}
				if operation.manual != nil {
					projectedOperation.ReviewedCompanion = &JSONCompatibilityReviewedCompanion{Path: operation.manual.path, SHA256: operation.manual.sha256, PostconditionDigest: operation.manual.postcondition}
				}
				projectedPhase.Operations[operationIndex] = projectedOperation
			}
			projected.Phases[phaseIndex] = projectedPhase
		}
		value.Providers[providerIndex] = projected
	}
	return value
}
