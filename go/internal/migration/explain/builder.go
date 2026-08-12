package explain

import (
	"encoding/hex"
	"go/token"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
)

type reportInput struct {
	formatVersion uint16
	mode          Mode
	providers     []providerInput
}

type providerInput struct {
	provider          ir.Provider
	initial           bool
	beforeFingerprint migration.Digest
	afterFingerprint  migration.Digest
	phases            []phaseInput
	artifacts         []artifactInput
}

type artifactInput struct {
	path   string
	sha256 migration.Digest
}

type phaseInput struct {
	ordinal           uint32
	mode              migration.TransactionMode
	beforeFingerprint migration.Digest
	afterFingerprint  migration.Digest
	operations        []operationInput
}

type operationInput struct {
	id               migration.OperationID
	kind             migration.OperationKind
	stage            uint16
	identity         identityInput
	display          displayInput
	risk             migration.Risk
	mode             migration.TransactionMode
	before           migration.Digest
	after            migration.Digest
	dependencies     []migration.OperationID
	capabilities     []ir.CapabilityID
	approvalRequired bool
	approvalPresent  bool
	effect           effectInput
	manual           *manualInput
}

// identityInput has no generic string escape hatch. A future Plan adapter may
// populate only identities obtained from typed before/after snapshots.
type identityInput struct {
	modelID     ir.ModelID
	fieldID     ir.FieldID
	indexID     ir.IndexID
	relationID  ir.RelationID
	keyID       ir.KeyID
	checkID     ir.CheckID
	extensionID ir.ExtensionID
}

// displayInput is assembled from Go-facing typed snapshot labels. It cannot
// carry a physical qualified name or an arbitrary preformatted path.
type displayInput struct {
	model  string
	member string
}

type preservationProof uint8

const (
	preservationUnspecified preservationProof = iota
	preservationSafeWidening
	preservationRewrite
)

type effectInput struct {
	beforePresent       bool
	afterPresent        bool
	preservation        preservationProof
	extensionRecreation bool
}

type manualInput struct {
	path          string
	sha256        migration.Digest
	postcondition migration.Digest
}

func buildReport(input reportInput) (Report, error) {
	if input.formatVersion != reportFormatVersion || input.mode != ModeProspective && input.mode != ModeReviewed || len(input.providers) == 0 || len(input.providers) > maxProviders {
		return Report{}, unavailable()
	}
	providers := append([]providerInput(nil), input.providers...)
	sort.SliceStable(providers, func(i, j int) bool {
		return providerOrder(providers[i].provider) < providerOrder(providers[j].provider)
	})
	seenProviders := make(map[ir.Provider]bool, len(providers))
	report := Report{formatVersion: reportFormatVersion, mode: input.mode}
	allWarnings := warningSet{}
	allWarnings.add(WarningZeroDowntimeNotGuaranteed)
	operationCount := 0
	for _, candidate := range providers {
		if providerOrder(candidate.provider) == 0 || seenProviders[candidate.provider] {
			return Report{}, unavailable()
		}
		seenProviders[candidate.provider] = true
		provider, count, err := buildProvider(candidate)
		if err != nil {
			return Report{}, err
		}
		operationCount += count
		report.providers = append(report.providers, provider)
		allWarnings.addAll(provider.warnings)
	}
	if operationCount == 0 {
		report.status = StatusNoChanges
	} else {
		report.status = StatusReviewRequired
	}
	report.warnings = allWarnings.values()
	if err := validateReportStrings(report); err != nil {
		return Report{}, err
	}
	if _, err := MarshalJSON(report); err != nil {
		return Report{}, err
	}
	if _, err := MarshalText(report); err != nil {
		return Report{}, err
	}
	return report, nil
}

func buildProvider(input providerInput) (Provider, int, error) {
	if !validDigest(input.beforeFingerprint, false) || !validDigest(input.afterFingerprint, false) || len(input.phases) > maxCollectionItems || len(input.artifacts) > maxCollectionItems {
		return Provider{}, 0, unavailable()
	}
	result := Provider{provider: input.provider, initial: input.initial, beforeFingerprint: input.beforeFingerprint, afterFingerprint: input.afterFingerprint}
	warnings := warningSet{}
	warnings.add(WarningZeroDowntimeNotGuaranteed)
	riskCounts := map[migration.Risk]uint32{}
	operationCount := 0
	expectedFingerprint := input.beforeFingerprint
	for index, phase := range input.phases {
		if phase.ordinal != uint32(index) || phase.beforeFingerprint != expectedFingerprint || len(phase.operations) == 0 || len(phase.operations) > maxCollectionItems || operationCount > maxCollectionItems-len(phase.operations) || !validTransactionMode(phase.mode) || !validDigest(phase.beforeFingerprint, false) || !validDigest(phase.afterFingerprint, false) {
			return Provider{}, 0, unavailable()
		}
		built := Phase{ordinal: phase.ordinal, mode: phase.mode, beforeFingerprint: phase.beforeFingerprint, afterFingerprint: phase.afterFingerprint}
		phaseWarnings := warningSet{}
		for _, operation := range phase.operations {
			value, err := buildOperation(operation)
			if err != nil || value.mode != phase.mode {
				return Provider{}, 0, unavailable()
			}
			built.operations = append(built.operations, value)
			phaseWarnings.addAll(value.warnings)
			riskCounts[value.risk]++
			operationCount++
		}
		built.warnings = phaseWarnings.values()
		warnings.addAll(built.warnings)
		result.phases = append(result.phases, built)
		expectedFingerprint = phase.afterFingerprint
	}
	if expectedFingerprint != input.afterFingerprint {
		return Provider{}, 0, unavailable()
	}
	for _, artifact := range input.artifacts {
		if !validRelativePath(artifact.path) || !validDigest(artifact.sha256, false) {
			return Provider{}, 0, unavailable()
		}
		result.artifacts = append(result.artifacts, Artifact{path: artifact.path, sha256: artifact.sha256})
	}
	for _, risk := range []migration.Risk{migration.RiskSafe, migration.RiskLocking, migration.RiskRewrite, migration.RiskDataLoss, migration.RiskManual} {
		result.riskCounts = append(result.riskCounts, RiskCount{risk: risk, count: riskCounts[risk]})
	}
	result.warnings = warnings.values()
	return result, operationCount, nil
}

func buildOperation(input operationInput) (Operation, error) {
	if !validStableID(string(input.id), false) || !validString(string(input.kind), false, false) || !validDigest(input.before, true) || !validDigest(input.after, true) || len(input.dependencies) > maxCollectionItems || len(input.capabilities) > maxCollectionItems || !validRisk(input.risk) || !validTransactionMode(input.mode) || input.approvalPresent && !input.approvalRequired {
		return Operation{}, unavailable()
	}
	identity, display, err := buildIdentity(input.kind, input.identity, input.display)
	if err != nil {
		return Operation{}, err
	}
	effect, effectWarnings, err := classifyEffect(input.kind, input.effect)
	if err != nil {
		return Operation{}, err
	}
	warnings := warningSet{}
	warnings.addAll(effectWarnings)
	switch input.risk {
	case migration.RiskLocking:
		warnings.add(WarningStrongLockPossible)
	case migration.RiskRewrite:
		warnings.add(WarningTableOrIndexRewrite)
		warnings.add(WarningStrongLockPossible)
	case migration.RiskDataLoss:
		warnings.add(WarningDataLoss)
	case migration.RiskManual:
		warnings.add(WarningManualReview)
	}
	if input.approvalRequired {
		warnings.add(WarningApprovalRequired)
	}
	if input.mode == migration.AutocommitOnly {
		warnings.add(WarningAutocommitBoundary)
	}
	result := Operation{
		id: input.id, kind: input.kind, stage: input.stage, identity: identity, display: display,
		risk: input.risk, effect: effect, mode: input.mode,
		before: input.before, after: input.after, approvalRequired: input.approvalRequired,
		approvalPresent: input.approvalPresent,
	}
	for _, dependency := range input.dependencies {
		if !validStableID(string(dependency), false) {
			return Operation{}, unavailable()
		}
		result.dependencies = append(result.dependencies, dependency)
	}
	for _, capability := range input.capabilities {
		if !validString(string(capability), false, false) {
			return Operation{}, unavailable()
		}
		result.capabilities = append(result.capabilities, capability)
	}
	if input.manual != nil {
		if input.kind != migration.BackfillColumn || input.risk != migration.RiskManual || !input.approvalRequired || !validRelativePath(input.manual.path) || !validDigest(input.manual.sha256, false) || !validDigest(input.manual.postcondition, false) {
			return Operation{}, unavailable()
		}
		result.manual = &ManualCompanion{path: input.manual.path, sha256: input.manual.sha256, postcondition: input.manual.postcondition}
		warnings.add(WarningReviewedBackfill)
	}
	result.warnings = warnings.values()
	return result, nil
}

func knownOperationKinds() []migration.OperationKind {
	return []migration.OperationKind{
		migration.BootstrapSystemSchema, migration.AddSystemObject, migration.CreateNamespace,
		migration.CreateTable, migration.RenameTable, migration.DropTable, migration.AddColumn,
		migration.RenameColumn, migration.AlterColumnType, migration.AlterColumnNullability,
		migration.SetColumnDefault, migration.DropColumnDefault, migration.DropColumn,
		migration.AddPrimaryKey, migration.DropPrimaryKey, migration.AddUnique, migration.DropUnique,
		migration.AddForeignKey, migration.DropForeignKey, migration.AddCheck, migration.DropCheck,
		migration.CreateIndex, migration.DropIndex, migration.RenameIndex,
		migration.CreateProviderExtension, migration.DropProviderExtension, migration.BackfillColumn,
		migration.InitializeConcurrencyColumn,
		migration.RebuildTable, migration.ValidateConstraint, migration.ManualStep,
		migration.RecordSchemaVersion,
	}
}

func classifyEffect(kind migration.OperationKind, facts effectInput) (Effect, []Warning, error) {
	switch kind {
	case migration.DropTable, migration.DropColumn:
		if facts.beforePresent && !facts.afterPresent && facts.preservation == preservationRewrite && !facts.extensionRecreation {
			return EffectValueRewritten, nil, nil
		}
		if facts.beforePresent && !facts.afterPresent && facts.preservation == preservationUnspecified && !facts.extensionRecreation {
			return EffectValueDeleted, nil, nil
		}
	case migration.AlterColumnType:
		if !facts.beforePresent || !facts.afterPresent || facts.extensionRecreation {
			break
		}
		switch facts.preservation {
		case preservationSafeWidening:
			return EffectValuePreserving, nil, nil
		case preservationRewrite:
			return EffectValueRewritten, nil, nil
		case preservationUnspecified:
			return EffectUnknown, []Warning{WarningManualReview}, nil
		}
	case migration.RebuildTable:
		if facts.beforePresent && facts.afterPresent && facts.preservation == preservationRewrite && !facts.extensionRecreation {
			return EffectValueRewritten, nil, nil
		}
	case migration.CreateProviderExtension:
		if !facts.beforePresent && facts.afterPresent && facts.preservation == preservationUnspecified {
			if facts.extensionRecreation {
				return EffectValueRewritten, nil, nil
			}
			return EffectSchemaOnly, nil, nil
		}
	case migration.DropProviderExtension:
		if facts.beforePresent && !facts.afterPresent && facts.preservation == preservationUnspecified {
			if facts.extensionRecreation {
				return EffectValueRewritten, nil, nil
			}
			return EffectValueDeleted, nil, nil
		}
	case migration.BackfillColumn, migration.ManualStep:
		if facts.beforePresent && facts.afterPresent && facts.preservation == preservationUnspecified && !facts.extensionRecreation {
			return EffectManualDataTransform, nil, nil
		}
	case migration.InitializeConcurrencyColumn:
		if facts.beforePresent && facts.afterPresent && facts.preservation == preservationRewrite && !facts.extensionRecreation {
			return EffectValueRewritten, nil, nil
		}
	case migration.BootstrapSystemSchema, migration.AddSystemObject, migration.CreateNamespace,
		migration.CreateTable, migration.AddColumn, migration.AddPrimaryKey, migration.AddUnique,
		migration.AddForeignKey, migration.AddCheck, migration.CreateIndex:
		if !facts.beforePresent && facts.afterPresent && facts.preservation == preservationRewrite && !facts.extensionRecreation {
			return EffectValueRewritten, nil, nil
		}
		if !facts.beforePresent && facts.afterPresent && facts.preservation == preservationUnspecified && !facts.extensionRecreation {
			return EffectSchemaOnly, nil, nil
		}
	case migration.RenameTable, migration.RenameColumn, migration.AlterColumnNullability,
		migration.SetColumnDefault, migration.DropColumnDefault, migration.RenameIndex,
		migration.ValidateConstraint, migration.RecordSchemaVersion:
		if facts.beforePresent && facts.afterPresent && facts.preservation == preservationUnspecified && !facts.extensionRecreation {
			return EffectSchemaOnly, nil, nil
		}
	case migration.DropPrimaryKey, migration.DropUnique, migration.DropForeignKey,
		migration.DropCheck, migration.DropIndex:
		if facts.beforePresent && !facts.afterPresent && facts.preservation == preservationUnspecified && !facts.extensionRecreation {
			return EffectSchemaOnly, nil, nil
		}
	default:
		return EffectUnknown, []Warning{WarningManualReview}, unavailable()
	}
	return EffectUnknown, []Warning{WarningManualReview}, unavailable()
}

func buildIdentity(kind migration.OperationKind, input identityInput, display displayInput) (Identity, string, error) {
	if !validIdentityInput(input) {
		return Identity{}, "", unavailable()
	}
	allowed := identityInput{}
	switch kind {
	case migration.CreateTable, migration.RenameTable, migration.DropTable, migration.RebuildTable:
		allowed.modelID = input.modelID
	case migration.AddColumn, migration.RenameColumn, migration.AlterColumnType,
		migration.AlterColumnNullability, migration.SetColumnDefault, migration.DropColumnDefault,
		migration.DropColumn, migration.BackfillColumn, migration.InitializeConcurrencyColumn:
		allowed.modelID, allowed.fieldID = input.modelID, input.fieldID
	case migration.AddPrimaryKey, migration.DropPrimaryKey, migration.AddUnique, migration.DropUnique:
		allowed.modelID, allowed.keyID = input.modelID, input.keyID
	case migration.AddForeignKey, migration.DropForeignKey:
		allowed.modelID, allowed.relationID = input.modelID, input.relationID
	case migration.AddCheck, migration.DropCheck:
		allowed.modelID, allowed.checkID = input.modelID, input.checkID
	case migration.CreateIndex, migration.DropIndex, migration.RenameIndex:
		allowed.modelID, allowed.indexID = input.modelID, input.indexID
	case migration.CreateProviderExtension, migration.DropProviderExtension:
		allowed.extensionID = input.extensionID
	case migration.ValidateConstraint:
		allowed.modelID = input.modelID
		memberCount := 0
		for _, present := range []bool{input.fieldID != "", input.keyID != "", input.relationID != "", input.checkID != ""} {
			if present {
				memberCount++
			}
		}
		if memberCount > 1 {
			return Identity{}, "", unavailable()
		}
		allowed.fieldID, allowed.keyID, allowed.relationID, allowed.checkID = input.fieldID, input.keyID, input.relationID, input.checkID
	case migration.BootstrapSystemSchema, migration.AddSystemObject, migration.CreateNamespace,
		migration.ManualStep, migration.RecordSchemaVersion:
		// These operations have no safe logical compiler identity in the v1
		// report. Their migration operation ID remains authoritative.
	default:
		return Identity{}, "", unavailable()
	}
	if input != allowed {
		return Identity{}, "", unavailable()
	}
	hasIdentity := input != (identityInput{})
	label, err := buildDisplay(display, hasIdentity)
	if err != nil {
		return Identity{}, "", err
	}
	return Identity{
		modelID: input.modelID, fieldID: input.fieldID, indexID: input.indexID,
		relationID: input.relationID, keyID: input.keyID, checkID: input.checkID,
		extensionID: input.extensionID,
	}, label, nil
}

func validIdentityInput(value identityInput) bool {
	for _, id := range []string{
		string(value.modelID), string(value.fieldID), string(value.indexID),
		string(value.relationID), string(value.keyID), string(value.checkID),
		string(value.extensionID),
	} {
		if !validStableID(id, true) {
			return false
		}
	}
	return true
}

func buildDisplay(input displayInput, hasIdentity bool) (string, error) {
	if input.model == "" && input.member == "" {
		return "", nil
	}
	if !hasIdentity || !token.IsIdentifier(input.model) || input.member != "" && !token.IsIdentifier(input.member) || !validString(input.model, false, true) || !validString(input.member, true, true) {
		return "", unavailable()
	}
	if input.member == "" {
		return input.model, nil
	}
	return input.model + "." + input.member, nil
}

func validStableID(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return len(value) == 32 && validLowerHex(value)
}

func validDigest(value migration.Digest, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return len(value) == 64 && validLowerHex(string(value))
}

func validLowerHex(value string) bool {
	decoded := make([]byte, hex.DecodedLen(len(value)))
	if _, err := hex.Decode(decoded, []byte(value)); err != nil {
		return false
	}
	return strings.ToLower(value) == value
}

func providerOrder(provider ir.Provider) int {
	switch provider {
	case ir.SQLite:
		return 1
	case ir.PostgreSQL:
		return 2
	default:
		return 0
	}
}

func validRisk(risk migration.Risk) bool {
	switch risk {
	case migration.RiskSafe, migration.RiskLocking, migration.RiskRewrite, migration.RiskDataLoss, migration.RiskManual:
		return true
	default:
		return false
	}
}

func validTransactionMode(mode migration.TransactionMode) bool {
	return mode == migration.Transactional || mode == migration.AutocommitOnly
}

func validRelativePath(value string) bool {
	return validString(value, false, false) && path.Clean(value) == value && value != "." && !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "../") && !strings.Contains(value, "\\") && !strings.Contains(value, ":")
}

func validString(value string, allowEmpty, display bool) bool {
	if (!allowEmpty && value == "") || len(value) > maxStringBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == '\u2028' || character == '\u2029' {
			return false
		}
	}
	if display && (strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") || strings.Contains(value, "://") || len(value) >= 2 && value[1] == ':') {
		return false
	}
	return true
}

func validateReportStrings(report Report) error {
	if report.formatVersion != reportFormatVersion || report.mode != ModeProspective && report.mode != ModeReviewed || report.status != StatusNoChanges && report.status != StatusReviewRequired || len(report.providers) == 0 || len(report.providers) > maxProviders || !validWarnings(report.warnings) {
		return unavailable()
	}
	totalOperations := 0
	previousProviderOrder := 0
	for _, provider := range report.providers {
		order := providerOrder(provider.provider)
		if order == 0 || order <= previousProviderOrder || !validDigest(provider.beforeFingerprint, false) || !validDigest(provider.afterFingerprint, false) || len(provider.phases) > maxCollectionItems || len(provider.artifacts) > maxCollectionItems || !validWarnings(provider.warnings) || len(provider.riskCounts) != 5 {
			return unavailable()
		}
		previousProviderOrder = order
		for index, count := range provider.riskCounts {
			if count.risk != []migration.Risk{migration.RiskSafe, migration.RiskLocking, migration.RiskRewrite, migration.RiskDataLoss, migration.RiskManual}[index] {
				return unavailable()
			}
		}
		for _, artifact := range provider.artifacts {
			if !validRelativePath(artifact.path) || !validDigest(artifact.sha256, false) {
				return unavailable()
			}
		}
		expectedFingerprint := provider.beforeFingerprint
		providerOperationCount := 0
		actualRiskCounts := map[migration.Risk]uint32{}
		for phaseIndex, phase := range provider.phases {
			if phase.ordinal != uint32(phaseIndex) || phase.beforeFingerprint != expectedFingerprint || !validTransactionMode(phase.mode) || !validDigest(phase.beforeFingerprint, false) || !validDigest(phase.afterFingerprint, false) || len(phase.operations) == 0 || len(phase.operations) > maxCollectionItems || providerOperationCount > maxCollectionItems-len(phase.operations) || !validWarnings(phase.warnings) {
				return unavailable()
			}
			for _, operation := range phase.operations {
				if !validStableID(string(operation.id), false) || !knownOperationKind(operation.kind) || operation.mode != phase.mode || !validRisk(operation.risk) || !validEffect(operation.effect) || !validDigest(operation.before, true) || !validDigest(operation.after, true) || len(operation.dependencies) > maxCollectionItems || len(operation.capabilities) > maxCollectionItems || !validWarnings(operation.warnings) || operation.approvalPresent && !operation.approvalRequired {
					return unavailable()
				}
				identity := identityInput{
					modelID: operation.identity.modelID, fieldID: operation.identity.fieldID,
					indexID: operation.identity.indexID, relationID: operation.identity.relationID,
					keyID: operation.identity.keyID, checkID: operation.identity.checkID,
					extensionID: operation.identity.extensionID,
				}
				display, ok := parseBuiltDisplay(operation.display)
				if !ok {
					return unavailable()
				}
				validatedIdentity, validatedDisplay, err := buildIdentity(operation.kind, identity, display)
				if err != nil || validatedIdentity != operation.identity || validatedDisplay != operation.display {
					return unavailable()
				}
				for _, dependency := range operation.dependencies {
					if !validStableID(string(dependency), false) {
						return unavailable()
					}
				}
				for _, capability := range operation.capabilities {
					if !validString(string(capability), false, false) {
						return unavailable()
					}
				}
				if operation.manual != nil && (!validRelativePath(operation.manual.path) || !validDigest(operation.manual.sha256, false) || !validDigest(operation.manual.postcondition, false)) {
					return unavailable()
				}
				actualRiskCounts[operation.risk]++
			}
			providerOperationCount += len(phase.operations)
			expectedFingerprint = phase.afterFingerprint
		}
		if expectedFingerprint != provider.afterFingerprint {
			return unavailable()
		}
		for _, count := range provider.riskCounts {
			if count.count != actualRiskCounts[count.risk] {
				return unavailable()
			}
		}
		totalOperations += providerOperationCount
	}
	if totalOperations == 0 && report.status != StatusNoChanges || totalOperations != 0 && report.status != StatusReviewRequired {
		return unavailable()
	}
	return nil
}

func parseBuiltDisplay(value string) (displayInput, bool) {
	if value == "" {
		return displayInput{}, true
	}
	if strings.Count(value, ".") > 1 {
		return displayInput{}, false
	}
	model, member, found := strings.Cut(value, ".")
	if !found {
		return displayInput{model: model}, true
	}
	if member == "" {
		return displayInput{}, false
	}
	return displayInput{model: model, member: member}, true
}

func knownOperationKind(kind migration.OperationKind) bool {
	for _, candidate := range knownOperationKinds() {
		if kind == candidate {
			return true
		}
	}
	return false
}

func validEffect(effect Effect) bool {
	switch effect {
	case EffectValuePreserving, EffectValueRewritten, EffectValueDeleted,
		EffectSchemaOnly, EffectManualDataTransform, EffectUnknown:
		return true
	default:
		return false
	}
}

func validWarnings(values []Warning) bool {
	if len(values) > maxCollectionItems {
		return false
	}
	allowed := warningSet{}
	for _, warning := range []Warning{
		WarningApprovalRequired, WarningDataLoss, WarningTableOrIndexRewrite,
		WarningStrongLockPossible, WarningManualReview, WarningReviewedBackfill,
		WarningAutocommitBoundary, WarningZeroDowntimeNotGuaranteed,
	} {
		allowed.add(warning)
	}
	seen := warningSet{}
	for _, warning := range values {
		if !allowed[warning] || seen[warning] || !validString(string(warning), false, false) {
			return false
		}
		seen.add(warning)
	}
	return true
}

type warningSet map[Warning]bool

func (set warningSet) add(value Warning) { set[value] = true }
func (set warningSet) addAll(values []Warning) {
	for _, value := range values {
		set.add(value)
	}
}
func (set warningSet) values() []Warning {
	order := []Warning{
		WarningApprovalRequired, WarningDataLoss, WarningTableOrIndexRewrite,
		WarningStrongLockPossible, WarningManualReview, WarningReviewedBackfill,
		WarningAutocommitBoundary, WarningZeroDowntimeNotGuaranteed,
	}
	result := make([]Warning, 0, len(set))
	for _, value := range order {
		if set[value] {
			result = append(result, value)
		}
	}
	return result
}
