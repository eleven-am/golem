package physical

import (
	"bytes"
	"strings"
	"testing"
)

func TestDataIncarnationSelectsExplicitPhysicalFour(t *testing.T) {
	ordinary := sqliteSocialSchema()
	old, err := CanonicalEncode(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	opted := sqliteSocialSchema()
	opted.Version, opted.CanonicalVersion = DataIncarnationFormatVersion, DataIncarnationFormatVersion
	opted.System.Objects = append(opted.System.Objects, DataIncarnationSystemObjectV1())
	payload, err := CanonicalEncode(opted)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := CanonicalDecode(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !HasDataIncarnation(decoded.System) {
		t.Fatal("lost singleton")
	}
	if _, err := CanonicalDecodeReviewedHistory(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalDecodeHistoricalV3(payload); err == nil {
		t.Fatal("V3 reader admitted V4")
	}
	opted.Version, opted.CanonicalVersion = 3, 3
	if _, err := CanonicalEncode(opted); err == nil {
		t.Fatal("V3 admitted new singleton")
	}
	if _, err := CanonicalEncodeHistoricalV3(opted); err == nil {
		t.Fatal("frozen V3 admitted new singleton")
	}
	again, err := CanonicalEncode(ordinary)
	if err != nil || !bytes.Equal(old, again) {
		t.Fatal("ordinary bytes changed")
	}
}

func TestPhysicalFourRefusesMissingFeatureAndMixedEnvelope(t *testing.T) {
	for _, emptySystem := range []bool{false, true} {
		schema := sqliteSocialSchema()
		schema.Version, schema.CanonicalVersion = DataIncarnationFormatVersion, DataIncarnationFormatVersion
		if emptySystem {
			schema.System = SystemSchema{}
		}
		payload, err := canonicalValueVersion(schema, DataIncarnationFormatVersion)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CanonicalDecode(payload); err == nil {
			t.Fatalf("physical4 without feature admitted (emptySystem=%v)", emptySystem)
		}
	}
	schema := sqliteSocialSchema()
	schema.Version = DataIncarnationFormatVersion
	payload, err := canonicalValueVersion(schema, DataIncarnationFormatVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalDecode(payload); err == nil {
		t.Fatal("mixed physical/canonical envelope admitted")
	}

	schema = sqliteSocialSchema()
	payload, err = canonicalValueVersion(schema, DataIncarnationFormatVersion)
	if err != nil {
		t.Fatal(err)
	}
	for _, decode := range []func([]byte) (PhysicalSchema, error){CanonicalDecode, CanonicalDecodeReviewedHistory} {
		if _, err := decode(payload); err == nil || !strings.Contains(err.Error(), "canonical envelope version 4 requires exact physical/canonical versions 4/4") {
			t.Fatalf("outer v4 with embedded v3 must fail envelope validation: %v", err)
		}
	}
}

func TestPhysicalFourEmptySystemIsOnlyForEmptyMigrationBefore(t *testing.T) {
	empty := sqliteSocialSchema()
	empty.Version, empty.CanonicalVersion = DataIncarnationFormatVersion, DataIncarnationFormatVersion
	empty.System = SystemSchema{}
	empty.Tables, empty.Extensions, empty.Unmanaged = nil, nil, nil
	payload, err := CanonicalEncode(empty)
	if err != nil {
		t.Fatal(err)
	}
	for _, decode := range []func([]byte) (PhysicalSchema, error){CanonicalDecode, CanonicalDecodeReviewedHistory} {
		if _, err := decode(payload); err != nil {
			t.Fatalf("empty migration-before snapshot refused: %v", err)
		}
	}
	for _, shape := range []string{"tables", "extensions", "unmanaged"} {
		t.Run(shape, func(t *testing.T) {
			schema := empty
			switch shape {
			case "tables":
				schema.Tables = sqliteSocialSchema().Tables
			case "extensions":
				schema.Extensions = []Extension{{}}
			case "unmanaged":
				schema.Unmanaged = []UnmanagedObject{{Kind: "table", Name: "external_data"}}
			}
			if _, err := CanonicalEncode(schema); err == nil || !strings.Contains(err.Error(), "physical v4 requires the data incarnation singleton") {
				t.Fatalf("nonempty %s shape must require singleton: %v", shape, err)
			}
		})
	}
}
