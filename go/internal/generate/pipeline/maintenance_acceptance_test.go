package pipeline

import (
	"strings"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestFreshGeneratedBoundedMaintenance(t *testing.T) {
	runGeneratedDTOConsumerWithProviders(t, "boundedmaintenance", nil, boundedMaintenanceConsumerTest, compilerir.SQLite, compilerir.PostgreSQL)
}

// The base fixture stays provider-neutral and unindexed. This separately
// generated variant uses the same domain commands with a real semantic index.
// Both providers execute the same rollback/retry and real-vector convergence
// assertions; PostgreSQL requires the existing explicit pgvector profile.
func TestFreshGeneratedBoundedMaintenanceSemantic(t *testing.T) {
	rewrite := func(source string) string {
		provider := "golem.Providers(schema, golem.SQLite, golem.PostgreSQL)"
		index := "golem.Subscriptions[Episode](golem.DeleteSnapshot(Episodes.ShowID, Episodes.Title)),"
		if strings.Count(source, provider) != 1 || strings.Count(source, index) != 1 {
			t.Fatal("maintenance semantic rewrite no longer matches its shared declaration owners")
		}
		source = strings.Replace(source, provider, provider+"\n\tgolem.EmbeddingSpace(schema, \"maintenance\", 3)", 1)
		return strings.Replace(source, index, index+"\n\t\tgolem.SemanticIndex(\"titles\", \"maintenance\", Episodes.Title),", 1)
	}
	runGeneratedDTOConsumerWithProviders(t, "boundedmaintenance", rewrite, boundedMaintenanceSemanticConsumerTest, compilerir.SQLite, compilerir.PostgreSQL)
}

const boundedMaintenanceConsumerTest = `package acceptance_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	app "example.test/home"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	providerpostgres "github.com/eleven-am/golem/go/provider/postgresql"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

func maintenanceID(kind byte, number uint32) golem.UUID {
	var id golem.UUID
	id[0] = kind
	binary.BigEndian.PutUint32(id[12:], number)
	return id
}

func TestUnboundedRemovalRefusesAndRollsBack(t *testing.T) {
	ctx := context.Background()
	var database *provider.Database
	var err error
	switch os.Getenv("GOLEM_OBJECT_PROVIDER") {
	case "sqlite":
		database, err = providersqlite.Open(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
	case "postgresql":
		database, err = providerpostgres.Open(ctx, providerpostgres.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB"), Pool: providerpostgres.PoolConfig{MaximumOpen: 4, MaximumIdle: 4}})
	default:
		t.Fatal("explicit reviewed provider required")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	registry := queue.NewRegistry()
	maintenance, err := app.RegisterMaintenance(registry)
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.Open(ctx, app.Config[app.Principal]{
		Database: database, EventTransport: transport,
		Queue:               &golemruntime.QueueConfig{Registry: registry, Limits: queue.Limits{Concurrency: 1, ClaimBatch: 1, PollInterval: 10 * time.Millisecond, RetentionEvery: queue.RetentionDisabled}},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		ResolvePrincipal: func(_ context.Context, principal app.Principal) (app.Actor, error) {
			return app.Actor{OwnerID: principal.OwnerID}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	maintenance.Application = application
	system := application.System()
	showID, ownerID := maintenanceID(1, 1), maintenanceID(2, 1)
	if _, err := system.Shows.Create(ctx, app.Shows.Create(app.Shows.ID.Create(showID), app.Shows.OwnerID.Create(ownerID), app.Shows.Removed.Create(false), app.Shows.Fence.Create(0))); err != nil {
		t.Fatal(err)
	}
	const episodeCount = 511
	// Real public mutations seed 511 episodes and two private assets each.
	// Every seed transaction itself remains comfortably under the hard limits.
	for start := 1; start <= episodeCount; start += 32 {
		end := min(start+32, episodeCount+1)
		if err := system.Transaction(ctx, func(tx *app.SystemTx[app.Principal]) error {
			for index := start; index < end; index++ {
				episodeID := maintenanceID(3, uint32(index))
				if _, err := tx.Episodes.Create(ctx, app.Episodes.Create(app.Episodes.ID.Create(episodeID), app.Episodes.ShowID.Create(showID), app.Episodes.Title.Create("episode"))); err != nil {
					return err
				}
				for child := 0; child < 2; child++ {
					if _, err := tx.Assets.Create(ctx, app.Assets.Create(app.Assets.ID.Create(maintenanceID(4, uint32(index*2+child))), app.Assets.EpisodeID.Create(episodeID), app.Assets.Body.Create("private physical leaf"))); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertCounts := func() {
		t.Helper()
		shows, e1 := system.Shows.Count(ctx)
		episodes, e2 := system.Episodes.Count(ctx)
		assets, e3 := system.Assets.Count(ctx)
		tasks, e4 := system.RemovalTasks.Count(ctx)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || shows != 1 || episodes != episodeCount || assets != 2*episodeCount || tasks != 0 {
			t.Fatalf("counts shows=%d episodes=%d assets=%d tasks=%d errors=%v/%v/%v/%v", shows, episodes, assets, tasks, e1, e2, e3, e4)
		}
	}
	assertCounts()
	// Read the actual reviewed provider snapshot from the generated bundle;
	// PostgreSQL system tables need not share the application's search path.
	manifestBytes, err := app.GolemGeneratedSchemaBundle().MigrationManifest(database.Provider())
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Entries []struct {
			AfterSnapshot struct {
				Namespace struct{ Name string }
				Tables    []struct {
					Name    string
					Columns []struct{ Name string }
				}
				System struct {
					Namespace struct{ Name string }
					Objects   []struct{ Kind, Name string }
				}
			}
		}
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) == 0 {
		t.Fatal("reviewed provider history is absent")
	}
	physical := manifest.Entries[len(manifest.Entries)-1].AfterSnapshot
	physicalSystem := physical.System
	quote := func(value string) string { return "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\"" }
	if physicalSystem.Namespace.Name == "" {
		t.Fatal("reviewed system namespace is absent")
	}
	tables := map[string]string{}
	for _, object := range physicalSystem.Objects {
		if object.Kind == "outbox" || object.Kind == "outbox_delivery" {
			if object.Name == "" || tables[object.Kind] != "" {
				t.Fatal("invalid reviewed system object inventory")
			}
			tables[object.Kind] = quote(physicalSystem.Namespace.Name) + "." + quote(object.Name)
		}
	}
	if len(tables) != 2 {
		t.Fatal("reviewed outbox/delivery objects are absent")
	}
	// Trusted, read-only infrastructure assertions. All application writes
	// above and below use generated clients and their normal mutation owners.
	durableCounts := func() [2]int64 {
		t.Helper()
		var counts [2]int64
		if err := database.UnsafeSQLX().GetContext(ctx, &counts[0], "SELECT COUNT(*) FROM "+tables["outbox"]); err != nil {
			t.Fatal(err)
		}
		if err := database.UnsafeSQLX().GetContext(ctx, &counts[1], "SELECT COUNT(*) FROM "+tables["outbox_delivery"]); err != nil {
			t.Fatal(err)
		}
		return counts
	}
	// These are trusted physical assertions, not an application read API. Resolve
	// every table and version column against the actual reviewed provider shape.
	applicationTables := map[string]string{}
	for _, table := range physical.Tables {
		if !strings.HasPrefix(table.Name, "maintenance_") {
			continue
		}
		hasVersion := false
		for _, column := range table.Columns {
			if column.Name == "golem_version" {
				hasVersion = true
			}
		}
		if !hasVersion {
			t.Fatalf("reviewed table %s has no runtime version", table.Name)
		}
		applicationTables[table.Name] = quote(physical.Namespace.Name) + "." + quote(table.Name)
	}
	if len(applicationTables) != 4 {
		t.Fatalf("reviewed application tables=%v", applicationTables)
	}
	versions := func() map[string]int64 {
		t.Helper()
		result := map[string]int64{}
		for name, table := range applicationTables {
			var rows []struct {
				ID      string
				Version int64
			}
			if err := database.UnsafeSQLX().SelectContext(ctx, &rows, "SELECT CAST(id AS TEXT) AS id, golem_version AS version FROM "+table); err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.Version <= 0 {
					t.Fatalf("unstamped %s/%s", name, row.ID)
				}
				result[name+"/"+row.ID] = row.Version
			}
		}
		return result
	}
	assertVersions := func(before map[string]int64) {
		t.Helper()
		if !maps.Equal(before, versions()) {
			t.Fatal("rolled-back work changed surviving rows or their runtime versions")
		}
		// In particular, never assert a PostgreSQL sequence value or gaplessness:
		// failed statements/transactions may legitimately consume version stamps.
	}
	type storedFact struct {
		EventID   string ` + "`" + `db:"event_id"` + "`" + `
		Causation string ` + "`" + `db:"causation_id"` + "`" + `
		Ordinal   int64  ` + "`" + `db:"transaction_ordinal"` + "`" + `
		Action    string ` + "`" + `db:"action"` + "`" + `
		Metadata  []byte ` + "`" + `db:"metadata"` + "`" + `
		Before    []byte ` + "`" + `db:"before_identity"` + "`" + `
		After     []byte ` + "`" + `db:"after_identity"` + "`" + `
		Snapshot  []byte ` + "`" + `db:"delete_snapshot"` + "`" + `
	}
	facts := func() map[string][]storedFact {
		t.Helper()
		var rows []storedFact
		if err := database.UnsafeSQLX().SelectContext(ctx, &rows, "SELECT event_id,causation_id,transaction_ordinal,action,metadata,before_identity,after_identity,delete_snapshot FROM "+tables["outbox"]+" ORDER BY causation_id,transaction_ordinal"); err != nil {
			t.Fatal(err)
		}
		groups := map[string][]storedFact{}
		seen := map[string]bool{}
		for _, row := range rows {
			if row.EventID == "" || seen[row.EventID] || row.Ordinal != int64(len(groups[row.Causation])+1) {
				t.Fatalf("duplicate event or incomplete ordinal group: %+v", row)
			}
			seen[row.EventID] = true
			groups[row.Causation] = append(groups[row.Causation], row)
		}
		for cause, group := range groups {
			if len(group) > 1000 {
				t.Fatalf("causation %s exceeds hard fact bound: %d", cause, len(group))
			}
			rawBytes := 0
			for _, row := range group {
				rawBytes += len(row.Metadata) + len(row.Before) + len(row.After) + len(row.Snapshot)
			}
			// This measures RAW STORED BLOB BYTES, not the canonical encoder's
			// accounting or a proof that arbitrary authored content fits its cap.
			if rawBytes > 1<<20 {
				t.Fatalf("fixture raw stored payload exceeds 1MiB: %d", rawBytes)
			}
		}
		var deliveries []string
		if err := database.UnsafeSQLX().SelectContext(ctx, &deliveries, "SELECT causation_id FROM "+tables["outbox_delivery"]); err != nil {
			t.Fatal(err)
		}
		if len(deliveries) != len(groups) {
			t.Fatalf("facts/delivery cardinality differs: %d/%d", len(groups), len(deliveries))
		}
		for _, cause := range deliveries {
			if len(groups[cause]) == 0 {
				t.Fatal("delivery has no complete fact group")
			}
		}
		return groups
	}
	assertPageFacts := func(before map[string][]storedFact, wantGroups int) {
		t.Helper()
		after := facts()
		if len(after)-len(before) != wantGroups {
			t.Fatalf("page causations=%d want=%d", len(after)-len(before), wantGroups)
		}
		finals := 0
		for cause, group := range after {
			if old, ok := before[cause]; ok {
				if len(old) != len(group) {
					t.Fatal("appended to a committed causation")
				}
				continue
			}
			deletes, updates := 0, 0
			for _, row := range group {
				switch row.Action {
				case "deleted":
					deletes++
				case "updated":
					updates++
				default:
					t.Fatalf("page emitted %s", row.Action)
				}
			}
			if updates != 2 || deletes > app.CleanupLeafPageSize || len(group) != deletes+2 {
				t.Fatalf("unbounded/incomplete physical page: deletes=%d updates=%d facts=%d", deletes, updates, len(group))
			}
			if deletes == 1 {
				finals++
				if len(group) != 3 {
					t.Fatal("final root removal must have exactly three facts")
				}
			}
		}
		if wantGroups > 1 && finals != 1 {
			t.Fatalf("final root causations=%d", finals)
		}
	}
	baselineVersions := versions()
	before := durableCounts()
	if before[0] != 1+3*episodeCount {
		t.Fatalf("seed facts=%d; every private leaf must retain its fact", before[0])
	}
	err = system.Transaction(ctx, func(tx *app.SystemTx[app.Principal]) error {
		if _, err := tx.Shows.Update(ctx, app.Shows.ByID.Value(showID), app.Shows.Update(app.Shows.Removed.Set(true), app.Shows.Fence.Set(1))); err != nil {
			return err
		}
		if _, err := tx.RemovalTasks.Create(ctx, app.RemovalTasks.Create(
			app.RemovalTasks.ID.Create(maintenanceID(5, 1)), app.RemovalTasks.ShowID.Create(showID), app.RemovalTasks.OwnerID.Create(ownerID),
			app.RemovalTasks.Generation.Create(maintenanceID(6, 1)), app.RemovalTasks.Sequence.Create(0), app.RemovalTasks.Phase.Create(0),
			app.RemovalTasks.Deleted.Create(0), app.RemovalTasks.Completed.Create(false))); err != nil {
			return err
		}
		_, err := tx.Shows.Delete(ctx, app.Shows.ByID.Value(showID))
		return err
	})
	var limit *golem.Error
	if !errors.As(err, &limit) || limit.Code != golem.CodeLimitExceeded || limit.Limit != golem.LimitTouchedRows || limit.LimitMax != 1000 {
		t.Fatalf("expected unchanged 1000 touched-row cap, got %v", err)
	}
	assertCounts()
	assertVersions(baselineVersions)
	facts()
	if after := durableCounts(); after != before {
		t.Fatalf("failed cascade detached durable facts/delivery: before=%v after=%v", before, after)
	}
	caller, err := application.ForPrincipal(ctx, app.Principal{OwnerID: ownerID})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := caller.Shows.Count(ctx); err != nil || count != 1 {
		t.Fatalf("rolled-back logical removal still hid root: %d %v", count, err)
	}
	if count, err := caller.Episodes.Count(ctx); err != nil || count != episodeCount {
		t.Fatalf("rolled-back logical removal hid episodes: %d %v", count, err)
	}
	if _, err := caller.Assets.FindMany(ctx); !errors.Is(err, golem.CodeForbidden) {
		t.Fatalf("private physical leaves became readable: %v", err)
	}
	rows, err := system.Shows.FindMany(ctx, golem.Select[app.Show](app.Shows.Fence, app.Shows.Removed))
	if err != nil || len(rows) != 1 {
		t.Fatalf("root state unavailable: %v", err)
	}
	fence, fOK := golem.Value(rows[0], app.Shows.Fence).Get()
	removed, rOK := golem.Value(rows[0], app.Shows.Removed).Get()
	if !fOK || !rOK || fence != 0 || removed {
		t.Fatalf("earlier domain writes escaped rollback: fence=%d removed=%v", fence, removed)
	}
	// The source fixture has no semantic index. This first checkpoint proves
	// mutation facts/queue/progress atomicity, not semantic drain behavior.
	t.Run("bounded worker and replay", func(t *testing.T) {
		beforeIngestVersions := versions()
		if err := app.IngestSystem(ctx, application, app.IngestEpisode{ID: maintenanceID(3, 1), ShowID: showID, Title: "system refresh"}); err != nil {
			t.Fatal(err)
		}
		afterIngestVersions := versions()
		for _, key := range []string{"maintenance_shows/" + showID.String(), "maintenance_episodes/" + maintenanceID(3, 1).String()} {
			if afterIngestVersions[key] <= beforeIngestVersions[key] {
				t.Fatalf("successful ingest did not advance row version %s", key)
			}
		}
		if err := app.IngestCaller(ctx, caller, app.IngestEpisode{ID: maintenanceID(3, 1), ShowID: showID, Title: "caller refresh"}); err != nil {
			t.Fatal(err)
		}

		otherRoot := maintenanceID(1, 2)
		createOther := func() {
			t.Helper()
			if _, err := system.Shows.Create(ctx, app.Shows.Create(app.Shows.ID.Create(otherRoot), app.Shows.OwnerID.Create(ownerID), app.Shows.Removed.Create(false), app.Shows.Fence.Create(0))); err != nil {
				t.Fatal(err)
			}
		}
		deleteOther := func() {
			t.Helper()
			if _, err := system.Shows.Delete(ctx, app.Shows.ByID.Value(otherRoot)); err != nil {
				t.Fatal(err)
			}
		}
		t.Run("caller and system reparent through both root fences", func(t *testing.T) {
			createOther()
			if err := app.IngestSystem(ctx, application, app.IngestEpisode{ID: maintenanceID(3, 1), ShowID: otherRoot, Title: "moved by system"}); err != nil {
				t.Fatal(err)
			}
			if err := app.IngestCaller(ctx, caller, app.IngestEpisode{ID: maintenanceID(3, 1), ShowID: showID, Title: "moved back by caller"}); err != nil {
				t.Fatal(err)
			}
			deleteOther()
			assertCounts()
		})
		beforeOversize, oversizeVersions := durableCounts(), versions()
		if err := app.IngestSystem(ctx, application, app.IngestEpisode{ID: maintenanceID(3, 1), ShowID: showID, Title: strings.Repeat("x", 1025)}); err == nil {
			t.Fatal("unbounded title accepted")
		}
		if durableCounts() != beforeOversize {
			t.Fatal("oversized ingest committed its earlier root fence")
		}
		assertVersions(oversizeVersions)

		queueCounts := func() queue.StateCounts {
			t.Helper()
			counts, err := application.QueueOperator().CountByState(ctx, queue.CountQuery{Types: []string{app.CleanupJobType}})
			if err != nil {
				t.Fatal(err)
			}
			return counts
		}
		// The queue table name is the existing queue provider's fixed name;
		// its namespace comes from the same reviewed physical snapshot above.
		queueTable := quote(physicalSystem.Namespace.Name) + "." + quote("golem_queue")
		injectEnqueueFailure := func(t *testing.T) func() {
			t.Helper()
			trigger := quote("maintenance_fail_enqueue")
			function := quote(physicalSystem.Namespace.Name) + "." + quote("maintenance_fail_enqueue_fn")
			var install, uninstall []string
			if database.Provider() == golem.SQLite {
				install = []string{"CREATE TRIGGER " + trigger + " BEFORE INSERT ON " + queueTable + " BEGIN SELECT RAISE(ABORT, 'injected maintenance enqueue failure'); END"}
				uninstall = []string{"DROP TRIGGER IF EXISTS " + trigger}
			} else {
				install = []string{"CREATE FUNCTION " + function + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected maintenance enqueue failure'; END $$", "CREATE TRIGGER " + trigger + " BEFORE INSERT ON " + queueTable + " FOR EACH ROW EXECUTE FUNCTION " + function + "()"}
				uninstall = []string{"DROP TRIGGER IF EXISTS " + trigger + " ON " + queueTable, "DROP FUNCTION IF EXISTS " + function + "()"}
			}
			for _, statement := range install {
				if _, err := database.UnsafeSQLX().ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			dropped := false
			drop := func() {
				if dropped {
					return
				}
				for _, statement := range uninstall {
					if _, err := database.UnsafeSQLX().ExecContext(ctx, statement); err != nil {
						t.Errorf("remove injected fault: %v", err)
					}
				}
				dropped = true
			}
			t.Cleanup(drop)
			return drop
		}
		taskID, generation := maintenanceID(5, 2), maintenanceID(6, 2)
		t.Run("logical removal enqueue failure rolls everything back", func(t *testing.T) {
			before, queued, beforeVersions := durableCounts(), queueCounts(), versions()
			drop := injectEnqueueFailure(t)
			if err := maintenance.RequestRemoval(ctx, caller, showID, taskID, generation); err == nil {
				t.Fatal("queue insertion fault did not abort removal")
			}
			drop()
			assertVersions(beforeVersions)
			assertCounts()
			if durableCounts() != before || queueCounts() != queued {
				t.Fatal("failed removal detached event or queue work")
			}
			if count, err := caller.Shows.Count(ctx); err != nil || count != 1 {
				t.Fatalf("failed removal changed visibility: %d %v", count, err)
			}
		})
		if err := maintenance.RequestRemoval(ctx, caller, showID, taskID, generation); err != nil {
			t.Fatal(err)
		}
		if count, err := caller.Shows.Count(ctx); err != nil || count != 0 {
			t.Fatalf("logical removal is not atomic: %d %v", count, err)
		}
		if count, err := caller.Episodes.Count(ctx); err != nil || count != 0 {
			t.Fatalf("logical removal exposed partial descendants: %d %v", count, err)
		}
		if queued := queueCounts(); queued.Pending != 1 || queued.Leased != 0 {
			t.Fatalf("first page not durable: %+v", queued)
		}
		idempotentCounts := durableCounts()
		if err := maintenance.RequestRemoval(ctx, caller, showID, maintenanceID(5, 99), maintenanceID(6, 99)); err != nil {
			t.Fatal(err)
		}
		if durableCounts() != idempotentCounts || queueCounts().Pending != 1 {
			t.Fatal("idempotent removal created another generation")
		}
		for name, ingest := range map[string]func(context.Context, app.IngestEpisode) error{
			"caller": func(ctx context.Context, input app.IngestEpisode) error { return app.IngestCaller(ctx, caller, input) },
			"system": func(ctx context.Context, input app.IngestEpisode) error {
				return app.IngestSystem(ctx, application, input)
			},
		} {
			t.Run(name+" ingest cannot resurrect removed root", func(t *testing.T) {
				before := durableCounts()
				if err := ingest(ctx, app.IngestEpisode{ID: maintenanceID(3, 900), ShowID: showID, Title: "late"}); err == nil {
					t.Fatal("ingest entered removed root")
				}
				if durableCounts() != before {
					t.Fatal("refused ingest leaked an earlier fence")
				}
			})
		}

		t.Run("reparent cannot rescue descendants from a removed root", func(t *testing.T) {
			createOther()
			before := durableCounts()
			input := app.IngestEpisode{ID: maintenanceID(3, 1), ShowID: otherRoot, Title: "late reparent"}
			if err := app.IngestSystem(ctx, application, input); err == nil {
				t.Fatal("system moved a removed descendant")
			}
			if err := app.IngestCaller(ctx, caller, input); err == nil {
				t.Fatal("caller moved a removed descendant")
			}
			if durableCounts() != before {
				t.Fatal("refused reparent committed a fence or descendant")
			}
			if count, err := system.Episodes.Count(ctx, golem.Where(app.Episodes.ShowID.Eq(otherRoot))); err != nil || count != 0 {
				t.Fatalf("removed descendant escaped: %d %v", count, err)
			}
			deleteOther()
		})
		progress := func() (int32, int32, bool) {
			t.Helper()
			rows, err := system.RemovalTasks.FindMany(ctx, golem.Where(app.RemovalTasks.ID.Eq(taskID)))
			if err != nil || len(rows) != 1 {
				t.Fatalf("durable progress unavailable: %v", err)
			}
			seq, s := golem.Value(rows[0], app.RemovalTasks.Sequence).Get()
			deleted, d := golem.Value(rows[0], app.RemovalTasks.Deleted).Get()
			complete, c := golem.Value(rows[0], app.RemovalTasks.Completed).Get()
			if !s || !d || !c {
				t.Fatal("progress fields unavailable")
			}
			return seq, deleted, complete
		}
		page := app.CleanupPage{TaskID: taskID, Generation: generation}
		t.Run("leaf progress and successor rollback together", func(t *testing.T) {
			before, queued, beforeVersions := durableCounts(), queueCounts(), versions()
			drop := injectEnqueueFailure(t)
			if err := maintenance.ProcessPage(ctx, page); err == nil {
				t.Fatal("successor insertion fault did not abort page")
			}
			drop()
			assertVersions(beforeVersions)
			if count, err := system.Assets.Count(ctx); err != nil || count != 2*episodeCount {
				t.Fatalf("failed page lost leaves: %d %v", count, err)
			}
			if seq, deleted, completed := progress(); seq != 0 || deleted != 0 || completed {
				t.Fatal("failed page committed progress")
			}
			if durableCounts() != before || queueCounts() != queued {
				t.Fatal("failed page detached facts or successor")
			}
		})
		beforeFirstPage := facts()
		if err := maintenance.ProcessPage(ctx, page); err != nil {
			t.Fatal(err)
		}
		assertPageFacts(beforeFirstPage, 1)
		if seq, deleted, completed := progress(); seq != 1 || deleted != app.CleanupLeafPageSize || completed {
			t.Fatalf("first committed page: seq=%d deleted=%d completed=%v", seq, deleted, completed)
		}
		t.Run("committed handler before acknowledgement replays harmlessly", func(t *testing.T) {
			before, queued := durableCounts(), queueCounts()
			if err := maintenance.ProcessPage(ctx, page); err != nil {
				t.Fatal(err)
			}
			stale := page
			stale.Generation = maintenanceID(6, 99)
			if err := maintenance.ProcessPage(ctx, stale); err != nil {
				t.Fatal(err)
			}
			if durableCounts() != before || queueCounts() != queued {
				t.Fatal("replay or stale generation repeated committed work")
			}
		})
		beforeWorkerPages := facts()
		workerCtx, stopWorker := context.WithCancel(ctx)
		workerDone := make(chan error, 1)
		go func() { workerDone <- application.RunQueueWorker(workerCtx) }()
		t.Cleanup(func() {
			stopWorker()
			select {
			case err := <-workerDone:
				if err != nil {
					t.Errorf("worker: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("worker did not stop")
			}
		})
		deadline := time.Now().Add(45 * time.Second)
		for {
			seq, deleted, completed := progress()
			counts := queueCounts()
			if counts.Failed != 0 {
				t.Fatalf("cleanup worker failed: %+v", counts)
			}
			if completed && counts.Pending == 0 && counts.Leased == 0 {
				if deleted != 3*episodeCount || counts.Succeeded != int64(seq) {
					t.Fatalf("incomplete or duplicated pages: seq=%d deleted=%d jobs=%+v", seq, deleted, counts)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("cleanup did not converge: seq=%d deleted=%d completed=%v jobs=%+v", seq, deleted, completed, counts)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if count, err := system.Shows.Count(ctx); err != nil || count != 0 {
			t.Fatalf("final root survives: %d %v", count, err)
		}
		if count, err := system.Episodes.Count(ctx); err != nil || count != 0 {
			t.Fatalf("episode orphans: %d %v", count, err)
		}
		if count, err := system.Assets.Count(ctx); err != nil || count != 0 {
			t.Fatalf("private leaf orphans: %d %v", count, err)
		}
		assertPageFacts(beforeWorkerPages, 50)
		t.Log("511-episode cleanup converged through real durable queue pages; semantic state not configured in this fixture")
	})

	t.Run("real transaction commit ordering", func(t *testing.T) {
		rollbackWinner := errors.New("roll back held command")
		// The held callback is the REAL outer transaction. Business commands
		// have already acquired their root CAS/write lock before ready closes.
		// No observer (which is post-transaction) or product hook is a barrier.
		race := func(t *testing.T, winner func(context.Context, func() error) error, contender func(context.Context) error, rollback bool) (error, error) {
			t.Helper()
			raceCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			ready, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			winnerDone, contenderDone := make(chan error, 1), make(chan error, 1)
			go func() {
				winnerDone <- winner(raceCtx, func() error {
					close(ready)
					select {
					case <-release:
						if rollback {
							return rollbackWinner
						}
						return nil
					case <-raceCtx.Done():
						return raceCtx.Err()
					}
				})
			}()
			select {
			case <-ready:
			case err := <-winnerDone:
				t.Fatalf("winner did not acquire its real fence: %v", err)
			case <-raceCtx.Done():
				t.Fatal(raceCtx.Err())
			}
			go func() { contenderDone <- contender(raceCtx) }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				select {
				case err := <-contenderDone:
					t.Fatalf("contender completed while winner still held root write: %v", err)
				default:
				}
				if database.Provider() == golem.PostgreSQL {
					// This private fixture has no background workers here. Prove
					// a real backend is waiting on the application's advisory lock,
					// not merely that a contender goroutine was scheduled.
					var waiting int
					query := "SELECT COUNT(*) FROM pg_catalog.pg_stat_activity a WHERE a.datname=current_database() AND a.pid<>pg_backend_pid() AND a.wait_event_type='Lock' AND a.query LIKE '%pg_advisory_xact_lock%' AND cardinality(pg_catalog.pg_blocking_pids(a.pid))>0"
					if err := database.UnsafeSQLX().GetContext(raceCtx, &waiting, query); err != nil {
						t.Fatal(err)
					}
					if waiting != 0 {
						break
					}
				} else if database.UnsafeSQLX().Stats().InUse >= 2 {
					// Verified SQLite opens every transaction BEGIN IMMEDIATE.
					// Winner holds its write transaction; a second leased provider
					// connection cannot enter its callback before that writer ends.
					// This asserts that profile boundary, not a fictitious lock view.
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("contender did not reach provider lock boundary")
				}
				time.Sleep(time.Millisecond)
			}
			unblock()
			var first, second error
			select {
			case first = <-winnerDone:
			case <-raceCtx.Done():
				t.Fatal(raceCtx.Err())
			}
			select {
			case second = <-contenderDone:
			case <-raceCtx.Done():
				t.Fatal(raceCtx.Err())
			}
			return first, second
		}
		seedRoot := func(id golem.UUID) {
			t.Helper()
			if _, err := system.Shows.Create(ctx, app.Shows.Create(app.Shows.ID.Create(id), app.Shows.OwnerID.Create(ownerID), app.Shows.Removed.Create(false), app.Shows.Fence.Create(0))); err != nil {
				t.Fatal(err)
			}
		}
		assertEpisodeRoot := func(id, root golem.UUID, present bool) {
			t.Helper()
			rows, err := system.Episodes.FindMany(ctx, golem.Where(app.Episodes.ID.Eq(id)), golem.Select[app.Episode](app.Episodes.ShowID))
			if err != nil {
				t.Fatal(err)
			}
			if !present {
				if len(rows) != 0 {
					t.Fatal("refused ingest committed a descendant")
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("episode rows=%d", len(rows))
			}
			got, ok := golem.Value(rows[0], app.Episodes.ShowID).Get()
			if !ok || got != root {
				t.Fatalf("episode moved outside its fenced root: %v", got)
			}
		}
		assertRemoved := func(root golem.UUID, want bool) {
			t.Helper()
			rows, err := system.Shows.FindMany(ctx, golem.Where(app.Shows.ID.Eq(root)), golem.Select[app.Show](app.Shows.Removed))
			if err != nil || len(rows) != 1 {
				t.Fatalf("root absent: %v", err)
			}
			got, ok := golem.Value(rows[0], app.Shows.Removed).Get()
			if !ok || got != want {
				t.Fatalf("removed=%v want=%v", got, want)
			}
		}
		queued := func() queue.StateCounts {
			t.Helper()
			counts, err := application.QueueOperator().CountByState(ctx, queue.CountQuery{Types: []string{app.CleanupJobType}})
			if err != nil {
				t.Fatal(err)
			}
			return counts
		}
		caseNumber := uint32(100)
		for _, mode := range []string{"caller", "system"} {
			for _, scenario := range []string{"ingest first", "remove first", "remove rolls back", "reparent first", "remove before reparent"} {
				t.Run(mode+"/"+scenario, func(t *testing.T) {
					caseNumber++
					root, destination, episode := maintenanceID(10, caseNumber), maintenanceID(11, caseNumber), maintenanceID(12, caseNumber)
					task, generation := maintenanceID(13, caseNumber), maintenanceID(14, caseNumber)
					seedRoot(root)
					input := app.IngestEpisode{ID: episode, ShowID: root, Title: "concurrent ingest"}
					reparent := scenario == "reparent first" || scenario == "remove before reparent"
					if reparent {
						seedRoot(destination)
						if err := app.IngestSystem(ctx, application, input); err != nil {
							t.Fatal(err)
						}
						if _, err := system.Assets.Create(ctx, app.Assets.Create(app.Assets.ID.Create(maintenanceID(15, caseNumber)), app.Assets.EpisodeID.Create(episode), app.Assets.Body.Create("private reparent witness"))); err != nil {
							t.Fatal(err)
						}
						input.ShowID = destination
					}
					ingest := func(ctx context.Context) error {
						if mode == "caller" {
							return app.IngestCaller(ctx, caller, input)
						}
						return app.IngestSystem(ctx, application, input)
					}
					ingestHeld := func(ctx context.Context, hold func() error) error {
						if mode == "caller" {
							return caller.Transaction(ctx, func(tx *app.CallerTx[app.Principal]) error {
								if err := app.IngestCallerIntoTx(ctx, tx, input); err != nil {
									return err
								}
								return hold()
							})
						}
						return system.Transaction(ctx, func(tx *app.SystemTx[app.Principal]) error {
							if err := app.IngestSystemIntoTx(ctx, tx, input); err != nil {
								return err
							}
							return hold()
						})
					}
					remove := func(ctx context.Context) error {
						return maintenance.RequestRemoval(ctx, caller, root, task, generation)
					}
					removeHeld := func(ctx context.Context, hold func() error) error {
						return caller.Transaction(ctx, func(tx *app.CallerTx[app.Principal]) error {
							if err := maintenance.RequestRemovalIntoTx(ctx, tx, root, task, generation); err != nil {
								return err
							}
							return hold()
						})
					}
					before, jobsBefore := facts(), queued()
					if scenario == "ingest first" || scenario == "reparent first" {
						first, second := race(t, ingestHeld, remove, false)
						if first != nil || second != nil {
							t.Fatalf("ordered ingest/removal failed: %v / %v", first, second)
						}
						assertEpisodeRoot(episode, input.ShowID, true)
						assertRemoved(root, true)
						// Drain through the SAME handler owner: moved-out descendants
						// survive under the destination; newly-ingested ones are removed.
						for sequence := int32(0); sequence < 6; sequence++ {
							if err := maintenance.ProcessPage(ctx, app.CleanupPage{TaskID: task, Generation: generation, Sequence: sequence}); err != nil {
								t.Fatal(err)
							}
						}
						assertEpisodeRoot(episode, input.ShowID, reparent)
						if reparent {
							count, err := system.Assets.Count(ctx, golem.Where(app.Assets.EpisodeID.Eq(episode)))
							if err != nil || count != 1 {
								t.Fatalf("old-root cleanup lost moved private leaf: %d %v", count, err)
							}
						}
					} else {
						rollback := scenario == "remove rolls back"
						first, second := race(t, removeHeld, ingest, rollback)
						if rollback {
							if !errors.Is(first, rollbackWinner) || second != nil {
								t.Fatalf("rollback did not release ingest: %v / %v", first, second)
							}
							assertRemoved(root, false)
							if queued() != jobsBefore {
								t.Fatal("rolled-back removal leaked durable queue work")
							}
							assertEpisodeRoot(episode, root, true)
							if count, err := system.RemovalTasks.Count(ctx, golem.Where(app.RemovalTasks.ID.Eq(task))); err != nil || count != 0 {
								t.Fatalf("rolled-back removal left task: %d %v", count, err)
							}
							// Only the successful ingest's root+episode facts survive.
							after := facts()
							if len(after) != len(before)+1 {
								t.Fatal("rollback leaked a causation")
							}
							for cause, group := range after {
								if _, old := before[cause]; !old && len(group) != 2 {
									t.Fatal("rollback leaked removal facts")
								}
							}
						} else {
							if first != nil || second == nil {
								t.Fatalf("removed root admitted ingest: %v / %v", first, second)
							}
							assertRemoved(root, true)
							assertEpisodeRoot(episode, root, reparent)
							if reparent {
								assertRemoved(destination, false)
							}
						}
					}
					facts()
				})
			}
		}
		t.Run("duplicate page commits once", func(t *testing.T) {
			root, episode, task, generation := maintenanceID(20, 1), maintenanceID(21, 1), maintenanceID(22, 1), maintenanceID(23, 1)
			seedRoot(root)
			if err := app.IngestSystem(ctx, application, app.IngestEpisode{ID: episode, ShowID: root, Title: "duplicate page"}); err != nil {
				t.Fatal(err)
			}
			for index := uint32(1); index <= 2; index++ {
				if _, err := system.Assets.Create(ctx, app.Assets.Create(app.Assets.ID.Create(maintenanceID(24, index)), app.Assets.EpisodeID.Create(episode), app.Assets.Body.Create("leaf"))); err != nil {
					t.Fatal(err)
				}
			}
			if err := maintenance.RequestRemoval(ctx, caller, root, task, generation); err != nil {
				t.Fatal(err)
			}
			page := app.CleanupPage{TaskID: task, Generation: generation}
			before := facts()
			first, second := race(t, func(ctx context.Context, hold func() error) error {
				return system.Transaction(ctx, func(tx *app.SystemTx[app.Principal]) error {
					if err := maintenance.ProcessPageIntoTx(ctx, tx, page); err != nil {
						return err
					}
					return hold()
				})
			}, func(ctx context.Context) error { return maintenance.ProcessPage(ctx, page) }, false)
			if first != nil {
				t.Fatal(first)
			}
			// PG can read old progress before waiting on the root fence; its
			// final task CAS then refuses that attempt. The queue retries it.
			if second != nil && !errors.Is(second, app.ErrFenceChanged) && !errors.Is(second, golem.CodeConflict) {
				t.Fatalf("unexpected duplicate refusal: %v", second)
			}
			assertPageFacts(before, 1)
			stableVersions, stableCounts := versions(), durableCounts()
			if err := maintenance.ProcessPage(ctx, page); err != nil {
				t.Fatal(err)
			}
			assertVersions(stableVersions)
			if durableCounts() != stableCounts {
				t.Fatal("committed replay added facts")
			}
			rows, err := system.RemovalTasks.FindMany(ctx, golem.Where(app.RemovalTasks.ID.Eq(task)))
			if err != nil || len(rows) != 1 {
				t.Fatalf("task unavailable: %v", err)
			}
			seq, ok := golem.Value(rows[0], app.RemovalTasks.Sequence).Get()
			deleted, ok2 := golem.Value(rows[0], app.RemovalTasks.Deleted).Get()
			if !ok || !ok2 || seq != 1 || deleted != 2 {
				t.Fatalf("duplicate page changed progress twice: %d/%d", seq, deleted)
			}
		})
	})

}
`

const boundedMaintenanceSemanticConsumerTest = `package acceptance_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	app "example.test/home"
	"fmt"
	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	providerpostgres "github.com/eleven-am/golem/go/provider/postgresql"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

` + maintenanceEmbeddingProviderSource + `
func semanticMaintenanceID(kind byte, number uint32) golem.UUID {
	var id golem.UUID
	id[0] = kind
	binary.BigEndian.PutUint32(id[12:], number)
	return id
}

func TestMaintenanceSemanticEpisodeDeletionIsAtomic(t *testing.T) {
	ctx := context.Background()
	selectedProvider := golem.Provider(os.Getenv("GOLEM_OBJECT_PROVIDER"))
	var database *provider.Database
	var err error
	switch selectedProvider {
	case golem.SQLite:
		database, err = providersqlite.Open(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
	case golem.PostgreSQL:
		database, err = providerpostgres.Open(ctx, providerpostgres.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
	default:
		t.Fatalf("unsupported semantic maintenance provider %q", selectedProvider)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	spec, err := embedding.NewSpecification("test", "maintenance-constant", "v1", 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &maintenanceEmbedder{spec: spec}
	embeddings, err := embedding.NewRegistry(map[string]embedding.Provider{"maintenance": embedder})
	if err != nil {
		t.Fatal(err)
	}
	registry := queue.NewRegistry()
	maintenance, err := app.RegisterMaintenance(registry)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.Open(ctx, app.Config[app.Principal]{Database: database, Embeddings: embeddings, EventTransport: transport,
		Queue:               &golemruntime.QueueConfig{Registry: registry, Limits: queue.Limits{Concurrency: 1, ClaimBatch: 1, PollInterval: 10 * time.Millisecond, RetentionEvery: queue.RetentionDisabled}},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		ResolvePrincipal:    func(_ context.Context, p app.Principal) (app.Actor, error) { return app.Actor{OwnerID: p.OwnerID}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	maintenance.Application = application
	system := application.System()
	root, owner, task, generation := semanticMaintenanceID(1, 1), semanticMaintenanceID(2, 1), semanticMaintenanceID(5, 1), semanticMaintenanceID(6, 1)
	if _, err := system.Shows.Create(ctx, app.Shows.Create(app.Shows.ID.Create(root), app.Shows.OwnerID.Create(owner), app.Shows.Removed.Create(false), app.Shows.Fence.Create(0))); err != nil {
		t.Fatal(err)
	}
	const episodes = 33
	for index := uint32(1); index <= episodes; index++ {
		episode := semanticMaintenanceID(3, index)
		if err := system.Transaction(ctx, func(tx *app.SystemTx[app.Principal]) error {
			if err := app.IngestSystemIntoTx(ctx, tx, app.IngestEpisode{ID: episode, ShowID: root, Title: "indexed episode"}); err != nil {
				return err
			}
			for child := uint32(0); child < 2; child++ {
				if _, err := tx.Assets.Create(ctx, app.Assets.Create(app.Assets.ID.Create(semanticMaintenanceID(4, index*2+child)), app.Assets.EpisodeID.Create(episode), app.Assets.Body.Create("private leaf"))); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if embedder.inputs.Load() != 0 {
		t.Fatal("mutation synchronously called embedding provider")
	}

	// Resolve names from the actual reviewed physical schema. No fabricated
	// shadow rows, private registry swap, or custom semantic handler is used.
	manifestBytes, err := app.GolemGeneratedSchemaBundle().MigrationManifest(selectedProvider)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Entries []struct {
			AfterSnapshot struct {
				Namespace struct{ Name string }
				Tables    []struct{ Name string }
				System    struct {
					Namespace struct{ Name string }
					Objects   []struct{ Kind, Name string }
				}
				Extensions []struct {
					Kind       string
					Attributes []struct {
						Name  string
						Value struct{ String string }
					}
				}
			}
		}
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) == 0 {
		t.Fatal("reviewed schema absent")
	}
	physical := manifest.Entries[len(manifest.Entries)-1].AfterSnapshot
	quote := func(value string) string { return "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\"" }
	appTable := func(name string) string {
		for _, table := range physical.Tables {
			if table.Name == name {
				return quote(physical.Namespace.Name) + "." + quote(name)
			}
		}
		t.Fatalf("reviewed table %s absent", name)
		return ""
	}
	tables := map[string]string{}
	for _, object := range physical.System.Objects {
		if object.Kind == "outbox" || object.Kind == "outbox_delivery" {
			tables[object.Kind] = quote(physical.System.Namespace.Name) + "." + quote(object.Name)
		}
	}
	if len(tables) != 2 {
		t.Fatal("reviewed event inventory absent")
	}
	storage := ""
	for _, extension := range physical.Extensions {
		if extension.Kind == "golem.semantic-index" {
			if storage != "" {
				t.Fatal("semantic fixture must have exactly one index")
			}
			for _, attribute := range extension.Attributes {
				if attribute.Name == "storage" {
					storage = attribute.Value.String
				}
			}
		}
	}
	if storage == "" {
		t.Fatal("reviewed semantic storage absent")
	}
	shadow, vectors := quote(physical.Namespace.Name)+"."+quote(storage+"_state"), quote(physical.Namespace.Name)+"."+quote(storage+"_vec")
	queueTable := quote(physical.System.Namespace.Name) + "." + quote("golem_queue")
	scalar := func(statement string) int64 {
		t.Helper()
		var value int64
		if err := database.UnsafeSQLX().GetContext(ctx, &value, statement); err != nil {
			t.Fatal(err)
		}
		return value
	}
	queueCounts := func(types ...string) queue.StateCounts {
		t.Helper()
		value, err := application.QueueOperator().CountByState(ctx, queue.CountQuery{Types: types})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	startWorker := func(t *testing.T) func() {
		t.Helper()
		workerCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- application.RunQueueWorker(workerCtx) }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("worker: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("worker did not stop")
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	wait := func(t *testing.T, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			if condition() {
				return
			}
			if counts := queueCounts(); counts.Failed != 0 {
				t.Fatalf("real queue worker failed: %+v", counts)
			}
			if time.Now().After(deadline) {
				t.Fatal("semantic maintenance did not converge")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	stop := startWorker(t)
	wait(t, func() bool {
		return scalar("SELECT COUNT(*) FROM "+shadow+" WHERE status='ready'") == episodes && scalar("SELECT COUNT(*) FROM "+vectors) == episodes && queueCounts().Pending == 0 && queueCounts().Leased == 0
	})
	stop()
	if embedder.inputs.Load() != episodes {
		t.Fatalf("real seed embedding inputs=%d want=%d", embedder.inputs.Load(), episodes)
	}
	caller, err := application.ForPrincipal(ctx, app.Principal{OwnerID: owner})
	if err != nil {
		t.Fatal(err)
	}
	vector, err := embedding.NewVector([]float32{1, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	query, err := embedding.NewQueryVector(spec, vector)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := caller.Episodes.SearchByVectorTitles(ctx, query, 40)
	if err != nil || len(rows) != episodes {
		t.Fatalf("ready native semantic results=%d err=%v", len(rows), err)
	}
	if err := maintenance.RequestRemoval(ctx, caller, root, task, generation); err != nil {
		t.Fatal(err)
	}
	rows, err = caller.Episodes.SearchByVectorTitles(ctx, query, 40)
	if err != nil || len(rows) != 0 {
		t.Fatalf("logical removal still exposed ready vectors: %d %v", len(rows), err)
	}
	if scalar("SELECT COUNT(*) FROM "+vectors) != episodes || scalar("SELECT COUNT(*) FROM "+shadow+" WHERE status='ready'") != episodes {
		t.Fatal("logical removal test did not retain real ready vectors")
	}

	// 66 private leaves require three deletion pages and an empty transition.
	// Only the NEXT page deletes Episodes and produces semantic tombstone marks.
	for sequence := int32(0); sequence < 4; sequence++ {
		if err := maintenance.ProcessPage(ctx, app.CleanupPage{TaskID: task, Generation: generation, Sequence: sequence}); err != nil {
			t.Fatal(err)
		}
	}
	taskRows, err := system.RemovalTasks.FindMany(ctx, golem.Where(app.RemovalTasks.ID.Eq(task)))
	if err != nil || len(taskRows) != 1 {
		t.Fatalf("task: %v", err)
	}
	sequence, sOK := golem.Value(taskRows[0], app.RemovalTasks.Sequence).Get()
	phase, pOK := golem.Value(taskRows[0], app.RemovalTasks.Phase).Get()
	if !sOK || !pOK || sequence != 4 || phase != 1 {
		t.Fatalf("did not enter indexed episode phase: seq=%d phase=%d", sequence, phase)
	}
	if count, err := system.Assets.Count(ctx); err != nil || count != 0 {
		t.Fatalf("asset phase incomplete: %d %v", count, err)
	}
	if scalar("SELECT COUNT(*) FROM "+shadow+" WHERE status='pending'") != 0 {
		t.Fatal("asset-only pages unexpectedly marked episode index")
	}

	// Snapshot exact stored application versions/cells, event rows, queue jobs,
	// ready shadow metadata and vector bytes while the worker is stopped.
	// This is trusted test introspection, not an alternate index-state writer.
	snapshot := func() map[string][][]any {
		t.Helper()
		result := map[string][][]any{}
		inventory := map[string]string{appTable("maintenance_shows"): "id", appTable("maintenance_episodes"): "id", appTable("maintenance_assets"): "id", appTable("maintenance_tasks"): "id", tables["outbox"]: "event_id", tables["outbox_delivery"]: "causation_id", queueTable: "id", shadow: "record_key", vectors: "record_key"}
		for table, order := range inventory {
			rows, err := database.UnsafeSQLX().QueryxContext(ctx, "SELECT * FROM "+table+" ORDER BY "+quote(order))
			if err != nil {
				t.Fatal(err)
			}
			var values [][]any
			for rows.Next() {
				cells, err := rows.SliceScan()
				if err != nil {
					_ = rows.Close()
					t.Fatal(err)
				}
				for index, cell := range cells {
					if bytes, ok := cell.([]byte); ok {
						cells[index] = append([]byte(nil), bytes...)
					}
				}
				values = append(values, cells)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			result[table] = values
		}
		return result
	}
	before := snapshot()
	beforeFacts, beforeDeliveries := scalar("SELECT COUNT(*) FROM "+tables["outbox"]), scalar("SELECT COUNT(*) FROM "+tables["outbox_delivery"])
	beforeJobs := queueCounts(app.CleanupJobType)
	beforeCalls := embedder.inputs.Load()
	// Fail at the actual semantic drain enqueue, AFTER page effects, successor,
	// event flush and 32 semantic marks. Order guards ensure an unrelated earlier
	// error cannot satisfy this acceptance.
	trigger := fmt.Sprintf("CREATE TRIGGER maintenance_semantic_drain_fault BEFORE INSERT ON %s WHEN NEW.type='semantic.drain' BEGIN "+
		"SELECT CASE WHEN (SELECT COUNT(*) FROM %s)!=1 THEN RAISE(ABORT,'semantic-order-episodes') END; "+
		"SELECT CASE WHEN (SELECT sequence FROM %s)!=5 THEN RAISE(ABORT,'semantic-order-progress') END; "+
		"SELECT CASE WHEN (SELECT COUNT(*) FROM %s WHERE status='pending')!=32 THEN RAISE(ABORT,'semantic-order-marks') END; "+
		"SELECT CASE WHEN (SELECT COUNT(*) FROM %s)!=%d THEN RAISE(ABORT,'semantic-order-facts') END; "+
		"SELECT CASE WHEN (SELECT COUNT(*) FROM %s)!=%d THEN RAISE(ABORT,'semantic-order-delivery') END; "+
		"SELECT CASE WHEN (SELECT COUNT(*) FROM %s WHERE type='maintenance.cleanup-page' AND status='pending')!=%d THEN RAISE(ABORT,'semantic-order-successor') END; "+
		"SELECT RAISE(ABORT,'maintenance-semantic-drain-refusal'); END", queueTable, appTable("maintenance_episodes"), appTable("maintenance_tasks"), shadow, tables["outbox"], beforeFacts+34, tables["outbox_delivery"], beforeDeliveries+1, queueTable, beforeJobs.Pending+1)
	install := []string{trigger}
	uninstall := []string{"DROP TRIGGER IF EXISTS maintenance_semantic_drain_fault"}
	if selectedProvider == golem.PostgreSQL {
		function := quote(physical.System.Namespace.Name) + "." + quote("maintenance_semantic_drain_fault_fn")
		body := fmt.Sprintf("CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN "+
			"IF (SELECT COUNT(*) FROM %s)<>1 THEN RAISE EXCEPTION 'semantic-order-episodes'; END IF; "+
			"IF (SELECT sequence FROM %s)<>5 THEN RAISE EXCEPTION 'semantic-order-progress'; END IF; "+
			"IF (SELECT COUNT(*) FROM %s WHERE status='pending')<>32 THEN RAISE EXCEPTION 'semantic-order-marks'; END IF; "+
			"IF (SELECT COUNT(*) FROM %s)<>%d THEN RAISE EXCEPTION 'semantic-order-facts'; END IF; "+
			"IF (SELECT COUNT(*) FROM %s)<>%d THEN RAISE EXCEPTION 'semantic-order-delivery'; END IF; "+
			"IF (SELECT COUNT(*) FROM %s WHERE type='maintenance.cleanup-page' AND status='pending')<>%d THEN RAISE EXCEPTION 'semantic-order-successor'; END IF; "+
			"RAISE EXCEPTION 'maintenance-semantic-drain-refusal'; END $$", function, appTable("maintenance_episodes"), appTable("maintenance_tasks"), shadow, tables["outbox"], beforeFacts+34, tables["outbox_delivery"], beforeDeliveries+1, queueTable, beforeJobs.Pending+1)
		install = []string{body, "CREATE TRIGGER maintenance_semantic_drain_fault BEFORE INSERT ON " + queueTable + " FOR EACH ROW WHEN (NEW.type='semantic.drain') EXECUTE FUNCTION " + function + "()"}
		uninstall = []string{"DROP TRIGGER IF EXISTS maintenance_semantic_drain_fault ON " + queueTable, "DROP FUNCTION IF EXISTS " + function + "()"}
	}
	drop := func() {
		for _, statement := range uninstall {
			if _, err := database.UnsafeSQLX().ExecContext(ctx, statement); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(drop)
	for _, statement := range install {
		if _, err := database.UnsafeSQLX().ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	page := app.CleanupPage{TaskID: task, Generation: generation, Sequence: 4}
	err = maintenance.ProcessPage(ctx, page)
	if err == nil || !strings.Contains(err.Error(), "maintenance-semantic-drain-refusal") {
		t.Fatalf("expected real semantic drain fault, got %v", err)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("episode page failure leaked rows/versions, facts, progress, semantic marks/vectors or queue work")
	}
	if embedder.inputs.Load() != beforeCalls {
		t.Fatal("failed mutation called embedding provider")
	}
	drop()
	if err := maintenance.ProcessPage(ctx, page); err != nil {
		t.Fatal(err)
	}
	if count, err := system.Episodes.Count(ctx); err != nil || count != 1 {
		t.Fatalf("retry episode survivors=%d err=%v", count, err)
	}
	if scalar("SELECT COUNT(*) FROM "+shadow+" WHERE status='pending'") != 32 || scalar("SELECT COUNT(*) FROM "+vectors) != episodes {
		t.Fatal("retry did not atomically persist real deletion marks while retaining worker-owned vectors")
	}
	if jobs := queueCounts("semantic.drain"); jobs.Pending != 1 || jobs.Leased != 0 {
		t.Fatalf("retry drain not durable: %+v", jobs)
	}
	if jobs := queueCounts(app.CleanupJobType); jobs.Pending != beforeJobs.Pending+1 {
		t.Fatalf("retry successor missing/duplicated: %+v", jobs)
	}
	if embedder.inputs.Load() != beforeCalls {
		t.Fatal("unstarted drain unexpectedly embedded")
	}
	stopFinal := startWorker(t)
	wait(t, func() bool {
		return scalar("SELECT COUNT(*) FROM "+appTable("maintenance_shows")) == 0 && scalar("SELECT COUNT(*) FROM "+appTable("maintenance_episodes")) == 0 && scalar("SELECT COUNT(*) FROM "+shadow) == 0 && scalar("SELECT COUNT(*) FROM "+vectors) == 0 && queueCounts().Pending == 0 && queueCounts().Leased == 0
	})
	stopFinal()
	if count, err := system.Assets.Count(ctx); err != nil || count != 0 {
		t.Fatalf("remaining private leaves: %d %v", count, err)
	}
	taskRows, err = system.RemovalTasks.FindMany(ctx, golem.Where(app.RemovalTasks.ID.Eq(task)))
	if err != nil || len(taskRows) != 1 {
		t.Fatalf("completed task: %v", err)
	}
	completed, cOK := golem.Value(taskRows[0], app.RemovalTasks.Completed).Get()
	deleted, dOK := golem.Value(taskRows[0], app.RemovalTasks.Deleted).Get()
	sequence, sOK = golem.Value(taskRows[0], app.RemovalTasks.Sequence).Get()
	if !cOK || !dOK || !sOK || !completed || deleted != episodes*3 || sequence != 8 {
		t.Fatalf("incomplete bounded semantic cleanup: completed=%v deleted=%d seq=%d", completed, deleted, sequence)
	}
	if jobs := queueCounts(app.CleanupJobType); jobs.Succeeded != 8 || jobs.Failed != 0 {
		t.Fatalf("maintenance replay/convergence jobs=%+v", jobs)
	}
	if embedder.inputs.Load() != beforeCalls {
		t.Fatal("deleting missing semantic owners called embedding provider")
	}
	t.Logf("%s real semantic deletion/rollback and durable queue convergence verified", selectedProvider)
}
`

// Shared generated-consumer support: deterministic vectors and input accounting.
// Both maintenance and one-off copying exercise the same actual worker owner.
const maintenanceEmbeddingProviderSource = `type maintenanceEmbedder struct {
	spec   embedding.Specification
	inputs atomic.Int64
}

func (provider *maintenanceEmbedder) Specification() embedding.Specification { return provider.spec }
func (provider *maintenanceEmbedder) Embed(ctx context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	provider.inputs.Add(int64(len(inputs)))
	result := make([]embedding.Vector, len(inputs))
	for index := range result {
		value, err := embedding.NewVector([]float32{1, 0, 0})
		if err != nil {
			return nil, err
		}
		result[index] = value
	}
	return result, nil
}
`
