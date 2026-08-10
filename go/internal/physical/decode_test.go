package physical

import (
	"bytes"
	"strings"
	"testing"
)

func TestCanonicalDecodeRoundTripsProviderSchemasExactly(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema PhysicalSchema
	}{
		{name: "sqlite", schema: sqliteSocialSchema()},
		{name: "postgresql", schema: postgresqlSocialSchema()},
	} {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := Normalize(test.schema)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := CanonicalEncode(test.schema)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := CanonicalDecode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Provider.Provider != normalized.Provider.Provider || decoded.Namespace != normalized.Namespace || len(decoded.Tables) != len(normalized.Tables) {
				t.Fatalf("decoded schema identity differs from normalized input: %#v", decoded)
			}
			roundTrip, err := CanonicalEncode(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(roundTrip, encoded) {
				t.Fatal("decode/re-encode changed canonical bytes")
			}

			physicalFingerprint, err := PhysicalFingerprint(test.schema)
			if err != nil {
				t.Fatal(err)
			}
			systemFingerprint, err := SystemFingerprint(test.schema.Provider, test.schema.System)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := CanonicalDecodeVerified(encoded, physicalFingerprint, systemFingerprint); err != nil {
				t.Fatalf("verified decode: %v", err)
			}
		})
	}
}

func TestCanonicalDecodeRejectsMalformedDocuments(t *testing.T) {
	encoded, err := CanonicalEncode(sqliteSocialSchema())
	if err != nil {
		t.Fatal(err)
	}
	rootOffset := 1 + len(canonicalMagic) + 1

	tests := []struct {
		name     string
		payload  []byte
		contains string
	}{
		{name: "empty", payload: nil, contains: "truncated"},
		{name: "malformed magic length", payload: append(bytes.Repeat([]byte{0xff}, 10), 2), contains: "malformed unsigned varint"},
		{name: "wrong magic", payload: replaceCanonicalText(t, encoded, string(canonicalMagic), strings.Repeat("x", len(canonicalMagic))), contains: "unknown canonical document magic"},
		{name: "unknown version", payload: mutateByte(t, encoded, 1+len(canonicalMagic), byte(CanonicalFormatVersion+1)), contains: "canonical format version"},
		{name: "unknown root tag", payload: mutateByte(t, encoded, rootOffset, 'x'), contains: "type tag"},
		{name: "unknown struct type", payload: replaceCanonicalText(t, encoded, "PhysicalSchema", "PhysicalSchemX"), contains: "unknown struct type"},
		{name: "unknown field", payload: replaceCanonicalText(t, encoded, "CanonicalVersion", "CanonicalVersioX"), contains: "unknown or out-of-order field"},
		{name: "trailing bytes", payload: append(append([]byte(nil), encoded...), 0), contains: "trailing bytes"},
		{name: "invalid schema", payload: replaceCanonicalText(t, encoded, "github.com/ncruces/go-sqlite3", "github.com/ncruces/go-sqliteX"), contains: "invalid schema"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CanonicalDecode(test.payload)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v; want text %q", err, test.contains)
			}
		})
	}
}

func TestCanonicalDecodeRejectsTruncationAtStructuralBoundaries(t *testing.T) {
	encoded, err := CanonicalEncode(postgresqlSocialSchema())
	if err != nil {
		t.Fatal(err)
	}
	cuts := []int{0, 1, len(canonicalMagic), len(canonicalMagic) + 1, len(encoded) / 4, len(encoded) / 2, len(encoded) - 1}
	for _, cut := range cuts {
		if _, err := CanonicalDecode(encoded[:cut]); err == nil {
			t.Fatalf("truncation at byte %d was accepted", cut)
		}
	}
}

func TestCanonicalDecodeRejectsValidButNonCanonicalOrder(t *testing.T) {
	schema := sqliteSocialSchema()
	reverse(schema.Tables)
	encoded, err := canonicalValue(schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalDecode(encoded); err == nil || !strings.Contains(err.Error(), "not in canonical normalized form") {
		t.Fatalf("error = %v; want canonical-order rejection", err)
	}
}

func TestCanonicalDecodeVerifiedRejectsEachFingerprintDomain(t *testing.T) {
	schema := sqliteSocialSchema()
	encoded, err := CanonicalEncode(schema)
	if err != nil {
		t.Fatal(err)
	}
	physicalFingerprint, _ := PhysicalFingerprint(schema)
	systemFingerprint, _ := SystemFingerprint(schema.Provider, schema.System)

	badPhysical := physicalFingerprint
	badPhysical[0] ^= 0xff
	if _, err := CanonicalDecodeVerified(encoded, badPhysical, systemFingerprint); err == nil || !strings.Contains(err.Error(), "physical fingerprint mismatch") {
		t.Fatalf("physical mismatch error = %v", err)
	}
	badSystem := systemFingerprint
	badSystem[0] ^= 0xff
	if _, err := CanonicalDecodeVerified(encoded, physicalFingerprint, badSystem); err == nil || !strings.Contains(err.Error(), "system fingerprint mismatch") {
		t.Fatalf("system mismatch error = %v", err)
	}
}

func postgresqlSocialSchema() PhysicalSchema {
	schema := sqliteSocialSchema()
	schema.Provider = PostgreSQLManifest()
	schema.Namespace = Namespace{Name: "public"}
	schema.System.Namespace = Namespace{Name: "_golem"}
	for tableIndex := range schema.Tables {
		table := &schema.Tables[tableIndex]
		table.RequiredCapabilities = nil
		table.Checks = nil
		for columnIndex := range table.Columns {
			column := &table.Columns[columnIndex]
			column.RequiredCapabilities = nil
			switch column.Storage.Kind {
			case StorageSQLiteInteger:
				column.Storage.Kind = StoragePostgreSQLBoolean
			case StorageSQLiteText:
				column.Storage.Kind = StoragePostgreSQLText
			}
		}
		for foreignKeyIndex := range table.ForeignKeys {
			table.ForeignKeys[foreignKeyIndex].RequiredCapabilities = nil
		}
	}
	return schema
}

func mutateByte(t *testing.T, source []byte, offset int, value byte) []byte {
	t.Helper()
	if offset < 0 || offset >= len(source) {
		t.Fatalf("mutation offset %d outside %d-byte source", offset, len(source))
	}
	result := append([]byte(nil), source...)
	result[offset] = byte(value)
	return result
}

func replaceCanonicalText(t *testing.T, source []byte, old, replacement string) []byte {
	t.Helper()
	if len(old) != len(replacement) {
		t.Fatalf("replacement changes encoded length: %q -> %q", old, replacement)
	}
	needle := append([]byte{byte(len(old))}, old...)
	replace := append([]byte{byte(len(replacement))}, replacement...)
	if bytes.Count(source, needle) != 1 {
		t.Fatalf("canonical text %q occurrence count = %d; want 1", old, bytes.Count(source, needle))
	}
	return bytes.Replace(append([]byte(nil), source...), needle, replace, 1)
}
