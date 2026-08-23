package runtime

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
)

func TestSemanticRankedIdentityCannotEscapeAuthorizedCandidates(t *testing.T) {
	// Ranking runs over a candidate subquery, but the ranked page is still read
	// back through the ordinary authorized row statement. A ranked identity that
	// statement did not return must close the request, never be skipped.
	ranks := []semanticruntime.Rank{{Key: "foreign", Distance: 0}, {Key: "other-foreign", Distance: 1}}
	result, err := assembleSemanticResults(ranks, map[string]golem.Row[struct{}]{})
	if err == nil || err.Error() != "P9_SEMANTIC_QUERY: ranked identity escaped authorized candidates" || len(result) != 0 {
		t.Fatalf("escaped identity result=%#v error=%v", result, err)
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
