package postgresql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

// IncrementalPlan is immutable provider-generated PostgreSQL DDL for one
// reviewed migration entry. SQL is exposed for review, never accepted as input.
type IncrementalPlan struct {
	MigrationID migration.MigrationID
	script      Script
}

func (plan IncrementalPlan) SQL() string { return plan.script.SQL() }

// PlanIncremental lowers an already reviewed semantic migration entry. It does
// not invent casts, transforms, rebuilds, manual SQL, or autocommit phases.
func (provider *Provider) PlanIncremental(entry migration.ManifestEntry) (IncrementalPlan, error) {
	return provider.planIncremental(entry)
}

// ApplyMigration verifies immutable history and applies exactly the next
// reviewed entry. It never skips or batches ledger entries.
func (provider *Provider) ApplyMigration(ctx context.Context, database *sqlx.DB, manifest migration.Manifest, files map[string][]byte) error {
	return provider.applyMigration(ctx, database, manifest, files)
}

// ReadLedger returns the exact ordered PostgreSQL migration chain.
func (*Provider) ReadLedger(ctx context.Context, database *sqlx.DB) ([]migration.LedgerEntry, error) {
	if database == nil {
		return nil, fmt.Errorf("postgresql ledger: database is nil")
	}
	exists, err := postgresqlLedgerExists(ctx, database, systemSchema())
	if err != nil {
		return nil, err
	}
	if !exists {
		return []migration.LedgerEntry{}, nil
	}
	return readPostgreSQLLedger(ctx, database, systemSchema())
}

func (provider *Provider) planIncremental(entry migration.ManifestEntry) (IncrementalPlan, error) {
	semantic, err := migration.Diff(entry.BeforeSnapshot, entry.AfterSnapshot)
	if err != nil {
		return IncrementalPlan{}, fmt.Errorf("postgresql migration %s semantic diff: %w", entry.ID, err)
	}
	if semantic.Initial {
		return IncrementalPlan{}, fmt.Errorf("postgresql migration %s is an initial bootstrap, not an incremental entry", entry.ID)
	}
	if semantic.Provider != ir.PostgreSQL || semantic.BeforeFingerprint != entry.BeforePhysical || semantic.AfterFingerprint != entry.AfterPhysical ||
		!reflect.DeepEqual(semantic.Operations, entry.Operations) || !reflect.DeepEqual(semantic.Phases, entry.Phases) {
		return IncrementalPlan{}, fmt.Errorf("postgresql migration %s operation graph differs from embedded snapshots", entry.ID)
	}
	if err := migration.ValidatePlan(semantic, entry.Approvals); err != nil {
		return IncrementalPlan{}, fmt.Errorf("postgresql migration %s invalid reviewed plan: %w", entry.ID, err)
	}
	if len(entry.Manual) != 0 {
		return IncrementalPlan{}, fmt.Errorf("postgresql migration %s manual companions are unsupported by the automatic runner", entry.ID)
	}
	before, err := physical.Normalize(entry.BeforeSnapshot)
	if err != nil {
		return IncrementalPlan{}, err
	}
	after, err := physical.Normalize(entry.AfterSnapshot)
	if err != nil {
		return IncrementalPlan{}, err
	}
	if len(before.Extensions) != 0 || len(after.Extensions) != 0 {
		return IncrementalPlan{}, fmt.Errorf("postgresql migration %s provider extensions are outside the transactional baseline", entry.ID)
	}
	for _, phase := range semantic.Phases {
		if phase.Mode != migration.Transactional {
			return IncrementalPlan{}, fmt.Errorf("postgresql migration %s phase %d is not transactional", entry.ID, phase.Ordinal)
		}
	}
	for _, operation := range semantic.Operations {
		if operation.Mode != migration.Transactional || operation.Transform != nil {
			return IncrementalPlan{}, fmt.Errorf("postgresql migration %s operation %s requires unsupported transaction or transform semantics", entry.ID, operation.ID)
		}
		switch operation.Kind {
		case migration.AlterColumnType, migration.BackfillColumn, migration.RebuildTable, migration.ValidateConstraint, migration.ManualStep, migration.CreateNamespace, migration.BootstrapSystemSchema:
			return IncrementalPlan{}, fmt.Errorf("postgresql migration %s operation %s kind %s is outside the automatic transactional baseline", entry.ID, operation.ID, operation.Kind)
		}
	}

	beforeTables := postgresqlTableMap(before.Tables)
	afterTables := postgresqlTableMap(after.Tables)
	owners, err := postgresqlOperationOwners(before, after, semantic.Operations)
	if err != nil {
		return IncrementalPlan{}, err
	}
	if err := validatePostgreSQLColumnOrder(beforeTables, afterTables, owners, semantic.Operations); err != nil {
		return IncrementalPlan{}, fmt.Errorf("postgresql migration %s: %w", entry.ID, err)
	}
	renderer := ddlRenderer{schema: after, tables: afterTables}
	plan := IncrementalPlan{MigrationID: entry.ID}
	for _, operation := range semantic.Operations {
		statements, renderErr := renderer.incrementalOperation(operation, owners, beforeTables, afterTables)
		if renderErr != nil {
			return IncrementalPlan{}, fmt.Errorf("postgresql migration %s operation %s: %w", entry.ID, operation.ID, renderErr)
		}
		plan.script.statements = append(plan.script.statements, statements...)
	}
	return plan, nil
}

func (r ddlRenderer) incrementalOperation(operation migration.Operation, owners map[migration.OperationID]ir.ModelID, beforeTables, afterTables map[ir.ModelID]physical.PhysicalTable) ([]string, error) {
	if operation.Kind == migration.RecordSchemaVersion {
		return nil, nil
	}
	if operation.Kind == migration.AddSystemObject {
		object, exists := postgresqlSystemObject(r.schema.System, ir.ObjectID(operation.ObjectID))
		if !exists || (!physical.IsOutboxSystemObjectV1(object) && !physical.IsOutboxDeliverySystemObjectV1(object) && !physical.IsUpsertGuardSystemObjectV1(object)) {
			return nil, fmt.Errorf("system object %s is not registered", operation.ObjectID)
		}
		if physical.IsUpsertGuardSystemObjectV1(object) {
			return nil, nil
		}
		if physical.IsOutboxDeliverySystemObjectV1(object) {
			statements := renderOutboxDelivery(r.schema.System.Namespace.Name, object.Name)
			return append(statements, postgresqlOutboxDeliveryBackfill(r.schema.System)), nil
		}
		return renderOutbox(r.schema.System.Namespace.Name, object.Name), nil
	}
	tableID, exists := owners[operation.ID]
	if !exists {
		return nil, fmt.Errorf("cannot resolve owning table")
	}
	before, hadBefore := beforeTables[tableID]
	after, hasAfter := afterTables[tableID]
	target := after
	if !hasAfter {
		target = before
	}
	switch operation.Kind {
	case migration.CreateTable:
		if !hasAfter {
			return nil, fmt.Errorf("create table target is absent")
		}
		created := after
		created.ForeignKeys = nil
		created.Indexes = nil
		statement, err := r.table(created)
		return oneStatement(statement, err)
	case migration.RenameTable:
		if !hadBefore || !hasAfter {
			return nil, fmt.Errorf("rename table target is absent")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s RENAME TO %s", qualified(r.schema.Namespace.Name, before.Name), quote(after.Name))}, nil
	case migration.DropTable:
		if !hadBefore {
			return nil, fmt.Errorf("drop table target is absent")
		}
		return []string{"DROP TABLE " + qualified(r.schema.Namespace.Name, before.Name)}, nil
	case migration.AddColumn:
		column, ok := postgresqlColumn(after, ir.FieldID(operation.ObjectID))
		if !ok || !hasAfter {
			return nil, fmt.Errorf("add column target is absent")
		}
		if column.Generated == nil && !column.Nullable && column.Default.Kind == physical.DefaultNone {
			return nil, fmt.Errorf("required field %s has no reviewed default or generated value", column.ID)
		}
		rendered, err := r.column(after, column)
		return oneStatement(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", qualified(r.schema.Namespace.Name, after.Name), rendered), err)
	case migration.RenameColumn:
		old, oldOK := postgresqlColumn(before, ir.FieldID(operation.ObjectID))
		current, currentOK := postgresqlColumn(after, ir.FieldID(operation.ObjectID))
		if !oldOK || !currentOK {
			return nil, fmt.Errorf("rename column target is absent")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", qualified(r.schema.Namespace.Name, after.Name), quote(old.Name), quote(current.Name))}, nil
	case migration.AlterColumnNullability:
		column, ok := postgresqlColumn(after, ir.FieldID(operation.ObjectID))
		if !ok {
			return nil, fmt.Errorf("alter nullability target is absent")
		}
		action := "SET NOT NULL"
		if column.Nullable {
			action = "DROP NOT NULL"
		}
		return []string{fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s", qualified(r.schema.Namespace.Name, after.Name), quote(column.Name), action)}, nil
	case migration.SetColumnDefault:
		old, oldOK := postgresqlColumn(before, ir.FieldID(operation.ObjectID))
		current, currentOK := postgresqlColumn(after, ir.FieldID(operation.ObjectID))
		if !oldOK || !currentOK || current.Default.Kind == physical.DefaultNone {
			return nil, fmt.Errorf("set default target is absent or has no default")
		}
		return r.setColumnDefault(after, old, current)
	case migration.DropColumnDefault:
		old, oldOK := postgresqlColumn(before, ir.FieldID(operation.ObjectID))
		current, currentOK := postgresqlColumn(after, ir.FieldID(operation.ObjectID))
		if !oldOK || !currentOK || current.Default.Kind != physical.DefaultNone {
			return nil, fmt.Errorf("drop default target is absent or retains a default")
		}
		action := "DROP DEFAULT"
		if postgresqlIdentityDefault(old.Default) {
			action = "DROP IDENTITY"
		}
		return []string{fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s", qualified(r.schema.Namespace.Name, after.Name), quote(current.Name), action)}, nil
	case migration.DropColumn:
		column, ok := postgresqlColumn(before, ir.FieldID(operation.ObjectID))
		if !ok || !hasAfter {
			return nil, fmt.Errorf("drop column target is absent")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", qualified(r.schema.Namespace.Name, after.Name), quote(column.Name))}, nil
	case migration.AddPrimaryKey, migration.AddUnique:
		key, ok := postgresqlKey(after, operation.Kind, operation.ObjectID)
		if !ok {
			return nil, fmt.Errorf("add key target is absent")
		}
		kind := "UNIQUE"
		if operation.Kind == migration.AddPrimaryKey {
			kind = "PRIMARY KEY"
		}
		rendered, err := r.key(after, key, kind)
		return oneStatement(fmt.Sprintf("ALTER TABLE %s ADD %s", qualified(r.schema.Namespace.Name, after.Name), rendered), err)
	case migration.DropPrimaryKey, migration.DropUnique:
		key, ok := postgresqlKey(before, operation.Kind, operation.ObjectID)
		if !ok {
			return nil, fmt.Errorf("drop key target is absent")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", qualified(r.schema.Namespace.Name, target.Name), quote(key.Name))}, nil
	case migration.AddForeignKey:
		foreign, ok := postgresqlForeignKey(after, ir.ForeignKeyID(operation.ObjectID))
		if !ok {
			return nil, fmt.Errorf("add foreign key target is absent")
		}
		statement, err := r.foreignKey(after, foreign)
		return oneStatement(statement, err)
	case migration.DropForeignKey:
		foreign, ok := postgresqlForeignKey(before, ir.ForeignKeyID(operation.ObjectID))
		if !ok {
			return nil, fmt.Errorf("drop foreign key target is absent")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", qualified(r.schema.Namespace.Name, target.Name), quote(foreign.Name))}, nil
	case migration.AddCheck:
		check, ok := postgresqlCheck(after, ir.CheckID(operation.ObjectID))
		if !ok {
			return nil, fmt.Errorf("add check target is absent")
		}
		expression, err := r.expression(after, check.Expression)
		return oneStatement(fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s)", qualified(r.schema.Namespace.Name, after.Name), quote(check.Name), expression), err)
	case migration.DropCheck:
		check, ok := postgresqlCheck(before, ir.CheckID(operation.ObjectID))
		if !ok {
			return nil, fmt.Errorf("drop check target is absent")
		}
		return []string{fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", qualified(r.schema.Namespace.Name, target.Name), quote(check.Name))}, nil
	case migration.CreateIndex:
		index, ok := postgresqlIndex(after, ir.IndexID(operation.ObjectID))
		if !ok {
			return nil, fmt.Errorf("create index target is absent")
		}
		statement, err := r.index(after, index)
		return oneStatement(statement, err)
	case migration.DropIndex:
		index, ok := postgresqlIndex(before, ir.IndexID(operation.ObjectID))
		if !ok {
			return nil, fmt.Errorf("drop index target is absent")
		}
		return []string{"DROP INDEX " + qualified(r.schema.Namespace.Name, index.Name)}, nil
	case migration.RenameIndex:
		old, oldOK := postgresqlIndex(before, ir.IndexID(operation.ObjectID))
		current, currentOK := postgresqlIndex(after, ir.IndexID(operation.ObjectID))
		if !oldOK || !currentOK {
			return nil, fmt.Errorf("rename index target is absent")
		}
		return []string{fmt.Sprintf("ALTER INDEX %s RENAME TO %s", qualified(r.schema.Namespace.Name, old.Name), quote(current.Name))}, nil
	default:
		return nil, fmt.Errorf("operation kind %s was not lowered", operation.Kind)
	}
}

func postgresqlOutboxDeliveryBackfill(system physical.SystemSchema) string {
	outbox := physical.OutboxSystemObjectV1().Name
	delivery := physical.OutboxDeliverySystemObjectV1().Name
	for _, object := range system.Objects {
		if physical.IsOutboxSystemObjectV1(object) {
			outbox = object.Name
		}
		if physical.IsOutboxDeliverySystemObjectV1(object) {
			delivery = object.Name
		}
	}
	columns := strings.Join([]string{quote("causation_id"), quote("status"), quote("first_recorded_at"), quote("attempt_count"), quote("available_at"), quote("updated_at")}, ",")
	return "INSERT INTO " + qualified(system.Namespace.Name, delivery) + " (" + columns + ") SELECT " + quote("causation_id") + ",'pending',MIN(" + quote("recorded_at") + "),0,MIN(" + quote("recorded_at") + "),MIN(" + quote("recorded_at") + ") FROM " + qualified(system.Namespace.Name, outbox) + " GROUP BY " + quote("causation_id") + " ON CONFLICT (" + quote("causation_id") + ") DO NOTHING"
}

func validatePostgreSQLColumnOrder(beforeTables, afterTables map[ir.ModelID]physical.PhysicalTable, owners map[migration.OperationID]ir.ModelID, operations []migration.Operation) error {
	for tableID, before := range beforeTables {
		after, exists := afterTables[tableID]
		if !exists {
			continue
		}
		retained := map[ir.FieldID]bool{}
		for _, column := range after.Columns {
			retained[column.ID] = true
		}
		actual := make([]ir.FieldID, 0, len(after.Columns))
		for _, column := range before.Columns {
			if retained[column.ID] {
				actual = append(actual, column.ID)
			}
		}
		for _, operation := range operations {
			if operation.Kind == migration.AddColumn && owners[operation.ID] == tableID {
				actual = append(actual, ir.FieldID(operation.ObjectID))
			}
		}
		if len(actual) != len(after.Columns) {
			return fmt.Errorf("table %s final column inventory cannot be represented by ALTER TABLE", tableID)
		}
		for index := range actual {
			if actual[index] != after.Columns[index].ID {
				return fmt.Errorf("table %s final column order requires a table rebuild", tableID)
			}
		}
	}
	return nil
}

func (r ddlRenderer) setColumnDefault(table physical.PhysicalTable, before, after physical.PhysicalColumn) ([]string, error) {
	target := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s ", qualified(r.schema.Namespace.Name, table.Name), quote(after.Name))
	beforeIdentity := postgresqlIdentityDefault(before.Default)
	afterIdentity := postgresqlIdentityDefault(after.Default)
	if afterIdentity {
		if beforeIdentity {
			return nil, fmt.Errorf("identity default did not semantically change")
		}
		statements := make([]string, 0, 2)
		if before.Default.Kind != physical.DefaultNone {
			statements = append(statements, target+"DROP DEFAULT")
		}
		statements = append(statements, target+"ADD GENERATED BY DEFAULT AS IDENTITY")
		return statements, nil
	}
	value, err := r.defaultValue(table, after.Default)
	if err != nil {
		return nil, err
	}
	statements := make([]string, 0, 2)
	if beforeIdentity {
		statements = append(statements, target+"DROP IDENTITY")
	}
	statements = append(statements, target+"SET DEFAULT "+value)
	return statements, nil
}

func postgresqlIdentityDefault(value physical.PhysicalDefault) bool {
	return value.Kind == physical.DefaultProvider && value.Expression != nil && value.Expression.Symbol != nil && value.Expression.Symbol.Identity == "golem.postgresql.default.identity.v1"
}

func oneStatement(statement string, err error) ([]string, error) {
	if err != nil {
		return nil, err
	}
	return []string{statement}, nil
}

func postgresqlOperationOwners(before, after physical.PhysicalSchema, operations []migration.Operation) (map[migration.OperationID]ir.ModelID, error) {
	owners := map[string]ir.ModelID{}
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
		for _, foreign := range table.ForeignKeys {
			ids = append(ids, string(foreign.ID))
		}
		for _, check := range table.Checks {
			ids = append(ids, string(check.ID))
		}
		for _, index := range table.Indexes {
			ids = append(ids, string(index.ID))
		}
		for _, id := range ids {
			if owner, exists := owners[id]; exists && owner != table.ID {
				return fmt.Errorf("postgresql migration stable object ID %s has multiple table owners", id)
			}
			owners[id] = table.ID
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
	result := map[migration.OperationID]ir.ModelID{}
	for _, operation := range operations {
		if operation.Kind == migration.RecordSchemaVersion || operation.Kind == migration.AddSystemObject {
			continue
		}
		owner, exists := owners[operation.ObjectID]
		if !exists {
			return nil, fmt.Errorf("postgresql migration object %s has no table owner", operation.ObjectID)
		}
		result[operation.ID] = owner
	}
	return result, nil
}

func postgresqlSystemObject(system physical.SystemSchema, id ir.ObjectID) (physical.SystemObject, bool) {
	for _, object := range system.Objects {
		if object.ID == id {
			return object, true
		}
	}
	return physical.SystemObject{}, false
}

func postgresqlTableMap(tables []physical.PhysicalTable) map[ir.ModelID]physical.PhysicalTable {
	result := make(map[ir.ModelID]physical.PhysicalTable, len(tables))
	for _, table := range tables {
		result[table.ID] = table
	}
	return result
}

func postgresqlColumn(table physical.PhysicalTable, id ir.FieldID) (physical.PhysicalColumn, bool) {
	for _, value := range table.Columns {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalColumn{}, false
}

func postgresqlKey(table physical.PhysicalTable, kind migration.OperationKind, objectID string) (physical.PhysicalKey, bool) {
	if kind == migration.AddPrimaryKey || kind == migration.DropPrimaryKey {
		if table.PrimaryKey != nil && string(table.PrimaryKey.ID) == objectID {
			return *table.PrimaryKey, true
		}
		return physical.PhysicalKey{}, false
	}
	for _, value := range table.Uniques {
		if string(value.ID) == objectID {
			return value, true
		}
	}
	return physical.PhysicalKey{}, false
}

func postgresqlForeignKey(table physical.PhysicalTable, id ir.ForeignKeyID) (physical.PhysicalForeignKey, bool) {
	for _, value := range table.ForeignKeys {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalForeignKey{}, false
}

func postgresqlCheck(table physical.PhysicalTable, id ir.CheckID) (physical.PhysicalCheck, bool) {
	for _, value := range table.Checks {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalCheck{}, false
}

func postgresqlIndex(table physical.PhysicalTable, id ir.IndexID) (physical.PhysicalIndex, bool) {
	for _, value := range table.Indexes {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalIndex{}, false
}

type postgresqlLedgerRow struct {
	migrationID, parentChainHash, chainHash             string
	beforePhysicalFingerprint, afterPhysicalFingerprint string
	fileChecksums, phases                               []byte
	appliedAt                                           time.Time
}

type postgresqlLedgerFile struct {
	Path   string           `json:"path"`
	SHA256 migration.Digest `json:"sha256"`
}

type postgresqlLedgerPhase struct {
	Ordinal          uint32                `json:"ordinal"`
	Status           migration.PhaseStatus `json:"status"`
	AfterFingerprint migration.Digest      `json:"afterFingerprint"`
}

func readPostgreSQLLedger(ctx context.Context, query catalogQueryer, system physical.SystemSchema) ([]migration.LedgerEntry, error) {
	ledgerName, err := postgresqlLedgerName(system)
	if err != nil {
		return nil, err
	}
	statement := fmt.Sprintf("SELECT migration_id,parent_chain_hash,chain_hash,file_checksums,before_physical_fingerprint,after_physical_fingerprint,phases,applied_at FROM %s", qualified(system.Namespace.Name, ledgerName))
	rows, err := query.QueryxContext(ctx, statement)
	if err != nil {
		return nil, fmt.Errorf("postgresql ledger read: %w", err)
	}
	defer rows.Close()
	var raw []postgresqlLedgerRow
	for rows.Next() {
		var row postgresqlLedgerRow
		if err := rows.Scan(&row.migrationID, &row.parentChainHash, &row.chainHash, &row.fileChecksums, &row.beforePhysicalFingerprint, &row.afterPhysicalFingerprint, &row.phases, &row.appliedAt); err != nil {
			return nil, err
		}
		raw = append(raw, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	byParent := map[migration.Digest]postgresqlLedgerRow{}
	for _, row := range raw {
		parent := migration.Digest(row.parentChainHash)
		if _, duplicate := byParent[parent]; duplicate {
			return nil, fmt.Errorf("postgresql ledger has a branch at parent hash %q", parent)
		}
		byParent[parent] = row
	}
	result := make([]migration.LedgerEntry, 0, len(raw))
	parent := migration.Digest("")
	for len(result) < len(raw) {
		row, exists := byParent[parent]
		if !exists {
			return nil, fmt.Errorf("postgresql ledger chain is disconnected")
		}
		entry, decodeErr := decodePostgreSQLLedgerRow(row)
		if decodeErr != nil {
			return nil, decodeErr
		}
		result = append(result, entry)
		delete(byParent, parent)
		parent = entry.ChainHash
	}
	return result, nil
}

func decodePostgreSQLLedgerRow(row postgresqlLedgerRow) (migration.LedgerEntry, error) {
	var files []postgresqlLedgerFile
	if err := decodePostgreSQLLedgerJSON(row.fileChecksums, &files); err != nil {
		return migration.LedgerEntry{}, fmt.Errorf("postgresql ledger migration %s file checksums: %w", row.migrationID, err)
	}
	var phases []postgresqlLedgerPhase
	if err := decodePostgreSQLLedgerJSON(row.phases, &phases); err != nil {
		return migration.LedgerEntry{}, fmt.Errorf("postgresql ledger migration %s phases: %w", row.migrationID, err)
	}
	entry := migration.LedgerEntry{MigrationID: migration.MigrationID(row.migrationID), ParentChainHash: migration.Digest(row.parentChainHash), ChainHash: migration.Digest(row.chainHash), BeforePhysical: migration.Digest(row.beforePhysicalFingerprint), AfterPhysical: migration.Digest(row.afterPhysicalFingerprint), AppliedAt: row.appliedAt.UTC()}
	for _, file := range files {
		entry.Files = append(entry.Files, migration.FileChecksum{Path: file.Path, SHA256: file.SHA256})
	}
	for _, phase := range phases {
		entry.Phases = append(entry.Phases, migration.LedgerPhase{Ordinal: phase.Ordinal, Status: phase.Status, AfterFingerprint: phase.AfterFingerprint})
	}
	return entry, nil
}

func decodePostgreSQLLedgerJSON(value []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func encodePostgreSQLLedger(entry migration.LedgerEntry) ([]byte, []byte, error) {
	files := make([]postgresqlLedgerFile, len(entry.Files))
	for index, file := range entry.Files {
		files[index] = postgresqlLedgerFile{Path: file.Path, SHA256: file.SHA256}
	}
	phases := make([]postgresqlLedgerPhase, len(entry.Phases))
	for index, phase := range entry.Phases {
		phases[index] = postgresqlLedgerPhase{Ordinal: phase.Ordinal, Status: phase.Status, AfterFingerprint: phase.AfterFingerprint}
	}
	fileBytes, err := json.Marshal(files)
	if err != nil {
		return nil, nil, err
	}
	phaseBytes, err := json.Marshal(phases)
	if err != nil {
		return nil, nil, err
	}
	return fileBytes, phaseBytes, nil
}

func (provider *Provider) applyMigration(ctx context.Context, database *sqlx.DB, manifest migration.Manifest, files map[string][]byte) (err error) {
	if database == nil {
		return fmt.Errorf("postgresql migration: database is nil")
	}
	if err := migration.VerifyManifest(manifest, files); err != nil {
		return fmt.Errorf("postgresql migration manifest: %w", err)
	}
	if !samePostgreSQLProviderManifest(manifest.Provider, provider.Manifest()) || manifest.Provider.Provider != ir.PostgreSQL {
		return fmt.Errorf("postgresql migration manifest provider does not match the active provider")
	}
	if len(manifest.Entries) == 0 {
		return fmt.Errorf("postgresql migration has no reviewed entry")
	}
	if _, err := probeCapabilities(ctx, database); err != nil {
		return err
	}
	lockSchema, err := physical.Normalize(manifest.Entries[0].AfterSnapshot)
	if err != nil {
		return err
	}
	transaction, err := database.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgresql migration begin: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_xact_lock($1)`, advisoryKey(lockSchema)); err != nil {
		return fmt.Errorf("postgresql migration advisory lock: %w", err)
	}
	ledgerExists, err := postgresqlLedgerExists(ctx, transaction, systemSchema())
	if err != nil {
		return err
	}
	if !ledgerExists {
		return provider.applyBootstrapEntry(ctx, transaction, manifest, files)
	}
	ledger, err := readPostgreSQLLedger(ctx, transaction, systemSchema())
	if err != nil {
		return err
	}
	if len(ledger) > len(manifest.Entries) {
		return fmt.Errorf("postgresql migration ledger is ahead of reviewed manifest")
	}
	prefix := manifest
	prefix.Entries = manifest.Entries[:len(ledger)]
	if err := migration.VerifyLedger(prefix, ledger); err != nil {
		return fmt.Errorf("postgresql migration starting ledger: %w", err)
	}
	if len(ledger) == len(manifest.Entries) {
		return fmt.Errorf("postgresql migration has no unapplied reviewed entry")
	}
	entry := manifest.Entries[len(ledger)]
	plan, err := provider.planIncremental(entry)
	if err != nil {
		return err
	}
	if err := verifyPostgreSQLMigrationArtifact(entry, files, plan.SQL()); err != nil {
		return err
	}
	actualBefore, err := provider.introspectQuery(ctx, transaction, entry.BeforeSnapshot)
	if err != nil {
		return fmt.Errorf("postgresql migration starting introspection: %w", err)
	}
	if err := compareFingerprints(entry.BeforeSnapshot, actualBefore); err != nil {
		return fmt.Errorf("postgresql migration starting fingerprint: %w", err)
	}
	for index, statement := range plan.script.statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("postgresql migration statement %d: %w", index, err)
		}
	}
	actualAfter, err := provider.introspectQuery(ctx, transaction, entry.AfterSnapshot)
	if err != nil {
		return fmt.Errorf("postgresql migration final introspection: %w", err)
	}
	if err := compareFingerprints(entry.AfterSnapshot, actualAfter); err != nil {
		return fmt.Errorf("postgresql migration final fingerprint: %w", err)
	}
	if err := writePostgreSQLLedger(ctx, transaction, manifest, len(ledger), entry); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("postgresql migration commit: %w", err)
	}
	return nil
}

func samePostgreSQLProviderManifest(left, right physical.ProviderManifest) bool {
	a, b := left, right
	a.Capabilities = append([]physical.CapabilityFact(nil), left.Capabilities...)
	b.Capabilities = append([]physical.CapabilityFact(nil), right.Capabilities...)
	less := func(values []physical.CapabilityFact) func(int, int) bool {
		return func(i, j int) bool {
			if values[i].ID != values[j].ID {
				return values[i].ID < values[j].ID
			}
			if values[i].Version != values[j].Version {
				return values[i].Version < values[j].Version
			}
			return values[i].Verification < values[j].Verification
		}
	}
	sort.Slice(a.Capabilities, less(a.Capabilities))
	sort.Slice(b.Capabilities, less(b.Capabilities))
	return reflect.DeepEqual(a, b)
}

func (provider *Provider) applyBootstrapEntry(ctx context.Context, transaction *sqlx.Tx, manifest migration.Manifest, files map[string][]byte) error {
	entry := manifest.Entries[0]
	semantic, err := migration.Diff(entry.BeforeSnapshot, entry.AfterSnapshot)
	if err != nil {
		return fmt.Errorf("postgresql initial migration %s semantic diff: %w", entry.ID, err)
	}
	if !semantic.Initial || semantic.Provider != ir.PostgreSQL || !reflect.DeepEqual(semantic.Operations, entry.Operations) || !reflect.DeepEqual(semantic.Phases, entry.Phases) {
		return fmt.Errorf("postgresql initial migration %s lacks the exact reviewed bootstrap plan", entry.ID)
	}
	if err := migration.ValidatePlan(semantic, entry.Approvals); err != nil {
		return fmt.Errorf("postgresql initial migration %s invalid reviewed plan: %w", entry.ID, err)
	}
	if len(entry.Manual) != 0 || len(entry.AfterSnapshot.Extensions) != 0 {
		return fmt.Errorf("postgresql initial migration %s uses unsupported manual or extension semantics", entry.ID)
	}
	for _, operation := range semantic.Operations {
		if operation.Mode != migration.Transactional || operation.Transform != nil || operation.Kind == migration.ManualStep || operation.Kind == migration.BackfillColumn || operation.Kind == migration.RebuildTable || operation.Kind == migration.ValidateConstraint || operation.Kind == migration.AlterColumnType {
			return fmt.Errorf("postgresql initial migration %s operation %s is outside the automatic transactional baseline", entry.ID, operation.ID)
		}
	}
	after, err := physical.Normalize(entry.AfterSnapshot)
	if err != nil {
		return err
	}
	script, err := provider.renderInitial(after)
	if err != nil {
		return err
	}
	if err := verifyPostgreSQLMigrationArtifact(entry, files, script.SQL()); err != nil {
		return err
	}
	if err := requireBlankNamespace(ctx, transaction, after.Namespace.Name, nil); err != nil {
		return err
	}
	if err := requireBlankNamespace(ctx, transaction, after.System.Namespace.Name, nil); err != nil {
		return err
	}
	actualBefore, err := provider.introspectQuery(ctx, transaction, entry.BeforeSnapshot)
	if err != nil {
		return fmt.Errorf("postgresql initial migration starting introspection: %w", err)
	}
	if err := compareFingerprints(entry.BeforeSnapshot, actualBefore); err != nil {
		return fmt.Errorf("postgresql initial migration starting fingerprint: %w", err)
	}
	for index, statement := range script.statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("postgresql initial migration statement %d: %w", index, err)
		}
	}
	actualAfter, err := provider.introspectQuery(ctx, transaction, entry.AfterSnapshot)
	if err != nil {
		return fmt.Errorf("postgresql initial migration final introspection: %w", err)
	}
	if err := compareFingerprints(entry.AfterSnapshot, actualAfter); err != nil {
		return fmt.Errorf("postgresql initial migration final fingerprint: %w", err)
	}
	if err := writePostgreSQLLedger(ctx, transaction, manifest, 0, entry); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("postgresql initial migration commit: %w", err)
	}
	return nil
}

func verifyPostgreSQLMigrationArtifact(entry migration.ManifestEntry, files map[string][]byte, expectedSQL string) error {
	var sqlFile *migration.FileChecksum
	for index := range entry.Files {
		if !strings.HasSuffix(entry.Files[index].Path, ".sql") {
			continue
		}
		if sqlFile != nil {
			return fmt.Errorf("postgresql migration %s binds multiple SQL artifacts", entry.ID)
		}
		sqlFile = &entry.Files[index]
	}
	if sqlFile == nil {
		return fmt.Errorf("postgresql migration %s has no manifest-bound SQL artifact", entry.ID)
	}
	content, exists := files[sqlFile.Path]
	if !exists || !bytes.Equal(content, []byte(expectedSQL)) {
		return fmt.Errorf("postgresql migration %s SQL artifact differs from deterministic typed rendering", entry.ID)
	}
	return nil
}

func postgresqlLedgerExists(ctx context.Context, query rowQueryer, system physical.SystemSchema) (bool, error) {
	name, err := postgresqlLedgerName(system)
	if err != nil {
		return false, err
	}
	var count int
	if err := query.QueryRowxContext(ctx, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind='r'`, string(system.Namespace.Name), string(name)).Scan(&count); err != nil {
		return false, fmt.Errorf("postgresql migration inspect ledger: %w", err)
	}
	return count == 1, nil
}

func postgresqlLedgerName(system physical.SystemSchema) (physical.PhysicalName, error) {
	for _, object := range system.Objects {
		if object.Kind == physical.SystemMigrationLedger && object.Version == 1 {
			return object.Name, nil
		}
	}
	return "", fmt.Errorf("postgresql migration ledger v1 is absent from the system schema")
}

func writePostgreSQLLedger(ctx context.Context, transaction *sqlx.Tx, manifest migration.Manifest, applied int, entry migration.ManifestEntry) error {
	record := migration.LedgerEntry{MigrationID: entry.ID, ParentChainHash: entry.ParentChainHash, ChainHash: entry.ChainHash, Files: append([]migration.FileChecksum(nil), entry.Files...), BeforePhysical: entry.BeforePhysical, AfterPhysical: entry.AfterPhysical, AppliedAt: time.Now().UTC().Truncate(time.Microsecond)}
	for _, phase := range entry.Phases {
		record.Phases = append(record.Phases, migration.LedgerPhase{Ordinal: phase.Ordinal, Status: migration.PhaseApplied, AfterFingerprint: phase.AfterFingerprint})
	}
	files, phases, err := encodePostgreSQLLedger(record)
	if err != nil {
		return fmt.Errorf("postgresql migration ledger encoding: %w", err)
	}
	ledgerName, err := postgresqlLedgerName(entry.AfterSnapshot.System)
	if err != nil {
		return err
	}
	statement := fmt.Sprintf("INSERT INTO %s (migration_id,parent_chain_hash,chain_hash,file_checksums,before_physical_fingerprint,after_physical_fingerprint,phases,applied_at) VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7::jsonb,$8)", qualified(entry.AfterSnapshot.System.Namespace.Name, ledgerName))
	if _, err := transaction.ExecContext(ctx, statement, string(record.MigrationID), string(record.ParentChainHash), string(record.ChainHash), files, string(record.BeforePhysical), string(record.AfterPhysical), phases, record.AppliedAt); err != nil {
		return fmt.Errorf("postgresql migration ledger write: %w", err)
	}
	finalLedger, err := readPostgreSQLLedger(ctx, transaction, entry.AfterSnapshot.System)
	if err != nil {
		return err
	}
	finalManifest := manifest
	finalManifest.Entries = manifest.Entries[:applied+1]
	if err := migration.VerifyLedger(finalManifest, finalLedger); err != nil {
		return fmt.Errorf("postgresql migration final ledger: %w", err)
	}
	return nil
}
