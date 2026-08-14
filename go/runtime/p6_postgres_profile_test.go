package runtime_test

import (
	"context"
	"os"
	"strings"
	"testing"

	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
)

func TestPostgreSQLProfilesAreLiveDistinctAndCollationVerified(t *testing.T) {
	cDSN := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))
	linguisticDSN := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"))
	if cDSN == "" || linguisticDSN == "" {
		t.Skip("both PostgreSQL provider profiles are required")
	}
	if cDSN == linguisticDSN {
		t.Fatal("C and linguistic PostgreSQL DSNs resolve to the same configured value")
	}
	type profile struct {
		database, collation string
	}
	inspect := func(t *testing.T, dsn string) profile {
		t.Helper()
		database, _, err := postgresprovider.New().Open(context.Background(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		var result profile
		if err := database.QueryRowxContext(context.Background(), `SELECT current_database(), datcollate FROM pg_database WHERE datname = current_database()`).Scan(&result.database, &result.collation); err != nil {
			t.Fatal(err)
		}
		return result
	}
	cProfile := inspect(t, cDSN)
	linguisticProfile := inspect(t, linguisticDSN)
	canonical := func(value string) string { return strings.ToUpper(strings.TrimSpace(value)) }
	collation := canonical(cProfile.collation)
	if collation != "C" && collation != "POSIX" {
		t.Fatalf("C profile database %q reports datcollate %q, want C or POSIX", cProfile.database, cProfile.collation)
	}
	linguisticCollation := canonical(linguisticProfile.collation)
	if linguisticCollation == "C" || linguisticCollation == "POSIX" {
		t.Fatalf("linguistic profile database %q reports binary datcollate %q", linguisticProfile.database, linguisticProfile.collation)
	}
	if collation == linguisticCollation {
		t.Fatalf("PostgreSQL profiles do not differ: C=%+v linguistic=%+v", cProfile, linguisticProfile)
	}
}
