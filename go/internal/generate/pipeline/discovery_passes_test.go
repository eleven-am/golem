package pipeline

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/bindings"
)

func TestBindingDiscoveryLoadsASourceThatTypeChecksAgainstTheShellOnce(t *testing.T) {
	var calls []bindings.DiscoveryRequest
	original := bindingDiscovery
	bindingDiscovery = func(ctx context.Context, request bindings.DiscoveryRequest) bindings.Result {
		calls = append(calls, request)
		return original(ctx, request)
	}
	t.Cleanup(func() { bindingDiscovery = original })
	if _, err := Build(context.Background(), multipackageRequest(t)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("binding discovery loaded the source %d times, want 1", len(calls))
	}
	if len(calls[0].BuildFlags) != 0 {
		t.Fatalf("binding discovery resolved an alternate module file on the first pass: %v", calls[0].BuildFlags)
	}
}
