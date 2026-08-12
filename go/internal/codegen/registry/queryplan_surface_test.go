package registry

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestGeneratedQueryPlanSurfaceIsCallerOnlyAndExact(t *testing.T) {
	request := optimisticConcurrencyRegistryRequest(t, false)
	request.Schema.Contract.Models[0].Aggregation = &ir.AggregationContractIR{
		Enabled: true,
		RelationDimensions: []ir.RelationDimensionContractIR{{
			Name: "authorName", Path: []ir.RelationID{"00000000000000000000000000000021"}, TerminalField: "00000000000000000000000000000022",
		}},
	}
	request.Schema.Contract.Models[0].ScopedReads = true
	request.Schema.ContractFingerprint = fingerprintContract(t, request.Schema.Contract)

	final, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	finalSource := string(final.Source)
	for _, fragment := range []string{
		"func (client CallerPostClient[P]) ExplainFindMany(",
		"func (client CallerPostClient[P]) ExplainFindFirst(",
		"func (client CallerPostClient[P]) ExplainFindUnique(",
		"func (client CallerPostClient[P]) ExplainCount(",
		"func (client CallerPostClient[P]) ExplainAggregate(",
		"func (client CallerPostClient[P]) ExplainGroupBy(",
		"func (client CallerPostClient[P]) ExplainRelationGroupBy(",
		"func (client CallerPostClient[P]) ExplainScoped(",
		"return golemruntime.CallerExplainFindMany(ctx, client.runtime, models.GolemGeneratedPostDescriptor, options...)",
		"return golemruntime.CallerExplainFindFirst(ctx, client.runtime, models.GolemGeneratedPostDescriptor, options...)",
		"return golemruntime.CallerExplainFindUnique(ctx, client.runtime, models.GolemGeneratedPostDescriptor, selector, options...)",
		"return golemruntime.CallerExplainCount(ctx, client.runtime, models.GolemGeneratedPostDescriptor, options...)",
		"return golemruntime.CallerExplainAggregate(ctx, client.runtime, models.GolemGeneratedPostDescriptor, request)",
		"return golemruntime.CallerExplainGroupBy(ctx, client.runtime, models.GolemGeneratedPostDescriptor, request)",
		"return golemruntime.CallerExplainRelationGroupBy(ctx, client.runtime, models.GolemGeneratedPostDescriptor, request)",
		"return golemruntime.CallerExplainScoped(ctx, client.runtime, models.GolemGeneratedPostDescriptor, request)",
		"func (client CallerUserClient[P]) ExplainFindMany(",
		"func (client CallerUserClient[P]) ExplainGroupBy(",
	} {
		if !strings.Contains(finalSource, fragment) {
			t.Errorf("final registry missing %q:\n%s", fragment, finalSource)
		}
	}
	for _, forbidden := range []string{
		"func (client SystemPostClient[P]) Explain",
		"func (client CallerTxPostClient[P]) Explain",
		"func (client SystemTxPostClient[P]) Explain",
		"func (client CallerUserClient[P]) ExplainRelationGroupBy",
		"func (client CallerUserClient[P]) ExplainScoped",
		"ExplainCreate", "ExplainUpdate", "ExplainDelete",
	} {
		if strings.Contains(finalSource, forbidden) {
			t.Errorf("final registry contains forbidden query-plan surface %q:\n%s", forbidden, finalSource)
		}
	}

	finalShell, err := EmitShell(ShellRequest{AppPackage: request.AppPackage, Actor: request.Actor, Model: request.Schema.Model, Contract: request.Schema.Contract})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := callerABI(t, finalShell.Source), callerABI(t, final.Source); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("final shell caller ABI differs from final registry\nshell: %v\nfinal: %v", got, want)
	}

	discoveryRequest := request.Schema.Contract
	discoveryRequest.Models[0].Aggregation = nil
	discoveryRequest.Models[0].ScopedReads = false
	discovery, err := EmitShell(ShellRequest{AppPackage: request.AppPackage, Actor: request.Actor, Model: request.Schema.Model, Contract: discoveryRequest, DeclarationDiscovery: true})
	if err != nil {
		t.Fatal(err)
	}
	discoverySource := string(discovery.Source)
	for _, model := range []string{"Post", "User"} {
		for _, conditional := range []string{"ExplainRelationGroupBy", "ExplainScoped"} {
			if !strings.Contains(discoverySource, "func (Caller"+model+"Client[P]) "+conditional+"(") {
				t.Errorf("declaration-discovery shell missing %s.%s:\n%s", model, conditional, discoverySource)
			}
		}
	}
	for _, forbidden := range []string{"func (CallerTxPostClient[P]) Explain", "func (CallerTxUserClient[P]) Explain"} {
		if strings.Contains(discoverySource, forbidden) {
			t.Errorf("declaration-discovery shell leaked query-plan capability %q:\n%s", forbidden, discoverySource)
		}
	}
}

func fingerprintContract(t *testing.T, contract ir.ContractIR) ir.Fingerprint {
	t.Helper()
	fingerprint, err := ir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}
