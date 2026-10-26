package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationnested "github.com/eleven-am/golem/go/internal/mutation/nested"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/observe"
)

type publicErrorPost struct{}

func TestValueReasonsNameTheCallerFieldWithACataloguedMessage(t *testing.T) {
	fixture := schematest.NewMutationExactValues(t)
	registry := publicErrorRegistry(t, fixture.Bundle, func(model *compilerir.ModelIR) {
		maximum := uint32(255)
		for index := range model.Models[1].Fields {
			if model.Models[1].Fields[index].LogicalName == "Title" {
				model.Models[1].Fields[index].Scalar.Type.MaxLength = &maximum
			}
		}
	})
	title := golem.GeneratedTextField[publicErrorPost, string](fixture.PostTitle)
	decimal := golem.GeneratedEqualField[publicErrorPost, golem.Decimal](fixture.PostDecimal)
	instant := golem.GeneratedOrderedField[publicErrorPost, time.Time](fixture.PostDateTime)
	overlong := strings.Repeat("s3cr3t-", 37)
	precise, err := golem.NewDecimal(987654321, 14)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		input   golem.UpdateInput[publicErrorPost]
		reason  golem.Reason
		field   golem.FieldID
		message string
		value   string
	}{
		{
			name:    "too long",
			input:   golem.GeneratedUpdateInput[publicErrorPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, overlong)),
			reason:  golem.ReasonValueTooLong,
			field:   fixture.PostTitle,
			message: "value for Post.Title exceeds maximum length 255",
			value:   "s3cr3t",
		},
		{
			name:    "precision",
			input:   golem.GeneratedUpdateInput[publicErrorPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, decimal, precise)),
			reason:  golem.ReasonValuePrecision,
			field:   fixture.PostDecimal,
			message: "value for Post.Decimal exceeds the declared precision",
			value:   "987654321",
		},
		{
			name:    "range",
			input:   golem.GeneratedUpdateInput[publicErrorPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, instant, time.Date(10000, 1, 2, 3, 4, 5, 0, time.UTC))),
			reason:  golem.ReasonValueRange,
			field:   fixture.PostDateTime,
			message: "value for Post.DateTime is outside the supported range",
			value:   "10000",
		},
		{
			name:    "invalid",
			input:   golem.GeneratedUpdateInput[publicErrorPost](fixture.Post, golem.GeneratedSetFieldValue(fixture.Post, title, "s3cr3t\x00")),
			reason:  golem.ReasonInvalidValue,
			field:   fixture.PostTitle,
			message: "value for Post.Title is invalid",
			value:   "s3cr3t",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, err := golem.RuntimeFreezeUpdateInput(test.input)
			if err != nil {
				t.Fatal(err)
			}
			_, bindErr := mutationbind.UpdateInput(frozen, registry)
			if bindErr == nil {
				t.Fatal("binder accepted the value")
			}
			for _, presented := range []error{
				publicMutationPreparationError(mutationir.Update, fixture.Post, bindErr),
				publicBatchPreparationError(mutationir.UpdateMany, fixture.Post, bindErr),
			} {
				var failure *golem.Error
				if !errors.As(presented, &failure) {
					t.Fatalf("presented=%v", presented)
				}
				if failure.Code != golem.CodeBadUserInput || failure.Reason != test.reason || failure.Field != test.field || failure.Message != test.message || failure.Model != fixture.Post || failure.Limit != "" || failure.LimitMax != 0 {
					t.Fatalf("failure=%#v", failure)
				}
				if strings.Contains(failure.Message, test.value) || strings.Contains(failure.Error(), test.value) {
					t.Fatalf("public message echoed the value: %q", failure.Error())
				}
				if !errors.Is(presented, golem.CodeBadUserInput) || !errors.Is(presented, test.reason) || errors.Is(presented, golem.CodeForbidden) || errors.Is(presented, golem.ReasonLimitExceeded) {
					t.Fatalf("errors.Is does not match code and reason: %v", presented)
				}
				if unwrapped := errors.Unwrap(presented); unwrapped != bindErr {
					t.Fatalf("errors.Unwrap=%v want the trusted binder cause", unwrapped)
				}
			}
		})
	}
}

func TestReasonCatalogueIsClosedAndCarriesNoValues(t *testing.T) {
	want := map[golem.Reason]string{
		golem.ReasonValueTooLong:          "value for Asset.Name exceeds maximum length 255",
		golem.ReasonValuePrecision:        "value for Asset.Name exceeds the declared precision",
		golem.ReasonValueRange:            "value for Asset.Name is outside the supported range",
		golem.ReasonInvalidValue:          "value for Asset.Name is invalid",
		golem.ReasonLimitExceeded:         "request exceeds the Asset.Name limit of 255",
		golem.ReasonNotGranted:            "operation is not granted",
		golem.ReasonFieldNotReadable:      "field is not readable in this position",
		golem.ReasonAuthorizationUnproven: "authorization could not be proven",
		golem.ReasonPolicyInvalid:         "policy cannot be applied to this request",
	}
	for reason, message := range want {
		if got := reasonMessage(reason, "Asset.Name", 255); got != message {
			t.Fatalf("%s message=%q want %q", reason, got, message)
		}
		if reason.Error() != string(reason) {
			t.Fatalf("reason text=%q", reason.Error())
		}
	}
}

func TestReadPlanFieldIsPublicOnlyWhenTheCallerNamedIt(t *testing.T) {
	fixture := schematest.New(t)
	selection, err := readir.NewScalarSelection(policyir.FieldID(fixture.UserName))
	if err != nil {
		t.Fatal(err)
	}
	request, err := readir.NewRequest(readir.RequestInput{Operation: readir.FindMany, Model: policyir.ModelID(fixture.User), Selection: []readir.Selection{selection}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		field  golem.FieldID
		reason golem.Reason
		want   golem.FieldID
	}{
		{name: "named by the caller", field: fixture.UserName, reason: golem.ReasonFieldNotReadable, want: fixture.UserName},
		{name: "named only by the policy", field: fixture.UserPosts, reason: golem.ReasonFieldNotReadable, want: golem.FieldID{}},
		{name: "not granted", reason: golem.ReasonNotGranted, want: golem.FieldID{}},
		{name: "policy invalid on a policy relation", field: fixture.UserPosts, reason: golem.ReasonPolicyInvalid, want: golem.FieldID{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cause := &readplan.Error{Code: readplan.CodeField, Reason: test.reason, Model: policyir.ModelID(fixture.User), Field: policyir.FieldID(test.field), Detail: "trusted detail"}
			presented := publicPlanError(PreparedRead{request: request}, cause)
			var failure *golem.Error
			if !errors.As(presented, &failure) || failure.Code != golem.CodeForbidden || failure.Reason != test.reason || failure.Field != test.want || failure.Message != reasonMessage(test.reason, "", 0) {
				t.Fatalf("failure=%#v", failure)
			}
			if !errors.Is(presented, golem.CodeForbidden) || !errors.Is(presented, test.reason) || errors.Unwrap(presented) != error(cause) {
				t.Fatalf("errors.Is/Unwrap mismatch: %v", presented)
			}
		})
	}
	unclassified := publicPlanError(PreparedRead{request: request}, &readplan.Error{Code: readplan.CodePolicy, Model: policyir.ModelID(fixture.User), Field: policyir.FieldID(fixture.UserPosts), Detail: "relation dependency is absent from the schema"})
	var failure *golem.Error
	if !errors.As(unclassified, &failure) || failure.Code != golem.CodeForbidden || failure.Reason != "" || failure.Field != (golem.FieldID{}) || failure.Message != "read is not permitted" {
		t.Fatalf("unclassified policy failure=%#v", failure)
	}
}

func TestLimitMappersRaiseLimitExceededBeforeAnyOtherClassification(t *testing.T) {
	model := golem.ModelID{15: 1}
	tests := []struct {
		name    string
		cause   error
		limit   golem.Limit
		maximum int
	}{
		{name: "touched rows", cause: &mutationLimitError{limit: golem.LimitTouchedRows, max: 3}, limit: golem.LimitTouchedRows, maximum: 3},
		{name: "facts", cause: &mutationLimitError{limit: golem.LimitFacts, max: 1}, limit: golem.LimitFacts, maximum: 1},
		{name: "outbox bytes", cause: &mutationLimitError{limit: golem.LimitOutboxBytes, max: 64}, limit: golem.LimitOutboxBytes, maximum: 64},
		{name: "nested depth", cause: &mutationnested.Error{Code: mutationnested.CodeDepth, Limit: golem.LimitNestedDepth, Maximum: 2}, limit: golem.LimitNestedDepth, maximum: 2},
		{name: "wrapped", cause: fmt.Errorf("outer: %w", &mutationLimitError{limit: golem.LimitTransactionOrdinal, max: 7}), limit: golem.LimitTransactionOrdinal, maximum: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := fmt.Sprintf("request exceeds the %s limit of %d", test.limit, test.maximum)
			for name, presented := range map[string]error{
				"preparation":       publicMutationPreparationError(mutationir.Create, model, test.cause),
				"batch preparation": publicBatchPreparationError(mutationir.DeleteMany, model, test.cause),
				"nested execution":  publicNestedMutationExecutionError(mutationir.Update, model, test.cause),
				"scalar":            publicScalarMutationError(model, test.cause),
				"root upsert":       publicRootUpsertError(model, test.cause),
				"absent upsert":     publicAbsentUpsertExecutionError(model, test.cause),
				"rebased upsert":    rebaseOptimisticUpsertError(model, publicRootUpsertError(model, test.cause)),
			} {
				var failure *golem.Error
				if !errors.As(presented, &failure) || failure.Code != golem.CodeLimitExceeded || failure.Reason != golem.ReasonLimitExceeded || failure.Limit != test.limit || failure.LimitMax != test.maximum || failure.Field != (golem.FieldID{}) || failure.Message != message {
					t.Fatalf("%s failure=%#v", name, failure)
				}
				if !errors.Is(presented, golem.CodeLimitExceeded) || !errors.Is(presented, golem.ReasonLimitExceeded) || errors.Is(presented, golem.CodeBadUserInput) {
					t.Fatalf("%s errors.Is mismatch: %v", name, presented)
				}
			}
		})
	}
}

func TestLimitExceededObservesAsALimitRefusal(t *testing.T) {
	limited := golem.RuntimeLimitError("delete", golem.ModelID{15: 1}, golem.LimitOutboxBytes, 64, "request exceeds the outbox_bytes limit of 64", nil)
	if outcome, reason := observationResult(limited); outcome != observe.OutcomeRefused || reason != observe.ReasonLimit {
		t.Fatalf("outcome=%s reason=%s", outcome, reason)
	}
	if outcome, reason := observationResult(fmt.Errorf("wrapped: %w", limited)); outcome != observe.OutcomeRefused || reason != observe.ReasonLimit {
		t.Fatalf("wrapped outcome=%s reason=%s", outcome, reason)
	}
	invalid := golem.RuntimeOperationErrorWithReason(golem.CodeBadUserInput, golem.ReasonValueTooLong, "create", golem.ModelID{15: 1}, golem.FieldID{15: 2}, "value for Post.Title exceeds maximum length 255", nil)
	if outcome, reason := observationResult(invalid); outcome != observe.OutcomeRefused || reason != observe.ReasonInvalidInput {
		t.Fatalf("invalid outcome=%s reason=%s", outcome, reason)
	}
}

func TestMutationLimitsAreLimitExceededAcrossProviders(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "public_limits", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, compilerir.ActionCascade)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(compilerir.ActionCascade), func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx, fixture := context.Background(), profile.fixture
		for _, id := range []byte{50, 51} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 2}, "limited")); err != nil {
				t.Fatal(err)
			}
		}
		clearCascadeOutbox(t, profile)
		ownedByBob := fixture.authorID.Eq(golem.UUID{15: 2})

		outbox := reopenMutationResultWithLimits(t, fixture, MutationLimits{MaxOutboxBytes: 16})
		_, err := SystemDelete(ctx, outbox.app.System(), outbox.postDescriptor, outbox.target(50))
		assertPublicLimit(t, err, "delete", golem.LimitOutboxBytes, 16)
		_, err = SystemDeleteMany(ctx, outbox.app.System(), outbox.postDescriptor, ownedByBob)
		assertPublicLimit(t, err, "deleteMany", golem.LimitOutboxBytes, 16)

		facts := reopenMutationResultWithLimits(t, fixture, MutationLimits{MaxFacts: 1})
		_, err = SystemDeleteMany(ctx, facts.app.System(), facts.postDescriptor, ownedByBob)
		assertPublicLimit(t, err, "deleteMany", golem.LimitFacts, 1)

		batch := reopenMutationResultWithLimits(t, fixture, MutationLimits{MaxTouchedRows: 1})
		_, err = SystemDeleteMany(ctx, batch.app.System(), batch.postDescriptor, ownedByBob)
		assertPublicLimit(t, err, "deleteMany", golem.LimitBatchRows, 1)
		err = SystemTransaction(ctx, batch.app.System(), func(tx *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, err := SystemTxDelete(ctx, tx, batch.postDescriptor, batch.target(50)); err != nil {
				return err
			}
			_, err := SystemTxDelete(ctx, tx, batch.postDescriptor, batch.target(51))
			return err
		})
		assertPublicLimit(t, err, "delete", golem.LimitTouchedRows, 1)

		assertCascadeFacts(t, profile, nil)
		if count := countAcceptancePosts(t, profile, 50) + countAcceptancePosts(t, profile, 51); count != 2 {
			t.Fatalf("a refused limit removed rows: remaining=%d", count)
		}
	})
}

func assertPublicLimit(t *testing.T, err error, operation string, limit golem.Limit, maximum int) {
	t.Helper()
	var failure *golem.Error
	message := fmt.Sprintf("request exceeds the %s limit of %d", limit, maximum)
	if !errors.As(err, &failure) || failure.Code != golem.CodeLimitExceeded || failure.Reason != golem.ReasonLimitExceeded || failure.Limit != limit || failure.LimitMax != maximum || failure.Field != (golem.FieldID{}) || failure.Operation != operation || failure.Message != message {
		t.Fatalf("limit failure=%#v err=%v, want %s %s %d", failure, err, operation, limit, maximum)
	}
	if !errors.Is(err, golem.CodeLimitExceeded) || !errors.Is(err, golem.ReasonLimitExceeded) {
		t.Fatalf("limit is not matched by errors.Is: %v", err)
	}
}

func publicErrorRegistry(t testing.TB, bundle golem.SchemaBundle, mutate func(*compilerir.ModelIR)) *schema.Registry {
	t.Helper()
	var model compilerir.ModelIR
	if err := json.Unmarshal(bundle.Model().Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	mutate(&model)
	payload, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("fingerprint: %v", err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], decoded)
	document := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	logical := map[compilerir.FieldID]compilerir.LogicalTypeIR{}
	for _, declared := range model.Models {
		for _, field := range declared.Fields {
			if field.Scalar != nil {
				logical[field.ID] = field.Scalar.Type
			}
		}
	}
	providers := bundle.Providers()
	for index, provider := range providers {
		value, err := physical.CanonicalDecode(provider.Schema().Bytes())
		if err != nil {
			t.Fatal(err)
		}
		for tableIndex := range value.Tables {
			for columnIndex, column := range value.Tables[tableIndex].Columns {
				declared, ok := logical[column.ID]
				if !ok {
					continue
				}
				storage, err := physical.ExpectedStorage(value.Provider.Provider, declared)
				if err != nil {
					t.Fatal(err)
				}
				value.Tables[tableIndex].Columns[columnIndex].Storage = storage
			}
		}
		encoded, err := physical.CanonicalEncode(value)
		if err != nil {
			t.Fatal(err)
		}
		physicalDigest, err := physical.PhysicalFingerprint(value)
		if err != nil {
			t.Fatal(err)
		}
		system, err := physical.SystemFingerprint(value.Provider, value.System)
		if err != nil {
			t.Fatal(err)
		}
		providers[index] = golem.GeneratedProviderSchemaDocument(provider.Provider(), golem.SchemaDigest(system), golem.GeneratedSchemaDocument(value.Version, value.CanonicalVersion, golem.SchemaDigest(physicalDigest), encoded))
	}
	updated := golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), document, bundle.Contract(), providers...)
	registry, err := schema.New(updated)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
