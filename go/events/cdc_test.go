package events

import (
	"context"
	"strings"
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

func TestCDCIdentityErrorsNameTheViolatedField(t *testing.T) {
	for name, testCase := range map[string]struct {
		identity CDCIdentity
		names    string
	}{
		"absent name":      {CDCIdentity{Version: "1", Provider: golem.PostgreSQL}, "Name"},
		"oversized name":   {CDCIdentity{Name: strings.Repeat("a", MaximumCDCIdentityBytes+1), Version: "1", Provider: golem.PostgreSQL}, "Name"},
		"malformed name":   {CDCIdentity{Name: ".wal", Version: "1", Provider: golem.PostgreSQL}, "Name"},
		"absent version":   {CDCIdentity{Name: "wal", Provider: golem.PostgreSQL}, "Version"},
		"oversized ver":    {CDCIdentity{Name: "wal", Version: strings.Repeat("1", MaximumCDCIdentityBytes+1), Provider: golem.PostgreSQL}, "Version"},
		"malformed ver":    {CDCIdentity{Name: "wal", Version: "@1", Provider: golem.PostgreSQL}, "Version"},
		"unknown provider": {CDCIdentity{Name: "wal", Version: "1", Provider: golem.Provider("mysql")}, "Provider"},
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateCDCIdentity(testCase.identity)
			if codeForCDC(t, err) != CodeCDCInvalid {
				t.Fatalf("code = %v", err)
			}
			if !strings.Contains(err.Error(), testCase.names) {
				t.Fatalf("error %q does not name %s", err, testCase.names)
			}
		})
	}
}

func TestCDCAdapterErrorsSeparateIdentityFromProvider(t *testing.T) {
	tooMany := make([]CDCAdapter, MaximumCDCAdapters+1)
	for index := range tooMany {
		tooMany[index] = stubCDCAdapter{identity: CDCIdentity{Name: "wal", Version: "1", Provider: golem.PostgreSQL}}
	}
	_, err := ValidateCDCAdapters(golem.PostgreSQL, tooMany)
	if codeForCDC(t, err) != CodeEventConfig || !strings.Contains(err.Error(), "adapters") {
		t.Fatalf("inventory ceiling error = %v", err)
	}
	_, err = ValidateCDCAdapters(golem.PostgreSQL, []CDCAdapter{nil})
	if codeForCDC(t, err) != CodeCDCInvalid || !strings.Contains(err.Error(), "adapters[0]") {
		t.Fatalf("nil adapter error = %v", err)
	}
	_, err = ValidateCDCAdapters(golem.PostgreSQL, []CDCAdapter{stubCDCAdapter{identity: CDCIdentity{Version: "1", Provider: golem.PostgreSQL}}})
	if codeForCDC(t, err) != CodeCDCInvalid || !strings.Contains(err.Error(), "Name") {
		t.Fatalf("invalid identity error = %v", err)
	}
	_, err = ValidateCDCAdapters(golem.PostgreSQL, []CDCAdapter{stubCDCAdapter{identity: CDCIdentity{Name: "wal", Version: "1", Provider: golem.SQLite}}})
	if codeForCDC(t, err) != CodeCDCInvalid || !strings.Contains(err.Error(), "Provider") || strings.Contains(err.Error(), "Name must") {
		t.Fatalf("provider mismatch error = %v", err)
	}
	duplicate := stubCDCAdapter{identity: CDCIdentity{Name: "wal", Version: "1", Provider: golem.PostgreSQL}}
	_, err = ValidateCDCAdapters(golem.PostgreSQL, []CDCAdapter{duplicate, duplicate})
	if codeForCDC(t, err) != CodeCDCInvalid || !strings.Contains(err.Error(), "adapters[1]") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestCDCProviderErrorNamesTheValidProvidersWithoutEchoingTheValue(t *testing.T) {
	secret := "postgres://operator:s3cr3t@replica.internal/db"
	err := ValidateCDCIdentity(CDCIdentity{Name: "wal", Version: "1", Provider: golem.Provider(secret)})
	if codeForCDC(t, err) != CodeCDCInvalid {
		t.Fatalf("code = %v", err)
	}
	if !strings.Contains(err.Error(), "Provider") {
		t.Fatalf("error %q does not name Provider", err)
	}
	if !strings.Contains(err.Error(), string(golem.SQLite)) || !strings.Contains(err.Error(), string(golem.PostgreSQL)) {
		t.Fatalf("error %q does not name the valid providers", err)
	}
	for _, leak := range []string{secret, "s3cr3t", "replica.internal", "operator"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error %q echoed the caller-supplied provider", err)
		}
	}
}
