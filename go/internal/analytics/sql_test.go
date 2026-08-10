package analytics

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type analyticsRendererPost struct{}

func TestP6ProviderExactRendererQualifiesCollatesClassifiesTiesAndReverses(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	titleField := golem.GeneratedOrderedField[analyticsRendererPost, string](fixture.PostTitle)
	bigField := golem.GeneratedOrderedField[analyticsRendererPost, int64](fixture.PostBigInt)
	decimalField := golem.GeneratedOrderedField[analyticsRendererPost, golem.Decimal](fixture.PostDecimal)
	title := golem.GeneratedDimension[analyticsRendererPost](fixture.Post, titleField, true)
	privateSum := golem.GeneratedMeasure[analyticsRendererPost, int64, golem.ExactInteger](fixture.Post, bigField, golem.AggregateSum)
	decimalAverage := golem.GeneratedMeasure[analyticsRendererPost, golem.Decimal, golem.ExactDecimal](fixture.Post, decimalField, golem.AggregateAverage)
	request := golem.GeneratedGroupBy(fixture.Post,
		golem.GeneratedGroupDimensions[analyticsRendererPost](title),
		golem.GeneratedGroupMeasures[analyticsRendererPost](decimalAverage),
		golem.GeneratedGroupHaving(privateSum.GT(golem.NewExactInteger(9))),
		golem.GeneratedGroupOrderBy(privateSum.Asc()),
		golem.GeneratedGroupTake[analyticsRendererPost](-2),
		golem.GeneratedGroupSkip[analyticsRendererPost](1),
	)
	frozen, err := golem.RuntimeFreezeGroupRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := planned.Field(fixture.PostBigInt); !ok {
		t.Fatal("private having/order measure was not included in planning/classification")
	}

	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		t.Run(fmt.Sprint(provider), func(t *testing.T) {
			proof, proofErr := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
			if proofErr != nil {
				t.Fatal(proofErr)
			}
			first, renderErr := Render(planned, fixture.Registry, provider, proof, RenderOptions{MaxResultRows: 7})
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			second, renderErr := Render(planned, fixture.Registry, provider, proof, RenderOptions{MaxResultRows: 7})
			if renderErr != nil || first.SQL() != second.SQL() {
				t.Fatalf("nondeterministic SQL: %v\nfirst=%s\nsecond=%s", renderErr, first.SQL(), second.SQL())
			}
			sql := first.SQL()
			if !strings.Contains(sql, `FROM (SELECT`) || !strings.Contains(sql, `AS "golem_page" ORDER BY`) {
				t.Fatalf("negative take was not selected in reverse SQL and restored by an outer query: %s", sql)
			}
			if !strings.Contains(sql, `"golem_page"."golem_o0"`) || !strings.Contains(sql, `"golem_page"."golem_d0"`) {
				t.Fatalf("private requested order or complete dimension tie is absent: %s", sql)
			}
			if got := first.Args(); len(got) != 3 || got[0] != "9" || got[1] != int64(2) || got[2] != int64(1) {
				t.Fatalf("args=%#v", got)
			}
			if provider == policyir.ProviderPostgreSQL {
				for _, fragment := range []string{`FROM "public"."posts"`, `COLLATE "C"`, `SUM(`, `::text`, `CAST(`, `AS NUMERIC)`, `ROUND(AVG(`} {
					if !strings.Contains(sql, fragment) {
						t.Errorf("PostgreSQL SQL lacks %q: %s", fragment, sql)
					}
				}
			} else {
				for _, fragment := range []string{`FROM "posts"`, `COLLATE BINARY`, `golem_analytics_integer_sum_v1`, `golem_analytics_decimal_avg_v1`, `golem_analytics_numeric_compare_v1`, `COLLATE golem_analytics_numeric_v1`} {
					if !strings.Contains(sql, fragment) {
						t.Errorf("SQLite SQL lacks %q: %s", fragment, sql)
					}
				}
			}
		})
	}
}

func TestP6MissingTakeUsesVisiblePlusOneResultGuard(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	titleField := golem.GeneratedOrderedField[analyticsRendererPost, string](fixture.PostTitle)
	title := golem.GeneratedDimension[analyticsRendererPost](fixture.Post, titleField, true)
	request := golem.GeneratedGroupBy(fixture.Post, golem.GeneratedGroupDimensions[analyticsRendererPost](title))
	frozen, err := golem.RuntimeFreezeGroupRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := policysql.NewCapabilityProof(policyir.ProviderSQLite, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	statement, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof, RenderOptions{MaxResultRows: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statement.SQL(), "LIMIT ?") || len(statement.Args()) != 1 || statement.Args()[0] != int64(8) {
		t.Fatalf("SQL=%s args=%#v", statement.SQL(), statement.Args())
	}
	if statement.ResultOverflow(7) || !statement.ResultOverflow(8) {
		t.Fatal("result overflow sentinel boundary is not visible")
	}
}

func TestP6ContributionOverflowSentinelSurvivesEmptyHavingResult(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	titleField := golem.GeneratedOrderedField[analyticsRendererPost, string](fixture.PostTitle)
	title := golem.GeneratedDimension[analyticsRendererPost](fixture.Post, titleField, true)
	count := golem.GeneratedCountAll[analyticsRendererPost](fixture.Post)
	request := golem.GeneratedGroupBy(fixture.Post,
		golem.GeneratedGroupDimensions[analyticsRendererPost](title),
		golem.GeneratedGroupHaving(count.GT(100)),
	)
	frozen, err := golem.RuntimeFreezeGroupRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := policysql.NewCapabilityProof(policyir.ProviderSQLite, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	statement, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof, RenderOptions{MaxContributionRows: 2, MaxIntermediateGroups: 10, MaxResultRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"WITH \"golem_contributions\" AS MATERIALIZED", "\"golem_groups\" AS MATERIALIZED", "LIMIT ?1", "LIMIT ?2", "COUNT(*) OVER ()", "UNION ALL", "golem_guard_row"} {
		if !strings.Contains(statement.SQL(), fragment) {
			t.Fatalf("guard SQL lacks %q: %s", fragment, statement.SQL())
		}
	}

	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := provider.ApplyInitial(context.Background(), database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO "users"("id","name") VALUES ('00000000-0000-0000-0000-000000000001','owner')`); err != nil {
		t.Fatal(err)
	}
	for index, titleValue := range []string{"a", "b", "c"} {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", index+10)
		if _, err := database.Exec(`INSERT INTO "posts"("id","author_id","title","big_int","decimal_value") VALUES (?,?,?,?,?)`, id, "00000000-0000-0000-0000-000000000001", titleValue, int64(index+1), int64((index+1)*100)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := database.Queryx(statement.SQL(), statement.Args()...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	rowCount := 0
	for rows.Next() {
		rowCount++
		var dimension any
		var contributions, intermediate, marker int64
		if err := rows.Scan(&dimension, &contributions, &intermediate, &marker); err != nil {
			t.Fatal(err)
		}
		if dimension != nil || contributions != 3 || intermediate != 3 || marker != 1 {
			t.Fatalf("dimension=%#v contributions=%d intermediate=%d marker=%d", dimension, contributions, intermediate, marker)
		}
		if detail := statement.LimitOverflow(contributions, intermediate, 0); detail != "analytics contribution limit exceeded" {
			t.Fatalf("overflow detail=%q", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("rows=%d, want exactly one private guard row", rowCount)
	}
}

func TestP6IntermediateGuardPrecedesHavingAndFinalCapFollowsHaving(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	titleField := golem.GeneratedOrderedField[analyticsRendererPost, string](fixture.PostTitle)
	title := golem.GeneratedDimension[analyticsRendererPost](fixture.Post, titleField, true)
	request := golem.GeneratedGroupBy(fixture.Post,
		golem.GeneratedGroupDimensions[analyticsRendererPost](title),
		golem.GeneratedGroupHaving(title.Eq("c")),
	)
	frozen, err := golem.RuntimeFreezeGroupRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := policysql.NewCapabilityProof(policyir.ProviderSQLite, [32]byte(fixture.Registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}

	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "having-limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := provider.ApplyInitial(context.Background(), database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO "users"("id","name") VALUES ('00000000-0000-0000-0000-000000000001','owner')`); err != nil {
		t.Fatal(err)
	}
	for index, titleValue := range []string{"a", "b", "c"} {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", index+20)
		if _, err := database.Exec(`INSERT INTO "posts"("id","author_id","title","big_int","decimal_value") VALUES (?,?,?,?,?)`, id, "00000000-0000-0000-0000-000000000001", titleValue, int64(index+1), int64((index+1)*100)); err != nil {
			t.Fatal(err)
		}
	}

	readGuarded := func(t *testing.T, statement Statement) (visible []string, contributions, intermediate int64) {
		t.Helper()
		rows, queryErr := database.Queryx(statement.SQL(), statement.Args()...)
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		defer rows.Close()
		for rows.Next() {
			var value *string
			var marker int64
			if scanErr := rows.Scan(&value, &contributions, &intermediate, &marker); scanErr != nil {
				t.Fatal(scanErr)
			}
			if marker == 0 {
				if value == nil {
					t.Fatal("visible analytical row has a null dimension")
				}
				visible = append(visible, *value)
			}
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			t.Fatal(rowsErr)
		}
		return visible, contributions, intermediate
	}

	// The final result cap is a plus-one probe over the complete post-HAVING
	// result. It must not be substituted for the larger intermediate cap.
	final, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof, RenderOptions{MaxContributionRows: 10, MaxIntermediateGroups: 10, MaxResultRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	visible, contributions, intermediate := readGuarded(t, final)
	if len(visible) != 1 || visible[0] != "c" || contributions != 3 || intermediate != 3 || final.LimitOverflow(contributions, intermediate, len(visible)) != "" {
		t.Fatalf("post-HAVING final cap visible=%#v contributions=%d intermediate=%d overflow=%q", visible, contributions, intermediate, final.LimitOverflow(contributions, intermediate, len(visible)))
	}

	// The intermediate guard counts pre-HAVING groups. Although HAVING leaves
	// one visible row, three groups existed and a limit of two must refuse.
	preHaving, err := Render(planned, fixture.Registry, policyir.ProviderSQLite, proof, RenderOptions{MaxContributionRows: 10, MaxIntermediateGroups: 2, MaxResultRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	visible, contributions, intermediate = readGuarded(t, preHaving)
	if len(visible) != 1 || visible[0] != "c" || contributions != 3 || intermediate != 3 || preHaving.LimitOverflow(contributions, intermediate, len(visible)) != "analytics intermediate group limit exceeded" {
		t.Fatalf("pre-HAVING guard visible=%#v contributions=%d intermediate=%d overflow=%q", visible, contributions, intermediate, preHaving.LimitOverflow(contributions, intermediate, len(visible)))
	}
}

func TestP6RelationCorrelationDischargeCarriesOnlyProvedExactEquality(t *testing.T) {
	fixture := schematest.NewIndexedExact(t)
	endpoint, ok := fixture.Registry.ForwardToOneRelation(fixture.Post, fixture.Authorship)
	if !ok {
		t.Fatal("post author endpoint is absent")
	}
	uuidType, err := policyir.NewTypeRef(policyir.ValueUUID, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	uuidOperand, err := policyir.OneOperand(policyir.UUIDValue([16]byte{15: 7}))
	if err != nil {
		t.Fatal(err)
	}
	authorEquality, err := policyir.NewScalar(policyir.ModelID(fixture.Post), policyir.FieldID(fixture.AuthorID), uuidType, policyir.OperatorEqual, policyir.ComparisonSensitive, uuidOperand, nil)
	if err != nil {
		t.Fatal(err)
	}
	inferred, present, err := inferCorrelatedEqualities(authorEquality, endpoint)
	if err != nil || !present {
		t.Fatalf("exact equality inference present=%t error=%v", present, err)
	}
	if inferred.ModelID() != policyir.ModelID(fixture.User) {
		t.Fatalf("inferred model=%x", inferred.ModelID())
	}
	if field, scalar := inferred.Field(); !scalar || field != policyir.FieldID(fixture.UserID) {
		t.Fatalf("inferred child field=%x scalar=%t", field, scalar)
	}

	stringType, err := policyir.NewTypeRef(policyir.ValueString, false, 0, 0, policyir.EnumID{}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	titleOperand, err := policyir.OneOperand(mustAnalyticsStringValue(t, "public"))
	if err != nil {
		t.Fatal(err)
	}
	titleEquality, err := policyir.NewScalar(policyir.ModelID(fixture.Post), policyir.FieldID(fixture.PostTitle), stringType, policyir.OperatorEqual, policyir.ComparisonSensitive, titleOperand, nil)
	if err != nil {
		t.Fatal(err)
	}
	nearMiss, err := policyir.NewLogical(policyir.ModelID(fixture.Post), policyir.LogicalOr, []policyir.Condition{authorEquality, titleEquality})
	if err != nil {
		t.Fatal(err)
	}
	if _, present, err := inferCorrelatedEqualities(nearMiss, endpoint); err != nil || present {
		t.Fatalf("one OR branch incorrectly discharged relation field: present=%t error=%v", present, err)
	}
}

func TestP6ForgedUngroupableDimensionsAndFieldCountsFailInPlanning(t *testing.T) {
	fixture := schematest.NewMutationExactValues(t)
	bytesField := golem.GeneratedBytesField[analyticsRendererPost](fixture.PostBytes)
	jsonField := golem.GeneratedJSONField[analyticsRendererPost](fixture.PostJSON)
	listField := golem.GeneratedListField[analyticsRendererPost, string](fixture.PostList)
	tests := []struct {
		name      string
		dimension golem.LocalGroupDimension[analyticsRendererPost]
		count     golem.AggregateMeasure[analyticsRendererPost]
	}{
		{
			name:      "bytes",
			dimension: golem.GeneratedDimension[analyticsRendererPost](fixture.Post, bytesField, false),
			count:     golem.GeneratedMeasure[analyticsRendererPost, []byte, int64](fixture.Post, bytesField, golem.AggregateCountField),
		},
		{
			name:      "json",
			dimension: golem.GeneratedDimension[analyticsRendererPost](fixture.Post, jsonField, false),
			count:     golem.GeneratedMeasure[analyticsRendererPost, golem.JSON[any], int64](fixture.Post, jsonField, golem.AggregateCountField),
		},
		{
			name:      "scalar list",
			dimension: golem.GeneratedDimension[analyticsRendererPost](fixture.Post, listField, false),
			count:     golem.GeneratedMeasure[analyticsRendererPost, golem.List[string], int64](fixture.Post, listField, golem.AggregateCountField),
		},
	}
	for _, test := range tests {
		t.Run(test.name+" dimension", func(t *testing.T) {
			request := golem.GeneratedGroupBy(fixture.Post, golem.GeneratedGroupDimensions[analyticsRendererPost](test.dimension))
			frozen, err := golem.RuntimeFreezeGroupRequest(request)
			if err != nil {
				t.Fatalf("forged request did not reach planner: %v", err)
			}
			if _, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits()); err == nil || !strings.Contains(err.Error(), "not a dimension") {
				t.Fatalf("planner error=%v", err)
			}
		})
		t.Run(test.name+" field count", func(t *testing.T) {
			request := golem.GeneratedAggregate(fixture.Post, golem.GeneratedAggregateSelect[analyticsRendererPost](test.count))
			frozen, err := golem.RuntimeFreezeAggregateRequest(request)
			if err != nil {
				t.Fatalf("forged request did not reach planner: %v", err)
			}
			if _, err := System(frozen, fixture.Registry, policyir.PortableProviders(), readplan.DefaultLimits()); err == nil || !strings.Contains(err.Error(), "does not support field-count") {
				t.Fatalf("planner error=%v", err)
			}
		})
	}
}

func TestP8SQLiteMultiPolicyFragmentsUseDisjointNumberedPlaceholders(t *testing.T) {
	join := rebase(`join_a=?1 AND join_b=?2`, 0, policyir.ProviderSQLite)
	root := rebase(`root_a=?1 AND root_b=?2`, 2, policyir.ProviderSQLite)
	if got, want := join+";"+root, `join_a=?1 AND join_b=?2;root_a=?3 AND root_b=?4`; got != want {
		t.Fatalf("multi-fragment placeholders=%q want=%q", got, want)
	}
}

func mustAnalyticsStringValue(t *testing.T, value string) policyir.Value {
	t.Helper()
	result, err := policyir.StringValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
