package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestCountParameterLimitUsesStablePublicBadUserInput(t *testing.T) {
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
	if failure.Code != golem.CodeBadUserInput || failure.Operation != "count" || failure.Model != harness.fixture.User || failure.Message != "count plan could not be rendered" {
		t.Fatalf("public failure=%#v", failure)
	}
	if strings.Contains(failure.Error(), "parameter") || strings.Contains(failure.Error(), "ceiling") {
		t.Fatalf("public error leaked renderer details: %s", failure.Error())
	}
	if cause := errors.Unwrap(failure); cause == nil || !strings.Contains(cause.Error(), "parameter ceiling") {
		t.Fatalf("trusted cause=%v", cause)
	}
}
