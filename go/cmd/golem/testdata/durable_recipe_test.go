package durable

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

type principalKey struct{}

type recipeFailure func(context.Context, *SystemTx[Principal], queue.Failure[ExportPayload]) error

func recipeOwner(number byte) golem.UUID {
	return golem.NewUUID([16]byte{number})
}

func recipeDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("pgx", os.Getenv("DURABLE_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func openRecipe(t *testing.T, onFailed recipeFailure) *App[Principal] {
	t.Helper()
	ctx := context.Background()
	database, err := postgresql.Open(ctx, postgresql.Config{DataSourceName: os.Getenv("DURABLE_DSN")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	var application *App[Principal]
	registry := queue.NewRegistry()
	ExportJob, err = queue.Register(registry, queue.Definition[ExportPayload]{
		Type:        "export",
		MaxAttempts: 1,
		Timeout:     30 * time.Second,
		Backoff:     queue.Backoff{Base: 10 * time.Millisecond, Cap: 50 * time.Millisecond},
		Handle: func(ctx context.Context, job queue.Job[ExportPayload]) error {
			return RunExport(ctx, application.System(), job.Payload)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err = Open(ctx, Config[Principal]{
		Database:            database,
		EventTransport:      transport,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		Queue:               &golemruntime.QueueConfig{Registry: registry},
		QueueFailed:         []golemruntime.JobFailed[Principal, Actor]{OnJobFailed(ExportJob, onFailed)},
		ResolvePrincipal: func(_ context.Context, principal Principal) (Actor, error) {
			return Actor{UserID: principal.UserID, Authenticated: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func recipeCaller(t *testing.T, application *App[Principal], owner golem.UUID) *Caller[Principal] {
	t.Helper()
	caller, err := application.ForPrincipal(context.Background(), Principal{UserID: owner})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func lockWaiters(t *testing.T, database *sql.DB) int {
	t.Helper()
	var waiting int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pg_catalog.pg_stat_activity WHERE datname = pg_catalog.current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	return waiting
}

func runCancel(caller *Caller[Principal], id string) <-chan cancelOutcome {
	outcome := make(chan cancelOutcome, 1)
	go func() {
		handle, err := Mutate(context.Background(), caller, CancelExport, CancelExportArgs{ID: id})
		outcome <- cancelOutcome{handle: handle, err: err}
	}()
	return outcome
}

type cancelOutcome struct {
	handle golem.Row[Export]
	err    error
}

func TestCancelTakesTheJobRowBeforeTheStatusRow(t *testing.T) {
	ctx := context.Background()
	inside := make(chan struct{})
	gate := make(chan struct{})
	var insideOnce, gateOnce sync.Once
	open := func() { gateOnce.Do(func() { close(gate) }) }
	application := openRecipe(t, func(ctx context.Context, tx *SystemTx[Principal], failure queue.Failure[ExportPayload]) error {
		insideOnce.Do(func() { close(inside) })
		<-gate
		return MarkExportFailed(ctx, tx, failure)
	})
	caller := recipeCaller(t, application, recipeOwner(1))
	database := recipeDatabase(t)
	if _, err := Mutate(ctx, caller, RequestExport, RequestExportArgs{ID: "failing", Fail: true}); err != nil {
		t.Fatal(err)
	}
	worker, stop := context.WithCancel(ctx)
	t.Cleanup(stop)
	go application.RunQueueWorker(worker)
	t.Cleanup(open)
	<-inside

	outcome := runCancel(caller, "failing")
	eventually(t, "the cancel to wait on the job row", func() bool { return lockWaiters(t, database) > 0 })

	probe, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, probeErr := probe.ExecContext(ctx, `SELECT 1 FROM exports WHERE id = $1 FOR UPDATE NOWAIT`, "failing")
	if err := probe.Rollback(); err != nil {
		t.Fatal(err)
	}
	if probeErr != nil {
		t.Fatalf("the cancel holds the status row while it waits for the job row: %v", probeErr)
	}
	open()

	result := <-outcome
	if result.err != nil {
		t.Fatalf("cancelExport: %v", result.err)
	}
	row, err := caller.Exports.FindUnique(ctx, Exports.ByID.Value("failing"))
	if err != nil {
		t.Fatal(err)
	}
	status, _ := golem.Value(row, Exports.Status).Get()
	code, _ := golem.Value(row, Exports.ErrorCode).Get()
	jobID, _ := golem.Value(row, Exports.JobID).Get()
	if status != StatusFailed || code != string(queue.FailureAttemptsExhausted) {
		t.Fatalf("status=%s code=%s", status, code)
	}
	inspected, err := application.QueueOperator().Inspect(ctx, queue.JobID(jobID))
	if err != nil {
		t.Fatal(err)
	}
	if inspected.State != queue.StateFailed {
		t.Fatalf("job state=%s", inspected.State)
	}
}

func TestCancelGuardsOnTheObservedJob(t *testing.T) {
	ctx := context.Background()
	application := openRecipe(t, MarkExportFailed)
	caller := recipeCaller(t, application, recipeOwner(1))
	database := recipeDatabase(t)
	if _, err := Mutate(ctx, caller, RequestExport, RequestExportArgs{ID: "guarded"}); err != nil {
		t.Fatal(err)
	}

	gate, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Rollback() })
	if _, err := gate.ExecContext(ctx, `SELECT 1 FROM exports WHERE id = $1 FOR UPDATE`, "guarded"); err != nil {
		t.Fatal(err)
	}
	outcome := runCancel(caller, "guarded")
	eventually(t, "the cancel to wait on the status row", func() bool { return lockWaiters(t, database) > 0 })
	if _, err := gate.ExecContext(ctx, `UPDATE exports SET job_id = $2, status = 'queued' WHERE id = $1`, "guarded", "job-b"); err != nil {
		t.Fatal(err)
	}
	if err := gate.Commit(); err != nil {
		t.Fatal(err)
	}

	result := <-outcome
	if result.err != nil {
		t.Fatalf("cancelExport: %v", result.err)
	}
	status, _ := golem.Value(result.handle, Exports.Status).Get()
	jobID, _ := golem.Value(result.handle, Exports.JobID).Get()
	if status != StatusQueued || jobID != "job-b" {
		t.Fatalf("the cancel overwrote a replaced job: status=%s job=%s", status, jobID)
	}
}

func TestRecipeOverGraphQL(t *testing.T) {
	application := openRecipe(t, MarkExportFailed)
	server, err := application.GraphQL(GraphQLConfig[Principal]{
		PrincipalFromContext: func(ctx context.Context) (Principal, bool) {
			principal, ok := ctx.Value(principalKey{}).(Principal)
			return principal, ok
		},
		ReportInternalError: func(_ context.Context, err error) {
			t.Errorf("graphql internal error: %v", err)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Shutdown(context.Background()) })
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner := recipeOwner(1)
		if r.Header.Get("X-Owner") == "2" {
			owner = recipeOwner(2)
		}
		server.Handler().ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, Principal{UserID: owner})))
	}))
	t.Cleanup(web.Close)

	post := func(owner, query string) (map[string]any, []any) {
		t.Helper()
		body, err := json.Marshal(map[string]string{"query": query})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, web.URL, strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Owner", owner)
		response, err := web.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Data   map[string]any `json:"data"`
			Errors []any          `json:"errors"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("response %s: %v", raw, err)
		}
		return decoded.Data, decoded.Errors
	}

	data, problems := post("1", `mutation { requestExport(id: "gql", fail: false) { id } }`)
	if len(problems) != 0 || data["requestExport"].(map[string]any)["id"] != "gql" {
		t.Fatalf("requestExport data=%v errors=%v", data, problems)
	}
	data, problems = post("2", `mutation { cancelExport(id: "gql") { id status } }`)
	if len(problems) == 0 || data["cancelExport"] != nil {
		t.Fatalf("a second owner cancelled the export: data=%v errors=%v", data, problems)
	}
	data, problems = post("1", `mutation { cancelExport(id: "gql") { id status } }`)
	if len(problems) != 0 {
		t.Fatalf("cancelExport errors=%v", problems)
	}
	cancelled := data["cancelExport"].(map[string]any)
	if cancelled["id"] != "gql" || cancelled["status"] != "StatusCanceled" {
		t.Fatalf("cancelExport returned %v", cancelled)
	}
}
