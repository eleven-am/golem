package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"example.com/golempolicykit/policy"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/queryplan"
)

const queryPlanBindCanary = "query-plan-bind-canary-that-must-not-escape"

func TestExternalGeneratedApplicationQueryPlanIsCallerOnlyTypedAndRedacted(t *testing.T) {
	for _, value := range targets(t) {
		value := value
		t.Run(value.name, func(t *testing.T) {
			database := value.open(t)
			database.UnsafeSQLX().SetMaxOpenConns(1)
			database.UnsafeSQLX().SetMaxIdleConns(1)
			defer func() {
				if err := database.Close(); err != nil {
					t.Errorf("close database: %v", err)
				}
			}()

			live := openApplication(t, database)
			seed(t, live)
			assertGeneratedQueryPlanSurface(t, live)

			before := queryPlanDataSnapshot(t, live)
			reports := explainEveryGeneratedQueryPlan(t, live)
			after := queryPlanDataSnapshot(t, live)
			if before != after {
				t.Fatalf("Explain changed application data:\nbefore=%s\nafter=%s", before, after)
			}

			for _, report := range reports {
				assertExternalQueryPlanReport(t, report.name, report.value, database.Provider(), report.operation, report.purpose)
			}
		})
	}
}

type externalQueryPlanReport struct {
	name      string
	value     queryplan.Report
	operation queryplan.Operation
	purpose   queryplan.StatementPurpose
}

func explainEveryGeneratedQueryPlan(t *testing.T, live application) []externalQueryPlanReport {
	t.Helper()
	ctx := context.Background()
	where := policy.Articles.Title.Contains(queryPlanBindCanary)
	readOptions := []golem.ReadOption[policy.Article]{
		policy.Articles.Where(where),
		policy.Articles.Select(policy.Articles.ID),
	}
	uniqueOptions := []golem.ReadOption[policy.Article]{policy.Articles.Select(policy.Articles.ID)}
	countOptions := []golem.ReadOption[policy.Article]{policy.Articles.Where(where)}
	count, views := policy.Articles.CountAll(), policy.Articles.Views.Sum()
	public := policy.Articles.Public.Dimension()
	authorName := policy.Articles.AuthorName
	aggregate := policy.Articles.Aggregate(
		policy.Articles.AggregateWhere(where),
		policy.Articles.AggregateSelect(count, views),
	)
	group := policy.Articles.GroupBy(
		policy.Articles.GroupDimensions(public),
		policy.Articles.GroupMeasures(count, views),
		policy.Articles.GroupWhere(where),
		policy.Articles.GroupOrderBy(public.Asc()),
		policy.Articles.GroupTake(10),
	)
	relationGroup := policy.Articles.RelationGroupBy(
		policy.Articles.RelationGroupDimensions(authorName),
		policy.Articles.RelationGroupMeasures(count, views),
		policy.Articles.RelationGroupWhere(where),
		policy.Articles.RelationGroupOrderBy(authorName.Asc()),
		policy.Articles.RelationGroupTake(10),
	)
	articles := policy.Articles.Scope()
	authors := golem.InnerJoin(articles, policy.Articles.Author)
	scoped := golem.From(articles).
		Join(authors).
		Select(policy.Articles.Title.At(articles), policy.Authors.Name.At(authors)).
		Take(10)

	calls := []struct {
		name      string
		operation queryplan.Operation
		purpose   queryplan.StatementPurpose
		call      func() (queryplan.Report, error)
	}{
		{name: "find-many", operation: queryplan.OperationFindMany, purpose: queryplan.StatementPurposeRoot, call: func() (queryplan.Report, error) {
			return live.caller.Articles.ExplainFindMany(ctx, readOptions...)
		}},
		{name: "find-first", operation: queryplan.OperationFindFirst, purpose: queryplan.StatementPurposeRoot, call: func() (queryplan.Report, error) {
			return live.caller.Articles.ExplainFindFirst(ctx, readOptions...)
		}},
		{name: "find-unique", operation: queryplan.OperationFindUnique, purpose: queryplan.StatementPurposeRoot, call: func() (queryplan.Report, error) {
			return live.caller.Articles.ExplainFindUnique(ctx, policy.Articles.ByID.Value(identifier(t, ownedPrivateText)), uniqueOptions...)
		}},
		{name: "count", operation: queryplan.OperationCount, purpose: queryplan.StatementPurposeRoot, call: func() (queryplan.Report, error) {
			return live.caller.Articles.ExplainCount(ctx, countOptions...)
		}},
		{name: "aggregate", operation: queryplan.OperationAggregate, purpose: queryplan.StatementPurposeAnalytics, call: func() (queryplan.Report, error) {
			return live.caller.Articles.ExplainAggregate(ctx, aggregate)
		}},
		{name: "group-by", operation: queryplan.OperationGroupBy, purpose: queryplan.StatementPurposeAnalytics, call: func() (queryplan.Report, error) {
			return live.caller.Articles.ExplainGroupBy(ctx, group)
		}},
		{name: "relation-group-by", operation: queryplan.OperationRelationGroupBy, purpose: queryplan.StatementPurposeAnalytics, call: func() (queryplan.Report, error) {
			return live.caller.Articles.ExplainRelationGroupBy(ctx, relationGroup)
		}},
		{name: "scoped", operation: queryplan.OperationScoped, purpose: queryplan.StatementPurposeScoped, call: func() (queryplan.Report, error) {
			return live.caller.Articles.ExplainScoped(ctx, scoped)
		}},
	}

	result := make([]externalQueryPlanReport, 0, len(calls))
	for _, candidate := range calls {
		report, err := candidate.call()
		if err != nil {
			t.Fatalf("Explain %s: %v", candidate.name, err)
		}
		assertQueryPlanConnectionReleased(t, live)
		result = append(result, externalQueryPlanReport{
			name: candidate.name, value: report, operation: candidate.operation, purpose: candidate.purpose,
		})
	}
	return result
}

func assertGeneratedQueryPlanSurface(t *testing.T, live application) {
	t.Helper()
	want := []string{
		"ExplainAggregate", "ExplainCount", "ExplainFindFirst", "ExplainFindMany",
		"ExplainFindUnique", "ExplainGroupBy", "ExplainRelationGroupBy", "ExplainScoped",
	}
	if got := explainMethodNames(reflect.TypeOf(live.caller.Articles)); !reflect.DeepEqual(got, want) {
		t.Fatalf("Caller Article Explain methods=%v want=%v", got, want)
	}
	if got := explainMethodNames(reflect.TypeOf(live.system.Articles)); len(got) != 0 {
		t.Fatalf("System Article unexpectedly exposes Explain methods %v", got)
	}
	for _, candidate := range []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "CallerTx", typeOf: reflect.TypeOf((*policy.CallerTx[policy.Actor])(nil)).Elem()},
		{name: "SystemTx", typeOf: reflect.TypeOf((*policy.SystemTx[policy.Actor])(nil)).Elem()},
	} {
		field, ok := candidate.typeOf.FieldByName("Articles")
		if !ok {
			t.Fatalf("generated %s has no Articles client", candidate.name)
		}
		if got := explainMethodNames(field.Type); len(got) != 0 {
			t.Fatalf("%s Article unexpectedly exposes Explain methods %v", candidate.name, got)
		}
	}
}

func explainMethodNames(value reflect.Type) []string {
	result := make([]string, 0)
	for index := 0; index < value.NumMethod(); index++ {
		name := value.Method(index).Name
		if strings.HasPrefix(name, "Explain") {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func assertExternalQueryPlanReport(t *testing.T, name string, report queryplan.Report, provider golem.Provider, operation queryplan.Operation, purpose queryplan.StatementPurpose) {
	t.Helper()
	if report.FormatVersion() != 1 || report.Provider() != provider || report.Operation() != operation {
		t.Fatalf("%s report header=(%d,%q,%q), want (1,%q,%q)", name, report.FormatVersion(), report.Provider(), report.Operation(), provider, operation)
	}
	if report.RootModelID() != policy.GolemGeneratedArticleDescriptor.Metadata().ModelID() {
		t.Fatalf("%s report has the wrong root model", name)
	}
	if report.CanonicalDigest() == ([32]byte{}) {
		t.Fatalf("%s report has a zero canonical digest", name)
	}
	if report.MinimumExecutionStatements() == 0 || report.MinimumExecutionStatements() > report.MaximumExecutionStatements() {
		t.Fatalf("%s report bounds=%d..%d", name, report.MinimumExecutionStatements(), report.MaximumExecutionStatements())
	}
	statements := report.Statements()
	if len(statements) == 0 || statements[0].Purpose() != purpose {
		t.Fatalf("%s statements=%d primary purpose=%q want=%q", name, len(statements), firstPurpose(statements), purpose)
	}
	for index, statement := range statements {
		if statement.Ordinal() != uint32(index) {
			t.Fatalf("%s statement %d has ordinal %d", name, index, statement.Ordinal())
		}
		if index > 0 && statement.Purpose() != queryplan.StatementPurposeRelationBatch && statement.Purpose() != queryplan.StatementPurposePolicyHydration {
			t.Fatalf("%s statement %d has invalid secondary purpose %q", name, index, statement.Purpose())
		}
		assertExternalQueryPlanNode(t, name, statement.Root())
	}
	for _, warning := range report.Warnings() {
		if !validExternalQueryPlanWarning(warning) {
			t.Fatalf("%s report has an open warning %q", name, warning)
		}
	}
	if _, ok := any(report).(json.Marshaler); ok {
		t.Fatalf("%s report unexpectedly exposes JSON wire authority", name)
	}
	assertExternalQueryPlanRedacted(t, name, report)
	assertExternalQueryPlanImmutable(t, name, report)
}

func firstPurpose(statements []queryplan.Statement) queryplan.StatementPurpose {
	if len(statements) == 0 {
		return ""
	}
	return statements[0].Purpose()
}

func assertExternalQueryPlanNode(t *testing.T, name string, node queryplan.Node) {
	t.Helper()
	validKinds := map[queryplan.NodeKind]bool{
		queryplan.NodeKindAccess: true, queryplan.NodeKindJoin: true, queryplan.NodeKindSort: true,
		queryplan.NodeKindAggregate: true, queryplan.NodeKindMaterialize: true,
		queryplan.NodeKindCorrelatedRelation: true, queryplan.NodeKindDeferredBatch: true,
		queryplan.NodeKindConstant: true, queryplan.NodeKindUnknown: true,
	}
	validAccess := map[queryplan.AccessKind]bool{
		queryplan.AccessKindNone: true, queryplan.AccessKindPrimaryKey: true, queryplan.AccessKindUniqueIndex: true,
		queryplan.AccessKindIndex: true, queryplan.AccessKindBitmapIndex: true, queryplan.AccessKindFullScan: true,
		queryplan.AccessKindConstant: true, queryplan.AccessKindUnknown: true,
	}
	if !validKinds[node.Kind()] || !validAccess[node.Access()] {
		t.Fatalf("%s report has an open node fact kind=%q access=%q", name, node.Kind(), node.Access())
	}
	if model, ok := node.ModelID(); ok && model == (golem.ModelID{}) {
		t.Fatalf("%s report claims a zero model identity", name)
	}
	if relation, ok := node.RelationID(); ok && relation == (golem.RelationID{}) {
		t.Fatalf("%s report claims a zero relation identity", name)
	}
	if index, ok := node.IndexID(); ok && index == (queryplan.IndexID{}) {
		t.Fatalf("%s report claims a zero index identity", name)
	}
	seenFields := make(map[golem.FieldID]bool)
	for _, field := range node.FieldIDs() {
		if field == (golem.FieldID{}) || seenFields[field] {
			t.Fatalf("%s report has a zero or duplicate field identity", name)
		}
		seenFields[field] = true
	}
	if capacity, ok := node.BatchCapacity(); ok {
		minimum, minimumOK := node.MinimumExecutionStatements()
		maximum, maximumOK := node.MaximumExecutionStatements()
		if !minimumOK || !maximumOK || capacity == 0 || minimum > maximum {
			t.Fatalf("%s report has invalid batch facts capacity=%d bounds=%d..%d", name, capacity, minimum, maximum)
		}
	}
	for _, warning := range node.Warnings() {
		if !validExternalQueryPlanWarning(warning) {
			t.Fatalf("%s report node has an open warning %q", name, warning)
		}
	}
	for _, child := range node.Children() {
		assertExternalQueryPlanNode(t, name, child)
	}
}

func validExternalQueryPlanWarning(value queryplan.Warning) bool {
	switch value {
	case queryplan.WarningFullScan, queryplan.WarningTemporarySort, queryplan.WarningMaterialization,
		queryplan.WarningDeferredBatch, queryplan.WarningMultiStatement, queryplan.WarningUnknownProviderNode:
		return true
	default:
		return false
	}
}

func assertExternalQueryPlanRedacted(t *testing.T, name string, report queryplan.Report) {
	t.Helper()
	encoded := fmt.Sprintf("%#v", report)
	for _, forbidden := range []string{
		queryPlanBindCanary, "articles", "authors", "owner_id", "author_id",
		ownedPrivateText, "title-" + ownedPrivateText, "secret-" + ownedPrivateText,
	} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("%s report leaked %q: %s", name, forbidden, encoded)
		}
	}
}

func assertExternalQueryPlanImmutable(t *testing.T, name string, report queryplan.Report) {
	t.Helper()
	before := fmt.Sprintf("%#v", report)
	statements := report.Statements()
	if len(statements) != 0 {
		root := statements[0].Root()
		fields := root.FieldIDs()
		if len(fields) != 0 {
			fields[0] = golem.FieldID{}
		}
		children := root.Children()
		if len(children) != 0 {
			children[0] = queryplan.Node{}
		}
		statements[0] = queryplan.Statement{}
	}
	warnings := report.Warnings()
	if len(warnings) != 0 {
		warnings[0] = "mutated"
	}
	if after := fmt.Sprintf("%#v", report); after != before {
		t.Fatalf("%s report aliases accessor storage", name)
	}
}

func assertQueryPlanConnectionReleased(t *testing.T, live application) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := live.database.UnsafeSQLX().PingContext(ctx); err != nil {
		t.Fatalf("planning connection was not released: %v", err)
	}
}

func queryPlanDataSnapshot(t *testing.T, live application) string {
	t.Helper()
	// This public external gate proves that Explain leaves application data
	// unchanged. The exact proof that the provider never issues the data SELECT
	// belongs to the recorder-backed runtime/provider gates; Database exposes no
	// public statement tracer that could honestly duplicate that authority here.
	return strings.Join([]string{
		"authors:\n" + queryPlanAuthorSnapshot(t, live),
		"articles:\n" + queryPlanArticleSnapshot(t, live),
		"comments:\n" + queryPlanCommentSnapshot(t, live),
	}, "\n")
}

func queryPlanArticleSnapshot(t *testing.T, live application) string {
	t.Helper()
	rows, err := live.system.Articles.FindMany(context.Background(),
		policy.Articles.OrderBy(policy.Articles.ID.Asc()),
		policy.Articles.Select(
			policy.Articles.ID, policy.Articles.OwnerID, policy.Articles.AuthorID,
			policy.Articles.Public, policy.Articles.Views, policy.Articles.Title,
			policy.Articles.Draft, policy.Articles.Notes, policy.Articles.Summary, policy.Articles.Secret,
		),
	)
	if err != nil {
		t.Fatalf("snapshot articles: %v", err)
	}
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		id, idOK := golem.Value(row, policy.Articles.ID).Get()
		owner, ownerOK := golem.Value(row, policy.Articles.OwnerID).Get()
		authorValue := golem.Value(row, policy.Articles.AuthorID)
		author := "null"
		if !authorValue.IsSelected() {
			t.Fatal("snapshot Article.AuthorID is not selected")
		}
		if value, present := authorValue.Get(); present {
			author = value.String()
		} else if !authorValue.IsNull() {
			t.Fatal("snapshot Article.AuthorID has neither a value nor null state")
		}
		public, publicOK := golem.Value(row, policy.Articles.Public).Get()
		views, viewsOK := golem.Value(row, policy.Articles.Views).Get()
		title, titleOK := golem.Value(row, policy.Articles.Title).Get()
		draft, draftOK := golem.Value(row, policy.Articles.Draft).Get()
		notes, notesOK := golem.Value(row, policy.Articles.Notes).Get()
		summary, summaryOK := golem.Value(row, policy.Articles.Summary).Get()
		secret, secretOK := golem.Value(row, policy.Articles.Secret).Get()
		if !idOK || !ownerOK || !publicOK || !viewsOK || !titleOK || !draftOK || !notesOK || !summaryOK || !secretOK {
			t.Fatal("snapshot projection is incomplete")
		}
		result = append(result, fmt.Sprintf("%s|%s|%s|%t|%d|%q|%q|%q|%q|%q",
			id.String(), owner.String(), author, public, views, title, draft, notes, summary, secret))
	}
	return strings.Join(result, "\n")
}

func queryPlanAuthorSnapshot(t *testing.T, live application) string {
	t.Helper()
	rows, err := live.system.Authors.FindMany(context.Background(),
		policy.Authors.OrderBy(policy.Authors.ID.Asc()),
		policy.Authors.Select(policy.Authors.ID, policy.Authors.Name, policy.Authors.Verified, policy.Authors.Listed),
	)
	if err != nil {
		t.Fatalf("snapshot authors: %v", err)
	}
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		id, idOK := golem.Value(row, policy.Authors.ID).Get()
		name, nameOK := golem.Value(row, policy.Authors.Name).Get()
		verified, verifiedOK := golem.Value(row, policy.Authors.Verified).Get()
		listed, listedOK := golem.Value(row, policy.Authors.Listed).Get()
		if !idOK || !nameOK || !verifiedOK || !listedOK {
			t.Fatal("snapshot Author projection is incomplete")
		}
		result = append(result, fmt.Sprintf("%s|%q|%t|%t", id.String(), name, verified, listed))
	}
	return strings.Join(result, "\n")
}

func queryPlanCommentSnapshot(t *testing.T, live application) string {
	t.Helper()
	rows, err := live.system.Comments.FindMany(context.Background(),
		policy.Comments.OrderBy(policy.Comments.ID.Asc()),
		policy.Comments.Select(policy.Comments.ID, policy.Comments.ArticleID, policy.Comments.Body, policy.Comments.Approved),
	)
	if err != nil {
		t.Fatalf("snapshot comments: %v", err)
	}
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		id, idOK := golem.Value(row, policy.Comments.ID).Get()
		article, articleOK := golem.Value(row, policy.Comments.ArticleID).Get()
		body, bodyOK := golem.Value(row, policy.Comments.Body).Get()
		approved, approvedOK := golem.Value(row, policy.Comments.Approved).Get()
		if !idOK || !articleOK || !bodyOK || !approvedOK {
			t.Fatal("snapshot Comment projection is incomplete")
		}
		result = append(result, fmt.Sprintf("%s|%s|%q|%t", id.String(), article.String(), body, approved))
	}
	return strings.Join(result, "\n")
}
