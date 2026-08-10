package completion

import (
	"path/filepath"
	"sort"
	"time"
)

const moduleImport = "github.com/eleven-am/golem/go"

func DocumentationSpec(moduleDir string, timeout time.Duration) Spec {
	repositoryRoot := filepath.Dir(moduleDir)
	return Spec{
		Command:   "p8docs",
		ModuleDir: moduleDir,
		Packages: []Package{{
			Path:       "./cmd/golem",
			ImportPath: moduleImport + "/cmd/golem",
			Tests: []string{
				"TestP8DocumentationCommandCorpus",
				"TestP8QuickstartFromEmptyDirectory",
				"TestP8DeploymentAndRecoveryRunbookDrills",
				"TestP8DeploymentAndRecoveryRunbookDrills/sqlite-backup-drift-restore",
				"TestP8DeploymentAndRecoveryRunbookDrills/postgresql-c-backup-drift-restore",
				"TestP8DeploymentAndRecoveryRunbookDrills/postgresql-linguistic-backup-drift-restore",
				"TestP8EveryPublicSnippetTypeChecks",
				"TestP8DocumentationStatusAndLinkAudit",
				"TestP8IntentionalBoundaryDisclosureCorpus",
				"TestP8NoCompletedABIStillClaimsUnimplemented",
				"TestP8READMEAndReleaseNotesCapabilityAgreement",
			},
		}},
		Profiles: []string{"postgresql-c", "postgresql-linguistic", "sqlite"},
		Timeout:  timeout,
		WatchPaths: []string{
			moduleDir,
			filepath.Join(repositoryRoot, "README.md"),
			filepath.Join(repositoryRoot, "RELEASE_NOTES.md"),
			filepath.Join(repositoryRoot, "docs", "golem-go"),
		},
	}
}

func CompatibilitySpec(moduleDir string, timeout time.Duration) Spec {
	return Spec{
		Command:   "p8compat",
		ModuleDir: moduleDir,
		Packages: []Package{
			{
				Path:       "./internal/compatibility",
				ImportPath: moduleImport + "/internal/compatibility",
				Tests: []string{
					"TestP8FrozenCompatibilityCorpusLoads",
					"TestP8CompatibilityManifestCanonicalAndComplete",
					"TestP8PublicGoAPIDiffGate",
					"TestP8GeneratedAndGraphQLCompatibilityGate",
				},
			},
			{
				Path:       "./cmd/golem",
				ImportPath: moduleImport + "/cmd/golem",
				Tests: []string{
					"TestP8P7ToReleaseUpgradeSQLite",
					"TestP8P7ToReleaseUpgradePostgreSQLProfiles",
					"TestP8P7ToReleaseUpgradePostgreSQLProfiles/postgresql-c",
					"TestP8P7ToReleaseUpgradePostgreSQLProfiles/postgresql-linguistic",
					"TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents",
					"TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents/sqlite",
					"TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents/postgresql-c",
					"TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents/postgresql-linguistic",
					"TestP8CLIJSONAndPersistedFormatCompatibilityGate",
					"TestP8StaleArtifactAndCompatibilityManifestRejection",
				},
			},
		},
		Profiles:   []string{"postgresql-c", "postgresql-linguistic", "sqlite"},
		Timeout:    timeout,
		WatchPaths: []string{moduleDir},
	}
}

func FailureSpec(moduleDir string, timeout time.Duration) Spec {
	tests := []string{
		"TestP8CancellationAndSlowClientRecoveryMatrix",
		"TestP8ProviderContentionAndPoolStarvationRecovery",
		"TestP8HookComputedAndObserverFailureIsolation",
		"TestP8PublisherCDCAndMigrationCrashRecovery",
		"TestP8GracefulAndForcedShutdownSubprocessMatrix",
	}
	profiles := []string{"postgresql-c", "postgresql-linguistic", "sqlite"}
	for _, base := range append([]string(nil), tests...) {
		for _, profile := range profiles {
			tests = append(tests, base+"/"+profile)
		}
	}
	sort.Strings(tests)
	return Spec{
		Command:   "p8failure",
		ModuleDir: moduleDir,
		Packages: []Package{{
			Path:       "./internal/p8oracle/failure",
			ImportPath: moduleImport + "/internal/p8oracle/failure",
			Tests:      tests,
		}},
		Profiles:   profiles,
		Timeout:    timeout,
		WatchPaths: []string{moduleDir},
	}
}
