package testenv

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// DisposablePostgreSQLInRoot uses the existing disposable database owner, but
// permits only the explicitly supplied private Unix-socket cluster. It checks
// the actual server data directory on the admin handle before the first write.
// Neither this helper nor its validator selects an environment variable.
func DisposablePostgreSQLInRoot(t testing.TB, root, baseDSN string) string {
	t.Helper()
	dsn, err := rootPostgreSQLDSN(root, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	fatal := func(t testing.TB, format string, args ...any) {
		t.Helper()
		t.Fatalf(format, args...)
	}
	return disposablePostgreSQL(t, dsn, fatal, func(ctx context.Context, database *sql.DB) error {
		return validatePostgreSQLRootConnection(ctx, database, root)
	})
}

// ValidatePostgreSQLRoot performs read-only ownership verification before a
// caller opens its public provider handle. The caller retains database ownership.
func ValidatePostgreSQLRoot(ctx context.Context, root, dsn string) error {
	canonical, err := rootPostgreSQLDSN(root, dsn)
	if err != nil {
		return err
	}
	if ctx == nil {
		return fmt.Errorf("private PostgreSQL validation requires a context")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	database, err := sql.Open("pgx", canonical)
	if err != nil {
		return fmt.Errorf("cannot open private PostgreSQL ownership check")
	}
	defer database.Close()
	return validatePostgreSQLRootConnection(ctx, database, root)
}

func rootPostgreSQLDSN(root, dsn string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || dsn == "" {
		return "", fmt.Errorf("private PostgreSQL requires an explicit absolute root and DSN")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "", fmt.Errorf("private PostgreSQL DSN is invalid")
	}
	socket := filepath.Join(filepath.Clean(root), "socket")
	if config.Host != socket {
		return "", fmt.Errorf("private PostgreSQL host is not the owned root/socket")
	}
	for _, fallback := range config.Fallbacks {
		if fallback.Host != socket || fallback.Port != config.Port {
			return "", fmt.Errorf("private PostgreSQL fallback leaves the owned socket endpoint")
		}
	}
	// Pin the checked connection properties. In particular, do not carry a
	// second dbname query parameter into disposable database replacement, or
	// allow a later connection to inherit a different environment host.
	address := url.URL{Scheme: "postgres", Host: "localhost", User: url.User(config.User), Path: "/" + config.Database}
	if config.Password != "" {
		address.User = url.UserPassword(config.User, config.Password)
	}
	address.RawQuery = url.Values{
		"host": {socket}, "port": {strconv.Itoa(int(config.Port))},
		"sslmode": {"disable"}, "connect_timeout": {"5"},
	}.Encode()
	return address.String(), nil
}

func validatePostgreSQLRootConnection(ctx context.Context, database *sql.DB, root string) error {
	var directory string
	if err := database.QueryRowContext(ctx, `SELECT current_setting('data_directory')`).Scan(&directory); err != nil {
		return fmt.Errorf("cannot read private PostgreSQL data directory")
	}
	want, wantErr := filepath.EvalSymlinks(filepath.Join(root, "data"))
	actual, actualErr := filepath.EvalSymlinks(directory)
	if wantErr != nil || actualErr != nil || actual != want {
		return fmt.Errorf("private PostgreSQL data directory is not the owned root/data")
	}
	return nil
}
