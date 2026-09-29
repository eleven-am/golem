package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

type identityEventFactory struct {
	model  golem.ModelID
	schema golem.EventSchemaDigest
}

func (factory identityEventFactory) ModelID() golem.ModelID { return factory.model }
func (factory identityEventFactory) EventSchemaDigest() golem.EventSchemaDigest {
	return factory.schema
}
func (factory identityEventFactory) Build(value ValidatedEvent) (any, error) {
	return value.IdentityValues(), nil
}

func identityEventFactories(t *testing.T, config *Config[mutationResultPrincipal, mutationResultActor]) {
	t.Helper()
	registry, err := schema.New(config.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	factories := make([]EventFactory, 0)
	for _, model := range registry.EventModels() {
		fingerprint, _, _ := model.EventSchema()
		digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		factories = append(factories, identityEventFactory{model: model.ID(), schema: golem.EventSchemaDigest(digest)})
	}
	config.EventFactories, err = GeneratedEventFactoryRegistry(config.Bundle.GenerationDigest(), GeneratedPackageEventFactories(config.Bundle.GenerationDigest(), factories...))
	if err != nil {
		t.Fatal(err)
	}
}

func TestSubscribersReceiveAnEmptyBytesKeyIntact(t *testing.T) {
	runExactValueProfilesFor(t, schematest.NewSubscribedBytesKeyed, func(t *testing.T, profile exactValueProfile) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		provider := golem.SQLite
		if profile.provider == policyir.ProviderPostgreSQL {
			provider = golem.PostgreSQL
		}
		schema := profile.fixture
		allowUsers := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[mutationResultUser]()
			rules.CanRead(golem.All[mutationResultUser]())
			return rules.Freeze(schema.User)
		})
		allowPosts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[mutationResultPost]()
			rules.CanRead(golem.All[mutationResultPost]())
			rules.CanCreate(golem.All[mutationResultPost]())
			return rules.Freeze(schema.Post)
		})
		bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(),
			golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{allowUsers, allowPosts}, nil))
		if err != nil {
			t.Fatal(err)
		}
		reader := newBytesKeyedReader(t, profile.database, provider, schema, func(config *Config[mutationResultPrincipal, mutationResultActor]) {
			config.Bindings = bindings
			identityEventFactories(t, config)
		})
		app := reader.fixture.app
		caller, err := app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		stream, err := CallerEvents[mutationResultPrincipal, mutationResultActor, mutationResultPost, []any](ctx, caller, reader.posts)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		published := make(chan error, 1)
		go func() { published <- app.RunEventPublisher(ctx) }()
		post := profile.fixture.Post
		if _, err := SystemCreate(ctx, app.System(), reader.posts, golem.GeneratedCreateInput[mutationResultPost](post,
			golem.GeneratedCreateFieldValue(post, reader.postID, []byte{}),
			golem.GeneratedCreateFieldValue(post, golem.GeneratedBytesField[mutationResultPost](profile.fixture.AuthorID), []byte{}),
			golem.GeneratedCreateFieldValue(post, golem.GeneratedTextField[mutationResultPost, string](profile.fixture.PostAuthorName), "alice"),
			golem.GeneratedCreateFieldValue(post, reader.title, "alice"),
		)); err != nil {
			t.Fatalf("create a row keyed by empty bytes: %v", err)
		}
		receiveContext, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		identity, err := stream.Recv(receiveContext)
		if err != nil {
			t.Fatalf("subscriber did not receive the change: %v", err)
		}
		key, isBytes := identity[0].([]byte)
		if len(identity) != 2 || !isBytes || key == nil || len(key) != 0 || identity[1] != "alice" {
			t.Fatalf("event identity=%#v; want an empty non-nil bytes key and alice", identity)
		}
		cancel()
		<-published
	})
}
