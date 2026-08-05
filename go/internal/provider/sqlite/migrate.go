package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

// ColumnCopy is one explicit, identity-preserving leg of a SQLite rebuild.
// Generated and newly defaulted columns are intentionally absent.
type ColumnCopy struct {
	FieldID     ir.FieldID
	Source      physical.PhysicalName
	Destination physical.PhysicalName
}

// TableRebuild is the reviewed semantic description lowered to SQLite's
// complete-table replacement algorithm. SQL remains provider-private.
type TableRebuild struct {
	TableID         ir.ModelID
	SourceTableName physical.PhysicalName
	TemporaryName   physical.PhysicalName
	DesiredTable    physical.PhysicalTable
	OperationIDs    []migration.OperationID
	Columns         []ColumnCopy
}

// IncrementalPlan is an immutable provider-generated execution plan. Rebuilds
// are exposed for auditability; executable statements cannot be supplied by a
// caller.
type IncrementalPlan struct {
	MigrationID migration.MigrationID
	Rebuilds    []TableRebuild
	steps       []migrationStep
	guards      []structuralGuard
}

type migrationStep struct {
	statements []string
	rebuild    *TableRebuild
}

type structuralGuard struct {
	tableID ir.ModelID
	action  string
}

// PlanIncremental lowers an already reviewed semantic migration entry. It
// accepts no casts, backfills, manual SQL, or caller-defined transforms.
func (provider *Provider) PlanIncremental(entry migration.ManifestEntry) (IncrementalPlan, error) {
	return provider.planIncremental(entry)
}

// ApplyMigration verifies immutable history and applies exactly the next
// reviewed entry. A call never skips, rewrites, or batches ledger entries.
func (provider *Provider) ApplyMigration(ctx context.Context, database *sqlx.DB, manifest migration.Manifest, files map[string][]byte) error {
	return provider.applyMigration(ctx, database, manifest, files)
}

// RenderMigration returns the exact provider DDL reviewed for one typed entry.
// ApplyMigration independently reconstructs the same private plan from the
// checksummed snapshots; reviewed SQL is never accepted as executable input.
func (provider *Provider) RenderMigration(entry migration.ManifestEntry) (Script, error) {
	semantic, err := migration.Diff(entry.BeforeSnapshot, entry.AfterSnapshot)
	if err != nil {
		return Script{}, err
	}
	if semantic.Initial {
		return provider.renderInitial(entry.AfterSnapshot)
	}
	plan, err := provider.planIncremental(entry)
	if err != nil {
		return Script{}, err
	}
	var statements []string
	for _, step := range plan.steps {
		statements = append(statements, step.statements...)
	}
	return Script{statements: statements}, nil
}

func (provider *Provider) verifyReviewedSQL(manifest migration.Manifest, files map[string][]byte) error {
	for _, entry := range manifest.Entries {
		var sqlPath string
		for _, file := range entry.Files {
			if strings.HasSuffix(file.Path, ".sql") {
				if sqlPath != "" {
					return fmt.Errorf("sqlite migration %s has multiple reviewed SQL artifacts", entry.ID)
				}
				sqlPath = file.Path
			}
		}
		if sqlPath == "" {
			return fmt.Errorf("sqlite migration %s has no reviewed SQL artifact", entry.ID)
		}
		script, err := provider.RenderMigration(entry)
		if err != nil {
			return fmt.Errorf("sqlite migration %s render reviewed SQL: %w", entry.ID, err)
		}
		if !bytes.Equal(files[sqlPath], []byte(script.SQL())) {
			return fmt.Errorf("sqlite migration %s reviewed SQL differs from deterministic provider rendering", entry.ID)
		}
	}
	return nil
}

// ReadLedger returns the exact ordered SQLite migration chain.
func (*Provider) ReadLedger(ctx context.Context, database *sqlx.DB) ([]migration.LedgerEntry, error) {
	if database == nil {
		return nil, fmt.Errorf("sqlite ledger: database is nil")
	}
	exists, err := sqliteObjectExists(ctx, database, "table", "_golem_migrations")
	if err != nil {
		return nil, err
	}
	if !exists {
		return []migration.LedgerEntry{}, nil
	}
	return readLedger(ctx, database)
}

func (provider *Provider) planIncremental(entry migration.ManifestEntry) (IncrementalPlan, error) {
	semantic, err := migration.Diff(entry.BeforeSnapshot, entry.AfterSnapshot)
	if err != nil {
		return IncrementalPlan{}, fmt.Errorf("sqlite migration %s semantic diff: %w", entry.ID, err)
	}
	if semantic.Provider != ir.SQLite || semantic.BeforeFingerprint != entry.BeforePhysical || semantic.AfterFingerprint != entry.AfterPhysical ||
		!reflect.DeepEqual(semantic.Operations, entry.Operations) || !reflect.DeepEqual(semantic.Phases, entry.Phases) {
		return IncrementalPlan{}, fmt.Errorf("sqlite migration %s operation graph differs from embedded snapshots", entry.ID)
	}
	if err := migration.ValidatePlan(semantic, entry.Approvals); err != nil {
		return IncrementalPlan{}, fmt.Errorf("sqlite migration %s invalid reviewed plan: %w", entry.ID, err)
	}
	if len(entry.Manual) != 0 {
		return IncrementalPlan{}, fmt.Errorf("sqlite migration %s manual companions are unsupported by the automatic runner", entry.ID)
	}
	for _, phase := range entry.Phases {
		if phase.Mode != migration.Transactional {
			return IncrementalPlan{}, fmt.Errorf("sqlite migration %s phase %d is not transactional", entry.ID, phase.Ordinal)
		}
	}
	for _, operation := range semantic.Operations {
		if operation.Transform != nil || operation.Kind == migration.BackfillColumn || operation.Kind == migration.ManualStep || operation.Kind == migration.ValidateConstraint {
			return IncrementalPlan{}, fmt.Errorf("sqlite migration %s operation %s requires an unsupported transform or manual step", entry.ID, operation.ID)
		}
	}

	beforeTables := tableMap(entry.BeforeSnapshot.Tables)
	afterTables := tableMap(entry.AfterSnapshot.Tables)
	owners, err := operationOwners(entry.BeforeSnapshot, entry.AfterSnapshot)
	if err != nil {
		return IncrementalPlan{}, err
	}
	newTables := map[ir.ModelID]bool{}
	for id := range afterTables {
		_, existed := beforeTables[id]
		newTables[id] = !existed
	}
	rebuildTables := map[ir.ModelID]bool{}
	for _, operation := range semantic.Operations {
		if operation.Kind == migration.RecordSchemaVersion {
			continue
		}
		tableID, ok := owners[operation.ID]
		if !ok {
			return IncrementalPlan{}, fmt.Errorf("sqlite migration %s cannot resolve owner of operation %s", entry.ID, operation.ID)
		}
		if newTables[tableID] || operation.Kind == migration.CreateTable || operation.Kind == migration.DropTable {
			continue
		}
		if !directSQLiteOperation(operation, beforeTables[tableID], afterTables[tableID]) {
			rebuildTables[tableID] = true
		}
	}

	plan := IncrementalPlan{MigrationID: entry.ID}
	guarded := map[ir.ModelID]bool{}
	for _, operation := range semantic.Operations {
		if operation.Kind != migration.DropTable && operation.Kind != migration.RenameTable && operation.Kind != migration.RenameColumn {
			continue
		}
		tableID := owners[operation.ID]
		if !rebuildTables[tableID] && !guarded[tableID] {
			plan.guards = append(plan.guards, structuralGuard{tableID: tableID, action: string(operation.Kind)})
			guarded[tableID] = true
		}
	}
	rebuildByTable := map[ir.ModelID]TableRebuild{}
	for tableID := range rebuildTables {
		before, beforeOK := beforeTables[tableID]
		after, afterOK := afterTables[tableID]
		if !beforeOK || !afterOK {
			return IncrementalPlan{}, fmt.Errorf("sqlite migration %s cannot rebuild missing table %s", entry.ID, tableID)
		}
		rebuild, buildErr := buildTableRebuild(before, after, entry.AfterPhysical)
		if buildErr != nil {
			return IncrementalPlan{}, fmt.Errorf("sqlite migration %s: %w", entry.ID, buildErr)
		}
		rebuildByTable[tableID] = rebuild
	}
	for _, operation := range semantic.Operations {
		if tableID, exists := owners[operation.ID]; exists && rebuildTables[tableID] {
			rebuild := rebuildByTable[tableID]
			rebuild.OperationIDs = append(rebuild.OperationIDs, operation.ID)
			rebuildByTable[tableID] = rebuild
		}
	}
	for _, rebuild := range rebuildByTable {
		plan.Rebuilds = append(plan.Rebuilds, rebuild)
	}
	sort.Slice(plan.Rebuilds, func(i, j int) bool { return plan.Rebuilds[i].TableID < plan.Rebuilds[j].TableID })

	emitted := map[ir.ModelID]bool{}
	for _, operation := range semantic.Operations {
		if operation.Kind == migration.RecordSchemaVersion {
			continue
		}
		tableID := owners[operation.ID]
		if emitted[tableID] && (newTables[tableID] || rebuildTables[tableID]) {
			continue
		}
		if newTables[tableID] {
			table := afterTables[tableID]
			statements, renderErr := renderCompleteTable(table, afterTables)
			if renderErr != nil {
				return IncrementalPlan{}, renderErr
			}
			plan.steps = append(plan.steps, migrationStep{statements: statements})
			emitted[tableID] = true
			continue
		}
		if rebuildTables[tableID] {
			rebuild := rebuildByTable[tableID]
			statements, renderErr := renderRebuild(beforeTables[tableID], afterTables[tableID], afterTables, rebuild)
			if renderErr != nil {
				return IncrementalPlan{}, renderErr
			}
			plan.steps = append(plan.steps, migrationStep{statements: statements, rebuild: &rebuild})
			emitted[tableID] = true
			continue
		}
		statements, renderErr := renderDirectOperation(operation, beforeTables, afterTables, tableID)
		if renderErr != nil {
			return IncrementalPlan{}, fmt.Errorf("sqlite migration %s operation %s: %w", entry.ID, operation.ID, renderErr)
		}
		plan.steps = append(plan.steps, migrationStep{statements: statements})
	}
	return plan, nil
}

func directSQLiteOperation(operation migration.Operation, before, after physical.PhysicalTable) bool {
	switch operation.Kind {
	case migration.RenameTable, migration.RenameColumn, migration.CreateIndex, migration.DropIndex, migration.RenameIndex:
		return true
	case migration.AddColumn:
		for _, column := range after.Columns {
			if string(column.ID) != operation.ObjectID {
				continue
			}
			if column.Generated != nil || column.Collation != nil || column.Default.Kind == physical.DefaultProvider {
				return false
			}
			// SQLite appends an ALTER TABLE column. Inserting a semantic field at
			// an earlier physical ordinal requires a rebuild to preserve catalog
			// order and therefore the physical fingerprint.
			return column.Ordinal == uint32(len(before.Columns)) && (column.Nullable || column.Default.Kind == physical.DefaultLiteral)
		}
	}
	_ = before
	return false
}

func buildTableRebuild(before, after physical.PhysicalTable, fingerprint migration.Digest) (TableRebuild, error) {
	oldColumns := map[ir.FieldID]physical.PhysicalColumn{}
	for _, column := range before.Columns {
		oldColumns[column.ID] = column
	}
	rebuild := TableRebuild{TableID: after.ID, SourceTableName: before.Name, TemporaryName: rebuildTemporaryName(after, fingerprint), DesiredTable: after}
	for _, column := range after.Columns {
		old, exists := oldColumns[column.ID]
		if !exists {
			if column.Generated == nil && !column.Nullable && column.Default.Kind == physical.DefaultNone {
				return TableRebuild{}, fmt.Errorf("table %s adds required field %s without a reviewed default/backfill", after.ID, column.ID)
			}
			continue
		}
		if !reflect.DeepEqual(old.Storage, column.Storage) {
			return TableRebuild{}, fmt.Errorf("table %s field %s changes storage and requires an explicit reviewed cast", after.ID, column.ID)
		}
		if column.Generated != nil {
			continue
		}
		rebuild.Columns = append(rebuild.Columns, ColumnCopy{FieldID: column.ID, Source: old.Name, Destination: column.Name})
	}
	if len(rebuild.Columns) == 0 {
		return TableRebuild{}, fmt.Errorf("table %s rebuild has no explicit writable field mapping", after.ID)
	}
	return rebuild, nil
}

func rebuildTemporaryName(table physical.PhysicalTable, fingerprint migration.Digest) physical.PhysicalName {
	prefix := make([]byte, 0, 20)
	for _, value := range []byte(strings.ToLower(string(table.Name))) {
		if (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') || value == '_' {
			prefix = append(prefix, value)
		}
		if len(prefix) == 20 {
			break
		}
	}
	if len(prefix) == 0 {
		prefix = []byte("table")
	}
	sum := sha256.Sum256([]byte(string(table.ID) + "\x00" + string(fingerprint)))
	return physical.PhysicalName("_golem_tmp_" + string(prefix) + "_" + hex.EncodeToString(sum[:6]))
}

func renderCompleteTable(table physical.PhysicalTable, tables map[ir.ModelID]physical.PhysicalTable) ([]string, error) {
	statement, err := renderTable(table, tables)
	if err != nil {
		return nil, err
	}
	statements := []string{statement}
	for _, index := range table.Indexes {
		rendered, renderErr := renderIndex(table, index)
		if renderErr != nil {
			return nil, renderErr
		}
		statements = append(statements, rendered)
	}
	return statements, nil
}

func renderRebuild(before, after physical.PhysicalTable, tables map[ir.ModelID]physical.PhysicalTable, rebuild TableRebuild) ([]string, error) {
	temporary := after
	temporary.Name = rebuild.TemporaryName
	create, err := renderTable(temporary, tables)
	if err != nil {
		return nil, err
	}
	destinations := make([]string, len(rebuild.Columns))
	sources := make([]string, len(rebuild.Columns))
	for index, column := range rebuild.Columns {
		destinations[index] = quote(column.Destination)
		sources[index] = quote(column.Source)
	}
	statements := []string{
		create,
		"INSERT INTO " + quote(rebuild.TemporaryName) + " (" + strings.Join(destinations, ", ") + ") SELECT " + strings.Join(sources, ", ") + " FROM " + quote(before.Name),
		"DROP TABLE " + quote(before.Name),
		"ALTER TABLE " + quote(rebuild.TemporaryName) + " RENAME TO " + quote(after.Name),
	}
	for _, index := range after.Indexes {
		rendered, renderErr := renderIndex(after, index)
		if renderErr != nil {
			return nil, renderErr
		}
		statements = append(statements, rendered)
	}
	return statements, nil
}

func renderDirectOperation(operation migration.Operation, beforeTables, afterTables map[ir.ModelID]physical.PhysicalTable, tableID ir.ModelID) ([]string, error) {
	before, hadBefore := beforeTables[tableID]
	after, hasAfter := afterTables[tableID]
	switch operation.Kind {
	case migration.DropTable:
		if !hadBefore {
			return nil, fmt.Errorf("drop table target is absent")
		}
		return []string{"DROP TABLE " + quote(before.Name)}, nil
	case migration.RenameTable:
		return []string{"ALTER TABLE " + quote(before.Name) + " RENAME TO " + quote(after.Name)}, nil
	case migration.RenameColumn:
		old, oldOK := findColumnByID(before, ir.FieldID(operation.ObjectID))
		current, currentOK := findColumnByID(after, ir.FieldID(operation.ObjectID))
		if !oldOK || !currentOK {
			return nil, fmt.Errorf("rename column target is absent")
		}
		return []string{"ALTER TABLE " + quote(after.Name) + " RENAME COLUMN " + quote(old.Name) + " TO " + quote(current.Name)}, nil
	case migration.AddColumn:
		column, ok := findColumnByID(after, ir.FieldID(operation.ObjectID))
		if !ok {
			return nil, fmt.Errorf("add column target is absent")
		}
		columns := map[ir.FieldID]physical.PhysicalColumn{}
		for _, value := range after.Columns {
			columns[value.ID] = value
		}
		rendered, err := renderColumn(column, columns)
		if err != nil {
			return nil, err
		}
		return []string{"ALTER TABLE " + quote(after.Name) + " ADD COLUMN " + rendered}, nil
	case migration.CreateIndex:
		index, ok := findPhysicalIndex(after, ir.IndexID(operation.ObjectID))
		if !ok || !hasAfter {
			return nil, fmt.Errorf("create index target is absent")
		}
		rendered, err := renderIndex(after, index)
		return []string{rendered}, err
	case migration.DropIndex:
		index, ok := findPhysicalIndex(before, ir.IndexID(operation.ObjectID))
		if !ok {
			return nil, fmt.Errorf("drop index target is absent")
		}
		return []string{"DROP INDEX " + quote(index.Name)}, nil
	case migration.RenameIndex:
		old, oldOK := findPhysicalIndex(before, ir.IndexID(operation.ObjectID))
		current, currentOK := findPhysicalIndex(after, ir.IndexID(operation.ObjectID))
		if !oldOK || !currentOK {
			return nil, fmt.Errorf("rename index target is absent")
		}
		rendered, err := renderIndex(after, current)
		return []string{"DROP INDEX " + quote(old.Name), rendered}, err
	default:
		return nil, fmt.Errorf("operation kind %s was not lowered", operation.Kind)
	}
}

func operationOwners(before, after physical.PhysicalSchema) (map[migration.OperationID]ir.ModelID, error) {
	objectOwners := map[string]ir.ModelID{}
	add := func(table physical.PhysicalTable) error {
		ids := []string{string(table.ID)}
		for _, column := range table.Columns {
			ids = append(ids, string(column.ID))
		}
		if table.PrimaryKey != nil {
			ids = append(ids, string(table.PrimaryKey.ID))
		}
		for _, key := range table.Uniques {
			ids = append(ids, string(key.ID))
		}
		for _, foreignKey := range table.ForeignKeys {
			ids = append(ids, string(foreignKey.ID))
		}
		for _, check := range table.Checks {
			ids = append(ids, string(check.ID))
		}
		for _, index := range table.Indexes {
			ids = append(ids, string(index.ID))
		}
		for _, id := range ids {
			if owner, exists := objectOwners[id]; exists && owner != table.ID {
				return fmt.Errorf("sqlite migration stable object ID %s has multiple table owners", id)
			}
			objectOwners[id] = table.ID
		}
		return nil
	}
	for _, table := range before.Tables {
		if err := add(table); err != nil {
			return nil, err
		}
	}
	for _, table := range after.Tables {
		if err := add(table); err != nil {
			return nil, err
		}
	}
	plan, err := migration.Diff(before, after)
	if err != nil {
		return nil, err
	}
	result := map[migration.OperationID]ir.ModelID{}
	for _, operation := range plan.Operations {
		if operation.Kind == migration.RecordSchemaVersion {
			continue
		}
		owner, exists := objectOwners[operation.ObjectID]
		if !exists {
			return nil, fmt.Errorf("sqlite migration object %s has no table owner", operation.ObjectID)
		}
		result[operation.ID] = owner
	}
	return result, nil
}

func tableMap(tables []physical.PhysicalTable) map[ir.ModelID]physical.PhysicalTable {
	result := make(map[ir.ModelID]physical.PhysicalTable, len(tables))
	for _, table := range tables {
		result[table.ID] = table
	}
	return result
}

func findColumnByID(table physical.PhysicalTable, id ir.FieldID) (physical.PhysicalColumn, bool) {
	for _, column := range table.Columns {
		if column.ID == id {
			return column, true
		}
	}
	return physical.PhysicalColumn{}, false
}

func findPhysicalIndex(table physical.PhysicalTable, id ir.IndexID) (physical.PhysicalIndex, bool) {
	for _, index := range table.Indexes {
		if index.ID == id {
			return index, true
		}
	}
	return physical.PhysicalIndex{}, false
}

type ledgerRow struct {
	MigrationID               string `db:"migration_id"`
	ParentChainHash           string `db:"parent_chain_hash"`
	ChainHash                 string `db:"chain_hash"`
	FileChecksums             string `db:"file_checksums"`
	BeforePhysicalFingerprint string `db:"before_physical_fingerprint"`
	AfterPhysicalFingerprint  string `db:"after_physical_fingerprint"`
	Phases                    string `db:"phases"`
	AppliedAt                 string `db:"applied_at"`
}

type ledgerFileJSON struct {
	Path   string           `json:"path"`
	SHA256 migration.Digest `json:"sha256"`
}
type ledgerPhaseJSON struct {
	Ordinal          uint32                `json:"ordinal"`
	Status           migration.PhaseStatus `json:"status"`
	AfterFingerprint migration.Digest      `json:"afterFingerprint"`
}

func readLedger(ctx context.Context, database catalogQuerier) ([]migration.LedgerEntry, error) {
	var rows []ledgerRow
	if err := database.SelectContext(ctx, &rows, "SELECT migration_id,parent_chain_hash,chain_hash,file_checksums,before_physical_fingerprint,after_physical_fingerprint,phases,applied_at FROM _golem_migrations"); err != nil {
		return nil, fmt.Errorf("sqlite ledger read: %w", err)
	}
	byParent := map[migration.Digest]ledgerRow{}
	for _, row := range rows {
		parent := migration.Digest(row.ParentChainHash)
		if _, duplicate := byParent[parent]; duplicate {
			return nil, fmt.Errorf("sqlite ledger has a branch at parent hash %q", parent)
		}
		byParent[parent] = row
	}
	result := make([]migration.LedgerEntry, 0, len(rows))
	parent := migration.Digest("")
	for len(result) < len(rows) {
		row, exists := byParent[parent]
		if !exists {
			return nil, fmt.Errorf("sqlite ledger chain is disconnected")
		}
		entry, err := decodeLedgerRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, entry)
		delete(byParent, parent)
		parent = entry.ChainHash
	}
	return result, nil
}

func decodeLedgerRow(row ledgerRow) (migration.LedgerEntry, error) {
	var filesJSON []ledgerFileJSON
	if err := decodeCanonicalJSON(row.FileChecksums, &filesJSON); err != nil {
		return migration.LedgerEntry{}, fmt.Errorf("sqlite ledger migration %s file checksums: %w", row.MigrationID, err)
	}
	var phasesJSON []ledgerPhaseJSON
	if err := decodeCanonicalJSON(row.Phases, &phasesJSON); err != nil {
		return migration.LedgerEntry{}, fmt.Errorf("sqlite ledger migration %s phases: %w", row.MigrationID, err)
	}
	appliedAt, err := time.Parse(time.RFC3339Nano, row.AppliedAt)
	if err != nil {
		return migration.LedgerEntry{}, fmt.Errorf("sqlite ledger migration %s applied_at: %w", row.MigrationID, err)
	}
	entry := migration.LedgerEntry{MigrationID: migration.MigrationID(row.MigrationID), ParentChainHash: migration.Digest(row.ParentChainHash), ChainHash: migration.Digest(row.ChainHash), BeforePhysical: migration.Digest(row.BeforePhysicalFingerprint), AfterPhysical: migration.Digest(row.AfterPhysicalFingerprint), AppliedAt: appliedAt}
	for _, file := range filesJSON {
		entry.Files = append(entry.Files, migration.FileChecksum{Path: file.Path, SHA256: file.SHA256})
	}
	for _, phase := range phasesJSON {
		entry.Phases = append(entry.Phases, migration.LedgerPhase{Ordinal: phase.Ordinal, Status: phase.Status, AfterFingerprint: phase.AfterFingerprint})
	}
	return entry, nil
}

func decodeCanonicalJSON(value string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("trailing JSON value")
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, []byte(value)) {
		return fmt.Errorf("value is not canonical JSON")
	}
	return nil
}

func encodeLedger(entry migration.LedgerEntry) (string, string, error) {
	files := make([]ledgerFileJSON, len(entry.Files))
	for index, file := range entry.Files {
		files[index] = ledgerFileJSON{Path: file.Path, SHA256: file.SHA256}
	}
	phases := make([]ledgerPhaseJSON, len(entry.Phases))
	for index, phase := range entry.Phases {
		phases[index] = ledgerPhaseJSON{Ordinal: phase.Ordinal, Status: phase.Status, AfterFingerprint: phase.AfterFingerprint}
	}
	fileBytes, err := json.Marshal(files)
	if err != nil {
		return "", "", err
	}
	phaseBytes, err := json.Marshal(phases)
	if err != nil {
		return "", "", err
	}
	return string(fileBytes), string(phaseBytes), nil
}

func (provider *Provider) applyMigration(ctx context.Context, database *sqlx.DB, manifest migration.Manifest, files map[string][]byte) (resultErr error) {
	if database == nil {
		return fmt.Errorf("sqlite migration: database is nil")
	}
	if err := migration.VerifyManifest(manifest, files); err != nil {
		return fmt.Errorf("sqlite migration manifest: %w", err)
	}
	if err := provider.verifyReviewedSQL(manifest, files); err != nil {
		return err
	}
	if _, err := provider.probe(ctx, database); err != nil {
		return err
	}
	connection, err := database.Connx(ctx)
	if err != nil {
		return fmt.Errorf("sqlite migration connection: %w", err)
	}
	defer connection.Close()
	ledgerExists, err := sqliteObjectExists(ctx, connection, "table", "_golem_migrations")
	if err != nil {
		return err
	}
	if !ledgerExists {
		if len(manifest.Entries) == 0 {
			return fmt.Errorf("sqlite migration has no reviewed entry for an empty database")
		}
		return provider.applyBootstrapMigration(ctx, connection, manifest)
	}

	ledger, err := readLedger(ctx, connection)
	if err != nil {
		return err
	}
	if len(ledger) > len(manifest.Entries) {
		return fmt.Errorf("sqlite migration ledger is ahead of reviewed manifest")
	}
	prefix := manifest
	prefix.Entries = manifest.Entries[:len(ledger)]
	if err := migration.VerifyLedger(prefix, ledger); err != nil {
		return fmt.Errorf("sqlite migration starting ledger: %w", err)
	}
	if len(ledger) == len(manifest.Entries) {
		return fmt.Errorf("sqlite migration has no unapplied reviewed entry")
	}
	entry := manifest.Entries[len(ledger)]
	plan, err := provider.planIncremental(entry)
	if err != nil {
		return err
	}

	var foreignKeys int
	if err := connection.GetContext(ctx, &foreignKeys, "PRAGMA foreign_keys"); err != nil {
		return fmt.Errorf("sqlite migration read foreign_keys: %w", err)
	}
	if foreignKeys != 0 && foreignKeys != 1 {
		return fmt.Errorf("sqlite migration invalid foreign_keys state %d", foreignKeys)
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("sqlite migration disable foreign_keys: %w", err)
	}
	defer func() {
		_, restoreErr := connection.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA foreign_keys = %d", foreignKeys))
		if restoreErr == nil {
			var restored int
			restoreErr = connection.GetContext(context.WithoutCancel(ctx), &restored, "PRAGMA foreign_keys")
			if restoreErr == nil && restored != foreignKeys {
				restoreErr = fmt.Errorf("restored state=%d want=%d", restored, foreignKeys)
			}
		}
		if restoreErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("sqlite migration restore foreign_keys: %w", restoreErr)
		}
	}()

	transaction, err := connection.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("sqlite migration BEGIN IMMEDIATE: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "INSERT INTO _golem_migration_lock (lock_id) VALUES (1)"); err != nil {
		return fmt.Errorf("sqlite migration acquire lock: %w", err)
	}

	lockedLedger, err := readLedger(ctx, transaction)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(ledger, lockedLedger) {
		return fmt.Errorf("sqlite migration ledger changed before lock acquisition")
	}
	actualBefore, err := provider.introspectCatalog(ctx, transaction, entry.BeforeSnapshot)
	if err != nil {
		return fmt.Errorf("sqlite migration starting introspection: %w", err)
	}
	beforeFingerprint, err := physical.PhysicalFingerprint(actualBefore)
	if err != nil || migration.Digest(beforeFingerprint.String()) != entry.BeforePhysical {
		return fmt.Errorf("sqlite migration starting physical fingerprint mismatch")
	}

	for _, guard := range plan.guards {
		if err := refuseUnmanagedStructuralDependents(ctx, transaction, entry.BeforeSnapshot, guard.tableID, guard.action); err != nil {
			return err
		}
	}
	for _, rebuild := range plan.Rebuilds {
		if err := refuseUnmanagedStructuralDependents(ctx, transaction, entry.BeforeSnapshot, rebuild.TableID, "rebuildTable"); err != nil {
			return err
		}
		var count int
		if err := transaction.GetContext(ctx, &count, "SELECT count(*) FROM (SELECT name FROM sqlite_schema UNION ALL SELECT name FROM sqlite_temp_schema) WHERE name = ?", string(rebuild.TemporaryName)); err != nil {
			return fmt.Errorf("sqlite migration temp collision check: %w", err)
		}
		if count != 0 {
			return fmt.Errorf("sqlite migration temporary object %s already exists", rebuild.TemporaryName)
		}
	}
	for stepIndex, step := range plan.steps {
		for statementIndex, statement := range step.statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("sqlite migration step %d statement %d: %w", stepIndex, statementIndex, err)
			}
		}
	}
	if err := verifyForeignKeys(ctx, transaction); err != nil {
		return err
	}
	actualAfter, err := provider.introspectCatalog(ctx, transaction, entry.AfterSnapshot)
	if err != nil {
		return fmt.Errorf("sqlite migration final introspection: %w", err)
	}
	afterFingerprint, err := physical.PhysicalFingerprint(actualAfter)
	if err != nil || migration.Digest(afterFingerprint.String()) != entry.AfterPhysical {
		return fmt.Errorf("sqlite migration final physical fingerprint mismatch")
	}
	wantSystem, _ := physical.SystemFingerprint(entry.AfterSnapshot.Provider, entry.AfterSnapshot.System)
	gotSystem, _ := physical.SystemFingerprint(actualAfter.Provider, actualAfter.System)
	if gotSystem != wantSystem {
		return fmt.Errorf("sqlite migration final system fingerprint mismatch")
	}

	if err := writeTerminalLedger(ctx, transaction, manifest, len(ledger), entry); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM _golem_migration_lock WHERE lock_id = 1"); err != nil {
		return fmt.Errorf("sqlite migration release lock: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("sqlite migration commit: %w", err)
	}
	return nil
}

func (provider *Provider) applyBootstrapMigration(ctx context.Context, connection *sqlx.Conn, manifest migration.Manifest) (resultErr error) {
	entry := manifest.Entries[0]
	semantic, err := migration.Diff(entry.BeforeSnapshot, entry.AfterSnapshot)
	if err != nil {
		return fmt.Errorf("sqlite initial migration %s semantic diff: %w", entry.ID, err)
	}
	if !semantic.Initial || semantic.Provider != ir.SQLite || !reflect.DeepEqual(semantic.Operations, entry.Operations) || !reflect.DeepEqual(semantic.Phases, entry.Phases) {
		return fmt.Errorf("sqlite initial migration %s lacks the exact reviewed bootstrap plan", entry.ID)
	}
	script, err := provider.renderInitial(entry.AfterSnapshot)
	if err != nil {
		return err
	}
	var foreignKeys int
	if err := connection.GetContext(ctx, &foreignKeys, "PRAGMA foreign_keys"); err != nil {
		return fmt.Errorf("sqlite initial migration read foreign_keys: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("sqlite initial migration disable foreign_keys: %w", err)
	}
	defer func() {
		_, restoreErr := connection.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA foreign_keys = %d", foreignKeys))
		if restoreErr == nil {
			var restored int
			restoreErr = connection.GetContext(context.WithoutCancel(ctx), &restored, "PRAGMA foreign_keys")
			if restoreErr == nil && restored != foreignKeys {
				restoreErr = fmt.Errorf("restored state=%d want=%d", restored, foreignKeys)
			}
		}
		if restoreErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("sqlite initial migration restore foreign_keys: %w", restoreErr)
		}
	}()
	transaction, err := connection.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("sqlite initial migration BEGIN IMMEDIATE: %w", err)
	}
	defer transaction.Rollback()
	var existing []schemaRow
	if err := transaction.SelectContext(ctx, &existing, "SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND type IN ('table','index','view','trigger')"); err != nil {
		return fmt.Errorf("sqlite initial migration inspect blank schema: %w", err)
	}
	if len(existing) != 0 {
		if ledgerExists, _ := sqliteObjectExists(ctx, transaction, "table", "_golem_migrations"); ledgerExists {
			ledger, ledgerErr := readLedger(ctx, transaction)
			if ledgerErr == nil && len(ledger) <= len(manifest.Entries) {
				prefix := manifest
				prefix.Entries = manifest.Entries[:len(ledger)]
				if migration.VerifyLedger(prefix, ledger) == nil {
					return fmt.Errorf("sqlite migration has no unapplied reviewed entry")
				}
			}
		}
		return fmt.Errorf("sqlite initial migration requires a truly blank managed namespace; found %s %s", existing[0].Type, existing[0].Name)
	}
	actualBefore, err := provider.introspectCatalog(ctx, transaction, entry.BeforeSnapshot)
	if err != nil {
		return fmt.Errorf("sqlite initial migration starting introspection: %w", err)
	}
	beforeFingerprint, _ := physical.PhysicalFingerprint(actualBefore)
	if migration.Digest(beforeFingerprint.String()) != entry.BeforePhysical {
		return fmt.Errorf("sqlite initial migration starting physical fingerprint mismatch")
	}
	for index, statement := range script.statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlite initial migration statement %d: %w", index, err)
		}
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO _golem_migration_lock (lock_id) VALUES (1)"); err != nil {
		return fmt.Errorf("sqlite initial migration acquire lock: %w", err)
	}
	if err := verifyForeignKeys(ctx, transaction); err != nil {
		return err
	}
	actualAfter, err := provider.introspectCatalog(ctx, transaction, entry.AfterSnapshot)
	if err != nil {
		return fmt.Errorf("sqlite initial migration final introspection: %w", err)
	}
	afterFingerprint, _ := physical.PhysicalFingerprint(actualAfter)
	if migration.Digest(afterFingerprint.String()) != entry.AfterPhysical {
		return fmt.Errorf("sqlite initial migration final physical fingerprint mismatch")
	}
	wantSystem, _ := physical.SystemFingerprint(entry.AfterSnapshot.Provider, entry.AfterSnapshot.System)
	gotSystem, _ := physical.SystemFingerprint(actualAfter.Provider, actualAfter.System)
	if gotSystem != wantSystem {
		return fmt.Errorf("sqlite initial migration final system fingerprint mismatch")
	}
	if err := writeTerminalLedger(ctx, transaction, manifest, 0, entry); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM _golem_migration_lock WHERE lock_id = 1"); err != nil {
		return fmt.Errorf("sqlite initial migration release lock: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("sqlite initial migration commit: %w", err)
	}
	return nil
}

func writeTerminalLedger(ctx context.Context, transaction *sqlx.Tx, manifest migration.Manifest, applied int, entry migration.ManifestEntry) error {
	ledgerEntry := migration.LedgerEntry{MigrationID: entry.ID, ParentChainHash: entry.ParentChainHash, ChainHash: entry.ChainHash, Files: append([]migration.FileChecksum(nil), entry.Files...), BeforePhysical: entry.BeforePhysical, AfterPhysical: entry.AfterPhysical, AppliedAt: time.Now().UTC()}
	for _, phase := range entry.Phases {
		ledgerEntry.Phases = append(ledgerEntry.Phases, migration.LedgerPhase{Ordinal: phase.Ordinal, Status: migration.PhaseApplied, AfterFingerprint: phase.AfterFingerprint})
	}
	fileChecksums, phases, err := encodeLedger(ledgerEntry)
	if err != nil {
		return fmt.Errorf("sqlite migration ledger encoding: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, "INSERT INTO _golem_migrations (migration_id,parent_chain_hash,chain_hash,file_checksums,before_physical_fingerprint,after_physical_fingerprint,phases,applied_at) VALUES (?,?,?,?,?,?,?,?)", string(ledgerEntry.MigrationID), string(ledgerEntry.ParentChainHash), string(ledgerEntry.ChainHash), fileChecksums, string(ledgerEntry.BeforePhysical), string(ledgerEntry.AfterPhysical), phases, ledgerEntry.AppliedAt.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("sqlite migration ledger write: %w", err)
	}
	finalLedger, err := readLedger(ctx, transaction)
	if err != nil {
		return err
	}
	finalManifest := manifest
	finalManifest.Entries = manifest.Entries[:applied+1]
	if err := migration.VerifyLedger(finalManifest, finalLedger); err != nil {
		return fmt.Errorf("sqlite migration final ledger: %w", err)
	}
	return nil
}

func sqliteObjectExists(ctx context.Context, database interface {
	GetContext(context.Context, any, string, ...any) error
}, kind, name string) (bool, error) {
	var count int
	if err := database.GetContext(ctx, &count, "SELECT count(*) FROM sqlite_schema WHERE type = ? AND name = ?", kind, name); err != nil {
		return false, fmt.Errorf("sqlite migration inspect system object: %w", err)
	}
	return count != 0, nil
}

func refuseUnmanagedStructuralDependents(ctx context.Context, database *sqlx.Tx, before physical.PhysicalSchema, tableID ir.ModelID, action string) error {
	table, exists := tableMap(before.Tables)[tableID]
	if !exists {
		return fmt.Errorf("sqlite migration structural target table %s is absent from starting snapshot", tableID)
	}
	for _, unmanaged := range before.Unmanaged {
		if unmanaged.Kind != "trigger" && unmanaged.Kind != "view" {
			continue
		}
		var rows []schemaRow
		if err := database.SelectContext(ctx, &rows, "SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE type = ? AND name = ?", unmanaged.Kind, string(unmanaged.Name)); err != nil {
			return fmt.Errorf("sqlite migration inspect unmanaged object %s: %w", unmanaged.Name, err)
		}
		for _, row := range rows {
			// SQLite reports a trigger's owning table structurally. Views carry
			// arbitrary SQL outside Golem's grammar, so a rebuild conservatively
			// refuses reviewed unmanaged views rather than guessing dependencies.
			if row.Type == "view" || (row.Type == "trigger" && row.Table == string(table.Name)) {
				return fmt.Errorf("sqlite migration %s table %s refuses unmanaged dependent %s %s", action, tableID, row.Type, row.Name)
			}
		}
	}
	return nil
}

func verifyForeignKeys(ctx context.Context, database interface {
	QueryxContext(context.Context, string, ...any) (*sqlx.Rows, error)
}) error {
	rows, err := database.QueryxContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("sqlite migration foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("sqlite migration foreign_key_check reported a violation")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite migration foreign_key_check: %w", err)
	}
	return nil
}
