package fact

import (
	"bytes"
	"fmt"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
)

var policyHistoryPrivateMagic = []byte("GOLEMPOLICYEVIDENCE")

// V4 uses the existing opaque private column for a versioned container. The
// base snapshot remains the exact V3 row blob. Only ordinal 1 stores the graph,
// so existing complete-causation byte accounting and retention own one copy.
func encodePolicyHistoryPrivateEvidence(base, graph []byte) ([]byte, error) {
	if len(graph) > MaxPolicyHistoryEncodedBytes {
		return nil, fmt.Errorf("P5_POLICY_HISTORY: encoded graph exceeds its byte limit")
	}
	e := encoder{limit: eventprovider.MaximumClaimBytes}
	e.raw(policyHistoryPrivateMagic)
	e.u16(PolicyHistoryFormatVersion)
	e.bytes(base)
	e.bytes(graph)
	if e.err != nil {
		return nil, e.err
	}
	return e.data, nil
}

func decodePolicyHistoryPrivateEvidence(payload []byte) ([]byte, []byte, error) {
	if len(payload) > eventprovider.MaximumClaimBytes {
		return nil, nil, fmt.Errorf("P5_POLICY_HISTORY: private evidence exceeds the whole-claim byte bound")
	}
	d := decoder{data: payload}
	magic, err := d.raw(len(policyHistoryPrivateMagic))
	if err != nil || !bytes.Equal(magic, policyHistoryPrivateMagic) {
		return nil, nil, d.fail("invalid historical private-evidence magic")
	}
	version, err := d.u16()
	if err != nil || version != PolicyHistoryFormatVersion {
		return nil, nil, d.fail("unsupported historical private-evidence version")
	}
	base, err := d.bytes()
	if err != nil {
		return nil, nil, err
	}
	graph, err := d.bytes()
	if err != nil {
		return nil, nil, err
	}
	if len(graph) > MaxPolicyHistoryEncodedBytes || d.remaining() != 0 {
		return nil, nil, d.fail("invalid historical private-evidence size or trailing bytes")
	}
	return base, graph, nil
}

func (envelope Envelope) PolicyHistoryCompanion() (PolicyHistory, bool) {
	if envelope.graphCompanion == nil {
		return PolicyHistory{}, false
	}
	return *envelope.graphCompanion, true
}

func (envelope Envelope) validatePolicyHistoryCompanion(companion PolicyHistory) error {
	reference, err := companion.Reference()
	if err != nil {
		return err
	}
	if envelope.ordinal != 1 || envelope.history == nil || *envelope.history != reference || envelope.causation != companion.CausationID() || envelope.generation != companion.Generation() {
		return fmt.Errorf("P5_POLICY_HISTORY: historical graph does not belong to this first fact")
	}
	return nil
}

func (envelope Envelope) withPolicyHistoryCompanion(companion PolicyHistory) (Envelope, error) {
	if err := envelope.validatePolicyHistoryCompanion(companion); err != nil {
		return Envelope{}, err
	}
	envelope.graphCompanion = &companion
	return envelope, nil
}
