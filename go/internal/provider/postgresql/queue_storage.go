package postgresql

import (
	"context"
	"database/sql"
	"fmt"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/jmoiron/sqlx"
)

type queueStorageQuery interface {
	SelectContext(context.Context, any, string, ...any) error
	GetContext(context.Context, any, string, ...any) error
}

type queueStorageColumn struct {
	kind       string
	required   bool
	defaultSQL string
}

func (store *queueStore) InspectStorage(ctx context.Context) (queueprovider.StorageInspection, error) {
	transaction, err := store.database.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return queueprovider.StorageInspection{}, err
	}
	defer transaction.Rollback()
	return store.inspectStorage(ctx, transaction)
}

func (store *queueStore) inspectStorage(ctx context.Context, query queueStorageQuery) (queueprovider.StorageInspection, error) {
	result := queueprovider.StorageInspection{}
	var tables []struct {
		Kind, Persistence, Identity string
		Plain                       bool
	}
	const tableSQL = `SELECT c.relkind::text AS kind,c.relpersistence::text AS persistence,c.relreplident::text AS identity,NOT EXISTS(SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attisdropped) AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity AND NOT c.relispartition AND COALESCE(cardinality(c.reloptions),0)=0 AND am.amname='heap' AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_inherits h WHERE h.inhrelid=c.oid OR h.inhparent=c.oid) AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_policy p WHERE p.polrelid=c.oid) AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid=c.oid AND NOT t.tgisinternal) AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_rewrite r WHERE r.ev_class=c.oid) AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint f WHERE f.confrelid=c.oid AND f.contype='f') AS plain FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_am am ON am.oid=c.relam WHERE n.nspname=$1 AND c.relname='golem_queue'`
	if err := query.SelectContext(ctx, &tables, tableSQL, string(store.namespace)); err != nil {
		return result, err
	}
	if len(tables) == 0 {
		var collisions int
		if err := query.GetContext(ctx, &collisions, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname IN ('golem_queue_pkey','golem_queue_claim','golem_queue_dedupe','golem_queue_exclusive','golem_queue_enqueued','golem_queue_history','golem_queue_terminal')`, string(store.namespace)); err != nil {
			return result, err
		}
		if collisions != 0 {
			return result, queueprovider.ErrStorageUnrecognized
		}
		return queueprovider.StorageInspection{Version: queueprovider.StorageAbsent}, nil
	}
	if len(tables) != 1 || tables[0].Kind != "r" || tables[0].Persistence != "p" || tables[0].Identity != "d" || !tables[0].Plain {
		return result, queueprovider.ErrStorageUnrecognized
	}
	var nameCollisions int
	if err := query.GetContext(ctx, &nameCollisions, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname IN ('golem_queue_pkey','golem_queue_claim','golem_queue_dedupe','golem_queue_exclusive','golem_queue_enqueued','golem_queue_history','golem_queue_terminal') AND c.relkind<>'i'`, string(store.namespace)); err != nil {
		return result, err
	}
	if nameCollisions != 0 {
		return result, queueprovider.ErrStorageUnrecognized
	}
	var columns []struct {
		Name, Kind, DefaultSQL string
		Required, Ordinary     bool
	}
	const columnsSQL = `SELECT a.attname AS name,pg_catalog.format_type(a.atttypid,a.atttypmod) AS kind,a.attnotnull AS required,COALESCE(pg_catalog.pg_get_expr(d.adbin,d.adrelid),'') AS defaultsql,NOT a.attisdropped AND a.attidentity='' AND a.attgenerated='' AND a.attndims=0 AND a.attcollation=t.typcollation AS ordinary FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_type t ON t.oid=a.atttypid LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=to_regclass($1) AND a.attnum>0 ORDER BY a.attnum`
	if err := query.SelectContext(ctx, &columns, columnsSQL, store.table()); err != nil {
		return result, err
	}
	expected := map[string]queueStorageColumn{
		"id": {"text", true, ""}, "type": {"text", true, ""}, "payload": {"bytea", true, ""}, "status": {"text", true, ""},
		"attempt_count": {"bigint", true, "0"}, "max_attempts": {"bigint", true, ""}, "available_at": {"timestamp with time zone", true, ""},
		"lease_token": {"text", false, ""}, "lease_until": {"timestamp with time zone", false, ""},
		"resource_name": {"text", false, ""}, "resource_cost": {"bigint", false, ""}, "resource_capacity": {"bigint", false, ""},
		"dedupe_key": {"text", false, ""}, "exclusive_key": {"text", false, ""}, "cancel_requested_at": {"timestamp with time zone", false, ""},
		"last_code": {"text", false, ""}, "enqueued_at": {"timestamp with time zone", true, ""}, "finished_at": {"timestamp with time zone", false, ""}, "updated_at": {"timestamp with time zone", true, ""},
	}
	priority := false
	for _, column := range columns {
		want, known := expected[column.Name]
		if column.Name == "priority" && !priority {
			want, known, priority = queueStorageColumn{"bigint", true, "0"}, true, true
		}
		if !known || !column.Ordinary || column.Kind != want.kind || column.Required != want.required || column.DefaultSQL != want.defaultSQL {
			return result, queueprovider.ErrStorageUnrecognized
		}
		delete(expected, column.Name)
	}
	result.Version = queueprovider.StorageLegacyV088
	if len(expected) != 0 {
		if priority || len(expected) != 3 {
			return result, queueprovider.ErrStorageUnrecognized
		}
		for _, name := range []string{"resource_name", "resource_cost", "resource_capacity"} {
			if _, missing := expected[name]; !missing {
				return result, queueprovider.ErrStorageUnrecognized
			}
		}
		result.Version = queueprovider.StorageLegacyPreResources
	}
	if priority {
		result.Version = queueprovider.StoragePriorityV1
	}
	var constraints []struct {
		Name, Kind, Columns, Expression string
		Valid                           bool
	}
	// PostgreSQL marks primary keys NO INHERIT; our authored CHECK remains
	// inheritable. Require each exact kind instead of relaxing inheritance checks.
	const constraintsSQL = `SELECT con.conname AS name,con.contype::text AS kind,COALESCE((SELECT string_agg(a.attname,',' ORDER BY k.position) FROM unnest(con.conkey) WITH ORDINALITY k(attnum,position) JOIN pg_catalog.pg_attribute a ON a.attrelid=con.conrelid AND a.attnum=k.attnum),'') AS columns,COALESCE(pg_catalog.pg_get_expr(con.conbin,con.conrelid),'') AS expression,` + validatedCatalogConstraintSQL + ` AND NOT con.condeferrable AND NOT con.condeferred AND ((con.contype='p' AND con.connoinherit) OR (con.contype='c' AND NOT con.connoinherit)) AND con.conislocal AND con.coninhcount=0 AS valid FROM pg_catalog.pg_constraint con WHERE con.conrelid=to_regclass($1) AND ` + catalogConstraintNeedsInspectionSQL + ` ORDER BY con.conname`
	if err := query.SelectContext(ctx, &constraints, constraintsSQL, store.table()); err != nil {
		return result, err
	}
	primary, check := false, false
	for _, constraint := range constraints {
		if !constraint.Valid {
			return result, queueprovider.ErrStorageUnrecognized
		}
		switch {
		case constraint.Name == "golem_queue_pkey" && constraint.Kind == "p" && constraint.Columns == "id" && constraint.Expression == "" && !primary:
			primary = true
		case constraint.Name == "golem_queue_priority_check" && constraint.Kind == "c" && constraint.Columns == "priority" && priority && !check:
			if constraint.Expression != `(priority = ANY (ARRAY[0, 1]))` && constraint.Expression != `(priority = ANY (ARRAY[(0)::bigint, (1)::bigint]))` {
				return result, queueprovider.ErrStorageUnrecognized
			}
			check = true
		default:
			return result, queueprovider.ErrStorageUnrecognized
		}
	}
	if !primary || check != priority {
		return result, queueprovider.ErrStorageUnrecognized
	}
	var indexes []struct {
		Name, Table, Method, Columns, Predicate string
		Unique, Valid, Ready, Ordinary          bool
	}
	const indexesSQL = `SELECT ci.relname AS name,ct.relname AS "table",am.amname AS method,i.indisunique AS "unique",i.indisvalid AS valid,i.indisready AS ready,i.indnkeyatts=i.indnatts AND NOT i.indnullsnotdistinct AND i.indexprs IS NULL AND COALESCE(cardinality(ci.reloptions),0)=0 AND NOT EXISTS(SELECT 1 FROM unnest(i.indoption::int2[]) AS opt(value) WHERE opt.value<>0) AND NOT EXISTS(SELECT 1 FROM unnest(i.indclass::oid[]) AS cls(value) JOIN pg_catalog.pg_opclass op ON op.oid=cls.value WHERE NOT op.opcdefault) AND NOT EXISTS(SELECT 1 FROM unnest(i.indkey::int2[],i.indcollation::oid[]) AS col(attnum,collation_oid) JOIN pg_catalog.pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=col.attnum WHERE col.collation_oid<>a.attcollation) AS ordinary,COALESCE((SELECT string_agg(COALESCE(a.attname,'<expression>'),',' ORDER BY k.position) FROM unnest(i.indkey::int2[]) WITH ORDINALITY k(attnum,position) LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.attnum),'') AS columns,COALESCE(pg_catalog.pg_get_expr(i.indpred,i.indrelid),'') AS predicate FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class ci ON ci.oid=i.indexrelid JOIN pg_catalog.pg_class ct ON ct.oid=i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=ci.relnamespace JOIN pg_catalog.pg_am am ON am.oid=ci.relam WHERE n.nspname=$1 AND (ct.relname='golem_queue' OR ci.relname IN ('golem_queue_pkey','golem_queue_claim','golem_queue_dedupe','golem_queue_exclusive','golem_queue_enqueued','golem_queue_history','golem_queue_terminal')) ORDER BY ci.relname`
	if err := query.SelectContext(ctx, &indexes, indexesSQL, string(store.namespace)); err != nil {
		return result, err
	}
	wanted := postgresqlQueueIndexDefinitions()
	if !priority {
		wanted["golem_queue_claim"] = postgresqlQueueIndex{columns: "status,available_at,type"}
	}
	present := map[string]bool{}
	for _, index := range indexes {
		want, known := wanted[index.Name]
		if !known || present[index.Name] || index.Table != "golem_queue" || index.Method != "btree" || !index.Valid || !index.Ready || !index.Ordinary || index.Unique != want.unique || index.Columns != want.columns || index.Predicate != want.predicate {
			return result, queueprovider.ErrStorageUnrecognized
		}
		present[index.Name] = true
	}
	for _, name := range []string{"golem_queue_pkey", "golem_queue_claim", "golem_queue_dedupe", "golem_queue_exclusive"} {
		if !present[name] {
			return result, queueprovider.ErrStorageUnrecognized
		}
	}
	history := 0
	for _, name := range []string{"golem_queue_enqueued", "golem_queue_history", "golem_queue_terminal"} {
		if present[name] {
			history++
		}
	}
	if history != 0 && history != 3 {
		return result, queueprovider.ErrStorageUnrecognized
	}
	result.HistoryIndexes = history == 3
	return result, nil
}

func (store *queueStore) UpgradeStorage(ctx context.Context) (queueprovider.StorageInspection, error) {
	transaction, err := store.database.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return queueprovider.StorageInspection{}, err
	}
	defer transaction.Rollback()
	if err := store.lockStorage(ctx, transaction); err != nil {
		return queueprovider.StorageInspection{}, err
	}
	before, err := store.inspectStorage(ctx, transaction)
	if err != nil {
		return queueprovider.StorageInspection{}, err
	}
	if before.Version == queueprovider.StorageAbsent {
		return queueprovider.StorageInspection{}, fmt.Errorf("QUEUE_STORAGE_ABSENT: no existing queue storage to upgrade")
	}
	if _, err := transaction.ExecContext(ctx, `LOCK TABLE ONLY `+store.table()+` IN ACCESS EXCLUSIVE MODE`); err != nil {
		return queueprovider.StorageInspection{}, err
	}
	before, err = store.inspectStorage(ctx, transaction)
	if err != nil {
		return queueprovider.StorageInspection{}, err
	}
	if before.Version == queueprovider.StorageLegacyPreResources {
		for _, declaration := range []string{`"resource_name" TEXT`, `"resource_cost" BIGINT`, `"resource_capacity" BIGINT`} {
			if _, err := transaction.ExecContext(ctx, `ALTER TABLE `+store.table()+` ADD COLUMN `+declaration); err != nil {
				return queueprovider.StorageInspection{}, err
			}
		}
	}
	if before.Version != queueprovider.StoragePriorityV1 {
		for _, statement := range []string{`ALTER TABLE ` + store.table() + ` ADD COLUMN "priority" BIGINT NOT NULL DEFAULT 0 CHECK ("priority" IN (0,1))`, `DROP INDEX ` + qualified(store.namespace, "golem_queue_claim"), `CREATE INDEX "golem_queue_claim" ON ` + store.table() + ` ("status","priority","available_at","id","type")`} {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return queueprovider.StorageInspection{}, err
			}
		}
	}
	after, err := store.inspectStorage(ctx, transaction)
	if err != nil {
		return queueprovider.StorageInspection{}, err
	}
	if after.Version != queueprovider.StoragePriorityV1 || after.HistoryIndexes != before.HistoryIndexes {
		return queueprovider.StorageInspection{}, queueprovider.ErrStorageUnrecognized
	}
	if err := transaction.Commit(); err != nil {
		return queueprovider.StorageInspection{}, err
	}
	return after, nil
}

// The transaction lock also protects absent storage, which has no table to
// lock yet. Startup and explicit upgrades use the same namespace identity.
func (store *queueStore) lockStorage(ctx context.Context, transaction *sqlx.Tx) error {
	_, err := transaction.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_xact_lock($1)`, queueAdvisoryKey("queue-storage\x00"+string(store.namespace)))
	return err
}
