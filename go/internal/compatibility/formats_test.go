package compatibility

import "testing"

func TestPersistedFormatSemanticDiffRequiresHistoricalDecode(t *testing.T) {
	base := PersistedInventory{FormatVersion: PersistedInventoryFormatVersion, Formats: []PersistedFormat{
		{Name: "event-codec", Current: "golem.event.v1", Historical: []string{"golem.event.v1"}},
	}}
	retained := base
	retained.Formats = []PersistedFormat{{Name: "event-codec", Current: "golem.event.v2", Historical: []string{"golem.event.v1", "golem.event.v2"}}}
	dropped := base
	dropped.Formats = []PersistedFormat{{Name: "event-codec", Current: "golem.event.v2", Historical: []string{"golem.event.v2"}}}
	if got := ComparePersisted(base, retained); got != LayerAdditive {
		t.Fatalf("retained decoder transition = %q", got)
	}
	if got := ComparePersisted(base, dropped); got != LayerBreaking {
		t.Fatalf("dropped decoder transition = %q", got)
	}
	encoded, err := EncodePersistedInventory(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePersistedInventory(encoded); err != nil {
		t.Fatal(err)
	}
}
