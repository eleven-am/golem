package events

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestCDCAdapterInventoryIsBoundedProviderExactAndUnique(t *testing.T) {
	valid := stubCDCAdapter{identity: CDCIdentity{Name: "wal-reader", Version: "1.0.0+build", Provider: golem.PostgreSQL}}
	identities, err := ValidateCDCAdapters(golem.PostgreSQL, []CDCAdapter{valid})
	if err != nil || len(identities) != 1 {
		t.Fatalf("valid inventory: identities=%v error=%v", identities, err)
	}
	for name, adapters := range map[string][]CDCAdapter{
		"nil":            {nil},
		"duplicate":      {valid, valid},
		"wrong provider": {valid},
	} {
		t.Run(name, func(t *testing.T) {
			provider := golem.PostgreSQL
			if name == "wrong provider" {
				provider = golem.SQLite
			}
			if _, err := ValidateCDCAdapters(provider, adapters); codeForCDC(t, err) != CodeCDCInvalid {
				t.Fatalf("inventory error = %v", err)
			}
		})
	}
	tooMany := make([]CDCAdapter, MaximumCDCAdapters+1)
	for index := range tooMany {
		tooMany[index] = stubCDCAdapter{identity: CDCIdentity{Name: "adapter" + string(rune('A'+index%26)), Version: "v" + string(rune('A'+index/26)), Provider: golem.PostgreSQL}}
	}
	if _, err := ValidateCDCAdapters(golem.PostgreSQL, tooMany); codeForCDC(t, err) != CodeEventConfig {
		t.Fatalf("oversized inventory = %v", err)
	}
}

func TestCDCIdentityRejectsAmbiguousOrUnknownValues(t *testing.T) {
	for _, identity := range []CDCIdentity{
		{},
		{Name: "bad:name", Version: "1", Provider: golem.PostgreSQL},
		{Name: "good", Version: "bad@version", Provider: golem.PostgreSQL},
		{Name: "good", Version: "1", Provider: "mysql"},
	} {
		if err := ValidateCDCIdentity(identity); codeForCDC(t, err) != CodeCDCInvalid {
			t.Fatalf("identity accepted: %+v", identity)
		}
	}
}

type stubCDCAdapter struct{ identity CDCIdentity }

func (adapter stubCDCAdapter) Identity() CDCIdentity         { return adapter.identity }
func (stubCDCAdapter) Run(context.Context, CDCEmitter) error { return nil }
func (stubCDCAdapter) CorrelatesGolemTransaction(context.Context, CDCCorrelationInput) (bool, error) {
	return false, nil
}

func codeForCDC(t testing.TB, err error) ErrorCode {
	t.Helper()
	code, ok := CodeOf(err)
	if !ok {
		t.Fatalf("error = %v", err)
	}
	return code
}
