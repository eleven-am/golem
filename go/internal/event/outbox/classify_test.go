package outbox

import (
	"errors"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

func TestFactFailureClassificationFollowsTheErrorTypeNotItsText(t *testing.T) {
	unavailable := fmt.Errorf("GOLEM_EVENT_CODEC: validate stored fact: %w", mutationfact.SchemaUnavailable(mutationfact.SchemaReference{
		FormatVersion: mutationfact.FormatVersion, EventSchema: golem.SchemaDigest{1},
	}))
	for name, test := range map[string]struct {
		err  error
		want string
	}{
		"typed unavailable schema":                             {err: unavailable, want: "schema-unavailable"},
		"typed retired format":                                 {err: fmt.Errorf("GOLEM_EVENT_CODEC: validate stored fact: %w", mutationfact.FormatRetired(2)), want: "format-retired"},
		"corrupt fact that mentions a schema and a generation": {err: errors.New("P4_FACT_CODEC: resolved V1 generation fingerprint disagrees with the historical schema"), want: "fact-invalid"},
		"corrupt fact":                                         {err: errors.New("P4_FACT_CODEC: byte=3: invalid fact magic"), want: "fact-invalid"},
	} {
		if got := classifyFactFailure(test.err); got != test.want {
			t.Errorf("%s: classified as %q, want %q", name, got, test.want)
		}
	}
}
