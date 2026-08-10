package handle

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/jmoiron/sqlx"
)

func TestP8PostgreSQLPoolDefaultsAndHardLimits(t *testing.T) {
	defaults, err := normalizePostgreSQLPool(PostgreSQLPoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.MaximumOpen != postgreSQLDefaultMaximumOpen || defaults.MaximumIdle != postgreSQLDefaultMaximumIdle ||
		defaults.ConnectionMaximumLifetime != postgreSQLDefaultConnectionMaximumLifetime ||
		defaults.ConnectionMaximumIdleTime != postgreSQLDefaultConnectionMaximumIdleTime {
		t.Fatalf("defaults=%+v", defaults)
	}
	for _, invalid := range []PostgreSQLPoolConfig{
		{MaximumOpen: -1},
		{MaximumOpen: postgreSQLMaximumPoolWidth + 1},
		{MaximumOpen: 2, MaximumIdle: 3},
		{ConnectionMaximumLifetime: time.Nanosecond},
		{ConnectionMaximumIdleTime: postgreSQLMaximumConnectionDuration + time.Second},
		{ConnectionMaximumLifetime: time.Minute, ConnectionMaximumIdleTime: 2 * time.Minute},
	} {
		if _, err := normalizePostgreSQLPool(invalid); err == nil {
			t.Fatalf("accepted invalid pool %+v", invalid)
		}
	}
}

func TestP8PostgreSQLConnectionIdentityNeverFallsBackToAmbientEnvironment(t *testing.T) {
	t.Setenv("PGHOST", "ambient-host")
	t.Setenv("PGUSER", "ambient-user")
	t.Setenv("PGDATABASE", "ambient-database")

	for _, value := range []string{
		"sslmode=disable",
		"host=explicit user=explicit",
		"host=explicit dbname=explicit",
		"user=explicit dbname=explicit",
		"postgresql://user@host",
		"postgresql://host/database",
		"postgresql:///database?user=user",
	} {
		if err := validatePostgreSQLDataSourceName(value); err == nil {
			t.Fatalf("accepted incomplete connection identity %q", value)
		}
	}
	for _, value := range []string{
		"host=localhost user=golem dbname=app sslmode=disable",
		"host='/var/run/postgresql' user='golem user' dbname='golem app'",
		"postgresql://golem@localhost/golem?sslmode=disable",
	} {
		if err := validatePostgreSQLDataSourceName(value); err != nil {
			t.Fatalf("rejected explicit connection identity %q: %v", value, err)
		}
	}
}

func TestP8PostgreSQLRefusesCallerOwnedSessionOverrides(t *testing.T) {
	for _, value := range []string{
		"postgresql://golem@localhost/golem?options=-c%20timezone%3DEurope%2FParis",
		"postgresql://golem@localhost/golem?opt%69ons=-c%20datestyle%3DSQL%2CDMY",
		"postgresql://golem@localhost/golem?timezone=Europe%2FParis",
		"postgresql://golem@localhost/golem?DateStyle=SQL%2CDMY",
		"postgresql://golem@localhost/golem?intervalstyle=postgres",
		"postgresql://golem@localhost/golem?standard_conforming_strings=off",
		"host=localhost user=golem dbname=app options='-c timezone=Europe/Paris'",
		"host=localhost user=golem dbname=app TimeZone=Europe/Paris",
		"host=localhost user=golem dbname=app datestyle='SQL, DMY'",
		"host=localhost user=golem dbname=app intervalstyle=postgres",
		"host=localhost user=golem dbname=app standard_conforming_strings=off",
	} {
		if err := validatePostgreSQLDataSourceName(value); err == nil {
			t.Fatalf("accepted provider-owned PostgreSQL session override %q", value)
		}
	}

	for _, value := range []string{
		"postgresql://golem@localhost/golem?application_name=golem&sslmode=disable",
		"host=localhost user=golem dbname=app application_name=golem sslmode=disable",
	} {
		if err := validatePostgreSQLDataSourceName(value); err != nil {
			t.Fatalf("rejected unrelated PostgreSQL setting %q: %v", value, err)
		}
	}
}

func TestDatabaseCloseFailureIsStableClosedAndRedacted(t *testing.T) {
	const canary = "raw-driver-close-canary"
	raw := sql.OpenDB(closeFailureConnector{message: canary})
	if err := raw.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	database := newDatabase(sqlx.NewDb(raw, "close-failure"), golem.SQLite, Version{Major: 3, Minor: 38}, nil, PoolStatus{maximumOpen: 1, maximumIdle: 1})

	first := database.Close()
	second := database.Close()
	if first == nil || first != second {
		t.Fatalf("close errors are not stable: first=%v second=%v", first, second)
	}
	if strings.Contains(first.Error(), canary) || first.Error() != "provider close failed" {
		t.Fatalf("close failure was not redacted: %v", first)
	}
	if code, ok := CodeOf(first); !ok || code != CodeClose {
		t.Fatalf("close code=%q known=%t", code, ok)
	}
	if database.UnsafeSQLX() != nil {
		t.Fatal("failed close retained public raw-pool access")
	}
}

type closeFailureConnector struct{ message string }

func (connector closeFailureConnector) Connect(context.Context) (driver.Conn, error) {
	return &closeFailureConnection{message: connector.message}, nil
}

func (connector closeFailureConnector) Driver() driver.Driver {
	return closeFailureDriver{connector: connector}
}

type closeFailureDriver struct{ connector closeFailureConnector }

func (driver closeFailureDriver) Open(string) (driver.Conn, error) {
	return driver.connector.Connect(context.Background())
}

type closeFailureConnection struct{ message string }

func (*closeFailureConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (connection *closeFailureConnection) Close() error { return errors.New(connection.message) }
func (*closeFailureConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("begin unsupported")
}
func (*closeFailureConnection) Ping(context.Context) error { return nil }

var _ driver.Connector = closeFailureConnector{}
var _ driver.Pinger = (*closeFailureConnection)(nil)
var _ io.Closer = (*closeFailureConnection)(nil)
