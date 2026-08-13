package gate

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"example.com/golempolicykit/policy"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/golemtest"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

type externalCoverageObserver struct{}

var externalCoverageMu sync.Mutex

func (externalCoverageObserver) ObserveGolem(_ context.Context, value observe.Observation) {
	path := os.Getenv("P8_OBSERVATION_COVERAGE_FILE")
	if path == "" {
		return
	}
	externalCoverageMu.Lock()
	defer externalCoverageMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic("open observation coverage sink")
	}
	if _, err := fmt.Fprintln(file, value.Provider(), value.Operation()); err != nil {
		_ = file.Close()
		panic("write observation coverage sink")
	}
	if err := file.Close(); err != nil {
		panic("close observation coverage sink")
	}
}

const (
	aliceText = "11111111-1111-4111-8111-111111111111"
	bobText   = "22222222-2222-4222-8222-222222222222"

	authorOpenText   = "aa000001-0000-4000-8000-000000000001"
	authorHiddenText = "aa000002-0000-4000-8000-000000000002"
	authorPlainText  = "aa000003-0000-4000-8000-000000000003"

	ownedPrivateText = "bb000001-0000-4000-8000-000000000001"
	ownedPublicText  = "bb000002-0000-4000-8000-000000000002"
	otherPublicText  = "bb000003-0000-4000-8000-000000000003"
	otherPrivateText = "bb000004-0000-4000-8000-000000000004"
	hiddenAuthorText = "bb000005-0000-4000-8000-000000000005"
)

func identifier(t *testing.T, text string) golem.UUID {
	t.Helper()
	value, err := golem.ParseUUID(text)
	if err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	return value
}

func actor(t *testing.T) policy.Actor {
	t.Helper()
	return policy.Actor{UserID: identifier(t, aliceText), Authenticated: true}
}

func declaredRowConstraint(t *testing.T) golem.Predicate[policy.Article] {
	t.Helper()
	return policy.Articles.Public.Eq(true).Or(policy.Articles.OwnerID.Eq(identifier(t, aliceText)))
}

type readField struct {
	name        string
	handle      golem.Field[policy.Article]
	selection   golem.Selection[policy.Article]
	column      golem.ScalarColumn[policy.Article, string]
	conditional bool
	declared    golem.Predicate[policy.Article]
	observable  golem.Predicate[policy.Article]
}

func readableAuthor() golem.Predicate[policy.Author] {
	return policy.Authors.Verified.Eq(true).And(policy.Authors.Listed.Eq(true))
}

func projectedFields(t *testing.T) []readField {
	t.Helper()
	return []readField{
		{
			name: "Title", handle: policy.Articles.Title, selection: policy.Articles.Title, column: policy.Articles.Title,
			conditional: true, declared: declaredRowConstraint(t), observable: declaredRowConstraint(t),
		},
		{
			name: "Draft", handle: policy.Articles.Draft, selection: policy.Articles.Draft, column: policy.Articles.Draft,
			conditional: true,
			declared:    policy.Articles.OwnerID.Eq(identifier(t, aliceText)),
			observable:  policy.Articles.OwnerID.Eq(identifier(t, aliceText)),
		},
		{
			name: "Notes", handle: policy.Articles.Notes, selection: policy.Articles.Notes, column: policy.Articles.Notes,
			conditional: true,
			declared:    declaredRowConstraint(t).And(policy.Articles.Author.Is(policy.Authors.Verified.Eq(true))),
			observable:  declaredRowConstraint(t).And(policy.Articles.Author.Is(readableAuthor())),
		},
		{
			name: "Summary", handle: policy.Articles.Summary, selection: policy.Articles.Summary, column: policy.Articles.Summary,
			conditional: true,
			declared:    declaredRowConstraint(t).And(policy.Articles.Comments.Some(policy.Comments.Approved.Eq(true))),
			observable:  declaredRowConstraint(t).And(policy.Articles.Comments.Some(policy.Comments.Approved.Eq(true))),
		},
	}
}

type target struct {
	name string
	open func(*testing.T) *provider.Database
}

func targets(t *testing.T) []target {
	t.Helper()
	result := []target{}
	sqliteDSN := strings.TrimSpace(os.Getenv("GOLEM_KIT_SQLITE_DSN"))
	if sqliteDSN == "" {
		t.Fatal("GOLEM_KIT_SQLITE_DSN is required")
	}
	result = append(result, target{name: "sqlite", open: func(t *testing.T) *provider.Database {
		t.Helper()
		database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: sqliteDSN})
		if err != nil {
			t.Fatalf("open SQLite: %v", err)
		}
		return database
	}})
	for _, profile := range []struct{ name, variable string }{
		{name: "postgresql-c", variable: "GOLEM_KIT_POSTGRES_C_DSN"},
		{name: "postgresql-linguistic", variable: "GOLEM_KIT_POSTGRES_LINGUISTIC_DSN"},
	} {
		dsn := strings.TrimSpace(os.Getenv(profile.variable))
		if dsn == "" {
			if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1" {
				t.Fatalf("%s is required when mandatory PostgreSQL evidence is enabled", profile.variable)
			}
			continue
		}
		result = append(result, target{name: profile.name, open: func(t *testing.T) *provider.Database {
			t.Helper()
			database, err := postgresql.Open(context.Background(), postgresql.Config{
				DataSourceName: dsn,
				Pool:           postgresql.PoolConfig{MaximumOpen: 4, MaximumIdle: 4},
			})
			if err != nil {
				t.Fatalf("open PostgreSQL %s: %v", profile.name, err)
			}
			return database
		}})
	}
	if os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL") == "1" && len(result) != 3 {
		t.Fatalf("mandatory PostgreSQL evidence requires all three providers, resolved %d", len(result))
	}
	return result
}

type staticAnswers struct {
	articles    golemtest.ModelPolicy[policy.Article]
	row         golemtest.Constraint[policy.Article]
	plan        golemtest.ReadPlan[policy.Article]
	reachPlan   golemtest.ReadPlan[policy.Article]
	authorRow   golemtest.Constraint[policy.Author]
	commentRow  golemtest.Constraint[policy.Comment]
	commentPlan golemtest.ReadPlan[policy.Comment]
}

func staticKitAnswers(t *testing.T) staticAnswers {
	t.Helper()
	bindings, err := policy.GolemGeneratedApplicationBindings()
	if err != nil {
		t.Fatalf("bindings: %v", err)
	}
	descriptors, err := policy.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatalf("descriptors: %v", err)
	}
	kit, err := golemtest.New(bindings, descriptors, policy.GolemGeneratedSchemaBundle())
	if err != nil {
		t.Fatalf("golemtest.New: %v", err)
	}
	policies, err := kit.ForActor(actor(t))
	if err != nil {
		t.Fatalf("ForActor: %v", err)
	}
	articles, err := golemtest.Model(policies, policy.GolemGeneratedArticleDescriptor)
	if err != nil {
		t.Fatalf("Model: %v", err)
	}
	row, err := articles.RowConstraint(golem.FrozenActionRead)
	if err != nil {
		t.Fatalf("RowConstraint: %v", err)
	}
	fields := projectedFields(t)
	handles := make([]golem.Field[policy.Article], 0, len(fields)+1)
	for _, field := range fields {
		handles = append(handles, field.handle)
	}
	handles = append(handles, policy.Articles.Secret)
	plan, err := articles.ClassifyReadFields(golemtest.UseProjection, golem.FrozenActionRead, handles...)
	if err != nil {
		t.Fatalf("ClassifyReadFields: %v", err)
	}
	reachPlan, err := articles.ClassifyReadFieldsWithReach(
		golemtest.UseProjection, golem.FrozenActionRead,
		policy.Articles.OwnerID.Eq(identifier(t, aliceText)),
		policy.Articles.Draft,
	)
	if err != nil {
		t.Fatalf("ClassifyReadFieldsWithReach: %v", err)
	}
	authors, err := golemtest.Model(policies, policy.GolemGeneratedAuthorDescriptor)
	if err != nil {
		t.Fatalf("Model author: %v", err)
	}
	authorRow, err := authors.RowConstraint(golem.FrozenActionRead)
	if err != nil {
		t.Fatalf("RowConstraint author: %v", err)
	}
	comments, err := golemtest.Model(policies, policy.GolemGeneratedCommentDescriptor)
	if err != nil {
		t.Fatalf("Model comment: %v", err)
	}
	commentRow, err := comments.RowConstraint(golem.FrozenActionRead)
	if err != nil {
		t.Fatalf("RowConstraint comment: %v", err)
	}
	commentPlan, err := comments.ClassifyReadFields(golemtest.UseProjection, golem.FrozenActionRead, policy.Comments.ID, policy.Comments.Body)
	if err != nil {
		t.Fatalf("ClassifyReadFields comment: %v", err)
	}
	return staticAnswers{
		articles: articles, row: row, plan: plan, reachPlan: reachPlan,
		authorRow: authorRow, commentRow: commentRow, commentPlan: commentPlan,
	}
}

type application struct {
	database *provider.Database
	app      *policy.App[policy.Actor]
	caller   *policy.Caller[policy.Actor]
	system   policy.System[policy.Actor]
}

func openApplication(t *testing.T, database *provider.Database) application {
	t.Helper()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 32})
	if err != nil {
		t.Fatalf("memory transport: %v", err)
	}
	app, err := policy.Open(context.Background(), policy.Config[policy.Actor]{
		Database:       database,
		EventTransport: transport,
		Observer:       externalCoverageObserver{},
		ResolvePrincipal: func(_ context.Context, value policy.Actor) (policy.Actor, error) {
			return value, nil
		},
		SnapshotPrincipal:   func(value policy.Actor) (policy.Actor, error) { return value, nil },
		SnapshotActor:       func(value policy.Actor) (policy.Actor, error) { return value, nil },
		AuditPrincipal:      func(policy.Actor) string { return "policy-kit-gate" },
		ReportScopedQuery:   func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	caller, err := app.ForPrincipal(context.Background(), actor(t))
	if err != nil {
		t.Fatalf("ForPrincipal: %v", err)
	}
	return application{database: database, app: app, caller: caller, system: app.System()}
}

func seed(t *testing.T, live application) {
	t.Helper()
	ctx := context.Background()
	for _, author := range []struct {
		id               string
		verified, listed bool
		name             string
	}{
		{id: authorOpenText, verified: true, listed: true, name: "open"},
		{id: authorHiddenText, verified: true, listed: false, name: "hidden"},
		{id: authorPlainText, verified: false, listed: true, name: "plain"},
	} {
		if _, err := live.system.Authors.Create(ctx, policy.Authors.Create(
			policy.Authors.ID.Create(identifier(t, author.id)),
			policy.Authors.Name.Create(author.name),
			policy.Authors.Verified.Create(author.verified),
			policy.Authors.Listed.Create(author.listed),
		)); err != nil {
			t.Fatalf("seed author %s: %v", author.name, err)
		}
	}
	for _, article := range []struct {
		id     string
		owner  string
		author string
		public bool
	}{
		{id: ownedPrivateText, owner: aliceText, author: authorOpenText, public: false},
		{id: ownedPublicText, owner: aliceText, author: "", public: true},
		{id: otherPublicText, owner: bobText, author: authorPlainText, public: true},
		{id: otherPrivateText, owner: bobText, author: authorOpenText, public: false},
		{id: hiddenAuthorText, owner: bobText, author: authorHiddenText, public: true},
	} {
		values := []golem.CreateValue[policy.Article]{
			policy.Articles.ID.Create(identifier(t, article.id)),
			policy.Articles.OwnerID.Create(identifier(t, article.owner)),
			policy.Articles.Public.Create(article.public),
			policy.Articles.Title.Create("title-" + article.id),
			policy.Articles.Draft.Create("draft-" + article.id),
			policy.Articles.Notes.Create("notes-" + article.id),
			policy.Articles.Summary.Create("summary-" + article.id),
			policy.Articles.Secret.Create("secret-" + article.id),
		}
		if article.author == "" {
			values = append(values, policy.Articles.AuthorID.CreateNull())
		} else {
			values = append(values, policy.Articles.AuthorID.Create(identifier(t, article.author)))
		}
		if _, err := live.system.Articles.Create(ctx, policy.Articles.Create(values...)); err != nil {
			t.Fatalf("seed article %s: %v", article.id, err)
		}
	}
	for _, comment := range []struct {
		id       string
		article  string
		approved bool
	}{
		{id: "cc000001-0000-4000-8000-000000000001", article: ownedPrivateText, approved: true},
		{id: "cc000002-0000-4000-8000-000000000002", article: otherPublicText, approved: false},
		{id: "cc000003-0000-4000-8000-000000000003", article: otherPrivateText, approved: true},
		{id: "cc000004-0000-4000-8000-000000000004", article: hiddenAuthorText, approved: true},
		{id: "cc000005-0000-4000-8000-000000000005", article: hiddenAuthorText, approved: false},
	} {
		if _, err := live.system.Comments.Create(ctx, policy.Comments.Create(
			policy.Comments.ID.Create(identifier(t, comment.id)),
			policy.Comments.ArticleID.Create(identifier(t, comment.article)),
			policy.Comments.Body.Create("body-"+comment.id),
			policy.Comments.Approved.Create(comment.approved),
		)); err != nil {
			t.Fatalf("seed comment %s: %v", comment.id, err)
		}
	}
}

func systemIdentifiers(t *testing.T, live application, predicate golem.Predicate[policy.Article]) []string {
	t.Helper()
	rows, err := live.system.Articles.FindMany(context.Background(),
		policy.Articles.Where(predicate),
		policy.Articles.OrderBy(policy.Articles.ID.Asc()),
		policy.Articles.Select(policy.Articles.ID),
	)
	if err != nil {
		t.Fatalf("system evaluation of a kit-certified predicate failed: %v", err)
	}
	return rowIdentifiers(t, rows)
}

func rowIdentifiers(t *testing.T, rows []golem.Row[policy.Article]) []string {
	t.Helper()
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		value, present := golem.Value(row, policy.Articles.ID).Get()
		if !present {
			t.Fatal("a returned row carries no readable identity")
		}
		result = append(result, value.String())
	}
	sort.Strings(result)
	return result
}

func intersect(left, right []string) []string {
	member := make(map[string]struct{}, len(right))
	for _, value := range right {
		member[value] = struct{}{}
	}
	result := make([]string, 0, len(left))
	for _, value := range left {
		if _, ok := member[value]; ok {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func sameSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestExternalGeneratedApplicationPolicyKitMatchesCallerBehaviour(t *testing.T) {
	answers := staticKitAnswers(t)
	fields := projectedFields(t)

	t.Run("TheKitAnswersTheAuthoredPolicyWithoutADatabase", func(t *testing.T) {
		if _, constant := answers.row.Constant(); constant {
			t.Fatal("the authored read policy collapsed to a constant")
		}
		equivalent, err := golemtest.Equivalent(answers.row, declaredRowConstraint(t))
		if err != nil || !equivalent {
			t.Fatalf("the resolved read row constraint is not the authored predicate: equivalent=%v err=%v", equivalent, err)
		}
		authorEquivalent, authorErr := golemtest.Equivalent(answers.authorRow, policy.Authors.Listed.Eq(true))
		if authorErr != nil || !authorEquivalent {
			t.Fatalf("the relation target's read row constraint is not the authored predicate: equivalent=%v err=%v", authorEquivalent, authorErr)
		}
		secret, ok := answers.plan.Field(policy.Articles.Secret)
		if !ok || secret.Access() != golemtest.AccessNever {
			t.Fatalf("the denied field classified as %v present=%v", secret.Access(), ok)
		}
		for _, field := range fields {
			classification, present := answers.plan.Field(field.handle)
			if !present {
				t.Fatalf("%s: the kit did not classify a requested field", field.name)
			}
			if field.conditional {
				condition, conditional := classification.Condition()
				if classification.Access() != golemtest.AccessConditional || !conditional {
					t.Fatalf("%s: classified as %v, want a condition", field.name, classification.Access())
				}
				equivalent, err := golemtest.Equivalent(condition, field.declared)
				if err != nil || !equivalent {
					t.Fatalf("%s: the field condition is not the authored predicate: equivalent=%v err=%v", field.name, equivalent, err)
				}
				continue
			}
			if classification.Access() != golemtest.AccessAlways {
				t.Fatalf("%s: classified as %v, want always readable", field.name, classification.Access())
			}
		}
	})

	t.Run("TheKitRetainsTheRelationHopsTheConditionsRequire", func(t *testing.T) {
		article := policy.GolemGeneratedArticleDescriptor.Metadata().ModelID()
		author := policy.GolemGeneratedAuthorDescriptor.Metadata().ModelID()
		comment := policy.GolemGeneratedCommentDescriptor.Metadata().ModelID()

		notes, ok := answers.plan.Field(policy.Articles.Notes)
		if !ok {
			t.Fatal("the kit did not classify the relation-conditional field")
		}
		requireRelationDependency(t, notes.Dependencies(), article, fieldIdentity(t, policy.Articles.Author), author, fieldIdentity(t, policy.Authors.Verified))

		summary, ok := answers.plan.Field(policy.Articles.Summary)
		if !ok {
			t.Fatal("the kit did not classify the quantifier-conditional field")
		}
		requireRelationDependency(t, summary.Dependencies(), article, fieldIdentity(t, policy.Articles.Comments), comment, fieldIdentity(t, policy.Comments.Approved))

		draft, ok := answers.plan.Field(policy.Articles.Draft)
		if !ok {
			t.Fatal("the kit did not classify the owner-conditional field")
		}
		tree := draft.Dependencies()
		if tree.ModelID() != article {
			t.Fatal("the scalar dependency tree is not rooted at the classified model")
		}
		scalar := false
		for _, entry := range tree.Entries() {
			if entry.Kind() == golemtest.DependencyScalar && entry.FieldID() == fieldIdentity(t, policy.Articles.OwnerID) {
				scalar = true
			}
		}
		if !scalar {
			t.Fatal("the owner condition lost the scalar field it is decided from")
		}
	})

	for _, value := range targets(t) {
		value := value
		t.Run(value.name, func(t *testing.T) {
			database := value.open(t)
			defer func() {
				if err := database.Close(); err != nil {
					t.Errorf("close database: %v", err)
				}
			}()
			live := openApplication(t, database)
			seed(t, live)
			assertRuntimeAgreesWithKit(t, live, answers, fields)
		})
	}
}

func fieldIdentity[M any](t *testing.T, field golem.Field[M]) golem.FieldID {
	t.Helper()
	value, ok := golem.FieldIdentity(field)
	if !ok {
		t.Fatal("a generated field handle carries no identity")
	}
	return value
}

func requireRelationDependency(t *testing.T, tree golemtest.DependencyTree, root golem.ModelID, hop golem.FieldID, targetModel golem.ModelID, child golem.FieldID) {
	t.Helper()
	if tree.ModelID() != root {
		t.Fatal("the dependency tree is not rooted at the classified model")
	}
	for _, entry := range tree.Entries() {
		if entry.FieldID() != hop {
			continue
		}
		if entry.Kind() != golemtest.DependencyRelation {
			t.Fatal("the relation dependency was flattened into a scalar")
		}
		model, relation := entry.TargetModelID()
		if !relation || model != targetModel {
			t.Fatal("the relation dependency lost the target model it must hydrate")
		}
		for _, inner := range entry.Children().Entries() {
			if inner.FieldID() == child && inner.Kind() == golemtest.DependencyScalar {
				return
			}
		}
		t.Fatal("the relation dependency lost the target field its condition reads")
	}
	t.Fatal("the condition lost the relation hop it is decided through")
}

func assertRuntimeAgreesWithKit(t *testing.T, live application, answers staticAnswers, fields []readField) {
	t.Helper()
	ctx := context.Background()

	constraintEquivalent, constraintErr := golemtest.Equivalent(answers.row, declaredRowConstraint(t))
	if constraintErr != nil || !constraintEquivalent {
		t.Fatalf("the kit's row constraint is not the predicate this comparison evaluates: equivalent=%v err=%v", constraintEquivalent, constraintErr)
	}
	authorEquivalent, authorErr := golemtest.Equivalent(answers.authorRow, policy.Authors.Listed.Eq(true))
	if authorErr != nil || !authorEquivalent {
		t.Fatalf("the kit's relation-target row constraint is not the predicate this comparison closes a hop with: equivalent=%v err=%v", authorEquivalent, authorErr)
	}

	visible := systemIdentifiers(t, live, declaredRowConstraint(t))
	if len(visible) != 4 {
		t.Fatalf("the fixture makes %d rows reachable, want 4", len(visible))
	}
	everything := systemIdentifiers(t, live, golem.All[policy.Article]())
	if len(everything) != 5 {
		t.Fatalf("the fixture stores %d rows, want 5", len(everything))
	}

	projection := make([]golem.Selection[policy.Article], 0, len(fields)+1)
	projection = append(projection, policy.Articles.ID)
	for _, field := range fields {
		projection = append(projection, field.selection)
	}
	rows, err := live.caller.Articles.FindMany(ctx,
		policy.Articles.OrderBy(policy.Articles.ID.Asc()),
		policy.Articles.Select(projection...),
	)
	if err != nil {
		t.Fatalf("the Caller refused a projection the kit classified as readable: %v", err)
	}
	returned := rowIdentifiers(t, rows)
	if !sameSet(returned, visible) {
		t.Fatalf("the Caller returned %v while the kit's row constraint admits %v", returned, visible)
	}

	for _, field := range fields {
		classification, present := answers.plan.Field(field.handle)
		if !present {
			t.Fatalf("%s: the kit did not classify a projected field", field.name)
		}
		exposed := exposedIdentifiers(t, rows, field)
		switch classification.Access() {
		case golemtest.AccessAlways:
			if !sameSet(exposed, visible) {
				t.Fatalf("%s: the kit calls the field always readable but the Caller exposed it only on %v of %v", field.name, exposed, visible)
			}
		case golemtest.AccessConditional:
			condition, conditional := classification.Condition()
			if !conditional {
				t.Fatalf("%s: a conditional classification carries no condition", field.name)
			}
			if !field.conditional {
				t.Fatalf("%s: the kit reports a condition for a field the fixture declares unconditional", field.name)
			}
			equivalent, equivalentErr := golemtest.Equivalent(condition, field.declared)
			if equivalentErr != nil || !equivalent {
				t.Fatalf("%s: the kit's condition is not the predicate the runtime comparison uses: equivalent=%v err=%v", field.name, equivalent, equivalentErr)
			}
			expected := intersect(systemIdentifiers(t, live, field.observable), visible)
			if !sameSet(exposed, expected) {
				t.Fatalf("%s: the Caller exposed the field on %v while the kit's condition holds on %v", field.name, exposed, expected)
			}
			assertDischarge(t, field.name, classification.DischargedByConstraint(), visible, exposed)
		case golemtest.AccessNever:
			t.Fatalf("%s: the kit calls the field never readable but the Caller returned it", field.name)
		default:
			t.Fatalf("%s: unknown access %v", field.name, classification.Access())
		}
	}

	t.Run("TheCallerRefusesExactlyTheFieldTheKitCallsNeverReadable", func(t *testing.T) {
		secret, ok := answers.plan.Field(policy.Articles.Secret)
		if !ok || secret.Access() != golemtest.AccessNever {
			t.Fatalf("the kit classified the denied field as %v present=%v", secret.Access(), ok)
		}
		if _, err := live.caller.Articles.FindMany(ctx, policy.Articles.Select(policy.Articles.ID, policy.Articles.Secret)); err == nil {
			t.Fatal("the Caller returned a field the kit classified as never readable")
		}
		if _, err := live.system.Articles.FindMany(ctx, policy.Articles.Select(policy.Articles.ID, policy.Articles.Secret)); err != nil {
			t.Fatalf("the refusal came from the schema rather than from the policy: %v", err)
		}
	})

	t.Run("ANarrowerReachDischargesTheConditionTheCallerWouldOtherwiseMask", func(t *testing.T) {
		classification, ok := answers.reachPlan.Field(policy.Articles.Draft)
		if !ok || classification.Access() != golemtest.AccessConditional {
			t.Fatalf("the kit classified the reached field as %v present=%v", classification.Access(), ok)
		}
		reach := policy.Articles.OwnerID.Eq(identifier(t, aliceText))
		reached := intersect(systemIdentifiers(t, live, reach), visible)
		if len(reached) == 0 || len(reached) == len(visible) {
			t.Fatalf("the fixture reach selects %d of %d rows and proves nothing", len(reached), len(visible))
		}
		reachedRows, reachErr := live.caller.Articles.FindMany(ctx,
			policy.Articles.Where(reach),
			policy.Articles.OrderBy(policy.Articles.ID.Asc()),
			policy.Articles.Select(policy.Articles.ID, policy.Articles.Draft),
		)
		if reachErr != nil {
			t.Fatalf("the Caller refused the narrowed statement: %v", reachErr)
		}
		if identifiers := rowIdentifiers(t, reachedRows); !sameSet(identifiers, reached) {
			t.Fatalf("the narrowed statement returned %v, want %v", identifiers, reached)
		}
		exposed := exposedIdentifiers(t, reachedRows, readField{name: "Draft", column: policy.Articles.Draft})
		assertDischarge(t, "Draft(reach)", classification.DischargedByConstraint(), reached, exposed)
	})

	t.Run("AMissingOrInvisibleRelationTargetDecidesTheConditionItCannotDisclose", func(t *testing.T) {
		unclosed := intersect(systemIdentifiers(t, live, declaredRowConstraint(t).And(policy.Articles.Author.Is(policy.Authors.Verified.Eq(true)))), visible)
		closed := intersect(systemIdentifiers(t, live, declaredRowConstraint(t).And(policy.Articles.Author.Is(readableAuthor()))), visible)
		if len(unclosed) != 2 || len(closed) != 1 {
			t.Fatalf("the fixture no longer separates a hop the actor may follow from one it may not: unclosed=%v closed=%v", unclosed, closed)
		}

		rows, relationErr := live.caller.Articles.FindMany(ctx,
			policy.Articles.OrderBy(policy.Articles.ID.Asc()),
			policy.Articles.Select(policy.Articles.ID, policy.Articles.AuthorID, policy.Articles.Notes,
				policy.Articles.Author.Select(policy.Authors.ID, policy.Authors.Verified, policy.Authors.Listed)),
		)
		if relationErr != nil {
			t.Fatalf("the Caller refused an always-readable relation: %v", relationErr)
		}
		seen := 0
		for _, row := range rows {
			identity, present := golem.Value(row, policy.Articles.ID).Get()
			if !present {
				t.Fatal("a returned row carries no readable identity")
			}
			notes := golem.Value(row, policy.Articles.Notes)
			related := golem.One(row, policy.Articles.Author.ToOne)
			switch identity.String() {
			case ownedPublicText:
				seen++
				if !golem.Value(row, policy.Articles.AuthorID).IsNull() {
					t.Fatal("the fixture row with no relation target carries a target key")
				}
				if related.IsPresent() {
					t.Fatal("a missing relation target was hydrated into the result")
				}
				if notes.IsPresent() {
					t.Fatal("the runtime exposed a field whose condition needs a relation target the row does not have")
				}
			case hiddenAuthorText:
				seen++
				if related.IsPresent() {
					t.Fatal("an unreadable relation target was returned to the caller")
				}
				if notes.IsPresent() {
					t.Fatal("the runtime decided a field condition from a relation target the actor may not read")
				}
			case ownedPrivateText:
				seen++
				if !related.IsPresent() {
					t.Fatal("a readable relation target was withheld from the result")
				}
				if !notes.IsPresent() {
					t.Fatal("the runtime masked a field whose relation hop the actor may follow")
				}
			}
		}
		if seen != 3 {
			t.Fatalf("the fixture returned %d of the 3 relation-target rows", seen)
		}
		authors, authorErr := live.caller.Authors.FindMany(ctx, policy.Authors.Select(policy.Authors.ID))
		if authorErr != nil {
			t.Fatalf("the Caller refused the relation target model: %v", authorErr)
		}
		for _, row := range authors {
			identity, present := golem.Value(row, policy.Authors.ID).Get()
			if present && identity.String() == authorHiddenText {
				t.Fatal("the unlisted relation target is readable in its own right")
			}
		}
	})

	t.Run("AnUnconditionallyGrantedFieldIsNeverMasked", func(t *testing.T) {
		body, ok := answers.commentPlan.Field(policy.Comments.Body)
		if !ok || body.Access() != golemtest.AccessAlways {
			t.Fatalf("the unconditionally granted field classified as %v present=%v", body.Access(), ok)
		}
		if value, constant := answers.commentRow.Constant(); !constant || !value {
			t.Fatalf("the unconditional read grant resolved to value=%v constant=%v", value, constant)
		}
		rows, err := live.caller.Comments.FindMany(ctx, policy.Comments.Select(policy.Comments.ID, policy.Comments.Body))
		if err != nil {
			t.Fatalf("the Caller refused an unconditionally granted projection: %v", err)
		}
		if len(rows) != 5 {
			t.Fatalf("the Caller returned %d rows while the kit admits every row", len(rows))
		}
		for _, row := range rows {
			if !golem.Value(row, policy.Comments.Body).IsPresent() {
				t.Fatal("the Caller masked a field the kit classified as always readable")
			}
		}
	})

	t.Run("AnUnreachableRowIsRefusedRatherThanMasked", func(t *testing.T) {
		if _, err := live.caller.Articles.FindUnique(ctx, policy.Articles.ByID.Value(identifier(t, otherPrivateText))); err == nil {
			t.Fatal("the Caller returned a row the kit's row constraint excludes")
		}
		if _, err := live.system.Articles.FindUnique(ctx, policy.Articles.ByID.Value(identifier(t, otherPrivateText))); err != nil {
			t.Fatalf("the excluded row is absent from storage rather than from the policy: %v", err)
		}
	})
}

func exposedIdentifiers(t *testing.T, rows []golem.Row[policy.Article], field readField) []string {
	t.Helper()
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		identity, present := golem.Value(row, policy.Articles.ID).Get()
		if !present {
			t.Fatal("a returned row carries no readable identity")
		}
		value := golem.Value(row, field.column)
		if !value.IsSelected() {
			t.Fatalf("%s: the Caller did not return a field the statement projected", field.name)
		}
		if value.IsPresent() {
			result = append(result, identity.String())
		}
	}
	sort.Strings(result)
	return result
}

func assertDischarge(t *testing.T, name string, discharged bool, reached, exposed []string) {
	t.Helper()
	if discharged {
		if len(reached) == 0 {
			t.Fatalf("%s: the kit discharged a condition over an empty reach", name)
		}
		if !sameSet(exposed, reached) {
			t.Fatalf("%s: the kit reports the reach discharges the condition but the Caller masked the field on %d of %d reachable rows", name, len(reached)-len(exposed), len(reached))
		}
		return
	}
	if len(exposed) >= len(reached) {
		t.Fatalf("%s: the kit refused to discharge a condition the Caller then satisfied on every one of the %d reachable rows", name, len(reached))
	}
}
