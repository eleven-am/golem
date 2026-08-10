package golem

import (
	"strings"
	"testing"
	"time"
)

type scopedTestPost struct{}
type scopedTestUser struct{}
type scopedTestComment struct{}

var (
	scopedPostModel        = ModelID{1}
	scopedUserModel        = ModelID{2}
	scopedCommentModel     = ModelID{3}
	scopedPostTitle        = FieldID{11}
	scopedPostViews        = FieldID{12}
	scopedPostAuthor       = FieldID{13}
	scopedUserCountry      = FieldID{21}
	scopedPostComments     = FieldID{14}
	scopedAuthorRelation   = RelationID{31}
	scopedCommentsRelation = RelationID{32}
)

func TestP6ScopedIRStructuralAllowlist(t *testing.T) {
	posts := GeneratedScope[scopedTestPost](scopedPostModel)
	author := LeftJoin(posts, GeneratedToOne[scopedTestPost, scopedTestUser](scopedPostAuthor, scopedAuthorRelation, scopedUserModel))
	comments := InnerJoin(posts, GeneratedToMany[scopedTestPost, scopedTestComment](scopedPostComments, scopedCommentsRelation, scopedCommentModel))
	title := GeneratedScopedTextField(posts, GeneratedTextField[scopedTestPost, string](scopedPostTitle))
	views := GeneratedScopedIntegerField(posts, GeneratedOrderedField[scopedTestPost, int64](scopedPostViews))
	country := GeneratedScopedTextField(author, GeneratedTextField[scopedTestUser, string](scopedUserCountry))
	total := views.Sum()
	query := From(posts).Join(comments).Join(author).
		Where(AndScoped(title.StartsWith("go"), views.GT(0))).
		GroupBy(country).
		Having(total.GT(NewExactInteger(10))).
		Select(country, total).
		OrderBy(total.Desc(), country.Asc()).Take(20).Skip(1)
	frozen, err := RuntimeFreezeScopedQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if len(frozen.Joins()) != 2 || len(frozen.Selections()) != 2 || len(frozen.GroupBy()) != 1 || frozen.PredicateNodeCount() != 4 {
		t.Fatalf("unexpected frozen scoped shape: joins=%d selections=%d groups=%d predicates=%d", len(frozen.Joins()), len(frozen.Selections()), len(frozen.GroupBy()), frozen.PredicateNodeCount())
	}
	joins := frozen.Joins()
	joinInventory := map[RelationID]FrozenScopedJoin{}
	for _, join := range joins {
		joinInventory[join.Relation] = join
	}
	if joinInventory[scopedCommentsRelation].Kind != ScopedInnerJoin || joinInventory[scopedCommentsRelation].Cardinality != ScopedRelationToMany || joinInventory[scopedAuthorRelation].Kind != ScopedLeftJoin {
		t.Fatalf("join allowlist changed: %#v", joins)
	}
	for _, expression := range frozen.Selections() {
		if expression.Kind < ScopedExpressionField || expression.Kind > ScopedExpressionMaximum {
			t.Fatalf("expression escaped allowlist: %d", expression.Kind)
		}
	}
}

func TestP6ScopedRuntimeForgeryAndMixedRootCorpusTouchesDatabaseZeroTimes(t *testing.T) {
	postsA := GeneratedScope[scopedTestPost](scopedPostModel)
	postsB := GeneratedScope[scopedTestPost](scopedPostModel)
	titleA := GeneratedScopedTextField(postsA, GeneratedTextField[scopedTestPost, string](scopedPostTitle))
	titleB := GeneratedScopedTextField(postsB, GeneratedTextField[scopedTestPost, string](scopedPostTitle))
	if _, err := RuntimeFreezeScopedQuery(From(postsA).Select(titleB)); err == nil || !strings.Contains(err.Error(), "mixed") {
		t.Fatalf("mixed root was not refused before runtime: %v", err)
	}
	if _, err := RuntimeFreezeScopedQuery(ScopedQuery[scopedTestPost]{}); err == nil {
		t.Fatal("zero query was not refused")
	}
	foreignJoin := InnerJoin(postsB, GeneratedToOne[scopedTestPost, scopedTestUser](scopedPostAuthor, scopedAuthorRelation, scopedUserModel))
	if _, err := RuntimeFreezeScopedQuery(From(postsA).Join(foreignJoin).Select(titleA)); err == nil {
		t.Fatal("foreign join was not refused")
	}
}

func TestP6ScopedLeftJoinMissingAndInvisibleTargetAreIndistinguishable(t *testing.T) {
	posts := GeneratedScope[scopedTestPost](scopedPostModel)
	author := LeftJoin(posts, GeneratedToOne[scopedTestPost, scopedTestUser](scopedPostAuthor, scopedAuthorRelation, scopedUserModel))
	country := GeneratedScopedTextField(author, GeneratedTextField[scopedTestUser, string](scopedUserCountry))
	frozen, err := RuntimeFreezeScopedQuery(From(posts).Join(author).Select(country))
	if err != nil {
		t.Fatal(err)
	}
	expression := frozen.Selections()[0]
	missing := RuntimeScopedRow(RuntimeNullScopedCell(expression))
	invisible := RuntimeScopedRow(RuntimeNullScopedCell(expression))
	if ScopedValue(missing, country).State() != ReadNull || ScopedValue(invisible, country).State() != ReadNull {
		t.Fatal("left target absence and policy invisibility did not share ReadNull")
	}
}

func TestP6ScopedAuditContainsStableInventoryAndFingerprintsOnly(t *testing.T) {
	posts := GeneratedScope[scopedTestPost](scopedPostModel)
	author := LeftJoin(posts, GeneratedToOne[scopedTestPost, scopedTestUser](scopedPostAuthor, scopedAuthorRelation, scopedUserModel))
	country := GeneratedScopedTextField(author, GeneratedTextField[scopedTestUser, string](scopedUserCountry))
	frozen, err := RuntimeFreezeScopedQuery(From(posts).Join(author).Select(country))
	if err != nil {
		t.Fatal(err)
	}
	record := RuntimeScopedAuditRecord(frozen, "audit-user-7", 77, false, SQLite, "SELECT private FROM secret WHERE token = ?", time.Millisecond, 3, ScopedOutcomeSucceeded)
	models := record.Models()
	fields := record.Fields()
	models[0] = ModelID{99}
	fields[0] = FieldID{99}
	if record.Models()[0] != scopedPostModel || record.Fields()[0] == (FieldID{99}) {
		t.Fatal("audit inventories were mutable")
	}
	if record.PrincipalAuditID() != "audit-user-7" || record.ExecutionID() != 77 || record.RowCount() != 3 || record.Outcome() != ScopedOutcomeSucceeded {
		t.Fatalf("audit metadata changed: %#v", record)
	}
	if record.ShapeFingerprint() == (SchemaDigest{}) || record.SQLFingerprint() == (SchemaDigest{}) {
		t.Fatal("audit fingerprints are missing")
	}
}

func TestP6ScopedAuditShapeExcludesValuesButIncludesSignedPaging(t *testing.T) {
	freeze := func(operand string, take, skip int) FrozenScopedQuery {
		posts := GeneratedScope[scopedTestPost](scopedPostModel)
		title := GeneratedScopedTextField(posts, GeneratedTextField[scopedTestPost, string](scopedPostTitle))
		value, err := RuntimeFreezeScopedQuery(From(posts).Where(title.Eq(operand)).Select(title).Take(take).Skip(skip))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	shape := func(query FrozenScopedQuery) SchemaDigest {
		return RuntimeScopedAuditRecord(query, "p", 1, false, SQLite, "sql", time.Second, 1, ScopedOutcomeSucceeded).ShapeFingerprint()
	}
	if shape(freeze("secret-a", -2, 1)) != shape(freeze("secret-b", -2, 1)) {
		t.Fatal("audit shape leaked predicate values")
	}
	if shape(freeze("secret-a", -2, 1)) == shape(freeze("secret-a", 2, 1)) {
		t.Fatal("audit shape omitted take sign")
	}
	if shape(freeze("secret-a", 2, 1)) == shape(freeze("secret-a", 2, 2)) {
		t.Fatal("audit shape omitted skip value")
	}
}

func TestP8ScopedAuditForUnfreezableInputDoesNotInventZeroModel(t *testing.T) {
	record := RuntimeScopedAuditRecord(FrozenScopedQuery{}, "audit-user", 1, false, SQLite, "", 0, 0, ScopedOutcomeRefused)
	if models := record.Models(); len(models) != 0 {
		t.Fatalf("unfreezable scoped audit models=%v want empty", models)
	}
}
