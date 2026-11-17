package pipeline

import (
	"os"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestFreshGeneratedCustomInvalidationHTTP(t *testing.T) {
	if os.Getenv("GOLEM_GENERATED_CLIENT_ROOT") == "" || os.Getenv("GOLEM_STAGED_FACT_WRITER") != "4" {
		t.Skip("requires generated client root and explicit staged V4 writer rehearsal")
	}
	runGeneratedDTOConsumerWithProviders(t, "custominvalidation", nil, customInvalidationConsumerTest, compilerir.SQLite, compilerir.PostgreSQL)
}

const customInvalidationConsumerTest = `package acceptance_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	app "example.test/home"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/provider"
	providerpostgres "github.com/eleven-am/golem/go/provider/postgresql"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

type invalidationPrincipalKey struct{}

type invalidationPublisherObserver struct{ acknowledged atomic.Int64 }

func (observer *invalidationPublisherObserver) ObserveGolem(_ context.Context, value observe.Observation) {
	if value.Operation() == observe.OperationEventPublisherAcknowledge && value.Outcome() == observe.OutcomeSuccess {
		observer.acknowledged.Add(value.AggregateCount())
	}
}

func fixtureID(n uint32) golem.UUID {
	var id golem.UUID
	binary.BigEndian.PutUint32(id[12:], n)
	return id
}

func TestCustomInvalidation(t *testing.T) {
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
	memory, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	var revision atomic.Int32
	var switched atomic.Bool
	var reports atomic.Int64
	var publisherObserver invalidationPublisherObserver
	application, err := app.Open(ctx, app.Config[app.Principal]{
		Database: database, EventTransport: memory, Observer: &publisherObserver,
		EventLimits:         events.Limits{PublisherConcurrency: 1, EvaluationConcurrency: 1, RevalidateEvery: time.Second, RetentionEvery: events.RetentionDisabled},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		ResolvePrincipal: func(_ context.Context, principal app.Principal) (app.Actor, error) {
			account := fixtureID(101)
			switch principal {
			case "a":
				if switched.Load() {
					account = fixtureID(102)
				}
			case "b":
				account = fixtureID(102)
			case "deny":
			default:
				return app.Actor{}, fmt.Errorf("unknown fixture principal")
			}
			return app.Actor{AccountID: account, Revision: revision.Load(), AllowProgress: principal != "deny"}, nil
		},
		Pages: &golemruntime.PageConfig[app.Actor]{ActiveKey: "shared", Keys: []golem.ContinuationKey{{ID: "shared", Material: [32]byte{1, 2, 3}}}, MaxAge: 10 * time.Minute, Scope: func(_ context.Context, actor app.Actor) (string, error) {
			return fmt.Sprintf("%s/%d", actor.AccountID.String(), actor.Revision), nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	system := application.System()
	for index := uint32(1); index <= 2; index++ {
		account := fixtureID(100 + index)
		if _, err := system.States.Create(ctx, app.States.Create(app.States.ID.Create(fixtureID(200+index)), app.States.AccountID.Create(account), app.States.Revision.Create(0))); err != nil {
			t.Fatal(err)
		}
		if _, err := system.Progresses.Create(ctx, app.Progresses.Create(app.Progresses.ID.Create(fixtureID(300+index)), app.Progresses.AccountID.Create(account), app.Progresses.Category.Create("watch"), app.Progresses.Position.Create(0), app.Progresses.Noise.Create("seed"))); err != nil {
			t.Fatal(err)
		}
		if _, err := system.Owners.Create(ctx, app.Owners.Create(app.Owners.ID.Create(fixtureID(400+index)), app.Owners.AccountID.Create(account), app.Owners.Ready.Create(false))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := system.Progresses.Create(ctx, app.Progresses.Create(app.Progresses.ID.Create(fixtureID(303)), app.Progresses.AccountID.Create(fixtureID(101)), app.Progresses.Category.Create("other"), app.Progresses.Position.Create(0), app.Progresses.Noise.Create("seed"))); err != nil {
		t.Fatal(err)
	}
	caller, err := application.ForPrincipal(ctx, app.Principal("a"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = caller.Owners.FindMany(ctx, golem.Take[app.Owner](1))
	var refusal *golem.Error
	if !errors.As(err, &refusal) || refusal.Code != golem.CodeForbidden {
		t.Fatalf("fact owner unexpectedly readable: %v", err)
	}
	graph, err := application.GraphQL(app.GraphQLConfig[app.Principal]{PrincipalFromContext: func(ctx context.Context) (app.Principal, bool) {
		p, ok := ctx.Value(invalidationPrincipalKey{}).(app.Principal)
		return p, ok
	}, ReportInternalError: func(_ context.Context, err error) { reports.Add(1); t.Errorf("trusted GraphQL failure: %v", err) }})
	if err != nil {
		t.Fatal(err)
	}
	publisherCtx, stopPublisher := context.WithCancel(ctx)
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- application.RunEventPublisher(publisherCtx) }()
	t.Cleanup(func() {
		stopPublisher()
		select {
		case err := <-publisherDone:
			if err != nil {
				t.Errorf("publisher: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("publisher did not stop")
		}
	})
	// Seed creates must be delivered before admitting any live observer; their
	// notices are unrelated to the mutations asserted by the client below.
	seedDeadline := time.Now().Add(5 * time.Second)
	for publisherObserver.acknowledged.Load() < 5 {
		if time.Now().After(seedDeadline) {
			t.Fatal("seed event publisher did not drain")
		}
		time.Sleep(5 * time.Millisecond)
	}

	type change struct {
		Kind, Account, Category string
		Revision, Position      int32
		Ready                   bool
	}
	apply := func(ctx context.Context, c change) error {
		index := uint32(1)
		if c.Account == "b" {
			index = 2
		} else if c.Account != "a" {
			return fmt.Errorf("unknown fixture account")
		}
		progress := fixtureID(300 + index)
		if c.Category == "other" {
			if index != 1 {
				return fmt.Errorf("unknown category")
			}
			progress = fixtureID(303)
		}
		return system.Transaction(ctx, func(tx *app.SystemTx[app.Principal]) error {
			switch c.Kind {
			case "position":
				if _, err := tx.Progresses.Update(ctx, app.Progresses.ByID.Value(progress), app.Progresses.Update(app.Progresses.Position.Set(c.Position))); err != nil {
					return err
				}
			case "noise":
				_, err := tx.Progresses.Update(ctx, app.Progresses.ByID.Value(progress), app.Progresses.Update(app.Progresses.Noise.Set(fmt.Sprint(c.Revision))))
				return err
			case "owner":
				if _, err := tx.Owners.Update(ctx, app.Owners.ByID.Value(fixtureID(400+index)), app.Owners.Update(app.Owners.Ready.Set(c.Ready))); err != nil {
					return err
				}
			case "reparent":
				if _, err := tx.Progresses.Update(ctx, app.Progresses.ByID.Value(progress), app.Progresses.Update(app.Progresses.AccountID.Set(fixtureID(102)))); err != nil {
					return err
				}
			case "delete":
				if _, err := tx.Progresses.Delete(ctx, app.Progresses.ByID.Value(progress)); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown fixture change")
			}
			_, err := tx.States.Update(ctx, app.States.ByID.Value(fixtureID(200+index)), app.States.Update(app.States.Revision.Set(c.Revision)))
			return err
		})
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/test/change":
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", 405)
				return
			}
			var c change
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&c); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if err := apply(r.Context(), c); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.WriteHeader(204)
		case "/test/profile":
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", 405)
				return
			}
			switched.Store(r.URL.Query().Get("account") == "b")
			revision.Add(1)
			w.WriteHeader(204)
		case "/test/stats":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int64{"resolvers": app.ResolverCalls.Load(), "binders": app.BinderCalls.Load(), "reports": reports.Load()})
		default:
			requestCtx, cancel := context.WithTimeout(r.Context(), time.Minute)
			defer cancel()
			requestCtx = golem.RuntimeContextWithActor(requestCtx, app.Actor{AccountID: fixtureID(999), AllowProgress: true})
			requestCtx = context.WithValue(requestCtx, invalidationPrincipalKey{}, app.Principal(r.Header.Get("x-golem-profile")))
			graph.Handler().ServeHTTP(w, r.WithContext(requestCtx))
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	clientRoot := os.Getenv("GOLEM_GENERATED_CLIENT_ROOT")
	if !filepath.IsAbs(clientRoot) {
		t.Fatal("absolute generated client root required")
	}
	manifest, err := filepath.Abs("zz_golem_client.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	commandCtx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, "node", filepath.Join(clientRoot, "node_modules/vitest/vitest.mjs"), "run", "--config", "vitest.config.ts")
	command.Dir = filepath.Join(clientRoot, "core")
	command.Env = append(os.Environ(), "GOLEM_GENERATED_HTTP_URL="+server.URL, "GOLEM_GENERATED_HTTP_MANIFEST="+manifest, "GOLEM_GENERATED_CLIENT_FIXTURE=invalidation", "NODE_OPTIONS=--max-old-space-size=256", "UV_THREADPOOL_SIZE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("real invalidation client failed: %v\n%s", err, output)
	}
	if reports.Load() != 0 {
		t.Fatalf("unexpected trusted GraphQL failures: %d", reports.Load())
	}
}
`
