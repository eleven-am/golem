package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	sqlite3 "github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

func TestQueueBootstrapFailureRollsBackAndRetries(t *testing.T) {
	for _, history := range []bool{false, true} {
		name := "fresh"
		if history {
			name = "history"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			database, _, err := New().Open(ctx, filepath.Join(t.TempDir(), "queue.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			database.SetMaxOpenConns(1)
			database.SetMaxIdleConns(1)
			store := &queueStore{database: database}
			want := queueprovider.StorageAbsent
			deniedIndex := "golem_queue_claim"
			if history {
				if err := store.EnsureSchema(ctx); err != nil {
					t.Fatal(err)
				}
				store.history = true
				want = queueprovider.StoragePriorityV1
				deniedIndex = "golem_queue_history"
			}
			denied := false
			connection, err := database.Connx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = connection.Raw(func(raw any) error {
				return raw.(sqlitedriver.Conn).Raw().SetAuthorizer(func(action sqlite3.AuthorizerActionCode, first, second, namespace, inner string) sqlite3.AuthorizerReturnCode {
					if action == sqlite3.AUTH_CREATE_INDEX && first == deniedIndex {
						denied = true
						return sqlite3.AUTH_DENY
					}
					return sqlite3.AUTH_OK
				})
			})
			if closeErr := connection.Close(); err != nil || closeErr != nil {
				t.Fatalf("authorizer setup=%v close=%v", err, closeErr)
			}
			bootstrapErr := store.EnsureSchema(ctx)
			connection, err = database.Connx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = connection.Raw(func(raw any) error { return raw.(sqlitedriver.Conn).Raw().SetAuthorizer(nil) })
			if closeErr := connection.Close(); err != nil || closeErr != nil {
				t.Fatalf("authorizer cleanup=%v close=%v", err, closeErr)
			}
			if !denied || bootstrapErr == nil {
				t.Fatalf("real index fault reached=%v bootstrap error=%v", denied, bootstrapErr)
			}
			state, err := store.InspectStorage(ctx)
			if err != nil || state.Version != want || state.HistoryIndexes {
				t.Fatalf("failed bootstrap left partial storage: %+v error=%v", state, err)
			}
			if err := store.EnsureSchema(ctx); err != nil {
				t.Fatalf("retry after failed bootstrap: %v", err)
			}
			state, err = store.InspectStorage(ctx)
			if err != nil || state.Version != queueprovider.StoragePriorityV1 || state.HistoryIndexes != history {
				t.Fatalf("retry storage=%+v error=%v", state, err)
			}
		})
	}
}
