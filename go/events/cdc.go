package events

import (
	"context"
	"regexp"
	"time"

	"github.com/eleven-am/golem/go/golem"
)

type CDCAdapter interface {
	Identity() CDCIdentity
	// CorrelatesGolemTransaction is an adapter-owned source-log decision. The
	// adapter knows how its SourceTransactionID/cursor map to transaction content;
	// core Golem never guesses from database clocks or unrelated IDs.
	CorrelatesGolemTransaction(context.Context, CDCCorrelationInput) (bool, error)
	Run(context.Context, CDCEmitter) error
}

type CDCCorrelationInput struct {
	sourceTransactionID string
	cursor              []byte
}

// RuntimeCDCCorrelationInput transfers validated, owned source identity into
// the installed adapter's required correlation capability.
func RuntimeCDCCorrelationInput(sourceTransactionID string, cursor []byte) CDCCorrelationInput {
	return CDCCorrelationInput{sourceTransactionID: sourceTransactionID, cursor: append([]byte(nil), cursor...)}
}

func (input CDCCorrelationInput) SourceTransactionID() string { return input.sourceTransactionID }
func (input CDCCorrelationInput) Cursor() []byte              { return append([]byte(nil), input.cursor...) }

type CDCEmitter interface {
	Emit(context.Context, CDCBatchInput) error
}

type CDCBatchInput struct {
	SourceTransactionID string
	RecordedAt          time.Time
	Cursor              []byte
	Changes             []CDCChangeInput
}

type CDCChangeInput struct {
	Ordinal uint32
	Model   golem.ModelID
	Action  golem.EventAction
	Before  *golem.RuntimeModelRow
	After   *golem.RuntimeModelRow
}

type CDCIdentity struct {
	Name     string
	Version  string
	Provider golem.Provider
}

const (
	MaximumCDCAdapters               = 64
	MaximumCDCChangesPerTransaction  = 4096
	MaximumCDCCursorBytes            = 1 << 20
	MaximumCDCSourceTransactionBytes = 4096
	MaximumCDCIdentityBytes          = 128
)

var canonicalCDCIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+/\-]*$`)

func ValidateCDCIdentity(identity CDCIdentity) error {
	if len(identity.Name) == 0 || len(identity.Name) > MaximumCDCIdentityBytes ||
		len(identity.Version) == 0 || len(identity.Version) > MaximumCDCIdentityBytes ||
		!canonicalCDCIdentity.MatchString(identity.Name) || !canonicalCDCIdentity.MatchString(identity.Version) ||
		(identity.Provider != golem.SQLite && identity.Provider != golem.PostgreSQL) {
		return Failure(CodeCDCInvalid)
	}
	return nil
}

func (identity CDCIdentity) CanonicalName() string {
	if ValidateCDCIdentity(identity) != nil {
		return ""
	}
	return string(identity.Provider) + ":" + identity.Name + "@" + identity.Version
}

func ValidateCDCAdapters(provider golem.Provider, adapters []CDCAdapter) ([]CDCIdentity, error) {
	if len(adapters) > MaximumCDCAdapters {
		return nil, Failure(CodeEventConfig)
	}
	identities := make([]CDCIdentity, len(adapters))
	seen := make(map[string]struct{}, len(adapters))
	for index, adapter := range adapters {
		if adapter == nil {
			return nil, Failure(CodeCDCInvalid)
		}
		identity := adapter.Identity()
		if ValidateCDCIdentity(identity) != nil || identity.Provider != provider {
			return nil, Failure(CodeCDCInvalid)
		}
		canonical := identity.CanonicalName()
		if _, exists := seen[canonical]; exists {
			return nil, Failure(CodeCDCInvalid)
		}
		seen[canonical] = struct{}{}
		identities[index] = identity
	}
	return identities, nil
}

func CDCAdapterCapabilities(provider golem.Provider, adapters []CDCAdapter) ([]string, bool, error) {
	identities, err := ValidateCDCAdapters(provider, adapters)
	if err != nil {
		return nil, false, err
	}
	names := make([]string, len(identities))
	for index, identity := range identities {
		names[index] = identity.CanonicalName()
	}
	return names, len(names) != 0, nil
}
