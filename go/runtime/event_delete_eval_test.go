package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

type deleteEvalPrincipal struct{ Name string }
type deleteEvalActor struct{ Name string }
type deleteEvalUser struct{}
type deleteEvalPost struct{}
type deleteEvalComment struct{}

type deleteEvalFields struct {
	userID       golem.EqualField[deleteEvalUser, golem.UUID]
	userName     golem.TextField[deleteEvalUser, string]
	postID       golem.EqualField[deleteEvalPost, golem.UUID]
	postAuthorID golem.EqualField[deleteEvalPost, golem.UUID]
	postTitle    golem.TextField[deleteEvalPost, string]
	postAuthor   golem.ToOne[deleteEvalPost, deleteEvalUser]
	commentID    golem.EqualField[deleteEvalComment, golem.UUID]
	commentPost  golem.ToOne[deleteEvalComment, deleteEvalPost]
	commentPID   golem.EqualField[deleteEvalComment, golem.UUID]
	commentBody  golem.TextField[deleteEvalComment, string]
}

type deleteEvalPolicies struct {
	user    func(deleteEvalFields, deleteEvalActor) (golem.Predicate[deleteEvalUser], bool)
	post    func(deleteEvalFields, deleteEvalActor) (golem.Predicate[deleteEvalPost], bool)
	comment func(deleteEvalFields, deleteEvalActor) (golem.Predicate[deleteEvalComment], bool)
	hook    func(deleteEvalFields) golem.Predicate[deleteEvalComment]
}

type deleteEvalFixture struct {
	app       *App[deleteEvalPrincipal, deleteEvalActor]
	schema    schematest.GraphFixture
	fields    deleteEvalFields
	comments  golem.ModelDescriptor[deleteEvalComment]
	posts     golem.ModelDescriptor[deleteEvalPost]
	digests   map[golem.ModelID]golem.EventSchemaDigest
	transport *p7SignalledTransport
	observed  *deleteSnapshotObservations
}

const (
	deleteEvalAlice     byte = 1
	deleteEvalBob       byte = 2
	deleteEvalPublished byte = 10
	deleteEvalDraft     byte = 11
	deleteEvalMissing   byte = 99
	deleteEvalOnPub     byte = 20
	deleteEvalOnDraft   byte = 21
)

func deleteEvalUUID(last byte) golem.UUID {
	return golem.UUID{15: last}
}

func runDeleteEvalProfiles(t *testing.T, policies deleteEvalPolicies, run func(*testing.T, deleteEvalFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		schemaFixture := schematest.NewSubscribedGraph(t)
		provider := sqlite.New()
		database, _, err := provider.Open(context.Background(), "file:"+t.TempDir()+"/delete-eval.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := provider.ApplyInitial(context.Background(), database, schemaFixture.SQLite); err != nil {
			t.Fatal(err)
		}
		run(t, newDeleteEvalFixture(t, database, golem.SQLite, schemaFixture, policies))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			namespace := physical.PhysicalName(fmt.Sprintf("golem_delete_eval_%s_%d_%d", profile.name, os.Getpid(), sequence))
			systemNamespace := physical.PhysicalName(string(namespace) + "_system")
			schemaFixture := schematest.NewSubscribedGraphPostgreSQLNamespaces(t, namespace, systemNamespace)
			provider := postgresprovider.New()
			database, _, err := provider.Open(context.Background(), profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteAcceptanceIdentifier(string(namespace))+` CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteAcceptanceIdentifier(string(systemNamespace))+` CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(context.Background(), database, schemaFixture.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			run(t, newDeleteEvalFixture(t, database, golem.PostgreSQL, schemaFixture, policies))
		})
	}
}

func newDeleteEvalFixture(t *testing.T, database *sqlx.DB, provider golem.Provider, schemaFixture schematest.GraphFixture, policies deleteEvalPolicies) deleteEvalFixture {
	t.Helper()
	ctx := context.Background()
	fields := deleteEvalFields{
		userID:       golem.GeneratedEqualField[deleteEvalUser, golem.UUID](schemaFixture.UserID),
		userName:     golem.GeneratedTextField[deleteEvalUser, string](schemaFixture.UserName),
		postID:       golem.GeneratedEqualField[deleteEvalPost, golem.UUID](schemaFixture.PostID),
		postAuthorID: golem.GeneratedEqualField[deleteEvalPost, golem.UUID](schemaFixture.AuthorID),
		postTitle:    golem.GeneratedTextField[deleteEvalPost, string](schemaFixture.PostTitle),
		postAuthor:   golem.GeneratedToOne[deleteEvalPost, deleteEvalUser](schemaFixture.PostAuthor, schemaFixture.Authorship, schemaFixture.User),
		commentID:    golem.GeneratedEqualField[deleteEvalComment, golem.UUID](schemaFixture.CommentID),
		commentPost:  golem.GeneratedToOne[deleteEvalComment, deleteEvalPost](schemaFixture.CommentPost, schemaFixture.Commenting, schemaFixture.Post),
		commentPID:   golem.GeneratedEqualField[deleteEvalComment, golem.UUID](schemaFixture.CommentPostID),
		commentBody:  golem.GeneratedTextField[deleteEvalComment, string](schemaFixture.CommentBody),
	}
	userIdentity := golem.GeneratedIdentityMetadata(schemaFixture.User, schemaFixture.UserKey, golem.PrimaryIdentity, schemaFixture.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schemaFixture.Post, schemaFixture.PostKey, golem.PrimaryIdentity, schemaFixture.PostID)
	commentIdentity := golem.GeneratedIdentityMetadata(schemaFixture.Comment, schemaFixture.CommentKey, golem.PrimaryIdentity, schemaFixture.CommentID)
	userDescriptor := golem.GeneratedModelDescriptor[deleteEvalUser](schemaFixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.UserID, schemaFixture.UserName}, nil, []golem.IdentityMetadata{userIdentity},
		[]golem.RelationMetadata{golem.GeneratedRelationMetadata(schemaFixture.User, schemaFixture.Post, schemaFixture.UserPosts, schemaFixture.Authorship, golem.RelationInverse, golem.RelationToMany)},
	))
	postDescriptor := golem.GeneratedModelDescriptor[deleteEvalPost](schemaFixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.PostID, schemaFixture.AuthorID, schemaFixture.PostTitle}, nil, []golem.IdentityMetadata{postIdentity},
		[]golem.RelationMetadata{
			golem.GeneratedRelationMetadata(schemaFixture.Post, schemaFixture.User, schemaFixture.PostAuthor, schemaFixture.Authorship, golem.RelationSource, golem.RelationToOne),
			golem.GeneratedRelationMetadata(schemaFixture.Post, schemaFixture.Comment, schemaFixture.PostComments, schemaFixture.Commenting, golem.RelationInverse, golem.RelationToMany),
		},
	))
	commentDescriptor := golem.GeneratedModelDescriptor[deleteEvalComment](schemaFixture.Comment, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.CommentID, schemaFixture.CommentPostID, schemaFixture.CommentBody}, nil, []golem.IdentityMetadata{commentIdentity},
		[]golem.RelationMetadata{golem.GeneratedRelationMetadata(schemaFixture.Comment, schemaFixture.Post, schemaFixture.CommentPost, schemaFixture.Commenting, golem.RelationSource, golem.RelationToOne)},
	))
	generation := schemaFixture.Bundle.GenerationDigest()
	descriptors, err := golem.GeneratedApplicationDescriptors(generation, golem.GeneratedStampedPackageDescriptors(generation, userDescriptor.Metadata(), postDescriptor.Metadata(), commentDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	userBinding := golem.GeneratedPolicyBinding[deleteEvalActor, deleteEvalUser](schemaFixture.User, func(actor deleteEvalActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[deleteEvalUser]()
		if policies.user == nil {
			rules.CanRead(golem.All[deleteEvalUser]())
		} else if predicate, ok := policies.user(fields, actor); ok {
			rules.CanRead(predicate)
		}
		return rules.Freeze(schemaFixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[deleteEvalActor, deleteEvalPost](schemaFixture.Post, func(actor deleteEvalActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[deleteEvalPost]()
		if policies.post == nil {
			rules.CanRead(golem.All[deleteEvalPost]())
		} else if predicate, ok := policies.post(fields, actor); ok {
			rules.CanRead(predicate)
		}
		return rules.Freeze(schemaFixture.Post)
	})
	commentBinding := golem.GeneratedPolicyBinding[deleteEvalActor, deleteEvalComment](schemaFixture.Comment, func(actor deleteEvalActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[deleteEvalComment]()
		if policies.comment == nil {
			rules.CanRead(golem.All[deleteEvalComment]())
		} else if predicate, ok := policies.comment(fields, actor); ok {
			rules.CanRead(predicate)
		}
		return rules.Freeze(schemaFixture.Comment)
	})
	var hooks []golem.HookBinding[deleteEvalActor]
	if policies.hook != nil {
		hooks = append(hooks, golem.GeneratedBeforeHookBinding[deleteEvalActor, deleteEvalComment, golem.FindManyHookRequest[deleteEvalComment]](schemaFixture.Comment, golem.HookFindMany, func(_ context.Context, request *golem.FindManyHookRequest[deleteEvalComment]) error {
			request.AppendOptions(golem.Where(policies.hook(fields)))
			return nil
		}))
	}
	bindings, err := golem.GeneratedApplicationBindings(generation, golem.GeneratedStampedPackageBindings(generation, []golem.PolicyBinding[deleteEvalActor]{userBinding, postBinding, commentBinding}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	digests := make(map[golem.ModelID]golem.EventSchemaDigest)
	var metadata []golem.EventModelMetadata
	var factories []EventFactory
	for _, entry := range []struct {
		model    golem.ModelID
		identity golem.FieldID
		name     string
	}{{schemaFixture.User, schemaFixture.UserID, "deleteEvalUserEvent"}, {schemaFixture.Post, schemaFixture.PostID, "deleteEvalPostEvent"}, {schemaFixture.Comment, schemaFixture.CommentID, "deleteEvalCommentEvent"}} {
		model, _ := schemaFixture.Registry.Model(entry.model)
		fingerprint, _, _ := model.EventSchema()
		parsed, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		digests[entry.model] = golem.EventSchemaDigest(parsed)
		metadata = append(metadata, golem.GeneratedEventModelMetadata(entry.model, digests[entry.model], []golem.FieldID{entry.identity}, entry.name, "golem.UUID"))
		factories = append(factories, p7EventOracleFactory{model: entry.model, schema: digests[entry.model]})
	}
	eventRegistry, err := golem.GeneratedEventRegistry(generation, golem.GeneratedPackageEventRegistry(generation, metadata...))
	if err != nil {
		t.Fatal(err)
	}
	factoryRegistry, err := GeneratedEventFactoryRegistry(generation, GeneratedPackageEventFactories(generation, factories...))
	if err != nil {
		t.Fatal(err)
	}
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 64})
	if err != nil {
		t.Fatal(err)
	}
	transport := &p7SignalledTransport{EventTransport: memory, subscribed: make(chan struct{})}
	app, err := Open(ctx, Config[deleteEvalPrincipal, deleteEvalActor]{
		Database: p8RuntimeTestDatabase(database, provider), Bundle: schemaFixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		EventRegistry: eventRegistry, EventFactories: factoryRegistry, EventTransport: transport,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		ResolvePrincipal: func(_ context.Context, principal deleteEvalPrincipal) (deleteEvalActor, error) {
			return deleteEvalActor(principal), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	observed := &deleteSnapshotObservations{}
	app.eventObserver = observed
	fixture := deleteEvalFixture{app: app, schema: schemaFixture, fields: fields, comments: commentDescriptor, posts: postDescriptor, digests: digests, transport: transport, observed: observed}
	for _, user := range []struct {
		id   byte
		name string
	}{{deleteEvalAlice, "alice"}, {deleteEvalBob, "bob"}} {
		fixture.exec(t, `INSERT INTO `+nestedAcceptanceTable(app, schemaFixture.User)+`("id","name") VALUES (?,?)`, mutationResultUUIDText(user.id), user.name)
	}
	for _, post := range []struct {
		id, author byte
		title      string
	}{{deleteEvalPublished, deleteEvalAlice, "published"}, {deleteEvalDraft, deleteEvalBob, "draft"}} {
		fixture.exec(t, `INSERT INTO `+nestedAcceptanceTable(app, schemaFixture.Post)+`("id","author_id","title") VALUES (?,?,?)`, mutationResultUUIDText(post.id), mutationResultUUIDText(post.author), post.title)
	}
	for _, comment := range []struct {
		id, post byte
		body     string
	}{{deleteEvalOnPub, deleteEvalPublished, "hello"}, {deleteEvalOnDraft, deleteEvalDraft, "hi"}} {
		fixture.exec(t, `INSERT INTO `+nestedAcceptanceTable(app, schemaFixture.Comment)+`("id","post_id","body") VALUES (?,?,?)`, mutationResultUUIDText(comment.id), mutationResultUUIDText(comment.post), comment.body)
	}
	return fixture
}

func (fixture deleteEvalFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := fixture.app.database.ExecContext(context.Background(), fixture.app.database.Rebind(query), args...); err != nil {
		t.Fatal(err)
	}
}

func (fixture deleteEvalFixture) removeComment(t *testing.T, id byte) {
	t.Helper()
	fixture.exec(t, `DELETE FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+` WHERE "id" = ?`, mutationResultUUIDText(id))
}

func (fixture deleteEvalFixture) cascadePost(t *testing.T, id byte) {
	t.Helper()
	fixture.exec(t, `DELETE FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+` WHERE "post_id" = ?`, mutationResultUUIDText(id))
	fixture.exec(t, `DELETE FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Post)+` WHERE "id" = ?`, mutationResultUUIDText(id))
}

func (fixture deleteEvalFixture) subscribeComments(t *testing.T, actor string, options ...golem.EventOption[deleteEvalComment]) golem.EventStream[p7EventOracleValue] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	caller, err := fixture.app.ForPrincipal(ctx, deleteEvalPrincipal{Name: actor})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := CallerEvents[deleteEvalPrincipal, deleteEvalActor, deleteEvalComment, p7EventOracleValue](ctx, caller, fixture.comments, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("model hub did not subscribe")
	}
	return stream
}

func (fixture deleteEvalFixture) liveCommentVisible(t *testing.T, actor string, comment byte) bool {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), deleteEvalPrincipal{Name: actor})
	if err != nil {
		t.Fatal(err)
	}
	count, err := CallerCount(context.Background(), caller, fixture.comments, golem.Where(fixture.fields.commentID.Eq(deleteEvalUUID(comment))))
	if err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func (fixture deleteEvalFixture) deleteComment(t *testing.T, eventID uint16, comment, post byte) {
	t.Helper()
	cells := []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentID), policyir.UUIDValue([16]byte{15: comment})),
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentPostID), policyir.UUIDValue([16]byte{15: post})),
	}
	body, err := policyir.StringValue("hello")
	if err != nil {
		t.Fatal(err)
	}
	cells = append(cells, mutationdecode.Value(policyir.FieldID(fixture.schema.CommentBody), body))
	fixture.publishDelete(t, fixture.schema.Comment, fixture.schema.CommentID, eventID, cells)
}

func (fixture deleteEvalFixture) publishDelete(t *testing.T, model golem.ModelID, identity golem.FieldID, eventID uint16, cells []mutationdecode.Cell) {
	t.Helper()
	row, err := mutationdecode.NewCompleteRow(fixture.schema.Registry, policyir.ModelID(model), cells)
	if err != nil {
		t.Fatal(err)
	}
	registered, _ := fixture.schema.Registry.Model(model)
	_, declared, _ := registered.EventSchema()
	stored := make([]policyir.FieldID, len(declared))
	for index, field := range declared {
		stored[index] = policyir.FieldID(field)
	}
	requirement, err := mutationir.NewDeleteFactRequirement([]policyir.FieldID{policyir.FieldID(identity)}, mutationir.DeleteSnapshotStoredScalars, stored)
	if err != nil {
		t.Fatal(err)
	}
	digest := fixture.digests[model]
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		t.Fatal(err)
	}
	id := p7OracleID(eventID)
	fact, err := mutationfact.NewV2(fixture.schema.Registry, golem.SchemaDigest(digest), mutationfact.EventID(id), requirement, mutationfact.CausationID(golem.CausationID(id)), 1, &row, nil)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := fact.OutboxRow(time.Unix(int64(eventID), 0))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := eventcodec.EncodeStoredRow(outbox, fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	notice, err := eventvalue.NewRoutedNotice(envelope.EventID(), envelope.GenerationDigest(), envelope.ResolvedEventSchemaDigest(), envelope.ModelID(), envelope.Action(), envelope.CausationID(), envelope.TransactionOrdinal(), envelope.Encoded())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := eventvalue.NewEventBatch(notice.CausationID(), []events.Notice{notice})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.transport.Publish(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func expectDeleteDelivered(t *testing.T, stream golem.EventStream[p7EventOracleValue], eventID uint16) {
	t.Helper()
	event := receiveP7Event(t, stream)
	if event.validated.Metadata().EventID() != p7OracleID(eventID) || event.validated.Metadata().Action() != golem.EventDeleted {
		t.Fatalf("received %x %q, want delete %x", event.validated.Metadata().EventID(), event.validated.Metadata().Action(), p7OracleID(eventID))
	}
	if _, present := event.validated.Entity(); present {
		t.Fatal("delete event exposed an entity")
	}
}

func expectDeleteResync(t *testing.T, stream golem.EventStream[p7EventOracleValue]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := stream.Recv(ctx)
	if err == nil {
		t.Fatalf("stream delivered %x instead of ending with RESYNC", value.validated.Metadata().EventID())
	}
	if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionResync {
		t.Fatalf("stream ended with %v, want %s", err, events.CodeSubscriptionResync)
	}
}

func expectDeleteSilent(t *testing.T, stream golem.EventStream[p7EventOracleValue]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	value, err := stream.Recv(ctx)
	if err == nil {
		t.Fatalf("suppressed delete delivered %x", value.validated.Metadata().EventID())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionCancelled {
			t.Fatalf("suppressed delete ended the stream: %v", err)
		}
	}
}

func (fixture deleteEvalFixture) awaitSuppression(t *testing.T, reason events.SuppressionReason) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, value := range fixture.observed.kind(events.ObservationSuppression) {
			if value.SuppressionReason() == reason && value.Action() == golem.EventDeleted {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("delete suppression %q was not observed: %#v", reason, fixture.observed.kind(events.ObservationSuppression))
}

func (fixture deleteEvalFixture) suppressions(reason events.SuppressionReason) int {
	count := 0
	for _, value := range fixture.observed.kind(events.ObservationSuppression) {
		if value.SuppressionReason() == reason {
			count++
		}
	}
	return count
}

func TestDeleteEventOverOneHopArrives(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postTitle.Eq("published")), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		if !fixture.liveCommentVisible(t, "alice", deleteEvalOnPub) || fixture.liveCommentVisible(t, "alice", deleteEvalOnDraft) {
			t.Fatal("live read disagrees with the one-hop policy")
		}
		fixture.removeComment(t, deleteEvalOnDraft)
		fixture.deleteComment(t, 1, deleteEvalOnDraft, deleteEvalDraft)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.deleteComment(t, 2, deleteEvalOnPub, deleteEvalPublished)
		expectDeleteDelivered(t, stream, 2)
	})
}

func TestDeleteEventArrivesWhenParentPolicyIsRelationalAndUnstated(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		post: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalPost], bool) {
			return fields.postAuthor.Is(fields.userName.Eq(actor.Name)), true
		},
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postTitle.Eq("published")), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		alice := fixture.subscribeComments(t, "alice")
		bob := fixture.subscribeComments(t, "bob")
		if !fixture.liveCommentVisible(t, "alice", deleteEvalOnPub) || fixture.liveCommentVisible(t, "bob", deleteEvalOnPub) {
			t.Fatal("live read does not apply the relational post policy inside the hop")
		}
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.deleteComment(t, 3, deleteEvalOnPub, deleteEvalPublished)
		expectDeleteDelivered(t, alice, 3)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, bob)
	})
}

func TestDeleteEventForCascadedParentResyncs(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postTitle.Eq("published")), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		fixture.deleteComment(t, 4, deleteEvalOnPub, deleteEvalPublished)
		expectDeleteResync(t, stream)
		fixture.awaitSuppression(t, events.SuppressionDeletionUnverifiable)
	})
}

func TestDeleteEventNegatedHopOverCascadedParentNeverDelivers(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			owner := deleteEvalUUID(deleteEvalAlice)
			if actor.Name == "bob" {
				owner = deleteEvalUUID(deleteEvalBob)
			}
			return golem.Not(fields.commentPost.Is(fields.postAuthorID.Eq(owner))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		if fixture.liveCommentVisible(t, "alice", deleteEvalOnPub) || !fixture.liveCommentVisible(t, "alice", deleteEvalOnDraft) {
			t.Fatal("live read disagrees with the negated hop")
		}
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.deleteComment(t, 5, deleteEvalOnPub, deleteEvalPublished)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		fixture.removeComment(t, deleteEvalOnDraft)
		fixture.deleteComment(t, 6, deleteEvalOnDraft, deleteEvalDraft)
		expectDeleteDelivered(t, stream, 6)
		fixture.cascadePost(t, deleteEvalPublished)
		fixture.deleteComment(t, 7, deleteEvalOnPub, deleteEvalPublished)
		expectDeleteResync(t, stream)
	})
}

func TestDeleteEventLocallyDeniedSubscriberGetsNothing(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPID.Eq(deleteEvalUUID(deleteEvalDraft)).And(fields.commentPost.Is(fields.postTitle.Eq("draft"))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.cascadePost(t, deleteEvalPublished)
		fixture.deleteComment(t, 8, deleteEvalOnPub, deleteEvalPublished)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		expectDeleteSilent(t, stream)
		fixture.removeComment(t, deleteEvalOnDraft)
		fixture.deleteComment(t, 9, deleteEvalOnDraft, deleteEvalDraft)
		expectDeleteDelivered(t, stream, 9)
		if fixture.suppressions(events.SuppressionDeletionUnverifiable) != 0 {
			t.Fatal("locally denied delete was reported unverifiable")
		}
	})
}

func TestDeleteEventMatchesLiveReadWhenParentIsUnreadable(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		post: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalPost], bool) {
			return fields.postTitle.Eq("published"), true
		},
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.IsNotNull(), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		if fixture.liveCommentVisible(t, "alice", deleteEvalOnDraft) || !fixture.liveCommentVisible(t, "alice", deleteEvalOnPub) {
			t.Fatal("live read does not hide comments under an unreadable post")
		}
		fixture.removeComment(t, deleteEvalOnDraft)
		fixture.deleteComment(t, 10, deleteEvalOnDraft, deleteEvalDraft)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.deleteComment(t, 11, deleteEvalOnPub, deleteEvalPublished)
		expectDeleteDelivered(t, stream, 11)
		if fixture.suppressions(events.SuppressionDeletionUnverifiable) != 0 {
			t.Fatal("unreadable parent was reported unverifiable instead of denied")
		}
	})
}

func TestDeleteEventFilterOutsideSnapshotOverDelivers(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice", golem.EventWhere[deleteEvalComment](fixture.fields.commentBody.Eq("hello")))
		fixture.removeComment(t, deleteEvalOnDraft)
		fixture.deleteComment(t, 12, deleteEvalOnDraft, deleteEvalDraft)
		expectDeleteDelivered(t, stream, 12)
		deadline := time.Now().Add(2 * time.Second)
		for {
			found := false
			for _, value := range fixture.observed.kind(events.ObservationDelivery) {
				if value.SuppressionReason() == events.SuppressionDeleteOverDelivered && value.Action() == golem.EventDeleted && value.ModelID() == fixture.schema.Comment {
					found = true
				}
			}
			if found {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("over-delivered delete was not reported")
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

type deleteEvalUnifiedCapture struct{ values []observe.Observation }

func (capture *deleteEvalUnifiedCapture) ObserveGolem(_ context.Context, value observe.Observation) {
	capture.values = append(capture.values, value)
}

func TestOverDeliveredDeleteMapsToDeletionUnverifiableDelivery(t *testing.T) {
	capture := &deleteEvalUnifiedCapture{}
	events.Observe(adaptEventObserver(capture, golem.SQLite), context.Background(), golem.ModelID{15: 1}, golem.EventDeleted, events.ObservationDelivery, events.OutcomeSuccess, events.SuppressionDeleteOverDelivered, 0, 0, 0, 0, 1)
	if len(capture.values) != 1 {
		t.Fatalf("unified observations=%d", len(capture.values))
	}
	value := capture.values[0]
	if value.Reason() != observe.ReasonDeletionUnverifiable || value.Outcome() != observe.OutcomeSuccess || value.Operation() != observe.OperationSubscriptionDelivery {
		t.Fatalf("over-delivered delete mapped to reason=%q outcome=%q operation=%q", value.Reason(), value.Outcome(), value.Operation())
	}
}

func TestDeleteEventTwoHopPositivePolicyDelivers(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postAuthor.Is(fields.userName.Eq("alice"))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "bob")
		fixture.removeComment(t, deleteEvalOnDraft)
		fixture.deleteComment(t, 60, deleteEvalOnDraft, deleteEvalDraft)
		fixture.awaitSuppression(t, events.SuppressionUnauthorized)
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.deleteComment(t, 61, deleteEvalOnPub, deleteEvalPublished)
		expectDeleteDelivered(t, stream, 61)
	})
}

func TestDeleteEventNegatedDeepHopOverDetachedTargetNeverDelivers(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return golem.Not(fields.commentPost.Is(fields.postAuthor.Is(fields.userName.Eq("alice")))), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "bob")
		if fixture.liveCommentVisible(t, "bob", deleteEvalOnPub) {
			t.Fatal("live read shows a comment under alice's post")
		}
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.exec(t, `DELETE FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.User)+` WHERE "id" = ?`, mutationResultUUIDText(deleteEvalAlice))
		fixture.deleteComment(t, 62, deleteEvalOnPub, deleteEvalPublished)
		expectDeleteResync(t, stream)
	})
}

func TestDeleteEventDeepIsNullOverDetachedTargetNeverDelivers(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postAuthor.IsNull()), true
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "bob")
		if fixture.liveCommentVisible(t, "bob", deleteEvalOnPub) {
			t.Fatal("live read shows a comment whose post has an author")
		}
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.exec(t, `DELETE FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.User)+` WHERE "id" = ?`, mutationResultUUIDText(deleteEvalAlice))
		fixture.deleteComment(t, 63, deleteEvalOnPub, deleteEvalPublished)
		expectDeleteResync(t, stream)
	})
}

func TestDeleteEventCombinesUnknownWithKleeneLogic(t *testing.T) {
	known := func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
		return fields.commentPID.Eq(deleteEvalUUID(deleteEvalPublished))
	}
	denied := func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
		return fields.commentPID.Eq(deleteEvalUUID(deleteEvalMissing))
	}
	unknown := func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
		return fields.commentBody.Eq("hello")
	}
	cases := []struct {
		name   string
		policy func(deleteEvalFields) golem.Predicate[deleteEvalComment]
		want   string
	}{
		{"true-and-unknown", func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return known(fields).And(unknown(fields))
		}, "resync"},
		{"false-and-unknown", func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return denied(fields).And(unknown(fields))
		}, "unauthorized"},
		{"true-or-unknown", func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return known(fields).Or(unknown(fields))
		}, "deliver"},
		{"false-or-unknown", func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return denied(fields).Or(unknown(fields))
		}, "resync"},
		{"not-unknown", func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return golem.Not(unknown(fields))
		}, "resync"},
		{"not-true-and-unknown", func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return golem.Not(known(fields).And(unknown(fields)))
		}, "resync"},
		{"not-false-and-unknown", func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return golem.Not(denied(fields).And(unknown(fields)))
		}, "deliver"},
	}
	for _, entry := range cases {
		entry := entry
		t.Run(entry.name, func(t *testing.T) {
			runDeleteEvalProfiles(t, deleteEvalPolicies{
				comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
					return entry.policy(fields), true
				},
			}, func(t *testing.T, fixture deleteEvalFixture) {
				stream := fixture.subscribeComments(t, "alice")
				fixture.removeComment(t, deleteEvalOnPub)
				fixture.deleteComment(t, 50, deleteEvalOnPub, deleteEvalPublished)
				switch entry.want {
				case "deliver":
					expectDeleteDelivered(t, stream, 50)
				case "unauthorized":
					fixture.awaitSuppression(t, events.SuppressionUnauthorized)
					expectDeleteSilent(t, stream)
				default:
					expectDeleteResync(t, stream)
				}
			})
		})
	}
}

func TestDeleteEventHonoursReadBeforeHookFilter(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{
		hook: func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return fields.commentPID.Eq(deleteEvalUUID(deleteEvalPublished))
		},
	}, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.removeComment(t, deleteEvalOnDraft)
		fixture.deleteComment(t, 13, deleteEvalOnDraft, deleteEvalDraft)
		fixture.awaitSuppression(t, events.SuppressionDeleteFiltered)
		fixture.removeComment(t, deleteEvalOnPub)
		fixture.deleteComment(t, 14, deleteEvalOnPub, deleteEvalPublished)
		expectDeleteDelivered(t, stream, 14)
	})
}
