package mutationtest

import (
	"context"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

func TestMigrationExplainAdapterMutationCatalogIsExactAndEveryPatchSiteExists(t *testing.T) {
	want := []string{
		"MIGRATION_PLAN_DIFF_OMITS_SNAPSHOT_FACTS",
		"MIGRATION_PLAN_NO_CHANGE_INVENTS_RECORD",
		"MIGRATION_PLAN_SHAPE_ACCEPTS_REORDERED_OPERATIONS",
		"MIGRATION_PLAN_SHAPE_ACCEPTS_UNKNOWN_PROVIDER",
		"MIGRATION_PLAN_SHAPE_ACCEPTS_FALSE_INITIAL",
		"MIGRATION_PLAN_SHAPE_ACCEPTS_INVENTED_HISTORICAL_IDENTITY",
		"PHYSICAL_HISTORICAL_ACCEPTS_UNREVIEWED_SQLITE_DRIVER",
		"MIGRATION_EXPLAIN_ADAPTER_ACCEPTS_FORGED_PLAN",
		"MIGRATION_EXPLAIN_RECREATION_USES_WHOLE_SNAPSHOT_PRESENCE",
		"MIGRATION_EXPLAIN_REVIEWED_SKIPS_FILE_CHECKSUMS",
		"MIGRATION_EXPLAIN_ADAPTER_LABELS_UNSAFE_TYPE_SAFE",
	}
	got := make([]string, len(AdapterCatalog()))
	for index, mutation := range AdapterCatalog() {
		got[index] = mutation.Label
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adapter mutation labels=%v want=%v", got, want)
	}
	if err := p8mutation.ValidateCatalog(AdapterCatalog()); err != nil {
		t.Fatal(err)
	}
	if err := p8mutation.ValidatePatchSites(repositoryRoot(t), AdapterCatalog()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationExplainAdapterMutationCatalogKillsEveryCompilingMutant(t *testing.T) {
	if os.Getenv("GOLEM_RUN_MIGRATION_EXPLAIN_ADAPTER_MUTATIONS") != "1" {
		return
	}
	runner := p8mutation.Runner{Repository: repositoryRoot(t)}
	for _, mutation := range AdapterCatalog() {
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
