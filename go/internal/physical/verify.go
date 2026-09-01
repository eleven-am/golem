package physical

import "fmt"

func CompareFingerprints(expected, actual PhysicalSchema) error {
	expectedPhysical, err := HistoricalPhysicalFingerprint(expected)
	if err != nil {
		return fmt.Errorf("expected application physical fingerprint is unavailable: %w", err)
	}
	actualPhysical, err := HistoricalPhysicalFingerprint(actual)
	if err != nil {
		return fmt.Errorf("actual application physical fingerprint is unavailable: %w", err)
	}
	if expectedPhysical != actualPhysical {
		return fmt.Errorf("application physical fingerprint mismatch: expected %s actual %s", expectedPhysical, actualPhysical)
	}
	expectedSystem, err := HistoricalSystemFingerprint(expected)
	if err != nil {
		return fmt.Errorf("expected system physical fingerprint is unavailable: %w", err)
	}
	actualSystem, err := HistoricalSystemFingerprint(actual)
	if err != nil {
		return fmt.Errorf("actual system physical fingerprint is unavailable: %w", err)
	}
	if expectedSystem != actualSystem {
		return fmt.Errorf("system physical fingerprint mismatch: expected %s actual %s", expectedSystem, actualSystem)
	}
	return nil
}

func CompareFingerprintDigest(actual PhysicalSchema, want string) error {
	if _, err := ParseDigest(want); err != nil {
		return fmt.Errorf("recorded application physical fingerprint is unusable: %w", err)
	}
	actualPhysical, err := HistoricalPhysicalFingerprint(actual)
	if err != nil {
		return fmt.Errorf("actual application physical fingerprint is unavailable: %w", err)
	}
	if actualPhysical.String() != want {
		return fmt.Errorf("application physical fingerprint mismatch: expected %s actual %s", want, actualPhysical)
	}
	return nil
}
