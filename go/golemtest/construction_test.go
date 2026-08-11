package golemtest

import (
	"os/exec"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/runtime/testdata/p5social"
	"github.com/eleven-am/golem/go/runtime/testdata/p5socialactive"
)

func socialKit(t *testing.T) *Kit[p5social.Actor] {
	t.Helper()
	bindings, err := p5social.GolemGeneratedApplicationBindings()
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := p5social.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	kit, err := New(bindings, descriptors, p5social.GolemGeneratedSchemaBundle())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return kit
}

func TestPolicyTestKitBuildsFreshActorScopedPoliciesWithoutDatabase(t *testing.T) {
	t.Run("NewRunsNoPolicyFactory", func(t *testing.T) {
		p5social.ResetPolicyProbe()
		socialKit(t)
		if invoked := p5social.PolicyProbe(); len(invoked) != 0 {
			t.Fatalf("New invoked %d policy factories, want 0", len(invoked))
		}
	})

	t.Run("ForActorInvokesEveryFactoryExactlyOnce", func(t *testing.T) {
		kit := socialKit(t)
		p5social.ResetPolicyProbe()
		if _, err := kit.ForActor(p5social.Actor{AllowedTag: "alpha"}); err != nil {
			t.Fatalf("ForActor: %v", err)
		}
		invoked := append([]string(nil), p5social.PolicyProbe()...)
		sort.Strings(invoked)
		expected := []string{"Comment", "Friendship", "Post", "PostTag", "Tag", "User"}
		if strings.Join(invoked, ",") != strings.Join(expected, ",") {
			t.Fatalf("factories invoked [%s], want [%s]", strings.Join(invoked, ","), strings.Join(expected, ","))
		}
	})

	t.Run("EveryCallRebuildsWithNoCrossCallCache", func(t *testing.T) {
		kit := socialKit(t)
		p5social.ResetPolicyProbe()
		first, err := kit.ForActor(p5social.Actor{AllowedTag: "alpha"})
		if err != nil {
			t.Fatalf("ForActor: %v", err)
		}
		second, err := kit.ForActor(p5social.Actor{AllowedTag: "alpha"})
		if err != nil {
			t.Fatalf("ForActor: %v", err)
		}
		if invoked := len(p5social.PolicyProbe()); invoked != 12 {
			t.Fatalf("two calls invoked %d factories, want 12", invoked)
		}
		if reflect.ValueOf(first.policies).Pointer() == reflect.ValueOf(second.policies).Pointer() {
			t.Fatal("two calls returned the same policy map")
		}
		for id, policy := range first.policies {
			other, ok := second.policies[id]
			if !ok {
				t.Fatal("the second call omitted a model built by the first")
			}
			if string(policy.CanonicalBytes()) != string(other.CanonicalBytes()) {
				t.Fatal("the same actor produced different policy evidence across calls")
			}
		}
	})

	t.Run("PolicySetRetainsNoActorValue", func(t *testing.T) {
		kit := socialKit(t)
		policies, err := kit.ForActor(p5social.Actor{UserID: golem.UUID{0x09}, AllowedTag: "alpha"})
		if err != nil {
			t.Fatalf("ForActor: %v", err)
		}
		kind := reflect.TypeOf(policies)
		actorKind := reflect.TypeOf(p5social.Actor{})
		for index := 0; index < kind.NumField(); index++ {
			if kind.Field(index).Type == actorKind {
				t.Fatal("the policy set retains an actor instance")
			}
		}
		if kind.Name() != "PolicySet" || kind.NumField() != 6 {
			t.Fatalf("policy set shape changed: %d fields", kind.NumField())
		}
	})

	t.Run("ForActorInvokesNoApplicationHook", func(t *testing.T) {
		bindings, err := p5socialactive.GolemGeneratedApplicationBindings()
		if err != nil {
			t.Fatal(err)
		}
		descriptors, err := p5socialactive.GolemGeneratedApplicationDescriptors()
		if err != nil {
			t.Fatal(err)
		}
		if len(bindings.RuntimeHookInventory()) == 0 {
			t.Fatal("the hook fixture declares no hooks, so this gate proves nothing")
		}
		kit, err := New(bindings, descriptors, p5socialactive.GolemGeneratedSchemaBundle())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		p5socialactive.ResetHooks()
		if _, err := kit.ForActor(p5socialactive.Actor{}); err != nil {
			t.Fatalf("ForActor: %v", err)
		}
		if snapshot := p5socialactive.SnapshotHooks(); snapshot != (p5socialactive.HookSnapshot{}) {
			t.Fatalf("ForActor invoked application hooks: %+v", snapshot)
		}
	})

	t.Run("NoGoroutineIsStarted", func(t *testing.T) {
		before := runtime.NumGoroutine()
		kit := socialKit(t)
		if _, err := kit.ForActor(p5social.Actor{AllowedTag: "alpha"}); err != nil {
			t.Fatalf("ForActor: %v", err)
		}
		if after := runtime.NumGoroutine(); after != before {
			t.Fatalf("goroutine count moved from %d to %d", before, after)
		}
	})

	t.Run("DependencyClosureCannotOpenADatabaseOrNetwork", func(t *testing.T) {
		command := exec.Command("go", "list", "-deps", packageImportPath)
		command.Dir = packageDirectory(t)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps: %v\n%s", err, output)
		}
		forbidden := map[string]bool{
			"database/sql":                            true,
			"database/sql/driver":                     true,
			"net":                                     true,
			"net/http":                                true,
			"os/exec":                                 true,
			"github.com/jmoiron/sqlx":                 true,
			"github.com/jackc/pgx/v5":                 true,
			"github.com/eleven-am/golem/go/provider":  true,
			"github.com/eleven-am/golem/go/runtime":   true,
			"github.com/eleven-am/golem/go/graphql":   true,
			"github.com/eleven-am/golem/go/events":    true,
			"github.com/eleven-am/golem/go/observe":   true,
			"github.com/eleven-am/golem/go/embedding": true,
		}
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if forbidden[strings.TrimSpace(line)] {
				t.Errorf("the package depends on %s, which can open a database, network, or runtime", line)
			}
		}
	})
}
