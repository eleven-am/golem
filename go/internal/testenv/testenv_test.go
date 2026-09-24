package testenv

import (
	"fmt"
	"strings"
	"testing"
)

type recorder struct {
	testing.TB
	fatal, skipped bool
	message        string
}

func (r *recorder) Helper() {}

func (r *recorder) Fatal(args ...any) {
	r.fatal, r.message = true, strings.TrimSpace(fmt.Sprint(args...))
	panic(r)
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.fatal, r.message = true, fmt.Sprintf(format, args...)
	panic(r)
}

func (r *recorder) Skip(args ...any) {
	r.skipped, r.message = true, strings.TrimSpace(fmt.Sprint(args...))
	panic(r)
}

func (r *recorder) Skipf(format string, args ...any) {
	r.skipped, r.message = true, fmt.Sprintf(format, args...)
	panic(r)
}

func observe(run func(testing.TB)) (result *recorder) {
	result = &recorder{}
	defer func() {
		if recovered := recover(); recovered != nil && recovered != result {
			panic(recovered)
		}
	}()
	run(result)
	return result
}

func TestMissingPostgreSQLFailsWhenRequired(t *testing.T) {
	t.Setenv(PostgreSQLRequiredVariable, "1")
	t.Setenv(PostgreSQLDSNVariable, "")
	for name, run := range map[string]func(testing.TB){
		"dsn":     func(tb testing.TB) { PostgreSQLDSN(tb, PostgreSQLDSNVariable) },
		"skip":    func(tb testing.TB) { SkipMissingPostgreSQL(tb, "absent") },
		"skipf":   func(tb testing.TB) { SkipMissingPostgreSQLf(tb, "%s", "absent") },
		"fail-if": func(tb testing.TB) { FailMissingPostgreSQLIfRequired(tb, "absent") },
	} {
		if result := observe(run); !result.fatal || result.skipped || !strings.Contains(result.message, PostgreSQLRequiredVariable) {
			t.Fatalf("%s: fatal=%t skipped=%t message=%q", name, result.fatal, result.skipped, result.message)
		}
	}
}

func TestMissingPostgreSQLSkipsWhenNotRequired(t *testing.T) {
	t.Setenv(PostgreSQLRequiredVariable, "")
	t.Setenv(PostgreSQLDSNVariable, "")
	for name, run := range map[string]func(testing.TB){
		"dsn":   func(tb testing.TB) { PostgreSQLDSN(tb, PostgreSQLDSNVariable) },
		"skip":  func(tb testing.TB) { SkipMissingPostgreSQL(tb, "absent") },
		"skipf": func(tb testing.TB) { SkipMissingPostgreSQLf(tb, "%s", "absent") },
	} {
		if result := observe(run); result.fatal || !result.skipped {
			t.Fatalf("%s: fatal=%t skipped=%t", name, result.fatal, result.skipped)
		}
	}
	if result := observe(func(tb testing.TB) { FailMissingPostgreSQLIfRequired(tb, "absent") }); result.fatal || result.skipped {
		t.Fatalf("fail-if without the flag: fatal=%t skipped=%t", result.fatal, result.skipped)
	}
}

func TestConfiguredPostgreSQLDSNIsReturnedTrimmed(t *testing.T) {
	t.Setenv(PostgreSQLRequiredVariable, "1")
	t.Setenv(PostgreSQLDSNVariable, "  postgresql://example  ")
	if got := PostgreSQLDSN(t, PostgreSQLDSNVariable); got != "postgresql://example" {
		t.Fatalf("dsn=%q", got)
	}
}

func TestMissingPGVectorFollowsItsOwnFlag(t *testing.T) {
	t.Setenv(PGVectorDSNVariable, "")
	t.Setenv(PostgreSQLRequiredVariable, "1")
	t.Setenv(PGVectorRequiredVariable, "1")
	if result := observe(func(tb testing.TB) { PGVectorDSN(tb) }); !result.fatal || !strings.Contains(result.message, PGVectorRequiredVariable) {
		t.Fatalf("required pgvector: fatal=%t message=%q", result.fatal, result.message)
	}
	t.Setenv(PGVectorRequiredVariable, "")
	if result := observe(func(tb testing.TB) { PGVectorDSN(tb) }); result.fatal || !result.skipped {
		t.Fatalf("optional pgvector: fatal=%t skipped=%t", result.fatal, result.skipped)
	}
}

func TestSocialPostgreSQLDSNPrefersItsOwnVariable(t *testing.T) {
	t.Setenv(PostgreSQLRequiredVariable, "1")
	t.Setenv(SocialPostgreSQLDSNVariable, "  postgresql://social  ")
	t.Setenv(PostgreSQLDSNVariable, "postgresql://shared")
	if got := SocialPostgreSQLDSN(t); got != "postgresql://social" {
		t.Fatalf("dsn=%q", got)
	}
}

func TestSocialPostgreSQLDSNFallsBackToTheSharedVariable(t *testing.T) {
	t.Setenv(PostgreSQLRequiredVariable, "1")
	t.Setenv(SocialPostgreSQLDSNVariable, "")
	t.Setenv(PostgreSQLDSNVariable, "  postgresql://shared  ")
	if got := SocialPostgreSQLDSN(t); got != "postgresql://shared" {
		t.Fatalf("dsn=%q", got)
	}
}

func TestMissingSocialPostgreSQLFollowsTheRequireFlag(t *testing.T) {
	t.Setenv(SocialPostgreSQLDSNVariable, "")
	t.Setenv(PostgreSQLDSNVariable, "")
	t.Setenv(PostgreSQLRequiredVariable, "1")
	result := observe(func(tb testing.TB) { SocialPostgreSQLDSN(tb) })
	if !result.fatal || result.skipped || !strings.Contains(result.message, PostgreSQLRequiredVariable) ||
		!strings.Contains(result.message, SocialPostgreSQLDSNVariable) || !strings.Contains(result.message, PostgreSQLDSNVariable) {
		t.Fatalf("required: fatal=%t skipped=%t message=%q", result.fatal, result.skipped, result.message)
	}
	t.Setenv(PostgreSQLRequiredVariable, "")
	if result := observe(func(tb testing.TB) { SocialPostgreSQLDSN(tb) }); result.fatal || !result.skipped {
		t.Fatalf("optional: fatal=%t skipped=%t", result.fatal, result.skipped)
	}
}
