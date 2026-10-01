package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const resolverSurfaceSchema = `package notes

import (
	"time"

	"github.com/eleven-am/golem/go/golem"
)

type Actor struct {
	Authenticated bool
}

type Principal struct {
	Authenticated bool
}

type Author struct {
	_ struct{} ` + "`golem:\"model;id=example.notes.Author;table=authors;graphql=Author\"`" + `
	_ struct{} ` + "`golem:\"unique=uq_authors_handle(handle)\"`" + `

	ID      golem.UUID ` + "`db:\"id\" golem:\"id=example.notes.Author.ID;pk;default=uuid\"`" + `
	Handle  string     ` + "`db:\"handle\" golem:\"type=varchar(40)\"`" + `
	Version int64      ` + "`db:\"version\"`" + `
	Notes   []Note     ` + "`db:\"-\" golem:\"relation=has_many;fields=id;references=author_id\"`" + `
}

func (Author) GolemModel() golem.ModelSpec[Author] {
	return golem.DefineModel(golem.OptimisticConcurrency(Authors.Version))
}

type Note struct {
	_ struct{} ` + "`golem:\"model;id=example.notes.Note;table=notes;graphql=Note\"`" + `
	_ struct{} ` + "`golem:\"unique=uq_notes_slug(slug)\"`" + `

	ID        golem.UUID ` + "`db:\"id\" golem:\"id=example.notes.Note.ID;pk;default=uuid\"`" + `
	AuthorID  golem.UUID ` + "`db:\"author_id\"`" + `
	Slug      string     ` + "`db:\"slug\" golem:\"type=varchar(80)\"`" + `
	Title     string     ` + "`db:\"title\" golem:\"type=varchar(200)\"`" + `
	Body      string     ` + "`db:\"body\" golem:\"type=varchar(2000)\"`" + `
	Views     int64      ` + "`db:\"views\" golem:\"default=0\"`" + `
	TagCount  int32      ` + "`db:\"tag_count\" golem:\"default=0;system\"`" + `
	CreatedAt time.Time  ` + "`db:\"created_at\" golem:\"default=now;readonly\"`" + `
	Author    *Author    ` + "`db:\"-\" golem:\"relation=belongs_to;fields=author_id;references=id\"`" + `
}

func (Note) GolemModel() golem.ModelSpec[Note] {
	return golem.DefineModel(
		golem.ScopedReads[Note](),
		golem.Subscriptions[Note](),
		golem.Analytics[Note](
			golem.AnalyticsDimensions(Notes.AuthorID),
			golem.AnalyticsMeasures(Notes.ID, Notes.Views),
			golem.AnalyticsRelationDimensions(
				golem.NamedRelationDimension("authorHandle", golem.Via(Notes.Author, golem.DimensionField(Authors.Handle))),
			),
		),
		golem.SemanticIndex("related", "content", Notes.Title, Notes.Body),
		golem.FullTextIndex("content",
			golem.FullTextField(Notes.Title, 3),
			golem.FullTextField(Notes.Body, 1),
		),
	)
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "notes")
	golem.Actor[Actor](schema)
	golem.Model[Author](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite)
	golem.EmbeddingSpace(schema, "content", 3)
}
`

const resolverSurfacePolicies = `package notes

import "github.com/eleven-am/golem/go/golem"

func (Author) DefinePolicy(rules *golem.Rules[Author], actor Actor) {
	rules.CanRead(golem.All[Author]())
	if actor.Authenticated {
		rules.CanCreate(golem.All[Author]())
	}
}

func (Note) DefinePolicy(rules *golem.Rules[Note], actor Actor) {
	rules.CanRead(golem.All[Note]())
	if actor.Authenticated {
		rules.CanCreate(golem.All[Note]())
		rules.CanUpdate(golem.All[Note]())
	}
}
`

const resolverSurfaceArguments = `package notes

import "github.com/eleven-am/golem/go/golem"

type TermArgs struct {
	Term string ` + "`golem:\"graphql=term\"`" + `
}

type NoteArgs struct {
	ID golem.UUID ` + "`golem:\"graphql=id\"`" + `
}
`

var resolverSurfaceFamilies = map[string]string{
	"selectors": `package notes

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "noteViews", NoteViews)
}

func NoteViews(ctx context.Context, caller *Caller[Principal], arguments NoteArgs) (int64, error) {
	row, err := caller.Notes.FindUnique(ctx, Notes.ByID.Value(arguments.ID))
	if err != nil {
		return 0, err
	}
	if _, err := caller.Notes.FindUnique(ctx, Notes.BySlug.Value("slug")); err != nil {
		return 0, err
	}
	if _, err := caller.Authors.FindUnique(ctx, Authors.ByHandle.Value("ada")); err != nil {
		return 0, err
	}
	views, _ := golem.Value(row, Notes.Views).Get()
	return views, nil
}
`,
	"accessors": `package notes

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/queryplan"
)

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Mutation(graphql, "touchNotes", TouchNotes)
}

func TouchNotes(ctx context.Context, caller *Caller[Principal], arguments TermArgs) (int64, error) {
	var _ golem.Scope[Note] = Notes.Scope()
	var _ golem.RelationGroupRequest[Note] = Notes.RelationGroupBy(Notes.RelationGroupDimensions(Notes.AuthorHandle))
	if _, err := caller.Notes.RelationGroupBy(ctx, Notes.RelationGroupBy(Notes.RelationGroupDimensions(Notes.AuthorHandle))); err != nil {
		return 0, err
	}
	var _ func(context.Context, golem.ScopedQuery[Note]) (queryplan.Report, error) = caller.Notes.ExplainScoped
	var touched int64
	err := caller.Transaction(ctx, func(transaction *CallerTx[Principal]) error {
		updated, err := SystemEscape(transaction).Notes.UpdateMany(ctx, Notes.Title.Eq(arguments.Term), Notes.UpdateMany(Notes.System().TagCount.Increment(1)))
		touched = updated
		return err
	})
	return touched, err
}
`,
	"semantic": `package notes

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "relatedCount", RelatedCount)
}

func RelatedCount(ctx context.Context, caller *Caller[Principal], arguments TermArgs) (int64, error) {
	results, err := caller.Notes.SearchRelated(ctx, arguments.Term, 3)
	if err != nil {
		return 0, err
	}
	var similar func(context.Context, golem.UniqueSelectorValue[Note], int, ...golem.Predicate[Note]) ([]golem.SemanticResult[Note], error) = caller.Notes.SimilarRelated
	_ = similar
	return int64(len(results)), nil
}
`,
	"fulltext": `package notes

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "textCount", TextCount)
}

func TextCount(ctx context.Context, caller *Caller[Principal], arguments TermArgs) (int64, error) {
	results, err := caller.Notes.TextSearchContent(ctx, arguments.Term, 10)
	if err != nil {
		return 0, err
	}
	selected, err := caller.Notes.TextSearchContentSelect(ctx, arguments.Term, 10, golem.Select(Notes.ID, Notes.Title))
	if err != nil {
		return 0, err
	}
	return int64(len(results) + len(selected)), nil
}
`,
	"events": `package notes

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "eventReady", EventReady)
}

func EventReady(ctx context.Context, caller *Caller[Principal], arguments TermArgs) (int64, error) {
	var subscribe func(context.Context, ...golem.EventOption[Note]) (golem.EventStream[NoteEvent], error) = caller.Notes.Events
	var identity NoteEventIdentity
	_, _ = subscribe, identity
	return int64(len(arguments.Term)), nil
}
`,
	"system": `package notes

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

var trusted System[Principal]

var serve func(*App[Principal], GraphQLConfig[Principal]) (*GraphQLServer, error) = (*App[Principal]).GraphQL

func Install(application *App[Principal]) { trusted = application.System() }

func CountNotes(ctx context.Context, system System[Principal]) (int64, error) {
	return system.Notes.Count(ctx)
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "noteTotal", NoteTotal)
}

func NoteTotal(ctx context.Context, caller *Caller[Principal], arguments TermArgs) (int64, error) {
	var _ func(context.Context, Config[Principal]) (*App[Principal], error) = Open[Principal]
	return CountNotes(ctx, trusted)
}
`,
}

func writeResolverSurfaceModule(t *testing.T, resolvers string) string {
	t.Helper()
	application := t.TempDir()
	golemModule, err := filepath.Abs(commandModuleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":             "module example.com/notes\n\ngo 1.25.0\n\nrequire github.com/eleven-am/golem/go v0.0.0\n\nreplace github.com/eleven-am/golem/go => " + filepath.ToSlash(golemModule) + "\n",
		"notes/schema.go":    resolverSurfaceSchema,
		"notes/policies.go":  resolverSurfacePolicies,
		"notes/arguments.go": resolverSurfaceArguments,
		"notes/resolvers.go": resolvers,
	}
	for path, body := range files {
		full := filepath.Join(application, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	quickstartGo(t, application, "mod", "tidy")
	return application
}

func copyResolverSurfaceModule(t *testing.T, application string) string {
	t.Helper()
	copied := t.TempDir()
	if err := os.CopyFS(copied, os.DirFS(application)); err != nil {
		t.Fatal(err)
	}
	return copied
}

func runResolverSurfaceCommand(t *testing.T, application string, arguments ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := runGolem(t, application, arguments, &stdout, &stderr); code != 0 {
		t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", arguments, code, stdout.String(), stderr.String())
	}
}

const resolverSurfaceMain = `package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"

	"example.com/notes/notes"
	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

type wordProvider struct{ specification embedding.Specification }

func (provider wordProvider) Specification() embedding.Specification { return provider.specification }

func (provider wordProvider) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	vectors := make([]embedding.Vector, 0, len(inputs))
	for _, input := range inputs {
		digest := fnv.New32a()
		_, _ = digest.Write([]byte(input.Text()))
		sum := digest.Sum32()
		vector, err := embedding.NewVector([]float32{float32(sum%97) / 97, float32((sum/97)%89) / 89, float32((sum/8633)%83) / 83})
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, vector)
	}
	return vectors, nil
}

func main() {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: DSN})
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	specification, err := embedding.NewSpecification("example", "word-count", "v1", 3, 32)
	if err != nil {
		log.Fatal(err)
	}
	registry, err := embedding.NewRegistry(map[string]embedding.Provider{"content": wordProvider{specification: specification}})
	if err != nil {
		log.Fatal(err)
	}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		log.Fatal(err)
	}
	application, err := notes.Open(ctx, notes.Config[notes.Principal]{
		EventTransport:      transport,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		Database:   database,
		Embeddings: registry,
		Queue:      &golemruntime.QueueConfig{Registry: queue.NewRegistry()},
		AuditPrincipal: func(notes.Principal) string { return "resolver" },
		ReportScopedQuery: func(context.Context, golem.ScopedAuditRecord) {},
		ResolvePrincipal: func(_ context.Context, principal notes.Principal) (notes.Actor, error) {
			return notes.Actor{Authenticated: principal.Authenticated}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	notes.Install(application)
	caller, err := application.ForPrincipal(ctx, notes.Principal{Authenticated: true})
	if err != nil {
		log.Fatal(err)
	}
	author, err := application.System().Authors.Create(ctx, notes.Authors.Create(notes.Authors.Handle.Create("ada")), notes.Authors.Select(notes.Authors.ID))
	if err != nil {
		log.Fatal(err)
	}
	authorID, _ := golem.Value(author, notes.Authors.ID).Get()
	created, err := caller.Notes.Create(ctx, notes.Notes.Create(
		notes.Notes.AuthorID.Create(authorID),
		notes.Notes.Slug.Create("slug"),
		notes.Notes.Title.Create("sourdough"),
		notes.Notes.Body.Create("flour water and patience"),
		notes.Notes.Views.Create(7),
	), notes.Notes.Select(notes.Notes.ID))
	if err != nil {
		log.Fatal(err)
	}
	noteID, _ := golem.Value(created, notes.Notes.ID).Get()
	views, err := notes.NoteViews(ctx, caller, notes.NoteArgs{ID: noteID})
	if err != nil {
		log.Fatal(err)
	}
	touched, err := notes.TouchNotes(ctx, caller, notes.TermArgs{Term: "sourdough"})
	if err != nil {
		log.Fatal(err)
	}
	text, err := notes.TextCount(ctx, caller, notes.TermArgs{Term: "sourdough"})
	if err != nil {
		log.Fatal(err)
	}
	related, err := notes.RelatedCount(ctx, caller, notes.TermArgs{Term: "flour"})
	if err != nil {
		log.Fatal(err)
	}
	ready, err := notes.EventReady(ctx, caller, notes.TermArgs{Term: "abc"})
	if err != nil {
		log.Fatal(err)
	}
	total, err := notes.NoteTotal(ctx, caller, notes.TermArgs{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("views=%d touched=%d text=%d related=%t ready=%d total=%d\n", views, touched, text, related >= 0, ready, total)
}
`

const resolverSurfaceBaseline = `package notes

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
)

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "noteCount", NoteCount)
}

func NoteCount(ctx context.Context, caller *Caller[Principal], arguments TermArgs) (int64, error) {
	return caller.Notes.Count(ctx)
}
`

func combinedResolverSurfaceFamilies(families []string) string {
	operations := map[string]string{"selectors": "Query(graphql, \"noteViews\", NoteViews)", "accessors": "Mutation(graphql, \"touchNotes\", TouchNotes)", "semantic": "Query(graphql, \"relatedCount\", RelatedCount)", "fulltext": "Query(graphql, \"textCount\", TextCount)", "events": "Query(graphql, \"eventReady\", EventReady)", "system": "Query(graphql, \"noteTotal\", NoteTotal)"}
	var combined strings.Builder
	combined.WriteString("package notes\n\nimport (\n\t\"context\"\n\n\t\"github.com/eleven-am/golem/go/golem\"\n\t\"github.com/eleven-am/golem/go/queryplan\"\n)\n\nvar _ queryplan.Report\n\nfunc DefineGraphQL(graphql *golem.GraphQLSchema) {\n")
	for _, family := range families {
		combined.WriteString("\tgolem." + operations[family] + "\n")
	}
	combined.WriteString("}\n")
	for _, family := range families {
		source := resolverSurfaceFamilies[family]
		body := source[strings.Index(source, "func DefineGraphQL"):]
		body = body[strings.Index(body, "}\n")+2:]
		if prefix := source[strings.Index(source, ")\n")+2 : strings.Index(source, "func DefineGraphQL")]; strings.TrimSpace(prefix) != "" {
			combined.WriteString(prefix)
		}
		combined.WriteString(body)
	}
	return combined.String()
}

func TestGenerateTypechecksSchemaPackageResolversAgainstTheGeneratedSurface(t *testing.T) {
	t.Parallel()
	baseline := strings.Replace(strings.Replace(resolverSurfaceSchema,
		"\t\tgolem.SemanticIndex(\"related\", \"content\", Notes.Title, Notes.Body),\n", "", 1),
		"\t\tgolem.FullTextIndex(\"content\",\n\t\t\tgolem.FullTextField(Notes.Title, 3),\n\t\t\tgolem.FullTextField(Notes.Body, 1),\n\t\t),\n", "", 1)
	if baseline == resolverSurfaceSchema {
		t.Fatal("baseline schema still declares the indexes")
	}
	application := writeResolverSurfaceModule(t, resolverSurfaceBaseline)
	schemaPath := filepath.Join(application, "notes", "schema.go")
	resolvers := filepath.Join(application, "notes", "resolvers.go")
	if err := os.WriteFile(schemaPath, []byte(baseline), 0o644); err != nil {
		t.Fatal(err)
	}
	runResolverSurfaceCommand(t, application, "migration", "new", "--schema", "./notes", "--name", "init")
	runResolverSurfaceCommand(t, application, "generate", "--schema", "./notes", "--app-out", "./notes")
	registry, err := os.ReadFile(filepath.Join(application, "notes", "zz_golem_registry.gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(registry, []byte("SearchRelated")) || bytes.Contains(registry, []byte("TextSearchContent")) {
		t.Fatal("baseline generation already published the index methods")
	}

	t.Run("a new index and the resolver using it in one change", func(t *testing.T) {
		if err := os.WriteFile(schemaPath, []byte(resolverSurfaceSchema), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(resolvers, []byte(combinedResolverSurfaceFamilies([]string{"semantic", "fulltext"})), 0o644); err != nil {
			t.Fatal(err)
		}
		runResolverSurfaceCommand(t, application, "migration", "new", "--schema", "./notes", "--name", "indexes")
		runResolverSurfaceCommand(t, application, "generate", "--schema", "./notes", "--app-out", "./notes")
	})
	families := []string{"selectors", "accessors", "semantic", "fulltext", "events", "system"}
	for _, family := range families {
		t.Run(family, func(t *testing.T) {
			t.Parallel()
			application := copyResolverSurfaceModule(t, application)
			if err := os.WriteFile(filepath.Join(application, "notes", "resolvers.go"), []byte(resolverSurfaceFamilies[family]), 0o644); err != nil {
				t.Fatal(err)
			}
			runResolverSurfaceCommand(t, application, "generate", "--schema", "./notes", "--app-out", "./notes")
		})
	}
	t.Run("every family together checks, builds, and runs", func(t *testing.T) {
		t.Parallel()
		application := copyResolverSurfaceModule(t, application)
		if err := os.WriteFile(filepath.Join(application, "notes", "resolvers.go"), []byte(combinedResolverSurfaceFamilies(families)), 0o644); err != nil {
			t.Fatal(err)
		}
		mainSource := strings.Replace(resolverSurfaceMain, "DSN", `"file:`+filepath.ToSlash(filepath.Join(application, "notes.db"))+`"`, 1)
		if err := os.MkdirAll(filepath.Join(application, "cmd", "notes"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(application, "cmd", "notes", "main.go"), []byte(mainSource), 0o644); err != nil {
			t.Fatal(err)
		}
		runResolverSurfaceCommand(t, application, "generate", "--schema", "./notes", "--app-out", "./notes")
		quickstartGo(t, application, "mod", "tidy")
		runResolverSurfaceCommand(t, application, "check", "--schema", "./notes", "--app-out", "./notes")
		assertSurfaceDeclaresThePublishedABI(t, application, "./notes")
		runResolverSurfaceCommand(t, application, "migration", "apply", "--provider", "sqlite", "--dsn", "file:"+filepath.ToSlash(filepath.Join(application, "notes.db")))
		output := quickstartGo(t, application, "run", "./cmd/notes")
		if !strings.Contains(output, "views=7 touched=1 text=2 related=true ready=3 total=1") {
			t.Fatalf("the resolver program printed %q", output)
		}
	})
}

func TestGenerateReportsOnlyTheSymbolTheCompleteSurfaceLacks(t *testing.T) {
	t.Parallel()
	resolvers := strings.Replace(resolverSurfaceFamilies["selectors"], "\tviews, _ := golem.Value(row, Notes.Views).Get()\n", "\tif _, err := caller.Notes.SearchUnrelated(ctx, \"term\", 1); err != nil {\n\t\treturn 0, err\n\t}\n\tviews, _ := golem.Value(row, Notes.Views).Get()\n", 1)
	if resolvers == resolverSurfaceFamilies["selectors"] {
		t.Fatal("the resolver does not reference the missing method")
	}
	application := writeResolverSurfaceModule(t, resolvers)
	before := treeSnapshot(t, application)
	var stdout, stderr bytes.Buffer
	code := runGolem(t, application, []string{"generate", "--schema", "./notes", "--app-out", "./notes"}, &stdout, &stderr)
	if code != 1 || !bytes.Contains(stdout.Bytes(), []byte("P1_METHOD_TYPECHECK")) || !bytes.Contains(stdout.Bytes(), []byte("caller.Notes.SearchUnrelated undefined")) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if count := bytes.Count(stdout.Bytes(), []byte(`"code":`)); count != 1 {
		t.Fatalf("generation reported %d diagnostics, want only the missing method:\n%s", count, stdout.String())
	}
	if after := treeSnapshot(t, application); !reflect.DeepEqual(before, after) {
		t.Fatal("a refused generation modified the module")
	}
}
