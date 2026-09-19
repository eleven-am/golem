package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
	"github.com/eleven-am/golem/go/queue"
)

type outageEmbedder struct {
	specification embedding.Specification
	failure       error
}

func (provider outageEmbedder) Specification() embedding.Specification {
	return provider.specification
}

func (provider outageEmbedder) Embed(context.Context, []embedding.Input) ([]embedding.Vector, error) {
	return nil, provider.failure
}

func (fixture semanticJobFixture) withProvider(t *testing.T, provider embedding.Provider) {
	t.Helper()
	registry, err := embedding.NewRegistry(map[string]embedding.Provider{"content": provider})
	if err != nil {
		t.Fatal(err)
	}
	schema := semanticJobPhysicalSchema(t)
	inventory, err := semanticruntime.NewInventory(schema, registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := semanticruntime.NewManager(fixture.database, ir.SQLite, schema, inventory)
	if err != nil {
		t.Fatal(err)
	}
	fixture.app.semantic = manager
}

func (fixture semanticJobFixture) status(t *testing.T, id string) string {
	t.Helper()
	key, err := semantickey.Encode([]any{id})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err := fixture.database.Get(&status, `SELECT status FROM "`+semanticJobStateTable+`" WHERE record_key=?`, key); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestSemanticDrainDefersAProviderOutageWithoutSpendingAnAttempt(t *testing.T) {
	ctx := context.Background()
	failures := map[string]error{
		"unavailable":  embedding.NewError(embedding.CodeUnavailable, fmt.Errorf("credential=semantic-outage-canary")),
		"unclassified": fmt.Errorf("status 429 credential=semantic-outage-canary"),
	}
	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			fixture := newSemanticJobFixture(t, nil)
			if _, err := fixture.database.Exec(`UPDATE "posts" SET title='alpha revised' WHERE id='a'`); err != nil {
				t.Fatal(err)
			}
			fixture.mark(t, "a")
			fixture.withProvider(t, outageEmbedder{specification: fixture.embedder.specification, failure: failure})
			payload := semanticJob{Model: string(semanticJobModelID()), Index: "related"}
			for _, test := range []struct {
				name     string
				waited   time.Duration
				earliest time.Duration
				latest   time.Duration
			}{
				{name: "fresh", waited: 0, earliest: semanticOutageBackoffFloor, latest: semanticOutageBackoffFloor},
				{name: "doubling", waited: 40 * time.Second, earliest: 40 * time.Second, latest: 45 * time.Second},
				{name: "capped", waited: 72 * time.Hour, earliest: semanticOutageBackoffCap, latest: semanticOutageBackoffCap},
			} {
				err := fixture.app.runSemanticDrain(ctx, queue.Job[semanticJob]{ID: "drain-outage", Payload: payload, Attempt: 5, MaxAttempts: 5, EnqueuedAt: time.Now().Add(-test.waited)})
				if err == nil {
					t.Fatalf("%s: a provider outage completed the drain", test.name)
				}
				for cause := error(err); cause != nil; cause = errors.Unwrap(cause) {
					if strings.Contains(cause.Error(), "canary") {
						t.Fatalf("%s: provider error text crossed the boundary: %v", test.name, cause)
					}
				}
				outcome := queue.Classify(err)
				if outcome.Resolution != queue.ResolutionRetry || !outcome.Uncounted || !outcome.Scheduled {
					t.Fatalf("%s: outage outcome=%#v want an uncounted scheduled retry", test.name, outcome)
				}
				if outcome.Delay < test.earliest || outcome.Delay > test.latest {
					t.Fatalf("%s: outage delay=%s want within [%s,%s]", test.name, outcome.Delay, test.earliest, test.latest)
				}
			}
			if status := fixture.status(t, "a"); status != "pending" {
				t.Fatalf("record status=%q after an outage, want it still owed", status)
			}
		})
	}
}

func TestSemanticDrainStillSpendsAnAttemptOnItsOwnFailure(t *testing.T) {
	ctx := context.Background()
	fixture := newSemanticJobFixture(t, nil)
	fixture.mark(t, "a")
	if _, err := fixture.database.Exec(`DROP TABLE "posts"`); err != nil {
		t.Fatal(err)
	}
	payload := semanticJob{Model: string(semanticJobModelID()), Index: "related"}
	err := fixture.app.runSemanticDrain(ctx, queue.Job[semanticJob]{ID: "drain-broken", Payload: payload, EnqueuedAt: time.Now()})
	if err == nil {
		t.Fatal("a drain that could not read its source completed")
	}
	if outcome := queue.Classify(err); outcome.Resolution != queue.ResolutionRetry || outcome.Uncounted {
		t.Fatalf("source failure outcome=%#v want an ordinary counted retry", outcome)
	}
}
