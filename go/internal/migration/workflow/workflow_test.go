package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	codegenmanifest "github.com/eleven-am/golem/go/internal/codegen/manifest"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/generate/publication"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/migration/explain"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestReviewedBackfillSealingUsesReviewedHistoryAuthorities(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate workflow source")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "backfill.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Count(text, "migration.DiffReviewed(") != 2 || strings.Contains(text, "migration.Diff(") {
		t.Fatal("reviewed backfill validation or sealing routes through the mutable current planner")
	}
	if strings.Count(text, "historicalSnapshotBytes(entry.") != 2 || strings.Contains(text, "snapshotBytes(entry.") {
		t.Fatal("reviewed backfill sealing routes through mutable current snapshot JSON")
	}
}

func TestLoadModelHistoryRejectsEmptyAndNonemptyProvidersInLockstepRegardlessOfMapOrder(t *testing.T) {
	entry := migration.ManifestEntry{ID: "0001_initial"}
	for iteration := 0; iteration < 128; iteration++ {
		histories := make(map[ir.Provider]History, 2)
		if iteration%2 == 0 {
			histories[ir.SQLite] = History{Manifest: migration.Manifest{}}
			histories[ir.PostgreSQL] = History{Manifest: migration.Manifest{Entries: []migration.ManifestEntry{entry}}}
		} else {
			histories[ir.PostgreSQL] = History{Manifest: migration.Manifest{Entries: []migration.ManifestEntry{entry}}}
			histories[ir.SQLite] = History{Manifest: migration.Manifest{}}
		}

		state := State{}
		err := loadModelHistory(DefaultRoot, nil, histories, &state)
		if err == nil || err.Error() != "declared provider migration heads are not in lockstep" {
			t.Fatalf("iteration %d empty/nonempty provider history error=%v", iteration, err)
		}
	}
}

func TestLoadModelHistoryAllowsAllProvidersToHaveEmptyDetachedHistory(t *testing.T) {
	histories := map[ir.Provider]History{
		ir.SQLite:     {Manifest: migration.Manifest{Entries: []migration.ManifestEntry{}}},
		ir.PostgreSQL: {Manifest: migration.Manifest{Entries: []migration.ManifestEntry{}}},
	}
	state := State{}
	if err := loadModelHistory(DefaultRoot, nil, histories, &state); err != nil {
		t.Fatalf("all-empty provider history: %v", err)
	}
	if state.HeadModel != nil || state.HeadModelFingerprint != "" {
		t.Fatalf("all-empty provider history attached a ModelIR head: %#v", state)
	}
}

func TestCheckedSocialFrozenHistoryLoadsWithoutRewritingReleasedBytes(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate workflow test source")
	}
	module := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	social := filepath.Join(module, "examples", "social")
	released := map[string]string{
		"migrations/models/0001_initial.after.snapshot.json":                 "54eca1411dfe1d2bdda60a47f7673d416194382256564311e3515f0ac975a667",
		"migrations/models/0001_initial.before.snapshot.json":                "69a22ca69b22b9fe854f41799b3bcdda3ab776f9e2f5c377e90ddd08348d4354",
		"migrations/models/0002_sqlite_vec_runtime.after.snapshot.json":      "54eca1411dfe1d2bdda60a47f7673d416194382256564311e3515f0ac975a667",
		"migrations/models/0002_sqlite_vec_runtime.before.snapshot.json":     "54eca1411dfe1d2bdda60a47f7673d416194382256564311e3515f0ac975a667",
		"migrations/models/0003_physical_v2.after.snapshot.json":             "54eca1411dfe1d2bdda60a47f7673d416194382256564311e3515f0ac975a667",
		"migrations/models/0003_physical_v2.before.snapshot.json":            "54eca1411dfe1d2bdda60a47f7673d416194382256564311e3515f0ac975a667",
		"migrations/postgresql/0001_initial.after.snapshot.json":             "233a849c4c796d68d65b889cec6f79664d3814205dabcbeeb6c093135a34ec20",
		"migrations/postgresql/0001_initial.before.snapshot.json":            "26b293bbeb7360cc4533a7cc4494e6472301d6e7bfb7969756612ac959002b60",
		"migrations/postgresql/0001_initial.sql":                             "196bfebf70f8c4b2487bf87b5ff1a7a5ec94251967e10701aab5566deafc2136",
		"migrations/postgresql/0002_sqlite_vec_runtime.after.snapshot.json":  "233a849c4c796d68d65b889cec6f79664d3814205dabcbeeb6c093135a34ec20",
		"migrations/postgresql/0002_sqlite_vec_runtime.before.snapshot.json": "233a849c4c796d68d65b889cec6f79664d3814205dabcbeeb6c093135a34ec20",
		"migrations/postgresql/0002_sqlite_vec_runtime.sql":                  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"migrations/postgresql/0003_physical_v2.after.snapshot.json":         "266f71c8c88384e415ceeb2cb259a2d7d1a3e5fbff6238e874c73165a15e4a48",
		"migrations/postgresql/0003_physical_v2.before.snapshot.json":        "233a849c4c796d68d65b889cec6f79664d3814205dabcbeeb6c093135a34ec20",
		"migrations/postgresql/0003_physical_v2.sql":                         "ffdff3e2c4dc6f86faa17a1cca62bc1d02b62ef2ba616ba6a4622e6316aafea3",
		"migrations/sqlite/0001_initial.after.snapshot.json":                 "29a59ec1e076a07cdcf6a5682114780930eb55b99a8c7c8b8c4b5a13bf6b8c6a",
		"migrations/sqlite/0001_initial.before.snapshot.json":                "e97a1fe22dd3ad198cfb0a6547ca3e63588162af571f64b3a976c4b8b9e85e77",
		"migrations/sqlite/0001_initial.sql":                                 "1ad19cf589c5b69f97f95290be4585efe0e3d409a8e1e5f39d9b49d29577e674",
		"migrations/sqlite/0002_sqlite_vec_runtime.after.snapshot.json":      "c36334f25fdbc61774103764efa2422d75fa5fab2139b8ef19e3241d3cf2e4f9",
		"migrations/sqlite/0002_sqlite_vec_runtime.before.snapshot.json":     "29a59ec1e076a07cdcf6a5682114780930eb55b99a8c7c8b8c4b5a13bf6b8c6a",
		"migrations/sqlite/0002_sqlite_vec_runtime.sql":                      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"migrations/sqlite/0003_physical_v2.after.snapshot.json":             "13ed85c858b59f606048ea916997af2f5c7b0c4773059264c118266c66846c6d",
		"migrations/sqlite/0003_physical_v2.before.snapshot.json":            "c36334f25fdbc61774103764efa2422d75fa5fab2139b8ef19e3241d3cf2e4f9",
		"migrations/sqlite/0003_physical_v2.sql":                             "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}
	assertReleasedSocialHistory := func() {
		t.Helper()
		for relative, want := range released {
			content, err := os.ReadFile(filepath.Join(social, filepath.FromSlash(relative)))
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(content)); got != want {
				t.Fatalf("released social history %s digest=%s want=%s", relative, got, want)
			}
		}
	}
	assertReleasedSocialHistory()
	manifestBytes, err := os.ReadFile(filepath.Join(social, "migrations", "sqlite", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := migration.ParseManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := historicalSnapshotBytes(manifest.Entries[1].AfterSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	releasedSnapshot, err := os.ReadFile(filepath.Join(social, "migrations", "sqlite", "0002_sqlite_vec_runtime.after.snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reencoded, releasedSnapshot) {
		index := 0
		for index < len(reencoded) && index < len(releasedSnapshot) && reencoded[index] == releasedSnapshot[index] {
			index++
		}
		start := index - 80
		if start < 0 {
			start = 0
		}
		end := index + 120
		if end > len(reencoded) {
			end = len(reencoded)
		}
		t.Fatalf("released snapshot re-encoding differs at byte %d: got=%q", index, reencoded[start:end])
	}
	if bytes.Contains(reencoded, []byte(`"OptimisticConcurrency"`)) {
		t.Fatal("pre-v3 snapshot re-encoding leaked the v3 optimistic-concurrency field")
	}
	state, err := loadReviewedState(context.Background(), social, DefaultRoot)
	if err != nil {
		t.Fatalf("load checked social frozen pre-v3 history: %v", err)
	}
	assertReleasedSocialHistory()
	for provider, want := range map[ir.Provider][]migration.Digest{
		ir.SQLite:     {"1460e997c8a275beb30c76a948fa6abeda092f4d36b93851e6f0ca2ad6865249", "b1ed9d3bd9207d69d80e1affabfad46f412afbab1183383200e4b59852ba81b7", "164a5de13865f9e2d4c8f424e494c764519f77b367b637510982e79f95d501d3", "2978bcfae671e90339b453462d4e8a913f9580fd8f69cc6ab7d3af489798081f", "b3a0f0fbf632f0984d1c3986d8ff9bcf6ba3d0724b52ba1b545c68172f4624d7", "0cb1bfe8aa21a6613e384b857a2ee65f30cd066d2f0fae2b422aa12352701620"},
		ir.PostgreSQL: {"1e6c1e82f8f56994b90b24697563f6ca4df9616b059a06736b2d0a1372ee1477", "3ab580245cb5f27bed603d293e514285906860dff09b854f35752c8ce3215578", "19c9b8114ba2f7cf7853e13bd2970d56aaab06db749f715f6442e9b45e9512b6", "99734d0bfb411d1a99100c3fa39acd298dfaae0cbfde310f8fb3206007c9bb92", "bb44afcbdc64dcda37b4608e2a569a771899bb25c3d209dbe9746d3e4b0847c7", "048aaeb08f2d4f96c66d6d67ca39c802827c9c477981a9cb2f208310fe9446d7"},
	} {
		entries := state.Histories[provider].Manifest.Entries
		if len(entries) != len(want) {
			t.Fatalf("provider %s released entry count=%d", provider, len(entries))
		}
		for index := range entries {
			if entries[index].ChainHash != want[index] {
				t.Fatalf("provider %s entry %d chain hash=%s want=%s", provider, index, entries[index].ChainHash, want[index])
			}
		}
	}
	renderers := map[ir.Provider]func(migration.ManifestEntry) ([]byte, error){
		ir.SQLite: func(entry migration.ManifestEntry) ([]byte, error) {
			script, renderErr := sqlite.New().RenderMigration(entry)
			return []byte(script.SQL()), renderErr
		},
		ir.PostgreSQL: func(entry migration.ManifestEntry) ([]byte, error) {
			script, renderErr := postgresql.New().RenderMigration(entry)
			return []byte(script.SQL()), renderErr
		},
	}
	for provider, history := range state.Histories {
		for _, entry := range history.Manifest.Entries {
			rendered, renderErr := renderers[provider](entry)
			if renderErr != nil {
				t.Fatalf("provider %s migration %s render: %v", provider, entry.ID, renderErr)
			}
			var reviewedSQL []byte
			for _, file := range entry.Files {
				if strings.HasSuffix(file.Path, ".sql") {
					reviewedSQL = history.Files[file.Path]
				}
			}
			if !bytes.Equal(rendered, reviewedSQL) {
				t.Fatalf("provider %s migration %s rendering changed released SQL bytes: got=%q want=%q", provider, entry.ID, rendered, reviewedSQL)
			}
		}
	}
}

func TestSQLiteProviderHistoryAllowsOnlyTheReviewedDriverTransition(t *testing.T) {
	vec := physical.CapabilityFact{ID: "sqlite.vec0.v1", Version: 1, Verification: physical.VerificationRuntimeProbe}
	current := physical.SQLiteManifest(vec)
	historical := current
	historical.Driver = physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
	historical.Capabilities = append([]physical.CapabilityFact(nil), historical.Capabilities[:len(historical.Capabilities)-1]...)
	if !physical.CompatibleProviderHistory(historical, current) {
		t.Fatal("reviewed modernc-to-ncruces transition was rejected")
	}
	forged := historical
	forged.Driver.Module = "example.com/unknown"
	if physical.CompatibleProviderHistory(forged, current) {
		t.Fatal("unknown historical driver was accepted")
	}
	downgraded := current
	downgraded.Capabilities = nil
	if physical.CompatibleProviderHistory(historical, downgraded) {
		t.Fatal("capability-losing provider transition was accepted")
	}
}

func TestInitialIncrementalDeterminismAndNoOpRefusal(t *testing.T) {
	compiled, providers := socialResult(t)
	module := t.TempDir()
	initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
	if initial.MigrationID != "0001_initial" || len(initial.Providers) != 2 {
		t.Fatalf("initial result = %#v", initial)
	}
	publish(t, module, initial)
	assertHistoryArtifacts(t, module, "0001_initial", 2)

	previous := compiled.Compilation.Model
	current := cloneModel(t, previous)
	current.Schema.StableName += "_v2"
	currentFingerprint, _ := ir.ModelFingerprint(current)
	updatedProviders := addPortableIndex(t, providers)
	incremental := prepareWithFingerprint(t, module, "add_lookup", current, &previous, currentFingerprint, compiled.ContractFingerprint, updatedProviders, nil)
	reversedProviders := append([]Provider(nil), updatedProviders...)
	for left, right := 0, len(reversedProviders)-1; left < right; left, right = left+1, right-1 {
		reversedProviders[left], reversedProviders[right] = reversedProviders[right], reversedProviders[left]
	}
	repeated := prepareWithFingerprint(t, module, "add_lookup", current, &previous, currentFingerprint, compiled.ContractFingerprint, reversedProviders, nil)
	if !bytes.Equal(incremental.Prospective.Bytes, repeated.Prospective.Bytes) || incremental.MigrationID != "0002_add_lookup" {
		t.Fatal("incremental migration preparation is not deterministic")
	}
	publish(t, module, incremental)
	assertHistoryArtifacts(t, module, "0002_add_lookup", 2)

	head := current
	_, err := PrepareNew(context.Background(), NewRequest{
		ModuleDir: module, Root: DefaultRoot, Name: "nothing", Model: current, PreviousModel: &head,
		ModelFingerprint: currentFingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: updatedProviders,
	})
	if err == nil || !strings.Contains(err.Error(), "no logical or physical schema changes") {
		t.Fatalf("pure no-op migration error = %v", err)
	}
	state, err := Load(context.Background(), module, DefaultRoot, updatedProviders)
	if err != nil {
		t.Fatal(err)
	}
	for provider, history := range state.Histories {
		if len(history.Manifest.Entries) != 2 || history.Manifest.Entries[1].ID != "0002_add_lookup" {
			t.Fatalf("provider %s history = %#v", provider, history.Manifest.Entries)
		}
	}
}

func TestTamperAndExactDestructiveApprovals(t *testing.T) {
	compiled, providers := socialResult(t)
	module := t.TempDir()
	initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
	publish(t, module, initial)
	sqlPath := filepath.Join(module, DefaultRoot, "sqlite", "0001_initial.sql")
	original, err := os.ReadFile(sqlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sqlPath, append(original, []byte("-- tampered\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), module, DefaultRoot, providers); err == nil || !strings.Contains(err.Error(), "rewritten") {
		t.Fatalf("tampered immutable SQL error = %v", err)
	}
	if err := os.WriteFile(sqlPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	previous := compiled.Compilation.Model
	current := cloneModel(t, previous)
	var removedModel ir.ModelID
	var removedField ir.FieldID
	for modelIndex := range current.Models {
		if current.Models[modelIndex].Go.Name != "Post" {
			continue
		}
		removedModel = current.Models[modelIndex].ID
		for fieldIndex, field := range current.Models[modelIndex].Fields {
			if field.GoName == "Body" {
				removedField = field.ID
				current.Models[modelIndex].Fields = append(current.Models[modelIndex].Fields[:fieldIndex:fieldIndex], current.Models[modelIndex].Fields[fieldIndex+1:]...)
				break
			}
		}
	}
	currentFingerprint, _ := ir.ModelFingerprint(current)
	droppedProviders := dropField(t, providers, removedModel, removedField)
	_, err = PrepareNew(context.Background(), NewRequest{
		ModuleDir: module, Root: DefaultRoot, Name: "drop_model", Model: current, PreviousModel: &previous,
		ModelFingerprint: currentFingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: droppedProviders,
	})
	if err == nil || !strings.Contains(err.Error(), "risk dataLoss requires --approve") || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("missing exact approval error = %v", err)
	}
	var approvals []migration.OperationID
	for index, provider := range droppedProviders {
		plan, diffErr := migration.Diff(providers[index].Result.Schema, provider.Result.Schema)
		if diffErr != nil {
			t.Fatal(diffErr)
		}
		for _, operation := range plan.Operations {
			if operation.Risk == migration.RiskDataLoss || operation.Risk == migration.RiskManual {
				approvals = append(approvals, operation.ID)
			}
		}
	}
	approved := prepareWithFingerprint(t, module, "drop_model", current, &previous, currentFingerprint, compiled.ContractFingerprint, droppedProviders, approvals)
	if approved.MigrationID != "0002_drop_model" {
		t.Fatalf("approved migration ID = %s", approved.MigrationID)
	}
	reversedProviders := append([]Provider(nil), droppedProviders...)
	for left, right := 0, len(reversedProviders)-1; left < right; left, right = left+1, right-1 {
		reversedProviders[left], reversedProviders[right] = reversedProviders[right], reversedProviders[left]
	}
	reversedApprovals := append([]migration.OperationID(nil), approvals...)
	for left, right := 0, len(reversedApprovals)-1; left < right; left, right = left+1, right-1 {
		reversedApprovals[left], reversedApprovals[right] = reversedApprovals[right], reversedApprovals[left]
	}
	reordered := prepareWithFingerprint(t, module, "drop_model", current, &previous, currentFingerprint, compiled.ContractFingerprint, reversedProviders, reversedApprovals)
	if !bytes.Equal(approved.Prospective.Bytes, reordered.Prospective.Bytes) {
		t.Fatal("provider or approval registry order changed the prospective migration bytes")
	}
	unknown := append(append([]migration.OperationID(nil), approvals...), "unknown")
	if _, err := PrepareNew(context.Background(), NewRequest{
		ModuleDir: module, Root: DefaultRoot, Name: "drop_model", Model: current, PreviousModel: &previous,
		ModelFingerprint: currentFingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: droppedProviders, Approvals: unknown,
	}); err == nil || !strings.Contains(err.Error(), "does not name a current") {
		t.Fatalf("unknown approval error = %v", err)
	}
}

func TestPostgreSQLReviewedBackfillAttachProducesCanonicalImmutableHistory(t *testing.T) {
	compiled, allProviders := socialResult(t)
	postgresProviders := providerSubset(t, allProviders, ir.PostgreSQL)
	module := t.TempDir()
	initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, postgresProviders, nil)
	publish(t, module, initial)
	beforeTree := tree(t, module)

	previous := compiled.Compilation.Model
	current, currentProviders, modelID, fieldID := addRequiredPostSlug(t, previous, postgresProviders)
	fingerprint, err := ir.ModelFingerprint(current)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := PrepareNew(context.Background(), NewRequest{ModuleDir: module, Root: DefaultRoot, Name: "add_post_slug", Model: current, PreviousModel: &previous, ModelFingerprint: fingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: currentProviders})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Pending == nil || pending.MigrationID != "0002_add_post_slug" || pending.Prospective.Bytes != nil {
		t.Fatalf("pending result = %#v", pending)
	}
	if afterPrepare := tree(t, module); !reflect.DeepEqual(afterPrepare, beforeTree) {
		t.Fatal("PrepareNew mutated canonical history before attach")
	}
	draftPath, err := WritePendingBackfill(module, *pending.Pending)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), module, DefaultRoot, postgresProviders); err != nil {
		t.Fatalf("non-authoritative draft affected publication loading: %v", err)
	}
	if _, err := WritePendingBackfill(module, *pending.Pending); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("draft overwrite error = %v", err)
	}

	reviewed := []byte(`UPDATE "public"."posts" SET "slug" = lower("title") WHERE "slug" IS NULL;` + "\n")
	attached, err := PrepareBackfillAttach(context.Background(), BackfillAttachRequest{ModuleDir: module, Root: DefaultRoot, MigrationID: pending.MigrationID, Field: "Post.Slug", SQL: reviewed, Render: currentProviders[0].Render})
	if err != nil {
		t.Fatal(err)
	}
	priorModelBytes, err := os.ReadFile(filepath.Join(module, DefaultRoot, "models", "0001_initial.after.snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var attachedBeforeModelBytes []byte
	for _, artifact := range attached.Prospective.Artifacts {
		if artifact.Path == filepath.ToSlash(filepath.Join(DefaultRoot, "models", "0002_add_post_slug.before.snapshot.json")) {
			attachedBeforeModelBytes = artifact.Content
		}
	}
	if !bytes.Equal(attachedBeforeModelBytes, priorModelBytes) {
		t.Fatal("reviewed backfill attach did not copy exact prior after-model bytes")
	}
	if attached.Entry.ChainHash == "" || len(attached.Entry.Manual) != 1 || attached.Entry.Manual[0].File.SHA256 != migration.Checksum(reviewed) || attached.Provider != ir.PostgreSQL {
		t.Fatalf("sealed entry is incomplete: %#v", attached.Entry)
	}
	planText, err := explain.MarshalText(attached.Report)
	if err != nil || attached.Report.Mode() != explain.ModeReviewed || !bytes.Contains(planText, []byte("reviewed backfill")) || !bytes.Contains(planText, []byte(string(attached.Entry.Manual[0].Postcondition))) {
		t.Fatalf("reviewed attach report is incomplete: report=%#v text=%q err=%v", attached.Report, planText, err)
	}
	if owner, ok := migration.BackfillOwner(attached.Entry.AfterSnapshot, fieldID); !ok || owner != modelID {
		t.Fatalf("backfill owner = %s,%v want %s", owner, ok, modelID)
	}
	published, err := publication.Apply(context.Background(), publication.Request{ModuleDir: module, ManifestPath: attached.PublicationPath, Prospective: attached.Prospective})
	if err != nil || len(published.Changed) == 0 {
		t.Fatalf("publish attach: changed=%v err=%v", published.Changed, err)
	}
	if err := RemovePendingBackfill(module, attached.PendingPath, attached.PendingSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(module, filepath.FromSlash(draftPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending draft remains after seal: %v", err)
	}
	state, err := Load(context.Background(), module, DefaultRoot, currentProviders)
	if err != nil {
		t.Fatal(err)
	}
	history := state.Histories[ir.PostgreSQL]
	if len(history.Manifest.Entries) != 2 || history.Manifest.Entries[1].ChainHash != attached.Entry.ChainHash {
		t.Fatalf("sealed history = %#v", history.Manifest.Entries)
	}
	companion := attached.Entry.Manual[0].File.Path
	if got := history.Files[companion]; !bytes.Equal(got, reviewed) {
		t.Fatalf("reviewed companion changed: %q", got)
	}
	if err := os.WriteFile(filepath.Join(module, filepath.FromSlash(companion)), append(reviewed, []byte("-- changed\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), module, DefaultRoot, currentProviders); err == nil || !strings.Contains(err.Error(), "rewritten") {
		t.Fatalf("immutable companion rewrite error = %v", err)
	}
}

func TestPostgreSQLReviewedBackfillAttachRefusesWrongFieldStaleParentAndInvalidArtifact(t *testing.T) {
	compiled, allProviders := socialResult(t)
	providers := providerSubset(t, allProviders, ir.PostgreSQL)
	newDraft := func(t *testing.T) (string, NewResult, []Provider) {
		module := t.TempDir()
		initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
		publish(t, module, initial)
		previous := compiled.Compilation.Model
		current, nextProviders, _, _ := addRequiredPostSlug(t, previous, providers)
		fingerprint, _ := ir.ModelFingerprint(current)
		pending, err := PrepareNew(context.Background(), NewRequest{ModuleDir: module, Root: DefaultRoot, Name: "add_post_slug", Model: current, PreviousModel: &previous, ModelFingerprint: fingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: nextProviders})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := WritePendingBackfill(module, *pending.Pending); err != nil {
			t.Fatal(err)
		}
		return module, pending, nextProviders
	}
	reviewed := []byte("UPDATE posts SET slug = title WHERE slug IS NULL;\n")

	t.Run("wrong field", func(t *testing.T) {
		module, pending, nextProviders := newDraft(t)
		if _, err := PrepareBackfillAttach(context.Background(), BackfillAttachRequest{ModuleDir: module, Root: DefaultRoot, MigrationID: pending.MigrationID, Field: "Post.Title", SQL: reviewed, Render: nextProviders[0].Render}); err == nil || !strings.Contains(err.Error(), "already existed") {
			t.Fatalf("wrong-field error = %v", err)
		}
	})
	t.Run("invalid artifact", func(t *testing.T) {
		module, pending, nextProviders := newDraft(t)
		if _, err := PrepareBackfillAttach(context.Background(), BackfillAttachRequest{ModuleDir: module, Root: DefaultRoot, MigrationID: pending.MigrationID, Field: "Post.Slug", SQL: []byte("UPDATE posts SET slug = title;\r\n"), Render: nextProviders[0].Render}); err == nil || !strings.Contains(err.Error(), "LF endings") {
			t.Fatalf("invalid-artifact error = %v", err)
		}
	})
	t.Run("stale parent publication", func(t *testing.T) {
		module, pending, nextProviders := newDraft(t)
		publicationPath := filepath.Join(module, DefaultRoot, PublicationFilename)
		content, err := os.ReadFile(publicationPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(publicationPath, append(content, ' '), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareBackfillAttach(context.Background(), BackfillAttachRequest{ModuleDir: module, Root: DefaultRoot, MigrationID: pending.MigrationID, Field: "Post.Slug", SQL: reviewed, Render: nextProviders[0].Render}); err == nil {
			t.Fatal("stale parent publication was accepted")
		}
	})
}

func TestInterruptedPublicationRecoversWithoutPartialHistory(t *testing.T) {
	compiled, providers := socialResult(t)
	module := t.TempDir()
	initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
	publish(t, module, initial)
	before := tree(t, module)
	previous := compiled.Compilation.Model
	current := previous
	current.Schema.StableName += "_next"
	fingerprint, _ := ir.ModelFingerprint(current)
	next := prepareWithFingerprint(t, module, "next", current, &previous, fingerprint, compiled.ContractFingerprint, addPortableIndex(t, providers), nil)
	injected := false
	_, err := publication.Apply(context.Background(), publication.Request{
		ModuleDir: module, ManifestPath: next.PublicationPath, Prospective: next.Prospective,
		Inject: func(step publication.Step, _ string) error {
			if !injected && step == publication.StepJournaled {
				injected = true
				return errors.New("simulated crash")
			}
			return nil
		},
	})
	if err == nil || !injected {
		t.Fatalf("publication interruption = %v", err)
	}
	if err := publication.Recover(context.Background(), module, next.PublicationPath); err != nil {
		t.Fatal(err)
	}
	if after := tree(t, module); !reflect.DeepEqual(before, after) {
		t.Fatal("recovery left a partial migration history")
	}
}

func TestMigrationPublicationRejectsStaleSystemFingerprint(t *testing.T) {
	compiled, providers := socialResult(t)
	staleProviders := append([]Provider(nil), providers...)
	staleProviders[0].Result.SystemFingerprint = ir.Fingerprint(strings.Repeat("f", 64))
	if _, err := PrepareNew(context.Background(), NewRequest{
		ModuleDir: t.TempDir(), Root: DefaultRoot, Name: "initial", Model: compiled.Compilation.Model,
		ModelFingerprint: compiled.ModelFingerprint, ContractFingerprint: compiled.ContractFingerprint, Providers: staleProviders,
	}); err == nil || !strings.Contains(err.Error(), "stale system fingerprint") {
		t.Fatalf("stale provider input error=%v", err)
	}
	module := t.TempDir()
	initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
	publish(t, module, initial)
	publicationPath := filepath.Join(module, DefaultRoot, PublicationFilename)
	encoded, err := os.ReadFile(publicationPath)
	if err != nil {
		t.Fatal(err)
	}
	current, err := codegenmanifest.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	fingerprints := append([]codegenmanifest.ProviderFingerprint(nil), current.ProviderFingerprints...)
	fingerprints[0].SystemFingerprint = ir.Fingerprint(strings.Repeat("f", 64))
	rebuilt, err := codegenmanifest.Build(codegenmanifest.Request{
		ModelFingerprint: current.ModelFingerprint, ContractFingerprint: current.ContractFingerprint,
		ProviderFingerprints: fingerprints, GeneratorVersion: current.GeneratorVersion,
		TemplateABIVersion: current.TemplateABIVersion, Artifacts: publicationArtifacts(t, module, current),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.GenerationDigest == current.GenerationDigest {
		t.Fatal("system-only publication fingerprint change did not alter GenerationDigest")
	}
	if err := os.WriteFile(publicationPath, rebuilt.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadModelHead(module, DefaultRoot); err == nil || !strings.Contains(err.Error(), "system fingerprint differs") {
		t.Fatalf("stale system fingerprint error=%v", err)
	}
}

func TestReadModelHeadUsesFullPublicationIntegrityChecks(t *testing.T) {
	compiled, providers := socialResult(t)
	for _, test := range []struct {
		name  string
		add   func(string, codegenmanifest.Manifest) codegenmanifest.Artifact
		match string
	}{
		{
			name: "out of scope",
			add: func(_ string, _ codegenmanifest.Manifest) codegenmanifest.Artifact {
				return codegenmanifest.Artifact{Path: "outside.sql", Kind: codegenmanifest.ArtifactMigrationSQL, Content: []byte("SELECT 1;\n"), Immutable: true}
			},
			match: "out-of-scope",
		},
		{
			name: "duplicate provider manifest",
			add: func(module string, _ codegenmanifest.Manifest) codegenmanifest.Artifact {
				content, err := os.ReadFile(filepath.Join(module, DefaultRoot, "sqlite", "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				return codegenmanifest.Artifact{Path: DefaultRoot + "/sqlite/other-manifest.json", Kind: codegenmanifest.ArtifactMigrationManifest, Content: content}
			},
			match: "duplicate provider manifest",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			module := t.TempDir()
			initial := prepare(t, module, "initial", compiled.Compilation.Model, nil, compiled.ContractFingerprint, providers, nil)
			publish(t, module, initial)
			publicationPath := filepath.Join(module, DefaultRoot, PublicationFilename)
			encoded, err := os.ReadFile(publicationPath)
			if err != nil {
				t.Fatal(err)
			}
			current, err := codegenmanifest.Parse(encoded)
			if err != nil {
				t.Fatal(err)
			}
			artifacts := publicationArtifacts(t, module, current)
			extra := test.add(module, current)
			artifacts = append(artifacts, extra)
			absolute := filepath.Join(module, filepath.FromSlash(extra.Path))
			if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(absolute, extra.Content, 0o644); err != nil {
				t.Fatal(err)
			}
			rebuilt, err := codegenmanifest.Build(codegenmanifest.Request{
				ModelFingerprint: current.ModelFingerprint, ContractFingerprint: current.ContractFingerprint,
				ProviderFingerprints: current.ProviderFingerprints, GeneratorVersion: current.GeneratorVersion,
				TemplateABIVersion: current.TemplateABIVersion, Artifacts: artifacts,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(publicationPath, rebuilt.Bytes, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadModelHead(module, DefaultRoot); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("ReadModelHead error = %v; want %q", err, test.match)
			}
		})
	}
}

func socialResult(t *testing.T) (pipeline.Result, []Provider) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	result, err := pipeline.Build(context.Background(), pipeline.Request{
		Compile:  compile.Config{Dir: root, Pattern: "./cmd/golem/testdata/social", Root: "DefineSchema"},
		Lowerers: []physical.Lowerer{sqlite.New(), postgresql.New()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result, providerRegistry(t, result.Providers)
}

func providerRegistry(t *testing.T, results []pipeline.ProviderResult) []Provider {
	t.Helper()
	providers := make([]Provider, 0, len(results))
	for _, result := range results {
		result := result
		switch result.Provider.Provider {
		case ir.SQLite:
			provider := sqlite.New()
			providers = append(providers, Provider{Result: result, Render: func(entry migration.ManifestEntry) ([]byte, error) {
				script, err := provider.RenderMigration(entry)
				return []byte(script.SQL()), err
			}})
		case ir.PostgreSQL:
			provider := postgresql.New()
			providers = append(providers, Provider{Result: result, Render: func(entry migration.ManifestEntry) ([]byte, error) {
				plan, err := migration.DiffReviewed(entry.BeforeSnapshot, entry.AfterSnapshot)
				if err != nil {
					return nil, err
				}
				if plan.Initial {
					script, renderErr := provider.RenderInitial(entry.AfterSnapshot)
					return []byte(script.SQL()), renderErr
				}
				script, renderErr := provider.PlanIncremental(entry)
				return []byte(script.SQL()), renderErr
			}})
		default:
			t.Fatalf("unsupported provider %s", result.Provider.Provider)
		}
	}
	return providers
}

func providerSubset(t *testing.T, providers []Provider, wanted ir.Provider) []Provider {
	t.Helper()
	for _, provider := range providers {
		if provider.Result.Provider.Provider == wanted {
			return []Provider{provider}
		}
	}
	t.Fatalf("provider %s is absent", wanted)
	return nil
}

func addRequiredPostSlug(t *testing.T, model ir.ModelIR, providers []Provider) (ir.ModelIR, []Provider, ir.ModelID, ir.FieldID) {
	t.Helper()
	current := cloneModel(t, model)
	fieldID := ir.FieldID("fffffffffffffffffffffffffffff101")
	var modelID ir.ModelID
	for modelIndex := range current.Models {
		declaration := &current.Models[modelIndex]
		if declaration.Go.Name != "Post" {
			continue
		}
		modelID = declaration.ID
		declaration.Fields = append(declaration.Fields, ir.FieldIR{ID: fieldID, CanonicalIdentity: declaration.CanonicalIdentity + ".field.slug", GoName: "Slug", LogicalName: "slug", DeclarationOrder: uint32(len(declaration.Fields)), Kind: ir.FieldScalar, Scalar: &ir.ScalarFieldIR{Column: "slug", Type: ir.LogicalTypeIR{Kind: ir.TypeString}}})
		break
	}
	if modelID == "" {
		t.Fatal("Post model is absent")
	}
	result := append([]Provider(nil), providers...)
	for providerIndex := range result {
		schema := result[providerIndex].Result.Schema
		schema.Tables = append([]physical.PhysicalTable(nil), schema.Tables...)
		for tableIndex := range schema.Tables {
			if schema.Tables[tableIndex].ID != modelID {
				continue
			}
			table := schema.Tables[tableIndex]
			table.Columns = append([]physical.PhysicalColumn(nil), table.Columns...)
			table.Columns = append(table.Columns, physical.PhysicalColumn{ID: fieldID, Name: "slug", Ordinal: uint32(len(table.Columns)), Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
			schema.Tables[tableIndex] = table
		}
		normalized, err := physical.Normalize(schema)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, _ := physical.PhysicalFingerprint(normalized)
		result[providerIndex].Result.Schema = normalized
		result[providerIndex].Result.Fingerprint = ir.Fingerprint(fingerprint.String())
	}
	return current, result, modelID, fieldID
}

func prepare(t *testing.T, module, name string, model ir.ModelIR, previous *ir.ModelIR, contract ir.Fingerprint, providers []Provider, approvals []migration.OperationID) NewResult {
	t.Helper()
	fingerprint, err := ir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	return prepareWithFingerprint(t, module, name, model, previous, fingerprint, contract, providers, approvals)
}

func prepareWithFingerprint(t *testing.T, module, name string, model ir.ModelIR, previous *ir.ModelIR, fingerprint, contract ir.Fingerprint, providers []Provider, approvals []migration.OperationID) NewResult {
	t.Helper()
	result, err := PrepareNew(context.Background(), NewRequest{ModuleDir: module, Root: DefaultRoot, Name: name, Model: model, PreviousModel: previous, ModelFingerprint: fingerprint, ContractFingerprint: contract, Providers: providers, Approvals: approvals})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func publish(t *testing.T, module string, value NewResult) {
	t.Helper()
	if _, err := publication.Apply(context.Background(), publication.Request{ModuleDir: module, ManifestPath: value.PublicationPath, Prospective: value.Prospective}); err != nil {
		t.Fatal(err)
	}
}

func addPortableIndex(t *testing.T, providers []Provider) []Provider {
	t.Helper()
	result := append([]Provider(nil), providers...)
	for index := range result {
		schema := result[index].Result.Schema
		schema.Tables = append([]physical.PhysicalTable(nil), schema.Tables...)
		table := schema.Tables[0]
		table.Indexes = append([]physical.PhysicalIndex(nil), table.Indexes...)
		field := table.Columns[0].ID
		table.Indexes = append(table.Indexes, physical.PhysicalIndex{
			ID: "ffffffffffffffffffffffffffffff01", Name: "idx_workflow_lookup", Method: physical.IndexBTree,
			Keys: []physical.IndexKey{{Column: &field, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: physical.IndexTransactional,
		})
		schema.Tables[0] = table
		normalized, err := physical.Normalize(schema)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, _ := physical.PhysicalFingerprint(normalized)
		result[index].Result.Schema, result[index].Result.Fingerprint = normalized, ir.Fingerprint(fingerprint.String())
	}
	return result
}

func dropField(t *testing.T, providers []Provider, modelID ir.ModelID, fieldID ir.FieldID) []Provider {
	t.Helper()
	result := append([]Provider(nil), providers...)
	for index := range result {
		schema := result[index].Result.Schema
		schema.Tables = append([]physical.PhysicalTable(nil), schema.Tables...)
		for tableIndex := range schema.Tables {
			if schema.Tables[tableIndex].ID != modelID {
				continue
			}
			table := schema.Tables[tableIndex]
			table.Columns = append([]physical.PhysicalColumn(nil), table.Columns...)
			for columnIndex, column := range table.Columns {
				if column.ID == fieldID {
					table.Columns = append(table.Columns[:columnIndex:columnIndex], table.Columns[columnIndex+1:]...)
					break
				}
			}
			for columnIndex := range table.Columns {
				table.Columns[columnIndex].Ordinal = uint32(columnIndex)
			}
			schema.Tables[tableIndex] = table
		}
		normalized, err := physical.Normalize(schema)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, _ := physical.PhysicalFingerprint(normalized)
		result[index].Result.Schema, result[index].Result.Fingerprint = normalized, ir.Fingerprint(fingerprint.String())
	}
	return result
}

func assertHistoryArtifacts(t *testing.T, module, id string, providers int) {
	t.Helper()
	for _, provider := range []string{"postgresql", "sqlite"}[:providers] {
		for _, suffix := range []string{".sql", ".before.snapshot.json", ".after.snapshot.json"} {
			if _, err := os.Stat(filepath.Join(module, DefaultRoot, provider, id+suffix)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, suffix := range []string{".before.snapshot.json", ".after.snapshot.json"} {
		if _, err := os.Stat(filepath.Join(module, DefaultRoot, "models", id+suffix)); err != nil {
			t.Fatal(err)
		}
	}
}

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	if err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, _ := filepath.Rel(root, name)
		content, readErr := os.ReadFile(name)
		if readErr != nil {
			return readErr
		}
		result[filepath.ToSlash(relative)] = string(content)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func publicationArtifacts(t *testing.T, module string, value codegenmanifest.Manifest) []codegenmanifest.Artifact {
	t.Helper()
	artifacts := make([]codegenmanifest.Artifact, len(value.Artifacts))
	for index, entry := range value.Artifacts {
		content, err := os.ReadFile(filepath.Join(module, filepath.FromSlash(entry.Path)))
		if err != nil {
			t.Fatal(err)
		}
		artifacts[index] = codegenmanifest.Artifact{Path: entry.Path, Kind: entry.Kind, Content: content, GeneratedHeader: entry.GeneratedHeader, Immutable: entry.Immutable}
	}
	return artifacts
}

func cloneModel(t *testing.T, value ir.ModelIR) ir.ModelIR {
	t.Helper()
	encoded, err := ir.CanonicalModel(value)
	if err != nil {
		t.Fatal(err)
	}
	var result ir.ModelIR
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPreviewAllowsSaturatedHistoryWhileMigrationNewRefusesAllocation(t *testing.T) {
	if err := validatePreviewHeadLength(9999); err != nil {
		t.Fatalf("read-only preview inherited authoring allocation policy: %v", err)
	}
	if err := validateNewHeadLength(9999); err == nil || !strings.Contains(err.Error(), "cannot allocate") {
		t.Fatalf("migration new accepted saturated four-digit history: %v", err)
	}
}
