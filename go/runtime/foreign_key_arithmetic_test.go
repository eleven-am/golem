package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/observe"
)

func TestArithmeticOnAForeignKeyIsRefusedBeforeAnyStatement(t *testing.T) {
	collector := &p8ObservationCollector{}
	configure := func(_ schematest.Fixture, config *Config[mutationResultPrincipal, mutationResultActor]) {
		config.Observer = collector
	}
	runConfiguredRelationDeleteProviderProfiles(t, "fk_arithmetic", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, compilerir.ActionCascade)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(compilerir.ActionCascade), configure, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(10, golem.UUID{15: 1}, "child")); err != nil {
			t.Fatal(err)
		}
		caller := mustMutationResultCaller(t, fixture)
		assertRefused := func(t *testing.T, operation observe.Operation, err error) {
			t.Helper()
			var failure *golem.Error
			if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || failure.Field != fixture.schema.AuthorID || failure.Message != "foreign key AuthorID cannot be changed by arithmetic" {
				t.Fatalf("error=%v failure=%#v; want the explicit foreign-key arithmetic refusal", err, failure)
			}
			for _, observed := range collector.matching(observe.KindMutation, operation) {
				if observed.statements != 0 {
					t.Fatalf("refused %s issued %d statements", operation, observed.statements)
				}
			}
		}
		increment := golem.GeneratedIncrementFieldValue[mutationResultPost, golem.UUID](fixture.schema.Post, fixture.authorID, golem.UUID{15: 1})
		_, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(10), golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, increment))
		assertRefused(t, observe.OperationMutationUpdate, err)
		decrement := golem.GeneratedDecrementFieldValue[mutationResultPost, golem.UUID](fixture.schema.Post, fixture.authorID, golem.UUID{15: 1})
		_, err = CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 10}), golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post, decrement))
		assertRefused(t, observe.OperationMutationUpdateMany, err)
		_, err = SystemUpdateMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 10}), golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post, decrement))
		assertRefused(t, observe.OperationMutationUpdateMany, err)
	})
}
