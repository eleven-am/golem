package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

type compileFuzzTemplate struct {
	query    string
	literals bool
}

var compileFuzzTemplates = []compileFuzzTemplate{
	{query: `query($u: UUID!, $n: Int, $k: Int) { users(where: {id: {equals: $u}}, take: $n, skip: $k) { id name } }`},
	{query: `query($s: String!) { users(where: {name: {contains: $s}}) { id } }`},
	{query: `query($n: Int, $k: Int) { users(take: $n, skip: $k, orderBy: [{name: asc}]) { id posts(take: 2) { id title } } }`},
	{query: `query($w: UserWhereInput) { users(where: $w) { id } }`},
	{query: `query($o: [UserOrderByInput!]) { users(orderBy: $o, take: 2) { id } }`},
	{query: `query($d: [UserScalarField!]) { users(distinct: $d) { id } }`},
	{query: `query($c: UserWhereUniqueInput, $n: Int) { users(cursor: $c, take: $n, orderBy: [{id: asc}]) { id } }`},
	{query: `query($w: PostWhereInput) { posts(where: $w) { id author { id name } } }`},
	{query: `query($w: FriendshipWhereUniqueInput!) { friendship(where: $w) { userID friendID } }`},
	{query: `query($w: UserWhereUniqueInput!) { user(where: $w) { id } }`},
	{query: `query($w: PostWhereInput) { users { _count { posts(where: $w) } } }`},
	{query: `query($n: Int) { users(take: 1) { posts(take: $n) { id } } }`},
	{query: `query($b: Boolean!) { users @skip(if: $b) { id } }`},
	{query: `{ users(where: {name: {equals: LITERAL}}) { id } }`, literals: true},
	{query: `{ users(where: {id: {equals: LITERAL}}, take: 1) { id } }`, literals: true},
	{query: `mutation($d: UserCreateInput!) { createUser(data: $d) { id } }`},
	{query: `mutation($w: UserWhereUniqueInput!, $d: UserUpdateInput!) { updateUser(where: $w, data: $d) { id } }`},
	{query: `mutation($w: UserWhereUniqueInput!, $c: UserCreateInput!, $u: UserUpdateInput!) { upsertUser(where: $w, create: $c, update: $u) { id } }`},
	{query: `mutation($w: UserWhereInput!, $d: UserUpdateManyInput!) { updateManyUsers(where: $w, data: $d) { count } }`},
	{query: `mutation($w: UserWhereInput!) { deleteManyUsers(where: $w) { count } }`},
	{query: `mutation($w: UserWhereUniqueInput!) { deleteUser(where: $w) { id } }`},
	{query: `mutation($d: PostCreateInput!) { createPost(data: $d) { id } }`},
}

type compileFuzzSeed struct {
	template  uint8
	variables string
	literal   string
}

func compileFuzzSeeds() []compileFuzzSeed {
	deep := strings.Repeat(`{"OR":[`, 20) + `{"all":true}` + strings.Repeat(`]}`, 20)
	longList := `{"name":{"in":[` + strings.TrimSuffix(strings.Repeat(`"a",`, 40), ",") + `]}}`
	return []compileFuzzSeed{
		{0, `{"u":"00000000-0000-0000-0000-000000000001","n":2,"k":0}`, ""},
		{0, `{"u":"private-driver-token","n":2,"k":0}`, ""},
		{0, `{"u":"00000000-0000-0000-0000-00000000000A","n":-1,"k":0}`, ""},
		{0, `{"u":"00000000-0000-0000-0000-000000000001","n":1000,"k":0}`, ""},
		{0, `{"u":"00000000-0000-0000-0000-000000000001","n":0,"k":0}`, ""},
		{0, `{"u":"00000000-0000-0000-0000-000000000001","n":-1,"k":0}`, ""},
		{0, `{"u":"00000000-0000-0000-0000-000000000001","n":1,"k":-5}`, ""},
		{0, `{"u":"00000000-0000-0000-0000-000000000001"}`, ""},
		{0, `{"u":"00000000-0000-0000-0000-000000000001","n":2147483647,"k":2147483647}`, ""},
		{1, `{"s":"private-driver-token"}`, ""},
		{1, "{\"s\":\"a\\u0000b private-driver-token\"}", ""},
		{1, `{"s":"x"}`, ""},
		{2, `{"n":1000,"k":0}`, ""},
		{2, `{"n":1,"k":-1}`, ""},
		{2, `{"n":-3,"k":0}`, ""},
		{3, `{"w":{"name":{"equals":"private-driver-token"}}}`, ""},
		{3, `{"w":{}}`, ""},
		{3, `{"w":{"AND":[]}}`, ""},
		{3, `{"w":{"all":true,"name":{"equals":"private-driver-token"}}}`, ""},
		{3, `{"w":{"name":null}}`, ""},
		{3, `{"w":{"posts":{"some":null}}}`, ""},
		{3, `{"w":{"posts":{"some":{}}}}`, ""},
		{3, `{"w":` + deep + `}`, ""},
		{3, `{"w":` + longList + `}`, ""},
		{3, `{"w":{"id":{"in":["private-driver-token","00000000-0000-0000-0000-000000000001"]}}}`, ""},
		{3, `{"w":{"NOT":[{"name":{"equals":"a"}},{"name":null}]}}`, ""},
		{4, `{"o":[{"id":"asc"},{"id":"desc"}]}`, ""},
		{4, `{"o":[]}`, ""},
		{4, `{"o":[{"id":"asc","name":"asc"}]}`, ""},
		{4, `{"o":[{}]}`, ""},
		{5, `{"d":["id","id"]}`, ""},
		{5, `{"d":[]}`, ""},
		{6, `{"c":{"id":"00000000-0000-0000-0000-000000000001"},"n":-1}`, ""},
		{6, `{"c":{"id":"private-driver-token"},"n":1}`, ""},
		{6, `{"c":{},"n":1}`, ""},
		{7, `{"w":{"author":{"is":null}}}`, ""},
		{7, `{"w":{"author":{"is":{}}}}`, ""},
		{7, `{"w":{"author":{"is":{"name":{"startsWith":"private-driver-token"}}}}}`, ""},
		{8, `{"w":{"UserID_FriendID":{"userID":"00000000-0000-0000-0000-000000000001","friendID":"private-driver-token"}}}`, ""},
		{8, `{"w":{"UserID_FriendID":{"userID":"00000000-0000-0000-0000-000000000001","friendID":"00000000-0000-0000-0000-000000000002"}}}`, ""},
		{9, `{"w":{"id":"private-driver-token"}}`, ""},
		{10, `{"w":{}}`, ""},
		{10, `{"w":{"title":{"equals":"private-driver-token"}}}`, ""},
		{11, `{"n":0}`, ""},
		{11, `{"n":-1}`, ""},
		{11, `{"n":1000}`, ""},
		{12, `{"b":true}`, ""},
		{12, `{"b":false}`, ""},
		{13, `{}`, `"private-driver-token"`},
		{13, `{}`, "\"a\\u0000b\""},
		{14, `{}`, `"private-driver-token"`},
		{14, `{}`, `"00000000-0000-0000-0000-000000000001"`},
		{14, `{}`, `99999999999999999999`},
		{15, `{"d":{"id":"private-driver-token","name":"u"}}`, ""},
		{15, `{"d":{"id":"00000000-0000-0000-0000-000000000001","name":"u","posts":{}}}`, ""},
		{15, `{"d":{"id":"00000000-0000-0000-0000-000000000001","name":"u","posts":{"create":[]}}}`, ""},
		{15, `{"d":{"id":"00000000-0000-0000-0000-000000000001","name":"u","posts":{"create":null}}}`, ""},
		{15, `{"d":{"id":"00000000-0000-0000-0000-000000000001","name":"u","posts":{"connect":[{}]}}}`, ""},
		{15, `{"d":{"id":"00000000-0000-0000-0000-000000000001","name":"u","posts":{"create":[{"id":"private-driver-token","title":"t"},{"id":"00000000-0000-0000-0000-000000000002","title":"t"}]}}}`, ""},
		{15, `{"d":{"id":"00000000-0000-0000-0000-000000000001","name":"u","posts":{"connectOrCreate":[{"where":{},"create":{"id":"00000000-0000-0000-0000-000000000002","title":"t"}}]}}}`, ""},
		{15, `{"d":{"id":"00000000-0000-0000-0000-000000000001","name":"u","posts":{"create":[{"id":"00000000-0000-0000-0000-000000000002","title":"a"},{"id":"00000000-0000-0000-0000-000000000003","title":"b"},{"id":"00000000-0000-0000-0000-000000000004","title":"c"},{"id":"00000000-0000-0000-0000-000000000005","title":"d"}]}}}`, ""},
		{16, `{"w":{"id":"00000000-0000-0000-0000-000000000001"},"d":{}}`, ""},
		{16, `{"w":{},"d":{"name":{"set":"x"}}}`, ""},
		{16, `{"w":{"id":"private-driver-token"},"d":{"name":{"set":"x"}}}`, ""},
		{16, `{"w":{"id":"00000000-0000-0000-0000-000000000001"},"d":{"name":{}}}`, ""},
		{16, `{"w":{"id":"00000000-0000-0000-0000-000000000001"},"d":{"name":{"set":null}}}`, ""},
		{16, `{"w":{"id":"00000000-0000-0000-0000-000000000001"},"d":{"id":{"set":"private-driver-token"}}}`, ""},
		{16, `{"w":{"id":"00000000-0000-0000-0000-000000000001"},"d":{"posts":{"update":[{"where":{},"data":{}}]}}}`, ""},
		{16, `{"w":{"id":"00000000-0000-0000-0000-000000000001"},"d":{"posts":{"deleteMany":[{}]}}}`, ""},
		{16, `{"w":{"id":"00000000-0000-0000-0000-000000000001"},"d":{"posts":{"updateMany":[{"where":{"AND":[]},"data":{"title":{"set":"x"}}}]}}}`, ""},
		{16, `{"w":{"id":"00000000-0000-0000-0000-000000000001"},"d":{"posts":{"upsert":[{"where":{},"create":{},"update":{}}]}}}`, ""},
		{17, `{"w":{"id":"00000000-0000-0000-0000-000000000001"},"c":{"id":"00000000-0000-0000-0000-000000000001","name":"u"},"u":{}}`, ""},
		{17, `{"w":{},"c":{"id":"private-driver-token","name":"u"},"u":{"name":{"set":"x"}}}`, ""},
		{18, `{"w":{},"d":{"name":{"set":"x"}}}`, ""},
		{18, `{"w":{"all":true},"d":{}}`, ""},
		{18, `{"w":{"AND":[]},"d":{"name":{"set":"x"}}}`, ""},
		{18, `{"w":{"name":{"equals":"private-driver-token"}},"d":{"id":{"set":"private-driver-token"}}}`, ""},
		{19, `{"w":{}}`, ""},
		{19, `{"w":{"all":true,"name":{"equals":"private-driver-token"}}}`, ""},
		{19, `{"w":{"NOT":[{"name":null}]}}`, ""},
		{20, `{"w":{}}`, ""},
		{20, `{"w":{"id":"private-driver-token"}}`, ""},
		{21, `{"d":{"id":"00000000-0000-0000-0000-000000000002","title":"t","author":{"connect":{}}}}`, ""},
		{21, `{"d":{"id":"private-driver-token","title":"t","author":{"connect":{"id":"00000000-0000-0000-0000-000000000001"}}}}`, ""},
		{21, `{"d":{"id":"00000000-0000-0000-0000-000000000002","title":"t","author":{"create":{}}}}`, ""},
	}
}

func compileFuzzVocabulary(t testing.TB) string {
	t.Helper()
	fixture := newSocialMutationFixture(t, golem.ModelID{}, nil)
	_, compilation := p5GraphQLBundle(t, fixture.schema.Bundle)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	return document.SDL
}

func collectCallerText(value any, texts *[]string) {
	switch typed := value.(type) {
	case string:
		*texts = append(*texts, typed)
	case []any:
		for _, item := range typed {
			collectCallerText(item, texts)
		}
	case map[string]any:
		for key, item := range typed {
			*texts = append(*texts, key)
			collectCallerText(item, texts)
		}
	}
}

func FuzzCompileErrorsAreTypedAndNeverEcho(f *testing.F) {
	vocabulary := compileFuzzVocabulary(f)
	var reports atomic.Int64
	harness := newP5SocialGraphQLHarnessWith(f, newSocialMutationFixture(f, golem.ModelID{}, nil), publicgraphql.Limits{}, nil, func(error) { reports.Add(1) })
	for _, seed := range compileFuzzSeeds() {
		f.Add(seed.template, seed.variables, seed.literal)
	}
	f.Fuzz(func(t *testing.T, template uint8, rawVariables string, literal string) {
		shape := compileFuzzTemplates[int(template)%len(compileFuzzTemplates)]
		query := shape.query
		if shape.literals {
			if !json.Valid([]byte(literal)) {
				t.Skip()
			}
			query = strings.Replace(query, "LITERAL", literal, 1)
		}
		var variables map[string]any
		decoder := json.NewDecoder(strings.NewReader(rawVariables))
		decoder.UseNumber()
		if err := decoder.Decode(&variables); err != nil {
			variables = nil
		}
		var callerText []string
		collectCallerText(variables, &callerText)
		if shape.literals {
			var decoded any
			if json.Unmarshal([]byte(literal), &decoded) == nil {
				collectCallerText(decoded, &callerText)
			}
			callerText = append(callerText, literal)
		}
		before := reports.Load()
		response := harness.server.Execute(context.Background(), graphMutationPrincipal{}, publicgraphql.Request{Query: query, Variables: variables})
		if reports.Load() != before {
			t.Fatalf("an internal error was reported for query %q variables %q", query, rawVariables)
		}
		encoded, err := json.Marshal(response.Errors)
		if err != nil {
			t.Fatal(err)
		}
		for _, failure := range response.Errors {
			if failure.Extensions["code"] == "INTERNAL_SERVER_ERROR" {
				t.Fatalf("a caller-reachable failure was presented as internal: %s (query %q variables %q)", encoded, query, rawVariables)
			}
			if failure.Extensions["code"] == "BAD_USER_INPUT" && failure.Message == "GraphQL operation could not be bound" {
				t.Fatalf("a compile failure kept the generic bind message: %s (query %q variables %q)", encoded, query, rawVariables)
			}
		}
		if bytes.Contains(encoded, []byte(compileErrorMarker)) {
			t.Fatalf("response echoes the marker: %s", encoded)
		}
		for _, text := range callerText {
			assertNoWindowEcho(t, string(encoded), text, vocabulary)
		}
	})
}

func assertNoWindowEcho(t *testing.T, response, text, vocabulary string) {
	t.Helper()
	const window = 8
	if len(text) < window {
		return
	}
	for start := 0; start+window <= len(text); start++ {
		piece := text[start : start+window]
		quoted := strconv.Quote(piece)
		quoted = quoted[1 : len(quoted)-1]
		if strings.Contains(response, quoted) && !strings.Contains(vocabulary, piece) {
			t.Fatalf("response echoes %d bytes of caller text %q: %s", window, piece, response)
		}
	}
}
