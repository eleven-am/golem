package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	"github.com/eleven-am/golem/go/internal/generate/pipeline"
	"github.com/eleven-am/golem/go/internal/physical"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/runtime/testdata/internalrelation"
)

func internalRelationGenerationRequest(directory string) pipeline.Request {
	return pipeline.Request{
		Compile:    compile.Config{Dir: directory, Pattern: ".", Root: "DefineSchema"},
		AppPackage: modelcodegen.PackageSpec{ImportPath: "github.com/eleven-am/golem/go/runtime/testdata/internalrelation", PackageName: "internalrelation", Directory: directory},
		Lowerers:   []physical.Lowerer{sqliteprovider.New()},
	}
}

func TestInternalRelationGeneratedFixtureRegeneratesByteIdentically(t *testing.T) {
	moduleRoot := p5ExtensionModuleRoot(t)
	directory := filepath.Join(moduleRoot, "runtime", "testdata", "internalrelation")
	result, err := pipeline.Build(context.Background(), internalRelationGenerationRequest(directory))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for path, content := range p5ExtensionArtifactMap(result) {
		if !strings.HasPrefix(path, "runtime/testdata/internalrelation/") {
			continue
		}
		checked++
		committed, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, committed) {
			t.Fatalf("committed generated fixture is stale: %s: %s", path, p5ExtensionFirstDifference(committed, content))
		}
	}
	if checked == 0 {
		t.Fatal("no generated artifact belongs to the fixture")
	}
}

type internalRelationHarness struct {
	app    *internalrelation.App[internalrelation.Principal]
	server *internalrelation.GraphQLServer
	caller *internalrelation.Caller[internalrelation.Principal]
}

func newInternalRelationHarness(t *testing.T) *internalRelationHarness {
	t.Helper()
	ctx := context.Background()
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "internal-relation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var encoded []byte
	for _, document := range internalrelation.GolemGeneratedSchemaBundle().Providers() {
		if document.Provider() == golem.SQLite {
			encoded = document.Schema().Bytes()
		}
	}
	schema, err := physical.CanonicalDecode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ApplyInitial(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	app, err := internalrelation.Open(ctx, internalrelation.Config[internalrelation.Principal]{
		Database:            p8AdoptTracedProviderHandle(database, p5ExtensionProviderProfiles()[0]),
		EventTransport:      transport,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		ResolvePrincipal: func(context.Context, internalrelation.Principal) (internalrelation.Actor, error) {
			return internalrelation.Actor{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := app.GraphQL(internalrelation.GraphQLConfig[internalrelation.Principal]{
		PrincipalFromContext: func(context.Context) (internalrelation.Principal, bool) { return internalrelation.Principal{}, true },
		ReportInternalError:  func(_ context.Context, err error) { t.Errorf("reported GraphQL error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := app.ForPrincipal(ctx, internalrelation.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	return &internalRelationHarness{app: app, server: server, caller: caller}
}

func (harness *internalRelationHarness) graphql(t *testing.T, query string) p5ExtensionGraphQLResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	harness.server.Handler().ServeHTTP(recorder, request)
	var response p5ExtensionGraphQLResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GraphQL status=%d body=%q: %v", recorder.Code, recorder.Body.String(), err)
	}
	return response
}

func TestInternalRelationIsRefusedOverGraphQLHTTPAndUsableFromGo(t *testing.T) {
	harness := newInternalRelationHarness(t)
	ctx := context.Background()
	parentID, childID := hookValueUUIDText(1), hookValueUUIDText(2)

	created := harness.graphql(t, fmt.Sprintf(`mutation { createParent(data: {id: %q, name: "alpha"}) { id } }`, parentID))
	if len(created.Errors) != 0 {
		t.Fatalf("createParent errors=%+v", created.Errors)
	}
	child := harness.graphql(t, fmt.Sprintf(`mutation { createChild(data: {id: %q, parentID: %q}) { id parentID } }`, childID, parentID))
	if len(child.Errors) != 0 {
		t.Fatalf("createChild with the foreign key scalar errors=%+v", child.Errors)
	}

	refused := harness.graphql(t, `{ children { id parent { id } } }`)
	if len(refused.Errors) == 0 || refused.Errors[0].Extensions["code"] != "GRAPHQL_VALIDATION_FAILED" {
		t.Fatalf("child.parent response=%+v, want GRAPHQL_VALIDATION_FAILED", refused)
	}
	refusedInput := harness.graphql(t, fmt.Sprintf(`{ children(where: {parent: {is: {id: {equals: %q}}}}) { id } }`, parentID))
	if len(refusedInput.Errors) == 0 || refusedInput.Errors[0].Extensions["code"] != "GRAPHQL_VALIDATION_FAILED" {
		t.Fatalf("ChildWhereInput.parent response=%+v, want GRAPHQL_VALIDATION_FAILED", refusedInput)
	}

	visible := harness.graphql(t, `{ parents { id children { id } } }`)
	if len(visible.Errors) != 0 {
		t.Fatalf("Parent.children errors=%+v", visible.Errors)
	}

	rows, err := harness.caller.Childs.FindMany(ctx,
		golem.Where(internalrelation.Childs.Parent.Is(internalrelation.Parents.Name.Eq("alpha"))),
		golem.Select[internalrelation.Child](internalrelation.Childs.ID, internalrelation.Childs.Parent.Select(internalrelation.Parents.Name)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("Go relation filter returned %d rows, want 1", len(rows))
	}
	loaded, ok := golem.One(rows[0], internalrelation.Childs.Parent.ToOne).Get()
	if !ok {
		t.Fatal("Go relation select returned no parent")
	}
	if name, present := golem.Value(loaded, internalrelation.Parents.Name).Get(); !present || name != "alpha" {
		t.Fatalf("selected parent name=%q present=%t", name, present)
	}
}
