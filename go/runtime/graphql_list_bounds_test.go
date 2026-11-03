package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/runtime/testdata/hookvalues"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
	"github.com/eleven-am/golem/go/runtime/testdata/p5extensions"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
	"github.com/eleven-am/golem/go/runtime/testdata/p6metrics"
)

type generatedBoundsExecutor struct{ bounds map[string]int }

func (generatedBoundsExecutor) Execute(context.Context, int, publicgraphql.Operation) publicgraphql.Response {
	return publicgraphql.Response{}
}

func (executor generatedBoundsExecutor) ListBounds() publicgraphql.ListBounds {
	return publicgraphql.NewListBounds(executor.bounds)
}

func TestGeneratedServersHaveBoundForEveryUndefaultedList(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		bundle golem.SchemaBundle
		keys   []string
	}{
		{"hookvalues", hookvalues.GolemGeneratedSchemaBundle(), nil},
		{"p5social", p5social.GolemGeneratedSchemaBundle(), []string{"Query.groupByPosts", "Query.relationGroupByPosts"}},
		{"p5extensions", p5extensions.GolemGeneratedSchemaBundle(), []string{"Query.searchUsers"}},
		{"p6metrics", p6metrics.GolemGeneratedSchemaBundle(), []string{"Query.groupByMetrics"}},
		{"p10operations", p10operations.GolemGeneratedSchemaBundle(), []string{"Mutation.submitCappedInvites", "Mutation.submitInvites", "Query.cappedInvites", "Query.groupedInvites", "Query.searchInvites", "Query.searchTeams", "Team.openInvites"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			compilation, err := policyschema.DecodeActive(fixture.bundle)
			if err != nil {
				t.Fatal(err)
			}
			compiler, err := graphqloperation.New(compilation, graphqloperation.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			bounds := compiler.ListBounds()
			var keys []string
			for key, bound := range bounds {
				keys = append(keys, key)
				if bound < 1 {
					t.Fatalf("%s has bound %d", key, bound)
				}
			}
			sort.Strings(keys)
			if strings.Join(keys, ",") != strings.Join(fixture.keys, ",") {
				t.Fatalf("generated bounds = %v, want %v", keys, fixture.keys)
			}
			sdl, err := os.ReadFile(filepath.Join("testdata", fixture.name, "zz_golem_graphql.schema.graphqls"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := publicgraphql.NewServer(string(sdl), publicgraphql.Config[int]{
				PrincipalFromContext: func(context.Context) (int, bool) { return 0, false },
				ReportInternalError:  func(context.Context, error) {},
			}, generatedBoundsExecutor{bounds: bounds}); err != nil {
				t.Fatalf("server refused the generated bounds: %v", err)
			}
		})
	}
}
