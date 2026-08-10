package postgresql_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/jmoiron/sqlx"
)

func TestP8PostgreSQLPublicOpenConfiguresEveryPooledConnection(t *testing.T) {
	profiles := []struct {
		name string
		env  string
	}{
		{name: "c", env: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			dataSourceName := strings.TrimSpace(os.Getenv(profile.env))
			if dataSourceName == "" {
				t.Skipf("%s is not configured", profile.env)
			}
			assertPostgreSQLPublicPoolProfile(t, dataSourceName)
		})
	}
}

func assertPostgreSQLPublicPoolProfile(t *testing.T, dataSourceName string) {
	t.Helper()
	ctx := context.Background()
	config := postgresql.Config{
		DataSourceName: dataSourceName,
		Pool: postgresql.PoolConfig{
			MaximumOpen:               4,
			MaximumIdle:               4,
			ConnectionMaximumLifetime: 2 * time.Minute,
			ConnectionMaximumIdleTime: time.Minute,
		},
	}
	database, err := postgresql.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if database.Provider() != golem.PostgreSQL || database.Capabilities().Provider() != golem.PostgreSQL {
		t.Fatalf("provider handle=%q capabilities=%q", database.Provider(), database.Capabilities().Provider())
	}
	version := database.Capabilities().ServerVersion()
	if version.Major < 15 {
		t.Fatalf("server version=%+v", version)
	}
	pool := database.Pool()
	if pool.MaximumOpen() != 4 || pool.MaximumIdle() != 4 || pool.ConnectionMaximumLifetime() != 2*time.Minute || pool.ConnectionMaximumIdleTime() != time.Minute {
		t.Fatalf("pool=%+v", pool)
	}
	for _, feature := range []provider.Feature{
		provider.FeatureJSON,
		provider.FeatureGeneratedColumns,
		provider.FeatureAdvisoryLocks,
		provider.FeaturePolicyBinaryText,
		provider.FeaturePolicyASCIIText,
		provider.FeaturePolicyExactJSON,
		provider.FeaturePolicyScalarList,
		provider.FeaturePolicyRelation,
		provider.FeatureAnalyticsExact,
	} {
		if !containsFeature(database.Capabilities().Features(), feature) {
			t.Fatalf("capabilities omit %q: %#v", feature, database.Capabilities().Features())
		}
	}

	db := database.UnsafeSQLX()
	if db == nil {
		t.Fatal("unsafe pool is nil")
	}
	connections := make([]*sqlx.Conn, 0, 4)
	for index := 0; index < 4; index++ {
		connection, err := db.Connx(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", index, err)
		}
		connections = append(connections, connection)
	}
	t.Cleanup(func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	})
	if stats := db.Stats(); stats.OpenConnections != 4 || stats.InUse != 4 || stats.MaxOpenConnections != 4 {
		t.Fatalf("pool was not exhausted: %+v", stats)
	}
	for index, connection := range connections {
		var timezone, dateStyle, intervalStyle, standardStrings string
		if err := connection.QueryRowxContext(ctx, `SELECT
current_setting('timezone'),
current_setting('datestyle'),
current_setting('intervalstyle'),
current_setting('standard_conforming_strings')`).Scan(&timezone, &dateStyle, &intervalStyle, &standardStrings); err != nil {
			t.Fatalf("connection %d session profile: %v", index, err)
		}
		if timezone != "UTC" || dateStyle != "ISO, YMD" || intervalStyle != "iso_8601" || standardStrings != "on" {
			t.Fatalf("connection %d timezone=%q datestyle=%q intervalstyle=%q standard_conforming_strings=%q", index, timezone, dateStyle, intervalStyle, standardStrings)
		}
	}
}

func TestP8PostgreSQLOpenFailureClosesAllResourcesAndRedactsDSN(t *testing.T) {
	const secret = "p8-postgresql-password-canary"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	database, err := postgresql.Open(ctx, postgresql.Config{
		DataSourceName: "postgresql://golem:" + secret + "@127.0.0.1:1/golem?sslmode=disable",
	})
	if database != nil {
		_ = database.Close()
		t.Fatal("failed PostgreSQL open returned a database handle")
	}
	if err == nil {
		t.Fatal("cancelled PostgreSQL open unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "sslmode") {
		t.Fatalf("PostgreSQL open failure disclosed connection data: %v", err)
	}
	if code, ok := provider.CodeOf(err); !ok || code != provider.CodeOpen {
		t.Fatalf("open code=%q known=%t", code, ok)
	}
}

func TestP8PostgreSQLPoolDefaultsAndHardLimits(t *testing.T) {
	for _, profile := range []struct {
		name string
		env  string
	}{
		{name: "c", env: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	} {
		t.Run("defaults-"+profile.name, func(t *testing.T) {
			dataSourceName := strings.TrimSpace(os.Getenv(profile.env))
			if dataSourceName == "" {
				t.Skipf("%s is not configured", profile.env)
			}
			database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dataSourceName})
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			pool := database.Pool()
			if pool.MaximumOpen() != 16 || pool.MaximumIdle() != 4 || pool.ConnectionMaximumLifetime() != 30*time.Minute || pool.ConnectionMaximumIdleTime() != 5*time.Minute {
				t.Fatalf("public default pool=%+v", pool)
			}
			if got := database.UnsafeSQLX().Stats().MaxOpenConnections; got != pool.MaximumOpen() {
				t.Fatalf("live maximum open=%d want=%d", got, pool.MaximumOpen())
			}
		})
	}

	dataSourceName := "postgresql://golem@127.0.0.1:1/golem?sslmode=disable"
	for _, invalid := range []postgresql.PoolConfig{
		{MaximumOpen: -1},
		{MaximumOpen: 257},
		{MaximumOpen: 2, MaximumIdle: 3},
		{ConnectionMaximumLifetime: time.Nanosecond},
		{ConnectionMaximumIdleTime: 24*time.Hour + time.Second},
		{ConnectionMaximumLifetime: time.Minute, ConnectionMaximumIdleTime: 2 * time.Minute},
	} {
		database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dataSourceName, Pool: invalid})
		if database != nil {
			_ = database.Close()
			t.Fatalf("public PostgreSQL open accepted invalid pool %+v", invalid)
		}
		if code, ok := provider.CodeOf(err); !ok || code != provider.CodeConfig {
			t.Fatalf("invalid pool %+v code=%q known=%t error=%v", invalid, code, ok, err)
		}
	}
}

func containsFeature(values []provider.Feature, target provider.Feature) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
