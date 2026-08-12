package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestP8FailureCommandOwnsClosedFailureBoundary(t *testing.T) {
	var output bytes.Buffer
	if code := run(context.Background(), []string{"-timeout", "invalid-canary"}, &output); code != 1 {
		t.Fatalf("exit=%d output=%s", code, output.Bytes())
	}
	if !strings.Contains(output.String(), `"command":"p8failure"`) || !strings.Contains(output.String(), `"status":"FAIL"`) || strings.Contains(output.String(), "invalid-canary") {
		t.Fatalf("failure output=%s", output.Bytes())
	}
}
