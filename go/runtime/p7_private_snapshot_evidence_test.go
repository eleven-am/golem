package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	graphqlselectset "github.com/eleven-am/golem/go/internal/graphql/select"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

type p7AllObservationCapture struct {
	mu     sync.Mutex
	values []events.Observation
}

func (capture *p7AllObservationCapture) ObserveEvent(_ context.Context, value events.Observation) {
	capture.mu.Lock()
	capture.values = append(capture.values, value)
	capture.mu.Unlock()
}

func TestP7PrivateSnapshotAbsentFromGoGraphQLErrorsObserversAndNoticeMetadata(t *testing.T) {
	const secret = "private-delete-token=never-public"
	fixture := newP7EventRuntimeFixture(t)
	observations := &p7AllObservationCapture{}
	fixture.app.eventObserver = observations
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	filtered, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor,
		golem.EventWhere[p7EventPost](fixture.title.Eq("not-the-secret")))
	if err != nil {
		t.Fatal(err)
	}
	defer filtered.Close()
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(time.Second):
		t.Fatal("transport subscription did not start")
	}

	notice := fixture.notice(t, golem.EventDeleted, 1920, secret, true)
	if !bytes.Contains(notice.Encoded(), []byte(secret)) {
		t.Fatal("red-team fixture did not place the secret in the trusted private transport bytes")
	}
	fixture.publish(t, notice)
	event := receiveP7Event(t, stream)
	if _, present := event.validated.Entity(); present {
		t.Fatal("delete exposed an entity")
	}
	assertP7SecretAbsent(t, secret, fmt.Sprintf("%#v", event), mustP7Marshal(t, event))

	metadata := event.validated.Metadata()
	noticeMetadata := fmt.Sprintf("%x|%x|%x|%s|%d", notice.EventID(), notice.CausationID(), notice.ModelID(), notice.Action(), notice.TransactionOrdinal())
	assertP7SecretAbsent(t, secret, noticeMetadata, fmt.Sprintf("%#v", metadata), mustP7Marshal(t, metadata))

	deadline := time.Now().Add(2 * time.Second)
	for {
		observations.mu.Lock()
		captured := append([]events.Observation(nil), observations.values...)
		observations.mu.Unlock()
		foundSuppression := false
		for _, observation := range captured {
			if observation.Kind() == events.ObservationSuppression {
				foundSuppression = true
			}
			assertP7SecretAbsent(t, secret, fmt.Sprintf("%#v", observation), mustP7Marshal(t, observation))
		}
		if foundSuppression {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("filtered delete did not emit a sanitized suppression observation")
		}
		time.Sleep(time.Millisecond)
	}

	graphQLPayload := p7EncodeDeleteGraphQL(t, fixture, event.validated)
	assertP7SecretAbsent(t, secret, string(graphQLPayload))

	fixture.app.snapshotPrincipal = func(p7EventPrincipal) (p7EventPrincipal, error) {
		return p7EventPrincipal{}, errors.New(secret)
	}
	_, publicErr := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
	if publicErr == nil {
		t.Fatal("secret-bearing snapshot error was accepted")
	}
	assertP7SecretAbsent(t, secret, publicErr.Error())
}

func p7EncodeDeleteGraphQL(t testing.TB, fixture p7EventRuntimeFixture, event ValidatedEvent) []byte {
	t.Helper()
	_, compilation := p5GraphQLBundle(t, fixture.schema.Bundle)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "p7-private.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	query, queryErrors := gqlparser.LoadQuery(schema, `subscription { postEvents { eventID causationID transactionOrdinal recordedAt type id entity { title } } }`)
	if len(queryErrors) != 0 {
		t.Fatal(queryErrors)
	}
	compiler, err := graphqloperation.New(compilation, graphqloperation.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations[0], nil)
	if err != nil || compiled.Event == nil {
		t.Fatalf("compile event root=%#v error=%v", compiled.Event, err)
	}
	prepared, failures, err := compiler.EncodeEventWithComputedPartial(context.Background(), *compiled.Event, event.Metadata(), event.IdentityValues(), nil,
		func(context.Context, compilerir.ModelID, []golem.RuntimeModelRow, graphqlselectset.Slot) ([]any, error) {
			return nil, errors.New("computed resolver must not run for a deleted entity")
		})
	if err != nil || len(failures) != 0 {
		t.Fatalf("encode GraphQL delete failures=%v error=%v", failures, err)
	}
	encoded, err := json.Marshal(map[string]any{compiled.Event.ResponseName: prepared})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func mustP7Marshal(t testing.TB, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func assertP7SecretAbsent(t testing.TB, secret string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(value, secret) {
			t.Fatalf("private delete snapshot leaked: %s", value)
		}
	}
}
