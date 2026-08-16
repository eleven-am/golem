package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
)

func TestSemanticCandidateLimitPrecedesReadableIdentityExtraction(t *testing.T) {
	// Zero-value rows model a conditionally unreadable primary identity. The
	// authorized row cardinality must still close the request before identity
	// extraction can discard every row and evade the portable ceiling.
	rows := make([]golem.Row[struct{}], semanticruntime.MaximumCandidates+2)
	_, err := rankSemanticRows(
		context.Background(),
		(*App[string, struct{}])(nil),
		golem.ModelDescriptor[struct{}]{},
		1,
		rows,
		"",
		func(context.Context, ir.ModelID, []string) ([]semanticruntime.Rank, error) {
			t.Fatal("ranking ran despite an exceeded candidate ceiling")
			return nil, nil
		},
	)
	if code, ok := embedding.CodeOf(err); !ok || code != embedding.CodeInvalidInput {
		t.Fatalf("candidate overflow error=%v code=%q ok=%t", err, code, ok)
	}
}

func TestSemanticQueryEncodingFailsBeforeReadPlanning(t *testing.T) {
	queries := []string{
		string([]byte{0xff}),
		strings.Repeat("x", embedding.MaximumInputBytes+1),
	}
	for _, query := range queries {
		if _, err := semanticReadOptions[struct{}](nil, query, 1); err == nil {
			t.Fatal("invalid semantic query was accepted")
		} else if code, ok := embedding.CodeOf(err); !ok || code != embedding.CodeInvalidInput {
			t.Fatalf("invalid query error=%v code=%q ok=%t", err, code, ok)
		}
	}
}
