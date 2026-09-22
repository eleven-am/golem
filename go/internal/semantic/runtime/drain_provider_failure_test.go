package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	"github.com/eleven-am/golem/go/observe"
)

type ambiguousProvider struct {
	inner      *deterministicProvider
	rejectText string
	calls      int
}

func (provider *ambiguousProvider) Specification() embedding.Specification {
	return provider.inner.specification
}

func (provider *ambiguousProvider) Embed(ctx context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	provider.calls++
	for _, input := range inputs {
		if provider.rejectText == "" || strings.Contains(input.Text(), provider.rejectText) {
			return nil, fmt.Errorf("status 429 credential=semantic-ambiguous-canary")
		}
	}
	return provider.inner.Embed(ctx, inputs)
}

func assertDrainErrorIsClosed(t *testing.T, err error) {
	t.Helper()
	if strings.Contains(err.Error(), "canary") {
		t.Fatalf("provider error text crossed the boundary: %v", err)
	}
	for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), "canary") {
			t.Fatalf("provider error text reachable through the cause chain: %v", cause)
		}
	}
}

func TestSemanticDrainDefersAnUnclassifiedProviderFailureWithoutQuarantine(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title=title||' revised'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "a", "b")
	_, _, before := fixture.failure(t, "a")
	failing := &ambiguousProvider{inner: fixture.embedder}
	fixture.manager.indexes[0].Provider = failing
	_, err := fixture.manager.Drain(ctx, "post", "related")
	if err == nil {
		t.Fatal("an unclassified provider failure did not fail the drain")
	}
	if !ProviderDeferred(err) {
		t.Fatalf("an unclassified provider failure was not deferred as systemic: %v", err)
	}
	assertDrainErrorIsClosed(t, err)
	// One refused batch plus the isolation that decides whether the provider is
	// down or one record is poisoned. The batch call is not counted against the
	// budget, because counting it would let one refused record exhaust the
	// budget and starve every record behind it.
	if want := 1 + semanticOutageBatches + semanticLivenessProbes; failing.calls > want {
		t.Fatalf("provider calls=%d exceeds the bound of %d: unclassified isolation escaped the outage budget", failing.calls, want)
	}
	for _, id := range []string{"a", "b"} {
		if status, code, attempts := fixture.failure(t, id); status != "pending" || code.Valid || attempts != before {
			t.Fatalf("record %q status=%q error_code=%#v attempts=%d: an unclassified failure quarantined it", id, status, code, attempts)
		}
	}
	fixture.manager.indexes[0].Provider = fixture.embedder
	if _, err := fixture.manager.Drain(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if fixture.status(t, "a") != "ready" || fixture.status(t, "b") != "ready" {
		t.Fatalf("recovered provider did not settle records: a=%q b=%q", fixture.status(t, "a"), fixture.status(t, "b"))
	}
}

func TestSemanticDrainNeverQuarantinesADocumentOnAnUnclassifiedRefusal(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO "posts" (id,title) VALUES ('c','poison c')`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "c")
	_, _, before := fixture.failure(t, "c")
	fixture.manager.indexes[0].Provider = &ambiguousProvider{inner: fixture.embedder, rejectText: "poison"}
	_, err := fixture.manager.Drain(ctx, "post", "related")
	if err == nil {
		t.Fatal("an unclassified refusal was treated as settled")
	}
	if !ProviderDeferred(err) {
		t.Fatalf("an unclassified refusal was not deferred as systemic: %v", err)
	}
	assertDrainErrorIsClosed(t, err)
	if status, code, attempts := fixture.failure(t, "c"); status != "pending" || code.Valid || attempts != before {
		t.Fatalf("record status=%q error_code=%#v attempts=%d: an unclassified refusal quarantined it", status, code, attempts)
	}
	if err := fixture.manager.Refresh(ctx, "post", "related"); err == nil {
		t.Fatal("reconcile treated an unclassified refusal as settled")
	}
	if status, code, _ := fixture.failure(t, "c"); status != "pending" || code.Valid {
		t.Fatalf("reconcile quarantined an unclassified refusal: status=%q error_code=%#v", status, code)
	}
}

func TestSemanticDrainPassContinuesPastADocumentTheProviderKeepsRefusing(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	poisoned := map[string]bool{"p02": true, "p17": true}
	ids := make([]string, 0, 32)
	for ordinal := range 32 {
		id := fmt.Sprintf("p%02d", ordinal)
		title := "gamma " + id
		if poisoned[id] {
			title = "poison " + id
		}
		if _, err := fixture.db.Exec(`INSERT INTO "posts" (id,title) VALUES (?,?)`, id, title); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	fixture.markStale(t, ids...)
	fixture.markStale(t, "b")
	fixture.markStale(t, "a")
	if _, err := fixture.db.Exec(`DELETE FROM "posts" WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	refusing := &ambiguousProvider{inner: fixture.embedder, rejectText: "poison"}
	fixture.manager.indexes[0].Provider = refusing
	for pass := range 3 {
		_, err := fixture.manager.Drain(ctx, "post", "related")
		if !ProviderDeferred(err) {
			t.Fatalf("pass %d error=%v, want the refused document deferred", pass, err)
		}
		assertDrainErrorIsClosed(t, err)
	}
	for id := range poisoned {
		if status, code, _ := fixture.failure(t, id); status != "pending" || code.Valid {
			t.Fatalf("refused record %q status=%q error_code=%#v", id, status, code)
		}
	}
	for _, id := range append(append([]string(nil), ids[8:16]...), ids[24:]...) {
		if status := fixture.status(t, id); status != "ready" {
			t.Fatalf("record %q after the refused batch status=%q: one refused document stalled the index", id, status)
		}
	}
	if status := fixture.status(t, "b"); status != "ready" {
		t.Fatalf("unchanged marked record status=%q: the refusal skipped the unchanged flips", status)
	}
	alpha, err := semantickey.Encode([]any{"a"})
	if err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := fixture.db.Get(&remaining, `SELECT COUNT(*) FROM "`+drainStateTable+`" WHERE record_key=?`, alpha); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("the refusal skipped cleanup of a record whose owner is gone")
	}
}

func TestSemanticReconcileContinuesPastADocumentTheProviderKeepsRefusing(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	ids := make([]string, 0, 20)
	for ordinal := range 20 {
		id := fmt.Sprintf("p%02d", ordinal)
		title := "gamma " + id
		if ordinal == 2 {
			title = "poison " + id
		}
		if _, err := fixture.db.Exec(`INSERT INTO "posts" (id,title) VALUES (?,?)`, id, title); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := fixture.db.Exec(`DELETE FROM "posts" WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	fixture.manager.indexes[0].Provider = &ambiguousProvider{inner: fixture.embedder, rejectText: "poison"}
	if err := fixture.manager.Refresh(ctx, "post", "related"); !ProviderDeferred(err) {
		t.Fatalf("reconcile error=%v, want the refused document deferred", err)
	}
	for _, id := range ids[8:] {
		if status := fixture.status(t, id); status != "ready" {
			t.Fatalf("record %q after the refused batch status=%q: one refused document stalled reconcile", id, status)
		}
	}
	// Isolating the refused batch lets every healthy document in it settle, so
	// only the poisoned one is left pending.
	for _, id := range ids {
		want := "ready"
		if id == "p02" {
			want = "pending"
		}
		if status := fixture.status(t, id); status != want {
			t.Fatalf("record %q status=%q want %q", id, status, want)
		}
	}
	if fixture.count(t, drainStateTable) != 1+len(ids) {
		t.Fatalf("state rows=%d: reconcile skipped cleanup of a record whose owner is gone", fixture.count(t, drainStateTable))
	}
}

func TestSemanticPassStopsCallingAProviderThatIsDownAcrossBatches(t *testing.T) {
	ctx := context.Background()
	fixture := newDrainFixture(t)
	ids := make([]string, 0, 40)
	for ordinal := range 40 {
		id := fmt.Sprintf("q%02d", ordinal)
		if _, err := fixture.db.Exec(`INSERT INTO "posts" (id,title) VALUES (?,?)`, id, "gamma "+id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	down := &ambiguousProvider{inner: fixture.embedder}
	fixture.manager.indexes[0].Provider = down
	if err := fixture.manager.Refresh(ctx, "post", "related"); !ProviderDeferred(err) {
		t.Fatalf("reconcile error=%v, want the outage deferred", err)
	}
	// One refused batch, the isolation that decides whether the provider is
	// down or one record is poisoned, and at most semanticLivenessProbes
	// re-embeds of already-stored documents. The count does not grow with the
	// number of batches, which is what this test exists to protect.
	if want := 1 + semanticOutageBatches + semanticLivenessProbes; down.calls > want {
		t.Fatalf("reconcile provider calls=%d exceeds the bound of %d during an outage spanning 5 batches", down.calls, want)
	}
	fixture.manager.indexes[0].Provider = fixture.embedder
	if err := fixture.manager.Refresh(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title=title||' revised'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "b")
	fixture.markStale(t, ids...)
	fixture.markStale(t, "a")
	if _, err := fixture.db.Exec(`DELETE FROM "posts" WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`UPDATE "posts" SET title='beta' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	down.calls = 0
	fixture.manager.indexes[0].Provider = down
	collector := &semanticObservationCollector{}
	fixture.manager.observer = collector
	for pass := range 2 {
		down.calls = 0
		if _, err := fixture.manager.Drain(ctx, "post", "related"); !ProviderDeferred(err) {
			t.Fatalf("pass %d error=%v, want the outage deferred", pass, err)
		}
		if want := 1 + semanticOutageBatches + semanticLivenessProbes; down.calls > want {
			t.Fatalf("pass %d provider calls=%d exceeds the bound of %d during an outage spanning 5 batches", pass, down.calls, want)
		}
	}
	for _, id := range ids {
		if status, code, _ := fixture.failure(t, id); status != "pending" || code.Valid {
			t.Fatalf("record %q status=%q error_code=%#v during an outage", id, status, code)
		}
	}
	if status := fixture.status(t, "b"); status != "ready" {
		t.Fatalf("unchanged marked record status=%q: the outage skipped the unchanged flips", status)
	}
	if fixture.count(t, drainStateTable) != 1+len(ids) {
		t.Fatalf("state rows=%d: the outage skipped cleanup of a record whose owner is gone", fixture.count(t, drainStateTable))
	}
	left := 0
	for _, observed := range collector.take() {
		if observed.kind == observe.KindSemantic && observed.operation == observe.OperationSemanticRefresh && observed.outcome == observe.OutcomeRetrying {
			if observed.reason != observe.ReasonProvider || observed.aggregate != int64(len(ids)) {
				t.Fatalf("deferral observation=%#v want %d records left pending", observed, len(ids))
			}
			left++
		}
	}
	if left != 2 {
		t.Fatalf("deferral observations=%d want one per pass", left)
	}
}
