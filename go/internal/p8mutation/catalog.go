package p8mutation

import "sort"

func Catalog() []Mutation {
	result := append([]Mutation{}, providerRuntimeMutations()...)
	result = append(result, observationWorkflowMutations()...)
	result = append(result, crossSurfaceMutations()...)
	result = append(result, runtimeResourceMutations()...)
	result = append(result, docsReleaseCompatibilityMutations()...)
	result = append(result, policyTestingKitMutations()...)
	result = append(result, queryPlanCoreMutations()...)
	result = append(result, queryPlanRegistryMutations()...)
	result = append(result, queryPlanPolicyAliasMutations()...)
	result = append(result, queryPlanReadAliasMutations()...)
	result = append(result, queryPlanScopedAliasMutations()...)
	result = append(result, queryPlanAnalyticsAliasMutations()...)
	result = append(result, queryPlanProviderCaptureMutations()...)
	result = append(result, queryPlanTypedBuilderMutations()...)
	result = append(result, queryPlanRuntimeMutations()...)
	result = append(result, optimisticConcurrencyContractMutations()...)
	result = append(result, roadmapCoreMutations()...)
	sort.Slice(result, func(i, j int) bool { return result[i].Label < result[j].Label })
	return result
}

func Find(label string) (Mutation, bool) {
	for _, mutation := range Catalog() {
		if mutation.Label == label {
			return mutation, true
		}
	}
	return Mutation{}, false
}
