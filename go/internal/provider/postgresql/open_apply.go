package postgresql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

func (provider *Provider) open(ctx context.Context, dataSourceName string) (*sqlx.DB, CapabilityReport, error) {
	config, err := driverConfig(dataSourceName)
	if err != nil {
		return nil, CapabilityReport{}, fmt.Errorf("postgresql open: %w", err)
	}
	database := sqlx.NewDb(stdlib.OpenDB(*config), "pgx")
	return provider.verifyOpenedDatabase(ctx, database)
}

// verifyOpenedDatabase owns every resource after the driver pool has been
// allocated. It is intentionally private but separately testable so a removed
// cleanup call is caught without exposing a provider injection API.
func (provider *Provider) verifyOpenedDatabase(ctx context.Context, database *sqlx.DB) (*sqlx.DB, CapabilityReport, error) {
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, CapabilityReport{}, fmt.Errorf("postgresql ping: %w", err)
	}
	report, err := probeCapabilities(ctx, database)
	if err != nil {
		database.Close()
		return nil, CapabilityReport{}, err
	}
	if report.Version.Major < provider.Manifest().MinimumVersion.Major {
		database.Close()
		return nil, CapabilityReport{}, fmt.Errorf("postgresql server %d.%d is below required 15.0", report.Version.Major, report.Version.Minor)
	}
	if !report.JSONB || !report.GeneratedColumns || !report.AdvisoryLocks {
		database.Close()
		return nil, CapabilityReport{}, fmt.Errorf("postgresql required capabilities missing: jsonb=%t generatedColumns=%t advisoryLocks=%t", report.JSONB, report.GeneratedColumns, report.AdvisoryLocks)
	}
	if !report.BinaryText || !report.ASCIIInsensitive || !report.ExactJSON || !report.ScalarListJSON || !report.RelationCorrelation {
		database.Close()
		return nil, CapabilityReport{}, fmt.Errorf("postgresql policy capabilities missing: binary=%t ascii=%t exactJSON=%t scalarListJSON=%t relation=%t", report.BinaryText, report.ASCIIInsensitive, report.ExactJSON, report.ScalarListJSON, report.RelationCorrelation)
	}
	if !report.AnalyticsExact {
		database.Close()
		return nil, CapabilityReport{}, fmt.Errorf("postgresql analytical exact-arithmetic capability is missing")
	}
	if !report.SessionSettings {
		database.Close()
		return nil, CapabilityReport{}, fmt.Errorf("postgresql provider-owned session settings are unavailable")
	}
	return database, report, nil
}

func driverConfig(dataSourceName string) (*pgx.ConnConfig, error) {
	if strings.TrimSpace(dataSourceName) == "" {
		return nil, fmt.Errorf("PostgreSQL DSN is required; ambient libpq environment is not accepted")
	}
	config, err := pgx.ParseConfig(dataSourceName)
	if err != nil {
		return nil, err
	}
	if config.RuntimeParams == nil {
		config.RuntimeParams = map[string]string{}
	}
	config.RuntimeParams["timezone"] = "UTC"
	config.RuntimeParams["datestyle"] = "ISO, YMD"
	config.RuntimeParams["intervalstyle"] = "iso_8601"
	config.RuntimeParams["standard_conforming_strings"] = "on"
	return config, nil
}

type rowQueryer interface {
	QueryRowxContext(context.Context, string, ...any) *sqlx.Row
}

func probeCapabilities(ctx context.Context, query rowQueryer) (CapabilityReport, error) {
	const statement = `SELECT
current_setting('server_version_num')::integer,
EXISTS (
  SELECT 1 FROM pg_catalog.pg_type t
  JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
  WHERE n.nspname = 'pg_catalog' AND t.typname = 'jsonb'
),
EXISTS (
  SELECT 1 FROM pg_catalog.pg_attribute a
  JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = 'pg_catalog' AND c.relname = 'pg_attribute' AND a.attname = 'attgenerated'
),
EXISTS (
  SELECT 1 FROM pg_catalog.pg_proc p
  JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
  WHERE n.nspname = 'pg_catalog' AND p.proname = 'pg_advisory_xact_lock'
),
('A' COLLATE "C") < ('a' COLLATE "C"),
pg_catalog.translate('ÅAZ', 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz') = 'Åaz',
('9007199254740993'::jsonb <> '9007199254740992'::jsonb)
  AND pg_catalog.jsonb_typeof('9007199254740993'::jsonb) = 'number',
pg_catalog.jsonb_array_length('[1,2,1]'::jsonb) = 3
  AND ('[1,2]'::jsonb <> '[2,1]'::jsonb),
EXISTS (SELECT 1 FROM (VALUES (1)) AS parent(id) WHERE EXISTS (SELECT 1 FROM (VALUES (1)) AS child(parent_id) WHERE child.parent_id = parent.id))`
	const analytics = `,
(
  (SELECT pg_catalog.sum(value)::text = '18446744073709551614' FROM (VALUES (9223372036854775807::bigint), (9223372036854775807::bigint)) AS exact_integer(value))
  AND (SELECT pg_catalog.round(pg_catalog.avg(value), 0)::text = '2' FROM (VALUES (1::numeric), (2::numeric)) AS positive_half(value))
  AND (SELECT pg_catalog.round(pg_catalog.avg(value), 0)::text = '-2' FROM (VALUES (-1::numeric), (-2::numeric)) AS negative_half(value))
)`
	const session = `,
current_setting('TimeZone') = 'UTC'
  AND current_setting('DateStyle') = 'ISO, YMD'
  AND current_setting('IntervalStyle') = 'iso_8601'
  AND current_setting('standard_conforming_strings') = 'on'`
	statementWithAnalytics := statement + analytics + session
	var version int
	var report CapabilityReport
	if err := query.QueryRowxContext(ctx, statementWithAnalytics).Scan(&version, &report.JSONB, &report.GeneratedColumns, &report.AdvisoryLocks, &report.BinaryText, &report.ASCIIInsensitive, &report.ExactJSON, &report.ScalarListJSON, &report.RelationCorrelation, &report.AnalyticsExact, &report.SessionSettings); err != nil {
		return CapabilityReport{}, fmt.Errorf("postgresql capability probe: %w", err)
	}
	report.Version = physical.Version{Major: uint32(version / 10000), Minor: uint32((version / 100) % 100), Patch: uint32(version % 100)}
	return report, nil
}

func (provider *Provider) applyInitial(ctx context.Context, database *sqlx.DB, schema physical.PhysicalSchema) (err error) {
	normalized, err := physical.Normalize(schema)
	if err != nil {
		return err
	}
	if database == nil {
		return fmt.Errorf("postgresql apply initial: nil database")
	}
	script, err := provider.renderInitial(normalized)
	if err != nil {
		return err
	}
	tx, err := database.BeginTxx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgresql begin migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	lockKey := advisoryKey(normalized)
	if _, err = tx.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_xact_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("postgresql advisory migration lock: %w", err)
	}
	if err = requireBlankManagedState(ctx, tx, normalized); err != nil {
		return err
	}
	for _, statement := range script.statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("postgresql apply initial DDL: %w", err)
		}
	}
	actual, err := provider.introspectQuery(ctx, tx, normalized)
	if err != nil {
		return fmt.Errorf("postgresql post-apply introspection: %w", err)
	}
	if err = compareFingerprints(normalized, actual); err != nil {
		return fmt.Errorf("postgresql post-apply verification: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("postgresql commit initial migration: %w", err)
	}
	return nil
}

func requireBlankManagedState(ctx context.Context, tx *sqlx.Tx, schema physical.PhysicalSchema) error {
	allowed := map[string]bool{}
	for _, item := range schema.Unmanaged {
		allowed[item.Kind+"\x00"+string(item.Name)] = true
	}
	if err := requireBlankNamespace(ctx, tx, schema.Namespace.Name, allowed); err != nil {
		return err
	}
	return requireBlankNamespace(ctx, tx, schema.System.Namespace.Name, nil)
}

func requireBlankNamespace(ctx context.Context, tx *sqlx.Tx, namespace physical.PhysicalName, allowed map[string]bool) error {
	rows, err := tx.QueryxContext(ctx, `SELECT c.relkind::text, c.relname
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','S')
ORDER BY c.relkind, c.relname`, string(namespace))
	if err != nil {
		return fmt.Errorf("postgresql inspect blank managed namespace: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return err
		}
		semanticKind := map[string]string{"r": "table", "p": "table", "v": "view", "m": "materialized_view", "S": "sequence"}[kind]
		if allowed == nil || !allowed[semanticKind+"\x00"+name] {
			return fmt.Errorf("postgresql initial migration requires blank managed namespace; found %s %q", semanticKind, name)
		}
	}
	return rows.Err()
}

func advisoryKey(schema physical.PhysicalSchema) int64 {
	hash := sha256.Sum256([]byte("golem:postgresql:migration-lock:v1\x00" + string(schema.Namespace.Name)))
	return int64(binary.BigEndian.Uint64(hash[:8]))
}

func compareFingerprints(expected, actual physical.PhysicalSchema) error {
	expectedPhysical, err := physical.HistoricalPhysicalFingerprint(expected)
	if err != nil {
		return err
	}
	actualPhysical, err := physical.HistoricalPhysicalFingerprint(actual)
	if err != nil {
		return err
	}
	if expectedPhysical != actualPhysical {
		return fmt.Errorf("application physical fingerprint mismatch: expected %s actual %s", expectedPhysical, actualPhysical)
	}
	expectedSystem, err := physical.HistoricalSystemFingerprint(expected)
	if err != nil {
		return err
	}
	actualSystem, err := physical.HistoricalSystemFingerprint(actual)
	if err != nil {
		return err
	}
	if expectedSystem != actualSystem {
		return fmt.Errorf("system physical fingerprint mismatch: expected %s actual %s", expectedSystem, actualSystem)
	}
	return nil
}
