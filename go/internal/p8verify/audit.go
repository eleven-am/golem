package p8verify

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Gate struct {
	Package string
	Test    string
}

type GateEvidence struct {
	Identity       string `json:"identity"`
	EventSHA256    string `json:"eventSHA256"`
	PassedTests    int    `json:"passedTests"`
	PassedPackages int    `json:"passedPackages"`
}

type Report struct {
	FormatVersion       uint16         `json:"formatVersion"`
	Command             string         `json:"command"`
	Status              string         `json:"status"`
	FormalEvidence      string         `json:"formalEvidence"`
	Profiles            []string       `json:"profiles"`
	Gates               []GateEvidence `json:"gates"`
	TreeSHA256          string         `json:"treeSHA256"`
	HostedPublicTag     string         `json:"hostedPublicTagInstallation"`
	PendingIntegrations []string       `json:"pendingIntegrations"`
}

type ErrorCode string

const (
	CodeConfig      ErrorCode = "P8_VERIFY_CONFIG"
	CodeStart       ErrorCode = "P8_VERIFY_START"
	CodeTimeout     ErrorCode = "P8_VERIFY_TIMEOUT"
	CodeEvents      ErrorCode = "P8_VERIFY_EVENTS"
	CodeGate        ErrorCode = "P8_VERIFY_GATE"
	CodeSkip        ErrorCode = "P8_VERIFY_SKIP"
	CodeMissing     ErrorCode = "P8_VERIFY_MISSING"
	CodeTreeChanged ErrorCode = "P8_VERIFY_TREE_CHANGED"
)

type Error struct{ Code ErrorCode }

func (failure *Error) Error() string {
	if failure == nil || failure.Code == "" {
		return string(CodeConfig)
	}
	return string(failure.Code)
}

var closedTest = regexp.MustCompile(`^TestP8[A-Za-z0-9]+(?:/[a-z0-9-]+)*$`)

func RunLocalAudit(ctx context.Context, moduleDir string, timeout time.Duration) (Report, error) {
	groups := []struct {
		profiles []string
		gates    []Gate
	}{
		{profiles: []string{"sqlite"}, gates: sqliteGates()},
		{profiles: []string{"postgresql-c", "postgresql-linguistic"}, gates: postgresqlGates()},
		{gates: artifactGates()},
	}
	return runAudit(ctx, moduleDir, timeout, []string{"postgresql-c", "postgresql-linguistic", "sqlite"}, flattenGroups(groups))
}

func runAudit(ctx context.Context, moduleDir string, timeout time.Duration, profiles []string, gates []Gate) (Report, error) {
	report := Report{
		FormatVersion: FormatVersion, Command: "p8verify", Status: "FAIL", FormalEvidence: "PENDING",
		Profiles: append([]string{}, profiles...), HostedPublicTag: "PENDING",
		PendingIntegrations: append([]string(nil), ReleaseAuditInventory().PendingIntegrations...),
	}
	if ctx == nil || moduleDir == "" || timeout <= 0 || timeout > time.Hour || len(gates) == 0 || !sort.StringsAreSorted(profiles) {
		return report, &Error{Code: CodeConfig}
	}
	absolute, err := filepath.Abs(moduleDir)
	if err != nil {
		return report, &Error{Code: CodeConfig}
	}
	if info, statErr := os.Stat(filepath.Join(absolute, "go.mod")); statErr != nil || info.IsDir() {
		return report, &Error{Code: CodeConfig}
	}
	before, err := treeDigest(absolute)
	if err != nil {
		return report, &Error{Code: CodeConfig}
	}
	report.TreeSHA256 = before
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, gate := range gates {
		if gate.Package == "" || !closedTest.MatchString(gate.Test) {
			return report, &Error{Code: CodeConfig}
		}
		evidence, gateErr := runGate(runContext, absolute, gate)
		if gateErr != nil {
			return report, gateErr
		}
		report.Gates = append(report.Gates, evidence)
	}
	after, err := treeDigest(absolute)
	if err != nil {
		return report, &Error{Code: CodeConfig}
	}
	if before != after {
		return report, &Error{Code: CodeTreeChanged}
	}
	report.Status = "LOCAL_CANDIDATE_PASS"
	return report, nil
}

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

func runGate(ctx context.Context, moduleDir string, gate Gate) (GateEvidence, error) {
	evidence := GateEvidence{Identity: gate.Package + ":" + gate.Test}
	command := exec.CommandContext(ctx, "go", "test", "-json", "-count=1", "-mod=readonly", "-run", "^"+gate.Test+"$", gate.Package)
	command.Dir = moduleDir
	command.Env = auditEnvironment()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return evidence, &Error{Code: CodeStart}
	}
	command.Stderr = nil
	if err := command.Start(); err != nil {
		return evidence, &Error{Code: CodeStart}
	}
	hash := sha256.New()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	exactPass, packagePass, skipped, failed := false, false, false, false
	for scanner.Scan() {
		var event testEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return evidence, &Error{Code: CodeEvents}
		}
		canonical, _ := json.Marshal(event)
		_, _ = hash.Write(append(canonical, '\n'))
		if event.Action == "skip" {
			skipped = true
		}
		if event.Test == gate.Test {
			switch event.Action {
			case "pass":
				exactPass = true
			case "fail":
				failed = true
			}
		}
		if event.Test == "" {
			switch event.Action {
			case "pass":
				packagePass = true
			case "fail":
				failed = true
			}
		}
	}
	scanErr := scanner.Err()
	waitErr := command.Wait()
	evidence.EventSHA256 = hex.EncodeToString(hash.Sum(nil))
	if ctx.Err() != nil {
		return evidence, &Error{Code: CodeTimeout}
	}
	if scanErr != nil {
		return evidence, &Error{Code: CodeEvents}
	}
	if skipped {
		return evidence, &Error{Code: CodeSkip}
	}
	if failed || waitErr != nil {
		return evidence, &Error{Code: CodeGate}
	}
	if !exactPass || !packagePass {
		return evidence, &Error{Code: CodeMissing}
	}
	evidence.PassedTests, evidence.PassedPackages = 1, 1
	return evidence, nil
}

func sqliteGates() []Gate {
	result := profileOracleGates("sqlite")
	return append(result,
		Gate{Package: "./provider/sqlite", Test: "TestP8SQLitePublicOpenAppliesInvariantToEveryPooledConnection"},
		Gate{Package: "./cmd/golem", Test: "TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents/sqlite"},
	)
}

func postgresqlGates() []Gate {
	result := []Gate{}
	for _, profile := range []struct{ suffix, providerSuffix string }{
		{suffix: "postgresql-c", providerSuffix: "c"},
		{suffix: "postgresql-linguistic", providerSuffix: "linguistic"},
	} {
		result = append(result, profileOracleGates(profile.suffix)...)
		result = append(result,
			Gate{Package: "./provider/postgresql", Test: "TestP8PostgreSQLPublicOpenConfiguresEveryPooledConnection/" + profile.providerSuffix},
			Gate{Package: "./cmd/golem", Test: "TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents/" + profile.suffix},
		)
	}
	return result
}

// profileOracleGates selects one strongest independently-authored external
// scenario for every behavioral domain used by the final audit. It is a
// bounded cross-section, not a recursive invocation of the full P8 suite.
func profileOracleGates(profile string) []Gate {
	return []Gate{
		{Package: "./internal/p8oracle", Test: "TestP8ReadCrossEntryPointIndependentOracle/" + profile},
		{Package: "./internal/p8oracle/mutation", Test: "TestP8MutationCrossEntryPointIndependentOracle/" + profile},
		{Package: "./internal/p8oracle/disclosure", Test: "TestP8HookComputedCustomAndAnalyticsDisclosureCorpus/" + profile},
		{Package: "./internal/p8oracle/analytics", Test: "TestP8ScopedReadAuthorizationAndAuditRedTeam/" + profile},
		{Package: "./internal/p8oracle/disclosure", Test: "TestP8DisclosureCanaryCorpusCallerGraphQLEvents/" + profile},
		{Package: "./internal/p8oracle/event", Test: "TestP8CDCAdapterUsesReleasedRuntimePath/" + profile},
		{Package: "./internal/p8oracle/diagnostic", Test: "TestP8DiagnosticAndTelemetryRedactionCanaryCorpus/" + profile},
		{Package: "./internal/p8oracle/load", Test: "TestP8StatementAndConnectionBudgetMatrix/" + profile},
		{Package: "./internal/p8oracle/failure", Test: "TestP8PublisherCDCAndMigrationCrashRecovery/" + profile},
	}
}

func artifactGates() []Gate {
	return []Gate{
		{Package: "./cmd/golem", Test: "TestP8DocumentationStatusAndLinkAudit"},
		{Package: "./cmd/golem", Test: "TestP8IntentionalBoundaryDisclosureCorpus"},
		{Package: "./cmd/golem", Test: "TestP8DocumentationCommandCorpus"},
		{Package: "./cmd/golem", Test: "TestP8QuickstartFromEmptyDirectory"},
		{Package: "./cmd/golem", Test: "TestP8EveryPublicSnippetTypeChecks"},
		{Package: "./internal/compatibility", Test: "TestP8PublicGoAPIDiffGate"},
		{Package: "./internal/compatibility", Test: "TestP8GeneratedAndGraphQLCompatibilityGate"},
		{Package: "./cmd/golem", Test: "TestP8CLIJSONAndPersistedFormatCompatibilityGate"},
		{Package: "./provider", Test: "TestP8PublicPackageInventoryHasNoInternalTypeLeak"},
		{Package: "./internal/release", Test: "TestP8ReleaseArtifactReproducibility"},
		{Package: "./internal/release", Test: "TestP8CleanConsumerModuleResolutionAndGoInstall"},
	}
}

func flattenGroups(groups []struct {
	profiles []string
	gates    []Gate
}) []Gate {
	var result []Gate
	for _, group := range groups {
		result = append(result, group.gates...)
	}
	return result
}

func auditEnvironment() []string {
	result := make([]string, 0, len(os.Environ())+4)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key == "GOWORK" || key == "GOFLAGS" {
			continue
		}
		result = append(result, value)
	}
	if strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN")) == "" {
		result = append(result, "GOLEM_TEST_POSTGRES_DSN=postgresql://postgres@127.0.0.1:55433/golem?sslmode=disable")
	}
	if strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN")) == "" {
		result = append(result, "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=postgresql://postgres@127.0.0.1:55432/golem?sslmode=disable")
	}
	return append(result, "GOWORK=off", "GOFLAGS=")
}

func treeDigest(root string) (string, error) {
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if entry.IsDir() && (entry.Name() == ".git" || strings.HasPrefix(entry.Name(), "golemgqlgentmp")) {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", name, info.Mode().String())
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			_, _ = hash.Write([]byte(target))
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported audit tree entry")
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = hash.Write(content)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
