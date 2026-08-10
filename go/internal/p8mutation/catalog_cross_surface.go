package p8mutation

import (
	"time"

	"github.com/eleven-am/golem/go/internal/mutationverify"
	"github.com/eleven-am/golem/go/internal/p7verify"
)

// crossSurfaceMutations closes the independent-engine and capability seams
// that P8's caller/GraphQL/provider oracle must detect. Inherited patches are
// copied from the executable P6/P7 catalogs, then deliberately mapped to the
// stronger P8 all-provider gates.
func crossSurfaceMutations() []Mutation {
	providerGate := func(pkg, test string) Gate {
		return Gate{Directory: "go", Package: pkg, Test: test, Required: []string{"GOLEM_TEST_POSTGRES_DSN", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}}
	}
	mutations := []Mutation{
		{
			Label: "GRAPHQL_SECOND_READ_ENGINE", Summary: "execute an independently repeated read before returning the GraphQL read result",
			Patches: []Patch{{
				Path:   "go/examples/social/social/zz_golem_graphql.gen.go",
				Before: "func (caller *golemGeneratedGraphQLCaller[P]) ExecuteFrozenRead(ctx context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {\n\treturn caller.execution.ExecuteFrozenRead(ctx, request)\n}\n",
				After:  "func (caller *golemGeneratedGraphQLCaller[P]) ExecuteFrozenRead(ctx context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {\n\tif _, err := caller.execution.ExecuteFrozenRead(ctx, request); err != nil { return nil, err }\n\treturn caller.execution.ExecuteFrozenRead(ctx, request)\n}\n",
			}},
			Gate: providerGate("./internal/p8oracle", "TestP8ReadCrossEntryPointIndependentOracle"), Timeout: 10 * time.Minute,
		},
		{
			Label: "GRAPHQL_SECOND_MUTATION_ENGINE", Summary: "execute the generated GraphQL mutation twice through a second mutation invocation",
			Patches: []Patch{{
				Path:   "go/examples/social/social/zz_golem_graphql.gen.go",
				Before: "func (caller *golemGeneratedGraphQLCaller[P]) ExecuteFrozenMutation(ctx context.Context, request golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error) {\n\treturn caller.execution.ExecuteFrozenMutation(ctx, request)\n}\n",
				After:  "func (caller *golemGeneratedGraphQLCaller[P]) ExecuteFrozenMutation(ctx context.Context, request golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error) {\n\tif _, err := caller.execution.ExecuteFrozenMutation(ctx, request); err != nil { return golem.RuntimeMutationResult{}, err }\n\treturn caller.execution.ExecuteFrozenMutation(ctx, request)\n}\n",
			}},
			Gate: providerGate("./internal/p8oracle/mutation", "TestP8MutationCrossEntryPointIndependentOracle"), Timeout: 10 * time.Minute,
		},
		{
			Label: "CUSTOM_ROOT_RECEIVES_SYSTEM_OR_DB", Summary: "hand a generated System capability to custom GraphQL roots instead of the caller-only capability",
			Patches: []Patch{
				{Path: "go/examples/social/social/zz_golem_graphql.gen.go", Before: "type golemGeneratedGraphQLCaller[P any] struct {\n\tpublic    *Caller[P]\n\texecution *golemruntime.CallerMutationExecution[P, Actor]\n}\n", After: "type golemGeneratedGraphQLCaller[P any] struct {\n\tpublic    *Caller[P]\n\tapplication *App[P]\n\texecution *golemruntime.CallerMutationExecution[P, Actor]\n}\n"},
				{Path: "go/examples/social/social/zz_golem_graphql.gen.go", Before: "\treturn caller.public\n}\n\nfunc (caller *golemGeneratedGraphQLCaller[P]) ExecuteFrozenRead", After: "\treturn caller.application.System()\n}\n\nfunc (caller *golemGeneratedGraphQLCaller[P]) ExecuteFrozenRead"},
				{Path: "go/examples/social/social/zz_golem_graphql.gen.go", Before: "\treturn &golemGeneratedGraphQLCaller[P]{public: caller, execution: execution}, nil\n", After: "\treturn &golemGeneratedGraphQLCaller[P]{public: caller, application: app, execution: execution}, nil\n"},
			},
			Gate: providerGate("./internal/p8oracle", "TestP8CustomQueryCannotChangeAuthorizationOrSystemCapability"), Timeout: 10 * time.Minute,
		},
		{
			Label: "SCOPED_SQL_SKIPS_HOP_POLICY", Summary: "omit every joined target-hop authorization predicate from scoped SQL",
			Patches: []Patch{{
				Path: "go/internal/scoped/scoped.go", Before: "\t\tconditions = append(conditions, \"(\"+policy+\")\")\n\t\tkeyword := \" INNER JOIN \"\n", After: "\t\t_ = policy\n\t\tkeyword := \" INNER JOIN \"\n",
			}},
			Gate: providerGate("./internal/p8oracle/analytics", "TestP8ScopedReadAuthorizationAndAuditRedTeam"), Timeout: 10 * time.Minute,
		},
		{
			Label: "ANALYTICS_PARTIAL_MASK", Summary: "replace every requested aggregate sum with a partial null cell",
			Patches: []Patch{{
				Path: "go/internal/analytics/sql.go", Before: "\tcase golem.AggregateSum:\n\t\tif typ.Kind == compilerir.TypeInt16", After: "\tcase golem.AggregateSum:\n\t\treturn \"NULL\"\n\t\tif typ.Kind == compilerir.TypeInt16",
			}},
			Gate: providerGate("./internal/p8oracle/analytics", "TestP8AnalyticsCrossEntryPointIndependentOracle"), Timeout: 10 * time.Minute,
		},
	}
	mutations = append(mutations,
		inheritedMutation("EVENT_SURFACES_DIVERGE", "serialize GraphQL events through a second event response engine", p7verify.MutationCatalog(), "GRAPHQL_SECOND_EVENT_ENGINE", providerGate("./internal/p8oracle/event", "TestP8EventCrossEntryPointIndependentOracle")),
	)
	return mutations
}

func inheritedMutation(label, summary string, catalog []mutationverify.Mutation, sourceLabel string, gate Gate) Mutation {
	for _, source := range catalog {
		if source.Label != sourceLabel {
			continue
		}
		patches := make([]Patch, len(source.Patches))
		for index, patch := range source.Patches {
			patches[index] = Patch{Path: "go/" + patch.Path, Before: patch.Before, After: patch.After}
		}
		return Mutation{Label: label, Summary: summary, Patches: patches, Gate: gate, Timeout: 10 * time.Minute}
	}
	panic("P8 mutation inherited source is absent: " + sourceLabel)
}
