package testenv

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func DisposablePostgreSQL(t testing.TB, variable string) string {
	t.Helper()
	return disposablePostgreSQL(t, PostgreSQLDSN(t, variable), SkipMissingPostgreSQLf)
}

func DisposableSocialPostgreSQL(t testing.TB) string {
	t.Helper()
	return disposablePostgreSQL(t, SocialPostgreSQLDSN(t), SkipMissingPostgreSQLf)
}

func DisposablePostgreSQLFrom(t testing.TB, base string) string {
	t.Helper()
	return disposablePostgreSQL(t, base, SkipMissingPostgreSQLf)
}

func DisposablePGVector(t testing.TB) string {
	t.Helper()
	return disposablePostgreSQL(t, PGVectorDSN(t), SkipMissingPGVectorf)
}

func SkipMissingPGVectorf(t testing.TB, format string, args ...any) {
	t.Helper()
	if PGVectorRequired() {
		t.Fatalf(PGVectorRequiredVariable+"=1 forbids skipping: "+format, args...)
	}
	t.Skipf(format, args...)
}

func disposablePostgreSQL(t testing.TB, base string, unavailable func(testing.TB, string, ...any)) string {
	t.Helper()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		unavailable(t, "open administrator connection: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		unavailable(t, "reach %s: %v", variableOf(base), err)
	}
	var encoding, collate, characterType string
	if err := admin.QueryRowContext(ctx, `SELECT pg_encoding_to_char(encoding),datcollate,datctype FROM pg_database WHERE datname=current_database()`).Scan(&encoding, &collate, &characterType); err != nil {
		_ = admin.Close()
		t.Fatalf("read template locale: %v", err)
	}
	name := fmt.Sprintf("golem_disposable_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, disposableCreateStatement(name, encoding, collate, characterType)); err != nil {
		_ = admin.Close()
		t.Fatalf("create disposable database: %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		removal, cancelRemoval := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelRemoval()
		_, _ = admin.ExecContext(removal, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, name)
		if _, err := admin.ExecContext(removal, "DROP DATABASE IF EXISTS "+quoteDisposableIdentifier(name)); err != nil {
			t.Errorf("drop disposable database %s: %v", name, err)
		}
	})
	return replaceDisposableDatabase(base, name)
}

func disposableCreateStatement(name, encoding, collate, characterType string) string {
	return fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0 ENCODING %s LC_COLLATE %s LC_CTYPE %s",
		quoteDisposableIdentifier(name), quoteDisposableLiteral(encoding), quoteDisposableLiteral(collate), quoteDisposableLiteral(characterType))
}

func replaceDisposableDatabase(base, name string) string {
	parsed, err := url.Parse(base)
	if err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		parsed.Path, parsed.RawPath = "/"+name, ""
		return parsed.String()
	}
	return strings.TrimSpace(base) + " dbname=" + name
}

func quoteDisposableIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteDisposableLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func variableOf(base string) string {
	for _, variable := range []string{PostgreSQLDSNVariable, LinguisticDSNVariable, SocialPostgreSQLDSNVariable, PGVectorDSNVariable} {
		if strings.TrimSpace(os.Getenv(variable)) == base {
			return variable
		}
	}
	return "the configured PostgreSQL server"
}
