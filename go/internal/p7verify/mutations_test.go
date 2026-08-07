package p7verify

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestP7MutationCatalogExactlyMatchesEvidenceInventoryAndPatchSites(t *testing.T) {
	wanted := []string{
		"MUTATE_FACT_ON_CLAIM", "ACK_BEFORE_TRANSPORT_SUCCESS", "NEW_EVENT_ID_ON_RETRY", "ACK_FOREIGN_OR_EXPIRED_LEASE",
		"LEASE_BY_WORKER_NAME", "LEASE_WITH_WORKER_CLOCK", "SQLITE_DEFERRED_CLAIM", "POSTGRES_NO_SKIP_LOCKED",
		"SPLIT_CAUSATION_CLAIM", "ORDER_BY_EVENT_ID", "CLAIM_GLOBAL_COMMIT_ORDER", "TRUST_DUPLICATE_COLUMNS",
		"CURRENT_GENERATION_ONLY", "EVENT_SCHEMA_INCLUDES_GRAPHQL_NAME", "ACCEPT_UNKNOWN_CODEC", "DROP_AFTER_MAX_ATTEMPTS",
		"SILENTLY_DROP_CORRUPT_FACT", "DELETE_PENDING_ON_RETENTION", "PARTIAL_BATCH_ACK", "SKIP_DELETE_SNAPSHOT",
		"EXPOSE_DELETE_SNAPSHOT", "FILTER_DELETED_SNAPSHOT_IN_GO", "REUSE_SUBSCRIBE_TIME_POLICY", "REUSE_SUBSCRIBE_TIME_ACTOR",
		"REUSE_EVENT_LOADERS", "SUBSCRIBE_WITHOUT_INITIAL_READ_AUTH", "AUTH_ONLY_WHEN_ENTITY_SELECTED", "GROUP_ACROSS_PRINCIPALS",
		"GROUP_WITHOUT_SELECTION_IN_KEY", "AUDIT_ID_IS_SECURITY_ID", "SHARE_HOOKED_OR_COMPUTED_EVALUATION", "DROP_ON_QUEUE_OVERFLOW",
		"UNBOUNDED_EVALUATION_GOROUTINES", "START_EVALUATION_AFTER_CANCEL", "GRAPHQL_SECOND_EVENT_ENGINE", "EMIT_DISABLED_SUBSCRIPTION",
		"HAND_SERIALIZE_WS_ENTITY", "ACCEPT_LEGACY_WS_AS_NEW", "OBSERVER_PANIC_PROPAGATES", "CLAIM_EXTERNAL_WRITES_VISIBLE_WITHOUT_CDC",
		"CDC_RANDOM_EVENT_ID", "CDC_CHECKPOINT_BEFORE_ACCEPT", "CDC_SECOND_CODEC_OR_AUTH_PATH", "CDC_DUPLICATES_GOLEM_OUTBOX_WRITE",
		"RUN_HOOK_ON_PUBLISH_RETRY", "SKIP_REQUIRED_POSTGRES_LIVE_GATE",
	}
	catalog := MutationCatalog()
	if len(catalog) != len(wanted) {
		t.Fatalf("catalog entries=%d want=%d", len(catalog), len(wanted))
	}
	root := filepath.Clean(filepath.Join("..", ".."))
	for index, mutation := range catalog {
		if mutation.Label != wanted[index] {
			t.Fatalf("catalog[%d]=%q want=%q", index, mutation.Label, wanted[index])
		}
		if !mutation.Covered() {
			t.Fatalf("mutation %s is not executable", mutation.Label)
		}
		for _, patch := range mutation.Patches {
			content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(patch.Path)))
			if err != nil {
				t.Fatalf("%s: %v", mutation.Label, err)
			}
			if count := bytes.Count(content, []byte(patch.Before)); count != 1 {
				t.Fatalf("%s patch %s matches=%d", mutation.Label, patch.Path, count)
			}
		}
	}
}
