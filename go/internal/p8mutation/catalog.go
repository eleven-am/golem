package p8mutation

import "sort"

func Catalog() []Mutation {
	result := append([]Mutation{}, providerRuntimeMutations()...)
	result = append(result, observationWorkflowMutations()...)
	result = append(result, crossSurfaceMutations()...)
	result = append(result, runtimeResourceMutations()...)
	result = append(result, docsReleaseCompatibilityMutations()...)
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
