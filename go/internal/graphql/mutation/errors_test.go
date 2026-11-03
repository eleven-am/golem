package mutation

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/graphql/compileerr"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type refusal struct {
	name     string
	kind     compileerr.Kind
	message  string
	argument string
}

func assertRefusal(t *testing.T, err error, want refusal) {
	t.Helper()
	var typed *compileerr.Error
	if !errors.As(err, &typed) {
		t.Fatalf("%s: error %v is not a compileerr.Error", want.name, err)
	}
	if typed.Kind() != want.kind || typed.Message() != want.message || typed.Argument() != want.argument {
		t.Fatalf("%s: kind=%d message=%q argument=%q want kind=%d message=%q argument=%q", want.name, typed.Kind(), typed.Message(), typed.Argument(), want.kind, want.message, want.argument)
	}
}

func versionedCompilation(t *testing.T) (compilerir.CompilationIR, mutationIDs) {
	t.Helper()
	compilation, ids := mutationCompilation()
	contracts := append([]compilerir.ModelContractIR(nil), compilation.Contract.Models...)
	for index := range contracts {
		if contracts[index].ModelID == ids.post {
			concurrency := ids.postID
			contracts[index].OptimisticConcurrency = &concurrency
		}
	}
	compilation.Contract.Models = contracts
	return compilation, ids
}

type writeCase struct {
	refusal
	operation RootOperation
	model     func(mutationIDs) compilerir.ModelID
	arguments map[string]any
	limits    Limits
}

func userModel(ids mutationIDs) compilerir.ModelID { return ids.user }
func postModel(ids mutationIDs) compilerir.ModelID { return ids.post }

func object(pairs ...any) map[string]any {
	result := map[string]any{}
	for index := 0; index < len(pairs); index += 2 {
		result[pairs[index].(string)] = pairs[index+1]
	}
	return result
}

func TestLowerValuesRefusalsAreTypedLocatedAndNeverEchoCallerText(t *testing.T) {
	const marker = "private-driver-token"
	cases := []writeCase{
		{refusal{"create-where-argument", compileerr.KindFieldUnavailable, "Argument where on User is not available for this operation", "where"}, Create, userModel, object("data", object(), "where", object("id", int32(1))), Limits{}},
		{refusal{"create-data-absent", compileerr.KindRequired, "Argument data on User is required", "data"}, Create, userModel, object(), Limits{}},
		{refusal{"create-data-null", compileerr.KindNullNotAllowed, "Argument data on User cannot be null", "data"}, Create, userModel, object("data", nil), Limits{}},
		{refusal{"create-data-not-object", compileerr.KindInvalidValue, "Argument data on User has the wrong kind of value", "data"}, Create, userModel, object("data", marker), Limits{}},
		{refusal{"create-unknown-field", compileerr.KindFieldUnavailable, "Input data.nope on User is not available for this operation", "data.nope"}, Create, userModel, object("data", object("nope", marker)), Limits{}},
		{refusal{"create-null-on-required-field", compileerr.KindNullNotAllowed, "Input data.handle on User cannot be null", "data.handle"}, Create, userModel, object("data", object("handle", nil)), Limits{}},
		{refusal{"create-wrong-value-kind", compileerr.KindInvalidValue, "Input data.handle on User must be of type String", "data.handle"}, Create, userModel, object("data", object("handle", int32(5))), Limits{}},
		{refusal{"update-data-empty", compileerr.KindEmpty, "Argument data on User must not be empty", "data"}, Update, userModel, object("where", object("id", int32(1)), "data", object()), Limits{}},
		{refusal{"update-two-operations", compileerr.KindExactlyOne, "Input data.name on User must set exactly one member", "data.name"}, Update, userModel, object("where", object("id", int32(1)), "data", object("name", object("set", "a", "setNull", true))), Limits{}},
		{refusal{"update-operation-not-a-map", compileerr.KindExactlyOne, "Input data.name on User must set exactly one member", "data.name"}, Update, userModel, object("where", object("id", int32(1)), "data", object("name", marker)), Limits{}},
		{refusal{"update-unknown-operation", compileerr.KindOperatorUnsupported, "Input data.name.bogus on User is not supported for this field or provider", "data.name.bogus"}, Update, userModel, object("where", object("id", int32(1)), "data", object("name", object("bogus", marker))), Limits{}},
		{refusal{"update-set-null-false", compileerr.KindNotAllowedHere, "Input data.name.setNull on User must be true", "data.name.setNull"}, Update, userModel, object("where", object("id", int32(1)), "data", object("name", object("setNull", false))), Limits{}},
		{refusal{"update-set-null-on-required-field", compileerr.KindNullNotAllowed, "Input data.handle on User cannot be null", "data.handle"}, Update, userModel, object("where", object("id", int32(1)), "data", object("handle", object("setNull", true))), Limits{}},
		{refusal{"update-set-null-value", compileerr.KindNullNotAllowed, "Input data.name.set on User cannot be null", "data.name.set"}, Update, userModel, object("where", object("id", int32(1)), "data", object("name", object("set", nil))), Limits{}},
		{refusal{"update-increment-on-text", compileerr.KindNotAllowedHere, "Input data.name.increment on User is only allowed on numeric fields", "data.name.increment"}, Update, userModel, object("where", object("id", int32(1)), "data", object("name", object("increment", int32(1)))), Limits{}},
		{refusal{"update-wrong-value-kind", compileerr.KindInvalidValue, "Input data.handle.set on User must be of type String", "data.handle.set"}, Update, userModel, object("where", object("id", int32(1)), "data", object("handle", object("set", int32(5)))), Limits{}},
		{refusal{"update-where-null", compileerr.KindNullNotAllowed, "Argument where on User cannot be null", "where"}, Update, userModel, object("where", nil, "data", object("name", object("set", "a"))), Limits{}},
		{refusal{"update-where-empty-selector", compileerr.KindExactlyOne, "Argument where on User must set exactly one member", "where"}, Update, userModel, object("where", object(), "data", object("name", object("set", "a"))), Limits{}},
		{refusal{"upsert-create-refusal-is-located", compileerr.KindFieldUnavailable, "Input create.nope on User is not available for this operation", "create.nope"}, Upsert, userModel, object("where", object("id", int32(1)), "create", object("nope", marker), "update", object("name", object("set", "a"))), Limits{}},
		{refusal{"upsert-update-refusal-is-located", compileerr.KindEmpty, "Argument update on User must not be empty", "update"}, Upsert, userModel, object("where", object("id", int32(1)), "create", object(), "update", object()), Limits{}},
		{refusal{"update-many-where-refusal", compileerr.KindEmpty, "Argument where on User must not be empty", "where"}, UpdateMany, userModel, object("where", object(), "data", object("name", object("set", "a"))), Limits{}},
		{refusal{"update-many-relation", compileerr.KindNotAllowedHere, "Input data.posts on User cannot be a relation in an update-many input", "data.posts"}, UpdateMany, userModel, object("where", object("all", true), "data", object("posts", object("create", []any{}))), Limits{}},
		{refusal{"delete-many-where-null", compileerr.KindNullNotAllowed, "Argument where on User cannot be null", "where"}, DeleteMany, userModel, object("where", nil), Limits{}},
		{refusal{"input-depth", compileerr.KindInputLimit, "Input exceeds MaxInputDepth (1)", ""}, Create, userModel, object("data", object("posts", object("create", []any{object()}))), Limits{MaxInputDepth: 1}},
		{refusal{"input-nodes", compileerr.KindInputLimit, "Input exceeds MaxInputNodes (2)", ""}, Create, userModel, object("data", object("handle", "a", "name", "b")), Limits{MaxInputNodes: 2}},
		{refusal{"nested-unknown-operation", compileerr.KindOperatorUnsupported, "Input data.posts.bogus on User is not supported for this field or provider", "data.posts.bogus"}, Create, userModel, object("data", object("posts", object("bogus", []any{}))), Limits{}},
		{refusal{"nested-envelope-empty", compileerr.KindEmpty, "Input data.posts on User must not be empty", "data.posts"}, Create, userModel, object("data", object("posts", object())), Limits{}},
		{refusal{"nested-envelope-not-object", compileerr.KindInvalidValue, "Input data.posts on User has the wrong kind of value", "data.posts"}, Create, userModel, object("data", object("posts", marker)), Limits{}},
		{refusal{"nested-update-while-creating", compileerr.KindNotAllowedHere, "Input data.posts.update on User is not allowed while creating", "data.posts.update"}, Create, userModel, object("data", object("posts", object("update", []any{}))), Limits{}},
		{refusal{"nested-to-one-set", compileerr.KindNotAllowedHere, "Input data.author.set on Post is not allowed on a to-one relation", "data.author.set"}, Update, postModel, object("where", object("id", int32(1)), "data", object("author", object("set", object("id", int32(1))))), Limits{}},
		{refusal{"nested-null-operation", compileerr.KindNullNotAllowed, "Input data.posts.create on User cannot be null", "data.posts.create"}, Create, userModel, object("data", object("posts", object("create", nil))), Limits{}},
		{refusal{"nested-to-one-flag-false", compileerr.KindNotAllowedHere, "Input data.author.disconnect on Post must be true", "data.author.disconnect"}, Update, postModel, object("where", object("id", int32(1)), "data", object("author", object("disconnect", false))), Limits{}},
		{refusal{"nested-to-many-not-a-list", compileerr.KindInvalidValue, "Input data.posts.create on User has the wrong kind of value", "data.posts.create"}, Create, userModel, object("data", object("posts", object("create", marker))), Limits{}},
		{refusal{"nested-list-items", compileerr.KindInputLimit, "Input exceeds MaxListItems (1)", ""}, Create, userModel, object("data", object("posts", object("create", []any{object(), object()}))), Limits{MaxListItems: 1}},
		{refusal{"nested-list-empty", compileerr.KindEmpty, "Input data.posts.create on User must not be empty", "data.posts.create"}, Create, userModel, object("data", object("posts", object("create", []any{}))), Limits{}},
		{refusal{"nested-entry-refusal-is-indexed", compileerr.KindFieldUnavailable, "Input data.posts.create[1].nope on Post is not available for this operation", "data.posts.create[1].nope"}, Create, userModel, object("data", object("posts", object("create", []any{object(), object("nope", marker)}))), Limits{}},
		{refusal{"nested-connect-selector", compileerr.KindExactlyOne, "Input data.posts.connect[0] on Post must set exactly one member", "data.posts.connect[0]"}, Create, userModel, object("data", object("posts", object("connect", []any{object()}))), Limits{}},
		{refusal{"nested-connect-or-create-member-absent", compileerr.KindRequired, "Input data.posts.connectOrCreate[0].create on User is required", "data.posts.connectOrCreate[0].create"}, Create, userModel, object("data", object("posts", object("connectOrCreate", []any{object("where", object("id", int32(1)))}))), Limits{}},
		{refusal{"nested-connect-or-create-extra-member", compileerr.KindInvalidValue, "Input data.posts.connectOrCreate[0] on User has the wrong kind of value", "data.posts.connectOrCreate[0]"}, Create, userModel, object("data", object("posts", object("connectOrCreate", []any{object("where", object("id", int32(1)), "create", object(), "extra", 1)}))), Limits{}},
		{refusal{"nested-connect-or-create-not-object", compileerr.KindInvalidValue, "Input data.posts.connectOrCreate[0] on User has the wrong kind of value", "data.posts.connectOrCreate[0]"}, Create, userModel, object("data", object("posts", object("connectOrCreate", []any{marker}))), Limits{}},
		{refusal{"nested-connect-or-create-selector", compileerr.KindExactlyOne, "Input data.posts.connectOrCreate[0].where on Post must set exactly one member", "data.posts.connectOrCreate[0].where"}, Create, userModel, object("data", object("posts", object("connectOrCreate", []any{object("where", object(), "create", object())}))), Limits{}},
		{refusal{"nested-connect-or-create-input", compileerr.KindFieldUnavailable, "Input data.posts.connectOrCreate[0].create.nope on Post is not available for this operation", "data.posts.connectOrCreate[0].create.nope"}, Create, userModel, object("data", object("posts", object("connectOrCreate", []any{object("where", object("id", int32(1)), "create", object("nope", 1))}))), Limits{}},
		{refusal{"nested-entries-consume-nodes", compileerr.KindInputLimit, "Input exceeds MaxInputNodes (4)", ""}, Create, userModel, object("data", object("name", "x", "posts", object("create", []any{object(), object()}))), Limits{MaxInputNodes: 4}},
		{refusal{"nested-update-member-absent", compileerr.KindRequired, "Input data.posts.update[0].where on User is required", "data.posts.update[0].where"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("update", []any{object("data", object("title", object("set", "a")))}))), Limits{}},
		{refusal{"nested-update-many-member-absent", compileerr.KindRequired, "Input data.posts.updateMany[0].where on User is required", "data.posts.updateMany[0].where"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("updateMany", []any{object("data", object("title", object("set", "a")))}))), Limits{}},
		{refusal{"nested-upsert-member-absent", compileerr.KindRequired, "Input data.posts.upsert[0].update on User is required", "data.posts.upsert[0].update"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("upsert", []any{object("where", object("id", int32(1)), "create", object())}))), Limits{}},
		{refusal{"nested-update-many-predicate", compileerr.KindEmpty, "Input data.posts.updateMany[0].where on Post must not be empty", "data.posts.updateMany[0].where"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("updateMany", []any{object("where", object(), "data", object("title", object("set", "a")))}))), Limits{}},
		{refusal{"nested-update-many-data", compileerr.KindEmpty, "Input data.posts.updateMany[0].data on Post must not be empty", "data.posts.updateMany[0].data"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("updateMany", []any{object("where", object("all", true), "data", object())}))), Limits{}},
		{refusal{"nested-update-selector", compileerr.KindExactlyOne, "Input data.posts.update[0].where on Post must set exactly one member", "data.posts.update[0].where"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("update", []any{object("where", object(), "data", object("title", object("set", "a")))}))), Limits{}},
		{refusal{"nested-update-data", compileerr.KindEmpty, "Input data.posts.update[0].data on Post must not be empty", "data.posts.update[0].data"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("update", []any{object("where", object("id", int32(1)), "data", object())}))), Limits{}},
		{refusal{"nested-upsert-selector", compileerr.KindExactlyOne, "Input data.posts.upsert[0].where on Post must set exactly one member", "data.posts.upsert[0].where"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("upsert", []any{object("where", object(), "create", object(), "update", object("title", object("set", "a")))}))), Limits{}},
		{refusal{"nested-upsert-create", compileerr.KindFieldUnavailable, "Input data.posts.upsert[0].create.nope on Post is not available for this operation", "data.posts.upsert[0].create.nope"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("upsert", []any{object("where", object("id", int32(1)), "create", object("nope", 1), "update", object("title", object("set", "a")))}))), Limits{}},
		{refusal{"nested-upsert-update", compileerr.KindEmpty, "Input data.posts.upsert[0].update on Post must not be empty", "data.posts.upsert[0].update"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("upsert", []any{object("where", object("id", int32(1)), "create", object(), "update", object())}))), Limits{}},
		{refusal{"nested-delete-many-predicate", compileerr.KindEmpty, "Input data.posts.deleteMany[0] on Post must not be empty", "data.posts.deleteMany[0]"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("deleteMany", []any{object()}))), Limits{}},
		{refusal{"nested-to-one-update-input", compileerr.KindEmpty, "Input data.author.update on User must not be empty", "data.author.update"}, Update, postModel, object("where", object("id", int32(1)), "data", object("author", object("update", object()))), Limits{}},
	}
	compilation, ids := mutationCompilation()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			limits := test.limits
			if limits == (Limits{}) {
				limits = Limits{MaxInputDepth: 16, MaxInputNodes: 4096, MaxListItems: 16}
			}
			binder, err := NewMapBinder(compilation, limits)
			if err != nil {
				t.Fatal(err)
			}
			_, err = binder.LowerValues(test.operation, test.model(ids), test.arguments, nil)
			if err == nil {
				t.Fatal("the refusal was accepted")
			}
			assertRefusal(t, err, test.refusal)
			if got := err.Error(); got != test.message {
				t.Fatalf("Error()=%q want the public message %q", got, test.message)
			}
			for _, text := range []string{marker} {
				if containsText(err.Error(), text) {
					t.Fatalf("message echoes caller text: %q", err.Error())
				}
			}
		})
	}
}

func containsText(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestVersionedModelRefusalsAreTyped(t *testing.T) {
	compilation, ids := versionedCompilation(t)
	binder, err := NewMapBinder(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	where := object("id", int32(1))
	data := object("title", object("set", "a"))
	cases := []writeCase{
		{refusal{"update-many-batch", compileerr.KindNotAllowedHere, "Batch writes are not available on Post", ""}, UpdateMany, postModel, object("where", object("all", true), "data", data), Limits{}},
		{refusal{"delete-many-batch", compileerr.KindNotAllowedHere, "Batch writes are not available on Post", ""}, DeleteMany, postModel, object("where", object("all", true)), Limits{}},
		{refusal{"expected-version-absent", compileerr.KindRequired, "Argument expectedVersion on Post is required", "expectedVersion"}, Update, postModel, object("where", where, "data", data), Limits{}},
		{refusal{"expected-version-null", compileerr.KindNullNotAllowed, "Argument expectedVersion on Post cannot be null", "expectedVersion"}, Delete, postModel, object("where", where, "expectedVersion", nil), Limits{}},
		{refusal{"expected-version-wrong-kind", compileerr.KindInvalidValue, "Argument expectedVersion on Post must be of type BigInt", "expectedVersion"}, Update, postModel, object("where", where, "data", data, "expectedVersion", "7"), Limits{}},
		{refusal{"expected-version-zero", compileerr.KindIntegerRange, "Argument expectedVersion on Post must be at least 1", "expectedVersion"}, Delete, postModel, object("where", where, "expectedVersion", int64(0)), Limits{}},
		{refusal{"expectation-two-members", compileerr.KindExactlyOne, "Argument expectation on Post must set exactly one member", "expectation"}, Upsert, postModel, object("where", where, "create", object(), "update", data, "expectation", object("version", int64(1), "absent", true)), Limits{}},
		{refusal{"expectation-not-object", compileerr.KindExactlyOne, "Argument expectation on Post must set exactly one member", "expectation"}, Upsert, postModel, object("where", where, "create", object(), "update", data, "expectation", "x"), Limits{}},
		{refusal{"expectation-version-wrong-kind", compileerr.KindInvalidValue, "Input expectation.version on Post must be of type BigInt", "expectation.version"}, Upsert, postModel, object("where", where, "create", object(), "update", data, "expectation", object("version", "1")), Limits{}},
		{refusal{"expectation-version-zero", compileerr.KindIntegerRange, "Input expectation.version on Post must be at least 1", "expectation.version"}, Upsert, postModel, object("where", where, "create", object(), "update", data, "expectation", object("version", int64(0))), Limits{}},
		{refusal{"expectation-absent-false", compileerr.KindNotAllowedHere, "Input expectation.absent on Post must be true", "expectation.absent"}, Upsert, postModel, object("where", where, "create", object(), "update", data, "expectation", object("absent", false)), Limits{}},
		{refusal{"expectation-unknown-member", compileerr.KindFieldUnavailable, "Argument expectation on Post is not available for this operation", "expectation"}, Upsert, postModel, object("where", where, "create", object(), "update", data, "expectation", object("bogus", true)), Limits{}},
		{refusal{"runtime-owned-field", compileerr.KindFieldUnavailable, "Input data.id on Post is not available for this operation", "data.id"}, Update, postModel, object("where", where, "expectedVersion", int64(1), "data", object("id", object("set", int32(2)))), Limits{}},
		{refusal{"versioned-update-with-relation", compileerr.KindNotAllowedHere, "Argument data on Post cannot carry relation writes without per-row expectations", "data"}, Update, postModel, object("where", where, "expectedVersion", int64(1), "data", object("author", object("disconnect", true))), Limits{}},
		{refusal{"nested-write-on-versioned-target", compileerr.KindNotAllowedHere, "Input data.posts.update on User requires an expectation because the target model is versioned", "data.posts.update"}, Update, userModel, object("where", object("id", int32(1)), "data", object("posts", object("update", []any{}))), Limits{}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := binder.LowerValues(test.operation, test.model(ids), test.arguments, nil)
			if err == nil {
				t.Fatal("the refusal was accepted")
			}
			assertRefusal(t, err, test.refusal)
		})
	}
}

func TestMapBinderFaultsThatNoCallerCanCauseAreInvariants(t *testing.T) {
	compilation, ids := mutationCompilation()
	binder, err := NewMapBinder(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var absent *MapBinder
	userID := golem.ModelID(mustFixed(t, ids.user))
	cases := map[string]error{
		"lower-values-absent-binder": func() error {
			_, err := absent.LowerValues(Create, ids.user, object("data", object()), nil)
			return err
		}(),
		"lower-values-unsupported-root": func() error { _, err := binder.LowerValues(RootOperation(99), ids.user, object(), nil); return err }(),
		"lower-absent-binder":           func() error { _, err := absent.Lower(MapRootInput{Operation: Create, Model: ids.user}); return err }(),
		"lower-unsupported-root": func() error {
			_, err := binder.Lower(MapRootInput{Operation: RootOperation(99), Model: ids.user})
			return err
		}(),
		"lower-malformed-model":      func() error { _, err := binder.Lower(MapRootInput{Operation: Create, Model: "not-hex"}); return err }(),
		"target-absent-binder":       func() error { _, err := absent.Target(ids.user, object("id", int32(1)), "where"); return err }(),
		"target-malformed-model":     func() error { _, err := binder.Target("not-hex", object("id", int32(1)), "where"); return err }(),
		"custom-input-absent-binder": func() error { _, err := absent.CustomInput(CreateInput, ids.user, object()); return err }(),
		"custom-input-invalid-kind":  func() error { _, err := binder.CustomInput(InputKind(99), ids.user, object()); return err }(),
		"input-model-absent": func() error {
			_, err := (&mapInputState{binder: binder}).input("absent", CreateInput, object(), nil, 1)
			return err
		}(),
		"relation-without-relation-fields": func() error {
			_, err := (&mapInputState{binder: binder}).relation(compilation.Model.Models[0], compilation.Model.Models[0].Fields[0], CreateInput, object(), 1)
			return err
		}(),
		"relation-identity-absent": func() error {
			field := compilation.Model.Models[0].Fields[len(compilation.Model.Models[0].Fields)-1]
			relation := *field.Relation
			relation.RelationID = "absent"
			field.Relation = &relation
			_, err := (&mapInputState{binder: binder}).relation(compilation.Model.Models[0], field, CreateInput, object("create", []any{}), 1)
			return err
		}(),
		"relation-entry-unsupported-action": func() error {
			_, err := (&mapInputState{binder: binder}).relationEntry(ids.post, golem.MutationRelationAction(99), true, object(), nil, 1)
			return err
		}(),
		"fixed-id": func() error { _, err := fixedID("zz"); return err }(),
	}
	_ = userID
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			var typed *compileerr.Error
			if !errors.As(err, &typed) || typed.Kind() != compileerr.KindInvariant || typed.Message() != "internal server error" {
				t.Fatalf("error=%#v want an Invariant", err)
			}
		})
	}
}

func TestMapBinderLowerAndTargetRefusals(t *testing.T) {
	compilation, ids := mutationCompilation()
	binder, err := NewMapBinder(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binder.Lower(MapRootInput{Operation: Create, Model: ids.user, Where: object("id", int32(1)), Data: &Input{Kind: CreateInput, Model: golem.ModelID(mustFixed(t, ids.user))}})
	assertRefusal(t, err, refusal{"create-with-where", compileerr.KindNotAllowedHere, "Argument where on User is not accepted by create", "where"})
	_, err = binder.Target(ids.user, nil, "selector")
	assertRefusal(t, err, refusal{"target-null", compileerr.KindNullNotAllowed, "Argument selector on User cannot be null", "selector"})
	_, err = binder.Target(ids.user, object(), "selector")
	assertRefusal(t, err, refusal{"target-empty", compileerr.KindExactlyOne, "Argument selector on User must set exactly one member", "selector"})
	_, err = binder.CustomInput(CreateInput, ids.user, object("nope", 1))
	assertRefusal(t, err, refusal{"custom-input-unknown-field", compileerr.KindFieldUnavailable, "Argument nope on User is not available for this operation", "nope"})
	_, err = binder.CustomInput(UpdateInput, ids.user, object())
	assertRefusal(t, err, refusal{"custom-input-empty-update", compileerr.KindEmpty, "Value on User must not be empty", ""})
}

func TestLowerRefusalsAreTypedAndIdentityFaultsAreInvariants(t *testing.T) {
	model, field := modelID(20), fieldID(21)
	other := modelID(22)
	target := frozenTarget(t, model, field, 1)
	otherTarget := frozenTarget(t, other, field, 1)
	predicate := target.SelectorPredicate()
	otherPredicate := otherTarget.SelectorPredicate()
	create := &Input{Kind: CreateInput, Model: model}
	update := &Input{Kind: UpdateInput, Model: model}
	updateMany := &Input{Kind: UpdateManyInput, Model: model}
	relation := func(action golem.MutationRelationAction, entries ...RelationEntry) *Input {
		return &Input{Kind: UpdateInput, Model: model, Relations: []Relation{{Field: fieldID(30), Relation: relationID(31), TargetModel: model, Action: action, Entries: entries}}}
	}
	createWith := func(action golem.MutationRelationAction, entries ...RelationEntry) *Input {
		value := relation(action, entries...)
		value.Kind = CreateInput
		return value
	}
	invariant := func(root RootInput) error { _, err := Lower(root, Limits{}); return err }
	cases := []struct {
		name string
		err  error
		want *refusal
	}{
		{"model-absent", invariant(RootInput{Operation: Create, Data: create}), nil},
		{"operation-unsupported", invariant(RootInput{Operation: RootOperation(99), Model: model}), nil},
		{"root-shape", invariant(RootInput{Operation: Create, Model: model, Target: &target, Data: create}), nil},
		{"target-model", invariant(RootInput{Operation: Delete, Model: model, Target: &otherTarget}), nil},
		{"where-model", invariant(RootInput{Operation: DeleteMany, Model: model, Where: &otherPredicate}), nil},
		{"input-absent", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: &Input{Kind: CreateInput, Model: model}}), nil},
		{"scalar-identity", invariant(RootInput{Operation: Create, Model: model, Data: &Input{Kind: CreateInput, Model: model, Scalars: []Scalar{{Operation: golem.MutationFieldCreate}}}}), nil},
		{"scalar-duplicate", invariant(RootInput{Operation: Create, Model: model, Data: &Input{Kind: CreateInput, Model: model, Scalars: []Scalar{{Field: field, Operation: golem.MutationFieldCreate, Value: "a"}, {Field: field, Operation: golem.MutationFieldCreate, Value: "b"}}}}), nil},
		{"scalar-operation", invariant(RootInput{Operation: Create, Model: model, Data: &Input{Kind: CreateInput, Model: model, Scalars: []Scalar{{Field: field, Operation: golem.MutationFieldSet, Value: "a"}}}}), nil},
		{"relation-identities", invariant(RootInput{Operation: Create, Model: model, Data: &Input{Kind: CreateInput, Model: model, Relations: []Relation{{}}}}), nil},
		{"relation-action", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationAction(99), RelationEntry{})}), nil},
		{"relation-update-in-create", invariant(RootInput{Operation: Create, Model: model, Data: createWith(golem.MutationRelationUpdate, RelationEntry{})}), nil},
		{"create-entry-with-target", invariant(RootInput{Operation: Create, Model: model, Data: createWith(golem.MutationRelationCreate, RelationEntry{Create: create, Target: &target})}), nil},
		{"membership-with-input", invariant(RootInput{Operation: Create, Model: model, Data: createWith(golem.MutationRelationConnect, RelationEntry{Create: create, Target: &target})}), nil},
		{"membership-without-target", invariant(RootInput{Operation: Create, Model: model, Data: createWith(golem.MutationRelationConnect, RelationEntry{})}), nil},
		{"membership-other-model", invariant(RootInput{Operation: Create, Model: model, Data: createWith(golem.MutationRelationConnect, RelationEntry{Target: &otherTarget})}), nil},
		{"connect-or-create-shape", invariant(RootInput{Operation: Create, Model: model, Data: createWith(golem.MutationRelationConnectOrCreate, RelationEntry{Create: create})}), nil},
		{"connect-or-create-other-model", invariant(RootInput{Operation: Create, Model: model, Data: createWith(golem.MutationRelationConnectOrCreate, RelationEntry{Create: create, Target: &otherTarget})}), nil},
		{"update-entry-shape", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationUpdate, RelationEntry{Update: update, Create: create})}), nil},
		{"update-other-model", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationUpdate, RelationEntry{Update: update, Target: &otherTarget})}), nil},
		{"update-many-shape", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationUpdateMany, RelationEntry{Update: updateMany})}), nil},
		{"update-many-other-model", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationUpdateMany, RelationEntry{Update: updateMany, Predicate: &otherPredicate})}), nil},
		{"upsert-with-predicate", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationUpsert, RelationEntry{Create: create, Update: update, Predicate: &predicate})}), nil},
		{"upsert-other-model", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationUpsert, RelationEntry{Create: create, Update: update, Target: &otherTarget})}), nil},
		{"create-entry-input-absent", invariant(RootInput{Operation: Create, Model: model, Data: createWith(golem.MutationRelationCreate, RelationEntry{})}), nil},
		{"connect-or-create-input-absent", invariant(RootInput{Operation: Create, Model: model, Data: createWith(golem.MutationRelationConnectOrCreate, RelationEntry{Target: &target})}), nil},
		{"update-input-absent", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationUpdate, RelationEntry{})}), nil},
		{"update-many-input-absent", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationUpdateMany, RelationEntry{Predicate: &predicate})}), nil},
		{"upsert-create-absent", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationUpsert, RelationEntry{Update: update})}), nil},
		{"upsert-update-absent", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationUpsert, RelationEntry{Create: create})}), nil},
		{"delete-entry-shape", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationDelete, RelationEntry{Create: create})}), nil},
		{"delete-other-model", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationDelete, RelationEntry{Target: &otherTarget})}), nil},
		{"delete-many-shape", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationDeleteMany, RelationEntry{Target: &target})}), nil},
		{"delete-many-other-model", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationDeleteMany, RelationEntry{Predicate: &otherPredicate})}), nil},
		{"boundary", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: &Input{Kind: UpdateInput, Model: model, Scalars: []Scalar{{Field: field, Operation: golem.MutationFieldSet, Value: policyir.Value{}}}}}), nil},
		{"update-many-relations", invariant(RootInput{Operation: UpdateMany, Model: model, Where: &predicate, Data: &Input{Kind: UpdateManyInput, Model: model, Relations: []Relation{{}}}}), &refusal{"update-many-relations", compileerr.KindNotAllowedHere, "Value cannot be a relation in an update-many input", ""}},
		{"no-entries", invariant(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationCreate)}), &refusal{"no-entries", compileerr.KindEmpty, "Value must not be empty", ""}},
		{"too-deep", func() error {
			_, err := Lower(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationCreate, RelationEntry{Create: create})}, Limits{MaxInputDepth: 1})
			return err
		}(), &refusal{"too-deep", compileerr.KindInputLimit, "Input exceeds MaxInputDepth (1)", ""}},
		{"too-many-items", func() error {
			_, err := Lower(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationCreate, RelationEntry{Create: create}, RelationEntry{Create: create})}, Limits{MaxListItems: 1})
			return err
		}(), &refusal{"too-many-items", compileerr.KindInputLimit, "Input exceeds MaxListItems (1)", ""}},
		{"too-many-nodes", func() error {
			_, err := Lower(RootInput{Operation: Update, Model: model, Target: &target, Data: relation(golem.MutationRelationCreate, RelationEntry{Create: create})}, Limits{MaxInputNodes: 2})
			return err
		}(), &refusal{"too-many-nodes", compileerr.KindInputLimit, "Input exceeds MaxInputNodes (2)", ""}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.err == nil {
				t.Fatal("the invalid lowering was accepted")
			}
			if test.want != nil {
				assertRefusal(t, test.err, *test.want)
				return
			}
			var typed *compileerr.Error
			if !errors.As(test.err, &typed) || typed.Kind() != compileerr.KindInvariant {
				t.Fatalf("error=%#v want an Invariant", test.err)
			}
		})
	}
}

func TestImmutableFieldIsRefusedOnUpdateButAcceptedOnCreate(t *testing.T) {
	binder, ids := systemModeBinder(t, compilerir.FieldID(hexID(6)), compilerir.ModeImmutable)
	if _, err := binder.LowerValues(Create, ids.user, object("data", object("name", "x")), nil); err != nil {
		t.Fatalf("create refused an immutable field: %v", err)
	}
	_, err := binder.LowerValues(Update, ids.user, object("where", object("id", int32(1)), "data", object("name", object("set", "x"))), nil)
	assertRefusal(t, err, refusal{"immutable-update", compileerr.KindFieldUnavailable, "Input data.name on User is not available for this operation", "data.name"})
}
