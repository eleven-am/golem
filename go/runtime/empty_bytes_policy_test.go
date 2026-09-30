package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestPolicyRulesOnEmptyBytesAdmitTheEmptyRow(t *testing.T) {
	runExactValueProfiles(t, func(t *testing.T, profile exactValueProfile) {
		ctx := context.Background()
		profile.seed(t)
		profile.storeEmptyBytes(t, 202)
		schema := profile.fixture
		bytesField := golem.GeneratedBytesField[mutationResultPost](schema.PostBytes)
		allowUsers := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[mutationResultUser]()
			rules.CanRead(golem.All[mutationResultUser]())
			return rules.Freeze(schema.User)
		})
		emptyOnly := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[mutationResultPost]()
			rules.CanRead(bytesField.Eq([]byte{}))
			return rules.Freeze(schema.Post)
		})
		bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(),
			golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{allowUsers, emptyOnly}, nil))
		if err != nil {
			t.Fatal(err)
		}
		reader := newConfiguredEmptyBlobReader(t, profile, func(config *Config[mutationResultPrincipal, mutationResultActor]) {
			config.Bindings = bindings
		})
		caller := mustMutationResultCaller(t, reader.fixture)
		rows, err := CallerFindMany(ctx, caller, reader.posts, golem.RuntimeProjectionReadOption(reader.projection()))
		if err != nil || len(rows) != 1 {
			t.Fatalf("policy on empty bytes rows=%d err=%v", len(rows), err)
		}
		reader.assertRow(t, "policy on empty bytes", rows[0], "exact")
		profile.storeBytes(t, 202, []byte{1})
		rows, err = CallerFindMany(ctx, caller, reader.posts, golem.RuntimeProjectionReadOption(reader.projection()))
		if err != nil || len(rows) != 0 {
			t.Fatalf("policy on empty bytes admitted a non-empty row rows=%d err=%v", len(rows), err)
		}
	})
}
