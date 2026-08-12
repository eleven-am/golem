package schema

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestHistoricalProviderDocumentV1DispatchesFrozenProfile(t *testing.T) {
	snapshot := physical.PhysicalSchema{
		Version:          1,
		CanonicalVersion: 1,
		Provider: physical.ProviderManifest{
			Provider:       ir.SQLite,
			Driver:         physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"},
			MinimumVersion: physical.Version{Major: 3, Minor: 38},
		},
		Namespace: physical.Namespace{Name: "main"},
		Tables: []physical.PhysicalTable{{
			ID:   "81000000000000000000000000000001",
			Name: "items",
			Columns: []physical.PhysicalColumn{{
				ID:      "82000000000000000000000000000001",
				Name:    "id",
				Ordinal: 0,
				Storage: physical.StorageType{Kind: physical.StorageSQLiteText},
				Default: physical.PhysicalDefault{Kind: physical.DefaultNone},
			}},
		}},
	}
	normalized, err := physical.NormalizeHistoricalV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := physical.HistoricalV1CanonicalFragment(normalized)
	if err != nil {
		t.Fatal(err)
	}
	physicalFingerprint, err := physical.HistoricalPhysicalFingerprint(normalized)
	if err != nil {
		t.Fatal(err)
	}
	systemFingerprint, err := physical.HistoricalSystemFingerprint(normalized)
	if err != nil {
		t.Fatal(err)
	}
	document := golem.GeneratedSchemaDocument(1, 1, golem.SchemaDigest(physicalFingerprint), payload)
	decoded, err := decodeProviderSchemaDocument(document, golem.SchemaDigest(systemFingerprint), true)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != 1 || decoded.CanonicalVersion != 1 || decoded.Provider.Provider != ir.SQLite {
		t.Fatalf("historical physical-v1 projection = %#v", decoded)
	}
}

func TestHistoricalProviderDocumentV3DispatchesFrozenProfile(t *testing.T) {
	bundle, _ := testBundle(t)
	provider := bundle.Providers()[0]
	decoded, err := decodeProviderSchemaDocument(provider.Schema(), provider.SystemFingerprint(), true)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != 3 || decoded.CanonicalVersion != 3 {
		t.Fatalf("historical physical-v3 projection = %d/%d", decoded.Version, decoded.CanonicalVersion)
	}

	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve historical provider dispatch test source")
	}
	bootstrap, err := os.ReadFile(strings.TrimSuffix(source, "historical_provider_test.go") + "bootstrap.go")
	if err != nil {
		t.Fatal(err)
	}
	const frozenRoute = "case 3:\n\t\tdecoded, err = physical.CanonicalDecodeHistoricalV3(document.Bytes())"
	if !strings.Contains(string(bootstrap), frozenRoute) {
		t.Fatal("historical physical-v3 provider documents are not dispatched through the frozen v3 decoder")
	}
}
