package fact

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/policy/history"
)

// PolicyHistoryFactsDigest commits to the canonical V3 content of an ordered
// causation, including private before snapshots and recorded times. Removing
// only the history reference avoids a cyclic graph/fact digest definition.
// V3 and V4 inputs therefore produce the same commitment after sealing.
func PolicyHistoryFactsDigest(rows []OutboxRow, resolver HistoricalSchemaResolver) (golem.SchemaDigest, error) {
	_, digest, err := policyHistoryFacts(rows, resolver)
	return digest, err
}

func policyHistoryFacts(rows []OutboxRow, resolver HistoricalSchemaResolver) ([]Envelope, golem.SchemaDigest, error) {
	if len(rows) == 0 || len(rows) > eventprovider.MaximumCausationFacts {
		return nil, golem.SchemaDigest{}, fmt.Errorf("P5_POLICY_HISTORY: fact count exceeds the existing whole-causation bound")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("golem:policy-history-facts:v1\x00"))
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(rows)))
	_, _ = hash.Write(count[:])
	var size int
	envelopes := make([]Envelope, 0, len(rows))
	events := make(map[EventID]bool, len(rows))
	for index, row := range rows {
		if row.EncodedBytes() > eventprovider.MaximumCausationEvidenceBytes-size {
			return nil, golem.SchemaDigest{}, fmt.Errorf("P5_POLICY_HISTORY: fact bytes exceed the released whole-causation evidence bound")
		}
		size += row.EncodedBytes()
		envelope, err := ValidateStoredRow(row, resolver)
		if err != nil {
			return nil, golem.SchemaDigest{}, err
		}
		if envelope.TransactionOrdinal() != uint32(index+1) || events[envelope.EventID()] {
			return nil, golem.SchemaDigest{}, fmt.Errorf("P5_POLICY_HISTORY: fact identities or transaction order are invalid")
		}
		if index != 0 && (envelope.CausationID() != envelopes[0].CausationID() || envelope.Generation() != envelopes[0].Generation()) {
			return nil, golem.SchemaDigest{}, fmt.Errorf("P5_POLICY_HISTORY: facts do not share one causation and generation")
		}
		events[envelope.EventID()] = true
		envelopes = append(envelopes, envelope)
		base := envelope
		base.history = nil
		base.graphCompanion = nil
		canonical, err := base.OutboxRow(row.RecordedAt)
		if err != nil {
			return nil, golem.SchemaDigest{}, err
		}
		var timestamp [8]byte
		binary.BigEndian.PutUint64(timestamp[:], uint64(canonical.RecordedAt.UnixMicro()))
		_, _ = hash.Write(timestamp[:])
		for _, data := range [][]byte{canonical.Metadata, canonical.DeleteSnapshot} {
			binary.BigEndian.PutUint32(count[:], uint32(len(data)))
			_, _ = hash.Write(count[:])
			_, _ = hash.Write(data)
		}
	}
	var digest golem.SchemaDigest
	copy(digest[:], hash.Sum(nil))
	return envelopes, digest, nil
}

// SealPolicyHistoryCausation prepares history-bearing facts with one graph in
// the first returned row, without writing them. The transaction owner inserts
// the complete group atomically and rechecks its write epoch before insertion.
func SealPolicyHistoryCausation(rows []OutboxRow, graph history.Graph, resolver HistoricalSchemaResolver) ([]OutboxRow, PolicyHistory, error) {
	envelopes, digest, err := policyHistoryFacts(rows, resolver)
	if err != nil {
		return nil, PolicyHistory{}, err
	}
	if envelopes[0].Generation() != graph.Generation() {
		return nil, PolicyHistory{}, fmt.Errorf("P5_POLICY_HISTORY: facts and captured graph generations differ")
	}
	for _, envelope := range envelopes {
		if envelope.history != nil {
			return nil, PolicyHistory{}, fmt.Errorf("P5_POLICY_HISTORY: facts are already sealed")
		}
	}
	companion, err := NewPolicyHistory(envelopes[0].CausationID(), digest, graph)
	if err != nil {
		return nil, PolicyHistory{}, err
	}
	reference, err := companion.Reference()
	if err != nil {
		return nil, PolicyHistory{}, err
	}
	sealed := make([]OutboxRow, len(rows))
	for index, envelope := range envelopes {
		envelope, err = envelope.WithPolicyHistory(reference)
		if err != nil {
			return nil, PolicyHistory{}, err
		}
		if index == 0 {
			envelope, err = envelope.withPolicyHistoryCompanion(companion)
			if err != nil {
				return nil, PolicyHistory{}, err
			}
		}
		sealed[index], err = envelope.OutboxRow(rows[index].RecordedAt)
		if err != nil {
			return nil, PolicyHistory{}, err
		}
	}
	return sealed, companion, nil
}

// ValidatePolicyHistoryCausation is the publication boundary: no member may
// be published before the whole group's references and content match the one
// companion. Missing, mixed or swapped evidence is an error, never a downgrade
// to current database routing.
func ValidatePolicyHistoryCausation(rows []OutboxRow, companion PolicyHistory, resolver HistoricalSchemaResolver) error {
	envelopes, digest, err := policyHistoryFacts(rows, resolver)
	if err != nil {
		return err
	}
	return validatePolicyHistoryFacts(envelopes, digest, companion)
}

// DecodePolicyHistoryCausation loads the unique stored graph and validates
// the complete group before it can be published or used as historical evidence.
func DecodePolicyHistoryCausation(rows []OutboxRow, resolver HistoricalSchemaResolver) (PolicyHistory, error) {
	envelopes, digest, err := policyHistoryFacts(rows, resolver)
	if err != nil {
		return PolicyHistory{}, err
	}
	companion, present := envelopes[0].PolicyHistoryCompanion()
	if !present {
		return PolicyHistory{}, fmt.Errorf("P5_POLICY_HISTORY: the causation has no stored historical graph")
	}
	if err := validatePolicyHistoryFacts(envelopes, digest, companion); err != nil {
		return PolicyHistory{}, err
	}
	return companion, nil
}

func validatePolicyHistoryFacts(envelopes []Envelope, digest golem.SchemaDigest, companion PolicyHistory) error {
	reference, err := companion.Reference()
	if err != nil {
		return err
	}
	stored, present := envelopes[0].PolicyHistoryCompanion()
	if !present || stored.Digest() != companion.Digest() {
		return fmt.Errorf("P5_POLICY_HISTORY: the first fact has no matching stored graph")
	}
	if envelopes[0].CausationID() != companion.CausationID() || envelopes[0].Generation() != companion.Generation() || digest != companion.FactsDigest() {
		return fmt.Errorf("P5_POLICY_HISTORY: companion does not commit to this complete causation")
	}
	for _, envelope := range envelopes {
		actual, present := envelope.PolicyHistory()
		if !present || actual != reference {
			return fmt.Errorf("P5_POLICY_HISTORY: causation has missing or inconsistent historical graph references")
		}
	}
	return nil
}
