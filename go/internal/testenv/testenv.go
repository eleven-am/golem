package testenv

import (
	"os"
	"strings"
	"testing"
)

const (
	PostgreSQLRequiredVariable = "GOLEM_P8_REQUIRE_POSTGRESQL"
	PGVectorRequiredVariable   = "GOLEM_REQUIRE_PGVECTOR"
	NATSRequiredVariable       = "GOLEM_P8_REQUIRE_NATS"
	PostgreSQLDSNVariable      = "GOLEM_TEST_POSTGRES_DSN"
	LinguisticDSNVariable      = "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"
	PGVectorDSNVariable        = "GOLEM_TEST_PGVECTOR_DSN"

	SocialPostgreSQLDSNVariable = "GOLEM_P8_SOCIAL_POSTGRES_DSN"
)

func PostgreSQLRequired() bool {
	return os.Getenv(PostgreSQLRequiredVariable) == "1"
}

func PGVectorRequired() bool {
	return os.Getenv(PGVectorRequiredVariable) == "1"
}

func PostgreSQLDSN(t testing.TB, variable string) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(variable))
	if dsn == "" {
		SkipMissingPostgreSQL(t, variable+" is not configured")
	}
	return dsn
}

func SocialPostgreSQLDSN(t testing.TB) string {
	t.Helper()
	for _, variable := range []string{SocialPostgreSQLDSNVariable, PostgreSQLDSNVariable} {
		if dsn := strings.TrimSpace(os.Getenv(variable)); dsn != "" {
			return dsn
		}
	}
	SkipMissingPostgreSQL(t, "neither "+SocialPostgreSQLDSNVariable+" nor "+PostgreSQLDSNVariable+" is configured")
	return ""
}

func PGVectorDSN(t testing.TB) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(PGVectorDSNVariable))
	if dsn == "" {
		if PGVectorRequired() {
			t.Fatal(PGVectorRequiredVariable + "=1 forbids skipping: " + PGVectorDSNVariable + " is not configured")
		}
		t.Skip(PGVectorDSNVariable + " is not configured")
	}
	return dsn
}

func FailMissingPostgreSQLIfRequired(t testing.TB, args ...any) {
	t.Helper()
	if PostgreSQLRequired() {
		t.Fatal(append([]any{PostgreSQLRequiredVariable + "=1 forbids skipping:"}, args...)...)
	}
}

func SkipMissingPostgreSQL(t testing.TB, args ...any) {
	t.Helper()
	FailMissingPostgreSQLIfRequired(t, args...)
	t.Skip(args...)
}

func SkipMissingPostgreSQLf(t testing.TB, format string, args ...any) {
	t.Helper()
	if PostgreSQLRequired() {
		t.Fatalf(PostgreSQLRequiredVariable+"=1 forbids skipping: "+format, args...)
	}
	t.Skipf(format, args...)
}
