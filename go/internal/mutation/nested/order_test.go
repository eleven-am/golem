package nested

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestRelationExecutionRankIsTheDeclaredDestructiveFirstOrder(t *testing.T) {
	want := []golem.MutationRelationAction{
		golem.MutationRelationDelete, golem.MutationRelationDeleteMany, golem.MutationRelationDisconnect, golem.MutationRelationSet,
		golem.MutationRelationUpdate, golem.MutationRelationUpdateMany, golem.MutationRelationUpsert,
		golem.MutationRelationCreate, golem.MutationRelationCreateMany, golem.MutationRelationConnect, golem.MutationRelationConnectOrCreate,
	}
	seen := make(map[uint8]golem.MutationRelationAction, len(want))
	for index, action := range want {
		rank, ok := relationExecutionRank(action)
		if !ok || int(rank) != index {
			t.Fatalf("action %d rank=%d ok=%t want %d", action, rank, ok, index)
		}
		if previous, duplicate := seen[rank]; duplicate {
			t.Fatalf("actions %d and %d share rank %d", previous, action, rank)
		}
		seen[rank] = action
	}
	for action := golem.MutationRelationCreate; action <= golem.MutationRelationDeleteMany; action++ {
		if _, ok := relationExecutionRank(action); !ok {
			t.Fatalf("valid action %d has no execution rank", action)
		}
	}
	for _, invalid := range []golem.MutationRelationAction{0, golem.MutationRelationDeleteMany + 1} {
		if _, ok := relationExecutionRank(invalid); ok {
			t.Fatalf("invalid action %d has an execution rank", invalid)
		}
	}
}
