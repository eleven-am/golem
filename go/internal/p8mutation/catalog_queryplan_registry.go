package p8mutation

import "time"

func queryPlanRegistryMutations() []Mutation {
	gate := func(test string) Gate { return Gate{Directory: "go", Package: "./internal/policy/schema", Test: test} }
	return []Mutation{
		{
			Label: "QUERYPLAN_REGISTRY_UNKNOWN_TABLE_FALLBACK", Summary: "guess a model identity when a provider plan table name is unknown",
			Patches: []Patch{{Path: "go/internal/policy/schema/registry.go", Before: "func (registry *Registry) PhysicalModelIDByName(provider golem.Provider, name physical.PhysicalName) (golem.ModelID, bool) {\n\tif registry == nil {\n\t\treturn golem.ModelID{}, false\n\t}\n\tvalues, ok := registry.physicalModelNames[provider]\n\tif !ok {\n\t\treturn golem.ModelID{}, false\n\t}\n\tvalue, ok := values[name]\n\treturn value, ok\n}", After: "func (registry *Registry) PhysicalModelIDByName(provider golem.Provider, name physical.PhysicalName) (golem.ModelID, bool) {\n\tif registry == nil {\n\t\treturn golem.ModelID{}, false\n\t}\n\tvalues, ok := registry.physicalModelNames[provider]\n\tif !ok {\n\t\treturn golem.ModelID{}, false\n\t}\n\tvalue, ok := values[name]\n\tif !ok { for _, fallback := range values { return fallback, true } }\n\treturn value, ok\n}"}},
			Gate:    gate("TestRegistryPhysicalPlanObjectLookupFailsClosedForUnknownInputs"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_REGISTRY_IGNORES_PROVIDER_SCOPE", Summary: "resolve an access object from another provider snapshot",
			Patches: []Patch{{Path: "go/internal/policy/schema/registry.go", Before: "func (registry *Registry) PhysicalAccessObjectByName(provider golem.Provider, name physical.PhysicalName) (PhysicalAccessObject, bool) {\n\tif registry == nil {\n\t\treturn PhysicalAccessObject{}, false\n\t}\n\tvalues, ok := registry.physicalAccessObjects[provider]\n\tif !ok {\n\t\treturn PhysicalAccessObject{}, false\n\t}\n\tvalue, ok := values[name]\n\treturn value.clone(), ok\n}", After: "func (registry *Registry) PhysicalAccessObjectByName(provider golem.Provider, name physical.PhysicalName) (PhysicalAccessObject, bool) {\n\tif registry == nil {\n\t\treturn PhysicalAccessObject{}, false\n\t}\n\tvalues, ok := registry.physicalAccessObjects[provider]\n\tif !ok {\n\t\treturn PhysicalAccessObject{}, false\n\t}\n\tvalue, ok := values[name]\n\tif !ok { for _, fallback := range registry.physicalAccessObjects { if candidate, found := fallback[name]; found { return candidate.clone(), true } } }\n\treturn value.clone(), ok\n}"}},
			Gate:    gate("TestRegistryPhysicalPlanObjectLookupIsProviderScoped"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_REGISTRY_ACCEPTS_ACCESS_COLLISION", Summary: "overwrite one of two ambiguous provider access-object names",
			Patches: []Patch{{Path: "go/internal/policy/schema/bootstrap.go", Before: "if _, duplicate := values[name]; duplicate {", After: "if _, duplicate := values[name]; duplicate && false {"}},
			Gate:    gate("TestRegistryRejectsCrossTableAmbiguousPhysicalPlanAccessNameWithoutEcho"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_REGISTRY_EXPOSES_PHYSICAL_NAME", Summary: "retain a provider physical name on the typed access fact",
			Patches: []Patch{{Path: "go/internal/policy/schema/registry.go", Before: "\tfields  []golem.FieldID\n}", After: "\tfields       []golem.FieldID\n\tphysicalName physical.PhysicalName\n}"}},
			Gate:    gate("TestPhysicalPlanAccessFactContainsNoPhysicalNameSurface"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_REGISTRY_IGNORES_KEY_FIELD_ORDER", Summary: "match a reviewed composite key after its stable field order changes",
			Patches: []Patch{{Path: "go/internal/policy/schema/registry.go", Before: "if left[index] != right[index] {", After: "if false && left[index] != right[index] {"}},
			Gate:    gate("TestRegistryMapsPhysicalKeysByModelAndExactStableFieldSequence"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_REGISTRY_GUESSES_AMBIGUOUS_KEY", Summary: "choose one of two reviewed keys with the same provider field sequence",
			Patches: []Patch{{Path: "go/internal/policy/schema/registry.go", Before: "if found {\n\t\t\treturn PhysicalAccessObject{}, false\n\t\t}", After: "if false && found {\n\t\t\treturn PhysicalAccessObject{}, false\n\t\t}"}},
			Gate:    gate("TestRegistryPhysicalKeyFieldLookupRefusesAmbiguousReviewedSequence"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_REGISTRY_EXPOSES_KEY_FIELD_SLICE", Summary: "return registry-owned stable key field storage without copying",
			Patches: []Patch{{Path: "go/internal/policy/schema/registry.go", Before: "return append([]golem.FieldID(nil), value.fields...)", After: "return value.fields"}},
			Gate:    gate("TestRegistryMapsPhysicalKeysByModelAndExactStableFieldSequence"), Timeout: 2 * time.Minute,
		},
	}
}
