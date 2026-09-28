package runtime

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	fulltextruntime "github.com/eleven-am/golem/go/internal/fulltext/runtime"
)

func TestPublicFullTextQueryErrorClassifiesOnlyValidation(t *testing.T) {
	validation := fulltextruntime.InvalidQuery("full-text query exceeds the 32-term limit")
	presented := publicFullTextQueryError(golem.ModelID{}, validation)
	var failure *golem.Error
	if !errors.As(presented, &failure) {
		t.Fatalf("validation error type=%T", presented)
	}
	if failure.Code != golem.CodeBadUserInput || failure.Message != "full-text query exceeds the 32-term limit" || !errors.Is(presented, validation) {
		t.Fatalf("validation error=%#v unwrap=%v", failure, errors.Unwrap(presented))
	}

	database := errors.New("P9_FULLTEXT_QUERY: full-text ranking execution failed")
	presented = publicFullTextQueryError(golem.ModelID{}, database)
	if presented != database {
		t.Fatalf("database error=%v, want original internal error", presented)
	}
	if errors.As(presented, &failure) {
		t.Fatalf("database error became public=%#v", failure)
	}
}

func TestFullTextRequestValidationReasonsAreSpecific(t *testing.T) {
	for _, test := range []struct {
		name       string
		query      string
		take       int
		predicates []golem.Predicate[struct{}]
		reason     string
	}{
		{name: "empty query", take: 1, reason: "full-text query is empty"},
		{name: "result limit", query: "alpha", take: 0, reason: "full-text result limit is 0, outside 1..1000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := fullTextReadOptions(test.predicates, test.query, test.take)
			if err == nil {
				t.Fatal("request accepted")
			}
			if reason, ok := fulltextruntime.QueryValidationReason(err); !ok || reason != test.reason {
				t.Fatalf("error=%v reason=%q typed=%t", err, reason, ok)
			}
		})
	}
}
