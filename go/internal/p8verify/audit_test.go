package p8verify

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestP8IndependentReleaseOracleSQLite(t *testing.T) {
	report, err := runAudit(context.Background(), verifyModuleRoot(t), 20*time.Minute, []string{"sqlite"}, sqliteGates())
	if err != nil {
		t.Fatal(err)
	}
	assertLocalReport(t, report, []string{"sqlite"}, 11)
}

func TestP8IndependentReleaseOraclePostgreSQLProfiles(t *testing.T) {
	requirePostgreSQLProfiles(t)
	report, err := runAudit(context.Background(), verifyModuleRoot(t), 30*time.Minute, []string{"postgresql-c", "postgresql-linguistic"}, postgresqlGates())
	if err != nil {
		t.Fatal(err)
	}
	assertLocalReport(t, report, []string{"postgresql-c", "postgresql-linguistic"}, 22)
}

func TestP8IndependentPublicPackageAndArtifactAudit(t *testing.T) {
	report, err := runAudit(context.Background(), verifyModuleRoot(t), 12*time.Minute, nil, artifactGates())
	if err != nil {
		t.Fatal(err)
	}
	assertLocalReport(t, report, nil, 11)
	if report.Profiles == nil {
		t.Fatal("artifact audit encoded absent profiles as null instead of a closed empty array")
	}
	if report.HostedPublicTag != "PENDING" || !contains(report.PendingIntegrations, "hosted-public-tag-installation") {
		t.Fatalf("local artifact audit overstates hosted installation: %#v", report)
	}
}

func TestP8LocalAuditGateInventoryIsClosed(t *testing.T) {
	got := append(append(sqliteGates(), postgresqlGates()...), artifactGates()...)
	want := []string{
		"./internal/p8oracle:TestP8ReadCrossEntryPointIndependentOracle/sqlite",
		"./internal/p8oracle/mutation:TestP8MutationCrossEntryPointIndependentOracle/sqlite",
		"./internal/p8oracle/disclosure:TestP8HookComputedCustomAndAnalyticsDisclosureCorpus/sqlite",
		"./internal/p8oracle/analytics:TestP8ScopedReadAuthorizationAndAuditRedTeam/sqlite",
		"./internal/p8oracle/disclosure:TestP8DisclosureCanaryCorpusCallerGraphQLEvents/sqlite",
		"./internal/p8oracle/event:TestP8CDCAdapterUsesReleasedRuntimePath/sqlite",
		"./internal/p8oracle/diagnostic:TestP8DiagnosticAndTelemetryRedactionCanaryCorpus/sqlite",
		"./internal/p8oracle/load:TestP8StatementAndConnectionBudgetMatrix/sqlite",
		"./internal/p8oracle/failure:TestP8PublisherCDCAndMigrationCrashRecovery/sqlite",
		"./provider/sqlite:TestP8SQLitePublicOpenAppliesInvariantToEveryPooledConnection",
		"./cmd/golem:TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents/sqlite",
		"./internal/p8oracle:TestP8ReadCrossEntryPointIndependentOracle/postgresql-c",
		"./internal/p8oracle/mutation:TestP8MutationCrossEntryPointIndependentOracle/postgresql-c",
		"./internal/p8oracle/disclosure:TestP8HookComputedCustomAndAnalyticsDisclosureCorpus/postgresql-c",
		"./internal/p8oracle/analytics:TestP8ScopedReadAuthorizationAndAuditRedTeam/postgresql-c",
		"./internal/p8oracle/disclosure:TestP8DisclosureCanaryCorpusCallerGraphQLEvents/postgresql-c",
		"./internal/p8oracle/event:TestP8CDCAdapterUsesReleasedRuntimePath/postgresql-c",
		"./internal/p8oracle/diagnostic:TestP8DiagnosticAndTelemetryRedactionCanaryCorpus/postgresql-c",
		"./internal/p8oracle/load:TestP8StatementAndConnectionBudgetMatrix/postgresql-c",
		"./internal/p8oracle/failure:TestP8PublisherCDCAndMigrationCrashRecovery/postgresql-c",
		"./provider/postgresql:TestP8PostgreSQLPublicOpenConfiguresEveryPooledConnection/c",
		"./cmd/golem:TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents/postgresql-c",
		"./internal/p8oracle:TestP8ReadCrossEntryPointIndependentOracle/postgresql-linguistic",
		"./internal/p8oracle/mutation:TestP8MutationCrossEntryPointIndependentOracle/postgresql-linguistic",
		"./internal/p8oracle/disclosure:TestP8HookComputedCustomAndAnalyticsDisclosureCorpus/postgresql-linguistic",
		"./internal/p8oracle/analytics:TestP8ScopedReadAuthorizationAndAuditRedTeam/postgresql-linguistic",
		"./internal/p8oracle/disclosure:TestP8DisclosureCanaryCorpusCallerGraphQLEvents/postgresql-linguistic",
		"./internal/p8oracle/event:TestP8CDCAdapterUsesReleasedRuntimePath/postgresql-linguistic",
		"./internal/p8oracle/diagnostic:TestP8DiagnosticAndTelemetryRedactionCanaryCorpus/postgresql-linguistic",
		"./internal/p8oracle/load:TestP8StatementAndConnectionBudgetMatrix/postgresql-linguistic",
		"./internal/p8oracle/failure:TestP8PublisherCDCAndMigrationCrashRecovery/postgresql-linguistic",
		"./provider/postgresql:TestP8PostgreSQLPublicOpenConfiguresEveryPooledConnection/linguistic",
		"./cmd/golem:TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents/postgresql-linguistic",
		"./cmd/golem:TestP8DocumentationStatusAndLinkAudit",
		"./cmd/golem:TestP8IntentionalBoundaryDisclosureCorpus",
		"./cmd/golem:TestP8DocumentationCommandCorpus",
		"./cmd/golem:TestP8QuickstartFromEmptyDirectory",
		"./cmd/golem:TestP8EveryPublicSnippetTypeChecks",
		"./internal/compatibility:TestP8PublicGoAPIDiffGate",
		"./internal/compatibility:TestP8GeneratedAndGraphQLCompatibilityGate",
		"./cmd/golem:TestP8CLIJSONAndPersistedFormatCompatibilityGate",
		"./provider:TestP8PublicPackageInventoryHasNoInternalTypeLeak",
		"./internal/release:TestP8ReleaseArtifactReproducibility",
		"./internal/release:TestP8CleanConsumerModuleResolutionAndGoInstall",
	}
	if len(got) != len(want) {
		t.Fatalf("gate count=%d want=%d", len(got), len(want))
	}
	for index, gate := range got {
		if identity := gate.Package + ":" + gate.Test; identity != want[index] {
			t.Fatalf("gate %d=%q want=%q", index, identity, want[index])
		}
	}
}

func requirePostgreSQLProfiles(t *testing.T) {
	t.Helper()
	if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1" {
		return
	}
	profiles := []struct{ environment, fallback string }{
		{"GOLEM_TEST_POSTGRES_DSN", "postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable"},
		{"GOLEM_TEST_POSTGRES_LINGUISTIC_DSN", "postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable"},
	}
	for _, profile := range profiles {
		dsn := strings.TrimSpace(os.Getenv(profile.environment))
		if dsn == "" {
			dsn = profile.fallback
		}
		parsed, err := url.Parse(dsn)
		if err != nil || parsed.Host == "" {
			t.Skip("optional PostgreSQL release-audit profiles are not configured")
		}
		connection, err := net.DialTimeout("tcp", parsed.Host, 500*time.Millisecond)
		if err != nil {
			t.Skip("optional PostgreSQL release-audit profiles are unavailable")
		}
		_ = connection.Close()
	}
}

func TestP8LocalAuditRejectsSkipMissingAndTreeMutation(t *testing.T) {
	for _, testCase := range []struct {
		name, source, test string
		code               ErrorCode
	}{
		{name: "skip", source: "package fixture\nimport \"testing\"\nfunc TestP8Fixture(t *testing.T){ t.Skip(\"no\") }\n", test: "TestP8Fixture", code: CodeSkip},
		{name: "missing", source: "package fixture\nimport \"testing\"\nfunc TestP8Other(t *testing.T){}\n", test: "TestP8Fixture", code: CodeMissing},
		{name: "tree", source: "package fixture\nimport (\"os\";\"testing\")\nfunc TestP8Fixture(t *testing.T){ if err:=os.WriteFile(\"changed\",[]byte(\"x\"),0600); err!=nil { t.Fatal(err) } }\n", test: "TestP8Fixture", code: CodeTreeChanged},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			module := t.TempDir()
			writeVerifyFile(t, filepath.Join(module, "go.mod"), "module example.test/p8verify\n\ngo 1.25.0\n")
			writeVerifyFile(t, filepath.Join(module, "fixture_test.go"), testCase.source)
			_, err := runAudit(context.Background(), module, time.Minute, []string{"sqlite"}, []Gate{{Package: ".", Test: testCase.test}})
			var closed *Error
			if !errors.As(err, &closed) || closed.Code != testCase.code {
				t.Fatalf("error=%v want=%s", err, testCase.code)
			}
		})
	}
}

func TestP8LocalAuditReportIsClosedVersionedAndRedacted(t *testing.T) {
	module := t.TempDir()
	writeVerifyFile(t, filepath.Join(module, "go.mod"), "module example.test/p8verify\n\ngo 1.25.0\n")
	writeVerifyFile(t, filepath.Join(module, "fixture_test.go"), "package fixture\nimport \"testing\"\nfunc TestP8Fixture(t *testing.T){}\n")
	report, err := runAudit(context.Background(), module, time.Minute, []string{"sqlite"}, []Gate{{Package: ".", Test: "TestP8Fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if report.FormatVersion != 1 || report.Status != "LOCAL_CANDIDATE_PASS" || report.FormalEvidence != "PENDING" || strings.Contains(text, module) || strings.Contains(text, "postgresql://") {
		t.Fatalf("unsafe local audit report: %s", text)
	}
}

func assertLocalReport(t *testing.T, report Report, profiles []string, gates int) {
	t.Helper()
	if report.FormatVersion != FormatVersion || report.Command != "p8verify" || report.Status != "LOCAL_CANDIDATE_PASS" || report.FormalEvidence != "PENDING" || len(report.Gates) != gates || len(report.TreeSHA256) != 64 {
		t.Fatalf("local report shape=%#v", report)
	}
	if strings.Join(report.Profiles, ",") != strings.Join(profiles, ",") {
		t.Fatalf("profiles=%#v want=%#v", report.Profiles, profiles)
	}
	for _, gate := range report.Gates {
		if gate.Identity == "" || len(gate.EventSHA256) != 64 || gate.PassedTests != 1 || gate.PassedPackages != 1 {
			t.Fatalf("gate evidence=%#v", gate)
		}
	}
}

func verifyModuleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate p8verify module")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filename)))
}

func writeVerifyFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
