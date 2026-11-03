package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

const eventFanoutFirstID byte = 100

func eventFanoutUUID(index int) golem.UUID { return deleteEvalUUID(eventFanoutFirstID + byte(index)) }

func eventFanoutActorIndex(actor deleteEvalActor) int {
	var index int
	if _, err := fmt.Sscanf(actor.Name, "u%d", &index); err != nil {
		return -1
	}
	return index
}

func eventFanoutPolicies() deleteEvalPolicies {
	return deleteEvalPolicies{
		post: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalPost], bool) {
			return fields.postAuthorID.Eq(eventFanoutUUID(eventFanoutActorIndex(actor))), true
		},
		comment: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postAuthorID.Eq(eventFanoutUUID(eventFanoutActorIndex(actor)))), true
		},
	}
}

func runTracedEventProfiles(t *testing.T, policies deleteEvalPolicies, run func(*testing.T, deleteEvalFixture, *p5SQLTrace)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		trace := &p5SQLTrace{}
		schemaFixture := schematest.NewSubscribedGraph(t)
		plainDSN := "file:" + filepath.Join(t.TempDir(), "event-fanout.db")
		bootstrap, _, err := sqliteprovider.New().Open(context.Background(), plainDSN)
		if err != nil {
			t.Fatal(err)
		}
		registered := bootstrap.Driver()
		if err := bootstrap.Close(); err != nil {
			t.Fatal(err)
		}
		base := p5DriverConnector{driver: registered, dsn: plainDSN + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"}
		database := sqlx.NewDb(sql.OpenDB(p5TraceConnector{base: base, trace: trace}), "sqlite")
		database.SetMaxOpenConns(4)
		database.SetMaxIdleConns(4)
		t.Cleanup(func() { _ = database.Close() })
		if err := sqliteprovider.New().ApplyInitial(context.Background(), database, schemaFixture.SQLite); err != nil {
			t.Fatal(err)
		}
		run(t, newDeleteEvalFixture(t, database, golem.SQLite, schemaFixture, policies), trace)
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			trace := &p5SQLTrace{}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			namespace := physical.PhysicalName(fmt.Sprintf("golem_event_fanout_%s_%d_%d", profile.name, os.Getpid(), sequence))
			systemNamespace := physical.PhysicalName(string(namespace) + "_system")
			schemaFixture := schematest.NewSubscribedGraphPostgreSQLNamespaces(t, namespace, systemNamespace)
			configuration, err := pgx.ParseConfig(profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			if configuration.RuntimeParams == nil {
				configuration.RuntimeParams = map[string]string{}
			}
			configuration.RuntimeParams["timezone"] = "UTC"
			configuration.RuntimeParams["datestyle"] = "ISO, YMD"
			configuration.RuntimeParams["intervalstyle"] = "iso_8601"
			configuration.RuntimeParams["standard_conforming_strings"] = "on"
			database := sqlx.NewDb(sql.OpenDB(p5TraceConnector{base: stdlib.GetConnector(*configuration), trace: trace}), "pgx")
			database.SetMaxOpenConns(8)
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteAcceptanceIdentifier(string(namespace))+` CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteAcceptanceIdentifier(string(systemNamespace))+` CASCADE`)
				_ = database.Close()
			})
			if err := postgresprovider.New().ApplyInitial(context.Background(), database, schemaFixture.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			run(t, newDeleteEvalFixture(t, database, golem.PostgreSQL, schemaFixture, policies), trace)
		})
	}
}

func (fixture deleteEvalFixture) seedOwners(t *testing.T, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		id := mutationResultUUIDText(eventFanoutFirstID + byte(index))
		fixture.exec(t, `INSERT INTO `+nestedAcceptanceTable(fixture.app, fixture.schema.User)+`("id","name") VALUES (?,?)`, id, fmt.Sprintf("u%d", index))
		fixture.exec(t, `INSERT INTO `+nestedAcceptanceTable(fixture.app, fixture.schema.Post)+`("id","author_id","title") VALUES (?,?,?)`, id, id, "first")
		fixture.exec(t, `INSERT INTO `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+`("id","post_id","body") VALUES (?,?,?)`, id, id, "first")
	}
}

func (fixture deleteEvalFixture) subscribeActor(t *testing.T, model golem.ModelID, actor string, options ...golem.EventOption[deleteEvalPost]) golem.EventStream[p7EventOracleValue] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	caller, err := fixture.app.ForPrincipal(ctx, deleteEvalPrincipal{Name: actor})
	if err != nil {
		t.Fatal(err)
	}
	var stream golem.EventStream[p7EventOracleValue]
	if model == fixture.schema.Post {
		stream, err = CallerEvents[deleteEvalPrincipal, deleteEvalActor, deleteEvalPost, p7EventOracleValue](ctx, caller, fixture.posts, options...)
	} else {
		stream, err = CallerEvents[deleteEvalPrincipal, deleteEvalActor, deleteEvalComment, p7EventOracleValue](ctx, caller, fixture.comments)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	select {
	case <-fixture.transport.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("model hub did not subscribe")
	}
	return stream
}

func (fixture deleteEvalFixture) publishRowEvent(t *testing.T, action golem.EventAction, eventID uint16, model golem.ModelID, cells []mutationdecode.Cell, identity golem.FieldID) {
	t.Helper()
	row, err := mutationdecode.NewCompleteRow(fixture.schema.Registry, policyir.ModelID(model), withTestRowVersion(fixture.schema.Registry, model, cells))
	if err != nil {
		t.Fatal(err)
	}
	factAction := mutationir.FactCreated
	var before *mutationdecode.Row
	if action == golem.EventUpdated {
		factAction = mutationir.FactUpdated
		before = &row
	}
	var beforeFields []policyir.FieldID
	if action == golem.EventUpdated {
		beforeFields = []policyir.FieldID{policyir.FieldID(identity)}
	}
	requirement, err := mutationir.NewFactRequirement(factAction, beforeFields, []policyir.FieldID{policyir.FieldID(identity)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := fixture.digests[model]
	requirement, err = requirement.WithEventSchema([32]byte(digest))
	if err != nil {
		t.Fatal(err)
	}
	id := p7OracleID(eventID)
	fact, err := mutationfact.New(fixture.schema.Registry, golem.SchemaDigest(digest), mutationfact.EventID(id), requirement, mutationfact.CausationID(golem.CausationID(id)), 1, 7, before, &row)
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

func (fixture deleteEvalFixture) publishPostEvent(t *testing.T, action golem.EventAction, eventID uint16, post, author golem.UUID, title string) {
	t.Helper()
	value, err := policyir.StringValue(title)
	if err != nil {
		t.Fatal(err)
	}
	fixture.publishRowEvent(t, action, eventID, fixture.schema.Post, []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue(post)),
		mutationdecode.Value(policyir.FieldID(fixture.schema.AuthorID), policyir.UUIDValue(author)),
		mutationdecode.Value(policyir.FieldID(fixture.schema.PostTitle), value),
	}, fixture.schema.PostID)
}

func (fixture deleteEvalFixture) publishCommentEvent(t *testing.T, action golem.EventAction, eventID uint16, comment, post golem.UUID, body string) {
	t.Helper()
	value, err := policyir.StringValue(body)
	if err != nil {
		t.Fatal(err)
	}
	fixture.publishRowEvent(t, action, eventID, fixture.schema.Comment, []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentID), policyir.UUIDValue(comment)),
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentPostID), policyir.UUIDValue(post)),
		mutationdecode.Value(policyir.FieldID(fixture.schema.CommentBody), value),
	}, fixture.schema.CommentID)
}

func (fixture deleteEvalFixture) settledEvaluations(action golem.EventAction) (delivered, suppressed int64) {
	for _, value := range fixture.observed.kind(events.ObservationEvaluation) {
		if value.Action() == action {
			delivered += value.AggregateCount()
		}
	}
	for _, value := range fixture.observed.kind(events.ObservationSuppression) {
		if value.Action() == action {
			suppressed += value.AggregateCount()
		}
	}
	return delivered, suppressed
}

func (fixture deleteEvalFixture) awaitSettled(t *testing.T, action golem.EventAction, want int64) (delivered, suppressed int64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		delivered, suppressed = fixture.settledEvaluations(action)
		if delivered+suppressed >= want {
			return delivered, suppressed
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("only %d of %d evaluations settled (delivered %d, suppressed %d)", delivered+suppressed, want, delivered, suppressed)
	return 0, 0
}

func eventFanoutReads(trace *p5SQLTrace) []string {
	var reads []string
	for _, statement := range trace.snapshot() {
		text := strings.ToLower(strings.TrimSpace(statement))
		if strings.HasPrefix(text, "select") && !strings.Contains(text, "_golem") {
			reads = append(reads, statement)
		}
	}
	return reads
}

func TestEventFanoutReadsDoNotScaleWithInvisibleSubscribers(t *testing.T) {
	counts := map[string]map[int]int{}
	for _, model := range []string{"owner", "hop"} {
		counts[model] = map[int]int{}
	}
	runTracedEventProfiles(t, eventFanoutPolicies(), func(t *testing.T, fixture deleteEvalFixture, trace *p5SQLTrace) {
		fixture.seedOwners(t, 128)
		for _, scenario := range []struct {
			name    string
			model   golem.ModelID
			maximum int
			publish func(eventID uint16)
		}{
			{"owner", fixture.schema.Post, 2, func(eventID uint16) {
				fixture.publishPostEvent(t, golem.EventUpdated, eventID, eventFanoutUUID(0), eventFanoutUUID(0), "edited")
			}},
			{"hop", fixture.schema.Comment, 4, func(eventID uint16) {
				fixture.publishCommentEvent(t, golem.EventUpdated, eventID, eventFanoutUUID(0), eventFanoutUUID(0), "edited")
			}},
		} {
			observed := map[int]int{}
			for step, subscribers := range []int{16, 128} {
				eventID := uint16(100*(step+1)) + uint16(len(scenario.name))
				streams := make([]golem.EventStream[p7EventOracleValue], subscribers)
				for index := range streams {
					streams[index] = fixture.subscribeActor(t, scenario.model, fmt.Sprintf("u%d", index))
				}
				baseDelivered, baseSuppressed := fixture.settledEvaluations(golem.EventUpdated)
				trace.reset()
				scenario.publish(eventID)
				delivered, suppressed := fixture.awaitSettled(t, golem.EventUpdated, baseDelivered+baseSuppressed+int64(subscribers))
				if delivered-baseDelivered != 1 || suppressed-baseSuppressed != int64(subscribers-1) {
					t.Fatalf("%s N=%d: delivered %d suppressed %d, want 1 and %d", scenario.name, subscribers, delivered-baseDelivered, suppressed-baseSuppressed, subscribers-1)
				}
				event := receiveP7Event(t, streams[0])
				if event.validated.Metadata().EventID() != p7OracleID(eventID) {
					t.Fatalf("%s N=%d: owner received %x, want %x", scenario.name, subscribers, event.validated.Metadata().EventID(), p7OracleID(eventID))
				}
				reads := eventFanoutReads(trace)
				observed[subscribers] = len(reads)
				counts[scenario.name][subscribers] = len(reads)
				for _, stream := range streams {
					_ = stream.Close()
				}
			}
			if observed[16] != observed[128] {
				t.Fatalf("%s: reads per event depend on the subscriber count: N=16 %d, N=128 %d", scenario.name, observed[16], observed[128])
			}
			if observed[16] > scenario.maximum {
				t.Fatalf("%s: %d reads per event, want at most %d", scenario.name, observed[16], scenario.maximum)
			}
		}
	})
	t.Logf("reads per event %v", counts)
}
