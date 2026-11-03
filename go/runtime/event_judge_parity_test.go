package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

type parityCorpus struct {
	model    golem.ModelID
	metadata golem.EventModelMetadata
	key      golem.KeyID
	idField  golem.FieldID
	rows     []golem.UUID
	policies int
	wheres   int
	spec     func(policy, where int) *eventJudgeSpec
	exact    func(policy, where int, row golem.UUID) bool
}

type parityStats struct {
	decidedTrue, decidedFalse, unknown int
}

func runParityCorpus[P, A any](t *testing.T, app *App[P, A], corpus parityCorpus) parityStats {
	t.Helper()
	ctx := context.Background()
	var stats parityStats
	for _, row := range corpus.rows {
		component, err := mutationdecode.IdentityValue(policyir.FieldID(corpus.idField), policyir.UUIDValue(row))
		if err != nil {
			t.Fatal(err)
		}
		identity, err := mutationdecode.NewIdentity(corpus.key, []mutationdecode.IdentityComponent{component})
		if err != nil {
			t.Fatal(err)
		}
		type cell struct{ policy, where int }
		specs := make(map[cell]*eventJudgeSpec)
		var union []*eventJudgeSpec
		for policy := 0; policy < corpus.policies; policy++ {
			for where := 0; where < corpus.wheres; where++ {
				if spec := corpus.spec(policy, where); spec != nil {
					specs[cell{policy, where}] = spec
					union = append(union, spec)
				}
			}
		}
		snapshot, err := app.readEventSnapshot(ctx, corpus.metadata, union, identity)
		if err != nil || snapshot == nil {
			t.Fatalf("row %v: the union of %d specs has no snapshot: %v", row, len(union), err)
		}
		for policy := 0; policy < corpus.policies; policy++ {
			for where := 0; where < corpus.wheres; where++ {
				verdict := deleteUnknown
				if spec := specs[cell{policy, where}]; spec != nil {
					verdict = snapshot.judge(spec)
				}
				visible := corpus.exact(policy, where, row)
				switch verdict {
				case deleteTrue:
					stats.decidedTrue++
					if !visible {
						t.Errorf("row %v policy %d where %d: judged true but the exact read returns no row", row, policy, where)
					}
				case deleteFalse:
					stats.decidedFalse++
					if visible {
						t.Errorf("row %v policy %d where %d: judged false but the exact read returns the row", row, policy, where)
					}
				default:
					stats.unknown++
				}
			}
		}
	}
	return stats
}

type parityFilter[M any] struct {
	predicate golem.Predicate[M]
	present   bool
}

func parityNoFilter[M any]() parityFilter[M] { return parityFilter[M]{} }

func parityFilterOf[M any](predicate golem.Predicate[M]) parityFilter[M] {
	return parityFilter[M]{predicate: predicate, present: true}
}

func parityBoundWhere[P, A, M any](t *testing.T, app *App[P, A], caller *Caller[P, A], descriptor golem.ModelDescriptor[M], metadata golem.EventModelMetadata, filter parityFilter[M]) *policyir.Condition {
	t.Helper()
	if !filter.present {
		return nil
	}
	request, err := golem.RuntimeFreezeEventOptions(descriptor, golem.EventWhere(filter.predicate))
	if err != nil {
		t.Fatal(err)
	}
	readRequest, _, err := eventReadRequest(app.registry, request, metadata)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := caller.Prepare(readRequest)
	if err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
	where, present := prepared.request.Where()
	if !present {
		return nil
	}
	return &where
}

func parityCount[P, A, M any](t *testing.T, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], identity golem.Predicate[M], filter parityFilter[M]) bool {
	t.Helper()
	predicate := identity
	if filter.present {
		predicate = golem.And(identity, filter.predicate)
	}
	count, err := CallerCount(context.Background(), caller, descriptor, golem.Where(predicate))
	if err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func (stats parityStats) require(t *testing.T, name string, undecidable bool) {
	t.Helper()
	total := stats.decidedTrue + stats.decidedFalse + stats.unknown
	t.Logf("%s: %d cells, %d judged true, %d judged false, %d unknown", name, total, stats.decidedTrue, stats.decidedFalse, stats.unknown)
	if stats.decidedTrue == 0 || stats.decidedFalse == 0 {
		t.Fatalf("%s: the judge decided too little to prove anything: %+v", name, stats)
	}
	if undecidable && stats.unknown == 0 {
		t.Fatalf("%s: the corpus never reached an undecidable condition: %+v", name, stats)
	}
	if !undecidable && stats.unknown != 0 {
		t.Fatalf("%s: the judge left %d decidable cells unknown", name, stats.unknown)
	}
}

func TestEventJudgeAgreesWithExactReadOverHopsAndToMany(t *testing.T) {
	const rowsPerModel = 5
	published, draft, secret, carol := deleteEvalUUID(deleteEvalPublished), deleteEvalUUID(deleteEvalDraft), deleteEvalUUID(12), deleteEvalUUID(13)
	alice, bob, carolUser := deleteEvalUUID(deleteEvalAlice), deleteEvalUUID(deleteEvalBob), deleteEvalUUID(3)
	policies := deleteEvalPolicies{
		user: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalUser], bool) {
			return fields.userName.Ne("carol"), true
		},
		post: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalPost], bool) {
			var index int
			if _, err := fmt.Sscanf(actor.Name, "q%d", &index); err == nil {
				return parityPostPolicies(fields)[index], true
			}
			return fields.postTitle.Ne("secret"), true
		},
		comment: func(fields deleteEvalFields, actor deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			var index int
			if _, err := fmt.Sscanf(actor.Name, "c%d", &index); err != nil {
				return golem.All[deleteEvalComment](), true
			}
			return parityCommentPolicies(fields)[index], true
		},
	}
	runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
		for _, user := range []struct {
			id   golem.UUID
			name string
		}{{carolUser, "carol"}} {
			fixture.exec(t, `INSERT INTO `+nestedAcceptanceTable(fixture.app, fixture.schema.User)+`("id","name") VALUES (?,?)`, user.id.String(), user.name)
		}
		for _, post := range []struct {
			id, author golem.UUID
			title      string
		}{{secret, alice, "secret"}, {carol, carolUser, "carol's"}} {
			fixture.exec(t, `INSERT INTO `+nestedAcceptanceTable(fixture.app, fixture.schema.Post)+`("id","author_id","title") VALUES (?,?,?)`, post.id.String(), post.author.String(), post.title)
		}
		for _, comment := range []struct {
			id, post golem.UUID
			body     string
		}{{deleteEvalUUID(22), secret, "x"}, {deleteEvalUUID(23), carol, "y"}, {deleteEvalUUID(24), published, "hi"}} {
			fixture.exec(t, `INSERT INTO `+nestedAcceptanceTable(fixture.app, fixture.schema.Comment)+`("id","post_id","body") VALUES (?,?,?)`, comment.id.String(), comment.post.String(), comment.body)
		}
		_, _, _ = bob, draft, rowsPerModel

		commentMetadata, _ := fixture.app.eventRegistry.Lookup(fixture.schema.Comment)
		commentPolicies := parityCommentPolicies(fixture.fields)
		commentWheres := parityCommentWheres(fixture.fields)
		commentCallers := make([]*Caller[deleteEvalPrincipal, deleteEvalActor], len(commentPolicies))
		for index := range commentCallers {
			caller, err := fixture.app.ForPrincipal(context.Background(), deleteEvalPrincipal{Name: fmt.Sprintf("c%d", index)})
			if err != nil {
				t.Fatal(err)
			}
			commentCallers[index] = caller
		}
		stats := runParityCorpus(t, fixture.app, parityCorpus{
			model: fixture.schema.Comment, metadata: commentMetadata, key: fixture.schema.CommentKey, idField: fixture.schema.CommentID,
			rows:     []golem.UUID{deleteEvalUUID(deleteEvalOnPub), deleteEvalUUID(deleteEvalOnDraft), deleteEvalUUID(22), deleteEvalUUID(23), deleteEvalUUID(24)},
			policies: len(commentPolicies), wheres: len(commentWheres),
			spec: func(policy, where int) *eventJudgeSpec {
				caller := commentCallers[policy]
				return fixture.app.newEventJudgeSpec(caller, fixture.schema.Comment, commentMetadata, parityBoundWhere(t, fixture.app, caller, fixture.comments, commentMetadata, commentWheres[where]))
			},
			exact: func(policy, where int, row golem.UUID) bool {
				return parityCount(t, commentCallers[policy], fixture.comments, fixture.fields.commentID.Eq(row), commentWheres[where])
			},
		})
		stats.require(t, "comments over hops", false)

		postMetadata, _ := fixture.app.eventRegistry.Lookup(fixture.schema.Post)
		postPolicies := parityPostPolicies(fixture.fields)
		postWheres := parityPostWheres(fixture.fields)
		postCallers := make([]*Caller[deleteEvalPrincipal, deleteEvalActor], len(postPolicies))
		for index := range postCallers {
			caller, err := fixture.app.ForPrincipal(context.Background(), deleteEvalPrincipal{Name: fmt.Sprintf("q%d", index)})
			if err != nil {
				t.Fatal(err)
			}
			postCallers[index] = caller
		}
		stats = runParityCorpus(t, fixture.app, parityCorpus{
			model: fixture.schema.Post, metadata: postMetadata, key: fixture.schema.PostKey, idField: fixture.schema.PostID,
			rows:     []golem.UUID{published, draft, secret, carol},
			policies: len(postPolicies), wheres: len(postWheres),
			spec: func(policy, where int) *eventJudgeSpec {
				caller := postCallers[policy]
				return fixture.app.newEventJudgeSpec(caller, fixture.schema.Post, postMetadata, parityBoundWhere(t, fixture.app, caller, fixture.posts, postMetadata, postWheres[where]))
			},
			exact: func(policy, where int, row golem.UUID) bool {
				return parityCount(t, postCallers[policy], fixture.posts, fixture.fields.postID.Eq(row), postWheres[where])
			},
		})
		stats.require(t, "posts over to-many", true)
	})
}

func parityCommentPolicies(fields deleteEvalFields) []golem.Predicate[deleteEvalComment] {
	hop := fields.commentPost.Is(fields.postTitle.Eq("published"))
	deep := fields.commentPost.Is(fields.postAuthor.Is(fields.userName.Eq("alice")))
	return []golem.Predicate[deleteEvalComment]{
		golem.All[deleteEvalComment](),
		hop,
		deep,
		golem.Not(hop),
		golem.Or(hop, fields.commentBody.Eq("hi")),
		fields.commentPost.IsNot(fields.postTitle.Eq("draft")),
		fields.commentPost.Is(fields.postAuthor.IsNot(fields.userName.Eq("alice"))),
		golem.Not(deep),
		golem.And(golem.Not(fields.commentBody.Eq("hello")), fields.commentPost.IsNotNull()),
		fields.commentBody.In("hi", "y"),
		fields.commentBody.NotIn("hi"),
		fields.commentPost.Is(golem.All[deleteEvalPost]()),
		fields.commentPost.Is(fields.postAuthor.Is(golem.All[deleteEvalUser]())),
		golem.Not(fields.commentPost.Is(golem.All[deleteEvalPost]())),
	}
}

func parityCommentWheres(fields deleteEvalFields) []parityFilter[deleteEvalComment] {
	return []parityFilter[deleteEvalComment]{
		parityNoFilter[deleteEvalComment](),
		parityFilterOf(fields.commentBody.Eq("hi")),
		parityFilterOf(golem.Not(fields.commentPost.Is(fields.postTitle.Eq("draft")))),
		parityFilterOf(fields.commentBody.GT("h")),
	}
}

func parityPostPolicies(fields deleteEvalFields) []golem.Predicate[deleteEvalPost] {
	some := fields.postComments.Some(fields.commentBody.Eq("hello"))
	owned := fields.postAuthor.Is(fields.userName.Eq("alice"))
	return []golem.Predicate[deleteEvalPost]{
		some,
		golem.Not(some),
		fields.postComments.Every(fields.commentBody.Eq("hi")),
		fields.postComments.None(fields.commentBody.Eq("hi")),
		golem.And(fields.postTitle.Eq("published"), some),
		golem.Or(fields.postTitle.Eq("draft"), some),
		owned,
		golem.Not(owned),
		fields.postTitle.Contains("pub"),
		fields.postAuthor.Is(golem.All[deleteEvalUser]()),
	}
}

func parityPostWheres(fields deleteEvalFields) []parityFilter[deleteEvalPost] {
	return []parityFilter[deleteEvalPost]{
		parityNoFilter[deleteEvalPost](),
		parityFilterOf(fields.postTitle.Ne("draft")),
		parityFilterOf(golem.Not(fields.postComments.Some(fields.commentBody.Eq("hi")))),
	}
}

type parityOptPost struct{}
type parityOptUser struct{}
type parityOptPrincipal struct{ Name string }
type parityOptActor struct{ Name string }

type parityOptFields struct {
	postID     golem.EqualField[parityOptPost, golem.UUID]
	postAuthor golem.NullableEqualField[parityOptPost, golem.UUID]
	postTitle  golem.ModeTextField[parityOptPost, string]
	author     golem.ToOne[parityOptPost, parityOptUser]
	userName   golem.TextField[parityOptUser, string]
}

type parityOptFixture struct {
	app      *App[parityOptPrincipal, parityOptActor]
	schema   schematest.Fixture
	fields   parityOptFields
	posts    golem.ModelDescriptor[parityOptPost]
	metadata golem.EventModelMetadata
}

func parityOptPostPolicies(fields parityOptFields, u1, u2 golem.UUID) []golem.Predicate[parityOptPost] {
	byBob := fields.author.Is(fields.userName.Eq("bob"))
	return []golem.Predicate[parityOptPost]{
		golem.All[parityOptPost](),
		fields.postAuthor.Eq(u1),
		fields.postAuthor.Ne(u1),
		golem.Not(fields.postAuthor.Eq(u1)),
		fields.postAuthor.IsNull(),
		fields.postAuthor.In(u1, u2),
		fields.postAuthor.NotIn(u1),
		fields.postTitle.Eq("alpha"),
		fields.postTitle.Compare(golem.ASCIIInsensitive()).Eq("alpha"),
		fields.postTitle.GT("b"),
		fields.postTitle.Contains("lph"),
		golem.Or(fields.postAuthor.Eq(u1), fields.postTitle.Eq("beta")),
		golem.And(golem.Not(fields.postTitle.Eq("alpha")), fields.postAuthor.IsNotNull()),
		byBob,
		fields.author.IsNot(fields.userName.Eq("bob")),
		fields.author.IsNull(),
		golem.Not(byBob),
		fields.author.IsNotNull(),
		fields.author.Is(golem.All[parityOptUser]()),
		golem.Not(fields.author.Is(golem.All[parityOptUser]())),
		fields.postTitle.StartsWith("A"),
		fields.postTitle.Compare(golem.ASCIIInsensitive()).Ne("ALPHA"),
	}
}

func parityOptWheres(fields parityOptFields, u1 golem.UUID) []parityFilter[parityOptPost] {
	return []parityFilter[parityOptPost]{
		parityNoFilter[parityOptPost](),
		parityFilterOf(fields.postTitle.Eq("alpha")),
		parityFilterOf(fields.postAuthor.Ne(u1)),
		parityFilterOf(golem.Or(fields.postAuthor.IsNull(), fields.postTitle.GT("alpha"))),
	}
}

func runOptionalSourceProfiles(t *testing.T, run func(*testing.T, parityOptFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		schemaFixture := schematest.NewSubscribedIndexedOptionalSource(t)
		provider := sqliteprovider.New()
		database, _, err := provider.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "parity.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := provider.ApplyInitial(context.Background(), database, schemaFixture.SQLite); err != nil {
			t.Fatal(err)
		}
		run(t, newParityOptFixture(t, database, golem.SQLite, schemaFixture))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			namespace := physical.PhysicalName(fmt.Sprintf("golem_judge_parity_%s_%d_%d", profile.name, os.Getpid(), sequence))
			systemNamespace := physical.PhysicalName(string(namespace) + "_system")
			schemaFixture := schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces(t, namespace, systemNamespace)
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
			run(t, newParityOptFixture(t, database, golem.PostgreSQL, schemaFixture))
		})
	}
}

func newParityOptFixture(t *testing.T, database *sqlx.DB, provider golem.Provider, schemaFixture schematest.Fixture) parityOptFixture {
	t.Helper()
	ctx := context.Background()
	fields := parityOptFields{
		postID:     golem.GeneratedEqualField[parityOptPost, golem.UUID](schemaFixture.PostID),
		postAuthor: golem.GeneratedNullableEqualField[parityOptPost, golem.UUID](schemaFixture.AuthorID),
		postTitle:  golem.GeneratedModeTextField[parityOptPost, string](schemaFixture.PostTitle),
		author:     golem.GeneratedToOne[parityOptPost, parityOptUser](schemaFixture.PostAuthor, schemaFixture.Authorship, schemaFixture.User),
		userName:   golem.GeneratedTextField[parityOptUser, string](schemaFixture.UserName),
	}
	userIdentity := golem.GeneratedIdentityMetadata(schemaFixture.User, schemaFixture.UserKey, golem.PrimaryIdentity, schemaFixture.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schemaFixture.Post, schemaFixture.PostKey, golem.PrimaryIdentity, schemaFixture.PostID)
	userDescriptor := golem.GeneratedModelDescriptor[parityOptUser](schemaFixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.UserID, schemaFixture.UserName}, nil, []golem.IdentityMetadata{userIdentity},
		[]golem.RelationMetadata{golem.GeneratedRelationMetadata(schemaFixture.User, schemaFixture.Post, schemaFixture.UserPosts, schemaFixture.Authorship, golem.RelationInverse, golem.RelationToMany)},
	))
	postDescriptor := golem.GeneratedModelDescriptor[parityOptPost](schemaFixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.PostID, schemaFixture.AuthorID, schemaFixture.PostTitle}, nil, []golem.IdentityMetadata{postIdentity},
		[]golem.RelationMetadata{golem.GeneratedRelationMetadata(schemaFixture.Post, schemaFixture.User, schemaFixture.PostAuthor, schemaFixture.Authorship, golem.RelationSource, golem.RelationToOne)},
	))
	generation := schemaFixture.Bundle.GenerationDigest()
	descriptors, err := golem.GeneratedApplicationDescriptors(generation, golem.GeneratedStampedPackageDescriptors(generation, userDescriptor.Metadata(), postDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	u1, u2 := deleteEvalUUID(1), deleteEvalUUID(2)
	userBinding := golem.GeneratedPolicyBinding[parityOptActor, parityOptUser](schemaFixture.User, func(parityOptActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[parityOptUser]()
		rules.CanRead(fields.userName.Ne("carol"))
		return rules.Freeze(schemaFixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[parityOptActor, parityOptPost](schemaFixture.Post, func(actor parityOptActor) (golem.FrozenPolicy, error) {
		var index int
		if _, err := fmt.Sscanf(actor.Name, "p%d", &index); err != nil {
			return golem.FrozenPolicy{}, err
		}
		rules := golem.NewRules[parityOptPost]()
		rules.CanRead(parityOptPostPolicies(fields, u1, u2)[index])
		return rules.Freeze(schemaFixture.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(generation, golem.GeneratedStampedPackageBindings(generation, []golem.PolicyBinding[parityOptActor]{userBinding, postBinding}, nil))
	if err != nil {
		t.Fatal(err)
	}
	model, _ := schemaFixture.Registry.Model(schemaFixture.Post)
	fingerprint, _, _ := model.EventSchema()
	parsed, err := parityEventSchemaDigest(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	eventRegistry, err := golem.GeneratedEventRegistry(generation, golem.GeneratedPackageEventRegistry(generation, golem.GeneratedEventModelMetadata(schemaFixture.Post, parsed, []golem.FieldID{schemaFixture.PostID}, "p7EventOracleValue", "golem.UUID")))
	if err != nil {
		t.Fatal(err)
	}
	factories, err := GeneratedEventFactoryRegistry(generation, GeneratedPackageEventFactories(generation, p7EventOracleFactory{model: schemaFixture.Post, schema: parsed}))
	if err != nil {
		t.Fatal(err)
	}
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[parityOptPrincipal, parityOptActor]{
		Database: p8RuntimeTestDatabase(database, provider), Bundle: schemaFixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		EventRegistry: eventRegistry, EventFactories: factories, EventTransport: memory,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		ResolvePrincipal: func(_ context.Context, principal parityOptPrincipal) (parityOptActor, error) {
			return parityOptActor(principal), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := app.eventRegistry.Lookup(schemaFixture.Post)
	fixture := parityOptFixture{app: app, schema: schemaFixture, fields: fields, posts: postDescriptor, metadata: metadata}
	for _, user := range []struct {
		id   golem.UUID
		name string
	}{{u1, "alice"}, {u2, "bob"}, {deleteEvalUUID(3), "carol"}} {
		exec := `INSERT INTO ` + nestedAcceptanceTable(app, schemaFixture.User) + `("id","name") VALUES (?,?)`
		if _, err := database.ExecContext(ctx, database.Rebind(exec), user.id.String(), user.name); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func (fixture parityOptFixture) insertPost(t *testing.T, id byte, author *golem.UUID, title string) golem.UUID {
	t.Helper()
	post := deleteEvalUUID(id)
	var authorValue any
	if author != nil {
		authorValue = author.String()
	}
	query := `INSERT INTO ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + `("id","author_id","title") VALUES (?,?,?)`
	if _, err := fixture.app.database.ExecContext(context.Background(), fixture.app.database.Rebind(query), post.String(), authorValue, title); err != nil {
		t.Fatal(err)
	}
	return post
}

func TestEventJudgeAgreesWithExactReadOverNullsAndCollations(t *testing.T) {
	runOptionalSourceProfiles(t, func(t *testing.T, fixture parityOptFixture) {
		u1, u2, u3 := deleteEvalUUID(1), deleteEvalUUID(2), deleteEvalUUID(3)
		var rows []golem.UUID
		id := byte(10)
		for _, author := range []*golem.UUID{&u1, &u2, &u3, nil} {
			for _, title := range []string{"alpha", "ALPHA", "beta", "Ärger", "", "a%b", "z"} {
				rows = append(rows, fixture.insertPost(t, id, author, title))
				id++
			}
		}
		policies := parityOptPostPolicies(fixture.fields, u1, u2)
		wheres := parityOptWheres(fixture.fields, u1)
		callers := make([]*Caller[parityOptPrincipal, parityOptActor], len(policies))
		for index := range callers {
			caller, err := fixture.app.ForPrincipal(context.Background(), parityOptPrincipal{Name: fmt.Sprintf("p%d", index)})
			if err != nil {
				t.Fatal(err)
			}
			callers[index] = caller
		}
		stats := runParityCorpus(t, fixture.app, parityCorpus{
			model: fixture.schema.Post, metadata: fixture.metadata, key: fixture.schema.PostKey, idField: fixture.schema.PostID,
			rows: rows, policies: len(policies), wheres: len(wheres),
			spec: func(policy, where int) *eventJudgeSpec {
				caller := callers[policy]
				return fixture.app.newEventJudgeSpec(caller, fixture.schema.Post, fixture.metadata, parityBoundWhere(t, fixture.app, caller, fixture.posts, fixture.metadata, wheres[where]))
			},
			exact: func(policy, where int, row golem.UUID) bool {
				return parityCount(t, callers[policy], fixture.posts, fixture.fields.postID.Eq(row), wheres[where])
			},
		})
		stats.require(t, "posts over nulls and collations", false)
	})
}

func parityEventSchemaDigest(fingerprint compilerir.Fingerprint) (golem.EventSchemaDigest, error) {
	parsed, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		return golem.EventSchemaDigest{}, err
	}
	return golem.EventSchemaDigest(parsed), nil
}
