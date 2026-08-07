package subscription

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
)

// CanonicalIdentity is an opaque, comparable identity for already-normalized
// security and result-shape inputs. It never exposes the principal, filter, or
// selection bytes from which it was derived.
type CanonicalIdentity struct{ digest [32]byte }

func NewCanonicalIdentity(domain string, canonical []byte) (CanonicalIdentity, error) {
	if domain == "" || len(canonical) == 0 {
		return CanonicalIdentity{}, fmt.Errorf("P7_GROUP_KEY_INVALID")
	}
	hash := sha256.New()
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(domain)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write(canonical)
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return CanonicalIdentity{digest: digest}, nil
}

func (identity CanonicalIdentity) valid() bool { return identity.digest != ([32]byte{}) }

// SubscriberKey is deliberately exhaustive. Equivalent audit labels are not
// a security identity and therefore are not part of this key; the retained
// principal snapshot identity always is.
type SubscriberKey struct {
	generation   golem.SchemaDigest
	model        golem.ModelID
	principal    CanonicalIdentity
	policy       CanonicalIdentity
	filter       CanonicalIdentity
	selection    CanonicalIdentity
	dependencies CanonicalIdentity
	encoder      CanonicalIdentity
	membership   CanonicalIdentity
	shareable    bool
}

type SubscriberKeyInput struct {
	Generation       golem.SchemaDigest
	Model            golem.ModelID
	Principal        CanonicalIdentity
	PolicyGeneration CanonicalIdentity
	Filter           CanonicalIdentity
	Selection        CanonicalIdentity
	Dependencies     CanonicalIdentity
	EncoderShape     CanonicalIdentity
	Membership       CanonicalIdentity
	Shareable        bool
}

func NewSubscriberKey(input SubscriberKeyInput) (SubscriberKey, error) {
	if input.Generation == (golem.SchemaDigest{}) || input.Model == (golem.ModelID{}) ||
		!input.Principal.valid() || !input.PolicyGeneration.valid() || !input.Filter.valid() ||
		!input.Selection.valid() || !input.Dependencies.valid() || !input.EncoderShape.valid() ||
		!input.Membership.valid() {
		return SubscriberKey{}, fmt.Errorf("P7_GROUP_KEY_INVALID")
	}
	return SubscriberKey{
		generation: input.Generation, model: input.Model, principal: input.Principal,
		policy: input.PolicyGeneration, filter: input.Filter, selection: input.Selection,
		dependencies: input.Dependencies, encoder: input.EncoderShape,
		membership: input.Membership, shareable: input.Shareable,
	}, nil
}

func (key SubscriberKey) GenerationDigest() golem.SchemaDigest { return key.generation }
func (key SubscriberKey) ModelID() golem.ModelID               { return key.model }
func (key SubscriberKey) Shareable() bool                      { return key.shareable }

// evaluationKey includes the exact event ID. Non-shareable subscriptions also
// include their unique membership identity, preventing read hooks or computed
// fields from sharing actor/execution-specific work.
type evaluationKey struct {
	generation   golem.SchemaDigest
	model        golem.ModelID
	event        golem.EventID
	principal    CanonicalIdentity
	policy       CanonicalIdentity
	filter       CanonicalIdentity
	selection    CanonicalIdentity
	dependencies CanonicalIdentity
	encoder      CanonicalIdentity
	membership   CanonicalIdentity
}

func (key SubscriberKey) forEvent(event golem.EventID) evaluationKey {
	membership := CanonicalIdentity{}
	if !key.shareable {
		membership = key.membership
	}
	return evaluationKey{
		generation: key.generation, model: key.model, event: event,
		principal: key.principal, policy: key.policy, filter: key.filter,
		selection: key.selection, dependencies: key.dependencies, encoder: key.encoder,
		membership: membership,
	}
}
