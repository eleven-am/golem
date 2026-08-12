package explain

import (
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
)

// BuildProspective converts only a Diff-owned, shape-valid typed plan. It has
// no filesystem, provider, database, publication, or rendering side effects.
func BuildProspective(plan migration.Plan) (Report, error) {
	return BuildProspectiveAll([]migration.Plan{plan})
}

// BuildProspectiveAll validates every declared provider plan before producing
// one canonical report. Presentation filtering is deliberately a later step.
func BuildProspectiveAll(plans []migration.Plan) (Report, error) {
	if !preflightProspective(plans) {
		return Report{}, unavailable()
	}
	inputs := make([]providerInput, 0, len(plans))
	for _, plan := range plans {
		facts, ok := plan.SnapshotFacts()
		if !ok || migration.ValidatePlanShape(plan) != nil {
			return Report{}, unavailable()
		}
		before, after := facts.Before(), facts.After()
		if !planMatchesSnapshots(plan, before, after) {
			return Report{}, unavailable()
		}
		provider, err := adaptProvider(plan, before, after, nil, nil, nil)
		if err != nil {
			return Report{}, err
		}
		inputs = append(inputs, provider)
	}
	return buildReport(reportInput{formatVersion: reportFormatVersion, mode: ModeProspective, providers: inputs})
}

type ReviewedInput struct {
	Entry migration.ManifestEntry
	Files map[string][]byte
}

// BuildReviewed validates one sealed entry, its exact typed snapshot graph,
// approvals, risks, companions, self-chain binding, and every referenced file
// checksum before constructing the report. Complete manifest parent-chain
// validation remains owned by migration history loading.
func BuildReviewed(entry migration.ManifestEntry, files map[string][]byte) (Report, error) {
	return BuildReviewedAll([]ReviewedInput{{Entry: entry, Files: files}})
}

// BuildReviewedAll validates every selected provider entry and artifact set
// before producing one report. Callers must first validate the complete
// manifests; this adapter revalidates the exact selected entry facts.
func BuildReviewedAll(values []ReviewedInput) (Report, error) {
	if !preflightReviewed(values) {
		return Report{}, unavailable()
	}
	inputs := make([]providerInput, 0, len(values))
	for _, value := range values {
		provider, err := adaptReviewed(value.Entry, value.Files)
		if err != nil {
			return Report{}, err
		}
		inputs = append(inputs, provider)
	}
	return buildReport(reportInput{formatVersion: reportFormatVersion, mode: ModeReviewed, providers: inputs})
}

func preflightProspective(plans []migration.Plan) bool {
	if len(plans) == 0 || len(plans) > maxProviders {
		return false
	}
	for _, plan := range plans {
		if !preflightPlan(plan) {
			return false
		}
	}
	return true
}

func preflightReviewed(values []ReviewedInput) bool {
	if len(values) == 0 || len(values) > maxProviders {
		return false
	}
	for _, value := range values {
		entry := value.Entry
		if len(entry.Files) > maxCollectionItems || len(value.Files) > maxCollectionItems || len(entry.Manual) > maxCollectionItems || len(entry.Risks) > maxCollectionItems || len(entry.Approvals) > maxCollectionItems {
			return false
		}
		if !preflightPlan(migration.Plan{Operations: entry.Operations, Phases: entry.Phases}) {
			return false
		}
	}
	return true
}

func preflightPlan(plan migration.Plan) bool {
	if len(plan.Phases) > maxCollectionItems || len(plan.Operations) > maxCollectionItems {
		return false
	}
	remaining := maxCollectionItems
	for _, phase := range plan.Phases {
		if len(phase.Operations) > remaining {
			return false
		}
		remaining -= len(phase.Operations)
	}
	for _, operation := range plan.Operations {
		if len(operation.Dependencies) > maxCollectionItems || len(operation.Capabilities) > maxCollectionItems {
			return false
		}
	}
	return true
}

func adaptReviewed(entry migration.ManifestEntry, files map[string][]byte) (providerInput, error) {
	if entry.ID == "" || entry.ChainHash == "" || migration.ChainHash(entry) != entry.ChainHash {
		return providerInput{}, unavailable()
	}
	plan, err := migration.DiffReviewed(entry.BeforeSnapshot, entry.AfterSnapshot)
	if err != nil || !entryMatchesPlan(entry, plan) || migration.ValidatePlan(plan, entry.Approvals) != nil {
		return providerInput{}, unavailable()
	}
	if !validateReviewedRisks(entry, plan) || !validateReviewedFiles(entry, files) {
		return providerInput{}, unavailable()
	}
	manual, ok := validateReviewedCompanions(entry, plan, files)
	if !ok {
		return providerInput{}, unavailable()
	}
	artifacts := append([]migration.FileChecksum(nil), entry.Files...)
	return adaptProvider(plan, entry.BeforeSnapshot, entry.AfterSnapshot, entry.Approvals, manual, artifacts)
}

func entryMatchesPlan(entry migration.ManifestEntry, plan migration.Plan) bool {
	return plan.Provider == entry.AfterSnapshot.Provider.Provider &&
		entry.BeforeSnapshot.Provider.Provider == plan.Provider &&
		plan.BeforeFingerprint == entry.BeforePhysical &&
		plan.AfterFingerprint == entry.AfterPhysical &&
		reflect.DeepEqual(plan.Operations, entry.Operations) &&
		reflect.DeepEqual(plan.Phases, entry.Phases)
}

func planMatchesSnapshots(plan migration.Plan, before, after physical.PhysicalSchema) bool {
	rebuilt, err := migration.DiffReviewed(before, after)
	return err == nil && plan.Provider == rebuilt.Provider && plan.Initial == rebuilt.Initial &&
		plan.BeforeFingerprint == rebuilt.BeforeFingerprint && plan.AfterFingerprint == rebuilt.AfterFingerprint &&
		reflect.DeepEqual(plan.Operations, rebuilt.Operations) && reflect.DeepEqual(plan.Phases, rebuilt.Phases)
}

func validateReviewedRisks(entry migration.ManifestEntry, plan migration.Plan) bool {
	if len(entry.Risks) != len(plan.Operations) {
		return false
	}
	seen := make(map[migration.OperationID]bool, len(entry.Risks))
	operations := make(map[migration.OperationID]migration.Operation, len(plan.Operations))
	for _, operation := range plan.Operations {
		operations[operation.ID] = operation
	}
	for _, risk := range entry.Risks {
		operation, exists := operations[risk.OperationID]
		if !exists || seen[risk.OperationID] || operation.Risk != risk.Risk {
			return false
		}
		seen[risk.OperationID] = true
	}
	return true
}

func validateReviewedFiles(entry migration.ManifestEntry, files map[string][]byte) bool {
	if len(entry.Files) == 0 || files == nil {
		return false
	}
	seen := make(map[string]bool, len(entry.Files))
	for _, file := range entry.Files {
		content, exists := files[file.Path]
		if file.Path == "" || seen[file.Path] || !exists || migration.Checksum(content) != file.SHA256 {
			return false
		}
		seen[file.Path] = true
	}
	return true
}

func validateReviewedCompanions(entry migration.ManifestEntry, plan migration.Plan, files map[string][]byte) (map[migration.OperationID]migration.ManualCompanion, bool) {
	operations := make(map[migration.OperationID]migration.Operation, len(plan.Operations))
	seenPaths := make(map[string]bool, len(entry.Files)+len(entry.Manual))
	for _, file := range entry.Files {
		seenPaths[file.Path] = true
	}
	for _, operation := range plan.Operations {
		operations[operation.ID] = operation
	}
	manual := make(map[migration.OperationID]migration.ManualCompanion, len(entry.Manual))
	for _, companion := range entry.Manual {
		operation, exists := operations[companion.OperationID]
		content, fileExists := files[companion.File.Path]
		if !exists || !migration.RequiresManualCompanion(operation) || manual[companion.OperationID].OperationID != "" || companion.File.Path == "" || seenPaths[companion.File.Path] ||
			companion.Postcondition == "" || !fileExists || migration.Checksum(content) != companion.File.SHA256 {
			return nil, false
		}
		if operation.Kind == migration.BackfillColumn {
			field := ir.FieldID(operation.ObjectID)
			owner, ownerExists := migration.BackfillOwner(entry.AfterSnapshot, field)
			if !ownerExists || migration.BackfillPostcondition(owner, field) != companion.Postcondition {
				return nil, false
			}
		}
		seenPaths[companion.File.Path] = true
		manual[companion.OperationID] = companion
	}
	for _, operation := range plan.Operations {
		_, exists := manual[operation.ID]
		if migration.RequiresManualCompanion(operation) != exists {
			return nil, false
		}
	}
	return manual, true
}

func adaptProvider(plan migration.Plan, before, after physical.PhysicalSchema, approvals []migration.Approval, manual map[migration.OperationID]migration.ManualCompanion, artifacts []migration.FileChecksum) (providerInput, error) {
	beforeIndex, ok := buildSnapshotIndex(before)
	if !ok {
		return providerInput{}, unavailable()
	}
	afterIndex, ok := buildSnapshotIndex(after)
	if !ok {
		return providerInput{}, unavailable()
	}
	approved := make(map[migration.OperationID]migration.Approval, len(approvals))
	for _, approval := range approvals {
		approved[approval.OperationID] = approval
	}
	operations := make(map[migration.OperationID]migration.Operation, len(plan.Operations))
	for _, operation := range plan.Operations {
		operations[operation.ID] = operation
	}
	provider := providerInput{
		provider: plan.Provider, initial: plan.Initial,
		beforeFingerprint: plan.BeforeFingerprint, afterFingerprint: plan.AfterFingerprint,
	}
	for _, file := range canonicalArtifactOrder(artifacts) {
		provider.artifacts = append(provider.artifacts, artifactInput{path: file.Path, sha256: file.SHA256})
	}
	for _, phase := range plan.Phases {
		adaptedPhase := phaseInput{
			ordinal: phase.Ordinal, mode: phase.Mode,
			beforeFingerprint: phase.BeforeFingerprint, afterFingerprint: phase.AfterFingerprint,
		}
		for _, operationID := range phase.Operations {
			operation, exists := operations[operationID]
			if !exists {
				return providerInput{}, unavailable()
			}
			adapted, err := adaptOperation(plan, operation, beforeIndex, afterIndex, approved, manual)
			if err != nil {
				return providerInput{}, err
			}
			adaptedPhase.operations = append(adaptedPhase.operations, adapted)
		}
		provider.phases = append(provider.phases, adaptedPhase)
	}
	return provider, nil
}

// FilterProvider filters presentation only after the complete report has been
// validated and fully renderable. It cannot hide an invalid sibling provider.
func FilterProvider(report Report, providerID ir.Provider) (Report, error) {
	if err := validateReportStrings(report); err != nil {
		return Report{}, err
	}
	var selected *Provider
	for _, provider := range report.providers {
		if provider.provider == providerID {
			copy := cloneProviders([]Provider{provider})[0]
			selected = &copy
		}
	}
	if selected == nil {
		return Report{}, unavailable()
	}
	warnings := warningSet{}
	warnings.add(WarningZeroDowntimeNotGuaranteed)
	warnings.addAll(selected.warnings)
	filtered := Report{formatVersion: reportFormatVersion, mode: report.mode, providers: []Provider{*selected}, warnings: warnings.values()}
	filtered.status = StatusNoChanges
	for _, phase := range selected.phases {
		if len(phase.operations) != 0 {
			filtered.status = StatusReviewRequired
			break
		}
	}
	if _, err := MarshalJSON(filtered); err != nil {
		return Report{}, err
	}
	if _, err := MarshalText(filtered); err != nil {
		return Report{}, err
	}
	return filtered, nil
}

func adaptOperation(plan migration.Plan, operation migration.Operation, before, after snapshotIndex, approvals map[migration.OperationID]migration.Approval, manual map[migration.OperationID]migration.ManualCompanion) (operationInput, error) {
	identity, effect, ok := operationFacts(plan, operation, before, after)
	if !ok {
		return operationInput{}, unavailable()
	}
	_, approvalPresent := approvals[operation.ID]
	result := operationInput{
		id: operation.ID, kind: operation.Kind, stage: operation.Stage,
		identity: identity, risk: operation.Risk, mode: operation.Mode,
		before: operation.Before, after: operation.After,
		dependencies:     append([]migration.OperationID(nil), operation.Dependencies...),
		capabilities:     append([]ir.CapabilityID(nil), operation.Capabilities...),
		approvalRequired: migration.RequiresApproval(operation), approvalPresent: approvalPresent,
		effect: effect,
	}
	if companion, exists := manual[operation.ID]; exists {
		result.manual = &manualInput{path: companion.File.Path, sha256: companion.File.SHA256, postcondition: companion.Postcondition}
	}
	return result, nil
}

type ownedColumn struct {
	model  ir.ModelID
	column physical.PhysicalColumn
}

type ownedKey struct {
	model ir.ModelID
	key   physical.PhysicalKey
}

type snapshotIndex struct {
	version     uint32
	canonical   uint32
	tables      map[ir.ModelID]physical.PhysicalTable
	columns     map[ir.FieldID]ownedColumn
	keys        map[ir.KeyID]ownedKey
	foreignKeys map[ir.ForeignKeyID]ir.ModelID
	checks      map[ir.CheckID]ir.ModelID
	indexes     map[ir.IndexID]ir.ModelID
	extensions  map[ir.ExtensionID]physical.Extension
}

func buildSnapshotIndex(schema physical.PhysicalSchema) (snapshotIndex, bool) {
	result := snapshotIndex{
		version: schema.Version, canonical: schema.CanonicalVersion,
		tables: make(map[ir.ModelID]physical.PhysicalTable), columns: make(map[ir.FieldID]ownedColumn),
		keys: make(map[ir.KeyID]ownedKey), foreignKeys: make(map[ir.ForeignKeyID]ir.ModelID),
		checks: make(map[ir.CheckID]ir.ModelID), indexes: make(map[ir.IndexID]ir.ModelID),
		extensions: make(map[ir.ExtensionID]physical.Extension),
	}
	for _, table := range schema.Tables {
		if table.ID == "" || result.tables[table.ID].ID != "" {
			return snapshotIndex{}, false
		}
		result.tables[table.ID] = table
		for _, column := range table.Columns {
			if column.ID == "" || result.columns[column.ID].column.ID != "" {
				return snapshotIndex{}, false
			}
			result.columns[column.ID] = ownedColumn{model: table.ID, column: column}
		}
		if table.PrimaryKey != nil {
			if !addOwnedKey(result.keys, table.ID, *table.PrimaryKey) {
				return snapshotIndex{}, false
			}
		}
		for _, key := range table.Uniques {
			if !addOwnedKey(result.keys, table.ID, key) {
				return snapshotIndex{}, false
			}
		}
		for _, foreign := range table.ForeignKeys {
			if foreign.ID == "" || result.foreignKeys[foreign.ID] != "" {
				return snapshotIndex{}, false
			}
			result.foreignKeys[foreign.ID] = table.ID
		}
		for _, check := range table.Checks {
			if check.ID == "" || result.checks[check.ID] != "" {
				return snapshotIndex{}, false
			}
			result.checks[check.ID] = table.ID
		}
		for _, index := range table.Indexes {
			if index.ID == "" || result.indexes[index.ID] != "" {
				return snapshotIndex{}, false
			}
			result.indexes[index.ID] = table.ID
		}
	}
	for _, extension := range schema.Extensions {
		if extension.ID == "" || result.extensions[extension.ID].ID != "" {
			return snapshotIndex{}, false
		}
		result.extensions[extension.ID] = extension
	}
	return result, true
}

func addOwnedKey(keys map[ir.KeyID]ownedKey, model ir.ModelID, key physical.PhysicalKey) bool {
	if key.ID == "" || keys[key.ID].key.ID != "" {
		return false
	}
	keys[key.ID] = ownedKey{model: model, key: key}
	return true
}

func operationFacts(plan migration.Plan, operation migration.Operation, before, after snapshotIndex) (identityInput, effectInput, bool) {
	var identity identityInput
	facts := effectInput{}
	var ok bool
	id := operation.ObjectID
	switch operation.Kind {
	case migration.CreateTable, migration.RenameTable, migration.DropTable, migration.RebuildTable:
		left, beforePresent := before.tables[ir.ModelID(id)]
		right, afterPresent := after.tables[ir.ModelID(id)]
		if !beforePresent && !afterPresent {
			return identity, facts, false
		}
		identity.modelID = ir.ModelID(id)
		facts.beforePresent, facts.afterPresent, ok = operationLocalPresence(operation, beforePresent, afterPresent)
		if !ok {
			return identity, facts, false
		}
		if operation.Kind == migration.RebuildTable {
			facts.preservation = preservationRewrite
		}
		_ = left
		_ = right
	case migration.AddColumn, migration.RenameColumn, migration.AlterColumnType, migration.AlterColumnNullability, migration.SetColumnDefault, migration.DropColumnDefault, migration.DropColumn, migration.BackfillColumn, migration.InitializeConcurrencyColumn:
		left, beforePresent := before.columns[ir.FieldID(id)]
		right, afterPresent := after.columns[ir.FieldID(id)]
		if !beforePresent && !afterPresent {
			return identity, facts, false
		}
		owner := right.model
		if owner == "" {
			owner = left.model
		}
		identity.modelID, identity.fieldID = owner, ir.FieldID(id)
		facts.beforePresent, facts.afterPresent, ok = operationLocalPresence(operation, beforePresent, afterPresent)
		if !ok {
			return identity, facts, false
		}
		switch operation.Kind {
		case migration.InitializeConcurrencyColumn:
			facts.beforePresent, facts.afterPresent = true, true
			facts.preservation = preservationRewrite
		case migration.BackfillColumn:
			facts.beforePresent, facts.afterPresent = true, true
		case migration.AlterColumnNullability:
			// Required-column authoring adds a nullable physical column, runs the
			// reviewed backfill, validates it, and only then applies the final
			// non-null shape. The whole-plan before snapshot therefore lacks the
			// field even though this exact typed operation has both states.
			facts.beforePresent, facts.afterPresent = true, true
		case migration.AlterColumnType:
			if !beforePresent || !afterPresent {
				return identity, facts, false
			}
			formatUpgrade := before.version == 1 && before.canonical == 1 && after.version == 2 && after.canonical == 2
			if migration.SafeWidening(plan.Provider, left.column.Storage, right.column.Storage) ||
				formatUpgrade && plan.Provider == ir.PostgreSQL && migration.PostgreSQLAutomaticTypeTransition(left.column.Storage, right.column.Storage, true) {
				facts.preservation = preservationSafeWidening
			}
		case migration.AddColumn, migration.DropColumn:
			column := right.column
			if operation.Kind == migration.DropColumn {
				column = left.column
			}
			if operation.Risk == migration.RiskRewrite && column.Generated != nil {
				facts.preservation = preservationRewrite
			}
		}
	case migration.AddPrimaryKey, migration.DropPrimaryKey, migration.AddUnique, migration.DropUnique:
		left, beforePresent := before.keys[ir.KeyID(id)]
		right, afterPresent := after.keys[ir.KeyID(id)]
		if !beforePresent && !afterPresent {
			return identity, facts, false
		}
		identity.modelID, identity.keyID = right.model, ir.KeyID(id)
		if identity.modelID == "" {
			identity.modelID = left.model
		}
		facts.beforePresent, facts.afterPresent, ok = operationLocalPresence(operation, beforePresent, afterPresent)
		if !ok {
			return identity, facts, false
		}
	case migration.AddForeignKey, migration.DropForeignKey:
		left, beforePresent := before.foreignKeys[ir.ForeignKeyID(id)]
		right, afterPresent := after.foreignKeys[ir.ForeignKeyID(id)]
		if !beforePresent && !afterPresent {
			return identity, facts, false
		}
		identity.modelID = right
		if identity.modelID == "" {
			identity.modelID = left
		}
		facts.beforePresent, facts.afterPresent, ok = operationLocalPresence(operation, beforePresent, afterPresent)
		if !ok {
			return identity, facts, false
		}
	case migration.AddCheck, migration.DropCheck:
		left, beforePresent := before.checks[ir.CheckID(id)]
		right, afterPresent := after.checks[ir.CheckID(id)]
		if !beforePresent && !afterPresent {
			return identity, facts, false
		}
		identity.modelID, identity.checkID = right, ir.CheckID(id)
		if identity.modelID == "" {
			identity.modelID = left
		}
		facts.beforePresent, facts.afterPresent, ok = operationLocalPresence(operation, beforePresent, afterPresent)
		if !ok {
			return identity, facts, false
		}
	case migration.CreateIndex, migration.DropIndex, migration.RenameIndex:
		left, beforePresent := before.indexes[ir.IndexID(id)]
		right, afterPresent := after.indexes[ir.IndexID(id)]
		if !beforePresent && !afterPresent {
			return identity, facts, false
		}
		identity.modelID, identity.indexID = right, ir.IndexID(id)
		if identity.modelID == "" {
			identity.modelID = left
		}
		facts.beforePresent, facts.afterPresent, ok = operationLocalPresence(operation, beforePresent, afterPresent)
		if !ok {
			return identity, facts, false
		}
	case migration.CreateProviderExtension, migration.DropProviderExtension:
		_, beforePresent := before.extensions[ir.ExtensionID(id)]
		_, afterPresent := after.extensions[ir.ExtensionID(id)]
		if !beforePresent && !afterPresent {
			return identity, facts, false
		}
		identity.extensionID = ir.ExtensionID(id)
		facts.beforePresent, facts.afterPresent, ok = operationLocalPresence(operation, beforePresent, afterPresent)
		if !ok {
			return identity, facts, false
		}
		facts.extensionRecreation = operation.Risk == migration.RiskRewrite && facts.beforePresent != facts.afterPresent
	case migration.ValidateConstraint:
		left, beforePresent := before.columns[ir.FieldID(id)]
		right, afterPresent := after.columns[ir.FieldID(id)]
		if !beforePresent && !afterPresent {
			return identity, facts, false
		}
		identity.modelID, identity.fieldID = right.model, ir.FieldID(id)
		if identity.modelID == "" {
			identity.modelID = left.model
		}
		facts.beforePresent, facts.afterPresent = true, true
	case migration.BootstrapSystemSchema, migration.AddSystemObject, migration.CreateNamespace:
		facts.afterPresent = true
	case migration.ManualStep:
		facts.beforePresent, facts.afterPresent = true, true
	case migration.RecordSchemaVersion:
		facts.beforePresent, facts.afterPresent = true, true
	default:
		return identity, facts, false
	}
	return identity, facts, true
}

// operationLocalPresence derives the effect-local state transition from the
// exact re-derived typed operation. Whole snapshots remain authoritative for
// stable-identity binding, but cannot distinguish a same-ID drop/create pair.
func operationLocalPresence(operation migration.Operation, beforePresent, afterPresent bool) (bool, bool, bool) {
	switch operation.Kind {
	case migration.CreateTable, migration.AddColumn, migration.AddPrimaryKey, migration.AddUnique,
		migration.AddForeignKey, migration.AddCheck, migration.CreateIndex, migration.CreateProviderExtension:
		if operation.Before != "" || operation.After == "" || !afterPresent {
			return false, false, false
		}
		return false, true, true
	case migration.DropTable, migration.DropColumn, migration.DropPrimaryKey, migration.DropUnique,
		migration.DropForeignKey, migration.DropCheck, migration.DropIndex, migration.DropProviderExtension:
		if operation.Before == "" || operation.After != "" || !beforePresent {
			return false, false, false
		}
		return true, false, true
	default:
		return beforePresent, afterPresent, true
	}
}

func canonicalArtifactOrder(files []migration.FileChecksum) []migration.FileChecksum {
	result := append([]migration.FileChecksum(nil), files...)
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}
