package sqlite

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/jmoiron/sqlx"
	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

const (
	journalModeWAL    = "wal"
	journalModeMemory = "memory"
	synchronousFull   = 2
)

func (provider *Provider) open(ctx context.Context, dataSourceName string) (*sqlx.DB, CapabilityReport, error) {
	configured, err := configureDataSourceName(dataSourceName)
	if err != nil {
		return nil, CapabilityReport{}, err
	}
	standard, err := driver.Open(configured, initializeProviderConnection)
	if err != nil {
		return nil, CapabilityReport{}, fmt.Errorf("sqlite open: %w", err)
	}
	database := sqlx.NewDb(standard, "sqlite3")
	return provider.verifyOpenedDatabase(ctx, database)
}

func initializeProviderConnection(connection *sqlite3.Conn) error {
	if connection == nil {
		return fmt.Errorf("sqlite open: provider connection initialization received no connection")
	}
	if err := connection.Exec("PRAGMA synchronous=FULL;"); err != nil {
		return fmt.Errorf("sqlite open: provider-owned synchronous configuration failed: %w", err)
	}
	return nil
}

// verifyOpenedDatabase owns every resource after sqlx has allocated the pool.
// Keeping allocation cleanup in this separately testable step lets provider
// evidence observe the real failed-probe path without adding a production or
// public injection seam.
func (provider *Provider) verifyOpenedDatabase(ctx context.Context, database *sqlx.DB) (*sqlx.DB, CapabilityReport, error) {
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	if err := establishJournalMode(ctx, database); err != nil {
		_ = database.Close()
		return nil, CapabilityReport{}, err
	}
	report, err := provider.VerifyPool(ctx, database, 4)
	if err != nil {
		_ = database.Close()
		return nil, CapabilityReport{}, err
	}
	if err := verifyProviderOwnedDurability(ctx, database, 4); err != nil {
		_ = database.Close()
		return nil, CapabilityReport{}, err
	}
	return database, report, nil
}

const (
	journalModeTransitionBudget  = 5 * time.Second
	journalModeTransitionBackoff = 10 * time.Millisecond
)

func establishJournalMode(ctx context.Context, database *sqlx.DB) error {
	connection, err := database.Connx(ctx)
	if err != nil {
		return fmt.Errorf("sqlite open: bootstrap connection: %w", err)
	}
	defer connection.Close()
	required, err := requiredJournalMode(ctx, connection)
	if err != nil {
		return err
	}
	if required == journalModeMemory {
		observed, err := observedJournalMode(ctx, connection)
		if err != nil {
			return err
		}
		if observed != journalModeMemory {
			return fmt.Errorf("sqlite open: named shared in-memory database reported journal mode %q, want %q", observed, journalModeMemory)
		}
		return nil
	}
	deadline := time.Now().Add(journalModeTransitionBudget)
	for attempt := 0; ; attempt++ {
		established, err := observedJournalMode(ctx, connection)
		if err != nil {
			return err
		}
		if established == journalModeWAL {
			return nil
		}
		var applied string
		transitionErr := connection.GetContext(ctx, &applied, "PRAGMA journal_mode=WAL")
		if transitionErr == nil && normalizeJournalMode(applied) == journalModeWAL {
			return nil
		}
		if time.Now().After(deadline) {
			if transitionErr != nil {
				return fmt.Errorf("sqlite open: write-ahead logging transition failed: %w", transitionErr)
			}
			return fmt.Errorf("sqlite open: write-ahead logging transition reported journal mode %q, want %q", applied, journalModeWAL)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sqlite open: write-ahead logging transition failed: %w", ctx.Err())
		case <-time.After(journalModeTransitionBackoff):
		}
	}
}

func verifyProviderOwnedDurability(ctx context.Context, database *sqlx.DB, width int) error {
	if database == nil || width < 1 || database.Stats().InUse != 0 {
		return fmt.Errorf("sqlite durability verification requires an idle bounded database")
	}
	waitCount := database.Stats().WaitCount
	connections := make([]*sqlx.Conn, 0, width)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index := 0; index < width; index++ {
		connection, err := database.Connx(ctx)
		if err != nil {
			return fmt.Errorf("sqlite durability verification connection %d: %w", index, err)
		}
		connections = append(connections, connection)
		if database.Stats().WaitCount != waitCount {
			return fmt.Errorf("sqlite durability verification observed concurrent pool use")
		}
	}
	if database.Stats().InUse != width {
		return fmt.Errorf("sqlite durability verification did not exclusively hold every slot")
	}
	for index, connection := range connections {
		required, err := requiredJournalMode(ctx, connection)
		if err != nil {
			return err
		}
		observed, err := observedJournalMode(ctx, connection)
		if err != nil {
			return err
		}
		if observed != required {
			return fmt.Errorf("sqlite durability verification pooled connection %d: PRAGMA journal_mode=%q, want %q", index+1, observed, required)
		}
		var synchronous int
		if err := connection.GetContext(ctx, &synchronous, "PRAGMA synchronous"); err != nil {
			return fmt.Errorf("sqlite durability verification connection %d synchronous: %w", index+1, err)
		}
		if synchronous != synchronousFull {
			return fmt.Errorf("sqlite durability verification pooled connection %d: PRAGMA synchronous=%d, want %d", index+1, synchronous, synchronousFull)
		}
	}
	if database.Stats().WaitCount != waitCount {
		return fmt.Errorf("sqlite durability verification observed concurrent pool use")
	}
	return nil
}

func requiredJournalMode(ctx context.Context, connection *sqlx.Conn) (string, error) {
	var location string
	if err := connection.GetContext(ctx, &location, "SELECT file FROM pragma_database_list WHERE name = 'main'"); err != nil {
		return "", fmt.Errorf("sqlite open: main database location: %w", err)
	}
	if strings.TrimSpace(location) == "" {
		return journalModeMemory, nil
	}
	return journalModeWAL, nil
}

func observedJournalMode(ctx context.Context, connection *sqlx.Conn) (string, error) {
	var mode string
	if err := connection.GetContext(ctx, &mode, "PRAGMA journal_mode"); err != nil {
		return "", fmt.Errorf("sqlite open: journal mode: %w", err)
	}
	return normalizeJournalMode(mode), nil
}

func normalizeJournalMode(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func configureDataSourceName(dataSourceName string) (string, error) {
	if strings.TrimSpace(dataSourceName) == "" {
		return "", fmt.Errorf("sqlite open: data source name is empty")
	}
	if strings.Contains(dataSourceName, "#") {
		return "", fmt.Errorf("sqlite open: data source name fragments are unsupported")
	}
	base, rawQuery, hasQuery := strings.Cut(dataSourceName, "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("sqlite open: data source name query is invalid")
	}

	normalized := make(map[string][]string, len(query))
	for key, values := range query {
		canonicalKey := strings.ToLower(strings.TrimSpace(key))
		normalized[canonicalKey] = append(normalized[canonicalKey], values...)
		if canonicalKey == "_txlock" {
			return "", fmt.Errorf("sqlite open: transaction locking mode is provider-owned")
		}
		if canonicalKey != "_pragma" {
			continue
		}
		for _, value := range values {
			if providerOwnedPragma(value) {
				return "", fmt.Errorf("sqlite open: journal_mode, synchronous, foreign_keys and busy_timeout pragmas are provider-owned")
			}
		}
	}

	if dataSourceName == ":memory:" || strings.EqualFold(strings.TrimPrefix(base, "file:"), ":memory:") {
		return "", fmt.Errorf("sqlite open: private in-memory databases are incompatible with the verified multi-connection pool; use a named file: DSN with mode=memory&cache=shared")
	}
	modes, modePresent := normalizedQueryValue(normalized, "mode")
	caches, cachePresent := normalizedQueryValue(normalized, "cache")
	if modePresent && len(modes) != 1 {
		return "", fmt.Errorf("sqlite open: duplicate or conflicting mode parameters are unsupported")
	}
	if cachePresent && len(caches) != 1 {
		return "", fmt.Errorf("sqlite open: duplicate or conflicting cache parameters are unsupported")
	}
	if modePresent && modes[0] == "ro" {
		return "", fmt.Errorf("sqlite open: read-only databases cannot hold the provider-owned write-ahead logging profile")
	}
	if immutable, immutablePresent := normalizedQueryValue(normalized, "immutable"); immutablePresent {
		for _, value := range immutable {
			if value != "0" && value != "false" && value != "no" && value != "off" {
				return "", fmt.Errorf("sqlite open: immutable databases cannot hold the provider-owned write-ahead logging profile")
			}
		}
	}
	if modePresent && modes[0] == "memory" {
		name := strings.TrimPrefix(base, "file:")
		if !strings.HasPrefix(base, "file:") || strings.Trim(name, "/") == "" || strings.EqualFold(name, ":memory:") || !cachePresent || caches[0] != "shared" {
			return "", fmt.Errorf("sqlite open: private in-memory databases are incompatible with the verified multi-connection pool; use a named file: DSN with mode=memory&cache=shared")
		}
	}
	separator := "?"
	if hasQuery && rawQuery != "" {
		separator = "&"
	} else if hasQuery {
		separator = ""
	}
	if !strings.HasPrefix(base, "file:") {
		dataSourceName = "file:" + dataSourceName
	}
	return dataSourceName + separator + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate", nil
}

func normalizedQueryValue(query map[string][]string, key string) ([]string, bool) {
	values, ok := query[key]
	if !ok {
		return nil, false
	}
	normalized := make([]string, len(values))
	for index, value := range values {
		normalized[index] = strings.ToLower(strings.TrimSpace(value))
	}
	return normalized, true
}

func providerOwnedPragma(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if boundary := strings.IndexAny(value, "(= \t\r\n"); boundary >= 0 {
		value = strings.TrimSpace(value[:boundary])
	}
	if qualifier := strings.LastIndex(value, "."); qualifier >= 0 {
		value = strings.TrimSpace(value[qualifier+1:])
	}
	value = strings.Trim(value, "\"'`[]")
	switch value {
	case "journal_mode", "synchronous", "foreign_keys", "busy_timeout":
		return true
	default:
		return false
	}
}

func (provider *Provider) probe(ctx context.Context, database *sqlx.DB) (CapabilityReport, error) {
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
		PolicyBinaryText: firstReport.PolicyBinaryText && secondReport.PolicyBinaryText,
		PolicyASCIIText:  firstReport.PolicyASCIIText && secondReport.PolicyASCIIText,
		PolicyExactJSON:  firstReport.PolicyExactJSON && secondReport.PolicyExactJSON,
		PolicyScalarList: firstReport.PolicyScalarList && secondReport.PolicyScalarList,
		PolicyRelation:   firstReport.PolicyRelation && secondReport.PolicyRelation,
		AnalyticsExact:   firstReport.AnalyticsExact && secondReport.AnalyticsExact,
		Vec0:             firstReport.Vec0 && secondReport.Vec0,
		VecVersion:       firstReport.VecVersion,
	}, nil
}

// VerifyPool proves every sealed pool slot while holding all slots at once.
// A pre-existing borrower or acquisition wait invalidates startup because the
// complete connection state could not be observed atomically.
func (*Provider) VerifyPool(ctx context.Context, database *sqlx.DB, width int) (CapabilityReport, error) {
	if database == nil || width < 1 || database.Stats().InUse != 0 {
		return CapabilityReport{}, fmt.Errorf("sqlite pool verification requires an idle bounded database")
	}
	waitCount := database.Stats().WaitCount
	connections := make([]*sqlx.Conn, 0, width)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for index := 0; index < width; index++ {
		connection, err := database.Connx(ctx)
		if err != nil {
			return CapabilityReport{}, fmt.Errorf("sqlite pool verification connection %d: %w", index, err)
		}
		connections = append(connections, connection)
		if database.Stats().WaitCount != waitCount {
			return CapabilityReport{}, fmt.Errorf("sqlite pool verification observed concurrent pool use")
		}
	}
	if database.Stats().InUse != width {
		return CapabilityReport{}, fmt.Errorf("sqlite pool verification did not exclusively hold every slot")
	}
	var combined CapabilityReport
	for index, connection := range connections {
		report, err := probeConnection(ctx, connection, strconv.Itoa(index+1))
		if err != nil {
			return CapabilityReport{}, err
		}
		if index == 0 {
			combined = report
			continue
		}
		if combined.Version != report.Version {
			return CapabilityReport{}, fmt.Errorf("sqlite pool verification observed different server versions")
		}
		combined.ForeignKeys = combined.ForeignKeys && report.ForeignKeys
		combined.JSON1 = combined.JSON1 && report.JSON1
		combined.GeneratedColumns = combined.GeneratedColumns && report.GeneratedColumns
		combined.PolicyBinaryText = combined.PolicyBinaryText && report.PolicyBinaryText
		combined.PolicyASCIIText = combined.PolicyASCIIText && report.PolicyASCIIText
		combined.PolicyExactJSON = combined.PolicyExactJSON && report.PolicyExactJSON
		combined.PolicyScalarList = combined.PolicyScalarList && report.PolicyScalarList
		combined.PolicyRelation = combined.PolicyRelation && report.PolicyRelation
		combined.AnalyticsExact = combined.AnalyticsExact && report.AnalyticsExact
		combined.Vec0 = combined.Vec0 && report.Vec0
		if combined.VecVersion != report.VecVersion {
			return CapabilityReport{}, fmt.Errorf("sqlite pool verification observed different sqlite-vec versions")
		}
	}
	if database.Stats().WaitCount != waitCount {
		return CapabilityReport{}, fmt.Errorf("sqlite pool verification observed concurrent pool use")
	}
	return combined, nil
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
	var busyTimeout int
	if err := connection.GetContext(ctx, &busyTimeout, "PRAGMA busy_timeout"); err != nil {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s connection busy_timeout: %w", label, err)
	}
	if busyTimeout != 5000 {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s pooled connection: PRAGMA busy_timeout=%d, want 5000", label, busyTimeout)
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
	var ascii string
	if err := connection.GetContext(ctx, &ascii, "SELECT golem_policy_ascii_fold('AbÅ')"); err != nil || ascii != "abÅ" {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s connection policy ASCII fold unavailable: value=%q error=%v", label, ascii, err)
	}
	var listExact, jsonExact int
	if err := connection.GetContext(ctx, &listExact, "SELECT golem_policy_list('[9007199254740993]', '[9007199254740993]', '4:0:0', 101)"); err != nil || listExact != 1 {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s connection policy scalar-list exactness unavailable: value=%d error=%v", label, listExact, err)
	}
	if err := connection.GetContext(ctx, &jsonExact, "SELECT golem_policy_json('{\"n\":9007199254740993}', '[[\"k\",\"n\"]]', '9007199254740993', 203, 1, 2)"); err != nil || jsonExact != 1 {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s connection policy JSON exactness unavailable: value=%d error=%v", label, jsonExact, err)
	}
	var integerSum, decimalSum, decimalAverage string
	var comparison, ordering int
	if err := connection.QueryRowxContext(ctx, analyticsProbeSQL()).Scan(&integerSum, &decimalSum, &decimalAverage, &comparison, &ordering); err != nil || !validAnalyticsProbe(integerSum, decimalSum, decimalAverage, comparison, ordering) {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s connection analytical exactness unavailable: integer=%q decimalSum=%q decimalAverage=%q comparison=%d ordering=%d error=%v", label, integerSum, decimalSum, decimalAverage, comparison, ordering, err)
	}
	var vecVersion string
	if err := connection.GetContext(ctx, &vecVersion, "SELECT vec_version()"); err != nil || vecVersion == "" {
		return CapabilityReport{}, fmt.Errorf("sqlite probe %s connection sqlite-vec unavailable", label)
	}
	return CapabilityReport{Version: version, ForeignKeys: true, JSON1: true, GeneratedColumns: true, PolicyBinaryText: true, PolicyASCIIText: true, PolicyExactJSON: true, PolicyScalarList: true, PolicyRelation: true, AnalyticsExact: true, Vec0: true, VecVersion: vecVersion}, nil
}

// PolicyCapabilityProof measures the complete SQLite P2 policy function set on
// two distinct pooled connections, then binds that result to the caller's
// already-validated logical schema fingerprint.
func (provider *Provider) PolicyCapabilityProof(ctx context.Context, database *sqlx.DB, schemaFingerprint [32]byte) (policysql.CapabilityProof, error) {
	if schemaFingerprint == ([32]byte{}) {
		return policysql.CapabilityProof{}, fmt.Errorf("sqlite policy capability proof: schema fingerprint is required")
	}
	report, err := provider.probe(ctx, database)
	if err != nil {
		return policysql.CapabilityProof{}, err
	}
	if !report.PolicyBinaryText || !report.PolicyASCIIText || !report.PolicyExactJSON || !report.PolicyScalarList || !report.PolicyRelation {
		return policysql.CapabilityProof{}, fmt.Errorf("sqlite policy capability proof: incomplete runtime probe")
	}
	return policysql.NewCapabilityProof(policyir.ProviderSQLite, schemaFingerprint,
		policyir.CapabilityBinaryText,
		policyir.CapabilityASCIIInsensitiveText,
		policyir.CapabilityExactJSON,
		policyir.CapabilityScalarListJSON,
		policyir.CapabilityRelationCorrelation,
	)
}

// VerifiedPoolPolicyCapabilityProof binds a full sealed-pool verification to
// the logical schema fingerprint consumed by the runtime policy compiler.
func (provider *Provider) VerifiedPoolPolicyCapabilityProof(ctx context.Context, database *sqlx.DB, schemaFingerprint [32]byte, width int) (policysql.CapabilityProof, error) {
	if schemaFingerprint == ([32]byte{}) {
		return policysql.CapabilityProof{}, fmt.Errorf("sqlite policy capability proof: schema fingerprint is required")
	}
	report, err := provider.VerifyPool(ctx, database, width)
	if err != nil {
		return policysql.CapabilityProof{}, err
	}
	if !report.PolicyBinaryText || !report.PolicyASCIIText || !report.PolicyExactJSON || !report.PolicyScalarList || !report.PolicyRelation {
		return policysql.CapabilityProof{}, fmt.Errorf("sqlite policy capability proof: incomplete runtime probe")
	}
	return policysql.NewCapabilityProof(policyir.ProviderSQLite, schemaFingerprint,
		policyir.CapabilityBinaryText,
		policyir.CapabilityASCIIInsensitiveText,
		policyir.CapabilityExactJSON,
		policyir.CapabilityScalarListJSON,
		policyir.CapabilityRelationCorrelation,
	)
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
	normalized, err := physical.Normalize(schema)
	if err != nil {
		return err
	}
	if _, err := provider.probe(ctx, database); err != nil {
		return err
	}
	script, err := provider.renderInitial(normalized)
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
	allowed := make(map[string]struct{}, len(normalized.Unmanaged))
	for _, object := range normalized.Unmanaged {
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
	actual, err := provider.introspectCatalog(ctx, transaction, normalized)
	if err != nil {
		return fmt.Errorf("sqlite initial apply precommit verification: %w", err)
	}
	wantPhysical, _ := physical.PhysicalFingerprint(normalized)
	gotPhysical, _ := physical.PhysicalFingerprint(actual)
	if gotPhysical != wantPhysical {
		return fmt.Errorf("sqlite initial apply precommit physical fingerprint mismatch")
	}
	wantSystem, _ := physical.SystemFingerprint(normalized.Provider, normalized.System)
	gotSystem, _ := physical.SystemFingerprint(actual.Provider, actual.System)
	if gotSystem != wantSystem {
		return fmt.Errorf("sqlite initial apply precommit system fingerprint mismatch")
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("sqlite initial apply commit: %w", err)
	}
	return nil
}
