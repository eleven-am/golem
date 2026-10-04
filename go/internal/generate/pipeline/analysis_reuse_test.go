package pipeline

import (
	"context"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/codegen/bindings"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

type countingLowerer struct {
	physical.Lowerer
	calls *int
}

func (lowerer countingLowerer) Lower(ctx context.Context, model ir.ModelIR, options physical.LowerOptions) (physical.PhysicalSchema, error) {
	*lowerer.calls++
	return lowerer.Lowerer.Lower(ctx, model, options)
}

func TestAnalysisRunsEveryPreEmissionStageOnceAcrossItsBuilds(t *testing.T) {
	log := recordGoInvocations(t)
	discoveries := 0
	discover := bindingDiscovery
	bindingDiscovery = func(ctx context.Context, request bindings.DiscoveryRequest) bindings.Result {
		discoveries++
		return discover(ctx, request)
	}
	t.Cleanup(func() { bindingDiscovery = discover })
	lowerings := 0
	request := multipackageRequest(t)
	for index, lowerer := range request.Lowerers {
		request.Lowerers[index] = countingLowerer{Lowerer: lowerer, calls: &lowerings}
	}

	reference, err := Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	complete := goInvocations(t, log)
	wantDiscoveries, wantLowerings := discoveries, lowerings
	if complete.schemaLoads == 0 || complete.prospectiveCompiles != 1 || complete.gqlgenRuns != 1 || wantDiscoveries == 0 || wantLowerings != 2 {
		t.Fatalf("one Build ran stages %+v discoveries=%d lowerings=%d", complete, wantDiscoveries, wantLowerings)
	}
	discoveries, lowerings = 0, 0

	analysis, err := Analyze(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	analysed := goInvocations(t, log)
	if analysed.schemaLoads != complete.schemaLoads || analysed.prospectiveCompiles != 0 || analysed.gqlgenRuns != 0 || discoveries != wantDiscoveries || lowerings != wantLowerings {
		t.Fatalf("Analyze ran stages %+v discoveries=%d lowerings=%d", analysed, discoveries, lowerings)
	}
	if analysis.ModelFingerprint() != reference.ModelFingerprint || !reflect.DeepEqual(analysis.Providers(), reference.Providers) {
		t.Fatal("analysis model or providers differ from a complete Build")
	}
	for range 2 {
		result, err := analysis.Build(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		assertManifestResultsEqual(t, reference.Prospective, result.Prospective)
		if !reflect.DeepEqual(result, reference) {
			t.Fatal("analysis Build result differs from a complete Build")
		}
	}
	built := goInvocations(t, log)
	if built.schemaLoads != 0 || built.prospectiveCompiles != 2 || built.gqlgenRuns != 1 || discoveries != wantDiscoveries || lowerings != wantLowerings {
		t.Fatalf("two analysis Builds ran stages %+v discoveries=%d lowerings=%d", built, discoveries, lowerings)
	}
}
