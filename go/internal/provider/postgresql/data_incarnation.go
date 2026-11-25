package postgresql

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/eleven-am/golem/go/internal/dataincarnation"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

type dataIncarnationQuery interface {
	QueryxContext(context.Context, string, ...any) (*sqlx.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (*Provider) ReadDataIncarnation(ctx context.Context, database *sqlx.DB, system physical.SystemSchema) (dataincarnation.Value, error) {
	if database == nil {
		return dataincarnation.Value{}, fmt.Errorf("data incarnation database is nil")
	}
	return readDataIncarnation(ctx, database, system)
}

func dataIncarnationTable(system physical.SystemSchema) (string, error) {
	if !physical.HasDataIncarnation(system) {
		return "", fmt.Errorf("data incarnation is not declared")
	}
	if !eventNamespacePattern.MatchString(string(system.Namespace.Name)) {
		return "", fmt.Errorf("invalid data incarnation namespace")
	}
	return qualified(system.Namespace.Name, physical.DataIncarnationSystemObjectV1().Name), nil
}

func readDataIncarnation(ctx context.Context, query dataIncarnationQuery, system physical.SystemSchema) (dataincarnation.Value, error) {
	table, err := dataIncarnationTable(system)
	if err != nil {
		return dataincarnation.Value{}, err
	}
	rows, err := query.QueryxContext(ctx, `SELECT "singleton"=1, true, octet_length("value"), substring("value" from 1 for 33) FROM `+table+` LIMIT 2`)
	if err != nil {
		return dataincarnation.Value{}, err
	}
	defer rows.Close()
	var value dataincarnation.Value
	count := 0
	for rows.Next() {
		var singleton, textual bool
		var length int64
		var text string
		if err := rows.Scan(&singleton, &textual, &length, &text); err != nil {
			return dataincarnation.Value{}, err
		}
		count++
		if !singleton || !textual || length != 32 || count != 1 {
			return dataincarnation.Value{}, fmt.Errorf("data incarnation requires exactly one singleton row")
		}
		value, err = dataincarnation.Parse(text)
		if err != nil {
			return dataincarnation.Value{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return dataincarnation.Value{}, err
	}
	if count != 1 {
		return dataincarnation.Value{}, fmt.Errorf("data incarnation singleton is missing")
	}
	return value, nil
}

// Only a previously sealed reviewed entry can authorize initialization. This
// helper is called inside its DDL/ledger transaction, never by App admission.
func applyDataIncarnation(ctx context.Context, query dataIncarnationQuery, entry migration.ManifestEntry, previous dataincarnation.Value) error {
	if migration.IsDataCorrection(entry) {
		return rotatePostgreSQLCorrectionIncarnation(ctx, query, entry, previous)
	}
	if !physical.HasDataIncarnation(entry.AfterSnapshot.System) {
		return nil
	}
	if !physical.HasDataIncarnation(entry.BeforeSnapshot.System) {
		plan, err := reviewedEntryPlan(entry)
		if err != nil {
			return err
		}
		admitted := plan.Initial
		for _, operation := range plan.Operations {
			if operation.Kind == migration.AddSystemObject && operation.ObjectID == string(physical.DataIncarnationObjectIDV1) {
				admitted = true
			}
		}
		if !admitted {
			return fmt.Errorf("data incarnation initialization lacks reviewed authority")
		}
		value, err := dataincarnation.New()
		if err != nil {
			return err
		}
		table, err := dataIncarnationTable(entry.AfterSnapshot.System)
		if err != nil {
			return err
		}
		if _, err := query.ExecContext(ctx, `INSERT INTO `+table+` ("singleton","value") VALUES (1,$1)`, value.String()); err != nil {
			return err
		}
		previous = value
	}
	actual, err := readDataIncarnation(ctx, query, entry.AfterSnapshot.System)
	if err != nil {
		return err
	}
	if !previous.Valid() || actual != previous {
		return fmt.Errorf("reviewed migration changed data incarnation unexpectedly")
	}
	return nil
}

func renderDataIncarnation(namespace, name physical.PhysicalName) string {
	return `CREATE TABLE ` + qualified(namespace, name) + ` ("singleton" SMALLINT NOT NULL PRIMARY KEY CHECK ("singleton"=1), "value" TEXT NOT NULL CHECK (length("value")=32 AND translate("value",'0123456789abcdef','')='' AND "value"<>'00000000000000000000000000000000'))`
}

func beforeDataIncarnation(ctx context.Context, query dataIncarnationQuery, entry migration.ManifestEntry) (dataincarnation.Value, error) {
	if !physical.HasDataIncarnation(entry.BeforeSnapshot.System) {
		return dataincarnation.Value{}, nil
	}
	return readDataIncarnation(ctx, query, entry.BeforeSnapshot.System)
}

func introspectDataIncarnation(ctx context.Context, query catalogQueryer, namespace physical.PhysicalName) error {
	table := qualified(namespace, physical.DataIncarnationSystemObjectV1().Name)
	var ordinary bool
	err := query.QueryRowxContext(ctx, `SELECT c.relkind='r' AND c.relpersistence='p' AND NOT c.relispartition AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid=c.oid OR i.inhparent=c.oid) AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid=c.oid AND NOT t.tgisinternal) AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_rewrite r WHERE r.ev_class=c.oid) FROM pg_catalog.pg_class c WHERE c.oid=to_regclass($1)`, table).Scan(&ordinary)
	if err != nil {
		return err
	}
	if !ordinary {
		return fmt.Errorf("data incarnation relation shape is invalid")
	}
	rows, err := query.QueryxContext(ctx, `SELECT a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull AND a.attidentity='' AND a.attgenerated='' AND a.attcollation=t.typcollation AND d.adrelid IS NULL FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_type t ON t.oid=a.atttypid LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, table)
	if err != nil {
		return err
	}
	wanted := [][2]string{{"singleton", "smallint"}, {"value", "text"}}
	count := 0
	for rows.Next() {
		var name, kind string
		var valid bool
		if err := rows.Scan(&name, &kind, &valid); err != nil {
			rows.Close()
			return err
		}
		if count >= len(wanted) || !valid || wanted[count] != [2]string{name, kind} {
			rows.Close()
			return fmt.Errorf("data incarnation columns differ from registered shape")
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count != 2 {
		return fmt.Errorf("data incarnation columns are missing")
	}
	rows, err = query.QueryxContext(ctx, `SELECT con.contype::text,COALESCE(con.conkey::text,''),COALESCE(pg_catalog.pg_get_expr(con.conbin,con.conrelid),''),`+validatedCatalogConstraintSQL+` AND NOT con.condeferrable AND NOT con.condeferred AND con.conislocal AND con.coninhcount=0 AND ((con.contype='p' AND con.connoinherit) OR (con.contype='c' AND NOT con.connoinherit)) FROM pg_catalog.pg_constraint con WHERE (con.conrelid=to_regclass($1) OR con.confrelid=to_regclass($1)) AND `+catalogConstraintNeedsInspectionSQL+` ORDER BY con.contype,con.conkey::text`, table)
	if err != nil {
		return err
	}
	expected := map[string]string{
		"p:{1}": "",
		"c:{1}": `(singleton = 1)`,
		"c:{2}": `((length(value) = 32) AND (translate(value, '0123456789abcdef'::text, ''::text) = ''::text) AND (value <> '00000000000000000000000000000000'::text))`,
	}
	for rows.Next() {
		var kind, keys, expression string
		var valid bool
		if err := rows.Scan(&kind, &keys, &expression, &valid); err != nil {
			rows.Close()
			return err
		}
		want, ok := expected[kind+":"+keys]
		if !ok || !valid || expression != want {
			rows.Close()
			return fmt.Errorf("data incarnation constraints differ from registered shape: %s %s %s", kind, keys, expression)
		}
		delete(expected, kind+":"+keys)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(expected) != 0 {
		return fmt.Errorf("data incarnation constraints are missing")
	}
	var indexes int
	err = query.QueryRowxContext(ctx, `SELECT count(*) FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indexrelid JOIN pg_catalog.pg_am am ON am.oid=c.relam WHERE i.indrelid=to_regclass($1) AND i.indisprimary AND i.indisunique AND i.indisvalid AND i.indisready AND i.indnkeyatts=1 AND i.indnatts=1 AND i.indkey::text='1' AND i.indpred IS NULL AND i.indexprs IS NULL AND NOT i.indnullsnotdistinct AND am.amname='btree' AND i.indoption::text='0' AND COALESCE(cardinality(c.reloptions),0)=0 AND EXISTS(SELECT 1 FROM pg_catalog.pg_opclass op WHERE op.oid=i.indclass[0] AND op.opcdefault) AND (SELECT count(*) FROM pg_catalog.pg_index all_indexes WHERE all_indexes.indrelid=i.indrelid)=1`, table).Scan(&indexes)
	if err != nil {
		return err
	}
	if indexes != 1 {
		return fmt.Errorf("data incarnation identity index differs from registered shape")
	}
	return nil
}
