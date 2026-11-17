package pipeline

import (
	"strings"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

// The same browser client survives an actual stopped/replaced generated host.
func TestFreshGeneratedRestoreHTTP(t *testing.T) {
	runGeneratedDTOConsumerWithProviders(t, "continuationpages", func(source string) string {
		const declaration = "golem.Providers(schema, golem.SQLite, golem.PostgreSQL)"
		if strings.Count(source, declaration) != 1 {
			t.Fatal("restore fixture provider declaration changed")
		}
		return strings.Replace(source, declaration, "golem.Providers(schema, golem.SQLite)", 1)
	}, restoreConsumerTest, compilerir.SQLite)
}

const restoreConsumerTest = `package acceptance_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	app "example.test/home"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

type restorePrincipalKey struct{}
type restoreHostInfo struct {
	URL                            string
	PID                            int
	Identity, Contract, Generation string
}

func restoreOpen(ctx context.Context, path string) (*provider.Database, *app.App[app.Principal], http.Handler, restoreHostInfo, error) {
	database, err := providersqlite.Open(ctx, providersqlite.Config{DataSourceName: path})
	if err != nil {
		return nil, nil, nil, restoreHostInfo{}, err
	}
	application, err := app.Open(ctx, app.Config[app.Principal]{Database: database,
		ResolvePrincipal: func(_ context.Context, principal app.Principal) (app.Actor, error) {
			return app.Actor{Profile: string(principal)}, nil
		},
		Pages: &golemruntime.PageConfig[app.Actor]{ActiveKey: "shared-key", Keys: []golem.ContinuationKey{{ID: "shared-key", Material: [32]byte{1, 2, 3}}}, MaxAge: 10 * time.Minute,
			Scope: func(ctx context.Context, actor app.Actor) (string, error) {
				if golem.ActorFrom[app.Actor](ctx) != actor {
					return "", fmt.Errorf("scope actor is not pinned")
				}
				return fmt.Sprintf("account-1/%s/%d", actor.Profile, actor.Revision), nil
			},
		},
	})
	if err != nil {
		database.Close()
		return nil, nil, nil, restoreHostInfo{}, err
	}
	server, err := application.GraphQL(app.GraphQLConfig[app.Principal]{ReportInternalError: func(_ context.Context, err error) {
		fmt.Fprintln(os.Stderr, "restore fixture internal error:", err)
	}, PrincipalFromContext: func(ctx context.Context) (app.Principal, bool) {
		principal, ok := ctx.Value(restorePrincipalKey{}).(app.Principal)
		return principal, ok
	}})
	if err != nil {
		database.Close()
		return nil, nil, nil, restoreHostInfo{}, err
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.Handler().ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), restorePrincipalKey{}, app.Principal(r.Header.Get("x-golem-profile")))))
	})
	info := restoreHostInfo{PID: os.Getpid(), Identity: application.DataIncarnation().String(), Contract: server.ContractFingerprint().String(), Generation: app.GolemGeneratedSchemaBundle().GenerationDigest().String()}
	return database, application, handler, info, nil
}

// This child owns all application/provider handles. Its parent waits for real
// process exit, rather than interpreting a shutdown grace as fleet exclusion.
func TestRestoreHostChild(t *testing.T) {
	if os.Getenv("GOLEM_RESTORE_CHILD") != "1" {
		t.Skip("child host only")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	database, _, handler, info, err := restoreOpen(ctx, os.Getenv("GOLEM_OBJECT_DB"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	info.URL = "http://" + listener.Addr().String()
	if err := json.NewEncoder(os.Stdout).Encode(info); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	shutdown, finish := context.WithTimeout(context.Background(), 10*time.Second)
	defer finish()
	if err := server.Shutdown(shutdown); err != nil {
		t.Error(err)
	}
	if err := <-served; err != http.ErrServerClosed {
		t.Error(err)
	}
	if err := database.Close(); err != nil {
		t.Error(err)
	}
}

type restoreProcess struct {
	command *exec.Cmd
	done    chan error
	info    restoreHostInfo
	stopped bool
}

func startRestoreProcess(ctx context.Context) (*restoreProcess, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestRestoreHostChild$", "-test.timeout=2m")
	command.Env = append(os.Environ(), "GOLEM_RESTORE_CHILD=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &restoreProcess{command: command, done: make(chan error, 1)}
	go func() { process.done <- command.Wait() }()
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() {
			ready <- fmt.Errorf("host did not announce readiness: %v", scanner.Err())
			return
		}
		ready <- json.Unmarshal(scanner.Bytes(), &process.info)
	}()
	select {
	case err := <-ready:
		if err == nil && process.info.PID == command.Process.Pid && process.info.Identity != "" {
			return process, nil
		}
		command.Process.Kill()
		<-process.done
		return nil, fmt.Errorf("host readiness: %v", err)
	case <-time.After(15 * time.Second):
		command.Process.Kill()
		<-process.done
		return nil, fmt.Errorf("host startup timed out")
	}
}
func (process *restoreProcess) stop() error {
	if process == nil || process.stopped {
		return nil
	}
	process.stopped = true
	if err := process.command.Process.Signal(os.Interrupt); err != nil {
		process.command.Process.Kill()
		<-process.done
		return err
	}
	select {
	case err := <-process.done:
		return err
	case <-time.After(15 * time.Second):
		process.command.Process.Kill()
		<-process.done
		return fmt.Errorf("host did not exit cleanly")
	}
}

func TestRestoreBrowserBoundary(t *testing.T) {
	if os.Getenv("GOLEM_RESTORE_CHILD") == "1" {
		t.Skip("parent only")
	}
	clientRoot := os.Getenv("GOLEM_GENERATED_CLIENT_ROOT")
	if clientRoot == "" {
		t.Skip("requires explicit generated client root")
	}
	if !filepath.IsAbs(clientRoot) {
		t.Fatal("client root must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	path := strings.TrimPrefix(os.Getenv("GOLEM_OBJECT_DB"), "file:")
	database, application, _, _, err := restoreOpen(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(n int) error {
		_, err := application.System().Entries.Create(ctx, app.Entries.Create(app.Entries.ID.Create(app.CandidateID(uint32(n))), app.Entries.Ordinal.Create(int32(n)), app.Entries.Rank.Create(int32((n-1)/5)), app.Entries.Even.Create(n%2 == 0), app.Entries.Visible.Create(true), app.Entries.Title.Create(fmt.Sprintf("entry-%02d", n))))
		return err
	}
	for n := 1; n <= 60; n++ {
		if err := seed(n); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "archive.db")
	bundle := app.GolemGeneratedSchemaBundle()
	if err := providersqlite.Snapshot(ctx, database, bundle, archive); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := seed(99); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	child, err := startRestoreProcess(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var processMu sync.Mutex
	defer func() {
		processMu.Lock()
		defer processMu.Unlock()
		if err := child.stop(); err != nil {
			t.Error(err)
		}
	}()
	original := child.info
	var target atomic.Pointer[url.URL]
	parsed, err := url.Parse(original.URL)
	if err != nil {
		t.Fatal(err)
	}
	target.Store(parsed)
	proxy := &httputil.ReverseProxy{Director: func(r *http.Request) {
		host := target.Load()
		r.URL.Scheme = host.Scheme
		r.URL.Host = host.Host
		r.Host = host.Host
	}}
	var restored bool
	var controlCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test/restore" {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		processMu.Lock()
		defer processMu.Unlock()
		if restored {
			http.Error(w, "already restored", 409)
			return
		}
		controlCalls.Add(1)
		fail := func(err error) { http.Error(w, err.Error(), 500) }
		// Real stop and Wait complete before PrepareRestore touches the old path.
		if err := child.stop(); err != nil {
			fail(err)
			return
		}
		session, err := providersqlite.PrepareRestore(ctx, providersqlite.RestoreConfig{Archive: archive, Destination: path, ArchiveBundle: bundle, TargetBundle: bundle, Mode: providersqlite.RestoreResumeAtLeastOnce})
		if err != nil {
			fail(err)
			return
		}
		defer session.Close()
		if err := session.Database().Close(); err != nil {
			fail(err)
			return
		}
		identity, err := session.Publish(ctx)
		if err != nil {
			fail(err)
			return
		}
		replacement, err := startRestoreProcess(ctx)
		if err != nil {
			fail(err)
			return
		}
		child = replacement
		if child.info.PID == original.PID || child.info.Identity == original.Identity || child.info.Identity != identity.String() || child.info.Contract != original.Contract || child.info.Generation != original.Generation {
			fail(fmt.Errorf("host replacement identity/fingerprint invariant failed"))
			return
		}
		next, err := url.Parse(child.info.URL)
		if err != nil {
			fail(err)
			return
		}
		target.Store(next)
		restored = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"before": original, "after": child.info, "oldExited": true})
	}))
	defer server.Close()
	manifest, err := filepath.Abs("zz_golem_client.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	nodeCtx, finish := context.WithTimeout(ctx, 45*time.Second)
	defer finish()
	command := exec.CommandContext(nodeCtx, "node", filepath.Join(clientRoot, "node_modules/vitest/vitest.mjs"), "run", "--config", "vitest.config.ts")
	command.Dir = filepath.Join(clientRoot, "core")
	command.Env = append(os.Environ(), "GOLEM_GENERATED_HTTP_URL="+server.URL, "GOLEM_GENERATED_HTTP_MANIFEST="+manifest, "GOLEM_GENERATED_CLIENT_FIXTURE=restore", "NODE_OPTIONS=--max-old-space-size=256", "UV_THREADPOOL_SIZE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restore browser failed: %v\n%s", err, output)
	}
	processMu.Lock()
	err = child.stop()
	processMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !restored || controlCalls.Load() != 1 {
		t.Fatal("browser did not perform exactly one restore")
	}
	// A fresh generated app reads the actual restored disk after both child
	// processes exit; neither the live-only row nor the held write survived.
	check, final, _, info, err := restoreOpen(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	for _, n := range []uint32{99, 100} {
		rows, err := final.System().Entries.FindMany(ctx, golem.Where(app.Entries.ID.Eq(app.CandidateID(n))), golem.Take[app.Entry](1))
		if err != nil || len(rows) != 0 {
			t.Fatalf("restored row %d survived: %d %v", n, len(rows), err)
		}
	}
	rows, err := final.System().Entries.FindMany(ctx, golem.Take[app.Entry](100))
	if err != nil || len(rows) != 60 {
		t.Fatalf("restored seed rows=%d err=%v", len(rows), err)
	}
	if info.Identity != child.info.Identity {
		t.Fatal("final reopen changed incarnation")
	}
}
`
