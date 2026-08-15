package physical

import "testing"

func TestCurrentPhysicalFormatIsFrozenForPublication(t *testing.T) {
	if LatestFrozenPhysicalFormatVersion != uint16(SchemaFormatVersion) {
		t.Fatalf("current physical format %d is not independently frozen for publication; latest frozen is %d", SchemaFormatVersion, LatestFrozenPhysicalFormatVersion)
	}
}
