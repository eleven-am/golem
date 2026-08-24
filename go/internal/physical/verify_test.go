package physical

import (
	"strings"
	"testing"
)

func retainedHistoricalSchema() PhysicalSchema {
	schema := sqliteSocialSchema()
	schema.Version, schema.CanonicalVersion = 2, 2
	return schema
}

func unfingerprintableSchema() PhysicalSchema {
	schema := sqliteSocialSchema()
	schema.Version, schema.CanonicalVersion = 4, 4
	return schema
}

func TestCompareFingerprintsRefusesWhenFingerprintingFails(t *testing.T) {
	expected := unfingerprintableSchema()
	actual := unfingerprintableSchema()
	actual.Tables = actual.Tables[:1]
	if _, err := HistoricalPhysicalFingerprint(expected); err == nil {
		t.Fatal("fixture must be unfingerprintable for this gate to discriminate")
	}
	err := CompareFingerprints(expected, actual)
	if err == nil {
		t.Fatal("comparison accepted two schemas it could not fingerprint")
	}
	if !strings.Contains(err.Error(), "application physical fingerprint is unavailable") {
		t.Fatalf("comparison refused for the wrong reason: %v", err)
	}
}

func TestCompareFingerprintsRefusesWhenOnlyTheObservedSchemaFailsToFingerprint(t *testing.T) {
	err := CompareFingerprints(sqliteSocialSchema(), unfingerprintableSchema())
	if err == nil {
		t.Fatal("comparison accepted an observed schema it could not fingerprint")
	}
	if !strings.Contains(err.Error(), "actual application physical fingerprint is unavailable") {
		t.Fatalf("comparison refused for the wrong reason: %v", err)
	}
}

func TestCompareFingerprintsAcceptsRetainedHistoricalFormat(t *testing.T) {
	schema := retainedHistoricalSchema()
	if _, err := PhysicalFingerprint(schema); err == nil {
		t.Fatal("fixture must be rejected by the current-only family for this gate to discriminate")
	}
	if err := CompareFingerprints(schema, schema); err != nil {
		t.Fatalf("comparison refused a schema in a retained canonical format: %v", err)
	}
}

func TestCompareFingerprintsRefusesApplicationDrift(t *testing.T) {
	actual := sqliteSocialSchema()
	actual.Tables[1].Columns = actual.Tables[1].Columns[:2]
	err := CompareFingerprints(sqliteSocialSchema(), actual)
	if err == nil || !strings.Contains(err.Error(), "application physical fingerprint mismatch") {
		t.Fatalf("comparison did not refuse application drift: %v", err)
	}
}

func TestCompareFingerprintsRefusesSystemDrift(t *testing.T) {
	actual := sqliteSocialSchema()
	actual.System.Objects = nil
	err := CompareFingerprints(sqliteSocialSchema(), actual)
	if err == nil || !strings.Contains(err.Error(), "system physical fingerprint mismatch") {
		t.Fatalf("comparison did not refuse system drift: %v", err)
	}
}

func TestCompareFingerprintDigestRefusesWhenFingerprintingFails(t *testing.T) {
	recorded, err := HistoricalPhysicalFingerprint(sqliteSocialSchema())
	if err != nil {
		t.Fatal(err)
	}
	compareErr := CompareFingerprintDigest(unfingerprintableSchema(), recorded.String())
	if compareErr == nil {
		t.Fatal("comparison accepted a schema it could not fingerprint")
	}
	if !strings.Contains(compareErr.Error(), "actual application physical fingerprint is unavailable") {
		t.Fatalf("comparison refused for the wrong reason: %v", compareErr)
	}
}

func TestCompareFingerprintDigestRefusesUnusableRecordedDigest(t *testing.T) {
	err := CompareFingerprintDigest(sqliteSocialSchema(), "")
	if err == nil || !strings.Contains(err.Error(), "recorded application physical fingerprint is unusable") {
		t.Fatalf("comparison did not refuse an unusable recorded digest: %v", err)
	}
}

func TestCompareFingerprintDigestAcceptsRetainedHistoricalFormat(t *testing.T) {
	schema := retainedHistoricalSchema()
	recorded, err := HistoricalPhysicalFingerprint(schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, currentErr := PhysicalFingerprint(schema); currentErr == nil {
		t.Fatal("fixture must be rejected by the current-only family for this gate to discriminate")
	}
	if err := CompareFingerprintDigest(schema, recorded.String()); err != nil {
		t.Fatalf("comparison refused a schema in a retained canonical format: %v", err)
	}
}

func TestCompareFingerprintDigestRefusesDrift(t *testing.T) {
	recorded, err := HistoricalPhysicalFingerprint(sqliteSocialSchema())
	if err != nil {
		t.Fatal(err)
	}
	actual := sqliteSocialSchema()
	actual.Tables[1].Columns = actual.Tables[1].Columns[:2]
	compareErr := CompareFingerprintDigest(actual, recorded.String())
	if compareErr == nil || !strings.Contains(compareErr.Error(), "application physical fingerprint mismatch") {
		t.Fatalf("comparison did not refuse drift against the recorded digest: %v", compareErr)
	}
}
