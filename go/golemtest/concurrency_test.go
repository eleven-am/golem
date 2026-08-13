package golemtest

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
)

func concurrencyActors() []p5social.Actor {
	actors := make([]p5social.Actor, 0, 8)
	for index := 0; index < 8; index++ {
		actors = append(actors, p5social.Actor{
			UserID:     golem.UUID{byte(index + 1), 0x0a, 0x0b, 0x0c},
			AllowedTag: fmt.Sprintf("tag-%d", index),
		})
	}
	return actors
}

func policyEvidence(policies PolicySet) map[golem.ModelID]string {
	evidence := make(map[golem.ModelID]string, len(policies.policies))
	for id, policy := range policies.policies {
		evidence[id] = hex.EncodeToString(policy.CanonicalBytes())
	}
	return evidence
}

func TestPolicyTestKitConcurrentActorsNeverSharePolicyState(t *testing.T) {
	kit := socialKit(t)
	actors := concurrencyActors()

	bindings, err := p5social.GolemGeneratedApplicationBindings()
	if err != nil {
		t.Fatal(err)
	}
	generated := make([]map[golem.ModelID]string, len(actors))
	for index, actor := range actors {
		set, err := golem.BuildGeneratedPolicySet(bindings, actor)
		if err != nil {
			t.Fatalf("BuildGeneratedPolicySet: %v", err)
		}
		generated[index] = make(map[golem.ModelID]string, len(set.Policies()))
		for _, policy := range set.Policies() {
			generated[index][policy.View().ModelID()] = hex.EncodeToString(policy.CanonicalBytes())
		}
	}
	distinguishing := make([]golem.ModelID, 0)
	for id, evidence := range generated[0] {
		if generated[1][id] != evidence {
			distinguishing = append(distinguishing, id)
		}
	}
	if len(distinguishing) == 0 {
		t.Fatal("these application policies do not depend on the actor at all, so this gate proves nothing")
	}

	expected := make([]map[golem.ModelID]string, len(actors))
	for index, actor := range actors {
		policies, err := kit.ForActor(actor)
		if err != nil {
			t.Fatalf("ForActor: %v", err)
		}
		expected[index] = policyEvidence(policies)
	}

	for _, id := range distinguishing {
		for index := 1; index < len(actors); index++ {
			if expected[index][id] == expected[0][id] {
				t.Fatalf("the kit served actor %d the policy state built for actor 0 on model %s", index, hex.EncodeToString(id[:]))
			}
		}
		for index := range actors {
			if expected[index][id] != generated[index][id] {
				t.Fatalf("the kit served actor %d policy state that the production builder did not produce for it", index)
			}
		}
	}

	const rounds = 48
	retained := make([][]PolicySet, len(actors))
	var group sync.WaitGroup
	var mutex sync.Mutex
	failures := make([]string, 0)

	report := func(message string) {
		mutex.Lock()
		failures = append(failures, message)
		mutex.Unlock()
	}

	for index, actor := range actors {
		retained[index] = make([]PolicySet, rounds)
		group.Add(1)
		go func(index int, actor p5social.Actor) {
			defer group.Done()
			for round := 0; round < rounds; round++ {
				policies, err := kit.ForActor(actor)
				if err != nil {
					report(fmt.Sprintf("actor %d round %d: ForActor failed", index, round))
					return
				}
				retained[index][round] = policies

				observed := policyEvidence(policies)
				if len(observed) != len(expected[index]) {
					report(fmt.Sprintf("actor %d round %d: built %d policies, want %d", index, round, len(observed), len(expected[index])))
					return
				}
				for id, evidence := range expected[index] {
					if observed[id] != evidence {
						report(fmt.Sprintf("actor %d round %d: model %s carries another actor's policy evidence", index, round, hex.EncodeToString(id[:])))
						return
					}
				}

				tags, err := Model(policies, p5social.GolemGeneratedTagDescriptor)
				if err != nil {
					report(fmt.Sprintf("actor %d round %d: Model failed", index, round))
					return
				}
				if hex.EncodeToString(tags.policy.CanonicalBytes()) != expected[index][tags.model] {
					report(fmt.Sprintf("actor %d round %d: model policy carries another actor's policy evidence", index, round))
					return
				}
			}
		}(index, actor)
	}
	group.Wait()

	if len(failures) != 0 {
		t.Fatalf("concurrent actors shared policy state: %s", failures[0])
	}

	seen := make(map[uintptr]struct{})
	for index := range retained {
		for round := range retained[index] {
			pointer := reflect.ValueOf(retained[index][round].policies).Pointer()
			if pointer == 0 {
				t.Fatal("a concurrent build produced no policy state")
			}
			if _, duplicate := seen[pointer]; duplicate {
				t.Fatal("two concurrent builds returned the same policy state")
			}
			seen[pointer] = struct{}{}
		}
	}
	runtime.KeepAlive(retained)
}
