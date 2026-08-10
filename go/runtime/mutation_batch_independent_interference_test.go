package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
)

// TestBatchMutationIndependentConnectionInsertAfterCaptureDoesNotWidenExactSet
// proves the capture/apply boundary with a second physical database connection.
// PostgreSQL can commit the phantom while the first transaction is open;
// SQLite's BEGIN IMMEDIATE makes the independent writer wait until commit. In
// both cases the newly matching row is outside the captured identity set.
func TestBatchMutationIndependentConnectionInsertAfterCaptureDoesNotWidenExactSet(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		assertBatchIndependentInsertAfterCapture(t, profile)
	})
}

func assertBatchIndependentInsertAfterCapture(t *testing.T, profile mutationProviderAcceptanceFixture) {
	t.Helper()
	ctx := context.Background()
	fixture := profile.fixture
	for _, id := range []byte{230, 231} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "independent-interference")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+profile.outbox); err != nil {
		t.Fatal(err)
	}

	// Pin this connection before the batch starts. The batch transaction must
	// therefore acquire a different physical connection from the pool.
	independent, err := fixture.app.database.Connx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer independent.Close()
	if profile.provider == golem.SQLite {
		if _, err := independent.ExecContext(ctx, `PRAGMA busy_timeout = 25`); err != nil {
			t.Fatal(err)
		}
	}

	insert := func(ctx context.Context) error {
		statement := fmt.Sprintf(`INSERT INTO %s ("id","author_id","title") VALUES (%s,%s,%s)`, profile.posts, profile.placeholder(1), profile.placeholder(2), profile.placeholder(3))
		_, err := independent.ExecContext(ctx, statement, mutationResultUUIDText(232), mutationResultUUIDText(1), "independent-interference")
		return err
	}

	var sqliteBlocked bool
	observer := func(context.Context) error {
		if profile.provider == golem.PostgreSQL {
			// PostgreSQL permits this independent insert to commit after capture
			// and before the captured identities are applied.
			return insert(context.Background())
		}
		// A short synchronous attempt proves this second physical connection
		// actually entered SQLite's write-lock window after capture. Merely
		// starting a goroutine would not establish that ordering.
		err := insert(context.Background())
		if err == nil {
			return fmt.Errorf("independent SQLite insert unexpectedly committed inside the capture/apply window")
		}
		message := strings.ToLower(err.Error())
		if !strings.Contains(message, "locked") && !strings.Contains(message, "busy") {
			return fmt.Errorf("independent SQLite insert failed for a reason other than the immediate writer lock: %w", err)
		}
		sqliteBlocked = true
		return nil
	}

	batchContext := contextWithBatchAfterCaptureObserver(ctx, observer)
	count, err := SystemUpdateMany(batchContext, fixture.app.System(), fixture.postDescriptor,
		fixture.title.Eq("independent-interference"), fixture.updateManyTitle("captured-only"))
	if err != nil || count != 2 {
		t.Fatalf("captured update count=%d err=%v", count, err)
	}
	if profile.provider == golem.SQLite {
		if !sqliteBlocked {
			t.Fatal("independent SQLite connection did not observe the immediate writer lock")
		}
		if _, err := independent.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
			t.Fatal(err)
		}
		if err := insert(ctx); err != nil {
			t.Fatalf("independent SQLite insert after batch commit: %v", err)
		}
	}

	for _, row := range []struct {
		id    byte
		title string
	}{{230, "captured-only"}, {231, "captured-only"}, {232, "independent-interference"}} {
		var title string
		query := fixture.app.database.Rebind(`SELECT "title" FROM ` + profile.posts + ` WHERE "id" = ?`)
		if err := fixture.app.database.GetContext(ctx, &title, query, mutationResultUUIDText(row.id)); err != nil || title != row.title {
			t.Fatalf("post %d title=%q want=%q err=%v", row.id, title, row.title, err)
		}
	}
	assertPostFactSequence(t, fixture, mutationir.FactUpdated, 230, 231)
}
