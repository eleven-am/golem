package workflow

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
)

type DataCorrectionRequest struct {
	NewRequest
	Field string
	SQL   []byte
}

// PrepareDataCorrection seals the reviewed SQL through the ordinary immutable
// publication owner, while requiring unchanged schema and an existing field.
// SQL is provider-specific; one declared provider is supported per history.
func PrepareDataCorrection(ctx context.Context, request DataCorrectionRequest) (NewResult, error) {
	if err := migration.ValidateReviewedBackfillArtifact(request.SQL); err != nil {
		return NewResult{}, err
	}
	return prepareNew(ctx, request.NewRequest, &request)
}

func prepareDataCorrectionPreview(preview *PreviewResult, request NewRequest, selector string) error {
	if preview.HeadLength == 0 || preview.BeforeModelFingerprint != request.ModelFingerprint {
		return fmt.Errorf("data correction requires an existing unchanged reviewed model; apply schema migrations separately")
	}
	if len(preview.Providers) != 1 {
		return fmt.Errorf("data correction currently requires exactly one declared SQLite or PostgreSQL provider")
	}
	field, err := resolveDataCorrectionField(request.Model, selector)
	if err != nil {
		return err
	}
	provider := &preview.Providers[0]
	if !reflect.DeepEqual(provider.Before, provider.After) || len(provider.Plan.Operations) != 0 {
		return fmt.Errorf("data correction cannot include physical schema changes")
	}
	provider.Plan, err = migration.DataCorrectionPlan(provider.After, field)
	return err
}

func resolveDataCorrectionField(model ir.ModelIR, selector string) (ir.FieldID, error) {
	parts := strings.Split(selector, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("data correction field must be Model.Field")
	}
	var selected *ir.ModelDeclIR
	for index := range model.Models {
		candidate := &model.Models[index]
		if candidate.Go.Name == parts[0] || candidate.LogicalName == parts[0] {
			if selected != nil {
				return "", fmt.Errorf("data correction model is ambiguous")
			}
			selected = candidate
		}
	}
	if selected == nil {
		return "", fmt.Errorf("data correction model does not exist")
	}
	var field *ir.FieldIR
	for index := range selected.Fields {
		candidate := &selected.Fields[index]
		if candidate.GoName == parts[1] || candidate.LogicalName == parts[1] {
			if field != nil {
				return "", fmt.Errorf("data correction field is ambiguous")
			}
			field = candidate
		}
	}
	if field == nil || field.Kind != ir.FieldScalar || field.Scalar == nil || field.Scalar.Generation != nil {
		return "", fmt.Errorf("data correction field must be an existing stored scalar")
	}
	return field.ID, nil
}
