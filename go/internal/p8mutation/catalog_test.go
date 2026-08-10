package p8mutation

import (
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
)

func TestP8MutationCatalogExactlyMatchesControllingMatrix(t *testing.T) {
	want := []string{
		"PUBLIC_PROVIDER_RETURNS_UNVERIFIED_DB",
		"SECOND_PROVIDER_ENUM_WINS",
		"ADOPT_ARBITRARY_SQLX_POOL",
		"SAFE_NAMED_RAW_SQLX_ESCAPE",
		"SQLITE_SKIP_CONNECTION_PRAGMAS",
		"SQLITE_DEFERRED_DEFAULT",
		"POSTGRES_FIRST_CONNECTION_ONLY",
		"POSTGRES_UNBOUNDED_POOL_DEFAULT",
		"LEAK_DSN_IN_ERROR",
		"APP_OPEN_APPLIES_MIGRATION",
		"APP_OPEN_STARTS_WORKER",
		"APP_CLOSES_BORROWED_DATABASE",
		"DOCTOR_REPAIRS_STATE",
		"DOCTOR_EMITS_SOURCE_OR_SCHEMA_NAME",
		"EXAMPLE_USES_LOCAL_REPLACE",
		"EXAMPLE_HANDWRITES_CRUD_RESOLVER",
		"GRAPHQL_SECOND_READ_ENGINE",
		"GRAPHQL_SECOND_MUTATION_ENGINE",
		"CUSTOM_ROOT_RECEIVES_SYSTEM_OR_DB",
		"HOOK_RESULT_BEFORE_VERIFICATION",
		"AFTER_COMMIT_ERROR_REWRITES_SUCCESS",
		"COMPUTED_PRIVATE_DEPENDENCY_ESCAPES",
		"SCOPED_SQL_SKIPS_HOP_POLICY",
		"ANALYTICS_PARTIAL_MASK",
		"EVENT_SURFACES_DIVERGE",
		"TELEMETRY_INCLUDES_RAW_ERROR",
		"TELEMETRY_INCLUDES_MODEL_OR_FIELD_NAME",
		"OBSERVER_PANIC_PROPAGATES",
		"OBSERVER_QUEUE_UNBOUNDED",
		"RELATION_LOAD_N_PLUS_ONE",
		"CANCEL_LEAKS_GOROUTINE_OR_CONNECTION",
		"SLOW_SUBSCRIBER_DROPS_AND_CONTINUES",
		"UPGRADE_REWRITES_EVENT_ID",
		"UPGRADE_ADVANCES_LEDGER_BEFORE_VERIFY",
		"UNKNOWN_CODEC_BEST_EFFORT_DECODE",
		"PATCH_BREAKS_GENERATED_ABI",
		"PATCH_BREAKS_GRAPHQL_SCHEMA",
		"REQUIRED_PROVIDER_JOB_SKIPS",
		"RELEASE_FROM_MOVING_BRANCH",
		"RELEASE_TAG_MODULE_MISMATCH",
		"REPLACE_EXISTING_RELEASE_BYTES",
		"DOCUMENT_UNSUPPORTED_FEATURE",
		"DOC_SNIPPET_NOT_COMPILED",
	}
	got := make([]string, len(Catalog()))
	for index, mutation := range Catalog() {
		got[index] = mutation.Label
	}
	sort.Strings(want)
	sort.Strings(got)
	if len(got) != 43 || !reflect.DeepEqual(got, want) {
		t.Fatalf("mutation catalog labels=%v want=%v", got, want)
	}
}

func TestP8MutationManifestIsClosedAndEveryPatchAppliesToCurrentSource(t *testing.T) {
	catalog := Catalog()
	if err := ValidateCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve mutation source")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	if err := ValidatePatchSites(repository, catalog); err != nil {
		t.Fatal(err)
	}
}
