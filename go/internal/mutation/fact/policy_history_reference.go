package fact

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
)

const (
	// The default constructor still writes V3. V4 is admitted only when an
	// explicitly sealed causation references its immutable historical graph.
	HistoryFactVersion         uint16 = 4
	HistoryFactCodecIdentity          = "golem.fact.v4"
	PolicyHistoryFormatVersion uint16 = 1
	PolicyHistoryCodecIdentity        = "golem.policy-history.v1"
)

// PolicyHistoryReference binds a fact to a canonical companion graph. It does
// not prove that the companion exists or that it belongs to this causation;
// whole-causation validation must establish both before publication.
type PolicyHistoryReference struct {
	format     uint16
	projection golem.SchemaDigest
	digest     golem.SchemaDigest
}

func NewPolicyHistoryReference(format uint16, projection, digest golem.SchemaDigest) (PolicyHistoryReference, error) {
	reference := PolicyHistoryReference{format: format, projection: projection, digest: digest}
	if err := reference.validate(); err != nil {
		return PolicyHistoryReference{}, err
	}
	return reference, nil
}

func (reference PolicyHistoryReference) FormatVersion() uint16 { return reference.format }
func (reference PolicyHistoryReference) ProjectionFingerprint() golem.SchemaDigest {
	return reference.projection
}
func (reference PolicyHistoryReference) Digest() golem.SchemaDigest { return reference.digest }

func (reference PolicyHistoryReference) validate() error {
	if reference.format != PolicyHistoryFormatVersion {
		return fmt.Errorf("P5_POLICY_HISTORY: unsupported graph format")
	}
	if reference.projection == (golem.SchemaDigest{}) || reference.digest == (golem.SchemaDigest{}) {
		return fmt.Errorf("P5_POLICY_HISTORY: graph and capture fingerprints must be non-zero")
	}
	return nil
}

func (envelope Envelope) PolicyHistory() (PolicyHistoryReference, bool) {
	if envelope.history == nil {
		return PolicyHistoryReference{}, false
	}
	return *envelope.history, true
}

func (envelope Envelope) WithPolicyHistory(reference PolicyHistoryReference) (Envelope, error) {
	if err := reference.validate(); err != nil {
		return Envelope{}, err
	}
	if envelope.history != nil && *envelope.history != reference {
		return Envelope{}, fmt.Errorf("P5_POLICY_HISTORY: a sealed fact cannot change its historical graph")
	}
	envelope.history = &reference
	return envelope, nil
}
