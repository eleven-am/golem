package runtime_test

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

func TestGeneratedSocialMetaOnlyOperationsResolveThePrincipalWithoutPolicyOrSQL(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			h := newP5SocialGeneratedHarness(t, profile)
			server, err := h.app.GraphQL(p5social.GraphQLConfig[p5social.Principal]{
				Introspection: true,
				PrincipalFromContext: func(ctx context.Context) (p5social.Principal, bool) {
					principal, ok := ctx.Value(p5SocialPrincipalKey{}).(p5social.Principal)
					return principal, ok
				},
				ReportInternalError: func(_ context.Context, err error) { t.Errorf("internal error reported: %v", err) },
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
			h.server = server
			valid := p5social.Principal{UserID: golem.UUID{15: 1}, Valid: true}
			reset := func() {
				h.trace.reset()
				p5social.ResetPolicyProbe()
				h.resolutions.Store(0)
			}
			for _, query := range []string{
				`{ __typename }`,
				`{ __schema { queryType { name } } }`,
				`{ ...Meta } fragment Meta on Query { kind: __typename __type(name: "Post") { name } }`,
				`mutation { __typename }`,
			} {
				reset()
				response := h.execute(t, valid, query, nil)
				if len(response.Errors) != 0 || len(response.Data) == 0 {
					t.Fatalf("%s response=%#v", query, response)
				}
				if probe := p5social.PolicyProbe(); len(probe) != 0 {
					t.Fatalf("%s built policies %v", query, probe)
				}
				if statements := h.trace.snapshot(); len(statements) != 0 {
					t.Fatalf("%s ran SQL %v", query, statements)
				}
				if h.resolutions.Load() != 1 {
					t.Fatalf("%s resolved the principal %d times", query, h.resolutions.Load())
				}
			}

			reset()
			mixed := h.execute(t, valid, `{ __typename users { id } }`, nil)
			if len(mixed.Errors) != 0 || mixed.Data["__typename"] != "Query" || mixed.Data["users"] == nil {
				t.Fatalf("mixed response=%#v", mixed)
			}
			probe := p5social.PolicyProbe()
			sort.Strings(probe)
			if !reflect.DeepEqual(probe, []string{"Comment", "Friendship", "Post", "PostTag", "Tag", "User"}) {
				t.Fatalf("mixed policy trace=%v", probe)
			}
			if h.resolutions.Load() != 1 || len(h.trace.snapshot()) == 0 {
				t.Fatalf("mixed resolutions=%d statements=%d", h.resolutions.Load(), len(h.trace.snapshot()))
			}

			invalid := p5social.Principal{UserID: golem.UUID{15: 1}}
			reset()
			refusedData := h.execute(t, invalid, `{ users { id } }`, nil)
			if len(refusedData.Errors) != 1 || refusedData.Errors[0].Extensions["code"] != "UNAUTHENTICATED" || refusedData.Data != nil {
				t.Fatalf("invalid principal data response=%#v", refusedData)
			}
			for _, query := range []string{`{ __typename }`, `{ __schema { queryType { name } } }`, `mutation { __typename }`} {
				reset()
				refused := h.execute(t, invalid, query, nil)
				if !reflect.DeepEqual(refused, refusedData) {
					t.Fatalf("%s invalid principal response=%#v, data query response=%#v", query, refused, refusedData)
				}
				if h.resolutions.Load() != 1 || len(p5social.PolicyProbe()) != 0 || len(h.trace.snapshot()) != 0 {
					t.Fatalf("%s invalid principal resolutions=%d policies=%v statements=%v", query, h.resolutions.Load(), p5social.PolicyProbe(), h.trace.snapshot())
				}
			}
		})
	}
}
