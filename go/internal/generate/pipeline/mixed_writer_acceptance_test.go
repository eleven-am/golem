package pipeline

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/p8oracle"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgres "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

// This is an ordinary-policy rollout. Static capture is present, but no caller
// binds a fact scope or uses named facts while writer3 remains in the fleet.
func TestFreshGeneratedMixedWriterProviderRolloutRollback(t *testing.T) {
	rewrite := func(source string) string {
		for _, replacement := range [][2]string{
			{"golem.WithoutGraphQL(schema)", "golem.WithoutGraphQL(schema)\n\tgolem.WithDataIncarnation(schema)"},
			{"rules.BindFactScope(MediaScopes.Libraries(LibraryScopeArgs{StorageIDs: actor.StorageIDs}))\n\tready := rules.Fact(\"ready\", golem.All[Media]())\n\trules.CanRead(ready)", "rules.CanRead(golem.All[Media]())"},
		} {
			if strings.Count(source, replacement[0]) != 1 {
				t.Fatal("mixed-writer fixture source drift")
			}
			source = strings.Replace(source, replacement[0], replacement[1], 1)
		}
		return source + `
func (Video) DefinePolicy(rules *golem.Rules[Video], _ Actor) { rules.CanRead(golem.All[Video]()) }
func (StorageObject) DefinePolicy(rules *golem.Rules[StorageObject], _ Actor) { rules.CanRead(golem.All[StorageObject]()) }
`
	}
	runGeneratedDTOConsumerArtifacts(t, "policyscopes", rewrite, mixedWriterHostSource, generatedDTOExecution{coordinate: func(t *testing.T, artifacts generatedDTOArtifacts) {
		if artifacts.Provider == compilerir.PostgreSQL {
			p8oracle.WithLocalCoreNATS(t, func(natsURL string) { runMixedWriterScenario(t, artifacts, natsURL) })
		} else {
			runMixedWriterScenario(t, artifacts, "")
		}
	}}, compilerir.SQLite, compilerir.PostgreSQL)
}

func runMixedWriterScenario(t *testing.T, artifacts generatedDTOArtifacts, natsURL string) {
	t.Helper()
	assertMixedWriterCapture(t, artifacts.Result)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var database *sqlx.DB
	var err error
	switch artifacts.Provider {
	case compilerir.SQLite:
		database, _, err = sqlite.New().Open(ctx, artifacts.DSN)
	case compilerir.PostgreSQL:
		database, _, err = postgres.New().Open(ctx, artifacts.DSN)
	default:
		t.Fatal("unsupported rollout provider")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	namespace := ""
	for _, selected := range artifacts.Result.Providers {
		if selected.Provider.Provider == artifacts.Provider {
			namespace = string(selected.Schema.System.Namespace.Name)
		}
	}
	if namespace == "" {
		t.Fatal("selected rollout system namespace missing")
	}
	quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	facts := quote(namespace) + `."_golem_outbox"`
	delivery := quote(namespace) + `."_golem_outbox_delivery"`
	assertFacts := func(want3, want4 int, pending bool) {
		t.Helper()
		var count3, count4, countPending int
		if err := database.GetContext(ctx, &count3, "SELECT count(*) FROM "+facts+" WHERE fact_version=3"); err != nil {
			t.Fatal(err)
		}
		if err := database.GetContext(ctx, &count4, "SELECT count(*) FROM "+facts+" WHERE fact_version=4"); err != nil {
			t.Fatal(err)
		}
		if err := database.GetContext(ctx, &countPending, "SELECT count(*) FROM "+delivery+" WHERE status='pending'"); err != nil {
			t.Fatal(err)
		}
		if count3 != want3 || count4 != want4 || pending && countPending != 1 {
			t.Fatalf("stored writer evidence: v3=%d v4=%d pending=%d", count3, count4, countPending)
		}
	}
	waitDelivered := func(want int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var count int
			if err := database.GetContext(ctx, &count, "SELECT count(*) FROM "+delivery+" WHERE status='delivered'"); err != nil {
				t.Fatal(err)
			}
			if count == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("durable delivery barrier=%d want=%d", count, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	prefix := fmt.Sprintf("golem_rollout_%d", time.Now().UnixNano())
	start := func(binary string) *mixedWriterHost {
		return startMixedWriterHost(t, ctx, artifacts, binary, natsURL, prefix)
	}
	a := start(artifacts.Writer3)
	a.call(t, "create?id=1")
	assertFacts(1, 0, true)
	// SQLite has one serving application process at a time and local events.
	// PostgreSQL exercises simultaneous A/B subscribers on real Core NATS.
	if artifacts.Provider == compilerir.SQLite {
		a.stop(t)
	}
	b := start(artifacts.Writer4)
	if a.identity != b.identity || a.generation != b.generation || a.pid == b.pid {
		t.Fatal("mixed hosts did not share exact identity/schema in independent processes")
	}
	b.call(t, "publisher/start")
	if artifacts.Provider == compilerir.PostgreSQL {
		a.expect(t, 1)
	}
	b.expect(t, 1)
	waitDelivered(1)
	b.call(t, "publisher/stop")
	b.call(t, "create?id=2")
	assertFacts(1, 1, true)
	if artifacts.Provider == compilerir.PostgreSQL {
		a.call(t, "publisher/start")
		a.expect(t, 2)
		b.expect(t, 2)
		waitDelivered(2)
		a.call(t, "publisher/stop")
	} else {
		b.call(t, "publisher/start")
		b.expect(t, 2)
		waitDelivered(2)
		b.call(t, "publisher/stop")
	}
	b.call(t, "create?id=3")
	assertFacts(1, 2, true)
	b.stop(t)
	if artifacts.Provider == compilerir.PostgreSQL {
		a.stop(t)
	}
	rollback := start(artifacts.Writer3)
	if rollback.pid == a.pid || rollback.pid == b.pid || rollback.identity != a.identity || rollback.generation != a.generation {
		t.Fatal("rollback reused a process or changed database/schema identity")
	}
	rollback.call(t, "publisher/start")
	rollback.expect(t, 3)
	waitDelivered(3)
	rollback.call(t, "publisher/stop")
	rollback.call(t, "create?id=4")
	assertFacts(2, 2, true)
	rollback.call(t, "publisher/start")
	rollback.expect(t, 4)
	waitDelivered(4)
	rollback.call(t, "publisher/stop")
	var delivered, blocked int
	if err := database.GetContext(ctx, &delivered, "SELECT count(*) FROM "+delivery+" WHERE status='delivered'"); err != nil {
		t.Fatal(err)
	}
	if err := database.GetContext(ctx, &blocked, "SELECT count(*) FROM "+delivery+" WHERE status<>'delivered'"); err != nil {
		t.Fatal(err)
	}
	if delivered != 4 || blocked != 0 {
		t.Fatalf("rollout lost durable delivery: delivered=%d other=%d", delivered, blocked)
	}
	rollback.stop(t)
}

// Rebuild the registry from the exact compilation bytes/digests supplied to the
// emitter; both child processes also announce that emitted generation digest.
func assertMixedWriterCapture(t *testing.T, result Result) {
	t.Helper()
	digest := func(value string) golem.SchemaDigest {
		raw, err := hex.DecodeString(value)
		if err != nil || len(raw) != 32 {
			t.Fatal("invalid emitted digest")
		}
		var d golem.SchemaDigest
		copy(d[:], raw)
		return d
	}
	model, err := compilerir.CanonicalModel(result.Compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := compilerir.CanonicalContract(result.Compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	var providers []golem.ProviderSchemaDocument
	for _, provider := range result.Providers {
		payload, err := physical.CanonicalEncode(provider.Schema)
		if err != nil {
			t.Fatal(err)
		}
		providers = append(providers, golem.GeneratedProviderSchemaDocument(golem.Provider(provider.Provider.Provider), digest(string(provider.SystemFingerprint)), golem.GeneratedSchemaDocument(provider.Schema.Version, provider.Schema.CanonicalVersion, digest(string(provider.Fingerprint)), payload)))
	}
	manifest := result.Prospective.Manifest
	bundle := golem.GeneratedSchemaBundle(digest(result.Prospective.GenerationDigest), manifest.GeneratorVersion, manifest.TemplateABIVersion,
		golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest(string(result.ModelFingerprint)), model),
		golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest(string(result.ContractFingerprint)), contract), providers...)
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if registry.PolicyCaptureFingerprint() == (golem.SchemaDigest{}) {
		t.Fatal("mixed-writer fixture has no actual static capture inventory")
	}
	for _, model := range result.Compilation.Contract.Models {
		if len(model.FactTemplates) != 0 {
			t.Fatal("mixed-writer fixture enabled named fact templates")
		}
	}
}

type mixedWriterHost struct {
	command                   *exec.Cmd
	done                      chan error
	url, identity, generation string
	pid                       int
	stopped                   bool
}

func startMixedWriterHost(t *testing.T, ctx context.Context, artifacts generatedDTOArtifacts, binary, natsURL, prefix string) *mixedWriterHost {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	command := exec.CommandContext(ctx, binary, "-test.run=^TestMixedWriterHost$", "-test.count=1", "-test.timeout=100s")
	command.Dir = artifacts.Directory
	command.Env = append(append([]string(nil), artifacts.Environment...), "GOLEM_MIXED_HOST=1", "GOLEM_OBJECT_DB="+artifacts.DSN, "GOLEM_OBJECT_PROVIDER="+string(artifacts.Provider), "GOLEM_MIXED_NATS="+natsURL, "GOLEM_MIXED_PREFIX="+prefix)
	command.ExtraFiles = []*os.File{writer}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	writer.Close()
	host := &mixedWriterHost{command: command, done: make(chan error, 1)}
	go func() { host.done <- command.Wait() }()
	t.Cleanup(func() {
		if !host.stopped {
			_ = command.Process.Kill()
			<-host.done
			host.stopped = true
		}
	})
	type announcement struct {
		URL, Identity, Generation string
		PID                       int
	}
	ready := make(chan announcement, 1)
	go func() {
		var value announcement
		_ = json.NewDecoder(io.LimitReader(reader, 4096)).Decode(&value)
		ready <- value
	}()
	select {
	case value := <-ready:
		host.url, host.identity, host.generation, host.pid = value.URL, value.Identity, value.Generation, value.PID
	case <-time.After(20 * time.Second):
		t.Fatal("mixed host readiness timed out")
	}
	if host.url == "" || host.pid != command.Process.Pid || host.identity == "" || host.generation != artifacts.Result.Prospective.GenerationDigest {
		t.Fatal("mixed host failed exact generated runtime admission/readiness")
	}
	return host
}
func (host *mixedWriterHost) call(t *testing.T, path string) string {
	t.Helper()
	client := http.Client{Timeout: 15 * time.Second}
	response, err := client.Post(host.url+"/"+path, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("mixed host %s: status=%d body=%s err=%v", path, response.StatusCode, data, err)
	}
	return strings.TrimSpace(string(data))
}
func (host *mixedWriterHost) expect(t *testing.T, id byte) {
	t.Helper()
	if got := host.call(t, "recv"); got != (golem.UUID{15: id}).String() {
		t.Fatalf("mixed event identity=%s want=%d", got, id)
	}
}
func (host *mixedWriterHost) stop(t *testing.T) {
	t.Helper()
	host.call(t, "stop")
	select {
	case err := <-host.done:
		host.stopped = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("mixed host did not close and exit")
	}
}

const mixedWriterHostSource = `package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	app "example.test/home"
	"fmt"
	"github.com/eleven-am/golem/go/events"
	eventnats "github.com/eleven-am/golem/go/events/nats"
	"github.com/eleven-am/golem/go/golem"
	providerapi "github.com/eleven-am/golem/go/provider"
	providerpg "github.com/eleven-am/golem/go/provider/postgresql"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestMixedWriterHost(t *testing.T) {
	if os.Getenv("GOLEM_MIXED_HOST") != "1" {
		t.Skip("owned mixed-fleet host only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 95*time.Second)
	defer cancel()
	var database *providerapi.Database
	var err error
	switch os.Getenv("GOLEM_OBJECT_PROVIDER") {
	case "sqlite":
		database, err = providersqlite.Open(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
	case "postgresql":
		database, err = providerpg.Open(ctx, providerpg.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
	default:
		t.Fatal("unsupported host provider")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	var transport events.EventTransport
	if database.Provider() == golem.PostgreSQL {
		broker, openErr := eventnats.Open(ctx, database, eventnats.Config{URLs: []string{os.Getenv("GOLEM_MIXED_NATS")}, SubjectPrefix: os.Getenv("GOLEM_MIXED_PREFIX"), ConnectTimeout: 2 * time.Second, FlushTimeout: 2 * time.Second, MaxInboundPayloadBytes: 2 << 20})
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer broker.Close()
		transport = broker
	} else {
		// Supported SQLite process-local transport; there is no concurrent fleet.
		transport, err = events.NewMemoryTransport(events.MemoryLimits{Buffer: 32})
		if err != nil {
			t.Fatal(err)
		}
	}
	application, err := app.Open(ctx, app.Config[string]{Database: database, EventTransport: transport,
		ResolvePrincipal: func(context.Context, string) (app.Actor, error) { return app.Actor{}, nil }, SnapshotActor: func(actor app.Actor) (app.Actor, error) {
			actor.StorageIDs = append([]golem.UUID(nil), actor.StorageIDs...)
			return actor, nil
		},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) { t.Error("unexpected operator audit") }, AfterCommitError: func(context.Context, golem.AfterCommitFailure) { t.Error("unexpected after-commit failure") }})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := application.ForPrincipal(ctx, "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Medias.FindMany(ctx); err != nil {
		t.Fatal("ordinary caller admission", err)
	}
	stream, err := caller.Medias.Events(ctx)
	if err != nil {
		t.Fatal("ordinary subscriber admission", err)
	}
	defer stream.Close()
	var stopPublisher context.CancelFunc
	var publisherDone chan error
	stop := func() error {
		if stopPublisher == nil {
			return nil
		}
		stopPublisher()
		select {
		case err := <-publisherDone:
			stopPublisher = nil
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		case <-time.After(5 * time.Second):
			return fmt.Errorf("publisher did not stop")
		}
	}
	defer func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	}()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		var err error
		switch r.URL.Path {
		case "/create":
			id, parseErr := strconv.ParseUint(r.URL.Query().Get("id"), 10, 8)
			err = parseErr
			if err == nil {
				_, err = application.System().Medias.Create(ctx, app.Medias.Create(app.Medias.ID.Create(golem.UUID{15: byte(id)})))
			}
		case "/recv":
			receive, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			value, receiveErr := stream.Recv(receive)
			err = receiveErr
			if err == nil {
				fmt.Fprint(w, value.ID().String())
				return
			}
		case "/publisher/start":
			if stopPublisher != nil {
				err = fmt.Errorf("publisher already running")
				break
			}
			var running context.Context
			running, stopPublisher = context.WithCancel(ctx)
			publisherDone = make(chan error, 1)
			go func() { publisherDone <- application.RunEventPublisher(running) }()
			deadline := time.Now().Add(5 * time.Second)
			for !application.EventCapabilities().PublisherRunning() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !application.EventCapabilities().PublisherRunning() {
				err = fmt.Errorf("publisher not ready")
			}
		case "/publisher/stop":
			err = stop()
		case "/stop":
			finished <- struct{}{}
		default:
			err = fmt.Errorf("unknown control")
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprint(w, "ok")
	})
	server := http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	if !application.DataIncarnation().Valid() {
		t.Fatal("mixed host requires valid durable data identity")
	}
	ready := os.NewFile(3, "mixed-ready")
	if ready == nil {
		t.Fatal("missing readiness pipe")
	}
	err = json.NewEncoder(ready).Encode(struct {
		URL, Identity, Generation string
		PID                       int
	}{"http://" + listener.Addr().String(), application.DataIncarnation().String(), app.GolemGeneratedSchemaBundle().GenerationDigest().String(), os.Getpid()})
	ready.Close()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Error("host parent did not stop it")
	}
	shutdown, closeShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeShutdown()
	if err := server.Shutdown(shutdown); err != nil {
		t.Error(err)
	}
	if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
		t.Error(err)
	}
}
`
