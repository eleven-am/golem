package sqlite

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func (provider *Provider) open(ctx context.Context, dataSourceName string) (*sqlx.DB, CapabilityReport, error) {
	configured, err := configureDataSourceName(dataSourceName)
	if err != nil {
		return nil, CapabilityReport{}, err
	}
	database, err := sqlx.Open("sqlite", configured)
	if err != nil {
		return nil, CapabilityReport{}, fmt.Errorf("sqlite open: %w", err)
	}
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	report, err := provider.probe(ctx, database)
	if err != nil {
		_ = database.Close()
		return nil, CapabilityReport{}, err
	}
	return database, report, nil
}

func configureDataSourceName(dataSourceName string) (string, error) {
	if dataSourceName == "" {
		return "", fmt.Errorf("sqlite open: data source name is empty")
	}
	lower := strings.ToLower(dataSourceName)
	if (dataSourceName == ":memory:" || strings.Contains(lower, "mode=memory")) && !strings.Contains(lower, "cache=shared") {
		return "", fmt.Errorf("sqlite open: private in-memory databases are incompatible with the verified multi-connection pool; use a named file: DSN with mode=memory&cache=shared")
	}
	if strings.Contains(lower, "foreign_keys") || strings.Contains(lower, "busy_timeout") {
		return "", fmt.Errorf("sqlite open: foreign_keys and busy_timeout pragmas are provider-owned")
	}
	separator := "?"
	if strings.Contains(dataSourceName, "?") {
		separator = "&"
	}
	return dataSourceName + separator + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate", nil
}

func (*Provider) probe(ctx context.Context, database *sqlx.DB) (CapabilityReport, error) {
	if database == nil {
		return CapabilityReport{}, fmt.Errorf("sqlite probe: database is nil")
	}
	first, err := database.Connx(ctx)
	if err != nil {
		return CapabilityReport{}, fmt.Errorf("sqlite probe first pooled connection: %w", err)
	}
	defer first.Close()
	second, err := database.Connx(ctx)
	if err != nil {
		return CapabilityReport{}, fmt.Errorf("sqlite probe second pooled connection: %w", err)
	}
	defer second.Close()
	firstReport, err := probeConnection(ctx, first, "first")
	if err != nil {
		return CapabilityReport{}, err
	}
	secondReport, err := probeConnection(ctx, second, "second")
	if err != nil {
		return CapabilityReport{}, err
	}
	if firstReport.Version != secondReport.Version {
		return CapabilityReport{}, fmt.Errorf("sqlite probe: pooled connections report different versions")
	}
	return CapabilityReport{
		Version:          firstReport.Version,
		ForeignKeys:      firstReport.ForeignKeys && secondReport.ForeignKeys,
		JSON1:            firstReport.JSON1 && secondReport.JSON1,
		GeneratedColumns: firstReport.GeneratedColumns && secondReport.GeneratedColumns,
	}, nil
}

func probeConnection(ctx context.Context, connection *sqlx.Conn, label string) (CapabilityReport, error) {
	var versionText string
	if err := connection.GetContext(ctx, &versionText, "SELECT sqlite_version()"); err != nil {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s connection version: %w", label, err)
	}
	version, err := parseVersion(versionText)
	if err != nil {
		return CapabilityReport{}, err
	}
	if compareVersion(version, physical.Version{Major: 3, Minor: 38}) < 0 {
		return CapabilityReport{}, fmt.Errorf("sqlite probe: version %s is below required 3.38.0", versionText)
	}
	var foreignKeys int
	if err := connection.GetContext(ctx, &foreignKeys, "PRAGMA foreign_keys"); err != nil {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s connection foreign_keys: %w", label, err)
	}
	if foreignKeys != 1 {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s pooled connection: PRAGMA foreign_keys=%d, want 1", label, foreignKeys)
	}
	var jsonValid int
	if err := connection.GetContext(ctx, &jsonValid, "SELECT json_valid('{\"golem\":1}')"); err != nil || jsonValid != 1 {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s connection JSON1 unavailable: value=%d error=%v", label, jsonValid, err)
	}
	table := quote(physical.PhysicalName("_golem_probe_generated"))
	if _, err := connection.ExecContext(ctx, "DROP TABLE IF EXISTS temp."+table); err != nil {
		return CapabilityReport{}, fmt.Errorf("sqlite probe generated cleanup: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "CREATE TEMP TABLE "+table+" ("+quote("source")+" INTEGER, "+quote("derived")+" INTEGER GENERATED ALWAYS AS ("+quote("source")+" + 1) STORED)"); err != nil {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s connection generated columns unavailable: %w", label, err)
	}
	if _, err := connection.ExecContext(ctx, "DROP TABLE temp."+table); err != nil {
		return CapabilityReport{}, fmt.Errorf("sqlite probe generated cleanup: %w", err)
	}
	return CapabilityReport{Version: version, ForeignKeys: true, JSON1: true, GeneratedColumns: true}, nil
}

func parseVersion(value string) (physical.Version, error) {
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return physical.Version{}, fmt.Errorf("sqlite probe: invalid version %q", value)
	}
	values := [3]uint64{}
	for index, part := range parts {
		parsed, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return physical.Version{}, fmt.Errorf("sqlite probe: invalid version %q", value)
		}
		values[index] = parsed
	}
	return physical.Version{Major: uint32(values[0]), Minor: uint32(values[1]), Patch: uint32(values[2])}, nil
}

func compareVersion(left, right physical.Version) int {
	if left.Major != right.Major {
		if left.Major < right.Major {
			return -1
		}
		return 1
	}
	if left.Minor != right.Minor {
		if left.Minor < right.Minor {
			return -1
		}
		return 1
	}
	if left.Patch != right.Patch {
		if left.Patch < right.Patch {
			return -1
		}
		return 1
	}
	return 0
}

func (provider *Provider) applyInitial(ctx context.Context, database *sqlx.DB, schema physical.PhysicalSchema) error {
	if _, err := provider.probe(ctx, database); err != nil {
		return err
	}
	script, err := provider.renderInitial(schema)
	if err != nil {
		return err
	}
	transaction, err := database.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite initial apply begin: %w", err)
	}
	defer transaction.Rollback()
	var existingRows []schemaRow
	if err := transaction.SelectContext(ctx, &existingRows, "SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' AND type IN ('table','index','view','trigger')"); err != nil {
		return fmt.Errorf("sqlite initial apply inspect blank schema: %w", err)
	}
	allowed := make(map[string]struct{}, len(schema.Unmanaged))
	for _, object := range schema.Unmanaged {
		allowed[object.Kind+"\x00"+string(object.Name)] = struct{}{}
	}
	for _, row := range existingRows {
		if _, ok := allowed[row.Type+"\x00"+row.Name]; !ok {
			return fmt.Errorf("sqlite initial apply requires a blank managed schema; unexpected %s %s", row.Type, row.Name)
		}
	}
	for index, statement := range script.statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlite initial apply statement %d: %w", index, err)
		}
	}
	rows, err := transaction.QueryxContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("sqlite initial apply foreign_key_check: %w", err)
	}
	if rows.Next() {
		_ = rows.Close()
		return fmt.Errorf("sqlite initial apply foreign_key_check reported a violation")
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("sqlite initial apply foreign_key_check close: %w", err)
	}
	actual, err := provider.introspectCatalog(ctx, transaction, schema)
	if err != nil {
		return fmt.Errorf("sqlite initial apply precommit verification: %w", err)
	}
	wantPhysical, _ := physical.PhysicalFingerprint(schema)
	gotPhysical, _ := physical.PhysicalFingerprint(actual)
	if gotPhysical != wantPhysical {
		return fmt.Errorf("sqlite initial apply precommit physical fingerprint mismatch")
	}
	wantSystem, _ := physical.SystemFingerprint(schema.Provider, schema.System)
	gotSystem, _ := physical.SystemFingerprint(actual.Provider, actual.System)
	if gotSystem != wantSystem {
		return fmt.Errorf("sqlite initial apply precommit system fingerprint mismatch")
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("sqlite initial apply commit: %w", err)
	}
	return nil
}
