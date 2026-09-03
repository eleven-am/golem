package postgresql

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/scalar"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"github.com/jmoiron/sqlx"
)

type catalogQueryer interface {
	QueryxContext(context.Context, string, ...any) (*sqlx.Rows, error)
	QueryRowxContext(context.Context, string, ...any) *sqlx.Row
}

// reviewedSnapshot is intentionally constructible only from a sealed migration
// entry inside this package. It prevents active provider entry points from
// accepting an unverified historical schema as authority.
type reviewedSnapshot struct{ schema physical.PhysicalSchema }

func (provider *Provider) introspect(ctx context.Context, database *sqlx.DB, expected physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	normalized, err := physical.Normalize(expected)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	if database == nil {
		return physical.PhysicalSchema{}, fmt.Errorf("postgresql introspect: nil database")
	}
	return provider.introspectQuery(ctx, database, normalized)
}

func (provider *Provider) introspectQuery(ctx context.Context, query catalogQueryer, expected physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	expectedNormalized, err := physical.Normalize(expected)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	return provider.introspectNormalizedQuery(ctx, query, expectedNormalized)
}

func (provider *Provider) introspectReviewedQuery(ctx context.Context, query catalogQueryer, reviewed reviewedSnapshot) (physical.PhysicalSchema, error) {
	expectedNormalized, err := physical.NormalizeHistorical(reviewed.schema)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	return provider.introspectNormalizedQuery(ctx, query, expectedNormalized)
}

func (provider *Provider) introspectNormalizedQuery(ctx context.Context, query catalogQueryer, expectedNormalized physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	report, err := probeCapabilities(ctx, query)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	if report.Version.Major < 15 || !report.JSONB || !report.GeneratedColumns || !report.AdvisoryLocks || !report.BinaryText || !report.ASCIIInsensitive || !report.ExactJSON || !report.ScalarListJSON || !report.RelationCorrelation {
		return physical.PhysicalSchema{}, fmt.Errorf("postgresql capability verification failed: version=%d.%d jsonb=%t generated=%t advisory=%t binary=%t ascii=%t exactJSON=%t scalarListJSON=%t relation=%t", report.Version.Major, report.Version.Minor, report.JSONB, report.GeneratedColumns, report.AdvisoryLocks, report.BinaryText, report.ASCIIInsensitive, report.ExactJSON, report.ScalarListJSON, report.RelationCorrelation)
	}
	activeProbe := expectedNormalized
	activeProbe.Provider = provider.Manifest()
	activeNormalized, activeErr := physical.NormalizeHistorical(activeProbe)
	if activeErr != nil || !physical.CompatibleProviderHistory(expectedNormalized.Provider, activeNormalized.Provider) {
		return physical.PhysicalSchema{}, fmt.Errorf("postgresql active provider is incompatible with the reviewed provider identity")
	}
	// Catalog facts prove the active driver/capabilities above. Fingerprint
	// reconstruction must retain the reviewed provider identity rather than
	// silently stamping a newer runtime manifest into immutable history.
	actual := physical.PhysicalSchema{Version: expectedNormalized.Version, CanonicalVersion: expectedNormalized.CanonicalVersion, Provider: expectedNormalized.Provider, Namespace: expectedNormalized.Namespace, Unmanaged: append([]physical.UnmanagedObject(nil), expectedNormalized.Unmanaged...)}
	allowed := map[string]bool{}
	for _, object := range expectedNormalized.Unmanaged {
		allowed[object.Kind+"\x00"+string(object.Name)] = true
	}
	expectedTables := map[string]physical.PhysicalTable{}
	for _, table := range expectedNormalized.Tables {
		expectedTables[string(table.Name)] = table
	}
	semanticTables := map[string]bool{}
	if expectedNormalized.Version != 1 || expectedNormalized.CanonicalVersion != 1 {
		for _, extension := range expectedNormalized.Extensions {
			descriptor, decodeErr := semanticstorage.Decode(extension)
			if decodeErr != nil {
				return physical.PhysicalSchema{}, decodeErr
			}
			semanticTables[string(descriptor.Storage)+"_state"] = true
			semanticTables[string(descriptor.Storage)+"_vec"] = true
		}
	}
	tableByOID := map[int64]*physical.PhysicalTable{}
	tableOIDByName := map[string]int64{}
	rows, err := query.QueryxContext(ctx, `SELECT c.oid::bigint, c.relname,c.relkind::text,c.relpersistence::text,c.relrowsecurity,c.relforcerowsecurity
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname=$1 AND c.relkind IN ('r','p') ORDER BY c.relname`, string(actual.Namespace.Name))
	if err != nil {
		return physical.PhysicalSchema{}, fmt.Errorf("postgresql catalog tables: %w", err)
	}
	for rows.Next() {
		var oid int64
		var name, relationKind, persistence string
		var rowSecurity, forceRowSecurity bool
		if err = rows.Scan(&oid, &name, &relationKind, &persistence, &rowSecurity, &forceRowSecurity); err != nil {
			rows.Close()
			return physical.PhysicalSchema{}, err
		}
		if allowed["table\x00"+name] {
			continue
		}
		if semanticTables[name] {
			if err := validateCatalogTableFacts(relationKind, persistence); err != nil {
				return physical.PhysicalSchema{}, fmt.Errorf("postgresql semantic table %q: %w", name, err)
			}
			if err := validateCatalogBehaviorFlags(rowSecurity, forceRowSecurity); err != nil {
				return physical.PhysicalSchema{}, fmt.Errorf("postgresql semantic table %q: %w", name, err)
			}
			continue
		}
		if err := validateCatalogTableFacts(relationKind, persistence); err != nil {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql managed table %q: %w", name, err)
		}
		if err := validateCatalogBehaviorFlags(rowSecurity, forceRowSecurity); err != nil {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql managed table %q: %w", name, err)
		}
		id := ir.ModelID(stableID("catalog-table", string(actual.Namespace.Name), name))
		if value, ok := expectedTables[name]; ok {
			id = value.ID
		}
		actual.Tables = append(actual.Tables, physical.PhysicalTable{ID: id, Name: physical.PhysicalName(name)})
		tableByOID[oid] = &actual.Tables[len(actual.Tables)-1]
		tableOIDByName[name] = oid
	}
	if err = rows.Close(); err != nil {
		return physical.PhysicalSchema{}, err
	}
	otherRows, err := query.QueryxContext(ctx, `SELECT c.relkind::text,c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN('v','m','S') AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_depend d WHERE d.objid=c.oid AND d.classid='pg_catalog.pg_class'::pg_catalog.regclass AND d.refclassid='pg_catalog.pg_class'::pg_catalog.regclass AND d.deptype IN('a','i')) ORDER BY c.relkind,c.relname`, string(actual.Namespace.Name))
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	for otherRows.Next() {
		var kind, name string
		if err := otherRows.Scan(&kind, &name); err != nil {
			otherRows.Close()
			return physical.PhysicalSchema{}, err
		}
		semantic := map[string]string{"v": "view", "m": "materialized_view", "S": "sequence"}[kind]
		if !allowed[semantic+"\x00"+name] {
			otherRows.Close()
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql managed namespace drift: unexpected %s %q", semantic, name)
		}
	}
	if err := otherRows.Close(); err != nil {
		return physical.PhysicalSchema{}, err
	}
	// Appending can relocate the backing array; rebuild pointers after collection.
	for oid := range tableByOID {
		delete(tableByOID, oid)
	}
	for index := range actual.Tables {
		tableByOID[tableOIDByName[string(actual.Tables[index].Name)]] = &actual.Tables[index]
	}
	if err := rejectUnexpectedBehaviorObjects(ctx, query, actual.Namespace.Name, tableByOID, allowed); err != nil {
		return physical.PhysicalSchema{}, err
	}
	columnsByAttnum := map[int64]map[int]physical.PhysicalColumn{}
	visibleOrdinals := map[int64]uint32{}
	actualOwnerColumns := map[int64][]string{}
	type pendingGenerated struct {
		tableOID   int64
		attnum     int
		expression string
	}
	var pendingGeneratedExpressions []pendingGenerated
	rows, err = query.QueryxContext(ctx, `SELECT c.oid::bigint,a.attname,a.attnum::integer,
pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,
COALESCE(pg_catalog.pg_get_expr(d.adbin,d.adrelid),''),a.attidentity::text,a.attgenerated::text,
CASE WHEN a.attcollation=0 THEN true ELSE EXISTS(
  SELECT 1 FROM pg_catalog.pg_collation default_collation
  JOIN pg_catalog.pg_namespace default_namespace ON default_namespace.oid=default_collation.collnamespace
  WHERE default_collation.oid=a.attcollation
    AND default_namespace.nspname='pg_catalog'
    AND default_collation.collname='default'
) END
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
WHERE n.nspname=$1 AND c.relkind IN ('r','p') ORDER BY c.relname,a.attnum`, string(actual.Namespace.Name))
	if err != nil {
		return physical.PhysicalSchema{}, fmt.Errorf("postgresql catalog columns: %w", err)
	}
	for rows.Next() {
		var oid int64
		var name, typeText, defaultText, identity, generated string
		var attnum int
		var notNull, defaultCollation bool
		if err = rows.Scan(&oid, &name, &attnum, &typeText, &notNull, &defaultText, &identity, &generated, &defaultCollation); err != nil {
			rows.Close()
			return physical.PhysicalSchema{}, err
		}
		table := tableByOID[oid]
		if table == nil {
			continue
		}
		storage, parseErr := parseCatalogStorage(typeText)
		if parseErr != nil {
			rows.Close()
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql catalog column %s.%s: %w", table.Name, name, parseErr)
		}
		fieldID := ir.FieldID(stableID("catalog-column", string(table.ID), name))
		ordinal := visibleOrdinals[oid]
		if wanted, ok := expectedTables[string(table.Name)]; ok {
			for _, column := range wanted.Columns {
				if string(column.Name) == name {
					fieldID = column.ID
					ordinal = column.Ordinal
					break
				}
			}
		}
		column := physical.PhysicalColumn{ID: fieldID, Name: physical.PhysicalName(name), Ordinal: ordinal, Storage: storage, Nullable: !notNull, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}
		visibleOrdinals[oid]++
		if generated == "" {
			actualOwnerColumns[oid] = append(actualOwnerColumns[oid], name)
		}
		if !defaultCollation {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql catalog column %s.%s: non-default collation is not baseline", table.Name, name)
		}
		if identity != "" {
			if err := validateIdentityMode(identity); err != nil {
				return physical.PhysicalSchema{}, fmt.Errorf("postgresql column %s.%s: %w", table.Name, name, err)
			}
			column.Default = physical.PhysicalDefault{Kind: physical.DefaultProvider, Expression: &physical.Expression{Kind: physical.ExpressionFunction, Type: storage, Symbol: postgresSymbol("golem.postgresql.default.identity.v1", ir.SchemaSymbolFunction)}}
		} else if defaultText != "" && generated == "" {
			literal, parseErr := parseCatalogLiteral(defaultText, storage)
			if parseErr != nil {
				return physical.PhysicalSchema{}, fmt.Errorf("postgresql default %s.%s: %w", table.Name, name, parseErr)
			}
			column.Default = physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &literal}
		}
		table.Columns = append(table.Columns, column)
		if columnsByAttnum[oid] == nil {
			columnsByAttnum[oid] = map[int]physical.PhysicalColumn{}
		}
		columnsByAttnum[oid][attnum] = column
		if generated != "" {
			if err := validateGeneratedMode(generated); err != nil {
				return physical.PhysicalSchema{}, fmt.Errorf("postgresql column %s.%s: %w", table.Name, name, err)
			}
			table.Columns[len(table.Columns)-1].Default = physical.PhysicalDefault{Kind: physical.DefaultNone}
			pendingGeneratedExpressions = append(pendingGeneratedExpressions, pendingGenerated{tableOID: oid, attnum: attnum, expression: defaultText})
			owner := physical.ObjectRef{Kind: ir.ObjectField, ModelID: table.ID, FieldID: fieldID}
			table.Columns[len(table.Columns)-1].RequiredCapabilities = append(table.Columns[len(table.Columns)-1].RequiredCapabilities, physical.CapabilityRequirement{Capability: CapabilityGeneratedColumns, Owner: owner})
		}
		if storage.Kind == physical.StoragePostgreSQLJSONB {
			owner := physical.ObjectRef{Kind: ir.ObjectField, ModelID: table.ID, FieldID: fieldID}
			table.Columns[len(table.Columns)-1].RequiredCapabilities = append(table.Columns[len(table.Columns)-1].RequiredCapabilities, physical.CapabilityRequirement{Capability: CapabilityJSONB, Owner: owner})
		}
	}
	if err = rows.Close(); err != nil {
		return physical.PhysicalSchema{}, err
	}
	for tableName, wanted := range expectedTables {
		oid, exists := tableOIDByName[tableName]
		if !exists {
			continue
		}
		if expectedNormalized.Version == physical.SchemaFormatVersion && expectedNormalized.CanonicalVersion == physical.CanonicalFormatVersion {
			expectedOwners := make([]string, 0, len(wanted.Columns))
			for _, column := range wanted.Columns {
				if column.Generated == nil {
					expectedOwners = append(expectedOwners, string(column.Name))
				}
			}
			if !reflect.DeepEqual(actualOwnerColumns[oid], expectedOwners) {
				return physical.PhysicalSchema{}, fmt.Errorf("postgresql catalog table %s: non-generated column order differs from the reviewed schema", tableName)
			}
		} else {
			expectedVisible := make([]string, 0, len(wanted.Columns))
			for _, column := range wanted.Columns {
				expectedVisible = append(expectedVisible, string(column.Name))
			}
			actualVisible := make([]string, 0, len(tableByOID[oid].Columns))
			for _, column := range tableByOID[oid].Columns {
				actualVisible = append(actualVisible, string(column.Name))
			}
			if !reflect.DeepEqual(actualVisible, expectedVisible) {
				return physical.PhysicalSchema{}, fmt.Errorf("postgresql historical catalog table %s: column order differs from the reviewed schema", tableName)
			}
		}
	}
	for _, pending := range pendingGeneratedExpressions {
		table := tableByOID[pending.tableOID]
		column := columnsByAttnum[pending.tableOID][pending.attnum]
		var reviewed *physical.Expression
		for _, expectedColumn := range expectedTables[string(table.Name)].Columns {
			if expectedColumn.ID == column.ID && expectedColumn.Generated != nil {
				reviewed = &expectedColumn.Generated.Expression
				break
			}
		}
		if reviewed == nil {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql generated expression has no reviewed owner")
		}
		expression, parseErr := parseCatalogGeneratedExpression(pending.expression, *table, *reviewed)
		if parseErr != nil {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql generated expression %s.%s: %w", table.Name, column.Name, parseErr)
		}
		for index := range table.Columns {
			if table.Columns[index].ID == column.ID {
				table.Columns[index].Generated = &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: expression}
				break
			}
		}
	}
	if err = introspectConstraints(ctx, query, actual.Namespace.Name, expectedTables, tableByOID, columnsByAttnum); err != nil {
		return physical.PhysicalSchema{}, err
	}
	if err = introspectIndexes(ctx, query, actual.Namespace.Name, expectedTables, tableByOID, columnsByAttnum); err != nil {
		return physical.PhysicalSchema{}, err
	}
	actual.System, err = introspectSystem(ctx, query, expectedNormalized.System, allowed)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	if expectedNormalized.Version != 1 || expectedNormalized.CanonicalVersion != 1 {
		if err := introspectSemanticExtensions(ctx, query, expectedNormalized); err != nil {
			return physical.PhysicalSchema{}, err
		}
	}
	actual.Extensions = append([]physical.Extension(nil), expectedNormalized.Extensions...)
	actualNormalized, err := physical.NormalizeHistorical(actual)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	return reconcileOptimisticConcurrency(expectedNormalized, actualNormalized)
}

// PostgreSQL has no catalog object for Golem's optimistic-concurrency
// declaration. Reconstruct that logical fact only after the catalog-backed
// table and its exact owner column have independently round-tripped. This keeps
// expected metadata from masking physical drift.
func reconcileOptimisticConcurrency(expected, actual physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	actualTables := make(map[ir.ModelID]int, len(actual.Tables))
	for index := range actual.Tables {
		actualTables[actual.Tables[index].ID] = index
	}
	for _, expectedTable := range expected.Tables {
		if expectedTable.OptimisticConcurrency == nil {
			continue
		}
		actualIndex, exists := actualTables[expectedTable.ID]
		if !exists || actual.Tables[actualIndex].Name != expectedTable.Name {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql optimistic concurrency table %s does not exactly match the reviewed table identity", expectedTable.ID)
		}
		actualTable := actual.Tables[actualIndex]
		field := *expectedTable.OptimisticConcurrency
		var expectedColumn, actualColumn *physical.PhysicalColumn
		for index := range expectedTable.Columns {
			if expectedTable.Columns[index].ID == field {
				expectedColumn = &expectedTable.Columns[index]
				break
			}
		}
		for index := range actualTable.Columns {
			if actualTable.Columns[index].ID == field {
				actualColumn = &actualTable.Columns[index]
				break
			}
		}
		if expectedColumn == nil || actualColumn == nil || !reflect.DeepEqual(*expectedColumn, *actualColumn) {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql optimistic concurrency column %s.%s does not exactly match the reviewed catalog-backed column", expectedTable.ID, field)
		}
		expectedWithoutDeclaration := expectedTable
		expectedWithoutDeclaration.OptimisticConcurrency = nil
		actualWithoutDeclaration := actualTable
		actualWithoutDeclaration.OptimisticConcurrency = nil
		if !reflect.DeepEqual(expectedWithoutDeclaration, actualWithoutDeclaration) {
			return physical.PhysicalSchema{}, fmt.Errorf("postgresql optimistic concurrency table %s does not exactly match the reviewed catalog-backed table", expectedTable.ID)
		}
		value := field
		actual.Tables[actualIndex].OptimisticConcurrency = &value
	}
	return physical.NormalizeHistorical(actual)
}

func introspectSemanticExtensions(ctx context.Context, query catalogQueryer, expected physical.PhysicalSchema) error {
	if len(expected.Extensions) == 0 {
		return nil
	}
	var vectorVersion string
	if err := query.QueryRowxContext(ctx, `SELECT extversion FROM pg_catalog.pg_extension WHERE extname='vector'`).Scan(&vectorVersion); err != nil || !supportedPGVectorVersion(vectorVersion) {
		return fmt.Errorf("postgresql semantic introspect: pgvector >=0.8.0 is required")
	}
	for _, extension := range expected.Extensions {
		descriptor, err := semanticstorage.Decode(extension)
		if err != nil {
			return err
		}
		state := string(descriptor.Storage) + "_state"
		vectors := string(descriptor.Storage) + "_vec"
		var stateColumns, vectorColumns string
		const columnsSQL = `SELECT COALESCE(string_agg(a.attname||':'||pg_catalog.format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull::text,',' ORDER BY a.attnum),'') FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped`
		if err := query.QueryRowxContext(ctx, columnsSQL, string(expected.Namespace.Name), state).Scan(&stateColumns); err != nil {
			return err
		}
		if err := query.QueryRowxContext(ctx, columnsSQL, string(expected.Namespace.Name), vectors).Scan(&vectorColumns); err != nil {
			return err
		}
		wantState := "record_key:text:true,source_hash:bytea:true,space_fingerprint:text:true,status:text:true,attempt_count:integer:true,error_code:text:false,updated_at:bigint:true"
		if len(descriptor.Identity) == 0 {
			return fmt.Errorf("postgresql semantic introspect: identity projection is absent extension=%s", extension.ID)
		}
		for _, column := range descriptor.Identity {
			storage, storageErr := renderStorage(column.Storage)
			if storageErr != nil {
				return fmt.Errorf("postgresql semantic introspect: %w", storageErr)
			}
			wantState += "," + string(column.Name) + ":" + storage + ":" + strconv.FormatBool(column.NotNull)
		}
		wantVectors := fmt.Sprintf("record_key:text:true,embedding:vector(%d):true", descriptor.Dimensions)
		if stateColumns != wantState || vectorColumns != wantVectors {
			return fmt.Errorf("postgresql semantic introspect: column drift extension=%s", extension.ID)
		}
	}
	return nil
}

func supportedPGVectorVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	numbers := make([]int, len(parts))
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return false
		}
		numbers[index] = value
	}
	return numbers[0] > 0 || numbers[0] == 0 && numbers[1] >= 8
}

func introspectConstraints(ctx context.Context, q catalogQueryer, namespace physical.PhysicalName, expected map[string]physical.PhysicalTable, tables map[int64]*physical.PhysicalTable, columns map[int64]map[int]physical.PhysicalColumn) error {
	rows, err := q.QueryxContext(ctx, `SELECT con.conrelid::bigint,con.conname,con.contype::text,COALESCE(con.conkey::text,''),COALESCE(con.confrelid::bigint,0),COALESCE(con.confkey::text,''),con.confupdtype::text,con.confdeltype::text,con.confmatchtype::text,con.condeferrable,con.condeferred,con.convalidated,con.connoinherit,COALESCE(pi.indnullsnotdistinct,false),COALESCE(pg_catalog.pg_get_expr(con.conbin,con.conrelid),'') FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_class c ON c.oid=con.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_index pi ON pi.indexrelid=con.conindid WHERE n.nspname=$1 ORDER BY c.relname,con.conname`, string(namespace))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var oid, remoteOID int64
		var name, kind, localText, remoteText, updateCode, deleteCode, matchCode, expression string
		var deferrable, deferred, validated, noInherit, nullsNotDistinct bool
		if err := rows.Scan(&oid, &name, &kind, &localText, &remoteOID, &remoteText, &updateCode, &deleteCode, &matchCode, &deferrable, &deferred, &validated, &noInherit, &nullsNotDistinct, &expression); err != nil {
			return err
		}
		table := tables[oid]
		if table == nil {
			continue
		}
		wanted := expected[string(table.Name)]
		if err := validateCatalogConstraintFacts(kind, matchCode, deferrable, deferred, validated, noInherit, nullsNotDistinct); err != nil {
			return fmt.Errorf("postgresql constraint %s: %w", name, err)
		}
		switch kind {
		case "p", "u":
			ids, err := fieldIDs(parseCatalogNumbers(localText), columns[oid])
			if err != nil {
				return err
			}
			key := physical.PhysicalKey{ID: keyID(wanted, name, kind, table.ID), Name: physical.PhysicalName(name), Columns: ids}
			if kind == "p" {
				table.PrimaryKey = &key
			} else {
				table.Uniques = append(table.Uniques, key)
			}
		case "f":
			local, err := fieldIDs(parseCatalogNumbers(localText), columns[oid])
			if err != nil {
				return err
			}
			remote, err := fieldIDs(parseCatalogNumbers(remoteText), columns[remoteOID])
			if err != nil {
				return err
			}
			remoteTable := tables[remoteOID]
			if remoteTable == nil {
				return fmt.Errorf("postgresql foreign key %s references table outside managed namespace", name)
			}
			foreign := physical.PhysicalForeignKey{ID: foreignID(wanted, name, table.ID), Name: physical.PhysicalName(name), Columns: local, ReferencedTable: remoteTable.ID, ReferencedColumns: remote, OnUpdate: parseAction(updateCode), OnDelete: parseAction(deleteCode), Deferrable: parseDeferrable(deferrable, deferred)}
			table.ForeignKeys = append(table.ForeignKeys, foreign)
		case "c":
			parsed, err := parseCatalogExpression(expression, *table)
			if err != nil {
				return fmt.Errorf("postgresql check %s: %w", name, err)
			}
			id := checkID(wanted, name, table.ID)
			var reviewed *physical.Expression
			for index := range wanted.Checks {
				if wanted.Checks[index].ID == id {
					reviewed = &wanted.Checks[index].Expression
					break
				}
			}
			if reviewed == nil {
				return fmt.Errorf("postgresql check has no reviewed expression")
			}
			parsed, err = normalizeCatalogExpressionAgainstReviewed(parsed, *reviewed)
			if err != nil {
				return fmt.Errorf("postgresql check expression: %w", err)
			}
			table.Checks = append(table.Checks, physical.PhysicalCheck{ID: id, Name: physical.PhysicalName(name), Expression: parsed})
		default:
			return fmt.Errorf("postgresql constraint %s has unsupported type %q", name, kind)
		}
	}
	return rows.Err()
}

func introspectIndexes(ctx context.Context, q catalogQueryer, namespace physical.PhysicalName, expected map[string]physical.PhysicalTable, tables map[int64]*physical.PhysicalTable, columns map[int64]map[int]physical.PhysicalColumn) error {
	rows, err := q.QueryxContext(ctx, `SELECT i.indrelid::bigint,i.indexrelid::bigint,ci.relname,i.indisunique,am.amname,i.indkey::text,i.indoption::text,i.indnkeyatts::integer,i.indnatts::integer,i.indisvalid,i.indisready,i.indnullsnotdistinct,EXISTS(SELECT 1 FROM unnest(i.indclass::oid[]) value(opcoid) JOIN pg_catalog.pg_opclass opc ON opc.oid=value.opcoid WHERE NOT opc.opcdefault),EXISTS(SELECT 1 FROM unnest(i.indcollation::oid[]) value(collid) LEFT JOIN pg_catalog.pg_collation coll ON coll.oid=value.collid LEFT JOIN pg_catalog.pg_namespace coll_namespace ON coll_namespace.oid=coll.collnamespace WHERE value.collid<>0 AND NOT (coll_namespace.nspname='pg_catalog' AND coll.collname='default')),COALESCE(pg_catalog.pg_get_expr(i.indpred,i.indrelid),'') FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class ci ON ci.oid=i.indexrelid JOIN pg_catalog.pg_class ct ON ct.oid=i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=ct.relnamespace JOIN pg_catalog.pg_am am ON am.oid=ci.relam WHERE n.nspname=$1 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint con WHERE con.conindid=i.indexrelid) ORDER BY ct.relname,ci.relname`, string(namespace))
	if err != nil {
		return err
	}
	type catalogIndex struct {
		tableOID, indexOID                                                             int64
		name, method, keyText, optionText, predicateText                               string
		unique, valid, ready, nullsNotDistinct, nondefaultOpclass, nondefaultCollation bool
		keyCount, total                                                                int
	}
	var catalogIndexes []catalogIndex
	for rows.Next() {
		var item catalogIndex
		if err := rows.Scan(&item.tableOID, &item.indexOID, &item.name, &item.unique, &item.method, &item.keyText, &item.optionText, &item.keyCount, &item.total, &item.valid, &item.ready, &item.nullsNotDistinct, &item.nondefaultOpclass, &item.nondefaultCollation, &item.predicateText); err != nil {
			rows.Close()
			return err
		}
		catalogIndexes = append(catalogIndexes, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range catalogIndexes {
		table := tables[item.tableOID]
		if table == nil {
			continue
		}
		if err := validateCatalogIndexFacts(item.valid, item.ready, item.nullsNotDistinct, item.nondefaultOpclass, item.nondefaultCollation); err != nil {
			return fmt.Errorf("postgresql index %s: %w", item.name, err)
		}
		keys, options := parseCatalogNumbers(item.keyText), parseCatalogNumbers(item.optionText)
		index := physical.PhysicalIndex{ID: indexID(expected[string(table.Name)], item.name, table.ID), Name: physical.PhysicalName(item.name), Unique: item.unique, Method: physical.IndexMethod(item.method), CreationMode: physical.IndexTransactional}
		var reviewedIndex *physical.PhysicalIndex
		for position := range expected[string(table.Name)].Indexes {
			candidate := &expected[string(table.Name)].Indexes[position]
			if candidate.ID == index.ID {
				reviewedIndex = candidate
				break
			}
		}
		advanced := false
		for position := 0; position < item.keyCount; position++ {
			key := physical.IndexKey{Direction: ir.SortAsc, Nulls: ir.NullsDefault}
			option := 0
			if position < len(options) {
				option = options[position]
			}
			if option&1 != 0 {
				key.Direction = ir.SortDesc
			}
			nullsFirst := option&2 != 0
			if (key.Direction == ir.SortAsc && nullsFirst) || (key.Direction == ir.SortDesc && !nullsFirst) {
				if nullsFirst {
					key.Nulls = ir.NullsFirst
				} else {
					key.Nulls = ir.NullsLast
				}
				advanced = true
			}
			if position < len(keys) && keys[position] > 0 {
				column := columns[item.tableOID][keys[position]]
				id := column.ID
				key.Column = &id
			} else {
				var source string
				if err := q.QueryRowxContext(ctx, `SELECT pg_catalog.pg_get_indexdef($1,$2,true)`, item.indexOID, position+1).Scan(&source); err != nil {
					return err
				}
				expression, err := parseCatalogExpression(source, *table)
				if err != nil {
					return err
				}
				if reviewedIndex == nil || position >= len(reviewedIndex.Keys) || reviewedIndex.Keys[position].Expression == nil {
					return fmt.Errorf("postgresql expression index has no reviewed key")
				}
				expression, err = normalizeCatalogExpressionAgainstReviewed(expression, *reviewedIndex.Keys[position].Expression)
				if err != nil {
					return fmt.Errorf("postgresql expression index key: %w", err)
				}
				key.Expression = &expression
				advanced = true
			}
			if key.Direction == ir.SortDesc {
				advanced = true
			}
			index.Keys = append(index.Keys, key)
		}
		for position := item.keyCount; position < item.total && position < len(keys); position++ {
			column := columns[item.tableOID][keys[position]]
			index.Include = append(index.Include, column.ID)
			advanced = true
		}
		if item.predicateText != "" {
			predicate, err := parseCatalogExpression(item.predicateText, *table)
			if err != nil {
				return err
			}
			if reviewedIndex == nil || reviewedIndex.Predicate == nil {
				return fmt.Errorf("postgresql partial index has no reviewed predicate")
			}
			predicate, err = normalizeCatalogExpressionAgainstReviewed(predicate, *reviewedIndex.Predicate)
			if err != nil {
				return fmt.Errorf("postgresql index predicate: %w", err)
			}
			index.Predicate = &predicate
			advanced = true
		}
		if advanced {
			owner := physical.ObjectRef{Kind: ir.ObjectIndex, ModelID: table.ID, ObjectID: ir.ObjectID(index.ID)}
			index.RequiredCapabilities = []physical.CapabilityRequirement{{Capability: capabilityAdvancedIndexes, Owner: owner}}
		}
		table.Indexes = append(table.Indexes, index)
	}
	return nil
}

func introspectSystem(ctx context.Context, q catalogQueryer, expected physical.SystemSchema, allowed map[string]bool) (physical.SystemSchema, error) {
	if expected.Version == 0 {
		return physical.SystemSchema{}, nil
	}
	rows, err := q.QueryxContext(ctx, `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN('r','p','v','m','S') ORDER BY c.relname`, string(expected.Namespace.Name))
	if err != nil {
		return physical.SystemSchema{}, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return physical.SystemSchema{}, err
		}
		if allowed["table\x00"+name] {
			continue
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return physical.SystemSchema{}, err
	}
	if err := rows.Close(); err != nil {
		return physical.SystemSchema{}, err
	}
	ledgerName := ""
	outboxName := ""
	deliveryName := ""
	var expectedRelations []string
	for _, object := range expected.Objects {
		switch object.Kind {
		case physical.SystemMigrationLedger:
			ledgerName = string(object.Name)
			expectedRelations = append(expectedRelations, ledgerName)
		case physical.SystemOutbox:
			outboxName = string(object.Name)
			expectedRelations = append(expectedRelations, outboxName)
		case physical.SystemOutboxDelivery:
			deliveryName = string(object.Name)
			expectedRelations = append(expectedRelations, deliveryName)
		}
	}
	sort.Strings(expectedRelations)
	if fmt.Sprint(names) != fmt.Sprint(expectedRelations) {
		return physical.SystemSchema{}, fmt.Errorf("postgresql system schema drift: relations=%v", names)
	}
	columnRows, err := q.QueryxContext(ctx, `SELECT a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,COALESCE(pg_catalog.pg_get_expr(d.adbin,d.adrelid),''),a.attidentity::text,a.attgenerated::text FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, string(expected.Namespace.Name), ledgerName)
	if err != nil {
		return physical.SystemSchema{}, err
	}
	type ledgerColumn struct {
		name, storage, defaultExpression, identity, generated string
		notNull                                               bool
	}
	var actualColumns []ledgerColumn
	for columnRows.Next() {
		var value ledgerColumn
		if err := columnRows.Scan(&value.name, &value.storage, &value.notNull, &value.defaultExpression, &value.identity, &value.generated); err != nil {
			columnRows.Close()
			return physical.SystemSchema{}, err
		}
		actualColumns = append(actualColumns, value)
	}
	if err := columnRows.Err(); err != nil {
		columnRows.Close()
		return physical.SystemSchema{}, err
	}
	if err := columnRows.Close(); err != nil {
		return physical.SystemSchema{}, err
	}
	wanted := []ledgerColumn{{name: "migration_id", storage: "text", notNull: true}, {name: "parent_chain_hash", storage: "text", notNull: true}, {name: "chain_hash", storage: "text", notNull: true}, {name: "file_checksums", storage: "jsonb", notNull: true}, {name: "before_physical_fingerprint", storage: "text", notNull: true}, {name: "after_physical_fingerprint", storage: "text", notNull: true}, {name: "phases", storage: "jsonb", notNull: true}, {name: "applied_at", storage: "timestamp(6) with time zone", notNull: true}}
	if fmt.Sprint(actualColumns) != fmt.Sprint(wanted) {
		return physical.SystemSchema{}, fmt.Errorf("postgresql migration ledger column drift")
	}
	constraintRows, err := q.QueryxContext(ctx, `SELECT con.contype::text,COALESCE(con.conkey::text,''),con.condeferrable,con.convalidated FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_class c ON c.oid=con.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 ORDER BY con.conname`, string(expected.Namespace.Name), ledgerName)
	if err != nil {
		return physical.SystemSchema{}, err
	}
	constraintCount := 0
	for constraintRows.Next() {
		var kind, keys string
		var deferrable, validated bool
		if err := constraintRows.Scan(&kind, &keys, &deferrable, &validated); err != nil {
			constraintRows.Close()
			return physical.SystemSchema{}, err
		}
		constraintCount++
		if kind != "p" || keys != "{1}" || deferrable || !validated {
			constraintRows.Close()
			return physical.SystemSchema{}, fmt.Errorf("postgresql migration ledger constraint drift")
		}
	}
	if err := constraintRows.Err(); err != nil {
		constraintRows.Close()
		return physical.SystemSchema{}, err
	}
	if err := constraintRows.Close(); err != nil {
		return physical.SystemSchema{}, err
	}
	if constraintCount != 1 {
		return physical.SystemSchema{}, fmt.Errorf("postgresql migration ledger constraint drift: got %d constraints", constraintCount)
	}
	var extraIndexes int
	if err := q.QueryRowxContext(ctx, `SELECT count(*) FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint con WHERE con.conindid=i.indexrelid)`, string(expected.Namespace.Name), ledgerName).Scan(&extraIndexes); err != nil {
		return physical.SystemSchema{}, err
	}
	if extraIndexes != 0 {
		return physical.SystemSchema{}, fmt.Errorf("postgresql migration ledger has %d unmanaged indexes", extraIndexes)
	}
	if outboxName != "" {
		if err := introspectOutbox(ctx, q, expected.Namespace.Name, outboxName); err != nil {
			return physical.SystemSchema{}, err
		}
	}
	if deliveryName != "" {
		if err := introspectOutboxDelivery(ctx, q, expected.Namespace.Name, deliveryName); err != nil {
			return physical.SystemSchema{}, err
		}
	}
	return expected, nil
}

func introspectOutbox(ctx context.Context, q catalogQueryer, namespace physical.PhysicalName, name string) error {
	columnRows, err := q.QueryxContext(ctx, `SELECT a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,COALESCE(pg_catalog.pg_get_expr(d.adbin,d.adrelid),''),a.attidentity::text,a.attgenerated::text FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, string(namespace), name)
	if err != nil {
		return err
	}
	type outboxColumn struct {
		name, storage, defaultExpression, identity, generated string
		notNull                                               bool
	}
	var actualColumns []outboxColumn
	for columnRows.Next() {
		var value outboxColumn
		if err := columnRows.Scan(&value.name, &value.storage, &value.notNull, &value.defaultExpression, &value.identity, &value.generated); err != nil {
			columnRows.Close()
			return err
		}
		actualColumns = append(actualColumns, value)
	}
	if err := columnRows.Close(); err != nil {
		return err
	}
	wanted := []outboxColumn{
		{name: "event_id", storage: "text", notNull: true},
		{name: "fact_version", storage: "integer", notNull: true},
		{name: "codec_identity", storage: "text", notNull: true},
		{name: "generation_fingerprint", storage: "text", notNull: true},
		{name: "model_id", storage: "text", notNull: true},
		{name: "action", storage: "text", notNull: true},
		{name: "before_identity", storage: "bytea"},
		{name: "after_identity", storage: "bytea"},
		{name: "causation_id", storage: "text", notNull: true},
		{name: "transaction_ordinal", storage: "integer", notNull: true},
		{name: "metadata", storage: "bytea", notNull: true},
		{name: "delete_snapshot", storage: "bytea"},
		{name: "recorded_at", storage: "timestamp(6) with time zone", notNull: true},
	}
	if fmt.Sprint(actualColumns) != fmt.Sprint(wanted) {
		return fmt.Errorf("postgresql outbox column drift")
	}
	constraintRows, err := q.QueryxContext(ctx, `SELECT con.contype::text,COALESCE(con.conkey::text,''),con.condeferrable,con.convalidated FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_class c ON c.oid=con.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 ORDER BY con.contype,con.conkey::text`, string(namespace), name)
	if err != nil {
		return err
	}
	actualConstraints := map[string]int{}
	for constraintRows.Next() {
		var kind, keys string
		var deferrable, validated bool
		if err := constraintRows.Scan(&kind, &keys, &deferrable, &validated); err != nil {
			constraintRows.Close()
			return err
		}
		if deferrable || !validated {
			constraintRows.Close()
			return fmt.Errorf("postgresql outbox constraint drift")
		}
		actualConstraints[kind+"\x00"+canonicalSystemConstraintKeys(kind, keys)]++
	}
	if err := constraintRows.Close(); err != nil {
		return err
	}
	wantedConstraints := map[string]int{"p\x00{1}": 1, "u\x00{9,10}": 1, "c\x00{2}": 1, "c\x00{6}": 1, "c\x00{10}": 1, "c\x00{6,7,8}": 1}
	if !reflect.DeepEqual(actualConstraints, wantedConstraints) {
		return fmt.Errorf("postgresql outbox constraint drift")
	}
	indexRows, err := q.QueryxContext(ctx, `SELECT ic.relname,i.indisunique,i.indisvalid,i.indkey::text,COALESCE(pg_catalog.pg_get_expr(i.indpred,i.indrelid),'') FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid WHERE n.nspname=$1 AND c.relname=$2 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint con WHERE con.conindid=i.indexrelid) ORDER BY ic.relname`, string(namespace), name)
	if err != nil {
		return err
	}
	defer indexRows.Close()
	count := 0
	for indexRows.Next() {
		var indexName, keys, predicate string
		var unique, valid bool
		if err := indexRows.Scan(&indexName, &unique, &valid, &keys, &predicate); err != nil {
			return err
		}
		count++
		if indexName != "_golem_outbox_pending" || unique || !valid || keys != "13 1" || predicate != "" {
			return fmt.Errorf("postgresql outbox index drift")
		}
	}
	if err := indexRows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("postgresql outbox index drift: got %d indexes", count)
	}
	return nil
}

func introspectOutboxDelivery(ctx context.Context, q catalogQueryer, namespace physical.PhysicalName, name string) error {
	columnRows, err := q.QueryxContext(ctx, `SELECT a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,COALESCE(pg_catalog.pg_get_expr(d.adbin,d.adrelid),''),a.attidentity::text,a.attgenerated::text FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, string(namespace), name)
	if err != nil {
		return err
	}
	type deliveryColumn struct {
		name, storage, defaultExpression, identity, generated string
		notNull                                               bool
	}
	var actualColumns []deliveryColumn
	for columnRows.Next() {
		var value deliveryColumn
		if err := columnRows.Scan(&value.name, &value.storage, &value.notNull, &value.defaultExpression, &value.identity, &value.generated); err != nil {
			columnRows.Close()
			return err
		}
		actualColumns = append(actualColumns, value)
	}
	if err := columnRows.Close(); err != nil {
		return err
	}
	wanted := []deliveryColumn{
		{name: "causation_id", storage: "text", notNull: true},
		{name: "status", storage: "text", notNull: true},
		{name: "first_recorded_at", storage: "timestamp(6) with time zone", notNull: true},
		{name: "attempt_count", storage: "bigint", notNull: true},
		{name: "available_at", storage: "timestamp(6) with time zone", notNull: true},
		{name: "lease_token", storage: "text"},
		{name: "lease_until", storage: "timestamp(6) with time zone"},
		{name: "delivered_at", storage: "timestamp(6) with time zone"},
		{name: "last_failure_code", storage: "text"},
		{name: "blocked_at", storage: "timestamp(6) with time zone"},
		{name: "retired_at", storage: "timestamp(6) with time zone"},
		{name: "updated_at", storage: "timestamp(6) with time zone", notNull: true},
	}
	if !reflect.DeepEqual(actualColumns, wanted) {
		return fmt.Errorf("postgresql outbox delivery column drift")
	}
	constraintRows, err := q.QueryxContext(ctx, `SELECT con.contype::text,COALESCE(con.conkey::text,''),con.condeferrable,con.convalidated FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_class c ON c.oid=con.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 ORDER BY con.contype,con.conkey::text`, string(namespace), name)
	if err != nil {
		return err
	}
	actualConstraints := map[string]int{}
	for constraintRows.Next() {
		var kind, keys string
		var deferrable, validated bool
		if err := constraintRows.Scan(&kind, &keys, &deferrable, &validated); err != nil {
			constraintRows.Close()
			return err
		}
		if deferrable || !validated {
			constraintRows.Close()
			return fmt.Errorf("postgresql outbox delivery constraint drift")
		}
		actualConstraints[kind+"\x00"+canonicalSystemConstraintKeys(kind, keys)]++
	}
	if err := constraintRows.Close(); err != nil {
		return err
	}
	wantedConstraints := map[string]int{
		"p\x00{1}":               1,
		"c\x00{1}":               1,
		"c\x00{2}":               1,
		"c\x00{4}":               1,
		"c\x00{6}":               1,
		"c\x00{9}":               1,
		"c\x00{2,6,7,8,9,10,11}": 1,
	}
	if !reflect.DeepEqual(actualConstraints, wantedConstraints) {
		return fmt.Errorf("postgresql outbox delivery constraint drift")
	}
	indexRows, err := q.QueryxContext(ctx, `SELECT ic.relname,i.indisunique,i.indisvalid,i.indkey::text,COALESCE(pg_catalog.pg_get_expr(i.indpred,i.indrelid),'') FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN pg_catalog.pg_class ic ON ic.oid=i.indexrelid WHERE n.nspname=$1 AND c.relname=$2 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint con WHERE con.conindid=i.indexrelid) ORDER BY ic.relname`, string(namespace), name)
	if err != nil {
		return err
	}
	defer indexRows.Close()
	count := 0
	for indexRows.Next() {
		var indexName, keys, predicate string
		var unique, valid bool
		if err := indexRows.Scan(&indexName, &unique, &valid, &keys, &predicate); err != nil {
			return err
		}
		count++
		if indexName != "_golem_outbox_delivery_pending" || unique || !valid || keys != "2 5 3 1" || predicate != "" {
			return fmt.Errorf("postgresql outbox delivery index drift")
		}
	}
	if err := indexRows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("postgresql outbox delivery index drift: got %d indexes", count)
	}
	return nil
}

func (provider *Provider) verify(ctx context.Context, database *sqlx.DB, expected physical.PhysicalSchema) error {
	actual, err := provider.introspect(ctx, database, expected)
	if err != nil {
		return err
	}
	return physical.CompareFingerprints(expected, actual)
}

var (
	numericType = regexp.MustCompile(`^numeric\(([0-9]+),([0-9]+)\)$`)
	varcharType = regexp.MustCompile(`^character varying\(([0-9]+)\)$`)
)

func parseCatalogStorage(value string) (physical.StorageType, error) {
	switch value {
	case "boolean":
		return physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}, nil
	case "smallint":
		return physical.StorageType{Kind: physical.StoragePostgreSQLSmallInt}, nil
	case "integer":
		return physical.StorageType{Kind: physical.StoragePostgreSQLInteger}, nil
	case "bigint":
		return physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}, nil
	case "real":
		return physical.StorageType{Kind: physical.StoragePostgreSQLReal}, nil
	case "double precision":
		return physical.StorageType{Kind: physical.StoragePostgreSQLDouble}, nil
	case "text":
		return physical.StorageType{Kind: physical.StoragePostgreSQLText}, nil
	case "bytea":
		return physical.StorageType{Kind: physical.StoragePostgreSQLBytea}, nil
	case "uuid":
		return physical.StorageType{Kind: physical.StoragePostgreSQLUUID}, nil
	case "date":
		return physical.StorageType{Kind: physical.StoragePostgreSQLDate}, nil
	case "jsonb":
		return physical.StorageType{Kind: physical.StoragePostgreSQLJSONB}, nil
	}
	if match := numericType.FindStringSubmatch(value); match != nil {
		p, _ := strconv.Atoi(match[1])
		s, _ := strconv.Atoi(match[2])
		return physical.StorageType{Kind: physical.StoragePostgreSQLNumeric, Precision: uint16(p), Scale: uint16(s)}, nil
	}
	if match := varcharType.FindStringSubmatch(value); match != nil {
		length, _ := strconv.Atoi(match[1])
		return physical.StorageType{Kind: physical.StoragePostgreSQLVarchar, Length: uint32(length)}, nil
	}
	for _, item := range []struct {
		prefix string
		kind   physical.StorageKind
	}{{"time(", physical.StoragePostgreSQLTime}, {"timestamp(", physical.StoragePostgreSQLTimestampTZ}} {
		if strings.HasPrefix(value, item.prefix) {
			end := strings.Index(value, ")")
			precision, err := strconv.Atoi(value[len(item.prefix):end])
			if err != nil {
				return physical.StorageType{}, err
			}
			return physical.StorageType{Kind: item.kind, Length: uint32(precision)}, nil
		}
	}
	return physical.StorageType{}, fmt.Errorf("unsupported PostgreSQL type %q", value)
}
func parseCatalogNumbers(value string) []int {
	value = strings.NewReplacer("{", "", "}", "", ",", " ").Replace(value)
	var result []int
	for _, field := range strings.Fields(value) {
		number, err := strconv.Atoi(field)
		if err == nil {
			result = append(result, number)
		}
	}
	return result
}

// PostgreSQL's pg_constraint.conkey is ordered for keys, where column order is
// semantic, but its order for CHECK constraints follows the server's internal
// expression-reference discovery. That order may differ from textual column
// order (and may repeat a column) without changing the referenced column set.
// Canonicalize CHECK references only; primary, unique, and foreign keys retain
// their exact catalog order.
func canonicalSystemConstraintKeys(kind, value string) string {
	if kind != "c" {
		return value
	}
	numbers := parseCatalogNumbers(value)
	sort.Ints(numbers)
	canonical := numbers[:0]
	for _, number := range numbers {
		if len(canonical) == 0 || canonical[len(canonical)-1] != number {
			canonical = append(canonical, number)
		}
	}
	parts := make([]string, len(canonical))
	for index, number := range canonical {
		parts[index] = strconv.Itoa(number)
	}
	return "{" + strings.Join(parts, ",") + "}"
}
func fieldIDs(numbers []int, columns map[int]physical.PhysicalColumn) ([]ir.FieldID, error) {
	result := make([]ir.FieldID, len(numbers))
	for i, number := range numbers {
		column, ok := columns[number]
		if !ok {
			return nil, fmt.Errorf("catalog references unknown attribute %d", number)
		}
		result[i] = column.ID
	}
	return result, nil
}
func keyID(table physical.PhysicalTable, name, kind string, owner ir.ModelID) ir.KeyID {
	if table.PrimaryKey != nil && string(table.PrimaryKey.Name) == name {
		return table.PrimaryKey.ID
	}
	for _, key := range table.Uniques {
		if string(key.Name) == name {
			return key.ID
		}
	}
	return ir.KeyID(stableID("catalog-key", string(owner), kind, name))
}
func foreignID(table physical.PhysicalTable, name string, owner ir.ModelID) ir.ForeignKeyID {
	for _, value := range table.ForeignKeys {
		if string(value.Name) == name {
			return value.ID
		}
	}
	return ir.ForeignKeyID(stableID("catalog-fk", string(owner), name))
}
func checkID(table physical.PhysicalTable, name string, owner ir.ModelID) ir.CheckID {
	for _, value := range table.Checks {
		if string(value.Name) == name {
			return value.ID
		}
	}
	return ir.CheckID(stableID("catalog-check", string(owner), name))
}
func indexID(table physical.PhysicalTable, name string, owner ir.ModelID) ir.IndexID {
	for _, value := range table.Indexes {
		if string(value.Name) == name {
			return value.ID
		}
	}
	return ir.IndexID(stableID("catalog-index", string(owner), name))
}
func parseAction(value string) ir.ReferentialAction {
	return map[string]ir.ReferentialAction{"a": ir.ActionNoAction, "r": ir.ActionRestrict, "c": ir.ActionCascade, "n": ir.ActionSetNull, "d": ir.ActionSetDefault}[value]
}
func parseDeferrable(value, deferred bool) ir.Deferrability {
	if !value {
		return ir.NotDeferrable
	}
	if deferred {
		return ir.InitiallyDeferred
	}
	return ir.InitiallyImmediate
}

func validateCatalogTableFacts(kind, persistence string) error {
	if kind != "r" {
		return fmt.Errorf("unsupported relkind=%s", kind)
	}
	if persistence != "p" {
		return fmt.Errorf("unsupported persistence=%s", persistence)
	}
	return nil
}
func validateCatalogBehaviorFlags(rowSecurity, forceRowSecurity bool) error {
	if rowSecurity || forceRowSecurity {
		return fmt.Errorf("row-level security flags are not baseline")
	}
	return nil
}

func rejectUnexpectedBehaviorObjects(ctx context.Context, query catalogQueryer, namespace physical.PhysicalName, managedTables map[int64]*physical.PhysicalTable, allowed map[string]bool) error {
	rows, err := query.QueryxContext(ctx, `SELECT behavior.table_oid,behavior.kind,behavior.name FROM (
  SELECT c.oid::bigint AS table_oid,'trigger'::text AS kind,t.tgname::text AS name
  FROM pg_catalog.pg_trigger t
  JOIN pg_catalog.pg_class c ON c.oid=t.tgrelid
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname=$1 AND NOT t.tgisinternal
  UNION ALL
  SELECT c.oid::bigint,'policy'::text,p.polname::text
  FROM pg_catalog.pg_policy p
  JOIN pg_catalog.pg_class c ON c.oid=p.polrelid
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname=$1
  UNION ALL
  SELECT c.oid::bigint,'rule'::text,r.rulename::text
  FROM pg_catalog.pg_rewrite r
  JOIN pg_catalog.pg_class c ON c.oid=r.ev_class
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname=$1 AND NOT (c.relkind IN ('v','m') AND r.rulename='_RETURN')
) behavior ORDER BY behavior.table_oid,behavior.kind,behavior.name`, string(namespace))
	if err != nil {
		return fmt.Errorf("postgresql catalog behavior objects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tableOID int64
		var kind, name string
		if err := rows.Scan(&tableOID, &kind, &name); err != nil {
			return err
		}
		if managedTables[tableOID] == nil {
			continue
		}
		if !catalogBehaviorObjectAllowed(kind, name, allowed) {
			return fmt.Errorf("postgresql managed table %s has unexpected %s %q", managedTables[tableOID].Name, kind, name)
		}
	}
	return rows.Err()
}

func catalogBehaviorObjectAllowed(kind, name string, allowed map[string]bool) bool {
	return allowed[kind+"\x00"+name]
}
func validateCatalogIndexFacts(valid, ready, nullsNotDistinct, nondefaultOpclass, nondefaultCollation bool) error {
	if !valid || !ready {
		return fmt.Errorf("not valid and ready")
	}
	if nullsNotDistinct {
		return fmt.Errorf("NULLS NOT DISTINCT is not baseline")
	}
	if nondefaultOpclass || nondefaultCollation {
		return fmt.Errorf("non-baseline operator class/collation")
	}
	return nil
}
func validateIdentityMode(value string) error {
	if value != "d" {
		return fmt.Errorf("unsupported identity mode %q", value)
	}
	return nil
}
func validateGeneratedMode(value string) error {
	if value != "s" {
		return fmt.Errorf("unsupported generated mode %q", value)
	}
	return nil
}
func validateCatalogConstraintFacts(kind, match string, deferrable, deferred, validated, noInherit, nullsNotDistinct bool) error {
	if !validated {
		return fmt.Errorf("not validated")
	}
	switch kind {
	case "p", "u":
		if deferrable || deferred {
			return fmt.Errorf("key is unexpectedly deferrable")
		}
		if nullsNotDistinct {
			return fmt.Errorf("NULLS NOT DISTINCT is not baseline")
		}
	case "f":
		if match != "s" {
			return fmt.Errorf("foreign key match is not SIMPLE")
		}
	case "c":
		if noInherit {
			return fmt.Errorf("NO INHERIT is not baseline")
		}
		if deferrable || deferred {
			return fmt.Errorf("check is unexpectedly deferrable")
		}
	default:
		return fmt.Errorf("unsupported constraint type %q", kind)
	}
	return nil
}

func parseCatalogLiteral(source string, storage physical.StorageType) (ir.TypedLiteralIR, error) {
	source = stripOuter(strings.TrimSpace(source))
	kind := literalKind(storage)
	if strings.HasPrefix(source, "'") {
		decoded, rest, err := readSQLString(source)
		if err != nil || !validCatalogLiteralCast(strings.TrimSpace(rest), storage) {
			return ir.TypedLiteralIR{}, fmt.Errorf("unsupported literal %q", source)
		}
		if storage.Kind == physical.StoragePostgreSQLBytea {
			if !strings.HasPrefix(decoded, `\x`) {
				return ir.TypedLiteralIR{}, fmt.Errorf("unsupported bytea literal")
			}
			bytes, err := hex.DecodeString(strings.TrimPrefix(decoded, `\x`))
			if err != nil {
				return ir.TypedLiteralIR{}, err
			}
			decoded = base64.RawStdEncoding.EncodeToString(bytes)
		}
		if storage.Kind == physical.StoragePostgreSQLJSONB {
			canonical, err := scalar.CanonicalJSON([]byte(decoded))
			if err != nil {
				return ir.TypedLiteralIR{}, err
			}
			decoded = string(canonical)
		}
		if storage.Kind == physical.StoragePostgreSQLDate || storage.Kind == physical.StoragePostgreSQLTime || storage.Kind == physical.StoragePostgreSQLTimestampTZ {
			canonical, err := canonicalCatalogTemporal(decoded, storage)
			if err != nil {
				return ir.TypedLiteralIR{}, err
			}
			decoded = canonical
		}
		return ir.TypedLiteralIR{Kind: kind, Canonical: decoded}, nil
	}
	base := source
	if index := strings.Index(base, "::"); index >= 0 {
		if !validCatalogLiteralCast(strings.TrimSpace(base[index:]), storage) {
			return ir.TypedLiteralIR{}, fmt.Errorf("unsupported literal cast in %q", source)
		}
		base = base[:index]
	}
	base = strings.TrimSpace(base)
	upper := strings.ToUpper(base)
	if upper == "TRUE" || upper == "FALSE" {
		return ir.TypedLiteralIR{Kind: ir.LiteralBool, Canonical: strings.ToLower(upper)}, nil
	}
	if kind == ir.LiteralInteger || kind == ir.LiteralFloat || kind == ir.LiteralDecimal {
		return ir.TypedLiteralIR{Kind: kind, Canonical: base}, nil
	}
	return ir.TypedLiteralIR{}, fmt.Errorf("unsupported literal expression %q", source)
}

func validCatalogLiteralCast(suffix string, storage physical.StorageType) bool {
	if suffix == "" {
		// The renderer intentionally leaves text/enum literals uncast. Every other
		// quoted storage form owns an explicit cast, so accepting an omitted cast
		// would broaden the catalog language beyond what Golem emits.
		return storage.Kind == physical.StoragePostgreSQLText || storage.Kind == physical.StoragePostgreSQLVarchar
	}
	accepted := map[physical.StorageKind][]string{
		physical.StoragePostgreSQLBoolean:     {"::boolean"},
		physical.StoragePostgreSQLSmallInt:    {"::smallint"},
		physical.StoragePostgreSQLInteger:     {"::integer"},
		physical.StoragePostgreSQLBigInt:      {"::bigint"},
		physical.StoragePostgreSQLReal:        {"::real"},
		physical.StoragePostgreSQLDouble:      {"::double precision"},
		physical.StoragePostgreSQLNumeric:     {"::numeric", fmt.Sprintf("::numeric(%d,%d)", storage.Precision, storage.Scale)},
		physical.StoragePostgreSQLVarchar:     {"::character varying", "::varchar", fmt.Sprintf("::character varying(%d)", storage.Length), fmt.Sprintf("::varchar(%d)", storage.Length)},
		physical.StoragePostgreSQLText:        {"::text"},
		physical.StoragePostgreSQLBytea:       {"::bytea"},
		physical.StoragePostgreSQLUUID:        {"::uuid"},
		physical.StoragePostgreSQLDate:        {"::date"},
		physical.StoragePostgreSQLTime:        {"::time without time zone", "::time", fmt.Sprintf("::time(%d) without time zone", storage.Length), fmt.Sprintf("::time(%d)", storage.Length)},
		physical.StoragePostgreSQLTimestampTZ: {"::timestamp with time zone", "::timestamptz", fmt.Sprintf("::timestamp(%d) with time zone", storage.Length), fmt.Sprintf("::timestamptz(%d)", storage.Length)},
		physical.StoragePostgreSQLJSONB:       {"::jsonb"},
	}
	for _, candidate := range accepted[storage.Kind] {
		if suffix == candidate {
			return true
		}
	}
	return false
}

func canonicalCatalogTemporal(value string, storage physical.StorageType) (string, error) {
	switch storage.Kind {
	case physical.StoragePostgreSQLDate:
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil {
			return "", fmt.Errorf("invalid PostgreSQL date literal %q", value)
		}
		return parsed.Format("2006-01-02"), nil
	case physical.StoragePostgreSQLTime:
		parsed, err := time.Parse("15:04:05.999999999", value)
		if err != nil {
			return "", fmt.Errorf("invalid PostgreSQL time literal %q", value)
		}
		return formatTemporalClock(parsed, uint16(storage.Length)), nil
	case physical.StoragePostgreSQLTimestampTZ:
		var parsed time.Time
		var err error
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07"} {
			parsed, err = time.Parse(layout, value)
			if err == nil {
				break
			}
		}
		if err != nil {
			return "", fmt.Errorf("invalid PostgreSQL timestamptz literal %q", value)
		}
		parsed = parsed.UTC()
		base := parsed.Format("2006-01-02T15:04:05")
		precision := uint16(storage.Length)
		if precision == 0 {
			return base + "Z", nil
		}
		return base + "." + fmt.Sprintf("%09d", parsed.Nanosecond())[:precision] + "Z", nil
	default:
		return "", fmt.Errorf("storage %s is not temporal", storage.Kind)
	}
}

func formatTemporalClock(value time.Time, precision uint16) string {
	base := value.Format("15:04:05")
	if precision == 0 {
		return base
	}
	return base + "." + fmt.Sprintf("%09d", value.Nanosecond())[:precision]
}
func literalKind(storage physical.StorageType) ir.LiteralKind {
	switch storage.Kind {
	case physical.StoragePostgreSQLBoolean:
		return ir.LiteralBool
	case physical.StoragePostgreSQLSmallInt, physical.StoragePostgreSQLInteger, physical.StoragePostgreSQLBigInt:
		return ir.LiteralInteger
	case physical.StoragePostgreSQLReal, physical.StoragePostgreSQLDouble:
		return ir.LiteralFloat
	case physical.StoragePostgreSQLNumeric:
		return ir.LiteralDecimal
	case physical.StoragePostgreSQLUUID:
		return ir.LiteralUUID
	case physical.StoragePostgreSQLDate:
		return ir.LiteralDate
	case physical.StoragePostgreSQLTime:
		return ir.LiteralTime
	case physical.StoragePostgreSQLTimestampTZ:
		return ir.LiteralDateTime
	case physical.StoragePostgreSQLJSONB:
		return ir.LiteralJSON
	case physical.StoragePostgreSQLBytea:
		return ir.LiteralBytes
	default:
		return ir.LiteralString
	}
}
func readSQLString(value string) (string, string, error) {
	if len(value) == 0 || value[0] != '\'' {
		return "", value, fmt.Errorf("not a string")
	}
	var b strings.Builder
	for i := 1; i < len(value); i++ {
		if value[i] == '\'' {
			if i+1 < len(value) && value[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			return b.String(), value[i+1:], nil
		}
		b.WriteByte(value[i])
	}
	return "", "", fmt.Errorf("unterminated string")
}
