package plan

import (
	"errors"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

// legacyMutationEventSchema is a verbatim transcription of the runtime helper
// every prepare site consults today. It is duplicated into the test on purpose:
// a gate that called the production helper would only prove the production
// helper agrees with itself once the six call sites are collapsed into it.
func legacyMutationEventSchema(registry *schema.Registry, model policyir.ModelID) (mutationir.FactCodecRequirement, [32]byte, []policyir.FieldID, error) {
	if registry == nil {
		return mutationir.FactCodecRequirement{}, [32]byte{}, nil, errors.New("registry is absent")
	}
	metadata, ok := registry.Model(golem.ModelID(model))
	if !ok || !metadata.SubscriptionsEnabled() {
		return mutationir.FactCodecRequirement{}, [32]byte{}, nil, errors.New("model is not subscription-enabled")
	}
	fingerprint, snapshot, ok := metadata.EventSchema()
	if !ok {
		return mutationir.FactCodecRequirement{}, [32]byte{}, nil, errors.New("normalized event schema is absent")
	}
	digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if err != nil {
		return mutationir.FactCodecRequirement{}, [32]byte{}, nil, err
	}
	codec, err := mutationir.NewFactCodecRequirement(mutationfact.FormatVersionV2, mutationfact.CodecIdentityV2, [32]byte(registry.GenerationDigest()))
	if err != nil {
		return mutationir.FactCodecRequirement{}, [32]byte{}, nil, err
	}
	fields := make([]policyir.FieldID, len(snapshot))
	for index, field := range snapshot {
		fields[index] = policyir.FieldID(field)
	}
	return codec, [32]byte(digest), fields, nil
}

// legacyFacts is the exact tuple one runtime prepare site used to write into
// RootRequest before durable capture moved into BuildRoot.
type legacyFacts struct {
	capture     bool
	codec       mutationir.FactCodecRequirement
	eventSchema [32]byte
	snapshot    []policyir.FieldID
}

// legacyFactBlock is one runtime prepare site's fact block, transcribed. The
// six differ only in how they spell the registry lookup and which operations
// they ask for a private pre-delete snapshot. They are the oracle this gate
// checks BuildRoot against, which is why they are duplicated here rather than
// delegated to the production helper.
type legacyFactBlock struct {
	site       string
	operations []mutationir.Operation
	apply      func(testing.TB, *schema.Registry, policyir.ModelID, mutationir.Operation) legacyFacts
}

func legacyPrepareSites() []legacyFactBlock {
	return []legacyFactBlock{
		{
			site:       "prepareScalarProgram",
			operations: []mutationir.Operation{mutationir.Create, mutationir.Update, mutationir.Delete},
			apply: func(t testing.TB, registry *schema.Registry, model policyir.ModelID, operation mutationir.Operation) legacyFacts {
				var facts legacyFacts
				modelFact, _ := registry.Model(golem.ModelID(model))
				if modelFact.SubscriptionsEnabled() {
					factCodec, eventSchema, snapshot, err := legacyMutationEventSchema(registry, model)
					if err != nil {
						t.Fatal(err)
					}
					facts.capture, facts.codec, facts.eventSchema = true, factCodec, eventSchema
					if operation == mutationir.Delete {
						facts.snapshot = snapshot
					}
				}
				return facts
			},
		},
		{
			site:       "prepareRootUpsert",
			operations: []mutationir.Operation{mutationir.Upsert},
			apply: func(t testing.TB, registry *schema.Registry, model policyir.ModelID, _ mutationir.Operation) legacyFacts {
				var facts legacyFacts
				modelFact, _ := registry.Model(golem.ModelID(model))
				if modelFact.SubscriptionsEnabled() {
					factCodec, eventSchema, _, err := legacyMutationEventSchema(registry, model)
					if err != nil {
						t.Fatal(err)
					}
					facts.capture, facts.codec, facts.eventSchema = true, factCodec, eventSchema
				}
				return facts
			},
		},
		{
			site:       "prepareFrozenBatchProgram",
			operations: []mutationir.Operation{mutationir.UpdateMany, mutationir.DeleteMany},
			apply: func(t testing.TB, registry *schema.Registry, model policyir.ModelID, operation mutationir.Operation) legacyFacts {
				var facts legacyFacts
				modelFact, _ := registry.Model(golem.ModelID(model))
				if modelFact.SubscriptionsEnabled() {
					factCodec, eventSchema, snapshot, err := legacyMutationEventSchema(registry, model)
					if err != nil {
						t.Fatal(err)
					}
					facts.capture, facts.codec, facts.eventSchema = true, factCodec, eventSchema
					if operation == mutationir.DeleteMany {
						facts.snapshot = snapshot
					}
				}
				return facts
			},
		},
		{
			site:       "prepareNestedCompilation",
			operations: []mutationir.Operation{mutationir.Create, mutationir.Update, mutationir.Delete},
			apply: func(t testing.TB, registry *schema.Registry, model policyir.ModelID, operation mutationir.Operation) legacyFacts {
				var facts legacyFacts
				if metadata, ok := registry.Model(golem.ModelID(model)); ok && metadata.SubscriptionsEnabled() {
					codec, eventSchema, snapshot, err := legacyMutationEventSchema(registry, model)
					if err != nil {
						t.Fatal(err)
					}
					facts.capture, facts.codec, facts.eventSchema = true, codec, eventSchema
					if operation == mutationir.Delete {
						facts.snapshot = snapshot
					}
				}
				return facts
			},
		},
		{
			site:       "compileBeforeParentHookReplacement",
			operations: []mutationir.Operation{mutationir.Create},
			apply: func(t testing.TB, registry *schema.Registry, model policyir.ModelID, _ mutationir.Operation) legacyFacts {
				var facts legacyFacts
				if metadata, present := registry.Model(golem.ModelID(model)); present && metadata.SubscriptionsEnabled() {
					codec, eventSchema, _, err := legacyMutationEventSchema(registry, model)
					if err != nil {
						t.Fatal(err)
					}
					facts.capture, facts.codec, facts.eventSchema = true, codec, eventSchema
				}
				return facts
			},
		},
		{
			site:       "compileExactHookReplacement",
			operations: []mutationir.Operation{mutationir.Update, mutationir.Delete},
			apply: func(t testing.TB, registry *schema.Registry, model policyir.ModelID, operation mutationir.Operation) legacyFacts {
				var facts legacyFacts
				var deleteSnapshot []policyir.FieldID
				if metadata, ok := registry.Model(golem.ModelID(model)); ok && metadata.SubscriptionsEnabled() {
					codec, eventSchema, snapshot, err := legacyMutationEventSchema(registry, model)
					if err != nil {
						t.Fatal(err)
					}
					facts.capture, facts.codec, facts.eventSchema = true, codec, eventSchema
					deleteSnapshot = snapshot
				}
				if operation == mutationir.Delete {
					facts.snapshot = deleteSnapshot
				}
				return facts
			},
		},
	}
}

type factShape struct {
	Operation     mutationir.Operation
	Enabled       bool
	Action        mutationir.FactAction
	ActionPresent bool
	Before        []policyir.FieldID
	After         []policyir.FieldID
	SnapshotState mutationir.DeleteSnapshotState
	Snapshot      []policyir.FieldID
	EventSchema   [32]byte
	SchemaPresent bool
}

type planFactShape struct {
	Codec          mutationir.FactCodecRequirement
	CodecRequested bool
	Nodes          []factShape
}

func shapeOfPlan(t testing.TB, planned mutationir.Plan) planFactShape {
	t.Helper()
	nodes := planned.Graph().Nodes()
	if len(nodes) == 0 {
		t.Fatal("planned graph has no nodes")
	}
	codec, codecRequested := planned.FactCodecRequirement()
	shape := planFactShape{Codec: codec, CodecRequested: codecRequested, Nodes: make([]factShape, len(nodes))}
	for index, node := range nodes {
		fact := node.Fact()
		action, actionPresent := fact.Action()
		eventSchema, schemaPresent := fact.EventSchema()
		shape.Nodes[index] = factShape{
			Operation: node.Operation(), Enabled: fact.Enabled(), Action: action, ActionPresent: actionPresent,
			Before: fact.BeforeIdentity(), After: fact.AfterIdentity(),
			SnapshotState: fact.DeleteSnapshotState(), Snapshot: fact.PrivateDeleteSnapshot(),
			EventSchema: eventSchema, SchemaPresent: schemaPresent,
		}
	}
	return shape
}

func configureFactShape(t testing.TB, fixture schematest.Fixture, request *RootRequest, operation mutationir.Operation) {
	t.Helper()
	inputs := boundInputs(t, fixture)
	predicate := boundBatchPredicate(t, fixture)
	switch operation {
	case mutationir.Create:
		request.Create = &inputs.create
	case mutationir.Update:
		request.Target, request.Update = &inputs.target, &inputs.update
	case mutationir.Delete:
		request.Target = &inputs.target
	case mutationir.Upsert:
		request.Target, request.Create, request.Update = &inputs.target, &inputs.create, &inputs.update
		request.Retry = mutationir.EngineOwnedUpsertRetry
	case mutationir.UpdateMany:
		request.Predicate, request.Update = &predicate, &inputs.updateMany
	case mutationir.DeleteMany:
		request.Predicate = &predicate
	default:
		t.Fatalf("unsupported operation %d", operation)
	}
}

// expectedPlanFactShape is the plan the transcribed prepare site would have
// produced. Everything it asserts comes from the site block, never from the
// derivation under test.
func expectedPlanFactShape(t testing.TB, fixture schematest.Fixture, model policyir.ModelID, planned mutationir.Plan, facts legacyFacts) planFactShape {
	t.Helper()
	metadata, ok := fixture.Registry.Model(golem.ModelID(model))
	if !ok {
		t.Fatal("model is absent from registry")
	}
	nodes := planned.Graph().Nodes()
	shape := planFactShape{Nodes: make([]factShape, len(nodes))}
	if !facts.capture {
		for index, node := range nodes {
			shape.Nodes[index] = factShape{Operation: node.Operation()}
		}
		return shape
	}
	identity := make([]policyir.FieldID, len(metadata.PrimaryKey()))
	for index, field := range metadata.PrimaryKey() {
		identity[index] = policyir.FieldID(field)
	}
	shape.Codec, shape.CodecRequested = facts.codec, true
	for index, node := range nodes {
		entry := factShape{Operation: node.Operation(), Enabled: true, ActionPresent: true, EventSchema: facts.eventSchema, SchemaPresent: true}
		switch node.Operation() {
		case mutationir.Create:
			entry.Action, entry.After = mutationir.FactCreated, identity
		case mutationir.Update, mutationir.UpdateMany:
			entry.Action, entry.Before, entry.After = mutationir.FactUpdated, identity, identity
		case mutationir.Delete, mutationir.DeleteMany:
			entry.Action, entry.Before = mutationir.FactDeleted, identity
			entry.SnapshotState, entry.Snapshot = mutationir.DeleteSnapshotUnverifiable, nil
			if facts.snapshot != nil {
				entry.SnapshotState, entry.Snapshot = mutationir.DeleteSnapshotStoredScalars, facts.snapshot
			}
		default:
			entry = factShape{Operation: node.Operation()}
		}
		shape.Nodes[index] = entry
	}
	return shape
}

// TestRootFactRequirementIsIdenticalAtEveryPrepareSite is the equivalence gate
// that must hold both before and after fact capture moves into BuildRoot. It
// asserts every prepare site that can plan a given operation produces exactly
// the same fact requirement, and that the shared result is the one derivable
// from the registry and the operation kind alone.
func TestRootFactRequirementIsIdenticalAtEveryPrepareSite(t *testing.T) {
	for _, subscribed := range []bool{false, true} {
		subscribed := subscribed
		name := "unsubscribed"
		if subscribed {
			name = "subscribed"
		}
		t.Run(name, func(t *testing.T) {
			fixture := schematest.NewIndexed(t)
			if subscribed {
				fixture = schematest.NewSubscribedIndexed(t)
			}
			model := policyir.ModelID(fixture.Post)
			policies := policySet(fixture, allowAllPolicy(t, model))
			for _, operation := range []mutationir.Operation{mutationir.Create, mutationir.Update, mutationir.Delete, mutationir.Upsert, mutationir.UpdateMany, mutationir.DeleteMany} {
				operation := operation
				t.Run(operationGateName(operation), func(t *testing.T) {
					sites := 0
					for _, block := range legacyPrepareSites() {
						if !planSiteSupports(block, operation) {
							continue
						}
						sites++
						request := baseRequest(t, fixture, policies, mutationir.Caller, operation)
						configureFactShape(t, fixture, &request, operation)
						planned, err := BuildRoot(request)
						if err != nil {
							t.Fatalf("%s: %v (cause: %v)", block.site, err, errors.Unwrap(err))
						}
						want := expectedPlanFactShape(t, fixture, model, planned, block.apply(t, fixture.Registry, model, operation))
						if got := shapeOfPlan(t, planned); !reflect.DeepEqual(got, want) {
							t.Fatalf("%s fact shape\n got=%+v\nwant=%+v", block.site, got, want)
						}
					}
					if sites == 0 {
						t.Fatalf("no prepare site plans operation %d", operation)
					}
				})
			}
		})
	}
}

func planSiteSupports(block legacyFactBlock, operation mutationir.Operation) bool {
	for _, candidate := range block.operations {
		if candidate == operation {
			return true
		}
	}
	return false
}

func operationGateName(operation mutationir.Operation) string {
	switch operation {
	case mutationir.Create:
		return "create"
	case mutationir.Update:
		return "update"
	case mutationir.Delete:
		return "delete"
	case mutationir.Upsert:
		return "upsert"
	case mutationir.UpdateMany:
		return "update-many"
	case mutationir.DeleteMany:
		return "delete-many"
	}
	return "unknown"
}
