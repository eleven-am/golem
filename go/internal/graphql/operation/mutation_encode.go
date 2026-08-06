package operation

import (
	"fmt"
	"math"

	"github.com/eleven-am/golem/go/golem"
	graphqlmutation "github.com/eleven-am/golem/go/internal/graphql/mutation"
)

// EncodeMutation feeds single-row P4 results through the same occurrence-aware
// row encoder as reads and renders batch counts only through BatchPayload.
func (c *Compiler) EncodeMutation(root MutationRoot, result golem.RuntimeMutationResult) (any, error) {
	if c == nil || root.ResponseName == "" || root.Model == "" {
		return nil, fmt.Errorf("P5_MUTATION_ENCODE: root metadata is absent")
	}
	if root.Operation == graphqlmutation.UpdateMany || root.Operation == graphqlmutation.DeleteMany {
		count, ok := result.Count()
		if !ok || count < 0 || count > math.MaxInt32 {
			return nil, fmt.Errorf("P5_MUTATION_ENCODE: batch result is absent or outside GraphQL Int")
		}
		payload := make(map[string]any, len(root.BatchSlots))
		for _, slot := range root.BatchSlots {
			if slot.Typename {
				payload[slot.ResponseName] = "BatchPayload"
			} else {
				payload[slot.ResponseName] = int32(count)
			}
		}
		return payload, nil
	}
	row, ok := result.Row()
	if !ok {
		return nil, fmt.Errorf("P5_MUTATION_ENCODE: row result is absent")
	}
	return c.encodeRow(root.Model, row, root.Slots)
}
