package runtime

import (
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestBatchLoaderKeyLimitAcceptsBoundaryAndRefusesOverflow(t *testing.T) {
	model, field := golem.ModelID{1}, golem.FieldID{2}
	if err := enforceBatchLoaderKeyLimit(golem.ReadFindMany, model, field, MaxBatchLoaderKeys); err != nil {
		t.Fatalf("exact loader-key ceiling rejected: %v", err)
	}
	err := enforceBatchLoaderKeyLimit(golem.ReadFindMany, model, field, MaxBatchLoaderKeys+1)
	var public *golem.Error
	if !errors.As(err, &public) || public.Code != golem.CodeBadUserInput || public.Operation != "findMany" || public.Model != model || public.Field != field {
		t.Fatalf("loader-key overflow=%#v", err)
	}
}

func TestConfiguredBatchLoaderKeyLimitAcceptsExactBoundaryAndRefusesOverflow(t *testing.T) {
	model, field := golem.ModelID{3}, golem.FieldID{4}
	if err := enforceBatchLoaderKeyLimitWith(golem.ReadFindMany, model, field, 7, 7); err != nil {
		t.Fatalf("configured exact loader-key limit rejected: %v", err)
	}
	err := enforceBatchLoaderKeyLimitWith(golem.ReadFindMany, model, field, 8, 7)
	var public *golem.Error
	if !errors.As(err, &public) || public.Code != golem.CodeBadUserInput || public.Field != field {
		t.Fatalf("configured loader-key overflow=%#v", err)
	}
}
