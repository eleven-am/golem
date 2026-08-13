package physical

import (
	"bytes"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
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

func TestCanonicalDecodeHistoricalAcceptsExactV1ButCurrentDecoderRefusesIt(t *testing.T) {
	schema := postgresqlSocialSchema()
	schema.Version = 1
	schema.CanonicalVersion = 1
	for tableIndex := range schema.Tables {
		for columnIndex := range schema.Tables[tableIndex].Columns {
			storage := &schema.Tables[tableIndex].Columns[columnIndex].Storage
			if storage.Kind == StoragePostgreSQLVarchar {
				storage.Kind = StoragePostgreSQLText
				storage.Length = 0
			}
		}
	}
	payload, err := canonicalValueVersion(schema, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalDecode(payload); err == nil || !strings.Contains(err.Error(), "canonical format version") {
		t.Fatalf("current decode error = %v; want closed v1 refusal", err)
	}
	decoded, err := CanonicalDecodeHistorical(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Version != 1 || decoded.CanonicalVersion != 1 {
		t.Fatalf("historical versions = %d/%d, want 1/1", decoded.Version, decoded.CanonicalVersion)
	}
	if reviewed, err := CanonicalDecodeReviewedHistory(payload); err != nil || reviewed.Version != 1 {
		t.Fatalf("reviewed-history v1 decode: version=%d err=%v", reviewed.Version, err)
	}
	currentPayload, err := CanonicalEncode(postgresqlSocialSchema())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalDecodeHistorical(currentPayload); err == nil || !strings.Contains(err.Error(), "canonical format version") {
		t.Fatalf("v1-only historical decoder accepted current bytes: %v", err)
	}
	if reviewed, err := CanonicalDecodeReviewedHistory(currentPayload); err != nil || reviewed.Version != SchemaFormatVersion {
		t.Fatalf("reviewed-history current decode: version=%d err=%v", reviewed.Version, err)
	}
	shuffled := schema
	shuffled.Tables = append([]PhysicalTable(nil), schema.Tables...)
	if len(shuffled.Tables) > 1 {
		shuffled.Tables[0], shuffled.Tables[1] = shuffled.Tables[1], shuffled.Tables[0]
	}
	shuffledPayload, err := canonicalValueVersion(shuffled, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalDecodeHistorical(shuffledPayload); err == nil || !strings.Contains(err.Error(), "canonical normalized form") {
		t.Fatalf("historical decoder accepted shuffled v1 collections: %v", err)
	}
	if roundTrip, err := canonicalValueVersion(decoded, 1); err != nil || !bytes.Equal(roundTrip, payload) {
		t.Fatalf("historical round trip changed bytes: err=%v", err)
	}
	for _, kind := range []StorageKind{StoragePostgreSQLVarchar, StorageKind("postgresql.future_kind")} {
		forged := schema
		forged.Tables = append([]PhysicalTable(nil), schema.Tables...)
		forged.Tables[0].Columns = append([]PhysicalColumn(nil), schema.Tables[0].Columns...)
		forged.Tables[0].Columns[0].Storage = StorageType{Kind: kind, Length: 16}
		if kind != StoragePostgreSQLVarchar {
			forged.Tables[0].Columns[0].Storage.Length = 0
		}
		forgedPayload, err := canonicalValueVersion(forged, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CanonicalDecodeHistorical(forgedPayload); err == nil || !strings.Contains(err.Error(), "historical physical v1 storage") {
			t.Fatalf("historical decoder accepted v2/future storage %q: %v", kind, err)
		}
	}
}

func TestCanonicalDecodeHistoricalV1OwnsClosedTypeAndEnumContract(t *testing.T) {
	schema := sqliteSocialSchema()
	schema.Version = 1
	schema.CanonicalVersion = 1
	if err := validateHistoricalV1SchemaShape(); err != nil {
		t.Fatalf("frozen v1 field/type projection: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*PhysicalSchema)
	}{
		{
			name: "future capability verification",
			mutate: func(value *PhysicalSchema) {
				value.Provider.Capabilities[0].Verification = CapabilityVerification("future_verification")
			},
		},
		{
			name: "future expression kind",
			mutate: func(value *PhysicalSchema) {
				value.Tables[0].Checks[0].Expression.Kind = ExpressionKind("future_expression")
			},
		},
		{
			name: "future system object kind",
			mutate: func(value *PhysicalSchema) {
				value.System.Objects[0].Kind = SystemObjectKind("future_system_object")
			},
		},
		{
			name: "future IR literal kind",
			mutate: func(value *PhysicalSchema) {
				value.Tables[0].Columns[2].Default.Literal.Kind = ir.LiteralKind("future_literal")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			forged := cloneSchema(schema)
			test.mutate(&forged)
			payload, err := canonicalValueVersion(forged, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := CanonicalDecodeHistorical(payload); err == nil || !strings.Contains(err.Error(), "frozen vocabulary") {
				t.Fatalf("historical decoder accepted future vocabulary: %v", err)
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
