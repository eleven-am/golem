package workflow

import (
	"bytes"
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
)

// PreviewRequest is the read-only all-provider planning input shared by
// migration authoring and migration explanation.
type PreviewRequest struct {
	ModuleDir        string
	Root             string
	ModelFingerprint ir.Fingerprint
	Model            ir.ModelIR
	PreviousModel    *ir.ModelIR
	Providers        []Provider
}

type PreviewProvider struct {
	Provider Provider
	Before   physical.PhysicalSchema
	After    physical.PhysicalSchema
	Plan     migration.Plan
}

type PreviewResult struct {
	State                  State
	Providers              []PreviewProvider
	HeadLength             int
	BeforeModel            ir.ModelIR
	BeforeModelFingerprint ir.Fingerprint
}

// Preview validates the complete reviewed state and derives every declared
// provider plan before returning any presentation-filterable value. It owns no
// publication, filesystem mutation, provider connection, or SQL execution.
func Preview(ctx context.Context, request PreviewRequest) (PreviewResult, error) {
	if err := ctx.Err(); err != nil {
		return PreviewResult{}, err
	}
	root, err := canonicalRoot(request.Root)
	if err != nil {
		return PreviewResult{}, err
	}
	providers, err := canonicalProviders(request.Providers)
	if err != nil {
		return PreviewResult{}, err
	}
	state, err := Load(ctx, request.ModuleDir, root, providers)
	if err != nil {
		return PreviewResult{}, err
	}
	headLength := -1
	for _, provider := range providers {
		length := len(state.Histories[provider.Result.Provider.Provider].Manifest.Entries)
		if headLength < 0 {
			headLength = length
		} else if length != headLength {
			return PreviewResult{}, fmt.Errorf("declared provider migration heads are not in lockstep")
		}
	}
	if err := validatePreviewHeadLength(headLength); err != nil {
		return PreviewResult{}, err
	}
	if fingerprint, fingerprintErr := ir.ModelFingerprint(request.Model); fingerprintErr != nil || fingerprint != request.ModelFingerprint {
		return PreviewResult{}, fmt.Errorf("current ModelIR does not match its migration fingerprint")
	}
	beforeModel := ir.CanonicalEmptyModel()
	if headLength != 0 {
		if state.HeadModel == nil || request.PreviousModel == nil {
			return PreviewResult{}, fmt.Errorf("incremental migration requires the exact previous reviewed ModelIR")
		}
		stateBytes, _ := ir.CanonicalModel(*state.HeadModel)
		requestBytes, previousErr := ir.CanonicalModel(*request.PreviousModel)
		if previousErr != nil || !bytes.Equal(stateBytes, requestBytes) {
			return PreviewResult{}, fmt.Errorf("incremental migration previous ModelIR differs from the reviewed head")
		}
		beforeModel = *request.PreviousModel
	} else if request.PreviousModel != nil {
		return PreviewResult{}, fmt.Errorf("initial migration must start from the canonical empty ModelIR")
	}
	beforeModelFingerprint, err := previewBeforeModelFingerprint(state, headLength, beforeModel)
	if err != nil {
		return PreviewResult{}, err
	}
	result := PreviewResult{
		State: state, HeadLength: headLength, BeforeModel: beforeModel,
		BeforeModelFingerprint: beforeModelFingerprint,
	}
	for _, provider := range providers {
		providerID := provider.Result.Provider.Provider
		history := state.Histories[providerID]
		before := canonicalEmpty(provider.Result.Schema)
		if len(history.Manifest.Entries) != 0 {
			head := history.Manifest.Entries[len(history.Manifest.Entries)-1]
			before = head.AfterSnapshot
			if head.AfterModel != migration.Digest(beforeModelFingerprint) {
				return PreviewResult{}, fmt.Errorf("provider %s model head differs from the reviewed ModelIR", providerID)
			}
		}
		after, normalizeErr := physical.Normalize(provider.Result.Schema)
		if normalizeErr != nil {
			return PreviewResult{}, normalizeErr
		}
		plan, planErr := migration.DiffReviewed(before, after)
		if planErr != nil {
			return PreviewResult{}, fmt.Errorf("plan provider %s: %w", providerID, planErr)
		}
		if err := migration.ValidatePlanShape(plan); err != nil {
			return PreviewResult{}, fmt.Errorf("plan provider %s: %w", providerID, err)
		}
		result.Providers = append(result.Providers, PreviewProvider{Provider: provider, Before: before, After: after, Plan: plan})
	}
	return result, nil
}

func previewBeforeModelFingerprint(state State, headLength int, beforeModel ir.ModelIR) (ir.Fingerprint, error) {
	if headLength == 0 {
		return ir.EmptyModelFingerprint(), nil
	}
	if state.HeadModelFingerprint == "" {
		return "", fmt.Errorf("reviewed ModelIR head fingerprint is absent")
	}
	// The decoded historical model is projected into the current in-memory
	// shape. Re-fingerprinting that projection would let later zero-valued
	// fields redefine the immutable head used by migration authoring.
	return state.HeadModelFingerprint, nil
}

func validatePreviewHeadLength(headLength int) error {
	if headLength < 0 {
		return fmt.Errorf("migration workflow requires a declared provider")
	}
	return nil
}
