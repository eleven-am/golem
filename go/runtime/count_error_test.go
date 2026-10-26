package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestCountParameterLimitIsAPublicLimitExceeded(t *testing.T) {
	harness := newOracleHarness(t)
	values := make([]string, 1_000)
	for index := range values {
		values[index] = "name"
	}
	count, err := SystemCount(context.Background(), harness.app.System(), harness.users,
		golem.Where(harness.userName.In(values...)),
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
