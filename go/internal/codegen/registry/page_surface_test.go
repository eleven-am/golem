package registry

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestPageResolverRegistrationIsSeparateFromMutationCapabilities(t *testing.T) {
	operation := operationQuery(operationQueryExtension, "feed", "Feed")
	operation.Pagination = &ir.CustomPaginationIR{DefaultTake: 25, MaxTake: 100, MaxCandidates: 200, Revision: "v1", Item: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String"}}
	operation.Result = ir.GraphQLTypeIR{Kind: ir.GraphQLTypeObject, Name: "FeedPage"}
	operation.Arguments = []ir.GraphQLArgumentContractIR{
		{Name: "take", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "Int", Nullable: true}},
		{Name: "after", Type: ir.GraphQLTypeIR{Kind: ir.GraphQLTypeScalar, Name: "String", Nullable: true}},
	}
	for _, withMutation := range []bool{false, true} {
		request := operationRequest(t)
		request.Schema.Contract.DataIncarnation = &ir.DataIncarnationContractIR{Kind: "provider", Version: 1}
		request.Schema.Contract.CustomOperations = []ir.CustomOperationContractIR{operation}
		request.Schema.Contract.GraphQLTypes = []ir.GraphQLTypeDefinitionIR{ir.GraphQLPageDefinition("FeedPage", *operation.Pagination)}
		if withMutation {
			request.Schema.Contract.CustomOperations = append(request.Schema.Contract.CustomOperations, operationMutation(operationMutationExtension, "create", "Create"))
		}
		fingerprint, err := ir.ContractFingerprint(request.Schema.Contract)
		if err != nil {
			t.Fatal(err)
		}
		request.Schema.ContractFingerprint = fingerprint
		file, err := Emit(request)
		if err != nil {
			t.Fatal(err)
		}
		source := string(file.Source)
		for _, want := range []string{"return golem.GeneratedApplicationPageQueries(bindings,", `golem.GeneratedPageQueryOperation("` + operationQueryExtension + `", Feed)`, "*golemruntime.PageConfig[Actor]", "engineConfig.Pages = config.Pages"} {
			if !strings.Contains(source, want) {
				t.Errorf("source missing %q", want)
			}
		}
		if strings.Contains(source, `GeneratedCustomMutationOperation("`+operationQueryExtension) {
			t.Fatal("page acquired mutation authority")
		}
		if withMutation != strings.Contains(source, "GeneratedApplicationOperations(") {
			t.Fatal("mutation registration was lost or invented")
		}
		surface, err := EmitSurface(SurfaceRequest{AppPackage: request.AppPackage, ModelPackages: request.ModelPackages, Actor: request.Actor, Model: request.Schema.Model, Contract: request.Schema.Contract})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"*golemruntime.PageConfig[Actor]", "engineConfig.Pages = config.Pages"} {
			if !strings.Contains(string(surface.Source), want) {
				t.Errorf("page surface missing %q", want)
			}
		}
	}
}

func TestOrdinaryRegistryOmitsPageConfiguration(t *testing.T) {
	request := operationRequest(t)
	final, err := Emit(request)
	if err != nil {
		t.Fatal(err)
	}
	surface, err := EmitSurface(SurfaceRequest{AppPackage: request.AppPackage, ModelPackages: request.ModelPackages, Actor: request.Actor, Model: request.Schema.Model, Contract: request.Schema.Contract})
	if err != nil {
		t.Fatal(err)
	}
	for name, file := range map[string]File{"final": final, "surface": surface} {
		for _, forbidden := range []string{"PageConfig[", "engineConfig.Pages", "config.Pages"} {
			if strings.Contains(string(file.Source), forbidden) {
				t.Errorf("ordinary %s registry contains %q", name, forbidden)
			}
		}
	}
}
