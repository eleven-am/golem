// Package handle owns the sealed, provider-verified database state used by the
// public provider packages. It is internal so applications cannot construct or
// classify provider handles through this implementation boundary.
package handle

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/golem"
	internalpostgresql "github.com/eleven-am/golem/go/internal/provider/postgresql"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

const (
	postgreSQLDefaultMaximumOpen = 16
	postgreSQLDefaultMaximumIdle = 4
	postgreSQLMaximumPoolWidth   = 256

	postgreSQLDefaultConnectionMaximumLifetime = 30 * time.Minute
	postgreSQLDefaultConnectionMaximumIdleTime = 5 * time.Minute
	postgreSQLMinimumConnectionDuration        = time.Second
	postgreSQLMaximumConnectionDuration        = 24 * time.Hour
)

type Code string

const (
	CodeConfig      Code = "PROVIDER_CONFIG"
	CodeOpen        Code = "PROVIDER_OPEN"
	CodeClose       Code = "PROVIDER_CLOSE"
	CodeMaintenance Code = "PROVIDER_MAINTENANCE"
)

type providerError struct {
	code    Code
	message string
}

func failure(code Code, message string) error {
	return &providerError{code: code, message: message}
}

func (failure *providerError) Error() string {
	if failure == nil {
		return "provider operation failed"
	}
	return failure.message
}

func CodeOf(err error) (Code, bool) {
	var classified *providerError
	if !errors.As(err, &classified) {
		return "", false
	}
	return classified.code, classified.code == CodeConfig || classified.code == CodeOpen || classified.code == CodeClose || classified.code == CodeMaintenance
}

type Version struct {
	Major uint32
	Minor uint32
	Patch uint32
}

type Capabilities struct {
	provider golem.Provider
	version  Version
	features []string
}

func (value Capabilities) Provider() golem.Provider { return value.provider }
func (value Capabilities) ServerVersion() Version   { return value.version }
func (value Capabilities) Features() []string       { return append([]string(nil), value.features...) }

type PoolStatus struct {
	maximumOpen               int
	maximumIdle               int
	connectionMaximumLifetime time.Duration
	connectionMaximumIdleTime time.Duration
}

func (value PoolStatus) MaximumOpen() int { return value.maximumOpen }
func (value PoolStatus) MaximumIdle() int { return value.maximumIdle }
func (value PoolStatus) ConnectionMaximumLifetime() time.Duration {
	return value.connectionMaximumLifetime
}
func (value PoolStatus) ConnectionMaximumIdleTime() time.Duration {
	return value.connectionMaximumIdleTime
}

// Database is exported only across Golem's internal package boundary. Public
// provider.Database is a distinct defined type over this representation.
type Database struct {
	state *databaseState
}

// TestMetadata describes the deliberately unverified metadata attached by
// AdoptUnverifiedForTest. It exists only under Golem's internal import boundary.
type TestMetadata struct {
	Provider                  golem.Provider
	Version                   Version
	Features                  []string
	MaximumOpen               int
	MaximumIdle               int
	ConnectionMaximumLifetime time.Duration
	ConnectionMaximumIdleTime time.Duration
}

type databaseState struct {
	database             *sqlx.DB
	provider             golem.Provider
	capabilities         Capabilities
	pool                 PoolStatus
	testOnly             bool
	sqliteDataSourceName string

	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}
	closeDone chan struct{}
}

func newDatabase(database *sqlx.DB, kind golem.Provider, version Version, features []string, pool PoolStatus) *Database {
	canonical := append([]string(nil), features...)
	sort.Strings(canonical)
	canonical = compactStrings(canonical)
	return &Database{state: &databaseState{
		database: database,
		provider: kind,
		capabilities: Capabilities{
			provider: kind,
			version:  version,
			features: canonical,
		},
		pool:      pool,
		closed:    make(chan struct{}),
		closeDone: make(chan struct{}),
	}}
}

// AdoptUnverifiedForTest wraps an already traced test pool with shared close
// ownership. It performs no provider, capability, schema, or pool proof and
// must never be used by generated or production code. Runtime Open is expected
// to reprove the live capabilities and physical schema of adopted test pools.
func AdoptUnverifiedForTest(database *sqlx.DB, metadata TestMetadata) *Database {
	result := newDatabase(database, metadata.Provider, metadata.Version, metadata.Features, PoolStatus{
		maximumOpen:               metadata.MaximumOpen,
		maximumIdle:               metadata.MaximumIdle,
		connectionMaximumLifetime: metadata.ConnectionMaximumLifetime,
		connectionMaximumIdleTime: metadata.ConnectionMaximumIdleTime,
	})
	if result != nil && result.state != nil {
		result.state.testOnly = true
	}
	return result
}

// IsUnverifiedForTest is visible only inside Golem's internal import boundary.
// It lets runtime fixtures preserve traced custom drivers without creating a
// public escape from reviewed migration-ledger startup verification.
func (database *Database) IsUnverifiedForTest() bool {
	return database != nil && database.state != nil && database.state.testOnly
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func (database *Database) Provider() golem.Provider {
	if database == nil || database.state == nil {
		return ""
	}
	return database.state.provider
}

func (database *Database) Capabilities() Capabilities {
	if database == nil || database.state == nil {
		return Capabilities{}
	}
	value := database.state.capabilities
	value.features = append([]string(nil), value.features...)
	return value
}

func (database *Database) Pool() PoolStatus {
	if database == nil || database.state == nil {
		return PoolStatus{}
	}
	return database.state.pool
}

func (database *Database) UnsafeSQLX() *sqlx.DB {
	if database == nil || database.state == nil || database.state.database == nil || database.state.isClosed() {
		return nil
	}
	return database.state.database
}

func (database *Database) Close() error {
	if database == nil || database.state == nil {
		return nil
	}
	database.state.closeOnce.Do(func() {
		defer close(database.state.closeDone)
		if database.state.closed != nil {
			close(database.state.closed)
		}
		if database.state.database != nil {
			if err := database.state.database.Close(); err != nil {
				database.state.closeErr = failure(CodeClose, "provider close failed")
			}
		}
	})
	return database.state.closeErr
}

func (state *databaseState) isClosed() bool {
	if state == nil || state.closed == nil {
		return false
	}
	select {
	case <-state.closed:
		return true
	default:
		return false
	}
}

func (state *databaseState) closeCompleted() bool {
	if state == nil || state.closeDone == nil {
		return false
	}
	select {
	case <-state.closeDone:
		return state.closeErr == nil
	default:
		return false
	}
}

func OpenSQLite(ctx context.Context, dataSourceName string) (*Database, error) {
	if ctx == nil || strings.TrimSpace(dataSourceName) == "" {
		return nil, failure(CodeConfig, "sqlite provider configuration is invalid")
	}
	database, report, err := internalsqlite.New().Open(ctx, dataSourceName)
	if err != nil {
		return nil, failure(CodeOpen, "sqlite provider open failed")
	}
	result := newDatabase(database, golem.SQLite, Version{
		Major: report.Version.Major,
		Minor: report.Version.Minor,
		Patch: report.Version.Patch,
	}, []string{
		"foreign-keys.v1",
		"json.v1",
		"generated-columns.v1",
		"policy-binary-text.v1",
		"policy-ascii-text.v1",
		"policy-exact-json.v1",
		"policy-scalar-list.v1",
		"policy-relation.v1",
		"analytics-exact.v1",
	}, PoolStatus{maximumOpen: internalsqlite.VerifiedPoolWidth, maximumIdle: internalsqlite.VerifiedPoolWidth})
	result.state.sqliteDataSourceName = dataSourceName
	return result, nil
}

// CheckpointSQLiteForBackup performs the provider-owned SQLite checkpoint for
// a verified handle after its application pool has been closed. Keeping the
// data-source identity inside the sealed handle prevents callers from
// checkpointing a different database than the one Golem opened and verified.
func CheckpointSQLiteForBackup(ctx context.Context, database *Database) error {
	if ctx == nil || database == nil || database.state == nil || database.state.provider != golem.SQLite || !database.state.closeCompleted() || strings.TrimSpace(database.state.sqliteDataSourceName) == "" {
		return failure(CodeMaintenance, "sqlite backup checkpoint requires a closed verified SQLite provider handle")
	}
	if err := internalsqlite.New().CheckpointForBackup(ctx, database.state.sqliteDataSourceName); err != nil {
		return failure(CodeMaintenance, "sqlite backup checkpoint failed")
	}
	return nil
}

type PostgreSQLPoolConfig struct {
	MaximumOpen               int
	MaximumIdle               int
	ConnectionMaximumLifetime time.Duration
	ConnectionMaximumIdleTime time.Duration
}

func OpenPostgreSQL(ctx context.Context, dataSourceName string, poolConfig PostgreSQLPoolConfig) (*Database, error) {
	if ctx == nil || strings.TrimSpace(dataSourceName) == "" {
		return nil, failure(CodeConfig, "postgresql provider configuration is invalid")
	}
	if err := validatePostgreSQLDataSourceName(dataSourceName); err != nil {
		return nil, failure(CodeConfig, "postgresql provider configuration is invalid")
	}
	pool, err := normalizePostgreSQLPool(poolConfig)
	if err != nil {
		return nil, err
	}
	database, report, err := internalpostgresql.New().Open(ctx, dataSourceName)
	if err != nil {
		return nil, failure(CodeOpen, "postgresql provider open failed")
	}
	database.SetMaxOpenConns(pool.MaximumOpen)
	database.SetMaxIdleConns(pool.MaximumIdle)
	database.SetConnMaxLifetime(pool.ConnectionMaximumLifetime)
	database.SetConnMaxIdleTime(pool.ConnectionMaximumIdleTime)
	return newDatabase(database, golem.PostgreSQL, Version{
		Major: report.Version.Major,
		Minor: report.Version.Minor,
		Patch: report.Version.Patch,
	}, []string{
		"json.v1",
		"generated-columns.v1",
		"advisory-locks.v1",
		"policy-binary-text.v1",
		"policy-ascii-text.v1",
		"policy-exact-json.v1",
		"policy-scalar-list.v1",
		"policy-relation.v1",
		"analytics-exact.v1",
	}, PoolStatus{
		maximumOpen:               pool.MaximumOpen,
		maximumIdle:               pool.MaximumIdle,
		connectionMaximumLifetime: pool.ConnectionMaximumLifetime,
		connectionMaximumIdleTime: pool.ConnectionMaximumIdleTime,
	}), nil
}

func normalizePostgreSQLPool(config PostgreSQLPoolConfig) (PostgreSQLPoolConfig, error) {
	if config.MaximumOpen == 0 {
		config.MaximumOpen = postgreSQLDefaultMaximumOpen
	}
	if config.MaximumIdle == 0 {
		config.MaximumIdle = postgreSQLDefaultMaximumIdle
	}
	if config.ConnectionMaximumLifetime == 0 {
		config.ConnectionMaximumLifetime = postgreSQLDefaultConnectionMaximumLifetime
	}
	if config.ConnectionMaximumIdleTime == 0 {
		config.ConnectionMaximumIdleTime = postgreSQLDefaultConnectionMaximumIdleTime
	}
	if config.MaximumOpen < 1 || config.MaximumOpen > postgreSQLMaximumPoolWidth {
		return PostgreSQLPoolConfig{}, failure(CodeConfig, "postgresql pool maximum open connections are invalid")
	}
	if config.MaximumIdle < 1 || config.MaximumIdle > config.MaximumOpen {
		return PostgreSQLPoolConfig{}, failure(CodeConfig, "postgresql pool maximum idle connections are invalid")
	}
	if config.ConnectionMaximumLifetime < postgreSQLMinimumConnectionDuration || config.ConnectionMaximumLifetime > postgreSQLMaximumConnectionDuration {
		return PostgreSQLPoolConfig{}, failure(CodeConfig, "postgresql pool connection maximum lifetime is invalid")
	}
	if config.ConnectionMaximumIdleTime < postgreSQLMinimumConnectionDuration || config.ConnectionMaximumIdleTime > postgreSQLMaximumConnectionDuration || config.ConnectionMaximumIdleTime > config.ConnectionMaximumLifetime {
		return PostgreSQLPoolConfig{}, failure(CodeConfig, "postgresql pool connection maximum idle time is invalid")
	}
	return config, nil
}

func validatePostgreSQLDataSourceName(dataSourceName string) error {
	value := strings.TrimSpace(dataSourceName)
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || parsed.User == nil || parsed.User.Username() == "" || strings.Trim(parsed.Path, "/") == "" {
			return fmt.Errorf("postgresql connection URL has incomplete identity")
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return fmt.Errorf("postgresql connection URL query is invalid")
		}
		for key := range query {
			if postgreSQLProviderOwnedSessionKey(key) {
				return fmt.Errorf("postgresql connection URL contains a provider-owned session setting")
			}
		}
		return nil
	}
	fields, err := parsePostgreSQLKeywordDataSourceName(value)
	if err != nil || strings.TrimSpace(fields["host"]) == "" || strings.TrimSpace(fields["user"]) == "" || strings.TrimSpace(fields["dbname"]) == "" {
		return fmt.Errorf("postgresql keyword connection string has incomplete identity")
	}
	for key := range fields {
		if postgreSQLProviderOwnedSessionKey(key) {
			return fmt.Errorf("postgresql keyword connection string contains a provider-owned session setting")
		}
	}
	return nil
}

func postgreSQLProviderOwnedSessionKey(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "options", "timezone", "datestyle", "intervalstyle", "standard_conforming_strings":
		return true
	default:
		return false
	}
}

func parsePostgreSQLKeywordDataSourceName(value string) (map[string]string, error) {
	fields := map[string]string{}
	for index := 0; index < len(value); {
		for index < len(value) && isPostgreSQLSpace(value[index]) {
			index++
		}
		if index == len(value) {
			break
		}
		keyStart := index
		for index < len(value) && !isPostgreSQLSpace(value[index]) && value[index] != '=' {
			index++
		}
		key := strings.ToLower(value[keyStart:index])
		for index < len(value) && isPostgreSQLSpace(value[index]) {
			index++
		}
		if key == "" || index == len(value) || value[index] != '=' {
			return nil, fmt.Errorf("invalid PostgreSQL keyword connection string")
		}
		index++
		for index < len(value) && isPostgreSQLSpace(value[index]) {
			index++
		}
		var builder strings.Builder
		if index < len(value) && value[index] == '\'' {
			index++
			closed := false
			for index < len(value) {
				if value[index] == '\'' {
					index++
					closed = true
					break
				}
				if value[index] == '\\' {
					index++
					if index == len(value) {
						return nil, fmt.Errorf("invalid PostgreSQL keyword escape")
					}
				}
				builder.WriteByte(value[index])
				index++
			}
			if !closed || (index < len(value) && !isPostgreSQLSpace(value[index])) {
				return nil, fmt.Errorf("invalid PostgreSQL quoted keyword value")
			}
		} else {
			for index < len(value) && !isPostgreSQLSpace(value[index]) {
				if value[index] == '\\' {
					index++
					if index == len(value) {
						return nil, fmt.Errorf("invalid PostgreSQL keyword escape")
					}
				}
				builder.WriteByte(value[index])
				index++
			}
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("duplicate PostgreSQL keyword")
		}
		fields[key] = builder.String()
	}
	return fields, nil
}

func isPostgreSQLSpace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}
