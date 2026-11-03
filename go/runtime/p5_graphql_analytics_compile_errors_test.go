package runtime_test

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

const analyticsCompileErrorMarker = "private-driver-token"

type analyticsCompileErrorCase struct {
	name       string
	query      string
	message    string
	path       []any
	extensions map[string]any
}

func analyticsInput(argument string) map[string]any {
	return map[string]any{"code": "BAD_USER_INPUT", "argument": argument}
}

func analyticsInvalid(argument string) map[string]any {
	return map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": argument}
}

func analyticsCompileErrorCases() []analyticsCompileErrorCase {
	group := func(arguments, selection string) string {
		return `{ groupByPosts(by: [title]` + arguments + `) { ` + selection + ` } }`
	}
	root := func(name string) []any { return []any{name} }
	plain := map[string]any{"code": "BAD_USER_INPUT"}
	return []analyticsCompileErrorCase{
		{"aggregate-without-measure", `{ aggregatePosts { __typename } }`, "An aggregate on Post must select at least one measure", root("aggregatePosts"), plain},
		{"aggregate-where-empty", `{ aggregatePosts(where: {}) { count } }`, "Argument where on Post must not be empty", root("aggregatePosts"), analyticsInput("where")},
		{"group-by-empty", `{ groupByPosts(by: []) { count } }`, "Argument by on Post must not be empty", root("groupByPosts"), analyticsInput("by")},
		{"group-by-duplicate", `{ groupByPosts(by: [title, title]) { count } }`, "Argument by on Post names a field more than once", root("groupByPosts"), analyticsInput("by")},
		{"relation-group-without-relation-dimension", `{ relationGroupByPosts(by: [title]) { count } }`, "Relation grouping on Post requires a relation dimension in by", root("relationGroupByPosts"), analyticsInput("by")},
		{"selected-key-not-in-by", group("", "key { id }"), "Key id on Post must be listed in by", root("groupByPosts"), analyticsInput("id")},
		{"every-measure-skipped", `query($s: Boolean!) { aggregatePosts { count @skip(if: $s) } }`, "Selection must not be empty", root("aggregatePosts"), plain},
		{"skip-negative", group(", skip: -1", "count"), "Argument skip on Post must be at least 0", root("groupByPosts"), analyticsInvalid("skip")},
		{"take-zero", group(", take: 0", "count"), "Argument take on Post must not be 0", root("groupByPosts"), analyticsInvalid("take")},
		{"take-over-maximum", group(", take: 101", "count"), "Argument take on Post must be between -100 and 100, excluding 0", root("groupByPosts"),
			map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "take", "min": -100, "max": 100}},
		{"take-negative-over-maximum", group(", take: -101", "count"), "Argument take on Post must be between -100 and 100, excluding 0", root("groupByPosts"),
			map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "take", "min": -100, "max": 100}},
		{"having-null", group(", having: null", "count"), "Argument having on Post cannot be null", root("groupByPosts"), analyticsInput("having")},
		{"having-empty", group(", having: {}", "count"), "Argument having on Post must not be empty", root("groupByPosts"), analyticsInput("having")},
		{"having-and-empty", group(", having: {AND: []}", "count"), "Input having.AND on Post must not be empty", root("groupByPosts"), analyticsInput("having.AND")},
		{"having-nested-element-is-indexed", group(`, having: {OR: [{count: {gte: "1"}}, {}]}`, "count"), "Input having.OR[1] on Post must not be empty", root("groupByPosts"), analyticsInput("having.OR[1]")},
		{"having-count-with-marker", group(`, having: {count: {gte: "`+analyticsCompileErrorMarker+`"}}`, "count"), "Input having.count.gte on Post is not a valid BigInt", root("groupByPosts"), analyticsInvalid("having.count.gte")},
		{"having-count-out-of-range", group(`, having: {count: {gte: "9223372036854775808"}}`, "count"), "Input having.count.gte on Post is outside the BigInt range", root("groupByPosts"), analyticsInvalid("having.count.gte")},
		{"having-count-null", group(", having: {count: {equals: null}}", "count"), "Input having.count.equals on Post cannot be null", root("groupByPosts"), analyticsInput("having.count.equals")},
		{"having-key-not-in-by", group(`, having: {key: {id: {equals: "00000000-0000-0000-0000-000000000001"}}}`, "count"), "Key having.key.id on Post must be listed in by", root("groupByPosts"), analyticsInput("having.key.id")},
		{"having-key-value-with-marker", group(`, having: {key: {title: {contains: "`+analyticsCompileErrorMarker+`", mode: insensitive}, id: {equals: "`+analyticsCompileErrorMarker+`"}}}`, "count"), "Key having.key.id on Post must be listed in by", root("groupByPosts"), analyticsInput("having.key.id")},
		{"having-mode-on-equality", group(`, having: {key: {title: {equals: "a", mode: insensitive}}}`, "count"), "Input having.key.title.mode on Post is not supported for this field or provider", root("groupByPosts"), analyticsInput("having.key.title.mode")},
		{"order-by-duplicate-count", group(", orderBy: [{count: asc}, {count: desc}]", "count"), "Argument orderBy on Post names a field more than once", root("groupByPosts"), analyticsInput("orderBy")},
		{"order-by-empty", group(", orderBy: []", "count"), "Argument orderBy on Post must not be empty", root("groupByPosts"), analyticsInput("orderBy")},
		{"order-by-two-terms", group(", orderBy: [{count: asc, key: {title: asc}}]", "count"), "Input orderBy[0] on Post must set exactly one member", root("groupByPosts"), analyticsInput("orderBy[0]")},
		{"order-by-key-not-in-by", group(", orderBy: [{key: {id: asc}}]", "count"), "Key id on Post must be listed in by", root("groupByPosts"), analyticsInput("id")},
		{"where-refusal-keeps-its-path", group(", where: {AND: []}", "count"), "Input where.AND on Post must not be empty", root("groupByPosts"), analyticsInput("where.AND")},
		{"second-root-fails-first-root-is-valid", `{ aggregatePosts { count } failing: groupByPosts(by: [title], take: 0) { count } }`, "Argument take on Post must not be 0", root("failing"), analyticsInvalid("take")},
	}
}

func TestGraphQLAnalyticsCompileErrorsAreTypedAndPrecise(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			harness := newP5SocialGeneratedHarness(t, profile)
			principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
			for _, test := range analyticsCompileErrorCases() {
				t.Run(test.name, func(t *testing.T) {
					harness.trace.reset()
					variables := map[string]any(nil)
					if strings.Contains(test.query, "$s") {
						variables = map[string]any{"s": true}
					}
					response := harness.execute(t, principal, test.query, variables)
					if response.Data != nil || len(response.Errors) != 1 {
						t.Fatalf("response=%#v want exactly one error and no data", response)
					}
					failure := response.Errors[0]
					if failure.Message != test.message {
						t.Fatalf("message=%q want %q", failure.Message, test.message)
					}
					gotPath, _ := json.Marshal(failure.Path)
					wantPath, _ := json.Marshal(test.path)
					gotExtensions, _ := json.Marshal(failure.Extensions)
					wantExtensions, _ := json.Marshal(test.extensions)
					if string(gotPath) != string(wantPath) || string(gotExtensions) != string(wantExtensions) {
						t.Fatalf("path=%s extensions=%s want path=%s extensions=%s", gotPath, gotExtensions, wantPath, wantExtensions)
					}
					encoded, err := json.Marshal(response)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(encoded), analyticsCompileErrorMarker) {
						t.Fatalf("response echoes caller text: %s", encoded)
					}
					if statements := harness.trace.snapshot(); len(statements) != 0 {
						t.Fatalf("a compile refusal ran SQL: %#v", statements)
					}
				})
			}
		})
	}
}

type quietTB struct{ testing.TB }

func (quietTB) Logf(string, ...any) {}

type analyticsFuzzTemplate struct {
	query    string
	literals bool
}

var analyticsFuzzTemplates = []analyticsFuzzTemplate{
	{query: `query($b: [PostGroupField!]!, $n: Int, $k: Int) { groupByPosts(by: $b, take: $n, skip: $k) { count key { title } } }`},
	{query: `query($w: PostWhereInput) { aggregatePosts(where: $w) { count min { title } countFields { title } } }`},
	{query: `query($h: PostGroupHavingInput) { groupByPosts(by: [title], having: $h) { count } }`},
	{query: `query($o: [PostGroupOrderByInput!]) { groupByPosts(by: [title], orderBy: $o) { count } }`},
	{query: `query($b: [PostRelationGroupField!]!, $h: PostRelationGroupHavingInput) { relationGroupByPosts(by: $b, having: $h) { count key { authorName } } }`},
	{query: `query($b: [PostGroupField!]!) { groupByPosts(by: $b) { count key { id title authorID } } }`},
	{query: `{ groupByPosts(by: [title], having: {count: {gte: LITERAL}}) { count } }`, literals: true},
	{query: `{ groupByPosts(by: [title], take: LITERAL) { count } }`, literals: true},
	{query: `{ groupByPosts(by: [title], having: {key: {title: {contains: LITERAL}}}) { count } }`, literals: true},
}

type analyticsFuzzSeed struct {
	template  uint8
	variables string
	literal   string
}

func analyticsFuzzSeeds() []analyticsFuzzSeed {
	deep := strings.Repeat(`{"NOT":`, 40) + `{"count":{"gte":"1"}}` + strings.Repeat(`}`, 40)
	long := `{"AND":[` + strings.TrimSuffix(strings.Repeat(`{"count":{"gte":"1"}},`, 60), ",") + `]}`
	return []analyticsFuzzSeed{
		{0, `{"b":["title"],"n":2,"k":0}`, ""},
		{0, `{"b":["title","title"],"n":2,"k":0}`, ""},
		{0, `{"b":[],"n":2}`, ""},
		{0, `{"b":["title"],"n":0}`, ""},
		{0, `{"b":["title"],"n":1000}`, ""},
		{0, `{"b":["title"],"n":-1000}`, ""},
		{0, `{"b":["title"],"k":-1}`, ""},
		{0, `{"b":["title"],"n":2147483647,"k":2147483647}`, ""},
		{1, `{"w":{}}`, ""},
		{1, `{"w":{"title":{"equals":"private-driver-token"}}}`, ""},
		{1, `{"w":{"id":{"equals":"private-driver-token"}}}`, ""},
		{1, `{"w":null}`, ""},
		{2, `{"h":{}}`, ""},
		{2, `{"h":null}`, ""},
		{2, `{"h":{"AND":[]}}`, ""},
		{2, `{"h":{"count":{}}}`, ""},
		{2, `{"h":{"count":{"gte":"private-driver-token"}}}`, ""},
		{2, `{"h":{"count":{"gte":"9223372036854775808"}}}`, ""},
		{2, `{"h":{"count":{"equals":null}}}`, ""},
		{2, `{"h":{"count":{"contains":"private-driver-token"}}}`, ""},
		{2, `{"h":{"key":{"id":{"equals":"00000000-0000-0000-0000-000000000001"}}}}`, ""},
		{2, `{"h":{"key":{"title":{"equals":"private-driver-token","mode":"insensitive"}}}}`, ""},
		{2, `{"h":{"key":{"title":{"contains":"private-driver-token","mode":"insensitive"}}}}`, ""},
		{2, `{"h":{"key":{"title":{"isNull":null}}}}`, ""},
		{2, `{"h":{"min":{"title":{"gte":null}}}}`, ""},
		{2, `{"h":{"NOT":{}}}`, ""},
		{2, `{"h":` + deep + `}`, ""},
		{2, `{"h":` + long + `}`, ""},
		{3, `{"o":[{"count":"asc"},{"count":"desc"}]}`, ""},
		{3, `{"o":[{"key":{"title":"asc"}},{"key":{"title":"desc"}}]}`, ""},
		{3, `{"o":[{"key":{"id":"asc"}}]}`, ""},
		{3, `{"o":[{}]}`, ""},
		{3, `{"o":[]}`, ""},
		{3, `{"o":[{"count":"asc","key":{"title":"asc"}}]}`, ""},
		{4, `{"b":["title"],"h":{}}`, ""},
		{4, `{"b":["authorName"],"h":{"key":{"authorName":{"equals":"private-driver-token"}}}}`, ""},
		{4, `{"b":["authorName","title"],"h":{"key":{"title":{"equals":"x"}}}}`, ""},
		{5, `{"b":["title"]}`, ""},
		{5, `{"b":["id"]}`, ""},
		{6, `{}`, `"private-driver-token"`},
		{6, `{}`, `99999999999999999999`},
		{6, `{}`, `null`},
		{7, `{}`, `99999999999999999999`},
		{7, `{}`, `-101`},
		{7, `{}`, `0`},
		{8, `{}`, `"private-driver-token"`},
		{8, `{}`, `null`},
		{8, `{}`, `1`},
	}
}

func collectAnalyticsCallerText(value any, texts *[]string) {
	switch typed := value.(type) {
	case string:
		*texts = append(*texts, typed)
	case []any:
		for _, item := range typed {
			collectAnalyticsCallerText(item, texts)
		}
	case map[string]any:
		for key, item := range typed {
			*texts = append(*texts, key)
			collectAnalyticsCallerText(item, texts)
		}
	}
}

func FuzzAnalyticsCompileErrorsAreTypedAndNeverEcho(f *testing.F) {
	profile := p5ExtensionProviderProfiles()[0]
	harness := newP5SocialGeneratedHarness(quietTB{f}, profile)
	principal := p5social.Principal{Valid: true, UserID: golem.UUID{15: 1}}
	vocabulary := harness.server.SDL()
	for _, seed := range analyticsFuzzSeeds() {
		f.Add(seed.template, seed.variables, seed.literal)
	}
	f.Fuzz(func(t *testing.T, template uint8, rawVariables string, literal string) {
		shape := analyticsFuzzTemplates[int(template)%len(analyticsFuzzTemplates)]
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
		collectAnalyticsCallerText(variables, &callerText)
		if shape.literals {
			callerText = append(callerText, literal)
		}
		response := harness.execute(t, principal, query, variables)
		encoded, err := json.Marshal(response.Errors)
		if err != nil {
			t.Fatal(err)
		}
		for _, failure := range response.Errors {
			if failure.Extensions["code"] == "INTERNAL_SERVER_ERROR" {
				t.Fatalf("a caller-reachable analytics failure was presented as internal: %s (query %q variables %q)", encoded, query, rawVariables)
			}
			if failure.Message == "GraphQL operation could not be bound" {
				t.Fatalf("an analytics compile failure kept the generic bind message: %s (query %q variables %q)", encoded, query, rawVariables)
			}
		}
		if strings.Contains(string(encoded), analyticsCompileErrorMarker) {
			t.Fatalf("response echoes the marker: %s (query %q variables %q)", encoded, query, rawVariables)
		}
		checked := analyticsEchoSubject(t, response.Errors)
		for _, text := range callerText {
			assertNoAnalyticsEcho(t, checked, text, vocabulary)
		}
	})
}

var analyticsNumber = regexp.MustCompile(`[0-9]+`)

func analyticsEchoSubject(t *testing.T, failures []struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path"`
	Extensions map[string]any `json:"extensions"`
}) string {
	t.Helper()
	var subject strings.Builder
	for _, failure := range failures {
		encoded, err := json.Marshal(failure)
		if err != nil {
			t.Fatal(err)
		}
		text := string(encoded)
		if failure.Extensions["code"] == "QUERY_LIMIT_EXCEEDED" {
			text = analyticsNumber.ReplaceAllString(text, "#")
		}
		subject.WriteString(text)
	}
	return subject.String()
}

func assertNoAnalyticsEcho(t *testing.T, response, text, vocabulary string) {
	t.Helper()
	const window = 8
	for start := 0; start+window <= len(text); start++ {
		piece := text[start : start+window]
		quoted := strconv.Quote(piece)
		quoted = quoted[1 : len(quoted)-1]
		if strings.Contains(response, quoted) && !strings.Contains(vocabulary, piece) {
			t.Fatalf("response echoes %d bytes of caller text %q: %s", window, piece, response)
		}
	}
}
