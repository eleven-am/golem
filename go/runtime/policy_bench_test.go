package runtime

import (
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type benchPolicyPrincipal struct{}
type benchPolicyActor struct{}
type benchPolicyUser struct{}
type benchPolicyPost struct{}

type benchPolicyFixture struct {
	app            *App[benchPolicyPrincipal, benchPolicyActor]
	schema         schematest.GraphFixture
	userDescriptor golem.ModelDescriptor[benchPolicyUser]
	postDescriptor golem.ModelDescriptor[benchPolicyPost]
	postID         golem.EqualField[benchPolicyPost, golem.UUID]
	postTitle      golem.TextField[benchPolicyPost, string]
	authorID       golem.EqualField[benchPolicyPost, golem.UUID]
	author         golem.UUID
	next           uint64
}

func newBenchPolicyFixture(b *testing.B, branches int) *benchPolicyFixture {
	b.Helper()
	ctx := context.Background()
	schema := schematest.NewGraph(b)
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(b.TempDir(), "policy-bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
		b.Fatal(err)
	}
	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)
	userPosts := golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)
	postAuthor := golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[benchPolicyUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userPosts},
	))
	postScalars := []golem.FieldID{schema.PostID, schema.AuthorID, schema.PostTitle}
	if schema.PostUpdatedAt != (golem.FieldID{}) {
		postScalars = append(postScalars, schema.PostUpdatedAt)
	}
	postDescriptor := golem.GeneratedModelDescriptor[benchPolicyPost](schema.Post, golem.GeneratedDescriptorShape(
		postScalars, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postAuthor},
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata()))
	if err != nil {
		b.Fatal(err)
	}
	postTitle := golem.GeneratedTextField[benchPolicyPost, string](schema.PostTitle)
	userName := golem.GeneratedTextField[benchPolicyUser, string](schema.UserName)
	author := golem.GeneratedToOne[benchPolicyPost, benchPolicyUser](schema.PostAuthor, schema.Authorship)
	userPolicy := golem.GeneratedPolicyBinding[benchPolicyActor, benchPolicyUser](schema.User, func(benchPolicyActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[benchPolicyUser]()
		rules.CanRead(golem.All[benchPolicyUser]())
		rules.CanCreate(golem.All[benchPolicyUser]())
		return rules.Freeze(schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[benchPolicyActor, benchPolicyPost](schema.Post, func(benchPolicyActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[benchPolicyPost]()
		reads := make([]golem.Predicate[benchPolicyPost], 0, 2*branches)
		creates := make([]golem.Predicate[benchPolicyPost], 0, 2*branches)
		for index := 0; index < branches; index++ {
			title := fmt.Sprintf("title-%d", index)
			name := fmt.Sprintf("name-%d", index)
			reads = append(reads, postTitle.Eq(title), author.Is(userName.Eq(name)))
			creates = append(creates, postTitle.Eq(title), author.Is(userName.Eq(name)))
		}
		rules.CanRead(golem.Or(reads...))
		rules.CanCreate(golem.Or(creates...))
		rules.CanUpdate(golem.Or(reads...))
		return rules.Freeze(schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[benchPolicyActor]{userPolicy, postPolicy}, nil))
	if err != nil {
		b.Fatal(err)
	}
	app, err := Open(ctx, Config[benchPolicyPrincipal, benchPolicyActor]{
		Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: schema.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, benchPolicyPrincipal) (benchPolicyActor, error) {
			return benchPolicyActor{}, nil
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	fixture := &benchPolicyFixture{
		app: app, schema: schema, userDescriptor: userDescriptor, postDescriptor: postDescriptor,
		postID: golem.GeneratedEqualField[benchPolicyPost, golem.UUID](schema.PostID), postTitle: postTitle,
		authorID: golem.GeneratedEqualField[benchPolicyPost, golem.UUID](schema.AuthorID), author: golem.UUID{0: 1},
	}
	if _, err := SystemCreate(ctx, app.System(), userDescriptor, golem.GeneratedCreateInput[benchPolicyUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, golem.GeneratedEqualField[benchPolicyUser, golem.UUID](schema.UserID), fixture.author),
		golem.GeneratedCreateFieldValue(schema.User, golem.GeneratedTextField[benchPolicyUser, string](schema.UserName), "seed"),
	)); err != nil {
		b.Fatal(err)
	}
	return fixture
}

func (fixture *benchPolicyFixture) nextPost() (golem.UUID, golem.CreateInput[benchPolicyPost]) {
	fixture.next++
	var id golem.UUID
	binary.BigEndian.PutUint64(id[8:], fixture.next)
	return id, golem.GeneratedCreateInput[benchPolicyPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, id),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, fixture.author),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "title-0"),
	)
}

func (fixture *benchPolicyFixture) caller(b *testing.B) *Caller[benchPolicyPrincipal, benchPolicyActor] {
	b.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), benchPolicyPrincipal{})
	if err != nil {
		b.Fatal(err)
	}
	return caller
}

func benchmarkPolicyCreate(b *testing.B, fixture *benchPolicyFixture, system bool) {
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_, input := fixture.nextPost()
		var err error
		if system {
			_, err = SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, input)
		} else {
			_, err = CallerCreate(ctx, fixture.caller(b), fixture.postDescriptor, input)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkPolicyFindFirst(b *testing.B, fixture *benchPolicyFixture, system bool) {
	ctx := context.Background()
	id, input := fixture.nextPost()
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, input); err != nil {
		b.Fatal(err)
	}
	where := golem.Where[benchPolicyPost](fixture.postID.Eq(id))
	b.ReportAllocs()
	for b.Loop() {
		var found bool
		var err error
		if system {
			_, found, err = SystemFindFirst(ctx, fixture.app.System(), fixture.postDescriptor, where)
		} else {
			_, found, err = CallerFindFirst(ctx, fixture.caller(b), fixture.postDescriptor, where)
		}
		if err != nil || !found {
			b.Fatalf("found=%t err=%v", found, err)
		}
	}
}

func BenchmarkCreate(b *testing.B) {
	b.Run("system", func(b *testing.B) { benchmarkPolicyCreate(b, newBenchPolicyFixture(b, 4), true) })
	b.Run("caller", func(b *testing.B) { benchmarkPolicyCreate(b, newBenchPolicyFixture(b, 4), false) })
}

func BenchmarkFindFirst(b *testing.B) {
	b.Run("system", func(b *testing.B) { benchmarkPolicyFindFirst(b, newBenchPolicyFixture(b, 4), true) })
	b.Run("caller", func(b *testing.B) { benchmarkPolicyFindFirst(b, newBenchPolicyFixture(b, 4), false) })
}

func BenchmarkForPrincipal(b *testing.B) {
	fixture := newBenchPolicyFixture(b, 4)
	b.ReportAllocs()
	for b.Loop() {
		fixture.caller(b)
	}
}

func BenchmarkPolicyGrowth(b *testing.B) {
	for _, branches := range []int{1, 4, 16, 64} {
		b.Run(fmt.Sprintf("branches=%d", branches), func(b *testing.B) {
			b.Run("create", func(b *testing.B) { benchmarkPolicyCreate(b, newBenchPolicyFixture(b, branches), false) })
			b.Run("findFirst", func(b *testing.B) { benchmarkPolicyFindFirst(b, newBenchPolicyFixture(b, branches), false) })
		})
	}
}
