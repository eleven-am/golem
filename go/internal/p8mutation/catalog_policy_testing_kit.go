package p8mutation

import "time"

func policyTestingKitMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{Directory: "go", Package: "./golemtest", Test: test}
	}
	return []Mutation{
		{
			Label:   "POLICY_KIT_DROP_DENY_RULE",
			Summary: "discard deny rules while resolving the effective row constraint",
			Patches: []Patch{{
				Path: "go/internal/policy/resolve/resolve.go",
				Before: `	openGrant := false

	for _, rule := range chain {
		trace = append(trace, rule.Position())
		condition, conditional := rule.Condition()
`,
				After: `	openGrant := false

	for _, rule := range chain {
		trace = append(trace, rule.Position())
		if rule.Effect() == ir.EffectDeny {
			continue
		}
		condition, conditional := rule.Condition()
`,
			}},
			Gate:    gate("TestPolicyTestKitRowConstraintMatchesProductionResolver"),
			Timeout: 5 * time.Minute,
		},
		{
			Label:   "POLICY_KIT_CONDITIONAL_ACCESS_ALWAYS",
			Summary: "classify every conditional read grant as unconditionally readable",
			Patches: []Patch{{
				Path:   "go/internal/policy/classify/classify.go",
				Before: "\t\tclassification.access = AccessConditional\n\t\tclassification.condition = fieldCondition\n",
				After:  "\t\tclassification.access = AccessAlways\n\t\tclassification.condition = fieldCondition\n",
			}},
			Gate:    gate("TestPolicyTestKitFieldClassificationMatchesRuntimeMasking"),
			Timeout: 5 * time.Minute,
		},
		{
			Label:   "POLICY_KIT_OMIT_RELATION_DEPENDENCY",
			Summary: "omit a relation hop and its target subtree from conditional-field hydration",
			Patches: []Patch{{
				Path: "go/internal/policy/dependency/dependency.go",
				Before: `		return collector.tree.merge(Entry{field: field, kind: Relation, target: target, children: children})
`,
				After: `		_ = Entry{field: field, kind: Relation, target: target, children: children}
		return nil
`,
			}},
			Gate:    gate("TestPolicyTestKitRelationDependencyTreeMatchesRuntimeHydration"),
			Timeout: 5 * time.Minute,
		},
		{
			Label:   "POLICY_KIT_UNPROVED_CONDITIONAL_DISCHARGED",
			Summary: "mark a conditional field discharged without proving the statement reach implies it",
			Patches: []Patch{{
				Path:   "go/internal/policy/classify/classify.go",
				Before: "\t\tclassification.discharged = discharged\n",
				After:  "\t\t_ = discharged\n\t\tclassification.discharged = true\n",
			}},
			Gate:    gate("TestPolicyTestKitNarrowerReachDischargesButNeverWidensPolicy"),
			Timeout: 5 * time.Minute,
		},
		{
			Label:   "POLICY_KIT_ACCEPT_WIDENED_REACH",
			Summary: "accept a caller reach that does not preserve the actor's action constraint",
			Patches: []Patch{{
				Path:   "go/internal/policy/classify/classify.go",
				Before: "\t\tif !proved {\n\t\t\treturn Request{}, fmt.Errorf(\"policy classify: actual selecting constraint does not preserve policy reach\")\n\t\t}\n",
				After:  "\t\tif false && !proved {\n\t\t\treturn Request{}, fmt.Errorf(\"policy classify: actual selecting constraint does not preserve policy reach\")\n\t\t}\n",
			}},
			Gate:    gate("TestPolicyTestKitNarrowerReachDischargesButNeverWidensPolicy"),
			Timeout: 5 * time.Minute,
		},
		{
			Label:   "POLICY_KIT_REUSE_ACTOR_POLICY_SET",
			Summary: "cache the first actor's compiled policy set and return it to every later actor",
			Patches: []Patch{
				{
					Path:   "go/golemtest/kit.go",
					Before: "import (\n",
					After:  "import (\n\t\"sync\"\n\n",
				},
				{
					Path: "go/golemtest/kit.go",
					Before: `type Kit[A any] struct {
	generation golem.SchemaDigest
	bindings   golem.ApplicationBindings[A]
	order      []golem.ModelID
	models     map[golem.ModelID]golem.ModelMetadata
	registry   *schema.Registry
	providers  ir.ProviderSet
}
`,
					After: `type Kit[A any] struct {
	generation golem.SchemaDigest
	bindings   golem.ApplicationBindings[A]
	order      []golem.ModelID
	models     map[golem.ModelID]golem.ModelMetadata
	registry   *schema.Registry
	providers  ir.ProviderSet
	mutex      sync.Mutex
	cached     PolicySet
	cachedSet  bool
}
`,
				},
				{
					Path: "go/golemtest/kit.go",
					Before: `	if kit == nil || kit.generation == (golem.SchemaDigest{}) || len(kit.models) == 0 || kit.registry == nil {
		return PolicySet{}, fail(ErrorInvalidInput, "policy kit was not produced by New")
	}

	built, err := buildGeneratedPolicySet(kit.bindings, actor)
`,
					After: `	if kit == nil || kit.generation == (golem.SchemaDigest{}) || len(kit.models) == 0 || kit.registry == nil {
		return PolicySet{}, fail(ErrorInvalidInput, "policy kit was not produced by New")
	}
	kit.mutex.Lock()
	defer kit.mutex.Unlock()
	if kit.cachedSet {
		return kit.cached, nil
	}

	built, err := buildGeneratedPolicySet(kit.bindings, actor)
`,
				},
				{
					Path: "go/golemtest/kit.go",
					Before: `	return PolicySet{
		generation: kit.generation,
		models:     kit.models,
		policies:   policies,
		bound:      bound,
		registry:   kit.registry,
		providers:  kit.providers,
	}, nil
`,
					After: `	result := PolicySet{
		generation: kit.generation,
		models:     kit.models,
		policies:   policies,
		bound:      bound,
		registry:   kit.registry,
		providers:  kit.providers,
	}
	kit.cached = result
	kit.cachedSet = true
	return result, nil
`,
				},
			},
			Gate:    gate("TestPolicyTestKitConcurrentActorsNeverSharePolicyState"),
			Timeout: 5 * time.Minute,
		},
		{
			Label:   "POLICY_KIT_ACCEPT_FOREIGN_GENERATION_DESCRIPTOR",
			Summary: "accept byte-identical model metadata while ignoring its foreign generation digest",
			Patches: []Patch{{
				Path:   "go/golemtest/kit.go",
				Before: "\tif descriptor.GenerationDigest() == (golem.SchemaDigest{}) || descriptor.GenerationDigest() != policies.generation {\n",
				After:  "\tif false && (descriptor.GenerationDigest() == (golem.SchemaDigest{}) || descriptor.GenerationDigest() != policies.generation) {\n",
			}},
			Gate:    gate("TestPolicyTestKitRejectsForeignGenerationModelAndFieldHandles"),
			Timeout: 5 * time.Minute,
		},
		{
			Label:   "POLICY_KIT_ACCEPT_FOREIGN_GENERATION_FIELD",
			Summary: "accept a byte-identical scalar or relation field carrying a foreign generation digest",
			Patches: []Patch{{
				Path:   "go/golemtest/classify.go",
				Before: "\t\tif !stamped || generation != policy.generation {\n",
				After:  "\t\tif false && (!stamped || generation != policy.generation) {\n",
			}},
			Gate:    gate("TestPolicyTestKitRejectsForeignGenerationModelAndFieldHandles"),
			Timeout: 5 * time.Minute,
		},
		{
			Label:   "POLICY_KIT_EXPOSE_FACTORY_PANIC_PAYLOAD",
			Summary: "embed the recovered policy-factory panic payload in the public closed error",
			Patches: []Patch{
				{
					Path:   "go/golemtest/kit.go",
					Before: "import (\n",
					After:  "import (\n\t\"fmt\"\n\n",
				},
				{
					Path: "go/golemtest/kit.go",
					Before: `	defer func() {
		if recover() != nil {
			set = golem.GeneratedPolicySet{}
			err = fail(ErrorPolicyFactory, "a generated policy factory panicked")
		}
	}()
`,
					After: `	defer func() {
		if recovered := recover(); recovered != nil {
			set = golem.GeneratedPolicySet{}
			err = fail(ErrorPolicyFactory, fmt.Sprintf("a generated policy factory panicked: %v", recovered))
		}
	}()
`,
				},
			},
			Gate:    gate("TestPolicyTestKitFactoryPanicAndErrorsAreClosedAndRedacted"),
			Timeout: 5 * time.Minute,
		},
	}
}
