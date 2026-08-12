package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestP8DocsCommandOwnsClosedFailureBoundary(t *testing.T) {
	var output bytes.Buffer
	if code := run(context.Background(), []string{"unexpected-canary"}, &output); code != 1 {
		t.Fatalf("exit=%d output=%s", code, output.Bytes())
	}
	if !strings.Contains(output.String(), `"command":"p8docs"`) || !strings.Contains(output.String(), `"status":"FAIL"`) || strings.Contains(output.String(), "unexpected-canary") {
		t.Fatalf("failure output=%s", output.Bytes())
	}
}
