package mutationtest

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

func TestMigrationExplainMutationCatalogIsExactAndEveryPatchSiteExists(t *testing.T) {
	want := []string{
		"MIGRATION_EXPLAIN_REORDER_TYPED_OPERATIONS",
		"MIGRATION_EXPLAIN_LABEL_DATA_LOSS_SAFE",
		"MIGRATION_EXPLAIN_LABEL_REWRITE_PRESERVING",
		"MIGRATION_EXPLAIN_LABEL_UNKNOWN_SAFE",
		"MIGRATION_EXPLAIN_OMIT_APPROVAL",
		"MIGRATION_EXPLAIN_OMIT_DEPENDENCY",
		"MIGRATION_EXPLAIN_OMIT_BACKFILL",
		"MIGRATION_EXPLAIN_OMIT_POSTCONDITION",
		"MIGRATION_EXPLAIN_CLAIM_ZERO_DOWNTIME",
		"MIGRATION_EXPLAIN_INVENT_DURATION",
		"MIGRATION_EXPLAIN_LEAK_RAW_SQL",
		"MIGRATION_EXPLAIN_LEAK_BOUND_VALUE",
		"MIGRATION_EXPLAIN_LEAK_DSN",
		"MIGRATION_EXPLAIN_LEAK_ABSOLUTE_PATH",
		"MIGRATION_EXPLAIN_LEAK_PHYSICAL_NAME",
		"MIGRATION_EXPLAIN_RENDER_BEFORE_VALIDATION",
		"MIGRATION_EXPLAIN_ACCEPT_TEXT_CONTROL",
		"MIGRATION_EXPLAIN_FILTER_BEFORE_PROVIDER_VALIDATION",
		"MIGRATION_EXPLAIN_PROSPECTIVE_WRITES",
		"MIGRATION_EXPLAIN_PROSPECTIVE_TEMP_LEAK",
		"MIGRATION_EXPLAIN_UNVERSIONED_JSON",
		"MIGRATION_EXPLAIN_OPEN_JSON",
		"MIGRATION_EXPLAIN_DIVERGE_COMPATIBILITY_PROJECTION",
		"MIGRATION_EXPLAIN_HIDE_FORMAT_BUMP_FROM_CLI_INVENTORY",
		"MIGRATION_EXPLAIN_CHANGE_OPTIONAL_WIRE_FIELD",
		"MIGRATION_EXPLAIN_OMIT_CLI_COMPATIBILITY_SOURCE",
	}
	got := make([]string, len(Catalog()))
	for index, mutation := range Catalog() {
		got[index] = mutation.Label
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mutation labels=%v want=%v", got, want)
	}
	if err := p8mutation.ValidateCatalog(Catalog()); err != nil {
		t.Fatal(err)
	}
	if err := p8mutation.ValidatePatchSites(repositoryRoot(t), Catalog()); err != nil {
		t.Fatal(err)
	}
}

// This exact gate is deliberately opt-in because it creates one isolated
// baseline/mutant module per record. Release evidence invokes it with the
// environment set; ordinary package tests still validate the complete catalog
// and every current patch site without reporting a skip.
func TestMigrationExplainMutationCatalogKillsEveryCompilingMutant(t *testing.T) {
	if os.Getenv("GOLEM_RUN_MIGRATION_EXPLAIN_MUTATIONS") != "1" {
		return
	}
	runner := p8mutation.Runner{Repository: repositoryRoot(t)}
	for _, mutation := range Catalog() {
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

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve mutation catalog source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", "..", ".."))
}
