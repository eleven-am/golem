package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestCountParameterLimitIsAPublicLimitExceeded(t *testing.T) {
	harness := newOracleHarness(t)
	// In now uses one compact parameter; independent substring comparisons
	// still exercise the unchanged provider-neutral 999-bind ceiling.
	predicates := make([]golem.Predicate[oracleUser], 1_000)
	for index := range predicates {
		predicates[index] = harness.userName.Contains(fmt.Sprintf("name-%04d", index))
	}
	count, err := SystemCount(context.Background(), harness.app.System(), harness.users,
		golem.Where(golem.Or(predicates...)),
	)
	var failure *golem.Error
	if count != 0 || !errors.As(err, &failure) {
		t.Fatalf("count=%d error=%v", count, err)
	}
	if failure.Code != golem.CodeLimitExceeded || failure.Reason != golem.ReasonLimitExceeded || failure.Limit != golem.LimitStatementParameters || failure.LimitMax != 999 || failure.Operation != "count" || failure.Model != harness.fixture.User || failure.Message != "request exceeds the statement_parameters limit of 999" {
		t.Fatalf("public failure=%#v", failure)
	}
	if strings.Contains(failure.Error(), "ceiling") || strings.Contains(failure.Error(), "name") || strings.Contains(failure.Error(), "P3_") {
		t.Fatalf("public error leaked renderer details: %s", failure.Error())
	}
	if cause := errors.Unwrap(failure); cause == nil || !strings.Contains(cause.Error(), "parameter ceiling") {
		t.Fatalf("trusted cause=%v", cause)
	}
}
