package ir

import (
	"bytes"
	"fmt"
)

// decodeCurrentModelCanonicalFraming checks only the current versioned JSON
// framing and byte-for-byte canonical encoding. It is deliberately private and
// non-authoritative: complete CompilationIR validation and policy/schema
// bootstrap own semantic agreement across projections.
func decodeCurrentModelCanonicalFraming(payload []byte) (ModelIR, error) {
	if err := validateModelJSONEnvelope(payload, ModelFormatVersion); err != nil {
		return ModelIR{}, err
	}
	var model ModelIR
	if err := decodeExactModelJSON(payload, &model); err != nil {
		return ModelIR{}, fmt.Errorf("model IR decode: %w", err)
	}
	canonical, err := CanonicalModel(model)
	if err != nil {
		return ModelIR{}, fmt.Errorf("model IR decode: canonicalize: %w", err)
	}
	if !bytes.Equal(canonical, payload) {
		return ModelIR{}, fmt.Errorf("model IR decode: document is not in canonical normalized form")
	}
	return model, nil
}
