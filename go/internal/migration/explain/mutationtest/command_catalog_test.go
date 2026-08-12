package mutationtest

import (
	"context"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

func TestMigrationPlanCommandMutationCatalogIsExactAndEveryPatchSiteExists(t *testing.T) {
	want := []string{
		"MIGRATION_PLAN_COMMAND_FILTERS_PROVIDER_BEFORE_COMPLETE_VALIDATION",
		"MIGRATION_PLAN_COMMAND_PROSPECTIVE_WRITES_MODULE",
		"MIGRATION_PLAN_COMMAND_PROSPECTIVE_LEAKS_TEMP",
		"MIGRATION_PLAN_COMMAND_RENDERS_BEFORE_PROVIDER_SQL_VALIDATION",
		"MIGRATION_PLAN_COMMAND_REVIEWED_FLAG_BYPASS",
	}
	got := make([]string, len(CommandCatalog()))
	for index, mutation := range CommandCatalog() {
		got[index] = mutation.Label
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command mutation labels=%v want=%v", got, want)
	}
	if err := p8mutation.ValidateCatalog(CommandCatalog()); err != nil {
		t.Fatal(err)
	}
	if err := p8mutation.ValidatePatchSites(repositoryRoot(t), CommandCatalog()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationPlanCommandMutationCatalogKillsEveryCompilingMutant(t *testing.T) {
	if os.Getenv("GOLEM_RUN_MIGRATION_PLAN_COMMAND_MUTATIONS") != "1" {
		return
	}
	runner := p8mutation.Runner{Repository: repositoryRoot(t)}
	for _, mutation := range CommandCatalog() {
		mutation := mutation
		t.Run(mutation.Label, func(t *testing.T) {
			result, err := runner.Run(context.Background(), mutation)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != p8mutation.StatusKilled {
				t.Fatalf("status=%s detail=%s baseline=%s mutant=%s", result.Status, result.Detail, result.BaselineEventSHA256, result.MutantEventSHA256)
			}
		})
	}
}
