package phase0_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/phase0"
	"github.com/eleven-am/golem/go/phase0/fixtures"
)

type findManyCase struct {
	Name        string         `json:"name"`
	ActorID     string         `json:"actorId"`
	System      bool           `json:"system"`
	Where       map[string]any `json:"where"`
	Skip        int            `json:"skip"`
	Take        int            `json:"take"`
	ExpectedIDs []string       `json:"expectedIds"`
}

type orderedRuleCase struct {
	Name        string        `json:"name"`
	Rules       []orderedRule `json:"rules"`
	ExpectedIDs []string      `json:"expectedIds"`
}

type orderedRule struct {
	Effect string         `json:"effect"`
	Where  map[string]any `json:"where"`
}

type oracleDeclaration[M any] struct {
	code  string
	apply func(*phase0.Rules[M])
}

func TestFindManyContractFixtures(t *testing.T) {
	posts := readJSON[[]phase0.Record](t, "testdata/posts.json")
	cases := readJSON[[]findManyCase](t, "testdata/find_many_cases.json")

	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			scope := phase0.All[fixtures.Post]()
			if !test.System {
				var rules phase0.Rules[fixtures.Post]
				fixtures.PostPolicy{}.Define(&rules, fixtures.Actor{ID: test.ActorID})
				scope = rules.Effective(phase0.Read)
			}
			scope = scope.And(requestPredicate(t, test.Where))

			visible := make([]phase0.Record, 0, len(posts))
			for _, post := range posts {
				matches, err := phase0.Evaluate(scope, post)
				if err != nil {
					t.Fatalf("evaluate policy: %v", err)
				}
				if matches {
					visible = append(visible, post)
				}
			}
			sort.Slice(visible, func(i, j int) bool {
				return visible[i].Fields["id"].(string) < visible[j].Fields["id"].(string)
			})
			visible = paginate(visible, test.Skip, test.Take)

			actual := make([]string, len(visible))
			for index, post := range visible {
				actual[index] = post.Fields["id"].(string)
			}
			if !reflect.DeepEqual(actual, test.ExpectedIDs) {
				t.Fatalf("expected ids %v, got %v", test.ExpectedIDs, actual)
			}
		})
	}
}

func TestRuleAlgebraIsDefaultDenyAndPriorityOrdered(t *testing.T) {
	var empty phase0.Rules[fixtures.Post]
	if phase0.Classify(empty.Effective(phase0.Read)) != phase0.Never {
		t.Fatal("an action with no allowance must be denied")
	}

	first := phase0.Rules[fixtures.Post]{}
	first.CanRead(phase0.All[fixtures.Post]())
	first.CannotRead(fixtures.PostPublished.Eq(false))
	first.CanRead(fixtures.PostAuthorID.Eq("u1"))

	second := phase0.Rules[fixtures.Post]{}
	second.CanRead(phase0.All[fixtures.Post]())
	second.CanRead(fixtures.PostAuthorID.Eq("u1"))
	second.CannotRead(fixtures.PostPublished.Eq(false))

	draftByActor := phase0.Record{Fields: map[string]any{
		"authorId": "u1", "published": false,
	}}
	if !mustEvaluate(t, first.Effective(phase0.Read), draftByActor) {
		t.Fatal("the newer matching grant must override the older denial")
	}
	if mustEvaluate(t, second.Effective(phase0.Read), draftByActor) {
		t.Fatal("the newer matching denial must override the older grant")
	}
}

func TestOrderedRuleCasesMatchTheTypeScriptCASLOracle(t *testing.T) {
	cases := readJSON[[]orderedRuleCase](t, "testdata/ordered_rule_cases.json")
	rows := []phase0.Record{
		{Fields: map[string]any{"id": "own-draft", "authorId": "u1", "published": false}},
		{Fields: map[string]any{"id": "other-draft", "authorId": "u2", "published": false}},
		{Fields: map[string]any{"id": "other-published", "authorId": "u2", "published": true}},
	}

	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			var rules phase0.Rules[fixtures.Post]
			for _, declaration := range test.Rules {
				predicate := requestPredicate(t, declaration.Where)
				switch declaration.Effect {
				case "can":
					rules.CanRead(predicate)
				case "cannot":
					rules.CannotRead(predicate)
				default:
					t.Fatalf("unknown rule effect %q", declaration.Effect)
				}
			}

			actual := make([]string, 0, len(rows))
			for _, row := range rows {
				if mustEvaluate(t, rules.Effective(phase0.Read), row) {
					actual = append(actual, row.Fields["id"].(string))
				}
			}
			if !reflect.DeepEqual(actual, test.ExpectedIDs) {
				t.Fatalf("TypeScript oracle expected ids %v, got %v", test.ExpectedIDs, actual)
			}
		})
	}
}

func TestGeneratedRuleChainsMatchTheTypeScriptCASLOracle(t *testing.T) {
	const (
		rowOracleCases    = 259
		rowOracleSHA256   = "b8d27ff35d8a6c682c6da43d731ac836a49f70c5d9b180ce2a1c636c41bebe08"
		fieldOracleCases  = 259
		fieldOracleSHA256 = "ef0151d547b7c7e059ccd5b890f212e2ef57b7a2eb23f8ace127bc9c7b330996"
	)

	rowAlphabet := []oracleDeclaration[fixtures.Post]{
		{"A", func(r *phase0.Rules[fixtures.Post]) { r.CanRead(phase0.All[fixtures.Post]()) }},
		{"D", func(r *phase0.Rules[fixtures.Post]) { r.CannotRead(phase0.All[fixtures.Post]()) }},
		{"O", func(r *phase0.Rules[fixtures.Post]) { r.CanRead(fixtures.PostAuthorID.Eq("u1")) }},
		{"X", func(r *phase0.Rules[fixtures.Post]) { r.CannotRead(fixtures.PostAuthorID.Eq("u1")) }},
		{"P", func(r *phase0.Rules[fixtures.Post]) { r.CanRead(fixtures.PostPublished.Eq(true)) }},
		{"N", func(r *phase0.Rules[fixtures.Post]) { r.CannotRead(fixtures.PostPublished.Eq(false)) }},
	}
	postRows := []phase0.Record{
		{Fields: map[string]any{"authorId": "u1", "published": false}},
		{Fields: map[string]any{"authorId": "u1", "published": true}},
		{Fields: map[string]any{"authorId": "u2", "published": false}},
		{Fields: map[string]any{"authorId": "u2", "published": true}},
	}
	rowChains := oracleChains(rowAlphabet, 3)
	if len(rowChains) != rowOracleCases {
		t.Fatalf("expected %d row oracle chains, built %d", rowOracleCases, len(rowChains))
	}
	rowDigest := oracleDigest(t, rowChains, func(rules *phase0.Rules[fixtures.Post]) string {
		var bits strings.Builder
		for _, row := range postRows {
			bits.WriteByte(verdictBit(mustEvaluate(t, rules.Effective(phase0.Read), row)))
		}
		return bits.String()
	})
	if rowDigest != rowOracleSHA256 {
		t.Fatalf("row rule oracle digest mismatch: TypeScript %s, Go %s", rowOracleSHA256, rowDigest)
	}

	fieldAlphabet := []oracleDeclaration[fixtures.User]{
		{"M", func(r *phase0.Rules[fixtures.User]) { r.CanRead(phase0.All[fixtures.User]()) }},
		{"m", func(r *phase0.Rules[fixtures.User]) { r.CannotRead(phase0.All[fixtures.User]()) }},
		{"E", func(r *phase0.Rules[fixtures.User]) {
			r.CanReadFields(phase0.All[fixtures.User](), fixtures.UserEmail)
		}},
		{"e", func(r *phase0.Rules[fixtures.User]) {
			r.CannotReadFields(phase0.All[fixtures.User](), fixtures.UserEmail)
		}},
		{"S", func(r *phase0.Rules[fixtures.User]) {
			r.CanReadFields(fixtures.UserID.Eq("u1"), fixtures.UserEmail)
		}},
		{"s", func(r *phase0.Rules[fixtures.User]) {
			r.CannotReadFields(fixtures.UserID.Eq("u1"), fixtures.UserEmail)
		}},
	}
	userRows := []phase0.Record{
		{Fields: map[string]any{"id": "u1"}},
		{Fields: map[string]any{"id": "u2"}},
	}
	fieldChains := oracleChains(fieldAlphabet, 3)
	if len(fieldChains) != fieldOracleCases {
		t.Fatalf("expected %d field oracle chains, built %d", fieldOracleCases, len(fieldChains))
	}
	fieldDigest := oracleDigest(t, fieldChains, func(rules *phase0.Rules[fixtures.User]) string {
		var bits strings.Builder
		for _, row := range userRows {
			bits.WriteByte(verdictBit(mustEvaluate(t, rules.Effective(phase0.Read), row)))
		}
		for _, row := range userRows {
			bits.WriteByte(verdictBit(mustEvaluate(t, rules.EffectiveField(phase0.Read, fixtures.UserEmail), row)))
		}
		for _, row := range userRows {
			bits.WriteByte(verdictBit(mustEvaluate(t, rules.EffectiveField(phase0.Read, fixtures.UserName), row)))
		}
		return bits.String()
	})
	if fieldDigest != fieldOracleSHA256 {
		t.Fatalf("field rule oracle digest mismatch: TypeScript %s, Go %s", fieldOracleSHA256, fieldDigest)
	}
}

func oracleChains[M any](alphabet []oracleDeclaration[M], maxDepth int) [][]oracleDeclaration[M] {
	all := [][]oracleDeclaration[M]{{}}
	level := [][]oracleDeclaration[M]{{}}
	for depth := 1; depth <= maxDepth; depth++ {
		next := make([][]oracleDeclaration[M], 0, len(level)*len(alphabet))
		for _, chain := range level {
			for _, declaration := range alphabet {
				candidate := append(append([]oracleDeclaration[M]{}, chain...), declaration)
				next = append(next, candidate)
			}
		}
		all = append(all, next...)
		level = next
	}
	return all
}

func oracleDigest[M any](
	t *testing.T,
	chains [][]oracleDeclaration[M],
	verdicts func(*phase0.Rules[M]) string,
) string {
	t.Helper()
	var lines strings.Builder
	for _, chain := range chains {
		var rules phase0.Rules[M]
		var code strings.Builder
		for _, declaration := range chain {
			code.WriteString(declaration.code)
			declaration.apply(&rules)
		}
		if code.Len() == 0 {
			code.WriteByte('-')
		}
		fmt.Fprintf(&lines, "%s:%s\n", code.String(), verdicts(&rules))
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(lines.String())))
}

func verdictBit(verdict bool) byte {
	if verdict {
		return '1'
	}
	return '0'
}

func TestFieldRulesShareTheOrderedModelRuleChain(t *testing.T) {
	var rules phase0.Rules[fixtures.User]
	rules.CanRead(phase0.All[fixtures.User]())
	rules.CannotReadFields(phase0.All[fixtures.User](), fixtures.UserEmail)
	rules.CanReadFields(fixtures.UserID.Eq("u1"), fixtures.UserEmail)

	if got := phase0.Classify(rules.EffectiveField(phase0.Read, fixtures.UserName)); got != phase0.Always {
		t.Fatalf("model grant should apply to an ordinary field, got %s", got)
	}
	if got := phase0.Classify(rules.EffectiveField(phase0.Read, fixtures.UserEmail)); got != phase0.Conditional {
		t.Fatalf("higher-priority conditional grant should override the field denial, got %s", got)
	}
	if got := phase0.Classify(rules.Effective(phase0.Read)); got != phase0.Always {
		t.Fatalf("field denial must not hide the row, got %s", got)
	}

	own := phase0.Record{Fields: map[string]any{"id": "u1"}}
	other := phase0.Record{Fields: map[string]any{"id": "u2"}}
	if !mustEvaluate(t, rules.EffectiveField(phase0.Read, fixtures.UserEmail), own) {
		t.Fatal("newer conditional field grant should expose the matching row")
	}
	if mustEvaluate(t, rules.EffectiveField(phase0.Read, fixtures.UserEmail), other) {
		t.Fatal("older unconditional field denial should cover the non-matching row")
	}
}

func TestFieldGrantAlsoGrantsTheModelAction(t *testing.T) {
	var rules phase0.Rules[fixtures.Post]
	rules.CanUpdateFields(fixtures.PostAuthorID.Eq("u1"), fixtures.PostTitle)

	owned := phase0.Record{Fields: map[string]any{"authorId": "u1"}}
	other := phase0.Record{Fields: map[string]any{"authorId": "u2"}}
	if !mustEvaluate(t, rules.Effective(phase0.Update), owned) {
		t.Fatal("a positive field rule must grant its model action on matching rows")
	}
	if mustEvaluate(t, rules.Effective(phase0.Update), other) {
		t.Fatal("a field grant must retain its row condition")
	}
	if phase0.Classify(rules.EffectiveField(phase0.Update, fixtures.PostAuthorID)) != phase0.Never {
		t.Fatal("the field grant must not grant an unmentioned field")
	}
}

func TestFieldRulesClassifyAndEvaluatePerRow(t *testing.T) {
	var rules phase0.Rules[fixtures.User]
	fixtures.UserPolicy{}.Define(&rules, fixtures.Actor{ID: "u1"})

	if got := phase0.Classify(rules.EffectiveField(phase0.Read, fixtures.UserName)); got != phase0.Always {
		t.Fatalf("ordinary field should inherit row access, got %s", got)
	}
	if got := phase0.Classify(rules.EffectiveField(phase0.Read, fixtures.UserEmail)); got != phase0.Conditional {
		t.Fatalf("email should be conditional, got %s", got)
	}
	if got := phase0.Classify(rules.EffectiveField(phase0.Update, fixtures.UserID)); got != phase0.Never {
		t.Fatalf("id should never be writable, got %s", got)
	}

	own := phase0.Record{Fields: map[string]any{"id": "u1"}}
	other := phase0.Record{Fields: map[string]any{"id": "u2"}}
	if !mustEvaluate(t, rules.EffectiveField(phase0.Read, fixtures.UserEmail), own) {
		t.Fatal("actor should read their own email")
	}
	if mustEvaluate(t, rules.EffectiveField(phase0.Read, fixtures.UserEmail), other) {
		t.Fatal("actor should not read another user's email")
	}
}

func TestOperationMethodsExpressTheSocialWritePolicy(t *testing.T) {
	var ownerRules phase0.Rules[fixtures.Post]
	fixtures.PostPolicy{}.Define(&ownerRules, fixtures.Actor{ID: "u1"})

	owned := phase0.Record{Fields: map[string]any{
		"authorId": "u1", "title": "mine", "published": false,
	}}
	other := phase0.Record{Fields: map[string]any{
		"authorId": "u2", "title": "theirs", "published": false,
	}}
	for _, action := range []phase0.Action{phase0.Create, phase0.Update, phase0.Delete} {
		if !mustEvaluate(t, ownerRules.Effective(action), owned) {
			t.Fatalf("owner should satisfy %s", action)
		}
		if mustEvaluate(t, ownerRules.Effective(action), other) {
			t.Fatalf("non-owner should not satisfy %s", action)
		}
	}
	if got := phase0.Classify(ownerRules.EffectiveField(phase0.Update, fixtures.PostAuthorID)); got != phase0.Never {
		t.Fatalf("authorId should never be writable, got %s", got)
	}
	if !mustEvaluate(t, ownerRules.EffectiveField(phase0.Update, fixtures.PostTitle), owned) {
		t.Fatal("owner should be able to update title")
	}

	var adminRules phase0.Rules[fixtures.Post]
	fixtures.PostPolicy{}.Define(&adminRules, fixtures.Actor{ID: "admin", Admin: true})
	for _, action := range []phase0.Action{phase0.Read, phase0.Create, phase0.Update, phase0.Delete} {
		if phase0.Classify(adminRules.Effective(action)) != phase0.Always {
			t.Fatalf("administrator %s should be unconditional", action)
		}
	}
}

func TestGeneratedStyleFieldsAndRelationsAreTyped(t *testing.T) {
	friendOfActor := fixtures.UserFriends.Some(fixtures.UserID.Eq("u1"))
	postVisibleThroughAuthor := fixtures.PostAuthor.Is(friendOfActor)

	post := phase0.Record{
		Fields: map[string]any{"id": "p1"},
		Relations: map[string][]phase0.Record{
			"author": {{
				Fields: map[string]any{"id": "u2"},
				Relations: map[string][]phase0.Record{
					"friends": {{Fields: map[string]any{"id": "u1"}}},
				},
			}},
		},
	}
	if !mustEvaluate(t, postVisibleThroughAuthor, post) {
		t.Fatal("nested relation predicate should match the fixture")
	}

	withoutFriends := phase0.Record{
		Fields: map[string]any{"id": "p2"},
		Relations: map[string][]phase0.Record{
			"author": {{Fields: map[string]any{"id": "u2"}}},
		},
	}
	if mustEvaluate(t, postVisibleThroughAuthor, withoutFriends) {
		t.Fatal("nested relation predicate should not match without the relation")
	}
}

func TestNormalizationIsStable(t *testing.T) {
	a := fixtures.PostPublished.Eq(true)
	b := fixtures.PostAuthorID.Eq("u1")
	first := a.Or(b, a, phase0.None[fixtures.Post]())
	second := b.Or(a)
	if mustCanonical(t, first) != mustCanonical(t, second) {
		t.Fatal("commutative order, duplicates, and identities must normalize away")
	}
	if phase0.Classify(phase0.And[fixtures.Post]()) != phase0.Always {
		t.Fatal("empty AND must be true")
	}
	if phase0.Classify(phase0.Or[fixtures.Post]()) != phase0.Never {
		t.Fatal("empty OR must be false")
	}
}

func TestPoliciesAreBuiltPerActor(t *testing.T) {
	var first, second phase0.Rules[fixtures.Post]
	fixtures.PostPolicy{}.Define(&first, fixtures.Actor{ID: "u1"})
	fixtures.PostPolicy{}.Define(&second, fixtures.Actor{ID: "u2"})
	if mustCanonical(t, first.Effective(phase0.Read)) == mustCanonical(t, second.Effective(phase0.Read)) {
		t.Fatal("two actors must not share a resolved policy")
	}
}

func TestEveryPhase0OperatorRequiresSQLiteAndPostgreSQLParity(t *testing.T) {
	if err := phase0.ValidateProviderParity(); err != nil {
		t.Fatal(err)
	}
	for _, operator := range phase0.Phase0Operators {
		if !phase0.Supports(phase0.SQLite, operator) || !phase0.Supports(phase0.PostgreSQL, operator) {
			t.Fatalf("operator %s is not portable", operator)
		}
	}
}

func TestEveryDeclaredOperatorHasEvaluatorSemantics(t *testing.T) {
	post := phase0.Record{
		Fields: map[string]any{
			"id": "p1", "authorId": "u1", "title": "hello", "published": true,
		},
		Relations: map[string][]phase0.Record{
			"author": {{Fields: map[string]any{"id": "u2"}}},
		},
	}
	postCases := []struct {
		operator  phase0.Operator
		predicate phase0.Predicate[fixtures.Post]
		expected  bool
	}{
		{phase0.OpAll, phase0.All[fixtures.Post](), true},
		{phase0.OpNone, phase0.None[fixtures.Post](), false},
		{phase0.OpEqual, fixtures.PostAuthorID.Eq("u1"), true},
		{phase0.OpNotEqual, fixtures.PostAuthorID.Ne("u2"), true},
		{phase0.OpIn, fixtures.PostAuthorID.In("u1", "u3"), true},
		{phase0.OpAnd, fixtures.PostAuthorID.Eq("u1").And(fixtures.PostPublished.Eq(true)), true},
		{phase0.OpOr, fixtures.PostAuthorID.Eq("u2").Or(fixtures.PostPublished.Eq(true)), true},
		{phase0.OpNot, fixtures.PostPublished.Eq(false).Not(), true},
		{phase0.OpRelationIs, fixtures.PostAuthor.Is(fixtures.UserID.Eq("u2")), true},
		{phase0.OpRelationIsNot, fixtures.PostAuthor.IsNot(fixtures.UserID.Eq("u1")), true},
	}

	covered := make(map[phase0.Operator]bool)
	for _, test := range postCases {
		covered[test.operator] = true
		if got := mustEvaluate(t, test.predicate, post); got != test.expected {
			t.Fatalf("operator %s: expected %v, got %v", test.operator, test.expected, got)
		}
	}

	user := phase0.Record{
		Fields: map[string]any{"id": "u2"},
		Relations: map[string][]phase0.Record{
			"friends": {
				{Fields: map[string]any{"id": "u1"}},
				{Fields: map[string]any{"id": "u3"}},
			},
		},
	}
	userCases := []struct {
		operator  phase0.Operator
		predicate phase0.Predicate[fixtures.User]
		expected  bool
	}{
		{phase0.OpRelationSome, fixtures.UserFriends.Some(fixtures.UserID.Eq("u1")), true},
		{phase0.OpRelationEvery, fixtures.UserFriends.Every(fixtures.UserID.Ne("blocked")), true},
		{phase0.OpRelationNone, fixtures.UserFriends.None(fixtures.UserID.Eq("u9")), true},
	}
	for _, test := range userCases {
		covered[test.operator] = true
		if got := mustEvaluate(t, test.predicate, user); got != test.expected {
			t.Fatalf("operator %s: expected %v, got %v", test.operator, test.expected, got)
		}
	}

	for _, operator := range phase0.Phase0Operators {
		if !covered[operator] {
			t.Fatalf("declared operator %s has no evaluator conformance case", operator)
		}
	}
}

func TestEvaluatorRejectsImpossibleToOneFixture(t *testing.T) {
	post := phase0.Record{
		Fields: map[string]any{"id": "p1"},
		Relations: map[string][]phase0.Record{
			"author": {
				{Fields: map[string]any{"id": "u1"}},
				{Fields: map[string]any{"id": "u2"}},
			},
		},
	}
	_, err := phase0.Evaluate(fixtures.PostAuthor.Is(fixtures.UserID.Eq("u1")), post)
	if err == nil {
		t.Fatal("a to-one fixture containing multiple rows must fail closed")
	}
}

func requestPredicate(t *testing.T, where map[string]any) phase0.Predicate[fixtures.Post] {
	t.Helper()
	parts := make([]phase0.Predicate[fixtures.Post], 0, len(where))
	for name, value := range where {
		switch name {
		case "authorId":
			parts = append(parts, fixtures.PostAuthorID.Eq(value.(string)))
		case "published":
			parts = append(parts, fixtures.PostPublished.Eq(value.(bool)))
		default:
			t.Fatalf("unsupported fixture filter %q", name)
		}
	}
	return phase0.And(parts...)
}

func paginate(records []phase0.Record, skip, take int) []phase0.Record {
	if skip >= len(records) {
		return nil
	}
	records = records[skip:]
	if take > 0 && take < len(records) {
		records = records[:take]
	}
	return records
}

func readJSON[T any](t *testing.T, path string) T {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err := json.Unmarshal(content, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func mustEvaluate[M any](t *testing.T, predicate phase0.Predicate[M], record phase0.Record) bool {
	t.Helper()
	matches, err := phase0.Evaluate(predicate, record)
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func mustCanonical[M any](t *testing.T, predicate phase0.Predicate[M]) string {
	t.Helper()
	value, err := phase0.Canonical(predicate)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
