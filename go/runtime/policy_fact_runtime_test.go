package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	"github.com/eleven-am/golem/go/internal/physical"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
	internalpostgresql "github.com/eleven-am/golem/go/internal/provider/postgresql"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/observe"
	providerapi "github.com/eleven-am/golem/go/provider"
	publicpostgresql "github.com/eleven-am/golem/go/provider/postgresql"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
)

// Keep this fixture independent of other runtime test helpers and generated
// applications so the production runtime files and this file can be tested
// together without compiling the large generated runtime acceptance fixtures.
type policyFactRuntimePrincipal struct {
	ID       golem.UUID
	Negative bool
}

type policyFactRuntimeActor struct {
	ID        golem.UUID
	Negative  bool
	NameGrant string
}
type policyFactRuntimeUser struct{}
type policyFactRuntimePost struct{}
type policyFactRuntimeComment struct{}
type policyFactRuntimeWatch struct{}

type policyFactRuntimeAuth struct {
	rejected       atomic.Bool
	grantID        atomic.Uint32
	otherNameGrant atomic.Bool
}

type policyFactRuntimeOptions struct {
	postgresDSN        string
	schemaTransform    func(schematest.GraphFixture) schematest.GraphFixture
	boundUserScope     func(schematest.GraphFixture, policyFactRuntimeActor) golem.BoundFactScope[policyFactRuntimeUser]
	ordinaryGuardGraph *schematest.OrdinaryGuardGraphFixture
	semanticEmbeddings *embedding.Registry
	afterCommitError   func(context.Context, golem.AfterCommitFailure)
	hooks              func(schematest.GraphFixture) []golem.HookBinding[policyFactRuntimeActor]
	postMutationGrants bool
	postCurrentGuard   bool
	commentOwnerRead   bool
	auth               *policyFactRuntimeAuth
	revalidateEvery    time.Duration
	unusedFact         bool
	ordinaryDependency bool
	nameMask           bool
	postLocalFact      bool
	postFactMask       bool
	postUnusedFact     bool
	rootNoFacts        bool
	userNameScope      bool
}

type policyFactRuntimeFactory struct {
	model  golem.ModelID
	schema golem.EventSchemaDigest
}

func (factory policyFactRuntimeFactory) ModelID() golem.ModelID { return factory.model }
func (factory policyFactRuntimeFactory) EventSchemaDigest() golem.EventSchemaDigest {
	return factory.schema
}
func (factory policyFactRuntimeFactory) Build(value ValidatedEvent) (any, error) {
	return value.Metadata(), nil
}

// This wrapper records only batches accepted by the actual memory transport.
// All encoding, routing and delivery remain owned by production code.
type policyFactRuntimeTransport struct {
	events.EventTransport
	published chan events.Notice
}

func (transport *policyFactRuntimeTransport) TransportCapabilities() events.TransportCapabilities {
	return events.CapabilitiesOf(transport.EventTransport)
}

func (transport *policyFactRuntimeTransport) Publish(ctx context.Context, batch events.EventBatch) error {
	if err := transport.EventTransport.Publish(ctx, batch); err != nil {
		return err
	}
	for _, notice := range batch.Events() {
		transport.published <- notice
	}
	return nil
}

type policyFactRuntimeObserver struct {
	post      golem.ModelID
	evaluated chan struct{}
}

func (observer policyFactRuntimeObserver) ObserveGolem(_ context.Context, value observe.Observation) {
	if value.ModelID() == observer.post && value.Operation() == observe.OperationSubscriptionSuppression {
		select {
		case observer.evaluated <- struct{}{}:
		default:
		}
	}
}

type policyFactRuntimeFixture struct {
	app               *App[policyFactRuntimePrincipal, policyFactRuntimeActor]
	ctx               context.Context
	stop              context.CancelFunc
	databaseConfig    publicsqlite.Config
	applicationConfig Config[policyFactRuntimePrincipal, policyFactRuntimeActor]
	schema            schematest.GraphFixture
	users             golem.ModelDescriptor[policyFactRuntimeUser]
	posts             golem.ModelDescriptor[policyFactRuntimePost]
	postID            golem.EqualField[policyFactRuntimePost, golem.UUID]
	authorID          golem.EqualField[policyFactRuntimePost, golem.UUID]
	title             golem.TextField[policyFactRuntimePost, string]
	transport         *policyFactRuntimeTransport
	evaluated         <-chan struct{}
	principal         policyFactRuntimePrincipal
}

// This is a separate positive acceptance fixture, not an application option.
// Fact admission and automatic evidence come from the actual compiled writer
// and a validated static declaration inventory. The raw fixture below remains
// available to exercise refusal and ordinary reader-release behavior.
func newHistoricalPolicyFactRuntimeFixture(t *testing.T, negative bool, options ...policyFactRuntimeOptions) policyFactRuntimeFixture {
	t.Helper()
	if mutationHistoryWriterVersion != mutationfact.HistoryFactVersion {
		t.Skip("named-fact runtime acceptance requires the compiled historical writer release")
	}
	var config policyFactRuntimeOptions
	if len(options) != 0 {
		config = options[0]
	}
	if config.ordinaryGuardGraph == nil {
		original := config.schemaTransform
		declarations := config
		config.schemaTransform = func(base schematest.GraphFixture) schematest.GraphFixture {
			if original != nil {
				// Explicit fixtures own their exact root inventory, including
				// tests in which a fact owner is a captured non-root row.
				base = original(base)
			} else {
				base = policyFactRuntimeCaptureSchema(t, base, declarations)
			}
			if base.Registry.PolicyCaptureFingerprint() == (golem.SchemaDigest{}) {
				t.Fatal("historical fact fixture has no registered capture inventory")
			}
			return base
		}
	}
	return newPolicyFactRuntimeFixture(t, negative, config)
}

// The templates below are ordinary parameterized predicates matching the
// fixture's actual scope/body variants. Projection and fingerprint ownership
// remain in scopecontract and schema.New; no test supplies a derived footprint.
func policyFactRuntimeCaptureSchema(t *testing.T, fixture schematest.GraphFixture, options policyFactRuntimeOptions) schematest.GraphFixture {
	t.Helper()
	compilation, err := policyschema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	model := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	field := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	relation := func(id golem.RelationID) compilerir.RelationID {
		return compilerir.RelationID(hex.EncodeToString(id[:]))
	}
	logical := func(owner golem.ModelID, id golem.FieldID) compilerir.LogicalTypeIR {
		value, ok := fixture.Registry.Field(owner, id)
		if !ok {
			t.Fatal("capture declaration field is absent")
		}
		return value.LogicalType()
	}
	user, post, comment := model(fixture.User), model(fixture.Post), model(fixture.Comment)
	args := compilerir.GoNamedTypeIR{PackagePath: "example.test/factruntime", Name: "CaptureArgs"}
	userScope := compilerir.ScopeTemplateIR{Name: "runtimeOwner", Arguments: args,
		Parameters: []compilerir.ScopeParameterIR{{Name: "Owner", Type: logical(fixture.User, fixture.UserID)}},
		Expression: compilerir.ScopeExpressionIR{ModelID: user, Operator: "eq", FieldID: field(fixture.UserID), Parameter: "Owner"},
	}
	if options.userNameScope {
		userScope.Parameters = append(userScope.Parameters, compilerir.ScopeParameterIR{Name: "Name", Type: logical(fixture.User, fixture.UserName)})
		userScope.Expression = compilerir.ScopeExpressionIR{ModelID: user, Operator: "and", Children: []compilerir.ScopeExpressionIR{
			userScope.Expression, {ModelID: user, Operator: "eq", FieldID: field(fixture.UserName), Parameter: "Name"},
		}}
	}
	scopes := []scopecontract.Declaration{{ModelID: user, Template: userScope}}
	if options.postLocalFact {
		scopes = append(scopes, scopecontract.Declaration{ModelID: post, Template: compilerir.ScopeTemplateIR{Name: "runtimePostOwner", Arguments: args,
			Parameters: []compilerir.ScopeParameterIR{{Name: "Owner", Type: logical(fixture.Post, fixture.AuthorID)}},
			Expression: compilerir.ScopeExpressionIR{ModelID: post, Operator: "eq", FieldID: field(fixture.AuthorID), Parameter: "Owner"},
		}})
	}
	if diagnostics := scopecontract.Normalize(&compilation, scopes); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	userBody := compilerir.ScopeExpressionIR{ModelID: user, Operator: "some", FieldID: field(fixture.UserPosts), RelationID: relation(fixture.Authorship), TargetModelID: post,
		Children: []compilerir.ScopeExpressionIR{{ModelID: post, Operator: "eq", FieldID: field(fixture.PostTitle), Parameter: "State"}},
	}
	userType := logical(fixture.Post, fixture.PostTitle)
	if options.ordinaryDependency {
		userBody = compilerir.ScopeExpressionIR{ModelID: user, Operator: "eq", FieldID: field(fixture.UserName), Parameter: "State"}
		userType = logical(fixture.User, fixture.UserName)
	}
	facts := []scopecontract.FactDeclaration{{ModelID: user, ScopeName: userScope.Name, Template: compilerir.FactTemplateIR{Name: "runtimeReady", Arguments: args,
		Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: userType}}, Expression: userBody,
	}}}
	if options.postLocalFact {
		postBody := compilerir.ScopeExpressionIR{ModelID: post, Operator: "eq", FieldID: field(fixture.PostTitle), Parameter: "State"}
		postType := logical(fixture.Post, fixture.PostTitle)
		if options.postUnusedFact {
			postBody = compilerir.ScopeExpressionIR{ModelID: post, Operator: "some", FieldID: field(fixture.PostComments), RelationID: relation(fixture.Commenting), TargetModelID: comment,
				Children: []compilerir.ScopeExpressionIR{{ModelID: comment, Operator: "eq", FieldID: field(fixture.CommentBody), Parameter: "State"}},
			}
			postType = logical(fixture.Comment, fixture.CommentBody)
		}
		facts = append(facts, scopecontract.FactDeclaration{ModelID: post, ScopeName: "runtimePostOwner", Template: compilerir.FactTemplateIR{Name: "runtimePostReady", Arguments: args,
			Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: postType}}, Expression: postBody,
		}})
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, facts); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(encoded) != 32 {
		t.Fatal("invalid capture contract fingerprint")
	}
	var digest golem.SchemaDigest
	copy(digest[:], encoded)
	bundle := fixture.Bundle
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	fixture.Bundle = golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), contract, bundle.Providers()...)
	fixture.Registry, err = policyschema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func newPolicyFactRuntimeFixture(t *testing.T, negative bool, options ...policyFactRuntimeOptions) policyFactRuntimeFixture {
	t.Helper()
	var config policyFactRuntimeOptions
	if len(options) != 0 {
		config = options[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	schema := schematest.NewSubscribedGraph(t)
	if config.ordinaryGuardGraph != nil {
		schema = config.ordinaryGuardGraph.Graph
	} else if config.schemaTransform != nil {
		schema = config.schemaTransform(schema)
	}
	databaseConfig := publicsqlite.Config{DataSourceName: "file:" + filepath.Join(t.TempDir(), "policy-facts.db")}
	var database *providerapi.Database
	var err error
	if config.postgresDSN != "" {
		database, err = policyFactRuntimeOpenPostgreSQL(t, ctx, config.postgresDSN)
	} else {
		database, err = publicsqlite.Open(ctx, databaseConfig)
	}
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	t.Cleanup(cancel)
	schema.Bundle = policyFactRuntimeApplyMigration(t, ctx, database, schema)
	// Seed only fixture source rows. Every observed dependency change below
	// goes through System mutation APIs and their durable outbox transaction.
	for _, id := range []golem.UUID{{15: 1}, {15: 2}} {
		if _, err := database.UnsafeSQLX().ExecContext(ctx, database.UnsafeSQLX().Rebind(`INSERT INTO users(id,name) VALUES(?,?)`), id.String(), "source"); err != nil {
			t.Fatal(err)
		}
	}
	users := golem.GeneratedModelDescriptor[policyFactRuntimeUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName, schema.UserRowVersion}, nil,
		[]golem.IdentityMetadata{golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)},
		[]golem.RelationMetadata{golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)},
	))
	postRelations := []golem.RelationMetadata{
		golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne),
		golem.GeneratedRelationMetadata(schema.Post, schema.Comment, schema.PostComments, schema.Commenting, golem.RelationInverse, golem.RelationToMany),
	}
	if extra := config.ordinaryGuardGraph; extra != nil {
		postRelations = append(postRelations, golem.GeneratedRelationMetadata(schema.Post, extra.Watch, extra.Inverse, extra.Relation, golem.RelationInverse, golem.RelationToMany))
	}
	posts := golem.GeneratedModelDescriptor[policyFactRuntimePost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostID, schema.AuthorID, schema.PostTitle, schema.PostRowVersion}, nil,
		[]golem.IdentityMetadata{golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)}, postRelations,
	))
	comments := golem.GeneratedModelDescriptor[policyFactRuntimeComment](schema.Comment, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.CommentID, schema.CommentPostID, schema.CommentBody, schema.CommentRowVersion}, nil,
		[]golem.IdentityMetadata{golem.GeneratedIdentityMetadata(schema.Comment, schema.CommentKey, golem.PrimaryIdentity, schema.CommentID)},
		[]golem.RelationMetadata{golem.GeneratedRelationMetadata(schema.Comment, schema.Post, schema.CommentPost, schema.Commenting, golem.RelationSource, golem.RelationToOne)},
	))
	generation := schema.Bundle.GenerationDigest()
	models := []golem.ModelMetadata{users.Metadata(), posts.Metadata(), comments.Metadata()}
	if extra := config.ordinaryGuardGraph; extra != nil {
		models = append(models, policyFactRuntimeWatchDescriptor(*extra).Metadata())
	}
	descriptors, err := golem.GeneratedApplicationDescriptors(generation, golem.GeneratedStampedPackageDescriptors(generation, models...))
	if err != nil {
		t.Fatal(err)
	}
	userID := golem.GeneratedEqualField[policyFactRuntimeUser, golem.UUID](schema.UserID)
	userName := golem.GeneratedTextField[policyFactRuntimeUser, string](schema.UserName)
	userPosts := golem.GeneratedToMany[policyFactRuntimeUser, policyFactRuntimePost](schema.UserPosts, schema.Authorship, schema.Post)
	title := golem.GeneratedTextField[policyFactRuntimePost, string](schema.PostTitle)
	var hooks []golem.HookBinding[policyFactRuntimeActor]
	if config.hooks != nil {
		hooks = config.hooks(schema)
	}
	policyBindings := []golem.PolicyBinding[policyFactRuntimeActor]{
		golem.GeneratedPolicyBinding[policyFactRuntimeActor, policyFactRuntimeUser](schema.User, func(actor policyFactRuntimeActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[policyFactRuntimeUser]()
			if config.rootNoFacts {
				rules.CanRead(userID.Eq(golem.UUID{15: 1}))
				return rules.Freeze(schema.User)
			}
			if config.boundUserScope != nil {
				rules.BindFactScope(config.boundUserScope(schema, actor))
			} else if config.userNameScope {
				rules.FactScope(userID.Eq(actor.ID).And(userName.Eq(actor.NameGrant)))
			} else {
				rules.FactScope(userID.Eq(actor.ID))
			}
			body := userPosts.Some(title.Eq("ready"))
			if config.ordinaryGuardGraph != nil {
				comments := golem.GeneratedToMany[policyFactRuntimePost, policyFactRuntimeComment](schema.PostComments, schema.Commenting, schema.Comment)
				body = userPosts.Some(title.Eq("video").And(comments.Some(golem.GeneratedTextField[policyFactRuntimeComment, string](schema.CommentBody).Eq("ready"))))
			}
			if config.ordinaryDependency {
				body = userName.Eq("source")
			}
			ready := rules.Fact("has_ready_post", body)
			if actor.Negative {
				ready = ready.Not()
			}
			if config.unusedFact {
				rules.CanRead(golem.All[policyFactRuntimeUser]())
			} else {
				if config.ordinaryDependency {
					ready = ready.And(userPosts.Some(title.Eq("ready")))
				}
				rules.CanRead(ready)
			}
			if config.nameMask {
				rules.CannotReadFields(golem.All[policyFactRuntimeUser](), userName)
				rules.CanReadFields(userName.Eq(actor.NameGrant), userName)
			}
			return rules.Freeze(schema.User)
		}),
		golem.GeneratedPolicyBinding[policyFactRuntimeActor, policyFactRuntimePost](schema.Post, func(actor policyFactRuntimeActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[policyFactRuntimePost]()
			if config.postCurrentGuard {
				rules.CanRead(title.Ne("revoked"))
			} else if config.postLocalFact {
				authorID := golem.GeneratedEqualField[policyFactRuntimePost, golem.UUID](schema.AuthorID)
				rules.FactScope(authorID.Eq(actor.ID))
				body := title.Eq("ready")
				if config.postUnusedFact {
					comments := golem.GeneratedToMany[policyFactRuntimePost, policyFactRuntimeComment](schema.PostComments, schema.Commenting, schema.Comment)
					body = comments.Some(golem.GeneratedTextField[policyFactRuntimeComment, string](schema.CommentBody).Eq("unused"))
				}
				ready := rules.Fact("ready", body)
				if config.postUnusedFact {
					rules.CanRead(golem.All[policyFactRuntimePost]())
				} else if config.postFactMask {
					rules.CanRead(golem.All[policyFactRuntimePost]())
					rules.CannotReadFields(golem.All[policyFactRuntimePost](), title)
					rules.CanReadFields(ready, title)
				} else {
					rules.CanRead(ready)
				}
			} else if config.ordinaryDependency {
				rules.CanRead(golem.All[policyFactRuntimePost]())
			}
			if config.postMutationGrants {
				if !config.postCurrentGuard {
					rules.CanRead(golem.All[policyFactRuntimePost]())
				}
				rules.CanUpdate(golem.All[policyFactRuntimePost]())
			}
			return rules.Freeze(schema.Post)
		}),
		golem.GeneratedPolicyBinding[policyFactRuntimeActor, policyFactRuntimeComment](schema.Comment, func(actor policyFactRuntimeActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[policyFactRuntimeComment]()
			if config.ordinaryGuardGraph != nil {
				rules.CanRead(golem.GeneratedTextField[policyFactRuntimeComment, string](schema.CommentBody).Eq("bridge"))
			} else if config.commentOwnerRead {
				id := golem.GeneratedEqualField[policyFactRuntimeComment, golem.UUID](schema.CommentID)
				post := golem.GeneratedToOne[policyFactRuntimeComment, policyFactRuntimePost](schema.CommentPost, schema.Commenting, schema.Post)
				author := golem.GeneratedToOne[policyFactRuntimePost, policyFactRuntimeUser](schema.PostAuthor, schema.Authorship, schema.User)
				// Ordinary caller reach expands User's physical fact while
				// retaining this independent source-row actor restriction.
				rules.CanRead(id.Eq(actor.ID).And(post.Is(author.Is(golem.All[policyFactRuntimeUser]()))))
			}
			return rules.Freeze(schema.Comment)
		}),
	}
	if extra := config.ordinaryGuardGraph; extra != nil {
		policyBindings = append(policyBindings, golem.GeneratedPolicyBinding[policyFactRuntimeActor, policyFactRuntimeWatch](extra.Watch, func(actor policyFactRuntimeActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[policyFactRuntimeWatch]()
			profile := golem.GeneratedEqualField[policyFactRuntimeWatch, golem.UUID](extra.Profile)
			video := golem.GeneratedToOne[policyFactRuntimeWatch, policyFactRuntimePost](extra.RelationField, extra.Relation, schema.Post)
			comments := golem.GeneratedToMany[policyFactRuntimePost, policyFactRuntimeComment](schema.PostComments, schema.Commenting, schema.Comment)
			post := golem.GeneratedToOne[policyFactRuntimeComment, policyFactRuntimePost](schema.CommentPost, schema.Commenting, schema.Post)
			author := golem.GeneratedToOne[policyFactRuntimePost, policyFactRuntimeUser](schema.PostAuthor, schema.Authorship, schema.User)
			rules.CanRead(profile.Eq(actor.ID).And(video.Is(comments.Some(post.Is(author.Is(golem.All[policyFactRuntimeUser]()))))))
			return rules.Freeze(extra.Watch)
		}))
	}
	bindings, err := golem.GeneratedApplicationBindings(generation, golem.GeneratedStampedPackageBindings(generation, policyBindings, hooks))
	if err != nil {
		t.Fatal(err)
	}
	var metadata []golem.EventModelMetadata
	var factories []EventFactory
	for _, entry := range []struct {
		model golem.ModelID
		field golem.FieldID
		name  string
	}{{schema.User, schema.UserID, "UserEvent"}, {schema.Post, schema.PostID, "PostEvent"}, {schema.Comment, schema.CommentID, "CommentEvent"}} {
		model, _ := schema.Registry.Model(entry.model)
		fingerprint, _, _ := model.EventSchema()
		parsed, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		digest := golem.EventSchemaDigest(parsed)
		metadata = append(metadata, golem.GeneratedEventModelMetadata(entry.model, digest, []golem.FieldID{entry.field}, entry.name, "golem.UUID"))
		factories = append(factories, policyFactRuntimeFactory{model: entry.model, schema: digest})
	}
	eventRegistry, err := golem.GeneratedEventRegistry(generation, golem.GeneratedPackageEventRegistry(generation, metadata...))
	if err != nil {
		t.Fatal(err)
	}
	eventFactories, err := GeneratedEventFactoryRegistry(generation, GeneratedPackageEventFactories(generation, factories...))
	if err != nil {
		t.Fatal(err)
	}
	memory, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	transport := &policyFactRuntimeTransport{EventTransport: memory, published: make(chan events.Notice, 16)}
	observer := policyFactRuntimeObserver{post: schema.Post, evaluated: make(chan struct{}, 16)}
	// This opt-in fixture has four ordinary hops followed by a two-hop
	// physical fact. Use the public configuration rather than changing the
	// default depth or bypassing policy expansion/cycle validation.
	limits := ReadLimits{}
	if config.ordinaryGuardGraph != nil {
		limits.MaxRelationDepth = 6
	}
	applicationConfig := Config[policyFactRuntimePrincipal, policyFactRuntimeActor]{
		Database: database, Bundle: schema.Bundle, Bindings: bindings, Descriptors: descriptors, ReadLimits: limits,
		AfterCommitError: config.afterCommitError,
		EventRegistry:    eventRegistry, EventFactories: eventFactories, EventTransport: transport,
		EventLimits: events.Limits{PublisherConcurrency: 1, EvaluationConcurrency: 1, RetentionEvery: events.RetentionDisabled, RevalidateEvery: config.revalidateEvery},
		Observer:    observer, ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		ResolvePrincipal: func(_ context.Context, principal policyFactRuntimePrincipal) (policyFactRuntimeActor, error) {
			actor := policyFactRuntimeActor{ID: principal.ID, Negative: principal.Negative, NameGrant: "source"}
			if config.auth != nil {
				if config.auth.rejected.Load() {
					return policyFactRuntimeActor{}, errors.New("fixture principal revoked")
				}
				if grant := config.auth.grantID.Load(); grant != 0 {
					actor.ID = golem.UUID{15: byte(grant)}
				}
				if config.auth.otherNameGrant.Load() {
					actor.NameGrant = "other"
				}
			}
			return actor, nil
		},
	}
	if config.semanticEmbeddings != nil {
		applicationConfig.Embeddings = *config.semanticEmbeddings
		applicationConfig.Queue = &QueueConfig{Registry: queue.NewRegistry()}
	}
	app, err := Open(ctx, applicationConfig)
	if err != nil {
		t.Fatal(err)
	}
	return policyFactRuntimeFixture{
		app: app, ctx: ctx, schema: schema, users: users, posts: posts,
		stop: cancel, databaseConfig: databaseConfig, applicationConfig: applicationConfig,
		postID:   golem.GeneratedEqualField[policyFactRuntimePost, golem.UUID](schema.PostID),
		authorID: golem.GeneratedEqualField[policyFactRuntimePost, golem.UUID](schema.AuthorID), title: title,
		transport: transport, evaluated: observer.evaluated,
		principal: policyFactRuntimePrincipal{ID: golem.UUID{15: 1}, Negative: negative},
	}
}

func policyFactRuntimeOpenPostgreSQL(t *testing.T, ctx context.Context, dsn string) (*providerapi.Database, error) {
	t.Helper()
	if err := testenv.ValidatePostgreSQLRoot(ctx, os.Getenv("GOLEM_POLICY_HISTORY_PG_ROOT"), dsn); err != nil {
		return nil, err
	}
	return publicpostgresql.Open(ctx, publicpostgresql.Config{DataSourceName: dsn, Pool: publicpostgresql.PoolConfig{MaximumOpen: 4, MaximumIdle: 4}})
}

// Apply the real reviewed baseline and embed its manifest. Verified handles
// must pass the same manifest/head/ledger checks as a generated application.
func policyFactRuntimeApplyMigration(t *testing.T, ctx context.Context, database *providerapi.Database, fixture schematest.GraphFixture) golem.SchemaBundle {
	t.Helper()
	physicalSchema := fixture.SQLite
	if database.Provider() == golem.PostgreSQL {
		physicalSchema = fixture.PostgreSQL
	}
	desired, err := physical.Normalize(physicalSchema)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := physical.Normalize(physical.PhysicalSchema{
		Version: desired.Version, CanonicalVersion: desired.CanonicalVersion,
		Provider: desired.Provider, Namespace: desired.Namespace,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migration.Diff(empty, desired)
	if err != nil {
		t.Fatal(err)
	}
	emptyModel, err := compilerir.ModelFingerprint(compilerir.CanonicalEmptyModel())
	if err != nil {
		t.Fatal(err)
	}
	allowlist, err := physical.UnmanagedAllowlistFingerprint(desired)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{
		ID: "0001_initial", Operations: plan.Operations, Phases: plan.Phases,
		BeforeModel: migration.Digest(emptyModel), AfterModel: migration.Digest(fixture.Bundle.Model().Fingerprint().String()),
		BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint,
		BeforeSnapshot: empty, AfterSnapshot: desired,
		UnmanagedAllowlistDigest: migration.Digest(allowlist.String()),
	}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
	}
	var scriptSQL string
	path := "migrations/sqlite/0001_initial.sql"
	apply := internalsqlite.New().ApplyMigration
	if database.Provider() == golem.PostgreSQL {
		provider := internalpostgresql.New()
		script, err := provider.RenderMigration(entry)
		if err != nil {
			t.Fatal(err)
		}
		scriptSQL, apply = script.SQL(), provider.ApplyMigration
		path = "migrations/postgresql/0001_initial.sql"
	} else {
		script, err := internalsqlite.New().RenderMigration(entry)
		if err != nil {
			t.Fatal(err)
		}
		scriptSQL = script.SQL()
	}
	files := map[string][]byte{path: []byte(scriptSQL)}
	entry.Files = []migration.FileChecksum{{Path: path, SHA256: migration.Checksum(files[path])}}
	entry.ChainHash = migration.ChainHash(entry)
	manifest := migration.Manifest{
		FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion,
		HashAlgorithm: "sha256", GeneratorVersion: "policy-fact-runtime-test", Provider: desired.Provider,
		Entries: []migration.ManifestEntry{entry},
	}
	encoded, err := migration.EncodeManifest(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(ctx, database.UnsafeSQLX(), manifest, files); err != nil {
		t.Fatal(err)
	}
	providers := fixture.Bundle.Providers()
	for index, document := range providers {
		if document.Provider() == database.Provider() {
			reviewed := golem.GeneratedMigrationManifestDocument(fixture.Bundle.GenerationDigest(), database.Provider(), encoded)
			providers[index] = golem.GeneratedProviderSchemaDocumentWithMigration(document.Provider(), document.SystemFingerprint(), document.Schema(), reviewed)
		}
	}
	return golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		fixture.Bundle.Model(), fixture.Bundle.Contract(), providers...,
	)
}

func (fixture policyFactRuntimeFixture) caller(t *testing.T) *Caller[policyFactRuntimePrincipal, policyFactRuntimeActor] {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(fixture.ctx, fixture.principal)
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func (fixture policyFactRuntimeFixture) subscribe(t *testing.T, options ...golem.EventOption[policyFactRuntimeUser]) golem.EventStream[golem.EventMetadata] {
	t.Helper()
	stream, err := CallerEvents[policyFactRuntimePrincipal, policyFactRuntimeActor, policyFactRuntimeUser, golem.EventMetadata](fixture.ctx, fixture.caller(t), fixture.users, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func (fixture policyFactRuntimeFixture) assertVisible(t *testing.T, ready bool) {
	t.Helper()
	want := int64(0)
	if ready != fixture.principal.Negative {
		want = 1
	}
	count, err := CallerCount(fixture.ctx, fixture.caller(t), fixture.users)
	if err != nil || count != want {
		t.Fatalf("ready=%v negative=%v visible=%d want=%d err=%v", ready, fixture.principal.Negative, count, want, err)
	}
}

func (fixture policyFactRuntimeFixture) assertDependencyHidden(t *testing.T) {
	t.Helper()
	count, err := CallerCount(fixture.ctx, fixture.caller(t), fixture.posts)
	var refusal *golem.Error
	if err != nil && (!errors.As(err, &refusal) || refusal.Code != golem.CodeForbidden) || err == nil && count != 0 {
		t.Fatalf("hidden dependency count=%d err=%v", count, err)
	}
}

func (fixture policyFactRuntimeFixture) mutate(t *testing.T, action golem.EventAction, post, author byte, title string) {
	t.Helper()
	id := golem.UUID{15: post}
	target := golem.GeneratedUniqueSelectorValue[policyFactRuntimePost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, id))
	var err error
	switch action {
	case golem.EventCreated:
		_, err = SystemCreate(fixture.ctx, fixture.app.System(), fixture.posts, golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, id),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: author}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, title),
		))
	case golem.EventUpdated:
		_, err = SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, target, golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, title)))
	case golem.EventDeleted:
		_, err = SystemDelete(fixture.ctx, fixture.app.System(), fixture.posts, target)
	default:
		t.Fatalf("unsupported test action %q", action)
	}
	if err != nil {
		t.Fatalf("hidden post %s: %v", action, err)
	}
}

func (fixture policyFactRuntimeFixture) startPublisher(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(fixture.ctx)
	done := make(chan error, 1)
	go func() { done <- fixture.app.RunEventPublisher(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("publisher shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("publisher did not stop")
		}
	})
}

func (fixture policyFactRuntimeFixture) awaitPublication(t *testing.T, action golem.EventAction) {
	t.Helper()
	ctx, cancel := context.WithTimeout(fixture.ctx, 5*time.Second)
	defer cancel()
	select {
	case notice := <-fixture.transport.published:
		if notice.ModelID() != fixture.schema.Post || notice.Action() != action {
			t.Fatalf("publisher emitted model=%x action=%s, want hidden Post %s", notice.ModelID(), notice.Action(), action)
		}
	case <-ctx.Done():
		t.Fatal("durable outbox publisher did not publish the hidden change")
	}
}

func (fixture policyFactRuntimeFixture) awaitPublicationAndEvaluation(t *testing.T, action golem.EventAction, evaluations int) {
	t.Helper()
	fixture.awaitPublication(t, action)
	ctx, cancel := context.WithTimeout(fixture.ctx, 5*time.Second)
	defer cancel()
	for index := 0; index < evaluations; index++ {
		select {
		case <-fixture.evaluated:
		case <-ctx.Done():
			t.Fatalf("hidden change finished %d of %d dependency evaluations", index, evaluations)
		}
	}
}

func (fixture policyFactRuntimeFixture) expectRecovery(t *testing.T, stream golem.EventStream[golem.EventMetadata], action golem.EventAction) {
	t.Helper()
	fixture.expectTermination(t, stream, events.CodeSubscriptionResync, events.ReasonDependencyChanged)
}

func (fixture policyFactRuntimeFixture) expectTermination(t *testing.T, stream golem.EventStream[golem.EventMetadata], wantCode events.ErrorCode, wantReason events.Reason) {
	t.Helper()
	ctx, cancel := context.WithTimeout(fixture.ctx, 5*time.Second)
	defer cancel()
	_, err := stream.Recv(ctx)
	code, _ := events.CodeOf(err)
	reason, _ := events.ReasonOf(err)
	if code != wantCode || reason != wantReason {
		t.Fatalf("principal=%s ended with %v (code=%q reason=%q), want code=%q reason=%q", fixture.principal.ID, err, code, reason, wantCode, wantReason)
	}
}

func TestPolicyFactRuntimeHiddenDependencyRecovery(t *testing.T) {
	for _, negative := range []bool{false, true} {
		name := "positive"
		if negative {
			name = "negative"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newHistoricalPolicyFactRuntimeFixture(t, negative)
			fixture.assertVisible(t, false)
			stream := fixture.subscribe(t)
			fixture.mutate(t, golem.EventCreated, 12, 2, "ready")
			// Before starting the publisher, prove the system write committed
			// a durable fact rather than publishing a synthetic notice.
			var stored int
			if err := fixture.app.database.GetContext(fixture.ctx, &stored, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || stored == 0 {
				t.Fatalf("committed hidden create has no outbox fact: rows=%d err=%v", stored, err)
			}
			fixture.startPublisher(t)
			for _, action := range []golem.EventAction{golem.EventCreated, golem.EventUpdated, golem.EventDeleted} {
				if action != golem.EventCreated {
					fixture.mutate(t, action, 12, 2, "draft")
				}
				fixture.awaitPublicationAndEvaluation(t, action, 1)
				if action == golem.EventCreated {
					fixture.assertDependencyHidden(t)
				}
				receiveContext, cancel := context.WithTimeout(fixture.ctx, 20*time.Millisecond)
				_, err := stream.Recv(receiveContext)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("another actor's hidden %s was observable: %v", action, err)
				}
				fixture.assertVisible(t, false)
			}
			for _, change := range []struct {
				action golem.EventAction
				title  string
				ready  bool
			}{{golem.EventCreated, "ready", true}, {golem.EventUpdated, "draft", false}, {golem.EventUpdated, "ready", true}, {golem.EventDeleted, "", false}} {
				fixture.mutate(t, change.action, 11, 1, change.title)
				fixture.awaitPublicationAndEvaluation(t, change.action, 1)
				fixture.expectRecovery(t, stream, change.action)
				fixture.assertVisible(t, change.ready)
				if change.action == golem.EventCreated {
					// A visible source discloses its fact's Boolean effect while
					// the existing physical target remains unreadable.
					fixture.assertDependencyHidden(t)
				}
				stream = fixture.subscribe(t)
			}

			// Establish a ready target through the existing stream and consume
			// its publication/evaluation before opening the two new audiences.
			// This prevents setup work from satisfying the reparenting barriers.
			fixture.mutate(t, golem.EventCreated, 11, 1, "ready")
			fixture.awaitPublicationAndEvaluation(t, golem.EventCreated, 1)
			fixture.expectRecovery(t, stream, golem.EventCreated)
			other := fixture
			other.principal.ID = golem.UUID{15: 2}
			fixture.assertVisible(t, true)
			other.assertVisible(t, false)
			oldAudience, newAudience := fixture.subscribe(t), other.subscribe(t)
			target := golem.GeneratedUniqueSelectorValue[policyFactRuntimePost](fixture.schema.Post, fixture.schema.PostKey,
				golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 11}))
			_, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, target,
				golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
					golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, other.principal.ID)))
			if err != nil {
				t.Fatalf("reparent hidden ready post: %v", err)
			}
			fixture.awaitPublicationAndEvaluation(t, golem.EventUpdated, 2)
			fixture.expectRecovery(t, oldAudience, golem.EventUpdated)
			other.expectRecovery(t, newAudience, golem.EventUpdated)
			fixture.assertVisible(t, false)
			other.assertVisible(t, true)
			fixture.assertDependencyHidden(t)
			other.assertDependencyHidden(t)
		})
	}
}

func TestPolicyFactRuntimeFreshAuthorization(t *testing.T) {
	t.Run("ordinary dependency checks fact authority", func(t *testing.T) {
		auth := &policyFactRuntimeAuth{}
		fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{auth: auth, ordinaryDependency: true})
		// The fact is root-local and has no physical paths. Readable Posts
		// are reached only by the ordinary relation beside that fact.
		stream := fixture.subscribe(t)
		fixture.startPublisher(t)
		auth.rejected.Store(true)
		fixture.mutate(t, golem.EventCreated, 11, 1, "ready")
		fixture.awaitPublication(t, golem.EventCreated)
		fixture.expectTermination(t, stream, events.CodeSubscriptionRevalidation, "")
	})

	t.Run("periodic selected ordinary scalar mask drift", func(t *testing.T) {
		auth := &policyFactRuntimeAuth{}
		fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
			auth: auth, revalidateEvery: time.Second, unusedFact: true, nameMask: true,
		})
		name := golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName)
		stream := fixture.subscribe(t, golem.EventSelect[policyFactRuntimeUser](name))
		// The source scope stays ID=1. Only the ordinary selected-field mask
		// changes, from Name="source" to Name="other", without a data event.
		auth.otherNameGrant.Store(true)
		fixture.expectTermination(t, stream, events.CodeSubscriptionResync, "")
		var stored int
		if err := fixture.app.database.GetContext(fixture.ctx, &stored, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || stored != 0 {
			t.Fatalf("selected-mask recovery required a data event: rows=%d err=%v", stored, err)
		}
	})

	t.Run("revoked principal", func(t *testing.T) {
		auth := &policyFactRuntimeAuth{}
		fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{auth: auth})
		stream := fixture.subscribe(t)
		fixture.startPublisher(t)
		auth.rejected.Store(true)
		fixture.mutate(t, golem.EventCreated, 11, 1, "ready")
		fixture.awaitPublication(t, golem.EventCreated)
		// Authorization errors need not emit a suppression observation.
		fixture.expectTermination(t, stream, events.CodeSubscriptionRevalidation, "")
	})

	t.Run("changed nonconstant scope and reopen", func(t *testing.T) {
		auth := &policyFactRuntimeAuth{}
		fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{auth: auth})
		stream := fixture.subscribe(t)
		fixture.startPublisher(t)
		// The immutable principal stays the same. The real resolver returns a
		// different actor, whose value naturally receives a distinct cache key.
		auth.grantID.Store(2)
		fixture.mutate(t, golem.EventCreated, 11, 1, "ready")
		fixture.awaitPublication(t, golem.EventCreated)
		fixture.expectTermination(t, stream, events.CodeSubscriptionResync, "")
		fixture.assertVisible(t, false)

		stream = fixture.subscribe(t)
		fixture.mutate(t, golem.EventCreated, 12, 2, "ready")
		fixture.awaitPublicationAndEvaluation(t, golem.EventCreated, 1)
		fixture.expectRecovery(t, stream, golem.EventCreated)
		fixture.assertVisible(t, true)
		fixture.assertDependencyHidden(t)

		stream = fixture.subscribe(t)
		fixture.mutate(t, golem.EventUpdated, 11, 1, "draft")
		fixture.awaitPublicationAndEvaluation(t, golem.EventUpdated, 1)
		ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Millisecond)
		defer cancel()
		if _, err := stream.Recv(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("reopened scope observed its former actor's activity: %v", err)
		}
		fixture.assertVisible(t, true)
	})

	t.Run("periodic revalidation without data events", func(t *testing.T) {
		for _, unusedFact := range []bool{false, true} {
			name := "referenced fact"
			if unusedFact {
				name = "unused fact with unconditional grant"
			}
			t.Run(name, func(t *testing.T) {
				auth := &policyFactRuntimeAuth{}
				fixture := newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{auth: auth, revalidateEvery: time.Second, unusedFact: unusedFact})
				stream := fixture.subscribe(t)
				auth.grantID.Store(2)
				fixture.expectTermination(t, stream, events.CodeSubscriptionResync, "")
				var stored int
				if err := fixture.app.database.GetContext(fixture.ctx, &stored, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || stored != 0 {
					t.Fatalf("periodic scope recovery required a data event: rows=%d err=%v", stored, err)
				}
			})
		}
	})
}

func TestPolicyFactRuntimeCheckContextFollowsBothOwners(t *testing.T) {
	for _, subscriberEnds := range []bool{false, true} {
		hub, stopHub := context.WithCancel(context.Background())
		subscriber, stopSubscriber := context.WithCancel(context.Background())
		check, release := subscriptionCheckContext(hub, subscriber)
		if subscriberEnds {
			stopSubscriber()
		} else {
			stopHub()
		}
		select {
		case <-check.Done():
		case <-time.After(time.Second):
			t.Errorf("subscriberEnds=%v authorization work outlived its owner", subscriberEnds)
		}
		release()
		stopHub()
		stopSubscriber()
	}
}

// The Watch source is intentionally unsubscribed; tests exercise the routed
// consumer's real SQL fence without claiming Watch transport coverage.
func policyFactRuntimeWatchDescriptor(extra schematest.OrdinaryGuardGraphFixture) golem.ModelDescriptor[policyFactRuntimeWatch] {
	return golem.GeneratedModelDescriptor[policyFactRuntimeWatch](extra.Watch, golem.GeneratedDescriptorShape(
		[]golem.FieldID{extra.ID, extra.Profile, extra.Video}, nil,
		[]golem.IdentityMetadata{golem.GeneratedIdentityMetadata(extra.Watch, extra.Key, golem.PrimaryIdentity, extra.ID)},
		[]golem.RelationMetadata{golem.GeneratedRelationMetadata(extra.Watch, extra.Graph.Post, extra.RelationField, extra.Relation, golem.RelationSource, golem.RelationToOne)},
	))
}
