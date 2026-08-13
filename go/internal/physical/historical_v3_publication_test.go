package physical

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestHistoricalV3ReviewedDispatchDoesNotRouteThroughMutableCurrentProfile(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve physical source")
	}
	directory := filepath.Dir(source)
	type retainedCall struct {
		text  string
		count int
	}
	want := map[string][]retainedCall{
		"normalize.go": {
			{text: "return NormalizeHistoricalV3(schema)", count: 1},
			{text: "return normalizeHistoricalV3Tagged(schema)", count: 1},
		},
		"canonical.go": {
			{text: "return canonicalHistoricalValueVersion(value, 3, historicalV3StructFields)", count: 1},
			{text: "return canonicalHistoricalValueVersion(normalized, 3, historicalV3StructFields)", count: 1},
		},
		"decode.go": {
			{text: "decoder.frozenProjection = historicalV3StructFields", count: 1},
			{text: "return validateHistoricalV3(schema)", count: 1},
		},
		"fingerprint.go": {
			{text: "return historicalV3PhysicalFingerprintNormalized(normalized)", count: 2},
			{text: "return historicalV3UnmanagedAllowlistFingerprintNormalized(normalized)", count: 2},
			{text: "return historicalV3SystemFingerprintNormalized(normalized)", count: 2},
		},
	}
	for name, calls := range want {
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range calls {
			if got := strings.Count(string(raw), call.text); got != call.count {
				t.Fatalf("reviewed v3 dispatch call %q count=%d want=%d in %s", call.text, got, call.count, name)
			}
		}
	}
}

func TestHistoricalV3FrozenProfileMatchesReviewedCurrentArtifacts(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		schema  PhysicalSchema
		storage StorageKind
	}{
		{name: "sqlite", schema: sqliteSocialSchema(), storage: StorageSQLiteInteger},
		{name: "postgresql", schema: postgresqlSocialSchema(), storage: StoragePostgreSQLBigInt},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			schema := fixture.schema
			field := schema.Tables[0].Columns[0].ID
			schema.Tables[0].OptimisticConcurrency = &field
			schema.Tables[0].Columns[0].Storage = StorageType{Kind: fixture.storage}
			schema.Tables[0].Columns[0].Nullable = false
			schema.Tables[0].Columns[0].Default = PhysicalDefault{Kind: DefaultNone}
			schema.Tables[0].Columns[0].Generated = nil
			schema.Tables[0].Columns[0].Collation = nil
			schema.Tables[0].PrimaryKey = nil

			current, err := Normalize(schema)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := NormalizeHistoricalV3(schema)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(current, frozen) {
				t.Fatal("frozen v3 normalization differs from reviewed current v3")
			}
			if frozen.Tables[0].OptimisticConcurrency == schema.Tables[0].OptimisticConcurrency {
				t.Fatal("frozen v3 normalization retained the caller's concurrency pointer")
			}

			currentBytes, err := CanonicalEncode(schema)
			if err != nil {
				t.Fatal(err)
			}
			frozenBytes, err := CanonicalEncodeHistoricalV3(schema)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(currentBytes, frozenBytes) {
				t.Fatal("frozen v3 canonical bytes differ from reviewed current v3")
			}
			decoded, err := CanonicalDecodeHistoricalV3(frozenBytes)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Version != 3 || decoded.CanonicalVersion != 3 || decoded.Tables[0].OptimisticConcurrency == nil || *decoded.Tables[0].OptimisticConcurrency != field {
				t.Fatal("frozen v3 decode lost reviewed v3 identity")
			}

			assertSameDigest(t, "physical", PhysicalFingerprint, HistoricalV3PhysicalFingerprint, schema)
			assertSameDigest(t, "unmanaged allowlist", UnmanagedAllowlistFingerprint, HistoricalV3UnmanagedAllowlistFingerprint, schema)
			currentSystem, err := SystemFingerprint(schema.Provider, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			frozenSystem, err := HistoricalV3SystemFingerprint(schema)
			if err != nil {
				t.Fatal(err)
			}
			if currentSystem != frozenSystem {
				t.Fatal("frozen v3 system fingerprint differs from reviewed current v3")
			}
			for name, fingerprint := range map[string]func(PhysicalSchema) (Digest, error){
				"reviewed physical":  HistoricalPhysicalFingerprint,
				"reviewed unmanaged": HistoricalUnmanagedAllowlistFingerprint,
				"reviewed system":    HistoricalSystemFingerprint,
			} {
				got, fingerprintErr := fingerprint(schema)
				if fingerprintErr != nil {
					t.Fatal(fingerprintErr)
				}
				var want Digest
				switch name {
				case "reviewed physical":
					want, _ = HistoricalV3PhysicalFingerprint(schema)
				case "reviewed unmanaged":
					want, _ = HistoricalV3UnmanagedAllowlistFingerprint(schema)
				default:
					want = frozenSystem
				}
				if got != want {
					t.Fatalf("%s dispatch bypassed frozen v3", name)
				}
			}
		})
	}
}

func TestHistoricalV3DecoderMatrixIsClosed(t *testing.T) {
	v3, err := CanonicalEncodeHistoricalV3(sqliteSocialSchema())
	if err != nil {
		t.Fatal(err)
	}
	v2Schema := sqliteSocialSchema()
	v2Schema.Version, v2Schema.CanonicalVersion = 2, 2
	v2, err := CanonicalEncodeHistoricalV2(v2Schema)
	if err != nil {
		t.Fatal(err)
	}
	v1Schema := sqliteSocialSchema()
	v1Schema.Version, v1Schema.CanonicalVersion = 1, 1
	v1, err := canonicalValueVersion(v1Schema, 1)
	if err != nil {
		t.Fatal(err)
	}

	for name, payload := range map[string][]byte{"v1": v1, "v2": v2} {
		if _, err := CanonicalDecodeHistoricalV3(payload); err == nil || !strings.Contains(err.Error(), "canonical format version") {
			t.Fatalf("v3-only decoder accepted %s: %v", name, err)
		}
	}
	if _, err := CanonicalDecodeHistorical(v3); err == nil {
		t.Fatal("v1-only decoder accepted v3")
	}
	if _, err := CanonicalDecodeHistoricalV2(v3); err == nil {
		t.Fatal("v2-only decoder accepted v3")
	}
	if _, err := CanonicalDecode(v2); err == nil {
		t.Fatal("active decoder accepted v2")
	}
	if _, err := CanonicalDecodeReviewedHistory(v3); err != nil {
		t.Fatalf("reviewed decoder refused frozen v3: %v", err)
	}

	future := append([]byte(nil), v3...)
	future[1+len(canonicalMagic)] = 4
	for name, decode := range map[string]func([]byte) (PhysicalSchema, error){
		"active":   CanonicalDecode,
		"v3-only":  CanonicalDecodeHistoricalV3,
		"reviewed": CanonicalDecodeReviewedHistory,
	} {
		if _, err := decode(future); err == nil || !strings.Contains(err.Error(), "canonical format version") {
			t.Fatalf("%s decoder accepted relabelled future bytes: %v", name, err)
		}
	}
}

func TestHistoricalV3ValidatorOwnsConcurrencyFailures(t *testing.T) {
	schema := sqliteSocialSchema()
	field := schema.Tables[0].Columns[0].ID
	schema.Tables[0].OptimisticConcurrency = &field
	schema.Tables[0].Columns[0].Storage = StorageType{Kind: StorageSQLiteInteger}
	schema.Tables[0].Columns[0].Nullable = true
	schema.Tables[0].PrimaryKey = nil
	_, err := NormalizeHistoricalV3(schema)
	if err == nil || !strings.Contains(err.Error(), "non-null") {
		t.Fatalf("frozen v3 validator error=%v; want concurrency non-null failure", err)
	}
	var diagnostics historicalV3ValidationErrors
	if !errors.As(err, &diagnostics) {
		t.Fatalf("frozen v3 validator returned untyped diagnostics: %T", err)
	}
	found := false
	for _, diagnostic := range diagnostics {
		found = found || diagnostic.Code == historicalV3CodeConcurrency && strings.Contains(diagnostic.Path, ".optimisticConcurrency")
	}
	if !found {
		t.Fatalf("frozen v3 diagnostics=%#v; want concurrency code/path", diagnostics)
	}
}

func assertSameDigest(t *testing.T, name string, current, frozen func(PhysicalSchema) (Digest, error), schema PhysicalSchema) {
	t.Helper()
	want, err := current(schema)
	if err != nil {
		t.Fatal(err)
	}
	got, err := frozen(schema)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("frozen v3 %s fingerprint differs from reviewed current v3", name)
	}
}
