package postgresql

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/scalar"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

type catalogQueryer interface {
	QueryxContext(context.Context, string, ...any) (*sqlx.Rows, error)
	QueryRowxContext(context.Context, string, ...any) *sqlx.Row
}

func (provider *Provider) introspect(ctx context.Context, database *sqlx.DB, expected physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	if database == nil {
		return physical.PhysicalSchema{}, fmt.Errorf("postgresql introspect: nil database")
	}
	return provider.introspectQuery(ctx, database, expected)
}

func (provider *Provider) introspectQuery(ctx context.Context, query catalogQueryer, expected physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	expectedNormalized, err := physical.Normalize(expected)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	report, err := probeCapabilities(ctx, query)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	if report.Version.Major < 15 || !report.JSONB || !report.GeneratedColumns || !report.AdvisoryLocks || !report.BinaryText || !report.ASCIIInsensitive || !report.ExactJSON || !report.ScalarListJSON || !report.RelationCorrelation {
		return physical.PhysicalSchema{}, fmt.Errorf("postgresql capability verification failed: version=%d.%d jsonb=%t generated=%t advisory=%t binary=%t ascii=%t exactJSON=%t scalarListJSON=%t relation=%t", report.Version.Major, report.Version.Minor, report.JSONB, report.GeneratedColumns, report.AdvisoryLocks, report.BinaryText, report.ASCIIInsensitive, report.ExactJSON, report.ScalarListJSON, report.RelationCorrelation)
	}
	actual := physical.PhysicalSchema{Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion, Provider: provider.Manifest(), Namespace: expectedNormalized.Namespace, Unmanaged: append([]physical.UnmanagedObject(nil), expectedNormalized.Unmanaged...)}
	allowed := map[string]bool{}
	for _, object := range expectedNormalized.Unmanaged {
		allowed[object.Kind+"\x00"+string(object.Name)] = true
	}
	expectedTables := map[string]physical.PhysicalTable{}
	for _, table := range expectedNormalized.Tables {
		expectedTables[string(table.Name)] = table
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
		if wanted, ok := expectedTables[string(table.Name)]; ok {
			for _, column := range wanted.Columns {
				if string(column.Name) == name {
					fieldID = column.ID
					break
				}
			}
		}
		column := physical.PhysicalColumn{ID: fieldID, Name: physical.PhysicalName(name), Ordinal: visibleOrdinals[oid], Storage: storage, Nullable: !notNull, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}
		visibleOrdinals[oid]++
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
	for _, pending := range pendingGeneratedExpressions {
		table := tableByOID[pending.tableOID]
		column := columnsByAttnum[pending.tableOID][pending.attnum]
		expression, parseErr := parseCatalogExpression(pending.expression, *table)
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
	actual.System, err = introspectSystem(ctx, query, expectedNormalized.System)
	if err != nil {
		return physical.PhysicalSchema{}, err
	}
	return physical.Normalize(actual)
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
			table.Checks = append(table.Checks, physical.PhysicalCheck{ID: checkID(wanted, name, table.ID), Name: physical.PhysicalName(name), Expression: parsed})
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
	defer rows.Close()
	for rows.Next() {
		var tableOID, indexOID int64
		var name, method, keyText, optionText, predicateText string
		var unique, valid, ready, nullsNotDistinct, nondefaultOpclass, nondefaultCollation bool
		var keyCount, total int
		if err := rows.Scan(&tableOID, &indexOID, &name, &unique, &method, &keyText, &optionText, &keyCount, &total, &valid, &ready, &nullsNotDistinct, &nondefaultOpclass, &nondefaultCollation, &predicateText); err != nil {
			return err
		}
		table := tables[tableOID]
		if table == nil {
			continue
		}
		if err := validateCatalogIndexFacts(valid, ready, nullsNotDistinct, nondefaultOpclass, nondefaultCollation); err != nil {
			return fmt.Errorf("postgresql index %s: %w", name, err)
		}
		keys, options := parseCatalogNumbers(keyText), parseCatalogNumbers(optionText)
		index := physical.PhysicalIndex{ID: indexID(expected[string(table.Name)], name, table.ID), Name: physical.PhysicalName(name), Unique: unique, Method: physical.IndexMethod(method), CreationMode: physical.IndexTransactional}
		advanced := false
		for position := 0; position < keyCount; position++ {
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
				column := columns[tableOID][keys[position]]
				id := column.ID
				key.Column = &id
			} else {
				var source string
				if err := q.QueryRowxContext(ctx, `SELECT pg_catalog.pg_get_indexdef($1,$2,true)`, indexOID, position+1).Scan(&source); err != nil {
					return err
				}
				expression, err := parseCatalogExpression(source, *table)
				if err != nil {
					return err
				}
				key.Expression = &expression
				advanced = true
			}
			if key.Direction == ir.SortDesc {
				advanced = true
			}
			index.Keys = append(index.Keys, key)
		}
		for position := keyCount; position < total && position < len(keys); position++ {
			column := columns[tableOID][keys[position]]
			index.Include = append(index.Include, column.ID)
			advanced = true
		}
		if predicateText != "" {
			predicate, err := parseCatalogExpression(predicateText, *table)
			if err != nil {
				return err
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
	return rows.Err()
}

func introspectSystem(ctx context.Context, q catalogQueryer, expected physical.SystemSchema) (physical.SystemSchema, error) {
	if expected.Version == 0 {
		return physical.SystemSchema{}, nil
	}
	rows, err := q.QueryxContext(ctx, `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN('r','p','v','m','S') ORDER BY c.relname`, string(expected.Namespace.Name))
	if err != nil {
		return physical.SystemSchema{}, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return physical.SystemSchema{}, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return physical.SystemSchema{}, err
	}
	ledgerName := ""
	for _, object := range expected.Objects {
		if object.Kind == physical.SystemMigrationLedger {
			ledgerName = string(object.Name)
		}
	}
	if len(names) != 1 || names[0] != ledgerName {
		return physical.SystemSchema{}, fmt.Errorf("postgresql system schema drift: relations=%v", names)
	}
	columnRows, err := q.QueryxContext(ctx, `SELECT a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,COALESCE(pg_catalog.pg_get_expr(d.adbin,d.adrelid),''),a.attidentity::text,a.attgenerated::text FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, string(expected.Namespace.Name), ledgerName)
	if err != nil {
		return physical.SystemSchema{}, err
	}
	defer columnRows.Close()
	type ledgerColumn struct {
		name, storage, defaultExpression, identity, generated string
		notNull                                               bool
	}
	var actualColumns []ledgerColumn
	for columnRows.Next() {
		var value ledgerColumn
		if err := columnRows.Scan(&value.name, &value.storage, &value.notNull, &value.defaultExpression, &value.identity, &value.generated); err != nil {
			return physical.SystemSchema{}, err
		}
		actualColumns = append(actualColumns, value)
	}
	wanted := []ledgerColumn{{name: "migration_id", storage: "text", notNull: true}, {name: "parent_chain_hash", storage: "text", notNull: true}, {name: "chain_hash", storage: "text", notNull: true}, {name: "file_checksums", storage: "jsonb", notNull: true}, {name: "before_physical_fingerprint", storage: "text", notNull: true}, {name: "after_physical_fingerprint", storage: "text", notNull: true}, {name: "phases", storage: "jsonb", notNull: true}, {name: "applied_at", storage: "timestamp(6) with time zone", notNull: true}}
	if fmt.Sprint(actualColumns) != fmt.Sprint(wanted) {
		return physical.SystemSchema{}, fmt.Errorf("postgresql migration ledger column drift")
	}
	constraintRows, err := q.QueryxContext(ctx, `SELECT con.contype::text,COALESCE(con.conkey::text,''),con.condeferrable,con.convalidated FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_class c ON c.oid=con.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 ORDER BY con.conname`, string(expected.Namespace.Name), ledgerName)
	if err != nil {
		return physical.SystemSchema{}, err
	}
	defer constraintRows.Close()
	constraintCount := 0
	for constraintRows.Next() {
		var kind, keys string
		var deferrable, validated bool
		if err := constraintRows.Scan(&kind, &keys, &deferrable, &validated); err != nil {
			return physical.SystemSchema{}, err
		}
		constraintCount++
		if kind != "p" || keys != "{1}" || deferrable || !validated {
			return physical.SystemSchema{}, fmt.Errorf("postgresql migration ledger constraint drift")
		}
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
	return systemSchema(), nil
}

func (provider *Provider) verify(ctx context.Context, database *sqlx.DB, expected physical.PhysicalSchema) error {
	actual, err := provider.introspect(ctx, database, expected)
	if err != nil {
		return err
	}
	return compareFingerprints(expected, actual)
}

var numericType = regexp.MustCompile(`^numeric\(([0-9]+),([0-9]+)\)$`)

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
		return storage.Kind == physical.StoragePostgreSQLText
	}
	accepted := map[physical.StorageKind][]string{
		physical.StoragePostgreSQLBoolean:     {"::boolean"},
		physical.StoragePostgreSQLSmallInt:    {"::smallint"},
		physical.StoragePostgreSQLInteger:     {"::integer"},
		physical.StoragePostgreSQLBigInt:      {"::bigint"},
		physical.StoragePostgreSQLReal:        {"::real"},
		physical.StoragePostgreSQLDouble:      {"::double precision"},
		physical.StoragePostgreSQLNumeric:     {"::numeric", fmt.Sprintf("::numeric(%d,%d)", storage.Precision, storage.Scale)},
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
