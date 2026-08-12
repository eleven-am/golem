package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"example.com/golempolicykit/policy"
	"github.com/eleven-am/golem/go/golem"
)

const (
	callerSystemRaceID = "dd000001-0000-4000-8000-000000000001"
	transactionRaceID  = "dd000002-0000-4000-8000-000000000002"
	graphqlRaceID      = "dd000003-0000-4000-8000-000000000003"
)

type optimisticConcurrencyWriter struct {
	title string
	run   func(context.Context) error
}

type optimisticConcurrencyOutcome struct {
	title string
	err   error
}

type graphQLConflictError struct {
	code    string
	message string
}

func (failure *graphQLConflictError) Error() string {
	return "GraphQL mutation failed with " + failure.code + ": " + failure.message
}

func TestExternalGeneratedApplicationOptimisticConcurrencyRaces(t *testing.T) {
	for _, value := range []struct {
		provider golem.Provider
		want     string
	}{
		{provider: golem.SQLite, want: `"_golem_outbox"`},
		{provider: golem.PostgreSQL, want: `"_golem"."_golem_outbox"`},
	} {
		if got, ok := managedOutboxTable(value.provider); !ok || got != value.want {
			t.Fatalf("managed outbox table for %q = %q/%t; want %q/true", value.provider, got, ok, value.want)
		}
	}
	if _, ok := managedOutboxTable(golem.Provider("forged")); ok {
		t.Fatal("unknown provider resolved a managed outbox table")
	}

	for _, value := range targets(t) {
		value := value
		t.Run(value.name, func(t *testing.T) {
			database := value.open(t)
			defer func() {
				if err := database.Close(); err != nil {
					t.Errorf("close database: %v", err)
				}
			}()
			live := openApplication(t, database)
			principal := actor(t)
			graph, err := live.app.GraphQL(policy.GraphQLConfig[policy.Actor]{
				PrincipalFromContext: func(context.Context) (policy.Actor, bool) { return principal, true },
				ReportInternalError:  func(context.Context, error) {},
			})
			if err != nil {
				t.Fatalf("GraphQL: %v", err)
			}
			defer graph.Shutdown(context.Background())

			t.Run("caller-vs-system", func(t *testing.T) {
				id := seedRaceNote(t, live, callerSystemRaceID)
				before := outboxCount(t, live)
				winner := runOptimisticConcurrencyRace(t, []optimisticConcurrencyWriter{
					{title: "caller-won", run: func(ctx context.Context) error {
						row, err := live.caller.RaceNotes.Update(ctx, policy.RaceNotes.ByID.Value(id), golem.ExpectVersion(1), policy.RaceNotes.Update(policy.RaceNotes.Title.Set("caller-won")), policy.RaceNotes.Select(policy.RaceNotes.Title, policy.RaceNotes.Version))
						return verifyRaceMutationRow(row, "caller-won", err)
					}},
					{title: "system-won", run: func(ctx context.Context) error {
						row, err := live.system.RaceNotes.Update(ctx, policy.RaceNotes.ByID.Value(id), golem.ExpectVersion(1), policy.RaceNotes.Update(policy.RaceNotes.Title.Set("system-won")), policy.RaceNotes.Select(policy.RaceNotes.Title, policy.RaceNotes.Version))
						return verifyRaceMutationRow(row, "system-won", err)
					}},
				})
				assertRaceResult(t, live, id, winner, before)
			})

			t.Run("caller-tx-vs-system-tx", func(t *testing.T) {
				id := seedRaceNote(t, live, transactionRaceID)
				before := outboxCount(t, live)
				winner := runOptimisticConcurrencyRace(t, []optimisticConcurrencyWriter{
					{title: "caller-tx-won", run: func(ctx context.Context) error {
						return live.caller.Transaction(ctx, func(tx *policy.CallerTx[policy.Actor]) error {
							row, err := tx.RaceNotes.Update(ctx, policy.RaceNotes.ByID.Value(id), golem.ExpectVersion(1), policy.RaceNotes.Update(policy.RaceNotes.Title.Set("caller-tx-won")), policy.RaceNotes.Select(policy.RaceNotes.Title, policy.RaceNotes.Version))
							return verifyRaceMutationRow(row, "caller-tx-won", err)
						})
					}},
					{title: "system-tx-won", run: func(ctx context.Context) error {
						return live.system.Transaction(ctx, func(tx *policy.SystemTx[policy.Actor]) error {
							row, err := tx.RaceNotes.Update(ctx, policy.RaceNotes.ByID.Value(id), golem.ExpectVersion(1), policy.RaceNotes.Update(policy.RaceNotes.Title.Set("system-tx-won")), policy.RaceNotes.Select(policy.RaceNotes.Title, policy.RaceNotes.Version))
							return verifyRaceMutationRow(row, "system-tx-won", err)
						})
					}},
				})
				assertRaceResult(t, live, id, winner, before)
			})

			t.Run("graphql-vs-graphql", func(t *testing.T) {
				id := seedRaceNote(t, live, graphqlRaceID)
				before := outboxCount(t, live)
				winner := runOptimisticConcurrencyRace(t, []optimisticConcurrencyWriter{
					{title: "graphql-left-won", run: func(ctx context.Context) error {
						return graphQLRaceUpdate(ctx, graph.Handler(), id, "graphql-left-won")
					}},
					{title: "graphql-right-won", run: func(ctx context.Context) error {
						return graphQLRaceUpdate(ctx, graph.Handler(), id, "graphql-right-won")
					}},
				})
				assertRaceResult(t, live, id, winner, before)
			})
		})
	}
}

func seedRaceNote(t *testing.T, live application, text string) golem.UUID {
	t.Helper()
	id := identifier(t, text)
	row, err := live.system.RaceNotes.Create(context.Background(), policy.RaceNotes.Create(
		policy.RaceNotes.ID.Create(id),
		policy.RaceNotes.Title.Create("before"),
	), policy.RaceNotes.Select(policy.RaceNotes.Version))
	if err != nil {
		t.Fatalf("seed race note: %v", err)
	}
	version, present := golem.Value(row, policy.RaceNotes.Version).Get()
	if !present || version != 1 {
		t.Fatalf("seed race version=%d present=%t", version, present)
	}
	return id
}

func runOptimisticConcurrencyRace(t *testing.T, writers []optimisticConcurrencyWriter) string {
	t.Helper()
	if len(writers) != 2 {
		t.Fatalf("race writers=%d want=2", len(writers))
	}
	start := make(chan struct{})
	outcomes := make(chan optimisticConcurrencyOutcome, len(writers))
	var ready sync.WaitGroup
	ready.Add(len(writers))
	for _, candidate := range writers {
		candidate := candidate
		go func() {
			ready.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			outcomes <- optimisticConcurrencyOutcome{title: candidate.title, err: candidate.run(ctx)}
		}()
	}
	ready.Wait()
	close(start)
	values := []optimisticConcurrencyOutcome{<-outcomes, <-outcomes}
	var winner string
	conflicts := 0
	for _, value := range values {
		if value.err == nil {
			if winner != "" {
				t.Fatalf("two race writers succeeded: %q and %q", winner, value.title)
			}
			winner = value.title
			continue
		}
		if !isOptimisticConcurrencyConflict(value.err) {
			t.Fatalf("race writer %q error=%v, want CONFLICT", value.title, value.err)
		}
		conflicts++
	}
	if winner == "" || conflicts != 1 {
		t.Fatalf("race winner=%q conflicts=%d, want one of each", winner, conflicts)
	}
	return winner
}

func verifyRaceMutationRow(row golem.Row[policy.RaceNote], title string, err error) error {
	if err != nil {
		return err
	}
	gotTitle, titlePresent := golem.Value(row, policy.RaceNotes.Title).Get()
	version, versionPresent := golem.Value(row, policy.RaceNotes.Version).Get()
	if !titlePresent || gotTitle != title || !versionPresent || version != 2 {
		return fmt.Errorf("successful row title=%q/%t version=%d/%t", gotTitle, titlePresent, version, versionPresent)
	}
	return nil
}

func isOptimisticConcurrencyConflict(err error) bool {
	var public *golem.Error
	if errors.As(err, &public) {
		return public.Code == golem.CodeConflict
	}
	var graphQL *graphQLConflictError
	return errors.As(err, &graphQL) && graphQL.code == "CONFLICT"
}

func assertRaceResult(t *testing.T, live application, id golem.UUID, winner string, before int64) {
	t.Helper()
	row, err := live.system.RaceNotes.FindUnique(context.Background(), policy.RaceNotes.ByID.Value(id), policy.RaceNotes.Select(policy.RaceNotes.Title, policy.RaceNotes.Version))
	if err != nil {
		t.Fatalf("read race result: %v", err)
	}
	title, titlePresent := golem.Value(row, policy.RaceNotes.Title).Get()
	version, versionPresent := golem.Value(row, policy.RaceNotes.Version).Get()
	if !titlePresent || title != winner || !versionPresent || version != 2 {
		t.Fatalf("final race row title=%q/%t winner=%q version=%d/%t", title, titlePresent, winner, version, versionPresent)
	}
	after := outboxCount(t, live)
	if after-before != 1 {
		t.Fatalf("race outbox delta=%d want=1 (before=%d after=%d)", after-before, before, after)
	}
}

func outboxCount(t *testing.T, live application) int64 {
	t.Helper()
	table, ok := managedOutboxTable(live.database.Provider())
	if !ok {
		t.Fatalf("count outbox for unsupported provider %q", live.database.Provider())
	}
	var count int64
	if err := live.database.UnsafeSQLX().GetContext(context.Background(), &count, `SELECT COUNT(*) FROM `+table); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return count
}

func managedOutboxTable(value golem.Provider) (string, bool) {
	switch value {
	case golem.SQLite:
		return `"_golem_outbox"`, true
	case golem.PostgreSQL:
		return `"_golem"."_golem_outbox"`, true
	default:
		return "", false
	}
}

func graphQLRaceUpdate(ctx context.Context, handler http.Handler, id golem.UUID, title string) error {
	requestBody, err := json.Marshal(map[string]any{
		"query": `mutation Race($id: UUID!, $title: String!) {
  updateRaceNote(where: {ID: $id}, expectedVersion: 1, data: {title: {set: $title}}) { id title version }
}`,
		"variables": map[string]any{"id": id.String(), "title": title},
	})
	if err != nil {
		return err
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(requestBody)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		return fmt.Errorf("GraphQL status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data   map[string]map[string]any `json:"data"`
		Errors []struct {
			Message    string         `json:"message"`
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		return fmt.Errorf("decode GraphQL response: %w", err)
	}
	if len(response.Errors) != 0 {
		code, _ := response.Errors[0].Extensions["code"].(string)
		return &graphQLConflictError{code: code, message: response.Errors[0].Message}
	}
	result := response.Data["updateRaceNote"]
	if result == nil || result["title"] != title || fmt.Sprint(result["version"]) != "2" {
		return fmt.Errorf("GraphQL success result=%v", result)
	}
	return nil
}
