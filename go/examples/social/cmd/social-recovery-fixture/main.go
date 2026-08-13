// Command social-recovery-fixture is the deterministic backup/restore canary
// used by the production runbook. It uses generated clients for application
// rows. Its explicitly unsafe SQL reads inspect only Golem's managed migration
// and outbox evidence; they never implement ordinary application CRUD.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

const (
	recoveryUserID = "80000000-0000-0000-0000-000000000001"
	recoveryPostID = "80000000-0000-0000-0000-000000000002"
	recoveryHandle = "p8-recovery-canary"
	recoveryTitle  = "P8 recovery canary post"
)

type recoverySnapshot struct {
	Canary     recoveryCanary      `json:"canary"`
	Migrations []recoveryMigration `json:"migrations"`
	Facts      []recoveryFact      `json:"facts"`
	Deliveries []recoveryDelivery  `json:"deliveries"`
}

type recoveryCanary struct {
	UserID    string `json:"userID"`
	Handle    string `json:"handle"`
	PostID    string `json:"postID"`
	Title     string `json:"title"`
	Published bool   `json:"published"`
}

type recoveryMigration struct {
	MigrationID string `db:"migration_id" json:"migrationID"`
	ChainHash   string `db:"chain_hash" json:"chainHash"`
}

type recoveryFact struct {
	EventID            string `db:"event_id" json:"eventID"`
	ModelID            string `db:"model_id" json:"modelID"`
	Action             string `db:"action" json:"action"`
	CausationID        string `db:"causation_id" json:"causationID"`
	TransactionOrdinal int64  `db:"transaction_ordinal" json:"transactionOrdinal"`
}

type recoveryDelivery struct {
	CausationID     string  `json:"causationID"`
	Status          string  `json:"status"`
	AttemptCount    int64   `json:"attemptCount"`
	LastFailureCode *string `json:"lastFailureCode"`
}

type recoveryDeliveryRow struct {
	CausationID     string         `db:"causation_id"`
	Status          string         `db:"status"`
	AttemptCount    int64          `db:"attempt_count"`
	LastFailureCode sql.NullString `db:"last_failure_code"`
}

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "seed" && os.Args[1] != "verify" && os.Args[1] != "drain") {
		fmt.Fprintln(os.Stderr, "usage: social-recovery-fixture <seed|verify|drain>")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := openRecoveryDatabase(ctx)
	if err != nil {
		recoveryFailure()
	}
	defer database.Close()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 32})
	if err != nil {
		recoveryFailure()
	}
	application, err := openRecoveryApplication(ctx, database, transport)
	if err != nil {
		recoveryFailure()
	}

	switch os.Args[1] {
	case "seed":
		err = seedRecoveryCanary(ctx, application)
	case "verify":
		// Verification below deliberately performs no writes.
	case "drain":
		err = drainRecoveryFact(ctx, database, application)
	}
	if err != nil {
		recoveryFailure()
	}
	snapshot, err := readRecoverySnapshot(ctx, database, application)
	if err != nil || validateRecoverySnapshot(snapshot, os.Args[1] == "drain") != nil {
		recoveryFailure()
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(snapshot); err != nil {
		recoveryFailure()
	}
}

func recoveryFailure() {
	fmt.Fprintln(os.Stderr, "social recovery fixture failed")
	os.Exit(1)
}

func openRecoveryDatabase(ctx context.Context) (*provider.Database, error) {
	dsn := strings.TrimSpace(os.Getenv("GOLEM_DATABASE_DSN"))
	if dsn == "" {
		return nil, errors.New("database DSN is required")
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOLEM_PROVIDER"))) {
	case "sqlite":
		return sqlite.Open(ctx, sqlite.Config{DataSourceName: dsn})
	case "postgresql":
		return postgresql.Open(ctx, postgresql.Config{DataSourceName: dsn})
	default:
		return nil, errors.New("provider is required")
	}
}

func openRecoveryApplication(ctx context.Context, database *provider.Database, transport events.EventTransport) (*social.App[social.Principal], error) {
	return social.Open(ctx, social.Config[social.Principal]{
		Database:       database,
		EventTransport: transport,
		ResolvePrincipal: func(context.Context, social.Principal) (social.Actor, error) {
			return social.Actor{}, nil
		},
		SnapshotPrincipal:   func(principal social.Principal) (social.Principal, error) { return principal, nil },
		SnapshotActor:       func(actor social.Actor) (social.Actor, error) { return actor, nil },
		AuditPrincipal:      func(social.Principal) string { return "recovery-anonymous" },
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
	})
}

func seedRecoveryCanary(ctx context.Context, application *social.App[social.Principal]) error {
	userID, err := golem.ParseUUID(recoveryUserID)
	if err != nil {
		return err
	}
	postID, err := golem.ParseUUID(recoveryPostID)
	if err != nil {
		return err
	}
	date, err := golem.ParseDate("2026-08-09")
	if err != nil {
		return err
	}
	clock, err := golem.ParseTime("13:14:15")
	if err != nil {
		return err
	}
	metadata, err := golem.NewJSONDocument[any]([]byte(`{"language":"en","pinned":true}`))
	if err != nil {
		return err
	}
	system := application.System()
	if _, err := system.Users.Create(ctx, social.Users.Create(
		social.Users.ID.Create(userID), social.Users.Handle.Create(recoveryHandle), social.Users.Email.Create("recovery-canary@example.invalid"),
	)); err != nil {
		return err
	}
	_, err = system.Posts.Create(ctx, social.Posts.Create(
		social.Posts.ID.Create(postID), social.Posts.AuthorID.Create(userID),
		social.Posts.Title.Create(recoveryTitle), social.Posts.Body.Create("durable recovery canary"),
		social.Posts.Published.Create(true), social.Posts.LiveDate.Create(date), social.Posts.LiveTime.Create(clock),
		social.Posts.Metadata.Create(metadata), social.Posts.Topics.Create(golem.List[string]{"recovery"}),
	))
	return err
}

func drainRecoveryFact(ctx context.Context, database *provider.Database, application *social.App[social.Principal]) error {
	postID, err := golem.ParseUUID(recoveryPostID)
	if err != nil {
		return err
	}
	caller, err := application.ForPrincipal(ctx, social.Principal{})
	if err != nil {
		return err
	}
	stream, err := caller.Posts.Events(ctx, golem.EventWhere(social.Posts.ID.Eq(postID)), golem.EventSelect[social.Post](social.Posts.ID))
	if err != nil {
		return err
	}
	defer stream.Close()
	publisherCtx, stopPublisher := context.WithCancel(ctx)
	publisherDone := make(chan error, 1)
	go func() { publisherDone <- application.RunEventPublisher(publisherCtx) }()
	receiveContext, stopReceive := context.WithTimeout(ctx, 10*time.Second)
	event, err := stream.Recv(receiveContext)
	stopReceive()
	if err == nil {
		deliveryContext, stopDelivery := context.WithTimeout(ctx, 10*time.Second)
		err = waitRecoveryFactDelivered(deliveryContext, database)
		stopDelivery()
	}
	stopPublisher()
	select {
	case publisherErr := <-publisherDone:
		if err == nil && publisherErr != nil && publisherCtx.Err() == nil {
			err = publisherErr
		}
	case <-time.After(3 * time.Second):
		if err == nil {
			err = errors.New("publisher did not stop")
		}
	}
	if err != nil {
		return err
	}
	if event.ID() != postID || event.Metadata().Action() != golem.EventCreated {
		return errors.New("publisher delivered the wrong recovery fact")
	}
	return nil
}

func waitRecoveryFactDelivered(ctx context.Context, database *provider.Database) error {
	managedSchema := ""
	switch database.Provider() {
	case golem.SQLite:
	case golem.PostgreSQL:
		managedSchema = `"_golem".`
	default:
		return errors.New("recovery provider is unsupported")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var delivered int
		if err := database.UnsafeSQLX().GetContext(ctx, &delivered, `SELECT COUNT(*) FROM `+managedSchema+`"_golem_outbox_delivery" WHERE status = 'delivered'`); err != nil {
			return err
		}
		if delivered == 1 {
			return nil
		}
		if delivered > 1 {
			return errors.New("recovery delivery evidence is ambiguous")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func readRecoverySnapshot(ctx context.Context, database *provider.Database, application *social.App[social.Principal]) (recoverySnapshot, error) {
	userID, err := golem.ParseUUID(recoveryUserID)
	if err != nil {
		return recoverySnapshot{}, err
	}
	postID, err := golem.ParseUUID(recoveryPostID)
	if err != nil {
		return recoverySnapshot{}, err
	}
	system := application.System()
	user, err := system.Users.FindUnique(ctx, social.Users.ByID.Value(userID), social.Users.Select(social.Users.ID, social.Users.Handle))
	if err != nil {
		return recoverySnapshot{}, err
	}
	post, err := system.Posts.FindUnique(ctx, social.Posts.ByID.Value(postID), social.Posts.Select(social.Posts.ID, social.Posts.Title, social.Posts.Published))
	if err != nil {
		return recoverySnapshot{}, err
	}
	storedUserID, userIDPresent := golem.Value(user, social.Users.ID).Get()
	handle, handlePresent := golem.Value(user, social.Users.Handle).Get()
	storedPostID, postIDPresent := golem.Value(post, social.Posts.ID).Get()
	title, titlePresent := golem.Value(post, social.Posts.Title).Get()
	published, publishedPresent := golem.Value(post, social.Posts.Published).Get()
	if !userIDPresent || !handlePresent || !postIDPresent || !titlePresent || !publishedPresent {
		return recoverySnapshot{}, errors.New("recovery canary projection is incomplete")
	}

	managedSchema := ""
	if database.Provider() == golem.PostgreSQL {
		managedSchema = `"_golem".`
	}
	snapshot := recoverySnapshot{Canary: recoveryCanary{
		UserID: storedUserID.String(), Handle: handle, PostID: storedPostID.String(), Title: title, Published: published,
	}}
	pool := database.UnsafeSQLX()
	if err := pool.SelectContext(ctx, &snapshot.Migrations, `SELECT migration_id,chain_hash FROM `+managedSchema+`"_golem_migrations" ORDER BY migration_id`); err != nil {
		return recoverySnapshot{}, err
	}
	if err := pool.SelectContext(ctx, &snapshot.Facts, `SELECT event_id,model_id,action,causation_id,transaction_ordinal FROM `+managedSchema+`"_golem_outbox" ORDER BY recorded_at,event_id`); err != nil {
		return recoverySnapshot{}, err
	}
	var deliveryRows []recoveryDeliveryRow
	if err := pool.SelectContext(ctx, &deliveryRows, `SELECT causation_id,status,attempt_count,last_failure_code FROM `+managedSchema+`"_golem_outbox_delivery" ORDER BY causation_id`); err != nil {
		return recoverySnapshot{}, err
	}
	snapshot.Deliveries = make([]recoveryDelivery, len(deliveryRows))
	for index, row := range deliveryRows {
		delivery := recoveryDelivery{CausationID: row.CausationID, Status: row.Status, AttemptCount: row.AttemptCount}
		if row.LastFailureCode.Valid {
			value := row.LastFailureCode.String
			delivery.LastFailureCode = &value
		}
		snapshot.Deliveries[index] = delivery
	}
	return snapshot, nil
}

func validateRecoverySnapshot(snapshot recoverySnapshot, drained bool) error {
	if snapshot.Canary != (recoveryCanary{UserID: recoveryUserID, Handle: recoveryHandle, PostID: recoveryPostID, Title: recoveryTitle, Published: true}) {
		return errors.New("recovery canary disagrees")
	}
	if len(snapshot.Migrations) == 0 || len(snapshot.Facts) != 1 || snapshot.Facts[0].Action != "created" || snapshot.Facts[0].EventID == "" || snapshot.Facts[0].CausationID == "" {
		return errors.New("managed recovery evidence is incomplete")
	}
	if drained {
		if len(snapshot.Deliveries) != 1 || snapshot.Deliveries[0].CausationID != snapshot.Facts[0].CausationID || snapshot.Deliveries[0].Status != "delivered" {
			return errors.New("recovery fact was not delivered")
		}
		return nil
	}
	for _, delivery := range snapshot.Deliveries {
		if delivery.Status == "delivered" || delivery.Status == "retired" {
			return errors.New("recovery fact is not pending")
		}
	}
	return nil
}
