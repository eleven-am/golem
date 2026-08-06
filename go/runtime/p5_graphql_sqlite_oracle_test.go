package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type p5OracleCapture struct {
	caller   *Caller[oraclePrincipal, oracleActor]
	requests *[]golem.FrozenReadRequest
}

func (capture p5OracleCapture) ExecuteFrozenRead(ctx context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	*capture.requests = append(*capture.requests, request)
	return capture.caller.ExecuteFrozenRead(ctx, request)
}

type p5OracleHarness struct {
	database  *App[oraclePrincipal, oracleActor]
	server    *publicgraphql.Server[oraclePrincipal]
	fixture   schematest.Fixture
	users     golem.ModelDescriptor[oracleUser]
	userID    golem.EqualField[oracleUser, golem.UUID]
	userName  golem.TextField[oracleUser, string]
	posts     golem.ModelDescriptor[oraclePost]
	postID    golem.EqualField[oraclePost, golem.UUID]
	postTitle golem.TextField[oraclePost, string]
	userPosts golem.ToMany[oracleUser, oraclePost]
	captured  *[]golem.FrozenReadRequest
}

func newP5OracleHarness(t *testing.T, indexed bool) p5OracleHarness {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.New(t)
	if indexed {
		fixture = schematest.NewIndexed(t)
	}
	bundle, compilation := p5GraphQLBundle(t, fixture.Bundle)
	db, _, err := sqliteprovider.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "p5-graphql-oracle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliteprovider.New().ApplyInitial(ctx, db, fixture.SQLite); err != nil {
		t.Fatal(err)
	}

	users := golem.GeneratedModelDescriptor[oracleUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	posts := golem.GeneratedModelDescriptor[oraclePost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), users.Metadata(), posts.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userID := golem.GeneratedEqualField[oracleUser, golem.UUID](fixture.UserID)
	userName := golem.GeneratedTextField[oracleUser, string](fixture.UserName)
	postID := golem.GeneratedEqualField[oraclePost, golem.UUID](fixture.PostID)
	postTitle := golem.GeneratedTextField[oraclePost, string](fixture.PostTitle)
	userPosts := golem.GeneratedToMany[oracleUser, oraclePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	userBinding := golem.GeneratedPolicyBinding[oracleActor, oracleUser](fixture.User, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oracleUser]()
		if actor.UserPrefix == "" {
			rules.CanRead(golem.All[oracleUser]())
		} else {
			rules.CanRead(userName.StartsWith(actor.UserPrefix))
		}
		rules.CanReadFields(golem.All[oracleUser](), userID, userPosts)
		rules.CannotReadFields(golem.All[oracleUser](), userName)
		rules.CanReadFields(userName.EndsWith("-open"), userName)
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[oracleActor, oraclePost](fixture.Post, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oraclePost]()
		if actor.PostPrefix == "" {
			rules.CanRead(golem.All[oraclePost]())
		} else {
			rules.CanRead(postTitle.StartsWith(actor.PostPrefix))
		}
		return rules.Freeze(fixture.Post)
	})
	bindingsPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{userBinding, postBinding}, nil)
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingsPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[oraclePrincipal, oracleActor]{DB: db, Provider: golem.SQLite, Bundle: bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(_ context.Context, p oraclePrincipal) (oracleActor, error) { return p, nil }})
	if err != nil {
		t.Fatal(err)
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	captured := &[]golem.FrozenReadRequest{}
	executor, err := publicgraphql.NewGeneratedExecutor(publicgraphql.GeneratedExecutorConfig[oraclePrincipal]{Bundle: bundle, BeginCaller: func(ctx context.Context, principal oraclePrincipal) (publicgraphql.CallerExecution, error) {
		caller, err := app.ForPrincipal(ctx, principal)
		if err != nil {
			return nil, err
		}
		return p5OracleCapture{caller: caller, requests: captured}, nil
	}, ReportInternalError: func(_ context.Context, err error) { t.Errorf("unexpected internal GraphQL error: %v", err) }})
	if err != nil {
		t.Fatal(err)
	}
	server, err := publicgraphql.NewServer(document.SDL, publicgraphql.Config[oraclePrincipal]{PrincipalFromContext: func(context.Context) (oraclePrincipal, bool) { return oraclePrincipal{}, true }, ContractFingerprint: bundle.Contract().Fingerprint(), ReportInternalError: func(_ context.Context, err error) { t.Errorf("unexpected GraphQL server error: %v", err) }}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return p5OracleHarness{database: app, server: server, fixture: fixture, users: users, userID: userID, userName: userName, posts: posts, postID: postID, postTitle: postTitle, userPosts: userPosts, captured: captured}
}

func p5GraphQLBundle(t testing.TB, bundle golem.SchemaBundle) (golem.SchemaBundle, compilerir.CompilationIR) {
	t.Helper()
	var compilation compilerir.CompilationIR
	if err := json.Unmarshal(bundle.Model().Bytes(), &compilation.Model); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bundle.Contract().Bytes(), &compilation.Contract); err != nil {
		t.Fatal(err)
	}
	models := make(map[compilerir.ModelID]compilerir.ModelDeclIR, len(compilation.Model.Models))
	for _, model := range compilation.Model.Models {
		models[model.ID] = model
	}
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		model := models[contract.ModelID]
		contract.GraphQLName, contract.Exposed = model.LogicalName, true
		fieldByID := make(map[compilerir.FieldID]compilerir.FieldIR, len(model.Fields))
		for _, field := range model.Fields {
			fieldByID[field.ID] = field
		}
		for fieldIndex := range contract.Fields {
			field := fieldByID[contract.Fields[fieldIndex].FieldID]
			contract.Fields[fieldIndex].GraphQLName = graphqlcontract.LowerCamel(field.GoName)
		}
		keys := make([]compilerir.KeyIR, 0, 1+len(model.Uniques))
		if model.PrimaryKey != nil {
			keys = append(keys, *model.PrimaryKey)
		}
		keys = append(keys, model.Uniques...)
		for _, key := range keys {
			if len(key.Fields) == 0 {
				continue
			}
			parts := make([]string, len(key.Fields))
			for i, fieldID := range key.Fields {
				parts[i] = graphqlcontract.LowerCamel(fieldByID[fieldID].GoName)
			}
			contract.Selectors = append(contract.Selectors, compilerir.SelectorContractIR{KeyID: key.ID, Kind: key.Kind, Name: strings.Join(parts, "_"), Fields: append([]compilerir.FieldID(nil), key.Fields...)})
		}
	}
	for _, diagnostic := range graphqlcontract.Normalize(&compilation, nil) {
		if diagnostic.Severity == compilerir.SeverityError {
			t.Fatalf("normalize GraphQL fixture: %s: %s", diagnostic.Code, diagnostic.Message)
		}
	}
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(string(fingerprint))
	if err != nil {
		t.Fatal(err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], decoded)
	contractDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	return golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), contractDocument, bundle.Providers()...), compilation
}

func (h p5OracleHarness) seed(t testing.TB, users [][2]string, posts [][3]string) {
	t.Helper()
	for _, row := range users {
		if _, err := h.database.database.Exec(`INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range posts {
		if _, err := h.database.database.Exec(`INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
}

type p5PolicyTrace struct {
	Operation golem.ReadOperation
	Model     golem.ModelID
	Orders    []golem.FieldID
	Fields    []p5PolicyField
	Relations []p5PolicyRelation
}
type p5PolicyField struct {
	ID             golem.FieldID
	Public, Masked bool
}
type p5PolicyRelation struct {
	ID             golem.FieldID
	Public, Masked bool
	Child          *p5PolicyTrace
}

func p5Trace(plan readplan.Plan) p5PolicyTrace {
	trace := p5PolicyTrace{Operation: golem.ReadOperation(plan.Operation()), Model: golem.ModelID(plan.ModelID())}
	for _, order := range plan.OrderBy() {
		trace.Orders = append(trace.Orders, golem.FieldID(order.FieldID()))
	}
	for _, field := range plan.Fields() {
		_, masked := field.Mask()
		trace.Fields = append(trace.Fields, p5PolicyField{golem.FieldID(field.FieldID()), field.Public(), masked})
	}
	for _, relation := range plan.Relations() {
		_, masked := relation.Mask()
		child := p5Trace(relation.Child())
		trace.Relations = append(trace.Relations, p5PolicyRelation{golem.FieldID(relation.FieldID()), relation.Public(), masked, &child})
	}
	return trace
}

func TestGraphQLAndGoCallerReadOracleAgreeOnRowsOrderMasksErrorsAndPolicyTrace(t *testing.T) {
	h := newP5OracleHarness(t, true)
	h.seed(t, [][2]string{{"00000000-0000-0000-0000-000000000001", "beta-closed"}, {"00000000-0000-0000-0000-000000000002", "alpha-open"}}, [][3]string{{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "allowed-z"}, {"00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "denied"}, {"00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000002", "allowed-a"}})
	principal := oraclePrincipal{PostPrefix: "allowed-"}
	response := h.server.Execute(context.Background(), principal, publicgraphql.Request{Query: `query { users(orderBy: [{id: asc}]) { id name posts(orderBy: [{title: desc}], take: 1) { id title } } }`})
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", response.Errors)
	}
	caller, err := h.database.ForPrincipal(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := CallerFindMany(context.Background(), caller, h.users, golem.OrderBy(h.userID.Asc()), golem.Select(h.userID, h.userName, h.userPosts.Args(golem.OrderBy(h.postTitle.Desc()), golem.Take[oraclePost](1), golem.Select(h.postID, h.postTitle))))
	if err != nil {
		t.Fatal(err)
	}
	want := make([]map[string]any, len(rows))
	for i, row := range rows {
		id, _ := golem.Value(row, h.userID).Get()
		var name any
		if value, present := golem.Value(row, h.userName).Get(); present {
			name = value
		}
		children, _ := golem.Many(row, h.userPosts).Get()
		encodedChildren := make([]map[string]any, len(children))
		for j, child := range children {
			childID, _ := golem.Value(child, h.postID).Get()
			title, _ := golem.Value(child, h.postTitle).Get()
			encodedChildren[j] = map[string]any{"id": childID.String(), "title": title}
		}
		want[i] = map[string]any{"id": id.String(), "name": name, "posts": encodedChildren}
	}
	data := response.Data.(map[string]any)
	gotJSON, _ := json.Marshal(data["users"])
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("GraphQL/Go rows disagree\ngot:  %#v\nwant: %#v", data["users"], want)
	}
	if len(*h.captured) != 1 {
		t.Fatalf("captured requests=%d want 1", len(*h.captured))
	}
	direct, err := golem.FreezeFindMany(h.users, golem.OrderBy(h.userID.Asc()), golem.Select(h.userID, h.userName, h.userPosts.Args(golem.OrderBy(h.postTitle.Desc()), golem.Take[oraclePost](1), golem.Select(h.postID, h.postTitle))))
	if err != nil {
		t.Fatal(err)
	}
	graphqlPrepared, err := caller.Prepare((*h.captured)[0])
	if err != nil {
		t.Fatal(err)
	}
	directPrepared, err := caller.Prepare(direct)
	if err != nil {
		t.Fatal(err)
	}
	graphqlPlan, err := preparePlan(graphqlPrepared, caller.app.registry, caller.app.readLimits.plan)
	if err != nil {
		t.Fatal(err)
	}
	directPlan, err := preparePlan(directPrepared, caller.app.registry, caller.app.readLimits.plan)
	if err != nil {
		t.Fatal(err)
	}
	if got, expected := p5Trace(graphqlPlan), p5Trace(directPlan); !reflect.DeepEqual(got, expected) {
		t.Fatalf("policy trace differs\ngraphql: %#v\ngo:      %#v", got, expected)
	}
	denied := h.server.Execute(context.Background(), principal, publicgraphql.Request{Query: `query { users(orderBy: [{name: asc}]) { id } }`})
	if len(denied.Errors) != 1 || denied.Errors[0].Extensions["code"] != "FORBIDDEN" {
		t.Fatalf("GraphQL conditional-order error=%#v", denied.Errors)
	}
	_, directErr := CallerFindMany(context.Background(), caller, h.users, golem.OrderBy(h.userName.Asc()), golem.Select(h.userID))
	var directFailure *golem.Error
	if !errors.As(directErr, &directFailure) || directFailure.Code != golem.CodeForbidden {
		t.Fatalf("Go conditional-order error=%v", directErr)
	}
}

func TestGraphQLMissingAndInvisibleUniqueHaveIdenticalDisclosure(t *testing.T) {
	h := newP5OracleHarness(t, false)
	h.seed(t, [][2]string{{"00000000-0000-0000-0000-000000000001", "owner-open"}}, [][3]string{{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "hidden"}})
	query := `query($id: UUID!) { post(where: {id: $id}) { id title } }`
	invisible := h.server.Execute(context.Background(), oraclePrincipal{PostPrefix: "visible-"}, publicgraphql.Request{Query: query, Variables: map[string]any{"id": "00000000-0000-0000-0000-000000000011"}})
	missing := h.server.Execute(context.Background(), oraclePrincipal{PostPrefix: "visible-"}, publicgraphql.Request{Query: query, Variables: map[string]any{"id": "00000000-0000-0000-0000-000000000099"}})
	if !reflect.DeepEqual(invisible, missing) {
		t.Fatalf("disclosure differs\ninvisible: %#v\nmissing:   %#v", invisible, missing)
	}
	if len(invisible.Errors) != 0 || !reflect.DeepEqual(invisible.Data, map[string]any{"post": nil}) {
		t.Fatalf("unexpected public disclosure: %#v", invisible)
	}
	caller, err := h.database.ForPrincipal(context.Background(), oraclePrincipal{PostPrefix: "visible-"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000099"} {
		parsed, _ := golem.ParseUUID(id)
		selector := golem.GeneratedUniqueSelectorValue[oraclePost](h.fixture.Post, h.fixture.PostKey, golem.GeneratedSelectorComponent(h.fixture.PostID, parsed))
		_, readErr := CallerFindUnique(context.Background(), caller, h.posts, selector, golem.Select(h.postID))
		var failure *golem.Error
		if !errors.As(readErr, &failure) || failure.Code != golem.CodeNotFound {
			t.Fatalf("%s error=%v", id, readErr)
		}
	}
}

func TestP5IndependentSocialGraphQLOracleSQLite(t *testing.T) {
	h := newP5OracleHarness(t, false)
	users := [][2]string{{"00000000-0000-0000-0000-000000000001", "zed-closed"}, {"00000000-0000-0000-0000-000000000002", "amy-open"}}
	posts := [][3]string{{"00000000-0000-0000-0000-000000000011", users[0][0], "allowed-second"}, {"00000000-0000-0000-0000-000000000012", users[0][0], "blocked"}, {"00000000-0000-0000-0000-000000000013", users[1][0], "allowed-first"}}
	h.seed(t, users, posts)
	response := h.server.Execute(context.Background(), oraclePrincipal{PostPrefix: "allowed-"}, publicgraphql.Request{Query: `query { users(orderBy: [{id: asc}]) { id name posts(orderBy: [{title: asc}]) { id title } } }`})
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL errors: %#v", response.Errors)
	}
	want := []map[string]any{{"id": users[0][0], "name": nil, "posts": []map[string]any{{"id": posts[0][0], "title": posts[0][2]}}}, {"id": users[1][0], "name": users[1][1], "posts": []map[string]any{{"id": posts[2][0], "title": posts[2][2]}}}}
	got := response.Data.(map[string]any)["users"]
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		encoded, _ := json.MarshalIndent(response, "", "  ")
		t.Fatalf("independent social oracle disagreement\ngot: %s\nwant: %#v", encoded, want)
	}
}

type p5Comment struct{}

type p5RecursiveHarness struct {
	app      *App[oraclePrincipal, oracleActor]
	server   *publicgraphql.Server[oraclePrincipal]
	fixture  schematest.RecursiveCommentFixture
	comments golem.ModelDescriptor[p5Comment]
	id       golem.EqualField[p5Comment, golem.UUID]
	body     golem.TextField[p5Comment, string]
	replies  golem.ToMany[p5Comment, p5Comment]
}

func newP5RecursiveHarness(t *testing.T) p5RecursiveHarness {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.NewSubscribedRecursiveComment(t)
	bundle, compilation := p5GraphQLBundle(t, fixture.Bundle)
	database, _, err := sqliteprovider.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "p5-recursive-graphql.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqliteprovider.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	comments := golem.GeneratedModelDescriptor[p5Comment](fixture.Comment, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.CommentID, fixture.ParentID, fixture.Body}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), comments.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	binding := golem.GeneratedPolicyBinding[oracleActor, p5Comment](fixture.Comment, func(oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[p5Comment]()
		rules.CanRead(golem.All[p5Comment]())
		return rules.Freeze(fixture.Comment)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{binding}, nil)
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[oraclePrincipal, oracleActor]{DB: database, Provider: golem.SQLite, Bundle: bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(_ context.Context, principal oraclePrincipal) (oracleActor, error) { return principal, nil }})
	if err != nil {
		t.Fatal(err)
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := publicgraphql.NewGeneratedExecutor(publicgraphql.GeneratedExecutorConfig[oraclePrincipal]{Bundle: bundle, BeginCaller: func(ctx context.Context, principal oraclePrincipal) (publicgraphql.CallerExecution, error) {
		return app.ForPrincipal(ctx, principal)
	}, ReportInternalError: func(_ context.Context, err error) { t.Errorf("recursive GraphQL internal error: %v", err) }})
	if err != nil {
		t.Fatal(err)
	}
	server, err := publicgraphql.NewServer(document.SDL, publicgraphql.Config[oraclePrincipal]{PrincipalFromContext: func(context.Context) (oraclePrincipal, bool) { return oraclePrincipal{}, true }, ContractFingerprint: bundle.Contract().Fingerprint(), ReportInternalError: func(_ context.Context, err error) { t.Errorf("recursive GraphQL server error: %v", err) }}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return p5RecursiveHarness{app: app, server: server, fixture: fixture, comments: comments, id: golem.GeneratedEqualField[p5Comment, golem.UUID](fixture.CommentID), body: golem.GeneratedTextField[p5Comment, string](fixture.Body), replies: golem.GeneratedToMany[p5Comment, p5Comment](fixture.Replies, fixture.Threading, fixture.Comment)}
}

func p5RecursiveRows(t testing.TB, row golem.Row[p5Comment], h p5RecursiveHarness, depth int) map[string]any {
	t.Helper()
	id, present := golem.Value(row, h.id).Get()
	if !present {
		t.Fatal("recursive row id is absent")
	}
	body, present := golem.Value(row, h.body).Get()
	if !present {
		t.Fatal("recursive row body is absent")
	}
	count, present := golem.RelationCount(row, h.replies).Get()
	if !present {
		t.Fatal("recursive reply count is absent")
	}
	result := map[string]any{"id": id.String(), "body": body, "_count": map[string]any{"replies": count}}
	if depth == 0 {
		return result
	}
	replies, present := golem.Many(row, h.replies).Get()
	if !present {
		t.Fatal("recursive replies are absent")
	}
	children := make([]map[string]any, len(replies))
	for index, child := range replies {
		children[index] = p5RecursiveRows(t, child, h, depth-1)
	}
	result["replies"] = children
	return result
}

func TestGraphQLNestedRecursiveSocialReadMatchesP3AcrossLoadingStrategies(t *testing.T) {
	h := newP5RecursiveHarness(t)
	for _, row := range [][3]any{
		{"00000000-0000-0000-0000-000000000001", nil, "root"},
		{"00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000001", "reply-b"},
		{"00000000-0000-0000-0000-000000000003", "00000000-0000-0000-0000-000000000001", "reply-a"},
		{"00000000-0000-0000-0000-000000000004", "00000000-0000-0000-0000-000000000003", "grandchild"},
	} {
		if _, err := h.app.database.Exec(`INSERT INTO "comments"("id","parent_id","body") VALUES (?,?,?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	query := `query($id: UUID!) { comment(where: {id: $id}) { id body _count { replies } replies(orderBy: [{body: asc}], take: 2) { id body _count { replies } replies(orderBy: [{body: asc}], take: 2) { id body _count { replies } replies(take: 2) { id body _count { replies } } } } } }`
	root, _ := golem.ParseUUID("00000000-0000-0000-0000-000000000001")
	selector := golem.GeneratedUniqueSelectorValue[p5Comment](h.fixture.Comment, h.fixture.CommentKey, golem.GeneratedSelectorComponent(h.fixture.CommentID, root))
	level3 := h.replies.Args(golem.Take[p5Comment](2), golem.Select(h.id, h.body, h.replies.Count()))
	level2 := h.replies.Args(golem.OrderBy(h.body.Asc()), golem.Take[p5Comment](2), golem.Select(h.id, h.body, h.replies.Count(), level3))
	level1 := h.replies.Args(golem.OrderBy(h.body.Asc()), golem.Take[p5Comment](2), golem.Select(h.id, h.body, h.replies.Count(), level2))
	for _, strategy := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "production", ctx: context.Background()},
		{name: "forced-batched", ctx: context.WithValue(context.Background(), relationLoadStrategyContextKey{}, relationLoadBatched)},
		{name: "correlated-oracle", ctx: context.WithValue(context.Background(), relationLoadStrategyContextKey{}, relationLoadCorrelatedOracle)},
	} {
		t.Run(strategy.name, func(t *testing.T) {
			response := h.server.Execute(strategy.ctx, oraclePrincipal{}, publicgraphql.Request{Query: query, Variables: map[string]any{"id": root.String()}})
			if len(response.Errors) != 0 {
				t.Fatalf("GraphQL errors: %#v", response.Errors)
			}
			caller, err := h.app.ForPrincipal(strategy.ctx, oraclePrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			row, err := CallerFindUnique(strategy.ctx, caller, h.comments, selector, golem.Select(h.id, h.body, h.replies.Count(), level1))
			if err != nil {
				t.Fatal(err)
			}
			want := p5RecursiveRows(t, row, h, 3)
			got := response.Data.(map[string]any)["comment"]
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("recursive GraphQL/P3 disagreement\ngot:  %s\nwant: %s", gotJSON, wantJSON)
			}
		})
	}
}
