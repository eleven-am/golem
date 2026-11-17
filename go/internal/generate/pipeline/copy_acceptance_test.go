package pipeline

import (
	"strings"
	"testing"
)

// A disposable one-off import reference, not an existing-database adoption API
// or a Frames schema migration. The source is closed while copying; destination
// traffic/workers stay stopped until copying and explicit domain work finish.
func TestFreshGeneratedRestartableLegacyCopy(t *testing.T) {
	runGeneratedDTOConsumer(t, "boundedmaintenance", func(source string) string {
		marker := "golem.Model[RemovalTask](schema)"
		if strings.Count(source, marker) != 1 {
			t.Fatal("maintenance schema declaration changed")
		}
		source = strings.Replace(source, marker, marker+"\n golem.Model[ImportCheckpoint](schema)", 1)
		index := "golem.Subscriptions[Episode](golem.DeleteSnapshot(Episodes.ShowID, Episodes.Title)),"
		provider := "golem.Providers(schema, golem.SQLite, golem.PostgreSQL)"
		if strings.Count(source, index) != 1 || strings.Count(source, provider) != 1 {
			t.Fatal("maintenance semantic declaration changed")
		}
		source = strings.Replace(source, provider, "golem.Providers(schema, golem.SQLite)\n golem.EmbeddingSpace(schema, \"maintenance\", 3)", 1)
		source = strings.Replace(source, index, index+"\n golem.SemanticIndex(\"titles\", \"maintenance\", Episodes.Title),", 1)
		return source + `
// Progress is destination-owned and commits in the same transaction as rows.
type ImportCheckpoint struct {
 _ struct{} ` + "`golem:\"model;table=import_checkpoints\"`" + `
 ID golem.UUID ` + "`db:\"id\" golem:\"pk\"`" + `
 Source string ` + "`db:\"source\"`" + `
 Revision int32 ` + "`db:\"revision\"`" + `
 LastSequence int64 ` + "`db:\"cursor\"`" + `
}
func (ImportCheckpoint) DefinePolicy(rules *golem.Rules[ImportCheckpoint], _ Actor) { rules.CanRead(golem.None[ImportCheckpoint]()) }
`
	}, legacyCopyConsumerTest)
}

const legacyCopyConsumerTest = `package acceptance_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	app "example.test/home"
	"fmt"
	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"io"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

` + maintenanceEmbeddingProviderSource + `

const copyPageSize = 48 // At most three domain writes per source row + checkpoint.
const copyRevision int32 = 1

func copyID(n int) golem.UUID {
	var id golem.UUID
	id[12] = byte(n >> 24)
	id[13] = byte(n >> 16)
	id[14] = byte(n >> 8)
	id[15] = byte(n)
	return id
}

var rootID = copyID(1)
var ownerID = copyID(2)
var progressID = copyID(3)

type copyApp struct {
	database    *provider.Database
	app         *app.App[app.Principal]
	maintenance *app.Maintenance
	embedder *maintenanceEmbedder
}

func openCopy(ctx context.Context) (copyApp, error) {
	database, err := providersqlite.Open(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
	if err != nil {
		return copyApp{}, err
	}
	memory, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		database.Close()
		return copyApp{}, err
	}
	registry := queue.NewRegistry()
	maintenance, err := app.RegisterMaintenance(registry)
	if err != nil {
		database.Close()
		return copyApp{}, err
	}
	spec, err := embedding.NewSpecification("test", "maintenance-constant", "v1", 3, 8)
	if err != nil { database.Close(); return copyApp{}, err }
	embedder := &maintenanceEmbedder{spec: spec}
	embeddings, err := embedding.NewRegistry(map[string]embedding.Provider{"maintenance": embedder})
	if err != nil { database.Close(); return copyApp{}, err }
	application, err := app.Open(ctx, app.Config[app.Principal]{Database: database, EventTransport: memory, Embeddings: embeddings,
		Queue: &golemruntime.QueueConfig{Registry: registry, Limits: queue.Limits{Concurrency: 1, ClaimBatch: 1, PollInterval: 10*time.Millisecond, RetentionEvery: queue.RetentionDisabled}}, ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		ResolvePrincipal: func(_ context.Context, p app.Principal) (app.Actor, error) { return app.Actor{OwnerID: p.OwnerID}, nil },
	})
	if err != nil {
		database.Close()
		return copyApp{}, err
	}
	maintenance.Application = application
	return copyApp{database, application, maintenance, embedder}, nil
}

type sourceRecord struct {
	Seq                int64
	Kind               int
	ID, Parent, Root   golem.UUID
	Value, ParentTitle string
}

func sourceDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4<<20|1))
	if err != nil {
		return "", err
	}
	if len(data) > 4<<20 {
		return "", errors.New("source exceeds fixture bound")
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
func readSourcePage(ctx context.Context, source *sql.DB, cursor int64) ([]sourceRecord, error) {
	// Reject oversized or non-text values before scanning/materializing them.
	var bad int
	err := source.QueryRowContext(ctx, "SELECT count(*) FROM records WHERE typeof(seq)!='integer' OR seq<1 OR typeof(kind)!='integer' OR kind NOT IN(0,1,2) OR typeof(id)!='text' OR length(id)!=36 OR typeof(parent)!='text' OR length(parent)!=36 OR typeof(root)!='text' OR length(root)!=36 OR typeof(value)!='text' OR length(CAST(value AS BLOB))>256").Scan(&bad)
	if err != nil {
		return nil, err
	}
	if bad != 0 {
		return nil, errors.New("invalid bounded source scalar")
	}
	rows, err := source.QueryContext(ctx, "SELECT seq,kind,id,parent,root,value FROM records WHERE seq>? ORDER BY seq LIMIT ?", cursor, copyPageSize)
	if err != nil {
		return nil, err
	}
	var page []sourceRecord
	for rows.Next() {
		var record sourceRecord
		var id, parent, root string
		if err = rows.Scan(&record.Seq, &record.Kind, &id, &parent, &root, &record.Value); err != nil {
			rows.Close()
			return nil, err
		}
		record.ID, err = golem.ParseUUID(id)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("source UUID: %w", err)
		}
		record.Parent, err = golem.ParseUUID(parent)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("source parent UUID: %w", err)
		}
		record.Root, err = golem.ParseUUID(root)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("source root UUID: %w", err)
		}
		if record.ID.String() != id || record.Parent.String() != parent || record.Root.String() != root || record.ID == (golem.UUID{}) || !utf8.ValidString(record.Value) || record.Seq != cursor+int64(len(page))+1 {
			rows.Close()
			return nil, errors.New("noncanonical source scalar or sequence")
		}
		if record.Root != rootID {
			rows.Close()
			return nil, errors.New("source root mismatch")
		}
		page = append(page, record)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// All source reads and references are validated BEFORE the destination TX.
	// The source is immutable for this offline operation, avoiding a second DB
	// read or a one-connection deadlock inside application transactions.
	for index := range page {
		record := &page[index]
		if record.Kind == 0 {
			if record.Seq != 1 || record.ID != rootID || record.Parent != ownerID {
				return nil, errors.New("source root header mismatch")
			}
			continue
		}
		var parentKind int
		var parentSeq int64
		var parentRoot, parentTitle string
		err := source.QueryRowContext(ctx, "SELECT seq,kind,root,value FROM records WHERE id=?", record.Parent.String()).Scan(&parentSeq, &parentKind, &parentRoot, &parentTitle)
		if err != nil || parentSeq >= record.Seq || parentKind != record.Kind-1 || parentRoot != record.Root.String() {
			return nil, errors.New("source parent mismatch")
		}
		record.ParentTitle = parentTitle
	}
	return page, nil
}

// copyLegacy is deliberately one fixed mapping, with no SQL on destination
// writes. Each asset contributes its parent guard/fence to the page budget;
// descendant count, not the number of root Shows, bounds the transaction.
func copyLegacy(ctx context.Context, value copyApp, path, interruption string) error {
	digest, err := sourceDigest(path)
	if err != nil {
		return err
	}
	sourceURL := url.URL{Scheme: "file", Path: path}
	query := sourceURL.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	sourceURL.RawQuery = query.Encode()
	source, err := sql.Open("sqlite", sourceURL.String())
	if err != nil {
		return err
	}
	defer source.Close()
	source.SetMaxOpenConns(1)
	var cursor int64
	existing, err := value.app.System().ImportCheckpoints.FindMany(ctx, golem.Where(app.ImportCheckpoints.ID.Eq(progressID)), golem.Take[app.ImportCheckpoint](1))
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		saved, _ := golem.Value(existing[0], app.ImportCheckpoints.Source).Get()
		revision, _ := golem.Value(existing[0], app.ImportCheckpoints.Revision).Get()
		cursor, _ = golem.Value(existing[0], app.ImportCheckpoints.LastSequence).Get()
		if saved != digest || revision != copyRevision {
			return errors.New("source identity or mapping revision mismatch")
		}
	}
	for {
		page, err := readSourcePage(ctx, source, cursor)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		next := page[len(page)-1].Seq
		err = value.app.System().Transaction(ctx, func(tx *app.SystemTx[app.Principal]) error {
			for _, record := range page {
				switch record.Kind {
				case 0:
					_, err = tx.Shows.Create(ctx, app.Shows.Create(app.Shows.ID.Create(record.ID), app.Shows.OwnerID.Create(record.Parent), app.Shows.Removed.Create(false), app.Shows.Fence.Create(0)))
				case 1:
					err = app.IngestSystemIntoTx(ctx, tx, app.IngestEpisode{ID: record.ID, ShowID: record.Parent, Title: record.Value})
				case 2:
					// Reuse the existing domain guard for this asset's parent and root;
					// removed or concurrently changed roots cannot acquire new descendants.
					err = app.IngestSystemIntoTx(ctx, tx, app.IngestEpisode{ID: record.Parent, ShowID: record.Root, Title: record.ParentTitle})
					if err == nil {
						_, err = tx.Assets.Create(ctx, app.Assets.Create(app.Assets.ID.Create(record.ID), app.Assets.EpisodeID.Create(record.Parent), app.Assets.Body.Create(record.Value)))
					}
				}
				if err != nil {
					return err
				}
			}
			if cursor == 0 {
				_, err = tx.ImportCheckpoints.Create(ctx, app.ImportCheckpoints.Create(app.ImportCheckpoints.ID.Create(progressID), app.ImportCheckpoints.Source.Create(digest), app.ImportCheckpoints.Revision.Create(copyRevision), app.ImportCheckpoints.LastSequence.Create(next)))
			} else {
				_, err = tx.ImportCheckpoints.Update(ctx, app.ImportCheckpoints.ByID.Value(progressID), app.ImportCheckpoints.Update(app.ImportCheckpoints.LastSequence.Set(next)))
			}
			if err != nil {
				return err
			}
			if value.embedder.inputs.Load()!=0 { return errors.New("copy transaction called embedding provider") }
			if interruption == "before-commit" {
				os.Exit(17)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if value.embedder.inputs.Load()!=0 { return errors.New("copy commit called embedding provider") }
		if interruption == "after-commit" {
			os.Exit(17)
		}
		cursor = next
	}
}

func TestLegacyCopyChild(t *testing.T) {
	mode := os.Getenv("GOLEM_COPY_MODE")
	if mode == "" {
		t.Skip("parent-owned child")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	value, err := openCopy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer value.database.Close()
	if err := copyLegacy(ctx, value, os.Getenv("GOLEM_COPY_SOURCE"), mode); err != nil {
		t.Fatal(err)
	}
	if value.embedder.inputs.Load()!=0 { t.Fatal("copy/restart called embedding provider") }
}

func TestLegacyCopyRestartsAndRemainsMaintainable(t *testing.T) {
	if os.Getenv("GOLEM_COPY_MODE") != "" {
		t.Skip("child only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sourcePath := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.Exec("CREATE TABLE records(seq INTEGER PRIMARY KEY,kind INTEGER NOT NULL,id TEXT UNIQUE NOT NULL,parent TEXT NOT NULL,root TEXT NOT NULL,value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	transaction, err := legacy.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var sequence int64
	insert := func(kind int, id, parent golem.UUID, text string) {
		t.Helper()
		sequence++
		if _, err := transaction.Exec("INSERT INTO records VALUES(?,?,?,?,?,?)", sequence, kind, id.String(), parent.String(), rootID.String(), text); err != nil {
			t.Fatal(err)
		}
	}
	insert(0, rootID, ownerID, "source-root")
	assets := 0
	for episode, children := range []int{1, 73, 1051} {
		id := copyID(100 + episode)
		insert(1, id, rootID, fmt.Sprintf("Épisode 日本 %d", episode))
		for child := 0; child < children; child++ {
			insert(2, copyID(1000+assets), id, fmt.Sprintf("asset-%d-雪", assets))
			assets++
		}
	}
	if err = transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}
	initialDigest, err := sourceDigest(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	child := func(mode string) {
		t.Helper()
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		childCtx, stop := context.WithTimeout(ctx, 65*time.Second)
		defer stop()
		command := exec.CommandContext(childCtx, executable, "-test.run=^TestLegacyCopyChild$", "-test.timeout=60s")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GOLEM_COPY_MODE=") && !strings.HasPrefix(entry, "GOLEM_COPY_SOURCE=") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "GOLEM_COPY_MODE="+mode, "GOLEM_COPY_SOURCE="+sourcePath)
		output, err := command.CombinedOutput()
		if mode == "complete" {
			if err != nil {
				t.Fatalf("copy child: %v %s", err, output)
			}
			return
		}
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != 17 {
			t.Fatalf("interruption %s: %v %s", mode, err, output)
		}
	}
	// Snapshot all durable user/system rows, including complete outbox payloads,
	// row versions and delivery state. No destination SQL is used to mutate data.
	snapshot := func() string {
		t.Helper()
		value, err := openCopy(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer value.database.Close()
		var names []string
		if err := value.database.UnsafeSQLX().SelectContext(ctx, &names, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"); err != nil {
			t.Fatal(err)
		}
		var records []string
		for _, name := range names {
			rows, err := value.database.UnsafeSQLX().QueryxContext(ctx, "SELECT * FROM \""+strings.ReplaceAll(name, "\"", "\"\"")+"\" LIMIT 10001")
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for rows.Next() {
				values, err := rows.SliceScan()
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				records = append(records, name+":"+string(raw))
				count++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if count > 10000 {
				t.Fatal("snapshot bound exceeded")
			}
		}
		sort.Strings(records)
		return strings.Join(records, "\n")
	}
	before := snapshot()
	for _, invalid := range []struct{ name, statement, want string }{
		{"uuid", "UPDATE records SET id='zzzzzzzz-0000-0000-0000-000000000064' WHERE seq=2", "source UUID"},
		{"reference", "UPDATE records SET parent='00000000-0000-0000-0000-000000000099' WHERE seq=3", "source parent mismatch"},
	} {
		path := filepath.Join(t.TempDir(), invalid.name+".sqlite")
		raw, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		writer, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Exec(invalid.statement); err != nil {
			t.Fatal(err)
		}
		writer.Close()
		target, err := openCopy(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = copyLegacy(ctx, target, path, "")
		target.database.Close()
		if err == nil || !strings.Contains(err.Error(), invalid.want) {
			t.Fatalf("%s source accepted or wrong refusal: %v", invalid.name, err)
		}
		if snapshot() != before {
			t.Fatalf("%s source committed partial rows/progress/facts", invalid.name)
		}
	}
	child("before-commit")
	if after := snapshot(); after != before {
		t.Fatal("precommit crash left rows, progress, versions or facts")
	}
	child("after-commit")
	partial := snapshot()
	if partial == before {
		t.Fatal("postcommit crash lost its committed page")
	}
	child("before-commit")
	if snapshot() != partial {
		t.Fatal("restart precommit crash changed the already committed prefix")
	}
	// Changed source identity cannot resume a checkpoint, even if the mapping
	// would otherwise decode. The caller has stopped all source writers.
	modified := filepath.Join(t.TempDir(), "changed.sqlite")
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(modified, raw, 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := sql.Open("sqlite", modified)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = changed.Exec("UPDATE records SET value='changed' WHERE seq=1"); err != nil {
		t.Fatal(err)
	}
	changed.Close()
	value, err := openCopy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = copyLegacy(ctx, value, modified, "")
	value.database.Close()
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("changed source accepted: %v", err)
	}
	if snapshot() != partial {
		t.Fatal("source refusal advanced progress or rows")
	}
	child("complete")
	completed := snapshot()
	child("complete")
	if snapshot() != completed {
		t.Fatal("completed restart duplicated rows, facts or versions")
	}
	for _, line := range strings.Split(partial, "\n") {
		table, _, _ := strings.Cut(line, ":")
		if strings.Contains(table, "outbox") && !strings.Contains("\n"+completed+"\n", "\n"+line+"\n") {
			t.Fatal("resume lost or changed retained causation/delivery evidence")
		}
	}
	if digest, err := sourceDigest(sourcePath); err != nil || digest != initialDigest {
		t.Fatalf("copy modified source: %v", err)
	}
	value, err = openCopy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer value.database.Close()
	system := value.app.System()
	checkpoint, err := system.ImportCheckpoints.FindUnique(ctx, app.ImportCheckpoints.ByID.Value(progressID))
	if err != nil {
		t.Fatal(err)
	}
	last, _ := golem.Value(checkpoint, app.ImportCheckpoints.LastSequence).Get()
	if last != sequence {
		t.Fatalf("checkpoint=%d want=%d", last, sequence)
	}
	if n, err := system.Shows.Count(ctx); err != nil || n != 1 {
		t.Fatalf("shows=%d %v", n, err)
	}
	if n, err := system.Episodes.Count(ctx); err != nil || n != 3 {
		t.Fatalf("episodes=%d %v", n, err)
	}
	if n, err := system.Assets.Count(ctx); err != nil || n != int64(assets) {
		t.Fatalf("assets=%d %v", n, err)
	}
	// Insert after final row counts and before removal. Read immutable source
	// pages and compare all authored destination fields through generated APIs.
	verifyURL := url.URL{Scheme: "file", Path: sourcePath}
	verifyQuery := verifyURL.Query()
	verifyQuery.Set("mode", "ro")
	verifyQuery.Set("immutable", "1")
	verifyURL.RawQuery = verifyQuery.Encode()
	verifySource, err := sql.Open("sqlite", verifyURL.String())
	if err != nil {
		t.Fatal(err)
	}
	verifySource.SetMaxOpenConns(1)
	defer verifySource.Close()
	var verifiedCursor int64
	for {
		page, err := readSourcePage(ctx, verifySource, verifiedCursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		expectedAssets := make(map[golem.UUID]sourceRecord)
		var assetIDs []golem.UUID
		for _, record := range page {
			switch record.Kind {
			case 0:
				row, err := system.Shows.FindUnique(ctx, app.Shows.ByID.Value(record.ID))
				if err != nil {
					t.Fatal(err)
				}
				id, idOK := golem.Value(row, app.Shows.ID).Get()
				owner, ownerOK := golem.Value(row, app.Shows.OwnerID).Get()
				removed, removedOK := golem.Value(row, app.Shows.Removed).Get()
				if !idOK || !ownerOK || !removedOK || id != record.ID || owner != record.Parent || removed {
					t.Fatalf("copied root differs from source %s", record.ID)
				}
			case 1:
				row, err := system.Episodes.FindUnique(ctx, app.Episodes.ByID.Value(record.ID))
				if err != nil {
					t.Fatal(err)
				}
				id, idOK := golem.Value(row, app.Episodes.ID).Get()
				parent, parentOK := golem.Value(row, app.Episodes.ShowID).Get()
				title, titleOK := golem.Value(row, app.Episodes.Title).Get()
				if !idOK || !parentOK || !titleOK || id != record.ID || parent != record.Parent || title != record.Value {
					t.Fatalf("copied episode differs from source %s", record.ID)
				}
			case 2:
				expectedAssets[record.ID] = record
				assetIDs = append(assetIDs, record.ID)
			}
		}
		if len(assetIDs) > 0 {
			rows, err := system.Assets.FindMany(ctx, golem.Where(app.Assets.ID.In(assetIDs...)), golem.Take[app.Asset](copyPageSize))
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				id, idOK := golem.Value(row, app.Assets.ID).Get()
				parent, parentOK := golem.Value(row, app.Assets.EpisodeID).Get()
				body, bodyOK := golem.Value(row, app.Assets.Body).Get()
				expected, present := expectedAssets[id]
				if !idOK || !parentOK || !bodyOK || !present || parent != expected.Parent || body != expected.Value {
					t.Fatalf("copied asset differs from source: id=%s parent=%s body=%q", id, parent, body)
				}
				delete(expectedAssets, id)
			}
			if len(expectedAssets) != 0 {
				t.Fatalf("source page missing %d copied asset identities", len(expectedAssets))
			}
		}
		verifiedCursor = page[len(page)-1].Seq
	}
	if verifiedCursor != sequence {
		t.Fatalf("verified source prefix=%d want=%d", verifiedCursor, sequence)
	}
	caller, err := value.app.ForPrincipal(ctx, app.Principal{OwnerID: ownerID})
	if err != nil {
		t.Fatal(err)
	}
	// No worker was started by Open, copying, retry or the complete no-op.
	// Copied semantic marks are now explicitly drained once, outside copy TXs.
	if value.embedder.inputs.Load()!=0 { t.Fatal("copy preparation called embedding provider") }
	vector,err:=embedding.NewVector([]float32{1,0,0});if err!=nil{t.Fatal(err)}
	query,err:=embedding.NewQueryVector(value.embedder.spec,vector);if err!=nil{t.Fatal(err)}
	beforeIndex,err:=caller.Episodes.SearchByVectorTitles(ctx,query,10)
	if err!=nil||len(beforeIndex)!=0{t.Fatalf("episodes indexed before explicit drain: %d %v",len(beforeIndex),err)}
	counts,err:=value.app.QueueOperator().CountByState(ctx,queue.CountQuery{});if err!=nil||counts.Pending==0{t.Fatalf("copy did not retain pending semantic work: %+v %v",counts,err)}
	drainSemantic:=func(){
		t.Helper()
		workerCtx,cancelWorker:=context.WithCancel(ctx)
		done:=make(chan error,1)
		go func(){done<-value.app.RunQueueWorker(workerCtx)}()
		var once sync.Once
		stop:=func(){once.Do(func(){cancelWorker();select{case err:=<-done:if err!=nil{t.Errorf("worker: %v",err)};case <-time.After(5*time.Second):t.Error("worker did not stop")}})}
		defer stop()
		deadline:=time.Now().Add(30*time.Second)
		for {
			counts,err:=value.app.QueueOperator().CountByState(ctx,queue.CountQuery{})
			if err!=nil{t.Fatal(err)}
			if counts.Failed!=0{t.Fatalf("semantic queue failed: %+v",counts)}
			if counts.Pending==0&&counts.Leased==0{break}
			if time.Now().After(deadline){t.Fatal("explicit semantic drain did not converge")}
			time.Sleep(5*time.Millisecond)
		}
		stop()
	}
	drainSemantic()
	if value.embedder.inputs.Load()!=3{t.Fatalf("embedding inputs=%d want exactly three copied Episodes",value.embedder.inputs.Load())}
	indexed,err:=caller.Episodes.SearchByVectorTitles(ctx,query,10);if err!=nil{t.Fatal(err)}
	wantIndexed:=map[golem.UUID]bool{copyID(100):true,copyID(101):true,copyID(102):true}
	for _,result:=range indexed{id,ok:=golem.Value(result.Row(),app.Episodes.ID).Get();if !ok||!wantIndexed[id]{t.Fatalf("unexpected/duplicate semantic identity %s",id)};delete(wantIndexed,id)}
	if len(wantIndexed)!=0{t.Fatalf("missing %d copied semantic identities",len(wantIndexed))}
	stranger,err:=value.app.ForPrincipal(ctx,app.Principal{OwnerID:copyID(9999)});if err!=nil{t.Fatal(err)}
	denied,err:=stranger.Episodes.SearchByVectorTitles(ctx,query,10);if err!=nil||len(denied)!=0{t.Fatalf("semantic owner isolation: %d %v",len(denied),err)}
	// An idle second drain must not re-embed completed work.
	drainSemantic()
	if value.embedder.inputs.Load()!=3{t.Fatal("completed semantic drain re-embedded rows")}
	task, generation := copyID(5), copyID(6)
	if err := value.maintenance.RequestRemoval(ctx, caller, rootID, task, generation); err != nil {
		t.Fatal(err)
	}
	if n, err := caller.Episodes.Count(ctx); err != nil || n != 0 {
		t.Fatalf("logical removal visibility=%d %v", n, err)
	}
	if rows,err:=caller.Episodes.SearchByVectorTitles(ctx,query,10);err!=nil||len(rows)!=0{t.Fatalf("logical removal exposed indexed rows: %d %v",len(rows),err)}
	if err := app.IngestSystem(ctx, value.app, app.IngestEpisode{ID: copyID(999), ShowID: rootID, Title: "late"}); !errors.Is(err, app.ErrRootUnavailable) {
		t.Fatalf("removed import root accepted ingest: %v", err)
	}
	for step := int32(0); step < 100; step++ {
		if err := value.maintenance.ProcessPage(ctx, app.CleanupPage{TaskID: task, Generation: generation, Sequence: step}); err != nil {
			t.Fatal(err)
		}
		row, err := system.RemovalTasks.FindUnique(ctx, app.RemovalTasks.ByID.Value(task))
		if err != nil {
			t.Fatal(err)
		}
		done, _ := golem.Value(row, app.RemovalTasks.Completed).Get()
		if done {
			break
		}
		if step == 99 {
			t.Fatal("cleanup failed to converge")
		}
	}
	if n, err := system.Assets.Count(ctx); err != nil || n != 0 {
		t.Fatalf("cleanup assets=%d %v", n, err)
	}
	if n, err := system.Episodes.Count(ctx); err != nil || n != 0 {
		t.Fatalf("cleanup episodes=%d %v", n, err)
	}
	if n, err := system.Shows.Count(ctx); err != nil || n != 0 {
		t.Fatalf("cleanup roots=%d %v", n, err)
	}
	drainSemantic()
	if value.embedder.inputs.Load()!=3{t.Fatal("cleanup embedded deleted rows")}
	if rows,err:=system.Episodes.SearchByVectorTitles(ctx,query,10);err!=nil||len(rows)!=0{t.Fatalf("cleanup left searchable copied rows: %d %v",len(rows),err)}
}
`
