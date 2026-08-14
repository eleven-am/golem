package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/observe"
)

type p7EventPrincipal struct{ Subject string }
type p7EventActor struct{ Allow bool }
type p7EventUser struct{}
type p7EventPost struct{}

type p7EventOracleValue struct{ validated ValidatedEvent }

type p7EventOracleFactory struct {
	model  golem.ModelID
	schema golem.EventSchemaDigest
}

func (factory p7EventOracleFactory) ModelID() golem.ModelID { return factory.model }
func (factory p7EventOracleFactory) EventSchemaDigest() golem.EventSchemaDigest {
	return factory.schema
}
func (factory p7EventOracleFactory) Build(value ValidatedEvent) (any, error) {
	return p7EventOracleValue{validated: value}, nil
}

type p7SignalledTransport struct {
	events.EventTransport
	once       sync.Once
	subscribed chan struct{}
}

type p7EventObserver struct{ suppressed chan observe.Observation }

func (observer p7EventObserver) ObserveGolem(_ context.Context, observation observe.Observation) {
	if observation.Operation() == observe.OperationSubscriptionSuppression {
		select {
		case observer.suppressed <- observation:
		default:
		}
	}
}

func (transport *p7SignalledTransport) Subscribe(ctx context.Context, request events.Subscription) (events.Stream, error) {
	stream, err := transport.EventTransport.Subscribe(ctx, request)
	if err == nil {
		transport.once.Do(func() { close(transport.subscribed) })
	}
	return stream, err
}

type p7EventRuntimeFixture struct {
	app        *App[p7EventPrincipal, p7EventActor]
	transport  *p7SignalledTransport
	schema     schematest.Fixture
	descriptor golem.ModelDescriptor[p7EventPost]
	title      golem.TextField[p7EventPost, string]
	digest     golem.EventSchemaDigest
	allow      *atomic.Bool
	suppressed chan observe.Observation
	resolved   chan bool
	resolves   *atomic.Int64
}

func TestSQLiteFreshSubscriptionAuthorizationDeleteAndDuplicateOracle(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	caller, err := fixture.app.ForPrincipal(ctx, p7EventPrincipal{Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor, golem.EventSelect[p7EventPost](fixture.title))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(time.Second):
		t.Fatal("model hub did not subscribe to the transport")
	}

	created := fixture.notice(t, golem.EventCreated, 1, "visible", false)
	fixture.publish(t, created, created)
	for duplicate := 0; duplicate < 2; duplicate++ {
		event := receiveP7Event(t, stream)
		if event.validated.Metadata().EventID() != p7OracleID(1) {
			t.Fatalf("duplicate %d event ID = %x", duplicate, event.validated.Metadata().EventID())
		}
		entity, present := event.validated.Entity()
		if !present {
			t.Fatalf("duplicate %d omitted selected entity", duplicate)
		}
		value, present := golem.RuntimeTransportField(entity, fixture.schema.PostTitle).Get()
		if !present || value != "visible" {
			t.Fatalf("duplicate %d entity title = %#v present=%t", duplicate, value, present)
		}
	}

	fixture.allow.Store(false)
	for len(fixture.resolved) != 0 {
		<-fixture.resolved
	}
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 2, "visible", false))
	select {
	case allowed := <-fixture.resolved:
		if allowed {
			t.Fatal("revoked event resolved an allowed actor")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked event did not resolve a fresh principal")
	}
	select {
	case <-fixture.suppressed:
	case <-time.After(2 * time.Second):
		t.Fatal("revoked event was not freshly evaluated and suppressed")
	}
	fixture.allow.Store(true)
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 3, "visible", false))
	if event := receiveP7Event(t, stream); event.validated.Metadata().EventID() != p7OracleID(3) {
		t.Fatalf("revoked event was delivered or fresh event was lost: %x", event.validated.Metadata().EventID())
	}

	filtered, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor,
		golem.EventWhere[p7EventPost](fixture.title.Eq("visible")), golem.EventSelect[p7EventPost](fixture.title))
	if err != nil {
		t.Fatal(err)
	}
	defer filtered.Close()
	deleted := fixture.notice(t, golem.EventDeleted, 4, "visible", true)
	fixture.publish(t, deleted)
	deleteEvent := receiveP7Event(t, stream)
	if deleteEvent.validated.Metadata().Action() != golem.EventDeleted {
		t.Fatalf("delete action = %q", deleteEvent.validated.Metadata().Action())
	}
	if _, present := deleteEvent.validated.Entity(); present {
		t.Fatal("delete event exposed an entity")
	}
	for {
		select {
		case observation := <-fixture.suppressed:
			if observation.Reason() == observe.ReasonFiltered {
				goto deleteFiltered
			}
		case <-time.After(2 * time.Second):
			t.Fatal("delete with where was not suppressed")
		}
	}
deleteFiltered:
	if _, err := fixture.app.database.Exec(`INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, mutationResultUUIDText(9), mutationResultUUIDText(1), "visible"); err != nil {
		t.Fatal(err)
	}
	fixture.publish(t, fixture.notice(t, golem.EventCreated, 5, "visible", false))
	if event := receiveP7Event(t, filtered); event.validated.Metadata().EventID() != p7OracleID(5) {
		t.Fatalf("where-delete leaked before next matching create: %x", event.validated.Metadata().EventID())
	}

	bulk, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](ctx, caller, fixture.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	defer bulk.Close()
	resolvedBefore := fixture.resolves.Load()
	for index := 0; index < 500; index++ {
		id := uint16(1000 + index)
		fixture.publish(t, fixture.notice(t, golem.EventUpdated, id, "visible", false))
		if event := receiveP7Event(t, bulk); event.validated.Metadata().EventID() != p7OracleID(id) {
			t.Fatalf("fresh event %d ID = %x", index+1, event.validated.Metadata().EventID())
		}
	}
	if fixture.resolves.Load()-resolvedBefore < 500 {
		t.Fatalf("500 events did not create 500 fresh principal resolutions: before=%d after=%d", resolvedBefore, fixture.resolves.Load())
	}

	child, err := golem.RuntimeFreezeReadRequest(golem.RuntimeReadRequestInput{
		Operation: golem.ReadFindMany, Model: fixture.schema.User,
		Selection: []golem.RuntimeReadSelectionInput{{Kind: golem.RuntimeReadScalar, Field: fixture.schema.UserName}}, Projection: golem.ProjectionSelect,
	})
	if err != nil {
		t.Fatal(err)
	}
	fullRead, err := golem.RuntimeFreezeReadRequest(golem.RuntimeReadRequestInput{
		Operation: golem.ReadFindMany, Model: fixture.schema.Post,
		Selection: []golem.RuntimeReadSelectionInput{{Kind: golem.RuntimeReadRelation, Field: fixture.schema.PostAuthor, Relation: fixture.schema.Authorship, Target: fixture.schema.User, Request: &child}}, Projection: golem.ProjectionSelect,
	})
	if err != nil {
		t.Fatal(err)
	}
	relationStream, err := CallerFrozenReadEvents[p7EventPrincipal, p7EventActor, p7EventOracleValue](ctx, caller, fullRead, true)
	if err != nil {
		t.Fatal(err)
	}
	defer relationStream.Close()
	fixture.publish(t, fixture.notice(t, golem.EventUpdated, 1600, "visible", false))
	relationEvent := receiveP7Event(t, relationStream)
	entity, present := relationEvent.validated.Entity()
	if !present {
		t.Fatal("full frozen read omitted entity")
	}
	related, present := golem.RuntimeTransportField(entity, fixture.schema.PostAuthor).Get()
	if !present {
		t.Fatal("full frozen read omitted selected relation")
	}
	if _, ok := related.(golem.RuntimeModelRow); !ok {
		t.Fatalf("selected relation type = %T", related)
	}
}

func TestDeleteSuppressionDistinguishesDeniedFromUnverifiable(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
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
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(time.Second):
		t.Fatal("model hub did not subscribe")
	}

	fixture.allow.Store(false)
	fixture.publish(t, fixture.notice(t, golem.EventDeleted, 700, "visible", false))
	if reason := receiveP7Suppression(t, fixture.suppressed); reason != observe.ReasonAuthorization {
		t.Fatalf("denied delete suppression=%q", reason)
	}

	fixture.allow.Store(true)
	fixture.publish(t, fixture.unverifiableDeleteNotice(t, 701, "visible"))
	if reason := receiveP7Suppression(t, fixture.suppressed); reason != observe.ReasonDeletionUnverifiable {
		t.Fatalf("unverifiable delete suppression=%q", reason)
	}
}

func receiveP7Suppression(t testing.TB, observations <-chan observe.Observation) observe.Reason {
	t.Helper()
	select {
	case observation := <-observations:
		return observation.Reason()
	case <-time.After(2 * time.Second):
		t.Fatal("suppression observation was not received")
		return ""
	}
}

func TestInitialReadRefusalStartsNoHubOrTransport(t *testing.T) {
	fixture := newP7EventRuntimeFixture(t)
	fixture.allow.Store(false)
	caller, err := fixture.app.ForPrincipal(context.Background(), p7EventPrincipal{Subject: "denied"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerEvents[p7EventPrincipal, p7EventActor, p7EventPost, p7EventOracleValue](context.Background(), caller, fixture.descriptor); err == nil {
		t.Fatal("model-read-denied subscription was accepted")
	}
	select {
	case <-fixture.transport.subscribed:
		t.Fatal("initial refusal started the transport source")
	case <-time.After(25 * time.Millisecond):
	}
	fixture.app.eventMu.Lock()
	hubs := len(fixture.app.eventHubs)
	fixture.app.eventMu.Unlock()
	if hubs != 0 {
		t.Fatalf("initial refusal created %d hubs", hubs)
	}
}

func receiveP7Event(t *testing.T, stream golem.EventStream[p7EventOracleValue]) p7EventOracleValue {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := stream.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newP7EventRuntimeFixture(t *testing.T) p7EventRuntimeFixture {
	t.Helper()
	ctx := context.Background()
	schemaFixture := schematest.NewSubscribedIndexed(t)
	provider := sqlite.New()
	database, _, err := provider.Open(ctx, "file:"+t.TempDir()+"/p7-events.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := provider.ApplyInitial(ctx, database, schemaFixture.SQLite); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, mutationResultUUIDText(1), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, mutationResultUUIDText(9), mutationResultUUIDText(1), "visible"); err != nil {
		t.Fatal(err)
	}

	userIdentity := golem.GeneratedIdentityMetadata(schemaFixture.User, schemaFixture.UserKey, golem.PrimaryIdentity, schemaFixture.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schemaFixture.Post, schemaFixture.PostKey, golem.PrimaryIdentity, schemaFixture.PostID)
	userRelation := golem.GeneratedRelationMetadata(schemaFixture.User, schemaFixture.Post, schemaFixture.UserPosts, schemaFixture.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schemaFixture.Post, schemaFixture.User, schemaFixture.PostAuthor, schemaFixture.Authorship, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[p7EventUser](schemaFixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.UserID, schemaFixture.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation},
	))
	postDescriptor := golem.GeneratedModelDescriptor[p7EventPost](schemaFixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.PostID, schemaFixture.AuthorID, schemaFixture.PostTitle}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation},
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(schemaFixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	allow := &atomic.Bool{}
	allow.Store(true)
	allowUsers := golem.GeneratedPolicyBinding[p7EventActor, p7EventUser](schemaFixture.User, func(p7EventActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[p7EventUser]()
		rules.CanRead(golem.All[p7EventUser]())
		return rules.Freeze(schemaFixture.User)
	})
	allowPosts := golem.GeneratedPolicyBinding[p7EventActor, p7EventPost](schemaFixture.Post, func(actor p7EventActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[p7EventPost]()
		if actor.Allow {
			rules.CanRead(golem.All[p7EventPost]())
		}
		return rules.Freeze(schemaFixture.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schemaFixture.Bundle.GenerationDigest(), []golem.PolicyBinding[p7EventActor]{allowUsers, allowPosts}, nil))
	if err != nil {
		t.Fatal(err)
	}
	model, _ := schemaFixture.Registry.Model(schemaFixture.Post)
	fingerprint, _, _ := model.EventSchema()
	parsed, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	digest := golem.EventSchemaDigest(parsed)
	eventRegistry, err := golem.GeneratedEventRegistry(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedPackageEventRegistry(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedEventModelMetadata(schemaFixture.Post, digest, []golem.FieldID{schemaFixture.PostID}, "p7EventOracleValue", "golem.UUID")))
	if err != nil {
		t.Fatal(err)
	}
	factories, err := GeneratedEventFactoryRegistry(schemaFixture.Bundle.GenerationDigest(), GeneratedPackageEventFactories(schemaFixture.Bundle.GenerationDigest(), p7EventOracleFactory{model: schemaFixture.Post, schema: digest}))
	if err != nil {
		t.Fatal(err)
	}
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	transport := &p7SignalledTransport{EventTransport: memory, subscribed: make(chan struct{})}
	suppressed := make(chan observe.Observation, 4)
	resolved := make(chan bool, 16)
	resolves := &atomic.Int64{}
	app, err := Open(ctx, Config[p7EventPrincipal, p7EventActor]{
		Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: schemaFixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		EventRegistry: eventRegistry, EventFactories: factories, EventTransport: transport, Observer: p7EventObserver{suppressed: suppressed},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		ResolvePrincipal: func(context.Context, p7EventPrincipal) (p7EventActor, error) {
			resolves.Add(1)
			allowed := allow.Load()
			select {
			case resolved <- allowed:
			default:
			}
			return p7EventActor{Allow: allowed}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p7EventRuntimeFixture{app: app, transport: transport, schema: schemaFixture, descriptor: postDescriptor, title: golem.GeneratedTextField[p7EventPost, string](schemaFixture.PostTitle), digest: digest, allow: allow, suppressed: suppressed, resolved: resolved, resolves: resolves}
}

func p7OracleID(id uint16) golem.EventID {
	var result golem.EventID
	result[14], result[15] = byte(id>>8), byte(id)
	return result
}

func (fixture p7EventRuntimeFixture) notice(t *testing.T, action golem.EventAction, id uint16, title string, deleteRow bool) events.Notice {
	return fixture.noticeWithDeleteState(t, action, id, title, deleteRow, mutationir.DeleteSnapshotStoredScalars)
}

func (fixture p7EventRuntimeFixture) unverifiableDeleteNotice(t *testing.T, id uint16, title string) events.Notice {
	return fixture.noticeWithDeleteState(t, golem.EventDeleted, id, title, false, mutationir.DeleteSnapshotUnverifiable)
}

func (fixture p7EventRuntimeFixture) noticeWithDeleteState(t *testing.T, action golem.EventAction, id uint16, title string, deleteRow bool, deleteState mutationir.DeleteSnapshotState) events.Notice {
	t.Helper()
	titleValue, err := policyir.StringValue(title)
	if err != nil {
		t.Fatal(err)
	}
	row, err := mutationdecode.NewCompleteRow(fixture.schema.Registry, policyir.ModelID(fixture.schema.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue([16]byte{15: 9})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.AuthorID), policyir.UUIDValue([16]byte{15: 1})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostTitle), titleValue),
	})
	if err != nil {
		t.Fatal(err)
	}
	var requirement mutationir.FactRequirement
	if action == golem.EventDeleted {
		var storedScalars []policyir.FieldID
		if deleteState == mutationir.DeleteSnapshotStoredScalars {
			storedScalars = []policyir.FieldID{policyir.FieldID(fixture.schema.PostID), policyir.FieldID(fixture.schema.AuthorID), policyir.FieldID(fixture.schema.PostTitle)}
		}
		requirement, err = mutationir.NewDeleteFactRequirement(
			[]policyir.FieldID{policyir.FieldID(fixture.schema.PostID)}, deleteState, storedScalars,
		)
	} else {
		factAction := mutationir.FactCreated
		before := []policyir.FieldID(nil)
		if action == golem.EventUpdated {
			factAction = mutationir.FactUpdated
			before = []policyir.FieldID{policyir.FieldID(fixture.schema.PostID)}
		}
		requirement, err = mutationir.NewFactRequirement(factAction, before, []policyir.FieldID{policyir.FieldID(fixture.schema.PostID)}, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	requirement, err = requirement.WithEventSchema([32]byte(fixture.digest))
	if err != nil {
		t.Fatal(err)
	}
	var before, after *mutationdecode.Row
	if action == golem.EventDeleted {
		before = &row
		if deleteRow {
			if _, err := fixture.app.database.Exec(`DELETE FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(9)); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		after = &row
		if action == golem.EventUpdated {
			before = &row
		}
	}
	eventID := p7OracleID(id)
	causationID := golem.CausationID(eventID)
	fact, err := mutationfact.NewV2(fixture.schema.Registry, golem.SchemaDigest(fixture.digest), mutationfact.EventID(eventID), requirement, mutationfact.CausationID(causationID), 1, before, after)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := fact.OutboxRow(time.Unix(int64(id), 0))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := eventcodec.EncodeStoredRow(stored, fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	notice, err := eventvalue.NewRoutedNotice(envelope.EventID(), envelope.GenerationDigest(), envelope.ResolvedEventSchemaDigest(), envelope.ModelID(), envelope.Action(), envelope.CausationID(), envelope.TransactionOrdinal(), envelope.Encoded())
	if err != nil {
		t.Fatal(err)
	}
	return notice
}

func (fixture p7EventRuntimeFixture) publish(t *testing.T, notices ...events.Notice) {
	t.Helper()
	if len(notices) == 0 {
		t.Fatal("publish requires notices")
	}
	// One causal batch requires contiguous ordinals. Repeated notices deliberately
	// model at-least-once transport recovery, so publish them as separate batches.
	for _, notice := range notices {
		batch, err := eventvalue.NewEventBatch(notice.CausationID(), []events.Notice{notice})
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.transport.Publish(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
	}
}
