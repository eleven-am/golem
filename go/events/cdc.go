package events

import (
	"context"
	"fmt"
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
	if detail := cdcIdentityDefect(identity); detail != "" {
		return Failf(CodeCDCInvalid, "%s", detail)
	}
	return nil
}

func cdcIdentityDefect(identity CDCIdentity) string {
	for _, field := range []struct{ name, value string }{
		{"Name", identity.Name},
		{"Version", identity.Version},
	} {
		if len(field.value) == 0 {
			return field.name + " must not be empty"
		}
		if len(field.value) > MaximumCDCIdentityBytes {
			return fmt.Sprintf("%s must not exceed %d bytes", field.name, MaximumCDCIdentityBytes)
		}
		if !canonicalCDCIdentity.MatchString(field.value) {
			return field.name + " must match " + canonicalCDCIdentity.String()
		}
	}
	if identity.Provider != golem.SQLite && identity.Provider != golem.PostgreSQL {
		return fmt.Sprintf("Provider must be %q or %q", golem.SQLite, golem.PostgreSQL)
	}
	return ""
}

func (identity CDCIdentity) CanonicalName() string {
	if ValidateCDCIdentity(identity) != nil {
		return ""
	}
	return string(identity.Provider) + ":" + identity.Name + "@" + identity.Version
}

func ValidateCDCAdapters(provider golem.Provider, adapters []CDCAdapter) ([]CDCIdentity, error) {
	if len(adapters) > MaximumCDCAdapters {
		return nil, Failf(CodeEventConfig, "adapters must not exceed %d entries, got %d", MaximumCDCAdapters, len(adapters))
	}
	identities := make([]CDCIdentity, len(adapters))
	seen := make(map[string]struct{}, len(adapters))
	for index, adapter := range adapters {
		if adapter == nil {
			return nil, Failf(CodeCDCInvalid, "adapters[%d] must not be nil", index)
		}
		identity := adapter.Identity()
		if detail := cdcIdentityDefect(identity); detail != "" {
			return nil, Failf(CodeCDCInvalid, "adapters[%d]: %s", index, detail)
		}
		if identity.Provider != provider {
			return nil, Failf(CodeCDCInvalid, "adapters[%d] declares a Provider that does not match the runtime provider", index)
		}
		canonical := identity.CanonicalName()
		if _, exists := seen[canonical]; exists {
			return nil, Failf(CodeCDCInvalid, "adapters[%d] repeats the canonical identity of an earlier adapter", index)
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
