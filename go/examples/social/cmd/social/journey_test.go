package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/gorilla/websocket"
)

func TestP8ExternalSocialApplicationSQLiteJourney(t *testing.T) {
	root := socialHostRoot(t)
	dsn := "file:" + t.TempDir() + "/social.sqlite"
	applyReviewedSQLiteMigration(t, root, dsn)
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	runP8SocialJourney(t, database)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	assertP8ReopenedApplication(t, reopened)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	assertP8SQLiteSubprocessRestart(t, root)
}

type restartFactState struct {
	EventID     string         `db:"event_id"`
	CausationID string         `db:"causation_id"`
	Action      string         `db:"action"`
	Status      sql.NullString `db:"status"`
}

func assertP8SQLiteSubprocessRestart(t *testing.T, exampleRoot string) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "restart.sqlite")
	applyReviewedSQLiteMigration(t, exampleRoot, dsn)
	environment := os.Environ()
	hostBinary := filepath.Join(t.TempDir(), "social-host")
	fixtureBinary := filepath.Join(t.TempDir(), "social-recovery-fixture")
	runP8Command(t, exampleRoot, environment, "go", "build", "-o", hostBinary, "./cmd/social")
	runP8Command(t, exampleRoot, environment, "go", "build", "-o", fixtureBinary, "./cmd/social-recovery-fixture")
	fixtureEnvironment := setP8Environment(environment, "GOLEM_PROVIDER", "sqlite")
	fixtureEnvironment = setP8Environment(fixtureEnvironment, "GOLEM_DATABASE_DSN", dsn)
	runP8Command(t, exampleRoot, fixtureEnvironment, fixtureBinary, "seed")
	pending := readP8RestartFact(t, dsn)
	if pending.EventID == "" || pending.CausationID == "" || pending.Action != "created" || !pending.Status.Valid || pending.Status.String != "pending" {
		t.Fatalf("restart pending fact=%+v", pending)
	}

	first, firstDone, firstOutput := startP8RestartHost(t, exampleRoot, hostBinary, dsn)
	awaitP8DeliveredFact(t, dsn, pending)
	stopP8RestartHost(t, first, firstDone, firstOutput)

	second, secondDone, secondOutput := startP8RestartHost(t, exampleRoot, hostBinary, dsn)
	query := `{"query":"query { post(where: {ID: \"80000000-0000-0000-0000-000000000002\"}) { id title published } }"}`
	request, err := http.NewRequest(http.MethodPost, "http://"+second.address+"/graphql", strings.NewReader(query))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"id":"80000000-0000-0000-0000-000000000002"`)) || !bytes.Contains(body, []byte(`"title":"P8 recovery canary post"`)) || bytes.Contains(body, []byte(`"errors"`)) {
		t.Fatalf("restarted public GraphQL status=%d body=%s", response.StatusCode, body)
	}
	stopP8RestartHost(t, second, secondDone, secondOutput)
	delivered := readP8RestartFact(t, dsn)
	if delivered.EventID != pending.EventID || delivered.CausationID != pending.CausationID || !delivered.Status.Valid || delivered.Status.String != "delivered" {
		t.Fatalf("restart durable fact before=%+v after=%+v", pending, delivered)
	}
}

type p8RestartProcess struct {
	command *exec.Cmd
	address string
	exited  *atomic.Bool
}

func startP8RestartHost(t *testing.T, directory, binary, dsn string) (p8RestartProcess, <-chan error, *strings.Builder) {
	t.Helper()
	address := reserveP8Address(t)
	command := exec.Command(binary)
	command.Dir = directory
	command.Env = setP8Environment(os.Environ(), "GOLEM_PROVIDER", "sqlite")
	command.Env = setP8Environment(command.Env, "GOLEM_DATABASE_DSN", dsn)
	command.Env = setP8Environment(command.Env, "GOLEM_HTTP_ADDRESS", address)
	output := &strings.Builder{}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	exited := &atomic.Bool{}
	go func() {
		err := command.Wait()
		exited.Store(true)
		done <- err
	}()
	t.Cleanup(func() {
		if exited.Load() {
			return
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
		}
	})
	awaitP8HTTPStatus(t, "http://"+address+"/health/ready", http.StatusNoContent, done, output)
	return p8RestartProcess{command: command, address: address, exited: exited}, done, output
}

func stopP8RestartHost(t *testing.T, process p8RestartProcess, done <-chan error, output *strings.Builder) {
	t.Helper()
	if err := process.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal restart host: %v\n%s", err, output.String())
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("restart host shutdown: %v\n%s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		_ = process.command.Process.Kill()
		t.Fatalf("restart host did not stop\n%s", output.String())
	}
}

func readP8RestartFact(t *testing.T, dsn string) restartFactState {
	t.Helper()
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var facts []restartFactState
	if err := database.UnsafeSQLX().SelectContext(context.Background(), &facts, `SELECT o.event_id,o.causation_id,o.action,d.status FROM "_golem_outbox" o LEFT JOIN "_golem_outbox_delivery" d ON d.causation_id=o.causation_id ORDER BY o.recorded_at,o.event_id`); err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("restart outbox facts=%d", len(facts))
	}
	return facts[0]
}

func awaitP8DeliveredFact(t *testing.T, dsn string, pending restartFactState) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state := readP8RestartFact(t, dsn)
		if state.EventID != pending.EventID || state.CausationID != pending.CausationID {
			t.Fatalf("restart fact identity changed: before=%+v after=%+v", pending, state)
		}
		if state.Status.Valid && state.Status.String == "delivered" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("restart publisher did not durably deliver the pending fact")
}

func TestP8ExternalSocialApplicationPostgreSQLJourney(t *testing.T) {
	profiles := []struct {
		name string
		env  string
	}{
		{name: "C", env: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
	for _, profile := range profiles {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			base := os.Getenv(profile.env)
			if base == "" {
				t.Skip(profile.env + " is not configured")
			}
			dsn, cleanup := createP8DisposablePostgreSQLDatabase(t, base, profile.name)
			defer cleanup()
			applyReviewedPostgreSQLMigration(t, socialHostRoot(t), dsn)
			database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dsn})
			if err != nil {
				t.Fatal(err)
			}
			runP8SocialJourney(t, database)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: dsn})
			if err != nil {
				t.Fatal(err)
			}
			assertP8ReopenedApplication(t, reopened)
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertP8ReopenedApplication(t *testing.T, database *provider.Database) {
	t.Helper()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	application, err := openApplication(context.Background(), database, transport)
	if err != nil {
		t.Fatalf("reopen generated application over reviewed database: %v", err)
	}
	if count, err := application.System().Users.Count(context.Background()); err != nil || count != 2 {
		t.Fatalf("reopened database user count=%d error=%v", count, err)
	}
}

func createP8DisposablePostgreSQLDatabase(t *testing.T, base, profile string) (string, func()) {
	t.Helper()
	admin, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: base})
	if err != nil {
		t.Fatalf("open PostgreSQL %s administrative connection: %v", profile, err)
	}
	pool := admin.UnsafeSQLX()
	locale := struct {
		Collate string `db:"datcollate"`
		CType   string `db:"datctype"`
	}{}
	if err := pool.GetContext(context.Background(), &locale, `SELECT datcollate,datctype FROM pg_database WHERE datname=current_database()`); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	name := fmt.Sprintf("golem_p8_social_%s_%d", strings.ToLower(profile), time.Now().UnixNano())
	for _, value := range name {
		if !((value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') || value == '_') {
			_ = admin.Close()
			t.Fatalf("unsafe generated database name %q", name)
		}
	}
	create := fmt.Sprintf(`CREATE DATABASE "%s" TEMPLATE template0 ENCODING 'UTF8' LC_COLLATE %s LC_CTYPE %s`, name, postgresLiteral(locale.Collate), postgresLiteral(locale.CType))
	if _, err := pool.ExecContext(context.Background(), create); err != nil {
		_ = admin.Close()
		t.Fatalf("create isolated PostgreSQL %s database: %v", profile, err)
	}
	parsed, err := url.Parse(base)
	if err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	parsed.RawPath = ""
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, name)
		if _, err := pool.ExecContext(ctx, `DROP DATABASE "`+name+`"`); err != nil {
			t.Errorf("drop isolated PostgreSQL database %s: %v", name, err)
		}
		_ = admin.Close()
	}
	return parsed.String(), cleanup
}

func postgresLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func applyReviewedPostgreSQLMigration(t *testing.T, exampleRoot, dsn string) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "golem")
	build := exec.Command("go", "build", "-o", binary, "github.com/eleven-am/golem/go/cmd/golem")
	build.Dir = exampleRoot
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build public golem CLI: %v\n%s", err, output)
	}
	apply := exec.Command(binary, "migration", "apply", "--provider", "postgresql", "--dsn", dsn, "--migrations", "migrations")
	apply.Dir = exampleRoot
	apply.Env = os.Environ()
	if output, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("apply reviewed PostgreSQL migration: %v\n%s", err, output)
	}
}

type postHookTrace struct {
	mu         sync.Mutex
	phases     []social.PostHookPhase
	readPhases []social.PostReadHookPhase
	readRows   int
}

func (trace *postHookTrace) ObservePostReadHook(_ context.Context, phase social.PostReadHookPhase, rows []golem.Row[social.Post]) error {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.readPhases = append(trace.readPhases, phase)
	trace.readRows += len(rows)
	return nil
}

func (trace *postHookTrace) ObservePostHook(_ context.Context, phase social.PostHookPhase, _ golem.Row[social.Post]) error {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.phases = append(trace.phases, phase)
	return nil
}

func (trace *postHookTrace) snapshot() []social.PostHookPhase {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]social.PostHookPhase(nil), trace.phases...)
}

func (trace *postHookTrace) reset() {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.phases = nil
}

func (trace *postHookTrace) readSnapshot() ([]social.PostReadHookPhase, int) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]social.PostReadHookPhase(nil), trace.readPhases...), trace.readRows
}

func runP8SocialJourney(t *testing.T, database *provider.Database) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 128})
	if err != nil {
		t.Fatal(err)
	}
	var scopedAudits atomic.Int64
	application, err := openApplicationWithScopedReport(ctx, database, transport, func(context.Context, golem.ScopedAuditRecord) {
		scopedAudits.Add(1)
	})
	if err != nil {
		t.Fatalf("open generated application: %v", err)
	}
	publisherCtx, stopPublisher := context.WithCancel(ctx)
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- application.RunEventPublisher(publisherCtx) }()
	defer func() {
		stopPublisher()
		select {
		case <-publisherDone:
		case <-time.After(3 * time.Second):
			t.Error("event publisher did not stop")
		}
	}()
	awaitPublisher(t, application)

	userID := mustUUID(t, "10000000-0000-0000-0000-000000000001")
	otherID := mustUUID(t, "10000000-0000-0000-0000-000000000002")
	postID := mustUUID(t, "20000000-0000-0000-0000-000000000001")
	commentID := mustUUID(t, "30000000-0000-0000-0000-000000000001")
	replyID := mustUUID(t, "30000000-0000-0000-0000-000000000002")
	tagID := mustUUID(t, "40000000-0000-0000-0000-000000000001")
	system := application.System()
	if _, err := system.Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(userID), social.Users.Handle.Create("alice"), social.Users.Email.Create("alice@example.test"),
	)); err != nil {
		t.Fatalf("seed principal user: %v", err)
	}
	if _, err := system.Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(otherID), social.Users.Handle.Create("bob"), social.Users.Email.Create("bob@example.test"),
	)); err != nil {
		t.Fatalf("seed conditional-field user: %v", err)
	}
	const bearerToken = "p8-social-example-bearer"
	tokenHash := sha256.Sum256([]byte(bearerToken))
	sessionID := mustUUID(t, "50000000-0000-0000-0000-000000000001")
	if _, err := system.Sessions.Create(ctx, social.Sessions.Create(
		social.Sessions.ID.Create(sessionID),
		social.Sessions.UserID.Create(userID), social.Sessions.TokenHash.Create(tokenHash[:]),
		social.Sessions.ExpiresAt.Create(time.Now().UTC().Add(time.Hour)),
	)); err != nil {
		t.Fatalf("seed bytes/session scalar: %v", err)
	}

	principal := social.Principal{TokenHash: tokenHash}
	caller, err := application.ForPrincipal(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := caller.Posts.Events(ctx)
	if err != nil {
		t.Fatalf("open caller event stream: %v", err)
	}
	defer stream.Close()

	decimal := mustDecimal(t, "123456789.01")
	date := mustDate(t, "2026-08-09")
	clock := mustTime(t, "13:14:15.123456")
	metadata := mustJSON(t, `{"language":"en","pinned":true}`)
	trace := &postHookTrace{}
	hookContext := social.WithPostHookObserver(ctx, trace)
	created, err := caller.Posts.Create(hookContext, social.Posts.Create(
		social.Posts.ID.Create(postID), social.Posts.Title.Create("First post"),
		social.Posts.Body.Create("A complete generated backend, exercised from its public API."),
		social.Posts.Published.Create(false), social.Posts.Reactions.Create(int16(7)),
		social.Posts.Priority.Create(int32(11)), social.Posts.Views.Create(int64(9_007_199_254_740_991)),
		social.Posts.Momentum.Create(float32(1.25)), social.Posts.Rating.Create(4.75),
		social.Posts.Budget.Create(decimal), social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock),
		social.Posts.Metadata.Create(metadata), social.Posts.Visibility.Create(social.VisibilityFollowers),
		social.Posts.Topics.Create(golem.List[string]{"go", "graphql"}),
		social.Posts.Comments.Create(social.Comments.Create(
			social.Comments.ID.Create(commentID), social.Comments.AuthorID.Create(userID), social.Comments.Body.Create("nested comment"),
		)),
	), social.Posts.Select(
		social.Posts.ID, social.Posts.AuthorID, social.Posts.Title, social.Posts.Body, social.Posts.Published,
		social.Posts.Reactions, social.Posts.Priority, social.Posts.Views, social.Posts.Momentum,
		social.Posts.Rating, social.Posts.Budget, social.Posts.LiveDate, social.Posts.LiveTime,
		social.Posts.Metadata, social.Posts.Visibility, social.Posts.Topics, social.Posts.CreatedAt, social.Posts.UpdatedAt,
	))
	if err != nil {
		t.Fatalf("create full scalar/nested post: %v", err)
	}
	assertPostScalarRoundTrip(t, created, userID, postID, decimal, date, clock, metadata)
	if got := trace.snapshot(); fmt.Sprint(got) != fmt.Sprint([]social.PostHookPhase{
		social.PostHookBeforeCreate, social.PostHookAfterCreate, social.PostHookAfterCommitCreate,
	}) {
		t.Fatalf("hook phase order=%v", got)
	}
	readContext := social.WithPostReadHookObserver(ctx, trace)
	readRows, err := caller.Posts.FindMany(readContext,
		social.Posts.Where(social.Posts.ID.Eq(postID)),
		social.Posts.Select(social.Posts.ID, social.Posts.Title, social.Posts.Body),
	)
	if err != nil || len(readRows) != 1 || !valueEquals(readRows[0], social.Posts.ID, postID) {
		t.Fatalf("read-hook authorized result rows=%d error=%v", len(readRows), err)
	}
	if phases, rows := trace.readSnapshot(); fmt.Sprint(phases) != fmt.Sprint([]social.PostReadHookPhase{social.PostHookBeforeFindMany, social.PostHookAfterFindMany}) || rows != 1 {
		t.Fatalf("read-hook phases=%v authorized rows=%d", phases, rows)
	}
	postScope := social.Posts.Scope()
	scopedID, scopedTitle := social.Posts.ID.At(postScope), social.Posts.Title.At(postScope)
	scopedRows, err := caller.Posts.Scoped(ctx, golem.From(postScope).Where(scopedID.Eq(postID)).Select(scopedID, scopedTitle))
	if err != nil || len(scopedRows) != 1 || scopedAudits.Load() != 1 {
		t.Fatalf("scoped read rows=%d audits=%d error=%v", len(scopedRows), scopedAudits.Load(), err)
	}

	eventContext, cancelEvent := context.WithTimeout(ctx, 5*time.Second)
	event, err := stream.Recv(eventContext)
	cancelEvent()
	if err != nil || event.ID() != postID || event.Metadata().Action() != golem.EventCreated {
		t.Fatalf("caller event id=%s action=%v error=%v", event.ID(), event.Metadata().Action(), err)
	}

	comments, err := caller.Comments.FindMany(ctx, social.Comments.Where(social.Comments.PostID.Eq(postID)))
	if err != nil || len(comments) != 1 {
		t.Fatalf("nested comment count=%d error=%v", len(comments), err)
	}
	if _, err := caller.Comments.Update(ctx, social.Comments.ByID.Value(commentID), social.Comments.Update(
		social.Comments.Replies.Create(social.Comments.Create(
			social.Comments.ID.Create(replyID), social.Comments.PostID.Create(postID),
			social.Comments.AuthorID.Create(userID), social.Comments.Body.Create("recursive reply"),
		)),
	)); err != nil {
		t.Fatalf("recursive nested reply: %v", err)
	}
	if count, err := caller.Comments.Count(ctx, social.Comments.Where(social.Comments.ParentID.Eq(commentID))); err != nil || count != 1 {
		t.Fatalf("recursive reply count=%d error=%v", count, err)
	}

	if _, err := caller.Tags.Create(ctx, social.Tags.Create(social.Tags.ID.Create(tagID), social.Tags.Name.Create("golem"))); err != nil {
		t.Fatalf("create tag: %v", err)
	}
	if _, err := caller.PostTags.Create(ctx, social.PostTags.Create(
		social.PostTags.PostID.Create(postID), social.PostTags.TagName.Create("golem"),
	)); err != nil {
		t.Fatalf("create composite-key join: %v", err)
	}
	if _, err := caller.PostTags.FindUnique(ctx, social.PostTags.PostID_TagName.Value(postID, "golem")); err != nil {
		t.Fatalf("read composite-key selector: %v", err)
	}

	if _, err := caller.Posts.Update(ctx, social.Posts.ByID.Value(postID), social.Posts.Update(
		social.Posts.Title.Set("Updated post"), social.Posts.Views.Increment(1),
	)); err != nil {
		t.Fatalf("ordinary update: %v", err)
	}
	if count, err := caller.Posts.UpdateMany(ctx, social.Posts.ID.Eq(postID), social.Posts.UpdateMany(social.Posts.Priority.Increment(1))); err != nil || count != 1 {
		t.Fatalf("bounded update-many count=%d error=%v", count, err)
	}
	if _, err := caller.Posts.Upsert(ctx, social.Posts.ByID.Value(postID),
		social.Posts.Create(social.Posts.ID.Create(postID), social.Posts.AuthorID.Create(userID), social.Posts.Title.Create("not-created"), social.Posts.Body.Create("not-created"), social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock), social.Posts.Metadata.Create(metadata), social.Posts.Topics.Create(golem.List[string]{"unused"})),
		social.Posts.Update(social.Posts.Title.Set("Upsert updated")),
	); err != nil {
		t.Fatalf("coordinated upsert update branch: %v", err)
	}

	transactionPostID := mustUUID(t, "20000000-0000-0000-0000-000000000002")
	if err := caller.Transaction(ctx, func(tx *social.CallerTx[social.Principal]) error {
		_, err := tx.Posts.Create(ctx, minimalPostInput(t, transactionPostID, "transaction post"))
		return err
	}); err != nil {
		t.Fatalf("application transaction closure: %v", err)
	}
	rollbackID := mustUUID(t, "20000000-0000-0000-0000-000000000003")
	trace.reset()
	rollbackContext := social.WithPostHookObserver(ctx, trace)
	err = caller.Transaction(rollbackContext, func(tx *social.CallerTx[social.Principal]) error {
		if _, createErr := tx.Posts.Create(rollbackContext, minimalPostInput(t, rollbackID, "rollback post")); createErr != nil {
			return createErr
		}
		return errors.New("intentional rollback")
	})
	if err == nil {
		t.Fatal("transaction rollback unexpectedly committed")
	}
	if _, err := caller.Posts.FindUnique(ctx, social.Posts.ByID.Value(rollbackID)); err == nil {
		t.Fatal("rolled-back post is visible")
	}
	if got := trace.snapshot(); fmt.Sprint(got) != fmt.Sprint([]social.PostHookPhase{social.PostHookBeforeCreate, social.PostHookAfterCreate}) {
		t.Fatalf("rollback hook phases=%v; after-commit must be suppressed", got)
	}

	countMeasure := social.Posts.CountAll()
	aggregate, err := caller.Posts.Aggregate(ctx, social.Posts.Aggregate(social.Posts.AggregateSelect(countMeasure, social.Posts.Views.Sum())))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if count, ok := golem.AggregateValue(aggregate, countMeasure).Get(); !ok || count < 2 {
		t.Fatalf("aggregate count=%d present=%t", count, ok)
	}
	groups, err := caller.Posts.GroupBy(ctx, social.Posts.GroupBy(
		social.Posts.GroupDimensions(social.Posts.Published.Dimension()), social.Posts.GroupMeasures(countMeasure),
		social.Posts.GroupOrderBy(social.Posts.Published.Dimension().Asc()), social.Posts.GroupTake(100),
	))
	if err != nil || len(groups) == 0 {
		t.Fatalf("group-by rows=%d error=%v", len(groups), err)
	}
	relationGroups, err := caller.Posts.RelationGroupBy(ctx, social.Posts.RelationGroupBy(
		social.Posts.RelationGroupDimensions(social.Posts.AuthorHandle), social.Posts.RelationGroupMeasures(countMeasure),
		social.Posts.RelationGroupOrderBy(social.Posts.AuthorHandle.Asc()), social.Posts.RelationGroupTake(100),
	))
	if err != nil || len(relationGroups) != 1 {
		t.Fatalf("accepted relation-group rows=%d error=%v", len(relationGroups), err)
	}
	if handle, ok := golem.RelationGroupValue(relationGroups[0], social.Posts.AuthorHandle).Get(); !ok || handle != "alice" {
		t.Fatalf("relation-group author=%q present=%t", handle, ok)
	}

	searched, err := social.SearchPosts(ctx, caller, social.SearchPostsArgs{Where: social.Posts.ID.Eq(postID), Take: 10})
	if err != nil || len(searched) != 1 {
		t.Fatalf("custom query rows=%d error=%v", len(searched), err)
	}
	if _, err := social.PublishPost(ctx, caller, social.PublishPostArgs{PostID: postID, Fail: true}); err == nil {
		t.Fatal("custom transaction mutation rollback was not returned")
	}
	persisted, err := caller.Posts.FindUnique(ctx, social.Posts.ByID.Value(postID), social.Posts.Select(social.Posts.Published))
	if err != nil {
		t.Fatal(err)
	}
	if published, _ := golem.Value(persisted, social.Posts.Published).Get(); published {
		t.Fatal("failed custom transaction mutation committed")
	}
	if updated, err := social.PublishPost(ctx, caller, social.PublishPostArgs{PostID: postID}); err != nil || updated != 1 {
		t.Fatalf("custom transaction mutation count=%d error=%v", updated, err)
	}

	var graphErrorsMu sync.Mutex
	var graphErrors []string
	graph, err := application.GraphQL(social.GraphQLConfig[social.Principal]{
		PrincipalFromContext: principalFromContext,
		ReportInternalError: func(_ context.Context, err error) {
			if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionCancelled {
				graphErrorsMu.Lock()
				graphErrors = append(graphErrors, err.Error())
				graphErrorsMu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Shutdown(context.Background())
	handler := principalMiddleware(application, graph.Handler())
	response := graphqlHTTP(t, handler, bearerToken, `query Journey($id: UUID!) {
  post(where: {ID: $id}) { id title excerpt(maximum: 8) displayCode(prefix: "post:") metadata topics }
  searchPosts(where: {id: {equals: $id}}, take: 10) { id }
  aggregatePosts { count }
  relationGroupByPosts(by: [authorHandle], take: 100) { key { authorHandle } count }
  users(orderBy: [{handle: asc}]) { handle email }
}`, map[string]any{"id": postID.String()})
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL query errors=%v body=%s", response.Errors, response.Raw)
	}
	post := graphMap(t, graphMap(t, response.Data)["post"])
	if post["excerpt"] != "A comple" || post["displayCode"] != "post:"+postID.String() {
		t.Fatalf("ordinary/batched computed output=%v", post)
	}
	users := graphSlice(t, graphMap(t, response.Data)["users"])
	if len(users) != 2 || graphMap(t, users[0])["email"] != "alice@example.test" || graphMap(t, users[1])["email"] != nil {
		t.Fatalf("conditional field masking=%v", users)
	}
	ordinaryMutation := graphqlHTTP(t, handler, bearerToken, `mutation JourneyUpdate($id: UUID!) {
  updatePost(where: {ID: $id}, data: {title: {set: "GraphQL ordinary mutation"}}) { id title }
}`, map[string]any{"id": postID.String()})
	if len(ordinaryMutation.Errors) != 0 || graphMap(t, graphMap(t, ordinaryMutation.Data)["updatePost"])["title"] != "GraphQL ordinary mutation" {
		t.Fatalf("generated GraphQL ordinary mutation=%s", ordinaryMutation.Raw)
	}
	if _, err := caller.Posts.Update(ctx, social.Posts.ByID.Value(postID), social.Posts.Update(social.Posts.Published.Set(false))); err != nil {
		t.Fatal(err)
	}
	failedCustomMutation := graphqlHTTP(t, handler, bearerToken, `mutation JourneyPublish($id: UUID!) {
  publishPost(postID: $id, fail: true)
}`, map[string]any{"id": postID.String()})
	if len(failedCustomMutation.Errors) == 0 {
		t.Fatalf("GraphQL custom transaction rollback unexpectedly succeeded: %s", failedCustomMutation.Raw)
	}
	if strings.Contains(failedCustomMutation.Raw, "requested publish rollback") {
		t.Fatalf("GraphQL custom transaction leaked trusted error: %s", failedCustomMutation.Raw)
	}
	graphErrorsMu.Lock()
	reported := append([]string(nil), graphErrors...)
	graphErrorsMu.Unlock()
	if len(reported) != 1 || reported[0] != "requested publish rollback" {
		t.Fatalf("trusted GraphQL custom failure reports=%v", reported)
	}
	afterFailedCustom, err := caller.Posts.FindUnique(ctx, social.Posts.ByID.Value(postID), social.Posts.Select(social.Posts.Published))
	if err != nil {
		t.Fatal(err)
	}
	if published, _ := golem.Value(afterFailedCustom, social.Posts.Published).Get(); published {
		t.Fatal("GraphQL custom transaction failure committed")
	}
	successfulCustomMutation := graphqlHTTP(t, handler, bearerToken, `mutation JourneyPublish($id: UUID!) {
  publishPost(postID: $id, fail: false)
}`, map[string]any{"id": postID.String()})
	if len(successfulCustomMutation.Errors) != 0 || graphMap(t, successfulCustomMutation.Data)["publishPost"] != "1" {
		t.Fatalf("GraphQL custom transaction success=%s", successfulCustomMutation.Raw)
	}
	graphErrorsMu.Lock()
	reportedAfterSuccess := len(graphErrors)
	graphErrorsMu.Unlock()
	if reportedAfterSuccess != 1 {
		t.Fatalf("successful GraphQL custom mutation reported trusted errors=%d", reportedAfterSuccess)
	}

	assertGraphQLSubscription(t, graph.Handler(), caller, principal, date, clock, metadata)

	deleteID := mustUUID(t, "20000000-0000-0000-0000-000000000004")
	if _, err := caller.Posts.Upsert(ctx, social.Posts.ByID.Value(deleteID), minimalPostInput(t, deleteID, "upsert created"), social.Posts.Update(social.Posts.Title.Set("unused"))); err != nil {
		t.Fatalf("coordinated upsert create branch: %v", err)
	}
	if _, err := caller.Posts.Delete(ctx, social.Posts.ByID.Value(deleteID)); err != nil {
		t.Fatalf("ordinary delete: %v", err)
	}
	if count, err := caller.Posts.DeleteMany(ctx, social.Posts.ID.Eq(transactionPostID)); err != nil || count != 1 {
		t.Fatalf("bounded delete-many count=%d error=%v", count, err)
	}
	assertRevokedSessionSuppressesPrivateEvent(t, ctx, system, caller, sessionID)
}

func assertRevokedSessionSuppressesPrivateEvent(t *testing.T, ctx context.Context, system social.System[social.Principal], caller *social.Caller[social.Principal], sessionID golem.UUID) {
	t.Helper()
	privateID := mustUUID(t, "20000000-0000-0000-0000-000000000097")
	publicID := mustUUID(t, "20000000-0000-0000-0000-000000000098")
	stream, err := caller.Posts.Events(ctx,
		golem.EventWhere(social.Posts.ID.In(privateID, publicID)),
		golem.EventSelect[social.Post](social.Posts.ID, social.Posts.Published),
	)
	if err != nil {
		t.Fatalf("open revocation event stream: %v", err)
	}
	defer stream.Close()
	if _, err := system.Sessions.Delete(ctx, social.Sessions.ByID.Value(sessionID)); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, err := system.Posts.Create(ctx, revocationPostInput(t, privateID, false)); err != nil {
		t.Fatalf("create private post after revocation: %v", err)
	}
	if _, err := system.Posts.Create(ctx, revocationPostInput(t, publicID, true)); err != nil {
		t.Fatalf("create public post after revocation: %v", err)
	}
	receiveContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	event, err := stream.Recv(receiveContext)
	if err != nil {
		t.Fatalf("receive post-revocation public event: %v", err)
	}
	if event.ID() != publicID {
		t.Fatalf("revoked subscription received unauthorized private event %s before public event", event.ID())
	}
	quietContext, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if trailing, trailingErr := stream.Recv(quietContext); trailingErr == nil {
		t.Fatalf("revoked subscription later received unauthorized event %s", trailing.ID())
	} else if code, ok := events.CodeOf(trailingErr); !ok || code != events.CodeSubscriptionCancelled {
		t.Fatalf("revocation quiet-period error=%v code=%q", trailingErr, code)
	}
}

func revocationPostInput(t *testing.T, id golem.UUID, published bool) social.PostCreateInput {
	t.Helper()
	return social.Posts.Create(
		social.Posts.ID.Create(id),
		social.Posts.AuthorID.Create(mustUUID(t, "10000000-0000-0000-0000-000000000001")),
		social.Posts.Title.Create("revocation post"), social.Posts.Body.Create("revocation body"),
		social.Posts.Published.Create(published),
		social.Posts.LiveDate.Create(mustDate(t, "2026-08-09")), social.Posts.LiveTime.Create(mustTime(t, "13:14:15")),
		social.Posts.Metadata.Create(mustJSON(t, `{"language":"en","pinned":false}`)),
		social.Posts.Topics.Create(golem.List[string]{"revocation"}),
	)
}

func minimalPostInput(t *testing.T, id golem.UUID, title string) social.PostCreateInput {
	t.Helper()
	return social.Posts.Create(
		social.Posts.ID.Create(id), social.Posts.AuthorID.Create(mustUUID(t, "10000000-0000-0000-0000-000000000001")), social.Posts.Title.Create(title), social.Posts.Body.Create(title+" body"),
		social.Posts.LiveDate.Create(mustDate(t, "2026-08-09")), social.Posts.LiveTime.Create(mustTime(t, "13:14:15")),
		social.Posts.Metadata.Create(mustJSON(t, `{"language":"en","pinned":false}`)),
		social.Posts.Topics.Create(golem.List[string]{"journey"}),
	)
}

func assertPostScalarRoundTrip(t *testing.T, row golem.Row[social.Post], userID, postID golem.UUID, decimal golem.Decimal, date golem.Date, clock golem.Time, metadata golem.JSON[any]) {
	t.Helper()
	checks := []struct {
		name string
		ok   bool
	}{
		{"UUID", valueEquals(row, social.Posts.ID, postID) && valueEquals(row, social.Posts.AuthorID, userID)},
		{"String", valueEquals(row, social.Posts.Title, "First post")},
		{"Bool", valueEquals(row, social.Posts.Published, false)},
		{"Int16", valueEquals(row, social.Posts.Reactions, int16(7))},
		{"Int32", valueEquals(row, social.Posts.Priority, int32(11))},
		{"Int64", valueEquals(row, social.Posts.Views, int64(9_007_199_254_740_991))},
		{"Float32", valueEquals(row, social.Posts.Momentum, float32(1.25))},
		{"Float64", valueEquals(row, social.Posts.Rating, 4.75)},
		{"Decimal", valueEquals(row, social.Posts.Budget, decimal)},
		{"Date", valueEquals(row, social.Posts.LiveDate, date)},
		{"Time", valueEquals(row, social.Posts.LiveTime, clock)},
		{"JSON", jsonValueEquals(row, metadata)},
		{"Enum", valueEquals(row, social.Posts.Visibility, social.VisibilityFollowers)},
		{"ScalarList", listValueEquals(row, golem.List[string]{"go", "graphql"})},
	}
	for _, check := range checks {
		if !check.ok {
			t.Errorf("%s scalar did not round-trip", check.name)
		}
	}
	if createdAt, ok := golem.Value(row, social.Posts.CreatedAt).Get(); !ok || createdAt.IsZero() {
		t.Error("DateTime scalar did not round-trip")
	}
}

func valueEquals[M any, V comparable](row golem.Row[M], field golem.ScalarColumn[M, V], want V) bool {
	got, ok := golem.Value(row, field).Get()
	return ok && got == want
}

func jsonValueEquals(row golem.Row[social.Post], want golem.JSON[any]) bool {
	got, ok := golem.Value(row, social.Posts.Metadata).Get()
	return ok && bytes.Equal(got.Bytes(), want.Bytes())
}

func listValueEquals(row golem.Row[social.Post], want golem.List[string]) bool {
	got, ok := golem.Value(row, social.Posts.Topics).Get()
	if !ok || len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func assertGraphQLSubscription(t *testing.T, graph http.Handler, caller *social.Caller[social.Principal], principal social.Principal, date golem.Date, clock golem.Time, metadata golem.JSON[any]) {
	t.Helper()
	host := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
		graph.ServeHTTP(writer, request.WithContext(ctx))
	}))
	defer host.Close()
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	connection, _, err := dialer.Dial("ws"+host.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		t.Fatal(err)
	}
	readWSFrame(t, connection, "connection_ack")
	if err := connection.WriteJSON(map[string]any{
		"id": "journey", "type": "subscribe",
		"payload": map[string]any{"query": `subscription { postEvents { type id entity { id title } } }`},
	}); err != nil {
		t.Fatal(err)
	}
	postID := mustUUID(t, "20000000-0000-0000-0000-000000000099")
	if _, err := caller.Posts.Create(context.Background(), social.Posts.Create(
		social.Posts.ID.Create(postID), social.Posts.Title.Create("subscription post"), social.Posts.Body.Create("subscription body"),
		social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock), social.Posts.Metadata.Create(metadata),
		social.Posts.Topics.Create(golem.List[string]{"events"}),
	)); err != nil {
		t.Fatalf("create GraphQL subscription event: %v", err)
	}
	awaitGraphQLCreatedEvent(t, connection, postID)
}

func awaitGraphQLCreatedEvent(t *testing.T, connection *websocket.Conn, postID golem.UUID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = connection.SetReadDeadline(deadline)
		var frame wsFrame
		if err := connection.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type != "next" {
			t.Fatalf("GraphQL subscription terminated before target event: type=%q payload=%s", frame.Type, frame.Payload)
		}
		if bytes.Contains(frame.Payload, []byte(postID.String())) && bytes.Contains(frame.Payload, []byte(`"type":"CREATED"`)) {
			return
		}
	}
	t.Fatalf("GraphQL subscription did not deliver created event for %s", postID)
}

type wsFrame struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func readWSFrame(t *testing.T, connection *websocket.Conn, want string) wsFrame {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	var frame wsFrame
	if err := connection.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != want {
		t.Fatalf("WebSocket frame type=%q want=%q payload=%s", frame.Type, want, frame.Payload)
	}
	return frame
}

type graphResponse struct {
	Data   any              `json:"data"`
	Errors []map[string]any `json:"errors"`
	Raw    string           `json:"-"`
}

func graphqlHTTP(t *testing.T, handler http.Handler, bearer, query string, variables map[string]any) graphResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+bearer)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response graphResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GraphQL response: %v body=%s", err, recorder.Body.String())
	}
	response.Raw = recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("GraphQL status=%d body=%s", recorder.Code, response.Raw)
	}
	return response
}

func graphMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("GraphQL value is %T, want object", value)
	}
	return result
}

func graphSlice(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("GraphQL value is %T, want list", value)
	}
	return result
}

func awaitPublisher(t *testing.T, application *social.App[social.Principal]) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !application.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !application.EventCapabilities().PublisherRunning() {
		t.Fatal("event publisher did not become ready")
	}
}

func mustUUID(t *testing.T, value string) golem.UUID {
	t.Helper()
	result, err := golem.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustDecimal(t *testing.T, value string) golem.Decimal {
	t.Helper()
	result, err := golem.ParseDecimal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustDate(t *testing.T, value string) golem.Date {
	t.Helper()
	result, err := golem.ParseDate(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustTime(t *testing.T, value string) golem.Time {
	t.Helper()
	result, err := golem.ParseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustJSON(t *testing.T, value string) golem.JSON[any] {
	t.Helper()
	result, err := golem.NewJSONDocument[any]([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return result
}
