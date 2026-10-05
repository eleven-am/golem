package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/observe"
)

func TestDeleteWithoutCascadingDependentsIssuesNoCaptureStatementsAcrossProviders(t *testing.T) {
	collector := &p8ObservationCollector{}
	configure := func(_ schematest.Fixture, config *Config[mutationResultPrincipal, mutationResultActor]) {
		config.Observer = collector
	}
	runConfiguredRelationDeleteProviderProfiles(t, "cascade_cost", schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, configure, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		if len(fixture.app.registry.DeleteEffects(fixture.schema.User)) != 0 {
			t.Fatal("the fixture's User has cascading dependents; the scenario is void")
		}
		seedCascadeUser(t, fixture, 3, "carol")
		seedCascadeUser(t, fixture, 4, "dave")
		caller := mustMutationResultCaller(t, fixture)
		rowLocks := 0
		if profile.provider == golem.PostgreSQL {
			rowLocks = 2
		}
		before := len(collector.matching(observe.KindMutation, observe.OperationMutationDelete))
		if _, err := CallerDelete(ctx, caller, fixture.userDescriptor, cascadeUserTarget(fixture, 3)); err != nil {
			t.Fatal(err)
		}
		assertDeleteStatementCount(t, collector.matching(observe.KindMutation, observe.OperationMutationDelete)[before:], observe.OperationMutationDelete, 2+rowLocks)
		before = len(collector.matching(observe.KindMutation, observe.OperationMutationDeleteMany))
		if count, err := CallerDeleteMany(ctx, caller, fixture.userDescriptor, fixture.userID.Eq(golem.UUID{15: 4})); err != nil || count != 1 {
			t.Fatalf("delete-many count=%d err=%v", count, err)
		}
		assertDeleteStatementCount(t, collector.matching(observe.KindMutation, observe.OperationMutationDeleteMany)[before:], observe.OperationMutationDeleteMany, 3+rowLocks)
	})
}

func assertDeleteStatementCount(t *testing.T, values []p8ObservationSnapshot, operation observe.Operation, want int) {
	t.Helper()
	var finished []p8ObservationSnapshot
	for _, value := range values {
		if value.phase == observe.PhaseFinish {
			finished = append(finished, value)
		}
	}
	if len(finished) != 1 || finished[0].outcome != observe.OutcomeSuccess || finished[0].statements != want {
		t.Fatalf("%s observations=%+v want one success with %d statements", operation, finished, want)
	}
}
