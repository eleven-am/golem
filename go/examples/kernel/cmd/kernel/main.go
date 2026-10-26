package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/kernel/kernel"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

const digestJobType = "kernel.note_digest"

type digestPayload struct {
	Title string `json:"title"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	database, err := openDatabase(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if err := runScenario(ctx, database); err != nil {
		log.Fatal(err)
	}
	fmt.Println("kernel scenario passed")
}

func openDatabase(ctx context.Context) (*provider.Database, error) {
	dsn := os.Getenv("GOLEM_DATABASE_DSN")
	if dsn == "" {
		return nil, errors.New("GOLEM_DATABASE_DSN is required")
	}
	switch os.Getenv("GOLEM_PROVIDER") {
	case "", "sqlite":
		return sqlite.Open(ctx, sqlite.Config{DataSourceName: dsn})
	case "postgresql":
		return postgresql.Open(ctx, postgresql.Config{DataSourceName: dsn})
	default:
		return nil, errors.New("GOLEM_PROVIDER must be sqlite or postgresql")
	}
}

func openApplication(ctx context.Context, database *provider.Database, transport events.EventTransport, registry *queue.Registry) (*kernel.App[kernel.Principal], error) {
	return kernel.Open(ctx, kernel.Config[kernel.Principal]{
		Database:       database,
		EventTransport: transport,
		Queue: &golemruntime.QueueConfig{
			Registry: registry,
			Limits:   queue.Limits{Concurrency: 1, ClaimBatch: 4, LeaseDuration: time.Second, PollInterval: 10 * time.Millisecond, ShutdownGrace: time.Second},
		},
		ResolvePrincipal: func(_ context.Context, principal kernel.Principal) (kernel.Actor, error) {
			return kernel.Actor{AccountID: principal.AccountID}, nil
		},
		SnapshotPrincipal:   func(principal kernel.Principal) (kernel.Principal, error) { return principal, nil },
		SnapshotActor:       func(actor kernel.Actor) (kernel.Actor, error) { return actor, nil },
		AuditPrincipal:      func(principal kernel.Principal) string { return principal.AccountID.String() },
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
	})
}

func runScenario(ctx context.Context, database *provider.Database) error {
	handled := make(chan string, 4)
	registry := queue.NewRegistry()
	digest, err := queue.Register(registry, queue.Definition[digestPayload]{
		Type: digestJobType,
		Handle: func(_ context.Context, job queue.Job[digestPayload]) error {
			handled <- job.Payload.Title
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("register queue job: %w", err)
	}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 64})
	if err != nil {
		return err
	}
	application, err := openApplication(ctx, database, transport, registry)
	if err != nil {
		return fmt.Errorf("open application: %w", err)
	}
	publisherContext, stopPublisher := context.WithCancel(ctx)
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- application.RunEventPublisher(publisherContext) }()
	defer func() {
		stopPublisher()
		<-publisherDone
	}()
	if err := awaitPublisher(ctx, application); err != nil {
		return err
	}

	system := application.System()
	aliceID, bobID := identity(1), identity(2)
	if err := createAccount(ctx, system, aliceID, "alice"); err != nil {
		return err
	}
	if err := createAccount(ctx, system, bobID, "bob"); err != nil {
		return err
	}
	alice, err := application.ForPrincipal(ctx, kernel.Principal{AccountID: aliceID})
	if err != nil {
		return fmt.Errorf("alice caller: %w", err)
	}
	bob, err := application.ForPrincipal(ctx, kernel.Principal{AccountID: bobID})
	if err != nil {
		return fmt.Errorf("bob caller: %w", err)
	}
	stream, err := alice.Notes.Events(ctx)
	if err != nil {
		return fmt.Errorf("subscribe to alice's notes: %w", err)
	}
	defer func() { _ = stream.Close() }()

	draftID, keptID := identity(11), identity(12)
	if _, err := alice.Notes.Create(ctx, kernel.Notes.Create(kernel.Notes.ID.Create(draftID), kernel.Notes.Title.Create("draft"))); err != nil {
		return fmt.Errorf("alice creates a note: %w", err)
	}
	draft, err := alice.Notes.FindUnique(ctx, kernel.Notes.ByID.Value(draftID))
	if err != nil {
		return fmt.Errorf("alice reads her note: %w", err)
	}
	if owner, _ := golem.Value(draft, kernel.Notes.OwnerID).Get(); owner != aliceID {
		return fmt.Errorf("hook-owned owner = %s, want %s", owner, aliceID)
	}
	if rows, err := alice.Notes.FindMany(ctx); err != nil || len(rows) != 1 {
		return fmt.Errorf("alice reads %d notes: %v", len(rows), err)
	}
	if rows, err := bob.Notes.FindMany(ctx); err != nil || len(rows) != 0 {
		return fmt.Errorf("bob reads %d of alice's notes: %v", len(rows), err)
	}

	if updated, err := alice.Notes.UpdateMany(ctx, kernel.Notes.ID.Eq(draftID), kernel.Notes.UpdateMany(kernel.Notes.Archived.Set(true))); err == nil {
		return fmt.Errorf("archive outside the Within grant was accepted and updated %d notes", updated)
	}
	archived, err := kernel.Mutate(ctx, alice, kernel.ArchiveNotes, kernel.ArchiveNotesArgs{Title: "draft"})
	if err != nil || archived != 1 {
		return fmt.Errorf("archive inside the Within grant updated %d notes: %v", archived, err)
	}
	if archived, err := kernel.Mutate(ctx, bob, kernel.ArchiveNotes, kernel.ArchiveNotesArgs{Title: "draft"}); err != nil || archived != 0 {
		return fmt.Errorf("bob archived %d of alice's notes: %v", archived, err)
	}
	row, err := alice.Notes.FindUnique(ctx, kernel.Notes.ByID.Value(draftID))
	if err != nil {
		return fmt.Errorf("reread archived note: %w", err)
	}
	if value, _ := golem.Value(row, kernel.Notes.Archived).Get(); !value {
		return errors.New("Mutate did not archive the note")
	}
	if err := expectEvents(ctx, stream, []string{"created " + draftID.String(), "updated " + draftID.String()}); err != nil {
		return err
	}

	rollback := errors.New("rollback")
	err = alice.Transaction(ctx, func(transaction *kernel.CallerTx[kernel.Principal]) error {
		if _, err := transaction.Notes.Create(ctx, kernel.Notes.Create(kernel.Notes.ID.Create(identity(13)), kernel.Notes.Title.Create("discarded"))); err != nil {
			return err
		}
		pending, err := digest.New(digestPayload{Title: "discarded"})
		if err != nil {
			return err
		}
		if _, err := transaction.Enqueue(ctx, pending); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		return fmt.Errorf("rolled-back transaction: %v", err)
	}
	err = alice.Transaction(ctx, func(transaction *kernel.CallerTx[kernel.Principal]) error {
		if _, err := transaction.Notes.Create(ctx, kernel.Notes.Create(kernel.Notes.ID.Create(keptID), kernel.Notes.Title.Create("kept"))); err != nil {
			return err
		}
		pending, err := digest.New(digestPayload{Title: "kept"})
		if err != nil {
			return err
		}
		_, err = transaction.Enqueue(ctx, pending)
		return err
	})
	if err != nil {
		return fmt.Errorf("committed transaction: %w", err)
	}
	if count, err := system.Notes.Count(ctx); err != nil || count != 2 {
		return fmt.Errorf("system counts %d notes after the transactions: %v", count, err)
	}
	if err := expectEvents(ctx, stream, []string{"created " + keptID.String()}); err != nil {
		return err
	}
	if _, err := alice.Notes.Delete(ctx, kernel.Notes.ByID.Value(keptID)); err != nil {
		return fmt.Errorf("alice deletes a note: %w", err)
	}
	if count, err := system.Notes.Count(ctx); err != nil || count != 1 {
		return fmt.Errorf("system counts %d notes after the delete: %v", count, err)
	}

	if err := expectEvents(ctx, stream, []string{"deleted " + keptID.String()}); err != nil {
		return err
	}

	workerContext, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- application.RunQueueWorker(workerContext) }()
	var title string
	select {
	case title = <-handled:
	case <-time.After(20 * time.Second):
		stopWorker()
		<-workerDone
		return errors.New("queue worker handled no job")
	}
	stopWorker()
	if err := <-workerDone; err != nil {
		return fmt.Errorf("queue worker: %w", err)
	}
	if title != "kept" || len(handled) != 0 {
		return fmt.Errorf("queue worker handled %q with %d more pending", title, len(handled))
	}
	return nil
}

func identity(value byte) golem.UUID {
	return golem.NewUUID([16]byte{0x6b, 0x65, 0x72, 0x6e, 0x65, 0x6c, 0x40, 0x00, 0x80, 15: value})
}

func createAccount(ctx context.Context, system kernel.System[kernel.Principal], identity golem.UUID, handle string) error {
	if _, err := system.Accounts.Create(ctx, kernel.Accounts.Create(kernel.Accounts.ID.Create(identity), kernel.Accounts.Handle.Create(handle))); err != nil {
		return fmt.Errorf("system creates %s: %w", handle, err)
	}
	return nil
}

func awaitPublisher(ctx context.Context, application *kernel.App[kernel.Principal]) error {
	deadline := time.Now().Add(5 * time.Second)
	for !application.EventCapabilities().PublisherRunning() {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return errors.New("event publisher did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

func expectEvents(ctx context.Context, stream golem.EventStream[kernel.NoteEvent], want []string) error {
	got := make([]string, 0, len(want))
	for range want {
		receive, cancel := context.WithTimeout(ctx, 5*time.Second)
		event, err := stream.Recv(receive)
		cancel()
		if err != nil {
			return fmt.Errorf("receive note event after %v: %w", got, err)
		}
		action := "updated"
		switch event.Metadata().Action() {
		case golem.EventCreated:
			action = "created"
		case golem.EventDeleted:
			action = "deleted"
		}
		got = append(got, action+" "+event.ID().String())
	}
	sort.Strings(got)
	expected := append([]string(nil), want...)
	sort.Strings(expected)
	if strings.Join(got, ",") != strings.Join(expected, ",") {
		return fmt.Errorf("note events = %v, want %v", got, expected)
	}
	return nil
}
